package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/convoy"
	"github.com/steveyegge/gastown/internal/util"
)

const (
	defaultStrandedScanInterval = 30 * time.Second
	eventPollInterval           = 5 * time.Second
	eventPollMaxBackoff         = 60 * time.Second
	// Beads lifecycle events use CURRENT_TIMESTAMP in Dolt, which is second
	// precision. Poll with a 1s overlap so transitions that happen in the same
	// second as the previous high-water mark are still visible next cycle.
	eventPollLookback = 1 * time.Second

	// convoyGracePeriod is how long after creation a convoy is immune from
	// auto-close. This prevents a race where the daemon's stranded scan
	// fires before the sling's bd dep add is visible in Dolt. See GH#2303.
	convoyGracePeriod = 5 * time.Minute

	// feedDispatchCooldown is the minimum time between sling attempts for the
	// same ready issue from the stranded scan. Without it, a bead that is
	// in_progress to a dead polecat is re-slung every scan tick (~30s) — and
	// if the new polecat also dies quickly (or sling itself fails), the loop
	// repeats indefinitely, creating spawn storms and pummeling Dolt with
	// repeated assignment writes. See gu-iygf / hq/gt-sfo6q.
	feedDispatchCooldown = 5 * time.Minute

	// missingBeadStrikeThreshold is the number of consecutive "bead not found"
	// sling failures tolerated for a single (convoy, bead) pair before the
	// bead is auto-untracked from the convoy. N=3 absorbs transient Dolt
	// hiccups (replication lag, brief outages) while still terminating the
	// forever-loop produced by a tracked bead that has been deleted /
	// squashed / never created. See gu-f0gq (hq-cv-p6ht2 looped 347x on
	// non-existent ta-9emq for 19h45m).
	missingBeadStrikeThreshold = 3
)

// strandedConvoyInfo matches the JSON output of `gt convoy stranded --json`.
type strandedConvoyInfo struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	TrackedCount int       `json:"tracked_count"`
	ReadyCount   int       `json:"ready_count"`
	ReadyIssues  []string  `json:"ready_issues"`
	CreatedAt    time.Time `json:"created_at"`
	BaseBranch   string    `json:"base_branch,omitempty"`
}

// ConvoyManager monitors beads events for issue closes and periodically scans for stranded convoys.
// It handles both event-driven completion checks (via convoy.CheckConvoysForIssue) and periodic
// stranded convoy feeding/cleanup.
//
// Event polling watches ALL beads stores (town-level hq + per-rig) so that close events from
// any rig are detected. Convoys live in the hq store, so convoy lookups always use hqStore.
// Parked rigs are skipped during event polling.
type ConvoyManager struct {
	townRoot     string
	scanInterval time.Duration
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	logger       func(format string, args ...interface{})

	// stores maps store names to beads stores for event polling.
	// Key "hq" is the town-level store (used for convoy lookups).
	// Other keys are rig names (e.g., "gastown", "beads", "shippercrm").
	// Populated lazily via openStores if nil at startup (e.g., Dolt not ready).
	// Protected by storesMu.
	stores   map[string]beadsdk.Storage
	storesMu sync.Mutex

	// openStores is called lazily to open beads stores when stores is nil.
	// This handles the case where Dolt isn't ready at daemon startup.
	// Once stores are successfully opened, this is not called again.
	// May be nil to disable lazy opening (stores must be provided upfront).
	openStores func() map[string]beadsdk.Storage

	// isRigParked reports whether a rig is currently parked/docked.
	// Parked rigs are skipped during event polling. May be nil (never parked).
	isRigParked func(string) bool

	gtPath string

	// started guards against double-call of Start() which would spawn duplicate goroutines.
	started atomic.Bool

	// recoveryMode is set true when an event-poll failure is detected (indicating
	// Dolt is down). While set, runStrandedScan uses a shorter 5s interval so it
	// retries quickly once Dolt comes back. Cleared after the first successful scan.
	recoveryMode atomic.Bool

	// scanMu serializes calls to scan() from runStrandedScan, runStartupSweep,
	// and the Dolt recovery callback. Without this, concurrent scans can spawn
	// duplicate convoy checks for the same stranded convoy.
	scanMu sync.Mutex

	// lastEventIDs tracks per-store high-water marks for event polling.
	// Key matches stores map keys ("hq", "gastown", etc.).
	lastEventIDs sync.Map // map[string]time.Time

	// seeded is true once the first poll cycle has run (warm-up).
	// The first cycle advances high-water marks without processing events,
	// preventing a burst of historical event replay on daemon restart.
	seeded atomic.Bool

	// processedCloses tracks issue IDs whose current closed state has already
	// been processed. This prevents duplicate convoy checks when the same close
	// event is seen from multiple stores or across poll cycles where high-water
	// marks don't perfectly deduplicate (e.g., event replication). The entry is
	// cleared when the issue is reopened so a later close is processed again.
	// See GH #1798.
	processedCloses sync.Map // map[string]bool

	// processedLifecycleEvents tracks close/reopen event IDs that have already
	// been handled. This allows the 1s overlap window above without replaying
	// the same lifecycle events on every poll.
	processedLifecycleEvents sync.Map // map[string]bool

	// lastFeedAttempt tracks the most recent sling attempt time per ready
	// issue ID. Entries older than feedDispatchCooldown are ignored (and
	// overwritten on the next attempt). Used to suppress hot-loop re-slings
	// of the same in_progress bead with a dead assignee. See gu-iygf.
	lastFeedAttempt sync.Map // map[string]time.Time

	// missingBeadStrikes counts consecutive "bead not found" sling failures
	// per (convoyID, issueID) pair. When the count reaches
	// missingBeadStrikeThreshold the bead is auto-untracked from the convoy
	// so the convoy can make progress (and ultimately auto-close if it
	// becomes empty). Cleared on successful sling. See gu-f0gq.
	missingBeadStrikes sync.Map // map[missingBeadKey]int

	// untrackMissingBeadFn untracks issueID from convoyID. Overridable for
	// tests; defaults to a `bd dep remove` subprocess call. Returning a
	// non-nil error leaves the strike counter in place so the next scan
	// retries the untrack rather than re-attempting the doomed sling.
	untrackMissingBeadFn func(convoyID, issueID string) error

	// now returns the current time. Overridable for tests; defaults to
	// time.Now. Only used by the feed-cooldown logic.
	now func() time.Time
}

// missingBeadKey identifies a (convoy, bead) pair for the strike counter.
type missingBeadKey struct {
	convoyID string
	issueID  string
}

// NewConvoyManager creates a new convoy manager.
// scanInterval controls the periodic stranded scan; 0 uses default (30s).
// stores maps store names ("hq", rig names) to beads stores for event polling.
// nil stores disables event-driven convoy checks (stranded scan still runs),
// unless openStores is provided for lazy initialization.
// openStores is called lazily if stores is nil (e.g., Dolt not ready at startup).
// isRigParked reports whether a rig should be skipped during polling (nil = never parked).
// gtPath is the resolved path to the gt binary for subprocess calls.
func NewConvoyManager(townRoot string, logger func(format string, args ...interface{}), gtPath string, scanInterval time.Duration, stores map[string]beadsdk.Storage, openStores func() map[string]beadsdk.Storage, isRigParked func(string) bool) *ConvoyManager {
	if scanInterval <= 0 {
		scanInterval = defaultStrandedScanInterval
	}
	if isRigParked == nil {
		isRigParked = func(string) bool { return false }
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &ConvoyManager{
		townRoot:     townRoot,
		scanInterval: scanInterval,
		ctx:          ctx,
		cancel:       cancel,
		logger:       logger,
		stores:       stores,
		openStores:   openStores,
		isRigParked:  isRigParked,
		gtPath:       gtPath,
		now:          time.Now,
	}
	m.untrackMissingBeadFn = m.untrackMissingBeadViaBd
	return m
}

// Start begins the convoy manager goroutines (event poll + stranded scan).
// It is safe to call multiple times; subsequent calls are no-ops.
func (m *ConvoyManager) Start() error {
	if !m.started.CompareAndSwap(false, true) {
		m.logger("Convoy: Start() already called, ignoring duplicate")
		return nil
	}
	m.wg.Add(2)
	go m.runEventPoll()
	go m.runStrandedScan()
	// Run a one-shot sweep to catch convoys that completed during any previous
	// outage or while the daemon was stopped.
	go m.runStartupSweep()
	return nil
}

// Stop gracefully stops the convoy manager and closes any beads stores it owns.
func (m *ConvoyManager) Stop() {
	m.cancel()
	m.wg.Wait()

	// Close stores (whether eagerly passed or lazily opened)
	m.storesMu.Lock()
	stores := m.stores
	m.stores = nil
	m.storesMu.Unlock()
	for name, store := range stores {
		if store != nil {
			if err := store.Close(); err != nil {
				m.logger("Convoy: error closing beads store (%s): %v", name, err)
			} else {
				m.logger("Convoy: closed beads store (%s)", name)
			}
		}
	}
}

// runEventPoll polls GetAllEventsSince every 5s and processes close events.
// If stores aren't available at startup (e.g., Dolt not ready), retries
// lazily via the openStores callback until stores become available.
func (m *ConvoyManager) runEventPoll() {
	defer m.wg.Done()

	m.storesMu.Lock()
	hasStores := len(m.stores) > 0
	hasOpener := m.openStores != nil
	m.storesMu.Unlock()

	if !hasStores && !hasOpener {
		m.logger("Convoy: no beads stores and no opener, event polling disabled")
		return
	}

	currentInterval := eventPollInterval
	ticker := time.NewTicker(currentInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.storesMu.Lock()
			// Lazy store initialization: retry if stores not yet available
			if len(m.stores) == 0 {
				if m.openStores != nil {
					m.stores = m.openStores()
				}
				if len(m.stores) == 0 {
					m.storesMu.Unlock()
					continue // still not ready, try next tick
				}
			}
			// Take a snapshot of stores for this tick to avoid holding the
			// lock across potentially slow network/Dolt calls.
			snapshot := make(map[string]beadsdk.Storage, len(m.stores))
			for k, v := range m.stores {
				snapshot[k] = v
			}
			m.storesMu.Unlock()

			hadError := m.pollStoresSnapshot(snapshot)
			// Exponential backoff on consecutive errors to avoid hammering
			// a recovering Dolt server. Reset on success. (GH#2686)
			if hadError {
				newInterval := currentInterval * 2
				if newInterval > eventPollMaxBackoff {
					newInterval = eventPollMaxBackoff
				}
				if newInterval != currentInterval {
					currentInterval = newInterval
					ticker.Reset(currentInterval)
					m.logger("Convoy: poll backoff → %s", currentInterval)
				}
			} else if currentInterval != eventPollInterval {
				currentInterval = eventPollInterval
				ticker.Reset(currentInterval)
				m.logger("Convoy: poll recovered, interval reset to %s", currentInterval)
			}
		}
	}
}

// pollStoresSnapshot polls events from all non-parked stores in the snapshot.
// The first call is a warm-up: it advances high-water marks without
// processing events, preventing a burst of historical replay on restart.
// A per-cycle seen set deduplicates close events across stores so each
// issueID is processed at most once per poll cycle.
// Returns true if any store poll encountered an error.
func (m *ConvoyManager) pollStoresSnapshot(stores map[string]beadsdk.Storage) bool {
	seen := make(map[string]bool)
	hadError := false
	for name, store := range stores {
		if name != "hq" && m.isRigParked(name) {
			continue
		}
		if err := m.pollStore(name, store, stores, seen); err != nil {
			hadError = true
		}
	}
	m.seeded.CompareAndSwap(false, true)
	return hadError
}

// pollStore fetches new events from a single store and processes close events.
// Convoy lookups always use the hq store since convoys are hq-* prefixed.
// The stores snapshot is passed to avoid accessing m.stores without the lock.
// The seen set deduplicates issueIDs across stores within a poll cycle.
// Returns an error if the poll failed (used by caller for backoff decisions).
func (m *ConvoyManager) pollStore(name string, store beadsdk.Storage, stores map[string]beadsdk.Storage, seen map[string]bool) error {
	// Load per-store high-water mark.
	// Default to Unix epoch (not zero time) because Go's zero time.Time
	// (0001-01-01) causes Dolt's SQL driver to produce +Inf when converting
	// to a float parameter, triggering "Error 1366: +Inf is not a valid
	// value for double". Unix epoch is safe for all SQL backends.
	highWater := time.Unix(0, 0).UTC()
	if v, ok := m.lastEventIDs.Load(name); ok {
		highWater = v.(time.Time)
	}
	querySince := highWater
	if !highWater.Equal(time.Unix(0, 0).UTC()) {
		querySince = highWater.Add(-eventPollLookback)
		if querySince.Before(time.Unix(0, 0).UTC()) {
			querySince = time.Unix(0, 0).UTC()
		}
	}

	events, err := store.GetAllEventsSince(m.ctx, querySince)
	if err != nil {
		if isInfNaNError(err) {
			// A corrupted row in the events table has +Inf/-Inf/NaN stored in a
			// double column (e.g. created_at serialized from Go's zero time.Time).
			// Advance the high-water mark to now so future polls skip past the
			// bad row entirely. Events before now are missed, but the stranded
			// convoy scanner will catch any completions that were lost.
			now := time.Now().UTC()
			m.lastEventIDs.Store(name, now)
			m.logger("Convoy: event poll (%s): +Inf/NaN row detected, advancing HWM to %s to skip corrupt data", name, now.Format(time.RFC3339))
			return nil
		}
		m.logger("Convoy: event poll error (%s): %v", name, err)
		// Signal recovery mode so the stranded scan shortens its interval and
		// retries quickly once Dolt comes back.
		m.recoveryMode.Store(true)
		return err
	}

	// Advance high-water mark from all events
	for _, e := range events {
		if e.CreatedAt.After(highWater) {
			highWater = e.CreatedAt
		}
	}
	m.lastEventIDs.Store(name, highWater)

	// First poll cycle is warm-up only: advance marks, skip processing.
	// This prevents replaying the entire event history on daemon restart.
	if !m.seeded.Load() {
		for _, e := range events {
			if e.ID == "" {
				continue
			}
			if isCloseEvent(e) || isReopenEvent(e) {
				m.processedLifecycleEvents.Store(e.ID, true)
			}
		}
		return nil
	}

	// Use hq store for convoy lookups (convoys are hq-* prefixed)
	hqStore := stores["hq"]
	if hqStore == nil {
		m.logger("Convoy: hq store unavailable, skipping convoy lookups for %s events", name)
		return nil
	}

	for _, e := range events {
		issueID := e.IssueID
		if issueID == "" {
			continue
		}

		if isCloseEvent(e) || isReopenEvent(e) {
			if _, alreadyHandled := m.processedLifecycleEvents.LoadOrStore(e.ID, true); alreadyHandled {
				continue
			}
		}

		if isReopenEvent(e) {
			// Reopening starts a new close epoch for this issue. Clear both the
			// per-cycle and cross-cycle dedup so a later close is processed again.
			delete(seen, issueID)
			m.processedCloses.Delete(issueID)

			// gu-kawd: if any convoy auto-closed on this issue's previous close,
			// reopen it so the convoy's lifecycle stays in sync with the tracked
			// bead. Without this, the convoy stays closed with completion_notified_at
			// set; the next genuine close suppresses the notification (idempotent
			// stamp), and operators see "Convoy complete" mails that look premature
			// because the bead they reference is currently in_progress.
			convoy.ReopenConvoysForIssue(m.ctx, hqStore, m.townRoot, issueID, "Convoy", m.logger, m.gtPath)
			continue
		}

		if !isCloseEvent(e) {
			continue
		}

		// Deduplicate: skip if already processed this issueID in this poll cycle
		// (same close may appear in multiple stores or as multiple event types).
		// Reopen events clear this marker so close→reopen→close can be processed
		// twice even when all three events land in the same poll cycle.
		if seen[issueID] {
			continue
		}
		seen[issueID] = true

		// Cross-cycle dedup: skip if this issue's close was already processed
		// in a previous poll cycle. The same close event can appear from
		// multiple stores (replication) or across poll cycles when high-water
		// marks don't perfectly filter. See GH #1798.
		if _, alreadyProcessed := m.processedCloses.LoadOrStore(issueID, true); alreadyProcessed {
			continue
		}

		m.logger("Convoy: close detected: %s (from %s)", issueID, name)
		resolver := convoy.NewStoreResolver(m.townRoot, stores)
		convoy.CheckConvoysForIssue(m.ctx, hqStore, m.townRoot, issueID, "Convoy", m.logger, m.gtPath, m.isRigParked, resolver)
		convoy.FireCrossRigDepNotifications(m.ctx, issueID, m.townRoot, stores, m.logger)
	}
	return nil
}

// isInfNaNError reports whether err is a Dolt/SQL error about an invalid float
// value (+Inf, -Inf, NaN) in a double column. These errors arise when a
// corrupted row (e.g. created_at written from Go's zero time.Time via an old
// driver path) is encountered during a query. The caller should advance the
// high-water mark to skip past the offending row rather than entering
// permanent backoff.
func isInfNaNError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Dolt wraps values in single quotes: "'+Inf' is not a valid value for 'double'"
	// Match both quoted and unquoted forms.
	return strings.Contains(msg, "+Inf is not a valid value") ||
		strings.Contains(msg, "'+Inf' is not a valid value") ||
		strings.Contains(msg, "-Inf is not a valid value") ||
		strings.Contains(msg, "'-Inf' is not a valid value") ||
		strings.Contains(msg, "NaN is not a valid value") ||
		strings.Contains(msg, "'NaN' is not a valid value")
}

func isCloseEvent(e *beadsdk.Event) bool {
	if e == nil {
		return false
	}
	if e.EventType == beadsdk.EventClosed {
		return true
	}
	return e.EventType == beadsdk.EventStatusChanged &&
		e.NewValue != nil &&
		*e.NewValue == "closed"
}

func isReopenEvent(e *beadsdk.Event) bool {
	if e == nil {
		return false
	}
	if e.EventType == beadsdk.EventReopened {
		return true
	}
	return e.EventType == beadsdk.EventStatusChanged &&
		e.OldValue != nil &&
		*e.OldValue == "closed" &&
		(e.NewValue == nil || *e.NewValue != "closed")
}

// runStrandedScan is the periodic stranded convoy scan loop.
// During recovery mode (after Dolt poll errors) the interval shrinks to 5s
// so a successful scan fires promptly once Dolt comes back. Recovery mode is
// cleared after the first successful scan.
func (m *ConvoyManager) runStrandedScan() {
	defer m.wg.Done()

	ticker := time.NewTicker(m.scanInterval)
	defer ticker.Stop()

	// Run once immediately, then on interval
	m.scan()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			// While in recovery mode, shorten the next tick so we retry quickly
			// after a Dolt outage without waiting the full scan interval.
			if m.recoveryMode.Load() {
				ticker.Reset(5 * time.Second)
			} else {
				ticker.Reset(m.scanInterval)
			}
			m.scan()
		}
	}
}

// scan runs one stranded scan cycle: find stranded convoys, feed or close each.
// Serialized by scanMu to prevent concurrent scans from spawning duplicate checks.
func (m *ConvoyManager) scan() {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()

	stranded, err := m.findStranded()
	if err != nil {
		m.logger("Convoy: stranded scan failed: %s", util.FirstLine(err.Error()))
		return
	}
	// Successful scan: clear recovery mode so the ticker returns to normal interval.
	m.recoveryMode.Store(false)

	for _, c := range stranded {
		select {
		case <-m.ctx.Done():
			return
		default:
		}

		if c.ReadyCount > 0 {
			m.feedFirstReady(c)
		} else if c.TrackedCount == 0 {
			// Empty convoy — but skip if it was just created (GH#2303).
			// The sling's bd dep add may not be visible in Dolt yet.
			if !c.CreatedAt.IsZero() && time.Since(c.CreatedAt) < convoyGracePeriod {
				m.logger("Convoy %s: empty but within grace period (created %s ago) — skipping", c.ID, time.Since(c.CreatedAt).Round(time.Second))
				continue
			}
			m.closeEmptyConvoy(c.ID)
		} else {
			// Tracked issues exist but none are ready. This could mean:
			// (a) all tracked issues are closed → convoy should auto-close
			// (b) issues are blocked/in-progress → needs agent review
			// Run convoy check to handle case (a); it's a no-op for (b).
			m.logger("Convoy %s: %d tracked issues, 0 ready — checking completion", c.ID, c.TrackedCount)
			m.checkConvoyCompletion(c.ID)
		}
	}
}

// findStranded runs `gt convoy stranded --json` and parses the output.
func (m *ConvoyManager) findStranded() ([]strandedConvoyInfo, error) {
	cmd := exec.CommandContext(m.ctx, m.gtPath, "convoy", "stranded", "--json")
	cmd.Dir = m.townRoot
	cmd.Env = bdReadOnlyRoutingEnv(m.townRoot)
	util.SetProcessGroup(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s", util.FirstLine(stderr.String()))
	}

	var stranded []strandedConvoyInfo
	if err := json.Unmarshal(stdout.Bytes(), &stranded); err != nil {
		// Include first line of raw output for debugging (e.g., non-JSON warnings on stdout)
		raw := util.FirstLine(stdout.String())
		return nil, fmt.Errorf("parsing stranded JSON: %w (raw: %q)", err, raw)
	}

	return stranded, nil
}

// feedFirstReady iterates through all ready issues in a stranded convoy and
// dispatches the first one that can be successfully slung. Issues are skipped
// (with logging) when the prefix is unresolvable, the rig has no route, the
// rig is parked, or the sling command fails. This ensures convoys progress
// even when some issues target unavailable rigs.
func (m *ConvoyManager) feedFirstReady(c strandedConvoyInfo) {
	if len(c.ReadyIssues) == 0 {
		return
	}

	for _, issueID := range c.ReadyIssues {
		prefix := beads.ExtractPrefix(issueID)
		if prefix == "" {
			m.logger("Convoy %s: no prefix for %s, skipping", c.ID, issueID)
			continue
		}

		rig := beads.GetRigNameForPrefix(m.townRoot, prefix)
		if rig == "" {
			m.logger("Convoy %s: no rig for %s (prefix %s), skipping", c.ID, issueID, prefix)
			continue
		}

		if m.isRigParked(rig) {
			m.logger("Convoy %s: rig %s is parked, skipping %s", c.ID, rig, issueID)
			continue
		}

		// Per-issue dispatch cooldown (gu-iygf). A bead that's in_progress to
		// a dead polecat reappears in `gt convoy stranded` every scan tick.
		// Re-slinging on every tick produces a spawn storm when sling itself
		// fails or the new polecat dies fast. Skip this issue (try the next
		// ready one) if we slung it within feedDispatchCooldown.
		if m.inFeedCooldown(issueID) {
			m.logger("Convoy %s: %s in feed cooldown, skipping", c.ID, issueID)
			continue
		}

		m.logger("Convoy %s: feeding %s to %s", c.ID, issueID, rig)

		// Record the attempt before invoking sling so a hung/slow sling still
		// counts toward cooldown. Failures intentionally also occupy the
		// cooldown window — repeating an immediately-failing sling every tick
		// is exactly the loop we're suppressing.
		m.recordFeedAttempt(issueID)

		slingArgs := []string{"sling", issueID, rig, "--no-boot"}
		if c.BaseBranch != "" {
			slingArgs = append(slingArgs, "--base-branch="+c.BaseBranch)
		}
		cmd := exec.CommandContext(m.ctx, m.gtPath, slingArgs...)
		cmd.Dir = m.townRoot
		cmd.Env = bdMutationRoutingEnv(m.townRoot)
		util.SetProcessGroup(cmd)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			stderrLine := util.FirstLine(stderr.String())
			m.logger("Convoy %s: sling %s failed: %s", c.ID, issueID, stderrLine)
			if isBeadNotFoundError(stderrLine) {
				m.handleMissingBeadStrike(c.ID, issueID, stderrLine)
			}
			continue
		}
		// Successful dispatch — clear any accumulated missing-bead strikes
		// for this (convoy, bead) pair. A bead that was transiently invisible
		// (Dolt restart, replication lag) shouldn't carry strikes forward.
		m.missingBeadStrikes.Delete(missingBeadKey{c.ID, issueID})
		return // Successfully dispatched one issue
	}

	m.logger("Convoy %s: no dispatchable issues (all %d skipped)", c.ID, len(c.ReadyIssues))
}

// isBeadNotFoundError reports whether a sling stderr line indicates the target
// bead does not exist. Matches the error shapes produced by verifyBeadExists
// and bd show ("bead 'xxx' not found", "bead xxx not found", and the bd-direct
// "no issue found matching 'xxx'" form). Match is case-insensitive and tolerant
// of quoting variants. (gu-f0gq)
func isBeadNotFoundError(stderrLine string) bool {
	if stderrLine == "" {
		return false
	}
	s := strings.ToLower(stderrLine)
	// gt sling: "bead 'xxx' not found" / "bead xxx not found"
	if strings.Contains(s, "not found") &&
		(strings.Contains(s, "bead ") || strings.Contains(s, "issue ")) {
		return true
	}
	// bd: "no issue found matching"
	if strings.Contains(s, "no issue found matching") {
		return true
	}
	return false
}

// handleMissingBeadStrike increments the strike counter for (convoyID, issueID)
// and, when the threshold is reached, untracks the bead from the convoy so the
// stranded scan stops re-attempting an impossible sling. The convoy itself is
// not closed here — the next scan will see the convoy with one fewer (or zero)
// tracked beads and the existing checkConvoyCompletion / closeEmptyConvoy paths
// take over. (gu-f0gq)
func (m *ConvoyManager) handleMissingBeadStrike(convoyID, issueID, stderrLine string) {
	key := missingBeadKey{convoyID, issueID}
	var count int
	if v, ok := m.missingBeadStrikes.Load(key); ok {
		if c, ok := v.(int); ok {
			count = c
		}
	}
	count++
	m.missingBeadStrikes.Store(key, count)

	if count < missingBeadStrikeThreshold {
		m.logger("Convoy %s: %s missing-bead strike %d/%d (last error: %s)",
			convoyID, issueID, count, missingBeadStrikeThreshold, stderrLine)
		return
	}

	m.logger("Convoy %s: %s missing for %d consecutive scans — auto-untracking",
		convoyID, issueID, count)
	if m.untrackMissingBeadFn == nil {
		return
	}
	if err := m.untrackMissingBeadFn(convoyID, issueID); err != nil {
		// Leave the strike counter in place so the next scan retries the
		// untrack instead of re-running the doomed sling.
		m.logger("Convoy %s: untrack of missing %s failed: %s",
			convoyID, issueID, util.FirstLine(err.Error()))
		return
	}
	// Untrack succeeded — drop the strike entry; the bead is gone from the
	// convoy and will not reappear in subsequent stranded scans.
	m.missingBeadStrikes.Delete(key)
	m.logger("Convoy %s: untracked missing bead %s — next scan will reassess convoy state",
		convoyID, issueID)
}

// untrackMissingBeadViaBd runs `bd dep remove <convoyID> <issueID> --type=tracks`
// to evict a tracked bead that no longer resolves. Uses the same mutation-routing
// env as the feeder's sling subprocess so cross-rig convoy lookups land in the
// hq store. Returns any error verbatim (caller logs the first line).
func (m *ConvoyManager) untrackMissingBeadViaBd(convoyID, issueID string) error {
	cmd := exec.CommandContext(m.ctx, "bd", "dep", "remove", convoyID, issueID, "--type=tracks")
	cmd.Dir = m.townRoot
	cmd.Env = bdMutationRoutingEnv(m.townRoot)
	util.SetProcessGroup(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return err
	}
	return nil
}

// checkConvoyCompletion runs gt convoy check to auto-close a convoy whose
// tracked issues may all be closed. This handles the case where the event poll
// missed the close events (e.g., daemon restart, Dolt latency).
func (m *ConvoyManager) checkConvoyCompletion(convoyID string) {
	cmd := exec.CommandContext(m.ctx, m.gtPath, "convoy", "check", convoyID)
	cmd.Dir = m.townRoot
	cmd.Env = bdMutationRoutingEnv(m.townRoot)
	util.SetProcessGroup(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		m.logger("Convoy %s: completion check failed: %s", convoyID, util.FirstLine(stderr.String()))
	}
}

// closeEmptyConvoy runs gt convoy check to auto-close an empty convoy.
func (m *ConvoyManager) closeEmptyConvoy(convoyID string) {
	m.logger("Convoy %s: auto-closing (empty)", convoyID)

	cmd := exec.CommandContext(m.ctx, m.gtPath, "convoy", "check", convoyID)
	cmd.Dir = m.townRoot
	cmd.Env = bdMutationRoutingEnv(m.townRoot)
	util.SetProcessGroup(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		m.logger("Convoy %s: check failed: %s", convoyID, util.FirstLine(stderr.String()))
	}
}

// inFeedCooldown reports whether issueID was slung within feedDispatchCooldown.
// Stale entries (older than the cooldown) are pruned in place to bound the
// lastFeedAttempt map. See gu-iygf.
func (m *ConvoyManager) inFeedCooldown(issueID string) bool {
	v, ok := m.lastFeedAttempt.Load(issueID)
	if !ok {
		return false
	}
	last, ok := v.(time.Time)
	if !ok {
		m.lastFeedAttempt.Delete(issueID)
		return false
	}
	if m.now().Sub(last) < feedDispatchCooldown {
		return true
	}
	// Cooldown expired — drop the entry so the map doesn't grow unbounded
	// across an infinite stream of one-shot issue IDs. The entry will be
	// rewritten by recordFeedAttempt on the next dispatch.
	m.lastFeedAttempt.Delete(issueID)
	return false
}

// recordFeedAttempt stamps issueID with the current time so subsequent scans
// within feedDispatchCooldown skip it. See gu-iygf.
func (m *ConvoyManager) recordFeedAttempt(issueID string) {
	m.lastFeedAttempt.Store(issueID, m.now())
}

// runStartupSweep runs one convoy check pass after a brief delay to catch
// convoys that completed while the daemon was stopped or Dolt was unavailable.
// It waits 10 seconds so Dolt has time to stabilize before the first query.
// This goroutine is not tracked in wg because it is short-lived (exits after
// a single scan) and does not need to participate in the Stop() shutdown.
func (m *ConvoyManager) runStartupSweep() {
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-m.ctx.Done():
		return
	case <-timer.C:
	}
	m.logger("Convoy: running startup sweep for stranded convoys")
	m.scan()
}

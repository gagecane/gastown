package daemon

import (
	"bytes"
	"context"
	"database/sql"
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
	"github.com/steveyegge/gastown/internal/sling"
	"github.com/steveyegge/gastown/internal/util"
)

const (
	defaultStrandedScanInterval = 60 * time.Second
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
	// in_progress to a dead polecat is re-slung every scan tick (~60s) — and
	// if the new polecat also dies quickly (or sling itself fails), the loop
	// repeats indefinitely, creating spawn storms and pummeling Dolt with
	// repeated assignment writes. See gu-iygf / hq/gt-sfo6q.
	feedDispatchCooldown = 5 * time.Minute

	// feedCooldownCap bounds the escalate-churn backoff (gs-skv). An unworkable
	// bead — one a polecat keeps refusing/escalating without ever closing or
	// making progress — reappears in the stranded scan every cycle. The flat
	// 5-minute cooldown alone re-fed it ~12×/hour for hours (dementus burned
	// dispatch cycles for 1hr+). The effective cooldown now grows with the
	// per-bead churn count and is capped here, so a churning bead is re-fed at
	// most hourly instead of every 5 minutes. The backoff self-heals: once the
	// bead stops reappearing for feedChurnWindow, its churn count ages out and
	// resets to the base cooldown.
	feedCooldownCap = 1 * time.Hour

	// feedChurnWindow is how long a prior feed keeps counting toward a bead's
	// churn streak. A re-feed within this window increments the streak (and the
	// backoff); a gap longer than this resets it — so a bead that genuinely
	// progressed and only much later reappears starts fresh, not pre-penalized.
	feedChurnWindow = 1 * time.Hour

	// missingBeadStrikeThreshold is the number of consecutive "bead not found"
	// sling failures before a confirmation re-check is triggered. Set to 1
	// so a single failure immediately confirms via `bd show` — this is
	// restart-proof (no state to persist across daemon lifetimes) while the
	// confirmation check absorbs the transient-hiccup tolerance that N=3
	// previously provided. See gu-f0gq, gu-dvcs4.
	missingBeadStrikeThreshold = 1

	// stableSkipThreshold is the number of consecutive scans a completion
	// candidate (tracked>0, ready=0) must remain unchanged before it is
	// skipped. A convoy whose tracked set hasn't changed in 3 scans is
	// unlikely to have completed between scans — the event-poll path handles
	// close-driven completion, so the batched check is wasted work. Skipping
	// saves one subprocess spawn per stable convoy per scan. See gu-0vuw1.
	stableSkipThreshold = 3

	// stableBackstopScans forces a re-check of stable completion candidates
	// after this many consecutive skipped scans (~100min at 60s interval).
	// Guards against missed close events (e.g., Dolt replication lag that
	// outlasts the event-poll lookback).
	stableBackstopScans = 100

	// completionBackstopInterval is the number of scan ticks between
	// unconditional completion checks. Completed convoys (all tracked closed)
	// are excluded from findStrandedConvoys (gu-urwg6), so they no longer
	// appear as completion candidates. This periodic backstop ensures they
	// still get auto-closed even when the event-poll misses the close events.
	// At 60s scan interval, 10 scans = ~10 minutes.
	completionBackstopInterval = 10

	// feedLogThrottleInterval throttles the repeating per-scan feeder "skip"
	// log lines (in-feed-cooldown and no-dispatchable-issues). The line is
	// emitted on the first occurrence and then every Nth repeat, so a
	// single-child convoy whose child is being worked by a live agent no
	// longer logs every ~60s scan indefinitely. At a 60s scan interval,
	// 30 = roughly one line every 30 minutes per quiescent convoy. The
	// dispatch behavior is unchanged; only log volume is reduced (gu-5d3a3).
	feedLogThrottleInterval = 30
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
	// Merge is the convoy's merge strategy ("direct"/"mr"/"local"). Threaded
	// into the re-dispatch sling so a merge=local relay leg is fed back as
	// merge=local instead of silently reverting to the merge-queue default
	// (gs-9ct #3) — otherwise a stranded do-not-merge prototype leg would
	// auto-MR to main on its next feed.
	Merge string `json:"merge,omitempty"`
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

	// seeded, when true, forces every store straight into the processing phase,
	// bypassing the per-store warm-up entirely. Production never sets it — there
	// the per-store seededStores map governs warm-up so each store is seeded only
	// after its OWN first successful poll (gs-rx1). It remains as an all-stores
	// fast path used by tests that assert close-event processing directly.
	seeded atomic.Bool

	// seededStores records which stores have completed their one-time warm-up
	// poll (advanced the high-water mark and marked pre-existing lifecycle events
	// processed). A store is recorded only after a SUCCESSFUL poll, so a store
	// that errored on the initial cycle — e.g. Dolt still warming up right after
	// a daemon restart — re-runs warm-up on its next successful poll instead of
	// being force-promoted to processing. Force-promotion was the gs-rx1 bug: the
	// errored store's HWM stayed at epoch, so the next successful poll replayed
	// its entire close backlog (~18k convoy-wisps) as fresh closes, firing a
	// CheckConvoysForIssue per historical close and starving dispatch for minutes.
	// Key matches stores map keys ("hq", rig names).
	seededStores sync.Map // map[string]bool

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
	// issue ID. Entries older than the (escalating) cooldown are ignored (and
	// overwritten on the next attempt). Used to suppress hot-loop re-slings
	// of the same in_progress bead with a dead assignee. See gu-iygf.
	lastFeedAttempt sync.Map // map[string]time.Time

	// feedChurn tracks the escalate-churn streak per issue ID (gs-skv): how
	// many times in a row a bead has been re-fed within feedChurnWindow without
	// ever leaving the ready set (i.e. without closing or sticking to a live
	// worker). The streak scales the effective cooldown (effectiveFeedCooldown)
	// so an unworkable, perpetually-refused bead backs off toward feedCooldownCap
	// instead of being re-fed every 5 minutes forever.
	feedChurn sync.Map // map[string]feedChurnEntry

	// missingBeadStrikes counts consecutive "bead not found" sling failures
	// per (convoyID, issueID) pair. When the count reaches
	// missingBeadStrikeThreshold the bead is auto-untracked from the convoy
	// so the convoy can make progress (and ultimately auto-close if it
	// becomes empty). Cleared on successful sling. See gu-f0gq.
	missingBeadStrikes sync.Map // map[missingBeadKey]int

	// checkBeadExistenceFn performs a definitive existence check (e.g.,
	// `bd show <issueID>`) before untracking. It returns a TRI-STATE
	// (beadExists / beadMissing / beadCheckAmbiguous) so an "I could not
	// determine state" infra error (Dolt circuit-breaker open, connection
	// refused, timeout) is never collapsed into a state verdict. Overridable
	// for tests; defaults to a `bd show` subprocess call. See gu-dvcs4
	// (existence check) and gu-3hi1f (ambiguous-vs-state separation).
	checkBeadExistenceFn func(issueID string) beadExistence

	// untrackMissingBeadFn untracks issueID from convoyID. Overridable for
	// tests; defaults to a `bd dep remove` subprocess call. Returning a
	// non-nil error leaves the strike counter in place so the next scan
	// retries the untrack rather than re-attempting the doomed sling.
	untrackMissingBeadFn func(convoyID, issueID string) error

	// now returns the current time. Overridable for tests; defaults to
	// time.Now. Only used by the feed-cooldown logic.
	now func() time.Time

	// seenSlingErrors deduplicates persistent sling failures so the daemon
	// escalates once per stranded issue instead of spamming an escalation
	// every scan cycle. Cleared on successful dispatch so a future failure
	// (if the issue is re-queued) is escalated again. Key: issueID. (gt-3798)
	seenSlingErrors sync.Map // map[string]bool

	// feedSkipLog throttles the per-scan "skip" log lines emitted when a
	// convoy's only ready child is non-dispatchable (in feed cooldown) and
	// thus no issue can be fed. The dispatch logic itself is already correctly
	// throttled (the gs-skv churn backoff), but these informational skip lines
	// re-fired every ~60s scan for every single-child convoy whose child was
	// hooked/in_progress to a live agent — dominating daemon.log (gu-5d3a3:
	// ~44K "no dispatchable issues" + ~32K "in feed cooldown" lines). The
	// counter logs the first occurrence then every feedLogThrottleInterval-th,
	// mirroring the zombie re-detection dedup (gu-50qv). Keys are prefixed
	// ("cd:"+issueID for cooldown skips, "nd:"+convoyID for the per-convoy
	// no-dispatchable line) and reset when the convoy makes progress.
	feedSkipLog sync.Map // map[string]int

	// stableCandidates tracks per-convoy stability for completion candidates
	// (tracked>0, ready=0). Candidates that remain unchanged for
	// stableSkipThreshold consecutive scans are skipped from the batched
	// completion check. Reset on close events or recovery mode. See gu-0vuw1.
	stableCandidates sync.Map // map[string]stableCandidate

	// strandedCache holds the last findStranded() result and its sentinel
	// (open convoy count + max updated_at). When the sentinel is unchanged
	// between scans, the cached result is reused without forking a subprocess.
	// Protected by scanMu (findStranded is only called from scan which holds it).
	// See gu-rd9ph.
	strandedCache *strandedCacheEntry

	// scanCount tracks the number of scan() invocations since start. Used to
	// fire the periodic completion backstop (completionBackstopInterval) that
	// catches completed convoys no longer reported by findStrandedConvoys.
	// Protected by scanMu.
	scanCount int
}

// missingBeadKey identifies a (convoy, bead) pair for the strike counter.
type missingBeadKey struct {
	convoyID string
	issueID  string
}

// stableCandidate tracks how long a completion candidate (tracked>0, ready=0)
// has been unchanged across consecutive scans. When stableCount reaches
// stableSkipThreshold the candidate is skipped (the batched completion check
// omits it); the backstop forces a re-check every stableBackstopScans. See
// gu-0vuw1.
type stableCandidate struct {
	trackedCount int // TrackedCount from previous scan
	stableCount  int // consecutive unchanged scans
}

// strandedCacheEntry holds a cached findStranded() result alongside the
// sentinel values that were current when the result was computed. If the
// sentinel is unchanged on the next scan, the cached result is reused without
// forking the expensive `gt convoy stranded --json` subprocess. See gu-rd9ph.
type strandedCacheEntry struct {
	openCount int       // COUNT(*) of open convoy-type issues
	maxUpdate time.Time // MAX(updated_at) of open convoy-type issues
	result    []strandedConvoyInfo
}

// NewConvoyManager creates a new convoy manager.
// scanInterval controls the periodic stranded scan; 0 uses default (60s).
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
	m.checkBeadExistenceFn = m.checkBeadExistenceViaBd
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
// Each store's first SUCCESSFUL poll is a warm-up that advances its high-water
// mark without processing events, preventing a burst of historical replay on
// restart (see pollStore / seededStores). A per-cycle seen set deduplicates
// close events across stores so each issueID is processed at most once per poll
// cycle. Returns true if any store poll encountered an error.
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
		// retries quickly once Dolt comes back. Also reset stable candidates
		// so the first successful scan after recovery re-checks everything
		// (close events may have been missed during the outage). See gu-0vuw1.
		m.recoveryMode.Store(true)
		m.resetStableCandidates()
		return err
	}

	// Advance high-water mark from all events
	for _, e := range events {
		if e.CreatedAt.After(highWater) {
			highWater = e.CreatedAt
		}
	}
	m.lastEventIDs.Store(name, highWater)

	// First poll cycle for THIS store is warm-up only: advance marks, skip
	// processing. This prevents replaying the entire event history on daemon
	// restart. Seeding is recorded per-store (seededStores) and only HERE, after
	// a successful fetch + HWM advance — so a store that errored on an earlier
	// cycle (Dolt still warming up post-restart) is not yet seeded and runs its
	// warm-up on its first SUCCESSFUL poll, rather than being promoted to
	// processing and replaying its whole close backlog (gs-rx1). The global
	// m.seeded flag is an all-stores fast path used by tests.
	if _, warmed := m.seededStores.Load(name); !warmed && !m.seeded.Load() {
		for _, e := range events {
			if e.ID == "" {
				continue
			}
			if isCloseEvent(e) || isReopenEvent(e) {
				m.processedLifecycleEvents.Store(e.ID, true)
			}
		}
		m.seededStores.Store(name, true)
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
		// A close event means a tracked issue may have completed — reset
		// stability tracking so the next scan includes all candidates in
		// the batched completion check. See gu-0vuw1.
		m.resetStableCandidates()
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

	// Count "tracked but 0 ready" convoys — the completion-check candidates.
	// Previously each was handled by a separate `gt convoy check <id>`
	// subprocess inside this loop; with N convoys that serial fan-out (full gt
	// cold-start + per-call bd queries each) blew the 5m dispatch budget
	// (gu-jqb47). We now collect them and run ONE batched `gt convoy check`
	// (no id → checkAndCloseCompletedConvoys, already IN(...)-batched per
	// gc-pai9b) after the loop, collapsing N subprocess spawns into one.
	completionCandidates := 0
	slingFailures := 0
	for _, c := range stranded {
		select {
		case <-m.ctx.Done():
			return
		default:
		}

		if c.ReadyCount > 0 {
			// Freshness guard (gs-cxex): the stranded cache (gu-rd9ph) can serve
			// a result captured while this convoy was open with a ready bead. If
			// the convoy has since closed without shifting the cache sentinel,
			// the cached entry re-feeds its already-completed bead every scan —
			// sling refuses (bead closed) but the loop churns daemon.log for
			// hours. Drop closed convoys here and invalidate the cache so the
			// next scan recomputes a fresh stranded set without the released
			// convoy. Also closes the TOCTOU window where a convoy closes
			// between findStranded computing the result and this feed.
			if m.convoyClosed(c.ID) {
				m.logger("Convoy %s: closed — dropping stale feed entry, invalidating stranded cache (gs-cxex)", c.ID)
				m.strandedCache = nil
				continue
			}
			slingFailures += m.feedFirstReady(c)
		} else if c.TrackedCount == 0 {
			// Empty convoy — but skip if it was just created (GH#2303).
			// The sling's bd dep add may not be visible in Dolt yet.
			if !c.CreatedAt.IsZero() && time.Since(c.CreatedAt) < convoyGracePeriod {
				m.logger("Convoy %s: empty but within grace period (created %s ago) — skipping", c.ID, time.Since(c.CreatedAt).Round(time.Second))
				continue
			}
			// Empty convoys are NOT closed by the batched completion check
			// (it deliberately treats 0/0 as "unresolved" to avoid false
			// "landed" notifications — GH#3xxx). Handle them per-convoy here;
			// they are rare relative to completion candidates.
			m.closeEmptyConvoy(c.ID)
		} else {
			// Tracked issues exist but none are ready: either all closed
			// (auto-close) or blocked/in-progress (no-op). Defer to the single
			// batched completion pass below instead of one subprocess per convoy.
			if m.isStableCandidate(c.ID, c.TrackedCount) {
				continue
			}
			m.logger("Convoy %s: %d tracked issues, 0 ready — completion candidate", c.ID, c.TrackedCount)
			completionCandidates++
		}
	}

	// ONE batched completion check for all candidates (gu-jqb47): `gt convoy
	// check` with no ID runs checkAndCloseCompletedConvoys over all open
	// convoys in a single subprocess with a single batched tracks query, vs
	// the previous N serial `gt convoy check <id>` spawns.
	//
	// Periodic backstop (gu-urwg6): completed convoys (all tracked closed)
	// are now excluded from findStrandedConvoys so they no longer inflate
	// the stranded result or burn scan cycles. But this means they never
	// increment completionCandidates. Fire the batched check every
	// completionBackstopInterval scans to catch them. The check is cheap
	// when there's nothing to close (early-exits on empty/filtered convoy
	// list) and correctness-critical when there IS something to close.
	m.scanCount++
	backstopDue := m.scanCount%completionBackstopInterval == 0
	if completionCandidates > 0 || backstopDue {
		if completionCandidates > 0 {
			m.logger("Convoy completion: batched check across %d candidate(s)", completionCandidates)
		} else {
			m.logger("Convoy completion: periodic backstop (scan %d)", m.scanCount)
		}
		m.checkAllConvoyCompletion()
	}

	// Feed-storm rate monitor (gc-wwpw2): a sustained high per-scan sling-failure
	// count is the signature of a re-dispatch storm (gu-q1wzq) — beads re-fed
	// every cycle that can never succeed, burning CPU + Dolt connections. No
	// existing monitor catches it: the capacity-exhaustion monitor watches a DEAD
	// pool (these failures never free or fill slots), and the per-bead respawn
	// circuit breaker counts only post-spawn (these are all PRE-spawn rejections).
	// The 2026-06-04 storm burned ~154 Dolt CPU-hrs and killed the data plane
	// before anything noticed. Escalate HIGH once the failure count stays above
	// threshold for several consecutive scans.
	m.monitorFeedStorm(slingFailures)
}

// checkAllConvoyCompletion runs a single `gt convoy check` (no convoy ID) which
// invokes the batched checkAndCloseCompletedConvoys over all open convoys. This
// replaces the per-convoy `gt convoy check <id>` fan-out that serialized the
// dispatch sweep (gu-jqb47). Empty (0-tracked) convoys are handled separately
// via closeEmptyConvoy, since the batched path intentionally leaves 0/0 open.
func (m *ConvoyManager) checkAllConvoyCompletion() {
	cmd := exec.CommandContext(m.ctx, m.gtPath, "convoy", "check")
	cmd.Dir = m.townRoot
	cmd.Env = bdMutationRoutingEnv(m.townRoot)
	util.SetProcessGroup(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		m.logger("Convoy: batched completion check failed: %s", util.FirstLine(stderr.String()))
	}
}

// findStranded runs `gt convoy stranded --json` and parses the output.
// Before forking the subprocess, it queries a lightweight sentinel (open convoy
// count + max updated_at) from the hq store. If unchanged since the last scan,
// the cached result is reused (~50ms no-op vs ~6s fork). See gu-rd9ph.
func (m *ConvoyManager) findStranded() ([]strandedConvoyInfo, error) {
	// Check sentinel before forking. Skip the sentinel on recovery mode (need
	// a fresh scan to clear stale state) or when stores aren't available.
	if !m.recoveryMode.Load() && m.strandedCache != nil {
		if count, maxUpd, ok := m.strandedSentinel(); ok {
			if count == m.strandedCache.openCount && maxUpd.Equal(m.strandedCache.maxUpdate) {
				return m.strandedCache.result, nil
			}
		}
	}

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

	// Update the cache with fresh sentinel values.
	if count, maxUpd, ok := m.strandedSentinel(); ok {
		m.strandedCache = &strandedCacheEntry{
			openCount: count,
			maxUpdate: maxUpd,
			result:    stranded,
		}
	}

	return stranded, nil
}

// strandedSentinel queries a cheap sentinel from the hq store: the count of
// open convoy-type issues and their maximum updated_at timestamp. Returns
// (count, maxUpdatedAt, true) on success, or (0, zero, false) if the query
// cannot be performed (store unavailable, type assertion fails, etc.).
func (m *ConvoyManager) strandedSentinel() (int, time.Time, bool) {
	m.storesMu.Lock()
	hqStore := m.stores["hq"]
	m.storesMu.Unlock()

	if hqStore == nil {
		return 0, time.Time{}, false
	}

	dbAccessor, ok := hqStore.(beadsDBAccessor)
	if !ok || dbAccessor.DB() == nil {
		return 0, time.Time{}, false
	}

	ctx, cancel := context.WithTimeout(m.ctx, 2*time.Second)
	defer cancel()

	var count int
	var maxUpdate sql.NullTime
	err := dbAccessor.DB().QueryRowContext(ctx,
		"SELECT COUNT(*), MAX(updated_at) FROM issues WHERE issue_type = 'convoy' AND status = 'open'",
	).Scan(&count, &maxUpdate)
	if err != nil {
		return 0, time.Time{}, false
	}

	var t time.Time
	if maxUpdate.Valid {
		t = maxUpdate.Time
	}
	return count, t, true
}

// convoyClosed reports whether convoyID is closed in the hq store. It is a
// freshness guard for the stranded-feed loop (gs-cxex): the stranded cache
// (gu-rd9ph) keys its sentinel on the open-convoy count + max convoy
// updated_at, which is BLIND to a convoy closing without shifting that
// sentinel (e.g. other open convoys keep the count/max stable). A stale cache
// entry then re-feeds an already-completed convoy's bead every scan — sling
// correctly refuses (bead closed, work done) but the loop churns daemon.log
// for hours with zero progress. Reading the convoy's live status lets scan()
// drop these released convoys before feeding.
//
// Fails OPEN (returns false → feed proceeds) when the store is unavailable or
// the convoy can't be read: the existing sling-failure paths
// (IsClosedBeadSlingError etc.) still handle a stale bead safely, so a missed
// read here degrades only to the pre-fix behavior, never to a wrong dispatch.
func (m *ConvoyManager) convoyClosed(convoyID string) bool {
	m.storesMu.Lock()
	hqStore := m.stores["hq"]
	m.storesMu.Unlock()

	if hqStore == nil {
		return false
	}

	ctx, cancel := context.WithTimeout(m.ctx, 2*time.Second)
	defer cancel()

	issue, err := hqStore.GetIssue(ctx, convoyID)
	if err != nil || issue == nil {
		return false
	}
	return string(issue.Status) == "closed"
}

// feedFirstReady iterates through all ready issues in a stranded convoy and
// dispatches the first one that can be successfully slung. Issues are skipped
// (with logging) when the prefix is unresolvable, the rig has no route, the
// rig is parked, or the sling command fails. This ensures convoys progress
// even when some issues target unavailable rigs.
// feedFirstReady returns the number of sling attempts that FAILED during this
// call. A single call may try several ready issues (it `continue`s past a failed
// one to the next), so the count can exceed 1. scan() sums these across all
// stranded convoys to drive the feed-storm rate monitor (gc-wwpw2): a sustained
// high per-cycle failure count is the signature of a re-dispatch storm (gu-q1wzq)
// that no pool/respawn monitor catches, because every failure is a pre-spawn
// rejection that never moves capacity or respawn metrics.
func (m *ConvoyManager) feedFirstReady(c strandedConvoyInfo) int {
	if len(c.ReadyIssues) == 0 {
		return 0
	}

	slingFailures := 0
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
			// Throttle this line: a single-child convoy whose child is being
			// worked by a live agent re-enters cooldown every scan, so logging
			// it per-cycle dominated daemon.log (gu-5d3a3). Log first + every
			// Nth repeat.
			if emit, count := m.shouldLogFeedSkip("cd:" + issueID); emit {
				m.logger("Convoy %s: %s in feed cooldown, skipping (×%d)", c.ID, issueID, count)
			}
			continue
		}
		m.resetFeedSkipLog("cd:" + issueID)

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
		// Preserve the convoy's merge strategy on re-dispatch. Without this a
		// stranded merge=local relay leg is re-fed with the default (merge
		// queue) strategy and would auto-MR a do-not-merge prototype to main
		// (gs-9ct #3).
		if c.Merge != "" {
			slingArgs = append(slingArgs, "--merge="+c.Merge)
		}
		cmd := exec.CommandContext(m.ctx, m.gtPath, slingArgs...)
		cmd.Dir = m.townRoot
		cmd.Env = bdMutationRoutingEnv(m.townRoot)
		util.SetProcessGroup(cmd)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			slingFailures++
			stderrLine := util.FirstLine(stderr.String())
			m.logger("Convoy %s: sling %s failed: %s", c.ID, issueID, stderrLine)
			switch {
			case sling.IsBeadNotFoundError(stderrLine):
				// Missing beads have their own resolution path (auto-untrack
				// after threshold strikes) — don't also escalate them.
				m.handleMissingBeadStrike(c.ID, issueID, stderrLine)
			case sling.IsClosedBeadSlingError(stderrLine):
				// Category A (gu-y6ild): the bead closed between the stranded
				// scan and this feed (TOCTOU race). The work is already done —
				// escalating is pure toil. Run a completion check so the convoy
				// auto-closes once all tracked beads are closed; the closed bead
				// drops from the next scan's ready set on its own.
				m.logger("Convoy %s: %s already closed (work completed) — running completion check instead of escalating", c.ID, issueID)
				m.runConvoyCheck(c.ID) // idempotent completion+close; no escalation
			case sling.IsDoNotDispatchSlingError(stderrLine):
				// Category C variant (gu-q1wzq): the bead is a do-not-dispatch /
				// pinned reference tripwire — a permanent live safety gate the
				// scheduler refuses by design (it must stay OPEN, never hooked).
				// Like other structural non-work items it can NEVER become a
				// dispatchable convoy step, so auto-untrack it (same seam as
				// handleNonWorkBead) instead of re-feeding it every scan. This was
				// the dominant share (~3,000/day, ≈383 per bead) of the convoy
				// re-dispatch storm — it previously fell through to the default
				// branch, which escalated once but kept re-feeding on the flat 5m
				// cooldown forever (no churn-backoff on the failure path).
				m.handleNonWorkBead(c.ID, issueID, stderrLine)
			case sling.IsStructuralNonWorkSlingError(stderrLine):
				// Category C (gu-y6ild): the bead is a structural non-work item
				// (epic/container with open children, identity bead, sling-context
				// wrapper, flag-like garbage, or polecat-owned). It can never be a
				// dispatchable convoy step. Auto-untrack it from the convoy (reusing
				// the missing-bead untrack path) so the convoy can progress and
				// auto-close, instead of escalating to the Mayor every scan.
				m.handleNonWorkBead(c.ID, issueID, stderrLine)
			case sling.IsActivelyWorkedSlingError(stderrLine):
				// The bead is already hooked / in_progress to a LIVE agent (gs-2dr).
				// sling's own dead-agent detection auto-forces dispatch when the
				// hooked agent's session is gone, so an "already hooked" / "already
				// in_progress" rejection means the agent is alive and the bead is
				// being worked right now — it is progressing, not wedged. Suppress
				// the escalation (no untrack: this is a legitimate convoy step that
				// will close on its own and trip the completion check). Without this
				// the feeder floods the Mayor with HIGH 'cannot dispatch / will never
				// progress' false-positives indistinguishable from real wedges.
				//
				// Advance the feed-churn streak (gu-q1wzq): an actively-worked bead
				// has neither a strike→untrack path (it's legitimate, not missing)
				// nor — previously — any backoff, so it was re-fed on the flat 5m
				// cooldown every scan for the entire duration of the work. A
				// long-running task therefore generated a steady re-feed/sling-fail
				// stream (1,883 such retries in the gu-q1wzq storm). Recording churn
				// here decays the re-feed interval 5m→1h while the work proceeds,
				// without untracking (the step is real and will close on its own).
				m.logger("Convoy %s: %s already hooked/in_progress to a live agent — in progress, suppressing escalation (gs-2dr)", c.ID, issueID)
				m.recordFeedChurn(issueID)
			case sling.IsDeferredSlingError(stderrLine):
				// The bead is intentionally DEFERRED (gt-3798). sling refuses by
				// design so deferred work doesn't consume polecat slots — but a
				// deferred step is not wedged, it's waiting: it becomes dispatchable
				// the moment it's un-deferred. Escalating it as "cannot dispatch /
				// will never progress" is wrong AND it re-fired every scan cycle for
				// EVERY deferred bead in EVERY system convoy, flooding the Mayor inbox
				// (the gt-3798 mass-escalation storm). Treat it like an actively-worked
				// bead: suppress the escalation and back off the re-feed (5m→1h), with
				// NO untrack — the step is legitimate tracked work that must survive
				// the hold (deferred beads must NOT be mass-closed/untracked).
				m.logger("Convoy %s: %s is deferred — intentionally held, suppressing escalation and backing off (gt-3798)", c.ID, issueID)
				m.recordFeedChurn(issueID)
			case sling.IsAwaitingRefineryMergeSlingError(stderrLine):
				// The bead is awaiting refinery merge (gu-ea25u): its MR is
				// submitted and sitting in the merge queue, and the bead is kept
				// OPEN only so the refinery's PostMerge path can close it with the
				// real commit_sha. sling refuses by design (re-slinging spawns a
				// fresh polecat that finds the work complete). This is a BENIGN,
				// self-resolving in-flight state — normal merge-queue latency, not a
				// stall: the refinery clears the label on merge (or the reaper for a
				// proven-merged orphan). Escalating it as "cannot dispatch / will
				// never progress" is both wrong (it WILL progress) and re-fired every
				// scan cycle for EVERY such bead across ALL rigs whose refineries are
				// actively merging, drowning the Mayor inbox (gt-3798 escalation
				// storm). Treat it like a deferred/actively-worked bead: suppress the
				// escalation and back off the re-feed (5m→1h), with NO untrack — the
				// step is legitimate tracked work that will close on merge.
				m.logger("Convoy %s: %s is awaiting refinery merge — MR queued, suppressing escalation and backing off (gu-ea25u)", c.ID, issueID)
				m.recordFeedChurn(issueID)
			default:
				if _, already := m.seenSlingErrors.LoadOrStore(issueID, true); !already {
					// Genuinely-ambiguous persistent failure (e.g. an unroutable
					// target under the capacity scheduler, mayor-only/no-polecat
					// assertion, gt-3798): escalate once per stranded issue instead
					// of every scan cycle. These legitimately need Mayor judgment.
					m.escalateSlingFailure(c.ID, issueID, stderrLine)
				}
			}
			continue
		}
		// Successful dispatch — clear any accumulated missing-bead strikes
		// for this (convoy, bead) pair. A bead that was transiently invisible
		// (Dolt restart, replication lag) shouldn't carry strikes forward.
		m.missingBeadStrikes.Delete(missingBeadKey{c.ID, issueID})
		// Clear any prior sling-failure record so a future failure (if the
		// issue is re-queued) is escalated again (gt-3798).
		m.seenSlingErrors.Delete(issueID)
		// Advance the escalate-churn streak: a bead re-dispatched again and
		// again (refused/escalated, never closing) backs off toward
		// feedCooldownCap instead of being re-fed every 5 min (gs-skv).
		m.recordFeedChurn(issueID)
		// Convoy made progress this scan — reset the per-convoy "no
		// dispatchable" throttle so a future stall logs immediately (gu-5d3a3).
		m.resetFeedSkipLog("nd:" + c.ID)
		return slingFailures // Successfully dispatched one issue
	}

	// Throttle: a single-child convoy whose only child is non-dispatchable
	// (in cooldown / hooked to a live agent) reaches this line every scan,
	// producing the bulk of daemon.log volume (gu-5d3a3). Log first + every
	// Nth repeat per convoy.
	if emit, count := m.shouldLogFeedSkip("nd:" + c.ID); emit {
		m.logger("Convoy %s: no dispatchable issues (all %d skipped) (×%d)", c.ID, len(c.ReadyIssues), count)
	}
	return slingFailures
}

// escalateSlingFailure fires a one-shot gt escalate for a stranded convoy step.
// It is called at most once per issueID (the caller deduplicates via
// seenSlingErrors), turning silent every-60s daemon.log spam into a single
// actionable HIGH escalation (gt-3798).
func (m *ConvoyManager) escalateSlingFailure(convoyID, issueID, errMsg string) {
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	defer cancel()

	msg := fmt.Sprintf("Convoy %s: step %s cannot be dispatched and will never progress — %s (gt-3798). Investigate with: gt convoy status %s", convoyID, issueID, errMsg, convoyID)
	cmd := exec.CommandContext(ctx, m.gtPath, "escalate", "-s", "HIGH", msg)
	cmd.Dir = m.townRoot
	util.SetProcessGroup(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		m.logger("Convoy %s: escalation failed: %v (%s)", convoyID, err, strings.TrimSpace(string(out)))
	}
}

// handleNonWorkBead untracks a structural non-work bead (Category C) from the
// convoy so the stranded scan stops re-attempting an impossible sling and the
// convoy can make progress / auto-close. It reuses the missing-bead untrack
// seam (untrackMissingBeadFn → `bd dep remove`). Unlike the missing-bead path,
// no strike threshold is applied: a structural rejection is deterministic and
// permanent (the bead's data shape will not change between scans), so a single
// failure is sufficient evidence to untrack. On untrack failure the next scan
// retries. (gu-y6ild)
func (m *ConvoyManager) handleNonWorkBead(convoyID, issueID, stderrLine string) {
	m.logger("Convoy %s: %s is a structural non-work bead (%s) — auto-untracking (gu-y6ild)",
		convoyID, issueID, stderrLine)
	if m.untrackMissingBeadFn == nil {
		return
	}
	if err := m.untrackMissingBeadFn(convoyID, issueID); err != nil {
		m.logger("Convoy %s: untrack of non-work %s failed: %s — will retry next scan",
			convoyID, issueID, util.FirstLine(err.Error()))
		return
	}
	// Clear any strike/error state for this pair so a future (re-added) bead
	// starts fresh.
	m.missingBeadStrikes.Delete(missingBeadKey{convoyID, issueID})
	m.seenSlingErrors.Delete(issueID)
	m.logger("Convoy %s: untracked non-work bead %s — next scan will reassess convoy state",
		convoyID, issueID)
}

// handleMissingBeadStrike handles a "bead not found" sling failure. With
// threshold=1, the first failure immediately triggers a confirmation re-check
// (`bd show`) to verify the bead truly doesn't exist. If confirmed missing,
// the bead is auto-untracked. If the confirmation shows the bead exists
// (transient Dolt hiccup), the strike is cleared and no untrack occurs.
// This is restart-proof: no in-memory counter needs to survive daemon
// restarts. See gu-f0gq, gu-dvcs4.
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

	// Threshold reached — perform a definitive confirmation check before
	// untracking. This absorbs transient Dolt hiccups (the tolerance N=3
	// previously provided) in a single scan cycle without requiring
	// persistent state across daemon restarts. (gu-dvcs4)
	if m.checkBeadExistenceFn != nil {
		switch m.checkBeadExistenceFn(issueID) {
		case beadExists:
			// Bead exists — the sling failure was a transient hiccup. Clear
			// the strike so the bead is retried next scan without penalty.
			m.missingBeadStrikes.Delete(key)
			m.logger("Convoy %s: %s confirmation check shows bead EXISTS — clearing strike (transient hiccup)",
				convoyID, issueID)
			return
		case beadCheckAmbiguous:
			// Infra error (Dolt circuit-breaker open, connection refused,
			// timeout) — we could NOT determine whether the bead exists.
			// Do NOT collapse this into a state verdict (gu-3hi1f). Leave
			// the strike in place (do not untrack, do not clear) so the
			// existence check is retried next scan once Dolt recovers,
			// rather than untracking a bead that may well exist.
			m.logger("Convoy %s: %s existence check ambiguous (infra error) — retrying next scan, not untracking (gu-3hi1f)",
				convoyID, issueID)
			return
		case beadMissing:
			// fall through to untrack below
		}
	}

	m.logger("Convoy %s: %s confirmed missing — auto-untracking",
		convoyID, issueID)
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

// beadExistence is the tri-state result of an existence check. It deliberately
// separates "I could not determine state" (beadCheckAmbiguous) from the two
// real states so an infra error is never folded into a state verdict (gu-3hi1f).
type beadExistence int

const (
	// beadExists: the bead was definitively found (the check succeeded).
	beadExists beadExistence = iota
	// beadMissing: the bead definitively does not exist ("not found" error).
	beadMissing
	// beadCheckAmbiguous: the check could not determine state (Dolt
	// circuit-breaker open, connection refused, timeout). NOT a state — the
	// caller must retry rather than infer existence or absence.
	beadCheckAmbiguous
)

// checkBeadExistenceViaBd runs `bd show <issueID>` and reports a tri-state
// result. It returns beadMissing only when stderr explicitly says "not found",
// beadExists on success, and beadCheckAmbiguous on any other failure (Dolt
// circuit-breaker open, connection refused, timeout). An ambiguous infra error
// must NOT be interpreted as a bead state — collapsing it into "exists" made
// dispatch decisions during a Dolt outage on assumptions rather than real
// state. See gu-dvcs4 (existence check) and gu-3hi1f (this separation).
func (m *ConvoyManager) checkBeadExistenceViaBd(issueID string) beadExistence {
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bd", "show", issueID)
	cmd.Dir = m.townRoot
	cmd.Env = bdReadOnlyRoutingEnv(m.townRoot)
	util.SetProcessGroup(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		// Only confirm missing if stderr explicitly says "not found"
		if sling.IsBeadNotFoundError(msg) {
			return beadMissing
		}
		// Ambiguous failure (circuit-breaker, timeout, Dolt down) — could not
		// determine state. Caller retries; do NOT infer existence (gu-3hi1f).
		m.logger("Convoy: checkBeadExistence(%s): ambiguous error: %s — could not determine state, will retry", issueID, util.FirstLine(msg))
		return beadCheckAmbiguous
	}
	// bd show succeeded — bead exists
	return beadExists
}

// closeEmptyConvoy runs gt convoy check to auto-close an empty convoy.
func (m *ConvoyManager) closeEmptyConvoy(convoyID string) {
	m.logger("Convoy %s: auto-closing (empty)", convoyID)
	m.runConvoyCheck(convoyID)
}

// runConvoyCheck runs `gt convoy check <convoyID>` for a single convoy. This is
// the idempotent completion-and-close pass: it closes the convoy iff all tracked
// beads are closed (and shipped). Used by closeEmptyConvoy and by the Category A
// closed-bead race path in feedFirstReady (gu-y6ild).
func (m *ConvoyManager) runConvoyCheck(convoyID string) {
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

// feedChurnEntry records a bead's escalate-churn streak: how many consecutive
// re-feeds within feedChurnWindow, and when the streak was last touched (so it
// can age out). See gs-skv.
type feedChurnEntry struct {
	count int
	last  time.Time
}

// effectiveFeedCooldown returns the cooldown for issueID, scaled by its
// escalate-churn streak (gs-skv). The first dispatch uses the base 5-minute
// cooldown; each consecutive re-feed within feedChurnWindow doubles it
// (5m → 10m → 20m → 40m → …), capped at feedCooldownCap. This converts a
// flat-rate re-feed loop (an unworkable bead re-fed every 5 min for hours)
// into a fast-decaying backoff that re-feeds at most hourly.
func (m *ConvoyManager) effectiveFeedCooldown(issueID string) time.Duration {
	churn := 0
	if v, ok := m.feedChurn.Load(issueID); ok {
		if e, ok := v.(feedChurnEntry); ok {
			churn = e.count
		}
	}
	if churn <= 1 {
		return feedDispatchCooldown
	}
	// Double per streak step; guard against overflow from a large shift.
	shift := churn - 1
	if shift > 16 {
		return feedCooldownCap
	}
	d := feedDispatchCooldown << uint(shift)
	if d <= 0 || d > feedCooldownCap {
		return feedCooldownCap
	}
	return d
}

// inFeedCooldown reports whether issueID was slung within its (escalating)
// effective cooldown. Stale entries (older than the cooldown) are pruned in
// place to bound the lastFeedAttempt map. See gu-iygf / gs-skv.
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
	if m.now().Sub(last) < m.effectiveFeedCooldown(issueID) {
		return true
	}
	// Cooldown expired — drop the entry so the map doesn't grow unbounded
	// across an infinite stream of one-shot issue IDs. The entry will be
	// rewritten by recordFeedAttempt on the next dispatch.
	m.lastFeedAttempt.Delete(issueID)
	return false
}

// recordFeedAttempt stamps issueID with the current time so subsequent scans
// within the effective cooldown skip it. See gu-iygf.
func (m *ConvoyManager) recordFeedAttempt(issueID string) {
	m.lastFeedAttempt.Store(issueID, m.now())
}

// shouldLogFeedSkip reports whether a repeating feeder "skip" log line keyed
// by `key` should be emitted this scan. It returns true on the first
// occurrence and then once every feedLogThrottleInterval repeats, so a
// quiescent single-child convoy stops re-logging the same skip every scan
// (gu-5d3a3). The repeat count is returned so callers can surface it. The
// counter is reset via resetFeedSkipLog once the convoy makes progress.
func (m *ConvoyManager) shouldLogFeedSkip(key string) (emit bool, count int) {
	var n int
	if v, ok := m.feedSkipLog.Load(key); ok {
		if c, ok := v.(int); ok {
			n = c
		}
	}
	n++
	m.feedSkipLog.Store(key, n)
	return n == 1 || n%feedLogThrottleInterval == 0, n
}

// resetFeedSkipLog clears the throttle counter for key so the next skip after
// progress logs immediately rather than being suppressed by a stale count.
func (m *ConvoyManager) resetFeedSkipLog(key string) {
	m.feedSkipLog.Delete(key)
}

// recordFeedChurn advances issueID's escalate-churn streak (gs-skv). It is
// called after a SUCCESSFUL re-dispatch, and also on the actively-worked
// (already-hooked/in_progress) failure path (gu-q1wzq) — both represent a bead
// that keeps reappearing in the stranded set without terminating, which is the
// escalate-churn this backs off (5m→1h). A re-dispatch/re-feed within
// feedChurnWindow raises the streak (and the next cooldown); a longer gap resets
// it to 1, so a bead that genuinely progressed and only later reappears is not
// pre-penalized.
//
// It is deliberately NOT called on the "bead not found" failure path: that is a
// missing-bead case with its own strike→untrack termination
// (missingBeadStrikeThreshold), and must not inherit the escalating backoff or
// it would never accumulate its 3 strikes. The structural / do-not-dispatch
// paths don't need it either — they untrack permanently on first failure.
func (m *ConvoyManager) recordFeedChurn(issueID string) {
	now := m.now()

	churn := 1
	if v, ok := m.feedChurn.Load(issueID); ok {
		if e, ok := v.(feedChurnEntry); ok && now.Sub(e.last) < feedChurnWindow {
			churn = e.count + 1
		}
	}
	m.feedChurn.Store(issueID, feedChurnEntry{count: churn, last: now})

	// Surface the moment a bead crosses into heavy backoff so an operator can
	// see the churn instead of it silently slowing.
	if churn >= 3 {
		m.logger("Convoy feed: %s churn ×%d — backing off to %s (gs-skv escalate-churn guard)",
			issueID, churn, m.effectiveFeedCooldown(issueID))
	}
}

// isStableCandidate checks whether a completion candidate (tracked>0, ready=0)
// has been unchanged long enough to skip. Returns true if the candidate should
// be skipped this scan. Updates the stability tracker for this convoy.
//
// The tracked count is used as the stability signal: if it hasn't changed for
// stableSkipThreshold consecutive scans, the convoy is considered stable and
// skipped from the batched completion check — the event-poll path handles
// close-driven completion, so the subprocess is wasted work. A hard backstop
// (stableBackstopScans) forces a periodic re-check. See gu-0vuw1.
func (m *ConvoyManager) isStableCandidate(convoyID string, trackedCount int) bool {
	var prev stableCandidate
	if v, ok := m.stableCandidates.Load(convoyID); ok {
		prev = v.(stableCandidate)
	}

	if prev.trackedCount == trackedCount && prev.trackedCount != 0 {
		prev.stableCount++
	} else {
		// TrackedCount changed (or first observation) — reset.
		prev = stableCandidate{trackedCount: trackedCount, stableCount: 1}
	}

	m.stableCandidates.Store(convoyID, prev)

	if prev.stableCount < stableSkipThreshold {
		return false
	}

	// Hard backstop: force re-check every stableBackstopScans.
	scansSinceCheck := prev.stableCount - stableSkipThreshold
	if scansSinceCheck > 0 && scansSinceCheck%stableBackstopScans == 0 {
		m.logger("Convoy %s: stable backstop reached (%d scans) — forcing completion check", convoyID, prev.stableCount)
		return false
	}

	m.logger("Convoy %s: stable for %d scans (tracked=%d) — skipping completion check", convoyID, prev.stableCount, trackedCount)
	return true
}

// resetStableCandidateForIssue resets stability tracking for any convoy that
// might track the given issueID. Called when a close event fires so that the
// next scan includes the convoy in the batched completion check.
//
// Since stableCandidates is keyed by convoyID and we don't maintain a reverse
// index (issueID → convoyIDs), this clears ALL stable candidate entries. This
// is correct (benign worst case: one extra completion check for unrelated
// convoys) and simple. The alternative (maintaining a reverse map) would add
// complexity for negligible gain since close events are infrequent relative to
// scan cycles.
func (m *ConvoyManager) resetStableCandidates() {
	m.stableCandidates.Range(func(key, _ any) bool {
		m.stableCandidates.Delete(key)
		return true
	})
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

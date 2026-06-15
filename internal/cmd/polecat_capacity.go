package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
)

// polecatCapacitySnapshotCacheTTL is the maximum staleness allowed for a
// cached town-wide polecat capacity snapshot. Computing a fresh snapshot
// requires one `bd list --label=gt:agent` subprocess per rig, which serialize
// through bd-list-read.flock (5s timeout). With 19 rigs in a busy town,
// recomputing per call accounts for ~49% of all bd subprocess traffic during
// a dispatch cycle and routinely starves the dispatcher's own bd calls
// (live trace evidence: 494/1011 PIDs were this single call).
//
// 5s aligns with the bd-list-read flock timeout — even if every dispatch
// path requested a snapshot back-to-back, no individual call would wait
// longer for fresh data than the flock would have made it wait anyway.
//
// Capacity counts (Working, RecoveryBlocked, etc.) drift slowly: a polecat
// state change takes at least a session-restart cycle (seconds), and the
// admission gate has its own per-flock atomic check on the actual
// reservation file — so a snapshot stale by up to 5s cannot cause
// overcommit, only delay reporting of newly-freed slots. (gu-yfv7)
const polecatCapacitySnapshotCacheTTL = 5 * time.Second

var (
	polecatCapacityCacheMu sync.Mutex
	polecatCapacityCache   map[string]cachedCapacitySnapshot // keyed by townRoot
)

type cachedCapacitySnapshot struct {
	snapshot polecatCapacitySnapshot
	// perRigOccupied is the per-rig occupied-slot count computed in the SAME
	// pass as the town snapshot, so the per-rig cap and the town cap count the
	// identical unit (Working + RecoveryBlocked + that rig's reservations) and
	// cannot drift (gu-ccycc). nil when max<=0 or the snapshot errored.
	perRigOccupied map[string]int
	err            error
	at             time.Time
}

var acquirePolecatAdmissionFn = acquirePolecatAdmission

// polecatAdmissionLoadPerCore returns the host 1-minute load average per
// logical core. A test seam; production wires it to the real load reader.
// Returns 0 when load is unavailable (e.g. Windows), which fails open — an
// unreadable load average must never block dispatch.
var polecatAdmissionLoadPerCore = util.LoadPerCore

// checkPolecatLoadThrottle refuses admission when the configured host
// load-per-core ceiling (scheduler.max_load_per_core) is exceeded. Unlike the
// capacity cap, this gate runs in ALL dispatch modes — including uncapped
// direct dispatch (max_polecats <= 0), the path that saturated the host in the
// gu-5j7p4 meltdown by granting admission immediately with zero load
// backpressure.
//
// Opt-in (default 0 = disabled) and fail-open on unknown load (0), matching the
// PressureCPUThreshold and refinery-backoff conventions. The returned error is
// retryable: the bead stays queued and the next dispatch re-evaluates once load
// eases.
func checkPolecatLoadThrottle(townRoot, rigName, beadID string) error {
	threshold, err := configuredSchedulerMaxLoadPerCore(townRoot)
	if err != nil {
		return err
	}
	if threshold <= 0 {
		return nil
	}
	loadPerCore := polecatAdmissionLoadPerCore()
	if loadPerCore <= 0 || loadPerCore <= threshold {
		return nil
	}
	return fmt.Errorf("polecat admission denied: host load/core %.2f exceeds "+
		"scheduler.max_load_per_core %.2f (rig %s bead %s). Deferring spawn so host "+
		"load can ease; bead stays queued and retries on the next dispatch. "+
		"Disable: gt config set scheduler.max_load_per_core 0",
		loadPerCore, threshold, rigName, beadID)
}

func configuredSchedulerMaxLoadPerCore(townRoot string) (float64, error) {
	settings, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot))
	if err != nil {
		return 0, fmt.Errorf("loading town settings for polecat admission: %w", err)
	}
	schedulerCfg := settings.Scheduler
	if schedulerCfg == nil {
		schedulerCfg = capacity.DefaultSchedulerConfig()
	}
	return schedulerCfg.GetMaxLoadPerCore(), nil
}

// configuredSchedulerGlobalMaxPolecats returns the configured town-wide polecat
// ceiling (scheduler.global_max_polecats), or 0 (unbounded) when unset.
func configuredSchedulerGlobalMaxPolecats(townRoot string) (int, error) {
	settings, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot))
	if err != nil {
		return 0, fmt.Errorf("loading town settings for polecat admission: %w", err)
	}
	schedulerCfg := settings.Scheduler
	if schedulerCfg == nil {
		schedulerCfg = capacity.DefaultSchedulerConfig()
	}
	return schedulerCfg.GetGlobalMaxPolecats(), nil
}

// countWorkingPolecatsTownWideFn is a test seam over countWorkingPolecatsTownWide.
// Production wires it to the real tmux+beads-backed counter.
var countWorkingPolecatsTownWideFn = countWorkingPolecatsTownWide

// countWorkingPolecatsTownWide returns the total number of working polecats
// across all rigs — the unit the global ceiling and the per-rig caps both
// count in. It sums the per-rig working counts; on failure to determine hook
// state via beads it falls back to the count of all active polecat sessions
// (working + idle), which over-counts. Over-counting is the safe direction for
// a ceiling: it can only refuse admission early, never overcommit.
func countWorkingPolecatsTownWide() int {
	counts, ok := countWorkingPolecatsByRig()
	if !ok {
		return countActivePolecats()
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	return total
}

// checkGlobalPolecatCeiling refuses admission when the configured town-wide
// ceiling (scheduler.global_max_polecats) is reached, counting working polecats
// across ALL rigs. Runs in every dispatch mode (gu-su334). Disabled (0) =
// no-op. The returned error is retryable: the bead stays queued and the next
// dispatch re-evaluates once a town-wide slot frees up.
func checkGlobalPolecatCeiling(townRoot, rigName, beadID string) error {
	ceiling, err := configuredSchedulerGlobalMaxPolecats(townRoot)
	if err != nil {
		return err
	}
	if ceiling <= 0 {
		return nil
	}
	townActive := countWorkingPolecatsTownWideFn()
	if townActive < ceiling {
		return nil
	}
	return fmt.Errorf("polecat admission denied: %d/%d town-wide working polecats "+
		"(scheduler.global_max_polecats ceiling reached; rig %s bead %s). "+
		"Wait for a polecat to finish, or raise: gt config set scheduler.global_max_polecats %d",
		townActive, ceiling, rigName, beadID, ceiling+1)
}

type polecatCapacitySnapshot struct {
	Max             int `json:"max"`
	Working         int `json:"working"`
	RecoveryBlocked int `json:"recovery_blocked"`
	ReusableIdle    int `json:"reusable_idle"`
	PendingMR       int `json:"pending_mr"`
	Reservations    int `json:"reservations"`
	Free            int `json:"free"`
	ActiveSessions  int `json:"active_sessions"`
}

func (s polecatCapacitySnapshot) occupied() int {
	return s.Working + s.RecoveryBlocked + s.Reservations
}

type polecatAdmissionReservation struct {
	ID        string    `json:"id"`
	PID       int       `json:"pid"`
	Rig       string    `json:"rig,omitempty"`
	Bead      string    `json:"bead,omitempty"`
	Operation string    `json:"operation"`
	CreatedAt time.Time `json:"created_at"`
}

type polecatAdmissionHandle struct {
	townRoot string
	id       string
	path     string
	disabled bool
}

func (h *polecatAdmissionHandle) Release() {
	if h == nil || h.disabled || h.path == "" {
		return
	}
	_ = os.Remove(h.path)
}

type polecatCapacityAdmissionError struct {
	Snapshot polecatCapacitySnapshot
	Rig      string
	Bead     string
	Reason   string
}

func (e *polecatCapacityAdmissionError) Error() string {
	if e == nil {
		return "polecat admission denied"
	}
	if e.Snapshot.Max <= 0 {
		return fmt.Sprintf("polecat admission denied: %s", e.Reason)
	}
	return fmt.Sprintf(
		"polecat admission denied: %s (max=%d occupied=%d working=%d recovery_blocked=%d reservations=%d reusable_idle=%d pending_mr=%d free=%d). Resolve recovery-needed polecats or raise scheduler.max_polecats; inspect with `gt scheduler status --json` or `gt polecat list --all --json`",
		e.Reason,
		e.Snapshot.Max,
		e.Snapshot.occupied(),
		e.Snapshot.Working,
		e.Snapshot.RecoveryBlocked,
		e.Snapshot.Reservations,
		e.Snapshot.ReusableIdle,
		e.Snapshot.PendingMR,
		e.Snapshot.Free,
	)
}

func acquirePolecatAdmission(townRoot, rigName, beadID, operation string) (*polecatAdmissionHandle, polecatCapacitySnapshot, error) {
	max, err := configuredSchedulerMaxPolecats(townRoot)
	if err != nil {
		return nil, polecatCapacitySnapshot{}, err
	}
	// Host-load backpressure gate runs in ALL dispatch modes, including
	// uncapped direct dispatch (max <= 0). This is the missing throttle that
	// let the host saturate in gu-5j7p4 — the capacity cap below only guards
	// the deferred (max > 0) path.
	if err := checkPolecatLoadThrottle(townRoot, rigName, beadID); err != nil {
		return nil, polecatCapacitySnapshot{Max: max, ActiveSessions: countActivePolecats()}, err
	}
	// Global ceiling (scheduler.global_max_polecats) is a hard town-wide cap on
	// working polecats enforced in ALL dispatch modes — unlike max_polecats,
	// which doubles as the direct/deferred switch and provides no global cap on
	// the direct path (gu-su334). Counts working polecats (same unit as per-rig
	// caps), so the effective per-rig limit is min(ceiling, per-rig cap).
	if err := checkGlobalPolecatCeiling(townRoot, rigName, beadID); err != nil {
		return nil, polecatCapacitySnapshot{Max: max, ActiveSessions: countActivePolecats()}, err
	}
	if max <= 0 {
		return &polecatAdmissionHandle{disabled: true}, polecatCapacitySnapshot{Max: max, ActiveSessions: countActivePolecats()}, nil
	}

	lock, err := acquirePolecatAdmissionLock(townRoot)
	if err != nil {
		return nil, polecatCapacitySnapshot{}, err
	}
	defer func() { _ = lock.Unlock() }()

	if err := cleanupStalePolecatAdmissionReservations(townRoot); err != nil {
		return nil, polecatCapacitySnapshot{}, err
	}

	snapshot, perRigOccupied, err := polecatCapacitySnapshotForTownNoCleanup(townRoot)
	if err != nil {
		return nil, polecatCapacitySnapshot{}, err
	}
	if snapshot.Free <= 0 {
		return nil, snapshot, &polecatCapacityAdmissionError{
			Snapshot: snapshot,
			Rig:      rigName,
			Bead:     beadID,
			Reason:   "configured scheduler.max_polecats capacity is full",
		}
	}

	// Authoritative per-rig cap check, genuinely under the admission flock
	// (gu-uxwob). perRigOccupied[rigName] counts the same unit as the town cap —
	// working + recovery_blocked + that rig's already-written reservations
	// (gu-ccycc) — and is recomputed here while the flock is held, AFTER stale
	// reservations were cleaned (above) and BEFORE this sling writes its own.
	// So a concurrent sling that claimed the rig's last slot has, by the time we
	// hold the lock, an on-disk reservation attributed to the rig and is counted
	// here. This closes the race the pre-flight/post-admission checks in
	// SpawnPolecatForSling could not: those run lock-free (the flock is released
	// when this function returns), so they are duplicate reads, not a
	// serialization point. The returned error is retryable (the bead stays
	// queued and the next dispatch re-evaluates once a per-rig slot frees up).
	if cap := loadRigPolecatMaxConcurrent(filepath.Join(townRoot, rigName)); cap > 0 {
		if occupied := perRigOccupied[rigName]; occupied >= cap {
			return nil, snapshot, &polecatCapacityAdmissionError{
				Snapshot: snapshot,
				Rig:      rigName,
				Bead:     beadID,
				Reason: fmt.Sprintf("rig %s per-rig cap reached (%d/%d working+recovery+reservations; "+
					"raise: gt rig settings set %s polecat.max_concurrent %d)",
					rigName, occupied, cap, rigName, cap+1),
			}
		}
	}

	reservation, path, err := writePolecatAdmissionReservation(townRoot, rigName, beadID, operation)
	if err != nil {
		return nil, snapshot, err
	}
	snapshot.Reservations++
	snapshot.Free--
	return &polecatAdmissionHandle{townRoot: townRoot, id: reservation.ID, path: path}, snapshot, nil
}

func configuredSchedulerMaxPolecats(townRoot string) (int, error) {
	settings, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot))
	if err != nil {
		return 0, fmt.Errorf("loading town settings for polecat admission: %w", err)
	}
	schedulerCfg := settings.Scheduler
	if schedulerCfg == nil {
		schedulerCfg = capacity.DefaultSchedulerConfig()
	}
	return schedulerCfg.GetMaxPolecats(), nil
}

func polecatCapacitySnapshotForTown(townRoot string) (polecatCapacitySnapshot, error) {
	// Fast path: serve a recent cached snapshot if one exists. The dispatch
	// loop's AvailableCapacity callback, the scheduler-status command, and
	// the failed-attempt summary all call this within milliseconds of one
	// another during a single dispatch cycle, and recomputing means
	// `bd list --label=gt:agent` x N rigs through the bd-list-read flock —
	// see polecatCapacitySnapshotCacheTTL doc for the full motivation.
	//
	// Cache miss falls through to the slow recompute path, which both:
	//   - cleans stale admission reservations (rate-limited within the
	//     reservation flock), and
	//   - performs the per-rig `bd list` fan-out.
	//
	// Both happen at most once per polecatCapacitySnapshotCacheTTL window. (gu-yfv7)
	if cached, ok := loadCachedPolecatCapacitySnapshot(townRoot); ok {
		return cached.snapshot, cached.err
	}

	max, err := configuredSchedulerMaxPolecats(townRoot)
	if err != nil {
		return polecatCapacitySnapshot{}, err
	}
	if max > 0 {
		if err := cleanupStalePolecatAdmissionReservationsWithLock(townRoot); err != nil {
			return polecatCapacitySnapshot{}, err
		}
	}
	snapshot, perRig, err := polecatCapacitySnapshotForTownNoCleanup(townRoot)
	storeCachedPolecatCapacitySnapshot(townRoot, snapshot, perRig, err)
	return snapshot, err
}

// perRigOccupiedSlots returns the per-rig occupied-slot map (working +
// recovery-blocked + that rig's reservations) computed in the same pass as the
// town capacity snapshot, serving it from the shared 5s cache. The per-rig cap
// reads this so it counts the IDENTICAL unit as the town cap and a
// dead-session-hooked polecat occupies a per-rig slot, not just a town slot
// (gu-ccycc). The boolean is false when no per-rig data is available (max<=0,
// snapshot error, or beads unreadable), in which case callers fall back to the
// tmux-session count.
func perRigOccupiedSlots(townRoot string) (map[string]int, bool) {
	if cached, ok := loadCachedPolecatCapacitySnapshot(townRoot); ok {
		if cached.err != nil || cached.perRigOccupied == nil {
			return nil, false
		}
		return cached.perRigOccupied, true
	}
	max, err := configuredSchedulerMaxPolecats(townRoot)
	if err != nil {
		return nil, false
	}
	if max > 0 {
		if err := cleanupStalePolecatAdmissionReservationsWithLock(townRoot); err != nil {
			return nil, false
		}
	}
	snapshot, perRig, err := polecatCapacitySnapshotForTownNoCleanup(townRoot)
	storeCachedPolecatCapacitySnapshot(townRoot, snapshot, perRig, err)
	if err != nil || perRig == nil {
		return nil, false
	}
	return perRig, true
}

// loadCachedPolecatCapacitySnapshot returns the cached snapshot if it is
// fresher than polecatCapacitySnapshotCacheTTL. Returns ok=false otherwise.
func loadCachedPolecatCapacitySnapshot(townRoot string) (cachedCapacitySnapshot, bool) {
	polecatCapacityCacheMu.Lock()
	defer polecatCapacityCacheMu.Unlock()
	if polecatCapacityCache == nil {
		return cachedCapacitySnapshot{}, false
	}
	entry, ok := polecatCapacityCache[townRoot]
	if !ok {
		return cachedCapacitySnapshot{}, false
	}
	if time.Since(entry.at) > polecatCapacitySnapshotCacheTTL {
		return cachedCapacitySnapshot{}, false
	}
	return entry, true
}

// storeCachedPolecatCapacitySnapshot records the result of a fresh snapshot
// computation for reuse by subsequent callers within the TTL window.
func storeCachedPolecatCapacitySnapshot(townRoot string, snapshot polecatCapacitySnapshot, perRigOccupied map[string]int, err error) {
	polecatCapacityCacheMu.Lock()
	defer polecatCapacityCacheMu.Unlock()
	if polecatCapacityCache == nil {
		polecatCapacityCache = make(map[string]cachedCapacitySnapshot)
	}
	polecatCapacityCache[townRoot] = cachedCapacitySnapshot{
		snapshot:       snapshot,
		perRigOccupied: perRigOccupied,
		err:            err,
		at:             time.Now(),
	}
}

func polecatCapacitySnapshotForTownNoCleanup(townRoot string) (polecatCapacitySnapshot, map[string]int, error) {
	max, err := configuredSchedulerMaxPolecats(townRoot)
	if err != nil {
		return polecatCapacitySnapshot{}, nil, err
	}
	snapshot := polecatCapacitySnapshot{Max: max, ActiveSessions: countActivePolecats()}
	if max <= 0 {
		return snapshot, nil, nil
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return snapshot, nil, fmt.Errorf("loading rigs config for polecat capacity: %w", err)
	}

	// Enumerate live tmux sessions ONCE for the whole snapshot instead of
	// shelling `tmux has-session` per polecat. At high session count the
	// per-session serial probe would dominate; a single `tmux list-sessions` is
	// ~4ms and the per-polecat liveness check below is an in-memory set lookup.
	liveSessions := liveSessionSet(tmux.NewTmux())

	// Phase 1 (serial, filesystem-only): gather the per-rig work items. Cheap —
	// os.Stat + readdir, no bd subprocess.
	type rigCapacityWork struct {
		rigName         string
		rigPath         string
		prefix          string
		polecatNames    []string
		agents          map[string]*beads.Issue // filled by the parallel phase
		hookedAssignees map[string]bool         // assignees of this rig's status=hooked work beads
		readOK          bool                    // true once ListAgentBeads succeeded
	}
	var work []*rigCapacityWork
	for rigName := range rigsConfig.Rigs {
		rigPath := filepath.Join(townRoot, rigName)
		if _, err := os.Stat(rigPath); err != nil {
			continue
		}
		polecatNames, err := listPolecatDirectoryNames(rigPath)
		if err != nil {
			return snapshot, nil, fmt.Errorf("listing polecat dirs for %s capacity: %w", rigName, err)
		}
		if len(polecatNames) == 0 {
			continue
		}
		work = append(work, &rigCapacityWork{
			rigName:      rigName,
			rigPath:      rigPath,
			prefix:       beads.GetPrefixForRig(townRoot, rigName),
			polecatNames: polecatNames,
		})
	}

	// Phase 2 (bounded-parallel): fetch each rig's agent beads concurrently. The
	// serial per-rig `bd list --label=gt:agent` fan-out (one ~0.85s cold-start
	// per rig) was the dominant scan cost — 16 rigs ≈ 14s (gu-el5bx). Each rig is
	// a separate Dolt DB so the reads cannot collapse into one query; we instead
	// run them concurrently behind a SEMAPHORE so even under contention they
	// cannot storm the single shared Dolt server. We keep WithoutReadThrottle
	// (gu-pug66 deliberately made this critical path lock-free; restoring the
	// throttle under scheduler-dispatch.lock re-opens that dispatch-starvation
	// deadlock) — the semaphore, not the throttle, bounds Dolt load here.
	//
	// In the SAME phase we also fetch the rig's status=hooked work beads (one
	// extra list per rig) so the working classification can read the canonical
	// work-bead signal instead of the deprecated agent-bead hook_bead field
	// (gu-c94lq / hq-l6mm5). The added cost is one query per rig per snapshot,
	// absorbed by the 5s capacity-snapshot cache — negligible next to the
	// agent-bead read already happening here.
	//
	// Per-rig read errors degrade gracefully: that rig contributes no capacity
	// rather than failing the whole snapshot (a transient Dolt blip on one rig
	// must not stall town-wide dispatch).
	sem := make(chan struct{}, capacityFanoutConcurrency())
	var wg sync.WaitGroup
	for _, w := range work {
		wg.Add(1)
		go func(w *rigCapacityWork) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rigBeads := beads.New(w.rigPath).WithoutReadThrottle()
			agents, err := rigBeads.ListAgentBeads()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s capacity_skip reason=agent_list_failed rig=%s: %v\n",
					style.Dim.Render("○"), w.rigName, err)
				return // graceful degrade: readOK stays false
			}
			hookedBeads, err := rigBeads.List(beads.ListOptions{
				Status:   beads.StatusHooked,
				Priority: -1,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s capacity_skip reason=hooked_list_failed rig=%s: %v\n",
					style.Dim.Render("○"), w.rigName, err)
				return // graceful degrade: readOK stays false
			}
			hookedAssignees := make(map[string]bool, len(hookedBeads))
			for _, b := range hookedBeads {
				if b != nil && b.Assignee != "" {
					hookedAssignees[b.Assignee] = true
				}
			}
			w.agents = agents
			w.hookedAssignees = hookedAssignees
			w.readOK = true
		}(w)
	}
	wg.Wait()

	// Phase 3 (serial): fold each rig's results into the shared snapshot
	// single-threaded, preserving deterministic counting without locking. Each
	// rig also folds into its OWN sub-snapshot so the per-rig cap reads the
	// identical occupancy unit the town cap reads — a dead-session-hooked
	// polecat classified RecoveryBlocked occupies a per-rig slot exactly as it
	// occupies a town slot (gu-ccycc).
	perRigOccupied := make(map[string]int)
	for _, w := range work {
		// A rig whose agent-bead read failed contributes NO capacity rather than
		// being miscounted: with a nil agents map every polecat would parse as a
		// fields==nil slot and inflate ReusableIdle/Working from stale guesses.
		// Skipping leaves that rig out of this snapshot entirely; the next scan
		// (or warm-cache expiry) re-reads it. Because the per-rig occupancy is
		// folded from the SAME scan, a skipped rig reports 0 to BOTH the town
		// and per-rig gates — they stay aligned (the inverse of the gu-y9epu
		// divergence) rather than one over- and one under-counting.
		if !w.readOK {
			continue
		}
		rigSnapshot := polecatCapacitySnapshot{}
		for _, name := range w.polecatNames {
			agentID := beads.PolecatBeadIDWithPrefix(w.prefix, w.rigName, name)
			issue := w.agents[agentID] // nil-safe: nil map lookup yields nil
			fields := (*beads.AgentFields)(nil)
			if issue != nil {
				fields = beads.ParseAgentFields(issue.Description)
				fields.AgentState = beads.ResolveAgentState(issue.Description, issue.AgentState)
			}
			// Canonical "working" signal: a status=hooked work bead assigned to
			// this polecat (gu-c94lq). Feed the SAME value into both the town and
			// per-rig sub-snapshots so they cannot drift (gu-ccycc invariant).
			assignee := fmt.Sprintf("%s/polecats/%s", w.rigName, name)
			hasHookedWorkBead := w.hookedAssignees[assignee]
			applyAgentFieldsToCapacitySnapshot(&snapshot, w.rigName, name, fields, liveSessions, hasHookedWorkBead)
			applyAgentFieldsToCapacitySnapshot(&rigSnapshot, w.rigName, name, fields, liveSessions, hasHookedWorkBead)
		}
		perRigOccupied[w.rigName] = rigSnapshot.occupied()
	}

	reservations, err := readPolecatAdmissionReservations(townRoot)
	if err != nil {
		return snapshot, nil, err
	}
	snapshot.Reservations = len(reservations)
	// Attribute reservations to their target rig so the per-rig occupied count
	// includes in-flight admissions (mirrors the town occupied() including
	// snapshot.Reservations). A reservation with no rig recorded only counts
	// town-wide.
	for _, res := range reservations {
		if res.Rig != "" {
			perRigOccupied[res.Rig]++
		}
	}
	if max > 0 {
		snapshot.Free = max - snapshot.occupied()
		if snapshot.Free < 0 {
			snapshot.Free = 0
		}
	}
	return snapshot, perRigOccupied, nil
}

func listPolecatDirectoryNames(rigPath string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(rigPath, "polecats"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// capacityFanoutConcurrency bounds how many per-rig agent-bead reads run
// concurrently in the capacity snapshot (gu-el5bx). These reads bypass the
// bd-list-read throttle (gu-pug66's lock-free critical path), so the semaphore
// is what keeps them from storming the single shared Dolt server. Default 4 —
// enough for the ~4× speedup over serial while adding at most 4 concurrent Dolt
// connections (Dolt sits at ~19/100, ample headroom). Tunable via
// GT_CAPACITY_FANOUT for operators who want more parallelism on big towns.
func capacityFanoutConcurrency() int {
	const def = 4
	if v := os.Getenv("GT_CAPACITY_FANOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	return def
}

// liveSessionSet enumerates all live tmux sessions in a single `tmux
// list-sessions` and returns them as a membership set. This replaces the
// per-polecat `tmux has-session` fan-out in the capacity snapshot (gu-el5bx).
// On enumeration error (e.g. no tmux server) it returns an empty set, which
// makes every per-polecat lookup report "not running" — identical to the prior
// behavior when HasSession errored.
func liveSessionSet(tmuxClient *tmux.Tmux) map[string]bool {
	set := make(map[string]bool)
	if tmuxClient == nil {
		return set
	}
	sessions, err := tmuxClient.ListSessions()
	if err != nil {
		return set
	}
	for _, name := range sessions {
		set[name] = true
	}
	return set
}

// applyAgentFieldsToCapacitySnapshot classifies one polecat into the capacity
// snapshot. The "is this polecat working" decision reads hasHookedWorkBead —
// whether a work bead with status=hooked is assigned to this polecat — NOT the
// agent-bead hook_bead field. hq-l6mm5 made the work bead canonical and turned
// hook_bead writes into a no-op; a polecat that reuses an idle slot is hooked
// via the work bead but never gets hook_bead set, so reading hook_bead here
// under-counted reuse polecats (gu-c94lq). The remaining branches still read
// agent-bead fields that hq-l6mm5 left maintained (ActiveMR, PushFailed/
// MRFailed, CleanupStatus, nuked).
func applyAgentFieldsToCapacitySnapshot(snapshot *polecatCapacitySnapshot, rigName, polecatName string, fields *beads.AgentFields, liveSessions map[string]bool, hasHookedWorkBead bool) {
	running := liveSessions[session.PolecatSessionName(session.PrefixFor(rigName), polecatName)]
	if fields == nil {
		// No agent bead exists for this polecat directory. Two distinct cases:
		//
		//   running: a session is alive but its bead is missing — anomalous
		//   working state, count as working so the slot reflects the live
		//   process and operators can investigate the missing bead.
		//
		//   !running: the polecat directory exists with no bead and no session.
		//   This is a fresh / orphan warm-pool slot — exactly what
		//   isReclaimCandidate treats as a "reusable warm-pool slot — don't
		//   churn it" (internal/daemon/daemon.go isStalledReclaimCandidate).
		//   Counting it as RecoveryBlocked falsely inflates the recovery count
		//   and starves dispatch — observed in production at 34/50 slots
		//   reporting recovery while only 4 polecats were actually
		//   recovery-needed (gu-o086). Align with reclaim's view: classify as
		//   ReusableIdle so the warm-pool benefit is preserved AND the
		//   capacity counter is honest.
		if running {
			snapshot.Working++
		} else {
			snapshot.ReusableIdle++
		}
		return
	}

	state := strings.TrimSpace(fields.AgentState)
	if hasHookedWorkBead {
		if running {
			snapshot.Working++
		} else {
			snapshot.RecoveryBlocked++
		}
		return
	}
	if fields.PushFailed || fields.MRFailed {
		snapshot.RecoveryBlocked++
		return
	}
	if fields.ActiveMR != "" {
		snapshot.PendingMR++
		return
	}
	if fields.CleanupStatus == "clean" || state == "nuked" {
		snapshot.ReusableIdle++
		return
	}
	snapshot.RecoveryBlocked++
}

func acquirePolecatAdmissionLock(townRoot string) (*flock.Flock, error) {
	lockDir := filepath.Join(townRoot, ".runtime", "locks")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return nil, fmt.Errorf("creating polecat admission lock dir: %w", err)
	}
	lock := flock.New(filepath.Join(lockDir, "polecat-admission.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("acquiring polecat admission lock: %w", err)
	}
	if !locked {
		return nil, fmt.Errorf("polecat admission is busy; retry shortly")
	}
	return lock, nil
}

func polecatAdmissionDir(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "polecat-admission")
}

func writePolecatAdmissionReservation(townRoot, rigName, beadID, operation string) (polecatAdmissionReservation, string, error) {
	dir := polecatAdmissionDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return polecatAdmissionReservation{}, "", fmt.Errorf("creating polecat admission dir: %w", err)
	}
	now := time.Now().UTC()
	id := fmt.Sprintf("%d-%d", os.Getpid(), now.UnixNano())
	reservation := polecatAdmissionReservation{
		ID:        id,
		PID:       os.Getpid(),
		Rig:       rigName,
		Bead:      beadID,
		Operation: operation,
		CreatedAt: now,
	}
	path := filepath.Join(dir, id+".json")
	tmpPath := path + ".tmp"
	data, err := json.MarshalIndent(reservation, "", "  ")
	if err != nil {
		return polecatAdmissionReservation{}, "", err
	}
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return polecatAdmissionReservation{}, "", fmt.Errorf("writing polecat admission reservation: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return polecatAdmissionReservation{}, "", fmt.Errorf("publishing polecat admission reservation: %w", err)
	}
	return reservation, path, nil
}

func readPolecatAdmissionReservations(townRoot string) ([]polecatAdmissionReservation, error) {
	dir := polecatAdmissionDir(townRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading polecat admission reservations: %w", err)
	}
	reservations := make([]polecatAdmissionReservation, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			_ = os.Remove(path)
			continue
		}
		var reservation polecatAdmissionReservation
		if err := json.Unmarshal(data, &reservation); err != nil {
			_ = os.Remove(path)
			continue
		}
		if reservation.ID == "" || reservation.PID <= 0 || reservation.CreatedAt.IsZero() || reservation.ID+".json" != entry.Name() {
			_ = os.Remove(path)
			continue
		}
		reservations = append(reservations, reservation)
	}
	return reservations, nil
}

func cleanupStalePolecatAdmissionReservations(townRoot string) error {
	dir := polecatAdmissionDir(townRoot)
	reservations, err := readPolecatAdmissionReservations(townRoot)
	if err != nil {
		return err
	}
	for _, reservation := range reservations {
		if reservation.PID <= 0 {
			continue
		}
		// A reservation is held only for the acquire→spawn window via the
		// dispatcher's deferred Release(); the file persists past that window
		// only when the owning process died ungracefully (crash/kill) before
		// the defer ran. Once the owner PID is dead the reservation can never
		// be released by its owner, so it must be reaped immediately — holding
		// it for an age grace period leaks admission capacity for up to that
		// grace window per dead dispatcher, which accumulated town-wide and
		// eroded usable capacity across rigs (gu-3jizl). processAlive is
		// conservative (a process we lack permission to signal counts as
		// alive), so an immediate dead-PID reap cannot remove a live owner's
		// reservation. This matches the doctor's OrphanedAdmissionRecordsCheck,
		// which already treats any dead-PID record as orphaned with no age gate.
		if processAlive(reservation.PID) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, reservation.ID+".json"))
	}
	return nil
}

func cleanupStalePolecatAdmissionReservationsWithLock(townRoot string) error {
	lock, err := acquirePolecatAdmissionLock(townRoot)
	if err != nil {
		if strings.Contains(err.Error(), "admission is busy") {
			return nil
		}
		return err
	}
	defer func() { _ = lock.Unlock() }()
	return cleanupStalePolecatAdmissionReservations(townRoot)
}

// processAlive is defined in platform-specific files:
// process_alive_unix.go and process_alive_windows.go

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
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
)

const polecatAdmissionReservationTTL = 30 * time.Minute

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
	err      error
	at       time.Time
}

// invalidatePolecatCapacityCache drops the cached snapshot for townRoot.
// Used by tests and by code paths that explicitly need a fresh read after
// a known state change (e.g., right after recording a dispatch).
func invalidatePolecatCapacityCache(townRoot string) {
	polecatCapacityCacheMu.Lock()
	defer polecatCapacityCacheMu.Unlock()
	delete(polecatCapacityCache, townRoot)
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
	if max <= 0 {
		return &polecatAdmissionHandle{disabled: true}, polecatCapacitySnapshot{Max: max, ActiveSessions: countActivePolecats()}, nil
	}

	lock, err := acquirePolecatAdmissionLock(townRoot)
	if err != nil {
		return nil, polecatCapacitySnapshot{}, err
	}
	defer func() { _ = lock.Unlock() }()

	if err := cleanupStalePolecatAdmissionReservations(townRoot, time.Now()); err != nil {
		return nil, polecatCapacitySnapshot{}, err
	}

	snapshot, err := polecatCapacitySnapshotForTownNoCleanup(townRoot)
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
		if err := cleanupStalePolecatAdmissionReservationsWithLock(townRoot, time.Now()); err != nil {
			return polecatCapacitySnapshot{}, err
		}
	}
	snapshot, err := polecatCapacitySnapshotForTownNoCleanup(townRoot)
	storeCachedPolecatCapacitySnapshot(townRoot, snapshot, err)
	return snapshot, err
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
func storeCachedPolecatCapacitySnapshot(townRoot string, snapshot polecatCapacitySnapshot, err error) {
	polecatCapacityCacheMu.Lock()
	defer polecatCapacityCacheMu.Unlock()
	if polecatCapacityCache == nil {
		polecatCapacityCache = make(map[string]cachedCapacitySnapshot)
	}
	polecatCapacityCache[townRoot] = cachedCapacitySnapshot{
		snapshot: snapshot,
		err:      err,
		at:       time.Now(),
	}
}

func polecatCapacitySnapshotForTownNoCleanup(townRoot string) (polecatCapacitySnapshot, error) {
	max, err := configuredSchedulerMaxPolecats(townRoot)
	if err != nil {
		return polecatCapacitySnapshot{}, err
	}
	snapshot := polecatCapacitySnapshot{Max: max, ActiveSessions: countActivePolecats()}
	if max <= 0 {
		return snapshot, nil
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return snapshot, fmt.Errorf("loading rigs config for polecat capacity: %w", err)
	}

	// Enumerate live tmux sessions ONCE for the whole snapshot instead of
	// shelling `tmux has-session` per polecat. At high session count the
	// per-session serial probe would dominate; a single `tmux list-sessions` is
	// ~4ms and the per-polecat liveness check below is an in-memory set lookup.
	liveSessions := liveSessionSet(tmux.NewTmux())

	// Phase 1 (serial, filesystem-only): gather the per-rig work items. Cheap —
	// os.Stat + readdir, no bd subprocess.
	type rigCapacityWork struct {
		rigName      string
		rigPath      string
		prefix       string
		polecatNames []string
		agents       map[string]*beads.Issue // filled by the parallel phase
		readOK       bool                    // true once ListAgentBeads succeeded
	}
	var work []*rigCapacityWork
	for rigName := range rigsConfig.Rigs {
		rigPath := filepath.Join(townRoot, rigName)
		if _, err := os.Stat(rigPath); err != nil {
			continue
		}
		polecatNames, err := listPolecatDirectoryNames(rigPath)
		if err != nil {
			return snapshot, fmt.Errorf("listing polecat dirs for %s capacity: %w", rigName, err)
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
			agents, err := beads.New(w.rigPath).WithoutReadThrottle().ListAgentBeads()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s capacity_skip reason=agent_list_failed rig=%s: %v\n",
					style.Dim.Render("○"), w.rigName, err)
				return // graceful degrade: readOK stays false
			}
			w.agents = agents
			w.readOK = true
		}(w)
	}
	wg.Wait()

	// Phase 3 (serial): fold each rig's results into the shared snapshot
	// single-threaded, preserving deterministic counting without locking.
	for _, w := range work {
		// A rig whose agent-bead read failed contributes NO capacity rather than
		// being miscounted: with a nil agents map every polecat would parse as a
		// fields==nil slot and inflate ReusableIdle/Working from stale guesses.
		// Skipping leaves that rig out of this snapshot entirely; the next scan
		// (or warm-cache expiry) re-reads it.
		if !w.readOK {
			continue
		}
		for _, name := range w.polecatNames {
			agentID := beads.PolecatBeadIDWithPrefix(w.prefix, w.rigName, name)
			issue := w.agents[agentID] // nil-safe: nil map lookup yields nil
			fields := (*beads.AgentFields)(nil)
			if issue != nil {
				fields = beads.ParseAgentFields(issue.Description)
				fields.AgentState = beads.ResolveAgentState(issue.Description, issue.AgentState)
			}
			applyAgentFieldsToCapacitySnapshot(&snapshot, w.rigName, name, fields, liveSessions)
		}
	}

	reservations, err := readPolecatAdmissionReservations(townRoot)
	if err != nil {
		return snapshot, err
	}
	snapshot.Reservations = len(reservations)
	if max > 0 {
		snapshot.Free = max - snapshot.occupied()
		if snapshot.Free < 0 {
			snapshot.Free = 0
		}
	}
	return snapshot, nil
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

func applyAgentFieldsToCapacitySnapshot(snapshot *polecatCapacitySnapshot, rigName, polecatName string, fields *beads.AgentFields, liveSessions map[string]bool) {
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
	if fields.HookBead != "" || state == "working" || state == "spawning" {
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

func applyWorkstateDispositionToCapacitySnapshot(snapshot *polecatCapacitySnapshot, state polecat.State, disposition polecat.WorkstateDisposition) {
	if disposition.ReuseStatus == "idle-pr-open" {
		snapshot.PendingMR++
		return
	}
	if disposition.Reusable {
		snapshot.ReusableIdle++
		return
	}
	if !disposition.CountsTowardCapacity {
		return
	}
	if state == polecat.StateWorking || disposition.Verdict == polecat.WorkstateVerdictWorking {
		snapshot.Working++
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

func cleanupStalePolecatAdmissionReservations(townRoot string, now time.Time) error {
	dir := polecatAdmissionDir(townRoot)
	reservations, err := readPolecatAdmissionReservations(townRoot)
	if err != nil {
		return err
	}
	for _, reservation := range reservations {
		if reservation.PID <= 0 {
			continue
		}
		age := now.Sub(reservation.CreatedAt)
		if processAlive(reservation.PID) {
			continue
		}
		if age < polecatAdmissionReservationTTL {
			continue
		}
		_ = os.Remove(filepath.Join(dir, reservation.ID+".json"))
	}
	return nil
}

func cleanupStalePolecatAdmissionReservationsWithLock(townRoot string, now time.Time) error {
	lock, err := acquirePolecatAdmissionLock(townRoot)
	if err != nil {
		if strings.Contains(err.Error(), "admission is busy") {
			return nil
		}
		return err
	}
	defer func() { _ = lock.Unlock() }()
	return cleanupStalePolecatAdmissionReservations(townRoot, now)
}

// processAlive is defined in platform-specific files:
// process_alive_unix.go and process_alive_windows.go

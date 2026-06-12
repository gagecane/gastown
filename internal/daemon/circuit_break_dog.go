package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/events"
)

// circuit_break_dog watches the town-wide circuit-break log and ESCALATES when
// the SAME work bead is circuit-broken repeatedly within a window — closing the
// gap deferred from gu-n0hvf to gu-ixo67. The sibling scheduler_stuck_dog
// covers the non-draining-ready-queue signature; this dog covers the inverse
// failure shape: work that IS being dispatched, fails deterministically every
// time, gets circuit-broken, and is re-dispatched into a fresh sling-context
// that breaks again — a loop no single-context counter can see.
//
// WHY A SEPARATE DATA SOURCE: circuit-break state is per-sling-context
// (SlingContextFields.DispatchFailures) and is destroyed when the context bead
// closes. `gt scheduler status --json` has no town-wide aggregate or recent
// count of circuit-breaks. So the dispatch path appends each break to
// .runtime/scheduler-circuit-breaks.jsonl (events.LogCircuitBreak), and this
// dog aggregates that log.
//
// Motivating case (gu-r8b0q): an epic was re-dispatched every cycle, the
// dispatch deterministically failed (container/epic, bead-not-found), the
// context circuit-broke each run, and nothing aggregated the pattern — it
// burned a dispatch slot indefinitely and was only caught by a human.
//
// Design decision (gu-muj66 / gu-n0hvf precedent): escalate, do NOT
// auto-remediate. A deterministic dispatch failure usually needs a human/agent
// to fix the bead (mistyped container, missing dep, epic that should not be
// slung) or close it. Auto-closing risks discarding real work; auto-retrying
// is exactly the loop we are trying to break. The dog hands an agent the
// offending bead(s), the break count, and the last failure so it acts with
// judgment.
//
//	scheduler dispatch loop
//	      │  context fails maxDispatchFailures times → circuit-broken
//	      ▼  (LogCircuitBreak appended to .runtime log)
//	circuit_break_dog (this patrol)          ← detector (NEW, gu-ixo67)
//	      │  escalates a work bead broken >= threshold within the window
//	      ▼
//	agent diagnoses + acts (fix or close the bead — no auto-remediate)
const (
	defaultCircuitBreakInterval = 5 * time.Minute
	circuitBreakSource          = "circuit_break_dog"

	// circuitBreakWindow is how far back the dog aggregates circuit-breaks.
	// Records older than this are pruned from the log on each read, bounding
	// the file. A bead broken N times across this rolling window is the signal.
	circuitBreakWindow = time.Hour

	// circuitBreakThreshold is how many distinct circuit-breaks a single work
	// bead must accumulate within circuitBreakWindow before the dog escalates.
	// maxDispatchFailures (3) closes ONE context; a bead reaching this many
	// closed contexts is being re-dispatched into a doomed loop. 3 distinct
	// breaks ≈ 9 failed dispatch attempts — well past transient flake.
	circuitBreakThreshold = 3

	// dispatchFailuresPerBreak mirrors cmd.maxDispatchFailures (the number of
	// consecutive dispatch failures that close one context as circuit-broken).
	// Used only in the escalation message; duplicated rather than imported to
	// avoid a daemon→cmd dependency. Keep in sync with cmd.maxDispatchFailures.
	dispatchFailuresPerBreak = 3
)

// CircuitBreakConfig holds configuration for the circuit_break patrol.
type CircuitBreakConfig struct {
	// Enabled controls whether the circuit-break monitor runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "5m").
	IntervalStr string `json:"interval,omitempty"`
}

// circuitBreakInterval returns the configured interval, or the default (5m).
func circuitBreakInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.CircuitBreak != nil {
		if config.Patrols.CircuitBreak.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.CircuitBreak.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultCircuitBreakInterval
}

// circuitBreakStateFile is the persisted per-bead escalation marker. Lives
// under .runtime so it is wiped with other ephemeral state.
func circuitBreakStateFile(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "daemon", "circuit-break-monitor.json")
}

// circuitBreakState records, per work bead, the break count at which we last
// escalated. This lets the dog escalate once when a bead first crosses the
// threshold and again only if it keeps breaking (count grows), while not
// re-escalating an unchanged steady state every tick. Beads no longer present
// in the window are pruned so a future recurrence re-arms.
type circuitBreakState struct {
	// EscalatedAtCount maps work bead ID → the distinct-break count that was
	// in effect the last time we escalated for that bead.
	EscalatedAtCount map[string]int `json:"escalated_at_count"`
}

// brokenBead is an aggregated view of one work bead's circuit-breaks in window.
type brokenBead struct {
	WorkBeadID  string
	Count       int    // distinct contexts that broke for this bead
	TargetRig   string // most recent target rig seen
	LastFailure string // most recent failure message
	LastBreakTS string // most recent break timestamp (RFC3339)
}

// runCircuitBreakDog is the main patrol function. It reads the circuit-break
// log (pruning the window), aggregates distinct breaks per work bead, and
// escalates any bead at or above the threshold whose count has grown since the
// last escalation.
func (d *Daemon) runCircuitBreakDog() {
	if !d.isPatrolActive("circuit_break") {
		return
	}

	breaks, err := events.ReadCircuitBreaks(d.config.TownRoot, circuitBreakWindow)
	if err != nil {
		d.logger.Printf("circuit_break: failed to read circuit-break log: %v", err)
		return
	}

	aggregated := aggregateCircuitBreaks(breaks)

	stateFile := circuitBreakStateFile(d.config.TownRoot)
	state, loadErr := loadCircuitBreakState(stateFile)
	if loadErr != nil || state.EscalatedAtCount == nil {
		state = circuitBreakState{EscalatedAtCount: map[string]int{}}
	}

	toEscalate, newState, changed := circuitBreakEscalations(aggregated, state)

	for _, b := range toEscalate {
		d.logger.Printf("circuit_break: %s circuit-broken %d times in %s — escalating",
			b.WorkBeadID, b.Count, circuitBreakWindow)
		// d.escalate dedups on signature, but the signature is per-source —
		// include the bead ID so distinct beads escalate independently.
		if err := d.escalate(circuitBreakSource+":"+b.WorkBeadID, d.buildCircuitBreakMessage(b)); err != nil {
			d.logger.Printf("circuit_break: %s: escalation failed: %v", b.WorkBeadID, err)
		}
	}

	if changed {
		if err := saveCircuitBreakState(stateFile, newState); err != nil {
			d.logger.Printf("circuit_break: failed to persist state: %v", err)
		}
	}
}

// circuitBreakEscalations is the pure decision core: given the aggregated
// in-window breaks and the prior escalation state, it returns which beads to
// escalate, the updated state, and whether the state changed (and must be
// persisted). A bead escalates the first time it crosses the threshold, and
// again only if its distinct-break count has GROWN since the last escalation —
// so a steady wedge does not re-escalate every tick, but a worsening one does.
// State entries for beads no longer in the window are pruned so a future
// recurrence re-arms.
func circuitBreakEscalations(aggregated []brokenBead, state circuitBreakState) (toEscalate []brokenBead, newState circuitBreakState, changed bool) {
	prevLen := len(state.EscalatedAtCount)
	next := make(map[string]int, len(state.EscalatedAtCount))
	inWindow := make(map[string]bool, len(aggregated))
	for _, b := range aggregated {
		inWindow[b.WorkBeadID] = true
	}
	// Carry forward only the entries still in the window (prune the rest).
	for id, c := range state.EscalatedAtCount {
		if inWindow[id] {
			next[id] = c
		}
	}

	for _, b := range aggregated {
		if b.Count < circuitBreakThreshold {
			continue
		}
		if prev, ok := next[b.WorkBeadID]; ok && b.Count <= prev {
			continue // already escalated at this (or higher) count
		}
		toEscalate = append(toEscalate, b)
		next[b.WorkBeadID] = b.Count
	}

	// State changed if we escalated anything OR the prune dropped entries.
	changed = len(toEscalate) > 0 || len(next) != prevLen
	return toEscalate, circuitBreakState{EscalatedAtCount: next}, changed
}

// aggregateCircuitBreaks groups records by work bead, counting DISTINCT
// sling-contexts (a bead re-dispatched into N contexts that each broke = N) so
// the two producer sites cannot double-count a single context. Returns a stable
// (count desc, then ID) ordering for deterministic escalation/logging.
func aggregateCircuitBreaks(breaks []events.CircuitBreakRecord) []brokenBead {
	type agg struct {
		contexts    map[string]bool
		targetRig   string
		lastFailure string
		lastTS      string
	}
	byBead := map[string]*agg{}
	for _, rec := range breaks {
		if rec.WorkBeadID == "" {
			continue
		}
		a := byBead[rec.WorkBeadID]
		if a == nil {
			a = &agg{contexts: map[string]bool{}}
			byBead[rec.WorkBeadID] = a
		}
		// Dedup by context ID. Empty context ID still counts once (defensive).
		key := rec.ContextID
		if key == "" {
			key = "ts:" + rec.Timestamp
		}
		a.contexts[key] = true
		// Track the most recent metadata by timestamp.
		if rec.Timestamp >= a.lastTS {
			a.lastTS = rec.Timestamp
			if rec.TargetRig != "" {
				a.targetRig = rec.TargetRig
			}
			if rec.LastFailure != "" {
				a.lastFailure = rec.LastFailure
			}
		}
	}

	out := make([]brokenBead, 0, len(byBead))
	for id, a := range byBead {
		out = append(out, brokenBead{
			WorkBeadID:  id,
			Count:       len(a.contexts),
			TargetRig:   a.targetRig,
			LastFailure: a.lastFailure,
			LastBreakTS: a.lastTS,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].WorkBeadID < out[j].WorkBeadID
	})
	return out
}

// buildCircuitBreakMessage assembles the escalation body: which bead keeps
// breaking, how many times, where, the last failure, and concrete next steps.
func (d *Daemon) buildCircuitBreakMessage(b brokenBead) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Bead %s circuit-broken %d times in %s — agent diagnosis required (NOT auto-remediated).\n\n",
		b.WorkBeadID, b.Count, circuitBreakWindow)
	fmt.Fprintf(&sb, "The scheduler keeps re-dispatching this bead into fresh sling-contexts that\n")
	fmt.Fprintf(&sb, "each fail %d times and circuit-break. This is the deterministic-failure loop\n", dispatchFailuresPerBreak)
	fmt.Fprintf(&sb, "deferred from gu-n0hvf (gu-ixo67) — a bead that will never dispatch but is\n")
	fmt.Fprintf(&sb, "retried forever, burning a dispatch slot every cycle.\n\n")
	fmt.Fprintf(&sb, "Details:\n")
	fmt.Fprintf(&sb, "  work_bead: %s\n", b.WorkBeadID)
	fmt.Fprintf(&sb, "  distinct circuit-breaks in window: %d (threshold %d)\n", b.Count, circuitBreakThreshold)
	if b.TargetRig != "" {
		fmt.Fprintf(&sb, "  target_rig: %s\n", b.TargetRig)
	}
	if b.LastBreakTS != "" {
		fmt.Fprintf(&sb, "  last_break: %s\n", b.LastBreakTS)
	}
	if b.LastFailure != "" {
		fmt.Fprintf(&sb, "  last_failure: %s\n", b.LastFailure)
	}
	fmt.Fprintf(&sb, "\nRECOMMENDED ACTION (diagnose, then act with judgment — do NOT blind-retry):\n")
	fmt.Fprintf(&sb, "  1. bd show %s — inspect the bead. Is it an epic/container that should\n", b.WorkBeadID)
	fmt.Fprintf(&sb, "     never be slung, or does it reference a missing dep/bead?\n")
	fmt.Fprintf(&sb, "  2. Read last_failure above — a deterministic error (bead-not-found,\n")
	fmt.Fprintf(&sb, "     container/epic) means re-dispatch will never succeed.\n")
	fmt.Fprintf(&sb, "  3. Fix the bead (correct the target/deps) OR close it if it should not\n")
	fmt.Fprintf(&sb, "     be dispatched. gt bead reset clears a circuit-broken dispatch.\n")
	fmt.Fprintf(&sb, "  4. If the failure looks like infra (not the bead), check the target rig's\n")
	fmt.Fprintf(&sb, "     capacity/health before re-enabling dispatch.\n")
	return sb.String()
}

func loadCircuitBreakState(path string) (circuitBreakState, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path derived from trusted townRoot
	if err != nil {
		return circuitBreakState{}, err
	}
	var s circuitBreakState
	if err := json.Unmarshal(data, &s); err != nil {
		return circuitBreakState{}, err
	}
	return s, nil
}

func saveCircuitBreakState(path string, state circuitBreakState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating daemon state dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling circuit-break state: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}

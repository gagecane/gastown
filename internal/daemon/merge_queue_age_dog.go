package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/rig"
)

// merge_queue_age_dog watches each rig's merge queue and ESCALATES when MRs
// age without landing or the head of the queue stops advancing — closing the
// gap from gu-78bbg where gastown_upstream's queue silently grew to 15 pending
// MRs (several aging >1h) while the refinery was parked on a red-main deadlock.
// Nothing watched the QUEUE itself: the backlog was only caught by a manual
// mayor health sweep. A growing/stalling merge queue is a leading indicator of
// a refinery problem and should self-announce.
//
// Two independent signatures fire an escalation (the gu-78bbg acceptance is an
// OR of these):
//
//   - OLDEST AGING: the oldest open, unblocked MR in a rig's queue has aged
//     past mergeQueueOldestAgeThreshold without landing. In a healthy draining
//     queue the oldest MR lands within a cycle or two; one surviving past the
//     threshold means the queue is not clearing.
//
//   - HEAD FROZEN: the same MR has sat at the head of the queue (highest
//     refinery score — the one the refinery processes next) for at least
//     mergeQueueHeadFrozenThreshold while a backlog waits behind it. The head
//     CHANGING across cycles is exactly the signal that the refinery is making
//     progress; a head frozen for sustained time with depth behind it is a
//     parked/stuck refinery.
//
// BUSY vs STUCK (the gu-eke9u discrete-cycle lesson): a legitimately busy
// queue — head advancing, just deep — must NOT escalate. The head-frozen check
// keys on the head MR ID staying identical across cycles, so a deep queue whose
// head keeps turning over never trips it; only a head that stops moving does.
// The oldest-aging check is bounded the same way: a draining queue never holds
// an MR past the threshold, so only a non-clearing queue fires it. Requiring
// depth >= mergeQueueHeadFrozenMinDepth behind a frozen head avoids escalating
// a single slow lone MR (which the oldest-aging check already covers if it is
// genuinely old).
//
// Design decision (gu-muj66 / gu-n0hvf / gu-ixo67 precedent): escalate, do NOT
// auto-remediate. A parked refinery usually needs an agent to unstick main
// health, retry a blocked MR, or clear a deadlock — auto-poking the refinery
// from the daemon risks the restart-loop class of failure. The dog hands the
// deacon (via gt escalate → mayor) the queue snapshot so an agent acts with
// judgment.
//
//	refinery parked / red-main deadlock                    ← failure mode
//	      │  MRs accumulate, head stops advancing
//	      ▼
//	merge_queue_age_dog (this patrol)                      ← detector (NEW, gu-78bbg)
//	      │  escalates oldest-aging / head-frozen per rig, deduped per condition
//	      ▼
//	deacon routes to mayor → agent diagnoses + acts (no auto-remediate)
//
// Pairs with gu-a522t (refinery self-escalation): this dog is the external
// observer for the case where the refinery cannot escalate for itself.
const (
	defaultMergeQueueAgeInterval = 5 * time.Minute
	mergeQueueAgeSource          = "merge_queue_age_dog"

	// mergeQueueOldestAgeThreshold is how old the oldest open, unblocked MR may
	// get before the dog escalates the "oldest aging" signature. gu-78bbg's
	// motivating incident had MRs aging >1h while the refinery was parked, and
	// the bead specifies HIGH > 60m for oldest age, so 60m is the trigger.
	mergeQueueOldestAgeThreshold = 60 * time.Minute

	// mergeQueueHeadFrozenThreshold is how long the same MR may sit at the head
	// of the queue (highest refinery score) before the dog treats the head as
	// not advancing. The refinery processes the head roughly every cycle, so a
	// head unchanged for ~6 monitor cycles (at the 5m default) is well past any
	// single slow merge and indicates the refinery is not progressing.
	mergeQueueHeadFrozenThreshold = 30 * time.Minute

	// mergeQueueHeadFrozenMinDepth is the minimum queue depth (including the
	// head) required to fire the head-frozen signature. A lone MR that is simply
	// slow to land is covered by the oldest-aging check; the head-frozen check
	// targets the "depth growing behind a stuck head" shape, so it needs a
	// backlog behind the frozen head.
	mergeQueueHeadFrozenMinDepth = 2
)

// MergeQueueAgeConfig holds configuration for the merge_queue_age patrol.
type MergeQueueAgeConfig struct {
	// Enabled controls whether the merge-queue-age monitor runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "5m").
	IntervalStr string `json:"interval,omitempty"`
}

// mergeQueueAgeInterval returns the configured interval, or the default (5m).
func mergeQueueAgeInterval(cfg *DaemonPatrolConfig) time.Duration {
	if cfg != nil && cfg.Patrols != nil && cfg.Patrols.MergeQueueAge != nil {
		if cfg.Patrols.MergeQueueAge.IntervalStr != "" {
			if d, err := time.ParseDuration(cfg.Patrols.MergeQueueAge.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultMergeQueueAgeInterval
}

// mergeQueueAgeStateFile is the persisted per-rig head/escalation marker. Lives
// under .runtime so it is wiped with other ephemeral state.
func mergeQueueAgeStateFile(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "daemon", "merge-queue-age.json")
}

// mergeQueueAgeState persists per-rig head tracking and per-condition
// escalation markers across ticks, so a head's age accumulates across cycles
// and each condition escalates at most once per episode (re-arming when the
// queue drains or the head/oldest changes).
type mergeQueueAgeState struct {
	Rigs map[string]*mergeQueueRigState `json:"rigs"`
}

// mergeQueueRigState is one rig's tracked queue state.
type mergeQueueRigState struct {
	// HeadMRID is the MR that was at the head (highest score) last cycle.
	HeadMRID string `json:"head_mr_id,omitempty"`
	// HeadSince is when the current head first reached the head position. The
	// head's "frozen age" is now - HeadSince.
	HeadSince time.Time `json:"head_since,omitempty"`
	// HeadEscalated records whether the head-frozen signature has already been
	// escalated for the current head. Reset when the head changes.
	HeadEscalated bool `json:"head_escalated,omitempty"`
	// OldestEscalatedID is the MR ID for which the oldest-aging signature was
	// last escalated. Lets a still-aging same-MR not re-escalate every tick
	// while a new oldest (after the prior one lands) re-arms.
	OldestEscalatedID string `json:"oldest_escalated_id,omitempty"`
}

// mqEntry is the minimal per-MR view the decision core reasons about.
type mqEntry struct {
	ID        string
	CreatedAt time.Time
	Score     float64
}

// mqEscalation is one decided escalation for a rig.
type mqEscalation struct {
	Rig       string
	Condition string // "oldest" | "head"
	Message   string
}

// runMergeQueueAgeDog is the main patrol function. It enumerates every
// registered rig, reads each rig's open/unblocked merge queue, and escalates
// the oldest-aging and/or head-frozen signatures (deduped per rig per
// condition). Empty queues clear the rig's state so the checks auto-recover.
func (d *Daemon) runMergeQueueAgeDog() {
	if !d.isPatrolActive("merge_queue_age") {
		return
	}

	// Gate on the shared Dolt circuit breaker: listing each rig's MRs touches
	// its beads DB (Dolt). When Dolt is degraded, skip and resume next tick —
	// queue age only grows, so the next cycle re-evaluates.
	if d.doltBreaker != nil && !d.doltBreaker.Allow() {
		d.logger.Printf("merge_queue_age: dolt-degraded — skipping tick (circuit breaker open)")
		return
	}

	rigsConfig, err := d.loadRigsConfig()
	if d.doltBreaker != nil {
		d.doltBreaker.Record(err)
	}
	if err != nil {
		d.logger.Printf("merge_queue_age: failed to load rigs config: %v", err)
		return
	}
	if rigsConfig == nil || len(rigsConfig.Rigs) == 0 {
		return
	}

	stateFile := mergeQueueAgeStateFile(d.config.TownRoot)
	state, loadErr := loadMergeQueueAgeState(stateFile)
	if loadErr != nil || state.Rigs == nil {
		state = mergeQueueAgeState{Rigs: map[string]*mergeQueueRigState{}}
	}

	now := time.Now().UTC()
	resolve := d.buildMergeQueueListers(rigsConfig)

	// Prune state for rigs no longer registered so removed rigs do not leak.
	for rigName := range state.Rigs {
		if _, ok := rigsConfig.Rigs[rigName]; !ok {
			delete(state.Rigs, rigName)
		}
	}

	for rigName := range rigsConfig.Rigs {
		lister, ok := resolve[rigName]
		if !ok {
			continue
		}
		entries, err := readRigMergeQueue(lister, rigName, now)
		if err != nil {
			d.logger.Printf("merge_queue_age: %s: failed to list merge queue: %v", rigName, err)
			continue
		}

		prev := state.Rigs[rigName]
		escalations, newRigState := evaluateMergeQueueRig(
			rigName, entries, now, prev,
			mergeQueueOldestAgeThreshold, mergeQueueHeadFrozenThreshold, mergeQueueHeadFrozenMinDepth,
		)

		if newRigState == nil {
			delete(state.Rigs, rigName)
		} else {
			state.Rigs[rigName] = newRigState
		}

		for _, e := range escalations {
			d.logger.Printf("merge_queue_age: %s: %s signature — escalating", e.Rig, e.Condition)
			// d.escalate dedups on signature; include rig + condition so each
			// rig and each condition escalate independently.
			if err := d.escalate(mergeQueueAgeSource+":"+e.Rig+":"+e.Condition, e.Message); err != nil {
				d.logger.Printf("merge_queue_age: %s: %s escalation failed, will retry next tick: %v",
					e.Rig, e.Condition, err)
				// Roll back the just-set escalation marker so the next tick
				// retries instead of burying the condition.
				if newRigState != nil {
					switch e.Condition {
					case "head":
						newRigState.HeadEscalated = false
					case "oldest":
						newRigState.OldestEscalatedID = ""
					}
				}
			}
		}
	}

	if err := saveMergeQueueAgeState(stateFile, state); err != nil {
		d.logger.Printf("merge_queue_age: failed to persist state: %v", err)
	}
}

// evaluateMergeQueueRig is the pure decision core: given a rig's current queue
// entries, the prior persisted state, and the thresholds, it returns the
// escalations to fire and the new persisted state for the rig (nil = clear the
// rig's state, used when the queue is empty so the checks auto-recover).
//
// The head is the highest-scored MR (the one the refinery processes next).
// Head-frozen age accumulates from when that MR first reached the head; it
// resets whenever the head MR ID changes — so an advancing (busy) queue never
// trips the head-frozen signature regardless of depth.
func evaluateMergeQueueRig(
	rigName string,
	entries []mqEntry,
	now time.Time,
	prev *mergeQueueRigState,
	oldestThreshold, headFrozenThreshold time.Duration,
	headFrozenMinDepth int,
) (escalations []mqEscalation, newState *mergeQueueRigState) {
	if len(entries) == 0 {
		// Empty queue — healthy. Clear the rig's state so a future backlog
		// starts fresh and any prior escalation re-arms.
		return nil, nil
	}

	// Head = highest score (refinery processes highest score first). Oldest =
	// earliest CreatedAt. Sort a copy by score desc to find the head, then scan
	// for the oldest independently.
	sorted := make([]mqEntry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Score > sorted[j].Score
	})
	head := sorted[0]

	oldest := entries[0]
	for _, e := range entries[1:] {
		if e.CreatedAt.Before(oldest.CreatedAt) {
			oldest = e
		}
	}

	state := &mergeQueueRigState{}
	if prev != nil {
		*state = *prev
	}

	// --- Head tracking ---
	if state.HeadMRID != head.ID {
		// Head advanced (or first observation): reset the frozen timer and
		// re-arm the head-frozen escalation.
		state.HeadMRID = head.ID
		state.HeadSince = now
		state.HeadEscalated = false
	}
	headFrozenAge := now.Sub(state.HeadSince)

	// --- Head-frozen signature ---
	if !state.HeadEscalated &&
		len(entries) >= headFrozenMinDepth &&
		headFrozenAge >= headFrozenThreshold {
		escalations = append(escalations, mqEscalation{
			Rig:       rigName,
			Condition: "head",
			Message: buildMergeQueueHeadFrozenMessage(
				rigName, head, len(entries), headFrozenAge, oldest, now,
			),
		})
		state.HeadEscalated = true
	}

	// --- Oldest-aging signature ---
	oldestAge := now.Sub(oldest.CreatedAt)
	if oldestAge >= oldestThreshold {
		if state.OldestEscalatedID != oldest.ID {
			escalations = append(escalations, mqEscalation{
				Rig:       rigName,
				Condition: "oldest",
				Message: buildMergeQueueOldestMessage(
					rigName, oldest, len(entries), oldestAge,
				),
			})
			state.OldestEscalatedID = oldest.ID
		}
	} else if state.OldestEscalatedID != "" {
		// Oldest dropped back under threshold (the aged MR landed and the new
		// oldest is young) — re-arm so a future ageout escalates again.
		state.OldestEscalatedID = ""
	}

	return escalations, state
}

// buildMergeQueueHeadFrozenMessage assembles the head-frozen escalation body.
func buildMergeQueueHeadFrozenMessage(
	rigName string, head mqEntry, depth int, frozenAge time.Duration, oldest mqEntry, now time.Time,
) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Merge queue head not advancing in rig %q — refinery may be parked/stuck (agent diagnosis required, NOT auto-remediated).\n\n", rigName)
	fmt.Fprintf(&sb, "The head of the merge queue (%s) has not advanced for %s while %d MR(s) wait behind it.\n",
		head.ID, frozenAge.Round(time.Second), depth)
	fmt.Fprintf(&sb, "A head that keeps turning over means the refinery is landing work; a frozen head with a backlog means it is not.\n")
	fmt.Fprintf(&sb, "\nQueue snapshot:\n")
	fmt.Fprintf(&sb, "  rig:           %s\n", rigName)
	fmt.Fprintf(&sb, "  depth:         %d open, unblocked MR(s)\n", depth)
	fmt.Fprintf(&sb, "  head MR:       %s (score-ranked #1, frozen %s)\n", head.ID, frozenAge.Round(time.Second))
	fmt.Fprintf(&sb, "  oldest MR:     %s (age %s)\n", oldest.ID, now.Sub(oldest.CreatedAt).Round(time.Second))
	fmt.Fprintf(&sb, "\nRECOMMENDED ACTION (diagnose, then act with judgment — do NOT blind-restart):\n")
	fmt.Fprintf(&sb, "  1. gt mq list %s --json — confirm the queue depth and head.\n", rigName)
	fmt.Fprintf(&sb, "  2. Check the refinery for this rig: is it parked, on a red-main deadlock,\n")
	fmt.Fprintf(&sb, "     or stuck on the head MR's gates? (gu-87thl-class deadlock).\n")
	fmt.Fprintf(&sb, "  3. gt mq status %s — inspect the head MR for a blocked/failing gate.\n", head.ID)
	return sb.String()
}

// buildMergeQueueOldestMessage assembles the oldest-aging escalation body.
func buildMergeQueueOldestMessage(
	rigName string, oldest mqEntry, depth int, oldestAge time.Duration,
) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Merge queue MR aging past threshold in rig %q — queue not clearing (agent diagnosis required, NOT auto-remediated).\n\n", rigName)
	fmt.Fprintf(&sb, "The oldest open, unblocked MR (%s) has been in the queue %s without landing.\n",
		oldest.ID, oldestAge.Round(time.Second))
	fmt.Fprintf(&sb, "A healthy draining queue lands its oldest MR within a cycle or two; one surviving past %s means the queue is not clearing.\n",
		mergeQueueOldestAgeThreshold)
	fmt.Fprintf(&sb, "\nQueue snapshot:\n")
	fmt.Fprintf(&sb, "  rig:        %s\n", rigName)
	fmt.Fprintf(&sb, "  depth:      %d open, unblocked MR(s)\n", depth)
	fmt.Fprintf(&sb, "  oldest MR:  %s (age %s, created %s)\n",
		oldest.ID, oldestAge.Round(time.Second), oldest.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(&sb, "\nRECOMMENDED ACTION (diagnose, then act with judgment — do NOT blind-restart):\n")
	fmt.Fprintf(&sb, "  1. gt mq list %s --json — see the full queue and ages.\n", rigName)
	fmt.Fprintf(&sb, "  2. gt mq status %s — why is the oldest MR not landing (blocked, failing gate, conflict)?\n", oldest.ID)
	fmt.Fprintf(&sb, "  3. Check the rig's refinery health and main-branch state.\n")
	return sb.String()
}

// readRigMergeQueue lists a rig's open, unblocked MRs (scoped to the rig) and
// returns them as mqEntry values with parsed creation times and refinery
// scores. Mirrors cmd.runMQList's filtering: label gt:merge-request, status
// open, unblocked, scoped to the rig. MRs whose CreatedAt cannot be parsed are
// skipped (they cannot contribute a meaningful age).
func readRigMergeQueue(lister daemonMRLister, rigName string, now time.Time) ([]mqEntry, error) {
	issues, err := lister.ListMergeRequests(beads.ListOptions{
		Label:    "gt:merge-request",
		Status:   "open",
		Priority: -1,
	})
	if err != nil {
		return nil, err
	}

	var entries []mqEntry
	for _, issue := range issues {
		if issue == nil || issue.Status != "open" {
			continue
		}
		if len(issue.BlockedBy) > 0 || issue.BlockedByCount > 0 {
			continue
		}
		fields := beads.ParseMRFields(issue)
		// Scope to this rig. An unscoped MR (no rig field) is counted — same
		// conservative choice as cmd.pendingMRsForRig / daemonMRProber.
		if fields != nil && fields.Rig != "" && !equalFold(fields.Rig, rigName) {
			continue
		}
		createdAt, ok := parseMRTime(issue.CreatedAt)
		if !ok {
			continue
		}
		entries = append(entries, mqEntry{
			ID:        issue.ID,
			CreatedAt: createdAt,
			Score:     scoreMergeRequest(issue, fields, now),
		})
	}
	return entries, nil
}

// scoreMergeRequest computes an MR's refinery score, mirroring
// cmd.calculateMRScore so the head identified here matches what the refinery
// processes next.
func scoreMergeRequest(issue *beads.Issue, fields *beads.MRFields, now time.Time) float64 {
	mrCreatedAt, ok := parseMRTime(issue.CreatedAt)
	if !ok {
		mrCreatedAt = now
	}
	input := refinery.ScoreInput{
		Priority:    issue.Priority,
		MRCreatedAt: mrCreatedAt,
		Now:         now,
	}
	if fields != nil {
		input.RetryCount = fields.RetryCount
		if fields.ConvoyCreatedAt != "" {
			if convoyTime, err := time.Parse(time.RFC3339, fields.ConvoyCreatedAt); err == nil {
				input.ConvoyCreatedAt = &convoyTime
			}
		}
	}
	return refinery.ScoreMRWithDefaults(input)
}

// parseMRTime parses an MR timestamp in the two formats beads emits, returning
// the time in UTC and whether parsing succeeded.
func parseMRTime(ts string) (time.Time, bool) {
	if ts == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse("2006-01-02T15:04:05Z", ts); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

// buildMergeQueueListers constructs a per-rig MR lister from the rigs registry,
// mirroring buildAgentHeartbeatProber's path resolution.
func (d *Daemon) buildMergeQueueListers(rigsConfig *config.RigsConfig) map[string]daemonMRLister {
	resolve := make(map[string]daemonMRLister, len(rigsConfig.Rigs))
	for rigName := range rigsConfig.Rigs {
		rigPath := filepath.Join(d.config.TownRoot, rigName)
		r := &rig.Rig{Name: rigName, Path: rigPath}
		resolve[rigName] = beads.New(r.BeadsPath())
	}
	return resolve
}

func loadMergeQueueAgeState(path string) (mergeQueueAgeState, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path derived from trusted townRoot
	if err != nil {
		return mergeQueueAgeState{}, err
	}
	var s mergeQueueAgeState
	if err := json.Unmarshal(data, &s); err != nil {
		return mergeQueueAgeState{}, err
	}
	return s, nil
}

func saveMergeQueueAgeState(path string, state mergeQueueAgeState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating daemon state dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling merge-queue-age state: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/dispatch"
	"github.com/steveyegge/gastown/internal/sling"
	"github.com/steveyegge/gastown/internal/style"
)

// orphanMolReconcileMinAge is the minimum age an open, unassigned molecule
// wisp must reach before reconciliation will touch it. Mirrors
// zombieMolRecoveryMinAge: a healthy dispatch creates the molecule wisp open
// (and, for standalone formula sling, hooks it) within seconds, so a wisp
// younger than this may simply be mid-dispatch. 5 minutes is comfortably
// longer than any expected attach→hook latency.
const orphanMolReconcileMinAge = 5 * time.Minute

// reconcileOrphanMolecules detects molecule wisps that have been left orphaned
// by the daemon's session-death reapers and either reaps the zombie or unblocks
// its work bead so the normal dispatch cycle can re-pick-up the work.
//
// Background (gu-sz6va). When a polecat/agent session dies, the daemon's
// dead-session reapers (reapDeadPolecatWisps, reapDeadAgentWisps) reset
// hooked/in_progress beads to `status=open --assignee=` with no type filter.
// For a molecule wisp (type=molecule, ID contains "-wisp-") this leaves the
// wisp open and unassigned forever: nothing re-enqueues it and nothing reaps
// it. Over time these orphan wisps accumulate (observed: 5 in talontriage,
// un-hooked over two days, never cleaned up), and any work bead still bonded
// to such a wisp via the `blocks` dep stays structurally un-dispatchable.
//
// The existing recoverZombieMolecules pass only approaches from the work-bead
// side (it iterates open sling-contexts). It cannot see a wisp whose work bead
// is already closed, or whose sling-context was closed "dispatched" when the
// now-dead polecat originally spawned. This pass closes that gap by enumerating
// wisps directly.
//
// Per orphan wisp (open + unassigned + older than the TTL), inspect the work
// beads bonded to it (its dependents):
//   - A dependent is hooked/in_progress → a live worker owns it; skip entirely.
//   - A dependent is open               → re-enqueue intent: burn the stale
//     wisp so the work bead is unblocked, and let the normal ready/dispatch
//     cycle re-pick-it-up (we deliberately do NOT re-enqueue inline — see the
//     reconcileDecision contract).
//   - No live dependents (none, or all closed/tombstone) → reap the zombie:
//     burn the wisp.
//
// In both action cases the mechanism is the same — burn the wisp via
// burnExistingMolecules — which detaches it from its base bead, removes the
// dep bond, and force-closes the wisp. Burn is idempotent (no-op on already
// closed targets), so this is safe to run every dispatch tick.
//
// Best-effort: per-dir / per-wisp errors are logged and skipped so a single bad
// wisp never stalls the dispatch tick. Returns the number of wisps reconciled.
// deadline, when non-zero, bounds the serial per-wisp bd-show loop: once it
// passes, the pass returns and defers its remaining candidates to the next
// dispatch tick. This keeps a large orphan-wisp backlog from consuming the whole
// daemon dispatch window and starving placement (gu-t6jqq). A zero deadline means
// unbounded (interactive runs / tests).
func reconcileOrphanMolecules(townRoot string, deadline time.Time) int {
	now := timeNowForOrphanReconcile()

	reconciled := 0
	// Dedup candidates by wisp ID. listOrphanWispCandidates scans one dir per
	// rig, deduped by *resolved .beads path* (beadsSearchDirs), but two distinct
	// paths can still front the same underlying Dolt database: a sibling dir whose
	// .beads has no metadata/redirect/own-DB (e.g. gastown/.beads) falls through to
	// the same shared `hq` database as the town root. Both dirs then return the
	// identical wisp rows, so the same wisp would be burned once per overlapping
	// dir within a single tick (gu-h7baw). This was the dominant cause of the
	// observed ~2x reconcile inflation — 1859 of 1866 extra burns were the same
	// wisp appearing twice in one tick, not cross-tick re-selection as originally
	// suspected. A wisp ID maps to exactly one logical database (prefix→db is 1:1
	// via routes.jsonl), so collapsing duplicate IDs here is always safe.
	seenThisTick := make(map[string]bool)
	candidates := listOrphanWispCandidates(townRoot, now)
	for i, wisp := range candidates {
		// Yield to placement once the maintenance budget is spent. The wisp burn
		// is idempotent and the candidate set is re-enumerated next tick, so the
		// deferred tail is reconciled later with no lost work.
		if !deadline.IsZero() && i > 0 && timeNowForOrphanReconcile().After(deadline) {
			fmt.Fprintf(os.Stderr, "%s orphan-mol reconcile: maintenance budget elapsed — reconciled %d, deferred %d wisp(s) to next tick\n",
				style.Dim.Render("○"), reconciled, len(candidates)-i)
			break
		}
		if seenThisTick[wisp.ID] {
			continue
		}
		seenThisTick[wisp.ID] = true
		// Resolve dependents from a full bd show: the list view omits both
		// assignee and dependents. listOrphanWispCandidates already confirmed
		// type/status/age; here we confirm unassigned and read the work-bead
		// linkage.
		info := fetchWispInfoForReconcile(townRoot, wisp.ID)
		if info == nil {
			continue
		}
		// Final orphan guard against the freshly-fetched assignee (the list
		// view didn't carry one). A wisp that is actually assigned to a live
		// worker must never be touched.
		if !dispatch.IsEmptyAssignee(info.Assignee) {
			continue
		}

		// gu-bdzbd: this is the authoritative identity gate. The list view
		// (`bd mol wisp list --json`) carries neither issue_type nor labels, so
		// isOrphanMoleculeWispListEntry cannot distinguish a real molecule from a
		// mail/dog-step/plugin-run wisp — they all just have a "-wisp-" ID. The
		// `bd show` fetch above DOES return issue_type and labels, so we enforce
		// the molecule-only scope here. Without this, the pass force-burned
		// type=task/chore operational wisps (dog/patrol formula steps, plugin
		// runs) — ~99% of observed burns — and would silently destroy any
		// unassigned mail (mail.go defaults --wisp=true).
		if !isReconcilableMoleculeWisp(info) {
			continue
		}

		// gs-bpq: an open merge-request wisp is a pending merge that
		// intentionally outlives its submitting polecat — it sits in the merge
		// queue until the refinery merges it. A polecat self-terminates right
		// after `gt mq submit`, so its dead tmux session is EXPECTED, not
		// orphaned. Reaping the MR here silently drops the queued merge: the
		// branch stays pushed-but-unmerged on origin and the source bead is
		// left HOOKED, with no merge/reject mail (observed gs-wisp-qen/1r9 on a
		// refinery backlog). The refinery owns the MR-wisp lifecycle and closes
		// the wisp on merge/reject; the orphan pass must never reap one.
		if beads.HasLabel(info, mergeRequestWispLabel) {
			continue
		}

		action := reconcileDecision(info.Dependents)
		if action == orphanWispActionSkip {
			continue
		}

		// Both burn and re-enqueue reduce to "burn the stale wisp". Use a live
		// work-bead dependent's ID as the burn base when one exists (so detach
		// clears its attached_molecule pointer and the dep bond); otherwise use
		// the wisp's own ID (detach is a tolerated no-op on a bead with no
		// attachment, and the force-close still reaps the wisp root).
		baseBead := burnBaseBeadForWisp(wisp.ID, info.Dependents)
		if err := burnExistingMoleculesForRecovery([]string{wisp.ID}, baseBead, townRoot); err != nil {
			fmt.Fprintf(os.Stderr, "%s orphan-mol reconcile: burn failed for %s (base=%s): %v\n",
				style.Dim.Render("⚠"), wisp.ID, baseBead, err)
			continue
		}

		switch action {
		case orphanWispActionReenqueue:
			fmt.Printf("%s Reconciled orphan molecule %s: burned stale wisp to unblock %s for re-dispatch\n",
				style.Bold.Render("○"), wisp.ID, baseBead)
		default: // orphanWispActionBurn
			fmt.Printf("%s Reconciled orphan molecule %s: reaped zombie wisp (no live work bead)\n",
				style.Bold.Render("○"), wisp.ID)
		}
		reconciled++
	}
	return reconciled
}

// orphanWispAction is the reconciliation verdict for a single orphan wisp.
type orphanWispAction int

const (
	// orphanWispActionSkip leaves the wisp untouched (a live worker owns a
	// dependent work bead).
	orphanWispActionSkip orphanWispAction = iota
	// orphanWispActionBurn reaps a zombie wisp whose work is already done (or
	// which has no work bead at all).
	orphanWispActionBurn
	// orphanWispActionReenqueue burns the stale wisp so an open work bead is
	// unblocked and the normal dispatch cycle can re-pick-it-up. The enum value
	// is kept distinct from Burn for logging and so a future change can make
	// re-enqueue do more without rewriting the predicate's tests.
	orphanWispActionReenqueue
)

// reconcileDecision decides what to do with an orphan molecule wisp given the
// work beads bonded to it (its dependents from bd show --json). A dependent is
// treated as a "work bead" when it is not itself a wisp.
//
//   - any work-bead dependent hooked/in_progress → Skip   (live owner)
//   - any work-bead dependent open               → Reenqueue
//   - otherwise (no dependents, or all closed)   → Burn   (zombie)
//
// Skip takes precedence over Reenqueue: if any dependent is actively being
// worked we never touch the wisp, even if another dependent is open.
func reconcileDecision(dependents []beads.IssueDep) orphanWispAction {
	sawOpen := false
	for _, dep := range dependents {
		if strings.Contains(dep.ID, "-wisp-") {
			continue // Not a work bead — wisp-to-wisp bonds aren't our concern.
		}
		switch dep.Status {
		case "hooked", "in_progress":
			return orphanWispActionSkip
		case "open":
			sawOpen = true
		}
	}
	if sawOpen {
		return orphanWispActionReenqueue
	}
	return orphanWispActionBurn
}

// burnBaseBeadForWisp returns the bead ID to pass as the burn "base". Prefer a
// non-closed work-bead dependent (so detach clears its attached_molecule and
// the dep bond); fall back to the wisp's own ID when no such dependent exists.
func burnBaseBeadForWisp(wispID string, dependents []beads.IssueDep) string {
	for _, dep := range dependents {
		if strings.Contains(dep.ID, "-wisp-") {
			continue
		}
		if dep.Status == "closed" || dep.Status == "tombstone" {
			continue
		}
		return dep.ID
	}
	return wispID
}

// isOrphanMoleculeWispListEntry reports whether a wisp returned by
// `bd mol wisp list --json` is a candidate orphan: a molecule wisp, status
// open, older than the reconcile TTL. The list view omits assignee and
// dependents, so the unassigned + dependents checks happen later against a
// full bd show (see reconcileOrphanMolecules). now/ttl are injected for
// deterministic tests.
func isOrphanMoleculeWispListEntry(w *beads.Issue, now time.Time, ttl time.Duration) bool {
	if w == nil {
		return false
	}
	// Never reap mail. `gt mail send` defaults --wisp=true, so every message is
	// an ephemeral bead with a "-wisp-" ID (but issue_type=task, carrying the
	// gt:message label). Mail GC belongs to the mail TTL reaper, not this pass;
	// burning an unassigned message here would silently destroy it (gu-bdzbd).
	if beads.HasLabel(w, mailMessageLabel) {
		return false
	}
	// Identity must be type-authoritative. The "-wisp-" ID convention is NOT a
	// sufficient signal: dog/patrol formula steps, plugin-run wisps, and mail
	// all get "-wisp-" IDs while being type=task/chore, and burning them is out
	// of this molecule-reconcile pass's scope (gu-bdzbd). Only treat a wisp as a
	// molecule candidate when its type says so. The "-wisp-" ID is used only as
	// a fallback when the list view omitted issue_type entirely.
	switch {
	case w.Type == "molecule":
		// Authoritative match.
	case w.Type == "":
		// Type omitted by the list view — fall back to the ID convention.
		if !strings.Contains(w.ID, "-wisp-") {
			return false
		}
	default:
		// type is set and is not "molecule" (task/chore/agent/…) — not ours.
		return false
	}
	if w.Status != "open" {
		return false
	}
	if !sling.ContextOlderThan(w, now, ttl) {
		return false // Too fresh — may be a healthy mid-dispatch wisp.
	}
	return true
}

// isReconcilableMoleculeWisp reports whether a wisp loaded via `bd show --json`
// (which, unlike the list view, carries issue_type and labels) is actually a
// molecule this pass is allowed to reap. This is the authoritative identity
// gate (gu-bdzbd): the list filter can only see the "-wisp-" ID convention,
// which mail, dog/patrol formula steps, and plugin-run wisps all share.
//
// In scope: issue_type=molecule, OR issue_type empty with a "-wisp-" ID (a
// defensive fallback for any show response that omits the type). Explicitly out
// of scope: anything labeled gt:message (mail — GC'd by the mail TTL reaper) and
// any non-molecule type (task/chore/agent/…), whose lifecycles are owned
// elsewhere.
func isReconcilableMoleculeWisp(info *beads.Issue) bool {
	if info == nil {
		return false
	}
	if beads.HasLabel(info, mailMessageLabel) {
		return false
	}
	switch {
	case info.Type == "molecule":
		return true
	case info.Type == "":
		return strings.Contains(info.ID, "-wisp-")
	default:
		return false
	}
}

// timeNowForOrphanReconcile is a clock seam for the reconcile TTL check.
// Production leaves it as the wall clock; tests stub it. Mirrors
// timeNowForZombieRecovery.
var timeNowForOrphanReconcile = func() time.Time { return time.Now() }

// listOrphanWispCandidates enumerates open molecule wisps older than the TTL
// across the town's beads dir and every rig. Extracted as a seam so the
// orchestration is testable without real bd. Returns wisps that still need an
// assignee/dependents check via a full bd show.
var listOrphanWispCandidates = func(townRoot string, now time.Time) []*beads.Issue {
	// Scan each rig's wisps concurrently behind the same bounded semaphore as the
	// sling-context scan (gu-1h3ur/gu-el5bx). This per-rig `bd mol wisp list`
	// fan-out is a maintenance pass that, before gu-pjrz3 decoupled it from the
	// per-tick path, serial-forked one cold-start per dir × ~19 dirs and
	// contributed to the 5m dispatch budget blowout (gu-rz169). Collapsing it to a
	// capped-parallel scan keeps the pass sub-second even though it now runs on a
	// separate cadence. The semaphore — not the read throttle — bounds Dolt load,
	// so we keep WithoutReadThrottle (gu-pug66's lock-free dispatch path).
	dirs := beadsSearchDirs(townRoot)
	perDir := make([][]*beads.Issue, len(dirs))
	sem := make(chan struct{}, dispatchScanConcurrency())
	var wg sync.WaitGroup
	for i, dir := range dirs {
		wg.Add(1)
		go func(i int, dir string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			beadsDir := filepath.Join(dir, ".beads")
			b := beads.NewWithBeadsDir(dir, beadsDir).WithoutReadThrottle()
			out, err := b.Run("mol", "wisp", "list", "--json")
			if err != nil {
				// Wisps table may not exist for this dir — not an error worth
				// surfacing on the hot path.
				return
			}
			if len(out) == 0 || (out[0] != '[' && out[0] != '{') {
				return
			}
			var wrapper struct {
				Wisps []*beads.Issue `json:"wisps"`
			}
			if jerr := json.Unmarshal(out, &wrapper); jerr != nil {
				fmt.Fprintf(os.Stderr, "%s orphan-mol reconcile: parse wisp list for %s failed: %v\n",
					style.Dim.Render("⚠"), dir, jerr)
				return
			}
			var dirCandidates []*beads.Issue
			for _, w := range wrapper.Wisps {
				if isOrphanMoleculeWispListEntry(w, now, orphanMolReconcileMinAge) {
					dirCandidates = append(dirCandidates, w)
				}
			}
			perDir[i] = dirCandidates
		}(i, dir)
	}
	wg.Wait()

	// Fold results in dir order (deterministic).
	var candidates []*beads.Issue
	for _, dirCandidates := range perDir {
		candidates = append(candidates, dirCandidates...)
	}
	return candidates
}

// fetchWispInfoForReconcile loads a wisp's full info (assignee + dependents)
// via bd show. Uses beads.Issue rather than the cmd-package beadInfo alias
// because only beads.Issue carries the Dependents slice (the wisp→work-bead
// reverse linkage). Extracted as a seam for tests. Returns nil on any failure
// so the caller skips the wisp rather than acting on partial data.
var fetchWispInfoForReconcile = func(townRoot, wispID string) *beads.Issue {
	townBeadsDir := filepath.Join(townRoot, ".beads")
	wispBeadsDir := beads.ResolveBeadsDirForID(townBeadsDir, wispID)
	b := beads.NewWithBeadsDir(filepath.Dir(wispBeadsDir), wispBeadsDir).WithoutReadThrottle()
	out, err := b.Run("show", "--json", wispID)
	if err != nil {
		return nil
	}
	if len(out) == 0 || (out[0] != '[' && out[0] != '{') {
		return nil
	}
	var infos []*beads.Issue
	if err := json.Unmarshal(out, &infos); err != nil {
		return nil
	}
	if len(infos) == 0 || infos[0] == nil {
		return nil
	}
	return infos[0]
}

// mergeRequestWispLabel is the label a `gt mq submit` MR wisp carries. Matches
// internal/scheduler/capacity.LabelMergeRequest and the refinery's discovery
// query; duplicated as a local const to avoid pulling the capacity package into
// this reconcile path's import set. The orphan pass uses it to recognize — and
// never reap — pending merge-request wisps (gs-bpq).
const mergeRequestWispLabel = "gt:merge-request"

// mailMessageLabel is the label every `gt mail send` bead carries (mail.go
// defaults --wisp=true, so messages live in the wisps table with "-wisp-" IDs
// but issue_type=task). The orphan pass uses it to recognize — and never reap —
// mail, whose GC belongs to the mail TTL reaper (gu-bdzbd).
const mailMessageLabel = "gt:message"

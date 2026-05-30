package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

// zombieMolRecoveryMinAge is the minimum age a queued work bead with an open
// attached molecule must reach before its molecule is considered a zombie.
//
// Why a TTL: in the normal flow, sling executes molecule attach + spawn +
// hook in seconds. A short window exists between attach (the molecule wisp
// becomes open) and hook (the work bead transitions to "hooked" and is no
// longer a recovery candidate). Burning during that window would race a
// healthy dispatch. 5 minutes is comfortably longer than any expected
// hook latency (the daemon's scheduler tick cap is ~minutes) and shorter
// than the slingContextTTL stale-context cutoff so this pass can resolve
// the wedge before the context itself ages out.
const zombieMolRecoveryMinAge = 5 * time.Minute

// recoverZombieMolecules detects work beads in the deferred-dispatch queue
// that are stuck behind a never-claimed molecule wisp, and burns the orphan
// molecules so the bead can be re-dispatched on the next cycle.
//
// Background (gu-w49a). When a sling dispatches a work bead with an attached
// `mol-polecat-work` molecule, but no polecat ever claims the molecule (the
// pool is at capacity, all idle polecats hit a capacity-admission denial,
// the spawn races with the worker draining its queue, etc.), the molecule
// wisp persists indefinitely as an OPEN ephemeral. `bd mol bond` defaults
// to a `blocks` dependency type, so the molecule edge appears in
// `bd blocked`'s output and the scheduler's `isScheduledWorkBeadReady` gate
// permanently filters the work bead out of the ready set.
//
// Every other lifecycle path (polecat-claims-and-completes, polecat-claims-
// and-fails) GCs the molecule wisp. The "no polecat ever claimed" path
// leaves it forever — the work bead becomes structurally un-dispatchable
// without manual intervention.
//
// This recovery pass closes the gap. For each open sling-context whose
// work bead is itself open and aged past zombieMolRecoveryMinAge, we
// inspect the bead for attached molecule wisps. If any of those wisps
// look orphaned by isOrphanMolecule (status=open with no live worker,
// unassigned-but-active, or a dead tmux session), we burn them via the
// same burnExistingMolecules path that --force sling re-dispatches use.
// Burning the molecule clears the dep edge, so the next cycle's
// listBlockedWorkBeadIDs walk no longer marks the bead blocked and the
// dispatcher re-spawns it.
//
// Best-effort: per-bead errors are logged and skipped so a single bad
// bead doesn't stall the dispatch tick. Returns the number of work beads
// for which at least one molecule was burned.
func recoverZombieMolecules(townRoot string) int {
	contexts := listAllSlingContextRecords(townRoot)
	if len(contexts) == 0 {
		return 0
	}

	// Collect candidate work beads: sling-contexts older than the recovery
	// minimum age (so we never race a healthy in-progress sling that's mid
	// hook). Use the context's CreatedAt as the age proxy — a context only
	// exists once scheduleBead has run, and the molecule attach happens at
	// dispatch time strictly after that, so a context older than the TTL
	// implies the molecule (if any) is also older than the TTL.
	now := timeNowForZombieRecovery()
	candidatesByDir := make(map[string][]string)
	for _, ctx := range contexts {
		fields := beads.ParseSlingContextFields(ctx.issue.Description)
		if fields == nil || fields.WorkBeadID == "" {
			continue
		}
		if fields.DispatchFailures >= maxDispatchFailures {
			continue
		}
		if !isContextOlderThan(ctx.issue, now, zombieMolRecoveryMinAge) {
			continue
		}
		// Resolve where the work bead actually lives (rig DB, not the
		// context's home dir necessarily) so bd show targets the right
		// shard. The context's beadsDir is the rig dir for prefix-routed
		// rigs but may differ from the work bead's prefix-resolved dir
		// when contexts and work beads live in different shards (gu-38ov).
		townBeadsDir := filepath.Join(townRoot, ".beads")
		workBeadsDir := beads.ResolveBeadsDirForID(townBeadsDir, fields.WorkBeadID)
		candidatesByDir[workBeadsDir] = append(candidatesByDir[workBeadsDir], fields.WorkBeadID)
	}
	if len(candidatesByDir) == 0 {
		return 0
	}

	recovered := 0
	for beadsDir, ids := range candidatesByDir {
		// Use the same Beads wrapper pattern as batchFetchBeadInfoByIDs so
		// BEADS_DIR / dolt port resolution match. We need the full bead info
		// (including dependencies), which batchFetchBeadInfoByIDs strips, so
		// we shell out here directly.
		b := beads.NewWithBeadsDir(filepath.Dir(beadsDir), beadsDir)
		args := append([]string{"show", "--json"}, ids...)
		out, err := b.Run(args...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s zombie-mol recovery: bd show failed for %s: %v\n",
				style.Dim.Render("⚠"), beadsDir, err)
			continue
		}
		var infos []beadInfo
		if err := unmarshalBeadInfoArray(out, &infos); err != nil {
			fmt.Fprintf(os.Stderr, "%s zombie-mol recovery: parse bd show failed for %s: %v\n",
				style.Dim.Render("⚠"), beadsDir, err)
			continue
		}
		for i := range infos {
			info := &infos[i]
			if !isZombieMoleculeCandidate(info) {
				continue
			}
			molecules := openMoleculeDeps(info)
			if len(molecules) == 0 {
				continue
			}
			if err := burnExistingMoleculesForRecovery(molecules, info.ID, townRoot); err != nil {
				fmt.Fprintf(os.Stderr, "%s zombie-mol recovery: burn failed for %s: %v\n",
					style.Dim.Render("⚠"), info.ID, err)
				continue
			}
			fmt.Printf("%s Recovered zombie molecule(s) on %s: burned %s\n",
				style.Bold.Render("○"), info.ID, strings.Join(molecules, ", "))
			recovered++
		}
	}
	return recovered
}

// isZombieMoleculeCandidate reports whether the work bead is in the
// "queued but stuck behind an unclaimed molecule" state that recovery is
// meant to resolve.
//
// The contract is intentionally conservative: only act when the bead is
// status=open (not hooked / in_progress / blocked-by-real-deps) and
// isOrphanMolecule says no live worker owns the attached state. The
// status=open + open-molecule combination is the structural fingerprint
// of the zombie pattern: a bead the scheduler should be dispatching, with
// a leftover molecule edge that prevents it.
//
// Not a candidate when:
//   - status != open: hooked / in_progress / blocked / deferred / closed
//     either has a live worker or already has the right lifecycle gate
//   - isOrphanMolecule(info) is false: a live polecat may still pick up the
//     existing molecule, or the bead is in a non-orphanable status
func isZombieMoleculeCandidate(info *beadInfo) bool {
	if info == nil {
		return false
	}
	if info.Status != "open" {
		return false
	}
	return isOrphanMolecule(info)
}

// openMoleculeDeps returns the attached molecule wisp IDs that are still
// open. Mirrors collectExistingMolecules but is restricted to wisps still
// in an active status — recovery only needs to burn the live blockers; a
// closed-but-still-bonded molecule is not blocking dispatch (bd blocked
// already excludes closed deps) and burning it is wasted work.
func openMoleculeDeps(info *beadInfo) []string {
	if info == nil {
		return nil
	}
	seen := make(map[string]bool)
	var molecules []string
	for _, dep := range info.Dependencies {
		if !strings.Contains(dep.ID, "-wisp-") {
			continue
		}
		switch dep.Status {
		case "open", "hooked", "in_progress":
		default:
			continue
		}
		if seen[dep.ID] {
			continue
		}
		seen[dep.ID] = true
		molecules = append(molecules, dep.ID)
	}
	// Also honor the description's attached_molecule pointer in case it
	// points to a wisp that bd show didn't return as a dep row (description
	// metadata can drift from bonds, see collectExistingMolecules). The
	// ground-truth status check happens inside burnExistingMolecules, which
	// is no-op on already-closed targets, so this is safe even if the
	// referenced wisp is already closed.
	issue := &beads.Issue{Description: info.Description}
	fields := beads.ParseAttachmentFields(issue)
	if fields != nil && fields.AttachedMolecule != "" && !seen[fields.AttachedMolecule] {
		seen[fields.AttachedMolecule] = true
		molecules = append(molecules, fields.AttachedMolecule)
	}
	return molecules
}

// timeNowForZombieRecovery is a clock seam that lets tests inject a
// deterministic "current time" for the recovery TTL check. Production
// callers leave it unset so we use the wall clock. Mirrors
// nowForDeferRelease in capacity_dispatch.go.
var timeNowForZombieRecovery = func() time.Time { return time.Now() }

// burnExistingMoleculesForRecovery is a seam over burnExistingMolecules
// so tests can stub the destructive burn path without touching real bd
// or Dolt state. Mirrors burnExistingMoleculesForRollback in sling.go.
var burnExistingMoleculesForRecovery = burnExistingMolecules

// unmarshalBeadInfoArray decodes a `bd show --json` array response into
// the provided slice. Extracted as a var so tests can stub the decoder
// without driving real bd output; the production implementation is plain
// encoding/json.
var unmarshalBeadInfoArray = func(out []byte, dst *[]beadInfo) error {
	if len(out) == 0 {
		return fmt.Errorf("empty bd show output")
	}
	return json.Unmarshal(out, dst)
}

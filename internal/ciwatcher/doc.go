// Package ciwatcher implements the post-merge CI watcher (gu-xuzc).
//
// The Refinery merges work to main and trusts that pre-push gates have caught
// problems. In practice, infrastructure flakes, transitive dependency rot, and
// rare polecat skips occasionally land bad commits. Once the bad commit is on
// main, the polecat is gone, the bead is closed, and only `gh run list` would
// surface the failure — manually.
//
// ciwatcher is the last line of defense. On every poll cycle:
//
//  1. Fetch recent CI runs for `main` from the Git host (currently `gh run
//     list`).
//  2. For each completed run we have not already processed, classify:
//     - failure → identify the bead that landed the offending commit, reopen
//     it with the `broke-main-ci` label, mail the mayor at HIGH severity,
//     and write a freeze flag at <townRoot>/.runtime/mq-frozen-<rig> that
//     the Refinery checks before processing the next MR.
//     - success after a freeze → clear the freeze flag and notify the mayor
//     that main is healthy again.
//  3. Persist the seen-run IDs so a subsequent poll never double-fires on the
//     same run.
//
// The watcher itself is intentionally stateless beyond the freeze file and the
// seen-runs ledger. It is invoked one-shot from the CLI (`gt ci-watcher poll`)
// or from a deacon patrol — there is no long-lived process. All side effects
// (bead reopen, mail, freeze) are idempotent so a re-run after a partial
// failure converges to the correct state.
//
// Phase split (per the bead description):
//
//   - Phase 1: detect + reopen bead + mail mayor — implemented here, plus the
//     freeze flag because writing a single file is cheap and mirrors the
//     already-existing `merge-queue.frozen` discriminator surfaced in the
//     refinery guard.
//   - Phase 2: refinery integration — implemented in
//     internal/refinery/freeze_guard.go. The Engineer checks IsFrozen() at
//     the top of ProcessMRInfo and returns NoMerge when the flag is set.
//
// Design context: bead gu-xuzc, parent gu-rh0g (Pattern B aftermath analysis),
// siblings gu-7f0v (pre-push gate split) and gu-zy57 (skip audit).
package ciwatcher

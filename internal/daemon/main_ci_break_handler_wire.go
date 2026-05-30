// Wiring for the Mayor SEV-1 main-CI-break handler (Phase 0 task 11,
// gu-36voy / D16).
//
// This file bridges the daemon package's MainCIBreakEvent/Handler types
// with the autotestpr.MainCIBreakHandler. The dispatch dog
// (main_ci_break_dog.go, gu-15c8) owns event detection; this wiring
// supplies the response handler that runs the SEV-1 chain when an
// auto-test-pr-attributable break is dispatched.
//
// The wiring is called once during daemon startup (in Run(), after
// beads stores are opened, after the existing
// initMRCycleCloseHandler() call) to install the real handler. Before
// this call — or when the main_ci_break patrol is disabled — events
// dispatch to the daemon's noopMainCIBreakHandler which logs and
// drops, just like the cycle-close path before its handler lands.
package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/steveyegge/gastown/internal/autotestpr"
	"github.com/steveyegge/gastown/internal/beads"
)

// initMainCIBreakHandler creates and registers the SEV-1 handler. Called
// from Run() after the cycle-close handler is wired and before the
// main_ci_break ticker starts.
//
// The handler requires:
//   - A beads.Beads wrapper pointed at the town root (for town-state
//     bead mutations).
//   - A revert-task filer that creates a P1 task bead in the affected
//     rig (best-effort; logged on failure — see the handler docstring).
//   - The daemon's logger for structured output.
//   - A nudge function that shells out to `gt nudge overseer` for the
//     SEV-1 payload.
//
// If the main_ci_break patrol is not active, this is a no-op (the
// handler stays nil and events go to noopMainCIBreakHandler).
func (d *Daemon) initMainCIBreakHandler() {
	if !d.isPatrolActive("main_ci_break") {
		return
	}

	// Use the in-process beadsdk.Storage (hq store) when available.
	// Mirrors initMRCycleCloseHandler so SEV-1 town-state writes go
	// through the same in-process path as cycle-close writes — keeps
	// CAS-conflict semantics identical between the two callers.
	var b *beads.Beads
	if hqStore := d.beadsStores["hq"]; hqStore != nil {
		b = beads.NewWithStore(d.config.TownRoot, hqStore)
	} else {
		b = beads.New(d.config.TownRoot)
		d.logger.Printf("main-ci-break-handler: hq store not available, falling back to bd subprocess")
	}

	handler := &autotestpr.MainCIBreakHandler{
		Beads:      b,
		FileRevert: d.fileMainCIBreakRevertTask,
		NudgeOverseer: func(msg string) {
			d.nudgeOverseer(msg)
		},
		Now:  time.Now,
		Logf: d.logger.Printf,
	}

	d.SetMainCIBreakHandler(func(ev MainCIBreakEvent) {
		handler.HandleEvent(autotestpr.MainCIBreakEvent{
			RigName:      ev.RigName,
			CommitSHA:    ev.CommitSHA,
			PreviousSHA:  ev.PreviousSHA,
			MRBeadID:     ev.MRBeadID,
			EscalationID: ev.EscalationID,
			Body:         ev.Body,
		})
	})

	d.logger.Printf("Mayor main-CI-break SEV-1 handler registered (Phase 0 task 11, D16)")
}

// fileMainCIBreakRevertTask files a P1 task bead in the affected rig
// asking an operator (or a follow-up patrol) to land a revert MR for
// the breaking commit. Synthesis §D16 (a) names "the existing revert-MR
// formula" — in Phase 0 there is no Mayor-driven git-revert path; the
// realistic surface is a tracked task that the runbook (Phase 0 task
// 12, .gt/auto-test-pr/sev1-runbook.md) drives. Phase 1+ replaces this
// with an automated revert-MR formula dispatch when one exists.
//
// Best-effort: errors propagate up to the handler which logs them. The
// rig pause + Overseer nudge happen regardless of whether this
// succeeds — losing the task bead degrades to "operator must file
// the revert from the runbook by hand", not "rig keeps running".
//
// Idempotent via a label-based dedup: the bead carries
// `mr:<mrBeadID>` and `gt:auto-test-pr-revert`, so a second SEV-1
// for the same MR (e.g., dolt-write retry storm) finds the existing
// bead and skips creation. Identical pattern to fileClassifierBead.
func (d *Daemon) fileMainCIBreakRevertTask(rigName, mrBeadID, commitSHA, previousSHA, escalationID string) error {
	rigDir := beads.GetRigDirForName(d.config.TownRoot, rigName)
	if rigDir == "" {
		return fmt.Errorf("no beads dir for rig %s", rigName)
	}

	mrLabel := "mr:" + mrBeadID
	revertLabel := "gt:auto-test-pr-revert"

	// Skip if a revert task already exists for this MR.
	if d.mainCIBreakRevertTaskExists(rigDir, mrLabel, revertLabel) {
		d.logger.Printf("main-ci-break-handler: revert task already exists for rig=%s mr=%s — skipping",
			rigName, mrBeadID)
		return nil
	}

	title := fmt.Sprintf("D16 SEV-1 auto-revert: %s on %s", shortRevertSHA(commitSHA), rigName)
	desc := fmt.Sprintf(`Auto-Test-PR broke main CI on rig %s.

Commit: %s
Previous-good: %s
MR bead: %s
Escalation: %s

Action: file a revert MR for the breaking commit, land it, then
either wait out the 7d circuit-breaker cooldown or override:

    gt auto-test-pr resume --rig=%s --override-circuit-breaker

See runbook: .gt/auto-test-pr/sev1-runbook.md (Phase 0 task 12).
Auto-filed by main-ci-break-handler (D16, gu-36voy).`,
		rigName, commitSHA, previousSHA, mrBeadID, escalationID, rigName)

	allLabels := []string{
		"gt:task",
		"gt:auto-test-pr",
		revertLabel,
		mrLabel,
		"sev:1",
	}
	labelStr := joinLabels(allLabels)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.bdPath, //nolint:gosec // G204: args constructed internally
		"create",
		"--title="+title,
		"--description="+desc,
		"--type=task",
		"--priority=P1",
		"--label="+labelStr,
	)
	cmd.Dir = rigDir
	cmd.Env = append(os.Environ(), "BD_ACTOR=mayor/main-ci-break-handler")
	setSysProcAttr(cmd)

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd create revert task: %v (%s)", err, string(out))
	}
	d.logger.Printf("main-ci-break-handler: filed revert task for rig=%s mr=%s commit=%s",
		rigName, mrBeadID, shortRevertSHA(commitSHA))
	return nil
}

// mainCIBreakRevertTaskExists checks whether an open revert task with
// the given (mr-label + revert-label) pair already exists in rigDir.
// Returns false on any error so a flaky `bd list` doesn't suppress the
// SEV-1 — duplicate beads are recoverable; a missed SEV-1 is not.
func (d *Daemon) mainCIBreakRevertTaskExists(rigDir, mrLabel, revertLabel string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.bdPath, //nolint:gosec // G204: args constructed internally
		"list",
		"--label="+revertLabel+","+mrLabel,
		"--status=open",
		"--json",
	)
	cmd.Dir = rigDir
	cmd.Env = os.Environ()
	setSysProcAttr(cmd)

	out, err := cmd.Output()
	if err != nil {
		return false
	}

	// `bd list --json` returns a JSON array; we only care whether it's
	// non-empty. Cheap structural check that doesn't pull in
	// encoding/json: an empty array round-trips through Trim as "[]".
	for _, b := range out {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '[':
			// Found '[' — keep scanning for non-whitespace before ']'.
		default:
			// Bare value or non-array — treat as "unparseable, skip".
			return false
		}
		break
	}
	hasContent := false
	for _, b := range out {
		switch b {
		case '[', ' ', '\t', '\r', '\n':
			continue
		case ']':
			return hasContent
		default:
			hasContent = true
		}
	}
	return hasContent
}

// joinLabels comma-joins labels for the bd CLI's `--label=` form. Pulled
// out so the wiring file doesn't import strings just for one Join — it
// matches the `strings.Join(allLabels, ",")` pattern used elsewhere in
// the daemon package.
func joinLabels(labels []string) string {
	out := ""
	for i, l := range labels {
		if i > 0 {
			out += ","
		}
		out += l
	}
	return out
}

// shortRevertSHA truncates a SHA for log/title readability. Shares the
// 12-char convention used by the dog's logging.
func shortRevertSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/session"
)

// push_stranded_dog auto-recovers "pushed-but-no-MR" stranded branches from the
// daemon — closing the gap that motivated TAL-46 (parent TAL-27). A polecat can
// commit + push its branch to origin and then have its session end before it
// submits the merge request: the branch is safe on origin, the hooked bead is
// left labeled stranded-merge, and `gt done` (via fileStrandedPushWisp) files a
// gt:push-stranded wisp — but NOTHING consumed that wisp. The witness's
// recoverUnfiledMR can only act while the polecat WORKTREE still exists on disk
// (it git-cherries the worktree); once the slot is reaped the branch strands
// until a human sweeps it. NEEDS_MQ_SUBMIT (workstate.go) is advisory-only.
//
// This dog is the missing daemon-side consumer. Each tick it scans every rig
// for open gt:push-stranded wisps and, for each one whose work is genuinely
// stranded (branch on origin, no open MR for that branch, owning polecat
// session NOT live), submits the MR directly from the pushed branch tip via the
// existing, tested `gt mq submit --branch` path:
//
//	polecat pushes branch, session dies before `gt done` submit
//	      │  fileStrandedPushWisp files gt:push-stranded wisp (done.go)
//	      ▼
//	push_stranded_dog (this patrol)                ← consumer (NEW, TAL-46)
//	      │  branch-on-origin + no-open-MR + session-dead → gt mq submit --branch
//	      ▼
//	MR enters the queue; wisp closed (recovered). On repeated failure → escalate.
//
// SAFETY (TAL-46 acceptance #3 — no false positives on live polecats):
//   - Skip any wisp whose owning polecat tmux session is still live (a working
//     polecat will submit its own MR via gt done). Checked with a TOCTOU
//     re-check immediately before submit, mirroring maybeReapDeadAgentBead.
//   - Skip when an open MR already exists for the branch (idempotent; the
//     submit path itself dedups on branch+SHA, this just avoids the work).
//   - Skip when the branch is not actually on origin (nothing to recover; that
//     is a genuine push failure for a human/escalation, not an MR strand).
//
// Auto-remediation here (vs the escalate-only stance of merge_queue_age_dog) is
// deliberate and bounded: `gt mq submit` is idempotent, the action is gated on
// a provably-dead session, and after maxAttempts failed submits the dog stops
// retrying and escalates so a human looks at a branch that will not submit.
const (
	// defaultPushStrandedInterval is the patrol cadence. Recovery is not
	// latency-critical (the branch is safe on origin), but a stranded branch
	// blocks the source bead from closing, so run reasonably often.
	defaultPushStrandedInterval = 10 * time.Minute

	// defaultPushStrandedMaxAttempts is how many times the dog will try to
	// submit a given wisp's MR before giving up and escalating instead of
	// retrying forever. A submit that keeps failing is a real problem (gates,
	// conflicts, a deleted branch) that needs a human, not another retry.
	defaultPushStrandedMaxAttempts = 3

	pushStrandedSource = "push_stranded_dog"

	// pushStrandedLabel is the wisp label fileStrandedPushWisp writes (done.go /
	// mq_submit.go). It is intentionally NOT gt:merge-request so the refinery's
	// queue scan never treats a not-yet-landed branch as a merge candidate.
	pushStrandedLabel = "gt:push-stranded"
)

// PushStrandedConfig holds configuration for the push_stranded patrol.
type PushStrandedConfig struct {
	// Enabled controls whether the stranded-push recovery dog runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "10m").
	IntervalStr string `json:"interval,omitempty"`

	// MaxAttempts caps how many times a single wisp's MR submit is retried
	// before the dog escalates instead of retrying. 0 → default.
	MaxAttempts int `json:"max_attempts,omitempty"`
}

// pushStrandedInterval returns the configured interval, or the default (10m).
func pushStrandedInterval(cfg *DaemonPatrolConfig) time.Duration {
	if cfg != nil && cfg.Patrols != nil && cfg.Patrols.PushStranded != nil {
		if cfg.Patrols.PushStranded.IntervalStr != "" {
			if d, err := time.ParseDuration(cfg.Patrols.PushStranded.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultPushStrandedInterval
}

// pushStrandedMaxAttempts returns the configured cap, or the default (3).
func pushStrandedMaxAttempts(cfg *DaemonPatrolConfig) int {
	if cfg != nil && cfg.Patrols != nil && cfg.Patrols.PushStranded != nil {
		if cfg.Patrols.PushStranded.MaxAttempts > 0 {
			return cfg.Patrols.PushStranded.MaxAttempts
		}
	}
	return defaultPushStrandedMaxAttempts
}

// strandedWisp is the minimal view of a gt:push-stranded wisp the dog reasons
// about. Parsed from the wisp's key:value description (fileStrandedPushWisp
// writes branch / source_issue / commit_sha / worker / rig).
type strandedWisp struct {
	WispID    string
	Branch    string
	IssueID   string
	CommitSHA string
	Worker    string
	Rig       string
}

// pushStrandedAction is the verdict the pure decision core returns for one wisp.
type pushStrandedAction int

const (
	// pushStrandedSkipInvalid: the wisp lacks the fields needed to recover
	// (no branch / no source issue) — leave it for a human.
	pushStrandedSkipInvalid pushStrandedAction = iota
	// pushStrandedSkipLive: the owning polecat session is still live — never
	// yank a working polecat's branch (TAL-46 acceptance #3).
	pushStrandedSkipLive
	// pushStrandedSkipHasMR: an open MR already exists for this branch — the
	// work is already in the queue; close the wisp as recovered.
	pushStrandedSkipHasMR
	// pushStrandedSkipNoBranch: the branch is not on origin — there is nothing
	// to submit (a real push failure, not an MR strand). Leave for escalation.
	pushStrandedSkipNoBranch
	// pushStrandedSubmit: stranded and safe to recover — submit the MR.
	pushStrandedSubmit
	// pushStrandedGiveUp: submit has failed maxAttempts times — escalate instead
	// of retrying forever.
	pushStrandedGiveUp
)

// pushStrandedFacts are the gathered, side-effecting observations the pure core
// turns into an action. Gathering (git/tmux/beads) happens in the caller; the
// decision itself is pure and unit-tested.
type pushStrandedFacts struct {
	// SessionLive is whether the owning polecat tmux session is still alive.
	SessionLive bool
	// OpenMRExists is whether an open gt:merge-request for the branch was found.
	OpenMRExists bool
	// BranchOnOrigin is whether the branch tip exists on origin.
	BranchOnOrigin bool
	// Attempts is how many times this wisp's submit has already been tried.
	Attempts int
	// MaxAttempts is the give-up threshold.
	MaxAttempts int
}

// decidePushStrandedAction is the pure decision core. It evaluates the guards in
// safety-first order so a live polecat or an already-queued branch is never
// touched, and only a provably-stranded branch is submitted.
func decidePushStrandedAction(w strandedWisp, f pushStrandedFacts) pushStrandedAction {
	if w.Branch == "" || w.IssueID == "" {
		return pushStrandedSkipInvalid
	}
	// Safety guard first: a live polecat owns its branch and will submit its own
	// MR. Never recover underneath it.
	if f.SessionLive {
		return pushStrandedSkipLive
	}
	// Already in the queue — nothing to submit; the wisp can be retired.
	if f.OpenMRExists {
		return pushStrandedSkipHasMR
	}
	// Not on origin — the push genuinely failed; this is not an MR strand and
	// submitting would fail. Leave it for the human/escalation path.
	if !f.BranchOnOrigin {
		return pushStrandedSkipNoBranch
	}
	// Stranded and recoverable. Give up (escalate) once we've burned the retry
	// budget so a never-submittable branch does not loop forever.
	if f.MaxAttempts > 0 && f.Attempts >= f.MaxAttempts {
		return pushStrandedGiveUp
	}
	return pushStrandedSubmit
}

// runPushStrandedDog is the patrol entry point. It enumerates every registered
// rig, lists that rig's open gt:push-stranded wisps, and recovers each one whose
// work is genuinely stranded (branch on origin, no open MR, session dead).
func (d *Daemon) runPushStrandedDog() {
	if !d.isPatrolActive("push_stranded") {
		return
	}

	// Gate on the shared Dolt circuit breaker: listing wisps touches each rig's
	// beads DB. When Dolt is degraded, skip and resume next tick — a stranded
	// branch only waits, it does not worsen.
	if d.doltBreaker != nil && !d.doltBreaker.Allow() {
		d.logger.Printf("push_stranded: dolt-degraded — skipping tick (circuit breaker open)")
		return
	}

	rigsConfig, err := d.loadRigsConfig()
	if d.doltBreaker != nil {
		d.doltBreaker.Record(err)
	}
	if err != nil {
		d.logger.Printf("push_stranded: failed to load rigs config: %v", err)
		return
	}
	if rigsConfig == nil || len(rigsConfig.Rigs) == 0 {
		return
	}

	maxAttempts := pushStrandedMaxAttempts(d.patrolConfig)
	listers := d.buildMergeQueueListers(rigsConfig)

	for rigName := range rigsConfig.Rigs {
		lister, ok := listers[rigName]
		if !ok {
			continue
		}
		d.recoverStrandedPushesForRig(rigName, lister, maxAttempts)
	}
}

// recoverStrandedPushesForRig processes one rig's open gt:push-stranded wisps.
func (d *Daemon) recoverStrandedPushesForRig(rigName string, lister daemonMRLister, maxAttempts int) {
	wisps, err := lister.ListMergeRequests(beads.ListOptions{
		Label:    pushStrandedLabel,
		Status:   "open",
		Priority: -1,
	})
	if err != nil {
		d.logger.Printf("push_stranded: %s: failed to list push-stranded wisps: %v", rigName, err)
		return
	}

	for _, wisp := range wisps {
		if wisp == nil || wisp.Status != "open" {
			continue
		}
		sw := parseStrandedWisp(wisp)
		// Scope to this rig: a wisp with a rig field that names a different rig
		// is handled when we iterate that rig (its lister owns that DB).
		if sw.Rig != "" && !equalFold(sw.Rig, rigName) {
			continue
		}

		facts := d.gatherStrandedFacts(rigName, sw, maxAttempts)
		switch decidePushStrandedAction(sw, facts) {
		case pushStrandedSkipInvalid:
			d.logger.Printf("push_stranded: %s: wisp %s missing branch/issue — leaving for human", rigName, sw.WispID)
		case pushStrandedSkipLive:
			d.logger.Printf("push_stranded: %s: wisp %s owner session live — not recovering live polecat", rigName, sw.WispID)
		case pushStrandedSkipHasMR:
			d.logger.Printf("push_stranded: %s: wisp %s already has an open MR — closing wisp as recovered", rigName, sw.WispID)
			d.closeStrandedWisp(rigName, sw, "recovered: open MR already exists for branch")
		case pushStrandedSkipNoBranch:
			d.logger.Printf("push_stranded: %s: wisp %s branch %q not on origin — push genuinely failed, leaving for recovery sweep", rigName, sw.WispID, sw.Branch)
		case pushStrandedGiveUp:
			d.logger.Printf("push_stranded: %s: wisp %s exceeded %d submit attempts — escalating", rigName, sw.WispID, maxAttempts)
			d.escalateStrandedWisp(rigName, sw, maxAttempts)
		case pushStrandedSubmit:
			d.submitStrandedWisp(rigName, sw)
		}
	}
}

// gatherStrandedFacts performs the side-effecting observations (tmux liveness,
// open-MR lookup, branch-on-origin) that decidePushStrandedAction consumes.
func (d *Daemon) gatherStrandedFacts(rigName string, sw strandedWisp, maxAttempts int) pushStrandedFacts {
	f := pushStrandedFacts{
		Attempts:    d.strandedAttemptCount(rigName, sw.WispID),
		MaxAttempts: maxAttempts,
	}

	// Owning polecat session liveness. A missing worker means we can't name a
	// session; treat as not-live (the wisp exists because a session ended). Any
	// tmux error is treated as live (conservative: do not recover under doubt).
	if sw.Worker != "" {
		sessionName := session.PolecatSessionName(session.PrefixFor(rigName), sw.Worker)
		if alive, err := d.tmux.HasSession(sessionName); err != nil || alive {
			f.SessionLive = true
		}
	}

	// Open MR already in the queue for this branch?
	bd := beads.New(rigBDWorkingDir(d.config.TownRoot, rigName))
	if mr, err := bd.FindMRForBranch(sw.Branch); err != nil {
		// Lookup failed — conservatively assume an MR may exist so we don't
		// double-submit. A genuine strand re-evaluates next tick.
		d.logger.Printf("push_stranded: %s: open-MR lookup failed for %q: %v (skipping this tick)", rigName, sw.Branch, err)
		f.OpenMRExists = true
	} else if mr != nil {
		f.OpenMRExists = true
	}

	// Branch tip on origin? Use the rig's bare repo / clone. ls-remote against
	// the push URL is a network check that does not need local refs, so this
	// works even after the polecat worktree was reaped.
	if g := d.rigGitForStranded(rigName); g != nil {
		if exists, err := g.PushRemoteBranchExists("origin", sw.Branch); err != nil {
			// Can't prove the branch is on origin — do not submit this tick.
			d.logger.Printf("push_stranded: %s: branch-on-origin check failed for %q: %v (skipping this tick)", rigName, sw.Branch, err)
			f.BranchOnOrigin = false
		} else {
			f.BranchOnOrigin = exists
		}
	}

	return f
}

// rigGitForStranded returns a *git.Git rooted at a usable clone for the rig: the
// refinery clone (a normal worktree) when present, else the bare .repo.git.
// Returns nil when neither exists.
func (d *Daemon) rigGitForStranded(rigName string) *git.Git {
	rigPath := filepath.Join(d.config.TownRoot, rigName)
	refineryClone := filepath.Join(rigPath, "refinery", "rig")
	if _, err := os.Stat(refineryClone); err == nil {
		return git.NewGit(refineryClone)
	}
	bareRepo := filepath.Join(rigPath, ".repo.git")
	if _, err := os.Stat(bareRepo); err == nil {
		return git.NewGitWithDir(bareRepo, "")
	}
	return nil
}

// submitStrandedWisp recovers one stranded branch by submitting its MR from the
// pushed tip, then closes the wisp on success. It re-checks session liveness
// immediately before submitting (TOCTOU guard) so a polecat respawned since the
// initial scan is never recovered underneath.
func (d *Daemon) submitStrandedWisp(rigName string, sw strandedWisp) {
	// TOCTOU guard: re-check liveness right before acting.
	if sw.Worker != "" {
		sessionName := session.PolecatSessionName(session.PrefixFor(rigName), sw.Worker)
		if alive, err := d.tmux.HasSession(sessionName); err != nil || alive {
			d.logger.Printf("push_stranded: %s: wisp %s owner session became live before submit — aborting recovery", rigName, sw.WispID)
			return
		}
	}

	d.recordStrandedAttempt(rigName, sw.WispID)

	ctx, cancel := context.WithTimeout(d.ctx, 5*time.Minute)
	defer cancel()

	// Run `gt mq submit --branch` from a usable rig clone. The command verifies
	// the branch tip on origin, builds + verifies the MR bead, and is idempotent
	// on branch+SHA, so a concurrent recovery or a late polecat submit cannot
	// double-file. --no-cleanup: there is no live polecat session to tear down.
	args := []string{"mq", "submit", "--branch", sw.Branch, "--no-cleanup"}
	if sw.IssueID != "" {
		args = append(args, "--issue", sw.IssueID)
	}
	cmd := exec.CommandContext(ctx, d.gtPath, args...) //nolint:gosec // G204: args constructed internally
	cmd.Dir = d.strandedSubmitDir(rigName)
	cmd.Env = append(os.Environ(), "BD_ACTOR=daemon", "GT_RIG="+rigName)
	setSysProcAttr(cmd)

	out, err := cmd.CombinedOutput()
	if d.doltBreaker != nil {
		d.doltBreaker.Record(err)
	}
	if err != nil {
		d.logger.Printf("push_stranded: %s: gt mq submit --branch %s failed: %v\nOutput: %s",
			rigName, sw.Branch, err, strings.TrimSpace(string(out)))
		// Leave the wisp open: the next tick retries until maxAttempts, then escalates.
		return
	}

	d.logger.Printf("push_stranded: %s: recovered stranded branch %s (issue=%s) — MR submitted", rigName, sw.Branch, sw.IssueID)
	d.closeStrandedWisp(rigName, sw, "recovered: MR submitted from pushed branch tip by push_stranded_dog")
	d.clearStrandedAttempts(rigName, sw.WispID)
}

// strandedSubmitDir returns the directory `gt mq submit` runs in. The refinery
// clone is a real worktree (preferred); fall back to the rig bd working dir.
func (d *Daemon) strandedSubmitDir(rigName string) string {
	refineryClone := filepath.Join(d.config.TownRoot, rigName, "refinery", "rig")
	if _, err := os.Stat(refineryClone); err == nil {
		return refineryClone
	}
	return rigBDWorkingDir(d.config.TownRoot, rigName)
}

// closeStrandedWisp closes a gt:push-stranded wisp once its work is recovered
// (or already in the queue). Best-effort: a failure just leaves the wisp for
// the next tick / the wisp_reaper.
func (d *Daemon) closeStrandedWisp(rigName string, sw strandedWisp, reason string) {
	bd := beads.New(rigBDWorkingDir(d.config.TownRoot, rigName))
	if err := bd.CloseWithReason(reason, sw.WispID); err != nil {
		d.logger.Printf("push_stranded: %s: failed to close wisp %s: %v", rigName, sw.WispID, err)
	}
}

// escalateStrandedWisp raises a deduped escalation for a wisp whose MR submit
// keeps failing, so a human looks at a branch that will not submit on its own.
func (d *Daemon) escalateStrandedWisp(rigName string, sw strandedWisp, maxAttempts int) {
	msg := buildPushStrandedEscalationMessage(rigName, sw, maxAttempts)
	if err := d.escalate(pushStrandedSource+":"+rigName+":"+sw.WispID, msg); err != nil {
		d.logger.Printf("push_stranded: %s: escalation for wisp %s failed, will retry next tick: %v", rigName, sw.WispID, err)
	}
}

// buildPushStrandedEscalationMessage assembles the give-up escalation body.
func buildPushStrandedEscalationMessage(rigName string, sw strandedWisp, maxAttempts int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Stranded push could not be auto-recovered in rig %q after %d attempts (agent diagnosis required).\n\n", rigName, maxAttempts)
	fmt.Fprintf(&sb, "A polecat pushed a branch to origin but no MR was ever submitted, and the daemon's\n")
	fmt.Fprintf(&sb, "push_stranded_dog failed to submit it via `gt mq submit --branch` %d times.\n", maxAttempts)
	fmt.Fprintf(&sb, "\nStranded work:\n")
	fmt.Fprintf(&sb, "  rig:          %s\n", rigName)
	fmt.Fprintf(&sb, "  wisp:         %s\n", sw.WispID)
	fmt.Fprintf(&sb, "  branch:       %s\n", sw.Branch)
	fmt.Fprintf(&sb, "  source issue: %s\n", sw.IssueID)
	if sw.CommitSHA != "" {
		fmt.Fprintf(&sb, "  commit:       %s\n", sw.CommitSHA)
	}
	if sw.Worker != "" {
		fmt.Fprintf(&sb, "  worker:       %s\n", sw.Worker)
	}
	fmt.Fprintf(&sb, "\nRECOMMENDED ACTION (diagnose, then act with judgment):\n")
	fmt.Fprintf(&sb, "  1. cd into the rig and run `gt mq submit --branch %s --issue %s` to see the failure.\n", sw.Branch, sw.IssueID)
	fmt.Fprintf(&sb, "  2. Common causes: branch deleted from origin, gate/conflict on the branch, or a\n")
	fmt.Fprintf(&sb, "     stale fork-sync base. Resolve, then resubmit — or close the wisp if obsolete.\n")
	return sb.String()
}

// parseStrandedWisp extracts the recovery fields from a gt:push-stranded wisp's
// key:value description (written by fileStrandedPushWisp).
func parseStrandedWisp(issue *beads.Issue) strandedWisp {
	sw := strandedWisp{}
	if issue == nil {
		return sw
	}
	sw.WispID = issue.ID
	for _, line := range strings.Split(issue.Description, "\n") {
		line = strings.TrimSpace(line)
		colonIdx := strings.Index(line, ":")
		if colonIdx == -1 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:colonIdx]))
		value := strings.TrimSpace(line[colonIdx+1:])
		if value == "" || strings.EqualFold(value, "null") {
			continue
		}
		switch key {
		case "branch":
			sw.Branch = value
		case "source_issue", "source-issue", "sourceissue":
			sw.IssueID = value
		case "commit_sha", "commit-sha", "commitsha":
			sw.CommitSHA = value
		case "worker":
			sw.Worker = value
		case "rig":
			sw.Rig = value
		}
	}
	return sw
}

// --- per-wisp attempt tracking ---------------------------------------------
//
// Attempts are tracked in-memory per daemon process. A daemon restart re-arms
// the budget, which is the desired behavior: a fresh process should re-attempt
// recovery (the failure may have been a transient Dolt/network blip) before
// giving up again. The map is keyed rig+wispID so two rigs never collide.

func (d *Daemon) strandedAttemptKey(rigName, wispID string) string {
	return rigName + "/" + wispID
}

func (d *Daemon) strandedAttemptCount(rigName, wispID string) int {
	d.pushStrandedMu.Lock()
	defer d.pushStrandedMu.Unlock()
	if d.pushStrandedAttempts == nil {
		return 0
	}
	return d.pushStrandedAttempts[d.strandedAttemptKey(rigName, wispID)]
}

func (d *Daemon) recordStrandedAttempt(rigName, wispID string) {
	d.pushStrandedMu.Lock()
	defer d.pushStrandedMu.Unlock()
	if d.pushStrandedAttempts == nil {
		d.pushStrandedAttempts = map[string]int{}
	}
	d.pushStrandedAttempts[d.strandedAttemptKey(rigName, wispID)]++
}

func (d *Daemon) clearStrandedAttempts(rigName, wispID string) {
	d.pushStrandedMu.Lock()
	defer d.pushStrandedMu.Unlock()
	if d.pushStrandedAttempts != nil {
		delete(d.pushStrandedAttempts, d.strandedAttemptKey(rigName, wispID))
	}
}

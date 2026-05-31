package witness

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/tmux"
)

// pushFailedTestSetup builds a workspace-shaped temp dir and configures the
// nudge log so any mayor PUSH_FAILED nudges are captured rather than sent to
// a live tmux session. Returns the (townRoot, workDir) pair.
//
// Mirrors push_failed_nudge_isolation_test.go's setup; centralised so the
// per-outcome cases below stay focused on the assertion.
func pushFailedTestSetup(t *testing.T) (townRoot, workDir string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv(tmux.EnvTestNudgeLog, logPath)

	townRoot = t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	workDir = filepath.Join(townRoot, "gastown", "witness")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return townRoot, workDir
}

// withStubbedRecovery installs a stub for recoverPushFailed that returns the
// requested outcome and restores the previous binding when the test ends.
// Recovery budget is reset before and after so cap state from earlier tests
// can't leak in (or out).
func withStubbedRecovery(t *testing.T, outcome PushRecoveryOutcome) {
	t.Helper()
	resetPushRecoveryBudget()
	prev := recoverPushFailed
	recoverPushFailed = func(_ string, _, _, _ string) PushRecoveryOutcome {
		return outcome
	}
	t.Cleanup(func() {
		recoverPushFailed = prev
		resetPushRecoveryBudget()
	})
}

// pushFailedFields builds the canonical PushFailed=true agent fields used by
// the per-outcome cases below.
func pushFailedFields() *beads.AgentFields {
	return &beads.AgentFields{
		ExitType:        "FAILED",
		Branch:          "polecat/deathclaw/lost",
		HookBead:        "gt-test-issue",
		LastSourceIssue: "gt-test-issue",
		PushFailed:      true,
		CompletionTime:  "2026-05-31T05:00:00Z",
	}
}

// TestHandlePolecatDoneFromBead_PushFailed_Diverged_Escalates verifies that
// when the recovery handler reports incompatible histories, the witness
// preserves the existing escalate-to-mayor behavior with an action label
// that describes the divergence.
func TestHandlePolecatDoneFromBead_PushFailed_Diverged_Escalates(t *testing.T) {
	withStubbedRecovery(t, PushRecoveryDiverged)
	_, workDir := pushFailedTestSetup(t)

	result := HandlePolecatDoneFromBead(DefaultBdCli(), workDir, "gastown", "deathclaw", pushFailedFields(), nil)

	if !result.Handled {
		t.Fatalf("expected Handled=true on diverged outcome, got result=%+v", result)
	}
	if !strings.Contains(result.Action, "push-failed-recovery-diverged") {
		t.Errorf("expected diverged action label, got %q", result.Action)
	}
}

// TestHandlePolecatDoneFromBead_PushFailed_Backoff_Escalates verifies that
// when the per-branch retry budget is exhausted, the witness still routes
// through the escalate path (mayor needs to know we've stopped trying).
func TestHandlePolecatDoneFromBead_PushFailed_Backoff_Escalates(t *testing.T) {
	withStubbedRecovery(t, PushRecoveryBackoff)
	_, workDir := pushFailedTestSetup(t)

	result := HandlePolecatDoneFromBead(DefaultBdCli(), workDir, "gastown", "deathclaw", pushFailedFields(), nil)

	if !result.Handled {
		t.Fatalf("expected Handled=true on backoff outcome, got result=%+v", result)
	}
	if !strings.Contains(result.Action, "push-failed-recovery-backoff") {
		t.Errorf("expected backoff action label, got %q", result.Action)
	}
}

// TestHandlePolecatDoneFromBead_PushFailed_Unknown_Escalates verifies that
// when recovery couldn't be attempted (worktree missing, ls-remote failed,
// etc) the witness preserves the original escalate-to-mayor behavior.
func TestHandlePolecatDoneFromBead_PushFailed_Unknown_Escalates(t *testing.T) {
	withStubbedRecovery(t, PushRecoveryUnknown)
	_, workDir := pushFailedTestSetup(t)

	result := HandlePolecatDoneFromBead(DefaultBdCli(), workDir, "gastown", "deathclaw", pushFailedFields(), nil)

	if !result.Handled {
		t.Fatalf("expected Handled=true on unknown outcome, got result=%+v", result)
	}
	if !strings.Contains(result.Action, "push-failed-recovery-unknown") {
		t.Errorf("expected unknown action label, got %q", result.Action)
	}
}

// TestHandlePolecatDoneFromBead_PushFailed_Pushed_FallsThrough verifies the
// success path: when recovery successfully pushes the branch the action
// label records the recovery and the handler routes through normal
// completion (Handled is set by the downstream completion routing, not by
// the recovery branch returning early).
func TestHandlePolecatDoneFromBead_PushFailed_Pushed_FallsThrough(t *testing.T) {
	withStubbedRecovery(t, PushRecoveryPushed)
	_, workDir := pushFailedTestSetup(t)

	fields := pushFailedFields()
	fields.ExitType = "COMPLETED" // exercise the no-MR completion path

	result := HandlePolecatDoneFromBead(DefaultBdCli(), workDir, "gastown", "deathclaw", fields, nil)

	// The recovery branch DOES NOT return early on Pushed/AlreadyOnOrigin —
	// it falls through to the existing routing, which handles the COMPLETED
	// payload. We assert the action label was set by recovery and that the
	// handler did NOT short-circuit with the divergence-style escalate.
	if !strings.Contains(result.Action, "push-failed-recovery-pushed") {
		t.Errorf("expected pushed action label to survive fallthrough, got %q", result.Action)
	}
	if strings.Contains(result.Action, "diverged") || strings.Contains(result.Action, "backoff") {
		t.Errorf("pushed outcome should not produce a diverge/backoff action, got %q", result.Action)
	}
}

// TestHandlePolecatDoneFromBead_PushFailed_AlreadyOnOrigin_FallsThrough is
// the symmetric case to Pushed: a race-safe re-check showed origin already
// has the branch, so we clear the sticky flag and continue routing.
func TestHandlePolecatDoneFromBead_PushFailed_AlreadyOnOrigin_FallsThrough(t *testing.T) {
	withStubbedRecovery(t, PushRecoveryAlreadyOnOrigin)
	_, workDir := pushFailedTestSetup(t)

	fields := pushFailedFields()
	fields.ExitType = "COMPLETED"

	result := HandlePolecatDoneFromBead(DefaultBdCli(), workDir, "gastown", "deathclaw", fields, nil)

	if !strings.Contains(result.Action, "push-failed-recovery-already-on-origin") {
		t.Errorf("expected already-on-origin action label, got %q", result.Action)
	}
}

// TestHandlePolecatDoneFromBead_PushFailed_PatchEquivalent_FallsThrough is
// the gu-l0u0 regression: the rebase-after-push pattern produces textual
// divergence with patch-equivalent content. The handler must treat this like
// AlreadyOnOrigin/Pushed — clear push_failed and route through normal
// completion. Before gu-l0u0 this fell into the `default:` arm and escalated
// to mayor as PUSH_FAILED with possible-work-loss, leaving the bead stranded.
func TestHandlePolecatDoneFromBead_PushFailed_PatchEquivalent_FallsThrough(t *testing.T) {
	withStubbedRecovery(t, PushRecoveryPatchEquivalent)
	_, workDir := pushFailedTestSetup(t)

	fields := pushFailedFields()
	fields.ExitType = "COMPLETED"

	result := HandlePolecatDoneFromBead(DefaultBdCli(), workDir, "gastown", "deathclaw", fields, nil)

	if !strings.Contains(result.Action, "push-failed-recovery-patch-equivalent") {
		t.Errorf("expected patch-equivalent action label, got %q", result.Action)
	}
	// Must NOT be classified as a divergence escalate — that was the gu-l0u0 bug.
	if strings.Contains(result.Action, "diverged") {
		t.Errorf("patch-equivalent must not collapse into 'diverged' (gu-l0u0 regression): %q", result.Action)
	}
}

// TestRecoverPushFailed_BudgetCap_TripsBackoff verifies that the in-process
// per-branch retry cap is enforced. After pushRecoveryMaxAttempts successive
// calls for the same (rig, polecat, branch), the next attempt returns
// PushRecoveryBackoff regardless of what the underlying git ops would do.
//
// We exercise the real _recoverPushFailed (not the stub) so this also serves
// as the budget-accounting regression test: a future refactor that moves
// the chargePushRecovery call could silently uncap the loop, and this test
// would catch it.
func TestRecoverPushFailed_BudgetCap_TripsBackoff(t *testing.T) {
	resetPushRecoveryBudget()
	t.Cleanup(resetPushRecoveryBudget)

	// townRoot points at a non-existent path so polecatWorktreePath will
	// stat-miss → PushRecoveryUnknown. That is sufficient: each call still
	// charges the budget (per the chargePushRecovery contract — see
	// push_failed_recovery.go), so after MaxAttempts further calls flip to
	// Backoff. We are testing the cap, not the git path.
	bogusTownRoot := filepath.Join(t.TempDir(), "no-such-town")

	for i := 0; i < pushRecoveryMaxAttempts; i++ {
		got := _recoverPushFailed(bogusTownRoot, "gastown", "deathclaw", "polecat/deathclaw/lost")
		if got != PushRecoveryUnknown {
			t.Fatalf("attempt %d: expected Unknown (worktree absent), got %s", i+1, got)
		}
	}
	// Cap exhausted — the next call MUST short-circuit to Backoff before
	// reaching the worktree stat (otherwise a stuck rig spins forever).
	got := _recoverPushFailed(bogusTownRoot, "gastown", "deathclaw", "polecat/deathclaw/lost")
	if got != PushRecoveryBackoff {
		t.Errorf("expected Backoff after %d attempts, got %s", pushRecoveryMaxAttempts, got)
	}
}

// TestRecoverPushFailed_BudgetIsPerBranch verifies that the cap is keyed by
// branch — exhausting one branch's budget does NOT block a different branch
// on the same polecat.
func TestRecoverPushFailed_BudgetIsPerBranch(t *testing.T) {
	resetPushRecoveryBudget()
	t.Cleanup(resetPushRecoveryBudget)

	bogusTownRoot := filepath.Join(t.TempDir(), "no-such-town")
	const polecatName = "deathclaw"
	const branchA = "polecat/deathclaw/branch-a"
	const branchB = "polecat/deathclaw/branch-b"

	// Exhaust branch A.
	for i := 0; i < pushRecoveryMaxAttempts; i++ {
		_ = _recoverPushFailed(bogusTownRoot, "gastown", polecatName, branchA)
	}
	if got := _recoverPushFailed(bogusTownRoot, "gastown", polecatName, branchA); got != PushRecoveryBackoff {
		t.Fatalf("branchA: expected Backoff, got %s", got)
	}
	// Branch B should still be allowed.
	if got := _recoverPushFailed(bogusTownRoot, "gastown", polecatName, branchB); got == PushRecoveryBackoff {
		t.Errorf("branchB: expected non-Backoff (independent budget), got Backoff")
	}
}

// TestPushRecoveryOutcome_String covers the action-string contract — these
// values are embedded in HandlerResult.Action and CompletionDiscovery.Action,
// which the daemon and operator log scrapers match on. A typo here would
// silently desync those surfaces from the witness.
func TestPushRecoveryOutcome_String(t *testing.T) {
	cases := []struct {
		outcome PushRecoveryOutcome
		want    string
	}{
		{PushRecoveryUnknown, "unknown"},
		{PushRecoveryAlreadyOnOrigin, "already-on-origin"},
		{PushRecoveryPushed, "pushed"},
		{PushRecoveryDiverged, "diverged"},
		{PushRecoveryBackoff, "backoff"},
		{PushRecoveryPatchEquivalent, "patch-equivalent"},
	}
	for _, tc := range cases {
		if got := tc.outcome.String(); got != tc.want {
			t.Errorf("outcome %d: got %q, want %q", tc.outcome, got, tc.want)
		}
	}
}

// fakeCherryChecker drives cherryAllShippedOrOnMainline without a real git
// repo. The mainlineSet is the set of SHAs treated as ancestors of mainline.
type fakeCherryChecker struct {
	out          string
	err          error
	call         int
	mainlineSet  map[string]bool
	ancestorErr  error
	ancestorCall int
}

func (f *fakeCherryChecker) Cherry(upstream, head string) (string, error) {
	f.call++
	return f.out, f.err
}

func (f *fakeCherryChecker) IsAncestor(ancestor, descendant string) (bool, error) {
	f.ancestorCall++
	if f.ancestorErr != nil {
		return false, f.ancestorErr
	}
	return f.mainlineSet[ancestor], nil
}

// TestCherryAllShippedOrOnMainline verifies the patch-equivalence decision
// matrix for gu-l0u0. A "+ <sha>" line means the patch is NOT on
// origin/<branch>; we accept it only when the SHA is already on mainline
// (rebase swept it in). "-" means patch-equivalent and is always accepted.
// Mis-classifying leads to either silent work loss (false-positive) or
// zombie strands (false-negative).
func TestCherryAllShippedOrOnMainline(t *testing.T) {
	cases := []struct {
		name        string
		out         string
		err         error
		mainlineSet map[string]bool
		ancestorErr error
		want        bool
	}{
		{
			name: "all patches already on upstream (post-rebase replay)",
			out:  "- abcdef0\n- 1234567\n- fedcba9\n",
			want: true,
		},
		{
			name: "no commits at all (HEAD == merge-base)",
			out:  "",
			want: true,
		},
		{
			name: "single unmerged patch with no mainline membership — divergence",
			out:  "+ deadbeef\n",
			want: false,
		},
		{
			name:        "unmerged patch reachable from mainline — rebase incorporation",
			out:         "+ deadbeef\n- abcdef0\n",
			mainlineSet: map[string]bool{"deadbeef": true},
			want:        true,
		},
		{
			name:        "mix of mainline-incorporated and unique-work — divergence wins",
			out:         "+ deadbeef\n+ uniquefe\n",
			mainlineSet: map[string]bool{"deadbeef": true},
			want:        false,
		},
		{
			name: "cherry returned an error — conservative false",
			out:  "- abcdef0\n",
			err:  errors.New("ls-remote refused"),
			want: false,
		},
		{
			name:        "ancestor check errors — conservative false",
			out:         "+ deadbeef\n",
			mainlineSet: map[string]bool{"deadbeef": true},
			ancestorErr: errors.New("revision unknown"),
			want:        false,
		},
		{
			name:        "leading whitespace on lines is tolerated",
			out:         "  - abcdef0\n  - 1234567\n",
			mainlineSet: nil,
			want:        true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeCherryChecker{
				out:         tc.out,
				err:         tc.err,
				mainlineSet: tc.mainlineSet,
				ancestorErr: tc.ancestorErr,
			}
			if got := cherryAllShippedOrOnMainline(fake, "origin/feature", "HEAD", "origin/main"); got != tc.want {
				t.Errorf("got %v, want %v (cherry=%q err=%v)", got, tc.want, tc.out, tc.err)
			}
			if tc.err == nil && fake.call != 1 {
				t.Errorf("expected exactly 1 Cherry call, got %d", fake.call)
			}
		})
	}
}

// TestCherryAllShippedOrOnMainline_NilGuards verifies the guards against bogus
// inputs. A typo in the caller (passing "" for upstream or head) must not
// silently return true — that would short-circuit recovery into "patch-
// equivalent" when nothing was actually checked.
func TestCherryAllShippedOrOnMainline_NilGuards(t *testing.T) {
	if cherryAllShippedOrOnMainline(nil, "origin/x", "HEAD", "origin/main") {
		t.Error("nil git should not be patch-equivalent")
	}
	fake := &fakeCherryChecker{out: ""}
	if cherryAllShippedOrOnMainline(fake, "", "HEAD", "origin/main") {
		t.Error("empty upstream should not be patch-equivalent")
	}
	if cherryAllShippedOrOnMainline(fake, "origin/x", "", "origin/main") {
		t.Error("empty head should not be patch-equivalent")
	}
	if fake.call != 0 {
		t.Errorf("guard cases must not invoke Cherry, got %d calls", fake.call)
	}

	// Empty mainlineRef + a "+" line cannot be justified — must reject.
	fakeWithUnmerged := &fakeCherryChecker{out: "+ deadbeef\n"}
	if cherryAllShippedOrOnMainline(fakeWithUnmerged, "origin/x", "HEAD", "") {
		t.Error("unmerged patch with no mainline ref should not be patch-equivalent")
	}
}

// TestRecoverPushFailed_RebaseAfterPush_PatchEquivalent reproduces the
// gu-l0u0 scenario end-to-end against a real git repo: branch was pushed,
// then locally rebased onto a moved base, producing different SHAs that share
// patch-ids with origin. _recoverPushFailed must return PatchEquivalent.
func TestRecoverPushFailed_RebaseAfterPush_PatchEquivalent(t *testing.T) {
	resetPushRecoveryBudget()
	t.Cleanup(resetPushRecoveryBudget)

	tmp := t.TempDir()
	townRoot := filepath.Join(tmp, "town")
	rigName := "gastown"
	polecatName := "fury"
	branch := "polecat/fury/gu-36voy--mpruma8a"

	// Bare "remote" repo to push to.
	remoteRepo := filepath.Join(tmp, "remote.git")
	runGitT(t, tmp, "init", "--bare", remoteRepo)

	// Polecat worktree path (matches polecatWorktreePath fallback layout).
	worktree := filepath.Join(townRoot, rigName, "polecats", polecatName)
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitT(t, tmp, "init", "--initial-branch", "main", worktree)
	runGitT(t, worktree, "config", "user.email", "test@test.com")
	runGitT(t, worktree, "config", "user.name", "Test")
	runGitT(t, worktree, "remote", "add", "origin", remoteRepo)

	// Initial main commit, push to remote.
	writeFileT(t, worktree, "README.md", "# initial\n")
	runGitT(t, worktree, "add", ".")
	runGitT(t, worktree, "commit", "-m", "initial")
	runGitT(t, worktree, "push", "-u", "origin", "main")

	// Polecat branches off and creates a feature commit, pushes it.
	runGitT(t, worktree, "checkout", "-b", branch)
	writeFileT(t, worktree, "feature.txt", "feature work\n")
	runGitT(t, worktree, "add", ".")
	runGitT(t, worktree, "commit", "-m", "feature")
	runGitT(t, worktree, "push", "-u", "origin", branch)

	// Origin now has the polecat branch at SHA-A. Capture it for sanity.
	originSHA := strings.TrimSpace(runGitOutT(t, worktree, "rev-parse", "HEAD"))

	// Move main forward (simulates another polecat's work landing on mainline).
	runGitT(t, worktree, "checkout", "main")
	writeFileT(t, worktree, "main-new.txt", "new on main\n")
	runGitT(t, worktree, "add", ".")
	runGitT(t, worktree, "commit", "-m", "advance main")
	runGitT(t, worktree, "push", "origin", "main")

	// Polecat returns to its branch and rebases onto the new main. This
	// rewrites SHA-A → SHA-B with the same patch content.
	runGitT(t, worktree, "checkout", branch)
	runGitT(t, worktree, "rebase", "main")
	rebasedSHA := strings.TrimSpace(runGitOutT(t, worktree, "rev-parse", "HEAD"))

	if rebasedSHA == originSHA {
		t.Fatalf("test setup bug: rebase did not rewrite SHA (origin=%s rebased=%s)", originSHA, rebasedSHA)
	}

	// At this point: HEAD=SHA-B, origin/<branch>=SHA-A. Neither is an ancestor
	// of the other (rebase rewrote history). The pre-fix behavior was to
	// return PushRecoveryDiverged → escalate to mayor. With gu-l0u0 the
	// cherry check sees zero unmerged patches and returns PatchEquivalent.
	got := _recoverPushFailed(townRoot, rigName, polecatName, branch)
	if got != PushRecoveryPatchEquivalent {
		t.Errorf("expected PushRecoveryPatchEquivalent for rebase-after-push, got %s", got)
	}
}

// TestRecoverPushFailed_GenuineDivergenceStaysDiverged is the false-positive
// guard: a branch with a unique local commit that origin does NOT have must
// keep returning Diverged. If patch-equivalence ever reports true here we
// would silently lose that local commit.
func TestRecoverPushFailed_GenuineDivergenceStaysDiverged(t *testing.T) {
	resetPushRecoveryBudget()
	t.Cleanup(resetPushRecoveryBudget)

	tmp := t.TempDir()
	townRoot := filepath.Join(tmp, "town")
	rigName := "gastown"
	polecatName := "fury"
	branch := "polecat/fury/feature"

	remoteRepo := filepath.Join(tmp, "remote.git")
	runGitT(t, tmp, "init", "--bare", remoteRepo)

	worktree := filepath.Join(townRoot, rigName, "polecats", polecatName)
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitT(t, tmp, "init", "--initial-branch", "main", worktree)
	runGitT(t, worktree, "config", "user.email", "test@test.com")
	runGitT(t, worktree, "config", "user.name", "Test")
	runGitT(t, worktree, "remote", "add", "origin", remoteRepo)

	writeFileT(t, worktree, "README.md", "# initial\n")
	runGitT(t, worktree, "add", ".")
	runGitT(t, worktree, "commit", "-m", "initial")
	runGitT(t, worktree, "push", "-u", "origin", "main")

	// Polecat branches off, commits, pushes.
	runGitT(t, worktree, "checkout", "-b", branch)
	writeFileT(t, worktree, "feature-a.txt", "alpha work\n")
	runGitT(t, worktree, "add", ".")
	runGitT(t, worktree, "commit", "-m", "feature alpha")
	runGitT(t, worktree, "push", "-u", "origin", branch)

	// Now mutate locally on a different file path AND mutate origin
	// independently. Local has a unique patch origin doesn't have.
	writeFileT(t, worktree, "feature-b.txt", "beta work\n")
	runGitT(t, worktree, "add", ".")
	runGitT(t, worktree, "commit", "-m", "feature beta — only on local")

	// Move origin forward via a different worktree (simulate another agent).
	otherWT := filepath.Join(tmp, "other")
	runGitT(t, tmp, "clone", remoteRepo, otherWT)
	runGitT(t, otherWT, "config", "user.email", "test@test.com")
	runGitT(t, otherWT, "config", "user.name", "Test")
	runGitT(t, otherWT, "checkout", branch)
	writeFileT(t, otherWT, "feature-c.txt", "gamma work — only on origin\n")
	runGitT(t, otherWT, "add", ".")
	runGitT(t, otherWT, "commit", "-m", "feature gamma — diverging")
	runGitT(t, otherWT, "push", "origin", branch)

	// Now local has commits A+B, origin has commits A+C. A is patch-equivalent
	// (same on both), but B is only local — recovery must NOT lose it.
	got := _recoverPushFailed(townRoot, rigName, polecatName, branch)
	if got != PushRecoveryDiverged {
		t.Errorf("expected PushRecoveryDiverged for genuine divergence, got %s — would lose local commit", got)
	}
}

// runGitT and helpers — local copies so push_failed_recovery_test.go does not
// depend on test helpers in unrelated files.
func runGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func runGitOutT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return string(out)
}

func writeFileT(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

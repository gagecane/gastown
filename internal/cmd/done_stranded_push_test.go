package cmd

// Tests for gu-epv5: `gt done` must not silently strand work when push
// reports failure. Two recovery layers are exercised here:
//
//   Option C (recoverPushFromOriginTip): re-check origin/<branch> directly
//   after a push or verify reports failure. If the tip matches the expected
//   commit SHA, the push really did land — proceed to MR creation instead
//   of going straight to notifyWitness.
//
//   Option B (fileStrandedPushWisp): when recovery fails, file a
//   discoverable wisp labeled `gt:push-stranded` so the work isn't
//   invisible. Operator visibility via `gt mq list` and witness/mayor
//   sweeps is the safety net behind the merge queue.
//
// These tests exercise the small, well-scoped helpers added in done.go.
// End-to-end push-fail-then-recover is covered by integration tests.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// setupBareWithBranch creates a bare remote with a single feature branch
// and a worktree clone whose HEAD points at the same commit as origin's
// feature-branch tip. Returns (worktreePath, expectedSHA, branch).
func setupBareWithBranch(t *testing.T, branch string) (string, string, string) {
	t.Helper()
	bare := t.TempDir()
	for _, args := range [][]string{
		{"init", "--bare", "-b", "main"},
	} {
		c := exec.Command("git", args...)
		c.Dir = bare
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v in bare: %v\n%s", args, err, out)
		}
	}

	wt := t.TempDir()
	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	runIn := func(dir string, args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = gitEnv
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
		return string(out)
	}
	runIn(wt, "init", "-b", "main")
	runIn(wt, "config", "user.email", "test@example.com")
	runIn(wt, "config", "user.name", "Test")
	runIn(wt, "remote", "add", "origin", bare)

	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(wt, "add", "README.md")
	runIn(wt, "commit", "-m", "seed")
	runIn(wt, "push", "origin", "main")

	// Create a feature branch and add a commit
	runIn(wt, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(wt, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(wt, "add", "feature.txt")
	runIn(wt, "commit", "-m", "feat: add feature (gu-epv5)")
	runIn(wt, "push", "origin", branch)

	sha := runIn(wt, "rev-parse", "HEAD")
	// rev-parse output has trailing newline
	for len(sha) > 0 && (sha[len(sha)-1] == '\n' || sha[len(sha)-1] == ' ') {
		sha = sha[:len(sha)-1]
	}
	return wt, sha, branch
}

// TestRecoverPushFromOriginTip_MatchingTipReturnsTrue verifies the happy
// recovery path: after a (simulated) push failure, the remote branch tip
// matches the expected SHA — recoverPushFromOriginTip must return true so
// the caller proceeds to MR creation.
func TestRecoverPushFromOriginTip_MatchingTipReturnsTrue(t *testing.T) {
	wt, sha, branch := setupBareWithBranch(t, "feature/gu-epv5-recover")
	g := git.NewGit(wt)

	if !recoverPushFromOriginTip(g, branch, sha) {
		t.Fatalf("expected recoverPushFromOriginTip to return true when origin tip matches HEAD SHA")
	}
}

// TestRecoverPushFromOriginTip_MismatchReturnsFalse verifies that when
// the remote branch tip is at a different SHA than expected, recovery
// must fail — the push genuinely didn't land. Without this the helper
// would fall back to a permissive "branch exists" check, which would
// cause us to file an MR for a stale remote SHA.
func TestRecoverPushFromOriginTip_MismatchReturnsFalse(t *testing.T) {
	wt, _, branch := setupBareWithBranch(t, "feature/gu-epv5-mismatch")
	g := git.NewGit(wt)

	wrong := "0000000000000000000000000000000000000000"
	if recoverPushFromOriginTip(g, branch, wrong) {
		t.Fatalf("expected recoverPushFromOriginTip to return false when tip != expected SHA")
	}
}

// TestRecoverPushFromOriginTip_MissingBranchReturnsFalse covers the case
// where the push truly failed and no remote branch exists. The helper
// must report no recovery so the caller falls through to filing a
// stranded-push wisp.
func TestRecoverPushFromOriginTip_MissingBranchReturnsFalse(t *testing.T) {
	wt, sha, _ := setupBareWithBranch(t, "feature/gu-epv5-present")
	g := git.NewGit(wt)

	if recoverPushFromOriginTip(g, "feature/never-pushed", sha) {
		t.Fatalf("expected false when remote branch does not exist")
	}
}

// TestRecoverPushFromOriginTip_RejectsEmptyInputs verifies the helper
// fails closed on degenerate inputs: an empty SHA could come from a
// reflog read failure, and we must not treat "tip == ”" as a match.
func TestRecoverPushFromOriginTip_RejectsEmptyInputs(t *testing.T) {
	wt, _, branch := setupBareWithBranch(t, "feature/gu-epv5-empty")
	g := git.NewGit(wt)

	if recoverPushFromOriginTip(g, branch, "") {
		t.Errorf("empty expectedSHA must not match any remote tip")
	}
	if recoverPushFromOriginTip(g, "", "abc123") {
		t.Errorf("empty branch must not match any remote tip")
	}
	if recoverPushFromOriginTip(nil, branch, "abc123") {
		t.Errorf("nil git client must not panic and must return false")
	}
}

// TestRecoverPushFromOriginTip_NoRemoteReturnsFalse exercises the
// remote-read-error branch: if git ls-remote fails (network, missing
// remote, etc.), the helper must report "could not confirm" rather than
// claim success or panic. The integration scenario is a transient
// connectivity blip during gt done — we must not silently treat an
// unreachable origin as proof of a successful push.
func TestRecoverPushFromOriginTip_NoRemoteReturnsFalse(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	g := git.NewGit(dir)
	if recoverPushFromOriginTip(g, "any-branch", "abc123") {
		t.Errorf("missing remote must not be treated as successful push")
	}
}

// errorMatches checks that err is non-nil and its Error string contains s.
func errorMatches(err error, s string) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, errors.New(s)) || (err.Error() != "" && containsString(err.Error(), s))
}

func containsString(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestErrorMatches_Sanity ensures the test helper actually works.
func TestErrorMatches_Sanity(t *testing.T) {
	if !errorMatches(errors.New("verified_push_failed: tip mismatch"), "tip mismatch") {
		t.Errorf("errorMatches helper broken")
	}
	if errorMatches(nil, "anything") {
		t.Errorf("errorMatches must return false for nil error")
	}
}

package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// TestVerifyCommitReferencesBead_AcceptsMatchingCommit verifies the happy
// path: a commit whose message contains the bead ID is accepted as evidence
// that the work shipped. This is what legitimate `gt done` no-MR closes look
// like — the polecat that landed the work used a conventional-commit subject
// like `fix(foo): bar (gu-abc)` and we honor that. (gu-551r)
func TestVerifyCommitReferencesBead_AcceptsMatchingCommit(t *testing.T) {
	repo := newTestGitRepo(t)
	sha := commitWithMessage(t, repo, "fix(foo): bar (gu-551r)\n\nbody mentioning gu-551r explicitly")

	g := git.NewGit(repo)
	if err := verifyCommitReferencesBead(g, sha, "gu-551r"); err != nil {
		t.Fatalf("verifyCommitReferencesBead rejected a matching commit: %v", err)
	}
}

// TestVerifyCommitReferencesBead_RejectsUnrelatedCommit reproduces the
// gu-551r false-close pattern: a polecat with no commits of its own about
// to close citing whatever HEAD happens to be — which is a sibling
// polecat's work for a different bead. The guard must refuse and surface
// a descriptive error so the polecat falls through to ESCALATED/DEFERRED.
// (gu-551r)
func TestVerifyCommitReferencesBead_RejectsUnrelatedCommit(t *testing.T) {
	repo := newTestGitRepo(t)
	// Sibling polecat's commit, citing a different bead.
	sha := commitWithMessage(t, repo, "fix(beads): omit BEADS_DOLT_DATA_DIR when rig uses server mode (gu-6a68)")

	g := git.NewGit(repo)
	err := verifyCommitReferencesBead(g, sha, "gu-9qwk")
	if err == nil {
		t.Fatal("verifyCommitReferencesBead accepted a commit that doesn't reference the bead — false-close gate broken")
	}
	msg := err.Error()
	for _, want := range []string{"gu-9qwk", "does not reference"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %q", want, msg)
		}
	}
}

// TestVerifyCommitReferencesBead_FailsClosedOnEmptyInputs verifies the
// guard fails closed (returns an error) when given an empty bead ID,
// empty SHA, or unreadable commit. This matters because the false-close
// path is triggered by an unusual state where the polecat has no
// legitimate evidence — we must never silently accept and wave through.
// (gu-551r)
func TestVerifyCommitReferencesBead_FailsClosedOnEmptyInputs(t *testing.T) {
	repo := newTestGitRepo(t)
	g := git.NewGit(repo)

	if err := verifyCommitReferencesBead(g, "abc123", ""); err == nil {
		t.Error("empty bead ID should fail closed")
	}
	if err := verifyCommitReferencesBead(g, "", "gu-551r"); err == nil {
		t.Error("empty commit SHA should fail closed")
	}
	if err := verifyCommitReferencesBead(g, "0000000000000000000000000000000000000000", "gu-551r"); err == nil {
		t.Error("unreadable commit SHA should fail closed")
	}
}

// TestVerifyCommitReferencesBead_AcceptsBeadInBody verifies that the
// match is anywhere in the commit message — body counts, not just
// subject. Some polecats put the bead reference in a `Refs:` trailer.
// (gu-551r)
func TestVerifyCommitReferencesBead_AcceptsBeadInBody(t *testing.T) {
	repo := newTestGitRepo(t)
	sha := commitWithMessage(t, repo, "feat: add new thing\n\nRefs: gu-551r\n")

	g := git.NewGit(repo)
	if err := verifyCommitReferencesBead(g, sha, "gu-551r"); err != nil {
		t.Errorf("commit with bead ID in body should be accepted, got: %v", err)
	}
}

// --- Test helpers ---

// newTestGitRepo creates a temp git repo with one initial commit and returns
// its path. Identity is set to a fixed test user so commits are deterministic.
func newTestGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		// Provide a clean, deterministic env so user-level git config doesn't
		// affect identity (especially in CI sandboxes).
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	run("git", "init", "-q")
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	run("git", "add", "README.md")
	run("git", "commit", "-q", "-m", "seed")
	return repo
}

// commitWithMessage adds an empty change and creates a commit with the
// given message in the given repo. Returns the resulting commit SHA.
func commitWithMessage(t *testing.T, repo, message string) string {
	t.Helper()
	// Append a unique line so the commit is not empty (avoids needing
	// --allow-empty which interacts oddly with hooks).
	path := filepath.Join(repo, "marker.txt")
	data, _ := os.ReadFile(path)
	data = append(data, []byte(message+"\n")...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	cmds := [][]string{
		{"git", "add", "marker.txt"},
		{"git", "commit", "-q", "-m", message},
		{"git", "rev-parse", "HEAD"},
	}
	var sha string
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		if args[1] == "rev-parse" {
			sha = strings.TrimSpace(string(out))
		}
	}
	return sha
}

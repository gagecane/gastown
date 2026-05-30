package git

import (
	"strings"
	"testing"
)

// TestLogGrepFixedHead_FindsCitingCommit verifies that a real git invocation
// with --grep + --fixed-strings returns the SHA of a citing commit. Uses a
// throwaway repo seeded with two commits — only one cites the bead ID.
func TestLogGrepFixedHead_FindsCitingCommit(t *testing.T) {
	repo := t.TempDir()
	g := NewGit(repo)
	if _, err := g.run("init", "-b", "main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if _, err := g.run("config", "user.email", "test@local"); err != nil {
		t.Fatalf("git config user.email: %v", err)
	}
	if _, err := g.run("config", "user.name", "Test"); err != nil {
		t.Fatalf("git config user.name: %v", err)
	}
	if _, err := g.run("commit", "--allow-empty", "-m", "feat: unrelated change"); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if _, err := g.run("commit", "--allow-empty", "-m", "fix: do the thing (gu-test1)"); err != nil {
		t.Fatalf("citing commit: %v", err)
	}

	sha, err := g.LogGrepFixedHead("main", "gu-test1")
	if err != nil {
		t.Fatalf("LogGrepFixedHead: %v", err)
	}
	sha = strings.TrimSpace(sha)
	if len(sha) < 40 {
		t.Errorf("sha = %q, want a 40-char SHA", sha)
	}
}

// TestLogGrepFixedHead_NoMatchReturnsEmpty verifies that a needle that does
// not appear in any commit message returns an empty string (and no error).
func TestLogGrepFixedHead_NoMatchReturnsEmpty(t *testing.T) {
	repo := t.TempDir()
	g := NewGit(repo)
	if _, err := g.run("init", "-b", "main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if _, err := g.run("config", "user.email", "test@local"); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if _, err := g.run("config", "user.name", "Test"); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if _, err := g.run("commit", "--allow-empty", "-m", "feat: unrelated change"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	sha, err := g.LogGrepFixedHead("main", "gu-doesnotexist")
	if err != nil {
		t.Fatalf("LogGrepFixedHead: %v", err)
	}
	if strings.TrimSpace(sha) != "" {
		t.Errorf("sha = %q, want empty", sha)
	}
}

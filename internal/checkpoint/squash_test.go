package checkpoint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTestRepo creates a fresh git repo with an initial commit and returns its path.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args[1:], err, out)
		}
	}

	// Create initial commit on main
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", "initial commit"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args[1:], err, out)
		}
	}

	return dir
}

// createBranch creates a branch from current HEAD and switches to it.
func createBranch(t *testing.T, dir, branch string) {
	t.Helper()
	cmd := exec.Command("git", "checkout", "-b", branch)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkout -b %s failed: %v\n%s", branch, err, out)
	}
}

// addCommit adds a file and commits with the given message.
func addCommit(t *testing.T, dir, filename, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", filename},
		{"git", "commit", "-m", msg},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args[1:], err, out)
		}
	}
}

// getCommitSubjects returns the commit subjects on the branch since main.
func getCommitSubjects(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("git", "log", "--format=%s", "main..HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

func TestCountWIPCommits_NoWIP(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", "add feature A")
	addCommit(t, dir, "b.go", "package b", "add feature B")

	count, err := CountWIPCommits(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0 WIP commits, got %d", count)
	}
}

func TestCountWIPCommits_AllWIP(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", WIPCommitPrefix)
	addCommit(t, dir, "b.go", "package b", WIPCommitPrefix)

	count, err := CountWIPCommits(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("expected 2 WIP commits, got %d", count)
	}
}

func TestCountWIPCommits_Mixed(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", "real work")
	addCommit(t, dir, "b.go", "package b", WIPCommitPrefix)
	addCommit(t, dir, "c.go", "package c", "more real work")

	count, err := CountWIPCommits(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 WIP commit, got %d", count)
	}
}

func TestSquashWIPCommits_NoWIP(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", "real work")

	wipCount, err := SquashWIPCommits(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if wipCount != 0 {
		t.Errorf("expected 0, got %d", wipCount)
	}

	// Verify commit is untouched
	subjects := getCommitSubjects(t, dir)
	if len(subjects) != 1 || subjects[0] != "real work" {
		t.Errorf("expected [real work], got %v", subjects)
	}
}

func TestSquashWIPCommits_AllWIP(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", WIPCommitPrefix)
	addCommit(t, dir, "b.go", "package b", WIPCommitPrefix)

	wipCount, err := SquashWIPCommits(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if wipCount != 2 {
		t.Errorf("expected 2, got %d", wipCount)
	}

	// Verify squashed into single commit with generic message
	subjects := getCommitSubjects(t, dir)
	if len(subjects) != 1 {
		t.Errorf("expected 1 commit after squash, got %d: %v", len(subjects), subjects)
	}
	if len(subjects) > 0 && subjects[0] != "squashed WIP checkpoint commits" {
		t.Errorf("expected generic message, got %q", subjects[0])
	}

	// Verify files exist
	for _, f := range []string{"a.go", "b.go"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected %s to exist after squash", f)
		}
	}
}

func TestSquashWIPCommits_Mixed(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", "implement auth handler")
	addCommit(t, dir, "b.go", "package b", WIPCommitPrefix)
	addCommit(t, dir, "c.go", "package c", "add auth tests")
	addCommit(t, dir, "d.go", "package d", WIPCommitPrefix)

	wipCount, err := SquashWIPCommits(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if wipCount != 2 {
		t.Errorf("expected 2, got %d", wipCount)
	}

	// Verify squashed into single commit with non-WIP subjects preserved
	subjects := getCommitSubjects(t, dir)
	if len(subjects) != 1 {
		t.Errorf("expected 1 commit after squash, got %d: %v", len(subjects), subjects)
	}

	// Verify all files exist
	for _, f := range []string{"a.go", "b.go", "c.go", "d.go"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected %s to exist after squash", f)
		}
	}
}

func TestSquashWIPCommits_NoCommits(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")

	wipCount, err := SquashWIPCommits(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if wipCount != 0 {
		t.Errorf("expected 0 for no commits, got %d", wipCount)
	}
}

// --- BestCommitMessage tests (gu-zd2) --------------------------------------

// addCommitWithBody adds a file and commits with a subject and multi-line body.
// Using a helper keeps the intent (multi-line body matters for %B selection)
// visible in the tests.
func addCommitWithBody(t *testing.T, dir, filename, content, subject, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", filename},
		{"git", "commit", "-m", subject, "-m", body},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args[1:], err, out)
		}
	}
}

// TestBestCommitMessage_NoWIPReturnsTipMessage: a branch with only real
// commits returns the tip commit's message, same as GetBranchCommitMessage.
func TestBestCommitMessage_NoWIPReturnsTipMessage(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a\n", "feat: add feature A")
	addCommit(t, dir, "b.go", "package b\n", "feat: add feature B")

	msg, err := BestCommitMessage(dir, "feature", "main")
	if err != nil {
		t.Fatalf("BestCommitMessage returned error: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(msg), "feat: add feature B") {
		t.Errorf("expected tip message 'feat: add feature B', got %q", msg)
	}
}

// TestBestCommitMessage_WIPTipSkipped: a branch whose tip is a WIP commit
// returns the preceding real commit's message. This is the gu-zd2 primary
// acceptance case — preserves conventional commit format on the squash
// commit even when the polecat's last commit was an auto-checkpoint.
func TestBestCommitMessage_WIPTipSkipped(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a\n", "feat: real work")
	addCommit(t, dir, "b.txt", "b\n", WIPCommitPrefix)

	msg, err := BestCommitMessage(dir, "feature", "main")
	if err != nil {
		t.Fatalf("BestCommitMessage returned error: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(msg), "feat: real work") {
		t.Errorf("expected 'feat: real work' (WIP tip skipped), got %q", msg)
	}
	if strings.HasPrefix(strings.TrimSpace(msg), WIPCommitPrefix) {
		t.Errorf("expected non-WIP message, still got a WIP: %q", msg)
	}
}

// TestBestCommitMessage_AllWIPReturnsEmpty: if every commit on the branch
// is a WIP checkpoint (polecat crashed before any real commit), return ""
// so the caller can produce a descriptive fallback message.
func TestBestCommitMessage_AllWIPReturnsEmpty(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.txt", "a\n", WIPCommitPrefix)
	addCommit(t, dir, "b.txt", "b\n", WIPCommitPrefix)

	msg, err := BestCommitMessage(dir, "feature", "main")
	if err != nil {
		t.Fatalf("BestCommitMessage returned error: %v", err)
	}
	if strings.TrimSpace(msg) != "" {
		t.Errorf("expected empty message when all commits are WIP, got %q", msg)
	}
}

// TestBestCommitMessage_MultipleWIPsBetweenReals: walks back past several
// consecutive WIPs to find the real commit beneath.
func TestBestCommitMessage_MultipleWIPsBetweenReals(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a\n", "feat: first real")
	addCommit(t, dir, "w1.txt", "1\n", WIPCommitPrefix)
	addCommit(t, dir, "w2.txt", "2\n", WIPCommitPrefix)
	addCommit(t, dir, "w3.txt", "3\n", WIPCommitPrefix)

	msg, err := BestCommitMessage(dir, "feature", "main")
	if err != nil {
		t.Fatalf("BestCommitMessage returned error: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(msg), "feat: first real") {
		t.Errorf("expected 'feat: first real' beneath three WIPs, got %q", msg)
	}
}

// TestBestCommitMessage_PreservesBody: the full %B (subject + body) of the
// chosen commit must be returned, not just the subject. Conventional commit
// bodies often carry important context that should survive on the squash
// commit message.
func TestBestCommitMessage_PreservesBody(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommitWithBody(t, dir, "a.go", "package a\n",
		"feat: add feature A",
		"This body describes the change.\n\nWith a second paragraph.")
	addCommit(t, dir, "w.txt", "w\n", WIPCommitPrefix)

	msg, err := BestCommitMessage(dir, "feature", "main")
	if err != nil {
		t.Fatalf("BestCommitMessage returned error: %v", err)
	}
	if !strings.Contains(msg, "feat: add feature A") {
		t.Errorf("expected subject in message, got %q", msg)
	}
	if !strings.Contains(msg, "This body describes the change.") {
		t.Errorf("expected body in message (multi-line preserved), got %q", msg)
	}
	if !strings.Contains(msg, "second paragraph") {
		t.Errorf("expected full body including second paragraph, got %q", msg)
	}
}

// TestBestCommitMessage_EmptyRangeReturnsEmpty: when the branch has no
// commits past baseRef (freshly created, not diverged), return "" with no
// error.
func TestBestCommitMessage_EmptyRangeReturnsEmpty(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	// No new commits on feature.

	msg, err := BestCommitMessage(dir, "feature", "main")
	if err != nil {
		t.Fatalf("BestCommitMessage returned error on empty range: %v", err)
	}
	if strings.TrimSpace(msg) != "" {
		t.Errorf("expected empty message for no diverged commits, got %q", msg)
	}
}

// TestBestCommitMessage_InvalidBaseRefErrors: an unknown baseRef returns an
// error so the caller can log and fall back; it must not silently succeed
// with a misleading message.
func TestBestCommitMessage_InvalidBaseRefErrors(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a\n", "feat: add A")

	_, err := BestCommitMessage(dir, "feature", "origin/does-not-exist")
	if err == nil {
		t.Errorf("expected error for unknown baseRef, got nil")
	}
}

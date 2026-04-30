package health

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TCPCheck
// ---------------------------------------------------------------------------

func TestTCPCheck_Success(t *testing.T) {
	// Start a local TCP listener and verify TCPCheck returns true.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}
	defer ln.Close()

	// Accept in the background to avoid leaving half-open sockets around.
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("failed to split host port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("failed to parse port: %v", err)
	}

	if !TCPCheck("127.0.0.1", port, 2*time.Second) {
		t.Fatalf("TCPCheck returned false for a known-listening port %d", port)
	}
}

func TestTCPCheck_Failure(t *testing.T) {
	// Bind+close a socket to get a port that should have nothing listening on it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close() // Release the port so the next connect fails.

	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("failed to parse port: %v", err)
	}

	if TCPCheck("127.0.0.1", port, 250*time.Millisecond) {
		t.Fatalf("TCPCheck returned true for port %d with nothing listening", port)
	}
}

func TestTCPCheck_InvalidHost(t *testing.T) {
	// Unresolvable host should return false, not panic.
	if TCPCheck("invalid.host.that.does.not.exist.example.", 12345, 500*time.Millisecond) {
		t.Fatal("TCPCheck returned true for an unresolvable host")
	}
}

// ---------------------------------------------------------------------------
// LatencyCheck
// ---------------------------------------------------------------------------

func TestLatencyCheck_ConnectionFailure(t *testing.T) {
	// Bind+release to get a port with nothing listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	port, _ := strconv.Atoi(portStr)

	d, err := LatencyCheck("127.0.0.1", port, 500*time.Millisecond)
	if err == nil {
		t.Fatalf("LatencyCheck expected error for port %d with no server, got duration %v", port, d)
	}
	if d != 0 {
		t.Fatalf("LatencyCheck should return zero duration on failure, got %v", d)
	}
}

// ---------------------------------------------------------------------------
// DatabaseCount
// ---------------------------------------------------------------------------

func TestDatabaseCount_ConnectionFailure(t *testing.T) {
	// Bind+release to get a port with nothing listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	port, _ := strconv.Atoi(portStr)

	count, dbs, err := DatabaseCount("127.0.0.1", port)
	if err == nil {
		t.Fatalf("DatabaseCount expected error, got count=%d dbs=%v", count, dbs)
	}
	if count != 0 {
		t.Fatalf("DatabaseCount should return 0 on failure, got %d", count)
	}
	if dbs != nil {
		t.Fatalf("DatabaseCount should return nil slice on failure, got %v", dbs)
	}
}

// ---------------------------------------------------------------------------
// FindZombieServers
// ---------------------------------------------------------------------------

// Note: FindZombieServers calls doltserver.FindAllDoltListeners() which shells out
// to `lsof`. In typical CI environments no dolt processes are running, so this
// exercises the "no listeners" branch. The test tolerates either outcome to stay
// robust across environments (developer laptops may have dolt running).

func TestFindZombieServers_Runs(t *testing.T) {
	// Just assert the function returns a well-formed result without panicking.
	result := FindZombieServers([]int{3307, 3308})
	if result.Count < 0 {
		t.Fatalf("FindZombieServers returned negative Count: %d", result.Count)
	}
	if result.Count != len(result.PIDs) {
		t.Fatalf("FindZombieServers Count (%d) does not match len(PIDs) (%d)", result.Count, len(result.PIDs))
	}
}

func TestFindZombieServers_EmptyExpected(t *testing.T) {
	// With an empty expectedPorts list, every listener (if any) would count as a zombie.
	// The invariant we check: the result is internally consistent.
	result := FindZombieServers(nil)
	if result.Count != len(result.PIDs) {
		t.Fatalf("Count (%d) != len(PIDs) (%d)", result.Count, len(result.PIDs))
	}
}

// ---------------------------------------------------------------------------
// BackupFreshness
// ---------------------------------------------------------------------------

func TestBackupFreshness_MissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	got := BackupFreshness(missing)
	if !got.IsZero() {
		t.Fatalf("BackupFreshness(missing) = %v, want zero time", got)
	}
}

func TestBackupFreshness_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	got := BackupFreshness(dir)
	if !got.IsZero() {
		t.Fatalf("BackupFreshness(empty dir) = %v, want zero time", got)
	}
}

func TestBackupFreshness_ReturnsNewest(t *testing.T) {
	dir := t.TempDir()

	older := filepath.Join(dir, "old.txt")
	newer := filepath.Join(dir, "new.txt")

	if err := os.WriteFile(older, []byte("old"), 0o644); err != nil {
		t.Fatalf("write old: %v", err)
	}
	if err := os.WriteFile(newer, []byte("new"), 0o644); err != nil {
		t.Fatalf("write new: %v", err)
	}

	// Force distinct mtimes to avoid same-second filesystem resolution collisions.
	oldTime := time.Now().Add(-2 * time.Hour)
	newTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old: %v", err)
	}
	if err := os.Chtimes(newer, newTime, newTime); err != nil {
		t.Fatalf("chtimes new: %v", err)
	}

	got := BackupFreshness(dir)
	// Compare at second granularity since some filesystems round.
	if got.Unix() != newTime.Unix() {
		t.Fatalf("BackupFreshness = %v, want %v (newer file mtime)", got, newTime)
	}
}

func TestBackupFreshness_RecursesSubdirs(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	top := filepath.Join(dir, "top.txt")
	nested := filepath.Join(sub, "nested.txt")

	if err := os.WriteFile(top, []byte("top"), 0o644); err != nil {
		t.Fatalf("write top: %v", err)
	}
	if err := os.WriteFile(nested, []byte("nested"), 0o644); err != nil {
		t.Fatalf("write nested: %v", err)
	}

	topTime := time.Now().Add(-2 * time.Hour)
	nestedTime := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(top, topTime, topTime); err != nil {
		t.Fatalf("chtimes top: %v", err)
	}
	if err := os.Chtimes(nested, nestedTime, nestedTime); err != nil {
		t.Fatalf("chtimes nested: %v", err)
	}

	got := BackupFreshness(dir)
	if got.Unix() != nestedTime.Unix() {
		t.Fatalf("BackupFreshness = %v, want nested mtime %v", got, nestedTime)
	}
}

// ---------------------------------------------------------------------------
// JSONLGitFreshness
// ---------------------------------------------------------------------------

func TestJSONLGitFreshness_NotARepo(t *testing.T) {
	dir := t.TempDir() // no .git inside

	_, err := JSONLGitFreshness(dir)
	if err == nil {
		t.Fatal("JSONLGitFreshness expected error for non-git dir, got nil")
	}
	if !strings.Contains(err.Error(), "not a git repo") {
		t.Fatalf("expected 'not a git repo' error, got %v", err)
	}
}

func TestJSONLGitFreshness_RealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available in PATH; skipping")
	}

	dir := t.TempDir()

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
			// Avoid picking up the user's global hooks/config in a pristine test.
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init", "-q", "-b", "main")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")
	runGit("config", "commit.gpgsign", "false")

	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit("add", "hello.txt")

	before := time.Now().Add(-2 * time.Second).Unix()
	runGit("commit", "-q", "-m", "init")
	after := time.Now().Add(2 * time.Second).Unix()

	ts, err := JSONLGitFreshness(dir)
	if err != nil {
		t.Fatalf("JSONLGitFreshness error: %v", err)
	}
	got := ts.Unix()
	if got < before || got > after {
		t.Fatalf("commit timestamp %v (%d) not within expected window [%d, %d]",
			ts, got, before, after)
	}
}

// ---------------------------------------------------------------------------
// DirSize
// ---------------------------------------------------------------------------

func TestDirSize_Empty(t *testing.T) {
	dir := t.TempDir()
	size, err := DirSize(dir)
	if err != nil {
		t.Fatalf("DirSize error: %v", err)
	}
	if size != 0 {
		t.Fatalf("DirSize(empty) = %d, want 0", size)
	}
}

func TestDirSize_SumsFileSizesRecursively(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	topBytes := []byte("hello")         // 5
	nestedBytes := []byte("world!!")    // 7
	otherBytes := []byte("abcdefghij") // 10 — in nested dir

	if err := os.WriteFile(filepath.Join(dir, "top.txt"), topBytes, 0o644); err != nil {
		t.Fatalf("write top: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.txt"), nestedBytes, 0o644); err != nil {
		t.Fatalf("write nested a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.txt"), otherBytes, 0o644); err != nil {
		t.Fatalf("write nested b: %v", err)
	}

	want := int64(len(topBytes) + len(nestedBytes) + len(otherBytes))
	got, err := DirSize(dir)
	if err != nil {
		t.Fatalf("DirSize error: %v", err)
	}
	if got != want {
		t.Fatalf("DirSize = %d, want %d", got, want)
	}
}

func TestDirSize_MissingReturnsError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	_, err := DirSize(missing)
	if err == nil {
		t.Fatal("DirSize expected error for missing path, got nil")
	}
}

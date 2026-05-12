package doctor

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTmpInodesCheck_Properties(t *testing.T) {
	check := NewTmpInodesCheck()

	if check.Name() != "tmp-inodes" {
		t.Errorf("Name() = %q, want %q", check.Name(), "tmp-inodes")
	}
	if check.Description() == "" {
		t.Error("Description() should not be empty")
	}
	if !check.CanFix() {
		t.Error("CanFix() should be true — tmp-inodes supports cleanup")
	}
	if check.Category() != CategoryInfrastructure {
		t.Errorf("Category() = %q, want %q", check.Category(), CategoryInfrastructure)
	}
}

func TestTmpInodesCheck_Run_Default(t *testing.T) {
	// Run the check against the real /tmp (or equivalent) via the
	// default tmpDirPath. We don't assert on the status — it depends
	// on host conditions — but Message should always be set and the
	// check must never panic.
	check := NewTmpInodesCheck()
	result := check.Run(&CheckContext{})

	if result.Name != "tmp-inodes" {
		t.Errorf("Name = %q, want %q", result.Name, "tmp-inodes")
	}
	if result.Message == "" {
		t.Error("Message should not be empty")
	}
}

// withTmpDirPath temporarily points the check at the given path and
// restores the previous value when the test ends.
func withTmpDirPath(t *testing.T, path string) {
	t.Helper()
	prev := tmpDirPath
	tmpDirPath = path
	t.Cleanup(func() { tmpDirPath = prev })
}

func TestTmpInodesCheck_Run_UnreadableFilesystem(t *testing.T) {
	// Point the check at a nonexistent path. The expected behavior is
	// StatusOK with a message noting that inode usage isn't available —
	// this mirrors the Windows code path, where the syscall has no
	// meaningful answer.
	withTmpDirPath(t, "/nonexistent/path/doctor-tmp-inodes-test")
	check := NewTmpInodesCheck()
	result := check.Run(&CheckContext{})

	if result.Status != StatusOK {
		t.Errorf("Status = %v, want OK for unreadable path", result.Status)
	}
	if result.Message == "" {
		t.Error("Message should not be empty even on unreadable path")
	}
}

func TestIsGoTestTempDir(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// Real Go test temp dir shapes.
		{"TestFoo1", true},
		{"TestBar1234567890", true},
		{"TestSomethingComplex_Subtest42", true},

		// Not a Go test temp dir.
		{"Test", false},            // just the prefix
		{"TestNotes", false},       // no trailing digits
		{"Test123", false},         // too generic (no alpha name)
		{"systemd-private-foo", false},
		{"randomfile.txt", false},
		{"", false},
		{"test123", false}, // lowercase
	}

	for _, tc := range cases {
		got := isGoTestTempDir(tc.name)
		if got != tc.want {
			t.Errorf("isGoTestTempDir(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCleanupStaleGoTestTempDirs(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	// Build a fixture:
	//   TestOld1         — stale, should be removed
	//   TestOld2_Sub42   — stale, should be removed
	//   TestFresh3       — fresh, should be kept
	//   TestNotes        — doesn't match digit pattern, kept
	//   unrelated.txt    — not a dir, kept
	//   systemd-foo      — wrong prefix, kept
	oldMtime := now.Add(-2 * time.Hour)
	freshMtime := now.Add(-5 * time.Minute)

	mkdir := func(name string, mtime time.Time) string {
		full := filepath.Join(dir, name)
		if err := os.Mkdir(full, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		// Drop a file inside so we exercise RemoveAll rather than rmdir.
		if err := os.WriteFile(filepath.Join(full, "marker"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write marker in %s: %v", full, err)
		}
		if err := os.Chtimes(full, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", full, err)
		}
		return full
	}

	oldA := mkdir("TestOld1", oldMtime)
	oldB := mkdir("TestOld2_Sub42", oldMtime)
	fresh := mkdir("TestFresh3", freshMtime)
	notes := mkdir("TestNotes", oldMtime)
	systemd := mkdir("systemd-foo", oldMtime)

	// Also a non-dir entry at the top level.
	unrelated := filepath.Join(dir, "unrelated.txt")
	if err := os.WriteFile(unrelated, []byte("y"), 0o600); err != nil {
		t.Fatalf("write unrelated: %v", err)
	}

	removed, skipped, err := cleanupStaleGoTestTempDirs(dir, 1*time.Hour, now)
	if err != nil {
		t.Fatalf("cleanupStaleGoTestTempDirs: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (TestFresh3)", skipped)
	}

	// Old Go-style dirs should be gone.
	for _, p := range []string{oldA, oldB} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed (err=%v)", p, err)
		}
	}

	// Fresh Go-style dir, non-matching-name dir, and unrelated file must survive.
	for _, p := range []string{fresh, notes, systemd, unrelated} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should have been kept (err=%v)", p, err)
		}
	}
}

func TestCleanupStaleGoTestTempDirs_MissingDir(t *testing.T) {
	removed, skipped, err := cleanupStaleGoTestTempDirs("/nonexistent/doctor/tmp-inodes-test", time.Hour, time.Now())
	if err == nil {
		t.Error("expected error for nonexistent dir, got nil")
	}
	if removed != 0 || skipped != 0 {
		t.Errorf("removed=%d skipped=%d, want both 0 on error", removed, skipped)
	}
}

func TestTmpInodesCheck_Fix_RemovesStaleDirs(t *testing.T) {
	dir := t.TempDir()
	withTmpDirPath(t, dir)

	// One stale dir and one fresh dir.
	stale := filepath.Join(dir, "TestStale1234")
	if err := os.Mkdir(stale, 0o755); err != nil {
		t.Fatalf("mkdir stale: %v", err)
	}
	oldMtime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, oldMtime, oldMtime); err != nil {
		t.Fatalf("chtimes stale: %v", err)
	}

	fresh := filepath.Join(dir, "TestFresh5678")
	if err := os.Mkdir(fresh, 0o755); err != nil {
		t.Fatalf("mkdir fresh: %v", err)
	}

	check := NewTmpInodesCheck()
	if err := check.Fix(&CheckContext{}); err != nil {
		t.Fatalf("Fix() failed: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale dir should have been removed (err=%v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh dir should have survived: %v", err)
	}
}

func TestTmpInodesCheck_Fix_ReadOnlyContext(t *testing.T) {
	dir := t.TempDir()
	withTmpDirPath(t, dir)

	stale := filepath.Join(dir, "TestStale9999")
	if err := os.Mkdir(stale, 0o755); err != nil {
		t.Fatalf("mkdir stale: %v", err)
	}
	oldMtime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, oldMtime, oldMtime); err != nil {
		t.Fatalf("chtimes stale: %v", err)
	}

	check := NewTmpInodesCheck()
	if err := check.Fix(&CheckContext{ReadOnly: true}); err != nil {
		t.Fatalf("Fix(read-only) should be a no-op, got err=%v", err)
	}

	if _, err := os.Stat(stale); err != nil {
		t.Errorf("stale dir should be untouched in read-only mode: %v", err)
	}
}

func TestBunHmLeakPattern(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// Real leaked-bun shapes (captured via bpftrace, see gs-a9n).
		{".fcfef3abcbdd763b-00000000.hm", true},
		{".18aeee9b7fdbffef-00000000.hm", true},
		{".0000000000000000-00000000.hm", true},
		{".ffffffffffffffff-00000000.hm", true},

		// Negatives.
		{"fcfef3abcbdd763b-00000000.hm", false},     // missing leading dot
		{".fcfef3abcbdd763b-00000001.hm", false},    // suffix isn't all zeros
		{".fcfef3abcbdd763b-00000000.txt", false},   // wrong extension
		{".FCFEF3ABCBDD763B-00000000.hm", false},    // uppercase not allowed
		{".fcfef3abcbdd763-00000000.hm", false},     // 15-char prefix
		{".fcfef3abcbdd763bb-00000000.hm", false},   // 17-char prefix
		{".fcfef3abcbdd763g-00000000.hm", false},    // non-hex char in prefix
		{".hm", false},
		{"", false},
	}

	for _, tc := range cases {
		got := bunHmLeakPattern.MatchString(tc.name)
		if got != tc.want {
			t.Errorf("bunHmLeakPattern.MatchString(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCleanupLeakedBunHmFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	oldMtime := now.Add(-2 * time.Hour)
	freshMtime := now.Add(-1 * time.Minute)

	mkfile := func(name string, size int, mtime time.Time) string {
		full := filepath.Join(dir, name)
		data := make([]byte, size)
		if err := os.WriteFile(full, data, 0o664); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
		if err := os.Chtimes(full, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", full, err)
		}
		return full
	}

	// Should be removed.
	leakedOld := mkfile(".fcfef3abcbdd763b-00000000.hm", 0, oldMtime)
	leakedOld2 := mkfile(".18aeee9b7fdbffef-00000000.hm", 0, oldMtime)

	// Should be skipped (too fresh).
	leakedFresh := mkfile(".5aeff8b2fffffaff-00000000.hm", 0, freshMtime)

	// Should be left alone (size > 0 — safety belt).
	leakedWithContent := mkfile(".aaaaaaaaaaaaaaaa-00000000.hm", 7, oldMtime)

	// Should be left alone (doesn't match pattern).
	unrelated := mkfile("some-other-file.hm", 0, oldMtime)
	notHidden := mkfile("fcfef3abcbdd763b-00000000.hm", 0, oldMtime)

	// Also a directory matching the pattern shouldn't be touched.
	dirNamedLikeLeak := filepath.Join(dir, ".bbbbbbbbbbbbbbbb-00000000.hm")
	if err := os.Mkdir(dirNamedLikeLeak, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dirNamedLikeLeak, err)
	}
	if err := os.Chtimes(dirNamedLikeLeak, oldMtime, oldMtime); err != nil {
		t.Fatalf("chtimes %s: %v", dirNamedLikeLeak, err)
	}

	removed, skipped, err := cleanupLeakedBunHmFiles(dir, 10*time.Minute, now)
	if err != nil {
		t.Fatalf("cleanupLeakedBunHmFiles: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the fresh leak)", skipped)
	}

	for _, p := range []string{leakedOld, leakedOld2} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed (err=%v)", p, err)
		}
	}
	for _, p := range []string{leakedFresh, leakedWithContent, unrelated, notHidden, dirNamedLikeLeak} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should have been kept (err=%v)", p, err)
		}
	}
}

func TestCleanupLeakedBunHmFiles_MissingDir(t *testing.T) {
	removed, skipped, err := cleanupLeakedBunHmFiles("/nonexistent/doctor/bun-hm-leak-test", 10*time.Minute, time.Now())
	if err == nil {
		t.Error("expected error for nonexistent dir, got nil")
	}
	if removed != 0 || skipped != 0 {
		t.Errorf("removed=%d skipped=%d, want both 0 on error", removed, skipped)
	}
}

func TestTmpInodesCheck_Fix_RemovesLeakedBunHmFiles(t *testing.T) {
	dir := t.TempDir()
	withTmpDirPath(t, dir)

	leaked := filepath.Join(dir, ".fcfef3abcbdd763b-00000000.hm")
	if err := os.WriteFile(leaked, nil, 0o664); err != nil {
		t.Fatalf("write leaked: %v", err)
	}
	oldMtime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(leaked, oldMtime, oldMtime); err != nil {
		t.Fatalf("chtimes leaked: %v", err)
	}

	// Also include a stale test dir so we exercise both cleaners in one Fix() call.
	staleDir := filepath.Join(dir, "TestStale1234")
	if err := os.Mkdir(staleDir, 0o755); err != nil {
		t.Fatalf("mkdir staleDir: %v", err)
	}
	if err := os.Chtimes(staleDir, oldMtime, oldMtime); err != nil {
		t.Fatalf("chtimes staleDir: %v", err)
	}

	check := NewTmpInodesCheck()
	if err := check.Fix(&CheckContext{}); err != nil {
		t.Fatalf("Fix() failed: %v", err)
	}

	if _, err := os.Stat(leaked); !os.IsNotExist(err) {
		t.Errorf("leaked .hm file should have been removed (err=%v)", err)
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Errorf("stale test dir should have been removed (err=%v)", err)
	}
}

func TestTmpInodesCheck_Fix_LeakedBunHmFiles_ReadOnlyContext(t *testing.T) {
	dir := t.TempDir()
	withTmpDirPath(t, dir)

	leaked := filepath.Join(dir, ".fcfef3abcbdd763b-00000000.hm")
	if err := os.WriteFile(leaked, nil, 0o664); err != nil {
		t.Fatalf("write leaked: %v", err)
	}
	oldMtime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(leaked, oldMtime, oldMtime); err != nil {
		t.Fatalf("chtimes leaked: %v", err)
	}

	check := NewTmpInodesCheck()
	if err := check.Fix(&CheckContext{ReadOnly: true}); err != nil {
		t.Fatalf("Fix(read-only) should be a no-op, got err=%v", err)
	}

	if _, err := os.Stat(leaked); err != nil {
		t.Errorf("leaked file should be untouched in read-only mode: %v", err)
	}
}

func TestTmpInodeUsage_UsedPercent(t *testing.T) {
	cases := []struct {
		name string
		u    tmpInodeUsage
		want float64
	}{
		{"zero total", tmpInodeUsage{Total: 0, Free: 0, Used: 0}, 0},
		{"half full", tmpInodeUsage{Total: 100, Free: 50, Used: 50}, 50},
		{"full", tmpInodeUsage{Total: 100, Free: 0, Used: 100}, 100},
	}
	for _, tc := range cases {
		got := tc.u.UsedPercent()
		if got != tc.want {
			t.Errorf("%s: UsedPercent() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

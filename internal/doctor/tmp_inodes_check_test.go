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

func TestIsLeakedBunTempFile(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// Real samples from the wild (gs-a9n bpftrace + ls output).
		{".18aba7d5df27fe9b-00000000.hm", true},
		{".fcfef3abcbdd763b-00000000.hm", true},
		{".98affab6bfed7cbd-00000000.hm", true},
		{".0000000000000000-00000000.hm", true},
		{".ffffffffffffffff-00000000.hm", true},

		// Wrong shape — must not match.
		{"18aba7d5df27fe9b-00000000.hm", false}, // missing leading dot
		{".18aba7d5df27fe9b-00000000.HM", false}, // uppercase ext
		{".18aba7d5df27fe9b-00000001.hm", false}, // wrong suffix counter
		{".18aba7d5df27fe9-00000000.hm", false},  // 15 hex digits
		{".18aba7d5df27fe9bb-00000000.hm", false}, // 17 hex digits
		{".18aba7d5df27feZb-00000000.hm", false},  // non-hex char
		{".18aba7d5DF27fe9b-00000000.hm", false},  // uppercase hex (bun uses lowercase)
		{".", false},
		{"", false},
		{".hm", false},
		{"-00000000.hm", false},
	}

	for _, tc := range cases {
		got := isLeakedBunTempFile(tc.name)
		if got != tc.want {
			t.Errorf("isLeakedBunTempFile(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCleanupLeakedBunTempFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	oldMtime := now.Add(-2 * time.Hour)
	freshMtime := now.Add(-5 * time.Minute)

	writeFile := func(name string, size int, mtime time.Time) string {
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

	// Fixture:
	staleA := writeFile(".18aba7d5df27fe9b-00000000.hm", 0, oldMtime) // remove
	staleB := writeFile(".fcfef3abcbdd763b-00000000.hm", 0, oldMtime) // remove
	fresh := writeFile(".aaaaaaaaaaaaaaaa-00000000.hm", 0, freshMtime) // keep (too young)
	nonEmpty := writeFile(".bbbbbbbbbbbbbbbb-00000000.hm", 42, oldMtime) // keep (not the bun leak)
	wrongName := writeFile(".dolt-cache.hm", 0, oldMtime)            // keep (wrong shape)
	unrelated := writeFile("dump.txt", 100, oldMtime)                // keep (wrong shape)

	// Also a subdirectory with the matching name — must not be treated as a file.
	dirNamedLikeLeak := filepath.Join(dir, ".cccccccccccccccc-00000000.hm")
	if err := os.Mkdir(dirNamedLikeLeak, 0o755); err != nil {
		t.Fatalf("mkdir lookalike dir: %v", err)
	}

	removed, skipped, err := cleanupLeakedBunTempFiles(dir, 1*time.Hour, now)
	if err != nil {
		t.Fatalf("cleanupLeakedBunTempFiles: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the fresh leak), got %d", skipped, skipped)
	}

	for _, p := range []string{staleA, staleB} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed (err=%v)", p, err)
		}
	}
	for _, p := range []string{fresh, nonEmpty, wrongName, unrelated, dirNamedLikeLeak} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should have been kept (err=%v)", p, err)
		}
	}
}

func TestCleanupLeakedBunTempFiles_MissingDir(t *testing.T) {
	removed, skipped, err := cleanupLeakedBunTempFiles("/nonexistent/doctor/tmp-inodes-bun-test", time.Hour, time.Now())
	if err == nil {
		t.Error("expected error for nonexistent dir, got nil")
	}
	if removed != 0 || skipped != 0 {
		t.Errorf("removed=%d skipped=%d, want both 0 on error", removed, skipped)
	}
}

func TestTmpInodesCheck_Fix_RemovesBothKinds(t *testing.T) {
	dir := t.TempDir()
	withTmpDirPath(t, dir)
	oldMtime := time.Now().Add(-2 * time.Hour)

	// Stale Go test dir.
	staleDir := filepath.Join(dir, "TestStale1234")
	if err := os.Mkdir(staleDir, 0o755); err != nil {
		t.Fatalf("mkdir stale dir: %v", err)
	}
	if err := os.Chtimes(staleDir, oldMtime, oldMtime); err != nil {
		t.Fatalf("chtimes stale dir: %v", err)
	}

	// Stale bun leak.
	staleLeak := filepath.Join(dir, ".deadbeefcafebabe-00000000.hm")
	if err := os.WriteFile(staleLeak, nil, 0o664); err != nil {
		t.Fatalf("write stale leak: %v", err)
	}
	if err := os.Chtimes(staleLeak, oldMtime, oldMtime); err != nil {
		t.Fatalf("chtimes stale leak: %v", err)
	}

	check := NewTmpInodesCheck()
	if err := check.Fix(&CheckContext{}); err != nil {
		t.Fatalf("Fix() failed: %v", err)
	}

	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Errorf("stale Go test dir should have been removed (err=%v)", err)
	}
	if _, err := os.Stat(staleLeak); !os.IsNotExist(err) {
		t.Errorf("stale bun .hm leak should have been removed (err=%v)", err)
	}
}

func TestTmpInodesCheck_Fix_ReadOnlyContext_LeavesBunLeaks(t *testing.T) {
	dir := t.TempDir()
	withTmpDirPath(t, dir)

	staleLeak := filepath.Join(dir, ".0123456789abcdef-00000000.hm")
	if err := os.WriteFile(staleLeak, nil, 0o664); err != nil {
		t.Fatalf("write stale leak: %v", err)
	}
	oldMtime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(staleLeak, oldMtime, oldMtime); err != nil {
		t.Fatalf("chtimes stale leak: %v", err)
	}

	check := NewTmpInodesCheck()
	if err := check.Fix(&CheckContext{ReadOnly: true}); err != nil {
		t.Fatalf("Fix(read-only) should be a no-op, got err=%v", err)
	}
	if _, err := os.Stat(staleLeak); err != nil {
		t.Errorf("bun leak should be untouched in read-only mode: %v", err)
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

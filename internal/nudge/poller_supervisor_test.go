package nudge

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestListPollers_MissingDir(t *testing.T) {
	townRoot := t.TempDir()
	entries, err := ListPollers(townRoot)
	if err != nil {
		t.Fatalf("ListPollers unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no entries, got %d", len(entries))
	}
}

func TestListPollers_Mixed(t *testing.T) {
	townRoot := t.TempDir()
	dir := pollerPidDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	// live entry (our own pid)
	liveSession := "gt-rig-crew-live"
	if err := os.WriteFile(pollerPidFile(townRoot, liveSession), []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		t.Fatal(err)
	}

	// stale entry (impossible pid)
	staleSession := "gt-rig-crew-stale"
	if err := os.WriteFile(pollerPidFile(townRoot, staleSession), []byte("999999999"), 0644); err != nil {
		t.Fatal(err)
	}

	// corrupt entry
	corruptSession := "gt-rig-crew-corrupt"
	if err := os.WriteFile(pollerPidFile(townRoot, corruptSession), []byte("not-a-number"), 0644); err != nil {
		t.Fatal(err)
	}

	// non-pid file should be ignored
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	entries, err := ListPollers(townRoot)
	if err != nil {
		t.Fatalf("ListPollers error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 pid entries, got %d: %v", len(entries), entries)
	}

	byName := map[string]PollerEntry{}
	for _, e := range entries {
		byName[e.Session] = e
	}

	if e, ok := byName[liveSession]; !ok || !e.Alive || e.PID != os.Getpid() {
		t.Errorf("live entry bad: %+v", e)
	}
	if e, ok := byName[staleSession]; !ok || e.Alive || e.PID != 999999999 {
		t.Errorf("stale entry bad: %+v", e)
	}
	if e, ok := byName[corruptSession]; !ok || e.Alive || e.PID != 0 {
		t.Errorf("corrupt entry bad: %+v", e)
	}
}

func TestRemoveStalePIDFile(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-rig-crew-gone"
	path := pollerPidFile(townRoot, session)

	// Missing is a no-op.
	if err := RemoveStalePIDFile(townRoot, session); err != nil {
		t.Fatalf("RemoveStalePIDFile on missing returned error: %v", err)
	}

	// Existing file gets removed.
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveStalePIDFile(townRoot, session); err != nil {
		t.Fatalf("RemoveStalePIDFile returned error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected file to be gone, got err=%v", err)
	}
}

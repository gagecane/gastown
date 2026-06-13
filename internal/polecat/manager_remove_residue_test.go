package polecat

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGuardPolecatSandboxPath proves the safety boundary for the gs-72ym
// privilege-escalated removal path: it accepts only paths that are
// unambiguously children of a polecats/ sandbox and rejects anything that
// could, if removed with elevated privileges, destroy a bare repo or Dolt data.
func TestGuardPolecatSandboxPath(t *testing.T) {
	accepted := []string{
		"/home/sika/gt/gastown/polecats/dementus/gastown",
		"/home/sika/gt/gastown/polecats/dementus",
		"/var/lib/town/rig/polecats/nux/rig",
	}
	for _, p := range accepted {
		if err := guardPolecatSandboxPath(p); err != nil {
			t.Errorf("guardPolecatSandboxPath(%q) = %v; want nil (valid sandbox child)", p, err)
		}
	}

	rejected := []string{
		"/home/sika/gt/gastown/.repo.git",                 // bare repo
		"/home/sika/gt/gastown/polecats/nux/rig/.git",     // worktree git dir
		"/home/sika/gt/gastown/polecats/nux/.beads/.dolt", // Dolt data
		"/home/sika/gt/gastown/mayor/rig",                 // not under polecats/
		"/home/sika/gt/gastown/polecats",                  // the polecats/ dir itself
		"/polecats",                                       // shallow + is polecats itself
		"/",                                               // root
		"/tmp/scratch",                                    // unrelated
	}
	for _, p := range rejected {
		if err := guardPolecatSandboxPath(p); err == nil {
			t.Errorf("guardPolecatSandboxPath(%q) = nil; want refusal", p)
		}
	}
}

// TestForceRemoveDir_NormalTree confirms the happy path is unchanged: an
// ordinary, owned directory tree is removed without ever reaching the escalated
// (sudo) residue path.
func TestForceRemoveDir_NormalTree(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "polecats", "nux", "rig", "tests", "__pycache__")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "foo.pyc"), []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	target := filepath.Join(dir, "polecats", "nux")
	if err := forceRemoveDir(target); err != nil {
		t.Fatalf("forceRemoveDir(%q) = %v; want nil", target, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("path %q still exists after forceRemoveDir (stat err: %v)", target, err)
	}
}

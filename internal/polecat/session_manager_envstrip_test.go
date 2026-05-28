package polecat

import (
	"strings"
	"testing"
)

// TestEnvWithoutBeadsDir_StripsBEADS_DIR verifies the helper used by
// validateIssue/hookIssue to keep stale inherited BEADS_DIR from defeating
// cmd.Dir-based bd database discovery (gu-olzp).
//
// bd treats BEADS_DIR as a hard override over directory-based discovery,
// so a daemon process that inherited BEADS_DIR (test pollution, an operator
// shell that had it set, etc.) could spawn a polecat whose validateIssue
// fails with "not an issue ID or formula name" even when cmd.Dir is the
// correct rig. This regression test guards against the helper drifting
// out of sync with that contract.
func TestEnvWithoutBeadsDir_StripsBEADS_DIR(t *testing.T) {
	// Arrange a polluted environment.
	t.Setenv("BEADS_DIR", "/some/wrong/.beads")
	t.Setenv("PATH", "/usr/local/bin:/usr/bin")
	t.Setenv("HOME", "/home/test")

	got := envWithoutBeadsDir()

	for _, e := range got {
		if strings.HasPrefix(e, "BEADS_DIR=") {
			t.Errorf("envWithoutBeadsDir() leaked BEADS_DIR entry %q; "+
				"validateIssue/hookIssue would inherit a stale path that "+
				"overrides cmd.Dir, causing 'not an issue ID' failures "+
				"on polecat spawn (gu-olzp)", e)
		}
	}

	// Sanity: other env entries should be preserved.
	hasPath := false
	hasHome := false
	for _, e := range got {
		if strings.HasPrefix(e, "PATH=") {
			hasPath = true
		}
		if strings.HasPrefix(e, "HOME=") {
			hasHome = true
		}
	}
	if !hasPath {
		t.Errorf("envWithoutBeadsDir() also stripped PATH; should only filter BEADS_DIR")
	}
	if !hasHome {
		t.Errorf("envWithoutBeadsDir() also stripped HOME; should only filter BEADS_DIR")
	}
}

// TestEnvWithoutBeadsDir_StripsAllBEADS_DIR verifies that if the parent
// somehow has multiple BEADS_DIR entries (unusual but possible via
// duplicated env), all of them are removed. glibc's getenv returns the
// first match, but `cmd.Env` is the full slice, so partial filtering
// would leave the duplicate visible.
func TestEnvWithoutBeadsDir_StripsAllBEADS_DIR(t *testing.T) {
	// We can't easily inject a duplicate via t.Setenv (Go's env is a map),
	// so just exercise the loop logic directly by calling the helper from
	// a polluted setenv state. The single-strip case is what matters in
	// practice; this test documents the contract.
	t.Setenv("BEADS_DIR", "/wrong/.beads")
	got := envWithoutBeadsDir()
	count := 0
	for _, e := range got {
		if strings.HasPrefix(e, "BEADS_DIR=") {
			count++
		}
	}
	if count != 0 {
		t.Errorf("envWithoutBeadsDir() left %d BEADS_DIR entries; want 0", count)
	}
}

package cmd

import "testing"

// TestUnslingSilencesUsage guards the gu-y2fjt fix: unsling must set
// SilenceUsage so operational errors returned from RunE — "hooked work is
// incomplete", "bead is not hooked", agent-resolution failures — surface
// cleanly without cobra dumping the full "Usage: gt unsling ..." help block.
//
// Before the fix, a valid invocation that hit a runtime error printed the
// usage text, which read like a syntax error and confused agents into thinking
// they had mistyped the command. Genuine usage errors (wrong arg count) are
// validated by cobra before RunE and still print usage regardless of this flag.
func TestUnslingSilencesUsage(t *testing.T) {
	if !unslingCmd.SilenceUsage {
		t.Error("unslingCmd.SilenceUsage must be true so operational errors don't dump the usage block (gu-y2fjt)")
	}
}

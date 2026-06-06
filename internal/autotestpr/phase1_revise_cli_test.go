package autotestpr

import "testing"

// TestPhase1ReviseCLI_Phase1Active verifies that when revision routing
// is disabled (Phase 1), the helper returns the full CLI string.
func TestPhase1ReviseCLI_Phase1Active(t *testing.T) {
	t.Parallel()

	got := Phase1ReviseCLI("gt-mr-abc12", false)
	want := "gt auto-test-pr revise --mr=gt-mr-abc12"
	if got != want {
		t.Errorf("Phase1ReviseCLI() = %q; want %q", got, want)
	}
}

// TestPhase1ReviseCLI_Phase2Active verifies that when revision routing
// is enabled (Phase 2 live), the helper returns empty (template omits
// the fallback line).
func TestPhase1ReviseCLI_Phase2Active(t *testing.T) {
	t.Parallel()

	got := Phase1ReviseCLI("gt-mr-abc12", true)
	if got != "" {
		t.Errorf("Phase1ReviseCLI() = %q; want empty when revision routing enabled", got)
	}
}

// TestPhase1ReviseCLI_EmptyMRID verifies that an empty MR bead ID
// returns empty regardless of phase.
func TestPhase1ReviseCLI_EmptyMRID(t *testing.T) {
	t.Parallel()

	got := Phase1ReviseCLI("", false)
	if got != "" {
		t.Errorf("Phase1ReviseCLI(\"\", false) = %q; want empty", got)
	}
}

// TestPhase1ReviseCLIWithComment_AllArgs verifies the extended form
// with a comment ID.
func TestPhase1ReviseCLIWithComment_AllArgs(t *testing.T) {
	t.Parallel()

	got := Phase1ReviseCLIWithComment("gt-mr-abc12", "cmt-42", false)
	want := "gt auto-test-pr revise --mr=gt-mr-abc12 --comment-id=cmt-42"
	if got != want {
		t.Errorf("Phase1ReviseCLIWithComment() = %q; want %q", got, want)
	}
}

// TestPhase1ReviseCLIWithComment_NoCommentID verifies fallback to the
// basic form when comment ID is empty.
func TestPhase1ReviseCLIWithComment_NoCommentID(t *testing.T) {
	t.Parallel()

	got := Phase1ReviseCLIWithComment("gt-mr-abc12", "", false)
	want := "gt auto-test-pr revise --mr=gt-mr-abc12"
	if got != want {
		t.Errorf("Phase1ReviseCLIWithComment() = %q; want %q", got, want)
	}
}

// TestPhase1ReviseCLIWithComment_Phase2 verifies Phase 2 suppression.
func TestPhase1ReviseCLIWithComment_Phase2(t *testing.T) {
	t.Parallel()

	got := Phase1ReviseCLIWithComment("gt-mr-abc12", "cmt-42", true)
	if got != "" {
		t.Errorf("Phase1ReviseCLIWithComment() = %q; want empty when Phase 2 live", got)
	}
}

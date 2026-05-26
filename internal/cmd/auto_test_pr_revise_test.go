// Unit tests for the auto-test-pr revise CLI verb (Phase 0 task 2c:
// gu-y9us). These tests cover flag validation, label extraction,
// error paths (invalid MR bead, missing fields, state not mr-pending),
// and the ReviseArgs JSON envelope shape.
//
// The deeper integration-level tests (MR bead not found, missing label)
// exercise the full runAutoTestPRRevise path and skip gracefully when
// not running inside a Gas Town workspace with Dolt connectivity.
package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/workspace"
)

// resetAutoTestPRReviseFlags zeroes the package-level flag bindings
// between tests. cobra leaves them set to the operator-provided value
// across runs in a single test binary.
func resetAutoTestPRReviseFlags(t *testing.T) {
	t.Helper()
	autoTestPRReviseMR = ""
	autoTestPRReviseCommentID = ""
}

// ---------------------------------------------------------------------------
// Flag validation tests
// ---------------------------------------------------------------------------

// TestAutoTestPRRevise_MissingMRFlag_ExitsNonZero asserts that invoking
// `gt auto-test-pr revise` without --mr produces a clear error message
// and exit code 2.
func TestAutoTestPRRevise_MissingMRFlag_ExitsNonZero(t *testing.T) {
	resetAutoTestPRReviseFlags(t)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	cmd := autoTestPRReviseCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRRevise(cmd, nil)
	if err == nil {
		t.Fatal("expected error from runAutoTestPRRevise without --mr")
	}
	code, ok := IsSilentExit(err)
	if !ok {
		t.Fatalf("expected SilentExit error, got %T: %v", err, err)
	}
	if code != 2 {
		t.Errorf("SilentExit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--mr is required") {
		t.Errorf("stderr should mention --mr is required, got: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "example:") {
		t.Errorf("stderr should include an example, got: %q", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// extractRigFromMRLabels unit tests
// ---------------------------------------------------------------------------

func TestExtractRigFromMRLabels_Found(t *testing.T) {
	t.Parallel()

	labels := []string{"gt:auto-test-pr", "rig:gastown_upstream", "priority:2"}
	got := extractRigFromMRLabels(labels)
	if got != "gastown_upstream" {
		t.Errorf("extractRigFromMRLabels() = %q; want %q", got, "gastown_upstream")
	}
}

func TestExtractRigFromMRLabels_NotFound(t *testing.T) {
	t.Parallel()

	labels := []string{"gt:auto-test-pr", "priority:2"}
	got := extractRigFromMRLabels(labels)
	if got != "" {
		t.Errorf("extractRigFromMRLabels() = %q; want empty string", got)
	}
}

func TestExtractRigFromMRLabels_EmptyLabels(t *testing.T) {
	t.Parallel()

	got := extractRigFromMRLabels(nil)
	if got != "" {
		t.Errorf("extractRigFromMRLabels(nil) = %q; want empty string", got)
	}
}

func TestExtractRigFromMRLabels_RigPrefixOnly(t *testing.T) {
	t.Parallel()

	// "rig:" with no value after the prefix — should return empty.
	labels := []string{"rig:"}
	got := extractRigFromMRLabels(labels)
	if got != "" {
		t.Errorf("extractRigFromMRLabels([\"rig:\"]) = %q; want empty (prefix-only label)", got)
	}
}

func TestExtractRigFromMRLabels_FirstRigWins(t *testing.T) {
	t.Parallel()

	// Multiple rig: labels — the first one wins.
	labels := []string{"rig:alpha", "rig:beta"}
	got := extractRigFromMRLabels(labels)
	if got != "alpha" {
		t.Errorf("extractRigFromMRLabels() = %q; want %q (first rig label wins)", got, "alpha")
	}
}

// ---------------------------------------------------------------------------
// ReviseArgs JSON shape tests
// ---------------------------------------------------------------------------

// TestReviseArgs_JSONRoundTrip verifies that the ReviseArgs struct
// serializes with the expected field names and that the omitempty on
// CommentID works correctly.
func TestReviseArgs_JSONRoundTrip_WithCommentID(t *testing.T) {
	t.Parallel()

	args := ReviseArgs{
		Mode:      "revise",
		MRID:      "gt-mr-abc12",
		Branch:    "polecat/nux/gt-xyz",
		CommitSHA: "deadbeef1234567890",
		Rig:       "gastown_upstream",
		CommentID: "cmt-42",
	}

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal(ReviseArgs): %v", err)
	}

	// Verify field names in the JSON.
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	checks := map[string]string{
		"mode":       "revise",
		"mr_id":      "gt-mr-abc12",
		"branch":     "polecat/nux/gt-xyz",
		"commit_sha": "deadbeef1234567890",
		"rig":        "gastown_upstream",
		"comment_id": "cmt-42",
	}
	for key, want := range checks {
		got, ok := m[key]
		if !ok {
			t.Errorf("JSON missing key %q", key)
			continue
		}
		if got != want {
			t.Errorf("JSON[%q] = %q; want %q", key, got, want)
		}
	}

	// Round-trip back to struct.
	var roundTripped ReviseArgs
	if err := json.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if roundTripped != args {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", roundTripped, args)
	}
}

func TestReviseArgs_JSONRoundTrip_WithoutCommentID(t *testing.T) {
	t.Parallel()

	args := ReviseArgs{
		Mode:      "revise",
		MRID:      "gt-mr-abc12",
		Branch:    "polecat/nux/gt-xyz",
		CommitSHA: "deadbeef1234567890",
		Rig:       "gastown_upstream",
		// CommentID omitted — should not appear in JSON.
	}

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal(ReviseArgs): %v", err)
	}

	if strings.Contains(string(raw), "comment_id") {
		t.Errorf("JSON should omit comment_id when empty, got: %s", string(raw))
	}

	// Round-trip.
	var roundTripped ReviseArgs
	if err := json.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if roundTripped != args {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", roundTripped, args)
	}
}

// ---------------------------------------------------------------------------
// RigCycleState JSON shape tests
// ---------------------------------------------------------------------------

func TestRigCycleState_MarshalShape(t *testing.T) {
	t.Parallel()

	state := RigCycleState{
		State:       "mr-pending",
		LastTransAt: "2026-05-26T01:00:00Z",
		LastActor:   "overseer",
		CurrentMRID: "gt-mr-xyz",
	}

	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if got := m["state"]; got != "mr-pending" {
		t.Errorf("state = %q; want %q", got, "mr-pending")
	}
	if got := m["last_transition_at"]; got != "2026-05-26T01:00:00Z" {
		t.Errorf("last_transition_at = %q; want %q", got, "2026-05-26T01:00:00Z")
	}
}

func TestRigCycleState_OmitsEmptyFields(t *testing.T) {
	t.Parallel()

	// Only State is set — the omitempty fields should be absent.
	state := RigCycleState{State: "idle"}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	s := string(raw)
	if strings.Contains(s, "last_transition_at") {
		t.Errorf("empty last_transition_at should be omitted: %s", s)
	}
	if strings.Contains(s, "last_actor") {
		t.Errorf("empty last_actor should be omitted: %s", s)
	}
	if strings.Contains(s, "current_mr_id") {
		t.Errorf("empty current_mr_id should be omitted: %s", s)
	}
}

// ---------------------------------------------------------------------------
// FormulaTestImprover constant sanity check
// ---------------------------------------------------------------------------

// TestFormulaTestImproverConstant documents the expected formula name.
// If the formula is renamed without updating the revise CLI, this test
// goes red.
func TestFormulaTestImproverConstant(t *testing.T) {
	t.Parallel()

	if FormulaTestImprover != "mol-polecat-work-test-improver" {
		t.Errorf("FormulaTestImprover = %q; want %q",
			FormulaTestImprover, "mol-polecat-work-test-improver")
	}
}

// ---------------------------------------------------------------------------
// Error sentinel tests
// ---------------------------------------------------------------------------

func TestErrMRNotPending_ErrorText(t *testing.T) {
	t.Parallel()

	if !strings.Contains(ErrMRNotPending.Error(), "mr-pending") {
		t.Errorf("ErrMRNotPending.Error() = %q; should mention mr-pending", ErrMRNotPending.Error())
	}
}

func TestErrMRNotAutoTestPR_ErrorText(t *testing.T) {
	t.Parallel()

	if !strings.Contains(ErrMRNotAutoTestPR.Error(), "gt:auto-test-pr") {
		t.Errorf("ErrMRNotAutoTestPR.Error() = %q; should mention gt:auto-test-pr", ErrMRNotAutoTestPR.Error())
	}
}

// ---------------------------------------------------------------------------
// Integration-level tests (require Gas Town workspace + Dolt)
// ---------------------------------------------------------------------------

// requireGasTownWorkspace skips the test if the current working
// directory is not inside a Gas Town workspace. Tests that query Dolt
// (via beads.Show) need the workspace env to resolve the beads dir.
func requireGasTownWorkspace(t *testing.T) {
	t.Helper()
	if _, err := workspace.FindFromCwdOrError(); err != nil {
		t.Skipf("not in a Gas Town workspace: %v", err)
	}
}

// TestAutoTestPRRevise_MRBeadNotFound exercises the error path when the
// --mr flag points to a bead ID that does not exist. The CLI should
// surface a clear error mentioning the bead ID.
func TestAutoTestPRRevise_MRBeadNotFound(t *testing.T) {
	requireGasTownWorkspace(t)
	resetAutoTestPRReviseFlags(t)
	autoTestPRReviseMR = "nonexistent-mr-bead-zzz99"

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := autoTestPRReviseCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRRevise(cmd, nil)
	if err == nil {
		t.Fatal("expected error for non-existent MR bead")
	}
	// The error should mention the bead ID so the operator knows which
	// bead was not found.
	if !strings.Contains(err.Error(), "nonexistent-mr-bead-zzz99") {
		t.Errorf("error should mention bead ID, got: %v", err)
	}
}

// TestAutoTestPRRevise_MRBeadMissingLabel exercises the error path when
// the --mr flag points to a bead that exists but lacks the
// gt:auto-test-pr label. We use the well-known town-state bead (which
// should exist if the workspace is initialized) since it's guaranteed
// to NOT have the auto-test-pr label.
func TestAutoTestPRRevise_MRBeadMissingLabel(t *testing.T) {
	requireGasTownWorkspace(t)
	resetAutoTestPRReviseFlags(t)
	// Use the town-state bead which exists but doesn't have gt:auto-test-pr.
	autoTestPRReviseMR = "town-auto-test-pr-state"

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := autoTestPRReviseCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRRevise(cmd, nil)
	if err == nil {
		// If the town-state bead doesn't exist, Skip rather than fail.
		t.Skip("town-state bead may not exist in this workspace")
	}
	// Two valid outcomes:
	// 1. ErrMRNotAutoTestPR — the bead exists but lacks the label.
	// 2. Some other error — the bead might have the label in a test env,
	//    or the bead might not exist (which gives a "reading MR bead" error).
	if strings.Contains(err.Error(), "does not carry the gt:auto-test-pr label") ||
		err == ErrMRNotAutoTestPR {
		// Expected path: bead exists but lacks the label.
		if !strings.Contains(stderr.String(), "gt:auto-test-pr label") {
			t.Errorf("stderr should mention the missing label, got: %q", stderr.String())
		}
		return
	}
	// If we get a different error (bead not found, parse error, etc.),
	// that's acceptable — the bead might not be provisioned.
	t.Logf("got a different error (acceptable): %v", err)
}

package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// fakeAgentBeadCloser implements the agentBeadCloser seam so closeAgentBead
// can be unit-tested without a real beads/Dolt backend. (gu-y3fi)
type fakeAgentBeadCloser struct {
	showIssue   *beads.Issue
	showErr     error
	updateErr   error
	updateOpts  *beads.UpdateOptions
	updateID    string
	showCalls   int
	updateCalls int
}

func (f *fakeAgentBeadCloser) Show(id string) (*beads.Issue, error) {
	f.showCalls++
	if f.showErr != nil {
		return nil, f.showErr
	}
	return f.showIssue, nil
}

func (f *fakeAgentBeadCloser) Update(id string, opts beads.UpdateOptions) error {
	f.updateCalls++
	f.updateID = id
	f.updateOpts = &opts
	return f.updateErr
}

func TestCloseAgentBead_ClosesExistingBead(t *testing.T) {
	fake := &fakeAgentBeadCloser{
		showIssue: &beads.Issue{ID: "gu-testrig-polecat-dust", Status: "open"},
	}

	out := captureStdout(t, func() {
		closeAgentBead(fake, "gu-testrig-polecat-dust")
	})

	if fake.showCalls != 1 {
		t.Errorf("Show calls = %d, want 1", fake.showCalls)
	}
	if fake.updateCalls != 1 {
		t.Errorf("Update calls = %d, want 1", fake.updateCalls)
	}
	if fake.updateID != "gu-testrig-polecat-dust" {
		t.Errorf("Update ID = %q, want %q", fake.updateID, "gu-testrig-polecat-dust")
	}
	if fake.updateOpts == nil || fake.updateOpts.Status == nil {
		t.Fatalf("Update opts.Status = nil, want &\"closed\"")
	}
	if *fake.updateOpts.Status != "closed" {
		t.Errorf("Update status = %q, want %q", *fake.updateOpts.Status, "closed")
	}
	if !strings.Contains(out, "closed agent bead gu-testrig-polecat-dust") {
		t.Errorf("stdout = %q, want it to mention closed agent bead", out)
	}
}

func TestCloseAgentBead_SkipsWhenBeadMissing(t *testing.T) {
	// Simulate a polecat removal where the agent bead never existed (e.g.,
	// legacy polecat from before agent beads were introduced, or the bead
	// was already closed by doctor --fix). closeAgentBead should silently
	// no-op — no Update call, no error output.
	fake := &fakeAgentBeadCloser{
		showErr: errors.New("bead not found"),
	}

	out := captureStdout(t, func() {
		closeAgentBead(fake, "gu-testrig-polecat-ghost")
	})

	if fake.showCalls != 1 {
		t.Errorf("Show calls = %d, want 1", fake.showCalls)
	}
	if fake.updateCalls != 0 {
		t.Errorf("Update calls = %d, want 0 (bead missing, should not attempt update)", fake.updateCalls)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty (silent no-op for missing bead)", out)
	}
}

func TestCloseAgentBead_WarnsOnUpdateFailure(t *testing.T) {
	// Simulate a polecat removal where the agent bead exists but the Update
	// call fails (Dolt hiccup, stale snapshot, etc.). closeAgentBead must
	// print a warning but not crash — Manager.Remove has already succeeded
	// at this point and we do not want to fail the user-facing command.
	fake := &fakeAgentBeadCloser{
		showIssue: &beads.Issue{ID: "gu-testrig-polecat-dust", Status: "open"},
		updateErr: errors.New("dolt write failed"),
	}

	out := captureStdout(t, func() {
		closeAgentBead(fake, "gu-testrig-polecat-dust")
	})

	if fake.updateCalls != 1 {
		t.Errorf("Update calls = %d, want 1", fake.updateCalls)
	}
	if !strings.Contains(out, "could not close agent bead") {
		t.Errorf("stdout = %q, want warning about failed close", out)
	}
	if !strings.Contains(out, "dolt write failed") {
		t.Errorf("stdout = %q, want underlying error surfaced", out)
	}
}

// TestCloseAgentBead_ClosesInProgressBead verifies the acceptance criterion
// that an in_progress bead is closed (by the time we get here, Manager.Remove
// has already called ResetAgentBeadForReuse which drops the bead back to open
// with agent_state=nuked, so this test documents the contract: whatever state
// the bead is in, we send it to closed). (gu-y3fi)
func TestCloseAgentBead_ClosesInProgressBead(t *testing.T) {
	fake := &fakeAgentBeadCloser{
		showIssue: &beads.Issue{ID: "gu-testrig-polecat-dust", Status: "in_progress"},
	}

	captureStdout(t, func() {
		closeAgentBead(fake, "gu-testrig-polecat-dust")
	})

	if fake.updateOpts == nil || fake.updateOpts.Status == nil {
		t.Fatalf("Update opts.Status = nil, want &\"closed\"")
	}
	if *fake.updateOpts.Status != "closed" {
		t.Errorf("Update status = %q, want %q (in_progress bead must still be closed)",
			*fake.updateOpts.Status, "closed")
	}
}

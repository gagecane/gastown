package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestSlingArgsValidator_AllMode verifies the positional-arg contract for --all.
func TestSlingArgsValidator_AllMode(t *testing.T) {
	origAll := slingAll
	t.Cleanup(func() { slingAll = origAll })

	cases := []struct {
		name    string
		all     bool
		args    []string
		wantErr bool
	}{
		{"normal mode requires a positional", false, []string{}, true},
		{"normal mode one positional ok", false, []string{"gt-abc"}, false},
		{"all mode zero positionals ok (rig via flag)", true, []string{}, false},
		{"all mode one positional ok (rig)", true, []string{"gastown"}, false},
		{"all mode two positionals rejected", true, []string{"a", "b"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slingAll = tc.all
			err := slingArgsValidator(slingCmd, tc.args)
			if (err != nil) != tc.wantErr {
				t.Errorf("slingArgsValidator(all=%v, %v) error = %v, wantErr %v", tc.all, tc.args, err, tc.wantErr)
			}
		})
	}
}

// TestParseSlingInvocation_AllRejectsOnAndReviewFlags verifies that --all is
// rejected when combined with single-bead-targeting flags.
func TestParseSlingInvocation_AllRejectsOnAndReviewFlags(t *testing.T) {
	save := func() func() {
		oAll, oOn, oRev, oGate := slingAll, slingOnTarget, slingReviews, slingReviewGate
		return func() {
			slingAll, slingOnTarget, slingReviews, slingReviewGate = oAll, oOn, oRev, oGate
		}
	}

	t.Run("--all with --on errors", func(t *testing.T) {
		restore := save()
		t.Cleanup(restore)
		slingAll, slingOnTarget, slingReviews, slingReviewGate = true, "gt-abc", "", false
		if _, err := parseSlingInvocation(); err == nil {
			t.Error("expected error for --all + --on, got nil")
		}
	})

	t.Run("--all with --review-gate errors", func(t *testing.T) {
		restore := save()
		t.Cleanup(restore)
		slingAll, slingOnTarget, slingReviews, slingReviewGate = true, "", "", true
		if _, err := parseSlingInvocation(); err == nil {
			t.Error("expected error for --all + --review-gate, got nil")
		}
	})

	t.Run("--all alone passes flag-combo check", func(t *testing.T) {
		restore := save()
		t.Cleanup(restore)
		slingAll, slingOnTarget, slingReviews, slingReviewGate = true, "", "", false
		// May still error on the polecat-role check depending on env, so only
		// assert it does NOT fail with the flag-combination messages.
		_, err := parseSlingInvocation()
		if err != nil &&
			(strings.Contains(err.Error(), "cannot be combined with --on") ||
				strings.Contains(err.Error(), "cannot be combined with --reviews")) {
			t.Errorf("--all alone should not trip flag-combo guards, got: %v", err)
		}
	})
}

// TestSortReadyBeadsByPriority verifies the priority-then-ID ordering used by
// readyDispatchableBeadIDsForRig (extracted to a pure check on Issue slices).
func TestSortReadyBeadsByPriority(t *testing.T) {
	issues := []*beads.Issue{
		{ID: "gt-c", Priority: 2},
		{ID: "gt-a", Priority: 1},
		{ID: "gt-b", Priority: 1},
		{ID: "gt-d", Priority: 0},
	}
	got := sortReadyIssueIDs(issues)
	want := []string{"gt-d", "gt-a", "gt-b", "gt-c"}
	if len(got) != len(want) {
		t.Fatalf("got %d ids, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCreateSynthesisBead_CreatesInRigDatabase is a regression test for gs-k9f:
// the synthesis bead must be created in the TARGET RIG's beads database (so it
// gets a rig prefix and slingSynthesis can verify it), not the town/hq database.
// Previously createSynthesisBead ran `bd create` from the town root, producing an
// hq-* bead that the sling verification refused, orphaning a fresh bead per retry.
func TestCreateSynthesisBead_CreatesInRigDatabase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, expectedWD := makeRoutingTownWorkspace(t)

	// Route gs- beads to the gastown rig directory and create that rig's .beads.
	routes := `{"prefix":"gs-","path":"gastown"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", ".beads"), 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	chdirConvoyTest(t, townRoot)

	pwdFile := filepath.Join(t.TempDir(), "create-pwd")
	scriptBody := `
case "$1" in
  create)
    printf '%s' "$PWD" > "` + pwdFile + `"
    echo '{"id":"gs-syn-test"}'
    ;;
  *)
    echo '[]'
    ;;
esac
`
	writeRoutingBdStub(t, scriptBody)

	// The cross-DB tracking relation is exercised elsewhere; stub it so this test
	// does not depend on a live Dolt store.
	oldTrack := addTrackingRelationFn
	addTrackingRelationFn = func(_, _, _ string) error { return nil }
	t.Cleanup(func() { addTrackingRelationFn = oldTrack })

	meta := &ConvoyMeta{ID: "hq-cv-test", Title: "Code Review: PR #1"}
	id, err := createSynthesisBead("hq-cv-test", meta, nil, nil, "pr1", "gastown")
	if err != nil {
		t.Fatalf("createSynthesisBead: %v", err)
	}
	if id != "gs-syn-test" {
		t.Errorf("synthesis ID = %q, want %q", id, "gs-syn-test")
	}

	gotPWD, err := os.ReadFile(pwdFile)
	if err != nil {
		t.Fatalf("read create pwd: %v", err)
	}
	wantWD := filepath.Join(expectedWD, "gastown")
	if resolved, err := filepath.EvalSymlinks(wantWD); err == nil && resolved != "" {
		wantWD = resolved
	}
	if strings.TrimSpace(string(gotPWD)) != wantWD {
		t.Errorf("bd create ran from %q, want rig dir %q (gs-k9f: must not run from town root)",
			strings.TrimSpace(string(gotPWD)), wantWD)
	}
}

func TestExpandOutputPath(t *testing.T) {
	tests := []struct {
		name      string
		directory string
		pattern   string
		reviewID  string
		legID     string
		want      string
	}{
		{
			name:      "basic expansion",
			directory: ".reviews/{{review_id}}",
			pattern:   "{{leg.id}}-findings.md",
			reviewID:  "abc123",
			legID:     "security",
			want:      ".reviews/abc123/security-findings.md",
		},
		{
			name:      "no templates",
			directory: ".output",
			pattern:   "results.md",
			reviewID:  "xyz",
			legID:     "test",
			want:      ".output/results.md",
		},
		{
			name:      "complex path",
			directory: "reviews/{{review_id}}/findings",
			pattern:   "leg-{{leg.id}}-analysis.md",
			reviewID:  "pr-123",
			legID:     "performance",
			want:      "reviews/pr-123/findings/leg-performance-analysis.md",
		},
		{
			name:      "go template expansion",
			directory: ".designs/{{.review_id}}",
			pattern:   "{{.leg.id}}.md",
			reviewID:  "abc123",
			legID:     "api",
			want:      ".designs/abc123/api.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandOutputPath(tt.directory, tt.pattern, tt.reviewID, tt.legID)
			if filepath.ToSlash(got) != tt.want {
				t.Errorf("expandOutputPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLegOutput(t *testing.T) {
	// Test LegOutput struct
	output := LegOutput{
		LegID:    "correctness",
		Title:    "Correctness Review",
		Status:   "closed",
		FilePath: "/tmp/findings.md",
		Content:  "## Findings\n\nNo issues found.",
		HasFile:  true,
	}

	if output.LegID != "correctness" {
		t.Errorf("LegID = %q, want %q", output.LegID, "correctness")
	}

	if output.Status != "closed" {
		t.Errorf("Status = %q, want %q", output.Status, "closed")
	}

	if !output.HasFile {
		t.Error("HasFile should be true")
	}
}

func TestConvoyMeta(t *testing.T) {
	// Test ConvoyMeta struct
	meta := ConvoyMeta{
		ID:        "hq-cv-abc",
		Title:     "Code Review: PR #123",
		Status:    "open",
		Formula:   "code-review",
		ReviewID:  "pr123",
		LegIssues: []string{"gt-leg1", "gt-leg2", "gt-leg3"},
	}

	if meta.ID != "hq-cv-abc" {
		t.Errorf("ID = %q, want %q", meta.ID, "hq-cv-abc")
	}

	if len(meta.LegIssues) != 3 {
		t.Errorf("len(LegIssues) = %d, want 3", len(meta.LegIssues))
	}
}

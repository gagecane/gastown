package upstreamsync

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestBuildResolutionBranch(t *testing.T) {
	got := buildResolutionBranch("gastown_upstream", "gu-sync-att-1234")
	want := "upstream-sync/gastown_upstream/gu-sync-att-1234"
	if got != want {
		t.Errorf("buildResolutionBranch = %q, want %q", got, want)
	}
}

func TestBuildWorkBeadTitle(t *testing.T) {
	cases := []struct {
		name string
		in   DispatchInput
		want string
	}{
		{
			name: "no files",
			in:   DispatchInput{Rig: "gu", AttemptID: "att-1"},
			want: "upstream-sync conflict in gu (att-1)",
		},
		{
			name: "one file",
			in: DispatchInput{
				Rig:             "gu",
				AttemptID:       "att-1",
				ConflictedFiles: []string{"foo.go"},
			},
			want: "upstream-sync conflict in gu: foo.go",
		},
		{
			name: "many files",
			in: DispatchInput{
				Rig:             "gu",
				AttemptID:       "att-1",
				ConflictedFiles: []string{"foo.go", "bar.go", "baz.go"},
			},
			want: "upstream-sync conflict in gu: foo.go and 2 more",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildWorkBeadTitle(tt.in); got != tt.want {
				t.Errorf("buildWorkBeadTitle = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildWorkBeadDescription_StructuredFields(t *testing.T) {
	payload := ConflictDispatchPayload{
		Version:          1,
		Rig:              "gastown_upstream",
		AttemptID:        "att-1",
		UpstreamRemote:   "upstream",
		UpstreamBranch:   "main",
		UpstreamSHA:      "abc123",
		TargetBranch:     "main",
		TargetSHA:        "def456",
		ResolutionBranch: "upstream-sync/gastown_upstream/att-1",
		ConflictedFiles:  []string{"a.go", "b.go"},
		HunkCount:        5,
		RestrictedPaths:  []string{"go.mod", "internal/auth/"},
		Strategy:         "merge",
	}
	desc := buildWorkBeadDescription(payload, `{"x":1}`)
	requiredSubstrings := []string{
		"gastown_upstream",
		"att-1",
		"upstream-sync/gastown_upstream/att-1",
		"abc123",
		"def456",
		"a.go",
		"b.go",
		"Hunk count: 5",
		"DO NOT MODIFY",
		"go.mod",
		"internal/auth/",
		"## Payload (JSON)",
		`{"x":1}`,
	}
	for _, want := range requiredSubstrings {
		if !strings.Contains(desc, want) {
			t.Errorf("description missing %q\n--- description ---\n%s", want, desc)
		}
	}
}

func TestSlingArgs_Roundtrip(t *testing.T) {
	payload := ConflictDispatchPayload{
		Version:          1,
		Rig:              "gu",
		AttemptID:        "att-1",
		UpstreamRemote:   "upstream",
		UpstreamBranch:   "main",
		UpstreamSHA:      "abc123",
		TargetBranch:     "main",
		ResolutionBranch: "upstream-sync/gu/att-1",
		ConflictedFiles:  []string{"x.go"},
		RestrictedPaths:  []string{"go.mod"},
		Strategy:         "merge",
	}
	args := slingArgs(payload)
	if args.Mode != "upstream-sync-conflict" {
		t.Errorf("Mode = %q, want upstream-sync-conflict", args.Mode)
	}
	if args.Rig != "gu" {
		t.Errorf("Rig = %q, want gu", args.Rig)
	}
	if args.AttemptID != "att-1" {
		t.Errorf("AttemptID = %q, want att-1", args.AttemptID)
	}
	if args.ResolutionBranch != "upstream-sync/gu/att-1" {
		t.Errorf("ResolutionBranch = %q, mismatch", args.ResolutionBranch)
	}
	if args.BaseBranch != "main" {
		t.Errorf("BaseBranch = %q, want main", args.BaseBranch)
	}
	if !containsString(args.ConflictedFiles, "x.go") {
		t.Errorf("ConflictedFiles missing x.go: %v", args.ConflictedFiles)
	}
	if !containsString(args.RestrictedPaths, "go.mod") {
		t.Errorf("RestrictedPaths missing go.mod: %v", args.RestrictedPaths)
	}

	// JSON round-trip yields the same shape.
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var back SlingArgs
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if back.Mode != args.Mode || back.AttemptID != args.AttemptID {
		t.Errorf("round-trip mismatch: %+v vs %+v", args, back)
	}
}

func TestDispatchConflictResolution_GuardClauses(t *testing.T) {
	// nil handles
	if _, err := DispatchConflictResolution(nil, nil, DispatchInput{}); err == nil {
		t.Errorf("expected error for nil beads")
	}
	// missing required fields
	tb := &beads.Beads{}
	if _, err := DispatchConflictResolution(tb, tb, DispatchInput{}); err == nil {
		t.Errorf("expected error for empty input")
	}
	// rig but no attempt id
	if _, err := DispatchConflictResolution(tb, tb, DispatchInput{Rig: "gu"}); err == nil {
		t.Errorf("expected error for missing attempt_id")
	}
	// no conflicts
	if _, err := DispatchConflictResolution(tb, tb, DispatchInput{
		Rig:       "gu",
		AttemptID: "att-1",
	}); err == nil {
		t.Errorf("expected error for empty conflict list")
	}
}

func TestConflictDispatchPayload_JSONStability(t *testing.T) {
	// Stable JSON tags — audit dashboards parse this shape.
	payload := ConflictDispatchPayload{
		Version:          1,
		Rig:              "gu",
		AttemptID:        "att-1",
		UpstreamRemote:   "upstream",
		UpstreamBranch:   "main",
		UpstreamSHA:      "sha1",
		TargetBranch:     "main",
		TargetSHA:        "sha2",
		ResolutionBranch: "upstream-sync/gu/att-1",
		ConflictedFiles:  []string{"a.go"},
		HunkCount:        2,
		RestrictedPaths:  []string{"go.mod"},
		Strategy:         "merge",
		EnqueuedAt:       "2026-05-29T22:00:00Z",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	keys := []string{
		`"version":1`,
		`"rig":"gu"`,
		`"attempt_id":"att-1"`,
		`"upstream_remote":"upstream"`,
		`"upstream_branch":"main"`,
		`"upstream_sha":"sha1"`,
		`"target_branch":"main"`,
		`"target_sha":"sha2"`,
		`"resolution_branch":"upstream-sync/gu/att-1"`,
		`"conflicted_files":["a.go"]`,
		`"hunk_count":2`,
		`"restricted_paths":["go.mod"]`,
		`"strategy":"merge"`,
		`"enqueued_at":"2026-05-29T22:00:00Z"`,
	}
	s := string(raw)
	for _, k := range keys {
		if !strings.Contains(s, k) {
			t.Errorf("payload JSON missing %q\n--- got ---\n%s", k, s)
		}
	}
}

func TestDispatchFormulaConstants(t *testing.T) {
	// These constants are part of the Phase 4 contract; flagging changes
	// here forces a deliberate review.
	if DispatchFormula != "mol-polecat-conflict-resolve" {
		t.Errorf("DispatchFormula = %q, want mol-polecat-conflict-resolve", DispatchFormula)
	}
	if DispatchLabel != "gt:upstream-sync-conflict" {
		t.Errorf("DispatchLabel = %q, want gt:upstream-sync-conflict", DispatchLabel)
	}
}

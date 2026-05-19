package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTownBeadsDirFrom(t *testing.T) {
	tmpDir := t.TempDir()

	// Layout: <tmp>/mayor/town.json marks tmp as the town root.
	if err := os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "mayor", "town.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0o755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}

	// Rig dir under the town root with its own .beads.
	rigBeadsDir := filepath.Join(tmpDir, "alleago_ui", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0o755); err != nil {
		t.Fatalf("mkdir rig beads: %v", err)
	}

	// From the rig beads dir we should walk up and find the town's .beads.
	got := townBeadsDirFrom(rigBeadsDir)
	if got != townBeadsDir {
		t.Fatalf("townBeadsDirFrom(rig) = %q, want %q", got, townBeadsDir)
	}

	// From the town beads dir we should also resolve to itself.
	if got := townBeadsDirFrom(townBeadsDir); got != townBeadsDir {
		t.Fatalf("townBeadsDirFrom(town) = %q, want %q", got, townBeadsDir)
	}

	// Empty input returns empty (no panic).
	if got := townBeadsDirFrom(""); got != "" {
		t.Fatalf("townBeadsDirFrom(\"\") = %q, want empty", got)
	}
}

func TestPickBestAgentBead(t *testing.T) {
	tests := []struct {
		name    string
		input   []agentBeadCandidate
		wantID  string
		wantNil bool
	}{
		{
			name:    "empty input returns nil",
			input:   nil,
			wantNil: true,
		},
		{
			name: "prefers rig-wisps over town-wisps",
			input: []agentBeadCandidate{
				{ID: "au-wisp-0ti", Status: "open", Source: "town-wisps"},
				{ID: "au-alleago_ui-refinery", Status: "open", Source: "rig-wisps"},
			},
			wantID: "au-alleago_ui-refinery",
		},
		{
			name: "prefers open over closed even if source is worse",
			input: []agentBeadCandidate{
				{ID: "au-alleago_ui-refinery", Status: "closed", Source: "rig-wisps"},
				{ID: "au-wisp-0ti", Status: "open", Source: "town-wisps"},
			},
			wantID: "au-wisp-0ti",
		},
		{
			name: "falls back to legacy hq.wisps when nothing in rig",
			input: []agentBeadCandidate{
				{ID: "au-wisp-0ti", Status: "open", Source: "town-wisps"},
			},
			wantID: "au-wisp-0ti",
		},
		{
			name: "issues table prefers rig over town",
			input: []agentBeadCandidate{
				{ID: "au-town-issue", Status: "open", Source: "town-issues"},
				{ID: "au-rig-issue", Status: "open", Source: "rig-issues"},
			},
			wantID: "au-rig-issue",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pickBestAgentBead(tc.input)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil match, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected match %q, got nil", tc.wantID)
			}
			if got.ID != tc.wantID {
				t.Fatalf("got ID %q, want %q (from %+v)", got.ID, tc.wantID, got)
			}
		})
	}
}

func TestEntryMatchesRole(t *testing.T) {
	cases := []struct {
		name string
		desc string
		role string
		want bool
	}{
		{name: "match on description", desc: "refinery for au\n\nrole_type: refinery\nrig: alleago_ui", role: "refinery", want: true},
		{name: "no match wrong role", desc: "role_type: witness\nrig: alleago_ui", role: "refinery", want: false},
		{name: "empty role matches all", desc: "anything", role: "", want: true},
		{name: "trims whitespace", desc: "  role_type: refinery  \nrig: x", role: "refinery", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := entryMatchesRole(tc.desc, nil, "", tc.role); got != tc.want {
				t.Fatalf("entryMatchesRole(%q, role=%q) = %v, want %v", tc.desc, tc.role, got, tc.want)
			}
		})
	}
}

func TestEntryMatchesRig(t *testing.T) {
	cases := []struct {
		name string
		desc string
		id   string
		rig  string
		want bool
	}{
		{name: "match on description", desc: "role_type: refinery\nrig: alleago_ui", id: "au-wisp-x", rig: "alleago_ui", want: true},
		{name: "fallback to id middle", desc: "", id: "gt-gastown-refinery", rig: "gastown", want: true},
		{name: "fallback to id trailing", desc: "", id: "au-alleago_ui", rig: "alleago_ui", want: true},
		{name: "no match", desc: "role_type: refinery\nrig: other", id: "au-wisp-x", rig: "alleago_ui", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := entryMatchesRig(tc.desc, tc.id, tc.rig); got != tc.want {
				t.Fatalf("entryMatchesRig(%q, id=%q, rig=%q) = %v, want %v", tc.desc, tc.id, tc.rig, got, tc.want)
			}
		})
	}
}

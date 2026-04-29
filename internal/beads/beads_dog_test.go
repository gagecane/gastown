package beads

import (
	"strings"
	"testing"
)

// TestFormatDogDescription covers the dog-description formatter: stable
// field order, name/location embedded, role_type/rig metadata present.
func TestFormatDogDescription(t *testing.T) {
	tests := []struct {
		name       string
		dogName    string
		location   string
		wantSubstr []string
	}{
		{
			name:     "standard dog",
			dogName:  "rex",
			location: "corp-pdx",
			wantSubstr: []string{
				"Dog: rex",
				"role_type: dog",
				"rig: town",
				"location: corp-pdx",
			},
		},
		{
			name:     "dog with hyphen name",
			dogName:  "border-collie-01",
			location: "west",
			wantSubstr: []string{
				"Dog: border-collie-01",
				"role_type: dog",
				"rig: town",
				"location: west",
			},
		},
		{
			name:     "empty location allowed",
			dogName:  "rex",
			location: "",
			wantSubstr: []string{
				"Dog: rex",
				"role_type: dog",
				"rig: town",
				"location: ",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDogDescription(tt.dogName, tt.location)
			for _, s := range tt.wantSubstr {
				if !strings.Contains(got, s) {
					t.Errorf("formatDogDescription(%q, %q) missing %q\nGot:\n%s",
						tt.dogName, tt.location, s, got)
				}
			}
		})
	}
}

// TestFormatDogDescription_FieldOrder verifies fields are in the expected
// order so parsers can rely on positional assumptions if needed.
func TestFormatDogDescription_FieldOrder(t *testing.T) {
	got := formatDogDescription("rex", "pdx")
	lines := strings.Split(got, "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d:\n%s", len(lines), got)
	}
	expected := []string{
		"Dog: rex",
		"",
		"role_type: dog",
		"rig: town",
		"location: pdx",
	}
	for i, want := range expected {
		if lines[i] != want {
			t.Errorf("line %d: got %q, want %q", i, lines[i], want)
		}
	}
}

// TestDogBeadIDTown_ExtraCases covers additional cases beyond those in
// agent_ids_test.go (e.g., hyphenated/numeric names).
func TestDogBeadIDTown_ExtraCases(t *testing.T) {
	tests := []struct {
		name    string
		dogName string
		want    string
	}{
		{"with hyphen", "border-collie", "hq-dog-border-collie"},
		{"with digits", "dog-01", "hq-dog-dog-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DogBeadIDTown(tt.dogName)
			if got != tt.want {
				t.Errorf("DogBeadIDTown(%q) = %q, want %q", tt.dogName, got, tt.want)
			}
		})
	}
}

// TestFindDogAgentBead_NotFound returns nil when no matching dog bead exists.
// This uses the in-process store path to avoid needing bd CLI.
func TestFindDogAgentBead_NotFound(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	issue, err := b.FindDogAgentBead("missing")
	if err != nil {
		t.Fatalf("FindDogAgentBead: %v", err)
	}
	if issue != nil {
		t.Errorf("FindDogAgentBead returned non-nil for missing dog: %+v", issue)
	}
}

// TestResetDogAgentBead_NoOpWhenMissing is idempotent on a missing bead.
func TestResetDogAgentBead_NoOpWhenMissing(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	if err := b.ResetDogAgentBead("missing"); err != nil {
		t.Errorf("ResetDogAgentBead: got %v, want nil (idempotent)", err)
	}
}

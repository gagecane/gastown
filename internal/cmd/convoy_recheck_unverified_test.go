package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeRecheckMockBd writes a mock bd that serves a fixed convoy `list`, a
// batched tracks `sql` lookup, per-bead `show` details, and logs every
// update/close call to mutationLog. The convoy and bead update_at timestamps are
// supplied so a test can model "tracked bead advanced after the label" vs not.
func writeRecheckMockBd(t *testing.T, binDir, beadsDir, listJSON, sqlJSON, showCases, mutationLog string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"),
		[]byte(`{"prefix":"gt-","path":"gastown/mayor/rig"}`+"\n"), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	script := `#!/bin/sh
i=0
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) eval "pos$i=\"$arg\""; i=$((i+1)) ;;
  esac
done
case "$pos0" in
  list)
    echo '` + listJSON + `'
    exit 0
    ;;
  sql)
    case "$*" in
      *"IN ("*) echo '` + sqlJSON + `' ;;
      *) echo '[]' ;;
    esac
    exit 0
    ;;
  show)
` + showCases + `
    exit 0
    ;;
  update|close)
    echo "$*" >> "` + mutationLog + `"
    exit 0
    ;;
  *)
    echo '[]'
    exit 0
    ;;
esac
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestRecheckShipUnverified_ReevaluatesAdvancedConvoy is the core gu-fnd1w
// regression guard: a convoy:ship-unverified convoy whose tracked bead advanced
// AFTER the label was applied (bead updated_at > convoy updated_at) is
// re-evaluated — its stale label is cleared (a `update --remove-label` write)
// and, with the bead now ship-verified, the convoy closes.
func TestRecheckShipUnverified_ReevaluatesAdvancedConvoy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping convoy shell-mock test on Windows")
	}
	binDir := t.TempDir()
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	mutationLog := filepath.Join(binDir, "mutations.log")

	old := "2026-06-16T04:00:00Z"
	newer := "2026-06-16T05:00:00Z"

	// hq-cv-adv: label applied at `old`; its tracked bead gt-done1 advanced at
	// `newer` (a citing commit landed) → should be re-evaluated and closed.
	listJSON := `[{"id":"hq-cv-adv","title":"Advanced","status":"open","updated_at":"` + old + `","labels":["gt:convoy","convoy:ship-unverified"]}]`
	sqlJSON := `[{"issue_id":"hq-cv-adv","target":"gt-done1"}]`
	// gt-done1 closed, shipped (close_reason cites a commit), advanced after label.
	showCases := `    case "$*" in
      *"hq-cv-adv"*) echo '[{"id":"hq-cv-adv","title":"Advanced","status":"open","issue_type":"convoy","labels":["gt:convoy","convoy:ship-unverified"]}]' ;;
      *gt-done1*) echo '[{"id":"gt-done1","status":"closed","issue_type":"task","close_reason":"shipped in abc123","updated_at":"` + newer + `"}]' ;;
      *) echo '[]' ;;
    esac`

	writeRecheckMockBd(t, binDir, beadsDir, listJSON, sqlJSON, showCases, mutationLog)

	rechecked, err := recheckShipUnverifiedConvoys(beadsDir, false)
	if err != nil {
		t.Fatalf("recheckShipUnverifiedConvoys() error: %v", err)
	}
	if len(rechecked) != 1 || rechecked[0].ConvoyID != "hq-cv-adv" {
		t.Fatalf("expected hq-cv-adv to be re-evaluated; got %v", rechecked)
	}

	// The stale label must have been cleared (checkSingleConvoy's removeShipUnverifiedLabel).
	logBytes, _ := os.ReadFile(mutationLog)
	logStr := string(logBytes)
	if !strings.Contains(logStr, "remove-label=convoy:ship-unverified") {
		t.Errorf("expected the stale ship-unverified label to be cleared; mutation log:\n%s", logStr)
	}
}

// TestRecheckShipUnverified_SkipsUnchangedConvoy guards the budget protection
// (gu-4cxuv): a ship-unverified convoy whose tracked beads have NOT advanced
// since the label was applied (bead updated_at <= convoy updated_at) must be
// filtered out in-memory — never re-evaluated, so no per-convoy subprocess and
// no churn. The whole point of the change-detection gate.
func TestRecheckShipUnverified_SkipsUnchangedConvoy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping convoy shell-mock test on Windows")
	}
	binDir := t.TempDir()
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	mutationLog := filepath.Join(binDir, "mutations.log")

	convoyTime := "2026-06-16T05:00:00Z"
	beadTime := "2026-06-16T04:00:00Z" // bead is OLDER than the label

	listJSON := `[{"id":"hq-cv-stuck","title":"Stuck","status":"open","updated_at":"` + convoyTime + `","labels":["gt:convoy","convoy:ship-unverified"]}]`
	sqlJSON := `[{"issue_id":"hq-cv-stuck","target":"gt-done1"}]`
	showCases := `    case "$*" in
      *gt-done1*) echo '[{"id":"gt-done1","status":"closed","issue_type":"task","updated_at":"` + beadTime + `"}]' ;;
      *) echo '[]' ;;
    esac`

	writeRecheckMockBd(t, binDir, beadsDir, listJSON, sqlJSON, showCases, mutationLog)

	rechecked, err := recheckShipUnverifiedConvoys(beadsDir, false)
	if err != nil {
		t.Fatalf("recheckShipUnverifiedConvoys() error: %v", err)
	}
	if len(rechecked) != 0 {
		t.Fatalf("unchanged convoy must not be re-evaluated; got %v", rechecked)
	}
	// No mutation (no label clear, no close) should have happened.
	if data, _ := os.ReadFile(mutationLog); len(data) != 0 {
		t.Errorf("unchanged convoy triggered a mutation (label clear/close); log:\n%s", data)
	}
}

// TestConvoyTrackedAdvanced covers the change-detection gate directly, including
// the fail-closed cases (unparseable convoy time, unreadable bead details).
func TestConvoyTrackedAdvanced(t *testing.T) {
	mk := func(updated string) *issueDetails { return &issueDetails{UpdatedAt: updated} }
	convoyTime := "2026-06-16T05:00:00Z"

	tests := []struct {
		name     string
		convoy   string
		tracked  []string
		details  map[string]*issueDetails
		expected bool
	}{
		{
			name:     "bead advanced after label",
			convoy:   convoyTime,
			tracked:  []string{"b1"},
			details:  map[string]*issueDetails{"b1": mk("2026-06-16T06:00:00Z")},
			expected: true,
		},
		{
			name:     "bead older than label",
			convoy:   convoyTime,
			tracked:  []string{"b1"},
			details:  map[string]*issueDetails{"b1": mk("2026-06-16T04:00:00Z")},
			expected: false,
		},
		{
			name:     "bead equal to label (not strictly after)",
			convoy:   convoyTime,
			tracked:  []string{"b1"},
			details:  map[string]*issueDetails{"b1": mk(convoyTime)},
			expected: false,
		},
		{
			name:     "one of several beads advanced",
			convoy:   convoyTime,
			tracked:  []string{"b1", "b2"},
			details:  map[string]*issueDetails{"b1": mk("2026-06-16T04:00:00Z"), "b2": mk("2026-06-16T06:00:00Z")},
			expected: true,
		},
		{
			name:     "unparseable convoy time fails closed",
			convoy:   "not-a-time",
			tracked:  []string{"b1"},
			details:  map[string]*issueDetails{"b1": mk("2026-06-16T06:00:00Z")},
			expected: false,
		},
		{
			name:     "missing bead details ignored",
			convoy:   convoyTime,
			tracked:  []string{"b1"},
			details:  map[string]*issueDetails{},
			expected: false,
		},
		{
			name:     "unparseable bead time ignored",
			convoy:   convoyTime,
			tracked:  []string{"b1"},
			details:  map[string]*issueDetails{"b1": mk("")},
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convoyTrackedAdvanced(tt.convoy, tt.tracked, tt.details)
			if got != tt.expected {
				t.Errorf("convoyTrackedAdvanced(%q, %v) = %v, want %v", tt.convoy, tt.tracked, got, tt.expected)
			}
		})
	}
}

// TestRecheckShipUnverified_NoConvoys verifies the empty path returns cleanly.
func TestRecheckShipUnverified_NoConvoys(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping convoy shell-mock test on Windows")
	}
	binDir := t.TempDir()
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	mutationLog := filepath.Join(binDir, "mutations.log")

	writeRecheckMockBd(t, binDir, beadsDir, "[]", "[]", `    case "$*" in *) echo '[]' ;; esac`, mutationLog)

	rechecked, err := recheckShipUnverifiedConvoys(beadsDir, false)
	if err != nil {
		t.Fatalf("recheckShipUnverifiedConvoys() error: %v", err)
	}
	if len(rechecked) != 0 {
		t.Fatalf("expected no rechecked convoys, got %v", rechecked)
	}
}

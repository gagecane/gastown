package witness

import (
	"fmt"
	"strings"
	"testing"
)

// showJSON builds a one-element `bd show --json` payload for a blocked bead with
// the given dependency edges. Each dep is "id:status" and is emitted as a
// dependency_type=blocks edge. desc is the bead description (carries attachment
// fields when a molecule is attached).
func showJSON(id, desc string, deps ...string) string {
	var depJSON []string
	for _, d := range deps {
		parts := strings.SplitN(d, ":", 2)
		depJSON = append(depJSON, fmt.Sprintf(
			`{"id":%q,"status":%q,"dependency_type":"blocks"}`, parts[0], parts[1]))
	}
	return fmt.Sprintf(
		`[{"id":%q,"status":"blocked","description":%q,"dependencies":[%s]}]`,
		id, desc, strings.Join(depJSON, ","))
}

// stalker wires a mock bd that answers a `list --status=blocked` with the given
// bead id and a `show` with the given payload, capturing every Run mutation.
func stalker(t *testing.T, listJSON, showResult string) (*BdCli, *[]string) {
	t.Helper()
	var runs []string
	bd, _ := mockBd(
		func(args []string) (string, error) {
			switch args[0] {
			case "list":
				return listJSON, nil
			case "show":
				return showResult, nil
			}
			return "", fmt.Errorf("unexpected exec: %v", args)
		},
		func(args []string) error {
			runs = append(runs, strings.Join(args, " "))
			return nil
		},
	)
	return bd, &runs
}

func TestDetectStaleParkedBeads_UnblocksWhenAllBlockersClosed(t *testing.T) {
	t.Parallel()
	bd, runs := stalker(t,
		`[{"id":"gs-dep"}]`,
		showJSON("gs-dep", "no attachment", "gs-blk1:closed", "gs-blk2:closed"))

	result := DetectStaleParkedBeads(bd, t.TempDir(), "testrig")

	if result.Checked != 1 {
		t.Fatalf("Checked = %d, want 1", result.Checked)
	}
	if len(result.Recovered) != 1 {
		t.Fatalf("Recovered = %d, want 1", len(result.Recovered))
	}
	rec := result.Recovered[0]
	if !rec.Unblocked {
		t.Errorf("Unblocked = false, want true (err=%v)", rec.Error)
	}
	if len(rec.ResolvedBlockers) != 2 {
		t.Errorf("ResolvedBlockers = %v, want 2 entries", rec.ResolvedBlockers)
	}
	joined := strings.Join(*runs, "\n")
	if !strings.Contains(joined, "dep remove gs-dep gs-blk1") ||
		!strings.Contains(joined, "dep remove gs-dep gs-blk2") {
		t.Errorf("expected both closed blockers removed, runs=%v", *runs)
	}
	if !strings.Contains(joined, "update gs-dep --status=open") {
		t.Errorf("expected status flip to open, runs=%v", *runs)
	}
}

func TestDetectStaleParkedBeads_LeavesBeadWithOpenBlocker(t *testing.T) {
	t.Parallel()
	bd, runs := stalker(t,
		`[{"id":"gs-dep"}]`,
		showJSON("gs-dep", "no attachment", "gs-blk1:closed", "gs-blk2:open"))

	result := DetectStaleParkedBeads(bd, t.TempDir(), "testrig")

	if len(result.Recovered) != 0 {
		t.Fatalf("Recovered = %d, want 0 (a still-open blocker must keep the park)", len(result.Recovered))
	}
	if len(*runs) != 0 {
		t.Errorf("expected no mutations, runs=%v", *runs)
	}
}

func TestDetectStaleParkedBeads_LeavesBeadWithNoExternalBlocker(t *testing.T) {
	t.Parallel()
	// Only the bead's own attached molecule is a dependency — there is no
	// external blocker, so this is not a dependency park.
	desc := "attached_molecule: gs-mol\nattached_formula: mol-polecat-work\n"
	bd, runs := stalker(t,
		`[{"id":"gs-dep"}]`,
		showJSON("gs-dep", desc, "gs-mol:open"))

	result := DetectStaleParkedBeads(bd, t.TempDir(), "testrig")

	if len(result.Recovered) != 0 {
		t.Fatalf("Recovered = %d, want 0 (no external blocker)", len(result.Recovered))
	}
	if len(*runs) != 0 {
		t.Errorf("expected no mutations, runs=%v", *runs)
	}
}

func TestDetectStaleParkedBeads_RemovesStaleMoleculeBond(t *testing.T) {
	t.Parallel()
	// External blocker closed AND the attached molecule is closed → unblock and
	// drop the stale molecule bond so re-dispatch is clean.
	desc := "attached_molecule: gs-mol\nattached_formula: mol-polecat-work\n"
	bd, runs := stalker(t,
		`[{"id":"gs-dep"}]`,
		showJSON("gs-dep", desc, "gs-blk1:closed", "gs-mol:closed"))

	result := DetectStaleParkedBeads(bd, t.TempDir(), "testrig")

	if len(result.Recovered) != 1 {
		t.Fatalf("Recovered = %d, want 1", len(result.Recovered))
	}
	rec := result.Recovered[0]
	if rec.DetachedMolecule != "gs-mol" {
		t.Errorf("DetachedMolecule = %q, want gs-mol", rec.DetachedMolecule)
	}
	// The molecule must not be counted as an external blocker.
	if len(rec.ResolvedBlockers) != 1 || rec.ResolvedBlockers[0] != "gs-blk1" {
		t.Errorf("ResolvedBlockers = %v, want [gs-blk1]", rec.ResolvedBlockers)
	}
	joined := strings.Join(*runs, "\n")
	if !strings.Contains(joined, "dep remove gs-dep gs-mol") {
		t.Errorf("expected stale molecule bond removed, runs=%v", *runs)
	}
}

func TestDetectStaleParkedBeads_SkipsMountainSkipped(t *testing.T) {
	t.Parallel()
	// A Mountain-Eater park (mountain:skipped) is deliberate and must never be
	// auto-unblocked, even with all blockers closed.
	show := fmt.Sprintf(
		`[{"id":"gs-dep","status":"blocked","description":"x","labels":[%q],`+
			`"dependencies":[{"id":"gs-blk1","status":"closed","dependency_type":"blocks"}]}]`,
		MountainSkippedLabel)
	bd, runs := stalker(t, `[{"id":"gs-dep"}]`, show)

	result := DetectStaleParkedBeads(bd, t.TempDir(), "testrig")

	if len(result.Recovered) != 0 {
		t.Fatalf("Recovered = %d, want 0 (mountain:skipped is a deliberate park)", len(result.Recovered))
	}
	if len(*runs) != 0 {
		t.Errorf("expected no mutations on mountain:skipped bead, runs=%v", *runs)
	}
}

func TestDetectStaleParkedBeads_EmptyList(t *testing.T) {
	t.Parallel()
	bd, runs := stalker(t, "[]", "")
	result := DetectStaleParkedBeads(bd, t.TempDir(), "testrig")
	if result.Checked != 0 || len(result.Recovered) != 0 {
		t.Errorf("Checked=%d Recovered=%d, want 0/0", result.Checked, len(result.Recovered))
	}
	if len(*runs) != 0 {
		t.Errorf("expected no mutations, runs=%v", *runs)
	}
}

func TestDetectStaleParkedBeads_ListError(t *testing.T) {
	t.Parallel()
	bdErr := fmt.Errorf("bd: not found")
	bd, _ := mockBd(
		func(args []string) (string, error) { return "", bdErr },
		func(args []string) error { return bdErr },
	)
	result := DetectStaleParkedBeads(bd, "/nonexistent", "testrig")
	if len(result.Errors) == 0 {
		t.Error("expected an error when bd list fails")
	}
	if len(result.Recovered) != 0 {
		t.Errorf("Recovered = %d, want 0", len(result.Recovered))
	}
}

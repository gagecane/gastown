package feed

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// helper: build a mock HealthDataSource and a StuckDetector from it.
func newTestDetector(source HealthDataSource) *StuckDetector {
	return NewStuckDetectorWithSource(source)
}

// TestPrintProblems_EmptyEmitsSentinel confirms that when no agents exist,
// the output contains a "no problem agents detected" sentinel. This lets
// callers distinguish "healthy" from "detector crashed".
func TestPrintProblems_EmptyEmitsSentinel(t *testing.T) {
	source := newMockHealthSource()
	detector := newTestDetector(source)

	var buf bytes.Buffer
	if err := printProblemsFromDetector(detector, &buf, PrintProblemsOptions{}); err != nil {
		t.Fatalf("printProblemsFromDetector: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "no problem agents") {
		t.Errorf("expected sentinel line when no agents; got:\n%s", got)
	}
}

// TestPrintProblems_DefaultOnlyProblems verifies that by default only agents
// needing attention (gupp, stalled, zombie) are emitted — working/idle are
// hidden. This is the headline fix for the bug: --plain --problems must
// actually filter output to problem agents.
func TestPrintProblems_DefaultOnlyProblems(t *testing.T) {
	source := newMockHealthSource()

	// Stalled agent (25m idle, hooked work) — should appear.
	stalledID := beads.PolecatBeadID("myrig", "stalled-cat")
	source.agents[stalledID] = &beads.Issue{
		ID:        stalledID,
		UpdatedAt: time.Now().Add(-25 * time.Minute).Format(time.RFC3339),
		HookBead:  "gu-work1",
	}
	// PolecatSessionName(PrefixFor("myrig"), name) → "gt-stalled-cat"
	source.sessions["gt-stalled-cat"] = true

	// Working agent (2m idle, hooked work) — should NOT appear by default.
	workingID := beads.PolecatBeadID("myrig", "busy-cat")
	source.agents[workingID] = &beads.Issue{
		ID:        workingID,
		UpdatedAt: time.Now().Add(-2 * time.Minute).Format(time.RFC3339),
		HookBead:  "gu-work2",
	}
	source.sessions["gt-busy-cat"] = true

	// Idle agent (no hook) — should NOT appear.
	idleID := beads.PolecatBeadID("myrig", "lazy-cat")
	source.agents[idleID] = &beads.Issue{
		ID:        idleID,
		UpdatedAt: time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
	}
	source.sessions["gt-lazy-cat"] = true

	detector := newTestDetector(source)

	var buf bytes.Buffer
	if err := printProblemsFromDetector(detector, &buf, PrintProblemsOptions{}); err != nil {
		t.Fatalf("printProblemsFromDetector: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "stalled-cat") {
		t.Errorf("expected stalled-cat in output; got:\n%s", got)
	}
	if strings.Contains(got, "busy-cat") {
		t.Errorf("working agent (busy-cat) should not appear in default output; got:\n%s", got)
	}
	if strings.Contains(got, "lazy-cat") {
		t.Errorf("idle agent (lazy-cat) should not appear in default output; got:\n%s", got)
	}
}

// TestPrintProblems_AllFlagEmitsEveryone verifies that All=true emits
// working and idle agents alongside problem agents.
func TestPrintProblems_AllFlagEmitsEveryone(t *testing.T) {
	source := newMockHealthSource()

	stalledID := beads.PolecatBeadID("myrig", "stalled-cat")
	source.agents[stalledID] = &beads.Issue{
		ID:        stalledID,
		UpdatedAt: time.Now().Add(-25 * time.Minute).Format(time.RFC3339),
		HookBead:  "gu-work1",
	}
	source.sessions["gt-stalled-cat"] = true

	workingID := beads.PolecatBeadID("myrig", "busy-cat")
	source.agents[workingID] = &beads.Issue{
		ID:        workingID,
		UpdatedAt: time.Now().Add(-2 * time.Minute).Format(time.RFC3339),
		HookBead:  "gu-work2",
	}
	source.sessions["gt-busy-cat"] = true

	detector := newTestDetector(source)

	var buf bytes.Buffer
	if err := printProblemsFromDetector(detector, &buf, PrintProblemsOptions{All: true}); err != nil {
		t.Fatalf("printProblemsFromDetector: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "stalled-cat") {
		t.Errorf("All=true: expected stalled-cat; got:\n%s", got)
	}
	if !strings.Contains(got, "busy-cat") {
		t.Errorf("All=true: expected busy-cat; got:\n%s", got)
	}
}

// TestPrintProblems_RigFilter verifies that Rig="foo" drops agents from
// other rigs.
func TestPrintProblems_RigFilter(t *testing.T) {
	source := newMockHealthSource()

	stalledA := beads.PolecatBeadID("riga", "cat-a")
	source.agents[stalledA] = &beads.Issue{
		ID:        stalledA,
		UpdatedAt: time.Now().Add(-25 * time.Minute).Format(time.RFC3339),
		HookBead:  "gu-a",
	}
	source.sessions["gt-cat-a"] = true

	stalledB := beads.PolecatBeadID("rigb", "cat-b")
	source.agents[stalledB] = &beads.Issue{
		ID:        stalledB,
		UpdatedAt: time.Now().Add(-25 * time.Minute).Format(time.RFC3339),
		HookBead:  "gu-b",
	}
	source.sessions["gt-cat-b"] = true

	detector := newTestDetector(source)

	var buf bytes.Buffer
	if err := printProblemsFromDetector(detector, &buf, PrintProblemsOptions{Rig: "riga"}); err != nil {
		t.Fatalf("printProblemsFromDetector: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "cat-a") {
		t.Errorf("Rig=riga: expected cat-a; got:\n%s", got)
	}
	if strings.Contains(got, "cat-b") {
		t.Errorf("Rig=riga: cat-b should be filtered out; got:\n%s", got)
	}
}

// TestPrintProblems_DetectorError surfaces errors from CheckAll verbatim.
func TestPrintProblems_DetectorError(t *testing.T) {
	source := newMockHealthSource()
	source.listErr = errors.New("boom")
	detector := newTestDetector(source)

	var buf bytes.Buffer
	err := printProblemsFromDetector(detector, &buf, PrintProblemsOptions{})
	if err == nil {
		t.Fatalf("expected error from detector failure; got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected underlying error wrapped; got: %v", err)
	}
}

// TestFormatProblemLine_Stable pins the output format so downstream parsers
// (shell pipelines, witness scripts) don't silently break.
func TestFormatProblemLine_Stable(t *testing.T) {
	agent := &ProblemAgent{
		Name:          "rust",
		Role:          "polecat",
		Rig:           "myrig",
		State:         StateGUPPViolation,
		IdleMinutes:   45,
		CurrentBeadID: "gu-xyz",
		ActionHint:    "GUPP violation: hooked work + 45m no progress",
	}

	line := formatProblemLine(agent)

	// Spot-check every field. Don't assert exact column widths — that's
	// cosmetic and would make the test brittle.
	for _, want := range []string{
		"gupp",        // state label
		"🔥",           // state symbol
		"45m",         // duration
		"gu-xyz",      // bead ID
		"myrig",       // rig
		"polecat/rust", // role/name
		"GUPP violation",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("formatProblemLine missing %q; got: %q", want, line)
		}
	}
}

// TestFormatProblemLine_MissingOptionalFields confirms that empty/unset
// fields render as "-" placeholders rather than blank columns (which would
// break whitespace-based parsers).
func TestFormatProblemLine_MissingOptionalFields(t *testing.T) {
	agent := &ProblemAgent{
		Role:        "witness",
		State:       StateZombie,
		IdleMinutes: 5,
	}

	line := formatProblemLine(agent)

	if !strings.Contains(line, "zombie") {
		t.Errorf("expected state label in output; got: %q", line)
	}
	// Name empty -> just "witness", not "witness/"
	if strings.Contains(line, "witness/") {
		t.Errorf("empty name should not produce trailing slash; got: %q", line)
	}
	// Bead ID and rig should be "-" when missing
	if !strings.Contains(line, " - ") {
		t.Errorf("expected '-' placeholder for empty fields; got: %q", line)
	}
}

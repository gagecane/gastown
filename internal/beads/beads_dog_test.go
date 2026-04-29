package beads

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestFormatDogDescription_ContainsRequiredMetadata verifies that the dog
// description contains the metadata fields the mail router relies on:
// role_type, rig, and location. These are parsed downstream to resolve
// the dog's agent address, so the exact key names matter.
func TestFormatDogDescription_ContainsRequiredMetadata(t *testing.T) {
	desc := formatDogDescription("rex", "corp-pdx")

	// The description must declare the dog's identity on the first line
	// for human readability in bd show output.
	if !strings.HasPrefix(desc, "Dog: rex") {
		t.Errorf("description should start with 'Dog: rex', got: %q", desc)
	}

	wantLines := []string{
		"role_type: dog",
		"rig: town",
		"location: corp-pdx",
	}
	for _, line := range wantLines {
		if !strings.Contains(desc, line) {
			t.Errorf("description missing line %q:\n%s", line, desc)
		}
	}
}

// TestFormatDogDescription_EmptyName verifies that a dog with an empty name
// still produces a well-formed description. This shouldn't happen in
// practice (validated upstream), but the formatter must not panic.
func TestFormatDogDescription_EmptyName(t *testing.T) {
	desc := formatDogDescription("", "somewhere")
	if !strings.Contains(desc, "role_type: dog") {
		t.Errorf("description missing role_type: %q", desc)
	}
	if !strings.Contains(desc, "location: somewhere") {
		t.Errorf("description missing location: %q", desc)
	}
}

// TestFormatDogDescription_LocationWithSpecialChars verifies that location
// values with characters that might need escaping (e.g., hyphens, dots)
// are embedded verbatim in the description.
func TestFormatDogDescription_LocationWithSpecialChars(t *testing.T) {
	tests := []struct {
		name     string
		location string
	}{
		{"airport code", "pdx"},
		{"corp prefix", "corp-pdx"},
		{"with dots", "us-west-2.internal"},
		{"with slashes", "region/us-west-2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc := formatDogDescription("alpha", tt.location)
			wantLine := "location: " + tt.location
			if !strings.Contains(desc, wantLine) {
				t.Errorf("description missing %q:\n%s", wantLine, desc)
			}
		})
	}
}

// TestFormatDogDescription_LineStructure verifies the description uses
// newline separators (not \r\n or ;). Downstream parsers split on \n.
func TestFormatDogDescription_LineStructure(t *testing.T) {
	desc := formatDogDescription("alpha", "pdx")
	lines := strings.Split(desc, "\n")
	// Expected: "Dog: alpha", "", "role_type: dog", "rig: town", "location: pdx"
	if len(lines) != 5 {
		t.Errorf("expected 5 lines, got %d:\n%s", len(lines), desc)
	}
	if lines[0] != "Dog: alpha" {
		t.Errorf("line 0 = %q, want 'Dog: alpha'", lines[0])
	}
	if lines[1] != "" {
		t.Errorf("line 1 = %q, want blank separator", lines[1])
	}
}

// installMockBDForDogCreate installs a mock bd that accepts `create` and
// returns a JSON issue. Other subcommands exit 0 with no output. This lets
// CreateDogAgentBead be tested without a real Dolt server.
//
// The mock records each invocation to a log file so tests can assert on
// the exact args passed.
func installMockBDForDogCreate(t *testing.T) (logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("dog create mock uses POSIX shell")
	}

	binDir := t.TempDir()
	logPath = filepath.Join(binDir, "bd.log")

	script := `#!/bin/sh
LOG_FILE='` + logPath + `'
printf '%s\n' "$*" >> "$LOG_FILE"

cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done

case "$cmd" in
  version)
    exit 0
    ;;
  create)
    # Parse --id=<value> from the args to echo back.
    id=""
    for arg in "$@"; do
      case "$arg" in
        --id=*) id="${arg#--id=}" ;;
      esac
    done
    printf '{"id":"%s","title":"Dog: mock","status":"open"}\n' "$id"
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// TestCreateDogAgentBead_UsesCanonicalID verifies that CreateDogAgentBead
// constructs the bead with the canonical hq-dog-<name> ID, as produced
// by DogBeadIDTown.
func TestCreateDogAgentBead_UsesCanonicalID(t *testing.T) {
	logPath := installMockBDForDogCreate(t)
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	b := NewIsolated(tmpDir)
	issue, err := b.CreateDogAgentBead("rex", "corp-pdx")
	if err != nil {
		t.Fatalf("CreateDogAgentBead: %v", err)
	}
	if issue == nil {
		t.Fatal("expected issue, got nil")
	}

	want := DogBeadIDTown("rex")
	if issue.ID != want {
		t.Errorf("issue.ID = %q, want %q", issue.ID, want)
	}

	// Verify the mock bd received --id=<canonical ID>.
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	log := string(data)
	if !strings.Contains(log, "--id="+want) {
		t.Errorf("mock bd log missing canonical --id=%s:\n%s", want, log)
	}
}

// TestCreateDogAgentBead_SetsRequiredLabels verifies that the bead is
// created with labels gt:agent, role_type:dog, rig:town, and
// location:<location>. The mail router and agent listing queries rely
// on these exact label strings.
func TestCreateDogAgentBead_SetsRequiredLabels(t *testing.T) {
	logPath := installMockBDForDogCreate(t)
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	b := NewIsolated(tmpDir)
	if _, err := b.CreateDogAgentBead("spot", "iad"); err != nil {
		t.Fatalf("CreateDogAgentBead: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	log := string(data)

	// The --labels flag is a comma-separated list. Verify each expected
	// label appears in the args stream.
	wantLabels := []string{
		"gt:agent",
		"role_type:dog",
		"rig:town",
		"location:iad",
	}
	for _, label := range wantLabels {
		if !strings.Contains(log, label) {
			t.Errorf("mock bd log missing label %q:\n%s", label, log)
		}
	}
}

// TestCreateDogAgentBead_IncludesTypeAndTitle verifies that the bead is
// created with type=task and a "Dog: <name>" title. Dogs are modeled
// as task-type agents (not a separate type) so FindDogAgentBead can
// discover them via title+label matching.
func TestCreateDogAgentBead_IncludesTypeAndTitle(t *testing.T) {
	logPath := installMockBDForDogCreate(t)
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	b := NewIsolated(tmpDir)
	if _, err := b.CreateDogAgentBead("alpha", "pdx"); err != nil {
		t.Fatalf("CreateDogAgentBead: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	log := string(data)

	if !strings.Contains(log, "--type=task") {
		t.Errorf("mock bd log missing --type=task:\n%s", log)
	}
	if !strings.Contains(log, "--title=Dog: alpha") {
		t.Errorf("mock bd log missing --title=Dog: alpha:\n%s", log)
	}
}

// TestCreateDogAgentBead_IsolatedModeOmitsActor verifies that in isolated
// (test) mode, CreateDogAgentBead does NOT pass --actor=<inherited BD_ACTOR>.
// This is part of the safety contract that prevents tests from
// contaminating production beads with actor-based routing.
func TestCreateDogAgentBead_IsolatedModeOmitsActor(t *testing.T) {
	logPath := installMockBDForDogCreate(t)
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Set BD_ACTOR in the environment — NewIsolated must ignore it.
	t.Setenv("BD_ACTOR", "should-not-leak@production")

	b := NewIsolated(tmpDir)
	if _, err := b.CreateDogAgentBead("rex", "pdx"); err != nil {
		t.Fatalf("CreateDogAgentBead: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	log := string(data)

	if strings.Contains(log, "should-not-leak@production") {
		t.Errorf("isolated mode leaked BD_ACTOR into bd invocation:\n%s", log)
	}
	if strings.Contains(log, "--actor=") {
		t.Errorf("isolated mode passed --actor= flag (should be suppressed):\n%s", log)
	}
}

// installMockBDForDogFind installs a mock bd that returns a fixed list
// payload for `bd list --json` and a fixed show payload. This lets
// FindDogAgentBead be tested deterministically.
func installMockBDForDogFind(t *testing.T, listOutput string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("dog find mock uses POSIX shell")
	}

	binDir := t.TempDir()

	script := `#!/bin/sh
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done

case "$cmd" in
  version)
    exit 0
    ;;
  list)
    printf '%s\n' "$MOCK_BD_LIST_OUTPUT"
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOCK_BD_LIST_OUTPUT", listOutput)
}

// TestFindDogAgentBead_MatchesTitleAndLabel verifies FindDogAgentBead
// returns the dog whose title is "Dog: <name>" AND has the role_type:dog
// label. The title alone is insufficient — another agent could
// coincidentally have the same title.
func TestFindDogAgentBead_MatchesTitleAndLabel(t *testing.T) {
	listOutput := `[
		{"id":"hq-dog-rex","title":"Dog: rex","labels":["gt:agent","role_type:dog","rig:town","location:pdx"]},
		{"id":"hq-dog-spot","title":"Dog: spot","labels":["gt:agent","role_type:dog","rig:town","location:iad"]}
	]`
	installMockBDForDogFind(t, listOutput)

	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	b := NewIsolated(tmpDir)

	issue, err := b.FindDogAgentBead("spot")
	if err != nil {
		t.Fatalf("FindDogAgentBead: %v", err)
	}
	if issue == nil {
		t.Fatal("expected to find dog spot, got nil")
	}
	if issue.ID != "hq-dog-spot" {
		t.Errorf("issue.ID = %q, want hq-dog-spot", issue.ID)
	}
	if issue.Title != "Dog: spot" {
		t.Errorf("issue.Title = %q, want 'Dog: spot'", issue.Title)
	}
}

// TestFindDogAgentBead_NotFound verifies that FindDogAgentBead returns
// (nil, nil) when no matching dog exists. Callers rely on this to
// distinguish "no such dog" from a real error.
func TestFindDogAgentBead_NotFound(t *testing.T) {
	listOutput := `[
		{"id":"hq-dog-rex","title":"Dog: rex","labels":["gt:agent","role_type:dog"]}
	]`
	installMockBDForDogFind(t, listOutput)

	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	b := NewIsolated(tmpDir)

	issue, err := b.FindDogAgentBead("missing")
	if err != nil {
		t.Fatalf("FindDogAgentBead: %v", err)
	}
	if issue != nil {
		t.Errorf("expected nil, got %+v", issue)
	}
}

// TestFindDogAgentBead_IgnoresNonDogAgents verifies that an agent bead with
// the right title shape ("Dog: xyz") but MISSING the role_type:dog label
// is not returned. This guards against accidental matches in environments
// where other agents happen to use a "Dog:" prefix in titles.
func TestFindDogAgentBead_IgnoresNonDogAgents(t *testing.T) {
	// This entry has the title pattern but lacks role_type:dog.
	listOutput := `[
		{"id":"hq-impostor","title":"Dog: rex","labels":["gt:agent","role_type:polecat"]}
	]`
	installMockBDForDogFind(t, listOutput)

	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	b := NewIsolated(tmpDir)

	issue, err := b.FindDogAgentBead("rex")
	if err != nil {
		t.Fatalf("FindDogAgentBead: %v", err)
	}
	if issue != nil {
		t.Errorf("impostor agent should not match: got %+v", issue)
	}
}

// TestResetDogAgentBead_IdempotentWhenMissing verifies that ResetDogAgentBead
// returns nil (success) when the dog bead doesn't exist. This is required
// for cleanup paths that may be re-run after a partial failure.
func TestResetDogAgentBead_IdempotentWhenMissing(t *testing.T) {
	installMockBDForDogFind(t, `[]`)

	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	b := NewIsolated(tmpDir)

	if err := b.ResetDogAgentBead("nonexistent"); err != nil {
		t.Errorf("ResetDogAgentBead for missing dog should be idempotent, got: %v", err)
	}
}

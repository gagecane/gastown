package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/templates"
)

func TestTownCLAUDEmdCheck_Missing(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)

	if result.Status != StatusError {
		t.Errorf("expected StatusError for missing file, got %v", result.Status)
	}
	if !check.fileMissing {
		t.Error("expected fileMissing=true")
	}
}

func TestTownCLAUDEmdCheck_Complete(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Write the canonical content
	canonical := templates.TownRootCLAUDEmd()
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(canonical), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK for complete file, got %v: %s", result.Status, result.Message)
	}
}

func TestTownCLAUDEmdCheck_MissingSections(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Write only the identity anchor (no Dolt or communication sections)
	content := `# Gas Town

This is a Gas Town workspace. Your identity and role are determined by ` + "`gt prime`" + `.

Run ` + "`gt prime`" + ` for full context after compaction, clear, or new session.
`
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning for missing sections, got %v", result.Status)
	}
	if len(check.missingSections) != 2 {
		t.Errorf("expected 2 missing sections, got %d", len(check.missingSections))
	}
}

func TestTownCLAUDEmdCheck_PartialSections(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Write identity anchor + Dolt section but no communication hygiene
	content := `# Gas Town

This is a Gas Town workspace.

## Dolt Server — Operational Awareness

Dolt is the data plane for beads.
`
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %v", result.Status)
	}
	if len(check.missingSections) != 1 {
		t.Errorf("expected 1 missing section, got %d", len(check.missingSections))
	}
	if check.missingSections[0].Name != "Communication hygiene" {
		t.Errorf("expected 'Communication hygiene' missing, got %q", check.missingSections[0].Name)
	}
}

func TestTownCLAUDEmdCheck_Fix_MissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Fatalf("expected StatusError, got %v", result.Status)
	}

	// Fix should create the file from canonical
	if err := check.Fix(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify file was created
	data, err := os.ReadFile(filepath.Join(tmpDir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.Contains(content, "## Dolt Server") {
		t.Error("created file missing Dolt Server section")
	}
	if !strings.Contains(content, "### Communication hygiene") {
		t.Error("created file missing Communication hygiene section")
	}
}

func TestTownCLAUDEmdCheck_Fix_AppendSections(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Write minimal anchor + a user custom section
	original := `# Gas Town

This is a Gas Town workspace.

## My Custom Section

This is user-added content that should be preserved.
`
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v", result.Status)
	}

	// Fix should append missing sections
	if err := check.Fix(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify file was updated
	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// User's custom section should be preserved
	if !strings.Contains(content, "## My Custom Section") {
		t.Error("user custom section was not preserved")
	}
	if !strings.Contains(content, "user-added content") {
		t.Error("user custom content was not preserved")
	}

	// Missing sections should be appended
	if !strings.Contains(content, "## Dolt Server") {
		t.Error("Dolt Server section was not appended")
	}
	if !strings.Contains(content, "### Communication hygiene") {
		t.Error("Communication hygiene section was not appended")
	}
}

func TestTownCLAUDEmdCheck_Fix_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Write the canonical content
	canonical := templates.TownRootCLAUDEmd()
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(canonical), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("expected StatusOK, got %v", result.Status)
	}

	// Fix on an OK file should be a no-op
	if err := check.Fix(ctx); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != canonical {
		t.Error("fix modified a complete file (should be idempotent)")
	}
}

// staleKillQuitCmd is the literal command line that earlier versions of the
// town-root CLAUDE.md instructed agents to run. The canonical template
// no longer contains this exact substring (it warns against it in prose
// only), so its presence is the unambiguous signal that the file is stale.
const staleKillQuitCmd = "kill -QUIT $(cat"

// TestTownCLAUDEmdCheck_StaleContent_KillQuit verifies the doctor flags a
// file that contains the legacy "kill -QUIT" Dolt instruction even when all
// required sections are present.
//
// Regression test for gu-orhn / incident gc-wisp-2yc7: agents followed the
// documented "safe" goroutine-dump procedure and the SIGQUIT terminated
// Dolt mid-investigation.
func TestTownCLAUDEmdCheck_StaleContent_KillQuit(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	stale := `# Gas Town

Run ` + "`gt prime`" + ` for full context.

## Dolt Server — Operational Awareness

` + "```bash\nkill -QUIT $(cat ~/gt/.dolt-data/dolt.pid)\n```" + `

### Communication hygiene

Use nudges.
`
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning for stale content, got %v: %s", result.Status, result.Message)
	}
	if len(check.stalePatterns) != 1 {
		t.Fatalf("expected 1 stale pattern, got %d", len(check.stalePatterns))
	}
	if check.stalePatterns[0].Substring != staleKillQuitCmd {
		t.Errorf("expected stale pattern substring %q, got %q",
			staleKillQuitCmd, check.stalePatterns[0].Substring)
	}
}

// TestTownCLAUDEmdCheck_Fix_ReplacesStaleDoltSection verifies Fix swaps the
// legacy Dolt section out wholesale when stale content is detected.
func TestTownCLAUDEmdCheck_Fix_ReplacesStaleDoltSection(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	stale := `# Gas Town

Run ` + "`gt prime`" + ` for full context.

## Dolt Server — Operational Awareness

Old text claiming SIGQUIT is safe:

` + "```bash\nkill -QUIT $(cat ~/gt/.dolt-data/dolt.pid)\n```" + `

### Communication hygiene

Use nudges.

## My Custom Section

This is user-added content that must survive.
`
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	if result := check.Run(ctx); result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v: %s", result.Status, result.Message)
	}
	if err := check.Fix(ctx); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if strings.Contains(content, staleKillQuitCmd) {
		t.Errorf("Fix should have removed the stale %q command from the file",
			staleKillQuitCmd)
	}
	if !strings.Contains(content, "gt dolt dump") {
		t.Error("Fix should have inserted the canonical 'gt dolt dump' instruction")
	}
	if !strings.Contains(content, "## My Custom Section") {
		t.Error("Fix must preserve unrelated user sections")
	}
	if !strings.Contains(content, "user-added content that must survive") {
		t.Error("Fix must preserve unrelated user content")
	}

	// Re-running Run should now report OK.
	check2 := NewTownCLAUDEmdCheck()
	if result := check2.Run(ctx); result.Status != StatusOK {
		t.Errorf("expected StatusOK after Fix, got %v: %s", result.Status, result.Message)
	}
}

// TestTownCLAUDEmdCheck_Fix_CollapsesDuplicateDoltSections verifies that when
// the legacy file has TWO copies of the Dolt section (the real-world failure
// mode in gu-orhn — duplicates created by an earlier merge), Fix collapses
// them into a single canonical copy.
func TestTownCLAUDEmdCheck_Fix_CollapsesDuplicateDoltSections(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	stale := `# Gas Town

Run ` + "`gt prime`" + ` for full context.

## Dolt Server — Operational Awareness (All Agents)

` + "```bash\nkill -QUIT $(cat ~/gt/.dolt-data/dolt.pid)\n```" + `

### Communication hygiene

Use nudges (first copy).

## Dolt Server — Operational Awareness (All Agents)

Duplicate section.

` + "```bash\nkill -QUIT $(cat ~/gt/.dolt-data/dolt.pid)\n```" + `

### Communication hygiene

Use nudges (second copy).
`
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	if result := check.Run(ctx); result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v: %s", result.Status, result.Message)
	}
	if err := check.Fix(ctx); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if strings.Contains(content, staleKillQuitCmd) {
		t.Errorf("Fix should have removed all stale %q commands", staleKillQuitCmd)
	}
	// Exactly one Dolt H2 section should remain.
	doltCount := strings.Count(content, "## Dolt Server")
	if doltCount != 1 {
		t.Errorf("expected exactly 1 '## Dolt Server' heading after Fix, got %d", doltCount)
	}
}

func TestParseH2Sections(t *testing.T) {
	content := `# Header

Preamble text.

## Section One

Content one.

## Section Two

Content two.
### Subsection

Sub content.

## Section Three

Content three.
`

	sections := parseH2Sections(content)

	if len(sections) != 4 { // preamble + 3 H2 sections
		t.Fatalf("expected 4 sections, got %d", len(sections))
	}

	// Preamble
	if sections[0].heading != "" {
		t.Errorf("preamble should have empty heading, got %q", sections[0].heading)
	}
	if !strings.Contains(sections[0].content, "Preamble text") {
		t.Error("preamble missing expected content")
	}

	// Section One
	if sections[1].heading != "## Section One" {
		t.Errorf("expected '## Section One', got %q", sections[1].heading)
	}

	// Section Two (should include H3 subsection)
	if sections[2].heading != "## Section Two" {
		t.Errorf("expected '## Section Two', got %q", sections[2].heading)
	}
	if !strings.Contains(sections[2].content, "### Subsection") {
		t.Error("Section Two should include H3 subsection")
	}

	// Section Three
	if sections[3].heading != "## Section Three" {
		t.Errorf("expected '## Section Three', got %q", sections[3].heading)
	}
}

func TestIsIdentityAnchor_MinimalAnchor(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "CLAUDE.md")

	content := `# Gas Town

Run ` + "`gt prime`" + ` for full context.
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if !isIdentityAnchor(path) {
		t.Error("minimal anchor should be recognized as identity anchor")
	}
}

func TestIsIdentityAnchor_ExpandedCLAUDEmd(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "CLAUDE.md")

	// Write canonical content (many lines)
	if err := os.WriteFile(path, []byte(templates.TownRootCLAUDEmd()), 0644); err != nil {
		t.Fatal(err)
	}

	if !isIdentityAnchor(path) {
		t.Error("expanded CLAUDE.md should be recognized as identity anchor")
	}
}

func TestIsIdentityAnchor_NonGasTownFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "CLAUDE.md")

	content := `# My Project

This is a regular project CLAUDE.md, not Gas Town.
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if isIdentityAnchor(path) {
		t.Error("non-Gas Town file should not be recognized as identity anchor")
	}
}

// TestTownRootCanonicalHasNoStalePatterns is a guard rail: the canonical
// embedded template must NEVER contain any pattern listed in
// templates.TownRootStalePatterns. Otherwise Fix would loop replacing the
// section with a copy that immediately re-trips the warning.
func TestTownRootCanonicalHasNoStalePatterns(t *testing.T) {
	canonical := templates.TownRootCLAUDEmd()
	for _, pattern := range templates.TownRootStalePatterns() {
		if strings.Contains(canonical, pattern.Substring) {
			t.Errorf("canonical template contains stale pattern %q (%s) — Fix would loop",
				pattern.Substring, pattern.Name)
		}
	}
}

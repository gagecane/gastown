package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewHookAttachmentValidCheck(t *testing.T) {
	check := NewHookAttachmentValidCheck()

	if check.Name() != "hook-attachment-valid" {
		t.Errorf("expected name 'hook-attachment-valid', got %q", check.Name())
	}

	if check.Description() != "Verify attached molecules exist and are not closed" {
		t.Errorf("unexpected description: %q", check.Description())
	}

	if !check.CanFix() {
		t.Error("expected CanFix to return true")
	}
}

func TestHookAttachmentValidCheck_NoBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()

	check := NewHookAttachmentValidCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	// No beads dir means nothing to check, should be OK
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when no beads dir, got %v", result.Status)
	}
}

func TestHookAttachmentValidCheck_EmptyBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	check := NewHookAttachmentValidCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	// Empty beads dir means no pinned beads, should be OK
	// Note: This may error if bd CLI is not available, but should still handle gracefully
	if result.Status != StatusOK && result.Status != StatusError {
		t.Errorf("expected StatusOK or graceful error, got %v", result.Status)
	}
}

func TestHookAttachmentValidCheck_FormatInvalid(t *testing.T) {
	check := NewHookAttachmentValidCheck()

	tests := []struct {
		inv      invalidAttachment
		expected string
	}{
		{
			inv: invalidAttachment{
				pinnedBeadID: "hq-123",
				moleculeID:   "gt-456",
				reason:       "not_found",
			},
			expected: "hq-123: attached molecule gt-456 not found",
		},
		{
			inv: invalidAttachment{
				pinnedBeadID: "hq-123",
				moleculeID:   "gt-789",
				reason:       "closed",
			},
			expected: "hq-123: attached molecule gt-789 is closed",
		},
	}

	for _, tt := range tests {
		result := check.formatInvalid(tt.inv)
		if result != tt.expected {
			t.Errorf("formatInvalid() = %q, want %q", result, tt.expected)
		}
	}
}

func TestHookAttachmentValidCheck_FindRigBeadsDirs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create town-level .beads (should be excluded)
	townBeads := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatal(err)
	}

	// Create rig-level .beads
	rigBeads := filepath.Join(tmpDir, "myrig", ".beads")
	if err := os.MkdirAll(rigBeads, 0755); err != nil {
		t.Fatal(err)
	}

	check := NewHookAttachmentValidCheck()
	dirs := check.findRigBeadsDirs(tmpDir)

	// Should find the rig-level beads but not town-level
	found := false
	for _, dir := range dirs {
		if dir == townBeads {
			t.Error("findRigBeadsDirs should not include town-level .beads")
		}
		if dir == rigBeads {
			found = true
		}
	}

	if !found && len(dirs) > 0 {
		t.Logf("Found dirs: %v", dirs)
	}
}

// Tests for HookSingletonCheck

func TestNewHookSingletonCheck(t *testing.T) {
	check := NewHookSingletonCheck()

	if check.Name() != "hook-singleton" {
		t.Errorf("expected name 'hook-singleton', got %q", check.Name())
	}

	if check.Description() != "Ensure each agent has at most one handoff bead" {
		t.Errorf("unexpected description: %q", check.Description())
	}

	if !check.CanFix() {
		t.Error("expected CanFix to return true")
	}
}

func TestHookSingletonCheck_NoBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()

	check := NewHookSingletonCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	// No beads dir means nothing to check, should be OK
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when no beads dir, got %v", result.Status)
	}
}

func TestHookSingletonCheck_EmptyBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	check := NewHookSingletonCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	// Empty beads dir means no pinned beads, should be OK
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when empty beads dir, got %v", result.Status)
	}
}

func TestHookSingletonCheck_FormatDuplicate(t *testing.T) {
	check := NewHookSingletonCheck()

	tests := []struct {
		dup      duplicateHandoff
		expected string
	}{
		{
			dup: duplicateHandoff{
				title:   "Mayor Handoff",
				beadIDs: []string{"hq-123", "hq-456"},
			},
			expected: `"Mayor Handoff" has 2 beads: hq-123, hq-456`,
		},
		{
			dup: duplicateHandoff{
				title:   "Witness Handoff",
				beadIDs: []string{"gt-1", "gt-2", "gt-3"},
			},
			expected: `"Witness Handoff" has 3 beads: gt-1, gt-2, gt-3`,
		},
	}

	for _, tt := range tests {
		result := check.formatDuplicate(tt.dup)
		if result != tt.expected {
			t.Errorf("formatDuplicate() = %q, want %q", result, tt.expected)
		}
	}
}

// Tests for OrphanedAttachmentsCheck

func TestNewOrphanedAttachmentsCheck(t *testing.T) {
	check := NewOrphanedAttachmentsCheck()

	if check.Name() != "orphaned-attachments" {
		t.Errorf("expected name 'orphaned-attachments', got %q", check.Name())
	}

	if check.Description() != "Detect handoff beads for non-existent agents" {
		t.Errorf("unexpected description: %q", check.Description())
	}

	// This check is not auto-fixable (uses BaseCheck, not FixableCheck)
	if check.CanFix() {
		t.Error("expected CanFix to return false")
	}
}

func TestOrphanedAttachmentsCheck_NoBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()

	check := NewOrphanedAttachmentsCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	// No beads dir means nothing to check, should be OK
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when no beads dir, got %v", result.Status)
	}
}

func TestOrphanedAttachmentsCheck_FormatOrphan(t *testing.T) {
	check := NewOrphanedAttachmentsCheck()

	tests := []struct {
		orph     orphanedHandoff
		expected string
	}{
		{
			orph: orphanedHandoff{
				beadID: "hq-123",
				agent:  "gastown/nux",
			},
			expected: `hq-123: agent "gastown/nux" no longer exists`,
		},
		{
			orph: orphanedHandoff{
				beadID: "gt-456",
				agent:  "gastown/crew/joe",
			},
			expected: `gt-456: agent "gastown/crew/joe" no longer exists`,
		},
	}

	for _, tt := range tests {
		result := check.formatOrphan(tt.orph)
		if result != tt.expected {
			t.Errorf("formatOrphan() = %q, want %q", result, tt.expected)
		}
	}
}

func TestOrphanedAttachmentsCheck_AgentExists(t *testing.T) {
	tmpDir := t.TempDir()

	// Create some agent directories
	polecatDir := filepath.Join(tmpDir, "gastown", "polecats", "nux")
	if err := os.MkdirAll(polecatDir, 0755); err != nil {
		t.Fatal(err)
	}

	crewDir := filepath.Join(tmpDir, "gastown", "crew", "joe")
	if err := os.MkdirAll(crewDir, 0755); err != nil {
		t.Fatal(err)
	}

	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	witnessDir := filepath.Join(tmpDir, "gastown", "witness")
	if err := os.MkdirAll(witnessDir, 0755); err != nil {
		t.Fatal(err)
	}

	check := NewOrphanedAttachmentsCheck()

	tests := []struct {
		agent    string
		expected bool
	}{
		// Existing agents
		{"gastown/nux", true},
		{"gastown/crew/joe", true},
		{"mayor", true},
		{"gastown-witness", true},

		// Non-existent agents
		{"gastown/deleted", false},
		{"gastown/crew/gone", false},
		{"otherrig-witness", false},
	}

	for _, tt := range tests {
		result := check.agentExists(tt.agent, tmpDir)
		if result != tt.expected {
			t.Errorf("agentExists(%q) = %v, want %v", tt.agent, result, tt.expected)
		}
	}
}

// TestHookAttachmentValidCheck_FixUsesLocalLookup is a regression test for
// gu-vkg3: HookAttachmentValidCheck.Fix MUST look up the pinned bead in the
// rig's own .beads directory (NOT via prefix routing).
//
// Before the fix, Fix called DetachMolecule → Show → prefix routing. For a
// legacy pinned bead whose prefix routes to a different rig than where it
// actually lives, Show routed lookups to the wrong DB and returned
// ErrNotFound, causing Fix to emit "failed to detach ... issue not found"
// and leaving the stale attached_molecule in place forever.
//
// After the fix, Fix uses DetachMoleculeLocal, which operates on the
// directory we already located the pinned bead in and bypasses routing.
//
// This test uses a bd-stub that returns "not found" in the town DB but
// returns the pinned bead in the legacy rig DB; with routing, Fix would
// fail, and without routing it succeeds.
func TestHookAttachmentValidCheck_FixUsesLocalLookup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	tmpDir := t.TempDir()

	// Town-level .beads + routes that map "gt-" to the TOWN (not the rig).
	// This is the misconfiguration that produces the gu-vkg3 failure.
	townBeads := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "mayor", "town.json"), []byte(`{}`), 0o644); err != nil {
		// mayor/town.json is needed by getTownRoot; create parent first.
		if err := os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "mayor", "town.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	routes := `{"prefix":"gt-","path":"."}` + "\n"
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routes), 0o644); err != nil {
		t.Fatal(err)
	}

	// Rig-level .beads that ACTUALLY holds the stale "gt-*" pinned bead.
	rigBeads := filepath.Join(tmpDir, "legacy", ".beads")
	if err := os.MkdirAll(filepath.Join(rigBeads, "locks"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Stub bd: returns the pinned bead only when running in the legacy rig DB.
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	updateLog := filepath.Join(tmpDir, "updates.log")
	bdScript := `#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"; shift
location=""
if [ -n "$BEADS_DIR" ]; then
    location="$BEADS_DIR"
else
    location="$PWD/.beads"
fi
case "$cmd" in
  show)
    case "$location" in
      *legacy*)
        cat <<'JSON'
[{"id":"gt-stale-legacy","title":"witness Handoff","status":"pinned","description":"attached_molecule: mol-dead"}]
JSON
        ;;
      *)
        echo "Issue not found: $1" >&2
        exit 1
        ;;
    esac
    ;;
  update)
    echo "location=$location args=$*" >> "` + updateLog + `"
    ;;
esac
exit 0
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BEADS_DIR", "")

	check := NewHookAttachmentValidCheck()
	check.invalidAttachments = []invalidAttachment{
		{
			pinnedBeadID:  "gt-stale-legacy",
			pinnedBeadDir: rigBeads, // where the bead was actually FOUND
			moleculeID:    "mol-dead",
			reason:        "not_found",
		},
	}

	ctx := &CheckContext{TownRoot: tmpDir}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed (regression: Fix must use DetachMoleculeLocal, not DetachMolecule): %v", err)
	}

	// The update must have landed in the legacy rig DB, not the town DB.
	data, err := os.ReadFile(updateLog)
	if err != nil {
		t.Fatalf("no bd update was recorded — Fix did not clear attached_molecule: %v", err)
	}
	if !strings.Contains(string(data), "legacy") {
		t.Errorf("Fix updated the wrong DB (expected legacy rig); updates.log:\n%s", data)
	}
}


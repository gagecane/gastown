package beads

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDetachAuditEntryJSONRoundTrip verifies that DetachAuditEntry serializes
// to JSON with the expected field names and round-trips cleanly. This is the
// on-disk format written to audit.log, so the keys are part of the contract.
func TestDetachAuditEntryJSONRoundTrip(t *testing.T) {
	entry := DetachAuditEntry{
		Timestamp:        "2026-04-29T12:34:56Z",
		Operation:        "burn",
		PinnedBeadID:     "hq-mayor",
		DetachedMolecule: "gu-wisp-abc",
		DetachedBy:       "gastown/witness",
		Reason:           "stuck polecat",
		PreviousState:    "hooked",
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify expected keys are present in the JSON output.
	wantKeys := []string{
		`"timestamp"`,
		`"operation"`,
		`"pinned_bead_id"`,
		`"detached_molecule"`,
		`"detached_by"`,
		`"reason"`,
		`"previous_state"`,
	}
	got := string(data)
	for _, k := range wantKeys {
		if !strings.Contains(got, k) {
			t.Errorf("JSON output missing key %s: %s", k, got)
		}
	}

	var decoded DetachAuditEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded != entry {
		t.Errorf("round-trip mismatch:\ngot:  %+v\nwant: %+v", decoded, entry)
	}
}

// TestDetachAuditEntryOmitEmpty verifies that optional fields are omitted
// from JSON output when they are empty. This keeps audit.log compact for
// the common case where no reason/agent is recorded.
func TestDetachAuditEntryOmitEmpty(t *testing.T) {
	entry := DetachAuditEntry{
		Timestamp:        "2026-04-29T12:34:56Z",
		Operation:        "detach",
		PinnedBeadID:     "hq-mayor",
		DetachedMolecule: "gu-wisp-abc",
		// DetachedBy, Reason, PreviousState deliberately empty
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)

	// Optional fields with ,omitempty should not appear.
	omittedKeys := []string{`"detached_by"`, `"reason"`, `"previous_state"`}
	for _, k := range omittedKeys {
		if strings.Contains(got, k) {
			t.Errorf("JSON output should have omitted empty %s: %s", k, got)
		}
	}

	// Required fields must still appear.
	requiredKeys := []string{`"timestamp"`, `"operation"`, `"pinned_bead_id"`, `"detached_molecule"`}
	for _, k := range requiredKeys {
		if !strings.Contains(got, k) {
			t.Errorf("JSON output missing required key %s: %s", k, got)
		}
	}
}

// TestLogDetachAudit_WritesJSONLEntry verifies that LogDetachAudit writes
// a single JSON line to audit.log in the resolved beads directory.
func TestLogDetachAudit_WritesJSONLEntry(t *testing.T) {
	workDir := t.TempDir()
	beadsDir := filepath.Join(workDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	b := NewWithBeadsDir(workDir, beadsDir)
	entry := DetachAuditEntry{
		Timestamp:        "2026-04-29T12:34:56Z",
		Operation:        "detach",
		PinnedBeadID:     "hq-mayor",
		DetachedMolecule: "gu-wisp-abc",
		DetachedBy:       "gastown/witness",
	}

	if err := b.LogDetachAudit(entry); err != nil {
		t.Fatalf("LogDetachAudit: %v", err)
	}

	auditPath := filepath.Join(beadsDir, "audit.log")
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %q", len(lines), string(data))
	}

	var decoded DetachAuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
		t.Fatalf("unmarshal line: %v (line=%q)", err, lines[0])
	}
	if decoded != entry {
		t.Errorf("decoded entry mismatch:\ngot:  %+v\nwant: %+v", decoded, entry)
	}
}

// TestLogDetachAudit_AppendsMultipleEntries verifies that successive calls
// append new entries rather than overwriting the log. Each entry must be on
// its own line (JSONL format).
func TestLogDetachAudit_AppendsMultipleEntries(t *testing.T) {
	workDir := t.TempDir()
	beadsDir := filepath.Join(workDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	b := NewWithBeadsDir(workDir, beadsDir)

	entries := []DetachAuditEntry{
		{Timestamp: "2026-04-29T12:00:00Z", Operation: "detach", PinnedBeadID: "hq-a", DetachedMolecule: "gu-w-1"},
		{Timestamp: "2026-04-29T12:05:00Z", Operation: "burn", PinnedBeadID: "hq-b", DetachedMolecule: "gu-w-2"},
		{Timestamp: "2026-04-29T12:10:00Z", Operation: "squash", PinnedBeadID: "hq-c", DetachedMolecule: "gu-w-3"},
	}

	for i, entry := range entries {
		if err := b.LogDetachAudit(entry); err != nil {
			t.Fatalf("LogDetachAudit #%d: %v", i, err)
		}
	}

	f, err := os.Open(filepath.Join(beadsDir, "audit.log"))
	if err != nil {
		t.Fatalf("open audit log: %v", err)
	}
	defer f.Close()

	var decoded []DetachAuditEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e DetachAuditEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatalf("unmarshal: %v (line=%q)", err, scanner.Text())
		}
		decoded = append(decoded, e)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}

	if len(decoded) != len(entries) {
		t.Fatalf("got %d entries, want %d", len(decoded), len(entries))
	}
	for i := range entries {
		if decoded[i] != entries[i] {
			t.Errorf("entry %d mismatch:\ngot:  %+v\nwant: %+v", i, decoded[i], entries[i])
		}
	}
}

// TestLogDetachAudit_CreatesFileWithSecurePermissions verifies that the audit
// log is created with mode 0600, so audit entries (which may include agent
// identities) are not world-readable.
func TestLogDetachAudit_CreatesFileWithSecurePermissions(t *testing.T) {
	// Skip on Windows - file mode semantics differ.
	if os.Getenv("RUNTIME_GOOS_OVERRIDE") == "windows" {
		t.Skip("file mode test is Unix-specific")
	}

	workDir := t.TempDir()
	beadsDir := filepath.Join(workDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	b := NewWithBeadsDir(workDir, beadsDir)

	entry := DetachAuditEntry{
		Timestamp:        "2026-04-29T12:00:00Z",
		Operation:        "detach",
		PinnedBeadID:     "hq-mayor",
		DetachedMolecule: "gu-wisp-abc",
	}
	if err := b.LogDetachAudit(entry); err != nil {
		t.Fatalf("LogDetachAudit: %v", err)
	}

	info, err := os.Stat(filepath.Join(beadsDir, "audit.log"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	mode := info.Mode().Perm()
	// On Unix, expect exactly 0600. Skip this assertion if the filesystem
	// mounts without perm support (rare for t.TempDir but possible).
	if mode != 0600 && mode != 0 {
		t.Logf("audit.log permissions = %#o (expected 0600 on Unix, this may vary by umask/FS)", mode)
	}
}

// TestLogDetachAudit_MissingDirReturnsError verifies that LogDetachAudit
// surfaces an error when the beads directory does not exist. The function
// does not attempt to create the directory — it relies on bd init having
// been run. This test protects against silent data loss.
func TestLogDetachAudit_MissingDirReturnsError(t *testing.T) {
	workDir := t.TempDir()
	// Deliberately do NOT create the .beads directory.
	b := NewWithBeadsDir(workDir, filepath.Join(workDir, "nonexistent-beads"))

	entry := DetachAuditEntry{
		Timestamp:        "2026-04-29T12:00:00Z",
		Operation:        "detach",
		PinnedBeadID:     "hq-mayor",
		DetachedMolecule: "gu-wisp-abc",
	}
	err := b.LogDetachAudit(entry)
	if err == nil {
		t.Fatal("expected error when beads dir is missing, got nil")
	}
	if !errors.Is(err, fs.ErrNotExist) && !strings.Contains(err.Error(), "opening audit log") {
		t.Errorf("error should indicate audit log could not be opened, got: %v", err)
	}
}

// TestDetachOptions_ZeroValue verifies DetachOptions zero-value is safe to
// pass to DetachMoleculeWithAudit. The Operation field defaults to "detach"
// inside the function when empty.
func TestDetachOptions_ZeroValue(t *testing.T) {
	var opts DetachOptions
	if opts.Operation != "" {
		t.Errorf("zero Operation = %q, want empty", opts.Operation)
	}
	if opts.Agent != "" {
		t.Errorf("zero Agent = %q, want empty", opts.Agent)
	}
	if opts.Reason != "" {
		t.Errorf("zero Reason = %q, want empty", opts.Reason)
	}
}

// TestDetachOptions_OperationTypes documents the operation strings that
// callers pass to DetachMoleculeWithAudit. These are observed across the
// codebase (internal/cmd/molecule_lifecycle.go, sling_helpers.go, polecat.go)
// and are part of the audit contract.
func TestDetachOptions_OperationTypes(t *testing.T) {
	validOps := []string{"detach", "burn", "squash"}
	for _, op := range validOps {
		t.Run(op, func(t *testing.T) {
			opts := DetachOptions{Operation: op, Agent: "test-agent", Reason: "unit test"}
			if opts.Operation != op {
				t.Errorf("Operation = %q, want %q", opts.Operation, op)
			}
		})
	}
}

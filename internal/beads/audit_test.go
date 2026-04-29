package beads

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	beadsdk "github.com/steveyegge/beads"
)

// newTestBeadsWithDir creates a Beads wrapper rooted at dir with the given
// mock store. Use this when tests need a real filesystem path (e.g., audit
// logging, lockBead).
func newTestBeadsWithDir(dir string, store *mockStorage) *Beads {
	return &Beads{
		workDir:  dir,
		beadsDir: dir,
		store:    store,
		isolated: true,
	}
}

// --- LogDetachAudit ---

// TestLogDetachAudit_WritesJSONLEntry writes a single JSONL line per call.
func TestLogDetachAudit_WritesJSONLEntry(t *testing.T) {
	dir := t.TempDir()
	b := newTestBeadsWithDir(dir, newMockStorage())

	entry := DetachAuditEntry{
		Timestamp:        "2026-04-29T00:00:00Z",
		Operation:        "detach",
		PinnedBeadID:     "pin-1",
		DetachedMolecule: "mol-1",
		DetachedBy:       "alice",
		Reason:           "rollback",
		PreviousState:    "pinned",
	}
	if err := b.LogDetachAudit(entry); err != nil {
		t.Fatalf("LogDetachAudit: %v", err)
	}

	auditPath := filepath.Join(dir, "audit.log")
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("audit file is empty")
	}
	if data[len(data)-1] != '\n' {
		t.Error("audit entry is not newline-terminated")
	}

	var got DetachAuditEntry
	if err := json.Unmarshal(data[:len(data)-1], &got); err != nil {
		t.Fatalf("unmarshal entry: %v", err)
	}
	if got != entry {
		t.Errorf("entry mismatch\ngot: %+v\nwant: %+v", got, entry)
	}
}

// TestLogDetachAudit_Appends appends multiple entries without overwriting.
func TestLogDetachAudit_Appends(t *testing.T) {
	dir := t.TempDir()
	b := newTestBeadsWithDir(dir, newMockStorage())

	entries := []DetachAuditEntry{
		{Timestamp: "t1", Operation: "detach", PinnedBeadID: "p1", DetachedMolecule: "m1"},
		{Timestamp: "t2", Operation: "burn", PinnedBeadID: "p2", DetachedMolecule: "m2"},
		{Timestamp: "t3", Operation: "squash", PinnedBeadID: "p3", DetachedMolecule: "m3"},
	}
	for _, e := range entries {
		if err := b.LogDetachAudit(e); err != nil {
			t.Fatalf("LogDetachAudit: %v", err)
		}
	}

	f, err := os.Open(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}

	for i, line := range lines {
		var got DetachAuditEntry
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d: unmarshal: %v", i, err)
		}
		if got != entries[i] {
			t.Errorf("line %d: got %+v, want %+v", i, got, entries[i])
		}
	}
}

// TestLogDetachAudit_OmitsEmptyOptional fields omits empty optional fields
// in the JSON output thanks to `omitempty` tags.
func TestLogDetachAudit_OmitsEmptyOptional(t *testing.T) {
	dir := t.TempDir()
	b := newTestBeadsWithDir(dir, newMockStorage())

	entry := DetachAuditEntry{
		Timestamp:        "t",
		Operation:        "detach",
		PinnedBeadID:     "p",
		DetachedMolecule: "m",
	}
	if err := b.LogDetachAudit(entry); err != nil {
		t.Fatalf("LogDetachAudit: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	line := strings.TrimSpace(string(data))
	// omitempty fields should not appear.
	if strings.Contains(line, "detached_by") {
		t.Errorf("detached_by should be omitted: %s", line)
	}
	if strings.Contains(line, "reason") {
		t.Errorf("reason should be omitted: %s", line)
	}
	if strings.Contains(line, "previous_state") {
		t.Errorf("previous_state should be omitted: %s", line)
	}
}

// --- DetachOptions ---

// TestDetachOptions_DefaultOperation defaults Operation to "detach" when
// unset. We verify via the audit log output.
func TestDetachOptions_DefaultOperation(t *testing.T) {
	dir := t.TempDir()
	store := newMockStorage()
	b := newTestBeadsWithDir(dir, store)
	ctx := context.Background()

	// Seed a pinned bead with an attached molecule.
	issue := &beadsdk.Issue{
		Title: "pinned",
		Description: strings.Join([]string{
			"attached_molecule: mol-1",
			"attached_at: 2026-01-01T00:00:00Z",
		}, "\n"),
	}
	if err := store.CreateIssue(ctx, issue, ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := b.DetachMoleculeWithAudit(issue.ID, DetachOptions{}); err != nil {
		t.Fatalf("DetachMoleculeWithAudit: %v", err)
	}

	// Read audit log and verify Operation defaulted.
	data, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	var entry DetachAuditEntry
	line := strings.TrimSpace(string(data))
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.Operation != "detach" {
		t.Errorf("Operation = %q, want 'detach'", entry.Operation)
	}
	if entry.PinnedBeadID != issue.ID {
		t.Errorf("PinnedBeadID = %q, want %q", entry.PinnedBeadID, issue.ID)
	}
	if entry.DetachedMolecule != "mol-1" {
		t.Errorf("DetachedMolecule = %q, want 'mol-1'", entry.DetachedMolecule)
	}
}

// TestDetachMoleculeWithAudit_ClearsAttachment removes attachment fields from
// the pinned bead's description.
func TestDetachMoleculeWithAudit_ClearsAttachment(t *testing.T) {
	dir := t.TempDir()
	store := newMockStorage()
	b := newTestBeadsWithDir(dir, store)
	ctx := context.Background()

	issue := &beadsdk.Issue{
		Title: "pinned",
		Description: strings.Join([]string{
			"Some header",
			"attached_molecule: mol-1",
			"attached_at: 2026-01-01T00:00:00Z",
			"Some footer",
		}, "\n"),
	}
	if err := store.CreateIssue(ctx, issue, ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	updated, err := b.DetachMoleculeWithAudit(issue.ID, DetachOptions{
		Operation: "burn",
		Agent:     "witness",
		Reason:    "stale",
	})
	if err != nil {
		t.Fatalf("DetachMoleculeWithAudit: %v", err)
	}
	if updated == nil {
		t.Fatal("updated issue is nil")
	}

	// Attachment fields should be cleared from description.
	parsed := ParseAttachmentFields(updated)
	if parsed != nil {
		t.Errorf("expected nil attachment fields after detach, got %+v", parsed)
	}

	// Non-attachment content must be preserved.
	if !strings.Contains(updated.Description, "Some header") {
		t.Errorf("lost non-attachment content in description:\n%s", updated.Description)
	}
	if !strings.Contains(updated.Description, "Some footer") {
		t.Errorf("lost non-attachment content in description:\n%s", updated.Description)
	}
}

// TestDetachMoleculeWithAudit_NoAttachmentNoOp does not error and returns the
// issue when there is no attachment.
func TestDetachMoleculeWithAudit_NoAttachmentNoOp(t *testing.T) {
	dir := t.TempDir()
	store := newMockStorage()
	b := newTestBeadsWithDir(dir, store)
	ctx := context.Background()

	issue := &beadsdk.Issue{
		Title:       "pinned",
		Description: "just plain text, no attachment",
	}
	if err := store.CreateIssue(ctx, issue, ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := b.DetachMoleculeWithAudit(issue.ID, DetachOptions{})
	if err != nil {
		t.Fatalf("DetachMoleculeWithAudit: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil issue")
	}
	if got.ID != issue.ID {
		t.Errorf("ID = %q, want %q", got.ID, issue.ID)
	}
	// No audit entry should have been written since nothing to detach.
	if _, err := os.Stat(filepath.Join(dir, "audit.log")); err == nil {
		t.Error("audit.log should not exist when there was nothing to detach")
	}
}

// TestDetachMoleculeWithAudit_UnknownBead errors when the bead doesn't exist.
func TestDetachMoleculeWithAudit_UnknownBead(t *testing.T) {
	dir := t.TempDir()
	store := newMockStorage()
	b := newTestBeadsWithDir(dir, store)

	if _, err := b.DetachMoleculeWithAudit("bogus-id", DetachOptions{}); err == nil {
		t.Error("expected error for unknown bead, got nil")
	}
}

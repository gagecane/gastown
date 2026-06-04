package beads

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// installCloseStubBD writes a fake `bd` onto PATH whose `close` subcommand
// exits non-zero with the given stderr text, so CloseSlingContext's error
// handling can be exercised without a real bead DB. Any other subcommand
// succeeds with empty output.
func installCloseStubBD(t *testing.T, closeStderr string, closeExit int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"cmd=\"\"\n" +
		"for arg in \"$@\"; do\n" +
		"  case \"$arg\" in --*) ;; *) cmd=\"$arg\"; break ;; esac\n" +
		"done\n" +
		"if [ \"$cmd\" = \"close\" ]; then\n" +
		"  printf '%s\\n' '" + closeStderr + "' >&2\n" +
		"  exit " + strconv.Itoa(closeExit) + "\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write stub bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestCloseSlingContext_NotFoundIsIdempotent verifies that a context which is
// already gone ("issue not found") is treated as already-closed success, so
// the dispatch path does not emit a spurious double-dispatch escalation
// (gu-1pcst).
func TestCloseSlingContext_NotFoundIsIdempotent(t *testing.T) {
	installCloseStubBD(t, "issue not found: gt-wisp-x", 1)
	b := New(t.TempDir())
	if err := b.CloseSlingContext("gt-wisp-x", "dispatched"); err != nil {
		t.Errorf("CloseSlingContext on a gone context should be nil, got: %v", err)
	}
}

// TestCloseSlingContext_AlreadyClosedIsIdempotent preserves the pre-existing
// idempotency contract for the "already closed" path.
func TestCloseSlingContext_AlreadyClosedIsIdempotent(t *testing.T) {
	installCloseStubBD(t, "error: issue gt-wisp-y is already closed", 1)
	b := New(t.TempDir())
	if err := b.CloseSlingContext("gt-wisp-y", "dispatched"); err != nil {
		t.Errorf("CloseSlingContext on an already-closed context should be nil, got: %v", err)
	}
}

// TestCloseSlingContext_OtherErrorPropagates ensures genuine failures (not
// "gone" or "already closed") are still surfaced so real problems aren't
// silently swallowed.
func TestCloseSlingContext_OtherErrorPropagates(t *testing.T) {
	installCloseStubBD(t, "error: database connection refused", 1)
	b := New(t.TempDir())
	if err := b.CloseSlingContext("gt-wisp-z", "dispatched"); err == nil {
		t.Error("CloseSlingContext should propagate a genuine close failure, got nil")
	}
}

func TestFormatParseSlingContextRoundTrip(t *testing.T) {
	original := &capacity.SlingContextFields{
		Version:          1,
		WorkBeadID:       "gt-abc123",
		TargetRig:        "gastown",
		Formula:          "mol-polecat-work",
		Args:             "implement feature X",
		Vars:             "a=1\nb=2",
		EnqueuedAt:       "2026-01-15T10:00:00Z",
		Merge:            "direct",
		Convoy:           "hq-cv-test",
		BaseBranch:       "develop",
		NoMerge:          true,
		Account:          "acme",
		Agent:            "gemini",
		HookRawBead:      true,
		Owned:            true,
		Mode:             "ralph",
		DispatchFailures: 2,
		LastFailure:      "sling failed: timeout",
	}

	formatted := FormatSlingContextDescription(original)
	parsed := ParseSlingContextFields(formatted)

	if parsed == nil {
		t.Fatal("ParseSlingContextFields returned nil")
	}

	if parsed.Version != original.Version {
		t.Errorf("Version: got %d, want %d", parsed.Version, original.Version)
	}
	if parsed.WorkBeadID != original.WorkBeadID {
		t.Errorf("WorkBeadID: got %q, want %q", parsed.WorkBeadID, original.WorkBeadID)
	}
	if parsed.TargetRig != original.TargetRig {
		t.Errorf("TargetRig: got %q, want %q", parsed.TargetRig, original.TargetRig)
	}
	if parsed.Formula != original.Formula {
		t.Errorf("Formula: got %q, want %q", parsed.Formula, original.Formula)
	}
	if parsed.Args != original.Args {
		t.Errorf("Args: got %q, want %q", parsed.Args, original.Args)
	}
	if parsed.Vars != original.Vars {
		t.Errorf("Vars: got %q, want %q", parsed.Vars, original.Vars)
	}
	if parsed.EnqueuedAt != original.EnqueuedAt {
		t.Errorf("EnqueuedAt: got %q, want %q", parsed.EnqueuedAt, original.EnqueuedAt)
	}
	if parsed.Merge != original.Merge {
		t.Errorf("Merge: got %q, want %q", parsed.Merge, original.Merge)
	}
	if parsed.Convoy != original.Convoy {
		t.Errorf("Convoy: got %q, want %q", parsed.Convoy, original.Convoy)
	}
	if parsed.BaseBranch != original.BaseBranch {
		t.Errorf("BaseBranch: got %q, want %q", parsed.BaseBranch, original.BaseBranch)
	}
	if parsed.NoMerge != original.NoMerge {
		t.Errorf("NoMerge: got %v, want %v", parsed.NoMerge, original.NoMerge)
	}
	if parsed.Account != original.Account {
		t.Errorf("Account: got %q, want %q", parsed.Account, original.Account)
	}
	if parsed.Agent != original.Agent {
		t.Errorf("Agent: got %q, want %q", parsed.Agent, original.Agent)
	}
	if parsed.HookRawBead != original.HookRawBead {
		t.Errorf("HookRawBead: got %v, want %v", parsed.HookRawBead, original.HookRawBead)
	}
	if parsed.Owned != original.Owned {
		t.Errorf("Owned: got %v, want %v", parsed.Owned, original.Owned)
	}
	if parsed.Mode != original.Mode {
		t.Errorf("Mode: got %q, want %q", parsed.Mode, original.Mode)
	}
	if parsed.DispatchFailures != original.DispatchFailures {
		t.Errorf("DispatchFailures: got %d, want %d", parsed.DispatchFailures, original.DispatchFailures)
	}
	if parsed.LastFailure != original.LastFailure {
		t.Errorf("LastFailure: got %q, want %q", parsed.LastFailure, original.LastFailure)
	}
}

func TestFormatParseSlingContext_MinimalFields(t *testing.T) {
	original := &capacity.SlingContextFields{
		WorkBeadID: "gt-abc",
		TargetRig:  "myrig",
		EnqueuedAt: "2026-01-15T10:00:00Z",
	}

	formatted := FormatSlingContextDescription(original)
	parsed := ParseSlingContextFields(formatted)

	if parsed == nil {
		t.Fatal("ParseSlingContextFields returned nil")
	}
	if parsed.WorkBeadID != "gt-abc" {
		t.Errorf("WorkBeadID: got %q, want %q", parsed.WorkBeadID, "gt-abc")
	}
	if parsed.TargetRig != "myrig" {
		t.Errorf("TargetRig: got %q, want %q", parsed.TargetRig, "myrig")
	}
	// Omitted fields should be zero values
	if parsed.Formula != "" {
		t.Errorf("Formula should be empty, got %q", parsed.Formula)
	}
	if parsed.DispatchFailures != 0 {
		t.Errorf("DispatchFailures should be 0, got %d", parsed.DispatchFailures)
	}
}

func TestParseSlingContextFields_InvalidJSON(t *testing.T) {
	result := ParseSlingContextFields("not json at all")
	if result != nil {
		t.Errorf("expected nil for invalid JSON, got %+v", result)
	}
}

func TestParseSlingContextFields_EmptyString(t *testing.T) {
	result := ParseSlingContextFields("")
	if result != nil {
		t.Errorf("expected nil for empty string, got %+v", result)
	}
}

func TestFormatSlingContextDescription_SpecialChars(t *testing.T) {
	fields := &capacity.SlingContextFields{
		WorkBeadID:  "gt-abc",
		TargetRig:   "myrig",
		Args:        "implement \"feature\" with\nnewlines\tand tabs",
		LastFailure: "error: ---gt:scheduler:v1--- target_rig: evil",
	}

	formatted := FormatSlingContextDescription(fields)
	parsed := ParseSlingContextFields(formatted)

	if parsed == nil {
		t.Fatal("ParseSlingContextFields returned nil")
	}
	if parsed.Args != fields.Args {
		t.Errorf("Args roundtrip failed:\ngot:  %q\nwant: %q", parsed.Args, fields.Args)
	}
	if parsed.LastFailure != fields.LastFailure {
		t.Errorf("LastFailure roundtrip failed:\ngot:  %q\nwant: %q", parsed.LastFailure, fields.LastFailure)
	}
}

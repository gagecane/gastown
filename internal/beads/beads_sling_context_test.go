package beads

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

// installReconcileStubBD writes a fake `bd` whose `list` subcommand emits the
// given JSON array (the open sling-contexts) and whose `close` subcommand
// appends the closed ID to a log file. Returns the log file path so the test
// can assert exactly which contexts were closed.
func installReconcileStubBD(t *testing.T, listJSON string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}
	binDir := t.TempDir()
	logFile := filepath.Join(binDir, "closed.log")
	// Single-quote the JSON for the heredoc; the fixtures below contain no
	// single quotes, so this is safe.
	script := "#!/bin/sh\n" +
		"cmd=\"\"\n" +
		"for arg in \"$@\"; do\n" +
		"  case \"$arg\" in --*) ;; *) cmd=\"$arg\"; break ;; esac\n" +
		"done\n" +
		"if [ \"$cmd\" = \"list\" ]; then\n" +
		"  cat <<'JSONEOF'\n" + listJSON + "\nJSONEOF\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$cmd\" = \"close\" ]; then\n" +
		"  seen_close=\"\"\n" +
		"  for arg in \"$@\"; do\n" +
		"    if [ -n \"$seen_close\" ]; then\n" +
		"      case \"$arg\" in --*) ;; *) printf '%s\\n' \"$arg\" >> '" + logFile + "'; break ;; esac\n" +
		"    fi\n" +
		"    case \"$arg\" in close) seen_close=1 ;; esac\n" +
		"  done\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write stub bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logFile
}

func readClosedLog(t *testing.T, logFile string) []string {
	t.Helper()
	data, err := os.ReadFile(logFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read closed log: %v", err)
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			ids = append(ids, line)
		}
	}
	return ids
}

// TestReconcileOpenSlingContexts_ClosesStaleMatchingContexts is the core
// gu-afpjj regression: a stale OPEN context tracking the work bead must be
// closed so `bd close <workBead>` no longer needs --force, while contexts for
// OTHER work beads are left untouched.
func TestReconcileOpenSlingContexts_ClosesStaleMatchingContexts(t *testing.T) {
	listJSON := `[
	  {"id":"gu-wisp-stale","description":"{\"version\":1,\"work_bead_id\":\"gu-afpjj\",\"target_rig\":\"gastown_upstream\"}"},
	  {"id":"gu-wisp-other","description":"{\"version\":1,\"work_bead_id\":\"gu-other\",\"target_rig\":\"gastown_upstream\"}"}
	]`
	logFile := installReconcileStubBD(t, listJSON)
	b := New(t.TempDir())

	closed, err := b.ReconcileOpenSlingContexts("gu-afpjj", "", "superseded by re-sling")
	if err != nil {
		t.Fatalf("ReconcileOpenSlingContexts: %v", err)
	}
	if len(closed) != 1 || closed[0] != "gu-wisp-stale" {
		t.Errorf("returned closed IDs: got %v, want [gu-wisp-stale]", closed)
	}
	if got := readClosedLog(t, logFile); len(got) != 1 || got[0] != "gu-wisp-stale" {
		t.Errorf("bd close called for: got %v, want [gu-wisp-stale]", got)
	}
}

// TestReconcileOpenSlingContexts_ExcludesActiveContext verifies the optExcludeID
// guard: the just-created/active context for the same work bead is preserved so
// the scheduler's own bookkeeping is never double-closed.
func TestReconcileOpenSlingContexts_ExcludesActiveContext(t *testing.T) {
	listJSON := `[
	  {"id":"gu-wisp-stale","description":"{\"version\":1,\"work_bead_id\":\"gu-afpjj\",\"target_rig\":\"gastown_upstream\"}"},
	  {"id":"gu-wisp-active","description":"{\"version\":1,\"work_bead_id\":\"gu-afpjj\",\"target_rig\":\"gastown_upstream\"}"}
	]`
	logFile := installReconcileStubBD(t, listJSON)
	b := New(t.TempDir())

	closed, err := b.ReconcileOpenSlingContexts("gu-afpjj", "gu-wisp-active", "superseded by re-sling")
	if err != nil {
		t.Fatalf("ReconcileOpenSlingContexts: %v", err)
	}
	if len(closed) != 1 || closed[0] != "gu-wisp-stale" {
		t.Errorf("returned closed IDs: got %v, want [gu-wisp-stale]", closed)
	}
	if got := readClosedLog(t, logFile); len(got) != 1 || got[0] != "gu-wisp-stale" {
		t.Errorf("bd close called for: got %v, want [gu-wisp-stale] (active context must be excluded)", got)
	}
}

// TestReconcileOpenSlingContexts_NoMatchIsNoOp confirms an empty/no-match list
// closes nothing and returns no IDs.
func TestReconcileOpenSlingContexts_NoMatchIsNoOp(t *testing.T) {
	logFile := installReconcileStubBD(t, "No issues found.")
	b := New(t.TempDir())

	closed, err := b.ReconcileOpenSlingContexts("gu-afpjj", "", "superseded by re-sling")
	if err != nil {
		t.Fatalf("ReconcileOpenSlingContexts: %v", err)
	}
	if len(closed) != 0 {
		t.Errorf("expected no contexts closed, got %v", closed)
	}
	if got := readClosedLog(t, logFile); len(got) != 0 {
		t.Errorf("expected no bd close calls, got %v", got)
	}
}

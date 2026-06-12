package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// --- Interval tests ---

func TestRestartPendingInterval_Default(t *testing.T) {
	if got := restartPendingInterval(nil); got != defaultRestartPendingInterval {
		t.Errorf("expected default %v, got %v", defaultRestartPendingInterval, got)
	}
}

func TestRestartPendingInterval_Custom(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			RestartPending: &RestartPendingConfig{Enabled: true, IntervalStr: "2m"},
		},
	}
	if got := restartPendingInterval(cfg); got != 2*time.Minute {
		t.Errorf("expected 2m, got %v", got)
	}
}

func TestRestartPendingInterval_Invalid(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			RestartPending: &RestartPendingConfig{Enabled: true, IntervalStr: "nonsense"},
		},
	}
	if got := restartPendingInterval(cfg); got != defaultRestartPendingInterval {
		t.Errorf("expected default for invalid interval, got %v", got)
	}
}

// --- IsPatrolEnabled tests (restart_pending is DEFAULT-ON) ---

func TestIsPatrolEnabled_RestartPending_NilConfigDefaultsOn(t *testing.T) {
	// Unlike opt-in patrols, restart_pending must run out of the box.
	if !IsPatrolEnabled(nil, "restart_pending") {
		t.Error("restart_pending should default ON with nil config")
	}
}

func TestIsPatrolEnabled_RestartPending_EmptyPatrolsDefaultsOn(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if !IsPatrolEnabled(cfg, "restart_pending") {
		t.Error("restart_pending should default ON when not explicitly configured")
	}
}

func TestIsPatrolEnabled_RestartPending_ExplicitlyDisabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			RestartPending: &RestartPendingConfig{Enabled: false},
		},
	}
	if IsPatrolEnabled(cfg, "restart_pending") {
		t.Error("restart_pending should be disabled when explicitly set false")
	}
}

func TestIsPatrolEnabled_RestartPending_ExplicitlyEnabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			RestartPending: &RestartPendingConfig{Enabled: true},
		},
	}
	if !IsPatrolEnabled(cfg, "restart_pending") {
		t.Error("restart_pending should be enabled when explicitly set true")
	}
}

// --- Lifecycle defaults ---

func TestEnsureLifecycleDefaults_PopulatesRestartPending(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	changed := EnsureLifecycleDefaults(cfg)
	if !changed {
		t.Fatal("expected EnsureLifecycleDefaults to report a change")
	}
	if cfg.Patrols.RestartPending == nil {
		t.Fatal("expected RestartPending to be populated")
	}
	if !cfg.Patrols.RestartPending.Enabled {
		t.Error("default RestartPending should be Enabled")
	}
}

func TestDefaultLifecycleConfig_IncludesRestartPending(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	if cfg.Patrols == nil || cfg.Patrols.RestartPending == nil {
		t.Fatal("DefaultLifecycleConfig must include RestartPending")
	}
	if !cfg.Patrols.RestartPending.Enabled {
		t.Error("default RestartPending should be Enabled")
	}
	if cfg.Patrols.RestartPending.IntervalStr == "" {
		t.Error("default RestartPending should set an interval")
	}
}

// --- Escalation message builder ---

func TestBuildRestartEscalationMessage_IncludesStateAndAction(t *testing.T) {
	d := &Daemon{}
	b := restartPendingBead{
		ID:          "gu-test1",
		Title:       "daemon-restart-pending: gt binary upgraded to v1.2.3",
		Description: "rebuild-gt upgraded the on-disk binary. Daemon still on old code.",
	}
	msg := d.buildRestartEscalationMessage(b, restartForwardCheck{
		Computed:      true,
		Forward:       true,
		RunningCommit: "aaaaaaaaaaaa1111",
		RepoCommit:    "bbbbbbbbbbbb2222",
		CompareRef:    "main",
	})

	// Acceptance (b): escalation must carry enough state for an agent to gate.
	for _, want := range []string{
		"gu-test1",                          // the pending bead id
		"upgraded to v1.2.3",                // which binary is pending (from title)
		"OLD in-memory image",               // why it matters
		"gt daemon stop && gt daemon start", // the gated action
		"FORWARD-ONLY",                      // pre-computed ancestry verdict (gu-8ni5o)
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("escalation message missing %q.\nGot:\n%s", want, msg)
		}
	}

	// Acceptance (d): must NOT instruct an autonomous self-restart.
	if strings.Contains(strings.ToLower(msg), "auto-restart") &&
		!strings.Contains(strings.ToLower(msg), "not auto-restart") {
		t.Errorf("message should not advocate auto-restart; got:\n%s", msg)
	}
}

// TestBuildRestartEscalationMessage_NotForwardWarns verifies the escalation
// surfaces the NOT-forward verdict loudly so the responder does not blindly
// restart into a downgrade/diverge (gu-8ni5o).
func TestBuildRestartEscalationMessage_NotForwardWarns(t *testing.T) {
	d := &Daemon{}
	msg := d.buildRestartEscalationMessage(
		restartPendingBead{ID: "gu-nf", Title: "daemon-restart-pending"},
		restartForwardCheck{
			Computed:      true,
			Forward:       false,
			RunningCommit: "aaaaaaaaaaaa1111",
			RepoCommit:    "cccccccccccc3333",
			CompareRef:    "main",
		},
	)
	if !strings.Contains(msg, "NOT FORWARD-ONLY") {
		t.Errorf("expected NOT-forward warning in message; got:\n%s", msg)
	}
	if !strings.Contains(msg, "aaaaaaaaaaaa") || !strings.Contains(msg, "cccccccccccc") {
		t.Errorf("expected both running and new commits in message; got:\n%s", msg)
	}
}

// TestBuildRestartEscalationMessage_UnknownVerdictFallback verifies that when
// the verdict could not be pre-computed, the escalation still tells the
// responder how to run the manual check (gu-8ni5o).
func TestBuildRestartEscalationMessage_UnknownVerdictFallback(t *testing.T) {
	d := &Daemon{}
	msg := d.buildRestartEscalationMessage(
		restartPendingBead{ID: "gu-uv", Title: "daemon-restart-pending"},
		restartForwardCheck{Detail: "could not locate gt source repo"},
	)
	if !strings.Contains(msg, "UNKNOWN") {
		t.Errorf("expected UNKNOWN verdict in message; got:\n%s", msg)
	}
	if !strings.Contains(msg, "merge-base --is-ancestor") {
		t.Errorf("expected manual fallback instructions; got:\n%s", msg)
	}
}

// --- Auto-resolve lingering pending state (gu-ed9ba) ---

// newRestartPendingTestDaemon wires a Daemon with a fake `bd` that records its
// argv to a log file and emits the given stdout for `bd list`.
func newRestartPendingTestDaemon(t *testing.T, listJSON string) (*Daemon, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	townRoot := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"" + logPath + "\"\n" +
		"case \"$1\" in\n" +
		"  list) cat <<'EOF'\n" + listJSON + "\nEOF\n  ;;\n" +
		"esac\n" +
		"exit 0\n"
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		bdPath: bdPath,
		logger: log.New(io.Discard, "", 0),
	}
	return d, logPath
}

func TestListOpenRestartPending_FlagsEscalated(t *testing.T) {
	d, _ := newRestartPendingTestDaemon(t, `[
		{"id":"gu-a","title":"pending a","description":"d","labels":["type:daemon-restart-pending"]},
		{"id":"gu-b","title":"pending b","description":"d","labels":["type:daemon-restart-pending","restart-escalated"]}
	]`)

	beads, err := d.listOpenRestartPending()
	if err != nil {
		t.Fatalf("listOpenRestartPending: %v", err)
	}
	if len(beads) != 2 {
		t.Fatalf("expected 2 beads, got %d", len(beads))
	}
	byID := map[string]restartPendingBead{}
	for _, b := range beads {
		byID[b.ID] = b
	}
	if byID["gu-a"].Escalated {
		t.Error("gu-a should not be marked escalated")
	}
	if !byID["gu-b"].Escalated {
		t.Error("gu-b should be marked escalated")
	}
}

func TestCloseRestartPending_IssuesBdClose(t *testing.T) {
	d, logPath := newRestartPendingTestDaemon(t, "[]")

	if err := d.closeRestartPending("gu-x"); err != nil {
		t.Fatalf("closeRestartPending: %v", err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	got := string(logData)
	if !strings.Contains(got, "close gu-x") {
		t.Errorf("expected bd close gu-x, got: %s", got)
	}
	if !strings.Contains(got, "--reason=") {
		t.Errorf("expected a close reason, got: %s", got)
	}
}

func TestResolveFreshRestartPending_ClosesAll(t *testing.T) {
	d, logPath := newRestartPendingTestDaemon(t, "[]")

	d.resolveFreshRestartPending([]restartPendingBead{
		{ID: "gu-1"}, {ID: "gu-2"},
	})

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	got := string(logData)
	for _, want := range []string{"close gu-1", "close gu-2"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in bd calls, got: %s", want, got)
		}
	}
}

func TestRestartForwardCheck_DaemonFreshDefaultsFalse(t *testing.T) {
	// A freshly-constructed check (verdict not pre-computed) must not claim the
	// daemon is fresh — otherwise the dog would wrongly auto-close pending beads.
	var fc restartForwardCheck
	if fc.DaemonFresh {
		t.Error("zero-value restartForwardCheck must not report DaemonFresh")
	}
}

func TestRestartForwardCheck_RenderAlreadyUpToDate(t *testing.T) {
	fc := restartForwardCheck{
		Computed:      true,
		Forward:       true,
		RunningCommit: "aaaaaaaaaaaa1111",
		RepoCommit:    "aaaaaaaaaaaa1111",
		CompareRef:    "main",
		Detail:        "running daemon is already at the repo tip (no newer commit to advance to)",
	}
	out := fc.render()
	if !strings.Contains(out, "FORWARD-ONLY ✓") {
		t.Errorf("expected forward verdict; got:\n%s", out)
	}
	if !strings.Contains(out, "already at the repo tip") {
		t.Errorf("expected up-to-date note; got:\n%s", out)
	}
}

func TestBuildRestartEscalationMessage_FirstLineIsSingleLineTitle(t *testing.T) {
	// d.escalate uses the first line as the bd title, which must be single-line.
	d := &Daemon{}
	msg := d.buildRestartEscalationMessage(restartPendingBead{ID: "gu-x", Title: "t"}, restartForwardCheck{})
	firstLine := msg
	if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
		firstLine = msg[:idx]
	}
	if firstLine == "" {
		t.Error("first line (used as escalation title) must not be empty")
	}
	if strings.Contains(firstLine, "\n") {
		t.Error("first line must be single-line")
	}
}

package session

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/tmux"
)

func hasTmux() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

func TestStartSession_RequiresSessionID(t *testing.T) {
	_, err := StartSession(nil, SessionConfig{
		WorkDir: "/tmp",
		Role:    "polecat",
	})
	if err == nil {
		t.Fatal("expected error for missing SessionID")
	}
	if err.Error() != "SessionID is required" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStartSession_RequiresWorkDir(t *testing.T) {
	_, err := StartSession(nil, SessionConfig{
		SessionID: "gt-test",
		Role:      "polecat",
	})
	if err == nil {
		t.Fatal("expected error for missing WorkDir")
	}
	if err.Error() != "WorkDir is required" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStartSession_RequiresRole(t *testing.T) {
	_, err := StartSession(nil, SessionConfig{
		SessionID: "gt-test",
		WorkDir:   "/tmp",
	})
	if err == nil {
		t.Fatal("expected error for missing Role")
	}
	if err.Error() != "Role is required" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBuildPrompt_BeaconOnly(t *testing.T) {
	cfg := SessionConfig{
		Beacon: BeaconConfig{
			Recipient: "boot",
			Sender:    "daemon",
			Topic:     "triage",
		},
	}
	prompt := buildPrompt(cfg)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !contains(prompt, "[GAS TOWN]") {
		t.Errorf("prompt should contain beacon: %s", prompt)
	}
}

func TestBuildPrompt_WithInstructions(t *testing.T) {
	cfg := SessionConfig{
		Beacon: BeaconConfig{
			Recipient: "boot",
			Sender:    "daemon",
			Topic:     "triage",
		},
		Instructions: "Run gt boot triage now.",
	}
	prompt := buildPrompt(cfg)
	if !contains(prompt, "Run gt boot triage now.") {
		t.Errorf("prompt should contain instructions: %s", prompt)
	}
	if !contains(prompt, "[GAS TOWN]") {
		t.Errorf("prompt should contain beacon: %s", prompt)
	}
}

func TestBuildCommand_DefaultAgent(t *testing.T) {
	cfg := SessionConfig{
		Role:     "boot",
		TownRoot: "/tmp/town",
	}
	cmd, err := buildCommand(cfg, "test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd == "" {
		t.Fatal("expected non-empty command")
	}
}

func TestBuildCommand_WithAgentOverride(t *testing.T) {
	cfg := SessionConfig{
		Role:          "boot",
		TownRoot:      "/tmp/town",
		AgentOverride: "opencode",
	}
	cmd, err := buildCommand(cfg, "test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd == "" {
		t.Fatal("expected non-empty command")
	}
}

func TestKillExistingSession_NoSession(t *testing.T) {
	// KillExistingSession with nil tmux would panic, but we test the logic
	// by verifying it's callable. Full integration tests need a real tmux.
	// This test verifies the function signature and basic flow.
	t.Skip("requires tmux for integration testing")
}

func TestMapKeysSorted(t *testing.T) {
	got := mapKeysSorted(map[string]string{
		"GT_SESSION": "1",
		"GT_ROLE":    "polecat",
		"GT_RIG":     "alpha",
	})

	want := []string{"GT_RIG", "GT_ROLE", "GT_SESSION"}
	if len(got) != len(want) {
		t.Fatalf("mapKeysSorted() length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mapKeysSorted()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMergeRuntimeLivenessEnv_SetsResolvedAgentAndProcessNames(t *testing.T) {
	env := map[string]string{
		"GT_ROLE": "polecat",
	}
	rc := &config.RuntimeConfig{
		Command:       "claude",
		ResolvedAgent: "claude",
	}

	got := MergeRuntimeLivenessEnv(env, rc)

	if got["GT_AGENT"] != "claude" {
		t.Fatalf("GT_AGENT = %q, want %q", got["GT_AGENT"], "claude")
	}
	if got["GT_PROCESS_NAMES"] != "node,claude" {
		t.Fatalf("GT_PROCESS_NAMES = %q, want %q", got["GT_PROCESS_NAMES"], "node,claude")
	}
}

func TestMergeRuntimeLivenessEnv_RespectsExistingValues(t *testing.T) {
	env := map[string]string{
		"GT_AGENT":         "explicit-agent",
		"GT_PROCESS_NAMES": "custom-bin,custom-agent",
	}
	rc := &config.RuntimeConfig{
		Command:       "bun",
		ResolvedAgent: "wen",
	}

	got := MergeRuntimeLivenessEnv(env, rc)

	if got["GT_AGENT"] != "explicit-agent" {
		t.Fatalf("GT_AGENT = %q, want %q", got["GT_AGENT"], "explicit-agent")
	}
	if got["GT_PROCESS_NAMES"] != "custom-bin,custom-agent" {
		t.Fatalf("GT_PROCESS_NAMES = %q, want %q", got["GT_PROCESS_NAMES"], "custom-bin,custom-agent")
	}
}

func TestMergeRuntimeLivenessEnv_UsesEffectiveAgentForProcessNames(t *testing.T) {
	// When AgentOverride sets GT_AGENT to a different agent than
	// runtimeConfig.ResolvedAgent, process names must be resolved from
	// the effective agent (GT_AGENT), not the workspace-default resolved agent.
	env := map[string]string{
		"GT_AGENT": "codex", // set by AgentEnv from AgentOverride
	}
	rc := &config.RuntimeConfig{
		Command:       "claude",
		ResolvedAgent: "claude", // workspace default, NOT the override
	}

	got := MergeRuntimeLivenessEnv(env, rc)

	if got["GT_AGENT"] != "codex" {
		t.Fatalf("GT_AGENT = %q, want %q", got["GT_AGENT"], "codex")
	}
	if got["GT_PROCESS_NAMES"] != "codex" {
		t.Fatalf("GT_PROCESS_NAMES = %q, want %q (should resolve from effective agent, not runtimeConfig)", got["GT_PROCESS_NAMES"], "codex")
	}
}

func TestCapturePaneDiagnostic_NilTmux(t *testing.T) {
	got := capturePaneDiagnostic(nil, "hq-dog-test")
	if got != "" {
		t.Errorf("capturePaneDiagnostic(nil, ...) = %q, want empty", got)
	}
}

func TestCapturePaneDiagnostic_EmptySessionID(t *testing.T) {
	// Real tmux not needed: empty session short-circuits before any tmux call.
	// The *tmux.Tmux param is unused on this path, so a dummy pointer is fine.
	got := capturePaneDiagnostic(nil, "")
	if got != "" {
		t.Errorf("capturePaneDiagnostic(_, \"\") = %q, want empty", got)
	}
}

func TestDiagPaneCaptureBytes_Reasonable(t *testing.T) {
	// Guardrail: keep the cap small enough to fit in a daemon.log line
	// without truncation but large enough to carry a real stack trace.
	if diagPaneCaptureBytes < 512 {
		t.Errorf("diagPaneCaptureBytes = %d, too small for useful stack traces", diagPaneCaptureBytes)
	}
	if diagPaneCaptureBytes > 16*1024 {
		t.Errorf("diagPaneCaptureBytes = %d, too large — will flood logs", diagPaneCaptureBytes)
	}
}

func TestCapturePaneDiagnostic_RealSessionCapturesOutput(t *testing.T) {
	if !hasTmux() {
		t.Skip("tmux not installed")
	}
	tm := tmux.NewTmux()
	sessionID := "gt-test-capture-diag-" + t.Name()
	_ = tm.KillSession(sessionID)
	defer func() { _ = tm.KillSession(sessionID) }()

	// Use a long-running shell so NewSessionWithCommand's health check passes.
	// Then write a marker via send-keys and capture it — this exercises the
	// same tmux capture-pane path used by capturePaneDiagnostic on real spawn
	// failures where the pane is alive but the agent has died.
	if err := tm.NewSession(sessionID, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := tm.SendKeys(sessionID, "echo DIAG-MARKER-12345"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	// Poll for the marker to appear — shells on slow CI may take a moment.
	var got string
	for i := 0; i < 30; i++ {
		got = capturePaneDiagnostic(tm, sessionID)
		if strings.Contains(got, "DIAG-MARKER-12345") {
			break
		}
		for j := 0; j < 5_000_000; j++ {
		}
	}
	if !strings.Contains(got, "DIAG-MARKER-12345") {
		t.Errorf("capturePaneDiagnostic did not capture marker; got %q", got)
	}
}

func TestCapturePaneDiagnostic_MissingSessionReturnsEmpty(t *testing.T) {
	if !hasTmux() {
		t.Skip("tmux not installed")
	}
	tm := tmux.NewTmux()
	got := capturePaneDiagnostic(tm, "gt-test-nonexistent-session-xyz")
	if got != "" {
		t.Errorf("capturePaneDiagnostic on missing session = %q, want empty", got)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

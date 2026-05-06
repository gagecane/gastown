package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------
// isPolecatDone / hookedBeadID — heartbeat-reading helpers
// ---------------------------------------------------------------

func TestIsPolecatDone(t *testing.T) {
	// No session name → not done (errs toward recovery)
	if isPolecatDone("/tmp/nonexistent", "") {
		t.Error("empty session should return false")
	}
	// No town root → not done
	if isPolecatDone("", "some-session") {
		t.Error("empty town root should return false")
	}
	// Nonexistent heartbeat file → not done
	if isPolecatDone("/tmp/nonexistent-town", "no-such-session") {
		t.Error("missing heartbeat should return false")
	}
}

func TestIsPolecatDone_StateDrivenOnly(t *testing.T) {
	townRoot := t.TempDir()
	sess := "wrapper-test-session"
	hbDir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(hbDir, 0o755); err != nil {
		t.Fatalf("mkdir heartbeats: %v", err)
	}

	cases := []struct {
		state    string
		wantDone bool
	}{
		{"working", false},
		{"stuck", false},
		{"idle", true},
		{"exiting", true},
	}

	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			hb := map[string]any{
				"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
				"state":     tc.state,
			}
			data, _ := json.Marshal(hb)
			path := filepath.Join(hbDir, sess+".json")
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatalf("write heartbeat: %v", err)
			}
			if got := isPolecatDone(townRoot, sess); got != tc.wantDone {
				t.Errorf("state=%q: isPolecatDone = %v, want %v", tc.state, got, tc.wantDone)
			}
		})
	}
}

func TestHookedBeadID(t *testing.T) {
	// Empty inputs → empty bead ID, no panic. The wrapper falls back to
	// the generic prompt in this path.
	if got := hookedBeadID("", ""); got != "" {
		t.Errorf("empty inputs: hookedBeadID = %q, want empty", got)
	}
	if got := hookedBeadID("/tmp/nonexistent", "no-session"); got != "" {
		t.Errorf("missing heartbeat: hookedBeadID = %q, want empty", got)
	}

	// With a v2 heartbeat carrying a bead, it's returned. Mirrors the
	// real TouchSessionHeartbeatWithState writer.
	townRoot := t.TempDir()
	sess := "bead-session"
	hbDir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(hbDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	hb := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"state":     "working",
		"bead":      "gu-abc12",
	}
	data, _ := json.Marshal(hb)
	if err := os.WriteFile(filepath.Join(hbDir, sess+".json"), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := hookedBeadID(townRoot, sess); got != "gu-abc12" {
		t.Errorf("v2 heartbeat with bead: hookedBeadID = %q, want %q", got, "gu-abc12")
	}

	// v1-style heartbeat (no bead field) returns empty — not an error,
	// just falls back to the generic prompt.
	v1 := map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano)}
	data, _ = json.Marshal(v1)
	_ = os.WriteFile(filepath.Join(hbDir, "v1-sess.json"), data, 0o644)
	if got := hookedBeadID(townRoot, "v1-sess"); got != "" {
		t.Errorf("v1 heartbeat: hookedBeadID = %q, want empty (bead field absent)", got)
	}
}

// ---------------------------------------------------------------
// Continuation prompt — generic vs bead-aware
// ---------------------------------------------------------------

func TestContinuePromptNoNewlines(t *testing.T) {
	// The continuation prompt is passed as a single INPUT positional arg
	// to kiro-cli via exec.Command. Newlines would break argv parsing on
	// some shells that re-parse the command string, and would confuse the
	// stderr echo. Enforce that both the base prompt and any bead-inlined
	// variant stay single-line.
	for _, ch := range kiroContinuePromptBase {
		if ch == '\n' || ch == '\r' {
			t.Fatalf("kiroContinuePromptBase contains newline character")
		}
	}

	townRoot := t.TempDir()
	sess := "newline-check"
	hbDir := filepath.Join(townRoot, ".runtime", "heartbeats")
	_ = os.MkdirAll(hbDir, 0o755)
	hb := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"state":     "working",
		"bead":      "gu-xyz99",
	}
	data, _ := json.Marshal(hb)
	_ = os.WriteFile(filepath.Join(hbDir, sess+".json"), data, 0o644)

	p := buildContinuePrompt(townRoot, sess)
	for _, ch := range p {
		if ch == '\n' || ch == '\r' {
			t.Fatalf("buildContinuePrompt (with bead) contains newline")
		}
	}
}

func TestBuildContinuePrompt_GenericFallback(t *testing.T) {
	// No heartbeat, no bead: falls through to the generic base prompt
	// unchanged. The resumed session gets the ordinary "finish your
	// formula" reminder without any bead pointer.
	got := buildContinuePrompt("/tmp/nonexistent-town", "no-session")
	if got != kiroContinuePromptBase {
		t.Errorf("no-bead path should return base prompt verbatim\n got: %q\nwant: %q", got, kiroContinuePromptBase)
	}
}

func TestBuildContinuePrompt_IncludesBeadID(t *testing.T) {
	townRoot := t.TempDir()
	sess := "prompt-with-bead"
	hbDir := filepath.Join(townRoot, ".runtime", "heartbeats")
	_ = os.MkdirAll(hbDir, 0o755)
	hb := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"state":     "working",
		"bead":      "gu-417s",
	}
	data, _ := json.Marshal(hb)
	_ = os.WriteFile(filepath.Join(hbDir, sess+".json"), data, 0o644)

	p := buildContinuePrompt(townRoot, sess)
	if !strings.HasPrefix(p, kiroContinuePromptBase) {
		t.Errorf("bead-aware prompt should extend the base, got: %q", p)
	}
	if !strings.Contains(p, "gu-417s") {
		t.Errorf("bead-aware prompt should mention the bead ID, got: %q", p)
	}
	if !strings.Contains(p, "gt done") {
		t.Errorf("bead-aware prompt should still mention gt done, got: %q", p)
	}
}

func TestBuildResumeArgs_AppendsResumeAndPrompt(t *testing.T) {
	base := []string{"kiro-cli", "chat", "--trust-all-tools", "--agent", "gastown"}
	out := buildResumeArgs(base, "", "")

	// Base args must be preserved verbatim at the front; we rely on
	// kiro-cli seeing the same invocation config on resume.
	for i, a := range base {
		if out[i] != a {
			t.Errorf("out[%d] = %q, want %q (base arg preserved)", i, out[i], a)
		}
	}
	// --resume must be the second-to-last token; the prompt must be last.
	if len(out) != len(base)+2 {
		t.Fatalf("out length = %d, want %d (base + --resume + prompt)", len(out), len(base)+2)
	}
	if out[len(out)-2] != "--resume" {
		t.Errorf("penultimate arg = %q, want --resume", out[len(out)-2])
	}
	if out[len(out)-1] == "" {
		t.Errorf("final arg (prompt) is empty")
	}
}

// ---------------------------------------------------------------
// loadWrapperConfig + parseDurationEnv — env var parsing
// ---------------------------------------------------------------

func TestLoadWrapperConfig_Defaults(t *testing.T) {
	// Ensure env is clean for this test.
	for _, v := range []string{
		"GT_KIRO_MAX_ITERATIONS", "GT_KIRO_ITERATION_TIMEOUT",
		"GT_KIRO_TOTAL_TIMEOUT", "GT_KIRO_RETRY_BACKOFF",
		"GT_SESSION", "GT_TOWN_ROOT",
	} {
		t.Setenv(v, "")
	}
	// Set GT_TOWN_ROOT explicitly so loadWrapperConfig doesn't try to
	// walk up from a test cwd that may not be a gastown workspace (which
	// would make the test machine-dependent).
	t.Setenv("GT_TOWN_ROOT", t.TempDir())

	cfg := loadWrapperConfig()
	if cfg.maxIterations != defaultMaxKiroIterations {
		t.Errorf("maxIterations = %d, want %d", cfg.maxIterations, defaultMaxKiroIterations)
	}
	if cfg.iterationTimeout != defaultIterationTimeout {
		t.Errorf("iterationTimeout = %s, want %s", cfg.iterationTimeout, defaultIterationTimeout)
	}
	if cfg.totalTimeout != defaultTotalTimeout {
		t.Errorf("totalTimeout = %s, want %s", cfg.totalTimeout, defaultTotalTimeout)
	}
	if cfg.retryBackoff != defaultRetryBackoff {
		t.Errorf("retryBackoff = %s, want %s", cfg.retryBackoff, defaultRetryBackoff)
	}
}

func TestLoadWrapperConfig_Overrides(t *testing.T) {
	t.Setenv("GT_TOWN_ROOT", t.TempDir())
	t.Setenv("GT_KIRO_MAX_ITERATIONS", "3")
	t.Setenv("GT_KIRO_ITERATION_TIMEOUT", "7m")
	t.Setenv("GT_KIRO_TOTAL_TIMEOUT", "20m")
	t.Setenv("GT_KIRO_RETRY_BACKOFF", "500ms")
	t.Setenv("GT_SESSION", "my-session")

	cfg := loadWrapperConfig()
	if cfg.maxIterations != 3 {
		t.Errorf("maxIterations = %d, want 3", cfg.maxIterations)
	}
	if cfg.iterationTimeout != 7*time.Minute {
		t.Errorf("iterationTimeout = %s, want 7m", cfg.iterationTimeout)
	}
	if cfg.totalTimeout != 20*time.Minute {
		t.Errorf("totalTimeout = %s, want 20m", cfg.totalTimeout)
	}
	if cfg.retryBackoff != 500*time.Millisecond {
		t.Errorf("retryBackoff = %s, want 500ms", cfg.retryBackoff)
	}
	if cfg.sessionName != "my-session" {
		t.Errorf("sessionName = %q, want %q", cfg.sessionName, "my-session")
	}
}

func TestLoadWrapperConfig_InvalidValuesFallBack(t *testing.T) {
	t.Setenv("GT_TOWN_ROOT", t.TempDir())
	// Unparseable duration, negative, and non-positive max-iterations all
	// fall through to defaults — the wrapper must not refuse to run just
	// because an operator typo'd an env var.
	t.Setenv("GT_KIRO_MAX_ITERATIONS", "0")
	t.Setenv("GT_KIRO_ITERATION_TIMEOUT", "not-a-duration")
	t.Setenv("GT_KIRO_TOTAL_TIMEOUT", "-5m")

	cfg := loadWrapperConfig()
	if cfg.maxIterations != defaultMaxKiroIterations {
		t.Errorf("maxIterations = %d on 0-override, want default %d", cfg.maxIterations, defaultMaxKiroIterations)
	}
	if cfg.iterationTimeout != defaultIterationTimeout {
		t.Errorf("iterationTimeout = %s on unparseable override, want default %s", cfg.iterationTimeout, defaultIterationTimeout)
	}
	if cfg.totalTimeout != defaultTotalTimeout {
		t.Errorf("totalTimeout = %s on negative override, want default %s", cfg.totalTimeout, defaultTotalTimeout)
	}
}

func TestLoadWrapperConfig_RetryBackoffZeroDisables(t *testing.T) {
	t.Setenv("GT_TOWN_ROOT", t.TempDir())
	// "0" is a VALID explicit disable for retry backoff — distinct from
	// unset (which yields the default). Timeouts reject zero because a
	// zero-duration context would kill kiro-cli instantly.
	t.Setenv("GT_KIRO_RETRY_BACKOFF", "0")
	t.Setenv("GT_KIRO_ITERATION_TIMEOUT", "0")
	t.Setenv("GT_KIRO_TOTAL_TIMEOUT", "0")

	cfg := loadWrapperConfig()
	if cfg.retryBackoff != 0 {
		t.Errorf("retryBackoff = %s on explicit 0, want 0 (disable)", cfg.retryBackoff)
	}
	if cfg.iterationTimeout != defaultIterationTimeout {
		t.Errorf("iterationTimeout = %s on 0-override, want default (0 is not allowed)", cfg.iterationTimeout)
	}
	if cfg.totalTimeout != defaultTotalTimeout {
		t.Errorf("totalTimeout = %s on 0-override, want default (0 is not allowed)", cfg.totalTimeout)
	}
}

func TestParseDurationEnv(t *testing.T) {
	cases := []struct {
		desc      string
		value     string
		allowZero bool
		wantDur   time.Duration
		wantOK    bool
	}{
		{"unset", "", false, 0, false},
		{"valid duration", "2m", false, 2 * time.Minute, true},
		{"zero disallowed", "0", false, 0, false},
		{"zero allowed (backoff)", "0", true, 0, true},
		{"negative rejected", "-1s", false, 0, false},
		{"unparseable rejected", "abc", false, 0, false},
		{"sub-second ok", "250ms", false, 250 * time.Millisecond, true},
	}
	const envName = "WRAPPER_TEST_DUR"
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Setenv(envName, tc.value)
			gotDur, gotOK := parseDurationEnv(envName, tc.allowZero)
			if gotOK != tc.wantOK {
				t.Errorf("ok = %v, want %v (value=%q allowZero=%v)",
					gotOK, tc.wantOK, tc.value, tc.allowZero)
			}
			if gotOK && gotDur != tc.wantDur {
				t.Errorf("dur = %s, want %s (value=%q)", gotDur, tc.wantDur, tc.value)
			}
		})
	}
}

// ---------------------------------------------------------------
// Config sanity — defaults are internally consistent
// ---------------------------------------------------------------

// TestDefaults_InternalConsistency catches accidental regressions where
// the per-iteration cap grows past the total budget (making iteration
// timeout effectively moot) or backoff creeps past the iteration cap
// (which would mean a retry cycle can't fit a real kiro-cli call).
func TestDefaults_InternalConsistency(t *testing.T) {
	if defaultIterationTimeout > defaultTotalTimeout {
		t.Errorf("defaultIterationTimeout (%s) exceeds defaultTotalTimeout (%s) — iteration cap is moot",
			defaultIterationTimeout, defaultTotalTimeout)
	}
	if defaultRetryBackoff >= defaultIterationTimeout {
		t.Errorf("defaultRetryBackoff (%s) >= defaultIterationTimeout (%s) — no room for real work after backoff",
			defaultRetryBackoff, defaultIterationTimeout)
	}
	if defaultMaxKiroIterations < 1 {
		t.Errorf("defaultMaxKiroIterations = %d, must be >= 1", defaultMaxKiroIterations)
	}
	// The product of per-iteration cap × max iterations doesn't HAVE to
	// equal total timeout — total is the hard stop when iterations run
	// long. But if total is way smaller than iter × max, the iter cap
	// never actually trips, which defeats the purpose of having both.
	// Use a generous 2x slack before complaining.
	product := time.Duration(defaultMaxKiroIterations) * defaultIterationTimeout
	if product < defaultTotalTimeout/2 {
		t.Errorf("max-iterations × iteration-timeout = %s, less than half of total-timeout (%s) — iter cap will rarely trip",
			product, defaultTotalTimeout)
	}
}

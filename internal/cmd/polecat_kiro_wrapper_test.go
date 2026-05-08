package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

// TestBuildResumeArgs_StripsBootstrapInput_gu_q319 is the direct
// regression test for gu-q319. Before the fix, buildResumeArgs appended
// "--resume <continuation>" to baseArgs that already ended with the
// initial polecat bootstrap prompt as kiro-cli's [INPUT] positional,
// producing TWO positionals. kiro-cli's clap-based parser rejects that
// with "error: unexpected argument ... found" and exits 2, which the
// wrapper then propagated — killing the polecat mid-recovery on iter 2
// and stranding any dirty work.
//
// Post-fix, the trailing bootstrap prompt is stripped before --resume is
// appended, so the continuation prompt becomes the one-and-only [INPUT]
// positional. This test asserts the invariant "at most one non-flag
// token after `chat` in the positional slot" which is what clap actually
// enforces.
func TestBuildResumeArgs_StripsBootstrapInput_gu_q319(t *testing.T) {
	// Mirrors the real preset: AgentKiro.Args = [polecat-kiro-wrapper --
	// kiro-cli chat --trust-all-tools] + NonInteractive.PromptFlag
	// (--no-interactive) + BuildCommandWithPrompt's trailing bootstrap
	// prompt. By the time it reaches the wrapper, args look like this:
	base := []string{
		"kiro-cli", "chat",
		"--trust-all-tools",
		"--no-interactive",
		"You are polecat chrome. Start your assigned work now.",
	}

	out := buildResumeArgs(base, "", "")

	// The bootstrap prompt must be gone. The continuation prompt added
	// by buildResumeArgs is the only trailing positional after --resume.
	if got := out[len(out)-1]; !strings.Contains(got, "gt done") {
		t.Errorf("final arg is not the continuation prompt: %q", got)
	}
	if got := out[len(out)-2]; got != "--resume" {
		t.Fatalf("penultimate arg = %q, want --resume", got)
	}

	// Count non-flag tokens after "chat". kiro-cli chat accepts at most
	// one. Anything beyond the subcommand must either be a flag
	// (starts with "-") or a value consumed by a preceding flag.
	trailingPositionals := countTrailingPositionalsAfterChat(out, kiroValueConsumingFlags)
	if trailingPositionals > 1 {
		t.Errorf("argv has %d trailing positionals after `chat`, want <= 1 (clap would reject)\nargv: %v",
			trailingPositionals, out)
	}

	// Belt-and-suspenders: the bootstrap prompt itself must not appear
	// in the output. If it did, clap would fail whether or not my
	// counter got it right.
	for _, a := range out {
		if a == base[len(base)-1] {
			t.Errorf("bootstrap prompt survived into iter-2 argv: %q\nfull argv: %v",
				base[len(base)-1], out)
			break
		}
	}
}

// countTrailingPositionalsAfterChat counts positional args (non-flag,
// non-flag-value tokens) that appear after the `chat` subcommand. This
// is the same invariant clap enforces: kiro-cli chat accepts [OPTIONS]
// followed by at most one [INPUT]. Test helper — not used by the
// wrapper at runtime.
func countTrailingPositionalsAfterChat(argv []string, valueFlags map[string]bool) int {
	// Find `chat` subcommand; count positionals after it.
	chatIdx := -1
	for i, a := range argv {
		if a == "chat" {
			chatIdx = i
			break
		}
	}
	if chatIdx < 0 {
		return 0
	}
	count := 0
	i := chatIdx + 1
	for i < len(argv) {
		a := argv[i]
		if strings.HasPrefix(a, "-") {
			// Flag. If it's a value-consuming flag and has a following
			// token, skip that token too so we don't double-count it
			// as a positional.
			if valueFlags[a] && i+1 < len(argv) {
				i += 2
				continue
			}
			i++
			continue
		}
		count++
		i++
	}
	return count
}

// TestStripTrailingInput exercises the edge cases of the positional-
// detection heuristic that keeps iter-2 invocations from tripping
// kiro-cli's one-[INPUT] limit (gu-q319).
func TestStripTrailingInput(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "nil",
			in:   nil,
			want: nil,
		},
		{
			name: "empty",
			in:   []string{},
			want: []string{},
		},
		{
			name: "flag-only (no positional)",
			in:   []string{"kiro-cli", "chat", "--trust-all-tools"},
			want: []string{"kiro-cli", "chat", "--trust-all-tools"},
		},
		{
			name: "flag-value at end — must NOT strip value",
			in:   []string{"kiro-cli", "chat", "--agent", "gastown"},
			want: []string{"kiro-cli", "chat", "--agent", "gastown"},
		},
		{
			name: "short flag-value at end — must NOT strip value",
			in:   []string{"kiro-cli", "chat", "-f", "json"},
			want: []string{"kiro-cli", "chat", "-f", "json"},
		},
		{
			name: "trailing INPUT — must strip",
			in:   []string{"kiro-cli", "chat", "--trust-all-tools", "my bootstrap prompt"},
			want: []string{"kiro-cli", "chat", "--trust-all-tools"},
		},
		{
			name: "flag + value + INPUT — strip only INPUT",
			in:   []string{"kiro-cli", "chat", "--agent", "gastown", "prompt text"},
			want: []string{"kiro-cli", "chat", "--agent", "gastown"},
		},
		{
			name: "single arg that's a flag",
			in:   []string{"--resume"},
			want: []string{"--resume"},
		},
		{
			name: "single positional arg",
			in:   []string{"prompt"},
			want: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripTrailingInput(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len(out) = %d, want %d\n got: %v\nwant: %v",
					len(got), len(tc.want), got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("out[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestBuildResumeArgs_PreservesFlagsThatTakeValues guards against a
// regression where the gu-q319 fix's stripTrailingInput would incorrectly
// eat a flag value (e.g. "gastown" from "--agent gastown"), leaving the
// iter-2 invocation with a dangling --agent flag and no value. clap would
// accept that (kiro-cli would just use its default agent), but it would
// silently break the polecat preset.
func TestBuildResumeArgs_PreservesFlagsThatTakeValues(t *testing.T) {
	base := []string{"kiro-cli", "chat", "--no-interactive", "--agent", "gastown"}
	out := buildResumeArgs(base, "", "")

	// All original tokens, in order, must appear at the head of out.
	for i, a := range base {
		if i >= len(out) {
			t.Fatalf("out shorter than base: %v", out)
		}
		if out[i] != a {
			t.Errorf("out[%d] = %q, want %q (original arg preserved)", i, out[i], a)
		}
	}
	// --resume + prompt appended.
	if len(out) != len(base)+2 {
		t.Fatalf("len(out) = %d, want %d (base + --resume + prompt)", len(out), len(base)+2)
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

// ---------------------------------------------------------------
// Dirty-tree capture — gu-q319 acceptance criterion 3
// ---------------------------------------------------------------

// TestSanitizeRefComponent checks that arbitrary session-name-like
// strings get mapped to forms git update-ref will accept. The wrapper
// uses this to build the `refs/archive/polecat-autosave/<session>/...`
// ref path; a malformed segment would make update-ref fail and lose
// the snapshot.
func TestSanitizeRefComponent(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"simple", "simple"},
		{"has-dash", "has-dash"},
		{"has_underscore", "has_underscore"},
		{"has.dot", "has.dot"},
		{"with spaces", "with-spaces"},
		{"slash/in/name", "slash-in-name"},
		{"colon:inside", "colon-inside"},
		{"gastown_upstream.polecats.chrome.sess1", "gastown_upstream.polecats.chrome.sess1"},
		{".leading-dot", "leading-dot"},
		{"trailing-dash-", "trailing-dash"},
		{"--double-dash--", "double-dash"},
		{"", ""},
		{".-", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := sanitizeRefComponent(tc.in); got != tc.want {
				t.Errorf("sanitizeRefComponent(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestCaptureDirtyWorkingTree_NoSession guards the early-return path:
// without a session name we can't build a unique ref path, so capture
// is skipped silently.
func TestCaptureDirtyWorkingTree_NoSession(t *testing.T) {
	if got := captureDirtyWorkingTree("", 1); got != "" {
		t.Errorf("no session: captureDirtyWorkingTree = %q, want empty", got)
	}
	if got := captureDirtyWorkingTree(".-", 1); got != "" {
		t.Errorf("session that sanitizes to empty: captureDirtyWorkingTree = %q, want empty", got)
	}
}

// TestCaptureDirtyWorkingTree_CleanTreeSilent asserts that a clean
// working tree produces no archive ref and no stderr spam. The wrapper
// calls this on every iteration boundary, and the common case is a
// clean tree (agent committed its work before clean-exiting), so silent
// is the right behavior.
//
// Integration-style: builds a real temp git repo, changes cwd to it,
// and runs the helper. Uses t.Chdir for safe restoration and t.TempDir
// for cleanup. Requires `git` on PATH; skipped otherwise.
func TestCaptureDirtyWorkingTree_CleanTreeSilent(t *testing.T) {
	requireGit(t)
	repo := makeTempGitRepo(t, true /*withInitialCommit*/)
	t.Chdir(repo)

	ref := captureDirtyWorkingTree("clean-test-session", 1)
	if ref != "" {
		t.Errorf("clean tree: captureDirtyWorkingTree = %q, want empty (no archive expected)", ref)
	}
}

// TestCaptureDirtyWorkingTree_DirtyTreeWrites_gu_q319 is the direct
// acceptance test for gu-q319 criterion 3: the wrapper preserves dirty
// working-tree state across iteration boundaries. Modifies a tracked
// file, calls the helper, and checks that (a) the expected ref was
// created, (b) the worktree is still dirty afterward (the helper must
// NOT reset the tree — iter N+1's agent needs to see the same files),
// and (c) the snapshot commit actually contains the change.
func TestCaptureDirtyWorkingTree_DirtyTreeWrites_gu_q319(t *testing.T) {
	requireGit(t)
	repo := makeTempGitRepo(t, true)
	t.Chdir(repo)

	// Add an uncommitted change (mirrors what a crashed iter 1 would
	// leave behind — gu-6jgi's 89 lines of docs/otel-data-model.md).
	const payload = "uncommitted line from iter 1\n"
	writeWrapperTestFile(t, filepath.Join(repo, "README.md"), payload)

	ref := captureDirtyWorkingTree("iter-boundary-test", 1)
	if ref == "" {
		t.Fatalf("dirty tree: captureDirtyWorkingTree returned empty, want ref")
	}
	wantRef := "refs/archive/polecat-autosave/iter-boundary-test/iter1-dirty"
	if ref != wantRef {
		t.Errorf("ref name = %q, want %q", ref, wantRef)
	}

	// Worktree must be untouched — the whole point is iter N+1 sees
	// iter N's in-progress state, and the archive is a side channel.
	current := readWrapperTestFile(t, filepath.Join(repo, "README.md"))
	if current != payload {
		t.Errorf("worktree mutated by capture: got %q, want %q (capture must not reset worktree)",
			current, payload)
	}

	// The ref must point at a real commit object whose tree contains
	// the payload — this is what makes the snapshot actually useful
	// for recovery.
	shaOut, err := runWrapperTestGit(t, repo, "rev-parse", ref)
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	sha := strings.TrimSpace(shaOut)
	if sha == "" {
		t.Fatalf("ref %s resolved to empty SHA", ref)
	}
	showOut, err := runWrapperTestGit(t, repo, "show", sha+":README.md")
	if err != nil {
		t.Fatalf("git show %s:README.md: %v", sha, err)
	}
	if showOut != payload {
		t.Errorf("archived README.md = %q, want %q (dirty state not captured)",
			showOut, payload)
	}
}

// TestCaptureDirtyWorkingTree_NotAGitRepoFailsSoft asserts the
// best-effort contract: outside a git repo the helper returns empty
// and does NOT panic or block the wrapper loop.
func TestCaptureDirtyWorkingTree_NotAGitRepoFailsSoft(t *testing.T) {
	requireGit(t)
	nonRepo := t.TempDir()
	t.Chdir(nonRepo)

	// Should just return "" — wrapper continues to the next iteration.
	ref := captureDirtyWorkingTree("some-session", 1)
	if ref != "" {
		t.Errorf("non-git-repo cwd: captureDirtyWorkingTree = %q, want empty", ref)
	}
}

// ---------------------------------------------------------------
// Test helpers for git-based integration tests
//
// These are wrapper-test-local (writeWrapperTestFile, runWrapperTestGit,
// etc.) rather than the generic writeFile/runGit declared in
// orphans_test.go and polecat_test.go. Keeping distinct names avoids
// the "redeclared in this block" collision and makes it obvious in
// stack traces which suite a helper belongs to.
// ---------------------------------------------------------------

// requireGit skips the test if `git` isn't on PATH. The wrapper only
// runs in environments with git anyway (every polecat worktree needs
// it), so it's fine to depend on; but CI sandboxes that don't ship
// git shouldn't fail the whole build.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping git-integration test")
	}
}

// makeTempGitRepo initializes a temp repo suitable for the capture
// tests. Uses `git init -b main`, disables GPG signing, and optionally
// writes + commits an initial file so `git stash create` has a base
// to diff against. Returns the repo's absolute path (t.TempDir-backed,
// auto-cleaned).
func makeTempGitRepo(t *testing.T, withInitialCommit bool) string {
	t.Helper()
	dir := t.TempDir()
	for _, cmd := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "wrapper-test@example.com"},
		{"config", "user.name", "Wrapper Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		if _, err := runWrapperTestGit(t, dir, cmd...); err != nil {
			t.Fatalf("git %v in %s: %v", cmd, dir, err)
		}
	}
	if withInitialCommit {
		writeWrapperTestFile(t, filepath.Join(dir, "README.md"), "initial\n")
		for _, cmd := range [][]string{
			{"add", "README.md"},
			{"commit", "-m", "initial"},
		} {
			if _, err := runWrapperTestGit(t, dir, cmd...); err != nil {
				t.Fatalf("git %v in %s: %v", cmd, dir, err)
			}
		}
	}
	return dir
}

func runWrapperTestGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Pin identity via env so the test doesn't depend on the host's
	// ~/.gitconfig (which may be missing in minimal CI images).
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=wrapper-test",
		"GIT_AUTHOR_EMAIL=wrapper-test@example.com",
		"GIT_COMMITTER_NAME=wrapper-test",
		"GIT_COMMITTER_EMAIL=wrapper-test@example.com",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeWrapperTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readWrapperTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// ---------------------------------------------------------------
// Telemetry emission — runPolecatKiroWrapper end-to-end (gu-6jgi)
//
// These tests exercise the full recovery loop using a fake kiro-cli
// built from this test binary via the classic TestHelperProcess
// pattern. The wrapper's OTel emissions go to a noop provider (no
// telemetry.Init in test context), so assertions are made against
// the town.log mirror — it carries the same state/iteration/duration
// fields and is the durable operator-facing signal required by the
// gu-6jgi acceptance criteria.
//
// Three paths are covered:
//
//   1. iter-1 clean-done       — happy path, no retries
//   2. iter-N recovery (N=3)   — bug hits on iters 1–2, iter 3 succeeds
//   3. iter-max exhaustion     — every iter hits the bug, wrapper gives up
//
// The fake kiro-cli increments a counter file on each invocation and
// branches on GT_FAKE_KIRO_MODE:
//
//   - "exit-clean"   : exit 0, do nothing else (simulates gu-ronb)
//   - "exit-and-done": exit 0, write heartbeat "idle" (simulates gt done)
//   - "nth-done"     : on invocation N write heartbeat "idle" then exit 0;
//                      on earlier invocations just exit 0
//
// The helper process self-identifies via GO_WANT_HELPER_PROCESS — when
// that's set it runs the fake kiro-cli logic and os.Exit's before the
// regular test machinery can run.
// ---------------------------------------------------------------

// TestHelperProcess is the subprocess entry point for the fake
// kiro-cli used by the telemetry tests. It runs only when
// GO_WANT_HELPER_PROCESS=1 is set in the env — otherwise it's a no-op
// during normal test runs. Pattern lifted from exec/exec_test.go and
// used widely in the Go stdlib.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)

	counterPath := os.Getenv("GT_FAKE_KIRO_COUNTER")
	if counterPath == "" {
		os.Exit(0)
	}
	// Increment the invocation counter. Each kiro-cli spawn bumps it
	// by one, so the test can assert both "we were invoked the right
	// number of times" and "which invocation index decided what".
	n := readIntFile(counterPath) + 1
	writeIntFile(counterPath, n)

	mode := os.Getenv("GT_FAKE_KIRO_MODE")
	switch mode {
	case "exit-clean":
		// Pure gu-ronb simulation: clean exit 0 with no heartbeat flip.
		// The wrapper sees clean-exit-mid-task and retries.
		os.Exit(0)
	case "exit-and-done":
		// Simulates the happy path: agent called gt done, heartbeat
		// is flipped to "idle" before exit.
		writeFakeHeartbeatIdle(t)
		os.Exit(0)
	case "nth-done":
		// Recovery simulation: on invocation N flip heartbeat to idle
		// then exit. Earlier invocations just exit clean (gu-ronb).
		target := 0
		if _, err := fmt.Sscanf(os.Getenv("GT_FAKE_KIRO_SUCCESS_ON"), "%d", &target); err != nil || target == 0 {
			target = 2
		}
		if n >= target {
			writeFakeHeartbeatIdle(t)
		}
		os.Exit(0)
	default:
		os.Exit(0)
	}
}

// writeFakeHeartbeatIdle flips the heartbeat file to state="idle" so
// the wrapper's isPolecatDone check returns true and the recovery loop
// terminates cleanly. Uses the same JSON shape as the real
// TouchSessionHeartbeatWithState writer.
func writeFakeHeartbeatIdle(t *testing.T) {
	townRoot := os.Getenv("GT_TOWN_ROOT")
	sess := os.Getenv("GT_SESSION")
	if townRoot == "" || sess == "" {
		return
	}
	hbDir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(hbDir, 0o755); err != nil {
		_ = t // unused in subprocess path, but keep signature stable
		return
	}
	hb := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"state":     "idle",
	}
	data, _ := json.Marshal(hb)
	_ = os.WriteFile(filepath.Join(hbDir, sess+".json"), data, 0o644)
}

func readIntFile(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	_, _ = fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &n)
	return n
}

func writeIntFile(path string, n int) {
	_ = os.WriteFile(path, []byte(fmt.Sprintf("%d\n", n)), 0o644)
}

// wrapperIntegrationCase bundles the fake-kiro-cli config a single
// integration test needs. Each case becomes one runPolecatKiroWrapper
// invocation end-to-end, asserting the resulting town.log records.
type wrapperIntegrationCase struct {
	name       string
	mode       string // GT_FAKE_KIRO_MODE
	successOn  int    // for nth-done, which iter succeeds
	maxIter    string // GT_KIRO_MAX_ITERATIONS override
	wantState  string // expected terminal state label in town.log
	wantInvocs int    // expected fake-kiro spawn count
}

// setupWrapperIntegrationEnv prepares the minimal env + temp dirs a
// wrapper integration case needs. Returns the townRoot (so tests can
// read town.log) and the counter path. All state lives under
// t.TempDir() so cleanup is automatic and tests don't pollute each
// other's heartbeat state.
func setupWrapperIntegrationEnv(t *testing.T, maxIter string) (townRoot, sessName, counterPath string) {
	t.Helper()
	townRoot = t.TempDir()
	sessName = "wrapper-integration-sess"
	counterPath = filepath.Join(t.TempDir(), "invocation-count")

	t.Setenv("GT_TOWN_ROOT", townRoot)
	t.Setenv("GT_SESSION", sessName)
	t.Setenv("GT_FAKE_KIRO_COUNTER", counterPath)
	// Shorten all knobs so the test finishes in sub-second wall time.
	// The per-iter timeout must be long enough that a normal
	// subprocess startup (TestHelperProcess) doesn't race the deadline.
	t.Setenv("GT_KIRO_MAX_ITERATIONS", maxIter)
	t.Setenv("GT_KIRO_ITERATION_TIMEOUT", "5s")
	t.Setenv("GT_KIRO_TOTAL_TIMEOUT", "30s")
	t.Setenv("GT_KIRO_RETRY_BACKOFF", "1ms")
	// Neutralize rig/polecat names so the town.log "agent" field is
	// predictable across test hosts.
	t.Setenv("GT_RIG", "testrig")
	t.Setenv("GT_POLECAT", "testcat")
	return
}

// invokeWrapperWithFakeKiro runs runPolecatKiroWrapper with the current
// test binary masquerading as kiro-cli via TestHelperProcess. Returns
// the error the wrapper returned plus the town.log contents.
func invokeWrapperWithFakeKiro(t *testing.T, townRoot string, mode string, successOn int) (error, string) {
	t.Helper()
	t.Setenv("GT_FAKE_KIRO_MODE", mode)
	if successOn > 0 {
		t.Setenv("GT_FAKE_KIRO_SUCCESS_ON", fmt.Sprintf("%d", successOn))
	} else {
		t.Setenv("GT_FAKE_KIRO_SUCCESS_ON", "")
	}
	// Build the argv the wrapper receives. First arg is the "binary"
	// (this test's own path); subsequent args are what a real
	// polecat preset would send: kiro-cli chat + flags + bootstrap
	// prompt. The prompt is the trailing positional so the gu-q319
	// stripTrailingInput logic has something to strip on iter 2+.
	argv := []string{
		os.Args[0],
		"-test.run=TestHelperProcess",
		"--",
		"chat",
		"--trust-all-tools",
		"--no-interactive",
		"bootstrap prompt",
	}
	// Also export GO_WANT_HELPER_PROCESS so the spawned subprocess
	// knows to run the helper logic instead of the test suite.
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")

	err := runPolecatKiroWrapper(nil, argv)
	// Read town.log (may be absent if townRoot is empty — but all
	// integration cases set it).
	logPath := filepath.Join(townRoot, "logs", "town.log")
	data, _ := os.ReadFile(logPath)
	return err, string(data)
}

// TestKiroWrapperTerminal_CleanDoneOnIter1 exercises the happy path:
// kiro-cli exits 0 on iter 1 AND flips the heartbeat to idle, so the
// wrapper's isPolecatDone check returns true and no retry happens.
// Expected terminal state: "done", iterations=1.
func TestKiroWrapperTerminal_CleanDoneOnIter1(t *testing.T) {
	townRoot, _, counterPath := setupWrapperIntegrationEnv(t, "5")
	err, townLog := invokeWrapperWithFakeKiro(t, townRoot, "exit-and-done", 0)
	if err != nil {
		t.Fatalf("runPolecatKiroWrapper returned unexpected error: %v", err)
	}

	n := readIntFile(counterPath)
	if n != 1 {
		t.Errorf("fake kiro invoked %d times, want 1 (happy path should not retry)", n)
	}

	if !strings.Contains(townLog, "state=done") {
		t.Errorf("town.log missing `state=done` terminal record:\n%s", townLog)
	}
	if !strings.Contains(townLog, "iter=1/5") {
		t.Errorf("town.log should record iter=1/5 for happy path, got:\n%s", townLog)
	}
}

// TestKiroWrapperTerminal_RecoveryAtIter3 exercises the mid-recovery
// path: iters 1–2 hit the gu-ronb bug (clean exit, no gt done), iter 3
// succeeds. Expected terminal state: "done", iterations=3, and two
// "clean_exit_not_done" iteration events preceding the terminal record.
func TestKiroWrapperTerminal_RecoveryAtIter3(t *testing.T) {
	townRoot, _, counterPath := setupWrapperIntegrationEnv(t, "5")
	err, townLog := invokeWrapperWithFakeKiro(t, townRoot, "nth-done", 3)
	if err != nil {
		t.Fatalf("runPolecatKiroWrapper returned unexpected error: %v", err)
	}

	n := readIntFile(counterPath)
	if n != 3 {
		t.Errorf("fake kiro invoked %d times, want 3 (should recover on iter 3)", n)
	}

	// Count per-iteration "clean_exit_not_done" events — must see 2
	// (for the two failed iters before iter 3 succeeds). The wrapper
	// does not emit clean_exit_not_done for the final successful iter.
	cleanExits := strings.Count(townLog, "event=clean_exit_not_done")
	if cleanExits != 2 {
		t.Errorf("town.log has %d `event=clean_exit_not_done` iteration events, want 2\n%s",
			cleanExits, townLog)
	}

	// Terminal record must show iterations=3 and state=done.
	if !strings.Contains(townLog, "state=done") {
		t.Errorf("town.log missing `state=done` terminal record:\n%s", townLog)
	}
	if !strings.Contains(townLog, "iter=3/5") {
		t.Errorf("town.log should record iter=3/5 after recovery, got:\n%s", townLog)
	}

	// Two resume_start events are emitted for iters 2 and 3 (the
	// wrapper emits it right before each --resume spawn).
	resumeStarts := strings.Count(townLog, "event=resume_start")
	if resumeStarts != 2 {
		t.Errorf("town.log has %d `event=resume_start` iteration events, want 2\n%s",
			resumeStarts, townLog)
	}
}

// TestKiroWrapperTerminal_ExhaustionAtIterMax exercises the iteration-
// cap path: every iter hits the gu-ronb bug, wrapper gives up after
// maxIterations. Expected terminal state: "max_iterations",
// iterations=max, and maxIter-1 "clean_exit_not_done" iteration events.
func TestKiroWrapperTerminal_ExhaustionAtIterMax(t *testing.T) {
	const maxIter = 3
	townRoot, _, counterPath := setupWrapperIntegrationEnv(t, fmt.Sprintf("%d", maxIter))
	err, townLog := invokeWrapperWithFakeKiro(t, townRoot, "exit-clean", 0)
	if err != nil {
		t.Fatalf("runPolecatKiroWrapper returned unexpected error: %v (max-iter exhaustion returns nil)", err)
	}

	n := readIntFile(counterPath)
	if n != maxIter {
		t.Errorf("fake kiro invoked %d times, want %d (every iter should fail and retry)", n, maxIter)
	}

	// maxIter "clean_exit_not_done" events — one per failed iter,
	// including the last one (we emit before checking iter < maxIter
	// for the backoff decision).
	cleanExits := strings.Count(townLog, "event=clean_exit_not_done")
	if cleanExits != maxIter {
		t.Errorf("town.log has %d `event=clean_exit_not_done` iteration events, want %d\n%s",
			cleanExits, maxIter, townLog)
	}

	// Terminal record: state=max_iterations, iter=max/max.
	if !strings.Contains(townLog, "state=max_iterations") {
		t.Errorf("town.log missing `state=max_iterations` terminal record:\n%s", townLog)
	}
	expectedIter := fmt.Sprintf("iter=%d/%d", maxIter, maxIter)
	if !strings.Contains(townLog, expectedIter) {
		t.Errorf("town.log should record %s after exhaustion, got:\n%s", expectedIter, townLog)
	}
}

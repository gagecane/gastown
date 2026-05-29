package upstreamsync

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunGates_EmptyList(t *testing.T) {
	summary := RunGates(context.Background(), nil, GateRunOptions{})
	if !summary.AllPassed {
		t.Errorf("AllPassed = false for empty gates, want true")
	}
	if len(summary.Results) != 0 {
		t.Errorf("Results length = %d, want 0", len(summary.Results))
	}
}

func TestRunGates_AllPass(t *testing.T) {
	summary := RunGates(context.Background(), []string{
		"true",
		"echo hi",
		"true",
	}, GateRunOptions{Dir: "."})

	if !summary.AllPassed {
		t.Errorf("AllPassed = false, want true; summary = %+v", summary)
	}
	if len(summary.Results) != 3 {
		t.Fatalf("Results length = %d, want 3", len(summary.Results))
	}
	for i, r := range summary.Results {
		if r.Result != GatePass {
			t.Errorf("Results[%d] = %q, want %q (cmd=%q, err=%v)", i, r.Result, GatePass, r.Command, r.Err)
		}
	}
}

func TestRunGates_StopsOnFirstFailure(t *testing.T) {
	summary := RunGates(context.Background(), []string{
		"true",
		"false",
		"echo should-not-run",
	}, GateRunOptions{Dir: "."})

	if summary.AllPassed {
		t.Errorf("AllPassed = true, want false")
	}
	if summary.FailedCommand != "false" {
		t.Errorf("FailedCommand = %q, want %q", summary.FailedCommand, "false")
	}
	if len(summary.Results) != 3 {
		t.Fatalf("Results length = %d, want 3", len(summary.Results))
	}
	if summary.Results[0].Result != GatePass {
		t.Errorf("Results[0] = %q, want pass", summary.Results[0].Result)
	}
	if summary.Results[1].Result != GateFail {
		t.Errorf("Results[1] = %q, want fail", summary.Results[1].Result)
	}
	if summary.Results[2].Result != GateSkip {
		t.Errorf("Results[2] = %q, want skip", summary.Results[2].Result)
	}

	// Skipped command should NOT have run — its output should be empty.
	if summary.Results[2].Output != "" {
		t.Errorf("skipped command captured output: %q", summary.Results[2].Output)
	}
}

func TestRunGates_CapturesOutput(t *testing.T) {
	summary := RunGates(context.Background(), []string{
		"echo hello world",
	}, GateRunOptions{Dir: "."})

	if !summary.AllPassed {
		t.Fatalf("expected all passed, got %+v", summary)
	}
	out := summary.Results[0].Output
	if !strings.Contains(out, "hello world") {
		t.Errorf("captured output %q does not contain expected substring", out)
	}
}

func TestRunGates_ResultsMap(t *testing.T) {
	summary := RunGates(context.Background(), []string{
		"true",
		"false",
	}, GateRunOptions{Dir: "."})

	m := summary.GateResultsMap()
	if m["true"] != GatePass {
		t.Errorf("map[true] = %q, want pass", m["true"])
	}
	if m["false"] != GateFail {
		t.Errorf("map[false] = %q, want fail", m["false"])
	}
}

func TestRunGates_PerCommandTimeout(t *testing.T) {
	// Use a tiny per-command timeout so `sleep 5` is killed quickly.
	summary := RunGates(context.Background(), []string{
		"sleep 5",
	}, GateRunOptions{
		Dir:               ".",
		PerCommandTimeout: 100 * time.Millisecond,
	})

	if summary.AllPassed {
		t.Errorf("expected timeout to fail the gate, got AllPassed=true")
	}
	if summary.Results[0].Result != GateFail {
		t.Errorf("Results[0] = %q, want fail (timed out)", summary.Results[0].Result)
	}
	// Should have terminated well before the 5s sleep would have completed.
	if summary.Results[0].Duration > 2*time.Second {
		t.Errorf("Duration = %v, want <2s (timeout enforcement broken)", summary.Results[0].Duration)
	}
}

func TestRunGates_OuterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel before any gate runs

	summary := RunGates(ctx, []string{
		"echo first",
		"echo second",
	}, GateRunOptions{Dir: "."})

	if summary.AllPassed {
		t.Errorf("expected cancelled context to fail the suite, got AllPassed=true")
	}
	// Both should be skipped because ctx was cancelled before the loop ran.
	for i, r := range summary.Results {
		if r.Result != GateSkip {
			t.Errorf("Results[%d] = %q, want skip (ctx cancelled)", i, r.Result)
		}
	}
}

func TestTruncateGateOutput_UnderLimit(t *testing.T) {
	short := strings.Repeat("a", 100)
	got := truncateGateOutput(short)
	if got != short {
		t.Errorf("expected unchanged output for short input")
	}
}

func TestTruncateGateOutput_OverLimit(t *testing.T) {
	long := strings.Repeat("xx\n", MaxGateOutputBytes) // ~3 * MaxGateOutputBytes
	got := truncateGateOutput(long)
	if !strings.Contains(got, "[truncated") {
		t.Errorf("expected truncated marker in output, got %q...", got[:min(80, len(got))])
	}
	if len(got) > MaxGateOutputBytes+512 { // marker overhead allowance
		t.Errorf("truncated output length = %d, expected ~%d", len(got), MaxGateOutputBytes)
	}
	// Tail content should be preserved (last "xx\n").
	if !strings.HasSuffix(got, "xx\n") {
		t.Errorf("truncated output did not preserve tail; got suffix %q", got[max(0, len(got)-10):])
	}
}

// TestSandboxFilterEnv_DropsCredentialFamilies verifies that the pure-
// function env scrubber removes credential-bearing prefixes and exact
// names while preserving system vars like PATH/HOME/GOPATH.
//
// Phase 5 (gu-1zfy / C-SEC-1): a malicious upstream commit running
// during `go test` must not see push tokens, AWS creds, or beads/dolt
// session secrets in the inherited env. SandboxFilterEnv is the
// enforcement seam — RunGates calls it when SandboxedEnv=true.
func TestSandboxFilterEnv_DropsCredentialFamilies(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"GOPATH=/home/u/go",
		"GOCACHE=/home/u/.cache/go-build",
		"AWS_ACCESS_KEY_ID=AKIA",       // dropped
		"AWS_SECRET_ACCESS_KEY=secret", // dropped
		"AWS_SESSION_TOKEN=tok",        // dropped
		"GITHUB_TOKEN=ghp_xxx",         // dropped
		"GH_TOKEN=ghp_yyy",             // dropped
		"BD_ACTOR=brahmin",             // dropped
		"DOLT_USER=root",               // dropped
		"GT_DOLT_PORT=3307",            // dropped
		"ANTHROPIC_API_KEY=sk-ant",     // dropped
		"OPENAI_API_KEY=sk-oai",        // dropped
		"NPM_TOKEN=xxx",                // dropped
		"DOCKER_AUTH_CONFIG={}",        // dropped
		"VAULT_TOKEN=hvs.xxx",          // dropped (exact)
		"KUBECONFIG=/x",                // dropped (exact)
		"SSH_AUTH_SOCK=/tmp/ssh.sock",  // dropped (exact)
		"GOFLAGS=-mod=vendor",          // preserved
		"USER=brahmin",                 // preserved (no prefix match)
	}
	got := SandboxFilterEnv(parent)

	want := map[string]bool{
		"PATH=/usr/bin":                   true,
		"HOME=/home/u":                    true,
		"GOPATH=/home/u/go":               true,
		"GOCACHE=/home/u/.cache/go-build": true,
		"GOFLAGS=-mod=vendor":             true,
		"USER=brahmin":                    true,
	}
	mustNotAppear := []string{
		"AWS_", "GITHUB_", "GH_", "BD_", "DOLT_", "GT_DOLT_",
		"ANTHROPIC_", "OPENAI_", "NPM_", "DOCKER_",
		"VAULT_TOKEN=", "KUBECONFIG=", "SSH_AUTH_SOCK=",
	}

	gotSet := make(map[string]bool, len(got))
	for _, kv := range got {
		gotSet[kv] = true
	}

	for w := range want {
		if !gotSet[w] {
			t.Errorf("missing required env entry: %q", w)
		}
	}
	for _, kv := range got {
		for _, deny := range mustNotAppear {
			if strings.HasPrefix(kv, deny) {
				t.Errorf("scrubbed env still contains denied entry: %q (matched prefix %q)", kv, deny)
			}
		}
	}
}

// TestSandboxFilterEnv_DropsMalformedEntries ensures that defensive
// handling of entries without '=' does not crash and silently drops
// them (os.Environ never produces these but a hand-built input might).
func TestSandboxFilterEnv_DropsMalformedEntries(t *testing.T) {
	parent := []string{
		"=value-only",    // empty key — drop
		"key-without-eq", // no '=' — drop
		"PATH=/bin",      // valid — keep
	}
	got := SandboxFilterEnv(parent)
	if len(got) != 1 || got[0] != "PATH=/bin" {
		t.Errorf("got %v, want [PATH=/bin]", got)
	}
}

// TestRunGates_SandboxedEnv_ScrubsParentCredentials runs a real gate
// command (`env`) with a credential-bearing variable seeded into the
// process env, then verifies the captured output does NOT contain it
// when SandboxedEnv=true. This is the end-to-end check that the
// sandbox is wired into runOneGate, not just the pure filter.
func TestRunGates_SandboxedEnv_ScrubsParentCredentials(t *testing.T) {
	// Seed a credential into the parent env. t.Setenv handles the
	// cleanup; safe to use even with t.Parallel sibling tests because
	// it's per-test-process scoped via testing's helper.
	t.Setenv("AWS_SECRET_ACCESS_KEY", "S3CR3T-PHASE5-AUDIT")
	t.Setenv("BD_ACTOR", "brahmin-test")
	t.Setenv("PATH", os.Getenv("PATH")) // keep PATH so /usr/bin/env resolves

	summary := RunGates(context.Background(), []string{
		// `env` prints the inherited environment. If sandboxing is on,
		// the seeded secrets must not appear.
		"env",
	}, GateRunOptions{
		Dir:          ".",
		SandboxedEnv: true,
	})

	if !summary.AllPassed {
		t.Fatalf("gate failed unexpectedly: %+v", summary)
	}
	out := summary.Results[0].Output
	if strings.Contains(out, "S3CR3T-PHASE5-AUDIT") {
		t.Errorf("sandboxed gate leaked AWS_SECRET_ACCESS_KEY into child env:\n%s", out)
	}
	if strings.Contains(out, "brahmin-test") && strings.Contains(out, "BD_ACTOR") {
		t.Errorf("sandboxed gate leaked BD_ACTOR into child env:\n%s", out)
	}
	// PATH must survive — without it, the shell can't find subsequent commands.
	if !strings.Contains(out, "PATH=") {
		t.Errorf("sandboxed gate dropped PATH; child env has no command resolution")
	}
}

// TestRunGates_NotSandboxed_PreservesParentEnv verifies the legacy
// path still inherits the full parent env. Important: existing callers
// (Refinery's gate runner, manual operator-driven sync) rely on this.
func TestRunGates_NotSandboxed_PreservesParentEnv(t *testing.T) {
	t.Setenv("BRAHMIN_PHASE5_PROBE", "VISIBLE")

	summary := RunGates(context.Background(), []string{
		"env",
	}, GateRunOptions{
		Dir:          ".",
		SandboxedEnv: false,
	})
	if !summary.AllPassed {
		t.Fatalf("gate failed unexpectedly: %+v", summary)
	}
	if !strings.Contains(summary.Results[0].Output, "BRAHMIN_PHASE5_PROBE=VISIBLE") {
		t.Errorf("non-sandboxed gate did not inherit parent env probe; output:\n%s", summary.Results[0].Output)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

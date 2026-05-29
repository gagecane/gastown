package upstreamsync

import (
	"context"
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

package refinery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/rig"
)

func writeChecksGateConfig(t *testing.T, mq map[string]interface{}) *Engineer {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := map[string]interface{}{
		"type":        "rig",
		"version":     1,
		"name":        "test-rig",
		"merge_queue": mq,
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	return NewEngineer(&rig.Rig{Name: "test-rig", Path: tmpDir})
}

func TestLoadConfig_GitHubChecksGate_Valid(t *testing.T) {
	e := writeChecksGateConfig(t, map[string]interface{}{
		"enabled":        true,
		"merge_strategy": "pr",
		"vcs_provider":   "github",
		"gates": map[string]interface{}{
			"ci": map[string]interface{}{"type": "github-checks", "required_only": true, "timeout": "20m"},
		},
	})
	if err := e.LoadConfig(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gc := e.config.Gates["ci"]
	if gc == nil || gc.Type != gateTypeGitHubChecks {
		t.Fatalf("expected github-checks gate, got %+v", gc)
	}
	if !gc.RequiredOnly {
		t.Error("expected RequiredOnly true")
	}
	if gc.Timeout != 20*time.Minute {
		t.Errorf("expected timeout 20m, got %v", gc.Timeout)
	}
}

func TestLoadConfig_GitHubChecksGate_RejectsNonPRStrategy(t *testing.T) {
	e := writeChecksGateConfig(t, map[string]interface{}{
		"enabled": true,
		"gates": map[string]interface{}{
			"ci": map[string]interface{}{"type": "github-checks"},
		},
	})
	err := e.LoadConfig()
	if err == nil || !strings.Contains(err.Error(), "merge_strategy=pr") {
		t.Errorf("expected merge_strategy error, got %v", err)
	}
}

func TestLoadConfig_GitHubChecksGate_RejectsNonGitHubProvider(t *testing.T) {
	e := writeChecksGateConfig(t, map[string]interface{}{
		"enabled":        true,
		"merge_strategy": "pr",
		"vcs_provider":   "bitbucket",
		"gates": map[string]interface{}{
			"ci": map[string]interface{}{"type": "github-checks"},
		},
	})
	err := e.LoadConfig()
	if err == nil || !strings.Contains(err.Error(), "vcs_provider=github") {
		t.Errorf("expected vcs_provider error, got %v", err)
	}
}

func TestLoadConfig_RejectsUnknownGateType(t *testing.T) {
	e := writeChecksGateConfig(t, map[string]interface{}{
		"enabled": true,
		"gates": map[string]interface{}{
			"ci": map[string]interface{}{"type": "bogus"},
		},
	})
	err := e.LoadConfig()
	if err == nil || !strings.Contains(err.Error(), "invalid type") {
		t.Errorf("expected invalid type error, got %v", err)
	}
}

// fakeChecksProvider implements PRProvider + prChecksProvider for testing the
// github-checks gate (gs-vlyt). Each call to GetPRChecks returns the next entry
// in checks (the last entry repeats), so a test can model a PR transitioning
// pending -> green/red across polls.
type fakeChecksProvider struct {
	prNumber     int
	findErr      error
	checks       [][]PRCheck
	getErr       error
	calls        int
	lastRequired bool
}

func (f *fakeChecksProvider) FindPRNumber(string) (int, error) { return f.prNumber, f.findErr }
func (f *fakeChecksProvider) IsPRApproved(int) (bool, error)   { return true, nil }
func (f *fakeChecksProvider) MergePR(int, string) (string, error) {
	return "", nil
}

func (f *fakeChecksProvider) GetPRChecks(_ int, requiredOnly bool) ([]PRCheck, error) {
	f.lastRequired = requiredOnly
	if f.getErr != nil {
		return nil, f.getErr
	}
	idx := f.calls
	if idx >= len(f.checks) {
		idx = len(f.checks) - 1
	}
	f.calls++
	if len(f.checks) == 0 {
		return nil, nil
	}
	return f.checks[idx], nil
}

func newChecksGateEngineer(t *testing.T, p PRProvider) *Engineer {
	t.Helper()
	e := NewEngineer(&rig.Rig{Name: "test-rig", Path: t.TempDir()})
	e.workDir = t.TempDir()
	e.prProvider = p
	e.prChecksPollInterval = time.Millisecond // keep the poll loop fast
	return e
}

func TestSummarizeChecks(t *testing.T) {
	tests := []struct {
		name   string
		checks []PRCheck
		want   checksVerdict
	}{
		{"empty is pending", nil, checksPending},
		{"all pass", []PRCheck{{Name: "ci", Bucket: "pass"}, {Name: "lint", Bucket: "skipping"}}, checksPassed},
		{"any fail fails", []PRCheck{{Name: "ci", Bucket: "pass"}, {Name: "test", Bucket: "fail"}}, checksFailed},
		{"cancel fails", []PRCheck{{Name: "ci", Bucket: "cancel"}}, checksFailed},
		{"pending while others pass", []PRCheck{{Name: "ci", Bucket: "pass"}, {Name: "test", Bucket: "pending"}}, checksPending},
		{"fail wins over pending", []PRCheck{{Name: "a", Bucket: "pending"}, {Name: "b", Bucket: "fail"}}, checksFailed},
		{"bucket case-insensitive", []PRCheck{{Name: "ci", Bucket: "PASS"}}, checksPassed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := summarizeChecks(tc.checks)
			if got != tc.want {
				t.Errorf("summarizeChecks() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGitHubChecksGate_Passes(t *testing.T) {
	p := &fakeChecksProvider{prNumber: 12, checks: [][]PRCheck{
		{{Name: "ci", Bucket: "pending"}},
		{{Name: "ci", Bucket: "pass"}},
	}}
	e := newChecksGateEngineer(t, p)

	res := e.runGitHubChecksGate(context.Background(), "checks", &GateConfig{Type: gateTypeGitHubChecks}, "feat/x")
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if p.calls < 2 {
		t.Errorf("expected the gate to poll past the pending state, calls=%d", p.calls)
	}
}

func TestGitHubChecksGate_FailsOnRedCheck(t *testing.T) {
	p := &fakeChecksProvider{prNumber: 5, checks: [][]PRCheck{
		{{Name: "test", Bucket: "fail"}},
	}}
	e := newChecksGateEngineer(t, p)

	res := e.runGitHubChecksGate(context.Background(), "checks", &GateConfig{Type: gateTypeGitHubChecks}, "feat/x")
	if res.Success {
		t.Fatal("expected failure on red check")
	}
	if !strings.Contains(res.Error, "test") {
		t.Errorf("expected failing check name in error, got: %s", res.Error)
	}
}

func TestGitHubChecksGate_TimesOutWhilePending(t *testing.T) {
	p := &fakeChecksProvider{prNumber: 9, checks: [][]PRCheck{
		{{Name: "ci", Bucket: "pending"}},
	}}
	e := newChecksGateEngineer(t, p)

	res := e.runGitHubChecksGate(context.Background(), "checks", &GateConfig{
		Type:    gateTypeGitHubChecks,
		Timeout: 20 * time.Millisecond,
	}, "feat/x")
	if res.Success {
		t.Fatal("expected timeout failure")
	}
	if !strings.Contains(res.Error, "timed out") {
		t.Errorf("expected timeout error, got: %s", res.Error)
	}
}

func TestGitHubChecksGate_NoPR(t *testing.T) {
	p := &fakeChecksProvider{prNumber: 0}
	e := newChecksGateEngineer(t, p)

	res := e.runGitHubChecksGate(context.Background(), "checks", &GateConfig{Type: gateTypeGitHubChecks}, "feat/x")
	if res.Success {
		t.Fatal("expected failure when no PR exists")
	}
	if !strings.Contains(res.Error, "no open PR") {
		t.Errorf("expected no-PR error, got: %s", res.Error)
	}
}

func TestGitHubChecksGate_EmptyBranch(t *testing.T) {
	e := newChecksGateEngineer(t, &fakeChecksProvider{prNumber: 1})
	res := e.runGitHubChecksGate(context.Background(), "checks", &GateConfig{Type: gateTypeGitHubChecks}, "")
	if res.Success || !strings.Contains(res.Error, "branch context") {
		t.Errorf("expected branch-context error, got success=%v error=%s", res.Success, res.Error)
	}
}

func TestGitHubChecksGate_RequiredOnlyPassedThrough(t *testing.T) {
	p := &fakeChecksProvider{prNumber: 3, checks: [][]PRCheck{{{Name: "ci", Bucket: "pass"}}}}
	e := newChecksGateEngineer(t, p)

	e.runGitHubChecksGate(context.Background(), "checks", &GateConfig{
		Type:         gateTypeGitHubChecks,
		RequiredOnly: true,
	}, "feat/x")
	if !p.lastRequired {
		t.Error("expected RequiredOnly to be passed through to GetPRChecks")
	}
}

func TestGitHubChecksGate_ProviderWithoutCapability(t *testing.T) {
	// A non-GitHub provider lacking prChecksProvider must fail with a clear error.
	e := newChecksGateEngineer(t, &fakeMergedPRProvider{})
	res := e.runGitHubChecksGate(context.Background(), "checks", &GateConfig{Type: gateTypeGitHubChecks}, "feat/x")
	if res.Success || !strings.Contains(res.Error, "GitHub PR provider") {
		t.Errorf("expected provider-capability error, got success=%v error=%s", res.Success, res.Error)
	}
}

func TestGitHubChecksGate_GetChecksError(t *testing.T) {
	p := &fakeChecksProvider{prNumber: 4, getErr: errors.New("gh exploded")}
	e := newChecksGateEngineer(t, p)
	res := e.runGitHubChecksGate(context.Background(), "checks", &GateConfig{Type: gateTypeGitHubChecks}, "feat/x")
	if res.Success || !strings.Contains(res.Error, "gh exploded") {
		t.Errorf("expected query error, got success=%v error=%s", res.Success, res.Error)
	}
}

// runGate must dispatch a github-checks gate to the polling path rather than
// trying to exec an empty shell command.
func TestRunGate_DispatchesGitHubChecks(t *testing.T) {
	p := &fakeChecksProvider{prNumber: 8, checks: [][]PRCheck{{{Name: "ci", Bucket: "pass"}}}}
	e := newChecksGateEngineer(t, p)
	res := e.runGate(context.Background(), "checks", &GateConfig{Type: gateTypeGitHubChecks}, "feat/x")
	if !res.Success {
		t.Fatalf("expected github-checks dispatch to pass, got: %s", res.Error)
	}
}

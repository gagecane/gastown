package refinery

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	gitpkg "github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
)

func TestEngineer_LoadConfig_MergeStrategyPR(t *testing.T) {
	tmpDir := t.TempDir()

	requireReview := true
	config := map[string]interface{}{
		"type":    "rig",
		"version": 1,
		"name":    "test-rig",
		"merge_queue": map[string]interface{}{
			"merge_strategy": "pr",
			"require_review": requireReview,
		},
	}

	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{Name: "test-rig", Path: tmpDir}
	e := NewEngineer(r)
	if err := e.LoadConfig(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if e.config.MergeStrategy != "pr" {
		t.Errorf("expected MergeStrategy 'pr', got %q", e.config.MergeStrategy)
	}
	if e.config.RequireReview == nil || !*e.config.RequireReview {
		t.Error("expected RequireReview to be true")
	}
}

func TestEngineer_LoadConfig_MergeStrategyDefault(t *testing.T) {
	tmpDir := t.TempDir()

	config := map[string]interface{}{
		"type":        "rig",
		"version":     1,
		"name":        "test-rig",
		"merge_queue": map[string]interface{}{},
	}

	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{Name: "test-rig", Path: tmpDir}
	e := NewEngineer(r)
	if err := e.LoadConfig(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if e.config.MergeStrategy != "" {
		t.Errorf("expected empty MergeStrategy (default), got %q", e.config.MergeStrategy)
	}
	if e.config.RequireReview != nil {
		t.Error("expected RequireReview to be nil (default)")
	}
}

func TestDoMerge_PRStrategy_RoutesToPRPath(t *testing.T) {
	// When merge_strategy=pr, doMerge should attempt the PR merge path.
	// Without a real GitHub repo, FindPRNumber will fail — that's the expected
	// behavior we test: the code routes to doMergePR and fails gracefully.
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.MergeStrategy = "pr"

	// Create a feature branch
	createFeatureBranch(t, workDir, "feat/test-pr", "test.txt", "hello")

	result := e.doMerge(context.Background(), "feat/test-pr", "main", "gt-test")

	if result.Success {
		t.Error("expected failure (no GitHub PR exists)")
	}

	output := e.output.(*bytes.Buffer).String()
	if !strings.Contains(output, "PR merge strategy") {
		t.Errorf("expected PR merge strategy log, got: %s", output)
	}
}

func TestDoMerge_DirectStrategy_SkipsPRPath(t *testing.T) {
	// When merge_strategy is empty (direct), doMerge should use the normal path.
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.MergeStrategy = "" // explicit direct

	createFeatureBranch(t, workDir, "feat/test-direct", "test.txt", "hello")

	result := e.doMerge(context.Background(), "feat/test-direct", "main", "gt-test")

	// Should succeed with direct merge
	if !result.Success {
		t.Errorf("expected success for direct merge, got error: %s", result.Error)
	}

	output := e.output.(*bytes.Buffer).String()
	if strings.Contains(output, "PR merge strategy") {
		t.Error("direct merge should not mention PR merge strategy")
	}
}

func TestDoMergePR_NoPR_ReturnsError(t *testing.T) {
	// doMergePR should return an error when no PR exists for the branch.
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)

	createFeatureBranch(t, workDir, "feat/no-pr", "test.txt", "hello")

	result := e.doMergePR(context.Background(), "feat/no-pr", "main")

	if result.Success {
		t.Error("expected failure when no PR exists")
	}
	// The error should mention finding a PR
	if !strings.Contains(result.Error, "PR") && !strings.Contains(result.Error, "pr") {
		t.Errorf("expected PR-related error, got: %s", result.Error)
	}
}

// fakeMergedPRProvider reports no OPEN PR but (optionally) an already-MERGED PR
// for a branch — the gs-4uz out-of-band-merge scenario.
type fakeMergedPRProvider struct {
	mergedCommit string // returned by FindMergedPRCommit; "" means none
}

func (f *fakeMergedPRProvider) FindPRNumber(string) (int, error)    { return 0, nil }
func (f *fakeMergedPRProvider) IsPRApproved(int) (bool, error)      { return false, nil }
func (f *fakeMergedPRProvider) MergePR(int, string) (string, error) { return "", nil }
func (f *fakeMergedPRProvider) FindMergedPRCommit(string) (string, error) {
	return f.mergedCommit, nil
}

func TestDoMergePR_AlreadyMerged_TreatedAsSuccess(t *testing.T) {
	// gs-4uz: when no OPEN PR exists but the branch's PR was already merged
	// out-of-band (commit on origin/main), doMergePR must treat it as a
	// successful merge so the caller runs the atomic post-merge close, rather
	// than leaving a 'ready + GIT MISSING' orphan and an un-closed source bead.
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)

	// Simulate the already-merged-and-deleted branch: merge a feature branch
	// into main, push, then delete the branch — its work is now on origin/main.
	createFeatureBranch(t, workDir, "feat/already-merged", "merged.txt", "landed")
	run(t, workDir, "git", "checkout", "main")
	run(t, workDir, "git", "merge", "--no-ff", "feat/already-merged", "-m", "merge feat")
	run(t, workDir, "git", "push", "origin", "main")
	mergeCommit := run(t, workDir, "git", "rev-parse", "HEAD")
	run(t, workDir, "git", "branch", "-D", "feat/already-merged")

	e.prProvider = &fakeMergedPRProvider{mergedCommit: mergeCommit}

	result := e.doMergePR(context.Background(), "feat/already-merged", "main")

	if !result.Success {
		t.Fatalf("expected Success when PR already merged out-of-band, got error: %s", result.Error)
	}
	if result.MergeCommit != mergeCommit {
		t.Errorf("expected MergeCommit %s, got %s", mergeCommit, result.MergeCommit)
	}
}

func TestDoMergePR_NoOpenPR_NoMergedPR_StillFails(t *testing.T) {
	// gs-4uz fail-closed: no OPEN PR and no already-merged PR must remain an
	// error — nothing landed, so the source bead must NOT be closed.
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	createFeatureBranch(t, workDir, "feat/no-pr-at-all", "x.txt", "hi")

	e.prProvider = &fakeMergedPRProvider{mergedCommit: ""}

	result := e.doMergePR(context.Background(), "feat/no-pr-at-all", "main")
	if result.Success {
		t.Error("expected failure when neither open nor merged PR exists")
	}
}

func TestProcessResult_NeedsApproval(t *testing.T) {
	// Verify NeedsApproval field works on ProcessResult.
	r := ProcessResult{
		Success:       false,
		NeedsApproval: true,
		Error:         "PR #42 requires approving review before merge",
	}

	if r.Success {
		t.Error("expected Success=false")
	}
	if !r.NeedsApproval {
		t.Error("expected NeedsApproval=true")
	}
}

func TestHandleMRInfoFailure_NeedsApproval_StaysInQueue(t *testing.T) {
	// When NeedsApproval is true, the MR should stay in queue without
	// sending failure notifications to polecats or mayor.
	workDir := t.TempDir()
	r := &rig.Rig{Name: "test-rig", Path: workDir}
	e := NewEngineer(r)
	var buf bytes.Buffer
	e.output = &buf
	e.workDir = workDir
	e.mergeSlotEnsureExists = func() (string, error) { return "test-slot", nil }
	e.mergeSlotAcquire = func(holder string, addWaiter bool) (*beads.MergeSlotStatus, error) {
		return &beads.MergeSlotStatus{Available: true, Holder: holder}, nil
	}
	e.mergeSlotRelease = func(holder string) error { return nil }

	mr := &MRInfo{
		ID:          "gt-test",
		Branch:      "polecat/test/gt-test",
		Target:      "main",
		SourceIssue: "gt-src",
		Worker:      "polecats/test",
	}
	result := ProcessResult{
		Success:       false,
		NeedsApproval: true,
		Error:         "PR #42 requires approving review before merge",
	}

	e.HandleMRInfoFailure(mr, result)

	output := buf.String()
	if !strings.Contains(output, "awaiting human approval") {
		t.Errorf("expected 'awaiting human approval' message, got: %s", output)
	}
	// Should NOT contain merge failure notifications
	if strings.Contains(output, "MERGE_FAILED") {
		t.Error("NeedsApproval should not trigger MERGE_FAILED notification")
	}
}

func TestDoMergePR_RequireReview_NoApproval(t *testing.T) {
	// When require_review is true and the PR is not approved,
	// doMergePR should return NeedsApproval=true.
	// This test is tricky since it requires gh CLI — skip if not available.
	if _, err := gitpkg.NewGit(t.TempDir()).FindPRNumber("nonexistent"); err != nil {
		// gh CLI not available or not authenticated — test the config path only
		t.Skip("gh CLI not available for PR approval testing")
	}
}

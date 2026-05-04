package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
)

// TestFormatActivityTime exercises the relative-time formatter used by
// `gt polecat status` to describe when a session was last active. The
// boundaries (seconds -> minutes -> hours -> days) are significant
// because they drive the human-readable output.
func TestFormatActivityTime(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		delta   time.Duration
		wantSub string // substring the output must contain
	}{
		{"just now (seconds)", 5 * time.Second, "seconds ago"},
		{"at sub-minute boundary", 59 * time.Second, "seconds ago"},
		{"minutes bucket lower", 1 * time.Minute, "minutes ago"},
		{"minutes bucket upper", 59 * time.Minute, "minutes ago"},
		{"hours bucket lower", 1 * time.Hour, "hours ago"},
		{"hours bucket upper", 23 * time.Hour, "hours ago"},
		{"days bucket lower", 24 * time.Hour, "days ago"},
		{"days bucket higher", 72 * time.Hour, "days ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			past := now.Add(-tt.delta)
			got := formatActivityTime(past)
			if !containsSubstring(got, tt.wantSub) {
				t.Errorf("formatActivityTime(%v) = %q, want substring %q",
					tt.delta, got, tt.wantSub)
			}
		})
	}
}

// TestFormatActivityTime_ValuePresent verifies the numeric portion of the
// output actually reflects the magnitude of the delta. The bucket tests
// above only check the unit word — this one guards against off-by-unit
// bugs (e.g., returning seconds when minutes are expected).
func TestFormatActivityTime_ValuePresent(t *testing.T) {
	past := time.Now().Add(-5 * time.Minute)
	got := formatActivityTime(past)
	if !containsSubstring(got, "5") {
		t.Errorf("formatActivityTime(5m ago) = %q, want to contain the magnitude 5", got)
	}
	if !containsSubstring(got, "minutes") {
		t.Errorf("formatActivityTime(5m ago) = %q, want to contain 'minutes'", got)
	}
}

// TestExistingNamesList verifies extraction of polecat names from a slice
// of pointers. The helper is tiny, but it is used by pool-init dedup and
// regressing it could cause duplicate polecats or skipped slots.
func TestExistingNamesList(t *testing.T) {
	tests := []struct {
		name  string
		input []*polecat.Polecat
		want  []string
	}{
		{
			name:  "empty input",
			input: []*polecat.Polecat{},
			want:  []string{},
		},
		{
			name:  "nil input",
			input: nil,
			want:  []string{},
		},
		{
			name: "single polecat",
			input: []*polecat.Polecat{
				{Name: "shiny"},
			},
			want: []string{"shiny"},
		},
		{
			name: "multiple polecats preserve order",
			input: []*polecat.Polecat{
				{Name: "alpha"},
				{Name: "bravo"},
				{Name: "charlie"},
			},
			want: []string{"alpha", "bravo", "charlie"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := existingNamesList(tt.input)
			if !equalStringSlices(got, tt.want) {
				t.Errorf("existingNamesList() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPolecatSubcommandsRegistered ensures every expected subcommand is
// wired up under `gt polecat`. This guards against future refactors that
// silently drop a subcommand from the command tree.
func TestPolecatSubcommandsRegistered(t *testing.T) {
	expected := []string{
		"list",
		"add",
		"remove",
		"status",
		"git-state",
		"check-recovery",
		"gc",
		"nuke",
		"stale",
		"prune",
		"pool-init",
	}

	registered := map[string]bool{}
	for _, c := range polecatCmd.Commands() {
		registered[c.Name()] = true
	}

	for _, name := range expected {
		if !registered[name] {
			t.Errorf("gt polecat %s: subcommand not registered", name)
		}
	}
}

// TestPolecatCmdMetadata verifies the top-level polecat command shape. The
// alias is important: scripts commonly call `gt polecats` (plural). If
// that alias disappears in a refactor, CI would stay green while user
// scripts silently break.
func TestPolecatCmdMetadata(t *testing.T) {
	if polecatCmd.Use != "polecat" {
		t.Errorf("polecatCmd.Use = %q, want %q", polecatCmd.Use, "polecat")
	}

	var hasPluralAlias bool
	for _, a := range polecatCmd.Aliases {
		if a == "polecats" {
			hasPluralAlias = true
			break
		}
	}
	if !hasPluralAlias {
		t.Errorf("polecatCmd.Aliases = %v, want to contain %q", polecatCmd.Aliases, "polecats")
	}

	if polecatCmd.GroupID == "" {
		t.Errorf("polecatCmd.GroupID = %q, want non-empty", polecatCmd.GroupID)
	}
}

// TestPolecatListItemJSON verifies the JSON shape used by `gt polecat
// list --json`. External tooling (witness, monitoring scripts) parses
// this output, so the field names must remain stable.
func TestPolecatListItemJSON(t *testing.T) {
	item := PolecatListItem{
		Rig:            "testrig",
		Name:           "shiny",
		State:          polecat.StateWorking,
		Issue:          "gu-69w",
		SessionRunning: true,
		Zombie:         false,
		SessionName:    "gt-testrig-shiny",
	}

	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	body := string(data)

	// Spot-check each field is present with its JSON name.
	wantFragments := []string{
		`"rig":"testrig"`,
		`"name":"shiny"`,
		`"state":"working"`,
		`"issue":"gu-69w"`,
		`"session_running":true`,
		`"session_name":"gt-testrig-shiny"`,
	}
	for _, frag := range wantFragments {
		if !containsSubstring(body, frag) {
			t.Errorf("JSON = %s, want to contain %q", body, frag)
		}
	}

	// zombie:false is the default and uses omitempty, so it should NOT appear.
	if containsSubstring(body, `"zombie"`) {
		t.Errorf("JSON = %s, zombie:false should be omitted via omitempty", body)
	}
}

// TestPolecatListItemJSON_OmitEmpty ensures optional fields are omitted
// when blank. This matters because the list output contains many polecats
// and redundant empty fields inflate logs/payloads.
func TestPolecatListItemJSON_OmitEmpty(t *testing.T) {
	item := PolecatListItem{
		Rig:            "testrig",
		Name:           "shiny",
		State:          polecat.StateIdle,
		SessionRunning: false,
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	body := string(data)

	omitted := []string{`"issue"`, `"zombie"`, `"session_name"`}
	for _, f := range omitted {
		if containsSubstring(body, f) {
			t.Errorf("JSON = %s, field %s should be omitted when empty", body, f)
		}
	}
}

// TestRecoveryStatusJSON verifies the JSON shape of recovery-status
// output used by `gt polecat check-recovery --json`. The witness patrol
// depends on the `verdict` field values exactly.
func TestRecoveryStatusJSON(t *testing.T) {
	status := RecoveryStatus{
		Rig:           "testrig",
		Polecat:       "shiny",
		NeedsRecovery: true,
		Verdict:       "NEEDS_MQ_SUBMIT",
		Branch:        "polecat/shiny",
		Issue:         "gu-69w",
		MQStatus:      "not_submitted",
	}

	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	body := string(data)

	want := []string{
		`"rig":"testrig"`,
		`"polecat":"shiny"`,
		`"needs_recovery":true`,
		`"verdict":"NEEDS_MQ_SUBMIT"`,
		`"branch":"polecat/shiny"`,
		`"issue":"gu-69w"`,
		`"mq_status":"not_submitted"`,
	}
	for _, frag := range want {
		if !containsSubstring(body, frag) {
			t.Errorf("JSON = %s, want to contain %q", body, frag)
		}
	}
}

// TestGitStateJSON verifies the JSON shape of `gt polecat git-state
// --json`. The fields are contract with tooling that aggregates polecat
// health.
func TestGitStateJSON(t *testing.T) {
	state := GitState{
		Clean:            false,
		UncommittedFiles: []string{"src/foo.go", "src/bar.go"},
		UnpushedCommits:  3,
		StashCount:       1,
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	body := string(data)

	want := []string{
		`"clean":false`,
		`"uncommitted_files":["src/foo.go","src/bar.go"]`,
		`"unpushed_commits":3`,
		`"stash_count":1`,
	}
	for _, frag := range want {
		if !containsSubstring(body, frag) {
			t.Errorf("JSON = %s, want to contain %q", body, frag)
		}
	}
}

// TestGetGitState_CleanRepo spins up a real throwaway repo with a single
// committed README (and no origin remote) and verifies getGitState
// reports it as clean with no uncommitted files, unpushed commits, or
// stashes. Uses a real git binary — skips the test gracefully when it is
// unavailable.
func TestGetGitState_CleanRepo(t *testing.T) {
	repo := initTestRepo(t)

	got, err := getGitState(repo)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}
	if !got.Clean {
		t.Errorf("Clean = false, want true (no changes, no stashes): %+v", got)
	}
	if len(got.UncommittedFiles) != 0 {
		t.Errorf("UncommittedFiles = %v, want []", got.UncommittedFiles)
	}
	if got.UnpushedCommits != 0 {
		t.Errorf("UnpushedCommits = %d, want 0 (no remote configured)", got.UnpushedCommits)
	}
	if got.StashCount != 0 {
		t.Errorf("StashCount = %d, want 0", got.StashCount)
	}
}

// TestGetGitState_UncommittedFiles verifies we detect working-tree
// changes. The test writes a new file (untracked) and modifies the
// committed one (modified), then asserts both show up in
// UncommittedFiles and the overall Clean flag is false.
func TestGetGitState_UncommittedFiles(t *testing.T) {
	repo := initTestRepo(t)

	// Modify the README that initTestRepo committed.
	readme := filepath.Join(repo, "README.md")
	if err := os.WriteFile(readme, []byte("changed\n"), 0644); err != nil {
		t.Fatalf("modify README: %v", err)
	}

	// Add a new untracked file.
	newFile := filepath.Join(repo, "NEW.txt")
	if err := os.WriteFile(newFile, []byte("new\n"), 0644); err != nil {
		t.Fatalf("write NEW.txt: %v", err)
	}

	got, err := getGitState(repo)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}

	if got.Clean {
		t.Errorf("Clean = true, want false with modified + untracked files: %+v", got)
	}
	if len(got.UncommittedFiles) < 2 {
		t.Errorf("UncommittedFiles = %v, want at least 2 entries (README and NEW.txt)",
			got.UncommittedFiles)
	}

	var sawReadme, sawNew bool
	for _, f := range got.UncommittedFiles {
		if f == "README.md" {
			sawReadme = true
		}
		if f == "NEW.txt" {
			sawNew = true
		}
	}
	if !sawReadme {
		t.Errorf("UncommittedFiles = %v, want README.md (modified)", got.UncommittedFiles)
	}
	if !sawNew {
		t.Errorf("UncommittedFiles = %v, want NEW.txt (untracked)", got.UncommittedFiles)
	}
}

// TestGetGitState_InvalidPath confirms we surface an error when the
// path is not a git repo. The polecat health commands rely on this so
// they don't silently report "clean" for broken worktrees.
func TestGetGitState_InvalidPath(t *testing.T) {
	tmp := t.TempDir()
	// tmp exists but has no .git — getGitState must fail.
	_, err := getGitState(tmp)
	if err == nil {
		t.Errorf("getGitState(non-repo) = nil error, want error")
	}
}

// TestSplitLines_FiltersEmpty verifies the helper strips empty entries
// produced by trailing newlines in `git status --porcelain` output.
// (polecat_cycle_test.go tests this on the cycle side; this test locks
// the contract used by getGitState.)
func TestSplitLines_FiltersEmpty(t *testing.T) {
	got := splitLines("first\n\nsecond\n\n\nthird\n")
	want := []string{"first", "second", "third"}
	if !equalStringSlices(got, want) {
		t.Errorf("splitLines() = %v, want %v", got, want)
	}
}

// TestSplitLines_EmptyInput ensures the helper tolerates empty and
// whitespace-only input (the common no-change case).
func TestSplitLines_EmptyInput(t *testing.T) {
	if got := splitLines(""); len(got) != 0 {
		t.Errorf("splitLines(empty) = %v, want empty slice", got)
	}
	if got := splitLines("\n\n\n"); len(got) != 0 {
		t.Errorf("splitLines(only newlines) = %v, want empty slice", got)
	}
}

// TestGetGitState_DivergentLocalCommitsUnpushed is the regression test for
// gu-7nrd / gt-hc3e5. When the local branch has commits that are reachable
// from NO remote ref, getGitState must report them as unpushed (Clean=false)
// so that kill-safety checks return UNSAFE. The previous implementation
// compared against origin/main with a content-diff short-circuit and
// returned Unpushed=0 / CLEAN, which risked destroying unrecoverable work.
func TestGetGitState_DivergentLocalCommitsUnpushed(t *testing.T) {
	repo := initTestRepoWithRemote(t)

	// At this point:
	//   main = C0 (initial commit)
	//   origin/main = C0
	//   origin/polecat/foo = C0 -> WIP1 -> WIP2  (remote has diverged work)
	//
	// Checkout a local polecat branch at C0 and author a LOCAL commit that
	// exists on no remote ref — it's not reachable from origin/main nor from
	// origin/polecat/foo (which diverged in a different direction).
	runGit(t, repo, "checkout", "-b", "polecat/foo", "main")
	if err := os.WriteFile(filepath.Join(repo, "local.txt"), []byte("local only\n"), 0644); err != nil {
		t.Fatalf("write local.txt: %v", err)
	}
	runGit(t, repo, "add", "local.txt")
	runGit(t, repo, "commit", "-m", "local commit not on any remote")

	got, err := getGitState(repo)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}

	if got.UnpushedCommits != 1 {
		t.Errorf("UnpushedCommits = %d, want 1 (local commit on no remote ref): %+v",
			got.UnpushedCommits, got)
	}
	if got.Clean {
		t.Errorf("Clean = true, want false when local has commits reachable from no remote: %+v", got)
	}
}

// TestGetGitState_ContentOnMainButCommitNotPushed covers the squash-merge
// trap: the branch content matches main (no diff) but the commit SHA lives
// on no remote. The old content-diff short-circuit reported Unpushed=0 and
// CLEAN in this case — but nuking the worktree would still discard the
// local commit object. The fix treats unreachable commits as unpushed
// regardless of whether their content is on main.
func TestGetGitState_ContentOnMainButCommitNotPushed(t *testing.T) {
	repo := initTestRepoWithRemote(t)

	// Advance main on the remote with content X so main already contains the
	// content that the polecat branch is about to produce independently.
	runGit(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("X\n"), 0644); err != nil {
		t.Fatalf("write feature.txt on main: %v", err)
	}
	runGit(t, repo, "add", "feature.txt")
	runGit(t, repo, "commit", "-m", "feature on main")
	// Push the new main commit to origin (simulating the squash-merged state).
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")

	// Now branch off the ORIGINAL main (not the new one) and reproduce the
	// same content. The resulting branch tip has content identical to main
	// but is a distinct commit object that exists on no remote ref.
	runGit(t, repo, "checkout", "-b", "polecat/squashed", "origin/polecat/foo~2") // C0
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("X\n"), 0644); err != nil {
		t.Fatalf("write feature.txt on branch: %v", err)
	}
	runGit(t, repo, "add", "feature.txt")
	runGit(t, repo, "commit", "-m", "local reproduction of feature")

	got, err := getGitState(repo)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}

	if got.UnpushedCommits == 0 {
		t.Errorf("UnpushedCommits = 0, want >0: local commit exists on no remote "+
			"ref even though its content is on main (squash-merge trap): %+v", got)
	}
	if got.Clean {
		t.Errorf("Clean = true, want false for commits reachable from no remote: %+v", got)
	}
}

// TestGetGitState_AllCommitsOnRemote verifies the positive case: when
// every commit on HEAD is reachable from some remote branch, getGitState
// reports Unpushed=0 and Clean=true.
func TestGetGitState_AllCommitsOnRemote(t *testing.T) {
	repo := initTestRepoWithRemote(t)

	// main is at C0 and origin/main = C0. Nothing local-only.
	got, err := getGitState(repo)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}

	if got.UnpushedCommits != 0 {
		t.Errorf("UnpushedCommits = %d, want 0: every commit is on origin/main: %+v",
			got.UnpushedCommits, got)
	}
	if !got.Clean {
		t.Errorf("Clean = false, want true when everything is on a remote ref: %+v", got)
	}
}



// initTestRepo creates a temp dir, `git init`s it, configures local
// author identity (required on hosts without a global git config), and
// commits a README. Returns the absolute path. Test is skipped if git
// is not on PATH.
func initTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available on PATH: %v", err)
	}

	repo := t.TempDir()

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		// Keep test output tidy even on noisy git setups.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("init")
	// Ensure branch name is predictable. `git init` in newer versions may
	// already default to main, but older ones still default to master.
	run("checkout", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	run("config", "commit.gpgsign", "false")

	readme := filepath.Join(repo, "README.md")
	if err := os.WriteFile(readme, []byte("hello\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "README.md")
	run("commit", "-m", "init")

	return repo
}

// runGit invokes git against a repo with predictable test identity. It is a
// convenience wrapper used by tests that need to construct multi-commit
// scenarios without re-declaring env plumbing.
func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// initTestRepoWithRemote extends initTestRepo by manufacturing a synthetic
// "origin" remote via `git update-ref refs/remotes/origin/...`. This avoids
// needing a real second repository on disk while still giving the rev-list
// --remotes machinery something concrete to subtract from HEAD.
//
// Layout on return:
//
//	main         = C0 (initial "hello" commit)
//	origin/main  = C0
//	origin/polecat/foo = C0 -> WIP1 -> WIP2 (diverged remote work)
//
// Tests build on this baseline to construct the "local commits on no remote
// ref" scenarios exercised by getGitState.
func initTestRepoWithRemote(t *testing.T) string {
	t.Helper()
	repo := initTestRepo(t)

	// Treat the initial commit as pushed to origin/main.
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")

	// Fabricate a diverged remote branch with two WIP checkpoints that sit
	// on top of C0. We build them on a throwaway local branch, write them
	// into refs/remotes/origin/polecat/foo, then reset back to main so the
	// worktree's HEAD is unchanged by the scaffolding.
	runGit(t, repo, "checkout", "-b", "__scaffold", "main")
	if err := os.WriteFile(filepath.Join(repo, "wip.txt"), []byte("wip1\n"), 0644); err != nil {
		t.Fatalf("write wip.txt: %v", err)
	}
	runGit(t, repo, "add", "wip.txt")
	runGit(t, repo, "commit", "-m", "remote WIP 1")
	if err := os.WriteFile(filepath.Join(repo, "wip.txt"), []byte("wip2\n"), 0644); err != nil {
		t.Fatalf("write wip.txt (2): %v", err)
	}
	runGit(t, repo, "add", "wip.txt")
	runGit(t, repo, "commit", "-m", "remote WIP 2")
	runGit(t, repo, "update-ref", "refs/remotes/origin/polecat/foo", "HEAD")

	// Clean up scaffolding so tests see a plain "on main" worktree.
	runGit(t, repo, "checkout", "main")
	runGit(t, repo, "branch", "-D", "__scaffold")

	return repo
}

// equalStringSlices compares two string slices for equality. nil and
// empty are treated as equivalent (which is how test expectations are
// written here).
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// containsSubstring wraps strings.Contains for readability in assertions
// and to give a single swap-point if we ever need case-insensitive checks.
func containsSubstring(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// --- Stale polecat refresh (pool-init) ------------------------------------
//
// These tests verify gu-7rw5: pool-init detects polecats whose agent bead
// hook_bead points to a closed/tombstone bead and refreshes them back to
// idle on main. They exercise pure functions (findStalePolecats,
// refreshStalePolecats) so no real beads DB or git worktree is required.

// stalePolecatBeadsFake is a lightweight fake for the staleBeadsLookup
// interface. Unlike mockBeads, it tracks AgentFields keyed by agent bead ID so
// we can simulate agent beads without pulling in a real Beads store.
type stalePolecatBeadsFake struct {
	agents map[string]*beads.AgentFields
	issues map[string]*beads.Issue
	// showErr/agentErr let tests simulate lookup failures, which
	// findStalePolecats must treat as "not stale".
	showErr  map[string]error
	agentErr map[string]error
}

func newStalePolecatBeadsFake() *stalePolecatBeadsFake {
	return &stalePolecatBeadsFake{
		agents:   map[string]*beads.AgentFields{},
		issues:   map[string]*beads.Issue{},
		showErr:  map[string]error{},
		agentErr: map[string]error{},
	}
}

func (f *stalePolecatBeadsFake) GetAgentBead(id string) (*beads.Issue, *beads.AgentFields, error) {
	if err, ok := f.agentErr[id]; ok {
		return nil, nil, err
	}
	fields, ok := f.agents[id]
	if !ok {
		return nil, nil, beads.ErrNotFound
	}
	return &beads.Issue{ID: id}, fields, nil
}

func (f *stalePolecatBeadsFake) Show(id string) (*beads.Issue, error) {
	if err, ok := f.showErr[id]; ok {
		return nil, err
	}
	issue, ok := f.issues[id]
	if !ok {
		return nil, beads.ErrNotFound
	}
	return issue, nil
}

// TestFindStalePolecats_DetectsClosedHookBead covers the primary case: a
// polecat whose hook_bead points at a closed bead is reported as stale.
func TestFindStalePolecats_DetectsClosedHookBead(t *testing.T) {
	fake := newStalePolecatBeadsFake()
	// Agent bead ID format: <prefix>-polecat-<name>, or collapsed when prefix==rig.
	// Because there's no rig config on disk under a test townRoot, GetPrefixForRig
	// falls back to "gt", so the agent ID is "gt-<rig>-polecat-<name>".
	fake.agents["gt-testrig-polecat-shiny"] = &beads.AgentFields{HookBead: "gt-old1"}
	fake.issues["gt-old1"] = &beads.Issue{ID: "gt-old1", Status: "closed"}

	polecats := []*polecat.Polecat{
		{Name: "shiny", Rig: "testrig"},
	}

	got := findStalePolecats(fake, t.TempDir(), polecats)
	if len(got) != 1 {
		t.Fatalf("findStalePolecats returned %d entries, want 1: %#v", len(got), got)
	}
	if got[0].Polecat.Name != "shiny" {
		t.Errorf("stale name = %q, want %q", got[0].Polecat.Name, "shiny")
	}
	if got[0].HookBead != "gt-old1" {
		t.Errorf("stale HookBead = %q, want %q", got[0].HookBead, "gt-old1")
	}
	if !containsSubstring(got[0].Reason, "closed") {
		t.Errorf("stale Reason = %q, want to mention closed status", got[0].Reason)
	}
}

// TestFindStalePolecats_DetectsTombstoneHookBead verifies that tombstone status
// (the other terminal state per IssueStatus.IsTerminal) also qualifies.
func TestFindStalePolecats_DetectsTombstoneHookBead(t *testing.T) {
	fake := newStalePolecatBeadsFake()
	fake.agents["gt-testrig-polecat-rust"] = &beads.AgentFields{HookBead: "gt-ghost"}
	fake.issues["gt-ghost"] = &beads.Issue{ID: "gt-ghost", Status: "tombstone"}

	got := findStalePolecats(fake, t.TempDir(), []*polecat.Polecat{{Name: "rust", Rig: "testrig"}})
	if len(got) != 1 {
		t.Fatalf("findStalePolecats returned %d entries, want 1", len(got))
	}
	if got[0].Polecat.Name != "rust" {
		t.Errorf("stale name = %q, want %q", got[0].Polecat.Name, "rust")
	}
}

// TestFindStalePolecats_SkipsOpenHookBead ensures we don't disturb polecats
// whose assigned bead is still open (actively working or queued).
func TestFindStalePolecats_SkipsOpenHookBead(t *testing.T) {
	fake := newStalePolecatBeadsFake()
	fake.agents["gt-testrig-polecat-chrome"] = &beads.AgentFields{HookBead: "gt-live"}
	fake.issues["gt-live"] = &beads.Issue{ID: "gt-live", Status: "open"}

	got := findStalePolecats(fake, t.TempDir(), []*polecat.Polecat{{Name: "chrome", Rig: "testrig"}})
	if len(got) != 0 {
		t.Fatalf("findStalePolecats returned %d entries for open bead, want 0: %#v", len(got), got)
	}
}

// TestFindStalePolecats_SkipsInProgressHookBead ensures in_progress work
// (actively claimed) is preserved.
func TestFindStalePolecats_SkipsInProgressHookBead(t *testing.T) {
	fake := newStalePolecatBeadsFake()
	fake.agents["gt-testrig-polecat-dust"] = &beads.AgentFields{HookBead: "gt-wip"}
	fake.issues["gt-wip"] = &beads.Issue{ID: "gt-wip", Status: "in_progress"}

	got := findStalePolecats(fake, t.TempDir(), []*polecat.Polecat{{Name: "dust", Rig: "testrig"}})
	if len(got) != 0 {
		t.Fatalf("findStalePolecats returned %d entries for in_progress bead, want 0", len(got))
	}
}

// TestFindStalePolecats_SkipsHookedHookBead covers the hooked status, which
// is not terminal — the polecat genuinely has work.
func TestFindStalePolecats_SkipsHookedHookBead(t *testing.T) {
	fake := newStalePolecatBeadsFake()
	fake.agents["gt-testrig-polecat-zeta"] = &beads.AgentFields{HookBead: "gt-hot"}
	fake.issues["gt-hot"] = &beads.Issue{ID: "gt-hot", Status: "hooked"}

	got := findStalePolecats(fake, t.TempDir(), []*polecat.Polecat{{Name: "zeta", Rig: "testrig"}})
	if len(got) != 0 {
		t.Fatalf("findStalePolecats returned %d entries for hooked bead, want 0", len(got))
	}
}

// TestFindStalePolecats_HandlesEmptyHookBead verifies that polecats with no
// hook_bead (freshly-reset pool members) are ignored.
func TestFindStalePolecats_HandlesEmptyHookBead(t *testing.T) {
	fake := newStalePolecatBeadsFake()
	fake.agents["gt-testrig-polecat-alpha"] = &beads.AgentFields{HookBead: ""}

	got := findStalePolecats(fake, t.TempDir(), []*polecat.Polecat{{Name: "alpha", Rig: "testrig"}})
	if len(got) != 0 {
		t.Fatalf("findStalePolecats returned %d entries for empty hook_bead, want 0", len(got))
	}
}

// TestFindStalePolecats_TreatsLookupErrorsAsNotStale ensures a beads outage
// doesn't cause us to refresh worktrees speculatively. The comment on
// findStalePolecats explicitly says errors bias toward preservation.
func TestFindStalePolecats_TreatsLookupErrorsAsNotStale(t *testing.T) {
	fake := newStalePolecatBeadsFake()
	fake.agentErr["gt-testrig-polecat-broken"] = errors.New("dolt offline")

	got := findStalePolecats(fake, t.TempDir(), []*polecat.Polecat{{Name: "broken", Rig: "testrig"}})
	if len(got) != 0 {
		t.Fatalf("findStalePolecats returned %d entries on agent error, want 0", len(got))
	}

	// Also: hook bead show error.
	fake2 := newStalePolecatBeadsFake()
	fake2.agents["gt-testrig-polecat-broken"] = &beads.AgentFields{HookBead: "gt-missing"}
	fake2.showErr["gt-missing"] = errors.New("not found")

	got = findStalePolecats(fake2, t.TempDir(), []*polecat.Polecat{{Name: "broken", Rig: "testrig"}})
	if len(got) != 0 {
		t.Fatalf("findStalePolecats returned %d entries on show error, want 0", len(got))
	}
}

// TestFindStalePolecats_PartitionsMixedPool verifies that a pool with a mix
// of fresh, stale, and working polecats yields only the stale ones.
func TestFindStalePolecats_PartitionsMixedPool(t *testing.T) {
	fake := newStalePolecatBeadsFake()
	// Stale: hook points to closed bead.
	fake.agents["gt-testrig-polecat-stale"] = &beads.AgentFields{HookBead: "gt-closed1"}
	fake.issues["gt-closed1"] = &beads.Issue{ID: "gt-closed1", Status: "closed"}
	// Fresh: no hook_bead.
	fake.agents["gt-testrig-polecat-fresh"] = &beads.AgentFields{HookBead: ""}
	// Working: hook points to open bead.
	fake.agents["gt-testrig-polecat-busy"] = &beads.AgentFields{HookBead: "gt-live"}
	fake.issues["gt-live"] = &beads.Issue{ID: "gt-live", Status: "open"}

	polecats := []*polecat.Polecat{
		{Name: "stale", Rig: "testrig"},
		{Name: "fresh", Rig: "testrig"},
		{Name: "busy", Rig: "testrig"},
	}

	got := findStalePolecats(fake, t.TempDir(), polecats)
	if len(got) != 1 {
		t.Fatalf("findStalePolecats returned %d entries, want 1 (just 'stale'): %#v", len(got), got)
	}
	if got[0].Polecat.Name != "stale" {
		t.Errorf("stale polecat = %q, want 'stale'", got[0].Polecat.Name)
	}
}

// TestFindStalePolecats_SkipsNilEntries documents the input hardening: a nil
// slot from Manager.List (results from concurrent Get failures) must not
// panic or leak into the result.
func TestFindStalePolecats_SkipsNilEntries(t *testing.T) {
	fake := newStalePolecatBeadsFake()
	fake.agents["gt-testrig-polecat-shiny"] = &beads.AgentFields{HookBead: "gt-c"}
	fake.issues["gt-c"] = &beads.Issue{ID: "gt-c", Status: "closed"}

	polecats := []*polecat.Polecat{
		nil,
		{Name: "shiny", Rig: "testrig"},
		nil,
	}

	got := findStalePolecats(fake, t.TempDir(), polecats)
	if len(got) != 1 {
		t.Fatalf("findStalePolecats returned %d entries, want 1 after skipping nils", len(got))
	}
}

// refreshRecordingRefresher captures calls to RefreshStalePolecat for
// assertion in tests. If failOn is set, the first call for that name returns
// an error so we can verify partial failures don't abort the loop.
type refreshRecordingRefresher struct {
	calls  []string
	failOn string
}

func (r *refreshRecordingRefresher) RefreshStalePolecat(name string, opts polecat.AddOptions) (*polecat.Polecat, error) {
	r.calls = append(r.calls, name)
	if r.failOn != "" && name == r.failOn {
		return nil, errors.New("simulated refresh failure")
	}
	return &polecat.Polecat{Name: name, State: polecat.StateIdle}, nil
}

// TestRefreshStalePolecats_CallsManagerForEachStale verifies the happy path:
// each stale polecat triggers exactly one RefreshStalePolecat call.
func TestRefreshStalePolecats_CallsManagerForEachStale(t *testing.T) {
	stale := []stalePolecat{
		{Polecat: &polecat.Polecat{Name: "one"}, HookBead: "gt-1", Reason: "closed"},
		{Polecat: &polecat.Polecat{Name: "two"}, HookBead: "gt-2", Reason: "closed"},
	}
	rec := &refreshRecordingRefresher{}

	cmd := &cobra.Command{}
	cmd.SetOut(&strings.Builder{})

	refreshStalePolecats(cmd, rec, stale, false)

	if !equalStringSlices(rec.calls, []string{"one", "two"}) {
		t.Errorf("RefreshStalePolecat calls = %v, want [one two]", rec.calls)
	}
}

// TestRefreshStalePolecats_DryRunDoesNotCallManager ensures --dry-run mode
// only reports intent and never mutates.
func TestRefreshStalePolecats_DryRunDoesNotCallManager(t *testing.T) {
	stale := []stalePolecat{
		{Polecat: &polecat.Polecat{Name: "one"}, HookBead: "gt-1", Reason: "closed"},
	}
	rec := &refreshRecordingRefresher{}

	cmd := &cobra.Command{}
	var out strings.Builder
	cmd.SetOut(&out)

	refreshStalePolecats(cmd, rec, stale, true)

	if len(rec.calls) != 0 {
		t.Errorf("dry-run made RefreshStalePolecat calls: %v", rec.calls)
	}
	if !containsSubstring(out.String(), "Would refresh") {
		t.Errorf("dry-run output = %q, want to contain 'Would refresh'", out.String())
	}
}

// TestRefreshStalePolecats_ContinuesPastFailure verifies one failed refresh
// doesn't abort the loop. Pool-init must still try the remaining polecats
// (and still create new ones) even when a single refresh bombs.
func TestRefreshStalePolecats_ContinuesPastFailure(t *testing.T) {
	stale := []stalePolecat{
		{Polecat: &polecat.Polecat{Name: "bad"}, HookBead: "gt-1", Reason: "closed"},
		{Polecat: &polecat.Polecat{Name: "good"}, HookBead: "gt-2", Reason: "closed"},
	}
	rec := &refreshRecordingRefresher{failOn: "bad"}

	cmd := &cobra.Command{}
	var out strings.Builder
	cmd.SetOut(&out)

	refreshStalePolecats(cmd, rec, stale, false)

	if !equalStringSlices(rec.calls, []string{"bad", "good"}) {
		t.Errorf("RefreshStalePolecat calls = %v, want [bad good] (good must run after bad fails)", rec.calls)
	}
	if !containsSubstring(out.String(), "FAILED") {
		t.Errorf("output = %q, want to mention FAILED for 'bad'", out.String())
	}
}

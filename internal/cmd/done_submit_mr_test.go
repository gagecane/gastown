package cmd

// Phase-isolated tests for submitToMergeQueue, the MR-submit phase of runDone
// extracted in gs-t0k (phase 2, following the gs-pd6 pushBranchWithFallbacks and
// gs-bn1 completion-package extractions).
//
// The helper's contract with runDone is the (mrID, mrFailed) return: a true
// mrFailed routes the caller to notifyWitness instead of reporting COMPLETED.
// These tests pin the two routing outcomes that the extraction must preserve:
//
//   - idempotent reuse: an MR already exists for this branch+SHA, so the helper
//     returns it with mrFailed=false and never calls `bd create`.
//   - create failure: `bd create` fails, so the helper returns mrFailed=true
//     (branch is pushed, but there is no MR for the refinery).
//
// Both drive a stubbed `bd` on PATH (the same technique as
// done_review_only_defer_test.go) so no Dolt server is required.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
)

// writeSubmitMRStub installs a `bd` stub in binDir that logs every subcommand to
// callsLog. `sql` returns sqlJSON (the merge-request wisp rows); `list` returns
// an empty array; `create` either fails (createFails) or returns a fresh MR id.
// All other write subcommands succeed silently.
func writeSubmitMRStub(t *testing.T, binDir, callsLog, sqlJSON string, createFails bool) {
	t.Helper()
	createBody := `echo '{"id":"gt-mr-new"}'`
	if createFails {
		createBody = `echo "bd create: simulated failure" >&2; exit 1`
	}
	script := `#!/bin/sh
# Skip leading global flags (e.g. --allow-stale) to find the subcommand.
while [ $# -gt 0 ]; do
  case "$1" in
    --*) shift ;;
    *) break ;;
  esac
done
cmd="$1"
shift || true
echo "$cmd" >> "` + callsLog + `"
case "$cmd" in
  sql)   cat "` + filepath.Join(binDir, "sql.json") + `" ;;
  list)  echo "[]" ;;
  show)  echo "[]" ;;
  create) ` + createBody + ` ;;
  comments|agent|update|close|slot) exit 0 ;;
  *) exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "sql.json"), []byte(sqlJSON), 0644); err != nil {
		t.Fatalf("write sql.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
}

// submitMRTestParams builds an mrSubmitParams bound to tmp as both work dir and
// beads dir, with a real git.Git on tmp. agentBeadID is empty so the helper
// skips the checkpoint write (writeDoneCheckpoint requires an agent bead).
func submitMRTestParams(tmp, branch, commitSHA string) mrSubmitParams {
	return mrSubmitParams{
		bd:        beads.NewWithBeadsDir(tmp, tmp),
		g:         git.NewGit(tmp),
		cwd:       tmp,
		branch:    branch,
		commitSHA: commitSHA,
		target:    "main",
		issueID:   "gt-src",
		priority:  2,
	}
}

// TestSubmitToMergeQueue_IdempotentExisting verifies the idempotent-reuse path:
// when an MR already exists for this branch+SHA, submitToMergeQueue returns it
// with mrFailed=false and does NOT create a second MR.
func TestSubmitToMergeQueue_IdempotentExisting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}
	tmp := t.TempDir()
	callsLog := filepath.Join(tmp, "calls.log")
	branch := "polecat/test/gt-src"
	sha := "abc123"

	// One open merge-request wisp whose description matches branch + commit_sha.
	sqlJSON := `[{"id":"gt-mr-existing","title":"Merge: gt-src",` +
		`"description":"branch: ` + branch + `\ntarget: main\nsource_issue: gt-src\ncommit_sha: ` + sha + `\n",` +
		`"status":"open","priority":2,"labels_csv":"gt:merge-request"}]`
	writeSubmitMRStub(t, tmp, callsLog, sqlJSON, false)
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	mrID, mrFailed := submitToMergeQueue(submitMRTestParams(tmp, branch, sha))

	if mrFailed {
		t.Fatalf("idempotent reuse must not fail: mrFailed=true")
	}
	if mrID != "gt-mr-existing" {
		t.Fatalf("expected reused MR id gt-mr-existing, got %q", mrID)
	}
	if calls, _ := os.ReadFile(callsLog); strings.Contains(string(calls), "create") {
		t.Fatalf("idempotent path must not create a new MR; calls:\n%s", calls)
	}
}

// TestSubmitToMergeQueue_CreateFailureRoutesToWitness verifies the failure
// contract: when `bd create` fails, submitToMergeQueue returns mrFailed=true so
// runDone routes to notifyWitness instead of reporting COMPLETED.
func TestSubmitToMergeQueue_CreateFailureRoutesToWitness(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}
	tmp := t.TempDir()
	callsLog := filepath.Join(tmp, "calls.log")

	// No existing MR (empty sql rows) → helper falls through to create, which fails.
	writeSubmitMRStub(t, tmp, callsLog, "[]", true)
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	mrID, mrFailed := submitToMergeQueue(submitMRTestParams(tmp, "polecat/test/gt-src", "deadbeef"))

	if !mrFailed {
		t.Fatalf("create failure must set mrFailed=true")
	}
	if mrID != "" {
		t.Fatalf("create failure must leave mrID empty, got %q", mrID)
	}
	if calls, _ := os.ReadFile(callsLog); !strings.Contains(string(calls), "create") {
		t.Fatalf("expected a create attempt before failing; calls:\n%s", calls)
	}
}

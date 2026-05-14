package testutil

import (
	"os"
	"os/exec"
	"strings"
)

// CleanGTEnv returns os.Environ() with GT_* and BD_* variables removed, except
// GT_DOLT_PORT, GT_DOLT_HOST, and GT_TEST_EXTERNAL_DOLT which are preserved so
// subprocesses connect to and reuse the test Dolt server. BEADS_DOLT_PORT and
// BEADS_DOLT_SERVER_HOST (prefix BEADS_, not BD_) pass through implicitly since
// only BD_* is stripped.
//
// Use this when setting cmd.Env on bd/gt subprocess calls in tests.
// If you do NOT set cmd.Env, the process env (including GT_DOLT_PORT) is
// inherited automatically — no need for this function in that case.
func CleanGTEnv(extraEnv ...string) []string {
	var clean []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GT_") &&
			!strings.HasPrefix(e, "GT_DOLT_PORT=") &&
			!strings.HasPrefix(e, "GT_DOLT_HOST=") &&
			!strings.HasPrefix(e, "GT_TEST_EXTERNAL_DOLT=") {
			continue
		}
		if strings.HasPrefix(e, "BD_") {
			continue
		}
		clean = append(clean, e)
	}
	return append(clean, extraEnv...)
}

// NewBDCommand creates an exec.Command for the bd CLI with GT_DOLT_PORT
// automatically propagated. The command inherits the full process environment
// (which includes GT_DOLT_PORT set by TestMain).
//
// Use this instead of bare exec.Command("bd", ...) in tests.
func NewBDCommand(args ...string) *exec.Cmd {
	return exec.Command("bd", args...)
}

// NewGTCommand creates an exec.Command for the gt CLI with GT_DOLT_PORT
// automatically propagated. The command inherits the full process environment
// (which includes GT_DOLT_PORT set by TestMain).
//
// Use this instead of bare exec.Command("gt", ...) in tests.
func NewGTCommand(args ...string) *exec.Cmd {
	return exec.Command("gt", args...)
}

// NewIsolatedBDCommand creates an exec.Command for the bd CLI with GT_*/BD_*
// env stripped except GT_DOLT_PORT and BEADS_DOLT_PORT. Use this when you need
// to isolate a subprocess from the parent Gas Town workspace but still route
// to the test Dolt server.
func NewIsolatedBDCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("bd", args...)
	cmd.Env = CleanGTEnv()
	return cmd
}

// NewIsolatedGTCommand creates an exec.Command for the gt CLI with GT_*/BD_*
// env stripped except GT_DOLT_PORT and BEADS_DOLT_PORT. Use this when you need
// to isolate a subprocess from the parent Gas Town workspace but still route
// to the test Dolt server.
func NewIsolatedGTCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("gt", args...)
	cmd.Env = CleanGTEnv()
	return cmd
}

// gitRepoEnvPrefixes enumerates environment variables git(1) honors to locate
// a repository. Tests that build fixture repos in t.TempDir() and shell out
// to git MUST strip these from os.Environ() before invoking git — otherwise
// git reads GIT_DIR etc. from the environment and silently operates on the
// process's ambient repo instead of the fixture.
//
// This matters most when tests are invoked from a git hook (e.g. pre-push
// running `make verify`): the hook inherits GIT_DIR pointing at the pushing
// repo, a test `git push bare main` resolves GIT_DIR first (overriding
// cmd.Dir), and the push silently lands on the real repo. See bead gu-h2ru
// for the incident that motivated this helper.
//
// The list intentionally includes more than GIT_DIR alone — GIT_WORK_TREE,
// GIT_INDEX_FILE, GIT_OBJECT_DIRECTORY, GIT_COMMON_DIR et al. all redirect
// distinct pieces of git's storage model and any of them can leak.
var gitRepoEnvPrefixes = []string{
	"GIT_DIR=",
	"GIT_WORK_TREE=",
	"GIT_INDEX_FILE=",
	"GIT_OBJECT_DIRECTORY=",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES=",
	"GIT_COMMON_DIR=",
	"GIT_CEILING_DIRECTORIES=",
	"GIT_NAMESPACE=",
	"GIT_PREFIX=",
	"GIT_LITERAL_PATHSPECS=",
	"GIT_GLOB_PATHSPECS=",
	"GIT_NOGLOB_PATHSPECS=",
	"GIT_ICASE_PATHSPECS=",
}

// CleanGitEnv returns os.Environ() with git-repo-pointing variables removed
// (GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, etc.). Any extraEnv entries are
// appended verbatim — callers commonly add GIT_AUTHOR_NAME/EMAIL for
// deterministic commits.
//
// Use this when a test builds its own git fixture in t.TempDir() and shells
// out to git. If the test is run from a git hook (e.g. pre-push running
// `go test`), the hook exports GIT_DIR pointing at the pushing repo; git
// reads that env var before consulting cmd.Dir, so without scrubbing a
// test `git push` can silently land on the real repo. See bead gu-h2ru.
//
// Example:
//
//	cmd := exec.Command("git", "init", "--bare", "-b", "main", bareRepo)
//	cmd.Dir = someDir
//	cmd.Env = testutil.CleanGitEnv(
//	    "GIT_AUTHOR_NAME=Test",
//	    "GIT_AUTHOR_EMAIL=test@test.com",
//	    "GIT_COMMITTER_NAME=Test",
//	    "GIT_COMMITTER_EMAIL=test@test.com",
//	)
func CleanGitEnv(extraEnv ...string) []string {
	var out []string
	for _, e := range os.Environ() {
		if hasAnyPrefix(e, gitRepoEnvPrefixes) {
			continue
		}
		out = append(out, e)
	}
	return append(out, extraEnv...)
}

// gitRepoEnvKeys is the set of environment variable KEYS (no trailing "=")
// that redirect git to a repository other than cmd.Dir. It mirrors
// gitRepoEnvPrefixes but is the list form needed by os.Unsetenv.
var gitRepoEnvKeys = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_COMMON_DIR",
	"GIT_CEILING_DIRECTORIES",
	"GIT_NAMESPACE",
	"GIT_PREFIX",
	"GIT_LITERAL_PATHSPECS",
	"GIT_GLOB_PATHSPECS",
	"GIT_NOGLOB_PATHSPECS",
	"GIT_ICASE_PATHSPECS",
}

// UnsetGitRepoEnv clears git-repo-pointing environment variables (GIT_DIR,
// GIT_WORK_TREE, GIT_INDEX_FILE, etc.) from the current process's
// environment. Intended to be called from an init() in a _test.go file in
// any package whose tests spawn git via exec.Command — this immunizes
// every subsequent git subprocess at once without having to touch every
// call site.
//
// Example:
//
//	// internal/foo/testenv_test.go
//	package foo
//
//	import "github.com/steveyegge/gastown/internal/testutil"
//
//	func init() { testutil.UnsetGitRepoEnv() }
//
// Why at package init: when the test binary runs from a git hook (e.g.
// pre-push running `make verify`), the inherited GIT_DIR will otherwise
// win over cmd.Dir and the test's fixture git operations silently land on
// the real pushing repo. See bead gu-h2ru.
func UnsetGitRepoEnv() {
	for _, k := range gitRepoEnvKeys {
		_ = os.Unsetenv(k)
	}
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

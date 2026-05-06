package cmd

import "github.com/steveyegge/gastown/internal/testutil"

// init scrubs git-repo-pointing environment variables at test-binary startup
// so tests that shell out to git via `exec.Command("git", ...)` see a clean
// environment. When the suite runs from a git hook (e.g. pre-push running
// `make verify`), the hook exports GIT_DIR / GIT_WORK_TREE etc. pointing at
// the pushing repo. git reads those vars ahead of cmd.Dir, so fixture
// operations silently leak onto the real repo. See bead gu-h2ru for the
// incident that motivated this package-level scrubber.
//
// internal/cmd has many tests that build git fixtures in t.TempDir() and
// push between them (done_test.go, costs_workdir_test.go, polecat_test.go,
// etc.). Without this init every one of those is a potential leak source.
func init() {
	testutil.UnsetGitRepoEnv()
}

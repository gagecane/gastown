package git

import "github.com/steveyegge/gastown/internal/testutil"

// init scrubs git-repo-pointing environment variables at test-binary startup
// so tests that shell out to git via `exec.Command("git", ...)` see a clean
// environment. When the suite runs from a git hook (e.g. pre-push running
// `make verify`), the hook exports GIT_DIR / GIT_WORK_TREE etc. pointing at
// the pushing repo. git reads those vars ahead of cmd.Dir, so fixture
// operations silently leak onto the real repo. See bead gu-h2ru.
//
// Placing the unset in an init() inside a _test.go file ensures it runs once
// per test binary without modifying the 99+ exec.Command("git", ...) call
// sites in this package.
func init() {
	testutil.UnsetGitRepoEnv()
}

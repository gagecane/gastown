package deacon

import "github.com/steveyegge/gastown/internal/testutil"

// init scrubs git-repo-pointing environment variables at test-binary startup
// so tests that shell out to git via `exec.Command("git", ...)` see a clean
// environment. When the suite runs from a git hook (e.g. pre-push running
// `make verify`), git exports GIT_DIR to the hook (specifically when pushing
// from a worktree) pointing at the pushing repo's .git. git reads those vars
// ahead of cmd.Dir, so fixture operations silently leak onto the real repo.
// See bead gu-h2ru for the canonical incident and gu-ywxr for the follow-up
// that traced a HEAD-detach-on-push regression back to packages missing
// this init hook.
//
// stale_hooks_test.go builds real git fixtures in t.TempDir() via
// `exec.Command("git", ...)` without setting cmd.Env, relying on the
// process env being clean.
func init() {
	testutil.UnsetGitRepoEnv()
}

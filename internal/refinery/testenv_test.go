package refinery

import "github.com/steveyegge/gastown/internal/testutil"

// init scrubs git-repo-pointing environment variables at test-binary startup
// so tests that shell out to git via `exec.Command("git", ...)` see a clean
// environment. When the suite runs from a git hook (e.g. pre-push running
// `make verify`), git exports GIT_DIR to the hook (specifically when pushing
// from a worktree) pointing at the pushing repo's .git. git reads those vars
// ahead of cmd.Dir, so fixture operations silently leak onto the real repo.
// See bead gu-h2ru for the canonical incident and gu-ywxr for the follow-up
// that traced a HEAD-detach-on-push regression back to this package's
// missing init hook.
//
// internal/refinery/batch_test.go (and its peers) use a bare `run()` helper
// that calls `exec.Command("git", ...)` with cmd.Dir set but without
// scrubbing cmd.Env — relying on the process env being clean. Without this
// init(), a pre-push-hook invocation would leak GIT_DIR into every
// `git checkout -b ...` / `git checkout main` the fixtures do, detaching
// HEAD on the real pushing worktree and (on some git versions) deleting
// the polecat branch outright.
func init() {
	testutil.UnsetGitRepoEnv()
}

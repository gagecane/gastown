package doctor

import "github.com/steveyegge/gastown/internal/testutil"

// init scrubs git-repo-pointing environment variables at test-binary startup.
// See testutil.UnsetGitRepoEnv for rationale and bead gu-h2ru for the
// incident that motivated this package-level scrubber.
//
// This doctor package contains TestCheckoutWithWorktreeRetry_BareRepoConflict
// and several other tests that spawn git via exec.Command on t.TempDir()
// fixtures. Without this init the GIT_DIR exported by a pre-push hook wins
// over cmd.Dir and the fixture operations silently target the real repo.
func init() {
	testutil.UnsetGitRepoEnv()
}

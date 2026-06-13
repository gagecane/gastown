package completion

// Submit-side gofmt auto-format (gu-ph24z).
//
// gofmt is a REQUIRED refinery pre-merge gate AND is enforced by the pre-push
// hook (scripts/pre-push-check.sh FAST GATE 3: `gofmt -l .`), so an unformatted
// branch physically cannot land — the refinery rejects it. But before this
// guard nothing inside `gt done` ran gofmt, so the rejection surfaced AFTER the
// branch reached the merge queue instead of before. By then the polecat session
// is gone, so the trivial formatting fix has to bounce back through a fresh
// dispatch (gu-mxupc was rejected twice — dust then guzzle — for the same
// byte-identical struct-field-alignment failure on a committed test file).
//
// AutoFormatGoFiles closes that gap: it mirrors the pre-push hook's
// `gofmt -l .` invocation exactly so local and gate behavior match, and on any
// unformatted file it auto-fixes with `gofmt -w` and commits the result as a
// `style:` fixup. We prefer auto-fix (bead option a) over a blocking local
// failure (option b) so a dead polecat's trivial formatting never strands its
// work — gofmt only rewrites whitespace/alignment, never semantics, so the
// build/vet/test gates that already ran stay valid across the fixup commit.
//
// We never block submission on a gofmt tooling error (gofmt missing, parse
// failure): the pre-push hook and refinery gate remain the backstop, and
// stranding the polecat over a tool hiccup is strictly worse.

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/style"
)

// GofmtRunner runs `gofmt <args...>` in workDir and returns combined output.
// Indirected so tests can stub gofmt invocation without a real toolchain.
type GofmtRunner func(workDir string, args ...string) ([]byte, error)

// execGofmt is the production GofmtRunner: it shells out to the gofmt binary.
func execGofmt(workDir string, args ...string) ([]byte, error) {
	c := exec.Command("gofmt", args...) //nolint:gosec // G204: fixed binary, args are gofmt flags + repo-relative paths
	c.Dir = workDir
	return c.CombinedOutput()
}

// GofmtCommitter is the subset of *git.Git that AutoFormatGoFiles needs,
// defined as an interface so tests can assert on the commit without a repo.
type GofmtCommitter interface {
	WorkDir() string
	CommitPaths(message string, paths ...string) error
}

// parseGofmtList splits `gofmt -l` output into a list of repo-relative file
// paths, dropping blank lines.
func parseGofmtList(out []byte) []string {
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.TrimSpace(line); f != "" {
			files = append(files, f)
		}
	}
	return files
}

// AutoFormatGoFiles runs the pre-push hook's `gofmt -l .` check inside gt done.
// When it finds unformatted files it auto-fixes them with `gofmt -w` and
// commits the result, so an unformatted branch can no longer reach the refinery
// (gu-ph24z).
//
// Returns:
//   - formatted=true when files were reformatted and committed.
//   - err only on a hard failure of the auto-fix path (gofmt -w or the commit);
//     a gofmt *detection* tooling error is logged and swallowed (returns
//     false,nil) so submission is never stranded over a tool hiccup.
//
// run is the gofmt indirection (nil → execGofmt).
func AutoFormatGoFiles(g GofmtCommitter, run GofmtRunner) (bool, error) {
	if run == nil {
		run = execGofmt
	}
	workDir := g.WorkDir()

	// Mirror the pre-push hook exactly: `gofmt -l .` from the repo root.
	out, err := run(workDir, "-l", ".")
	if err != nil {
		style.PrintWarning("gofmt -l check failed: %v — skipping pre-submit auto-format (pre-push hook / refinery gate still applies)", err)
		return false, nil
	}

	unformatted := parseGofmtList(out)
	if len(unformatted) == 0 {
		return false, nil
	}

	fmt.Printf("%s gofmt: %d file(s) need formatting — auto-fixing before submit (gu-ph24z)\n",
		style.Bold.Render("→"), len(unformatted))
	for _, f := range unformatted {
		fmt.Printf("  %s\n", f)
	}

	if _, werr := run(workDir, append([]string{"-w"}, unformatted...)...); werr != nil {
		return false, fmt.Errorf("gofmt -w failed: %w", werr)
	}

	if cerr := g.CommitPaths("style: gofmt (auto-format pre-submit, gu-ph24z)", unformatted...); cerr != nil {
		return false, fmt.Errorf("committing gofmt fixes failed: %w", cerr)
	}

	fmt.Printf("%s Committed gofmt formatting fixes\n", style.Bold.Render("✓"))
	return true, nil
}

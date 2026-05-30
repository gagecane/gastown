package doctor

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gateFiles are the working-tree files that gate pushes. Git always executes
// the CHECKED-OUT copy of these — the pre-push hook and the script it invokes —
// so a clone whose working tree lags the default branch runs STALE gates.
//
// This is not hypothetical: a refinery worktree frozen before the gofmt and
// golangci-lint fast gates were added merges polecat work and pushes it to main
// while running the old build/vet/test-only script, so gofmt/lint failures sail
// through and CI rejects them — the recurring "style: gofmt X" / "fix(lint)"
// followup churn. See gs-812 (root cause) and gs-6zv (this check).
var gateFiles = []string{
	".githooks/pre-push",
	"scripts/pre-push-check.sh",
}

// GateScriptFreshAllRigsCheck verifies that every clone across all rigs has the
// current push-gate files checked out, matching the remote default branch. A
// clone running stale gate scripts silently skips gates that newer commits add,
// which is how CI-failing commits reach main from worktrees that lag behind.
type GateScriptFreshAllRigsCheck struct {
	FixableCheck
	staleDetails []string  // human-readable "relpath: file1, file2"
	staleFixes   []gateFix // remediation targets
}

type gateFix struct {
	clonePath     string
	defaultBranch string
	files         []string
}

// NewGateScriptFreshAllRigsCheck creates a new global gate-script freshness check.
func NewGateScriptFreshAllRigsCheck() *GateScriptFreshAllRigsCheck {
	return &GateScriptFreshAllRigsCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "gate-script-fresh-all-rigs",
				CheckDescription: "Check push-gate scripts match the default branch for all clones",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run compares each clone's checked-out gate files against the remote default
// branch tip. Comparison uses the clone's locally-known origin/<branch> ref (no
// network in Run); since clones update that ref when they push, an idle-but-
// pushing worktree like the refinery still reports accurately. Fix re-fetches
// to refresh against the true tip.
func (c *GateScriptFreshAllRigsCheck) Run(ctx *CheckContext) *CheckResult {
	rigs := findAllRigs(ctx.TownRoot)
	if len(rigs) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No rigs found",
		}
	}

	c.staleDetails = nil
	c.staleFixes = nil
	totalClones := 0

	for _, rigPath := range rigs {
		for _, clonePath := range findRigClones(rigPath) {
			// Only clones that use hooks gate pushes.
			if _, err := os.Stat(filepath.Join(clonePath, ".githooks")); os.IsNotExist(err) {
				continue
			}
			defaultBranch := remoteDefaultBranch(clonePath)
			if defaultBranch == "" {
				continue
			}
			totalClones++

			var stale []string
			for _, f := range gateFiles {
				if differs, determined := gateFileDiffersFromRemote(clonePath, defaultBranch, f); determined && differs {
					stale = append(stale, f)
				}
			}
			if len(stale) > 0 {
				relPath, _ := filepath.Rel(ctx.TownRoot, clonePath)
				if relPath == "" {
					relPath = clonePath
				}
				c.staleDetails = append(c.staleDetails, fmt.Sprintf("%s: %s", relPath, strings.Join(stale, ", ")))
				c.staleFixes = append(c.staleFixes, gateFix{
					clonePath:     clonePath,
					defaultBranch: defaultBranch,
					files:         stale,
				})
			}
		}
	}

	if len(c.staleFixes) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("All %d clone(s) run current push gates", totalClones),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d clone(s) running stale push-gate scripts", len(c.staleFixes)),
		Details: c.staleDetails,
		FixHint: "Run 'gt doctor --fix' to refresh gate scripts from the default branch",
	}
}

// Fix overwrites stale gate files in place with the default-branch version. It
// writes content directly (rather than `git checkout`) so it refreshes the
// RUNNING gate without staging changes in another agent's worktree; the file
// will show as modified until that worktree next advances to the branch tip.
func (c *GateScriptFreshAllRigsCheck) Fix(ctx *CheckContext) error {
	for _, fx := range c.staleFixes {
		// Refresh the remote-tracking ref so we write the true tip, not a stale
		// locally-known one. Best-effort: a fetch failure still lets us write
		// the latest content we have.
		_ = exec.Command("git", "-C", fx.clonePath, "fetch", "origin", fx.defaultBranch).Run()

		for _, f := range fx.files {
			content, err := exec.Command("git", "-C", fx.clonePath, "show", "origin/"+fx.defaultBranch+":"+f).Output()
			if err != nil {
				return fmt.Errorf("reading origin/%s:%s in %s: %w", fx.defaultBranch, f, fx.clonePath, err)
			}
			dst := filepath.Join(fx.clonePath, f)
			mode := os.FileMode(0o755) // gate files are executable
			if fi, statErr := os.Stat(dst); statErr == nil {
				mode = fi.Mode().Perm()
			}
			if err := os.WriteFile(dst, content, mode); err != nil {
				return fmt.Errorf("writing %s: %w", dst, err)
			}
		}
	}
	return nil
}

// gateFileDiffersFromRemote reports whether the clone's on-disk copy of relFile
// differs from origin/<defaultBranch>:relFile. determined is false when the
// comparison can't be made (no such ref, or the file doesn't exist at the
// branch tip) — in which case the file is not judged stale.
func gateFileDiffersFromRemote(clonePath, defaultBranch, relFile string) (differs, determined bool) {
	canonical, err := exec.Command("git", "-C", clonePath, "show", "origin/"+defaultBranch+":"+relFile).Output()
	if err != nil {
		return false, false
	}
	onDisk, err := os.ReadFile(filepath.Join(clonePath, relFile))
	if err != nil {
		// The branch tip carries this gate file but the clone doesn't — the
		// gate is missing entirely, which is the most severe form of stale.
		return true, true
	}
	return !bytes.Equal(canonical, onDisk), true
}

// remoteDefaultBranch resolves the clone's remote default branch, mirroring the
// fallback chain in .githooks/pre-push: origin/HEAD symref, then origin/master,
// then origin/main. Returns "" if the clone has no origin.
func remoteDefaultBranch(clonePath string) string {
	if out, err := exec.Command("git", "-C", clonePath, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output(); err == nil {
		ref := strings.TrimSpace(string(out)) // e.g. "origin/main"
		if i := strings.IndexByte(ref, '/'); i >= 0 && i+1 < len(ref) {
			return ref[i+1:]
		}
	}
	if exec.Command("git", "-C", clonePath, "rev-parse", "--verify", "origin/master").Run() == nil {
		return "master"
	}
	if exec.Command("git", "-C", clonePath, "rev-parse", "--verify", "origin/main").Run() == nil {
		return "main"
	}
	return ""
}

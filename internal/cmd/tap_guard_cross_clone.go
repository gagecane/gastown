package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

var tapGuardCrossCloneCmd = &cobra.Command{
	Use:   "cross-clone-block",
	Short: "Block write-class git operations against another crew's clone",
	Long: `Block git write-class operations targeting a DIFFERENT crew clone via
'git -C <other-crew-clone> ...'.

Background: a Gas Town agent (mayor, deacon, refinery, witness, etc.) running
from one clone can issue 'git -C /path/to/another/crew/clone <op>' and write
into a clone the agent does not own. This breaks crew isolation — the target
clone's active session can have its branch, index, or working tree mutated
mid-flight, polluting that crew's work (see dc-c6m2 incident, dc-v1fw RCA).

This guard inspects the proposed Bash command, extracts any 'git -C <path>'
target, and blocks if both:

  1. The target path resolves into a crew clone OTHER than the calling
     process's own crew clone (or the calling process is not in any crew
     clone), AND
  2. The git subcommand is write-class (commit, push, merge, rebase,
     cherry-pick, reset, stash, clean, branch -D, tag -d, apply, am).

Read-only ops (log, status, diff, show, rev-parse, etc) are explicitly
allowed — diagnostic visibility into other clones is useful and not a breach.

Exit codes:
  0 - Operation allowed (read-only, same-clone, or no git -C target)
  2 - Operation BLOCKED

Emergency override:
  Set DEACON_FORCE_REPLICATE=1 in the environment to bypass. Logged in
  the block message so the bypass is visible in transcripts.`,
	RunE: runTapGuardCrossClone,
}

func init() {
	tapGuardCmd.AddCommand(tapGuardCrossCloneCmd)
}

// crossCloneWriteOps lists the git subcommands considered write-class for
// the purposes of this guard. Subcommand is the first non-flag token after
// any '-C <path>' arguments.
var crossCloneWriteOps = map[string]bool{
	"commit":      true,
	"push":        true,
	"merge":       true,
	"rebase":      true,
	"cherry-pick": true,
	"reset":       true,
	"stash":       true,
	"clean":       true,
	"branch":      true, // we further filter for -D / -d below
	"tag":         true, // we further filter for -d below
	"apply":       true,
	"am":          true,
	"revert":      true,
	"checkout":    true, // covers checkout -- file (touches working tree)
	"restore":     true,
	"switch":      true,
	"rm":          true,
	"mv":          true,
	"add":         true,
}

// gitDashCRe matches 'git -C <path>' or 'git -C=<path>'. The path is in
// group 1 (with -C separated by space) or group 2 (with -C=).
//
// We intentionally keep this regex simple. Pathological shell quoting can
// evade the match; that is acceptable for this guard. The cost of evading is
// the user explicitly understands they are crossing the boundary.
var gitDashCRe = regexp.MustCompile(`(?:^|[;&|\s])git\s+(?:-C\s+([^\s]+)|-C=([^\s]+))(.*?)(?:[;&|]|$)`)

func runTapGuardCrossClone(cmd *cobra.Command, args []string) error {
	// Emergency override (consistent with PR #5 sentinel infra).
	if os.Getenv("DEACON_FORCE_REPLICATE") == "1" {
		return nil
	}

	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil // fail open — never break the user's session over a hook bug
	}
	command := extractCommand(input)
	if command == "" {
		return nil
	}

	matches := gitDashCRe.FindAllStringSubmatch(command, -1)
	if len(matches) == 0 {
		return nil // no git -C in this command — not our concern
	}

	myCrewClone := currentCrewClone() // may be "" if caller is not in a crew clone

	for _, m := range matches {
		// m[1] = path with '-C path'  form; m[2] = path with '-C=path' form
		// m[3] = trailing chunk that includes the subcommand
		var rawPath, trailing string
		if m[1] != "" {
			rawPath = m[1]
			trailing = m[3]
		} else {
			rawPath = m[2]
			trailing = m[3]
		}

		targetCrewClone := resolveCrewClone(rawPath)
		if targetCrewClone == "" {
			continue // path doesn't resolve to a crew clone — out of scope
		}
		if myCrewClone != "" && targetCrewClone == myCrewClone {
			continue // same-clone op — fine
		}

		subcommand := firstSubcommand(trailing)
		if subcommand == "" {
			continue
		}
		if !isWriteClass(subcommand, trailing) {
			continue // read-only op against another crew clone — allowed
		}

		printCrossCloneBlock(rawPath, subcommand)
		return NewSilentExit(2)
	}

	return nil
}

// currentCrewClone returns the absolute path of the crew clone that the
// current working directory belongs to, if any. A "crew clone" is identified
// by a path segment of the form '/crew/<name>/' that contains a '.git'
// directory or file. Returns "" if the caller is not in a crew clone.
func currentCrewClone() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return resolveCrewClone(cwd)
}

// resolveCrewClone walks up from the given path looking for the first
// '<...>/crew/<name>/' segment that itself is a git clone (has .git). Returns
// the absolute crew-clone root, or "" if no crew clone is found in the
// ancestor chain.
//
// Paths are canonicalized via filepath.EvalSymlinks so that platforms with
// /var → /private/var (macOS) compare equal to themselves regardless of
// which form the caller passed in.
func resolveCrewClone(path string) string {
	abs, err := filepath.Abs(expandTilde(path))
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	cur := abs
	for {
		parent := filepath.Dir(cur)
		// We are at <something>/crew/<name>/* — when 'cur' equals a crew
		// clone root, parent's basename is "crew" and parent's parent
		// holds a rig directory.
		if filepath.Base(parent) == "crew" {
			if isGitClone(cur) {
				return cur
			}
		}
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

// isGitClone returns true if the directory has a .git entry (file or dir).
func isGitClone(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (st.IsDir() || st.Mode().IsRegular())
}

// expandTilde replaces a leading "~" with the user's home directory.
// Only handles the bare "~" / "~/" form — that's the only form we care
// about for guard purposes.
func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

// firstSubcommand returns the first non-flag token from the trailing portion
// of a 'git -C <path> ...' invocation. Handles the cases where additional
// flags appear before the subcommand (e.g. '-c user.email=foo commit').
//
// Recognizes the small set of git top-level flags that take a value as a
// separate argument so we don't mistake the value for the subcommand.
func firstSubcommand(trailing string) string {
	flagsWithSeparateValue := map[string]bool{
		"-c": true, "--config-env": true,
	}
	fields := strings.Fields(trailing)
	skipNext := false
	for _, f := range fields {
		if skipNext {
			skipNext = false
			continue
		}
		if flagsWithSeparateValue[f] {
			skipNext = true
			continue
		}
		if strings.HasPrefix(f, "-") {
			continue
		}
		return f
	}
	return ""
}

// isWriteClass reports whether the given subcommand (with its full trailing
// argument string) is a write-class git operation under this guard's policy.
// Special-cases:
//   - 'branch' is write-class only with -D, --delete, -d, --delete (not list/show)
//   - 'tag' is write-class only with -d, --delete (not list/show)
//   - 'remote' has its own subcommand surface — not handled here (out of scope)
func isWriteClass(subcommand, trailing string) bool {
	if !crossCloneWriteOps[subcommand] {
		return false
	}
	switch subcommand {
	case "branch":
		// Plain 'branch' lists branches — read-only. Block only on delete forms.
		return strings.Contains(trailing, " -D") ||
			strings.Contains(trailing, " --delete") ||
			strings.Contains(trailing, " -d ") ||
			strings.HasSuffix(trailing, " -d")
	case "tag":
		// Plain 'tag' lists tags — read-only. Block only on delete forms.
		return strings.Contains(trailing, " -d") ||
			strings.Contains(trailing, " --delete")
	}
	return true
}

func printCrossCloneBlock(targetPath, subcommand string) {
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "╔══════════════════════════════════════════════════════════════════╗")
	fmt.Fprintln(os.Stderr, "║  ❌ CROSS-CLONE WRITE BLOCKED                                    ║")
	fmt.Fprintln(os.Stderr, "╠══════════════════════════════════════════════════════════════════╣")
	fmt.Fprintf(os.Stderr, "║  Target:  %-53s ║\n", truncateStr(targetPath, 53))
	fmt.Fprintf(os.Stderr, "║  Op:      %-53s ║\n", truncateStr("git "+subcommand, 53))
	fmt.Fprintln(os.Stderr, "║                                                                  ║")
	fmt.Fprintln(os.Stderr, "║  Crews own their own clones. Reaching across the boundary       ║")
	fmt.Fprintln(os.Stderr, "║  causes silent merge breaches when the target clone has an      ║")
	fmt.Fprintln(os.Stderr, "║  active session (see dc-v1fw root cause analysis).              ║")
	fmt.Fprintln(os.Stderr, "║                                                                  ║")
	fmt.Fprintln(os.Stderr, "║  Right pattern: push to origin/main from your own clone, let    ║")
	fmt.Fprintln(os.Stderr, "║  each crew rebase from origin/main on their next session start. ║")
	fmt.Fprintln(os.Stderr, "║                                                                  ║")
	fmt.Fprintln(os.Stderr, "║  Read-only ops (git -C <path> log/status/diff/show) are still   ║")
	fmt.Fprintln(os.Stderr, "║  allowed — diagnostic visibility is fine.                       ║")
	fmt.Fprintln(os.Stderr, "║                                                                  ║")
	fmt.Fprintln(os.Stderr, "║  Emergency override (logged):                                   ║")
	fmt.Fprintln(os.Stderr, "║    DEACON_FORCE_REPLICATE=1 <command>                           ║")
	fmt.Fprintln(os.Stderr, "╚══════════════════════════════════════════════════════════════════╝")
	fmt.Fprintln(os.Stderr, "")
}

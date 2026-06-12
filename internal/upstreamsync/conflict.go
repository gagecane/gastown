// Conflict detection for upstream merges.
//
// Phase 4 (gu-g5gh). When a fast-forward isn't possible, we need to
// know exactly what would conflict before deciding whether to dispatch
// a polecat for autonomous resolution. This file is the detector:
// given a git working directory and two refs, it returns the list of
// files that would conflict and an estimate of the total hunk count.
//
// Two helpers are exported:
//
//   - DetectConflicts runs `git merge-tree` on the two refs to compute
//     the merged tree without touching the working tree. It is the
//     non-destructive primary detector.
//
//   - ParseConflictMarkers walks the in-progress merge's working tree
//     for files containing conflict markers (<<<<<<<). This is the
//     fallback used by callers that have already initiated a real
//     merge and need to enumerate what's broken.
//
// Both helpers operate on plain string slices and ints — no global
// state, no side effects beyond `git -C <dir>`. The state machine
// transitions in upstream_sync.go consume the results.
//
// Design context: .designs/cv-2s6tq/data.md §"Conflict tracking".
package upstreamsync

import (
	"bufio"
	"fmt"
	"os/exec"
	"strings"
)

// ConflictReport is the structured output of DetectConflicts /
// ParseConflictMarkers. Fields are stable so the JSON shape can be
// embedded into SyncAttempt records and `gt upstream history`.
type ConflictReport struct {
	// Files is the list of conflicted file paths (forward slashes).
	Files []string

	// HunkCount is the total number of conflict hunks across all files.
	// Zero when the detector couldn't enumerate hunks (e.g., merge-tree
	// without --write-tree on older git versions); callers should treat
	// HunkCount == 0 as "unknown" and fall back to file-count thresholds.
	HunkCount int
}

// IsClean reports whether the report contains zero conflicts.
func (r ConflictReport) IsClean() bool {
	return len(r.Files) == 0
}

// DetectConflicts runs `git merge-tree` to compute the merged tree of
// `head` and `upstream` against their merge base, returning the files
// that would conflict and the hunk count.
//
// `head` and `upstream` are git refs (SHAs, branches, or remote refs).
// `gitDir` is the working directory; `git -C <gitDir>` is used so the
// caller's cwd is not affected.
//
// Modern git (2.40+) supports `git merge-tree --write-tree` which emits
// the merged tree SHA and conflict info on stderr. Older git versions
// emit conflict markers inline in stdout. We detect the format by trying
// the modern flag first and falling back to legacy parsing if that
// errors out.
//
// Errors from the underlying `git` invocation are wrapped — the caller
// should treat any error as "unable to determine conflicts; do not
// auto-resolve" and bail to escalation.
func DetectConflicts(gitDir, head, upstream string) (ConflictReport, error) {
	if gitDir == "" {
		return ConflictReport{}, fmt.Errorf("DetectConflicts: empty gitDir")
	}
	if head == "" || upstream == "" {
		return ConflictReport{}, fmt.Errorf("DetectConflicts: empty ref(s) head=%q upstream=%q", head, upstream)
	}

	// Try modern flag first. `git merge-tree --write-tree` uses its exit
	// code to signal the merge result, NOT command failure:
	//   exit 0 → clean merge (output is just the tree OID)
	//   exit 1 → merge conflicts (output lists conflicted files, then an
	//            informational section)
	//   other  → genuine error (e.g. old git: "unknown option --write-tree")
	// Treating exit 1 as a failure (the old `if err == nil` guard) made every
	// real conflict fall through to the legacy path below — which passed
	// `head` as both base and one side, so it could never report a conflict
	// and always returned "clean". That silently routed conflicted syncs into
	// the inline-merge path instead of escalation.
	out, err := exec.Command("git", "-C", gitDir,
		"merge-tree", "--write-tree", "--name-only", head, upstream).CombinedOutput()
	if err == nil {
		// Clean merge: only the tree OID is emitted.
		return parseMergeTreeOutput(string(out)), nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		// Conflicts: parse the conflicted-file list.
		return parseMergeTreeOutput(string(out)), nil
	}

	// Any other exit status means the modern flag is unsupported (old git)
	// or git itself errored. Fall back to the legacy merge-tree, which needs
	// the true merge base as the first argument so the three-way diff can
	// surface conflicts.
	base, berr := mergeBase(gitDir, head, upstream)
	if berr != nil {
		return ConflictReport{}, fmt.Errorf("git merge-tree --write-tree failed (%v) and merge-base fallback failed: %w",
			err, berr)
	}
	legacy, lerr := exec.Command("git", "-C", gitDir,
		"merge-tree", base, head, upstream).CombinedOutput()
	if lerr != nil {
		return ConflictReport{}, fmt.Errorf("git merge-tree (legacy fallback) failed: %w: %s",
			lerr, strings.TrimSpace(string(legacy)))
	}
	return parseLegacyMergeTreeOutput(string(legacy)), nil
}

// mergeBase returns the common ancestor of two refs, used to seed the
// legacy three-way `git merge-tree <base> <branch1> <branch2>` form.
func mergeBase(gitDir, a, b string) (string, error) {
	out, err := exec.Command("git", "-C", gitDir, "merge-base", a, b).Output()
	if err != nil {
		return "", fmt.Errorf("git merge-base %s %s: %w", a, b, err)
	}
	base := strings.TrimSpace(string(out))
	if base == "" {
		return "", fmt.Errorf("git merge-base %s %s: empty result", a, b)
	}
	return base, nil
}

// parseMergeTreeOutput parses the modern `git merge-tree --write-tree
// --name-only` format. The layout is:
//
//	<merged tree OID>
//	<conflicted file>        (zero or more, one per line)
//	...
//	<blank line>             (separator — present only when conflicts exist)
//	<informational messages> (e.g. "Auto-merging x", "CONFLICT (...)")
//
// When there are no conflicts, only the tree OID line is emitted. We read
// the file list and STOP at the first blank line so the trailing
// informational section ("Auto-merging …", "CONFLICT …") is not mistaken
// for conflicted file paths.
//
// We don't get hunk counts from --name-only; HunkCount stays 0. Callers
// that need a precise hunk count should call ParseConflictMarkers after
// the actual merge attempt.
func parseMergeTreeOutput(s string) ConflictReport {
	report := ConflictReport{}
	if s == "" {
		return report
	}
	scanner := bufio.NewScanner(strings.NewReader(s))
	first := true
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if first {
			// First line is the merged tree SHA; skip.
			first = false
			continue
		}
		if line == "" {
			// Blank line separates the file list from the trailing
			// informational section. Everything after it is messages,
			// not file paths.
			break
		}
		report.Files = append(report.Files, line)
	}
	return report
}

// parseLegacyMergeTreeOutput parses old-style merge-tree output: a
// pseudo-diff format with sections per conflicted file. We extract
// just the file names by scanning for the "<file mode> <sha> <path>"
// header lines that introduce each conflict block.
//
// Hunk counting in legacy output is unreliable (the format does not
// directly expose it); we count `+<<<<<<<` markers as a proxy.
func parseLegacyMergeTreeOutput(s string) ConflictReport {
	report := ConflictReport{}
	scanner := bufio.NewScanner(strings.NewReader(s))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	seen := map[string]bool{}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "+<<<<<<<") {
			report.HunkCount++
			continue
		}
		// Header lines look like:
		//   added in remote
		//     their  100644 abcd... path/to/file.go
		// We detect path lines by checking for a tab + file-shaped suffix.
		trim := strings.TrimSpace(line)
		if name := extractLegacyPath(trim); name != "" && !seen[name] {
			seen[name] = true
			report.Files = append(report.Files, name)
		}
	}
	return report
}

// extractLegacyPath pulls the trailing path field out of a legacy
// merge-tree header line (e.g. "their  100644 <sha> internal/cmd/foo.go").
// Returns "" if the line doesn't match the expected shape.
func extractLegacyPath(line string) string {
	if line == "" {
		return ""
	}
	// Need at least mode + sha + path; legacy format always has 4 fields:
	// "their", mode, sha, path (with optional whitespace tab/spaces).
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return ""
	}
	if fields[0] != "their" && fields[0] != "our" && fields[0] != "result" && fields[0] != "base" {
		return ""
	}
	// fields[1] should look like a numeric mode (6 digits, e.g. 100644)
	if len(fields[1]) != 6 || !allDigits(fields[1]) {
		return ""
	}
	return fields[len(fields)-1]
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ParseConflictMarkers walks each file path under `gitDir` and counts
// the conflict markers (<<<<<<< / >>>>>>>). Used after a real merge
// has produced unmerged files, when we want a precise hunk count.
//
// `paths` is the list of files reported as unmerged by `git status`
// or `git diff --name-only --diff-filter=U`. Files not matching the
// conflict shape silently contribute zero — bad inputs degrade
// gracefully rather than fail the call.
func ParseConflictMarkers(gitDir string, paths []string) (ConflictReport, error) {
	if gitDir == "" {
		return ConflictReport{}, fmt.Errorf("ParseConflictMarkers: empty gitDir")
	}
	report := ConflictReport{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		// Use `git show :2:<path>` would only fetch one side; we want the
		// raw conflicted file contents from the working tree, so cat the
		// file via git -C <dir> instead of os.ReadFile to honor the gitDir.
		out, err := exec.Command("git", "-C", gitDir, "cat-file", "-p", ":1:"+p).CombinedOutput()
		_ = out
		_ = err
		// Counting markers in the working-tree file is what matters.
		body, rerr := exec.Command("cat", joinGitPath(gitDir, p)).Output()
		if rerr != nil {
			// Skip unreadable files — caller already knows the file is
			// conflicted from the input list.
			report.Files = append(report.Files, p)
			continue
		}
		hunks := countConflictMarkers(string(body))
		report.Files = append(report.Files, p)
		report.HunkCount += hunks
	}
	return report, nil
}

// joinGitPath joins gitDir + relative path with a forward slash. Kept
// inline so we don't drag in path/filepath for one call site.
func joinGitPath(gitDir, rel string) string {
	if gitDir == "" {
		return rel
	}
	if strings.HasSuffix(gitDir, "/") {
		return gitDir + rel
	}
	return gitDir + "/" + rel
}

// countConflictMarkers counts <<<<<<< occurrences at the start of a
// line; each one corresponds to one hunk. >>>>>>> closes a hunk and is
// not counted separately.
func countConflictMarkers(s string) int {
	count := 0
	scanner := bufio.NewScanner(strings.NewReader(s))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "<<<<<<<") {
			count++
		}
	}
	return count
}

// ListUnmergedPaths returns the files git considers unmerged in the
// given working directory. Equivalent to:
//
//	git -C <gitDir> diff --name-only --diff-filter=U
//
// Returns an empty list if no merge is in progress or no conflicts.
func ListUnmergedPaths(gitDir string) ([]string, error) {
	if gitDir == "" {
		return nil, fmt.Errorf("ListUnmergedPaths: empty gitDir")
	}
	out, err := exec.Command("git", "-C", gitDir,
		"diff", "--name-only", "--diff-filter=U").Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --diff-filter=U: %w", err)
	}
	var paths []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

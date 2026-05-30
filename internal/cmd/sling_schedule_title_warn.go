package cmd

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
)

// titleWordRe extracts whole words from a bead title. We deliberately use
// [A-Za-z0-9_]+ instead of \b\w+\b to keep underscores attached (rig names
// like casc_constructs are single tokens, not casc / constructs).
var titleWordRe = regexp.MustCompile(`[A-Za-z0-9_]+`)

// repoBasenameFromGitURL extracts the repository basename from a git URL,
// stripping the trailing ".git" if present.
//
// Examples:
//
//	"https://github.com/owner/CodegenAgentSchedulerConstructs.git" -> "CodegenAgentSchedulerConstructs"
//	"git@github.com:owner/foo" -> "foo"
//	""                          -> ""
func repoBasenameFromGitURL(gitURL string) string {
	gitURL = strings.TrimSpace(gitURL)
	if gitURL == "" {
		return ""
	}
	// Normalize SCP-style refs ("git@host:owner/repo") into a path-like form so
	// path.Base lifts off the trailing segment.
	if i := strings.LastIndex(gitURL, ":"); i >= 0 && !strings.Contains(gitURL[:i], "/") {
		gitURL = strings.Replace(gitURL, ":", "/", 1)
	}
	base := path.Base(gitURL)
	base = strings.TrimSuffix(base, ".git")
	return base
}

// detectRigMismatchFromTitle scans a bead title for whole-word, case-insensitive
// mentions of registered rigs OTHER than the chosen targetRig. It returns the
// names of the foreign rigs that appear in the title.
//
// Match candidates per rig:
//  1. The rig's name (e.g., "casc_constructs")
//  2. The repo basename derived from rig.GitURL (e.g.,
//     "CodegenAgentSchedulerConstructs" from
//     "https://github.com/owner/CodegenAgentSchedulerConstructs.git")
//
// Word boundaries: title is tokenized via [A-Za-z0-9_]+. A candidate matches
// only if the candidate (also tokenized — single token) appears as a whole
// title token. This avoids "main"-style substring noise.
//
// Empty inputs (no title, no rigs, empty target) return nil. Self-mentions
// (the target rig itself) are filtered out.
//
// Used by scheduleBead to print a soft warning when a bead's title hints
// that the bead may have been filed under the wrong rig's prefix (gu-an4y).
func detectRigMismatchFromTitle(title, targetRig string, rigs []*rig.Rig) []string {
	title = strings.TrimSpace(title)
	if title == "" || len(rigs) == 0 {
		return nil
	}

	// Tokenize title into a set of lowercased whole words.
	tokens := titleWordRe.FindAllString(title, -1)
	if len(tokens) == 0 {
		return nil
	}
	tokenSet := make(map[string]bool, len(tokens))
	for _, tok := range tokens {
		tokenSet[strings.ToLower(tok)] = true
	}

	matched := make(map[string]bool)
	for _, r := range rigs {
		if r == nil || r.Name == "" {
			continue
		}
		if r.Name == targetRig {
			continue
		}
		// Candidate 1: rig name itself.
		if tokenSet[strings.ToLower(r.Name)] {
			matched[r.Name] = true
			continue
		}
		// Candidate 2: repo basename. Only useful if it tokenizes to a single
		// word (most repo names do); skip multi-token names rather than fall
		// into substring matching.
		base := repoBasenameFromGitURL(r.GitURL)
		if base == "" {
			continue
		}
		baseTokens := titleWordRe.FindAllString(base, -1)
		if len(baseTokens) != 1 {
			continue
		}
		if tokenSet[strings.ToLower(baseTokens[0])] {
			matched[r.Name] = true
		}
	}

	if len(matched) == 0 {
		return nil
	}
	out := make([]string, 0, len(matched))
	for name := range matched {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// titleMismatchWarner is the function actually invoked by scheduleBead. Stored
// in a package-level var so tests can stub it out (matches the seam pattern
// used elsewhere in this package, e.g., warnIfKiroPolecatTarget).
var titleMismatchWarner = warnIfTitleMentionsForeignRig

// warnIfTitleMentionsForeignRig emits a stderr warning when the bead's title
// names a registered rig OTHER than targetRig. Best-effort — failures resolving
// the rig list are silently swallowed so this never blocks dispatch.
//
// This is a SOFT warning, not an error. Title mentions can be legitimate
// (epic breakdowns, cross-rig coordination, dependency notes); refusing
// schedule on heuristic alone would block valid work. The hard guards
// (checkSchedulePrefixParity, checkCrossRigGuard) still refuse beads whose
// PREFIX is provably wrong for the target rig.
//
// Fixes one mode of gu-an4y: bootstrap-pattern beads filed under one rig's
// prefix that target work in a sibling rig. The dispatch path correctly
// matches prefix→rig, so the polecat lands in the wrong worktree silently.
// Catching the title mention at schedule time gives operators a chance to
// refile before a polecat slot is wasted.
func warnIfTitleMentionsForeignRig(townRoot, targetRig, beadID, title string) {
	if townRoot == "" || targetRig == "" {
		return
	}
	rigs, err := discoverRigsForTownRoot(townRoot)
	if err != nil || len(rigs) == 0 {
		return
	}
	mismatches := detectRigMismatchFromTitle(title, targetRig, rigs)
	if len(mismatches) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr,
		"%s Bead %s title mentions rig(s) %s but is being scheduled to %q.\n"+
			"   If this work targets a sibling rig, refile under that rig's prefix:\n"+
			"     cd <target-rig> && bd create --title=...\n"+
			"   Otherwise this is a false positive (epic ref, cross-rig note) — proceeding.\n"+
			"   See gu-an4y for context.\n",
		style.Warning.Render("⚠️  cross-rig title mismatch:"),
		beadID, strings.Join(mismatches, ", "), targetRig)
}

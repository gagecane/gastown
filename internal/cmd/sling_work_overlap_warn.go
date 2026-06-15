package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/style"
)

// Work-dedup soft warning (gu-reqfe). Two DIFFERENT beads describing the same
// fix can both be dispatched concurrently with no collision detection — the
// incident was gu-jaxdl (nitro) and gs-asme building the identical 4-file
// suppression fix in parallel; the refinery caught it at merge as SUPERSEDED,
// but a full polecat cycle was wasted on redundant work.
//
// This is the work-content/file-overlap dedup axis, distinct from gu-n3xz9
// (patrol-side alarm-fingerprint dedup, which is about re-dispatching for the
// same ALARM). We cannot know which files a bead will touch before the work
// starts, so the lighter heuristic the bead calls for is title/scope
// similarity against work that is already in flight.
//
// Like the cross-rig title warner (gu-an4y), this is a SOFT advisory, never a
// dispatch blocker: similar titles can be legitimate (sibling subtasks, an
// epic broken into per-file beads). The exit criteria is only that the
// coordinator gets a surfaced warning before two same-fix beads both reach
// working polecats — not that the second sling is refused.

// overlapSimilarityThreshold is the minimum Jaccard similarity (over
// stopword-filtered, lowercased title tokens) at which two beads are treated
// as describing potentially-overlapping work. Tuned to fire on near-identical
// titles (the gu-jaxdl / gs-asme failure mode) while tolerating titles that
// merely share a common domain word or two.
const overlapSimilarityThreshold = 0.6

// overlapStopwords are low-signal tokens stripped before computing title
// similarity so that boilerplate ("fix", "the", "in") does not inflate the
// overlap score. Kept deliberately small — only words that appear across
// unrelated bead titles and carry no scoping information.
var overlapStopwords = map[string]bool{
	"a": true, "an": true, "the": true, "to": true, "in": true, "on": true,
	"of": true, "for": true, "and": true, "or": true, "with": true, "at": true,
	"by": true, "fix": true, "bug": true, "add": true, "update": true,
	"when": true, "is": true, "are": true, "be": true, "not": true,
}

// titleTokenSet tokenizes a bead title into a set of lowercased, stopword-
// filtered words. Reuses titleWordRe ([A-Za-z0-9_]+) so rig-style underscore
// names stay single tokens, matching the cross-rig title warner.
func titleTokenSet(title string) map[string]bool {
	tokens := titleWordRe.FindAllString(strings.TrimSpace(title), -1)
	set := make(map[string]bool, len(tokens))
	for _, tok := range tokens {
		lower := strings.ToLower(tok)
		if overlapStopwords[lower] {
			continue
		}
		set[lower] = true
	}
	return set
}

// titleJaccard returns the Jaccard similarity (|A∩B| / |A∪B|) of two token
// sets. Returns 0 when either set is empty so a degenerate title never warns.
func titleJaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for tok := range a {
		if b[tok] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// overlapCandidate is the minimal shape the detector compares against:
// another bead's ID and title. Kept independent of *beads.Issue so the pure
// detector is trivially unit-testable.
type overlapCandidate struct {
	ID    string
	Title string
}

// detectOverlappingInFlightWork reports candidates whose title is similar
// enough to targetTitle to suggest the two beads describe the same fix. The
// target bead itself (matched by ID) is always excluded. Comparison is the
// stopword-filtered token Jaccard against overlapSimilarityThreshold.
//
// Pure function: no I/O, no globals beyond the stopword/threshold constants —
// the seam for unit tests.
func detectOverlappingInFlightWork(targetID, targetTitle string, candidates []overlapCandidate) []overlapCandidate {
	targetSet := titleTokenSet(targetTitle)
	if len(targetSet) == 0 {
		return nil
	}
	var matches []overlapCandidate
	for _, c := range candidates {
		if c.ID == targetID {
			continue
		}
		if titleJaccard(targetSet, titleTokenSet(c.Title)) >= overlapSimilarityThreshold {
			matches = append(matches, c)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	return matches
}

// workOverlapWarner is the function actually invoked by scheduleBead. Stored
// in a package-level var so tests can stub it out, matching the seam pattern
// used by titleMismatchWarner / warnIfKiroPolecatTarget.
var workOverlapWarner = warnIfOverlappingWorkInFlight

// inFlightBeadsForRig lists beads already hooked or in_progress in the target
// rig's beads database — the work that has (or is about to have) a polecat on
// it. Stored as a var so warnIfOverlappingWorkInFlight stays unit-testable
// without a real town on disk.
var inFlightBeadsForRig = func(townRoot, rigName string) []overlapCandidate {
	rigBeadsDir := doltserver.FindRigBeadsDir(townRoot, rigName)
	if rigBeadsDir == "" {
		return nil
	}
	b := beads.NewWithBeadsDir(townRoot, rigBeadsDir)

	var out []overlapCandidate
	for _, status := range []string{beads.StatusHooked, "in_progress"} {
		issues, err := b.List(beads.ListOptions{Status: status, Priority: -1})
		if err != nil {
			continue // best-effort: a transient list failure must never block dispatch
		}
		for _, iss := range issues {
			if iss == nil {
				continue
			}
			out = append(out, overlapCandidate{ID: iss.ID, Title: iss.Title})
		}
	}
	return out
}

// warnIfOverlappingWorkInFlight emits an advisory when another bead already
// hooked / in_progress in the target rig has a title similar enough to suggest
// the two describe the same fix. Best-effort — any resolution failure is
// silently swallowed so this never blocks dispatch.
//
// Written to STDOUT, not stderr (same rationale as warnIfTitleMentionsForeignRig,
// gu-ceocu): the convoy feeder classifies sling failures on the first stderr
// line, so an advisory on stderr would shadow the bead's real failure line and
// misroute the feeder's disposition. Keeping it on stdout leaves the error
// stream clean for the classifier.
func warnIfOverlappingWorkInFlight(townRoot, rigName, beadID, title string) {
	if townRoot == "" || rigName == "" || strings.TrimSpace(title) == "" {
		return
	}
	candidates := inFlightBeadsForRig(townRoot, rigName)
	if len(candidates) == 0 {
		return
	}
	matches := detectOverlappingInFlightWork(beadID, title, candidates)
	if len(matches) == 0 {
		return
	}
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, fmt.Sprintf("%s (%q)", m.ID, m.Title))
	}
	fmt.Fprintf(os.Stdout,
		"%s Bead %s (%q) has a near-identical title to in-flight work in %q:\n"+
			"     %s\n"+
			"   Two beads describing the same fix may be getting built in parallel —\n"+
			"   the refinery will reject the loser as SUPERSEDED, wasting a polecat cycle.\n"+
			"   If these are the same work, close one before it dispatches; otherwise\n"+
			"   this is a false positive (sibling subtasks / per-file split) — proceeding.\n"+
			"   See gu-reqfe for context.\n",
		style.Warning.Render("⚠️  possible duplicate work:"),
		beadID, title, rigName, strings.Join(ids, "\n     "))
}

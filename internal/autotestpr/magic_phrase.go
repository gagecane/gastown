// Reviewer magic-phrase parsing for mol-pr-feedback-patrol (D9).
//
// Phase 2 task 20 (gu-hqe16). When a reviewer pastes the exact token
// `gt auto-test-pr: pause-rig-7d` into any comment on a
// gt:auto-test-pr-labeled MR, the patrol writes a 7-day pause to that
// rig's state bead. This is the "under fire" fallback — a reviewer who
// wants auto-test PRs to stop doesn't need to find the CLI or the
// config; they paste one line and the next patrol cycle pauses the rig.
//
// The phrase is exact-match by design: near-misses (typos, extra
// whitespace, partial matches) do NOT trigger. This prevents accidental
// pauses from casual comment text that happens to contain fragments of
// the token.
//
// Design context: .designs/auto-test-pr/synthesis.md §D9
package autotestpr

import (
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// MagicPhrase is the exact token a reviewer can paste into any comment
// on a gt:auto-test-pr-labeled MR to trigger a 7-day rig pause.
const MagicPhrase = "gt auto-test-pr: pause-rig-7d"

// MagicPhrasePauseDuration is the pause duration applied when the magic
// phrase is detected (7 days per design D9).
const MagicPhrasePauseDuration = 7 * 24 * time.Hour

// MagicPhraseActor is the actor recorded in the audit log when the
// magic phrase triggers a pause. Identifies the source as the patrol
// (not a human operator or agent).
const MagicPhraseActor = "mol-pr-feedback-patrol"

// ContainsMagicPhrase reports whether body contains the exact magic
// phrase token. The check is line-by-line: the phrase must appear as a
// complete trimmed line to prevent false positives from phrases embedded
// in longer sentences or code blocks.
//
// Matching rules (per acceptance criteria):
//   - Exact match of the trimmed line against MagicPhrase.
//   - Leading/trailing whitespace on the line is ignored.
//   - The phrase embedded in a longer line does NOT match.
//   - Case-sensitive: "GT Auto-Test-PR: Pause-Rig-7d" does not match.
func ContainsMagicPhrase(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == MagicPhrase {
			return true
		}
	}
	return false
}

// ApplyMagicPhrasePause writes a 7-day rig pause to the town-state
// bead on behalf of the feedback patrol. Called by the patrol when
// ContainsMagicPhrase returns true for a comment on a
// gt:auto-test-pr-labeled MR.
//
// Parameters:
//   - b: beads client rooted at the town root.
//   - rigName: the rig to pause (extracted from the MR bead's
//     rig:<name> label).
//   - now: wall-clock time for the pause start (passed in for
//     testability).
//
// Errors are returned verbatim from SetRigPause — the caller (patrol)
// decides whether to retry or skip.
func ApplyMagicPhrasePause(b *beads.Beads, rigName string, now time.Time) error {
	req := PauseRequest{
		Until:  now.Add(MagicPhrasePauseDuration),
		Reason: "reviewer magic phrase: " + MagicPhrase,
		Actor:  MagicPhraseActor,
		Now:    now,
	}
	return SetRigPause(b, rigName, req)
}

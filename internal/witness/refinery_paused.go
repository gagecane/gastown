package witness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/events"
)

// RefineryPausedResult is a single observed refinery_paused event surfaced by
// DetectRefineryPaused. The witness uses these to escalate "queue is paused
// awaiting human direction" separately from STALE_RIG_AGENT (which only fires
// on heartbeat lag) — see gu-t3why / hq:gc-o66p90.
type RefineryPausedResult struct {
	// Rig is the rig whose refinery emitted the pause event (e.g. "gastown_upstream").
	Rig string
	// MRID is the merge-request bead ID being held in queue. May be empty if
	// the emitter could not identify it.
	MRID string
	// Branch is the source branch of the held MR. May be empty.
	Branch string
	// SourceIssue is the issue the MR was working (e.g. "gu-abc123"). May be empty.
	SourceIssue string
	// Reason is the machine-readable reason tag, e.g. "pr_needs_approval".
	Reason string
	// Details is a free-form human-readable diagnostic suitable for the body
	// of an escalation message. May be empty.
	Details string
	// SuspectedConvention is the emitter's best guess at the convention/policy
	// blocking progress (e.g. "github_pr_review", "approved-by:<user> label").
	// May be empty.
	SuspectedConvention string
	// FirstSeen is the timestamp of the first refinery_paused event observed
	// for this (rig, mrID) pair within the lookback window.
	FirstSeen time.Time
	// LastSeen is the timestamp of the most recent refinery_paused event for
	// this (rig, mrID) pair within the lookback window.
	LastSeen time.Time
	// Count is the number of refinery_paused events observed for this
	// (rig, mrID) pair within the lookback window. The refinery emits one per
	// cycle the MR remains held, so a high count + old FirstSeen is the
	// "queue piling up silently" signature.
	Count int
}

// DetectRefineryPausedResult aggregates the per-MR results plus scan-wide errors.
type DetectRefineryPausedResult struct {
	// Scanned is the total number of refinery_paused events read in the lookback window.
	Scanned int
	// Paused is the deduplicated list of paused MRs, keyed on (rig, mrID).
	Paused []RefineryPausedResult
	// Errors captures non-fatal errors encountered scanning the events file.
	Errors []error
}

// DetectRefineryPaused scans ~/gt/.events.jsonl for refinery_paused events
// emitted within the lookback window, deduplicating on (rig, mrID).
//
// Why this exists (gu-t3why / hq:gc-o66p90): the refinery has paths that
// indefinitely defer an MR awaiting human direction (PR needs approving
// review, auto-test-pr awaits an approved-by:<user> label). The pause is
// correct uncertainty handling, but historically it had ZERO observable
// side-effect — no mail, no escalation, no witness alert — so the merge
// queue piled up silently for 15+ hours before an operator noticed. The
// emitting side now logs a structured refinery_paused event; this scan is
// the detection half that gives the witness something to act on.
//
// Behavior:
//   - lookback <= 0 disables the scan (returns empty result). Operators can
//     opt out by setting the corresponding witness config to "0".
//   - rigFilter, when non-empty, restricts results to that rig. Empty matches
//     all rigs (useful for town-wide views).
//   - The events file is read tail-first (best-effort) and parsed as JSONL.
//     Malformed lines are skipped silently — events are best-effort
//     telemetry, not authoritative state.
//   - Results are deduplicated on (rig, mrID): refinery emits one event per
//     poll the MR stays held, so a single pause becomes one entry with
//     FirstSeen/LastSeen/Count, not a flood.
//
// The detector intentionally does NOT send mail or take action itself. The
// CLI layer (patrol scan) decides escalation policy from the structured
// result, mirroring the DetectStaleRigAgentHeartbeats / DetectZombiePolecats
// shape used elsewhere in this package.
func DetectRefineryPaused(workDir, rigFilter string, lookback time.Duration) *DetectRefineryPausedResult {
	result := &DetectRefineryPausedResult{}

	if lookback <= 0 {
		return result
	}

	// Resolve the events file. The events package writes to <townRoot>/.events.jsonl
	// where townRoot is discovered from cwd; tests pass a fully-rooted workDir.
	townRoot := workDir
	eventsPath := filepath.Join(townRoot, events.EventsFile)

	f, err := os.Open(eventsPath) //nolint:gosec // events file path is internally controlled
	if err != nil {
		if os.IsNotExist(err) {
			return result
		}
		result.Errors = append(result.Errors, fmt.Errorf("open events file: %w", err))
		return result
	}
	defer f.Close() //nolint:errcheck // best-effort close

	// Read tail bytes only — this scan is bounded by lookback, but the events
	// file accumulates indefinitely. 1MB covers any realistic short window.
	const tailReadSize int64 = 1 << 20
	info, statErr := f.Stat()
	if statErr != nil {
		result.Errors = append(result.Errors, fmt.Errorf("stat events file: %w", statErr))
		return result
	}
	if info.Size() == 0 {
		return result
	}
	seekTo := info.Size() - tailReadSize
	if seekTo < 0 {
		seekTo = 0
	}
	if _, err := f.Seek(seekTo, io.SeekStart); err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("seek events file: %w", err))
		return result
	}

	scanner := bufio.NewScanner(f)
	// Allow long event lines; default 64KB is plenty but be explicit.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if seekTo > 0 {
		scanner.Scan() // skip potential partial first line at cut point
	}

	cutoff := time.Now().UTC().Add(-lookback)

	// Dedup on (rig, mrID). The refinery emits one event per poll the MR
	// stays held — collapsing them gives the witness one row per held MR.
	type key struct{ rig, mrID string }
	bucket := map[key]*RefineryPausedResult{}

	for scanner.Scan() {
		var event events.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Type != events.TypeRefineryPaused {
			continue
		}
		ts, err := time.Parse(time.RFC3339, event.Timestamp)
		if err != nil {
			continue
		}
		if ts.Before(cutoff) {
			continue
		}

		rig, _ := event.Payload["rig"].(string)
		mrID, _ := event.Payload["mr"].(string)
		// If rig is missing, fall back to the actor prefix ("<rig>/refinery").
		if rig == "" {
			if i := strings.Index(event.Actor, "/"); i > 0 {
				rig = event.Actor[:i]
			}
		}
		if rigFilter != "" && !strings.EqualFold(rig, rigFilter) {
			continue
		}

		result.Scanned++

		k := key{rig: rig, mrID: mrID}
		entry, ok := bucket[k]
		if !ok {
			branch, _ := event.Payload["branch"].(string)
			sourceIssue, _ := event.Payload["source_issue"].(string)
			reason, _ := event.Payload["reason"].(string)
			details, _ := event.Payload["details"].(string)
			suspected, _ := event.Payload["suspected_convention"].(string)
			entry = &RefineryPausedResult{
				Rig:                 rig,
				MRID:                mrID,
				Branch:              branch,
				SourceIssue:         sourceIssue,
				Reason:              reason,
				Details:             details,
				SuspectedConvention: suspected,
				FirstSeen:           ts,
				LastSeen:            ts,
				Count:               1,
			}
			bucket[k] = entry
			continue
		}
		entry.Count++
		if ts.Before(entry.FirstSeen) {
			entry.FirstSeen = ts
		}
		if ts.After(entry.LastSeen) {
			entry.LastSeen = ts
		}
	}
	if err := scanner.Err(); err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("scan events file: %w", err))
	}

	for _, v := range bucket {
		result.Paused = append(result.Paused, *v)
	}
	// Deterministic order: by (Rig, MRID) ascending so test output is stable
	// and human-readable patrol output reads top-to-bottom by rig.
	sortRefineryPausedResults(result.Paused)
	return result
}

// sortRefineryPausedResults sorts in place by (Rig, MRID) ascending.
func sortRefineryPausedResults(items []RefineryPausedResult) {
	// Stable insertion sort — typical N is tiny (one paused MR per rig)
	// so a dependency-free sort keeps this file self-contained.
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			a, b := items[j-1], items[j]
			if (a.Rig < b.Rig) || (a.Rig == b.Rig && a.MRID <= b.MRID) {
				break
			}
			items[j-1], items[j] = b, a
		}
	}
}

package curio

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// This file adds the three live collectors deferred from gu-6s8ao (Phase 1
// shipped only the dead-owner-admission collector in collect.go). Each carries
// an eng-review failure mode that is handled here, in pure helpers, so the
// logic is unit-testable without live I/O:
//
//   (a) bead_merged_not_landed — dual-source (bead Dolt + OTel) closed-merged
//       beads, deduped across the 2026-05-25 instrumentation boundary, with
//       git-ancestry resolved per owning rig.
//   (b) kill_signal_near_dolt — dog-log scan for kill/quit signals near a Dolt
//       PID, tolerant of log rotation / partial reads.
//   (c) alarm_rate_spike — window-bounded per-series event counts from
//       events.jsonl, structured so truncation/rotation can only undercount
//       (never inflate per-minute rates into false candidates).
//
// The rules in rules.go are unchanged — collectors resolve every probe so the
// rules stay pure and replay-gradeable.

// --- (c) alarm_rate_spike: events.jsonl rate collector ---

// eventJSON mirrors the events.jsonl line shape (see internal/events.Event).
// Only the fields the rate rule needs are read.
type eventJSON struct {
	Timestamp string `json:"ts"`
	Type      string `json:"type"`
	Actor     string `json:"actor"`
}

// rateSeriesForEventType maps a raw events.jsonl event type to the rule series
// name the rate rule thresholds are keyed on (rules.go rateThresholds). Event
// types with no rule series return "" and are ignored. bead.open / bead.close
// are intentionally absent: those series are bead-Dolt-sourced, not emitted to
// events.jsonl, so counting them here would be incomplete — they stay out of
// the events collector by design (documented on gu-9bnaw).
func rateSeriesForEventType(eventType string) string {
	switch eventType {
	case "sling":
		return "sling"
	case "done":
		return "done"
	case "mail":
		return "mail"
	case "escalation_sent":
		return "escalation"
	case "scheduler_dispatch_failed":
		return "sched_fail"
	case "dispatch.stuck_agent", "dispatch.stuck":
		return "dispatch.stuck_agent"
	default:
		return ""
	}
}

// CollectEventCounts reads events.jsonl from r and returns per-series counts
// for events whose timestamp falls within [start, end). Only events mapping to
// a known rule series are counted.
//
// Failure-mode handling (eng-review: ".events.jsonl truncation / log rotation
// mid-read → shrunk window inflates per-minute rates → false candidates"):
//
//   - Counts are RAW per-window totals, not rates. The rate rule compares raw
//     counts against daily thresholds, so a truncated/rotated log can only
//     UNDERCOUNT events in the window — it can never inflate a count above what
//     actually occurred, so truncation cannot manufacture a false candidate.
//   - Malformed / partial lines (a torn final line from a concurrent rotate)
//     are skipped silently rather than aborting the scan, so a mid-read
//     rotation degrades to "slightly fewer events counted," not an error.
//   - The loop-breaker excludes Curio's own events (safety invariant 5).
func CollectEventCounts(r io.Reader, start, end time.Time) []SeriesCount {
	counts := map[string]int{}
	sc := bufio.NewScanner(r)
	// Some event lines (large payloads) can exceed bufio's default 64KiB token
	// cap; raise it so a long line is skipped as malformed rather than tearing
	// the scan. 1 MiB is comfortably above any real event line.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e eventJSON
		if err := json.Unmarshal(line, &e); err != nil {
			continue // partial / malformed line (e.g. torn by rotation) — skip
		}
		series := rateSeriesForEventType(e.Type)
		if series == "" {
			continue
		}
		if isCurio(e.Actor) {
			continue // loop-breaker
		}
		ts, err := time.Parse(time.RFC3339, e.Timestamp)
		if err != nil {
			continue // undated line — cannot window it, skip
		}
		if ts.Before(start) || !ts.Before(end) {
			continue // outside [start, end)
		}
		counts[series]++
	}

	out := make([]SeriesCount, 0, len(counts))
	for series, n := range counts {
		out = append(out, SeriesCount{Series: series, Observed: n, FiledBy: "gt"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Series < out[j].Series })
	return out
}

// collectEventCountsFromFile opens the events.jsonl at path and counts the
// window. A missing file yields no counts (not an error — the log only exists
// once events have been written).
func collectEventCountsFromFile(path string, start, end time.Time) ([]SeriesCount, error) {
	f, err := os.Open(path) //nolint:gosec // G304: path constructed from trusted townRoot
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only file
	return CollectEventCounts(f, start, end), nil
}

// --- (b) kill_signal_near_dolt: dog-log collector ---

// killSignalTokens are the kill/quit signal markers we scan dog logs for. The
// gc-wisp-2yc7 anchor is a SIGQUIT to a Dolt sql-server PID.
var killSignalTokens = []string{"SIGQUIT", "SIGKILL", "kill -QUIT", "kill -9", "kill -quit"}

// doltProximityTokens mark a log line as referring to the Dolt server. A line
// is "near a Dolt PID" when it names Dolt's sql-server (and, when known PIDs are
// supplied, references one of them).
var doltProximityTokens = []string{"dolt", "sql-server"}

// lineHasKillSignal reports whether a log line mentions a kill/quit signal.
func lineHasKillSignal(line string) bool {
	lower := strings.ToLower(line)
	for _, tok := range killSignalTokens {
		if strings.Contains(lower, strings.ToLower(tok)) {
			return true
		}
	}
	return false
}

// lineNearDolt reports whether a kill-signal line is directed at a Dolt PID.
// When knownDoltPIDs is non-empty, the line must reference one of those PIDs
// (precise). When empty, a textual reference to Dolt's sql-server suffices
// (the live collector cannot always enumerate Dolt PIDs across rigs, and the
// anchor line names "dolt sql-server pid 12345").
func lineNearDolt(line string, knownDoltPIDs []int) bool {
	lower := strings.ToLower(line)
	mentionsDolt := false
	for _, tok := range doltProximityTokens {
		if strings.Contains(lower, tok) {
			mentionsDolt = true
			break
		}
	}
	if !mentionsDolt {
		return false
	}
	if len(knownDoltPIDs) == 0 {
		return true
	}
	for _, pid := range knownDoltPIDs {
		if lineReferencesPID(line, pid) {
			return true
		}
	}
	return false
}

// lineReferencesPID reports whether line contains pid as a standalone integer
// token (so PID 123 does not match "1234"). Digits are bounded by non-digits.
func lineReferencesPID(line string, pid int) bool {
	needle := strconv.Itoa(pid)
	idx := 0
	for {
		rel := strings.Index(line[idx:], needle)
		if rel < 0 {
			return false
		}
		start := idx + rel
		endPos := start + len(needle)
		beforeOK := start == 0 || !isDigit(line[start-1])
		afterOK := endPos == len(line) || !isDigit(line[endPos])
		if beforeOK && afterOK {
			return true
		}
		idx = start + 1
	}
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// scanLogForKillSignals scans one log's content for kill-signal-near-Dolt lines
// and returns a normalized LogLine per hit, with NearDoltPID pre-resolved.
func scanLogForKillSignals(source, content string, knownDoltPIDs []int) []LogLine {
	var out []LogLine
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !lineHasKillSignal(line) {
			continue
		}
		// Call 1(A) air-gap: a kill-signal line emitted by Curio's own log is
		// suppressed at the collector so Curio cannot detect itself even if its
		// log were scanned (the daemon scans sibling-dog logs, but defending
		// here keeps the loop-breaker uniform across collectors).
		if isCurio(source) {
			continue
		}
		out = append(out, LogLine{
			Source:      source,
			Text:        line,
			NearDoltPID: lineNearDolt(line, knownDoltPIDs),
			FiledBy:     source,
		})
	}
	return out
}

// CollectKillSignals scans the *.log files directly under logDir for kill/quit
// signals near a Dolt PID. knownDoltPIDs may be nil (textual Dolt reference then
// suffices). A missing dir yields no lines (not an error).
//
// Failure-mode handling (eng-review: log rotation mid-read / missing-data):
//
//   - A missing logDir is not an error (logs may not exist yet).
//   - An unreadable individual log file is skipped, not fatal — a file rotated
//     out from under us between ReadDir and ReadFile just contributes nothing.
//   - Scanning is line-oriented and tolerant of a torn final line.
func CollectKillSignals(logDir string, knownDoltPIDs []int) ([]LogLine, error) {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []LogLine
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(logDir, e.Name())) //nolint:gosec // G304: path from trusted townRoot
		if err != nil {
			continue // rotated/unreadable mid-read — skip, not fatal
		}
		source := strings.TrimSuffix(e.Name(), ".log")
		out = append(out, scanLogForKillSignals(source, string(data), knownDoltPIDs)...)
	}
	return out, nil
}

// --- (a) bead_merged_not_landed: dual-source closed-merged bead collector ---

// AncestryResolver resolves whether commit is an ancestor of the owning rig's
// main branch. Injected so the bead collector stays unit-testable without git.
type AncestryResolver func(rig, commit string) bool

// MergedBeadObservation is a closed-"merged" bead observation from one source,
// before ancestry resolution and cross-source dedup. The daemon's live gather
// constructs these from bead Dolt (and, when available, OTel) and hands slices
// of them to ResolveMergedBeads.
type MergedBeadObservation struct {
	ID     string
	Rig    string
	Commit string
}

// ResolveMergedBeads dedups closed-"merged" bead observations across sources
// (bead Dolt history + OTel) and resolves each one's main-ancestry probe.
//
// Failure-mode handling (eng-review: "dual-source reader, window straddles the
// 05-25 instrumentation boundary → double-count or gap"):
//
//   - The two sources are UNIONED (a bead present in only one source — e.g. a
//     pre-05-25 bead absent from OTel — is still observed → no gap).
//   - Observations are DEDUPED by bead ID, first-writer-wins (a bead present in
//     both sources is counted once → no double-count). Callers should pass the
//     more authoritative source first (bead Dolt before OTel).
//
// CommitInMainAncestry is meaningless when Commit is empty; we leave it false
// (the rule treats an empty commit as suspicious on its own).
func ResolveMergedBeads(sources [][]MergedBeadObservation, resolve AncestryResolver) []BeadRecord {
	seen := map[string]bool{}
	var out []BeadRecord
	for _, src := range sources {
		for _, b := range src {
			if b.ID == "" || seen[b.ID] {
				continue
			}
			seen[b.ID] = true
			inAncestry := false
			if b.Commit != "" && resolve != nil {
				inAncestry = resolve(b.Rig, b.Commit)
			}
			out = append(out, BeadRecord{
				ID:                   b.ID,
				Rig:                  b.Rig,
				CloseReason:          "merged",
				Commit:               b.Commit,
				CommitInMainAncestry: inAncestry,
				FiledBy:              "polecat",
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// GitAncestryResolver returns an AncestryResolver backed by
// `git merge-base --is-ancestor <commit> main`, run in each rig's directory
// (rigDirFor resolves a rig name to its directory, typically via
// beads.GetRigDirForName). A nil/empty rigDir lookup or a non-zero git exit
// (commit absent / not an ancestor) all resolve to false.
func GitAncestryResolver(rigDirFor func(rig string) string) AncestryResolver {
	return func(rig, commit string) bool {
		if commit == "" {
			return false
		}
		dir := rigDirFor(rig)
		if dir == "" {
			return false
		}
		cmd := exec.Command("git", "merge-base", "--is-ancestor", commit, "main")
		cmd.Dir = dir
		// Exit 0 → ancestor; exit 1 → not an ancestor; other → error. Any
		// non-nil error is treated as "not landed" (conservative: surfaces the
		// bead as a candidate for human triage rather than silently clearing).
		return cmd.Run() == nil
	}
}

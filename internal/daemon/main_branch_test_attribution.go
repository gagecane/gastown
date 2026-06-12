package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/util"
)

// Structured attribution lines emitted by main_branch_test on a per-rig
// failure section. A downstream daemon dog (Phase 0 task 11 of the
// auto-test-pr design) parses these to resolve the breaking commit back to
// its merge-request bead, which decides whether a SEV-1 auto-revert chain
// fires.
//
// Format (one line each, anywhere in the rig's failure section):
//
//	commit: <40-hex sha>
//	previous_commit: <40-hex sha or "unknown">
//
// Rationale for the "unknown" sentinel: on the very first run for a rig
// (no prior state on disk yet) we still want operators / downstream
// consumers to see a structurally-valid attribution rather than a missing
// field. Consumers that need a real previous SHA must guard against
// "unknown" explicitly.
const (
	attributionCommitPrefix         = "commit: "
	attributionPreviousCommitPrefix = "previous_commit: "
	attributionUnknown              = "unknown"
)

// mainBranchTestStatePath is the JSON file where main_branch_test stores
// the last-known-passing SHA per rig. Used as the source for the
// `previous_commit:` attribution line on the next failure escalation.
//
// Lives under the daemon state directory (alongside daemon.pid, dolt-state.json,
// etc.) so it survives daemon restarts but is treated as runtime state — not
// tracked in git, not synced via Dolt. A lost state file is benign: the next
// successful patrol cycle re-establishes the baseline, and intermediate
// failures emit `previous_commit: unknown` until then.
func mainBranchTestStatePath(townRoot string) string {
	return filepath.Join(townRoot, "daemon", "main_branch_test_state.json")
}

// mainBranchTestState is the on-disk per-rig attribution baseline. Schema is
// kept intentionally narrow: any field added here must be re-derivable from
// git so a stale or absent state file is not load-bearing for correctness.
type mainBranchTestState struct {
	Rigs map[string]rigAttributionEntry `json:"rigs"`
}

type rigAttributionEntry struct {
	// LastPassingSHA is the most recent origin/<default_branch> SHA whose
	// pre-merge gate suite passed in this rig. Used as `previous_commit:`
	// when the next run fails.
	LastPassingSHA string `json:"last_passing_sha"`

	// LastRunAt is the wall-clock time of the last patrol cycle for this rig
	// (pass or fail). Operators use this to spot stalled runners; the daemon
	// itself does not key any logic off it.
	LastRunAt string `json:"last_run_at,omitempty"`

	// ConsecutiveFailures counts back-to-back failing patrol cycles for this
	// rig (hq-6qnct). Reset to 0 on the first pass. A single intermittent flake
	// (fail surrounded by passes) stays at 1 and never reaches the HIGH-
	// escalation watermark; only a sustained red pages the overseer.
	ConsecutiveFailures int `json:"consecutive_failures,omitempty"`

	// LastEscalatedSignature is the failing-gate signature we last paged HIGH
	// on (hq-6qnct dedup). While the same signature stays red we don't re-page
	// every cycle; a new/different signature — or a recovery followed by a
	// re-break — pages again. Cleared on any pass.
	LastEscalatedSignature string `json:"last_escalated_signature,omitempty"`

	// LastFailedSHA is the origin/<default_branch> SHA of the most recent
	// failing cycle (gs-3pe). Once main is confirmed red on a SHA (the streak
	// reaches the flake watermark), re-running the heavyweight gate suite on
	// that SAME SHA every cycle just manufactures host load — the load spike
	// then times out the gates, which reads as a fresh failure, which triggers
	// another re-run (the load-174 estop). Only a NEW commit can turn main
	// green again, so the runner backs off until origin/main advances past this
	// SHA. Cleared on any pass.
	LastFailedSHA string `json:"last_failed_sha,omitempty"`

	// LastFailureWasTimeout records whether the most recent failing cycle was a
	// context-deadline timeout (errGateTimeout) rather than an assertion failure
	// (gs-iz2). A timeout is ambiguous — host-load slowdown vs a genuine hang —
	// so a timeout-red is held to a higher escalation/backoff watermark than an
	// assertion-red (timeoutEscalationThreshold): we demand one extra
	// confirmation cycle before paging HIGH or backing off, which suppresses
	// single/double load-timeout false pages while a sustained timeout still
	// escalates as a real hang. Cleared on any pass.
	LastFailureWasTimeout bool `json:"last_failure_was_timeout,omitempty"`
}

// stateFileMu serializes read/modify/write of the state file. Single-process
// daemon today, but a mutex makes multiple concurrent rig results from a
// future per-rig goroutine fan-out safe-by-default.
var stateFileMu sync.Mutex

// loadMainBranchTestState reads the per-rig attribution state file. A missing
// or unreadable file returns an empty (but valid) state — main_branch_test is
// a runtime patrol, not a source of truth, so a clean slate is the correct
// fallback.
func loadMainBranchTestState(townRoot string) *mainBranchTestState {
	path := mainBranchTestStatePath(townRoot)
	data, err := os.ReadFile(path) //nolint:gosec // G304: path under TownRoot
	if err != nil {
		return &mainBranchTestState{Rigs: map[string]rigAttributionEntry{}}
	}
	var state mainBranchTestState
	if err := json.Unmarshal(data, &state); err != nil {
		// Corrupt state file — start fresh rather than stall the patrol.
		return &mainBranchTestState{Rigs: map[string]rigAttributionEntry{}}
	}
	if state.Rigs == nil {
		state.Rigs = map[string]rigAttributionEntry{}
	}
	return &state
}

// saveMainBranchTestState writes the state file atomically (write to .tmp,
// then rename). A partial write on disk-full / process-kill is therefore
// either the old contents or the new contents — never a half-truncated file
// that loadMainBranchTestState would treat as "corrupt → reset".
func saveMainBranchTestState(townRoot string, state *mainBranchTestState) error {
	if state == nil {
		return nil
	}
	path := mainBranchTestStatePath(townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("ensuring state dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil { //nolint:gosec // G306: state file, not a secret
		return fmt.Errorf("writing tmp state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("renaming tmp state: %w", err)
	}
	return nil
}

// recordAttributionRun updates the in-memory + on-disk state for a single
// rig after a patrol cycle. On `passed=true` the current SHA becomes the
// new last-passing baseline; on `passed=false` we update LastRunAt only —
// the breaking SHA is NOT promoted to last-passing, so a subsequent failure
// keeps pointing back to the genuinely-good baseline.
func recordAttributionRun(townRoot, rigName, currentSHA string, passed bool, now time.Time) {
	stateFileMu.Lock()
	defer stateFileMu.Unlock()

	state := loadMainBranchTestState(townRoot)
	entry := state.Rigs[rigName]
	entry.LastRunAt = now.UTC().Format(time.RFC3339)
	if passed {
		if currentSHA != "" {
			entry.LastPassingSHA = currentSHA
		}
		// A pass clears the flake watermark so a future break re-pages from a
		// clean slate (hq-6qnct), and clears the red-backoff anchor so the next
		// failing SHA gets a fresh run instead of being mistaken for a still-red
		// baseline (gs-3pe).
		entry.ConsecutiveFailures = 0
		entry.LastEscalatedSignature = ""
		entry.LastFailedSHA = ""
		entry.LastFailureWasTimeout = false
	}
	state.Rigs[rigName] = entry
	if err := saveMainBranchTestState(townRoot, state); err != nil {
		// Don't fail the patrol over a state-write hiccup — the next run
		// re-establishes the baseline.
		fmt.Fprintf(os.Stderr, "daemon: failed to save attribution state for rig %s: %v\n", rigName, err)
	}
}

// gateSignatureRe extracts failing gate names from a main_branch_test failure
// error, which renders as `gate "<name>": <err>; gate "<name2>": <err>`.
var gateSignatureRe = regexp.MustCompile(`gate "([^"]+)"`)

// failureSignature derives a stable identifier for a failing cycle so repeat
// failures of the SAME gate(s) can be deduped (hq-6qnct). It prefers the set of
// failing gate names (the meaningful "which test is red" identity, stable
// across cycles and independent of volatile durations/output). When no gate
// name is present (legacy test_command rigs, or fetch/worktree infra errors) it
// falls back to the error's first line with digits zeroed out, so run-to-run
// numeric noise (PIDs, timings, exit codes) doesn't defeat the dedup.
func failureSignature(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if matches := gateSignatureRe.FindAllStringSubmatch(msg, -1); len(matches) > 0 {
		names := make([]string, 0, len(matches))
		seen := map[string]bool{}
		for _, m := range matches {
			if !seen[m[1]] {
				seen[m[1]] = true
				names = append(names, m[1])
			}
		}
		sort.Strings(names)
		return "gates:" + strings.Join(names, ",")
	}
	first := msg
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	first = strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return '0'
		}
		return r
	}, first)
	if len(first) > 200 {
		first = first[:200]
	}
	return "msg:" + first
}

// timeoutEscalationThreshold returns the consecutive-failure watermark a
// TIMEOUT-classified red must reach before main_branch_test pages HIGH or backs
// off (gs-iz2). It is one cycle higher than the assertion watermark: a
// context-deadline timeout is ambiguous (host-load slowdown vs a genuine hang),
// so we demand one extra confirmation cycle. That suppresses the false page a
// one/two-cycle load timeout would otherwise fire during the initial threshold
// runs, while a sustained timeout still escalates as a real hang. The +1 is
// deliberately small: the gs-b1l town-global gate flock already prevents the
// multi-rig pile-on behind the original load storm, and each extra heavy
// confirmation run costs host load — so we buy just enough confirmation to drop
// transient timeouts without re-creating the retry storm gs-3pe fixed.
func timeoutEscalationThreshold(threshold int) int {
	return threshold + 1
}

// recordFailureAndShouldEscalate updates the per-rig flake state for a FAILED
// cycle and reports whether this failure warrants a HIGH escalation (hq-6qnct).
// It escalates only when the gate has failed `threshold` cycles in a row (the
// watermark — single flakes stay silent) AND we have not already paged for this
// exact failure signature (dedup). A timeout-classified failure (isTimeout) is
// held to the higher timeoutEscalationThreshold instead, so a transient
// load-timeout doesn't false-page during the initial threshold runs (gs-iz2).
// The streak and the escalated signature are persisted BEFORE the caller emits
// the escalation, mirroring the file's crash-safety ordering: a single dropped
// page on emit failure is preferable to re-paging the same known-red signature
// on every subsequent cycle.
//
// Best-effort persistence: a state-write error still returns a decision so the
// patrol proceeds (the next cycle re-derives the streak).
func recordFailureAndShouldEscalate(townRoot, rigName, signature, currentSHA string, threshold int, isTimeout bool, now time.Time) (escalate bool, streak int) {
	stateFileMu.Lock()
	defer stateFileMu.Unlock()

	state := loadMainBranchTestState(townRoot)
	entry := state.Rigs[rigName]
	entry.LastRunAt = now.UTC().Format(time.RFC3339)
	entry.ConsecutiveFailures++
	streak = entry.ConsecutiveFailures
	// Anchor the red-backoff SHA (gs-3pe). Recorded on every failure (not just
	// escalations) so a NEW red commit that lands while main is already red —
	// and is therefore deduped out of escalation — still updates the anchor and
	// gets backed off after its first run, instead of re-running every cycle.
	if currentSHA != "" {
		entry.LastFailedSHA = currentSHA
	}
	// Record the classification so shouldBackOffOnRedMain applies the matching
	// (higher, for timeouts) watermark on the next cycle (gs-iz2).
	entry.LastFailureWasTimeout = isTimeout

	watermark := threshold
	if isTimeout {
		watermark = timeoutEscalationThreshold(threshold)
	}
	if streak >= watermark && signature != entry.LastEscalatedSignature {
		escalate = true
		entry.LastEscalatedSignature = signature
	}

	state.Rigs[rigName] = entry
	if err := saveMainBranchTestState(townRoot, state); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: failed to save flake state for rig %s: %v\n", rigName, err)
	}
	return escalate, streak
}

// revertEscalationMarkers restores the given rigs' attribution entries to a
// pre-cycle snapshot (gu-yl2av). It is called when the single batched
// main_branch_test escalation fails AFTER recordFailureAndShouldEscalate has
// already persisted this cycle's per-rig markers (LastEscalatedSignature,
// ConsecutiveFailures, LastFailedSHA, ...). Without this, a failed page would be
// permanently buried: the dedup marker suppresses the re-page and the gs-3pe
// backoff skips re-running the suite. Restoring the snapshot undoes this cycle's
// bookkeeping for exactly the escalated rigs, so the next cycle re-runs and
// re-escalates the still-red main.
//
// A rig absent from the snapshot (its first-ever failing cycle was this one)
// is restored to a zero entry — equivalent to "no prior state", which is what
// the next cycle would have seen had this cycle never run.
//
// Only the escalated rigs are touched: rigs that failed below the watermark
// (no page attempted) keep their accumulating streak so they still reach the
// watermark on schedule, and passing rigs keep their refreshed baseline.
//
// Best-effort persistence, mirroring the rest of this file: a write error is
// logged, not fatal — the next cycle re-derives state from disk regardless.
func revertEscalationMarkers(townRoot string, rigNames []string, preCycle map[string]rigAttributionEntry) {
	if len(rigNames) == 0 {
		return
	}
	stateFileMu.Lock()
	defer stateFileMu.Unlock()

	state := loadMainBranchTestState(townRoot)
	for _, rigName := range rigNames {
		// preCycle[rigName] is the zero rigAttributionEntry when the rig had no
		// prior on-disk state, which is the correct "as if this cycle never ran"
		// restore target.
		state.Rigs[rigName] = preCycle[rigName]
	}
	if err := saveMainBranchTestState(townRoot, state); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: failed to revert escalation markers for %d rig(s): %v\n", len(rigNames), err)
	}
}

// shouldBackOffOnRedMain reports whether main_branch_test should skip the
// heavyweight gate suite for a rig because main is already confirmed red at
// currentSHA (gs-3pe). The runner backs off once the failure streak has reached
// the flake watermark AND origin/main still sits at the SHA we last saw fail —
// i.e. no new commit has landed that could plausibly fix the break. Re-running
// the full act/Docker suite on a known-red SHA only manufactures host load,
// which times out the gates and reads as a fresh failure (the self-reinforcing
// retry storm behind the load-174 estop).
//
// Requiring streak >= threshold preserves the flake-confirmation semantics: the
// first `threshold` failing cycles still run so a single load flake never
// wedges the runner into a permanent skip without ever escalating. A threshold
// < 1 is clamped to 1 so a misconfigured watermark can't disable the backoff.
//
// A timeout-classified red (gs-iz2) uses the higher timeoutEscalationThreshold
// so the backoff does not fire before the timeout watermark is reached — if it
// did, a genuine hang would be backed off after `threshold` cycles and never
// reach the timeout watermark that escalates it, silently masking the hang.
func shouldBackOffOnRedMain(townRoot, rigName, currentSHA string, threshold int) bool {
	if currentSHA == "" {
		return false // No SHA to anchor on — fail open and run.
	}
	if threshold < 1 {
		threshold = 1
	}
	stateFileMu.Lock()
	defer stateFileMu.Unlock()
	entry, ok := loadMainBranchTestState(townRoot).Rigs[rigName]
	if !ok {
		return false
	}
	watermark := threshold
	if entry.LastFailureWasTimeout {
		watermark = timeoutEscalationThreshold(threshold)
	}
	return entry.LastFailedSHA == currentSHA && entry.ConsecutiveFailures >= watermark
}

// readPreviousPassingSHA returns the last-passing SHA for a rig from the
// attribution state file, or attributionUnknown if no prior pass was
// recorded (first run, post-cleanup, or fresh install).
func readPreviousPassingSHA(townRoot, rigName string) string {
	stateFileMu.Lock()
	defer stateFileMu.Unlock()
	state := loadMainBranchTestState(townRoot)
	if entry, ok := state.Rigs[rigName]; ok && entry.LastPassingSHA != "" {
		return entry.LastPassingSHA
	}
	return attributionUnknown
}

// captureRigHeadSHA returns the current `origin/<defaultBranch>` SHA for a
// rig. Runs against the rig's bare repo (the same one runMainBranchTests
// already fetched) so the SHA is exactly what the patrol's worktree was
// created at. Returns ("", err) if the bare repo or ref is missing — the
// caller treats this as "attribution unavailable" and skips emission.
//
// Captures stderr (via CombinedOutput) so a missing-ref / corrupt-repo
// failure surfaces in logs instead of silently producing an empty SHA.
func captureRigHeadSHA(ctx context.Context, rigPath, defaultBranch string) (string, error) {
	bareRepoPath := filepath.Join(rigPath, ".repo.git")
	if _, err := os.Stat(bareRepoPath); err != nil {
		return "", fmt.Errorf("bare repo not accessible: %w", err)
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "origin/"+defaultBranch)
	cmd.Dir = bareRepoPath
	util.SetDetachedProcessGroup(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse origin/%s: %v (%s)",
			defaultBranch, err, strings.TrimSpace(string(out)))
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", fmt.Errorf("git rev-parse origin/%s returned empty SHA", defaultBranch)
	}
	return sha, nil
}

// formatAttributionLines renders the structured `commit:` /
// `previous_commit:` lines that downstream daemon dogs parse out of the
// escalation body. Returns "" when both SHAs are empty so caller-side string
// concatenation does not introduce a stray blank section.
//
// Both lines are always emitted together (never one without the other) so
// consumers can rely on a fixed shape: missing previous_commit means a
// genuinely missing baseline, not a parser difference between code paths.
func formatAttributionLines(commitSHA, previousSHA string) string {
	if commitSHA == "" {
		return ""
	}
	if previousSHA == "" {
		previousSHA = attributionUnknown
	}
	return fmt.Sprintf("%s%s\n%s%s",
		attributionCommitPrefix, commitSHA,
		attributionPreviousCommitPrefix, previousSHA)
}

// CommitAttribution holds the structured commit / previous-commit pair
// extracted from a main_branch_test escalation body.
type CommitAttribution struct {
	// Commit is the SHA on origin/<default_branch> at the time of the
	// failing run. Always populated for attribution-bearing escalations.
	Commit string

	// Previous is the most recent SHA that passed pre-merge gates for the
	// rig before Commit. Set to attributionUnknown ("unknown") when no
	// prior pass was recorded — consumers must guard against that string.
	Previous string
}

// HasCommit reports whether the attribution carries a real (non-empty,
// non-"unknown") Commit SHA. Use this to skip downstream lookups on
// legacy / unattributed escalations rather than threading "" or "unknown"
// into every consumer.
func (a CommitAttribution) HasCommit() bool {
	return a.Commit != "" && a.Commit != attributionUnknown
}

// HasPreviousCommit reports whether the attribution carries a real
// previous-commit SHA. Distinguished from HasCommit because "unknown" is a
// valid first-run state — the consumer can still revert (current SHA is
// known) but cannot verify the revert target.
func (a CommitAttribution) HasPreviousCommit() bool {
	return a.Previous != "" && a.Previous != attributionUnknown
}

// ParseCommitAttribution extracts `commit:` / `previous_commit:` lines from
// an escalation body. Tolerant of leading whitespace and of either line
// appearing in any order; case-sensitive on the prefix to avoid false hits
// from prose like "Commit:" in a stack trace.
//
// Returns a zero-value attribution when no lines match — that is the
// expected shape for legacy escalations from before this attribution
// substrate landed.
func ParseCommitAttribution(description string) CommitAttribution {
	var attr CommitAttribution
	for _, line := range strings.Split(description, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		switch {
		case strings.HasPrefix(trimmed, attributionCommitPrefix):
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, attributionCommitPrefix))
			if val != "" {
				attr.Commit = val
			}
		case strings.HasPrefix(trimmed, attributionPreviousCommitPrefix):
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, attributionPreviousCommitPrefix))
			if val != "" {
				attr.Previous = val
			}
		}
	}
	return attr
}

// FindMRBeadByCommitSHA scans a list of merge-request beads and returns the
// one whose `commit_sha:` field matches commitSHA. Returns nil when no MR
// bead carries that SHA (commit was pushed directly to the default branch,
// or the MR bead was reaped before this lookup ran).
//
// This is the pure / fixture-friendly half of the resolver; the live
// daemon-side wrapper (LookupMRBeadByCommit on *Daemon) shells out to
// `bd list --label=gt:merge-request --json` and feeds the parsed issues
// into this function. Splitting the parse from the I/O keeps the substrate
// unit-testable without a live Dolt server.
func FindMRBeadByCommitSHA(issues []*beads.Issue, commitSHA string) *beads.Issue {
	if commitSHA == "" || commitSHA == attributionUnknown {
		return nil
	}
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		fields := beads.ParseMRFields(issue)
		if fields == nil {
			continue
		}
		if fields.CommitSHA == commitSHA {
			return issue
		}
	}
	return nil
}

// LookupMRBeadByCommit resolves a commit SHA to its MR bead in the given
// rig's bead store. Returns ("", nil) when no MR bead was found — that is
// not an error, just an unattributable commit (e.g. a direct-to-main push
// from a maintainer or a merge whose MR bead was already reaped).
//
// The label filter (`gt:merge-request`) is the canonical surface for MR
// beads in this repo; we deliberately do NOT filter by `gt:auto-test-pr`
// here because that label decision is the consumer's policy, not this
// resolver's. Returning the full bead lets the consumer inspect labels
// directly.
func (d *Daemon) LookupMRBeadByCommit(rigDir, commitSHA string) (string, error) {
	if commitSHA == "" || commitSHA == attributionUnknown {
		return "", nil
	}
	if rigDir == "" {
		return "", fmt.Errorf("rigDir is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	args := []string{
		"list",
		"--label=gt:merge-request",
		"--status=all",
		"--json",
		"--limit=200",
	}
	cmd := exec.CommandContext(ctx, d.bdPath, args...) //nolint:gosec // G204: args constructed internally
	cmd.Dir = rigDir
	cmd.Env = os.Environ()
	setSysProcAttr(cmd)

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("bd list merge-requests: %w", err)
	}

	var issues []*beads.Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return "", fmt.Errorf("parsing bd list output: %w", err)
	}

	if found := FindMRBeadByCommitSHA(issues, commitSHA); found != nil {
		return found.ID, nil
	}
	return "", nil
}

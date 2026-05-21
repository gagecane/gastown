package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
}

// stateFileMu serialises read/modify/write of the state file. Single-process
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
		return fmt.Errorf("marshalling state: %w", err)
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
	if passed && currentSHA != "" {
		entry.LastPassingSHA = currentSHA
	}
	state.Rigs[rigName] = entry
	if err := saveMainBranchTestState(townRoot, state); err != nil {
		// Don't fail the patrol over a state-write hiccup — the next run
		// re-establishes the baseline. Log via the package-default logger
		// is not available here; callers that need observability should
		// wrap this call.
		_ = err
	}
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

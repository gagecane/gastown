// False-deferred bead recovery (gu-wykt).
//
// Sibling pattern to gu-551r/gu-rh0g/gu-treq (false-close class), but for
// deferred state. Beads that have been deferred (during a polecat retry,
// hand-defer, or because their dispatching polecat ran `gt done --status
// DEFERRED`) stay `deferred` even after their underlying work lands on the
// rig's default branch — usually because a sibling fix shipped, the work was
// re-filed under a different bead, or someone cherry-picked the change.
//
// The defense-in-depth chain (gu-551r/gu-rh0g/gu-treq) all guard `gt done`
// and the polecat close path. None of those code paths execute for a deferred
// bead — by definition, no polecat is working on it. So a deferred bead whose
// work somehow lands becomes a zombie: the bead-cited commit IS on main, but
// no closure path fires. Mayor's nightly sweep finds these as "shipped but
// stuck deferred for 25h+/197h."
//
// This patrol scans status=deferred beads, runs `git log <default> --grep=<id>`
// against the rig bare repo (.repo.git), and if a citing commit exists,
// auto-closes the bead with a clear reason and a dedup label so we don't
// re-close it on every patrol cycle. The cited-commit guard is mechanical:
// the commit message must contain the bead ID — same rule that gu-551r's
// verifyCommitReferencesBead enforces on the polecat side.

package witness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/workspace"
)

// FalseDeferredRecoveredLabelPrefix is the label prefix written on a bead after
// the false-deferred recovery patrol auto-closes it. The full label is
// `false-deferred-recovered:<short-sha>`. Re-ticks see the prefix and skip the
// bead — but since the bead is also closed by then, the dedup is belt-and-
// suspenders: it preserves the audit trail for operators inspecting why the
// bead transitioned without a polecat ever touching it.
const FalseDeferredRecoveredLabelPrefix = "false-deferred-recovered"

// DeferredBeadRecovery records a single false-deferred recovery action.
type DeferredBeadRecovery struct {
	// BeadID is the bead that was found in deferred state.
	BeadID string
	// CitedCommitSHA is the on-mainline commit whose message references
	// BeadID. Empty when no cited commit was found (Action="skip-no-commit").
	CitedCommitSHA string
	// Action is the outcome: "closed", "skip-no-commit",
	// "skip-already-labeled", "skip-assigned", "skip-bare-repo-missing",
	// "close-failed", or "skip-grep-error".
	Action string
	// Error captures non-fatal errors so the caller can surface them without
	// aborting the rest of the scan.
	Error error
}

// DiscoverDeferredButShippedResult aggregates per-bead recovery results plus
// scan-wide errors.
type DiscoverDeferredButShippedResult struct {
	// Checked is the number of deferred beads scanned.
	Checked int
	// Recovered is the per-bead detection log. Includes both auto-closes
	// AND skips, so operators can audit the scan without re-running it.
	Recovered []DeferredBeadRecovery
	// Errors collects scan-wide failures (bd list error, JSON parse error,
	// etc.) that prevented the scan from running normally.
	Errors []error
}

// shippedScanConfig parameterizes the shared citing-commit scan
// (discoverShippedBeads) for a given bead status. Two callers configure it:
// the deferred-state scan (DiscoverDeferredButShipped, gu-wykt) and the
// re-dispatch zombie-loop scan (DiscoverOpenButShipped, gu-y55ux).
type shippedScanConfig struct {
	// status is the `bd list --status=<status>` filter the scan lists.
	status string
	// requireUnassigned, when true, restricts the scan to beads with an empty
	// assignee. The open-bead scan sets this so a bead a polecat is actively
	// hooked to (or an operator deliberately re-assigned) is never auto-closed
	// from under a live worker — a re-dispatchable zombie is, by definition,
	// unassigned (dispatch requires an empty assignee).
	requireUnassigned bool
	// skipIfLabeled, when true, skips beads already carrying the
	// false-deferred-recovered:* dedup label. The deferred scan sets this so a
	// recovered-then-manually-re-deferred bead isn't fought every tick. The
	// open scan sets it FALSE: a labeled bead that is open again means a prior
	// recovery was undone by a re-open, so it must be re-closed to break the
	// loop (close is idempotent; the citing commit is authoritative proof).
	skipIfLabeled bool
	// closeReason builds the bd close reason from the short SHA of the citing
	// commit.
	closeReason func(shortSHA string) string
}

// DiscoverDeferredButShipped scans status=deferred beads, looks for an on-
// mainline commit that cites each bead's ID, and auto-closes any bead whose
// work has shipped. Mirrors the structure of DiscoverPostHocCompletions
// (gu-jr8) — list candidates, verify against git, close with a clear reason.
//
// Preconditions for auto-closure (ALL must hold):
//   - Bead status is `deferred`.
//   - Bead does NOT already carry the `false-deferred-recovered:*` label
//     (re-tick dedup — the label survives even after close).
//   - The rig's bare repo at <town>/<rig>/.repo.git exists.
//   - `git log <defaultBranch> --grep=<beadID> --format=%H -n 1` returns a
//     non-empty SHA.
//
// When all preconditions hold: close the bead with reason
// `Work shipped at <sha> while deferred — auto-recovered (gu-wykt)`, and
// add the `false-deferred-recovered:<short-sha>` label.
//
// Why bare repo and not a worktree: deferred beads have no live polecat
// (definition of deferred), so there's no working directory to query. The
// bare repo at .repo.git mirrors all branches (gu-uk8f) and is the canonical
// place to ask "has this bead's ID been cited on origin/<default>?"
//
// Why --grep and not a stricter check: gu-551r's verifyCommitReferencesBead
// already enforces "commit message must contain bead ID" on the polecat side,
// so any commit cited here was either (a) authored by a polecat for a
// different bead that happens to mention this bead, or (b) authored by a
// crew member as a manual ship. Case (b) is the common false-deferred path
// (sibling fix, re-filed bead). Case (a) is the false-close pattern that
// gu-551r explicitly prevents on the polecat side; running --grep on origin/
// main is safe because gu-551r ensures the commit literally cites the bead.
func DiscoverDeferredButShipped(bd *BdCli, workDir, rigName string) *DiscoverDeferredButShippedResult {
	return discoverShippedBeads(bd, workDir, rigName, shippedScanConfig{
		status:            "deferred",
		requireUnassigned: false,
		skipIfLabeled:     true,
		closeReason: func(shortSHA string) string {
			return fmt.Sprintf(
				"Work shipped at %s while deferred — auto-recovered (gu-wykt)",
				shortSHA)
		},
	})
}

// DiscoverOpenButShipped scans status=open, UNASSIGNED beads for an on-mainline
// commit that cites each bead's ID and auto-closes any whose work has already
// shipped. It is the re-dispatch zombie-loop fix (gu-y55ux): the sibling of
// DiscoverDeferredButShipped for the OPEN state.
//
// Why this scan is needed. A work bead whose fix already landed gets closed
// `no-changes` by a polecat, but a reaper/patrol later flips it back to
// status=open (e.g. dead-agent-wisp reap, stale-park unblock, convoy re-open
// after a reopen event). bd CLEARS close_reason on re-open, so the dispatch
// chokepoints (isScheduledWorkBeadReady, the convoy feed) see a plain open
// bead with no surviving "no-changes" marker and re-dispatch it every cycle —
// spawning a polecat that re-closes no-changes, ad infinitum. The same loop
// hits a bead deferred without a defer_until (released straight back to open).
// Both reduce to "an open bead whose fix is already an ancestor of origin/main
// keeps getting dispatched." Three confirmed instances: gu-ecstl (4×),
// gu-9qz45, gu-0dnrh.
//
// Closing the bead at the source — once its work is proven on mainline by a
// citing commit — breaks the loop without putting a git query on the dispatch
// hot path: the witness patrol already runs DiscoverDeferredButShipped, and
// this is the same mechanical detector over a different status filter.
//
// Conservatism (differs from the deferred scan):
//   - requireUnassigned=true: a bead a polecat is actively hooked to, or one an
//     operator deliberately re-assigned, is never auto-closed. A genuinely
//     re-dispatchable zombie is unassigned by definition (dispatch requires an
//     empty assignee), so this loses no real coverage while guaranteeing we
//     never yank work from under a live worker.
//   - skipIfLabeled=false: unlike the deferred scan, a re-opened bead that we
//     already recovered (and labeled) MUST be re-closed — the re-open is
//     exactly the loop we are breaking. Close is idempotent and the citing
//     commit is authoritative, so re-closing a re-opened zombie is safe.
func DiscoverOpenButShipped(bd *BdCli, workDir, rigName string) *DiscoverDeferredButShippedResult {
	return discoverShippedBeads(bd, workDir, rigName, shippedScanConfig{
		status:            "open",
		requireUnassigned: true,
		skipIfLabeled:     false,
		closeReason: func(shortSHA string) string {
			return fmt.Sprintf(
				"Work shipped at %s — auto-closed to break re-dispatch zombie loop (gu-y55ux)",
				shortSHA)
		},
	})
}

// discoverShippedBeads is the shared citing-commit scan behind
// DiscoverDeferredButShipped (gu-wykt) and DiscoverOpenButShipped (gu-y55ux).
// It lists beads of cfg.status, and for each one with an on-mainline commit
// citing its ID, closes it with cfg.closeReason and stamps the dedup label.
func discoverShippedBeads(bd *BdCli, workDir, rigName string, cfg shippedScanConfig) *DiscoverDeferredButShippedResult {
	result := &DiscoverDeferredButShippedResult{}

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}

	// Default branch for this rig (origin/<default> is the lookup target).
	defaultBranch := "main"
	if rigCfg, cfgErr := rig.LoadRigConfig(filepath.Join(townRoot, rigName)); cfgErr == nil && rigCfg.DefaultBranch != "" {
		defaultBranch = rigCfg.DefaultBranch
	}

	// Bare repo path is the rig's <town>/<rig>/.repo.git mirror. If it's
	// absent, this scan can't run — skip without error. The next patrol
	// cycle retries.
	bareRepoPath := filepath.Join(townRoot, rigName, ".repo.git")
	if _, statErr := os.Stat(bareRepoPath); statErr != nil {
		// Surface as a single "bare-repo-missing" entry rather than a hard
		// error — caller logs and continues.
		result.Errors = append(result.Errors,
			fmt.Errorf("rig bare repo missing at %s: %w", bareRepoPath, statErr))
		return result
	}
	bareGit := git.NewGitWithDir(bareRepoPath, "")

	// List candidate beads of the configured status. --limit=0 mirrors
	// DetectStaleInProgressBeads so large rigs see all candidates per cycle.
	output, err := bd.Exec(workDir, "list", "--status="+cfg.status, "--json", "--limit=0")
	if err != nil {
		result.Errors = append(result.Errors,
			fmt.Errorf("listing %s beads: %w", cfg.status, err))
		return result
	}
	if output == "" {
		return result
	}

	type beadRow struct {
		ID       string   `json:"id"`
		Labels   []string `json:"labels"`
		Assignee string   `json:"assignee"`
	}
	var beadList []beadRow
	if err := json.Unmarshal([]byte(output), &beadList); err != nil {
		result.Errors = append(result.Errors,
			fmt.Errorf("parsing %s beads JSON: %w", cfg.status, err))
		return result
	}

	for _, b := range beadList {
		if b.ID == "" {
			continue
		}
		result.Checked++

		// Assignee guard (open scan only). Never auto-close a bead a polecat is
		// actively hooked to or an operator re-assigned — a re-dispatchable
		// zombie is unassigned by definition.
		if cfg.requireUnassigned && strings.TrimSpace(b.Assignee) != "" {
			result.Recovered = append(result.Recovered, DeferredBeadRecovery{
				BeadID: b.ID,
				Action: "skip-assigned",
			})
			continue
		}

		// Dedup: skip beads that already carry our recovery label. We close
		// the bead AND add the label, so this guard catches the rare case
		// where the close failed but the label was applied (or the bead was
		// re-deferred manually after close). Disabled for the open scan: a
		// re-opened labeled bead is the very loop we are breaking, so it must
		// be re-closed.
		if cfg.skipIfLabeled && hasFalseDeferredRecoveredLabel(b.Labels) {
			result.Recovered = append(result.Recovered, DeferredBeadRecovery{
				BeadID: b.ID,
				Action: "skip-already-labeled",
			})
			continue
		}

		// Look for a commit on origin/<default> that cites this bead ID.
		// We scan origin/<default> rather than the local default branch
		// because the bare repo's "live" branch is the remote tracking ref
		// (the rig polecat worktrees push here; refinery merges land here).
		sha, grepErr := findCitingCommit(bareGit, defaultBranch, b.ID)
		if grepErr != nil {
			// Transient git error — log per-bead and continue. Don't abort
			// the whole scan.
			result.Recovered = append(result.Recovered, DeferredBeadRecovery{
				BeadID: b.ID,
				Action: "skip-grep-error",
				Error:  grepErr,
			})
			continue
		}
		if sha == "" {
			// No citing commit on mainline — bead has real work outstanding.
			result.Recovered = append(result.Recovered, DeferredBeadRecovery{
				BeadID: b.ID,
				Action: "skip-no-commit",
			})
			continue
		}

		// Found a citing commit. Close with a clear reason citing the SHA,
		// then add the dedup label. Apply the label even if close fails:
		// the label is the "we observed this state" marker, independent of
		// bd close success.
		recovery := DeferredBeadRecovery{
			BeadID:         b.ID,
			CitedCommitSHA: sha,
			Action:         "closed",
		}

		closeReason := cfg.closeReason(shortShaTruncate(sha))
		if closeErr := bd.Run(workDir, "close", b.ID, "-r", closeReason); closeErr != nil {
			recovery.Action = "close-failed"
			recovery.Error = fmt.Errorf("closing bead %s: %w", b.ID, closeErr)
		}

		// Apply the dedup label even on close-failure so the next tick doesn't
		// re-issue the close attempt indefinitely. Operators remove the label
		// to force a retry once the close path is unblocked.
		label := fmt.Sprintf("%s:%s", FalseDeferredRecoveredLabelPrefix, shortShaTruncate(sha))
		if labelErr := bd.Run(workDir, "update", b.ID, "--add-label="+label); labelErr != nil {
			if recovery.Error == nil {
				recovery.Error = fmt.Errorf("add label: %w", labelErr)
			} else {
				recovery.Error = fmt.Errorf("%w; also add label: %v", recovery.Error, labelErr)
			}
		}

		result.Recovered = append(result.Recovered, recovery)
	}

	return result
}

// findCitingCommit runs `git log origin/<defaultBranch> --grep=<beadID>
// --format=%H -n 1 --fixed-strings` against the given bare repo and returns
// the SHA of the most recent commit citing the bead, or "" if none.
//
// --fixed-strings is critical: bead IDs like `gu-syn-4jbgs` contain hyphens
// which are not regex metacharacters but a literal-only matcher is the safer
// default and avoids ever having a future bead ID expanding into a regex
// expression that matches unrelated commits.
//
// We try `origin/<default>` first because the bare repo at .repo.git is the
// rig's mirror — that's where polecat pushes land and refinery merges resolve.
// Fall back to a plain `<default>` ref name if the origin-prefixed form
// returns an unknown-revision error (small rigs occasionally have a
// non-standard remote layout).
//
// Exposed as a package-level var so tests can stub it without invoking a
// real git binary.
var findCitingCommit = _findCitingCommit

func _findCitingCommit(bareGit *git.Git, defaultBranch, beadID string) (string, error) {
	if beadID == "" {
		return "", fmt.Errorf("internal error: bead ID empty")
	}
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	// Primary: origin/<default>. The bare repo at .repo.git uses
	// refs/remotes/origin/<default> as the canonical "shipped" pointer.
	out, err := runGitGrep(bareGit, "origin/"+defaultBranch, beadID)
	if err == nil {
		return strings.TrimSpace(out), nil
	}

	// Fallback: bare ref name. A bare repo without a configured `origin`
	// remote keeps the default branch at refs/heads/<default>.
	out2, err2 := runGitGrep(bareGit, defaultBranch, beadID)
	if err2 != nil {
		// Both forms failed. Surface the primary error — it's the more
		// informative one for the common-case repo layout.
		return "", err
	}
	return strings.TrimSpace(out2), nil
}

// runGitGrep runs the actual `git log --grep` invocation. Factored out so the
// fallback path in _findCitingCommit doesn't duplicate the argv.
//
// The git wrapper exposes neither `Log(--grep)` nor a generic Log() with
// arbitrary args — but RecentCommits and LogOneline both use the same
// unexported `run` method, so we model after them by going through the
// wrapper's command-execution path. Since none of the existing public
// helpers accept arbitrary log flags, this calls a thin custom helper.
//
// Exposed as a package-level var purely so tests can mock it without spawning
// real git subprocesses.
var runGitGrep = _runGitGrep

func _runGitGrep(bareGit *git.Git, ref, beadID string) (string, error) {
	return bareGit.LogGrepFixedHead(ref, beadID)
}

// shortShaTruncate returns the first 12 characters of sha, or sha itself if
// shorter. Used for both close-reason text and the dedup label suffix; bd's
// label charset accepts a-z0-9 so a hex prefix is safe.
func shortShaTruncate(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// hasFalseDeferredRecoveredLabel reports whether the label list contains any
// label with the FalseDeferredRecoveredLabelPrefix. The full label is
// `<prefix>:<short-sha>`; we match on prefix-with-colon to avoid false
// positives from labels that share the prefix but aren't recovery markers.
func hasFalseDeferredRecoveredLabel(labels []string) bool {
	prefix := FalseDeferredRecoveredLabelPrefix + ":"
	for _, l := range labels {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

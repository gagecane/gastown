package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// branchInfo holds parsed branch information.
type branchInfo struct {
	Branch string // Full branch name
	Issue  string // Issue ID extracted from branch
	Worker string // Worker name (polecat name)
}

// issuePattern matches issue IDs in branch names (e.g., "gt-xyz" or "gt-abc.1")
var issuePattern = regexp.MustCompile(`([a-z]+-[a-z0-9]+(?:\.[0-9]+)?)`)

// parseBranchName extracts issue ID and worker from a branch name.
// Supports formats:
//   - polecat/<worker>/<issue>                    → issue=<issue>, worker=<worker>
//   - polecat/<worker>/<issue>--<timestamp>       → issue=<issue>, worker=<worker> (current form)
//   - polecat/<worker>/<issue>@<timestamp>        → issue=<issue>, worker=<worker> (legacy form)
//   - polecat/<worker>-<timestamp>                → issue="", worker=<worker> (no issue)
//   - <issue>                                     → issue=<issue>, worker=""
//
// The issue/timestamp separator changed from "@" to "--" on 2026-04-28 for
// compatibility with git.amazon.com pre-receive hooks that reject "@" in
// refs. Both forms are recognized here so the merge queue, `gt done`, and
// refinery post-merge cleanup record the un-suffixed bead ID in the MR's
// source_issue field (gu-y2w). Without stripping "--<ts>", the bug bead
// stays HOOKED after merge because `bd close gu-aei--moiitf15` fails.
func parseBranchName(branch string) branchInfo {
	info := branchInfo{Branch: branch}

	// Try polecat/<worker>/<issue> with optional --<timestamp> or @<timestamp> suffix.
	if strings.HasPrefix(branch, constants.BranchPolecatPrefix) {
		parts := strings.SplitN(branch, "/", 3)
		if len(parts) == 3 {
			info.Worker = parts[1]
			info.Issue = stripBranchTimestampSuffix(parts[2])
			return info
		}
		// Modern polecat branch format: polecat/<worker>-<timestamp>
		// The second part is "worker-timestamp", not an issue ID.
		// Don't try to extract an issue ID - gt done will use hook_bead fallback.
		if len(parts) == 2 {
			// Extract worker name from "worker-timestamp" format
			workerPart := parts[1]
			if dashIdx := strings.LastIndex(workerPart, "-"); dashIdx > 0 {
				info.Worker = workerPart[:dashIdx]
			} else {
				info.Worker = workerPart
			}
			// Explicitly don't set info.Issue - let hook_bead fallback handle it
			return info
		}
	}

	// Try to find an issue ID pattern in the branch name
	// Common patterns: prefix-xxx, prefix-xxx.n (subtask)
	if matches := issuePattern.FindStringSubmatch(branch); len(matches) > 1 {
		info.Issue = matches[1]
	}

	return info
}

// resolveBranchSourceIssue validates a branch-parsed source issue id and,
// when it doesn't exist, falls back to the bead hooked to the current agent.
//
// gu-4ngu0: parseBranchName greedily matches the first "word-word" token, so
// non-polecat branches can yield a phantom id (e.g. "dolt-max" from
// "fix/dolt-max-connections-1000"). This guards against recording that phantom
// as the MR's source_issue. exists reports whether the parsed id is a real
// bead; hookedFallback returns the agent's hooked bead id (or "" if none).
// Returns the resolved id, or an error when neither the parsed id nor a hooked
// fallback resolves to a real bead.
func resolveBranchSourceIssue(parsed string, exists func(string) bool, hookedFallback func() string) (string, error) {
	if exists(parsed) {
		return parsed, nil
	}
	if hooked := hookedFallback(); hooked != "" {
		return hooked, nil
	}
	return "", fmt.Errorf("source issue %q does not exist and no hooked bead fallback available", parsed)
}

// stripBranchTimestampSuffix removes the timestamp suffix from the issue
// portion of a polecat branch name. The separator is "--" (current) or "@"
// (legacy, pre-2026-04-28). When both are present, "--" takes precedence to
// match parseFreshBranchName in internal/polecat/session_manager.go.
//
// Examples:
//
//	"gu-aei--moiitf15" -> "gu-aei"
//	"gt-abc@mk123456"  -> "gt-abc"
//	"gt-abc.1"         -> "gt-abc.1" (no suffix)
func stripBranchTimestampSuffix(issue string) string {
	if idx := strings.Index(issue, "--"); idx > 0 {
		return issue[:idx]
	}
	if idx := strings.Index(issue, "@"); idx > 0 {
		return issue[:idx]
	}
	return issue
}

// wipCheckpointTipWarning returns a warning message when the given branch-tip
// commit message is a `WIP: checkpoint (auto)` checkpoint, or "" otherwise.
// gu-weo4x criterion 3: `gt mq submit` warns (does not block) when the tip
// matches /^WIP: checkpoint/ so the worker squashes or recommits before the
// refinery refuses the all-WIP merge. Extracted as a pure helper for testing.
func wipCheckpointTipWarning(tipMessage string) string {
	subj := strings.SplitN(strings.TrimSpace(tipMessage), "\n", 2)[0]
	if !strings.HasPrefix(subj, "WIP: checkpoint") {
		return ""
	}
	return fmt.Sprintf("branch tip is a WIP checkpoint commit (%q).\n"+
		"  The refinery refuses to merge branches whose only commits are WIP checkpoints.\n"+
		"  Add a real commit (or squash the WIP) before this MR can land.", subj)
}

func runMqSubmit(cmd *cobra.Command, args []string) error {
	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Find current rig
	rigName, _, err := findCurrentRig(townRoot)
	if err != nil {
		return err
	}

	// Initialize git for the current directory
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// When gt is invoked via shell alias (cd ~/gt && gt), cwd is the town
	// root, not the polecat's worktree. Reconstruct actual path.
	if cwd == townRoot {
		// Gate polecat cwd switch on GT_ROLE: coordinators may have stale GT_POLECAT.
		isPolecat := false
		if role := os.Getenv("GT_ROLE"); role != "" {
			parsedRole, _, _ := parseRoleString(role)
			isPolecat = parsedRole == RolePolecat
		} else {
			isPolecat = os.Getenv("GT_POLECAT") != ""
		}
		if polecatName := os.Getenv("GT_POLECAT"); polecatName != "" && rigName != "" && isPolecat {
			polecatClone := filepath.Join(townRoot, rigName, "polecats", polecatName, rigName)
			if _, err := os.Stat(polecatClone); err == nil {
				cwd = polecatClone
			} else {
				polecatClone = filepath.Join(townRoot, rigName, "polecats", polecatName)
				if _, err := os.Stat(filepath.Join(polecatClone, ".git")); err == nil {
					cwd = polecatClone
				}
			}
		} else if crewName := os.Getenv("GT_CREW"); crewName != "" && rigName != "" {
			crewClone := filepath.Join(townRoot, rigName, "crew", crewName)
			if _, err := os.Stat(crewClone); err == nil {
				cwd = crewClone
			}
		}
	}

	g := git.NewGit(cwd)

	// Get current branch
	branch := mqSubmitBranch
	if branch == "" {
		branch, err = g.CurrentBranch()
		if err != nil {
			return fmt.Errorf("getting current branch: %w", err)
		}
	}

	// gu-ge1s: Reject the literal "HEAD" from detached-HEAD worktrees before
	// it flows into refspec construction, MR bead fields, or "gt mq submit"'s
	// dedup path. Refs/heads/HEAD pollution and `cannot determine source
	// issue from branch HEAD` loops both trace back to letting this value
	// propagate.
	if branch == "" || branch == "HEAD" {
		if detached, detErr := g.IsDetachedHEAD(); detErr == nil && detached {
			return fmt.Errorf("cannot submit from detached HEAD: no named branch to push\n" +
				"Create a branch first (git checkout -b <name>) or pass --branch explicitly")
		}
		return fmt.Errorf("refusing to submit with branch=%q (no named branch detected); use --branch to specify", branch)
	}

	// Get configured default branch for this rig. Source of truth is rig config
	// default_branch; when unreadable/empty, fall back to the repo's actual
	// default (origin/HEAD), not a hardcoded "main" — the latter misroutes MR
	// targets in a "mainline"-default repo (gu-wcb37).
	defaultBranch := resolveRigDefaultBranch(townRoot, rigName, g)

	if branch == defaultBranch || branch == "master" {
		return fmt.Errorf("cannot submit %s/master branch to merge queue", defaultBranch)
	}

	// gu-weo4x: warn when the branch tip is a `WIP: checkpoint (auto)` commit.
	// checkpoint_dog no longer commits to the branch, but a tip can still be a
	// WIP from older sessions or a manual checkpoint. Submitting it would make
	// the refinery refuse the merge (all-WIP) or, if a real commit is buried
	// underneath, land a confusing squash message. Surface it loudly so the
	// worker squashes or recommits before the refinery does. Warning only —
	// never blocks the submit.
	if tipSubject, tipErr := g.GetBranchCommitMessage(branch); tipErr == nil {
		if w := wipCheckpointTipWarning(tipSubject); w != "" {
			style.PrintWarning("%s", w)
		}
	}

	// Parse branch info
	info := parseBranchName(branch)

	// Override with explicit flags
	issueID := mqSubmitIssue
	if issueID == "" {
		issueID = info.Issue
	}
	worker := info.Worker

	if issueID == "" {
		return fmt.Errorf("cannot determine source issue from branch '%s'; use --issue to specify", branch)
	}

	// Initialize beads for looking up source issue
	bd := beads.New(cwd)

	// gu-4ngu0: When the source issue was derived from branch-name parsing
	// (not an explicit --issue flag), validate that it is a real bead before
	// recording it as the MR's source_issue. parseBranchName greedily matches
	// the FIRST "word-word" token, so non-polecat branches like
	// "fix/dolt-max-connections-1000" or "sync/upstream-main-gu-t6zhb" yield a
	// phantom id ("dolt-max" / "upstream-main") rather than the real bead. A
	// phantom source_issue breaks the back-link and leaves the bead HOOKED
	// after merge. If the parsed id doesn't exist, fall back to the bead
	// hooked to this agent (the same fallback gt done uses); if that also
	// fails, fail loudly with the actionable message instead of proceeding
	// with a bogus id.
	if mqSubmitIssue == "" && issueID != "" {
		fromBranch := issueID
		issueID, err = resolveBranchSourceIssue(
			issueID,
			func(id string) bool { _, e := bd.Show(id); return e == nil },
			func() string { return findHookedBeadForAgent(bd, detectSender()) },
		)
		if err != nil {
			return fmt.Errorf("source issue %q parsed from branch '%s' does not exist and no bead is hooked to this agent; use --issue to specify", fromBranch, branch)
		}
		if issueID != fromBranch {
			fmt.Printf("  Source issue %q parsed from branch not found; using hooked bead %s\n", fromBranch, issueID)
		}
	}

	// Determine target branch
	// Priority: explicit --epic > formula_vars base_branch > integration branch auto-detect > rig default.
	target := defaultBranch
	if mqSubmitEpic != "" {
		// Explicit --epic flag: read stored branch name, fall back to template
		rigPath := filepath.Join(townRoot, rigName)
		target = resolveIntegrationBranchName(bd, rigPath, mqSubmitEpic)
	} else {
		// Check for explicit --base-branch override in formula vars on the source issue.
		// When gt sling dispatches with --base-branch, the value is persisted in
		// the bead's formula_vars field. Without this check, MRs created via
		// gt mq submit always target the rig's default branch (usually main),
		// even when the polecat was working against a feature branch.
		if sourceIssue, showErr := bd.Show(issueID); showErr == nil {
			if af := beads.ParseAttachmentFields(sourceIssue); af != nil {
				if bb := extractFormulaVar(af.FormulaVars, "base_branch"); bb != "" && bb != defaultBranch {
					target = bb
					fmt.Printf("  Target branch override: %s (from formula_vars)\n", target)
				}
			}
		}

		// Auto-detect: check if source issue has a parent epic with an integration branch
		// Only if no explicit base_branch was found above
		if target == defaultBranch {
			refineryEnabled := true
			rigPath := filepath.Join(townRoot, rigName)
			settingsPath := filepath.Join(rigPath, "settings", "config.json")
			if settings, err := config.LoadRigSettings(settingsPath); err == nil && settings.MergeQueue != nil {
				refineryEnabled = settings.MergeQueue.IsRefineryIntegrationEnabled()
			}
			if refineryEnabled {
				autoTarget, err := beads.DetectIntegrationBranch(bd, g, issueID)
				if err != nil {
					// Non-fatal: log and continue with default branch as target
					fmt.Printf("  %s\n", style.Dim.Render(fmt.Sprintf("(note: %v)", err)))
				} else if autoTarget != "" {
					target = autoTarget
				}
			}
		}
	}

	// Phantom-"main" safety net (gu-aucji): a base_branch=main formula var that
	// leaked onto the bead resolves target to "main" even on a mainline-only rig.
	// Rewrite to the rig default when origin/main does not exist, mirroring the
	// guard in gt done. No-op for main-default rigs and when origin/main exists.
	if corrected := correctPhantomMainTarget(target, defaultBranch, g.RemoteBranchExists); corrected != target {
		style.PrintWarning("MR target was %q but rig default is %q and origin/main does not exist — retargeting to %q (gu-aucji)", target, defaultBranch, corrected)
		target = corrected
	}

	// Get source issue for priority inheritance and dependency check
	var priority int
	var sourceIssue *beads.Issue
	if mqSubmitPriority >= 0 {
		priority = mqSubmitPriority
	}
	// Always try to fetch source issue (needed for both priority and dep check)
	sourceIssue, err = bd.Show(issueID)
	if err != nil {
		if mqSubmitPriority < 0 {
			priority = 2
		}
	} else {
		if mqSubmitPriority < 0 {
			priority = sourceIssue.Priority
		}
	}

	// gu-czolf: Auto-prioritize fork-sync/rebase MRs to P0.
	// When the fork falls behind upstream, rebase-check fails fork-wide. The
	// only MR that fixes it is the sync MR — it must sort ahead of the doomed
	// MRs it unblocks, never behind them.
	if isForkSyncBranch(branch) && mqSubmitPriority < 0 {
		priority = 0
	}

	// Enforce molecule step dependencies before allowing submit.
	// If the source issue has an attached molecule, verify that prerequisite
	// steps are complete. This prevents polecats from skipping steps like
	// self-review, build-check, or state-update.
	if !mqSubmitSkipDeps && !mqSubmitResubmit && sourceIssue != nil {
		if err := checkMoleculeStepDeps(bd, sourceIssue); err != nil {
			return err
		}
	}

	// GH#3032/wa-skj: resolve the submitted branch tip for MR dedup and
	// verification. With --branch this can differ from the checked-out HEAD.
	commitSHA, shaErr := resolveMQSubmitCommitSHA(g, branch)
	if shaErr != nil {
		style.PrintWarning("could not resolve submitted branch SHA: %v (falling back to branch-only dedup)", shaErr)
	}

	// Build MR bead title and description
	title := fmt.Sprintf("Merge: %s", issueID)
	description := fmt.Sprintf("branch: %s\ntarget: %s\nsource_issue: %s\nrig: %s",
		branch, target, issueID, rigName)
	if commitSHA != "" {
		description += fmt.Sprintf("\ncommit_sha: %s", commitSHA)
	}
	if worker != "" {
		description += fmt.Sprintf("\nworker: %s", worker)
	}

	// Verify before either an idempotent success or a new MR registration.
	// Refinery's later branch check is local-ref based, so missing/stale pushes
	// must fail here instead of producing a delayed refinery rejection.
	if err := verifyMQSubmitPushedBranch(g, branch, commitSHA); err != nil {
		return err
	}

	// Check if MR bead already exists for this branch+SHA (idempotency)
	var mrIssue *beads.Issue
	var existingMR *beads.Issue
	if commitSHA != "" {
		existingMR, err = bd.FindMRForBranchAndSHA(branch, commitSHA)
	} else {
		existingMR, err = bd.FindMRForBranch(branch)
	}
	if err != nil {
		style.PrintWarning("could not check for existing MR: %v", err)
		// Dedup check failed — fall through to create a new MR
	}

	if existingMR != nil {
		mrIssue = existingMR
		fmt.Printf("%s MR already exists (idempotent)\n", style.Bold.Render("✓"))
	} else {
		// Create MR bead (ephemeral wisp - will be cleaned up after merge)
		mrIssue, err = bd.Create(beads.CreateOptions{
			// gs-onu: commit the MR bead to shared main immediately, independent
			// of any auto-commit config drift, so the refinery reliably sees it.
			DoltAutoCommit: "on",
			Title:          title,
			Labels:         []string{"gt:merge-request"},
			Priority:       priority,
			Description:    description,
			Ephemeral:      true,
			Rig:            rigName, // Ensure MR bead is created in the rig's database (gt-7y7)
		})
		if err != nil {
			return fmt.Errorf("creating merge request bead: %w", err)
		}

		// Guard against empty ID from bd create (observed in ephemeral/wisp mode).
		// Without this, we'd pass "" to bd.Show and nudge the refinery with no
		// durable MR wisp, producing spurious MQ_SUBMIT events (gu-v76i).
		if mrIssue.ID == "" {
			return fmt.Errorf("creating merge request bead: bd create returned empty ID")
		}

		// gu-v76i: Verify MR bead is readable before nudging the refinery.
		// bd.Create() can succeed when the bead is written locally but fail to
		// persist (Dolt hiccup, transaction rollback, cross-rig routing landing
		// in the wrong DB). If we nudge anyway, the refinery wakes, finds no
		// matching MR in its queue, and emits phantom MQ_SUBMIT noise across
		// the town. Fail the submit instead of leaking a spurious event.
		if verified, verifyErr := bd.Show(mrIssue.ID); verifyErr != nil || verified == nil {
			return fmt.Errorf("MR bead %s created but verification read-back failed: %w", mrIssue.ID, verifyErr)
		}

		// gs-9sr: MAIN-VIEW verify (gs-onu defense-in-depth), mirroring gt done.
		// The read-back above only proves the MR is in this session's LOCAL Dolt
		// view; under an auto-commit config drift the write may never reach shared
		// main, leaving the refinery blind. Re-run the refinery's own discovery
		// through a FRESH bd connection. If the MR isn't discoverable on main,
		// fail loud (stranded-push wisp + error) instead of nudging the refinery
		// for an MR it can't see.
		if visible, qErr := verifyMRVisibleOnMain(beads.New(cwd), branch, commitSHA); qErr != nil {
			style.PrintWarning("main-view MR verify inconclusive for %s (query error): %v\nProceeding on read-back + auto-commit durability.", mrIssue.ID, qErr)
		} else if !visible {
			strandErr := fmt.Errorf("MR %s not discoverable on shared main via fresh query (branch=%s sha=%s) — the refinery would not see it", mrIssue.ID, branch, shortSHA(commitSHA))
			fileStrandedPushWisp(beads.New(cwd), rigName, branch, commitSHA, target, issueID, "", worker, strandErr)
			return fmt.Errorf("MR bead %s created but not discoverable on shared main: %w", mrIssue.ID, strandErr)
		}

		// gt-gpy: Validate MR bead landed in the rig's database (warning only).
		if prefixErr := beads.ValidateRigPrefix(townRoot, rigName, mrIssue.ID); prefixErr != nil {
			style.PrintWarning("MR bead prefix mismatch: %v\nThe refinery may not find this MR — check 'gt mq list %s'", prefixErr, rigName)
		}

		// Nudge refinery to pick up the new MR
		nudgeRefinery(rigName, "MERGE_READY received - check inbox for pending work")

		// GH#2599: Back-link source issue to MR bead for discoverability.
		if issueID != "" {
			comment := fmt.Sprintf("MR created: %s", mrIssue.ID)
			if _, err := bd.Run("comments", "add", issueID, comment); err != nil {
				style.PrintWarning("could not back-link source issue %s to MR %s: %v", issueID, mrIssue.ID, err)
			}
		}

		// Supersede older open MRs for the same source issue.
		// When a new polecat reattempts an issue, the old MR (different branch)
		// is orphaned. Close it so the queue and GitHub PRs stay clean.
		if issueID != "" {
			if oldMRs, err := bd.FindOpenMRsForIssue(issueID); err == nil {
				for _, old := range oldMRs {
					if old.ID == mrIssue.ID {
						continue // skip the one we just created
					}
					reason := fmt.Sprintf("superseded by %s", mrIssue.ID)
					if err := bd.CloseWithReason(reason, old.ID); err != nil {
						style.PrintWarning("could not supersede old MR %s: %v", old.ID, err)
						continue
					}
					fmt.Printf("  %s Superseded old MR: %s\n", style.Dim.Render("○"), old.ID)

					// gs-stvm: re-point the superseded MR's owning agent bead to the
					// new MR so the post-merge orphan reconcile and `gt polecat nuke`
					// follow the MR that actually merges instead of this closed one.
					if repointErr := bd.RepointSupersededMRAgent(old, mrIssue.ID); repointErr != nil {
						style.PrintWarning("could not re-point superseded MR %s agent bead: %v", old.ID, repointErr)
					}

					// Delete the old remote branch to auto-close the GitHub PR.
					// Only polecat branches — non-polecat branches may belong to
					// contributor forks; deleting them closes upstream PRs. (GH#2669)
					oldFields := beads.ParseMRFields(old)
					if oldFields != nil && strings.HasPrefix(oldFields.Branch, "polecat/") {
						g := git.NewGit(cwd)
						if err := g.DeleteRemoteBranch("origin", oldFields.Branch); err != nil {
							style.PrintWarning("could not delete superseded branch %s: %v", oldFields.Branch, err)
						} else {
							fmt.Printf("  %s Deleted remote branch: %s\n", style.Dim.Render("○"), oldFields.Branch)
						}
					}
				}
			}
		}
	}

	// Success output
	fmt.Printf("%s Submitted to merge queue\n", style.Bold.Render("✓"))
	fmt.Printf("  MR ID: %s\n", style.Bold.Render(mrIssue.ID))
	fmt.Printf("  Source: %s\n", branch)
	fmt.Printf("  Target: %s\n", target)
	fmt.Printf("  Issue: %s\n", issueID)
	if worker != "" {
		fmt.Printf("  Worker: %s\n", worker)
	}
	fmt.Printf("  Priority: P%d\n", priority)

	// Auto-cleanup for polecats: if this is a polecat branch and cleanup not disabled,
	// send lifecycle request and wait for termination
	if worker != "" && !mqSubmitNoCleanup {
		fmt.Println()
		fmt.Printf("%s Auto-cleanup: polecat work submitted\n", style.Bold.Render("✓"))
		if err := polecatCleanup(rigName, worker, townRoot); err != nil {
			// Non-fatal: warn but return success (MR was created)
			style.PrintWarning("Could not auto-cleanup: %v", err)
			fmt.Println(style.Dim.Render("  You may need to run 'gt handoff --shutdown' manually"))
			return nil
		}
		// polecatCleanup may timeout while waiting, but MR was already created
	}

	return nil
}

func resolveMQSubmitCommitSHA(g *git.Git, branch string) (string, error) {
	return g.Rev(fmt.Sprintf("refs/heads/%s^{commit}", branch))
}

func verifyMQSubmitPushedBranch(g *git.Git, branch, commitSHA string) error {
	if commitSHA != "" {
		if err := g.VerifyPushedCommit("origin", branch, commitSHA); err != nil {
			return fmt.Errorf("%w\n\nHint: run 'git push origin %s' first (or 'gt done'), then re-run 'gt mq submit'", err, branch)
		}
		return nil
	}

	exists, err := g.PushRemoteBranchExists("origin", branch)
	if err != nil {
		return fmt.Errorf("verify branch on origin: %w\n\nHint: run 'git push origin %s' first (or 'gt done'), then re-run 'gt mq submit'", err, branch)
	}
	if !exists {
		return fmt.Errorf("branch %q not found on origin\n\nHint: run 'git push origin %s' first (or 'gt done'), then re-run 'gt mq submit'", branch, branch)
	}
	return nil
}

// checkMoleculeStepDeps verifies that all prerequisite molecule steps are closed
// before allowing submission to the merge queue. Returns an error listing
// incomplete steps if any prerequisites are not yet done.
func checkMoleculeStepDeps(bd *beads.Beads, sourceIssue *beads.Issue) error {
	// Check if issue has an attached molecule
	fields := beads.ParseAttachmentFields(sourceIssue)
	if fields == nil || fields.AttachedMolecule == "" {
		return nil // No molecule attached — no enforcement needed
	}

	moleculeID := fields.AttachedMolecule

	// List all molecule steps (children of the molecule)
	children, err := bd.List(beads.ListOptions{
		Parent:   moleculeID,
		Status:   "all",
		Priority: -1,
	})
	if err != nil {
		// If we can't list steps, warn but don't block submission
		style.PrintWarning("could not check molecule steps for %s: %v", moleculeID, err)
		return nil
	}

	return validateMoleculePrereqs(children)
}

// validateMoleculePrereqs checks that all molecule steps that are prerequisites
// of the submit step are closed. Returns an error listing incomplete steps.
// Extracted for testability — accepts step data directly.
func validateMoleculePrereqs(children []*beads.Issue) error {
	if len(children) == 0 {
		return nil // No steps to check
	}

	// Find the submit step — it's the step whose title contains "submit"
	// (case-insensitive). All steps that come before it in the dependency
	// chain must be closed.
	submitSeq := 999999
	for _, child := range children {
		titleLower := strings.ToLower(child.Title)
		if strings.Contains(titleLower, "submit") {
			seq := extractStepSequence(child.ID)
			if seq < submitSeq {
				submitSeq = seq
			}
		}
	}

	// Collect incomplete prerequisite steps.
	// A prerequisite is any step sequenced before the submit step (by step
	// number suffix) that is not closed. Steps at or after the submit step
	// are post-submit (await-verdict, self-clean) and don't need to be done.
	var incompleteSteps []*beads.Issue
	for _, child := range children {
		seq := extractStepSequence(child.ID)
		if seq >= submitSeq {
			continue // This is the submit step or a post-submit step
		}
		if child.Status != "closed" {
			incompleteSteps = append(incompleteSteps, child)
		}
	}

	if len(incompleteSteps) == 0 {
		return nil // All prerequisites are closed
	}

	// Sort by sequence for readable output
	sortStepsBySequence(incompleteSteps)

	// Build error message listing incomplete steps
	var sb strings.Builder
	sb.WriteString("molecule step dependencies not met — incomplete prerequisite steps:\n")
	for _, step := range incompleteSteps {
		sb.WriteString(fmt.Sprintf("  ✗ %s: %s [%s]\n", step.ID, step.Title, step.Status))
	}
	sb.WriteString(fmt.Sprintf("\nComplete these steps before submitting, or use --skip-deps to override."))

	return fmt.Errorf("%s", sb.String())
}

// maxCleanupWait bounds the post-submit wait for witness-driven termination.
//
// Capped at 30s (gu-ci0l): the previous 5-minute wait was the load-bearing
// piece of a wedge loop. When the witness was slow, restarting, or had a
// stale lifecycle queue, the polecat sat in this loop for the full 5 minutes,
// then returned — but the session stayed alive. A restarting witness could
// then re-discover the now-idle polecat and re-dispatch it, re-entering the
// same wedge. 30s is enough headroom for a healthy witness to issue the kill;
// if the witness misses that window, the safety net below self-terminates
// rather than waste 4.5 more minutes of slot time.
const maxCleanupWait = 30 * time.Second

// polecatCleanup sends a lifecycle shutdown request to the witness and waits for termination.
// This is called after a polecat successfully submits an MR.
//
// gu-ci0l defense-in-depth: if the witness does not terminate the session
// within maxCleanupWait, the polecat self-terminates via a detached tmux-kill
// subprocess. This eliminates the post-done wedge loop where slow / restarting
// witnesses left polecats stranded in the wait, only to be re-dispatched on
// the next witness patrol and wedge again.
func polecatCleanup(rigName, worker, townRoot string) error {
	// Send lifecycle request to witness
	manager := rigName + "/witness"
	subject := fmt.Sprintf("LIFECYCLE: polecat-%s requesting shutdown", worker)
	body := fmt.Sprintf(`Lifecycle request from polecat %s.

Action: shutdown
Reason: MR submitted to merge queue
Time: %s

Please verify state and execute lifecycle action.
`, worker, time.Now().Format(time.RFC3339))

	// Send via gt mail
	cmd := exec.Command("gt", "mail", "send", manager,
		"-s", subject,
		"-m", body,
	)
	cmd.Dir = townRoot

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sending lifecycle request: %w: %s", err, string(out))
	}
	fmt.Printf("%s Sent shutdown request to %s\n", style.Bold.Render("✓"), manager)

	// Wait for retirement with periodic status
	fmt.Println()
	fmt.Printf("%s Waiting for retirement (cap %s)...\n", style.Dim.Render("◌"), maxCleanupWait)
	fmt.Println(style.Dim.Render("(Witness will terminate this session; self-terminate fires on timeout)"))

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	timeout := time.After(maxCleanupWait)

	waitStart := time.Now()
	for {
		select {
		case <-ticker.C:
			elapsed := time.Since(waitStart).Round(time.Second)
			fmt.Printf("%s Still waiting (%v elapsed)...\n", style.Dim.Render("◌"), elapsed)
		case <-timeout:
			fmt.Printf("%s Timeout waiting for polecat retirement after %s\n", style.WarningPrefix, maxCleanupWait)
			// gu-ci0l safety net: spawn a detached tmux-kill so the session
			// terminates regardless of witness state. Without this fallback,
			// a slow/restarting witness left the session alive — exposing it
			// to re-dispatch on the next witness patrol and another round of
			// the same wedge. The detached subprocess survives our return.
			if worker != "" && rigName != "" {
				sessionName := session.PolecatSessionName(session.PrefixFor(rigName), worker)
				t := tmux.NewTmux()
				if err := t.DetachedKillSessionWithProcesses(sessionName, 3*time.Second); err != nil {
					style.PrintWarning("self-terminate fallback failed: %v (witness must clean up manually)", err)
					fmt.Println(style.Dim.Render("  You can verify with: gt polecat status"))
				} else {
					fmt.Printf("%s Self-terminate fallback dispatched (session: %s)\n", style.Bold.Render("✓"), sessionName)
				}
			}
			return nil // Don't fail the MR submission just because cleanup timed out
		}
	}
}

// isForkSyncBranch returns true if the branch name indicates a fork-sync or
// upstream-rebase operation. These MRs unblock the entire queue when
// rebase-check fails fork-wide, so they must auto-file at P0. (gu-czolf)
func isForkSyncBranch(branch string) bool {
	return strings.HasPrefix(branch, "sync/upstream") ||
		strings.HasPrefix(branch, "sync/fork") ||
		strings.Contains(branch, "upstream-sync") ||
		strings.Contains(branch, "fork-sync") ||
		strings.Contains(branch, "rebase-upstream")
}

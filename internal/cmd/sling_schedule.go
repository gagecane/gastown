package cmd

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
	"github.com/steveyegge/gastown/internal/sling"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// shouldDeferDispatch checks the town config to decide dispatch mode.
// Returns (true, nil) when max_polecats > 0 (deferred dispatch).
// Returns (false, nil) when max_polecats <= 0 (direct dispatch).
func shouldDeferDispatch() (bool, error) {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return false, nil // No town — direct dispatch
	}

	settingsPath := config.TownSettingsPath(townRoot)
	settings, err := config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		return false, fmt.Errorf("loading town settings: %w (dispatch blocked — fix config or use gt config set scheduler.max_polecats -1)", err)
	}

	schedulerCfg := settings.Scheduler
	if schedulerCfg == nil {
		return false, nil // No scheduler config — direct dispatch (default)
	}

	maxPol := schedulerCfg.GetMaxPolecats()
	if maxPol > 0 {
		return true, nil
	}
	return false, nil // -1 or 0 = direct dispatch
}

// ScheduleOptions holds options for scheduling a bead.
type ScheduleOptions struct {
	Formula       string   // Formula to apply at dispatch time (e.g., "mol-polecat-work")
	Args          string   // Natural language args for executor
	Vars          []string // Formula variables (key=value)
	Merge         string   // Merge strategy: direct/mr/local
	BaseBranch    string   // Override base branch for polecat worktree
	ResumeBranch  string   // Resume an existing branch (gh#3602); mutually exclusive with BaseBranch
	NoConvoy      bool     // Skip auto-convoy creation
	Owned         bool     // Mark auto-convoy as caller-managed lifecycle
	DryRun        bool     // Show what would be done without acting
	Force         bool     // Force schedule even if bead is hooked/in_progress
	NoMerge       bool     // Skip merge queue on completion
	ReviewOnly    bool     // Review-only mode: assignee evaluates and reports back, no merge/commit/push
	Account       string   // Claude Code account handle
	Agent         string   // Agent override (e.g., "gemini", "codex")
	HookRawBead   bool     // Hook raw bead without default formula
	Ralph         bool     // Ralph Wiggum loop mode
	PriorityFloor int      // Dispatch priority floor (0=normal, 2=low, 4=lowest)
}

// checkSchedulePrefixParity enforces, at enqueue time, the same cross-rig
// prefix invariant that the dispatcher enforces at dispatch time
// (capacity_dispatch.go: capacity.AcceptsPrefix). A bead whose ID prefix
// does not match the target rig's registered prefix can never be
// dispatched, so enqueueing its sling context creates an un-dispatchable
// context that wastes heartbeat cycles and eventually trips the
// circuit breaker silently.
//
// This guard is intentionally NOT bypassable by --force: the dispatcher
// has no --force override, and letting --force create un-dispatchable
// contexts just moves the silent failure downstream. --force exists to
// override status/assignee sanity checks (hooked, in_progress), not
// dispatcher invariants.
//
// Empty rigPrefix (capacity.AcceptsPrefix returns true) — e.g. when
// rig config is unavailable — is treated as "unknown, accept" to match
// the dispatcher's fail-open behavior, avoiding guard mismatches that
// would let the dispatcher accept something the enqueue refused.
//
// Fixes: gu-5ooj
func checkSchedulePrefixParity(townRoot, rigName, beadID string) error {
	rigPath := filepath.Join(townRoot, rigName)
	rigPrefix := rigBeadsPrefix(townRoot, rigPath, rigName)
	if capacity.AcceptsPrefix(rigPrefix, beadID) {
		return nil
	}
	gotPrefix := capacity.BeadIDPrefix(beadID)
	return fmt.Errorf("cross-rig prefix: bead %s (prefix %q) cannot be scheduled to rig %q (prefix %q)\n"+
		"The dispatcher will refuse this bead. Create the task from the target rig: cd %s && bd create --title=...\n"+
		"--force cannot override this — it is a dispatcher invariant",
		beadID, gotPrefix, rigName, rigPrefix, rigName)
}

// shouldReattachFormula delegates to sling.ShouldReattachFormula (the gs-am8
// GAP 2 re-attach decision). Kept as a thin cmd wrapper so existing callsites
// and tests stay unchanged.
func shouldReattachFormula(force bool, requestedFormula string, existing *capacity.SlingContextFields) bool {
	return sling.ShouldReattachFormula(force, requestedFormula, existing)
}

// isStaleOrFailedContext delegates to sling.IsStaleOrFailedContext (the
// gu-rm08l recovery predicate). Kept as a thin cmd wrapper.
func isStaleOrFailedContext(ctx *beads.Issue, fields *capacity.SlingContextFields, now time.Time) bool {
	return sling.IsStaleOrFailedContext(ctx, fields, now)
}

// staleContextReslingReason delegates to sling.StaleContextReslingReason,
// distinguishing transient-failure expiry from plain TTL expiry.
func staleContextReslingReason(fields *capacity.SlingContextFields) string {
	return sling.StaleContextReslingReason(fields)
}

// scheduleBead schedules a bead for deferred dispatch via the capacity scheduler.
// Creates a sling context bead to hold scheduling state. The work bead is never modified.
func scheduleBead(beadID, rigName string, opts ScheduleOptions) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	if err := verifyBeadExists(beadID); err != nil {
		return fmt.Errorf("bead '%s' not found", beadID)
	}

	if _, isRig := IsRigName(rigName); !isRig {
		return fmt.Errorf("'%s' is not a known rig", rigName)
	}
	if err := verifyBeadExistsInTargetRigDatabase(beadID, rigName, townRoot); err != nil {
		return err
	}

	// Cross-rig-prefix parity guard (gu-5ooj). Mirrors the unconditional
	// dispatch-time guard in capacity_dispatch.go: a bead whose ID prefix
	// does not match the target rig's registered prefix can never be
	// dispatched, so enqueueing its sling context is useless — the
	// context will sit in the queue, the dispatcher will refuse on every
	// heartbeat, and after maxDispatchFailures the circuit breaker
	// closes silently. Previously this invariant was only enforced at
	// dispatch time; --force would bypass the enqueue-side check
	// (checkCrossRigGuard below), letting operators create
	// un-dispatchable contexts. This guard runs BEFORE checkCrossRigGuard
	// and is NOT bypassable by --force, matching dispatcher semantics.
	if err := checkSchedulePrefixParity(townRoot, rigName, beadID); err != nil {
		return err
	}

	if !opts.Force {
		if err := checkCrossRigGuard(beadID, rigName+"/polecats/_", townRoot); err != nil {
			return err
		}
	}

	info, err := getBeadInfo(beadID)
	if err != nil {
		return fmt.Errorf("checking bead status: %w", err)
	}

	// Soft cross-rig title warning (gu-an4y). The hard prefix guards above
	// catch beads whose PREFIX provably can't dispatch to the target rig.
	// They miss the bootstrap-pattern failure mode where prefix and rig agree
	// but the title clearly mentions a different rig (e.g., a cadk-* bead
	// titled "CodegenAgentSchedulerConstructs..." being scheduled to
	// casc_cdk because cadk-* routes there). Emit a stderr warning naming
	// the foreign rig so operators can refile before a polecat slot is
	// wasted on the wrong worktree.
	titleMismatchWarner(townRoot, rigName, beadID, info.Title)

	// Guard against scheduling closed/tombstone beads (defense-in-depth, hq-ki2).
	// Mirrors the closed-bead guards in runSling (sling.go) and executeSling
	// (sling_dispatch.go). The daemon's stranded scan can route closed cross-prefix
	// beads through scheduleBead in deferred dispatch mode; without this check, a
	// fresh ghost convoy is created for already-completed work. Not bypassed by
	// --force — if you need to re-dispatch, reopen the bead first.
	//
	// Run this BEFORE the identity check so the error clearly states the closed
	// cause rather than the more general "identity bead" message (IsIdentityBead
	// treats status=closed as an identity signal per rule 3, gu-3znx). Matches
	// the ordering in sling.go:runSling and sling_dispatch.go:executeSling.
	if info.Status == "closed" || info.Status == "tombstone" {
		return fmt.Errorf("bead %s is %s (work already completed)", beadID, info.Status)
	}

	// Ghost-dispatch guard (gu-7gm + gu-3znx). Reject agent/identity beads —
	// gt:agent label, legacy type=agent, closed status, or polecat/refinery
	// title regex. Scheduling one causes a polecat to hook the identity bead
	// and submit whatever stale auto-save branch the prior polecat left
	// behind, potentially reverting merged commits. Previously this only
	// filtered on label/type (isAgentBead); gu-3znx widens it to match the
	// full beads.IsIdentityBead contract used by convoy feeding so all
	// dispatch paths share one filter.
	if isIdentityBeadInfo(info) {
		return fmt.Errorf("bead %s is an identity/system bead (gt:agent label, closed, or polecat/refinery title): %q — refusing to schedule", beadID, info.Title)
	}

	// Epic-like title guard (gu-smr1). Reject beads with "EPIC:" title
	// prefix and non-epic issue_type. The scheduler dispatch path is the
	// last chance before a polecat spawn, so we guard here too even though
	// detectSchedulerIDType already reroutes EPIC: titles down the epic
	// path — scheduleBead can be invoked directly by internal callers.
	if isEpicLikeBeadInfo(info) {
		return fmt.Errorf("bead %s has epic-like title %q but issue_type=%q — refusing to schedule. Fix with: bd update %s --type=epic",
			beadID, info.Title, info.IssueType, beadID)
	}

	// Container-type guard (gu-xymp6). scheduleBead is the last stop before a
	// sling context is created and an auto-convoy is spun up. A bead whose
	// issue_type is epic, convoy, or molecule (or carrying gt:epic / gt:convoy /
	// gt:molecule) is a non-work container — never dispatchable. detectSchedulerIDType
	// reroutes epics/convoys on the 1-arg path, but scheduleBead is reachable
	// directly by deferred/convoy callers on the 2-arg explicit-rig path that
	// bypasses it. Refusing here prevents a sling-context AND a "Work: ..." convoy
	// from ever wrapping a non-work bead (cadk-82a molecule witness-patrol CV).
	// Shares dispatch.IsContainerBeadInfo with `gt ready`; not bypassed by --force.
	if isContainerBeadInfo(info) {
		return fmt.Errorf("bead %s is a non-work container (issue_type=%q, labels=%v): %q — refusing to schedule. Epics, convoys, and molecules track work via children/steps; fix the data with bd update %s --type=task if this is wrong",
			beadID, info.IssueType, info.Labels, info.Title, beadID)
	}

	// Mayor-only guard (gu-bk6e). scheduleBead is the last stop before a
	// sling context is created and a polecat is selected. Without this
	// guard, a bead labeled mayor-only / no-polecat that slips past the
	// ready-filter (e.g. direct sling by an internal caller) would still
	// generate a sling-context wrapper that the scheduler tries to feed.
	// The polecat would then close no-changes for being out of scope, and
	// the cycle repeats. The label is an operator assertion, not a
	// dispatch preference — schedule refuses on both labels unconditionally.
	if isMayorOnlyBeadInfo(info) {
		return fmt.Errorf("bead %s is labeled mayor-only / no-polecat: %q — refusing to schedule. Polecats cannot resolve this work; remove the label first if that assessment is wrong",
			beadID, info.Title)
	}

	// Human-only guard (gs-4pe6). A bracketed [HUMAN] / [HUMAN-ONLY] title tag
	// (or the human-only label) declares work that structurally requires a human
	// — user-observation studies, sign-offs, judgment calls — that no polecat can
	// complete by changing code. Before this guard the tag was inert free text,
	// so a human-only bead swept into a convoy reached scheduleBead and spawned a
	// polecat that could only close no-changes (lb-wcdw.15). Shares
	// dispatch.IsHumanOnlyBeadInfo with `gt ready`; not bypassed by --force.
	if isHumanOnlyBeadInfo(info) {
		return fmt.Errorf("bead %s is marked human-only ([HUMAN] title tag or human-only label): %q — refusing to schedule. This work requires a human, not a polecat; remove the tag/label first if that assessment is wrong",
			beadID, info.Title)
	}

	// Awaiting-refinery-merge guard (gu-ea25u). A source bead carrying the
	// awaiting_refinery_merge label already has a submitted MR that the
	// refinery has not yet merged to origin/main — the work is done and the
	// bead stays open only so the refinery's PostMerge path can close it with
	// the real commit_sha. Creating a sling context for it spawns a fresh
	// polecat that finds the work complete, declines with no commits, and
	// defers — a wasted slot plus host/Dolt load (fury×2, pipboy in one day).
	// The label is cleared by the refinery on merge (or the reaper for a
	// proven-merged orphan), which is the correct re-dispatch path; not
	// bypassed by --force.
	if isAwaitingMergeBeadInfo(info) {
		return fmt.Errorf("bead %s is awaiting refinery merge (label %s): %q — refusing to schedule. Its MR is submitted and in the merge queue; the refinery will close it on merge",
			beadID, "awaiting_refinery_merge", info.Title)
	}

	// Reference/tripwire guard (hq-9jeyo). A bead labeled do-not-dispatch /
	// pinned, or issue_type=reference, is a permanent live safety gate, never
	// work. Refusing here prevents the sling-context AND the auto-convoy from
	// ever being created (the spurious hq-cv-ajqmm convoy that fed a tripwire),
	// reinforcing the dispatch-time filter. Not bypassable by --force: it is a
	// dispatcher invariant, not a status/assignee sanity check.
	if isReferenceTripwireBeadInfo(info) {
		return fmt.Errorf("bead %s is a do-not-dispatch / pinned reference tripwire: %q — refusing to schedule. It must stay OPEN as a live safety gate; remove the labels/type first if that assessment is wrong",
			beadID, info.Title)
	}

	// Nested-wrapper guard (gu-hfr3). Refuse to schedule a bead that is
	// itself a sling-context wrapper. Otherwise the idempotency check
	// below (keyed on WorkBeadID) misses, and a new wrapper is created
	// titled "sling-context: sling-context: ..." tracking the old one.
	// Each retry doubles the chain. Happens most often when a convoy
	// started tracking a sling-context mid-chain (because the real work
	// bead was reaped) and convoy schedule re-runs on the wrapper.
	if isSlingContextBeadInfo(info) {
		return fmt.Errorf("bead %s is a sling-context wrapper (label %s): %q — refusing to schedule (would create nested wrapper)", beadID, capacity.LabelSlingContext, info.Title)
	}

	// Polecat-owned bead guard (gu-gal8). scheduleBead is a dispatch entry
	// point used by deferred / convoy paths; without this guard, a polecat
	// self-filed bead would be queued and eventually fed to another polecat,
	// racing whatever real (user-filed) work tracks the same change.
	// Mirrors the same guard in runSling (sling.go) and executeSling
	// (sling_dispatch.go) so all dispatch paths share one filter.
	if isPolecatOwnedBeadInfo(info) {
		return fmt.Errorf("bead %s is owned by a polecat (%s): %q — refusing to schedule. Polecats may not dispatch their own work; reassign the owner first",
			beadID, info.Owner, info.Title)
	}

	// Open-children guard (gu-r8b0q, extends gu-fs88). A bead that is the
	// parent of any non-closed child is a container — the children track the
	// real work — and must never get a sling context. executeSling already
	// rejects these at dispatch time, but the rejection only closes the ONE
	// sling context (circuit-broken after maxDispatchFailures); the epic stays
	// in `bd ready`, so the next convoy/stranded scan re-runs scheduleBead and
	// creates a FRESH context → dispatch fails 3× again → circuit-breaks again
	// → forever. Refusing context creation here breaks that re-creation loop at
	// the source, so containers stop generating repeated circuit-break churn.
	// Runs after the cheap title/label guards so the common path short-circuits
	// before paying the extra `bd children` subprocess. Not bypassable by
	// --force — close or reparent the children first.
	if isParentOfOpenChildren(beadID) {
		return fmt.Errorf("bead %s has open children: %q — refusing to schedule. It is a container for work tracked by its children, not a work item itself; close or reparent the children first (see `bd show %s --children`)",
			beadID, info.Title, beadID)
	}

	// Idempotency: check for existing open sling context for this work bead.
	// Fail fast on errors to avoid creating duplicate contexts on transient DB failures.
	//
	// Create the sling context in the target rig's beads dir so that the target
	// rig's witness can discover it during patrol. Previously this used the HQ
	// beads dir, which meant non-HQ rig witnesses never saw the context. (GH#3468)
	rigBeadsDir := doltserver.FindRigBeadsDir(townRoot, rigName)
	rigBeads := beads.NewWithBeadsDir(townRoot, rigBeadsDir)
	existingCtx, existingFields, findErr := rigBeads.FindOpenSlingContext(beadID)
	if findErr != nil {
		return fmt.Errorf("checking for existing sling context: %w", findErr)
	}
	if existingCtx != nil {
		// Re-attach a DIFFERENT formula to an already-scheduled bead (gs-am8
		// GAP 2). The previous behavior unconditionally no-op'd, so a staged
		// bead stuck on the wrong formula — e.g. a review gate scheduled with
		// the default mol-polecat-work instead of mol-pw-adversarial-review —
		// could never be corrected (`gt sling mol-X --on <bead> --force`
		// no-op'd). With --force and a changed formula, rewrite the existing
		// context's formula in place (same context ID) so the next dispatch
		// runs the intended formula.
		if shouldReattachFormula(opts.Force, opts.Formula, existingFields) {
			if opts.Formula != "" {
				if err := verifyFormulaExists(opts.Formula); err != nil {
					return fmt.Errorf("formula %q not found: %w", opts.Formula, err)
				}
			}
			if opts.DryRun {
				fmt.Printf("Would re-attach formula %q to %s (context %s, was %q)\n",
					opts.Formula, beadID, existingCtx.ID, existingFields.Formula)
				return nil
			}
			if opts.Formula != "" {
				workDir := beads.ResolveHookDir(townRoot, beadID, "")
				if err := CookFormula(opts.Formula, workDir, townRoot); err != nil {
					return fmt.Errorf("formula %q failed to cook: %w", opts.Formula, err)
				}
			}
			newFields := *existingFields
			newFields.Formula = opts.Formula
			if err := rigBeads.UpdateSlingContextFields(existingCtx.ID, &newFields); err != nil {
				return fmt.Errorf("re-attaching formula to context %s: %w", existingCtx.ID, err)
			}
			fmt.Printf("%s Re-attached formula %q to %s (context %s, was %q)\n",
				style.Bold.Render("→"), opts.Formula, beadID, existingCtx.ID, existingFields.Formula)
			return nil
		}

		// Stale/failed-context recovery (gu-rm08l). A context that recorded a
		// transient dispatch failure (dispatch_failures>0) or has aged past the
		// TTL is NOT a healthy in-flight dispatch — it is a parked context the
		// daemon will never make progress on. Close it and fall through to
		// create a fresh one so the bead returns to the dispatchable pool.
		// Without this, re-slings (manual or from convoy/epic/stranded scans,
		// which already treat such contexts as unscheduled via areScheduled's
		// TTL logic) no-op on the lingering context and park the bead forever.
		if isStaleOrFailedContext(existingCtx, existingFields, time.Now()) {
			reason := staleContextReslingReason(existingFields)
			if opts.DryRun {
				fmt.Printf("Would recycle stale/failed context %s for %s (%s) and re-schedule\n",
					existingCtx.ID, beadID, reason)
			} else {
				if err := rigBeads.CloseSlingContext(existingCtx.ID, reason); err != nil {
					return fmt.Errorf("recycling stale sling context %s: %w", existingCtx.ID, err)
				}
				fmt.Printf("%s Recycled stale/failed context %s for %s (%s); re-scheduling\n",
					style.Bold.Render("↻"), existingCtx.ID, beadID, reason)
			}
			// Fall through to fresh schedule (existingCtx is now closed).
			existingCtx = nil
			existingFields = nil
		}
	}
	if existingCtx != nil {
		fmt.Printf("%s Bead %s is already scheduled (context: %s), no-op\n",
			style.Dim.Render("○"), beadID, existingCtx.ID)
		// Point the operator at --force when they're trying to change the
		// formula but didn't pass it (gs-am8 GAP 2).
		if existingFields != nil && opts.Formula != "" && opts.Formula != existingFields.Formula {
			fmt.Printf("  %s staged formula is %q; pass --force to re-attach %q\n",
				style.Dim.Render("Tip:"), existingFields.Formula, opts.Formula)
		}
		return nil
	}

	if (info.Status == "pinned" || info.Status == "hooked" || info.Status == "in_progress") && !opts.Force {
		return fmt.Errorf("bead %s is already %s to %s\nUse --force to override", beadID, info.Status, info.Assignee)
	}

	// Honor a 'gt:formula:<name>' label declared on the bead (gs-zq0 / gs-am8
	// GAP 1) so a pre-staged gated leg's intended formula survives the deferred
	// auto-dispatch path. The label outranks only the rig/system DEFAULT: when
	// the caller passed nothing or the resolved default, the declared formula
	// wins; an explicit non-default choice (e.g. `gt sling mol-X --on`) is
	// preserved. hook-raw-bead (an explicit no-formula request) is left alone.
	if declared := beads.FormulaFromLabels(info.Labels); declared != "" && !opts.HookRawBead {
		if opts.Formula == "" || opts.Formula == resolveFormula("", false, townRoot, rigName) {
			if declared != opts.Formula {
				fmt.Printf("  %s Honoring %s%s declared on %s (was %q)\n",
					style.Bold.Render("→"), beads.FormulaLabelPrefix, declared, beadID, opts.Formula)
			}
			opts.Formula = declared
		}
	}

	if opts.Formula != "" {
		if err := verifyFormulaExists(opts.Formula); err != nil {
			return fmt.Errorf("formula %q not found: %w", opts.Formula, err)
		}
	}

	if opts.DryRun {
		fmt.Printf("Would schedule %s → %s\n", beadID, rigName)
		fmt.Printf("  Would create sling context bead\n")
		if !opts.NoConvoy {
			fmt.Printf("  Would create auto-convoy\n")
		}
		return nil
	}

	// Cook formula after dry-run check to avoid side effects
	if opts.Formula != "" {
		workDir := beads.ResolveHookDir(townRoot, beadID, "")
		if err := CookFormula(opts.Formula, workDir, townRoot); err != nil {
			return fmt.Errorf("formula %q failed to cook: %w", opts.Formula, err)
		}
	}

	// Build sling context fields
	fields := &capacity.SlingContextFields{
		Version:    1,
		WorkBeadID: beadID,
		TargetRig:  rigName,
		EnqueuedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if opts.Formula != "" {
		fields.Formula = opts.Formula
	}
	if opts.Args != "" {
		fields.Args = opts.Args
	}
	if len(opts.Vars) > 0 {
		fields.Vars = strings.Join(opts.Vars, "\n")
	}
	if opts.Merge != "" {
		fields.Merge = opts.Merge
	}
	if opts.BaseBranch != "" {
		fields.BaseBranch = opts.BaseBranch
	}
	if opts.ResumeBranch != "" {
		fields.ResumeBranch = opts.ResumeBranch
	}
	fields.NoMerge = opts.NoMerge
	fields.ReviewOnly = opts.ReviewOnly
	if opts.Account != "" {
		fields.Account = opts.Account
	}
	if opts.Agent != "" {
		fields.Agent = opts.Agent
	}
	fields.HookRawBead = opts.HookRawBead
	if opts.Ralph {
		fields.Mode = "ralph"
	}
	fields.Owned = opts.Owned
	if opts.PriorityFloor > 0 {
		fields.PriorityFloor = opts.PriorityFloor
	}

	// Create sling context bead in the target rig's beads dir so the rig's
	// witness discovers it during patrol. (GH#3468)
	ctxBead, err := rigBeads.CreateSlingContext(info.Title, beadID, fields)
	if err != nil {
		return fmt.Errorf("creating sling context: %w", err)
	}

	// Auto-convoy (unless --no-convoy)
	if !opts.NoConvoy {
		existingConvoy := isTrackedByConvoy(beadID)
		if existingConvoy == "" {
			convoyID, err := createAutoConvoy(beadID, info.Title, opts.Owned, opts.Merge, opts.BaseBranch)
			if err != nil {
				fmt.Printf("%s Could not create auto-convoy: %v\n", style.Dim.Render("Warning:"), err)
			} else {
				fmt.Printf("%s Created convoy %s\n", style.Bold.Render("→"), convoyID)
				// Update the context bead fields with convoy ID
				fields.Convoy = convoyID
				if updateErr := rigBeads.UpdateSlingContextFields(ctxBead.ID, fields); updateErr != nil {
					fmt.Printf("%s Could not update context with convoy: %v\n", style.Dim.Render("Warning:"), updateErr)
				}
			}
		} else {
			fmt.Printf("%s Already tracked by convoy %s\n", style.Dim.Render("○"), existingConvoy)
		}
	}

	actor := detectActor()
	_ = events.LogFeed(events.TypeSchedulerEnqueue, actor, events.SchedulerEnqueuePayload(beadID, rigName))

	fmt.Printf("%s Scheduled %s → %s (context: %s)\n", style.Bold.Render("✓"), beadID, rigName, ctxBead.ID)
	return nil
}

// runBatchSchedule schedules multiple beads for deferred dispatch.
// Returns error when all schedule attempts fail.
func runBatchSchedule(beadIDs []string, rigName, townRoot string) error {
	if slingDryRun {
		fmt.Printf("%s Would schedule %d beads to rig '%s':\n", style.Bold.Render("📋"), len(beadIDs), rigName)
		for _, beadID := range beadIDs {
			fmt.Printf("  Would schedule: %s → %s\n", beadID, rigName)
		}
		return nil
	}

	fmt.Printf("%s Scheduling %d beads to rig '%s'...\n", style.Bold.Render("📋"), len(beadIDs), rigName)

	// Parse priority floor from global flag (already validated in runSling)
	batchPriorityFloor, _ := capacity.ParsePriorityFloor(slingPriorityFloor)

	successCount := 0
	for _, beadID := range beadIDs {
		formula := resolveFormula(slingFormula, slingHookRawBead, townRoot, rigName)
		err := scheduleBead(beadID, rigName, ScheduleOptions{
			Formula:       formula,
			Args:          slingArgs,
			Vars:          slingVars,
			NoConvoy:      slingNoConvoy,
			Owned:         slingOwned,
			Merge:         slingMerge,
			BaseBranch:    slingBaseBranch,
			ResumeBranch:  slingResumeBranch,
			DryRun:        false,
			Force:         slingForce,
			NoMerge:       slingNoMerge,
			Account:       slingAccount,
			Agent:         slingAgent,
			HookRawBead:   slingHookRawBead,
			Ralph:         slingRalph,
			PriorityFloor: batchPriorityFloor,
		})
		if err != nil {
			fmt.Printf("  %s %s: %v\n", style.Dim.Render("✗"), beadID, err)
			continue
		}
		successCount++
	}

	fmt.Printf("\n%s Scheduled %d/%d beads\n", style.Bold.Render("📊"), successCount, len(beadIDs))
	if successCount == 0 {
		return fmt.Errorf("all %d schedule attempts failed", len(beadIDs))
	}
	return nil
}

// resolveRigForBead determines the rig that owns a bead from its ID prefix.
func resolveRigForBead(townRoot, beadID string) string {
	prefix := beads.ExtractPrefix(beadID)
	if prefix == "" {
		return ""
	}
	return beads.GetRigNameForPrefix(townRoot, prefix)
}

// resolveRigForBeadWithLabels resolves the rig for a bead, honoring a
// 'gt:rig:<name>' label override before falling back to ID-prefix resolution.
// The override is honored only when <name> names a real rig in this town; an
// unknown rig falls through to the prefix. This lets a cross-rig child of an
// epic — whose ID prefix points at the epic's home rig — be dispatched to the
// rig its deliverable actually lives in (gs-8h8j).
func resolveRigForBeadWithLabels(townRoot, beadID string, labels []string) string {
	if want := beads.RigFromLabels(labels); want != "" && beads.IsKnownRig(townRoot, want) {
		return want
	}
	return resolveRigForBead(townRoot, beadID)
}

// resolveFormula determines the formula name from user flags and rig settings.
// Resolution order:
//  1. Explicit --formula flag
//  2. Rig property layers (wisp → bead → system default "mol-polecat-work")
//  3. Rig settings file (workflow.default_formula in settings/config.json)
//  4. Hardcoded fallback "mol-polecat-work"
//
// The property layers are the primary mechanism, supporting:
//
//	gt rig config set <rig> default_formula mol-evolve         # wisp layer
//	gt rig config set <rig> default_formula mol-evolve --global # bead layer
func resolveFormula(explicit string, hookRawBead bool, townRoot, rigName string) string {
	if hookRawBead {
		return ""
	}
	if explicit != "" {
		return explicit
	}
	// Check rig property layers: wisp → bead → system default (issue gt-y18).
	if townRoot != "" && rigName != "" {
		r := &rig.Rig{
			Name: rigName,
			Path: filepath.Join(townRoot, rigName),
		}
		if df := r.GetStringConfig("default_formula"); df != "" {
			return df
		}
	}
	// Fallback: check rig settings file (legacy path, issue gt-boc).
	if townRoot != "" && rigName != "" {
		rigPath := filepath.Join(townRoot, rigName)
		if df := config.GetDefaultFormula(rigPath); df != "" {
			return df
		}
	}
	return "mol-polecat-work"
}

// resolveFormulaForBead resolves the formula for a specific work bead, honoring
// a 'gt:formula:<name>' label declared on the bead (gs-zq0 / gs-am8 GAP 1).
// Precedence: explicit --formula flag > bead's gt:formula: label > rig/system
// default (resolveFormula). The label only outranks the DEFAULT, never an
// explicit choice — so a relay can pre-stage a gated leg (e.g. an adversarial
// review gate) with the intended formula and have it survive the bare
// auto-dispatch path that would otherwise silently apply mol-polecat-work.
// hookRawBead still forces "" (no formula).
func resolveFormulaForBead(explicit string, hookRawBead bool, townRoot, rigName string, labels []string) string {
	if explicit == "" && !hookRawBead {
		if declared := beads.FormulaFromLabels(labels); declared != "" {
			return declared
		}
	}
	return resolveFormula(explicit, hookRawBead, townRoot, rigName)
}

// mainTargetFormula returns the rig-configured formula to use when a bead
// resolves to the protected mainline (base=main), or "" when the rig has not
// configured one. A rig whose default_formula is an integration-branch
// dev-work workflow (which correctly REFUSES main targets) sets
// main_target_formula to a PR+review workflow so main-targeting beads are
// dispatched with the right formula instead of round-tripping through a wasted
// dev-work dispatch + ESCALATED close (gs-njym). Configured via:
//
//	gt rig config set <rig> main_target_formula mol-lia-pr-work
func mainTargetFormula(townRoot, rigName string) string {
	if townRoot == "" || rigName == "" {
		return ""
	}
	r := &rig.Rig{
		Name: rigName,
		Path: filepath.Join(townRoot, rigName),
	}
	return r.GetStringConfig("main_target_formula")
}

// resolveFormulaForBeadWithBase extends resolveFormulaForBead with target-base
// awareness (gs-njym). When a bead resolves to the protected mainline
// (baseBranch == "main") and the rig configures a main_target_formula, that
// formula is selected instead of the rig DEFAULT. The override only displaces
// the auto-applied default: an explicit --formula flag, a 'gt:formula:<name>'
// label, or hook-raw-bead all keep their existing precedence by falling through
// to resolveFormulaForBead unchanged. This stops a rig whose default is an
// integration-branch dev-work workflow from auto-dispatching main-targeting
// beads into a wasted ESCALATED round-trip.
func resolveFormulaForBeadWithBase(explicit string, hookRawBead bool, townRoot, rigName string, labels []string, baseBranch string) string {
	if explicit == "" && !hookRawBead && baseBranch == "main" && beads.FormulaFromLabels(labels) == "" {
		if mf := mainTargetFormula(townRoot, rigName); mf != "" {
			return mf
		}
	}
	return resolveFormulaForBead(explicit, hookRawBead, townRoot, rigName, labels)
}

// slingContextTTL is the maximum age of a sling context before it's considered
// stale and ignored by areScheduled(). This prevents orphaned sling contexts
// (from failed spawns or throttled dispatches) from permanently blocking tasks.
// See GH#2279. Aliases the domain constant sling.ContextTTL.
const slingContextTTL = sling.ContextTTL

// areScheduled returns a set of bead IDs that have open sling contexts.
// Scans all rig beads dirs since sling contexts are created in the target
// rig's beads dir (GH#3468). On error, fails closed: treats ALL requested
// beads as scheduled to prevent false stranded detection and duplicate
// scheduling attempts.
//
// Sling contexts older than slingContextTTL are ignored — they are likely
// orphans from failed spawn attempts (GH#2279).
func areScheduled(beadIDs []string) map[string]bool {
	result := make(map[string]bool)
	if len(beadIDs) == 0 {
		return result
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		// Can't determine town root — fail closed (treat all as scheduled)
		for _, id := range beadIDs {
			result[id] = true
		}
		return result
	}

	// Scan all rig beads dirs (sling contexts live in target rig's DB). (GH#3468)
	contexts := listAllSlingContexts(townRoot)

	// Build lookup of work bead IDs from open contexts, skipping stale ones.
	scheduledWorkBeads := make(map[string]bool)
	now := time.Now()
	for _, ctx := range contexts {
		// Skip stale sling contexts (GH#2279): contexts older than the TTL
		// are likely orphans from failed spawn attempts. Ignoring them allows
		// the task to appear as "ready" again for re-dispatch.
		if ctx.CreatedAt != "" {
			if created, err := time.Parse(time.RFC3339, ctx.CreatedAt); err == nil {
				if now.Sub(created) > slingContextTTL {
					continue
				}
			}
		}
		fields := beads.ParseSlingContextFields(ctx.Description)
		if fields != nil {
			scheduledWorkBeads[fields.WorkBeadID] = true
		}
	}

	// Filter to just the requested IDs
	for _, id := range beadIDs {
		if scheduledWorkBeads[id] {
			result[id] = true
		}
	}
	return result
}

// detectSchedulerIDType determines what kind of ID was passed for scheduling.
// Returns "convoy", "epic", or "task".
func detectSchedulerIDType(id string) (string, error) {
	// Fast path: hq-cv-* is always a convoy
	if strings.HasPrefix(id, "hq-cv-") {
		return "convoy", nil
	}

	info, err := getBeadInfo(id)
	if err != nil {
		return "", fmt.Errorf("cannot resolve bead '%s': %w", id, err)
	}

	switch info.IssueType {
	case "epic":
		return "epic", nil
	case "convoy":
		return "convoy", nil
	}

	for _, label := range info.Labels {
		switch label {
		case "gt:epic":
			return "epic", nil
		case "gt:convoy":
			return "convoy", nil
		}
	}

	// Data-hygiene fallback (gu-smr1): beads whose title starts with "EPIC:"
	// are treated as epics even when issue_type=task. Without this, the
	// auto-dispatcher happily slings them to polecats which waste a slot.
	if beads.IsEpicLikeTitle(info.Title) {
		return "epic", nil
	}

	return "task", nil
}

// schedulerTaskOnlyFlagNames lists flags that only apply to task bead scheduling,
// not convoy or epic mode.
var schedulerTaskOnlyFlagNames = []string{
	"account", "agent", "ralph", "args", "var",
	"merge", "base-branch", "no-convoy", "owned", "no-merge",
}

// validateNoTaskOnlySchedulerFlags checks that no task-only flags were set.
func validateNoTaskOnlySchedulerFlags(cmd *cobra.Command, mode string) error {
	var used []string
	for _, name := range schedulerTaskOnlyFlagNames {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			used = append(used, "--"+name)
		}
	}
	if len(used) > 0 {
		return fmt.Errorf("%s mode does not support: %s\nThese flags only apply to task bead scheduling",
			mode, strings.Join(used, ", "))
	}
	return nil
}

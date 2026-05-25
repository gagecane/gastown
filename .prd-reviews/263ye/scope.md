# Scope Analysis

## Summary

The problem statement is narrow and well-defined: automatically keep fixes from
`gastownhall/gastown` (upstream) merged into `gagecane/gastown` (fork) via this
local gastown_upstream rig. However, **the infrastructure largely already exists**.
The rig already has: (1) `plugins/sync-upstream/run.sh` — a periodic plugin that
merges `origin/main` into the `gagecane/gt` integration branch with 7 safety
guards, conflict escalation, and receipt tracking; (2) `scripts/check-upstream-
rebased.sh` — a pre-merge gate ensuring the fork stays rebased on upstream; and
(3) `internal/refinery/fork_sync.go` — refinery logic that preserves merge
topology when squash-merging fork-sync MRs.

This means the scope question is not "build upstream sync from scratch" but rather
"what's broken or missing in the existing flow, and what incremental changes make
it fully automatic?" The gap is likely operational — the plugin exists but may not
be running reliably, the crew checkout may not be configured correctly, or the
cooldown/quiescence checks may be overly conservative. The smallest version that
delivers value is probably "make the existing plugin work end-to-end without manual
intervention" — which is much smaller than "design and build an upstream sync system."

## Findings

### Critical Gaps / Questions

- **What specifically isn't working today?** The bead says "currently upstream
  fixes don't flow to the fork" but a complete `plugins/sync-upstream/run.sh`
  already exists with guards, fetch, merge, push, and conflict escalation. Is the
  plugin disabled (`.disabled` sentinel)? Is the `crew/gagecane` checkout missing
  or misconfigured? Is a guard perpetually tripping? Without diagnosing WHY the
  existing mechanism fails, any new scope risks building a second system alongside
  the first.
  - Why this matters: if the existing plugin just needs a config fix or a missing
    crew checkout, this is a 30-minute task, not a PRD.
  - Suggested clarifying question: *"Has `plugins/sync-upstream/run.sh` ever run
    successfully in this rig? If not, what's the blocking guard? If yes, when did
    it stop working and why?"*

- **"Crew workers" are mentioned in the architecture context but their role in
  the sync flow is undefined.** The bead says "The local rig gastown_upstream has
  crew workers." The existing sync plugin operates on `crew/gagecane/<rig>`
  worktrees — is the proposal to ALSO have crew workers do sync work (dispatch via
  beads), or is this acknowledging existing infrastructure?
  - Why this matters: adding human-dispatched crew work to a plugin-based
    automated flow creates two ownership paths for the same operation. Which one
    is authoritative? What happens when they race?
  - Suggested clarifying question: *"Is the desired architecture: (a) the existing
    plugin runs autonomously on a cooldown, or (b) a crew worker is dispatched to
    perform syncs, or (c) both with some coordination?"*

- **Missing explicit out-of-scope statements.** The problem statement doesn't
  declare what's NOT included. Natural ambiguities that will arise:
  - Does "keep fixes flowing" mean ONLY main→fork merges, or also cherry-picking
    specific upstream commits?
  - Does scope include resolving merge conflicts, or only clean fast-forward /
    merge cases (with conflict escalation)?
  - Does scope include syncing tags, releases, or GitHub metadata (issues,
    labels)?
  - Does scope include notifying downstream consumers when a sync lands?
  - Suggested clarifying question: *"Is the scope strictly 'upstream/main
    ancestry is preserved in the fork's main branch' (what check-upstream-
    rebased.sh validates), or does it extend to cherry-picks, conflict resolution,
    tag sync, or release management?"*

- **The `gagecane/gt` integration branch vs. fork's `main` — which is the
  target?** The existing sync plugin targets `gagecane/gt` (an integration
  branch). But the problem says "keep fixes from upstream merged into
  gagecane/gastown (fork)." Does that mean the fork's `main` branch directly, or
  is `gagecane/gt` the correct intermediary? The existing refinery fork_sync.go
  preserves topology when polecat branches integrating upstream are squash-merged.
  This suggests the full flow is: upstream/main → polecat branch → refinery →
  origin/main (fork's main).
  - Why this matters: the target branch determines the entire merge strategy.
    Pushing directly to the fork's main is different from flowing through the
    integration branch and refinery merge queue.
  - Suggested clarifying question: *"Is the desired landing target for upstream
    sync the fork's `main` branch (via refinery), the `gagecane/gt` integration
    branch, or both?"*

### Important Considerations

- **The existing `plugins/sync-upstream` has a `.disabled` sentinel mechanism.**
  If the plugin is disabled, the fix may be as simple as removing that file and
  ensuring the crew checkout exists. Before building anything new, verify the
  existing mechanism's state.

- **The 6-hour cooldown gate may be too conservative.** `plugin.md` declares
  `[gate] type = "cooldown" duration = "6h"`. If upstream advances frequently
  (multiple pushes per day), the fork could still fall behind. This is a tuning
  question, not a scope question, but it will inevitably be raised.

- **Conflict handling is scope-adjacent.** The existing plugin aborts on conflict
  and escalates to the mayor. If upstream diverges from the fork's integration
  branch, conflicts are inevitable. The PRD should explicitly state whether
  conflict resolution is in-scope (requiring polecat dispatch for manual
  resolution) or out-of-scope (escalate and wait).

- **Guard 6 (polecat in-flight check) can cause indefinite skips.** If a polecat
  always has a hook_bead (which is common in active rigs), the sync plugin will
  never fire. This is a plausible reason why "fixes don't flow" — the rig is
  always busy.

- **`check-upstream-rebased.sh` creates a hard gate.** Every PR/MR that tries to
  merge into the fork's main MUST have upstream/main as an ancestor. This means
  if the sync falls behind, ALL other polecat work is blocked until sync catches
  up. This interaction should be explicitly documented as the "forcing function"
  that makes sync correctness critical.

### Observations

- **The architecture is already well-decomposed.** Three layers exist:
  1. Plugin (`sync-upstream/run.sh`) — periodic automated merge
  2. Gate (`check-upstream-rebased.sh`) — enforcement that fork stays current
  3. Refinery helper (`fork_sync.go`) — topology preservation during squash-merge

  The natural seam for future work is: Phase 1 = make the existing plugin work
  reliably; Phase 2 = improve conflict resolution (auto-resolve trivial
  conflicts); Phase 3 = add observability and alerting when sync falls behind.

- **Features stakeholders will ask for the day after launch:**
  - "Can we cherry-pick specific commits instead of full merge?"
  - "Can we get a notification when sync happens?"
  - "Can we exclude certain paths from sync?" (e.g., upstream docs changes)
  - "Can sync auto-resolve trivial conflicts (import ordering, go.sum)?"
  - "Can we see a dashboard of sync health across all rigs?"

- **"While we're in there" refactors that are NOT required:**
  - Rewriting `run.sh` in Go (it works fine as shell)
  - Adding multi-branch sync (only `main` matters for now)
  - Integrating with GitHub Actions (the plugin model works)
  - Making the sync bidirectional (fork→upstream is a completely different problem)

- **Requirements that belong in a separate project:**
  - Bidirectional sync (contributing fork changes back upstream)
  - Multi-remote support (syncing from multiple upstreams)
  - Rig-config subsystem design (if config is the blocker, that's its own project)

## Confidence Assessment

**Medium.** The scope is inherently narrow — it's a single-direction merge between
two known repositories — but confidence is limited by the absence of a written PRD
draft. The problem statement implies something needs building, yet the existing
infrastructure covers the described functionality almost completely. The true scope
depends on the answer to "what's broken in the existing flow?" which is a
diagnostic question, not a design question.

If the existing plugin is just misconfigured/disabled: scope is **tiny** (config
fix + verification). If there's a deeper architectural gap (e.g., the crew model
replaces the plugin model): scope is **medium** and needs explicit phasing. Either
way, the scope creep risk is low because the problem is well-bounded by
construction (one remote, one direction, one branch, one rig).

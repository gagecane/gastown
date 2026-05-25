# User Experience Analysis

## Summary

The upstream-sync feature keeps `gagecane/gastown` (the fork) merged with
`gastownhall/gastown` (the upstream open-source repo) automatically, using
a plugin that runs during quiescent windows. From a UX perspective, the
feature's biggest strength is also its biggest risk: **it is invisible.**
When it works, nobody notices. When it fails (conflict, race, misconfigured
rig), users have no affordance to understand *why* the fork fell behind,
*when* the next sync will attempt, or *what* happened on the last attempt.

The mental model we want users to form is: **"My fork stays current with
upstream automatically. If it can't, I get told exactly what to fix and
how."** The mechanism succeeds when operators stop worrying about fork
drift entirely — they trust the system to either sync or escalate — and
power users can inspect the machinery when debugging.

## Analysis

### Key Considerations

- **The feature has zero daily interaction surface for most users.** Unlike
  `auto-test-pr` which produces visible artifacts (MRs for review), upstream
  sync produces merge commits that blend into git history. The primary UX
  challenge is *diagnostic* rather than *operational*.

- **Two distinct user personas:**
  1. **Rig maintainer / operator** — wants assurance the fork is current,
     wants to know when it isn't, wants the fix to be obvious when it breaks.
  2. **Polecat / automated agent** — encounters the `check-upstream-rebased.sh`
     gate during pre-merge verification. Its UX is the error message quality
     when the gate fails.

- **Failure is the interesting interaction.** The happy path is silent. Users
  only engage with this feature when: (a) a conflict occurs and they're
  asked to resolve it, (b) the `check-upstream-rebased.sh` gate fails on
  their branch, or (c) they proactively check sync status. All UX effort
  should focus on these three moments.

- **The cooldown gate (6h) means "when did it last try?" is a common
  question.** If a merge conflict is filed at 2am and the maintainer wakes
  up at 9am, they want to know: did the system try again? How many times?
  Is it still trying? This temporal awareness is crucial.

- **Progressive disclosure is essential.** Most of the time: one line in
  `gt rig status` or `gt plugin status`. When debugging: full history,
  conflict file lists, retry timeline. Don't force operators to parse
  shell scripts to understand the state.

- **The plugin runs as a Deacon plugin, not a top-level CLI command.** This
  means discoverability is lower than `gt auto-test-pr` which has its own
  namespace. Users must know to look under `gt plugin` to find it.

- **The existing `check-upstream-rebased.sh` error message is good** — it
  tells you what's wrong, shows the fix commands, and lists diverged
  commits. This is the highest-quality touchpoint today. Preserve it.

- **Crew members doing the actual resolution** (after a conflict escalation)
  need clear instructions in the escalation bead — not just "files X, Y, Z
  conflicted" but the full command sequence to reproduce and resolve.

### Options Explored

#### Option 1: Status integrated into existing `gt plugin` namespace

- **Description**: Upstream sync status is accessed via `gt plugin show
  sync-upstream` and `gt plugin history sync-upstream`. No new top-level
  CLI verb. Escalations use the standard bead/mail flow.
- **Pros**:
  - Zero new CLI surface — uses existing `gt plugin` infra.
  - Matches how deacon plugins are managed (list/show/history/run).
  - Plugin metadata (`gate.duration`, `tracking.labels`) drives the UX
    automatically.
  - Low implementation effort.
- **Cons**:
  - Discoverability is poor. Users think "fork sync" not "plugin show."
  - `gt plugin show sync-upstream` is 4 tokens; users want something
    shorter for a frequently-asked question.
  - Can't easily add sync-specific subcommands (e.g., "force a sync now"
    or "show conflict history").
- **Effort**: Low

#### Option 2: Dedicated `gt sync` top-level command

- **Description**: A new top-level verb `gt sync` with subcommands:
  `gt sync status [--rig=<rig>]`, `gt sync history`, `gt sync now
  [--rig=<rig>]`, `gt sync conflicts`. The plugin remains the
  execution engine but the CLI provides a purpose-built UX layer.
- **Pros**:
  - Highly discoverable: `gt sync` is intuitive for "keep things in sync."
  - Can evolve independently (add beads-sync, hooks-sync under same verb).
  - Allows sync-specific affordances (conflict history, force-now).
  - `gt sync status` can show a purpose-built table similar to
    `gt auto-test-pr status`.
- **Cons**:
  - Name collision risk: `gt hooks sync` already exists (different feature).
  - A new top-level verb for a feature that's mostly invisible is heavy.
  - Implementation effort is moderate — new Cobra tree, new formatting.
  - Risks overengineering for v1 where there's one rig and one operator.
- **Effort**: Medium

#### Option 3: Integration into `gt rig` status with plugin drill-down

- **Description**: `gt rig status <rig>` shows a "Fork Sync" line
  (e.g., "upstream: ✓ synced 3h ago" or "upstream: ✗ conflict, escalated
  2h ago"). Detailed info via `gt plugin show sync-upstream --rig=<rig>`.
  No new verb, but the primary surface is the rig status command.
- **Pros**:
  - The status is where operators already look (`gt rig status` is the
    dashboard command).
  - One-line summary answers the #1 question ("is my fork current?").
  - Drill-down uses existing plugin infra — no new commands needed.
  - Matches the "silent when happy, loud when broken" principle.
- **Cons**:
  - Relies on `gt rig status` having a plugin-contributed section —
    may require architecture work if not already supported.
  - Deep details still require knowing about `gt plugin show`.
  - "Force sync now" has no natural home (would need `gt plugin run
    sync-upstream --rig=<rig>`).
- **Effort**: Low-Medium

#### Option 4: Notification-only (no status surface, rely on escalations)

- **Description**: The system syncs or escalates. Users never proactively
  check status — they only interact when they receive an escalation bead
  or when the pre-merge gate (`check-upstream-rebased.sh`) fails on their
  branch. No new CLI surface at all.
- **Pros**:
  - Minimalist. The system is either working or it's telling you it's not.
  - Matches the "invisible infrastructure" principle perfectly.
  - Zero implementation beyond the plugin + gate script.
- **Cons**:
  - Users who distrust automation want to check ("is it actually running?").
  - During incidents, operators want to know sync state without waiting
    for the next 6h tick.
  - No way to force an immediate sync when you know upstream just landed
    something important.
  - Debugging requires reading plugin receipt beads directly.
- **Effort**: Low (already implemented)

### Recommendation

**Option 3 (rig status integration with plugin drill-down) for v1, with a
path to Option 2 for v2 if the feature expands.**

Rationale:
- The #1 UX question is "is my fork current?" — this belongs in the rig
  dashboard, not buried under `gt plugin show`.
- The feature is 95% invisible. A full top-level verb (Option 2) is
  over-engineering for something most users engage with only during
  failures.
- Option 4 (notification-only) is already the status quo and it works for
  happy paths but leaves operators blind during incidents.
- One line in `gt rig status` + drill-down via `gt plugin show
  sync-upstream` covers both the glance use case and the debug use case.

**Concrete v1 UX surfaces:**

**1. Rig status line (primary surface):**
```
$ gt rig status gastown_upstream
...
Fork Sync:   ✓ synced 3h ago (upstream/main → gagecane/gt, 0 commits behind)
...
```

Or when broken:
```
Fork Sync:   ✗ conflict (escalated 2h ago, 3 files)  [gt plugin show sync-upstream]
```

Or when skipped:
```
Fork Sync:   ○ skipped (merge queue not empty)  next attempt in ~3h
```

**2. Plugin show (drill-down):**
```
$ gt plugin show sync-upstream

sync-upstream v2 — Keep fork's gagecane/gt merged with upstream/main
Gate:      cooldown 6h (next eligible: 2026-05-26 05:47)
Last run:  2026-05-25 23:47 (success: merged 8e22636f → 94b3d5aa)
Status:    synced (0 commits behind upstream)

Recent history:
  2026-05-25 23:47  ✓ merged (gastown_upstream)
  2026-05-25 17:47  ○ skipped (polecat in-flight)
  2026-05-25 11:47  ✓ merged (gastown_upstream)
  2026-05-25 05:47  ○ skipped (dirty worktree)
```

**3. Gate failure message (already implemented, preserve as-is):**
```
✗ upstream/main is NOT an ancestor of HEAD.
  Your fork has fallen behind upstream. Rebase or merge upstream/main before merging.

  Fix:
    git fetch upstream
    git rebase upstream/main   # or: git merge upstream/main

  Commits in upstream but not here:
    abc1234 feat: new upstream feature
    def5678 fix: upstream bugfix
```

**4. Escalation bead content (conflict scenario):**
```
Subject: sync-upstream: merge conflict in gastown_upstream
Severity: medium

Merging origin/main into gagecane/gt conflicted on:
  internal/refinery/queue.go
  internal/cmd/done.go
  scripts/check-upstream-rebased.sh

Manual intervention needed:
  cd ~/gt/gastown_upstream/crew/gagecane
  git fetch origin main:refs/remotes/origin/main
  git merge origin/main

After resolving:
  git push origin gagecane/gt
  bd close <this-bead-id>
```

**5. Force-sync affordance:**
```
$ gt plugin run sync-upstream [--rig=gastown_upstream]
```

This bypasses the cooldown gate for manual trigger — useful when you know
upstream just landed critical fixes.

## Constraints Identified

- **No new top-level CLI verb in v1.** The feature lives under `gt plugin`
  and contributes a status line to `gt rig status`. This keeps the CLI
  surface small and avoids committing to a namespace before the feature
  matures.

- **Cooldown gate means at most 4 sync attempts per day.** Users must
  understand this cadence. If upstream lands 10 commits in an hour, the
  fork won't catch up for at most 6 hours. This is acceptable for the
  use case (periodic drift prevention) but must be documented.

- **Conflict resolution is manual.** The plugin aborts and escalates.
  There is no auto-resolution. The escalation must be actionable enough
  that a human (or dispatched polecat) can resolve without re-reading
  the plugin source.

- **The plugin only operates on `crew/gagecane/<rig>` worktrees.** Users
  should never need to know this implementation detail, but debugging
  instructions must reference the correct path.

- **Fast-forward vs merge is an implementation detail invisible to users.**
  Both result in "synced" status. The history log may optionally
  distinguish them (`fast-forwarded` vs `merged`) for debugging, but the
  primary status line should not.

- **`gt hooks sync` already exists as a different feature** (regenerating
  agent hook/settings files). Any new `sync` verb must avoid collision
  or confusion. This is another argument for keeping upstream sync under
  `gt plugin` rather than creating `gt sync`.

## Open Questions

- **Should `gt rig status` show fork-sync as a built-in line, or should
  plugins be able to contribute arbitrary status lines?** A generic
  "plugin status contribution" mechanism is more extensible but higher
  effort. For v1, a hardcoded fork-sync line is pragmatic.

- **Should failed syncs (conflict) block polecat dispatch on the rig?**
  Currently they don't — the `check-upstream-rebased.sh` gate on each
  MR catches divergence. But an operator might expect that a conflict
  escalation means "stop all work until resolved." This is a policy
  decision, not a UX decision, but the UX must clearly communicate
  whichever policy is chosen.

- **Should there be a notification when sync succeeds?** Currently,
  success is silent (a receipt bead is created but it's ephemeral/
  unlabeled). For the one-rig pilot this is fine. At scale (many rigs),
  operators may want a daily digest: "3 rigs synced, 1 skipped, 0
  conflicted." This could be a deacon digest feature.

- **What happens when the fork has local-only commits (features not yet
  upstreamed)?** The merge strategy handles this (merge commit preserves
  both histories), but users may not understand why their `git log` shows
  upstream commits interspersed with fork-only commits. A brief explanation
  in the `gt plugin show` output ("merge strategy: merge commits preserve
  polecat branch validity") helps power users.

- **Should `gt plugin run sync-upstream` require confirmation?** It pushes
  to origin. In the spirit of the plugin's own safety rails (only runs
  when idle), a manual trigger should probably warn if polecats are
  in-flight: "Warning: polecat guzzle has active work. Sync anyway? [y/N]"

## Integration Points

- **Plugin infrastructure (`gt plugin list/show/history/run`):** The UX
  recommendation relies on this existing infrastructure working well. The
  `show` command must support the history format described above. If plugin
  history isn't implemented, it's a blocker for the drill-down UX.

- **Rig status (`gt rig status`):** The fork-sync status line needs a
  mechanism for plugins (or specific features) to contribute lines to the
  rig status output. This may require a small architecture addition (a
  "status contributors" registry or simply a hardcoded section for
  fork-sync in the rig status renderer).

- **Escalation system (`gt escalate`):** The conflict escalation is the
  primary failure-mode UX. The escalation bead content (shown above) must
  include actionable commands. Integration with escalation severity routing
  ensures the right person is notified (Mayor for medium-severity conflicts).

- **Pre-merge gate (`scripts/check-upstream-rebased.sh`):** This is the
  polecat-facing UX and is already well-implemented. The gate script
  auto-adds the `upstream` remote if missing — this is good UX for
  polecat worktrees that start from a bare checkout. No changes needed.

- **Refinery fork-sync topology preservation (`internal/refinery/fork_sync.go`):**
  This is invisible to users but affects merge behavior. When the refinery
  uses `MergeNoFF` instead of squash for fork-sync branches, the resulting
  merge commit preserves upstream ancestry. Users don't interact with this
  directly, but `git log --graph` will show the preserved topology — which
  is the *correct* UX (matches what users expect from a merge-based sync).

- **Data dimension (receipts and state):** The plugin currently creates
  ephemeral receipt beads. For the drill-down history to work, these
  receipts must be queryable by the `gt plugin history` command. Confirm
  that ephemeral beads with `plugin:sync-upstream` label are retained
  long enough for meaningful history (suggest: 30 days or last 20 runs).

- **Safety rails (guards 1-7 in plugin.md):** Each guard produces a
  "skipped: <reason>" receipt. The UX must translate these internal reasons
  into user-friendly language in the status/history output:
  - "no crew checkout" → "not configured for this rig"
  - "parked/disabled" → "rig is paused"
  - "dirty worktree" → "crew workspace has uncommitted changes"
  - "merge queue not empty" → "waiting for merge queue to drain"
  - "polecat in-flight" → "waiting for active work to complete"
  - "already up to date" → "synced"
  - "fetch failed" → "network error (will retry)"

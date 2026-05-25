# Missing Requirements

## Summary

The sync-upstream PRD (plugin.md) is well-defined for its happy path — merge
`origin/main` into `gagecane/gt` during quiescent windows with seven safety
guards and conflict escalation. The problem it solves (divergence between
upstream and fork accumulates into painful rebases) is real and the approach
(periodic merge vs. rebase, preserving polecat branch validity) is sound.

However, the spec is essentially a single-writer, single-rig, sunny-day
document. It leaves large operational requirements completely unaddressed:
(1) **multi-rig divergence recovery** — what happens when sync has been
skipped for weeks and the divergence is now hundreds of commits;
(2) **credential and authentication model** — the push to `gagecane/gt`
requires write access but the spec never discusses whose token, what scope,
or what happens when credentials rotate/expire;
(3) **observability and debugging** — no way to ask "why hasn't this rig
synced in 3 days?" or get historical sync status;
(4) **data migration and rollback** — the spec doesn't address what happens
to existing rig state when the plugin is first enabled, or how to undo a
bad merge that the plugin introduced.

## Findings

### Critical Gaps / Questions

#### 1. Authentication and credential management

- **Finding:** The plugin pushes to `origin` (gagecane/gastown) via `git push`.
  The spec never states whose credentials are used, how they're provisioned,
  rotated, or scoped. The `crew/gagecane` worktree presumably has a credential
  helper configured, but this is implicit. If the token expires mid-run, the
  push fails silently (exit code handled, but no alerting or retry).
- **Why this matters:** A credential rotation that invalidates the push token
  will cause every sync to silently skip (push fails → logged as failure →
  no alert to the human). The rig diverges until someone notices manually.
  Additionally, overly-broad credentials (org-wide write) create a blast radius
  if the token leaks.
- **Suggested clarifying question:** "What identity pushes to `gagecane/gt`?
  Is it a GitHub App installation token (scoped per-repo), a PAT, or SSH key?
  What is the rotation schedule, and what happens when credentials expire —
  does the plugin alert the human, or just keep skipping?"

#### 2. Authorization: who can trigger or suppress sync

- **Finding:** The spec defines opt-out via `sync-upstream:disabled` label on
  the rig identity bead and a `.disabled` sentinel file. It does NOT specify
  who is authorized to set/unset these flags. Any agent with beads write access
  could disable sync for any rig. A confused polecat or a malicious actor could
  suppress sync silently, causing divergence to accumulate unnoticed.
- **Why this matters:** If a polecat adds `sync-upstream:disabled` during
  troubleshooting and forgets to remove it, the rig silently diverges
  indefinitely. There's no "sync hasn't run in N days" alert.
- **Suggested clarifying question:** "Who is authorized to disable sync for a
  rig? Should there be a TTL on the disabled state (auto-re-enable after 7d)?
  Should the Deacon alert if a rig hasn't synced successfully in >N cooldown
  periods?"

#### 3. Divergence size limits and progressive strategy

- **Finding:** The spec handles clean merges and conflicts equally regardless
  of divergence size. If sync has been disabled/skipped for weeks and upstream
  has 500+ commits, the merge (even if clean) produces a massive merge commit.
  Code review of the integration branch becomes impossible. If it conflicts,
  the escalation to the Mayor dispatches a polecat, but a 500-commit conflict
  resolution is not a polecat-sized task.
- **Why this matters:** Large divergence recovery needs a different strategy
  (incremental cherry-pick, or a human decision to rebase the fork). The plugin
  should detect "divergence > N commits" as a separate condition and escalate
  with severity=high rather than attempting a blind merge.
- **Suggested clarifying question:** "What is the maximum divergence (in
  commits or days) at which the plugin should attempt a merge vs. escalating
  for human review? Should there be a 'divergence alert' threshold (e.g.,
  >50 commits behind → warn, >200 → refuse to merge automatically)?"

#### 4. Observability, status dashboard, and historical tracking

- **Finding:** The plugin creates ephemeral beads as "receipts" (`bd create
  --ephemeral`) but these are digestible (compressed/removed after a period).
  There is no persistent record of sync history, no way to ask "when did this
  rig last sync successfully?", and no dashboard or CLI command to view
  sync status across all rigs. The only audit trail is ephemeral.
- **Why this matters:** When a rig starts failing its merge queue with
  conflicts against upstream, the first question ops asks is "when did we last
  sync?" Without persistent tracking, this becomes `git log --merges` archaeology.
  Additionally, there's no way to proactively detect "rig X hasn't synced in
  5 days" before it becomes a crisis.
- **Suggested clarifying question:** "Should sync-upstream maintain a persistent
  status file or bead per rig tracking last-success timestamp, last-attempt
  timestamp, and current divergence? Is there a `gt sync-upstream status`
  CLI command? What triggers a proactive alert for stale sync?"

#### 5. Concurrent execution and reentrancy

- **Finding:** The cooldown gate (`duration = "6h"`) prevents rapid re-runs,
  but the spec does not address what happens if two instances of the plugin
  run simultaneously (e.g., manual trigger + scheduled trigger, or two Deacons
  in a misconfigured town). The `git push --force-with-lease` on fast-forward
  provides *some* protection, but the merge path does a plain `git push` which
  can race with another merge.
- **Why this matters:** Two concurrent merges on the same rig can produce
  duplicate merge commits or push failures that leave the local checkout in a
  merged-but-not-pushed state. The next run would then find a dirty worktree
  (Guard 3) and skip — but the local state is silently diverged from origin.
- **Suggested clarifying question:** "Is there a lock/mutex for the sync
  operation per rig? Should the plugin check for and recover from a
  'merged locally but not pushed' state? What happens if the Deacon fires
  the plugin twice (e.g., after a Deacon restart within the cooldown window)?"

#### 6. Rollback and undo for bad merges

- **Finding:** The spec says conflicts are escalated and aborted. But what
  about a merge that is *clean* (no textual conflicts) but introduces a
  semantic regression? For example, upstream renames a function that the fork
  also uses — git merges it cleanly but the build breaks. The plugin pushes
  this to `gagecane/gt`, and now the integration branch is broken.
- **Why this matters:** There is no specified rollback procedure. The plugin
  has already pushed the merge commit. Polecats whose branches are based on
  the old tip are still valid (per the spec's merge-preserves-ancestry claim),
  but any new work branching from `gagecane/gt` now has a broken base. The
  spec needs a "revert sync" procedure and ideally a post-merge verification
  step.
- **Suggested clarifying question:** "Should the plugin run `go build ./...`
  (or the configured build command) after a merge but before pushing, to catch
  semantic conflicts? If a pushed merge breaks the build, what's the rollback
  procedure — revert commit and force-push? Who is authorized to do that?"

#### 7. Data migration: initial enablement for existing rigs

- **Finding:** The spec assumes `gagecane/gt` already exists and the
  `crew/gagecane` worktree is set up. It does not specify what happens when
  the plugin is first enabled for a rig that has never had sync:
  - Does `gagecane/gt` need to be created? From what base?
  - What if the fork has diverged significantly before the plugin existed?
  - What if `crew/gagecane` doesn't exist yet (Guard 1 just skips)?
- **Why this matters:** Without a "bootstrap" procedure, enabling the plugin
  for a new rig requires undocumented manual setup. The gap between "add
  `sync-upstream` label" and "sync actually works" is filled with implicit
  prerequisites that no one has written down.
- **Suggested clarifying question:** "What is the bootstrap procedure for
  enabling sync-upstream on a rig for the first time? Should the plugin (or
  a companion setup script) create `gagecane/gt` if it doesn't exist, set up
  `crew/gagecane` worktree, and handle the initial divergence?"

### Important Considerations

- **Rate limiting of GitHub API/push operations.** The plugin does
  `git fetch` + `git push` per rig per cycle. With many rigs (20+), this is
  40+ network operations per 6-hour window. If GitHub rate-limits or the
  network is flaky, partial runs leave some rigs synced and others not. The
  spec should state the expected rig count ceiling and whether fetches/pushes
  are serialized or parallelized.

- **Backwards compatibility with existing polecat branches.** The spec
  claims "merge commits keep polecat branches valid." This is true for
  ancestry checks, but not for *content* — a polecat's branch may now
  conflict with the new merged state of `gagecane/gt` when it tries to
  merge via Refinery. The spec should acknowledge this and describe what
  happens: does Refinery detect the conflict and ask the polecat to rebase?

- **Notification to downstream consumers.** When `gagecane/gt` advances,
  polecats and crew members working off that branch are unaware. There's no
  notification mechanism ("gagecane/gt moved, you may want to rebase"). For
  crew members this is fine (they fetch manually), but for long-running
  polecat branches it could mean merge conflicts at `gt done` time that
  weren't there when work started.

- **Audit logging for compliance.** Force-with-lease pushes and merge pushes
  are both write operations to a shared branch. For incident response ("who
  pushed what to gagecane/gt at 3am?"), the only record is git reflog on
  the crew worktree (local, non-durable) and GitHub's audit log (requires
  admin access). The spec should state whether sync events are logged to a
  durable, queryable store.

- **Handling of upstream force-pushes.** The spec assumes `origin/main`
  only advances linearly. If upstream force-pushes main (rare but possible
  in open source), the next sync will see a divergence that can't be
  resolved by merge. The plugin should detect non-fast-forward upstream
  advancement and escalate rather than attempting a merge that incorporates
  rewritten history.

- **Multi-remote configuration.** The plugin hardcodes
  `INTEGRATION_BRANCH="gagecane/gt"` and `UPSTREAM_BRANCH="main"`. But
  the rig's remote setup may vary — some rigs might use `upstream/main`
  vs `origin/main` differently. The spec doesn't address configuration of
  these branch names per rig.

- **Cleanup of failed state.** If the plugin crashes between `git merge`
  and `git push` (OOM, SIGKILL, timeout), the crew worktree is left in a
  merged-but-unpushed state. The next run hits Guard 3 (dirty worktree)
  but it's not actually *dirty* — it's cleanly merged but ahead of origin.
  The spec should define recovery for this state (detect "ahead of origin
  but clean" and push, vs. "actually dirty" and skip).

- **Deprecation path.** If the plugin is superseded (e.g., by GitHub's
  native branch sync or a different automation), how is it cleanly removed?
  The `sync-upstream:disabled` label persists forever. Is there a cleanup
  procedure?

### Observations

- **The `.disabled` sentinel file is a second opt-out mechanism** alongside
  the bead label. Having two mechanisms with different discovery paths (file
  on disk vs. Dolt query) invites confusion ("I disabled it but it kept
  running" because they disabled the wrong one).

- **The escalation on conflict uses `gt escalate` with severity=medium.**
  For a rig whose integration branch is now un-mergeable, medium may be
  too low — it means the rig can't land any new polecat work until
  someone resolves the conflict. Consider severity=high for production rigs.

- **Guard 6 (no polecat in-flight) parses bead JSON with Python inline.**
  This is fragile — if `bd list --json` output format changes, the guard
  silently returns "no" (the except clause catches all). A `bd` native
  command like `bd list --filter assignee~=polecat --rig X --status hooked`
  would be more robust.

- **The script uses `git reset --hard`** in both the fast-forward and
  diverged paths. Per the project's CLAUDE.md, `git reset --hard` is
  discouraged ("too easy to lose work"). While the guards ensure the
  worktree is clean before reaching this point, an interrupted run
  between guards and reset could lose state. Consider using
  `git checkout` or `git switch --discard-changes` instead.

- **The 6-hour cooldown is static.** A rig with high upstream activity
  (many commits/day) diverges more in 6 hours than a dormant one. An
  adaptive cooldown (shorter when upstream is active) would keep
  divergence bounded. Not a v1 requirement, but worth noting.

## Confidence Assessment

**Confidence: Low** that the PRD as written is sufficient for safe,
operable production deployment.

Rationale:
- **Direction is correct** — periodic merge during quiescent windows,
  safety guards for active polecats, conflict escalation. All sound.
- **Happy path is implemented** — the run.sh already works for the
  simple case (clean merge, push, receipt).
- **However**, the spec leaves credential management entirely implicit,
  has no observability story, no rollback procedure for semantic
  regressions, no bootstrap documentation, and no protection against
  large-divergence blind merges. Each of these can independently cause
  a production incident: silent divergence accumulation (credentials
  expire), broken integration branch (semantic conflict pushed), or
  operational confusion (no way to see sync status).
- **The guard system is well-thought-out** but doesn't protect against
  post-push failures (merge was clean textually but broke the build).
- A second pass addressing Critical Gaps 1, 3, 4, and 6 (credentials,
  divergence limits, observability, rollback) would bring confidence to
  Medium. Confidence reaches High once a post-merge verification gate
  and a status dashboard exist.

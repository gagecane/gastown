# Technical Feasibility

## Summary

The upstream-sync feature is **technically feasible and largely already implemented**. The
codebase contains a working plugin (`plugins/sync-upstream/run.sh` + `plugin.md`), a
Refinery-level fork-sync topology preservation module (`internal/refinery/fork_sync.go`),
and a pre-merge gate script (`scripts/check-upstream-rebased.sh`) that enforces upstream
ancestry. The hard engineering problems are not in the sync mechanism itself — `git merge`
is well-understood — but in the **operational envelope**: conflict resolution at scale,
credential lifecycle, concurrent-write safety, and observability of a system that runs
unattended. None of these are technically impossible; most are medium-effort engineering
with known solutions. The hardest single problem is **automated conflict resolution** for
non-trivial divergences, which the current implementation correctly punts to human
escalation.

## Findings

### Critical Gaps / Questions

#### 1. Conflict resolution is the hardest technical problem

- **Finding:** The current `run.sh` aborts on merge conflict and files a `gt escalate`.
  This is correct for v1, but the PRD's stated goal ("automatically keep fixes flowing")
  implies eventual automation. Automated conflict resolution for arbitrary Go code is an
  unsolved hard problem — even AI-assisted merge tools (e.g., Google's ML-based merge)
  have limited accuracy for semantic conflicts. For the common case of *textual* conflicts
  in non-semantic positions (import ordering, adjacent-line additions), tooling like
  `git rerere` or structured merge drivers can help. For true semantic conflicts (both
  sides modified the same function's logic), no reliable automated solution exists.
- **Why this matters:** If the fork diverges significantly from upstream (which is the
  scenario that motivates this feature), conflicts will be frequent. A system that
  escalates every conflict defeats the "automatic" goal.
- **Suggested clarifying question:** "Is the v1 goal 'auto-merge when clean, escalate
  when conflicted' (already implemented), or 'resolve most conflicts automatically'
  (hard, possibly infeasible for arbitrary code)?"

#### 2. The system has no formal PRD — specification risk, not technical risk

- **Finding:** The `.prd-reviews/upstream-sync/prd-draft.md` file referenced in the
  bead assignment does not exist. The entire spec is the plugin.md (87 lines) plus the
  run.sh implementation. This is a specification completeness problem, not a technical
  feasibility problem. The implementation *works* for its designed scope.
- **Why this matters:** Without success criteria, latency SLAs, or defined scope
  boundaries, it's impossible to assess whether the current implementation is "done" or
  whether additional capabilities are needed. Technical feasibility depends on *what*
  you're trying to build.
- **Suggested clarifying question:** "Is the plugin.md the complete spec (in which case
  feasibility is proven — it's already implemented), or does 'upstream sync' encompass
  a larger vision (selective sync, multi-branch, cross-rig coordination) that needs
  a proper PRD?"

#### 3. Credential rotation has no automated recovery path

- **Finding:** The `run.sh` uses `git push origin` via whatever credential helper is
  configured for the `crew/gagecane` worktree. There is no health-check, no token refresh
  logic, and no alerting when pushes fail due to auth expiry. The script logs the failure
  and moves on — the rig silently falls behind.
- **Why this matters:** GitHub PATs expire. App installation tokens expire. SSH keys
  can be revoked. An unattended sync system that silently degrades when credentials
  rotate is worse than no sync at all — it creates false confidence that the fork is
  current.
- **Suggested clarifying question:** "What is the credential type (PAT, SSH, GitHub App)?
  Should the plugin integrate with a credential-refresh mechanism, or is 'escalate on
  auth failure' sufficient for v1?"

#### 4. Race condition between sync and polecat work

- **Finding:** Guard 6 in `run.sh` checks for in-flight polecats before syncing, but
  uses a snapshot-in-time check (read polecat state → do merge → push). A polecat could
  hook work and start pushing between the guard-check and the sync push. The
  `--force-with-lease` on the fast-forward path protects against this race, but the
  merge path uses plain `git push origin HEAD:$INTEGRATION_BRANCH` with no lease,
  meaning a concurrent push would succeed and potentially land a merge commit that
  doesn't include the concurrent polecat's work.
- **Why this matters:** If a polecat pushes to `gagecane/gt` between the sync's merge
  and its push, the sync push wins (last-writer-wins) and the polecat's commit is
  effectively reverted on the remote until the next sync or manual fix.
- **Suggested clarifying question:** "Should the merge-path push also use
  `--force-with-lease` to fail-safe on concurrent writes? Or is the quiescence guard
  (refinery queue empty + no hooked polecats) considered sufficient?"

### Important Considerations

#### 5. Divergence size has no upper bound

- **Finding:** The plugin merges `origin/main` (upstream) regardless of how many commits
  have accumulated since the last sync. If sync has been disabled or failing for weeks,
  a single sync attempt may merge hundreds of commits. This creates a single large merge
  commit that is hard to bisect, may exceed PR review capacity, and could introduce
  multiple independent breakages simultaneously.
- **Assessment:** Technically feasible to implement a "max divergence" gate that refuses
  to auto-sync if the commit distance exceeds a threshold (e.g., `git rev-list --count
  origin/main..origin/gagecane/gt > 100 → escalate instead of merge`). This is a
  policy decision, not a technical blocker.

#### 6. The Refinery fork_sync.go already handles topology preservation

- **Finding:** `internal/refinery/fork_sync.go` implements `preserveForkSyncTopology()`
  which detects when a polecat branch has merged upstream and instructs Refinery to use
  a no-fast-forward merge instead of squash. This is critical infrastructure that already
  exists and works. It was built to fix gu-9yi3 where squash-merging a fork-sync MR
  destroyed the merge topology that `check-upstream-rebased.sh` validates.
- **Assessment:** This is a **solved prerequisite**. The plugin and Refinery already
  cooperate correctly. No new work needed here.

#### 7. Multi-rig support exists but is untested at scale

- **Finding:** `run.sh` iterates over all rigs from `mayor/rigs.json`. Currently there
  is likely one rig (`gastown_upstream`) using this feature. The per-rig guards are
  adequate for the single-rig case but have O(n) cost in polecat-state queries for n
  rigs. Each rig check involves `bd list --json` + Python parsing, which hits Dolt.
- **Assessment:** Feasible for the expected scale (1-5 rigs). Would need optimization
  (batch query) if scaling to dozens of rigs, but that's a v2 concern.

#### 8. The cooldown gate prevents sync storms but not starvation

- **Finding:** The plugin.md specifies a 6-hour cooldown between runs. If the plugin
  consistently hits a guard (merge queue not empty, polecat in-flight) during its
  6-hour window, it will continuously skip and the fork will never sync. There is no
  backpressure or priority escalation for "sync has been skipped N times in a row."
- **Assessment:** Low severity for v1 (the quiescence windows exist in practice), but
  worth adding a "consecutive skips" counter that escalates after a threshold (e.g., 5
  consecutive skips = MEDIUM escalation). Technically trivial.

### Observations

#### 9. Plugin architecture is the right substrate

- The plugin system (`plugins/<name>/plugin.md` + `run.sh`) with TOML frontmatter
  for gates, tracking, and execution config is an appropriate substrate for this
  feature. The 6-hour cooldown gate, receipt tracking, and failure notification are
  all handled by the plugin framework. No custom daemon or cron infrastructure needed.

#### 10. The `check-upstream-rebased.sh` gate creates a hard dependency

- This pre-merge gate script means that if upstream sync falls behind, *all* polecat
  MRs will fail the rebase check and cannot land. This is by design (forces the fork
  to stay current) but means sync failures cascade into total rig paralysis. The
  refinery's `fork_sync.go` bypass (gu-ofsg) mitigates this for fork-sync MRs
  specifically, but regular polecat work is still blocked.
- This is an operational concern, not a feasibility concern. The mitigation exists
  (bypass for sync MRs + escalation on failure).

#### 11. No technical impossibilities identified

- Git merge is deterministic and well-understood.
- The plugin framework supports the required execution model.
- Dolt/beads support the receipt and escalation patterns.
- The credential model (whatever it is) is standard git-credential-helper usage.
- The topology preservation in Refinery is already proven.
- The pre-merge gate (`check-upstream-rebased.sh`) correctly enforces the invariant.

#### 12. Implicit prerequisite: upstream remote must be configured

- The system assumes `upstream` remote exists in the crew worktree. The
  `check-upstream-rebased.sh` script auto-adds it if missing, but the plugin itself
  relies on `origin/$UPSTREAM_BRANCH` resolution which uses `origin` (the fork), not
  `upstream` (the upstream repo). The script merges `origin/main` (fork's main from
  `gastownhall/gastown`) into `gagecane/gt`. This works because `origin` points to
  `gagecane/gastown` which tracks `gastownhall/gastown`'s main. However, the naming
  is confusing and could lead to errors if a rig's remote topology differs.

## Confidence Assessment

**High** — The feature is technically feasible and the hard problems are already solved
in the codebase. The sync plugin exists and works for its designed scope (clean merges,
escalate on conflict, guard against concurrent operations). The remaining gaps are
operational (credential management, observability, conflict resolution policy) and
specification-level (no formal PRD with success criteria), not technical. Nothing in
the described feature requires capabilities the system doesn't have, depends on
unsupported third-party behavior, or demands performance that is fundamentally hard
to achieve. The hardest remaining technical problem — automated conflict resolution —
is correctly deferred to human escalation in the current implementation.

**What would double implementation effort if discovered mid-build:**
- A requirement for *selective* sync (cherry-pick specific upstream commits rather than
  full merge) would require a commit classification system that doesn't exist.
- A requirement for *zero-downtime* sync (no window where the rebase-check gate blocks
  other MRs) would require rearchitecting the gate to be aware of in-flight syncs.
- A requirement for *multi-remote* sync (sync from multiple upstreams into one fork)
  would require generalizing the current single-upstream-remote assumption.

None of these are technically impossible, but each would roughly double the scope.

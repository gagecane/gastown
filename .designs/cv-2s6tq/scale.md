# Scalability Analysis

## Summary

The proposed upstream-sync replacement system — which automatically keeps
`gagecane/gastown` (fork) main in sync with `gastownhall/gastown` (upstream)
via autonomous conflict resolution and full CI gates — operates in a regime
where the sync operation itself is cheap but the **verification gate** (build
+ test) dominates cost. At current scale (1 rig, ~8 upstream commits/day,
~2 min test suite), the system would fire at most once per cooldown period
(6h), cost ~2 minutes of CPU per sync, and generate negligible network I/O.
This is trivially within budget.

The interesting scaling questions emerge along three axes: (1) **rig count**
— what happens when 10+ rigs opt into upstream sync, each with their own
fork divergence and build gates; (2) **upstream velocity** — what happens
when upstream ships 50+ commits/day and sync needs to happen more frequently
to keep divergence bounded; and (3) **conflict resolution cost** — what
happens when autonomous agents attempt conflict resolution on large divergences.
The hard limit is polecat fleet capacity: each conflict resolution consumes a
polecat slot for the duration of the resolution + build + test cycle. At
current fleet sizes (4-8 polecats per rig), a single stuck conflict resolution
can block productive work dispatch.

## Analysis

### Key Considerations

**Scale dimensions for this system:**

- **Upstream commit rate**: Currently ~8 commits/day (203 in May 2026,
  ~25 days). Can spike to 20-30/day during active development periods.
  Upstream PRs are merged ~1-3 per day.
- **Fork divergence**: Currently 351 commits ahead of upstream, 0 behind.
  The fork advances faster than upstream (~20 commits/day vs ~8).
- **Repo size**: 29MB working tree, 1355 Go files, 640 test files.
  Moderate — not a monorepo, but non-trivial.
- **Build time**: ~8.5s for `go build ./...` (cached). Cold build ~30s.
- **Test time**: ~2 minutes for `go test ./...` (partially cached).
  Uncached full run likely 3-4 minutes.
- **Conflict probability**: Proportional to upstream velocity × fork
  velocity × file overlap. Currently low (fork is ahead, not diverged).
- **Polecat fleet**: 4-8 workers per rig, shared across all work types.

**Resource consumption per sync cycle (happy path):**

| Resource | Per-cycle cost | Notes |
|----------|---------------|-------|
| Network (fetch) | ~1-5 MB | Git pack negotiation, delta compression |
| Network (push) | ~10-100 KB | Usually 1 merge commit |
| CPU (build gate) | ~30s wall / 8.5s apparent | Parallelized across cores |
| CPU (test gate) | ~2 min wall / 7.5 min CPU | Heavy parallelism (387% CPU) |
| Disk | ~0 (in-place merge) | Working tree mutation, no new clone |
| Dolt commits | 2-4 per cycle | Receipt beads, status updates |
| Polecat slot | 0 (happy path) / 1 (conflict) | Only consumed on conflict resolution |

### Options Explored

#### Option 1: Fixed-interval sync (current design, 6h cooldown)

- **Description**: Sync attempts every 6 hours regardless of upstream activity.
  Build + test gate runs on every sync. Conflicts trigger polecat dispatch.
- **Pros**: Simple, predictable, bounded cost (max 4 syncs/day/rig).
  Easy to reason about resource consumption.
- **Cons**: 6 hours of divergence accumulates between syncs. If upstream
  lands 3 PRs in 6h, the merge becomes larger and more likely to conflict.
  Also wastes cycles when upstream is dormant (no-op syncs still fetch).
- **Effort**: Low (essentially the current plugin with gates added)

**Scaling behavior:**
- 10 rigs: 40 syncs/day → 80 min of test time/day. Acceptable.
- 100 rigs: 400 syncs/day → 13+ hours of test time/day. Requires
  dedicated compute or parallelism.
- 1000 rigs: Infeasible without sharding or federation.

#### Option 2: Event-driven sync (webhook on upstream push)

- **Description**: Sync triggered by upstream push events (GitHub webhook
  or polling `git ls-remote`). Only syncs when upstream actually advances.
  Batches multiple commits per sync.
- **Pros**: Zero wasted cycles. Sync happens close to upstream advancement.
  Divergence window is minimized (minutes, not hours). Natural batching
  (multiple upstream commits per sync).
- **Cons**: Higher complexity. Requires webhook infrastructure or polling
  daemon. Burst upstream activity (e.g., merge day with 10 PRs in 1 hour)
  could trigger 10 syncs vs 1 batched sync.
- **Effort**: Medium (needs webhook receiver or poll loop, debounce logic)

**Scaling behavior:**
- 10 rigs: Scales with upstream activity, not rig count (all rigs sync on
  the same upstream event). One fetch, N merges.
- 100 rigs: Same upstream event triggers 100 parallel merges. Network is
  fine (git is efficient), but 100 build+test gates = 200 min of CPU.
  Needs staggering.
- 1000 rigs: Still bounded by upstream event rate, not rig count. But
  build parallelism becomes the bottleneck.

#### Option 3: Adaptive cooldown with divergence-aware batching

- **Description**: Base cooldown (6h) that shortens when upstream is active
  and lengthens when dormant. Sync only fires if upstream has actually
  advanced since last sync. Batches all upstream commits since last sync
  into one merge, regardless of how many there are.
- **Pros**: Adapts to upstream velocity. Zero wasted cycles. Bounded
  divergence regardless of activity pattern. Simple to implement (just
  add a `git rev-parse upstream/main` check before proceeding).
- **Cons**: Slightly more complex gate evaluation. Adaptive cooldown needs
  tuning parameters (min/max bounds). Could still accumulate large batches
  during rig outages.
- **Effort**: Low-Medium (adds conditional checks to existing cooldown)

**Scaling behavior:**
- 10 rigs: Optimal. Syncs only when needed, each rig independent.
- 100 rigs: Same as fixed-interval but fewer wasted cycles. Build cost
  still proportional to rig count × sync frequency.
- 1000 rigs: Needs sharding — single town can't manage 1000 rig syncs.

#### Option 4: Deferred verification (merge first, gate later)

- **Description**: Perform the merge immediately (no gate), push to a
  staging branch (`sync/pending`), then run build+test asynchronously.
  Only promote to main after gates pass. If gates fail, revert and
  dispatch conflict-resolution polecat.
- **Pros**: Sync latency drops to seconds (just `git merge` + push).
  Gate cost is amortized asynchronously. Failures don't block the sync
  pipeline. Staging branch gives rollback point.
- **Cons**: More complex branch management. Introduces a "pending" state.
  If gates fail frequently, staging branch accumulates and becomes stale.
  Also introduces a window where main is "behind" the sync but not yet
  verified.
- **Effort**: High (staging branch lifecycle, promotion logic, rollback)

**Scaling behavior:**
- All scales: Decouples sync frequency from gate cost. Syncs can be
  frequent and cheap; gates run asynchronously in parallel.
- 100 rigs: 100 staging branches, 100 async gate runs. Manageable with
  job queue.
- Bottleneck shifts from "sync pipeline" to "gate compute pool."

### Recommendation

**Option 3 (Adaptive cooldown with divergence-aware batching)** for v1,
with a path to Option 2 (event-driven) for v2 if rig count exceeds 10.

Rationale:
1. **Simplest change from current design.** The existing plugin already has
   a 6h cooldown. Adding "skip if upstream hasn't advanced" is a single
   `git rev-parse` comparison.
2. **Eliminates wasted cycles.** Current upstream rate (~8 commits/day)
   means most 6h windows will have new commits, but dormant periods
   (weekends) won't trigger pointless fetch+gate.
3. **Bounded divergence.** The cooldown ensures sync fires within hours of
   upstream advancement, keeping divergence to 1-2 merge-PR-worth of commits.
4. **Build+test gate is the right tradeoff at this scale.** 2 minutes per
   sync, max 4 syncs/day = 8 min/day of compute. Trivial.
5. **Autonomous conflict resolution** is the right call at current
   divergence rates (conflicts are rare — fork is ahead, not diverged in
   incompatible ways). A polecat can resolve a typical Go merge conflict
   in 5-15 minutes.

**Critical design choice for scalability:** The gate (build+test) MUST run
on the same worktree that performed the merge, NOT require a fresh clone.
Fresh clones turn a 2-minute gate into a 5-minute gate (clone time + cold
build). At 10+ rigs this difference is significant.

## Constraints Identified

1. **Polecat fleet is the hard ceiling for conflict resolution.** Each
   conflict resolution consumes 1 polecat slot for 5-30 minutes. With 4-8
   polecats per rig, 2+ simultaneous conflicts will block other work. The
   system must queue conflicts, not fan them out.

2. **Test suite wall time (2 min) bounds minimum sync latency.** Even a
   clean merge takes 2+ minutes end-to-end. Sync frequency faster than
   every 3-5 minutes is impossible without deferred verification.

3. **Single-writer constraint on the target branch.** Only one sync can be
   in flight per rig at any time. Concurrent syncs race on push. The cooldown
   naturally enforces this, but a crash during push could leave state wedged.

4. **GitHub API rate limits** are not a binding constraint at current scale
   (5000 requests/hour for authenticated users, sync uses ~5 requests/cycle),
   but become relevant at 100+ rigs sharing a single token.

5. **Memory for build+test.** `go test ./...` at 387% CPU utilization
   implies 4+ parallel test processes. Memory footprint during test run is
   likely 1-2 GB. Multiple concurrent sync gates on the same host could
   OOM a resource-constrained machine.

6. **Git packfile negotiation.** Fetch cost grows with repo history but is
   bounded by delta compression. At 29MB working tree, fetches are fast.
   Would become a concern at 500MB+ repos (large binary assets, vendored deps).

## Open Questions

1. **What is the maximum acceptable divergence window?** Is 6 hours (current)
   acceptable, or does the team need "always within 1 hour of upstream"?
   This determines cooldown bounds.

2. **Should sync run on the crew worktree or a dedicated worktree?** Using
   `crew/gagecane` (current design) means sync blocks crew human work. A
   dedicated `sync/` worktree is isolated but costs disk and setup.

3. **What's the polecat budget for conflict resolution?** Should conflict
   resolution have a dedicated polecat slot (reserved, can't be used for
   other work) or share the general pool? If shared, what's the priority
   relative to feature work?

4. **Is the test suite stable enough for automated gating?** Flaky tests
   will cause spurious sync failures. Current flake rate needs measurement.
   A single flaky test that fails 5% of the time will block sync ~once per
   day at 4 syncs/day cadence.

5. **What happens during a rebase of the fork?** If the fork maintainer
   force-pushes main (e.g., squash-merging a large feature branch), the
   sync system needs to detect non-fast-forward local history and stop
   rather than creating a divergent merge.

## Integration Points

- **Refinery merge queue**: Sync merges bypass the MQ (direct push to main).
  This creates a potential race: if a polecat's MR is being merged to main
  at the same moment sync pushes, one will fail with a non-fast-forward
  rejection. The sync system needs to coordinate with Refinery — either
  sync only runs when MQ is empty (current guard 5), or it uses the MQ
  itself (submits an MR that the Refinery gates and merges).

- **Polecat branches**: After sync advances main, in-flight polecat branches
  may need rebase. The Refinery already handles this (rebase on merge), but
  polecats working on long-lived branches should be notified that main moved.

- **Witness monitoring**: The Witness should monitor sync health — alerting
  if sync hasn't succeeded in >2 cooldown periods (potential credential
  expiry, persistent conflict, or infrastructure issue).

- **Deacon plugin dispatch**: The sync plugin is dispatched by the Deacon
  patrol. At higher rig counts, the Deacon needs to stagger dispatches to
  avoid thundering-herd on `git fetch` and test gates.

- **Build cache**: `go build` and `go test` benefit enormously from build
  cache (`~/.cache/go-build`). The sync worktree MUST share the build cache
  with the main development environment to keep gate times at 8s/2min rather
  than 30s/4min (cold cache).

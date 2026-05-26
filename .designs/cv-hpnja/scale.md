# Scalability Analysis

> Convoy leg: **scale** for the `upstream-sync` design (cv-hpnja).
> Problem: Automatically keep fixes from `gastownhall/gastown` (upstream)
> merged into `gagecane/gastown` (fork) via the local `gastown_upstream` rig.

## Summary

The upstream-sync mechanism — a periodic merge of `upstream/main` into
the fork's main branch — is **inherently bounded by the cadence of
upstream commits, not by our own throughput**. The system scales along
two independent axes: (1) the volume and frequency of upstream commits
arriving between sync cycles, and (2) the number of fork-rigs that
opt into upstream tracking. At current scale (one fork rig,
~240 commits/month upstream, 6-hour sync cooldown), the feature
operates at <1% of every resource ceiling. The interesting scalability
questions are: **what breaks first as upstream velocity grows or
multiple fork-rigs opt in**, and **which design choices made now lock
in superlinear cost later**.

Three structural bottlenecks emerge at scale: (1) **merge conflict
probability grows with sync interval × commit velocity** — longer
intervals mean larger diffs mean more conflicts; (2) **the pre-merge
gate (`check-upstream-rebased.sh`) creates a fleet-wide stop-the-world
when sync falls behind** — every polecat MR fails until sync lands;
and (3) **the crew worktree approach serializes all sync operations
for a rig** — no parallelism, no concurrent rigs sharing a worktree.
All three are manageable at current scale and addressable without
architectural changes at 10-100× scale.

## Analysis

### Key Considerations

The feature touches **five independent scaling axes**:

**1. Upstream commit velocity.** Current: ~240 commits/month (~8/day).
Historical peak: 1561 commits/month (March 2026, ~52/day). At the
current 6-hour cooldown, each sync cycle absorbs ≤12 commits in the
steady case, ≤52 at peak. Merge complexity is sublinear in commit
count (most commits touch distinct files), but conflict probability
is roughly linear in the number of files modified × fork-only
divergence.

**2. Fork divergence (origin-only commits).** Current: 351 commits
ahead of upstream, touching 622 files with ~96K insertions. This is
the "conflict surface" — files modified in both upstream and fork
are conflict candidates on every sync. Larger divergence =
higher per-sync conflict probability. This is the dominant scaling
concern because divergence only grows unless actively managed.

**3. Number of fork-rigs.** Current: 1 (`gastown_upstream`). The
plugin already iterates over `mayor/rigs.json` to discover rigs.
Each rig adds one independent sync cycle (separate crew worktree,
separate state). No cross-rig coupling. Linear scaling.

**4. Repo size.** Current: ~500K lines of Go, 1749 files, ~8K total
commits. Git operations (fetch, merge, push) are O(pack-size) for
network and O(tree-diff) for the merge itself. At current size, a
full fetch is <5s and a merge is <2s. These grow linearly with repo
size but are dominated by network latency, not CPU.

**5. Concurrent polecat load.** The sync plugin's safety rails (#5,
#6) require an empty merge queue and no polecats with active work.
This creates a scheduling constraint: sync can only fire during
quiescent windows. As polecat throughput increases, quiescent windows
shrink. At high throughput (>50 MRs/day), finding a window becomes
the bottleneck.

### Resource Usage

| Resource | Current Load | At 10× | At 100× | Hard Limit |
|----------|-------------|--------|---------|------------|
| Network (fetch) | ~1 MB/cycle | ~10 MB/cycle | ~100 MB/cycle | GitHub rate limit (5000 req/hr) |
| CPU (merge) | <2s/cycle | <5s/cycle | ~30s/cycle | 10min plugin timeout |
| Disk (git objects) | 4K worktree link | Same | Same | Filesystem |
| Memory (git merge) | <50 MB | <100 MB | <500 MB | System RAM |
| Dolt (receipts) | 1 commit/cycle | 4/day | 40/day | ~50ms/commit, negligible |
| Polecat slots | 0 (plugin runs on dog) | 0 | 0 | N/A |
| GitHub API | 0 (local git ops) | 1 push/cycle | 4 push/day | 5000 req/hr |

### Bottlenecks: What Limits Growth?

**Bottleneck 1: Merge conflict probability.**

The probability of conflict per sync cycle is approximately:
```
P(conflict) ≈ 1 - (1 - overlap_ratio)^(commits_per_cycle)
```
Where `overlap_ratio` = files modified in fork ∩ files modified upstream /
total files. With 622 fork-modified files and ~8 upstream commits per
cycle touching ~20 files each, the overlap ratio is roughly
`(622 × 20) / (1749²) ≈ 0.004` per file-pair. At 8 commits/cycle this
gives P(conflict) ≈ 3% per cycle, or roughly one conflict every 8 days.

At 10× upstream velocity (80 commits/cycle): P(conflict) rises to ~25%
per cycle. At 100× (800 commits/cycle): essentially guaranteed conflict
every cycle.

**Mitigation:** Reduce sync interval. At 1-hour cooldown instead of
6-hour, the 10× case drops back to ~4% per cycle.

**Bottleneck 2: Fleet-wide gate failure cascade.**

When `upstream/main` advances and the sync hasn't landed, the
`check-upstream-rebased.sh` gate fails for EVERY polecat MR in the
rig. This is a stop-the-world event: no work can land until sync
completes. Duration of the blockage = time between upstream advance
and successful sync landing.

At current scale: upstream advances ~3 times/day, sync runs every 6h.
Maximum blockage: 6 hours (one full cooldown interval). In practice,
the gate only fails if the sync plugin itself fails (conflict,
quiescence window not found), because the 6h cooldown means sync
runs before most polecat branches complete.

At 10× polecat throughput: gate failures become more visible because
more MRs hit the queue per hour. A 6-hour blockage blocks ~20 MRs
instead of ~2.

**Mitigation:** Priority scheduling. Move sync ahead of polecat work
in the merge queue when the gate detects staleness. The refinery
already has `preserveForkSyncTopology()` for this path.

**Bottleneck 3: Quiescent window scarcity.**

The plugin requires: empty MQ + no polecats with hook_bead. At high
throughput, this window may never naturally occur. The 6-hour cooldown
means the plugin checks ~4 times/day. If each check finds polecats
active, sync never fires.

At 10× polecat throughput (50 MRs/day across 5 polecats): quiescent
windows of >5 minutes occur ~6 times/day (based on the Poisson
arrival model for MR completion events). Sync will still fire.

At 100× (500 MRs/day across 50 polecats): quiescent windows
essentially disappear. Sync never fires without forced scheduling.

**Mitigation:** Relax the quiescence requirement. Instead of "MQ
empty AND no active polecats," use "MQ has no MRs touching
files upstream also touches." This is a semantic check that's more
expensive but allows sync to proceed during normal work.

### Complexity Analysis

| Operation | Time | Space | Notes |
|-----------|------|-------|-------|
| `git fetch upstream` | O(pack_delta) | O(new_objects) | Network-bound; typically <5s |
| `git merge upstream/main` | O(tree_diff) | O(conflict_set) | CPU-bound; <2s for clean merges |
| `git push origin` | O(pack_delta) | O(1) | Network-bound; <3s |
| Safety rail checks | O(beads_query) | O(1) | Dolt query; <100ms each |
| Receipt recording | O(1) | O(1) | Single Dolt commit; ~50ms |
| Conflict detection | O(merge_diff) | O(conflict_files) | Falls out of merge; no extra cost |
| `check-upstream-rebased.sh` | O(fetch + ancestor_check) | O(1) | Per-MR gate; <3s total |
| Rig iteration | O(rig_count) | O(1) | Linear scan of rigs.json |

**Total per-cycle wall-clock:** ~15s (fetch + merge + push + rails)
at current scale. Stays under 60s up to 100× repo size. Well within
the 10-minute plugin timeout.

### Caching Opportunities

**1. Upstream ref cache.** Instead of a full `git fetch upstream` every
cycle, the plugin could check `upstream/main`'s SHA via the GitHub API
(a single HTTP request) and only fetch if it's advanced. At current
scale this saves ~3s/cycle (negligible). At 100× rigs (where each rig
fetches independently), this saves ~300 fetches/day. Worth implementing
at >10 rigs.

**2. Conflict surface cache.** Precompute the set of files modified in
the fork (vs. the last sync point) and cache it. On each cycle, only
check whether upstream's new commits touch those files. If no overlap,
the merge is guaranteed conflict-free and can proceed without the
quiescence guard. This converts ~80% of sync cycles from
"wait for quiescent window" to "fire immediately" at current
divergence levels.

**3. Gate result cache.** The `check-upstream-rebased.sh` gate runs per
MR. After a successful sync lands, ALL pending MRs in the queue will
pass the gate (until upstream advances again). Cache the "last synced
upstream SHA" and skip the gate check for MRs rebased on or after that
SHA. Saves ~3s per MR during the window between sync and next upstream
advance.

### Degradation Modes: What Happens at Limits?

**At 10× upstream velocity (~80 commits/day, ~2400/month):**
- Sync interval should shrink to 2h (from 6h) to keep conflict
  probability below 10% per cycle.
- Gate failures affect more in-flight MRs; priority scheduling
  becomes valuable.
- Resource usage remains negligible (fetch size grows linearly).
- No architectural changes needed.

**At 100× upstream velocity (~800 commits/day):**
- Sync becomes nearly continuous (every 15-30 minutes).
- Conflict probability per cycle stays manageable if interval is
  short enough.
- Quiescent window requirement becomes the binding constraint;
  must be relaxed or removed.
- Consider: instead of periodic merge, use a webhook/event-driven
  trigger from upstream push events. This eliminates the cooldown
  model entirely.
- Resource usage still manageable (git is efficient at incremental
  operations).

**At 10× fork divergence (3500 fork-only commits, ~6000 files):**
- Conflict probability rises sharply regardless of sync frequency.
- Every sync is a potential conflict event.
- Mitigation: reduce divergence (upstream more fork changes) or
  accept that manual resolution is part of the steady-state workflow.
- The escalation path (abort merge + file bead + mayor dispatches
  resolution polecat) is already designed for this case.

**At 100× rigs (100 independent fork-rigs):**
- Each rig is independent; no cross-rig coupling.
- Total plugin execution time: 100 × 15s = 25 minutes if sequential.
  Plugin already iterates serially. At this scale, parallelize rig
  processing (trivial: each rig uses its own worktree).
- Dolt commit budget: 100 receipts/cycle × 4 cycles/day = 400
  commits/day. At ~50ms/commit = 20s/day of Dolt write time.
  Negligible.
- GitHub push budget: 100 pushes/day (one per rig per successful
  sync). Well within rate limits.

**When sync completely fails (plugin disabled, persistent conflicts):**
- Gate cascade: all polecat MRs fail `check-upstream-rebased.sh`.
- Rig becomes inoperable within one upstream advance (~8 hours
  at current velocity).
- Recovery: manual `git merge upstream/main` on the crew worktree,
  resolve conflicts, push. Or: `gt upstream sync --force` (future
  CLI command) to bypass quiescence guards.
- The 6-hour cooldown means maximum 6 hours of gate failures between
  retry attempts.

### Options Explored

#### Option 1: Current design (6h cooldown, full quiescence, serial rig iteration)

- **Description:** Ship as designed in `plugins/sync-upstream/`: 6-hour
  cooldown gate, all 7 safety rails, serial rig iteration, merge-or-
  escalate on conflict.
- **Pros:**
  - Zero scaling problem at current load (1 rig, ~8 commits/day
    upstream).
  - All safety rails prevent race conditions.
  - Simple implementation; plugin already written and tested.
  - 15s/cycle wall-clock; well within 10min timeout.
- **Cons:**
  - 6h cooldown means up to 6h of gate failures after upstream advance.
  - Quiescence requirement becomes binding at >50 MRs/day.
  - Serial rig iteration becomes slow at >10 rigs.
- **Effort:** Low — already implemented.

#### Option 2: Adaptive cooldown with conflict-surface awareness

- **Description:** Dynamically adjust the cooldown based on upstream
  velocity. When upstream is active (>5 commits since last sync),
  shrink cooldown to 1h. When quiet, expand to 12h. Add conflict-
  surface pre-check: if upstream's new commits don't touch fork-modified
  files, skip quiescence and sync immediately.
- **Pros:**
  - Keeps conflict probability bounded regardless of upstream velocity.
  - Reduces gate failure windows from 6h to <1h during active periods.
  - Conflict-surface pre-check eliminates ~80% of quiescence waits.
- **Cons:**
  - More complex scheduling logic; needs upstream commit rate tracking.
  - Conflict-surface cache requires maintaining a "fork-modified files"
    set that updates on every merge to main.
  - Adds a state field to the plugin's tracking.
- **Effort:** Medium. Requires: (a) commit rate monitor in the plugin,
  (b) fork-modified file set computation, (c) adaptive cooldown gate.

#### Option 3: Event-driven sync (webhook/push trigger)

- **Description:** Instead of polling on a cooldown, trigger sync
  immediately when upstream pushes to main. Use GitHub webhooks (if
  available) or a lightweight polling loop (check upstream/main SHA
  every 5 minutes; sync only if changed).
- **Pros:**
  - Minimum latency between upstream advance and fork sync (~5 minutes).
  - Gate failure window shrinks to <10 minutes.
  - No wasted cycles polling when upstream is quiet.
  - Conflict size per sync is minimized (fewer commits per event).
- **Cons:**
  - Webhook infrastructure doesn't exist in Gas Town today.
  - Lightweight polling (5-minute SHA check) is functionally equivalent
    to a short cooldown with negligible cost, and doesn't require
    webhook infrastructure.
  - At very high upstream velocity (>100 pushes/day), could trigger
    too many sync cycles. Needs a minimum cooldown even in event mode.
- **Effort:** Low for poll-based variant (just reduce cooldown + add
  SHA change check). High for true webhooks (new infrastructure).

#### Option 4: Priority-scheduled sync with MQ integration

- **Description:** Instead of waiting for a quiescent window, schedule
  the sync merge as a special MR in the refinery's merge queue with
  highest priority. The refinery already handles fork-sync topology
  preservation via `preserveForkSyncTopology()`. Let it manage the
  merge timing rather than the plugin.
- **Pros:**
  - Eliminates the quiescent window bottleneck entirely.
  - Leverages existing refinery infrastructure (batching, bisecting).
  - Natural priority: sync MRs go first, then polecat MRs.
  - The refinery already knows how to preserve merge topology.
- **Cons:**
  - The refinery currently only merges polecat branches (one-parent
    merge). A sync "MR" is actually a merge of upstream/main — this
    is a two-parent merge that the refinery doesn't currently handle.
  - Adds complexity to the MQ: a sync MR can conflict with polecat
    MRs in the same batch.
  - Failure of the sync MR would trigger bisection logic designed for
    polecat MRs, which may not make sense for a sync.
- **Effort:** High. Requires refinery changes to handle sync MRs as
  a distinct MR type with different conflict/retry semantics.

### Recommendation

**Ship Option 1 (current design) for v1. Plan Option 2 for v2.**

The v1 design operates at <1% of every resource ceiling. The 6-hour
cooldown is adequate for current upstream velocity (~8 commits/day).
The safety rails are correctly conservative for a feature that modifies
the fork's main branch.

**For v2** (when upstream velocity exceeds 20 commits/day or a second
fork-rig opts in):

1. **Reduce default cooldown to 2h** and add a SHA-change guard (don't
   sync if upstream hasn't advanced since last sync). This is Option 3's
   poll-based variant — trivial to implement, halves gate failure window.

2. **Add conflict-surface pre-check** (Option 2 partial): compute the
   fork-modified file set once after each successful sync, cache it.
   On next cycle, if upstream's new commits don't touch those files, skip
   quiescence. This eliminates ~80% of unnecessary waits.

3. **Parallelize rig iteration** once >3 rigs opt in. Each rig's sync
   is independent; run them concurrently with a pool of 5.

**Defer Option 4** (refinery MQ integration) unless the quiescent window
problem becomes acute (>100 MRs/day). The refinery changes required are
non-trivial and the plugin approach works well up to that threshold.

## Constraints Identified

- **Per-cycle wall-clock must stay under 10 minutes** (plugin timeout).
  At current scale: 15s. At 100× repo size: ~60s. No risk. But if
  conflict resolution is added to the automated path (future), it
  could exceed the timeout on complex conflicts.

- **The `check-upstream-rebased.sh` gate is a binary stop/go** — there's
  no "partially synced" state. Either upstream/main is an ancestor of
  HEAD or it isn't. This creates a hard dependency: sync must fully
  succeed (merge + push) before any polecat work can land. Partial
  sync (e.g., sync one rig but not another) is fine because rigs are
  independent.

- **Quiescent window requirement constrains sync frequency.** Safety
  rails #5 and #6 (empty MQ, no active polecats) create a scheduling
  constraint. If the rig's throughput exceeds ~50 MRs/day, the window
  shrinks below the 6h cooldown check interval and sync may never fire.
  This is the first constraint that needs relaxing at scale.

- **Fork divergence is monotonically increasing** unless fork changes
  are upstreamed. The 351 fork-only commits will continue growing,
  increasing conflict surface over time. The sync mechanism doesn't
  reduce divergence — it only prevents upstream from falling further
  behind. A complementary "upstream fork changes" workflow would
  address this, but is out of scope for the sync feature.

- **Git push requires write access to origin's main branch** (or the
  integration branch). The plugin pushes via `git push origin HEAD:main`
  or `HEAD:gagecane/gt`. This is a credential requirement: the crew
  worktree must have push access. If credentials expire or are revoked,
  sync silently fails (push error → skip → receipt recorded).

- **Serial rig iteration means total sync time = N × per-rig time.**
  At 10 rigs × 15s = 2.5 minutes (fine). At 100 rigs × 15s = 25 minutes
  (exceeds 10-minute plugin timeout). Must parallelize before 40 rigs.

## Open Questions

1. **Should the cooldown be configurable per-rig, or town-wide?**
   Rigs with different upstream velocities may want different intervals.
   Current design uses a single 6h cooldown for the plugin. Recommendation:
   per-rig override via rig config (`upstream_sync_cooldown: 2h`), with
   the plugin-level cooldown as the floor. **Cross-ref:** API dimension
   owns the config schema.

2. **What's the acceptable gate-failure window?** The time between
   upstream advance and sync landing determines how long polecats are
   blocked. Current max: 6 hours. Is this acceptable for v1?
   Recommendation: yes for v1 (polecats are a small fleet); reduce to
   <2 hours for v2. **Cross-ref:** Integration dimension owns the gate
   behavior.

3. **Should the sync pre-empt the quiescence requirement when the
   gate is actively failing?** If `check-upstream-rebased.sh` is failing
   for MRs in the queue, the sync should fire immediately regardless of
   other safety rails — the alternative is a complete rig halt.
   Recommendation: yes, add a "gate-failure override" that relaxes
   rails #5 and #6 when the gate has been failing for >1 hour.
   **Cross-ref:** Integration dimension on override semantics.

4. **At what fork divergence level should we alert?** If fork-only
   commits grow past a threshold (e.g., 1000 commits, 100K LOC delta),
   conflict probability per sync becomes >50% and the automated path
   becomes unreliable. Should we alert the rig owner to upstream their
   changes? Recommendation: alert at >500 fork-only commits.
   **Cross-ref:** UX dimension on alerting surface.

5. **Should conflict resolution be automated (polecat-dispatched) or
   always manual?** Current design: abort + escalate + human resolves.
   At 10× scale with ~25% conflict rate, that's a human resolution
   every ~4 days. Automation is attractive but risky (wrong resolution
   corrupts main). Recommendation: keep manual for v1; file as v2
   research item. **Cross-ref:** Security dimension on automated
   resolution trust model.

6. **Is the 10-minute plugin timeout sufficient at scale?** At current
   size: 15s/cycle. At 100 rigs (serial): 25 minutes → timeout
   exceeded. When should we parallelize? Recommendation: parallelize
   when >5 rigs opt in (proactive; avoids the timeout cliff).
   **Cross-ref:** Integration dimension on plugin execution model.

## Integration Points

- **Security:** The sync performs `git push` to `origin/main` — a
  privileged operation. Credential rotation, access control, and
  audit logging are security's domain. The conflict-surface pre-check
  (Option 2) touches file-level diff data which could leak sensitive
  paths. Coordinate: file-set cache should not be stored in beads
  (which are Dolt-committed and visible to all agents); use a local
  ephemeral file instead.

- **Integration:** The `check-upstream-rebased.sh` gate and the
  refinery's `preserveForkSyncTopology()` are both dependencies.
  The gate creates the forcing function (sync must succeed for work
  to land); the refinery preserves the merge topology after sync.
  Scale concern: if the gate is cached (per our recommendation), the
  cache invalidation contract must be coordinated with Integration.

- **Data Model:** Per-rig sync state (last sync SHA, last conflict
  time, conflict file list, cooldown override) needs storage. Current
  design uses receipts (ephemeral beads with labels). At scale, a
  dedicated state bead per rig (similar to auto-test-pr's state bead)
  is cleaner. Coordinate: state bead schema with Data Model dimension.

- **API & Interface:** The `gt upstream status` command (proposed in
  API dimension) should surface scalability-relevant metrics: time
  since last sync, commits behind, conflict probability estimate,
  quiescent window availability. These are the operator's early
  warning system.

- **UX:** The gate failure experience for polecats is the primary
  user-facing scalability impact. When the gate fails, the polecat
  sees a cryptic error from `check-upstream-rebased.sh`. At scale,
  this error should include: "Sync is pending; expected to complete
  within ~X minutes. Your MR will be retried automatically." This
  converts a confusing blocker into an expected wait.

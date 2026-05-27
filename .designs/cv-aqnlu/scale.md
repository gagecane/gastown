# Scalability Analysis

## Summary

The proposed plugin-and-formula system for automated code quality analysis
composes two existing substrates (plugins for gating/triggering, convoy formulas
for parallel execution) into a feature whose scalability is dominated by a
single axis: **polecat fleet contention**. The trigger side (plugin gate
evaluation, Deacon patrol scanning) is trivially cheap at any realistic scale.
The execution side (a 7-leg convoy formula where each leg spawns a polecat) is
where resource limits bind.

At current scale (1 rig, 4-8 polecats, ~50 commits/day, one Deacon patrol
cycle every few minutes), a daily quality analysis consumes 7 polecat-slots for
5-15 minutes each, blocking ~35-105 polecat-minutes of feature work. This is
roughly 4-12% of a single rig's daily polecat capacity (assuming 8 polecats
working 8 productive hours/day = ~3840 polecat-minutes). Acceptable for daily
cadence, but the system must degrade gracefully as rigs, plugins, and convoy
legs multiply.

## Analysis

### Key Considerations

- **Plugin scanning is O(n) in plugin count, evaluated every patrol cycle.**
  The Deacon's `DiscoverAll()` walks `~/gt/plugins/` and `<rig>/plugins/`
  directories, reading one `plugin.md` TOML file per plugin. At 20 plugins
  (current state), this is ~20 file reads per patrol (~5ms). At 200 plugins,
  still under 50ms. Not a bottleneck.

- **Gate evaluation is the per-cycle cost.** Each gate type has different cost:
  - `cooldown`: 1 Dolt query (`CountRunsSince`) per plugin = ~50-200ms/query.
    At 20 plugins, this is 1-4 seconds per patrol cycle. At 200 plugins, 10-40
    seconds per patrol. **Dolt query latency is the binding constraint for gate
    evaluation at scale.**
  - `condition`: Runs a shell command (e.g., `git rev-parse`, `git log`). Fast
    (~100ms each) but forks a process per plugin. At 200 plugins, 20 seconds of
    sequential condition checks. Parallelizable.
  - `cron`: Pure computation (compare current time to schedule). Zero cost.
  - `event`/`manual`: No evaluation cost (only fires on explicit trigger).

- **Dolt is the data plane, and it is fragile.** Each `RecordRun()` creates a
  bead via `bd create` (= 1 Dolt commit) then immediately closes it (= 1 more
  Dolt commit). At 20 plugins running daily, that's 40 Dolt commits/day just
  for plugin receipts. At 200 plugins: 400 commits/day. Dolt can handle this
  throughput, but compound with other sources (polecats, refinery, mail) and
  total commit rate becomes relevant.

- **Convoy formula execution is O(legs) in polecat slots.** A 7-leg quality
  analysis formula consumes 7 polecat slots simultaneously. With 8 polecats
  per rig, this starves the system for the duration of the analysis (only 1
  slot left for feature work). At 10 legs (future expansion): complete
  polecat starvation.

- **Each polecat leg's execution is bounded by LLM context window and token
  throughput.** A quality analysis leg reads code, analyzes it, and writes a
  report. For a 29MB working tree with 1355 Go files, a whole-codebase scan
  per leg could take 10-30 minutes depending on how much code the LLM reads.
  This is the dominant wall-clock cost.

- **Plugin receipts (wisps) accumulate and need digesting.** The daily digest
  pattern (squash old wisps into a summary) keeps the ledger clean, but the
  digest itself is a periodic cost. Orphan receipts from failed or partial
  plugin runs can pollute Dolt if the reaper doesn't catch them.

### Options Explored

#### Option 1: Unrestricted Convoy (all legs parallel, no resource guarding)

- **Description**: Sling the 7-leg convoy formula as-is. All legs dispatch
  simultaneously to available polecats. Quality analysis takes however many
  slots it needs. Other work waits.
- **Pros**:
  - Simplest implementation (standard convoy execution)
  - Fastest wall-clock completion (all legs parallel = bottleneck is slowest leg)
  - No new coordination infrastructure needed
- **Cons**:
  - 7/8 polecats consumed for 10-30 minutes. Feature work starves.
  - At busy times (merges in flight, other convoys running), may exceed
    fleet capacity entirely (legs queue in auto-dispatch, latency explodes)
  - No backpressure: if quality analysis triggers alongside another convoy,
    combined demand = 14+ slots with only 8 available
  - Priority inversion: low-priority quality analysis blocks high-priority
    feature work
- **Effort**: Low

#### Option 2: Throttled Convoy (max-parallelism cap)

- **Description**: Extend convoy formulas to support a `max_parallelism`
  setting. For quality analysis, cap at 3-4 legs running simultaneously.
  Remaining legs queue until a slot frees. Total wall-clock time increases
  (2x if half-parallel), but fleet starvation is avoided.
- **Pros**:
  - Predictable resource consumption: max 3-4 polecats at any time
  - Feature work retains 4-5 polecat slots during analysis
  - Scales to larger leg counts without proportional resource consumption
  - General-purpose feature (benefits all convoy formulas, not just quality)
- **Cons**:
  - Longer wall-clock time for the full analysis (~2x at 50% parallelism)
  - Needs new convoy execution logic (queue within formula dispatch)
  - More complex synthesis scheduling (synthesis must wait for ALL legs,
    not just first-batch legs)
- **Effort**: Medium

#### Option 3: Off-Peak Scheduling (cron gate + fleet-check condition)

- **Description**: The plugin's gate fires only during off-peak hours (e.g.,
  nights/weekends when polecats are mostly idle) AND only if fleet utilization
  is below a threshold (e.g., <50% of slots occupied). This ensures quality
  analysis never competes with feature work.
- **Pros**:
  - Zero impact on productive work time
  - Full parallelism available (all 7 legs) since fleet is idle
  - No new convoy execution infrastructure needed
  - Gate logic is entirely in the condition check script
- **Cons**:
  - Results are always "stale by business hours" (analyzed last night's code)
  - May never fire if the system is always busy (e.g., 24h CI workload)
  - Timezone-dependent: "off-peak" varies per team
  - Human developers may want fresh results at code review time
- **Effort**: Low

#### Option 4: Incremental Analysis (diff-since-last, not whole-codebase)

- **Description**: Instead of analyzing the entire codebase every time, each
  leg only analyzes files changed since the last quality analysis run. The
  analysis is incremental: findings are accumulated across runs, with stale
  findings (for files that haven't changed) carried forward from the previous
  run.
- **Pros**:
  - Each leg processes 5-50 files instead of 1355, completing in 2-5 minutes
    instead of 10-30 minutes
  - Reduces polecat slot occupancy by 3-6x
  - Naturally scales with commit velocity, not codebase size
  - Results are always fresh (can afford to run more frequently)
  - Periodic full-analysis (weekly) catches drift that incremental misses
- **Cons**:
  - More complex leg instructions (must accept file-list input, not roam freely)
  - Needs state tracking: "which files have been analyzed, when?"
  - Stale findings can become incorrect if refactoring moves code
  - First run (or full-analysis fallback) still has whole-codebase cost
  - Some quality dimensions (architecture drift, dead code) require whole-
    codebase context regardless of what changed
- **Effort**: Medium-High

### Recommendation

**Option 2 (Throttled Convoy) + Option 4 (Incremental Analysis)** combined,
implemented in two phases:

**Phase 1 (v1):** Add `max_parallelism = 3` to the quality analysis convoy
formula and use the `condition` gate with embedded cooldown (from the
integration analysis). This immediately bounds resource consumption to 3
polecat slots and ensures the system doesn't starve feature work.

**Phase 2 (v2):** Implement incremental analysis with `--since` semantics
passed as a formula variable to leg prompts. Each leg receives a list of
changed files (or "full" for periodic whole-codebase sweeps). This reduces
per-leg execution time from 10-30 minutes to 2-5 minutes, enabling more
frequent analysis without proportional cost increase.

**Why not Option 3?** Off-peak scheduling is a good interim band-aid but
doesn't scale as the system grows. It's also fragile (assumes predictable
quiet periods) and doesn't help when teams want fresh results during work
hours.

**Scaling projection with recommended approach:**

| Scale | Legs | Max-parallel | Slot-minutes/run | Frequency | Daily cost |
|-------|------|--------------|------------------|-----------|------------|
| Current (1 rig, v1) | 7 | 3 | ~45 min | Daily | 45 min |
| Current (1 rig, v2 incremental) | 7 | 3 | ~15 min | Daily | 15 min |
| 5 rigs (v2) | 7 | 3 | ~15 min/rig | Daily | 75 min |
| 10 rigs (v2) | 7 | 3 | ~15 min/rig | Daily | 150 min |

At 10 rigs, 150 polecat-minutes/day is still only ~3% of total fleet capacity
(assuming 10 rigs x 8 polecats x 8h = 38,400 polecat-minutes/day).

## Constraints Identified

1. **Polecat fleet is the hard ceiling.** A rig with 8 polecats cannot run
   more than 8 convoy legs simultaneously. Quality analysis MUST NOT consume
   all available slots. Recommended cap: `max_parallelism <= fleet_size / 2`.

2. **Dolt query latency bounds gate evaluation throughput.** Each cooldown
   gate evaluation requires a `CountRunsSince()` query (~50-200ms). With 200
   plugins, sequential gate evaluation takes 10-40 seconds. For acceptable
   patrol cycle time (<60s), gate evaluation must be parallelized or batched
   into a single Dolt query at >50 plugins.

3. **LLM context window limits per-leg scope.** A quality analysis leg cannot
   "read all 1355 files" in one shot. It must use sampling, grep-based
   targeting, or file-list scoping. The effective scope per leg is ~20-50
   files (depending on file size), limiting what a single leg can analyze.

4. **Formula convoy execution creates O(legs) Dolt commits.** Each leg bead
   (create + dispatch + close) = 3-6 Dolt commits. A 7-leg convoy = ~21-42
   Dolt commits. The synthesis step adds ~3 more. Total: ~25-45 Dolt commits
   per quality analysis run. At daily frequency, this is negligible. At hourly,
   it compounds with other system activity.

5. **Synthesis step is serialized by design.** It depends on ALL legs
   completing. The synthesis polecat cannot start until the slowest leg
   finishes. With `max_parallelism = 3` and 7 legs, legs execute in ~3
   batches, so synthesis waits for batch-3 to complete (total wall-time =
   3 x slowest-leg-in-batch).

6. **Plugin receipt accumulation.** Each plugin run creates 1 receipt bead.
   Without daily digests, receipts accumulate at `plugins_count x runs/day`
   rate. At 20 plugins with daily frequency: 20 receipts/day = 600/month.
   The reaper handles cleanup, but digest generation should be prioritized
   for frequently-running plugins.

7. **Condition gate shell commands must be fast and idempotent.** A condition
   gate that runs `git log --since` or `git diff --stat` must complete in
   <1 second. Git operations on a 29MB repo are fast, but care must be taken
   to avoid unbounded `git log` traversals. Always use `--limit` or `--since`.

## Open Questions

1. **What is the acceptable polecat-starvation budget for quality analysis?**
   If 3/8 slots is too many during peak hours, should it drop to 2 during
   work hours and expand to 5 during off-peak? Dynamic parallelism adds
   complexity.

2. **Should quality analysis results be cached and queryable?** If so, the
   Dolt schema needs a `quality_findings` table (or findings as bead labels).
   This enables trend analysis across runs but adds schema maintenance burden.

3. **What is the maximum acceptable wall-clock time for a full analysis?**
   With 7 legs at `max_parallelism = 3`, wall-clock time is ~3 batches x
   10-30 min/batch = 30-90 minutes. Is this acceptable for daily cadence, or
   does it need to complete within a tighter window?

4. **How should the system handle leg timeouts?** If one leg hangs (LLM
   confusion, infinite loop in tool calls), it blocks subsequent legs in the
   same parallelism slot AND delays synthesis. Should timed-out legs produce
   a "timed out" report and allow synthesis to proceed?

5. **Should per-leg priority be configurable?** Some quality dimensions
   (security, dead-code) may be higher-value than others (documentation
   coverage). Priority-ordering within the `max_parallelism` queue ensures
   high-value legs run first, so if the analysis is interrupted, the most
   important results exist.

6. **Is there a Dolt query batching opportunity?** Instead of N sequential
   `CountRunsSince()` calls during gate evaluation, could the Deacon issue
   one SQL query like `SELECT plugin, COUNT(*) FROM runs WHERE created_at >
   ? GROUP BY plugin`? This would reduce gate evaluation from O(n) round-trips
   to O(1), but requires a SQL interface to the beads data.

## Integration Points

### -> Plugin System (`internal/plugin/`)

- `max_parallelism` is a convoy formula feature, not a plugin feature. The
  plugin's only scaling-relevant setting is its gate (which controls trigger
  frequency). The plugin frontmatter doesn't need new fields for scalability.
- Gate evaluation parallelism (for the future 200-plugin scenario) would
  require changes to the Deacon's patrol loop, not to the plugin package.

### -> Formula System (`internal/formula/`)

- **New field needed: `max_parallelism` in convoy formula TOML.** This tells
  `gt sling` to dispatch at most N legs simultaneously and queue the rest.
  Default: unlimited (current behavior). Quality formula sets it to 3.
- Formula variable `{{.since}}` can pass a timestamp or file-list to leg
  prompts for incremental analysis mode.

### -> Deacon Patrol / Gate Evaluation

- At current plugin count (~20), sequential gate evaluation is fine.
- At 50+ plugins, the Deacon patrol cycle time will exceed 60s from gate
  evaluation alone. The patrol loop in `mol-deacon-patrol` should be
  instrumented to track per-plugin gate evaluation time.
- Future optimization: batch cooldown gate queries into a single Dolt query.

### -> Polecat Fleet / Auto-Dispatch

- Quality analysis legs are normal task beads dispatched by auto-dispatch.
  The `max_parallelism` cap must be enforced at the convoy-sling level (before
  beads are created), not at the auto-dispatch level (too late - beads already
  exist and are dispatchable).
- Alternative: create all 7 leg beads but mark surplus legs as `blocked` on
  the currently-running legs. This uses existing dependency tracking to
  serialize batches. Simpler than new convoy queue logic, but less efficient
  (blocked beads consume Dolt rows and auto-dispatch evaluation time).

### -> Dolt Data Plane

- Receipts (40 Dolt commits/day for 20 plugins) are well within Dolt's
  throughput. At 200 plugins (400 commits/day), still fine. Dolt handles
  thousands of commits/day without degradation.
- The bigger Dolt concern is **query latency under load**. When multiple
  polecats and the Deacon all query Dolt simultaneously, response times
  increase. The system should avoid querying Dolt during peak convoy activity
  (when 7 polecats are simultaneously creating/closing beads).

### -> Refinery / Merge Queue

- Quality analysis legs that produce code-committed reports (`.quality/`) will
  submit MRs to the merge queue. 7 simultaneous MRs from the same convoy could
  create merge conflicts with each other (all modify different files in the
  same directory). Solution: only the synthesis step commits results, not
  individual legs. Legs write to beads; synthesis aggregates and commits.

### -> Scalability Dimension (self-referential)

- This analysis itself is a product of the design convoy formula (the "scale"
  leg). The convoy formula's scalability characteristics apply to itself:
  6 parallel legs consumed 6 polecat slots for this design exploration. The
  `max_parallelism` recommendation would also benefit design convoys.

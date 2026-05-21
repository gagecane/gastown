# Scalability Analysis

## Summary

The auto-test-PR mechanism, **as scoped for v1 (Refinery-only, Go-only,
single pilot rig `gastown_upstream`, ≤1 PR per rig per 7-day window, ≤3
files / ≤200 LOC per PR)**, has effectively no scalability problem. The
proposed flow runs at a steady state of roughly **one cycle per week per
opted-in rig**, which at the v1 footprint is one cycle per week
*total*. Every storage and compute axis (Dolt commits for state-bead
transitions, polecat fleet concurrency, Refinery merge queue throughput,
GitHub API rate limits) operates at <0.1% of its current saturation
point. v1 is comfortably small.

The interesting scaling questions are not "can v1 hold up?" but **"what
breaks first as we generalize, and which choices made for v1 lock in a
linear-or-worse cost as we add rigs?"**. Three structural choices in the
PRD have superlinear cost that is invisible at v1 scale: (1) the
**synthetic-mutant sanity check** (Q2 MUST gate 2) is `O(test_count ×
covered_lines × test_runtime)` per cycle on a rig — fine on a 200-LOC
package, painful on a multi-MB monorepo target; (2) the **N=10
flakiness re-run** is `O(N × test_runtime)` and dominates cycle
wall-clock once test suites exceed a few minutes; and (3) **target
selection ranks "all changed files in last 30 days"** — that's a
linear scan over rig churn that becomes a real cost at large repos.
The remaining scaling concerns (Dolt CAS contention, fleet starvation,
Refinery MQ saturation) are bounded by the explicit per-rig and
town-wide rate caps and don't surface until two-orders-of-magnitude
beyond pilot.

## Analysis

### Key Considerations

The mechanism touches **six independent scaling axes**. Each can be
analyzed largely independently, then composed.

**1. Rig count (number of opted-in rigs).** v1 = 1 rig. Realistic
near-term = 5-10 (one per active project rig in the town). 10x = 100
rigs (every Gas Town instance has every rig opted-in). 100x = 1000
rigs (multi-org Gas Town federation). Cost per rig per cycle is
**roughly constant** — the cycle is read-state, scan-coverage,
dispatch-polecat. Total cost is `O(rig_count)` linear, with no
super-linear coupling unless the Mayor patrol serializes ticks across
rigs (which it shouldn't, but see "Bottlenecks" below).

**2. Cycle frequency (PRs per rig per unit time).** v1 = ≤1 PR /
7-day window per rig. Hard cap is structural (PRD Q7 state machine).
A 10x bump (≤1 PR/day per rig) would multiply Mayor patrol ticks and
polecat dispatch volume by 7×, but each cycle still does fixed work.
A 100x bump (multiple PRs per rig per day) starts colliding with the
single-open-PR constraint and would require redesigning the
`mr-pending → mr-revising → cooled-down` state machine to permit
parallel branches per rig — out of v1 scope.

**3. Per-cycle polecat work.** This is the dominant cost axis and
the one most sensitive to repo size. Per cycle:
- `O(rig_loc)` git churn scan (last 30 days).
- `O(coverage_tool_runtime)` to produce a coverage profile.
- `O(target_count)` LLM-prompt cost to author tests.
- `O(target_count × test_count_authored × test_runtime × N)` for
  flakiness re-runs.
- `O(target_count × covered_lines_in_test × test_runtime)` for the
  synthetic-mutant sanity check.

The last two terms are the ones that grow on real codebases. On a
small Go package (~10 tests, ~1s suite), one cycle is sub-minute.
On a Go package with a few-minute test suite — say, gastown_upstream's
`internal/dolt` end-to-end tests — a single cycle could exceed
30 minutes from flakiness re-runs alone.

**4. Mayor coordination.** Mayor owns the state bead per rig. Each
cycle is **one Dolt CAS** (read-modify-write on the
`<rig>-auto-test-state` bead). Cycles per second is bounded by
`rig_count / cadence`. At 100 rigs × ≤1 PR / 7-day window =
~0.0001 cycles/sec town-wide. Dolt commit budget on this is
imperceptible (it's been measured at ~50ms per commit in the pinned
context, so even 1000 rigs is <1 commit per minute average).

**5. Refinery merge queue load.** Each successful auto-test cycle
produces one MR bead. v1 = 1 MR/week town-wide. 100x = 100 MRs/week
across all rigs = ~14 MRs/day. The Refinery already handles the
town's normal MR throughput (estimate from `git log` patterns:
~10-50 commits/day landing); adding 14/day is a 30-100% bump on
small towns, negligible on large ones. **Hard limit:** the Refinery's
bisecting-batcher behavior degrades when a batch contains
multiple-MRs-with-shared-files; auto-test PRs touching the same test
files as concurrent feature MRs will produce extra bisects. Quantified
in Bottlenecks below.

**6. GitHub API surface (deferred to v2).** v1 is Refinery-only;
no GitHub PR is opened, no API calls are made. Once v2 ships
external-PR mode, every cycle does at minimum 3-5 GitHub API calls
(check-existing-PR, create-PR, label-PR, post-body, fetch-comments
on revision). Per-rig GitHub App rate limits are 5000 req/hr. Even
at 100x cycle frequency this is <1% of the budget.

### Options Explored

#### Option 1: As-spec'd v1 (Refinery-only, ≤1 PR/rig/7d, full Q2 MUST gates)

- **Description:** Implement exactly the v1 PRD: single Mayor patrol
  ticks every 24h; coverage scan + churn ranking + AST-aware
  synthetic-mutant + N=10 flakiness + tautology lint + diff-marker;
  state bead serializes per-rig.
- **Pros:**
  - Zero scaling problem at v1. Linear in rig count, constant per
    cycle, no contention surfaces.
  - Hard caps (≤1 PR/rig/7d, ≤200 LOC, ≤3 files) bound blast radius
    of any individual cycle's runtime.
  - Reuses existing primitives (Refinery MQ, Dolt CAS, polecat fleet)
    which are already proven at the town's existing throughput.
- **Cons:**
  - The synthetic-mutant sanity check is `O(test × covered_lines ×
    test_runtime)`. On a package with a 5-minute test suite and 5
    new tests covering ~20 lines each, one cycle = `5 × 20 × 5min = 500min`
    if naively sequential. Mitigation: cap to one mutant per test
    (the PRD already implies this), and run mutants only against the
    direct package, not the rig.
  - N=10 flakiness re-run on the new tests' direct package is fine
    for fast packages (<1min) and fatal for slow ones (>5min).
  - Coverage scan walltime is dominated by `go test -coverprofile`
    on the rig — bounded by the test suite, not by the mechanism.
- **Effort:** This is the v1 plan; "additional scalability work"
  effort is **Low** — the plan already absorbs the right caps.

#### Option 2: Lazy / sampling target selection at large rig sizes

- **Description:** Instead of scanning *all* changed files in the
  last 30 days each cycle, sample K files (K=20) and rank only
  those. Skip the full churn × coverage join.
- **Pros:**
  - Bounded `O(K)` per cycle independent of rig size. Useful when
    pilot generalizes to monorepo-sized rigs.
  - Cheap, easy to add behind a config flag (`auto_test_pr.target_sample = N`).
- **Cons:**
  - Random sampling produces lower-quality picks. Maintainer-reject
    rate likely rises 10-20pp for a few cycles before the cooldown
    self-corrects.
  - Adds a config knob the PRD's Q4 explicitly disallows ("language-
    keyed allow-list, no custom commands in v1").
- **Effort:** Low. Can be added as a v2 escape hatch when a
  rig's `gt rig config auto_test_pr.cycle_walltime_p99 > 5min`.

#### Option 3: Two-tier pre-filter for synthetic-mutant check

- **Description:** Instead of running the AST mutant for every new
  test in a fresh tmpdir (which copies the whole package), maintain
  a single tmpdir per cycle, run all mutants against it serially,
  and revert via `git checkout` between mutants rather than by
  re-copying.
- **Pros:**
  - Drops mutant-check walltime by ~2-5× depending on package size
    (the dominant cost is the tmpdir copy, not `go test`).
  - Naturally amortizes a single coverage rebuild across all
    mutants in the same cycle.
- **Cons:**
  - More fragile: a mutant that produces a syntax error and a
    failed `git checkout` could leave the tmpdir in a bad state.
    Needs a "tmpdir checksum" guard between mutants.
  - More complex polecat formula step.
- **Effort:** Medium. Worthwhile when generalizing past Go pilot
  to languages with slower compile cycles (Rust, TypeScript with
  large dep graphs).

#### Option 4: Async / pipelined polecat fleet (deferred)

- **Description:** At >100x rigs, queue cycles in a Mayor-owned
  work bead rather than dispatching synchronously. Polecat pool
  pulls from queue; cycles can land out of order.
- **Pros:**
  - Decouples cycle-trigger rate from polecat-pool capacity.
    Useful at multi-org-federation scale (1000+ rigs).
- **Cons:**
  - State-machine-per-rig still serializes; queueing only helps if
    rigs differ. With the per-rig cooldown, this is largely a
    non-problem.
  - Adds queue, retry, idempotency surface that v1's
    compare-and-set on a single state bead avoids.
- **Effort:** High. Deferred to v3+. Not needed at v2 scale (10-100 rigs).

#### Option 5: Eager coverage cache vs. lazy per-cycle scan

- **Description:** Cache the coverage profile per rig in a
  Mayor-owned bead, refreshed only on `main`-branch changes
  (Refinery tells Mayor "rig X just merged a commit, invalidate
  its coverage cache"). Cycle reads the cached profile.
- **Pros:**
  - Drops coverage-walltime from the per-cycle critical path
    entirely. Cycle becomes ~10s instead of ~5min.
  - Coverage is already produced by CI; cache-on-merge is free.
- **Cons:**
  - Adds a coverage-cache bead per rig, and a Refinery → Mayor
    invalidation channel. Not free.
  - Stale-cache risk: if Refinery's invalidation lags, a cycle
    might pick a target that's already been covered by a recent
    PR. Mitigation: stamp the cache with the merge SHA and reject
    cache hits where SHA != current `origin/main`.
- **Effort:** Medium for v2 — straightforward but cross-component.

### Recommendation

**Ship v1 as-spec'd. Defer all of Options 2-5 to v2.**

The v1 footprint (one rig, one PR per week, three files) is small
enough that none of the non-obvious scaling concerns surface. The
PRD's hard caps (≤200 LOC, ≤3 files, ≤1 PR/7d, N=10 reruns on direct
package only) are already the right knobs and explicitly bound every
expensive axis. Adding more knobs now would dilute the spec without
buying real headroom for v1.

**For the v1 implementation, however, two narrow guards must be in
the polecat formula:**

1. **Per-cycle wall-clock cap.** The polecat's work formula needs
   a `cycle_max_walltime` parameter (default 30 minutes). If the
   cycle exceeds this — typically because the mutant-sanity step
   blew up on a slow package — the polecat exits with a NOTES
   recording "wall-clock cap exceeded; target package too large"
   and no PR opens. This converts a slow-suite failure mode from
   "polecat session dies / wedges" to "rig auto-cools-down for the
   week, Overseer notified after 3 in a row."

2. **Mutant-sanity bounded to ≤5 mutants per test.** Even if a test
   covers 50 lines, mutate 5 random ones (Q2's "single mutant per
   test" reading). Document this as the v1 mutant budget. This
   keeps `O(test × mutant × test_runtime)` from exploding on
   long-tail packages.

These are tiny additions to the formula (two parameters), don't
violate Q4's "no custom commands" rule (they're hard-coded in the
formula, not user-configurable), and preempt the only realistic v1
performance failure.

For **v2** (multi-rig generalization), prioritize **Option 5
(coverage cache)** and **Option 3 (single-tmpdir mutant check)**.
Skip Option 4 entirely until 100+ rigs are opted in. Option 2 is
a v3 contingency.

## Constraints Identified

Hard constraints discovered during the analysis:

- **Per-cycle wall-clock has an unstated upper bound that PRD must
  name.** Without an explicit `cycle_max_walltime`, a slow rig's
  cycle can hold a polecat slot for >1 hour, which collides with the
  Witness's idle-detection assumptions for the polecat fleet
  (`gt mol status` polecats are expected to make progress within
  minutes, not hours). **Required:** PRD must specify a hard cap
  (recommend 30min) and the polecat formula must enforce it.

- **Synthetic-mutant check is O(file_size) in tmpdir-copy cost.**
  The PRD's Q2 says "tmpdir copy," not "in-place." For a
  multi-hundred-MB rig (rare today, common by 100x), this is the
  bottleneck. **Required:** the polecat formula must scope the
  tmpdir to the package directory, not the rig root.

- **N=10 flakiness re-run cost is bounded by the package's test
  runtime, not by anything we control.** Already mitigated by
  Q2's "direct package only" rule. **Required:** v1 implementation
  must verify this scoping is mechanical, not aspirational —
  `go test -count=10 -run="^TestX|^TestY$" ./pkg/...` not
  `go test -count=10 ./...`.

- **Coverage tool format is per-language and the parser is
  non-trivial.** PRD's Q4 deferred custom commands; v1 ships only
  for Go (`go tool cover` / `golang.org/x/tools/cover`). **Required:**
  the polecat formula's coverage-parser code is Go-specific and the
  v2 work item to add TS/Python parsers must be filed before pilot
  graduates. Otherwise the second-rig opt-in will surface a
  parser-shape question that should have been answered up-front.

- **Refinery's bisecting MQ degrades on shared-file MR collisions.**
  Auto-test PRs touch test files; concurrent feature MRs may also
  touch test files (when devs add tests for new code). v1 collision
  rate is negligible (1 PR/week). At 100 rigs × 1 PR/day = 100/day,
  collision rate becomes non-trivial on rigs with deep concurrent
  PR streams. **Required:** v2 must add an MQ-collision metric per
  rig; if it exceeds 5% of MR batches, increase the cooldown.

- **State bead CAS is single-rig serialized; no cross-rig coupling.**
  Confirmed by the PRD's Q7 design: each rig has its own state bead.
  No global lock. This is the right shape and scales linearly.

## Open Questions

These need human input or cross-dimension discussion:

1. **What's the v1 `cycle_max_walltime` budget?** Recommendation:
   30 minutes. Needs Overseer concurrence — too tight and slow
   packages will never get auto-tested; too loose and polecat-pool
   exhaustion becomes a real risk. **Cross-ref:** Integration
   dimension may want to coordinate with the Witness's
   polecat-idle-detection thresholds.

2. **Should the mutant-sanity check be per-test or per-line?** PRD
   Q2 says "comment out one line," singular. But a test exercises
   many lines. Recommendation: pick one *covered* line per test
   (the line with highest mutation discriminating power, or just
   random). Needs ratification. **Cross-ref:** UX dimension may
   want this called out in the diff-marker comment so reviewers
   can see *which* line was mutated.

3. **What's the cache-invalidation contract between Refinery and
   Mayor for v2 coverage cache?** Out of scope for v1 but the v1
   design should not preclude it. Recommendation: Refinery emits
   a `coverage-invalidate <rig>` nudge to Mayor on every successful
   merge to `main`. Cheap, no Dolt commit. **Cross-ref:**
   Integration dimension owns the Mayor↔Refinery channel design.

4. **At 100x rigs, do we need a global polecat-pool cap for
   auto-test cycles specifically?** Or does the existing
   per-rig-cooldown + the polecat fleet's natural FIFO behavior
   suffice? Feasibility leg flagged this. Recommendation: defer;
   a global cap is two lines of code in the Mayor patrol when we
   need it. **Cross-ref:** integration-dimension and the existing
   polecat-fleet sizing logic.

5. **Should "no PR opened" cycles count toward the per-rig
   cadence?** If a cycle hits a wall-clock cap or fails all gates,
   does it consume the week's budget or retry tomorrow? PRD is
   silent. Recommendation: failed cycles do *not* consume the
   budget; they trigger an immediate cooldown of 24h
   (cycle-failure backoff) but the next cycle attempt is at the
   next scheduled tick. **Cross-ref:** UX dimension on what the
   `gt auto-test-pr status` output reports.

6. **AST-aware mutant generation: which Go AST library?**
   Recommendation: `go/ast` + `go/parser` + `go/format` from the
   stdlib; or pull in `golang.org/x/tools/go/analysis` if we want
   loaded-package analysis. Stdlib is sufficient for "comment out
   one line, re-emit." **Cross-ref:** Data Model and Integration
   dimensions on whether we add a `golang.org/x/tools` dep to the
   gt module.

## Integration Points

How this dimension connects to the others:

- **Security:** the synthetic-mutant tmpdir is the per-cycle
  high-blast-radius surface. Scalability optimizations (Option 3 —
  single tmpdir, revert between mutants) increase the chance of
  an in-place mutant leaking into the worktree. Security and
  Scale should jointly require: tmpdir is `os.MkdirTemp` outside
  the worktree; on any error, polecat exits before any push;
  gitleaks pre-push scan (already a Q6 v1 MUST) is the backstop.

- **Integration:** the Mayor patrol's tick cadence (recommend
  hourly check, fire only if rig's cooldown elapsed) is shared
  with how other Mayor patrols cohabit the same scheduler. Scale
  doesn't constrain this much at v1; integration dimension owns
  the actual scheduler design.

- **Data Model:** the state bead's notes field is the audit trail
  per Q7. If we cache coverage profiles in v2 (Option 5), that's
  a new bead type (or a new field on the state bead). Coverage
  profiles for a real rig can be 100KB-1MB; storing many of them
  in Dolt is fine but warrants a TTL or "keep latest only" rule.
  Coordinate with Data Model dimension on field shape.

- **API & Interface:** the `gt auto-test-pr status` command (Q6
  v1 MUST) needs to surface per-rig cycle wall-clock so Overseer
  can see slow rigs early. Recommend: `status` displays last 5
  cycles' duration per rig, plus a flag if any exceeded
  `cycle_max_walltime` (which would have produced a no-PR-opened
  cycle).

- **User Experience:** the PR body banner should report which
  scaling-related limits the cycle hit (if any) — e.g.,
  "Synthetic mutant check ran on `pkg/foo`; 3 of 5 mutants
  produced compile errors and were skipped." This is candor for
  the maintainer, not a UX requirement, but UX dimension should
  decide format.

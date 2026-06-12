# Testability and Verifiability

> Dimension review of `.designs/curio-p3-retrospect-agent/{design-doc.md,child-beads.md}`
> Leg: testability — "Can we verify the plan worked?"

## Verdict

FAIL — the design's central regression gate ("replay grades A") is invoked as a
mechanical pass/fail predicate in five places (invariant 4, Q4, Q6, B3, B7) but
**no computer-checkable "grade A" predicate exists** — `Grade()` returns a
`GradeReport` struct and the pass logic lives inline in `replay_test.go` with a
test-only bound, so the auto-merge gate and the human-path CR gate cannot be
implemented as specified; and the B0→B1 ledger seam — the entire precision
signal — is never exercised by any planned test (B1 uses *mock* outcome
history), so the first time real ledger data flows through the digest is in
production. Both are fixable by adding/relocating test scope, but as written the
plan's two most safety-critical paths are not verifiable by a computer.

## Must Fix (blocks implementation)

### 1. "Replay grades A" has no mechanical predicate — it's prose, not a gate

- **Issue.** The design treats "replay grades A" as a binary gate: invariant 4
  ("Replay-graded mutations … MUST pass the replay harness before the Refinery
  merges"), Q4 ("**Merge is blocked unless replay grades A**"), Q6 ("Merge is
  blocked on replay grade < A"), B3, and B7 conjunct 3 ("Replay CI grades A").
  But there is **no "Grade A" in the code.** `internal/curio/replay.go:108`
  `Grade(rules, fixtures)` returns a `GradeReport{AnchorsHit, MissingRules,
  NormalCandidates, WorstNormalWindow}` — a data struct, not a letter. The
  actual pass predicate (every anchor hit **AND** `NormalCandidates <=
  normalCandidateBound`) is hand-inlined in `replay_test.go:24-38`, and
  `normalCandidateBound = 20` is a **test-only const** (`replay_test.go:7`), not
  exported, not reachable from any gate script or merge policy.
- **Why it matters.** B7's auto-merge ("Replay CI grades A") and even the
  human-reviewed threshold-CR path (Q4/invariant 4) both need a gate that can
  *independently re-compute* "grade A" on the proposed overlay. Today the only
  thing that asserts the predicate is the `go test ./internal/curio/...` run
  against the **compiled defaults** — which, until B3, doesn't even see the
  config overlay (the sequencing review's point #2). So as specified, "merge
  blocked unless grade A" is unenforceable: there is no function a CI/merge gate
  can call that says yes/no for a given overlay. This directly fails the
  dimension question "which steps have acceptance criteria verifiable by a
  computer?" — the design's headline safety gate is not one of them.
- **Suggested resolution.** Make B3 extract the pass predicate into production
  (non-`_test.go`) code: e.g. `func (r GradeReport) IsA() bool` (or
  `GradeWithThresholds(overlay) (GradeReport, bool)`) with the bound promoted
  out of the test file, plus a thin CLI/exit-code wrapper a gate script can
  invoke. Then B7's gate and the human-path CR gate both call the *same*
  predicate. Add this to B3's acceptance ("a gate can mechanically determine
  grade-A for a supplied overlay, exit non-zero on < A") — without it, B3's
  "overlay grading" produces a `GradeReport` nobody can gate on.

### 2. The B0→B1 precision seam is never integration-tested — it first runs in prod

- **Issue.** B1's golden-file test explicitly uses **mock outcome history** (B1
  acceptance: "digest rendering from fixture candidates + **mock** outcome
  history is byte-stable"). B0 populates the real `curio_ledger`. **No planned
  test runs B0's writer → `ReadOutcomeHistory()` → digest end-to-end.** Verified:
  `curio_ledger` has only DDL today (`store.go`), Curio "NEVER files beads"
  (`curio_dog.go:94`), and there's no daemon post-close reconciler. So the seam
  B0 builds — the one that makes `ReadOutcomeHistory()` return anything — is
  exercised by B0's unit tests (close→outcome mapping) and B1's unit tests (mock
  history), but the **join** is not. The first time real ledger rows reach a
  real digest is B5's nightly dispatch, in production.
- **Why it matters.** The dimension asks "tests that can only run in production"
  and "how do we know the feature works end-to-end after all beads close." This
  is exactly that gap: B0 green + B1 green + B5 green can all hold while the
  integrated path emits a digest with a **blank precision table** (e.g. B0's
  reconciler writes a column B1's SELECT doesn't read, or the fingerprint/rule_id
  join key mismatches). The whole epic's value ("measure precision → justify a
  tune") rests on a seam no test crosses until prod.
- **Suggested resolution.** Add an integration test (owned by B1, depending on
  B0's writer being callable) that: seeds a candidate, drives B0's filing-row
  insert + post-close reconciler with a real close reason, then runs
  `--emit-digest` against that DB and asserts the precision table is **non-empty
  and numerically correct** for the seeded rule. Make "non-empty precision table
  from a B0-populated ledger" a B1 acceptance criterion, not just "stable digest
  from mock history."

## Should Fix (important but not blocking)

### 3. B0's reconciler has no integration test proving it actually *fires* on a close

- B0 says "extend the daemon's existing bead-close event stream (the refinery
  post-merge hook path)" but names no concrete hook. The daemon's close-adjacent
  paths are `convoy_manager.go` / `wisp_reaper.go`; there is no obviously named
  "on bead close" registry. B0's listed tests are all **direct-call units**
  ("closing a bead present in the ledger … sets `outcome='false_positive'`") —
  they test the reconciler *function*, not that it's *wired* to fire when a real
  bead closes. Classic unit-green / never-fires-in-prod risk. Add an
  acceptance: an integration test (or a wiring assertion) that a real bead-close
  event invokes the reconciler — name the integration point in B0's scope.

### 4. The per-run cap (N=3) and dedup are prompt-only — no mechanical backstop

- Q7 / B4 enforce "at most N=3 proposals per run" and "dedup against open
  proposals" via the **formula prompt** — i.e. the agent is *instructed* to obey.
  Neither is computer-verifiable: a non-deterministic agent can emit 5 CRs or
  re-propose a deduped cluster and no test catches it. The only mechanical
  backstops are B5's volume circuit breaker (caps *total* open proposals, not
  per-run) and B6's cluster-key dedup (only works if B6's `StateHash`→bead
  stamping lands — see B6). Recommend: state explicitly that the cap/dedup are
  *advisory at the agent layer* and that the *enforced* invariants are B5's
  breaker + B6's dedup-key linkage. Consider a B5/B6 post-dispatch assertion
  (e.g. reject a run that opened > N `curio-proposal` beads tagged with the same
  run id) so "≤ N per run" has a mechanical check, not just prose.

### 5. B4 formula acceptance requires a live LLM dispatch — make the deterministic part testable

- B4's acceptance is "`gt sling mol-curio-retrospect …` runs a polecat that opens
  the correct artifact kinds and respects the cap + dedup." Verifying that
  requires a **real polecat session** (LLM, non-deterministic, costs a dispatch)
  — a manual/prod-only check, not CI. The repo *does* have deterministic formula
  tests (`internal/cmd/sling_formula_required_vars_test.go` validates parse +
  required vars + var patterns). B4 should claim those: a formula-validation test
  (parses, `digest_path`/`max_proposals` required-var contract, step structure)
  as the **computer-verifiable** acceptance, and demote "a live polecat opens the
  right artifacts" to a documented manual smoke test. As written, B4's only
  acceptance is the unautomatable one.

### 6. B5's promised "path readable from the polecat's context" test exceeds the existing harness

- B5 commits to "a B5 test must assert the emitted path is readable from the
  slung polecat's working context." Good — that's the right computer-verifiable
  acceptance for the sandbox contract (completeness #6). But the existing
  dispatch-plugin tests (`plugins/wiki-patrol-dispatch/run_test.sh`,
  `casc-patrol-dispatch/run_test.sh`) are **grep-based static assertions** on the
  `gt sling` invocation shape — they deliberately *don't* dispatch or read a file
  from a real worktree ("We don't actually dispatch a workflow"). So B5's
  worktree-readability assertion needs a **new** test capability (stage a file,
  resolve the polecat's working dir, assert readability) beyond the run_test.sh
  pattern it's modeled on. Flag that B5's test is not a copy of the existing
  grep harness; budget for the new fixture, or the "readable from sandbox"
  acceptance silently degrades to another grep that doesn't prove readability.

### 7. No signal for the silent-degradation case (lane runs, precision table empty)

- "What's the first signal something went wrong in prod?" is answered for
  *dispatch failure* (`notify_on_failure` + receipt absence, design §Residual
  risks) and, via B8, for a *persistently-tripped breaker*. It is **not**
  answered for the most dangerous quiet failure: the lane dispatches
  successfully every night, but the ledger is empty / the B0 reconciler silently
  stopped firing → the digest's precision table is blank → the agent proposes
  nothing or junk → every receipt reads `result:success`. There is no alert on
  "digest emitted with zero rules carrying precision." Recommend: have
  `--emit-digest` record a count (e.g. `rules_with_precision`) in the
  plugin-run receipt and alert (or digest-line) if it's zero for K consecutive
  nights. Otherwise the L1-seam regression — the single most consequential
  failure mode — is invisible.

## Observations

(Non-blocking)

- **Strong, well-scoped deterministic tests where they exist.** B1 golden-file +
  `TestImportGraph_NoWritePath` invariant, B2 single-sourcing assertion (assert
  the air-gap predicate is the *same code path* as `suppressed()` /
  `CurioSeriesPrefix`, not a re-implementation), B3's two-direction overlay
  grading (loosen-below-anchor → fail; raise-noisy-ceiling → pass), B6's
  cluster-key round-trip and proposal-target-guard rejection test. These are
  genuinely computer-verifiable and the right shape. The testability problem is
  concentrated in the **seams** (#1, #2, #3, #6), not the per-unit coverage.
- **B2's single-sourcing test is the model to copy.** "assert no duplicate
  definition" of the air-gap predicate is exactly the right verifiable
  acceptance — it catches the divergence class that bit P2's
  unit-test-vs-reality precision gap. Apply the same "single-sourced, asserted"
  discipline to #1's grade-A predicate so the gate and the test can't drift.
- **Regression coverage for existing behavior is real.** `TestImportGraph_NoWritePath`,
  `TestClosedWindowCursor`, `TestKillSwitchIsolation`,
  `TestReplay_LoopBreakerWindow` (loop-breaker window must produce 0 candidates)
  are all preserved and explicitly carried as B1/B2 invariants. The "don't
  regress the air-gap / write-incapability" surface is well-guarded.
- **The non-determinism is honestly scoped and correctly mitigated.** The design
  is right that the agent step need not be deterministic *if* the gates are.
  The blockers above (#1, #2) are precisely about making those gates actually
  mechanical — once fixed, the "agent proposes, deterministic gate disposes"
  posture is verifiable.
- **B7's "re-verify, never trust the label" requirement is the correct testable
  stance.** B7's insistence that the gate independently re-checks diff scope +
  replay-A (not the polecat's self-asserted `curio-auto-eligible` label) is the
  right design for a computer-checkable auto-merge — it just depends entirely on
  #1's grade-A predicate existing.

## Sources

- `.designs/curio-p3-retrospect-agent/design-doc.md` — P3 design under review — accessed 2026-06-12
- `.designs/curio-p3-retrospect-agent/child-beads.md` — B0–B8 breakdown — accessed 2026-06-12
- `internal/curio/replay.go:93-140` — `Grade` returns `GradeReport` struct; no scalar/letter grade — accessed 2026-06-12
- `internal/curio/replay_test.go:5-40` — pass predicate inlined in test; `normalCandidateBound=20` is a test-only const — accessed 2026-06-12
- `internal/curio/store.go` — `curio_ledger` DDL only; no INSERT/UPDATE path — accessed 2026-06-12
- `internal/daemon/curio_dog.go:94,132` — Curio "NEVER files beads" / candidates-only (B0 precondition) — accessed 2026-06-12
- `plugins/wiki-patrol-dispatch/run_test.sh`, `plugins/casc-patrol-dispatch/run_test.sh` — grep-based dispatch-plugin smoke tests (no real dispatch / file read) — accessed 2026-06-12
- `internal/cmd/sling_formula_required_vars_test.go:28-60` — deterministic formula-validation test precedent for B4 — accessed 2026-06-12
- `internal/daemon/{convoy_manager.go,wisp_reaper.go}` — close-adjacent daemon paths; no named "on bead close" hook for B0 to extend — accessed 2026-06-12

# Curio Phase 3 — Implementation Epic: Child-Bead Breakdown

> Companion to [`design-doc.md`](./design-doc.md) (gu-sdjm5). This is the
> proposed breakdown for the NEXT epic — implementing the Retrospect lane as a
> Claude-agent polecat. These beads are ready to create and sling once the
> design is accepted. Suggested order respects dependencies (substrate first,
> dispatch + policy last).
>
> Parent epic to create: **"Curio P3 — Retrospect lane as a Claude-agent
> polecat"** (child of gu-fcwx8). Each bead below becomes a child of that epic.

## Dependency graph

```
B1 (digest + ReadOutcomeHistory) ─┬─> B2 (air-gap filter)
                                  └─> B4 (formula) ─> B5 (dispatch plugin)
B3 (replay config overlay) ───────────────────────> B7 (auto-merge policy)
B6 (proposal taxonomy/landing) ───> B4, B7
```

B1 and B3 are independent and can start in parallel. B4 depends on B1+B2+B6.
B5 depends on B4. B7 depends on B3+B6 and ships LAST (default OFF).

---

## B1 — Substrate: `ReadOutcomeHistory` + `--emit-digest`

**Type:** task · **Labels:** curio, retrospect, substrate

**Scope.** Add to `cmd/curio-proposer` a new `--emit-digest <path>` mode and to
`internal/curio.Reader` a new read-only method `ReadOutcomeHistory()`.

- `ReadOutcomeHistory()` — pure SELECT over `curio_ledger` returning per-rule
  `{total_resolved, false_positives, precision, recent_fp_summaries}`. No write
  deps. (This is the one piece of P2 build-4 scope we keep.)
- `--emit-digest` — load kill switch (existing); if `llm.enabled=false`, exit
  without emitting; else read `ReadCandidatesBefore(closedWindowCursor(now))` +
  `ReadOutcomeHistory()` and render the deterministic Markdown+JSON digest
  (shape in design-doc Q2) to the path.

**Invariants / tests.**
- `TestImportGraph_NoWritePath` still passes (digest renderer adds no write
  deps — it only formats `Reader` output).
- Unit: digest rendering from fixture candidates + mock outcome history is
  byte-stable (golden-file test).
- `TestClosedWindowCursor` / `TestKillSwitchIsolation` unaffected.

**Does NOT:** call an LLM, file anything, query Dolt outside `Reader`.

**Acceptance:** `curio-proposer --emit-digest /tmp/d.md` against a seeded test
DB produces a stable digest; with `llm.enabled=false` it emits nothing and
exits 0.

---

## B2 — Substrate: air-gap filter in the digest

**Type:** task · **Labels:** curio, retrospect, substrate, safety
**Depends on:** B1

**Scope.** Before rendering, the digest excludes self-referential candidates
(design-doc Q5 layer 1), reusing the live-side predicates so the air-gap is
single-sourced:

- drop `rule_id` prefixed `proposed_`,
- drop series with `CurioSeriesPrefix`,
- drop candidates whose causal root ∈ the Curio-filed set.

**Invariants / tests.**
- Unit: a fixture digest containing Curio-attributable + `proposed_` candidates
  renders with them excluded; a parallel non-Curio candidate survives.
- The exclusion predicate is the SAME code path as `Input.suppressed()` /
  `rateSpikeRule`'s `CurioSeriesPrefix` check — assert no duplicate definition.

**Acceptance:** no self-referential candidate ever appears in an emitted digest;
test proves the filter and its single-sourcing.

---

## B3 — Replay harness reads config overlay

**Type:** task · **Labels:** curio, retrospect, replay
**Depends on:** (none — independent)

**Scope.** Extend the replay harness so it can grade a rule set with a
`rate_thresholds` overlay applied (design-doc Q6). Because thresholds are
config-driven since gc-e2uvyr.3, a config-only threshold CR must be gradeable;
today replay grades only compiled defaults and would miss a config regression.

- Add a replay mode/helper that loads `rate_thresholds` from a supplied
  `daemon.json` (or an overlay map) and grades
  `DefaultRulesWithThresholds(overlay)` against the fixtures.
- Wire it so a CR touching `daemon.json` `rate_thresholds` is gradeable in CI.

**Invariants / tests.**
- Unit: an overlay that loosens a threshold below an anchor's expectation makes
  the relevant anchor FAIL to fire → grade < A (regression caught).
- Unit: an overlay that only raises a noisy series' ceiling keeps all anchors
  firing and normal-window volume ≤ prior → grade A.

**Acceptance:** `Grade` (or a thin wrapper) accepts an overlay and the two test
cases above pass.

---

## B4 — Retrospect formula

**Type:** task · **Labels:** curio, retrospect, formula
**Depends on:** B1, B2, B6

**Scope.** Author the Retrospect polecat formula (TOML under
`internal/formula/formulas/`, e.g. `mol-curio-retrospect.formula.toml`). Steps:

1. Read the digest at `{{digest_path}}`.
2. Dedup against open `curio-proposal` / `curio-hypothesis` beads
   (`bd list --label curio-proposal --status open`).
3. Reason about unresolved clusters + per-rule precision; produce at most
   `{{max_proposals}}` (default 3) proposals, ranked by precision impact.
4. Land each per the taxonomy (B6 / design-doc Q3): threshold tune → config CR;
   new-rule sketch → proposal bead; root-cause → hypothesis bead/comment.
5. Air-gap prompt scoping (design-doc Q5 layer 3): never reason about/propose
   rules detecting Curio's own activity.

**Invariants / tests.**
- Formula validates (`gt formula` validation / the formula-author skill).
- Dry-run / fixture: given a sample digest, the formula's instructions are
  unambiguous about which artifact each proposal kind produces.

**Acceptance:** `gt sling mol-curio-retrospect <rig> --var digest_path=... ` runs
a polecat that opens the correct artifact kinds and respects the cap + dedup.

---

## B5 — Dispatch plugin `curio-retrospect-dispatch`

**Type:** task · **Labels:** curio, retrospect, scheduler
**Depends on:** B4

**Scope.** Town-level plugin (`plugins/curio-retrospect-dispatch/{plugin.md,run.sh}`)
mirroring `casc-patrol-dispatch`. Cron gate `0 8 * * *`. `run.sh`:

1. Kill-switch pre-check: read `mayor/daemon.json` `patrols.curio.llm.enabled`;
   if false/absent → `result:skipped`, exit 0.
2. Single-instance guard: skip if a prior Retrospect run is still in flight.
3. Volume circuit breaker (design-doc Q7): skip if open `curio-proposal` beads
   exceed the ceiling (e.g. 10).
4. `curio-proposer --emit-digest <path>` then `gt sling mol-curio-retrospect
   <rig> --var digest_path=<path>`.
5. Record a `type:plugin-run` receipt.

**Invariants / tests.**
- `run_test.sh` asserts the positional sling shape (`gt sling <formula> <rig>`)
  and the three skip paths (kill-switch off, in-flight, volume breaker).
- Lane-off is graceful: skip → exit 0, no escalation.

**Acceptance:** with `llm.enabled=true` and a quiet backlog the plugin renders a
digest and slings one polecat nightly; with the switch off it skips cleanly.

---

## B6 — Proposal taxonomy landing + dedup

**Type:** task · **Labels:** curio, retrospect, proposals
**Depends on:** B1 (digest) — consumed by B4, B7

**Scope.** Define the concrete landing artifacts and dedup (design-doc Q3, Q7):

- Bead labels: `curio-proposal` (new-rule sketch, assigned mayor),
  `curio-hypothesis` (root-cause, informational).
- Threshold tune → config CR against `daemon.json` `rate_thresholds`.
- Dedup query contract (open proposals by label) the formula calls.
- Proposal-target guard (design-doc Q5 layer 2): a proposed rule/threshold may
  not target a `curio.*` series; document the CI check that enforces it.

**Invariants / tests.**
- Doc/test fixtures for each artifact kind's shape.
- Dedup: a cluster already covered by an open proposal bead is skipped.

**Acceptance:** each proposal kind has a single, documented landing path; dedup
prevents nightly re-proposal of the same cluster.

---

## B7 — Precision-gate auto-merge policy (default OFF)

**Type:** task · **Labels:** curio, retrospect, refinery, safety
**Depends on:** B3, B6 · **Ships LAST, disabled.**

**Scope.** A Refinery merge policy that lets a threshold-tune CR auto-merge
(no human approval) iff the P2 conjunction holds (design-doc Q4):

1. CR touches ONLY `daemon.json` `rate_thresholds` keys.
2. CR body asserts measured precision < 0.80 for the tuned series.
3. Replay CI grades A (uses B3's overlay grading).

**Default OFF.** Ship the policy disabled; every proposal requires human
approval initially. Enable only after observing several cycles of
human-reviewed tunes the human would have approved anyway (mirrors the
Phase-2 shadow→live and Phase-1 candidates-only disciplines).

**Invariants / tests.**
- Policy unit tests: each conjunct failing → human approval still required.
- A source change or fixture deletion in the CR → never auto-eligible.

**Acceptance:** with the policy ON (test env), a qualifying threshold CR
auto-merges after replay-A; any disqualifier routes to human review. With the
policy OFF (default), all CRs require human approval.

---

## Notes for epic creation

- Create the parent epic as a child of **gu-fcwx8** (Curio).
- B1+B3 are the parallel starting points; sling them first.
- B7 is explicitly a "ship disabled, enable later" bead — keep it last so the
  conservative default lands the lane safely before any auto-merge is possible.
- None of these beads land an in-process LLM client, a patch generator, or a
  `gt curio apply` write path — those P2 build-4/5/6 scopes are intentionally
  dropped (see design-doc Go/No-Go).

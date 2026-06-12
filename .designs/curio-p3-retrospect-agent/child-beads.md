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
B0 (L1 ledger population) ─> B1 (digest + ReadOutcomeHistory) ─┬─> B2 (air-gap filter)
                                                              └─> B4 (formula) ─> B5 (dispatch plugin)
B3 (replay config overlay) ─────────────────────────────────> B5 (gates first live threshold CR), B7
B6 (proposal taxonomy/landing + dedup key + CI guard) ─────────> B4, B5 (CI guard), B7
```

B0 builds the Layer-1 prerequisite the whole lane reasons over; B1 depends on it
(an empty ledger makes the digest's precision table — the tune justification —
blank). B0, B3, and B6 are the independent starting points and can run in parallel
(B6 is definitional — labels, dedup-key convention, CI-guard spec — and only needs
to *exist as a consumer contract* for B4, not B1's rendered artifact). B4 depends
on B1+B2+B6. **B5 depends on B4 + B3 + B6**: B3 because the first threshold CR
lands at B5 and without the replay overlay a config-only threshold regression sails
through `go test` trivially green (losing the mechanical gate even on the
human-reviewed path, not just B7's auto-merge); B6 because the proposal-target CI
guard must be *implemented and wired* before a real polecat dispatches. B7 depends
on B3+B6 and ships LAST (default OFF). B8 (anti-wedge) follows B5+B6.

---

## B0 — Layer-1 prerequisite: populate `curio_ledger`

**Type:** task · **Labels:** curio, retrospect, substrate, ledger
**Depends on:** (none — the foundational starting point)

**Why this exists.** The whole lane reasons over per-rule precision, which comes
from `curio_ledger`. The table is DDL-provisioned (`internal/curio/store.go`
`ledgerDDL`) but **nothing writes to it**: Curio is candidates-only and files no
beads (`internal/daemon/curio_dog.go` — "NEVER files beads"), and there is no
daemon post-close reconciler. P2 scoped population into builds 4-6, which P3
drops — so without this bead the ledger is permanently empty, `ReadOutcomeHistory()`
returns nothing, and the digest's precision table (the entire justification for a
threshold tune, design-doc Q4) is blank. B1's value is zero until B0 lands.

**Scope.** Two write paths, following the P2 population flow
(`.designs/curio-p2-retrospective/design-doc.md` §"Population flow"):

1. **Filing-time row insert.** When Curio files a bead for a candidate, write the
   ledger row `(bead_id, fingerprint, rule_id, filed_at, outcome='')`. NOTE: Curio
   filing beads at all is a precondition — if bead-filing is still gated off in
   the target epic, B0 must either (a) enable candidate→bead filing for the rules
   the lane tunes, or (b) seed the ledger from the `curio_candidate` table at
   file-time-equivalent. Pick and state which; do not assume filing exists.
2. **Post-close reconciler.** Extend the daemon's existing bead-close event
   stream (the refinery post-merge hook path) with a curio-ledger reconciler: on
   any bead close, if `bead_id ∈ curio_ledger`, set `outcome` + `resolved_at`
   from the close reason (`fixed` / `false_positive` / `duplicate` / `deferred`
   per P2's mapping).

**Trust-boundary note.** This write path lives in `internal/daemon` (or
`internal/beads`), NOT in `cmd/curio-proposer`. `curio-proposer` stays
write-incapable (`TestImportGraph_NoWritePath`). B0 does not touch the proposer
binary at all — it is daemon-side population, kept on the opposite side of the
read/write air-gap from B1.

**Invariants / tests.**
- Unit: closing a bead present in the ledger with a "false"-tagged reason sets
  `outcome='false_positive'`; a merge-commit close sets `outcome='fixed'`.
- Unit: closing a bead NOT in the ledger is a no-op (no spurious rows).
- `precision(rule)` per P2's formula is computable end-to-end on a seeded fixture.
- `curio-proposer`'s import graph is unaffected (no new deps in the proposer).

**Acceptance:** after a candidate is filed and its bead later closed, the ledger
row carries the correct outcome + `resolved_at`, and `ReadOutcomeHistory()` (B1)
returns a non-empty, correct precision for that rule.

**Alternatively** (if L1 is genuinely meant to be a separate epic landing first):
do NOT include B0 here — instead mark this entire epic **blocked** on the L1
tracking bead, cite it explicitly, and delete the "UNCHANGED / prerequisite" claim
that currently implies L1 already works. The one thing the plan must not do is
ship B1-B7 against a permanently-empty ledger.

---

## B1 — Substrate: `ReadOutcomeHistory` + `--emit-digest`

**Type:** task · **Labels:** curio, retrospect, substrate
**Depends on:** B0 (ledger must be populated for `ReadOutcomeHistory` to return data)

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
**Depends on:** (none — an independent starting point)
**Blocks:** B5 (the first live threshold CR) and B7 (auto-merge).

**Scope.** Extend the replay harness so it can grade a rule set with a
`rate_thresholds` overlay applied (design-doc Q6). Because thresholds are
config-driven since gc-e2uvyr.3, a config-only threshold CR must be gradeable;
today replay grades only compiled defaults and would miss a config regression.

**Why this gates B5, not just B7.** The first threshold-tune config CR lands when
B5 begins dispatching nightly. Without this overlay, a `daemon.json`
`rate_thresholds` CR passes `go test ./internal/curio/...` trivially green (the
harness ignores the overlay), so even the *human-reviewed* default path loses its
mechanical regression gate — invariant 4 ("replay-graded mutations") becomes
prose. B3 is a prerequisite of the threshold-tune landing path itself, present
from B5 onward, not merely a precondition of B7's auto-merge.

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
**Depends on:** B4, **B3** (replay overlay gates the first live threshold CR),
**B6** (the proposal-target CI guard must be implemented + wired before a real
polecat dispatches — air-gap layer 2 is otherwise unenforced at go-live)

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

**Digest-path sandbox contract (must specify).** Polecats run in isolated
worktrees. `<path>` MUST be a host-shared absolute path the agent can read from
inside its sandbox (e.g. under the town root, not the plugin's CWD), OR the
digest must be staged into the worktree before the formula's step-1 read. State
which, and prove it: a B5 test must assert the emitted path is readable from the
slung polecat's working context — otherwise step-1 ("read the digest at
`{{digest_path}}`") fails silently and the lane no-ops every night.

**Invariants / tests.**
- `run_test.sh` asserts the positional sling shape (`gt sling <formula> <rig>`)
  and the three skip paths (kill-switch off, in-flight, volume breaker).
- The digest path passed to `gt sling --var digest_path=` is the same host-shared
  path the formula reads (path-contract assertion).
- Lane-off is graceful: skip → exit 0, no escalation.

**Acceptance:** with `llm.enabled=true` and a quiet backlog the plugin renders a
digest and slings one polecat nightly; with the switch off it skips cleanly.

---

## B6 — Proposal taxonomy landing + dedup

**Type:** task · **Labels:** curio, retrospect, proposals
**Depends on:** (none — definitional; an independent starting point). B6's
deliverables are the label scheme, the cluster-key dedup convention, and the
proposal-target CI guard — a *consumer contract*, not code that parses B1's
digest. It can run in parallel with B0/B1/B3. **Consumed by** B4, B5 (CI guard),
B7.

**Scope.** Define the concrete landing artifacts and dedup (design-doc Q3, Q7):

- Bead labels: `curio-proposal` (new-rule sketch, assigned mayor),
  `curio-hypothesis` (root-cause, informational).
- Threshold tune → config CR against `daemon.json` `rate_thresholds`.
- **Cluster-key dedup identity (end-to-end).** The candidate's `StateHash`
  (`internal/curio/candidate.go`) is the dedup identity. This bead must specify
  HOW that key is stamped on the proposal bead (a `cluster:<StateHash>` label or
  a body field) AND how the formula queries it back
  (`bd list --label curio-proposal --status open` → match each digest cluster's
  `StateHash` to an existing bead). Without a stable cluster-key→bead linkage,
  the B4 dedup step cannot actually deduplicate and the lane re-proposes the same
  cluster nightly until the first proposal merges.
- **Proposal-target guard CI check — BUILD it, don't just document it**
  (design-doc Q5 layer 2). A CR proposing a rule/threshold that targets a
  `curio.*` series (or references Curio's own beads) must FAIL CI. This is
  air-gap layer 2 and must be enforcement, not prose. Either fold building the
  CI check (a gate script that inspects the CR diff + body) into this bead's
  scope, or split it into its own bead — but it must be implemented, with a test
  proving a `curio.*`-targeting CR is rejected.

**Invariants / tests.**
- Doc/test fixtures for each artifact kind's shape.
- Cluster-key round-trip: a proposal bead stamped with `StateHash` is found by
  the dedup query for the same digest cluster.
- Dedup: a cluster already covered by an open proposal bead is skipped.
- Proposal-target guard: a fixture CR proposing a `curio.*`-targeting rule fails
  the CI check; a non-Curio-targeting CR passes.

**Acceptance:** each proposal kind has a single, documented landing path; the
cluster key is stamped and queryable so dedup prevents nightly re-proposal of the
same cluster; a Curio-self-targeting proposal CR is rejected by an implemented CI
check.

---

## B7 — Precision-gate auto-merge policy (default OFF)

**Type:** task · **Labels:** curio, retrospect, refinery, safety
**Depends on:** B3, B6 · **Ships LAST, disabled.**

**Scope.** A Refinery merge policy that lets a threshold-tune CR auto-merge
(no human approval) iff the P2 conjunction holds (design-doc Q4):

1. CR touches ONLY `daemon.json` `rate_thresholds` keys.
2. CR body asserts measured precision < 0.80 for the tuned series.
3. Replay CI grades A (uses B3's overlay grading).

**Implementation mechanism (must pick one).** The Refinery has no path-scoped,
body-asserting conditional auto-merge engine today — its gates are per-rig shell
scripts (`internal/refinery/batch.go` `runBatchGates`, configured via
`gt rig settings … merge_queue.gates`). This bead must specify how the policy is
realized, e.g.:
- **(a) A merge-queue gate script** that inspects the CR diff (paths touched) and
  CR body (precision assertion), and only signals auto-merge-eligible when all
  three conjuncts hold; or
- **(b) A polecat-set label** (`curio-auto-eligible`) the polecat applies only
  when it believes the conjunction holds, which a gate then *independently
  re-verifies* (never trust the label alone — the gate must re-check the diff
  scope + replay grade).

Whichever is chosen, the diff-scope and replay-A checks MUST be re-verified by
the gate, not asserted by the proposer. As written in the design this was
under-scoped; pick the mechanism here.

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

## B8 — Proposal expiry + breaker-reset (anti-wedge)

**Type:** task · **Labels:** curio, retrospect, proposals, safety
**Depends on:** B5 (the volume breaker), B6 (proposal beads)

**Why this exists.** Q7's volume circuit breaker skips dispatch when open
`curio-proposal` beads exceed a ceiling (~10). Nothing ages out stale proposals
(P2 open-question #2, "proposal expiry," is unaddressed). Failure mode: proposals
accumulate, the breaker trips, the lane stops dispatching — and **stays stopped**
until a human manually works the backlog down. The lane can silently self-wedge.

**Scope.**
- **Expiry / auto-close.** A `curio-proposal` / `curio-hypothesis` bead untouched
  for N days (configurable, e.g. 14) is auto-closed, feeding `false_positive` or
  `deferred` back to the ledger (closing the L1 loop B0 opened). This both bounds
  the backlog and improves precision data.
- **Breaker-tripped alert.** When the volume breaker has been open for M
  consecutive days (e.g. 3), emit an observability signal (digest line or a
  low-severity escalation) so a wedged lane is visible, not silent.

**Invariants / tests.**
- Unit: a proposal bead past the expiry window is auto-closed with the configured
  outcome and the ledger row updated.
- Unit: a fresh/active proposal is NOT closed.
- Unit: breaker open ≥ M days emits the alert exactly once per trip (no spam).

**Acceptance:** stale proposals age out and stop wedging the breaker; a
persistently-tripped breaker is surfaced rather than silently halting the lane.

---

## Notes for epic creation

- Create the parent epic as a child of **gu-fcwx8** (Curio).
- **B0 is the true prerequisite — sling it FIRST.** The lane reasons over
  `curio_ledger` precision, which nothing populates today; B1-B7 are worthless
  against an empty ledger. **B0, B3, and B6 are the three parallel starting
  points** (B6 is definitional; B3 is independent).
- B4 depends on B1+B2+B6; **B5 depends on B4 + B3 + B6** (B3 gates the first live
  threshold CR; B6's CI guard must be wired before real dispatch). B7 and B8 ship
  after the lane is live.
- **B7 touches Refinery-internal policy code** (`internal/refinery`), a
  cross-cutting surface beyond the `internal/curio` package the other beads live
  in — its review scope and blast radius are wider; budget for it.
- B7 is explicitly a "ship disabled, enable later" bead — keep it before B8 in
  enablement order but both land after the core lane; the conservative default
  lands the lane safely before any auto-merge is possible.
- B8 prevents the lane from silently self-wedging via the volume breaker; it can
  land any time after B5+B6 but should not be deferred indefinitely.
- None of these beads land an in-process LLM client, a patch generator, or a
  `gt curio apply` write path — those P2 build-4/5/6 scopes are intentionally
  dropped (see design-doc Go/No-Go).

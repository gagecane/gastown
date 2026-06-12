# Sequencing and Dependencies

> Dimension review of `.designs/curio-p3-retrospect-agent/{design-doc.md,child-beads.md}`
> Leg: sequencing — "Is the order right? Are dependencies correct?"

## Verdict

PASS WITH NOTES — the macro-order is sound (substrate → formula → dispatch → policy, with the risky auto-merge last and default-OFF), but the breakdown omits one hard upstream prerequisite (L1 ledger population) and mis-scopes one internal dependency (B3 gates threshold CRs generally, not just B7's auto-merge), either of which produces a "ships green but operates blind" failure mid-implementation.

## Must Fix (blocks implementation)

### 1. L1 (`curio_ledger` population) is an undeclared blocking prerequisite of B1

- **Issue.** B1's `ReadOutcomeHistory()` is a SELECT over `curio_ledger`, and the digest's primary agent signal is the "Per-rule precision (from curio_ledger)" table (design-doc Q2). But the ledger is **empty and nothing writes to it**. Verified in code: `internal/curio/store.go:50` ledger DDL is commented *"Empty until Phase 2"*, and a repo-wide grep finds **zero** `INSERT INTO curio_ledger` / outcome-write path. The P2 doc itself states (`.designs/curio-p2-retrospective/design-doc.md:70-71`) the ledger "was provisioned … but nothing populates it yet," and the post-close write hook (P2 lines 117-122) is unbuilt.
- **Why it matters.** The design says L1 is "a prerequisite, not part of this epic" (design-doc table, L1 row) — but the child-bead breakdown encodes **no dependency** on it. B1 is listed as a parallel *starting point* with no upstream. If this epic is slung as written, B1 ships a renderer whose precision table is empty/degenerate, the agent reasons over noise, and every downstream bead (B4 formula ranks "by precision impact"; B7 auto-merge asserts "precision < 0.80") rests on data that does not exist. This is the canonical "we can't proceed meaningfully" moment — and it surfaces only at B4 runtime, not at B1 build time, because B1's golden-file test uses *mock* outcome history (B1 acceptance: "mock outcome history").
- **Suggested resolution.** Add an explicit blocking dependency from B1 onto the P2 L1 bead that builds the post-close ledger-population hook (the P2 doc references `gu-fcwx8.x`; identify and link it, or carry an L1 bead into this epic as B0). Make `B0 (ledger populate) → B1` the true root of the dependency graph. Until L1 lands, B1 cannot be acceptance-tested against real data.

## Should Fix (important but not blocking)

### 2. B3 (replay config overlay) is mis-scoped: it gates threshold CRs generally, not only B7's auto-merge

- **Issue.** The dependency graph routes B3 **only** to B7 (`B3 ───> B7`). But design-doc Q6 is explicit: a threshold tune is now a *config* CR (`daemon.json rate_thresholds`, since gc-e2uvyr.3 — verified `DefaultRulesWithThresholds` exists at `internal/curio/rules.go:236`), and *"today replay grades only compiled defaults and would miss a config-only regression."* The replay harness (`internal/curio/replay_test.go`, runs under the `go test ./...` gate listed on this very bead) does not yet read a config overlay.
- **Why it matters.** B4's formula produces threshold-tune config CRs, and B5 dispatches them nightly. Invariant 4 ("replay-graded mutations") and Q4's "Merge is blocked unless replay grades A" both assume the replay CI actually exercises the proposed overlay. If B4/B5 ship before B3, a `daemon.json` threshold CR sails through `go test` **trivially green** (the harness ignores the overlay), so even the *human-reviewed* default path loses its mechanical regression gate — not just the B7 auto-merge path. B3 is therefore a prerequisite of the threshold-tune *landing path itself*, present from B4 onward.
- **Suggested resolution.** Add `B3 → B4` (or, more precisely, `B3 → B5`, since B5 is when threshold CRs first land in production). Keep `B3 → B7` as well. B3 should not be presentable as parallel-to-everything-but-B7; it gates the first live threshold CR.

### 3. Proposal-target guard CI check (Q5 layer 2) has no clear owning bead in the live path

- **Issue.** B6 says it will *"document the CI check"* that blocks a proposed rule/threshold targeting a `curio.*` series. Documenting ≠ implementing. The actual enforcing check must exist and be wired into the CR gate before B5 dispatches a real polecat, or air-gap layer 2 is vapor at go-live (only layers 1 and 3 are real).
- **Why it matters.** B5 (dispatch) depends transitively on B6, but B6's acceptance criteria only require "each proposal kind has a single documented landing path" — it does not require the CI check to be executable. The guard's *enforcement* is a dangling dependency of B5.
- **Suggested resolution.** Either make implementing the proposal-target CI check an explicit acceptance criterion of B6 (and thus a hard predecessor of B5), or split it into its own bead blocking B5. State which gate runner hosts it (it is not one of the four rig gates on this bead).

## Observations

- **B6 → B1 dependency looks overstated.** B6's scope (bead-label definitions, CR-vs-bead routing, dedup query *contract*) is definitional and does not consume the digest *artifact*. Marking B6 "Depends on B1 (digest)" likely serializes work that could run in parallel. If B6's deliverable is the contract/labels (not code that parses a digest), it can join B1/B3 as a third parallel starting point, shortening the critical path to B4. Confirm whether B6 truly needs B1's output or only its existence as a consumer.

- **No circular dependencies.** Traced B1→{B2,B4,B6}, B6→{B4,B7}, B3→B7, B4→B5. Acyclic. Good.

- **Conservative tail-ordering is correct.** B7 (auto-merge) shipping last and default-OFF is the right sequencing — it mirrors the Phase-2 shadow→live and Phase-1 candidates-only disciplines and ensures the lane lands fully human-gated before any auto-merge is even possible. No change needed.

- **B2 before B4/B5 is correctly ordered.** Air-gap filter (B2) transitively blocks live dispatch (B2→B4→B5), so no un-air-gapped digest can be emitted in production. The substrate-filter-as-primary-defense (Q5 layer 1) is sequenced ahead of the consumer, which is right.

- **B5's content-conditional auto-merge (B7) is net-new Refinery policy, not an existing gate.** The Refinery runs rig-level gate *commands* on the stack tip (`internal/refinery/engineer.go` gates) plus an `approved-by:` label gate (`engineer_auto_test_pr_gate_test.go`); it has no existing "CR touches ONLY these keys → auto-eligible" file-scope policy. B7 correctly treats this as new work, but the breakdown should flag B7's dependency on Refinery-internal policy code (a cross-cutting surface beyond `internal/curio`), since that widens B7's blast radius and review scope beyond the curio package the other beads live in.

- **Cadence/closed-window sequencing is self-consistent.** The 08:00 UTC cron (Q1) reading a 30-min-trailing `closedWindowCursor` needs no extra ordering logic; dispatch time and window freeze are decoupled by construction. No timing dependency between B5's schedule and the substrate's cursor.

## Sources

- `.designs/curio-p3-retrospect-agent/design-doc.md` — P3 design under review — accessed 2026-06-12
- `.designs/curio-p3-retrospect-agent/child-beads.md` — B1–B7 breakdown + dependency graph — accessed 2026-06-12
- `.designs/curio-p2-retrospective/design-doc.md:70-71,117-122` — L1 ledger "nothing populates it yet" + post-close write-hook spec — accessed 2026-06-12
- `internal/curio/store.go:50-68` — `curio_ledger` DDL, "Empty until Phase 2"; no INSERT path in repo — accessed 2026-06-12
- `internal/curio/read.go:45-50` — `ReadCandidatesBefore` read-only API (B1 reuse) — accessed 2026-06-12
- `internal/curio/rules.go:139-241` — `DefaultRateThresholds` / `DefaultRulesWithThresholds` (config-driven thresholds, gc-e2uvyr.3 landed) — accessed 2026-06-12
- `internal/curio/replay_test.go`, `internal/curio/testdata/replay/` — replay harness grades compiled defaults only (no overlay yet) — accessed 2026-06-12
- `cmd/curio-proposer/config.go:19-38`, `main.go:85-89` — `curio.llm.enabled` kill switch already exists — accessed 2026-06-12
- `plugins/{casc-patrol-dispatch,wiki-patrol-dispatch}/` — cron-gated dispatch-plugin pattern B5 mirrors (exists) — accessed 2026-06-12
- `internal/refinery/engineer_auto_test_pr_gate_test.go`, `internal/refinery/batch.go:214-374` — existing gate-command + approval-label mechanism; no file-scope auto-merge policy (B7 is net-new) — accessed 2026-06-12

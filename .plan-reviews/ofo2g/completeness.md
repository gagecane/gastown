# Plan Completeness

## Verdict
FAIL — the plan rests on a Layer-1 prerequisite (`curio_ledger` outcome
population) that does not exist in the codebase and is not created by any
child bead, so the lane would ship reasoning over empty precision data; plus
several enforcement mechanisms it relies on (auto-merge policy, CR CI checks,
proposal expiry, cluster-keyed dedup) have no owning bead.

## Must Fix (blocks implementation)

### 1. L1 ledger population is unbuilt, yet treated as a done prerequisite
- **Issue.** The plan repeatedly assumes Layer-1 Outcome Tracking is a working
  prerequisite: the L1 table row says *"UNCHANGED. The agent READS the ledger"*
  and *"L1 is a prerequisite, not part of this epic"* (design-doc §"How this
  maps…"); B1 defines `ReadOutcomeHistory()` as *"a pure SELECT over
  `curio_ledger`"*; the digest's headline section is *"Per-rule precision (from
  `curio_ledger`)"* (Q2). **But nothing writes to `curio_ledger`.** Verified:
  `grep -rn "INSERT INTO curio_ledger\|UPDATE curio_ledger"` → only the DDL in
  `internal/curio/store.go`; there is no daemon post-close hook / reconciler
  (`internal/daemon/` has no ledger writer). The P2 doc explicitly scoped
  population as a separate piece (*"daemon's existing bead-close event stream …
  extended with a curio-ledger reconciler"*, P2 §"Population flow") — and P3
  drops P2 builds 4-6, which is where that work would have lived. So population
  is now **orphaned**: the table exists, schema is provisioned, but it is
  permanently empty.
- **Why it matters.** With an empty ledger, `ReadOutcomeHistory()` returns no
  rows, the digest's per-rule precision table is blank, and the polecat has no
  precision signal — which is the *entire* input that justifies a threshold
  tune (Q4 requires *"measured precision < 0.80"*). The lane would dispatch
  nightly, read a digest with zero outcome data, and either propose nothing or
  propose tunes it cannot justify. The whole epic's value proposition
  ("measure precision, tune thresholds") is non-functional on day one.
- **Suggested resolution.** Add a child bead **B0 — L1 ledger population**
  (daemon post-close reconciler: on any bead close, if `bead_id ∈ curio_ledger`,
  write `outcome` + `resolved_at` from the close reason; and a writer that
  inserts the `(bead_id, fingerprint, rule_id, filed_at, outcome='')` row when
  Curio files a bead). Make B1 depend on it. **Alternatively**, if L1 is
  genuinely meant to land in a separate epic first, the plan must say so
  explicitly, cite the tracking bead, and mark this epic **blocked** on it —
  not assert L1 is "unchanged/done."

## Should Fix (important but not blocking)

### 2. B7 auto-merge policy names no implementation mechanism
- B7 / Q4 describe a *"Refinery merge policy"* that conditionally auto-merges a
  threshold CR iff it *"touches ONLY `daemon.json` `rate_thresholds` keys"* +
  precision assertion + replay-A. But the Refinery's gates are **shell-script
  gates per rig** (`internal/refinery/batch.go` `runBatchGates`; gates are
  configured via `gt rig settings … merge_queue.gates`). There is no
  path-scoped, body-asserting, conditional auto-merge policy engine today
  (`grep` for MergePolicy/AutoMerge surfaces only CRUX `--auto-merge` and
  integration-branch auto-land). B7 needs to specify *how* this policy is
  implemented (new refinery code? a gate script that inspects the diff and the
  CR body? a label the polecat sets that a gate keys off?). As written it's
  under-scoped. Mitigating factor: B7 ships **default OFF**, so this can't break
  anything at launch — but the bead can't be implemented as described.

### 3. CR CI checks referenced by Q4/Q5 have no owning bead
- Q5 layer-2 says *"a CR CI check enforces … may NOT target a Curio-owned
  series (`curio.*`)"*; Q4 relies on *"a CI check on the CR re-runs the replay
  harness."* The replay re-run is covered (B3 + the Refinery already runs
  `go test ./internal/curio/...`). But the **proposal-target guard** CI check
  (reject a CR proposing a `curio.*`-targeting rule/threshold) is only
  *documented* in B6 ("document the CI check that enforces it"), never *built*.
  Either fold building it into B6's scope or add a bead. Otherwise air-gap
  layer-2 is prose, not enforcement.

### 4. Proposal expiry / breaker-reset is unplanned (lane can self-wedge)
- Q7's volume circuit breaker skips dispatch when open `curio-proposal` beads
  exceed a ceiling (~10). Nothing ages out stale proposals (P2 open-question #2,
  "proposal expiry," is left unaddressed). Failure mode: proposals accumulate,
  breaker trips, lane stops dispatching — and stays stopped until a human
  manually works the backlog down. No bead covers expiry, auto-close of stale
  proposals, or an alert when the breaker has been tripped for N days. Add an
  expiry/auto-close step (e.g., close `curio-proposal` beads untouched after N
  days → feeds `false_positive`/`deferred` back to the ledger) and/or a
  breaker-tripped alert.

### 5. Cluster identity for dedup is undefined end-to-end
- Q7/B6 dedup *"against open `curio-proposal` beads … skips any cluster already
  covered."* The candidate has a `StateHash` (the dedup identity,
  `internal/curio/candidate.go`), but the plan never says the proposal bead
  **records** that cluster key, nor how `bd list --label curio-proposal` lets
  the formula match a digest cluster to an existing bead. Without a stable
  cluster-key → bead linkage (a label, a field, or a convention in the bead
  body), the dedup step in B4/B6 cannot actually deduplicate. Specify how the
  cluster key is stamped on the proposal bead and queried back.

### 6. Digest-file delivery into the polecat sandbox is unspecified
- B5 run.sh does `curio-proposer --emit-digest <path>` then `gt sling …
  --var digest_path=<path>`. Polecats run in **isolated worktrees**. The plan
  never states whether `<path>` is a host-shared absolute path the agent can
  read from inside its sandbox, or whether the digest must be staged into the
  worktree. If the sandbox can't see the emit path, the formula's step-1 ("read
  the digest at `{{digest_path}}`") fails. Add a sentence (and a B5 test) on
  the path contract.

## Observations
(Non-blocking)

- **Migrations:** none needed — `curio_ledger` schema is already provisioned
  (`store.go` `ledgerDDL`, applied via `ensureTables`). Correctly implies no
  migration bead. Good.
- **P2 scaffolding cleanup:** confirmed there is **no** in-process Anthropic/LLM
  client or patch generator to remove — P2 builds 4-6 were never written
  (`cmd/curio-proposer` only reads config + closed-window candidates). So the
  "DELETE the in-proc LLM client" framing in the L2 table is aspirational, not
  actual deletion work. Harmless, but the plan slightly overstates what's being
  removed; no cleanup bead is needed (correct that none exists).
- **Test coverage per bead is solid:** B1 golden-file + import-graph invariant,
  B2 single-sourcing assertion, B3 two-direction overlay grading, B5 three
  skip-path tests. Good completeness on the testing dimension.
- **Monitoring:** `notify_on_failure = true` + receipt-absence observability
  (design §"Residual risks") covers dispatch failure adequately. The only gap
  is the breaker-tripped-backlog alert noted in Should-Fix #4.
- **Rollback:** the kill-switch mapping (stop slinging / `llm.enabled=false`) is
  a clean, well-covered disable path. No source rollback risk since nothing
  auto-merges by default. Adequate.
- **Substrate methods verified present:** `ReadCandidatesBefore`,
  `closedWindowCursor`, `DefaultRulesWithThresholds`, `DefaultRateThresholds`,
  `CurioSeriesPrefix`, `suppressed()`, and the daemon `rate_thresholds` overlay
  (`curioRateThresholds`) all exist — the B1/B2/B3 reuse claims check out. Only
  `ReadOutcomeHistory` is net-new (B1), and it is correctly scoped as new.

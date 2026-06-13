# Curio P3 — Proposal Taxonomy, Cluster-Key Dedup, and the Air-Gap Guard

This document is the **consumer contract** for the Curio P3 Retrospect lane
(epic `gu-60sk4`, child bead `gu-9tmry`). It defines:

1. The three proposal kinds and the single landing path each one takes.
2. The cluster-key dedup convention that lets the Retrospect formula recognize a
   cluster it already proposed on, so it does not re-propose nightly.
3. The proposal-target air-gap guard — built as a CI check, not just documented.

It is *definitional*: it does not parse the `--emit-digest` artifact. B0/B1/B4
(the formula) and B5 (the dispatch plugin) consume the conventions defined here.

## Proposal taxonomy (design-doc Q3)

The Retrospect polecat PROPOSES; the Refinery and a human DISPOSE. Each kind the
polecat's reasoning produces lands as exactly one concrete Gas Town artifact:

| Proposal kind | Artifact the polecat produces | Landing path | Gate |
|---------------|-------------------------------|--------------|------|
| **Threshold tune** | A CR editing `daemon.json` `patrols.curio.rate_thresholds` (config-driven since `gc-e2uvyr.3` — NO rebuild) plus an updated/added replay fixture if needed | Branch → CR → merge queue | Replay CI must grade A. Auto-merge path possible later (Q4 / B7), default OFF — every tune needs human approval initially. |
| **New-rule sketch** | A **proposal bead** (`type=task`, label `curio-proposal`, assigned to mayor) describing the rule, its signal, anchor incidents, and a suggested fixture — **NOT** a code CR | Bead assigned to mayor for a human to author the Go rule | Always human (never auto-merged). New rules are still compiled in. |
| **Root-cause hypothesis** | A **bead** (`type=task`, label `curio-hypothesis`) or a comment on the cluster's anchor bead, linking the cluster + the hypothesis + evidence | Bead / comment | None (informational). |

### Why a threshold tune is a config CR, not a source CR

Since `gc-e2uvyr.3`, the per-series fire thresholds are **config-driven**:
`curio.DefaultRateThresholds()` (in `internal/curio/rules.go`) supplies the
calibrated defaults, and `daemon.json` `patrols.curio.rate_thresholds` overlays
operator overrides on top (see `internal/daemon/curio_dog.go`
`curioRateThresholds`). A tune therefore edits JSON, not Go — smaller blast
radius, no rebuild, trivially revertible. **New rules**, by contrast, are still
compiled in, so the polecat only files a *sketch bead* a human turns into code —
strictly more conservative, matching "agent PROPOSES, human DISPOSES."

### The two bead labels

- `curio-proposal` — a new-rule sketch. `type=task`, assigned to mayor. The
  human authors the rule. Never auto-merged.
- `curio-hypothesis` — a root-cause hypothesis. `type=task`, informational. No
  gate; it exists to capture reasoning and evidence for a human to act on.

Both labels are queried by the dedup step below. A threshold tune produces **no
bead** — it is a CR — so threshold-tune dedup is the merge queue's job (a second
identical tune CR is a no-op diff against the first once it lands), not the
bead-query dedup described next.

## Cluster-key dedup convention (the dedup identity, end-to-end)

The dedup identity of a finding is the candidate's **`StateHash`**
(`internal/curio/candidate.go`). `StateHash` is deliberately independent of
volatile dimensions (e.g. which transient owner currently holds a leak): every
flap of the same actionable condition maps to one `StateHash`. It defaults to
the candidate's `Fingerprint` (a 12-hex `internal/fingerprint` digest), so a
rule that does not set a coarser state key behaves as a per-`(rule,target)` key.

For dedup to work end-to-end, the key must be **stamped on the proposal bead**
and **queried back** by the formula. The convention:

### Stamp: `cluster:<StateHash>` label on every proposal/hypothesis bead

When the Retrospect polecat files a `curio-proposal` or `curio-hypothesis` bead
for a digest cluster, it stamps the cluster's `StateHash` as a label:

```bash
bd create "Curio: <short cluster description>" \
  --type task --label curio-proposal --label "cluster:<StateHash>" \
  --assignee mayor --description "<sketch / hypothesis + evidence>"
```

`<StateHash>` is the literal `state_hash` value carried on the digest cluster
(the same field on `curio.Candidate`). The `cluster:` prefix namespaces it so it
never collides with other labels. The label value is the exact dedup key — no
transformation — so the stamp string equals the later query key by construction.

### Query back: match each digest cluster to an existing bead

Before proposing, the formula (B4 step 2) lists open proposal/hypothesis beads
and extracts their `cluster:` labels:

```bash
# All clusters already covered by an open proposal or hypothesis.
bd list --label curio-proposal --status open --json \
  | jq -r '.[].labels[]? | select(startswith("cluster:"))'
bd list --label curio-hypothesis --status open --json \
  | jq -r '.[].labels[]? | select(startswith("cluster:"))'
```

For each cluster in the digest, the polecat checks whether
`cluster:<that cluster's StateHash>` is in that set. If it is, the cluster is
**already covered** — skip it (do not re-propose). If it is not, the cluster is
new — propose, and stamp `cluster:<StateHash>` on the new bead so the *next*
night's run skips it.

### Round-trip guarantee

The stamp and the query use the identical string (`cluster:<StateHash>`), and
`StateHash` is stable across nightly runs for the same actionable condition
(that is the whole point of the state-hash damper). So a cluster proposed on
night N is found by night N+1's query and skipped, until the first proposal bead
is closed/merged — at which point the cluster, if it recurs, is eligible to be
proposed again. Without this linkage the lane would re-propose the same cluster
every night until the first proposal merges (the failure mode B6 prevents).

## Air-gap layer 2 — the proposal-target guard (BUILT, not just documented)

Design-doc Q5 specifies a three-layer self-reference air-gap. Layer 1 (the
`--emit-digest` substrate filter) keeps Curio's own series and self-reactions out
of the agent's input. Layer 3 is prompt scoping. **Layer 2 is this mechanical
guard on the CR the agent ultimately produces**, and it is enforcement, not
prose:

> A CR that proposes a rule or threshold **targeting a Curio-owned series
> (`curio.*`)** fails CI.

### Implementation

`scripts/guards/curio-proposal-target-guard.sh`. It diffs the CR against its
merge base (`origin/main` by default; override with `GT_PROPOSAL_GUARD_BASE`) and
inspects only the **added** lines for a quoted `"curio.<series>"` literal used as
a detection target. The canonical violation is a threshold tune that adds a
`"curio.cycle": 0` key to `daemon.json` `rate_thresholds`; any added
`"curio.<series>"` literal in non-test source/config is flagged. It exits 1
(block) on a hit and 0 (pass) otherwise.

Deliberately precise to keep false positives near zero:

- Only **added** lines are inspected — pre-existing references on main never
  trip it.
- Test files (`*_test.go`, `*_test.sh`) and fixture trees (`testdata/`,
  `testfixtures/`) are excluded: fixtures legitimately reference Curio series to
  exercise the *live* air-gap (e.g. `internal/curio/loopbreaker_test.go` uses a
  `"curio.cycle"` threshold to prove `rateSpikeRule` suppresses it).
- The bare `CurioSeriesPrefix = "curio."` constant does not match — the pattern
  requires a concrete series name after the dot.

**Bead references.** Q5 also air-gaps proposals that reference Curio's own beads.
Bead IDs are opaque random tokens and are not statically pattern-matchable, so
that half is enforced upstream by layer 1's substrate filter (the
`Input.CurioBeads` causal-root exclusion). This guard owns the `curio.*`-series
half, which *is* statically decidable.

### How it is exercised

- `scripts/guards/curio-proposal-target-guard_test.sh` proves a
  `curio.*`-targeting CR is rejected and a non-Curio CR passes (8 cases,
  including the test-file/fixture exclusions and the pre-existing-on-base case).
- That shell suite runs inside `go test ./...` via
  `internal/curio/proposal_guard_test.go` (`TestProposalTargetGuard`), so the
  **merge-queue gate and CI exercise the proof**, not only `make test-makefile`.
- It is also listed in `make test-makefile` alongside the other guard tests.

### Enabling it as a live merge-queue gate

Rig merge-queue gates are runtime configuration (`gt rig settings`, gitignored),
not committed source, so this bead ships the guard + its proof. The lane wires it
on when the Retrospect dispatch (B5) is enabled — so air-gap layer 2 is live
before any real polecat dispatches:

```bash
gt rig settings set <rig> \
  merge_queue.gates.curio-proposal-target \
  '{"cmd":"scripts/guards/curio-proposal-target-guard.sh","phase":"pre-merge","timeout":"1m"}'
```

## Precision-gate auto-merge policy (Curio P3 B7) — default OFF

A threshold-tune CR is normally a config CR that lands through the merge queue
**with human approval** (the taxonomy table above). B7 adds a narrow, opt-in path
for that one proposal kind to auto-merge (no human approval) when the P2
conjunction (design-doc Q4) holds:

1. The CR touches **only** `daemon.json` `patrols.curio.rate_thresholds` keys.
2. The CR body **asserts measured precision < 0.80** for the tuned series.
3. The replay harness **grades A** with the tuned overlay applied (B3's
   `GradeWithThresholds`).

### Mechanism: a re-verified label (never trusted)

The Retrospect polecat stamps the label **`curio-auto-eligible`** on its
threshold-tune proposal when it believes the conjunction holds. **The label only
makes the CR _subject_ to the policy — it is never trusted as authorization.**
The Refinery policy (`internal/refinery/curio_automerge.go`,
`EvaluateAutoMerge`) **independently re-derives** conjuncts 1 and 3 from git and
the replay harness:

- **Diff scope (conjunct 1)** is recomputed from `git diff --name-only` (only
  `mayor/daemon.json` may change) *and* a structural JSON compare that confirms
  the change is confined to the `rate_thresholds` block — any source change or
  fixture deletion fails, even if it rides in the same file.
- **Replay grade (conjunct 3)** is re-run by the gate over the checked-in
  corpus (`internal/curio/testdata/replay`) with the branch's overlay; the
  proposer's claimed grade is ignored.
- **Precision assertion (conjunct 2)** is the one human-authored input read from
  the CR body — a *necessary* condition the gate cannot itself measure (it has
  no ledger), not a merge authorization.

This is mechanism (b) from the B7 scope; the diff-scope and replay-A checks are
re-verified by the gate, not asserted by the proposer (B7 invariant 4).

### Default OFF

The policy ships **disabled** (`merge_queue.curio_auto_merge` absent / `enabled:
false`). With it off, a `curio-auto-eligible` CR is **held for human review**
(the Refinery defers it like a PR awaiting approval and emits a
`refinery_paused` event with reason `curio_needs_approval`); an unlabeled CR is
untouched and follows the normal merge path. Turn the policy on only after
observing several cycles of human-reviewed tunes the human would have approved
anyway (the Phase-2 shadow→live discipline). Enable it per rig:

```bash
gt rig settings set <rig> merge_queue.curio_auto_merge '{"enabled": true}'
```

With the policy ON, a CR passing all three conjuncts auto-merges after gates;
**any disqualifier routes to human review** — a source change, a fixture
deletion, a daemon.json edit beyond `rate_thresholds`, a missing/`>= 0.80`
precision assertion, or a replay grade below A.

## Sources

- [.designs/curio-p3-retrospect-agent/design-doc.md](../.designs/curio-p3-retrospect-agent/design-doc.md) — Q3 (taxonomy), Q4 (precision gate), Q5 (air-gap) — accessed 2026-06-13
- [.designs/curio-p3-retrospect-agent/child-beads.md](../.designs/curio-p3-retrospect-agent/child-beads.md) — §B6, §B7 scope — accessed 2026-06-13
- `internal/curio/candidate.go` (`StateHash`), `internal/curio/rules.go` (`DefaultRateThresholds`), `internal/curio/record.go` (`CurioSeriesPrefix`), `internal/curio/replay.go` (`GradeWithThresholds`, `GradeA`), `internal/daemon/curio_dog.go` (`curioRateThresholds`), `internal/refinery/curio_automerge.go` (the B7 policy) — accessed 2026-06-13

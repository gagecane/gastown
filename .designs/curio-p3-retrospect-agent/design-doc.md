# Design: Curio Phase 3 — Retrospect Lane as a Claude-Agent Polecat

> Design document for gu-sdjm5 (child of the Curio epic gu-fcwx8).
> Refines the Phase-2 design (`.designs/curio-p2-retrospective/design-doc.md`,
> gu-fcwx8.1) by REPLACING the Layer-2 implementation: the LLM Hypothesizer is
> no longer an in-process LLM client embedded in `cmd/curio-proposer`. It is a
> Claude-agent **polecat**, dispatched on a cadence through Gas Town's existing
> sling/formula/Refinery machinery. Layers 1 (Outcome Tracking) and 3
> (Self-Mutation Pipeline) are preserved in spirit; their gates collapse onto
> mechanisms the polecat model already provides.
>
> This is a DESIGN bead. No production code lands from it. Output = this doc +
> the child-bead breakdown in the companion `child-beads.md`, ready to become
> the next implementation epic.

## Executive Summary

The Phase-2 design specified the Retrospect lane (Layer 2) as `curio-proposer`
"builds 4-5/6": an in-process LLM client (Anthropic SDK), prompt assembly,
structured-output validation, and a code-patch generator, all compiled into a
new mutation-capable surface. That is a lot of net-new, security-sensitive
machinery — an LLM credential in a daemon-adjacent binary, a bespoke
prompt/response contract, a patch generator, and a `gt curio apply` write path —
all of which Gas Town **already has** in the polecat abstraction.

**The core idea:** a polecat already IS a Claude agent dispatched through Gas
Town, gated by the Refinery merge queue, isolated in its own worktree, and
fully audited in Dolt history. If we dispatch a "Retrospect polecat" on a
cadence, we get the LLM, the gated write path, the kill switch, and the audit
trail **for free** — none of it has to be built into `curio-proposer`.

In this model:

- **The LLM** = the polecat's own Claude session. No SDK, no credential in the
  daemon, no prompt plumbing, no model config in `curio_dog.go`.
- **The write path** = the polecat opens a CR; the Refinery merge queue gates
  it. "Earn filing rights via the merge queue" (gu-u6l9r / gu-4t8br) is the
  *default* polecat behavior, not new code.
- **The kill switch** = dispatch is opt-in. Not slinging the formula = lane
  off. The live Patrol (`curio.enabled`) is completely untouched.
- **The audit trail** = every proposal is a branch + CR + bead in Dolt, exactly
  like any other polecat's work.

`cmd/curio-proposer` does NOT disappear. It is **demoted from actor to
substrate**: it keeps its three tested invariants (write-incapable import
graph, closed-window cursor, kill-switch isolation) and gains exactly one new
job — render the closed-window candidate digest the polecat reads. It never
calls an LLM and never files anything. The agent consumes its output.

**Recommendation: GO.** Agent-as-polecat is clearly better than finishing the
embedded `curio-proposer` builds 4-6/6 on every axis that matters for a
low-frequency, high-stakes, human-reviewed lane — reviewability, blast radius,
credential surface, and lines of net-new trusted code — at the cost of
per-run determinism and latency that this lane does not need. Trade-offs are
enumerated in the final section.

## How this maps onto the Phase-2 design (gu-fcwx8.1)

The P2 doc specifies three layers. This epic keeps Layers 1 & 3 and replaces
the *implementation* of Layer 2. The table below is the authoritative diff;
every later section elaborates one row.

| P2 Layer | P2 implementation | P3 change | Net effect |
|----------|-------------------|-----------|------------|
| **L1 — Outcome Tracking** (`curio_ledger`) | daemon post-close hook populates ledger; `precision(rule)` computable | **UNCHANGED.** The agent READS the ledger (via a rendered digest or scoped read role). | Keep as designed. L1 is a prerequisite, not part of this epic. |
| **L2 — LLM Hypothesizer** | `curio-proposer` builds 4/6 (in-proc LLM call + JSON validation) and 5/6 (patch gen) | **REPLACED.** A Claude polecat slung on a cadence reads the digest + ledger and produces the same three proposal kinds. `curio-proposer` becomes the read substrate that renders the digest. | The in-proc LLM client, prompt plumbing, structured-output validator, and `model` config are all DELETED from the plan. |
| **L3 — Self-Mutation Pipeline** | `curio-apply` subcommand + human-gate beads + merge-queue entry (build 6/6) | **REPLACED in mechanism, preserved in spirit.** The "propose" step is literally the polecat opening a CR. The Refinery + human review IS the gate. No `gt curio apply` write path is built. | The staged gate (shadow-propose → human-approve OR precision-gate-pass → commit) maps onto branch → CR → merge queue. |

### Kill-switch mapping

P2 had four independent switches (`enabled` / `page_for_real` / `llm.enabled` /
`self_mutation.enabled`). In P3 the last two **collapse into one fact: "is the
Retrospect formula being slung?"**

| P2 switch | P3 equivalent |
|-----------|---------------|
| `curio.enabled` (live Patrol) | **UNCHANGED** — still gates the daemon dog. Untouched by this epic. |
| `curio.page_for_real` (paging) | **UNCHANGED** — Phase-2 shadow→live paging gate, orthogonal. |
| `curio.llm.enabled` (Retrospect on) | Becomes "the dispatch plugin is installed + its cron gate is active." Not slinging = lane off. Retained as a *declarative pre-check* the dispatch plugin reads (see Q1). |
| `self_mutation.enabled` + sub-gates | Becomes "the polecat opens a CR; the Refinery merges or doesn't." Auto-apply vs. human-gate is decided by proposal kind (Q4), not a daemon flag. |

The conservative default is preserved and *strengthened*: with no formula
dispatched, the lane produces nothing — identical to `llm.enabled=false`, but
with zero LLM-capable code in any long-running process.

## Preserved invariants (from the P2 doc, enumerated)

All eight P2 safety invariants survive. Where the mechanism changes, the new
enforcement point is named:

1. **Air-gap (Call 1A):** Curio cannot detect/propose about its own activity.
   *Enforced in P3 by:* (a) the existing loop-breaker on the live side; (b) the
   digest renderer filtering candidates whose `rule_id` starts with `proposed_`
   AND candidates attributable to the `curio` actor / `CurioBeads` causal set;
   (c) the polecat's prompt explicitly scoping out self-referential clusters.
   See Q5.
2. **Write-incapability of the read path:** `curio-proposer`'s import graph
   still excludes `internal/beads` and `internal/daemon`
   (`TestImportGraph_NoWritePath`). The digest renderer adds NO write deps —
   it only formats what `Reader` already returns. The POLECAT has a write path,
   but it is the *normal, Refinery-gated* one, not an in-binary one.
3. **Closed-window cursor:** The digest is rendered from
   `ReadCandidatesBefore(now - closedWindowMargin)`. The 30-minute margin is
   mechanically unchanged; the agent only ever sees the frozen window the
   substrate hands it.
4. **Replay-graded mutations:** Any threshold/rule CR the polecat opens MUST
   pass the replay harness (`internal/curio/testdata/replay/`) before the
   Refinery merges. This is now a CI gate on the CR, not a pre-apply check in a
   binary. See Q6.
5. **Human gate on structural changes:** New rules and retirements are CRs that
   require human approval before merge. Threshold tunes may ride the
   precision-gate auto-path (Q4). The Refinery's review requirement IS the gate.
6. **Kill-switch isolation:** Preserved (see mapping above). Stopping dispatch
   freezes the lane without touching Patrol.
7. **Audit trail:** Every proposal is a branch + CR + bead in Dolt. Strictly
   *more* auditable than P2's `.runtime/curio-proposals/*.json` files, which
   lived outside version control.
8. **No runtime rule mutation:** Rules stay compiled in. The polecat edits
   `internal/curio/rules.go` source via CR; the merge queue runs the tests.
   No runtime rule registry exists. Identical guarantee to P2.

---

## Design questions

### Q1 — Dispatch trigger & cadence

**Decision: a daemon patrol-plugin (`curio-retrospect-dispatch`) with a `cron`
gate, mirroring `casc-patrol-dispatch`/`wiki-patrol-dispatch`.**

The codebase already has the exact pattern: town-level plugins in
`~/gt/plugins/` whose frontmatter declares `[gate] type = "cron"` and whose
`run.sh` calls `gt sling <formula> <rig>` and records a receipt. The daemon dog
executes the script on schedule and the cooldown/cron gate prevents
double-dispatch.

```toml
# plugins/curio-retrospect-dispatch/plugin.md (frontmatter)
+++
name = "curio-retrospect-dispatch"
description = "Nightly dispatch of the Curio Retrospect polecat (LLM hypothesizer lane)"
version = 1

[gate]
type = "cron"
schedule = "0 8 * * *"          # 08:00 UTC daily, well after the busy window

[tracking]
labels = ["plugin:curio-retrospect-dispatch", "category:scheduler", "curio"]
digest = true

[execution]
type = "script"
timeout = "30m"
notify_on_failure = true
severity = "low"                # lane-off ≠ outage; rules just stay frozen
+++
```

`run.sh` responsibilities (kept deliberately thin — the formula does the work):

1. **Pre-check the declarative kill switch.** Read `mayor/daemon.json`
   `patrols.curio.llm.enabled`. If false/absent → record a `result:skipped`
   receipt and exit 0. This preserves the P2 `llm.enabled` switch as a
   *config-level* off-ramp even when the plugin is installed (defense in depth:
   uninstalling the plugin OR flipping the flag both disable the lane).
2. **Respect the closed-window margin implicitly.** The cron fires at 08:00 UTC;
   the substrate's `closedWindowCursor` already trails `now` by 30m, so the
   agent never reads in-flight candidates regardless of dispatch time. No extra
   logic needed.
3. **Single-instance guard.** Before slinging, check for an open
   `curio-retrospect` convoy/bead from a prior run still in flight (mirrors
   `wiki-patrol-dispatch`'s guard). If one exists → `result:skipped`. Prevents
   a slow review from stacking a second polecat.
4. `gt sling <retrospect-formula> <rig>` with `--var` for the run window.
5. Record a `type:plugin-run` receipt for the cooldown gate + digest.

**Cadence: nightly (`0 8 * * *`).** Rationale:

- Outcome data (ledger precision) changes on the order of days, not minutes.
  Nightly is frequent enough to catch drift, sparse enough to keep cost and CR
  volume negligible.
- 08:00 UTC is past the overnight-batch busy window; the closed window it reads
  is settled.
- **Post-incident augmentation (deferred to a follow-up bead):** an escalation
  of severity ≥ HIGH could *additionally* sling a Retrospect run scoped to the
  incident's candidate cluster. Designed-for but not in the first epic — the
  nightly cadence is the MVP.

**Alternatives considered:**

- *Cron (system crontab) instead of a daemon plugin* — rejected: loses the
  daemon's receipt/digest/cooldown machinery, failure isolation, and the
  `gt sling` convoy integration. The plugin abstraction exists precisely for
  "town-wide periodic dispatch."
- *Daemon hook firing on every patrol cycle* — rejected: 96×/day is far too
  frequent for a lane whose inputs change daily, and it would couple Retrospect
  dispatch to live-Patrol timing (violating kill-switch independence).

### Q2 — Input contract

**Decision: `curio-proposer` renders a deterministic, read-only digest to a
file; the polecat reads the file. The agent does NOT query Dolt directly.**

This keeps the trust boundary clean: the only component touching Dolt is the
already-tested write-incapable `Reader`. The agent gets a frozen artifact, not
a live DB connection (no scoped read role to provision, no risk of the agent
issuing an unbounded/expensive query, fully reproducible input).

`curio-proposer` gains one new mode, `--emit-digest <path>`, that:

1. Loads the kill switch (existing) — if `llm.enabled=false`, exits without
   emitting (existing behavior, unchanged).
2. Reads `ReadCandidatesBefore(closedWindowCursor(now))` (existing).
3. Reads outcome history from `curio_ledger` via a NEW read-only method
   `Reader.ReadOutcomeHistory()` (the one piece of P2 build-4 scope we keep:
   it's a pure SELECT, no write deps).
4. **Filters self-referential candidates** (Q5): drops `rule_id` prefixed
   `proposed_`, drops `CurioSeriesPrefix` series, drops candidates whose causal
   root is in the Curio-filed set.
5. Renders a stable Markdown + embedded-JSON digest to the path.

**Digest shape** (Markdown for agent readability, with a fenced JSON block for
exactness — the agent reads prose, the replay/test asserts the JSON):

```markdown
# Curio Retrospect Digest — window <= 2026-06-12T07:30:00Z

## Per-rule precision (from curio_ledger)
| rule_id              | resolved | precision | recent FPs |
|----------------------|----------|-----------|------------|
| alarm_rate_spike     | 42       | 0.65      | 3          |
| kill_signal_near_dolt| 20       | 0.45      | 11         |
| dead_owner_admission | 8        | 0.88      | 1          |

## Unresolved candidate clusters (closed window, self-refs excluded)
- cluster abc123 — rule alarm_rate_spike, series=sling, 7 occurrences
  - "series \"sling\" rate 450 exceeds threshold 350"
  - ...

```json
{ "cutoff": "...", "rules": [...], "clusters": [...] }
```
```

**What the agent receives, exactly:** the digest file path (via `--var
digest_path=...` on the sling) plus the formula's instructions (the output
contract, Q3). Nothing else. No DB credentials, no live state.

**Alternative considered — agent queries Dolt via a scoped read role:**
rejected for the first epic. It would require provisioning and securing a
read-only Dolt user, and it moves the closed-window/air-gap enforcement out of
the tested substrate and into the agent's discipline. The rendered-digest
approach keeps both invariants mechanical. (A scoped read role could be a
future optimization if digests grow too large to render eagerly.)

### Q3 — Output contract / proposal taxonomy

**Decision: three proposal kinds, each with a distinct, pre-defined landing
mechanism. The polecat's job is to produce the artifact for each; the
Refinery/human disposes.**

The P2 LLM output schema (hypotheses / threshold_adjustments /
new_rule_proposals / retirement_candidates) is preserved as the agent's
*thinking* structure, but each kind now lands as a concrete Gas Town artifact:

| Proposal kind | Artifact the polecat produces | Landing path | Gate |
|---------------|-------------------------------|--------------|------|
| **Threshold tune** | A CR editing `daemon.json patrols.curio.rate_thresholds` (config-driven since gc-e2uvyr.3 — NO rebuild) + an updated/added replay fixture if needed | Branch → CR → merge queue | Precision-gate auto-path possible (Q4); else human |
| **New-rule sketch** | A **proposal bead** (`type=task`, `label=curio-proposal`) containing the rule description, signal, anchor incidents, and a suggested fixture — NOT a code CR | Bead assigned to mayor for human authoring | Always human (never auto-merged) |
| **Root-cause hypothesis** | A **comment/bead** linking the candidate cluster + the hypothesis + evidence | Bead (`label=curio-hypothesis`) or comment on the cluster's anchor bead | None (informational) |

**Key refinement vs. P2:** P2 had threshold tunes editing `rateThresholds` in
`rules.go` (a Go source change requiring a rebuild). Since gc-e2uvyr.3 landed,
thresholds are **config-driven** via `daemon.json patrols.curio.rate_thresholds`
overlaid on `curio.DefaultRateThresholds()`. So a threshold tune is now a
**config CR**, not a source CR — smaller blast radius, no rebuild, trivially
revertible. The polecat edits JSON, not Go. (New *rules*, however, are still
compiled in — those remain a source change and stay on the human-author path.)

**Why new rules don't auto-generate code:** the P2 doc already rejected
auto-applying new rules (a new rule has no fixture to prove it works; it can
flood candidates). P3 goes further: the polecat doesn't even write the Go — it
files a *sketch bead* a human turns into code. This is strictly more
conservative and matches "agent PROPOSES, human DISPOSES."

### Q4 — The precision gate

**Decision: default conservative — the polecat PROPOSES; the Refinery + human
DISPOSE. A narrow auto-merge path exists for threshold tunes only, gated by the
existing replay harness as a CI check, NOT by daemon trust.**

P2 allowed auto-apply of a threshold tune when: precision < 0.80 AND the new
threshold eliminates ≥80% of measured FPs AND replay grades A AND no anchor
regression. P3 keeps the *criteria* but relocates *where they're checked*:

- The polecat, when it opens a threshold-tune CR, includes in the CR
  description the precision data and the replay grade (it runs `go test
  ./internal/curio/...` in its worktree, which exercises the replay harness).
- A CI check on the CR re-runs the replay harness independently. **Merge is
  blocked unless replay grades A** — same regression gate as P2, now enforced
  by the merge queue instead of a pre-apply binary check.
- **Auto-merge eligibility** for a threshold-tune CR (no human approval
  required) is the conjunction P2 specified, evaluated as a Refinery merge
  policy:
  1. CR touches ONLY `daemon.json` `rate_thresholds` keys (no source, no
     fixtures-as-anchors deletion).
  2. The CR body asserts measured precision < 0.80 for the tuned series.
  3. Replay CI grades A (all 5 anchors fire, normal-window volume ≤ prior).
- **Everything else requires human approval:** new rules, retirements, any
  source change, any fixture deletion, any threshold *loosening* that would
  drop below an anchor's expectation (caught by replay failing).

**Default posture for the first epic: even threshold auto-merge is OFF.**
Ship the lane with the polecat opening CRs that ALL require human approval.
Turn on the threshold auto-merge policy only after observing several cycles of
human-reviewed tunes that the human would have approved anyway (mirrors the
Phase-2 shadow→live paging discipline, and the candidates-only discipline that
kept Phase 1 safe). This is captured as a separate, later child bead.

### Q5 — Self-reference air-gap enforcement

**Decision: defense in depth — three independent layers, two mechanical and
one prompt-level.**

The risk: Curio proposes rules about Curio's own activity, or reasons over
candidates that its own past proposals generated (a proposal-about-proposals
loop). Enforcement:

1. **Substrate filter (mechanical, primary).** The `--emit-digest` renderer
   excludes, before the agent ever sees them:
   - candidates with `rule_id` prefixed `proposed_` (pending/prior proposals),
   - candidates whose series has `CurioSeriesPrefix` (Curio's own telemetry —
     already air-gapped on the live side by `rateSpikeRule.Eval`),
   - candidates whose causal root ∈ the Curio-filed bead set (the loop-breaker's
     causal half, `Input.CurioBeads`).
   This reuses the EXACT predicates the live `suppressed()` path uses, so the
   air-gap definition is single-sourced, not re-implemented.
2. **Proposal-target guard (mechanical, secondary).** The polecat's formula
   instructs it, and a CR CI check enforces, that a proposed rule/threshold may
   NOT target a Curio-owned series (`curio.*`) or reference Curio's own beads.
   A CR violating this fails CI.
3. **Prompt scoping (agent-level, tertiary).** The formula prompt explicitly
   states the air-gap: "You are reasoning about Gas Town's failure surface, NOT
   about Curio itself. Ignore any cluster attributable to the `curio` actor;
   never propose a rule that detects Curio's own activity."

Layer 1 means the self-referential data never reaches the agent, so layers 2-3
are backstops, not the primary defense. This matches P2's "proposals about
curio rules are filtered from the LLM's input."

### Q6 — Replay harness as regression gate

**Decision: the existing replay harness (`internal/curio/testdata/replay/`)
becomes a required CI check on every CR the polecat opens. Reuse, don't
rebuild.**

The harness already grades recall (anchors must fire) and a precision proxy
(bounded candidate volume on normal windows) via `curio.Grade`. The replay test
(`replay_test.go`) runs under `go test ./internal/curio/...`, which the Refinery
already runs on every CR.

Concretely:

- **Threshold-tune CR:** Because thresholds are now config (gc-e2uvyr.3), the
  replay test must grade the rule set *with the proposed overlay applied*. This
  needs a small addition: a replay test mode that loads
  `rate_thresholds` from the CR's `daemon.json` and grades
  `DefaultRulesWithThresholds(overlay)`. (Child bead: "replay harness reads
  config overlay.") Without it, replay grades only the compiled defaults and
  would miss a config-only regression.
- **New-rule CR (human-authored from the sketch bead):** the human adds a
  replay fixture (anchor + expected rule) as part of authoring. The P2 open
  question "can the LLM generate a fixture?" is resolved here: the *sketch bead*
  includes a suggested fixture, the human finalizes it. Replay then grades the
  new rule like any other.
- **Merge is blocked on replay grade < A.** This is invariant 4, enforced by CI.

### Q7 — Scope / cost guardrails

**Decision: bound the lane at every level — per-run proposal cap, dedup against
open proposals, and a circuit breaker on CR volume.**

1. **Max proposals per run.** The formula instructs the polecat to emit at most
   **N=3** proposals per run (configurable via `--var max_proposals`), ranked by
   precision impact. A noisy window cannot spawn a flood. (P2's "budget cap" of
   one LLM call is replaced by "one polecat run, bounded output.")
2. **Dedup against open proposals.** Before opening a CR/bead, the polecat
   queries open `label=curio-proposal` / `label=curio-hypothesis` beads and
   skips any cluster already covered. The formula step does this with `bd list
   --label curio-proposal --status open`. Prevents re-proposing the same tune
   every night until the first one merges.
3. **Circuit breaker on CR volume.** The dispatch plugin's single-instance
   guard (Q1) already prevents overlapping runs. Additionally: if the count of
   open `curio-proposal` beads exceeds a ceiling (e.g. 10), the plugin records
   `result:skipped` and does NOT dispatch — the backlog must be worked down
   first. Stops an unreviewed backlog from growing unboundedly.
4. **Token/cost.** One nightly polecat run reading a bounded digest is
   comparable to any other polecat task — no special budget machinery needed.
   The digest is size-bounded by the closed-window candidate count (already
   small; normal windows produce ≤2 candidates per the replay precision proxy).

---

## Go / No-Go recommendation

**GO — implement the Retrospect lane as a Claude-agent polecat. Do NOT finish
the embedded `curio-proposer` builds 4-6/6.**

### Why agent-as-polecat wins

| Axis | Embedded `curio-proposer` (P2 builds 4-6) | Agent-as-polecat (P3) | Winner |
|------|-------------------------------------------|------------------------|--------|
| **Net-new trusted code** | LLM SDK client, prompt assembly, structured-output validator, patch generator, `gt curio apply` write path | One read-only digest renderer + one `ReadOutcomeHistory` SELECT + one dispatch plugin + one formula | **P3** (an order of magnitude less, and none of it write-capable) |
| **Credential surface** | An LLM API credential lives in / is reachable by a daemon-adjacent binary | The polecat's existing Claude session; no new credential in any long-running process | **P3** |
| **Write-path safety** | New `gt curio apply` mutates source + pushes; bespoke gate | The polecat opens a CR; the Refinery merge queue — already trusted, already gating all town code — is the gate | **P3** |
| **Reviewability** | Proposals are `.runtime/*.json` files outside version control; apply is automated | Every proposal is a branch + CR + bead, reviewed in the normal flow, fully in Dolt history | **P3** |
| **Blast radius of a bad proposal** | Patch generator + auto-apply could push a bad source change | Worst case is an open CR a human rejects; nothing merges without replay-A + (for structural changes) human approval | **P3** |
| **Determinism per run** | Deterministic JSON in/out, fixture-testable end-to-end | Agent output varies run to run; only the *digest input* and the *replay gate* are deterministic | **P2** |
| **Latency per run** | Single bounded LLM call (seconds) | Full polecat dispatch + session (minutes) | **P2** |
| **Cost per run** | One 8K/4K LLM call | One polecat session (higher, but nightly) | **P2** |

### Why the P2 advantages don't matter for THIS lane

- **Determinism:** Retrospect is a *proposal* generator whose output is gated by
  replay (deterministic) and human review. We do not need the *proposal step*
  to be deterministic — we need the *gate* to be, and it is. The P2 design
  itself put structured-output validation + replay grading downstream precisely
  because the LLM step was never the trusted one.
- **Latency:** a nightly lane has no latency budget. Minutes vs. seconds is
  irrelevant when the cadence is 24h.
- **Cost:** one polecat session per night is negligible against town-wide
  polecat throughput, and far cheaper than the engineering + ongoing
  maintenance cost of a bespoke in-daemon LLM client + patch generator.

### What we give up, honestly

- A fully fixture-testable end-to-end Layer 2. *Mitigation:* the parts that
  must be deterministic (digest rendering, replay gate, air-gap filter) ARE
  unit/fixture tested; only the agent's judgment is non-deterministic, and that
  is exactly what human/Refinery review is for.
- A single self-contained binary. *Mitigation:* the substrate stays a binary;
  the LLM moves to where LLMs already live in Gas Town (polecats). This is more
  consistent with the system, not less.

### Residual risks & mitigations

- **Agent proposes plausible-but-wrong tunes.** → Replay-A gate + human review;
  threshold auto-merge stays OFF until trust is established.
- **Self-reference loop.** → Three-layer air-gap (Q5), primary layer mechanical.
- **CR flood.** → Per-run cap, dedup, volume circuit breaker (Q7).
- **Lane silently stops (plugin uninstalled / cron broken).** → Graceful
  degradation by design: rules freeze, Patrol keeps running. A `digest=true`
  plugin receipt + `notify_on_failure` surfaces a dispatch failure; absence of
  receipts is itself observable in the daemon digest.

## Child-bead breakdown (the next epic)

The implementation epic is enumerated in the companion file
[`child-beads.md`](./child-beads.md). Summary (7 beads, suggested order):

1. **Substrate: `ReadOutcomeHistory` + `--emit-digest`** — read-only ledger
   query + deterministic digest renderer in `curio-proposer`. Tested;
   `TestImportGraph_NoWritePath` still passes.
2. **Substrate: air-gap filter in the digest** — single-sourced self-reference
   exclusion (Q5 layer 1), reusing live `suppressed()` predicates.
3. **Replay harness reads config overlay** — grade `rate_thresholds` from a
   `daemon.json` overlay so config-only threshold CRs are gated (Q6).
4. **Retrospect formula** — the polecat's instructions: read digest, dedup,
   emit ≤N proposals as CR (threshold) / bead (new-rule sketch, hypothesis),
   air-gap prompt scoping (Q3, Q5, Q7).
5. **Dispatch plugin `curio-retrospect-dispatch`** — cron-gated `run.sh` +
   `plugin.md`, kill-switch pre-check, single-instance + volume circuit-breaker
   guards (Q1, Q7).
6. **Proposal taxonomy landing + dedup** — bead labels (`curio-proposal`,
   `curio-hypothesis`), the CR-vs-bead routing, dedup query (Q3, Q7).
7. **Precision-gate auto-merge policy (default OFF)** — Refinery merge policy
   for threshold-only CRs meeting the P2 conjunction; ships disabled, enabled
   later after observation (Q4).

## Sources

- `.designs/curio-p2-retrospective/design-doc.md` — the Phase-2 design this
  refines (gu-fcwx8.1) — accessed 2026-06-12
- `cmd/curio-proposer/{main.go,config.go}` — write-incapable read substrate — accessed 2026-06-12
- `internal/curio/read.go` — `Reader` / `ReadCandidatesBefore` read-only API — accessed 2026-06-12
- `internal/curio/store.go` — `curio_candidate` / `curio_ledger` schema — accessed 2026-06-12
- `internal/curio/rules.go` — `rateThresholds`, `DefaultRulesWithThresholds`, air-gap predicates — accessed 2026-06-12
- `internal/curio/replay.go` — replay harness `Grade` / fixtures — accessed 2026-06-12
- `internal/daemon/curio_dog.go` — `CurioConfig`, `rate_thresholds` overlay (gc-e2uvyr.3) — accessed 2026-06-12
- `plugins/ci-watcher-poll/plugin.md`, `plugins/casc-patrol-dispatch/{plugin.md,run.sh}`, `plugins/wiki-patrol-dispatch/plugin.md` — cron-gated dispatch-plugin pattern — accessed 2026-06-12
- `internal/formula/formulas/mol-dog-doctor.formula.toml` — formula structure exemplar — accessed 2026-06-12

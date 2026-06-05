# Design: Curio Phase 2 — Retrospective Layer (Autonomous Improvement)

> Design document for gu-fcwx8.1 (child of the Curio epic gu-fcwx8).
> Covers the mechanism by which Curio IMPROVES ITSELF over time: learning
> from outcomes, tuning thresholds, retiring false-positive rules, and
> proposing new detection patterns — all with safety invariants that
> prevent runaway self-mutation.

## Executive Summary

Phase 1 proved Curio can detect known failure classes via deterministic
content rules. Phase 2's retrospective layer closes the loop: it watches
whether candidates MATTERED (were they actioned? did they turn out
false?), uses that outcome data to propose rule improvements, and
applies changes through a gated, auditable self-mutation pipeline. The
result is a patrol that gets BETTER with each incident — without human
rule-authoring.

The design has three layers, each with its own safety gate:

1. **Outcome Tracking** — connects candidate fingerprints to bead
   resolution outcomes (fixed, false-positive, duplicate, deferred) via
   the already-provisioned `curio_ledger` table. Precision per rule is
   measurable at any point.

2. **LLM Hypothesizer** (the Retrospect lane, builds 4-5/6) — reads the
   closed-window candidates + outcome history and proposes: (a) root-cause
   hypotheses for unresolved clusters, (b) threshold adjustments for
   rules with measured false-positive rates above target, (c) NEW rule
   sketches for recurring patterns the existing rules miss.

3. **Self-Mutation Pipeline** (build 6/6) — applies LLM proposals through
   a staged gate: shadow-propose → human-approve OR precision-gate-pass →
   commit to rules. The mutation capability is isolated to a single binary
   (`curio-proposer`) that operates on a FORK of the rule set, never live
   rules directly.

**Key invariants maintained throughout:**

- Curio can never file beads about its own activity (Call 1A air-gap)
- Curio can never modify live patrol rules without passing the gate
- Every mutation is auditable (committed to Dolt history + the ledger)
- Kill switches are independent (patrol vs. LLM vs. self-mutation)
- The system degrades gracefully: LLM down = rules frozen, not broken

## Problem Statement

### What Phase 1 cannot do

Phase 1's content rules are **static**: thresholds were set from a
one-time baseline measurement, and new failure classes require a human to
write a rule, add a fixture, and deploy. This works when the set of
known failures is small and stable, but Gas Town's failure surface
evolves continuously:

1. **New failure classes emerge.** The gu-zadrb stuck-nuke class, the
   gu-hz3vx push-gate-loop class, and the gc-wisp-gw40 worktree-reuse
   orphan class were all discovered manually AFTER causing production
   incidents. Curio saw the symptoms (rate spikes, kill signals) but
   could not name them.

2. **Thresholds drift.** The rate thresholds in `rateThresholds` are
   daily values from a single measurement point. As Gas Town scales
   (more rigs, more polecats), normal-traffic ceilings will rise and
   the static thresholds will either false-positive (too low) or
   miss-detect (too high).

3. **Rule precision degrades.** Without outcome feedback, a rule that
   fires on every patrol cycle with false positives accrues candidate
   rows forever. Phase 1's `curio_ledger` table was provisioned for
   exactly this — but nothing populates it yet.

### What the retrospective layer provides

The ability to:
- **Measure precision** per rule from real outcome data
- **Auto-tune thresholds** when measured precision drops below target
- **Propose new rules** from clustered unresolved candidates
- **Retire or disable** rules whose precision stays below threshold
- **Explain findings** via LLM-generated root-cause hypotheses

All without human rule-authoring for routine maintenance.

## Design

### Layer 1: Outcome Tracking

#### Schema (already provisioned in store.go)

```sql
CREATE TABLE curio_ledger (
  bead_id varchar(255) NOT NULL,
  fingerprint varchar(12) NOT NULL DEFAULT '',
  rule_id varchar(255) NOT NULL DEFAULT '',
  filed_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  outcome varchar(32) NOT NULL DEFAULT '',
  resolved_at datetime,
  PRIMARY KEY (bead_id),
  KEY idx_curio_ledger_outcome (outcome)
);
```

**Outcome values:** `fixed`, `false_positive`, `duplicate`, `deferred`

- `fixed` — the finding led to a real fix (bead closed with a merge commit)
- `false_positive` — the finding was wrong (bead closed with reason containing "false" or manual FP tag)
- `duplicate` — finding was a dup of a known issue (close reason "duplicate")
- `deferred` — finding was real but not actionable now (close reason "deferred")

#### Population flow

```
Curio files bead (build 6/6)
  → ledger row created: (bead_id, fingerprint, rule_id, filed_at, outcome='')
      ↓
Bead is closed by any actor (human, refinery, polecat)
  → daemon post-close hook writes: (outcome, resolved_at)
```

The daemon's existing bead-close event stream (used by the refinery
post-merge hook) is extended with a curio-ledger reconciler: on any
bead close, if `bead_id` exists in `curio_ledger`, update the outcome
from the bead's close reason.

#### Precision computation

```
precision(rule_id, window) =
  1 - (count(outcome='false_positive') / count(outcome != ''))
```

A rule with < 5 resolved outcomes has insufficient data — precision is
`unknown` (the rule is young and cannot yet be judged). The target
precision threshold is **0.80** (4 out of 5 filings should be real).

### Layer 2: LLM Hypothesizer (Retrospect lane)

The `curio-proposer` binary (already a read-only skeleton at build 3/6)
gains a hypothesizer stage that reads the closed-window candidates +
outcome history and produces structured proposals.

#### Input to the LLM

```json
{
  "candidates": [/* closed-window candidates, newest first */],
  "outcome_history": {
    "rule_id": {
      "total_resolved": 42,
      "precision": 0.85,
      "recent_false_positives": [/* last 5 FP summaries */]
    }
  },
  "unresolved_clusters": [
    {
      "state_hash": "abc123",
      "rule_id": "alarm_rate_spike",
      "occurrences": 7,
      "summaries": ["series X rate 450 > 300", ...]
    }
  ]
}
```

#### LLM output schema (structured output)

```json
{
  "hypotheses": [
    {
      "cluster_key": "abc123",
      "root_cause": "The sling rate ceiling of 300 was baselined before the 4-rig expansion...",
      "confidence": "medium",
      "evidence": ["7 occurrences in 3 days, all in sling series", "normal daily max now 380"]
    }
  ],
  "threshold_adjustments": [
    {
      "rule_id": "alarm_rate_spike",
      "series": "sling",
      "current_threshold": 300,
      "proposed_threshold": 450,
      "rationale": "Measured daily max over last 7d is 380; 450 = ceil(380 * 1.2) for headroom",
      "precision_before": 0.65,
      "projected_precision": 0.90
    }
  ],
  "new_rule_proposals": [
    {
      "id": "proposed_stuck_nuke_gate",
      "description": "Detect a polecat whose gt-done push has been retrying for >20min",
      "signal": "A worktree with gt-done invoked + pre-push gate running >20min",
      "classification": "deterministic",
      "anchor_incidents": ["gu-zadrb", "gu-hz3vx"]
    }
  ],
  "retirement_candidates": [
    {
      "rule_id": "kill_signal_near_dolt",
      "precision": 0.45,
      "window_resolved": 20,
      "rationale": "Dolt kill signals are normal during planned restarts; rule fires on every restart"
    }
  ]
}
```

#### Safety constraints on the LLM

1. **Read-only invocation.** The proposer binary's import graph physically
   excludes `internal/beads` and `internal/daemon` (tested invariant,
   already enforced). The LLM output is a JSON proposal file written to
   `.runtime/curio-proposals/<timestamp>.json` — it cannot execute.

2. **Closed-window only.** The LLM never sees in-flight candidates (the
   30-minute cursor margin enforces this mechanically).

3. **No self-referential input.** Candidates whose `rule_id` starts with
   `proposed_` (pending proposals from prior runs) are filtered OUT of
   the LLM's input, preventing proposal-about-proposals feedback loops.

4. **Structured output enforcement.** The LLM is constrained to the JSON
   schema above; free-text reasoning goes in `rationale` fields only.
   Hallucinated rule_ids or unknown series names are rejected at
   validation.

5. **Budget cap.** A single Retrospect run is capped at one LLM call
   (the closed-window summary is bounded; no iterative refinement in
   this build). Token budget: 8K input, 4K output max.

### Layer 3: Self-Mutation Pipeline

#### Mutation types

| Type | What changes | Gate |
|------|-------------|------|
| Threshold adjustment | `rateThresholds` map value | Precision-gate (auto) |
| Rule retirement | Rule removed from `DefaultRules()` | Human-approve |
| New rule addition | New `Rule` impl + fixture | Human-approve |
| Hypothesis annotation | `Hypothesis` field on candidate | None (informational) |

#### The precision gate (auto-approval path)

A threshold adjustment proposal can be auto-applied WITHOUT human
approval if ALL of the following hold:

1. The rule's measured precision is below 0.80 (documented regression)
2. The proposed threshold would eliminate >= 80% of the measured FPs
3. The proposed threshold does not drop below the rule's ANCHOR
   expectations (i.e., all replay fixtures still pass with the new value)
4. The replay harness grades A on the modified rule set (no anchor
   regressions, normal-window volume ≤ previous)

This is the "autonomous improvement" path: the system measures a
precision problem, proposes a fix, proves the fix doesn't regress
anchors, and applies it — all without a human.

#### The human-approve path

New rules and rule retirements are NEVER auto-applied. They produce a
structured proposal that is filed as a bead (type=task, label=curio-proposal)
assigned to the mayor for review. The proposal bead contains:

- The full proposal JSON
- The replay grade showing anchor coverage is maintained
- The precision data justifying the change

A human (or the mayor agent) reviews and either approves (the proposer
applies the change in a follow-up run) or rejects (the proposal is
closed as `false_positive` — which feeds back into the ledger).

#### Mutation mechanics

Changes are applied to the rule set via **code generation**, not runtime
config:

1. Proposer writes a patch file to `.runtime/curio-proposals/approved/<id>.patch`
2. A dedicated `curio-apply` command (new subcommand of `gt`) reads
   approved patches, applies them to `internal/curio/rules.go`, runs
   the replay harness, and if green, commits + pushes to a branch
3. The branch enters the normal merge queue (Refinery gates it)

This means rule mutations follow the EXACT same merge/gate/review path
as any other code change. The self-mutation is not special — it's just
an automated PR.

#### Code generation templates

**Threshold adjustment** (auto path):
```go
// Generated by curio-proposer at <timestamp>
// Reason: precision 0.65 < target 0.80; measured daily max 380
// Replay grade: A (all anchors pass, normal volume unchanged)
var rateThresholds = map[string]int{
    ...
    "sling": 450,  // was: 300 (auto-tuned from outcome data)
    ...
}
```

**New rule** (human-approve path):
```go
// Generated by curio-proposer at <timestamp>
// Anchor incidents: gu-zadrb, gu-hz3vx
// Approved by: mayor (bead gu-XXXXX)
type stuckNukeGateRule struct{}
func (stuckNukeGateRule) ID() string { return "stuck_nuke_gate" }
func (r stuckNukeGateRule) Eval(in Input) []Candidate { ... }
```

### Kill-Switch Architecture

```
mayor/daemon.json:
{
  "patrols": {
    "curio": {
      "enabled": true,           // Kill switch 1: live Patrol
      "page_for_real": false,    // Kill switch 2: paging
      "llm": {
        "enabled": false         // Kill switch 3: Retrospect/LLM
      },
      "self_mutation": {
        "enabled": false,        // Kill switch 4: auto-apply
        "auto_threshold": true,  // Sub-gate: threshold auto-tuning
        "propose_rules": true    // Sub-gate: new rule proposals
      }
    }
  }
}
```

Each switch is INDEPENDENT: disabling `self_mutation` does not stop
Retrospect from producing proposals (they just accumulate unacted-on).
Disabling `llm` does not stop the live Patrol. Disabling `enabled` stops
everything (the Patrol doesn't run, so no candidates are produced).

### Data Flow Summary

```
┌────────────────────────────────────────────────────────────────────┐
│                        LIVE (daemon, 15m cycle)                     │
├────────────────────────────────────────────────────────────────────┤
│  Collect → Rules → Candidates → ReactionTracker → PagingEngine     │
│              │                        │                    │        │
│              │                        │                    ▼        │
│              │                        │          curio_shadow_page  │
│              │                        ▼                             │
│              │              curio_candidate table                   │
│              │                                                      │
│              │  ┌──── bead close event ────┐                       │
│              │  ▼                          │                        │
│              │  curio_ledger (outcome)     │                        │
└──────────────┼────────────────────────────┼────────────────────────┘
               │                            │
┌──────────────┼────────────────────────────┼────────────────────────┐
│              │    OFFLINE (curio-proposer, nightly)                 │
├──────────────┼────────────────────────────┼────────────────────────┤
│              ▼                            ▼                         │
│    DefaultRules()              ReadCandidatesBefore(cutoff)         │
│         │                              │                           │
│         │                              ▼                           │
│         │                     LLM Hypothesizer                     │
│         │                              │                           │
│         │                              ▼                           │
│         │                     Proposal JSON                        │
│         │                              │                           │
│         ▼                              ▼                           │
│    Replay Harness ◄──── Precision Gate (auto path)                 │
│         │                              │                           │
│         │              ┌───────────────┼───────────────┐           │
│         │              ▼               ▼               ▼           │
│         │       Threshold adj    New rule         Retirement       │
│         │       (auto-apply)    (human gate)    (human gate)       │
│         │              │               │               │           │
│         ▼              ▼               ▼               ▼           │
│    Grade report   Code patch      Proposal bead   Proposal bead   │
│                        │                                           │
│                        ▼                                           │
│              curio-apply → branch → merge queue → main             │
└────────────────────────────────────────────────────────────────────┘
```

### Safety Invariants (enumerated)

1. **Air-gap (Call 1A):** Curio's own activity cannot trigger itself.
   The loop-breaker suppresses records with `FiledBy == "curio"` AND
   records whose `CausalRoot` is in `CurioBeads`. The retrospective
   layer extends this: proposals ABOUT curio rules are filtered from
   the LLM's input (no proposal-about-proposals).

2. **Write-incapability of the read path:** `curio-proposer`'s import
   graph excludes `internal/beads` and `internal/daemon`. It cannot file
   beads, mutate candidates, or touch the live patrol state. Tested by
   `TestImportGraph_NoWritePath`.

3. **Closed-window cursor:** Retrospect never observes in-flight
   candidates. The 30-minute margin is mechanically enforced.

4. **Replay-graded mutations:** No threshold change is applied unless
   the replay harness grades A (all anchors still fire, normal volume
   doesn't increase). This is the regression gate.

5. **Human gate on structural changes:** New rules and retirements
   require explicit human (or mayor-agent) approval. Only threshold
   adjustments may auto-apply, and only when precision data justifies it.

6. **Kill-switch isolation:** Four independent switches. Any one can be
   off without affecting the others. The system degrades to "rules
   frozen, patrol still running" in the most conservative mode.

7. **Audit trail:** Every proposal, every auto-application, every
   rejection is recorded in Dolt history (the ledger, the proposal
   table, and git commits). The trail is append-only and queryable.

8. **No runtime rule mutation:** Rules are COMPILED IN. The self-mutation
   pipeline produces code patches that go through the normal merge queue.
   There is no runtime rule registry that could be corrupted by a bad
   LLM output.

## Build Plan

| Build | Scope | Gate | Risk |
|-------|-------|------|------|
| 4/6 | LLM hypothesizer: curio-proposer reads candidates + ledger, calls LLM, writes proposal JSON | Kill-switch gated, read-only binary | Low: no mutation, no side effects |
| 5/6 | Precision gate + auto-threshold: proposer validates thresholds via replay, writes patches | Replay must grade A; no code applied yet | Medium: patch generation could be wrong |
| 6/6 | `curio-apply` + human-gate beads: patches enter merge queue, structural proposals file beads | Merge queue gates (Refinery runs tests) | Medium: automated PR, but gated normally |

### Build 4/6 — LLM Hypothesizer (acceptance)

**Scope:** Wire the LLM call into `curio-proposer`. The proposer:
1. Reads closed-window candidates (already works)
2. Reads outcome history from `curio_ledger` (new: add `ReadOutcomeHistory()` to `Reader`)
3. Assembles the LLM prompt from the structured input
4. Calls the LLM (provider-agnostic; initially Claude via the Anthropic SDK)
5. Validates the structured JSON output against the schema
6. Writes the validated proposal to `.runtime/curio-proposals/<ts>.json`

**Does NOT:** Apply anything. File beads. Touch live state.

**Test plan:**
- Unit: mock LLM returns valid/invalid JSON; validate parsing + rejection
- Unit: prompt assembly from fixture candidates + mock history
- Integration: `TestImportGraph_NoWritePath` still passes (no new write deps)
- Replay: proposer run against test fixtures produces plausible proposals

### Build 5/6 — Precision Gate + Auto-Threshold

**Scope:** The proposer gains a `--apply-thresholds` mode that:
1. Reads approved threshold proposals from prior runs
2. Validates each against the precision gate (precision < 0.80 + eliminates FPs)
3. Generates the code patch (modified `rateThresholds` in rules.go)
4. Runs the replay harness on the patched rule set
5. If replay grades A: writes the patch to `.runtime/curio-proposals/approved/`
6. If replay fails: logs rejection and writes to `.runtime/curio-proposals/rejected/`

**Does NOT:** Apply the patch to the working tree. That's `curio-apply` (build 6).

**Test plan:**
- Unit: precision gate logic (various precision + FP elimination scenarios)
- Unit: code generation produces valid Go (compile the output)
- Integration: full proposer run with threshold adjustment (fixture-based)
- Replay: modified thresholds don't regress anchors

### Build 6/6 — `curio-apply` + Human-Gate Beads

**Scope:**
1. `gt curio apply` subcommand reads approved patches, applies to source, runs replay, commits, pushes
2. Structural proposals (new rules, retirements) file a bead (type=task, label=curio-proposal) for human review
3. Approved structural proposals get a patch generated + applied via the same mechanism

**Test plan:**
- Integration: `curio-apply` on a test worktree produces a valid commit
- Integration: structural proposal files a bead correctly
- E2E: full loop (candidate → ledger → proposal → apply → merge) in test environment

## Alternatives Considered

### Runtime rule registry (rejected)

Load rules from a config file at daemon startup instead of compiling
them in. This was rejected because:
- A corrupted config silently breaks detection with no CI gate
- The replay harness couldn't grade config-defined rules
- Config drift between environments would be invisible
- Code-as-rules means every change goes through the merge queue

### LLM-in-the-loop for EVERY finding (rejected)

Have the LLM classify each candidate in real-time during the patrol
cycle. This was rejected because:
- Latency: LLM calls are 2-10s; patrol cycle target is < 1s
- Availability: LLM downtime would stop detection
- Cost: 96 cycles/day × N candidates × LLM cost is prohibitive
- The closed-window / offline model is explicitly safer

### Auto-apply new rules without human gate (rejected)

Let the LLM propose AND apply new rules autonomously. This was rejected
because:
- A new rule can fire on every cycle, flooding candidates
- The replay harness can only catch regressions on EXISTING fixtures
- A genuinely new rule has no fixture to prove it works
- Human review of new detection logic is a reasonable cost

### Precision gate at 0.95 (rejected)

A tighter target would reject more threshold adjustments. 0.80 was
chosen because:
- Early data is sparse; requiring 95% with < 20 data points is brittle
- The goal is to eliminate OBVIOUS false-positive-generating thresholds
- The human gate on structural changes provides the high-bar backstop
- The target can be raised later as data accumulates

## Open Questions

1. **LLM provider choice.** The design is provider-agnostic (structured
   JSON input/output). Initial implementation targets Claude (via
   Anthropic SDK) since the host already has credentials. Should we
   support a fallback provider?

2. **Proposal expiry.** Should unapplied proposals expire after N days?
   Current design: proposals accumulate indefinitely; the operator can
   purge `.runtime/curio-proposals/` manually.

3. **Precision target per-rule.** Should high-stakes rules (like
   dead_owner_admission, which can reap capacity) have a higher precision
   target than informational ones (like rate_spike)?

4. **Anchor fixture generation.** When the LLM proposes a new rule, can
   it also generate a replay fixture? This would allow the replay
   harness to grade the new rule. Deferred to build 6 implementation.

## Implementation Dependencies

- gu-fcwx8.2 (extract `internal/liveness.PIDAlive` shared leaf) — no
  direct dependency, but the liveness package is used by the verified
  lane and should be stable before the retrospective layer exercises it
- gu-fcwx8.3 (L1 EWMA/MAD detector) — independent; the retrospective
  layer will tune its thresholds once it exists, but does not require it
- gu-fcwx8.4 (Patrol hardening Calls 1-3) — should land first so the
  paging/candidate path the retrospective layer reads is stable

## Success Criteria

1. **Measurable precision:** After 2 weeks of live filing (build 6),
   every rule's precision is queryable via `SELECT rule_id, precision
   FROM curio_precision_view`.

2. **Auto-tuning fires:** At least one threshold adjustment is
   auto-applied via the precision gate within the first month of
   operation, with replay grade A and no regression.

3. **Hypothesis quality:** LLM-generated hypotheses for unresolved
   clusters are reviewed by the mayor and rated as "useful" (provides
   actionable root-cause direction) in >= 60% of cases.

4. **No regressions:** The replay harness never drops below its current
   grade (all 5 anchors fire, normal-window volume ≤ 2).

5. **Kill-switch works:** Disabling any one switch in daemon.json takes
   effect within one patrol cycle with no side effects.

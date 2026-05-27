# Design: A Gas Town Plugin and Formula System for Automated Code Quality Analysis

## Executive Summary

This design proposes a **new plugin** (`code-quality`) and a **new convoy formula**
(`code-quality.formula.toml`) that together enable periodic, autonomous code quality
analysis across Gas Town rigs. The system composes two already-proven substrates:
plugins handle scheduling and gating (when to analyze), while the convoy formula
handles parallel execution (what to analyze and how to report). No new runtime
infrastructure, storage technology, or top-level CLI commands are required.

The plugin uses a `condition` gate with embedded cooldown logic (checking both
elapsed time and new commits since last run). When the gate opens, the Deacon
dispatches to a Dog that slings the convoy formula. The formula spawns up to 7
parallel analysis legs (dead-code, dependency-health, architecture-drift,
test-quality, complexity, documentation, tech-debt-trend), each executed by an
independent polecat. A synthesis step combines findings into a unified quality
report committed to `.quality/<date>/quality-report.md`. A structured
`summary.json` alongside enables trending. Only critical findings create beads;
all other findings remain in the report.

The system operates with a `max_parallelism` cap of 3 polecat slots to avoid
starving feature work, and uses presets (quick/standard/full) to control scope.
At daily cadence on a single rig, the estimated resource cost is 15-45
polecat-minutes per run — roughly 1-5% of fleet capacity.

## Problem Statement

Gas Town rigs accumulate technical debt, dead code, outdated dependencies, and
architectural drift over time. Currently, quality is only assessed reactively
(code-review at PR time) or manually. There is no systematic, periodic,
whole-codebase quality audit that tracks trends and detects regressions before
they compound.

**We need a system that:**
1. Runs automatically (no human trigger required)
2. Covers multiple quality dimensions in parallel
3. Produces actionable reports with severity classification
4. Tracks quality trends over time (is the codebase getting better or worse?)
5. Escalates critical findings for immediate attention
6. Operates within existing infrastructure (plugins, formulas, beads, Dolt)
7. Does not starve feature work of polecat slots

## Proposed Design

### Overview

```
┌──────────────────────────────────────────────────────────────────┐
│                       TRIGGER SIDE                                │
│                                                                   │
│  Deacon Patrol ──► Plugin Scanner ──► Gate Evaluation            │
│       │                                    │                     │
│       │            code-quality/           │ condition:           │
│       │            plugin.md              │ ≥6h since last +     │
│       │                                    │ new commits exist    │
│       │                                    │                     │
│       ▼            Gate OPEN ◄────────────┘                      │
│  FormatMailBody() ──► Dog Worker                                 │
│                          │                                       │
│                          ▼ slings convoy formula                 │
└──────────────────────────────────────────────────────────────────┘
                           │
                           ▼
┌──────────────────────────────────────────────────────────────────┐
│                     EXECUTION SIDE                                │
│                                                                   │
│  code-quality.formula.toml (convoy, max_parallelism=3)           │
│                                                                   │
│  ┌─────────┐ ┌─────────┐ ┌─────────┐    (batch 1: 3 parallel)  │
│  │dead-code│ │dep-health│ │ archit. │                            │
│  └────┬────┘ └────┬────┘ └────┬────┘                            │
│       │            │           │                                  │
│  ┌────┴────┐ ┌────┴────┐ ┌────┴────┐    (batch 2: 3 parallel)  │
│  │test-qual│ │complex. │ │  docs   │                            │
│  └────┬────┘ └────┬────┘ └────┬────┘                            │
│       │            │           │                                  │
│  ┌────┴────┐                                 (batch 3: 1 leg)   │
│  │debt-trnd│                                                     │
│  └────┬────┘                                                     │
│       │                                                          │
│       ▼ all legs complete                                        │
│  ┌─────────────────────────────────────┐                         │
│  │         SYNTHESIS STEP              │                         │
│  │  Reads all leg findings             │                         │
│  │  Produces quality-report.md         │                         │
│  │  Produces summary.json              │                         │
│  │  Files critical beads               │                         │
│  │  Records plugin receipt             │                         │
│  └─────────────────────────────────────┘                         │
└──────────────────────────────────────────────────────────────────┘
                           │
                           ▼
┌──────────────────────────────────────────────────────────────────┐
│                      OUTPUT SIDE                                  │
│                                                                   │
│  .quality/2026-05-27/                                            │
│  ├── dead-code.md          ← leg finding (markdown)              │
│  ├── dep-health.md         ← leg finding                         │
│  ├── architecture.md       ← leg finding                         │
│  ├── test-quality.md       ← leg finding                         │
│  ├── complexity.md         ← leg finding                         │
│  ├── documentation.md      ← leg finding                         │
│  ├── debt-trend.md         ← leg finding                         │
│  ├── quality-report.md     ← synthesis (human-readable)          │
│  └── summary.json          ← synthesis (machine-readable)        │
│                                                                   │
│  Dolt: ephemeral receipt bead (labels: score, finding counts)    │
│  Dolt: critical finding beads (only for P0 issues)               │
└──────────────────────────────────────────────────────────────────┘
```

### Key Components

| Component | Location | Role |
|-----------|----------|------|
| Plugin definition | `plugins/code-quality/plugin.md` | Gate configuration, Dog instructions |
| Gate check script | `plugins/code-quality/check.sh` | Condition evaluation (cooldown + new commits) |
| Convoy formula | `internal/formula/formulas/code-quality.formula.toml` | Leg definitions, presets, synthesis |
| Output directory | `.quality/<date>/` | Committed analysis artifacts |
| Receipt beads | Dolt (ephemeral, closed immediately) | Operational tracking, cooldown gate |
| Critical findings | Dolt (open beads, tagged `quality-critical`) | Workflow integration, auto-dispatch |

### Interface

**No new CLI commands.** Interaction flows through existing commands:

```bash
# Discovery
gt plugin list                              # Shows code-quality plugin
gt plugin show code-quality                 # Gate config, execution settings
gt formula show code-quality                # Legs, presets, inputs

# Manual trigger
gt plugin run code-quality --force          # Bypass gate, run now
gt formula run code-quality --preset=full   # Direct formula invocation

# Results
cat .quality/latest/quality-report.md       # Latest report (symlink)
gt plugin history code-quality              # Run history with results
bd list -l quality-critical --status=open   # Critical findings

# Tuning
$EDITOR plugins/code-quality/plugin.md      # Change gate, timeout, etc.
```

**Plugin frontmatter (trigger configuration):**
```toml
name = "code-quality"
description = "Periodic codebase quality analysis via parallel specialized reviewers"
version = 1

[gate]
type = "condition"
check = "./check.sh"

[tracking]
labels = ["plugin:code-quality", "category:quality"]
digest = true

[execution]
timeout = "30m"
notify_on_failure = true
severity = "medium"
```

**Formula presets (scope control):**

| Preset | Legs | Duration | Use Case |
|--------|------|----------|----------|
| `quick` | dead-code, dep-health, architecture | ~5 min | Daily gate-triggered |
| `standard` | + test-quality, complexity | ~10 min | Manual/after big merges |
| `full` | + documentation, debt-trend | ~20 min | Weekly comprehensive |

### Data Model

**Dual-write pattern (hybrid storage):**

1. **Git-committed files** (`.quality/<date>/`): Human-readable leg findings
   in markdown, machine-readable `summary.json` for trending.

2. **Dolt beads** (ephemeral receipts): Labels encode key metrics for
   operational queries (cooldown gate, patrol digest, trend queries).

**summary.json schema (v1):**
```json
{
  "schema_version": 1,
  "run_id": "gu-run-abc",
  "rig": "gastown_upstream",
  "timestamp": "2026-05-27T09:00:00Z",
  "overall_score": 0.78,
  "legs": {
    "dead-code": { "score": 0.85, "findings": { "critical": 0, "major": 1, "minor": 3 } }
  },
  "totals": { "critical": 0, "major": 5, "minor": 12 },
  "delta_since_last": { "overall_score": 0.03, "critical": 0, "major": -1 }
}
```

**Receipt bead labels:**
```
type:plugin-run, plugin:code-quality, result:success,
rig:gastown_upstream, score:0.78, critical:0, major:5, minor:12
```

**Schema evolution strategy:** Append-only. New fields are optional. Readers
ignore unknown fields. `schema_version` bumps only for breaking changes.

## Trade-offs and Decisions

### Decisions Made

1. **Composition over invention.** The system uses existing plugin + formula
   infrastructure rather than building a new "quality engine." This means
   faster delivery, fewer bugs, and natural integration with all existing tools.

2. **Condition gate with embedded cooldown (not a new composite gate type).**
   Embedding cooldown logic in the condition check script avoids infrastructure
   changes to the gate evaluator. The trade-off: the check script is slightly
   complex (but self-contained and readable).

3. **`max_parallelism = 3` cap on convoy legs.** Protects fleet from starvation
   while allowing meaningful parallelism. At 8 polecats per rig, this leaves
   5 slots for feature work during analysis.

4. **Report-only formula (`review_only = true`).** Quality analysis legs produce
   findings, not code fixes. This skips MQ processing for individual legs —
   only the synthesis step commits results.

5. **Only critical findings create beads.** Sub-critical findings stay in the
   report. This prevents bead noise (the existing code-scout cap of 5 beads/
   rig/cycle is the proven threshold for signal-to-noise).

6. **`.quality/<date>/` output pattern (per-rig, date-indexed).** Follows the
   established `.reviews/<id>/` and `.designs/<id>/` conventions. Date-indexed
   rather than ID-indexed because quality runs are periodic, not event-triggered.

7. **No `gt quality` command in v1.** Existing `gt plugin` and `gt formula`
   commands cover all interaction patterns. A convenience wrapper can be added
   later if demand emerges.

### Open Questions (Requiring Human Input)

1. **Retention policy for `.quality/` history.** At ~50KB/run × 365 days =
   ~18MB/year per rig. Options:
   - Keep all (acceptable growth for most repos)
   - Keep last 90 days, prune older
   - Keep last 30 days + summary.json only for older runs
   
   **Recommendation:** Keep all initially; add pruning when it becomes a problem.

2. **Critical finding threshold for bead creation.** Should the synthesis step
   create beads for:
   - Only P0/Critical findings?
   - P0 + P1/Major findings above a count threshold (e.g., >3 new Major)?
   - All findings above a configurable severity floor?
   
   **Recommendation:** Start with Critical-only. Add Major threshold as a
   follow-up if operators want more automated remediation.

3. **Should quality scores influence polecat dispatch priority?** A rig with
   declining quality could have its polecats prioritize quality-related beads.
   This creates a feedback loop. Is that desirable?
   
   **Recommendation:** No in v1. Quality analysis is advisory. Dispatch priority
   should remain based on bead priority labels, not quality scores.

4. **Multi-rig support.** Should one plugin instance analyze multiple rigs, or
   should each rig have its own `code-quality` plugin?
   
   **Recommendation:** Per-rig plugins. The scanner already supports rig-level
   plugins (`<rig>/plugins/`), and per-rig isolation is simpler for gate
   evaluation and output management.

5. **Relationship to code-scout plugin.** Code-scout files improvement beads
   per individual finding; quality analysis is holistic and periodic.
   
   **Recommendation:** Complementary, not competing. Code-scout targets
   incremental improvements on recent commits. Quality analysis targets
   codebase-wide trends. They may overlap on findings but serve different
   automation patterns.

### Trade-offs

| Choice | We gain | We lose |
|--------|---------|---------|
| No new gate type | Zero infrastructure changes | Less elegant gate composition (embedded in script) |
| max_parallelism=3 | Fleet stability, predictable resource use | ~2x longer wall-clock for full analysis |
| Report-only (no auto-fix) | Clean separation of analysis and action | No automated remediation in v1 |
| Beads only for criticals | Low noise in `bd list` | Sub-critical findings less discoverable |
| Per-date output dirs | Clean history, easy `git diff` between runs | Repo size grows linearly with time |
| Condition gate (not cron) | Smart triggering (skips if nothing changed) | Gate failures are harder to debug than cron |

## Risks and Mitigations

### Security Risks

| Risk | Severity | Mitigation |
|------|----------|------------|
| Condition gate shell execution (arbitrary commands) | High | Trusted plugin directory only; integrity monitoring via drift detection |
| Prompt injection via analyzed code | Medium | Analysis prompts include explicit injection resistance; findings machine-validated before bead creation |
| Plugin sync supply chain | Medium | Source is the gastown repo itself (version-controlled, reviewed) |
| Agent confusion (hallucinated findings) | Medium | Synthesis step validates leg outputs; scoring requires structured format |
| No per-agent Dolt ACLs (data plane poisoning) | Low | Receipt beads are ephemeral and closed immediately; audit via `author` labels |

### Scalability Risks

| Risk | Severity | Mitigation |
|------|----------|------------|
| Polecat fleet starvation | High | `max_parallelism = 3`; presets reduce leg count for routine runs |
| Gate evaluation latency at 200+ plugins | Medium | Current: sequential evaluation OK at 20 plugins; future: batch Dolt queries |
| LLM context window limits per-leg scope | Medium | Legs use file-list scoping; full codebase scan uses sampling |
| Dolt commit accumulation from receipts | Low | Ephemeral beads closed immediately; reaper handles cleanup |
| Report directory size growth | Low | ~18MB/year per rig; configurable retention policy |

## Implementation Plan

### Phase 1: MVP (Core Plugin + Formula)

**Scope:** Working quality analysis with 3 legs, condition gate, and synthesis.

1. Create `plugins/code-quality/plugin.md` with condition gate
2. Create `plugins/code-quality/check.sh` gate check script
3. Create `internal/formula/formulas/code-quality.formula.toml` (3 legs: dead-code, dep-health, architecture)
4. Add formula to embed.go for binary embedding
5. Mark formula as `review_only = true`
6. Define output directory convention: `.quality/<date>/`
7. Synthesis step: combine leg outputs + produce `summary.json`
8. Plugin receipt recording with score labels
9. Test: manual trigger via `gt plugin run code-quality --force`

**Deliverables:** Plugin triggers, legs execute, report is committed, receipt recorded.
**Effort:** Low-Medium (mostly config files + formula authoring).

### Phase 2: Full Legs + Trending (Polish)

**Scope:** Expand to 7 legs, add trending support, add presets.

1. Add remaining legs (test-quality, complexity, documentation, debt-trend)
2. Implement presets (quick/standard/full) with leg selection
3. Add `max_parallelism` support to convoy formula dispatch (if not already available)
4. Add trending: synthesis step reads prior `summary.json` and computes deltas
5. Add critical finding escalation (bead creation for P0 findings)
6. Add patrol digest integration (one-liner summary in daily digest)
7. Add `.quality/latest` symlink pointing to most recent run
8. Test: end-to-end automated cycle via Deacon patrol

**Deliverables:** Full-featured quality analysis with trending and escalation.
**Effort:** Medium.

### Phase 3: Incremental Analysis + Optimization (Future)

**Scope:** Performance optimization and advanced features.

1. Implement incremental analysis (`--since` semantics for delta-only legs)
2. Add `gt quality` convenience command (status, trends, findings)
3. Implement capability declarations for plugins ([capabilities] in frontmatter)
4. Add output isolation for analysis polecats (read-only worktree)
5. Batch gate evaluation for large plugin counts (Dolt SQL optimization)
6. Consider composite gate types (cooldown AND condition) as formula feature
7. Cross-rig quality dashboard (if demand emerges)

**Deliverables:** Optimized, hardened, extended quality system.
**Effort:** Medium-High.

## Appendix: Dimension Analyses

| Dimension | File | Key Recommendation |
|-----------|------|-------------------|
| API & Interface | [api.md](api.md) | Minimal surface — zero new commands, use existing `gt plugin`/`gt formula` |
| Data Model | [data.md](data.md) | Hybrid: beads for ops + git-committed summary.json for trending |
| User Experience | [ux.md](ux.md) | Report + escalation-only beads; progressive disclosure via presets |
| Scalability | [scale.md](scale.md) | `max_parallelism=3` + incremental analysis in v2 |
| Security | [security.md](security.md) | Plugin integrity monitoring; capability declarations; output isolation |
| Integration | [integration.md](integration.md) | Pure composition of existing plugin + formula; no new infrastructure |

### Cross-Dimension Conflicts Resolved

1. **API wants zero new code vs Scale wants `max_parallelism`.** Resolution: `max_parallelism`
   is a formula-level field in TOML, not a new command. It lives in the formula definition
   and is read by the existing convoy dispatch logic. If convoy dispatch doesn't support it
   yet, it's a small addition to the formula system — not a new command.

2. **UX wants low bead noise vs Data wants structured bead-per-finding.** Resolution: Only
   critical findings create beads. All findings stored in committed report files.
   `summary.json` provides machine-readable metrics without bead proliferation.

3. **Security wants sandboxing vs Integration wants zero infrastructure changes.** Resolution:
   Layered approach — immediate (integrity monitoring), short-term (capability declarations),
   long-term (sandboxing). v1 ships with monitoring only.

4. **Scale recommends incremental analysis vs API prefers simplicity.** Resolution: Phase 2.
   v1 uses whole-codebase analysis with `max_parallelism` throttling. Incremental adds
   complexity that isn't justified until scaling pressure materializes.

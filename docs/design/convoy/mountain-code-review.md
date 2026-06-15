# Mountain Code Review: Multi-Axis Source Review Formula

> Design spec for `mountain-code-review.formula.toml` — a re-runnable, multi-axis
> code-quality review over the gastown source tree, fanned out as a convoy and
> promoted to a mountain for autonomous rollup.
>
> **Status**: Design
> **Implements**: gu-do47m
> **Formula**: `internal/formula/formulas/mountain-code-review.formula.toml`
> **Related**: [mountain-eater.md](mountain-eater.md) | [convoy-lifecycle.md](convoy-lifecycle.md) | `internal/formula/formulas/code-quality.formula.toml`

---

## 1. Problem

We want a repeatable, comprehensive code-quality review across six independent
dimensions (performance, test coverage, integration, e2e, duplication, dead
code), each running in parallel and rolling up into a single dashboard with
actionable, deduplicated, severity-ranked findings — without flooding `bd list`
on every re-run.

This is exactly the shape Gas Town already has primitives for: a **convoy**
fans parallel legs out and a **synthesis** leg rolls them up; a **mountain**
(convoy + `mountain` label) adds Deacon-driven progress audit and Witness
failure tracking on top. The deliverable is therefore a `type = "convoy"`
formula plus the operating procedure to run it as a mountain.

---

## 2. Key design decision: convoy formula, promoted to a mountain

**A mountain is not a distinct formula type — it is a convoy with the `mountain`
label** (`docs/design/convoy/mountain-eater.md` §4). `gt mountain <id>` stages,
labels, and launches; the label is what opts the convoy into Layers 1-2
(Witness failure tracking, Deacon audit).

There is no formula field that emits the `mountain` label, and `gt formula run`
creates a plain convoy (`hq-cv-*` bead, `gt:convoy` label, parallel leg beads, a
synthesis bead blocked by all legs — see `executeConvoyFormula` in
`internal/cmd/formula.go`). So the integration is two steps, by design:

```bash
# 1. Fan out the review as a convoy
gt formula run mountain-code-review --rig=gastown_upstream --preset=full
#    → prints the convoy id, e.g. hq-cv-arra4

# 2. Promote that convoy to a mountain (enables audit + rollup grinding)
gt mountain hq-cv-arra4
```

This reuses every existing mechanism unchanged:

| Concern | Mechanism (already built) |
|---|---|
| Parallel fan-out (one polecat per axis) | convoy leg beads + `gt sling` per leg |
| Rollup / dashboard | synthesis bead, blocked on all legs, runs last |
| Findings as child beads | synthesis files beads; legs tracked by the convoy |
| Stall detection / skip-after-N | `mountain` label → Witness Layer 1 |
| Progress audit + completion notice | `mountain` label → Deacon Layer 2 |

**Rejected alternative — convoy-of-convoys / one mountain per axis.** Each axis
is a single analysis task, not a sub-DAG, so wrapping each in its own convoy
adds a coordination tier with no work to coordinate. A flat convoy of six legs
+ one synthesis is the minimal structure that satisfies "one child arc per
axis, parallel fan-out, single rollup."

---

## 3. Resolving the bead's open design questions

### 3.1 Mountain vs convoy-of-convoys; how rollup works for N axes

Flat convoy (§2). Rollup is the **synthesis bead**: `gt formula run` wires it
`blocks`-dependent on every leg bead (`addBlockingDepWithRetry`), so the
scheduler holds it until all six axes close, then dispatches it once. It reads
each axis's report (from the leg bead notes — canonical — and the per-axis
markdown file), merges, dedups, and emits `dashboard.md` + `summary.json`.

### 3.2 Per-axis tooling: linters vs LLM-review polecats

**Hybrid, leaning on tools where they exist, LLM-judged everywhere.** Each axis
is an LLM-review polecat (it can reason about contracts and loops a linter
can't), but the static axes are instructed to *run a tool first, then read to
confirm*:

| Axis | Tool-first | LLM judgment |
|---|---|---|
| performance | `go vet`, grep for per-loop queries / un-closed conns | hot-path + leak reasoning |
| dead-code | `deadcode`, `staticcheck -unused` | confirm vs reflection/embed/build-tags |
| duplication | `dupl` | genuine drift-risk vs coincidental similarity |
| test-coverage | `go test -cover`, find untested pkgs | "would this catch a real regression?" |
| integration | grep config keys / TOML tags | cross-component contract reasoning |
| e2e | trace loops; find e2e harness | "what leaves this loop half-done forever?" |

Tools cut false negatives cheaply; the LLM pass cuts the false positives tools
notoriously produce (a `deadcode` hit that's actually reached via reflection).
The axis prompts call out gastown's *actual* recurring failure modes (dolt conn
leaks, per-rig fan-out, `config_key` vs `key` drift, post-merge unhook strands)
so the review targets known-risky ground rather than generic advice.

### 3.3 How findings become beads without flooding (severity gate + dedup)

Two gates, both enforced in the synthesis prompt:

1. **Severity gate.** File a bead *only* for **new Critical (P0)** findings.
   Major/Minor live in `dashboard.md` only. (Same policy as `code-quality`,
   which proved out the low-`bd`-noise approach.)
2. **Dedup ledger.** Every finding carries a deterministic
   `Fingerprint = axis:<id>|<file>|<symbol-or-rule>`. The synthesis step:
   - collapses identical fingerprints across axes into one row;
   - reads the prior run's `summary.json.fingerprints` ledger and **does not
     re-file** a bead whose fingerprint already has an open bead;
   - re-files only if the prior bead was closed and the finding recurred.

   `summary.json` carries the `fingerprints` map forward, so the ledger is the
   re-runnability contract — not a guess based on bead-title matching.

Bead labels: `mountain-review`, `review-critical`, `rig:<rig>`, `axis:<id>` —
queryable, and easy to bulk-triage or close.

### 3.4 Scope bounding: full tree vs changed-paths-only

`--set scope=changed` (with optional `--branch`) restricts every axis to
`git diff --name-only <base>...HEAD` plus direct callers/callees; default
`scope=full` reviews the whole tree. Orthogonally, `--preset` controls axis
*breadth* (`quick` 3 axes / `standard` 5 / `full` 6). Scope × preset gives the
fast-PR-gate case (`quick` + `changed`) and the deep-audit case (`full` +
`full`) from one formula.

---

## 4. Re-runnability (idempotence + dedup)

- **Run-scoped output.** `directory = .reviews/mountain/{{.design_id}}` — the
  `design_id` is derived deterministically from the convoy bead id
  (`deriveDesignID`), so each run lands in its own directory and never clobbers
  a prior run.
- **Fingerprint dedup against the prior `summary.json`** (§3.3) means a re-run
  on an unchanged tree files **zero** new beads — it just refreshes the
  dashboard and records deltas.
- **Trend deltas.** `summary.json` carries scores + counts; synthesis computes
  `delta_since_last`, so successive runs show whether debt is being paid down.

---

## 5. `review_only` and the merge queue

The formula sets `review_only = true` (and each leg repeats it). Axis legs are
analysis-only — they produce reports, never code changes, so they exit via
`gt done --status DEFERRED` and never enter the merge queue. Only the synthesis
step commits (the `.reviews/mountain/<id>/` report files + the new finding
beads) and flows through the MQ as one normal change. This mirrors
`code-quality.formula.toml`, which is the closest existing precedent.

Because legs land their analysis in different physical locations depending on
completion path, the convoy runner appends a uniform "persist your full report
to this leg bead's notes" directive to every leg (see `executeConvoyFormula`),
and the synthesis prompt treats **bead notes as canonical**. This is the
gu-drftd fix and is why the prompt tells axes to write notes *and* (optionally)
a file.

---

## 6. Verification

- Offline structural validator (`scripts/validate-formula.py`): **pass**.
- Authoritative parser + dispatch preview
  (`gt formula run mountain-code-review --dry-run --rig gastown_upstream`):
  **6 parallel legs + synthesis**, run-scoped output dir — **pass**.
- `TestParseRealFormulas` parses every embedded `*.formula.toml`; the new file
  is embedded via `//go:embed formulas/*.formula.toml` and parses + validates
  as a convoy with legs and a synthesis whose `depends_on` resolves to real legs.

---

## 7. Operating procedure

```bash
# Full deep audit, promoted to a mountain
gt formula run mountain-code-review --rig=gastown_upstream --preset=full
gt mountain <hq-cv-id>
gt mountain status <hq-cv-id>          # progress bar, active/blocked/skipped

# Fast PR-scoped gate (3 static axes, changed paths only)
gt formula run mountain-code-review --preset=quick --set scope=changed --branch=<feature>

# Triage the findings it filed
bd list --label mountain-review --label review-critical
```

---

## 8. Future work (out of scope here)

- A formula-level field (or a `gt formula run --mountain`) that applies the
  `mountain` label automatically, collapsing the two-step run into one. Today
  the promote step is manual by design (§2).
- Weighted overall score (currently the unweighted mean of axis scores).
- Auto-`gt mountain` from a plugin condition gate (like `code-quality`'s
  6h-elapsed-AND-new-commits trigger) for periodic unattended runs.

## Sources

- [mountain-eater.md](mountain-eater.md) — Mountain-Eater design (label semantics, Layers 0-3) — accessed 2026-06-15
- `internal/cmd/formula.go` — `executeConvoyFormula` (convoy/leg/synthesis bead creation, dep wiring) — accessed 2026-06-15
- `internal/cmd/mountain.go` — `gt mountain` (stage + label + launch) — accessed 2026-06-15
- `internal/formula/formulas/code-quality.formula.toml` — closest precedent (review_only convoy, severity gate, summary.json) — accessed 2026-06-15
- `internal/formula/parser.go` — convoy `Validate` rules — accessed 2026-06-15

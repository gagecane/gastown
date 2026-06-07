# Design: Auto-Test-PR — Simplified, language-agnostic v1

> **Supersedes** `.designs/auto-test-pr/synthesis.md` for the v1 scope.
> The original synthesis (7-state machine, two pinned state beads,
> five quality gates, a `gt auto-test-pr` CLI tree, daemon dogs, and a
> Go-only language allow-list) is retained for historical context and
> for the dimension analyses it cites, but this document is the
> authoritative v1 design. Tracked under epic `gu-t8cp`.

## Why this redesign

The original design is correct but over-built. Almost all of its
machinery exists to track **one fact** — "is there auto-test work in
flight for this rig?" — which is already answered for free by querying
open merge requests. Two further sources of complexity are (a) a
compiled, Go-only quality-gate layer that hard-codes the feature to one
language, and (b) a `gt auto-test-pr` CLI tree whose every verb
duplicates either rig config or an existing generic command.

This v1 is built on four principles:

1. **State is derived, not stored.** The open-MR list *is* the state.
2. **Zero language coupling.** No per-language code anywhere. The test
   harness is **discovered at runtime** by an agent, not declared in
   config — so adding a language requires no change at all.
3. **No bespoke CLI.** Opt-in is rig config; observability is the
   generic `gt mr list` query; the cycle is driven by the patrol
   formula. The `gt auto-test-pr` command tree is removed.
4. **Single-responsibility agents.** The run is a *decomposed pipeline*
   of one polecat per responsibility (discover → select → research →
   implement → review-loop → retro), not one monolithic polecat. Each
   agent has a narrow job; a retro agent feeds friction back into the
   conventions sheet so future runs improve.

## Core behavior (the whole feature in one paragraph)

An opt-in, per-rig **standing patrol** that, on a cadence, checks for an
already-open auto-test MR and — if none — slings a **decomposed
pipeline** of single-responsibility polecats. The pipeline discovers how
to run tests/coverage for the rig, picks a single recently-churned file,
researches conventions and code pointers, implements tests, runs a
bounded quality-review loop back to the implementer, then retros the run
(documenting friction into the conventions sheet and filing beads). The
result is one small, human-reviewed, test-only MR — **skipped entirely
if an auto-test-pr MR is already open for that rig.**

## The minimal rig config (opt-in only — no language profile)

Because the harness is *discovered* at runtime, the rig config shrinks
to opt-in plus blast-radius knobs. It lives in the rig's existing
settings JSON (`auto_test_pr` block):

```json
"auto_test_pr": {
  "enabled": true,
  "paused": false,
  "cadence_days": 7,
  "churn_window_days": 30,
  "max_files": 3,
  "max_added_loc": 200,
  "conventions_path": ".gt/auto-test-pr/conventions.md"
}
```

There is **no** `language` field, no `test_command`, no
`coverage_command`, no `test_file_glob`, no allow-list, no
`languages.go`. The `discover-harness` pipeline step learns those at
runtime from repo markers + README/CONTRIBUTING and records them on the
work bead for the downstream steps. A rig is "supported" iff
`enabled=true` and the discover step can find a green test harness.
Go, Python, TypeScript, Rust, etc. all work with the *same* config and
the *same* formula — the only difference is what `discover-harness`
finds.

## Two formulas

| Formula | Type | Role |
|---|---|---|
| `mol-auto-test-pr-pipeline` | workflow | The per-run decomposed pipeline — one polecat per responsibility. Produces one MR. **Built and validated.** |
| `mol-auto-test-pr-cycle` *(simplified)* | patrol | Standing loop: cadence + in-flight check, then slings the pipeline for an eligible rig. Replaces the old inert state-machine cycle. |

### Standing patrol (`mol-auto-test-pr-cycle`, simplified)

```
1. opt-in?     read settings.auto_test_pr.enabled      → skip if false/paused
2. in-flight?  gt mr list -l gt:auto-test-pr (this rig) → SKIP IF ANY OPEN  ◄ core invariant
3. cadence?    last closed gt:auto-test-pr MR < N days  → skip
4. sling       gt sling / formula run mol-auto-test-pr-pipeline --rig <rig>
5. respawn     sleep cadence; bd mol step respawn back to step 1
```

Step 2 enforces "≤1 open auto-test PR per rig" with no stored state.
The query substrate already exists and is trusted by the current daemon
(`ListMergeRequests({Label:"gt:auto-test-pr"})`). The patrol itself
creates no per-cycle bead (respawn reuses the molecule).

### Per-run pipeline (`mol-auto-test-pr-pipeline`)

Seven single-responsibility steps; each is one polecat / one step bead:

| # | Step | Single responsibility |
|---|------|-----------------------|
| 1 | `discover-harness` | Learn `test_command` / `coverage_command` / `test_file_glob` from repo markers + README (NOT a hardcoded table); verify the clean tree is green. |
| 2 | `select-target` | Churn (`git log --since`) × low-coverage ranking, minus `skip.txt`; pick exactly ONE file. |
| 3 | `research-conventions` | Read conventions sheet + sibling tests; find reusable helpers; enumerate uncovered branches with file:line pointers. |
| 4 | `implement-tests` | Write tests from the research note; enforce allow-list + size caps; never fix product bugs (note them for retro). |
| 5 | `review-tests` | Run the command-driven gates + judgment; **respawn to `implement-tests`** on reject, **submit the `gt:auto-test-pr` MR** on pass. |
| 6 | `retro` | Update the conventions sheet with discovered friction (self-improving); file bug / follow-up / `skip.txt` beads. |
| 7 | `done` | Record a run summary. |

**Iteration without a DAG cycle.** The review→implement loop is *not* a
graph back-edge (the parser's `checkCycles` rejects those). On reject,
`review-tests` runs `bd mol step respawn --target implement-tests` with
its feedback; the built-in respawn limit (3) bounds the loop. On pass it
submits the MR. This is the same runtime-iteration pattern used by
`mol-pr-feedback-patrol` and the other standing patrols.

## Quality gates — all command-driven, zero AST

| Gate | Mechanism (language-agnostic) | Status |
|---|---|---|
| Output allow-list | every changed file matches `test_file_glob`; reject otherwise | ✅ always |
| Tests pass | run `test_command`; require exit 0 | ✅ always |
| Coverage up | run `coverage_command` before/after; require delta > 0 | ✅ if configured |
| Flakiness | run `test_command` N times; require all pass | ✅ always |
| gitleaks | `gitleaks detect` on the diff | ✅ always (already neutral) |
| ~~Tautology linter~~ | requires per-language AST | ❌ **cut** |
| ~~Mutant-sanity~~ | requires per-language AST | ❌ **cut** |

The commands the gates run (`test_command`, `coverage_command`,
`test_file_glob`) come from the `discover-harness` step at runtime, not
from config. The two cut gates are exactly the two that required
compiled per-language AST analysis. What remains is portable: match a
glob, run a command and check the exit code, run a command twice and
compare a number, rerun a command N times. The `review-tests` step
(agent 5) runs all of these.

**Quality-floor compensation for the dropped mutant gate:** the LLM
polecat's own judgment plus **mandatory human review** (no auto-merge,
unchanged from the original). If a specific rig later wants
mutant-sanity back, it is added as an *optional config command*
(`mutant_command` in the profile) — never as compiled code, preserving
the "commands not code" rule.

## State: derived, not stored

| Question | Source |
|---|---|
| Is the rig opted in? | rig settings JSON (`enabled`, `paused`) |
| Is work in flight? | open MR query by label `gt:auto-test-pr` |
| When did it last run? | timestamp of last closed `gt:auto-test-pr` MR |
| Don't retarget this file | `.gt/auto-test-pr/skip.txt` in the rig repo |

The skip-list is the **only** persisted memory, because "a reviewer
rejected file X — don't retarget it" is not derivable from the MR list
alone. It is a plain newline-delimited file of repo-relative paths
(optionally with a date), appended either by a reviewer directly or by
parsing a close-reason. It is intentionally *not* a bead and *not* a
state machine.

Removed entirely: the 7-state machine, both pinned state beads, CAS
transitions, the reconcile (`enabled_rigs[]`) step, the attachment-bead
transition/rejection audit log, the cooldown-release edge, and the
circuit-breaker counter bead.

## No CLI: `gt auto-test-pr` is removed

Every verb maps to config or an existing generic command:

| Removed verb | Replacement |
|---|---|
| `enable` / `disable` | edit `auto_test_pr.enabled` in rig settings JSON |
| `pause` / `resume` | set `auto_test_pr.paused` in the same block |
| `status` / `show` | `gt mr list -l gt:auto-test-pr` |
| `history` | `gt mr list -l gt:auto-test-pr --all` + `git log` |
| `revise` | reviewer comments on the MR; manual re-sling of the polecat formula if needed |
| `show-template` / `emit-template` | conventions sheet ships as a static template file in the repo; copy manually |
| `cycle-tick` | the patrol formula calls the workflow entry directly — no dedicated verb |

## Components after simplification

**Delete:**
- `rig_state.go`, `rig_state_store.go`, `town_state.go`,
  `town_state_mutators.go` (state machine + CAS)
- pinned-bead provisioning + reconcile (`enabled_rigs[]`)
- attachment-bead transition/rejection log (OQ4 machinery)
- `cooldown_release.go`
- CAS transitions in `dispatch.go` (keep only the envelope builder)
- daemon `mr_cycle_close_dog`, `main_ci_break_dog`,
  `dolt_circuit_breaker`, `main_ci_break_handler_wire`
- the AST gates in the polecat formula (4b mutant, 4d tautology)
- the entire `internal/cmd/auto_test_pr*.go` CLI tree

- `mol-polecat-work-test-improver` formula — superseded by
  `mol-auto-test-pr-pipeline` (the decomposed pipeline)
- the Go `RunCycle` state-machine entry + its `Targets`/`Dispatch`/
  `RigStore` hooks — superseded by the patrol formula's plain
  cadence + in-flight check (no Go orchestration code)

**Keep / adapt:**
- `mol-auto-test-pr-pipeline.formula.toml` — the 7-step decomposed
  pipeline (**built + validated**)
- `mol-auto-test-pr-cycle` formula — rewritten as a thin standing patrol
  (opt-in + in-flight check + sling pipeline + respawn); no Go hooks
- conventions sheet — kept as an in-repo template; now also **written
  to** by the `retro` step each run

The feature moves from **inert** (the old `cycle-tick` did nothing
because its `Targets`/`Dispatch` hooks were `nil`) to **runnable**:
orchestration is now formula-driven, so there are no Go hooks left to
wire.

## Honest residual risks

| Risk | Assessment |
|---|---|
| Dispatch race (two ticks, no CAS) | Worst case: one duplicate PR a human closes. Negligible at weekly cadence with a single patrol. Acceptable trade for deleting the state machine. |
| Lower quality floor (no mutant gate) | Mitigated by LLM polecat + mandatory human review (no auto-merge). Reversible per-rig via optional `mutant_command`. |
| `coverage_command` portability | Some ecosystems make "branch-coverage delta as a number" awkward. Handled by making the gate **optional** — unconfigured = skipped, rely on tests-pass + review. |
| No CI-break SEV-1 auto-revert | Dropped deliberately. Test-only + human-reviewed + Refinery gates already protect `main`; a post-merge break is a normal revert, not a bespoke robot. |
| Higher per-run footprint from decomposition | ~7 step beads + up to ~9 agent sessions per produced PR (review loop adds re-runs), vs. 1 agent / 2 beads for a monolithic polecat. Deliberate trade: single-responsibility agents + bounded quality loop + self-improving conventions sheet. Sized for a **weekly** cadence, not hourly; skipped ticks remain 0-agent / 0-bead. |

## What is unchanged from the original (load-bearing, kept on purpose)

- Per-rig opt-in, default OFF.
- One open auto-test MR per rig (now via MR query).
- Test-only output (the allow-list is the security boundary).
- Bounded blast radius (`max_files`, `max_added_loc`).
- Human review required; no auto-merge.
- The polecat files a separate bead for bugs it finds; it never bundles
  a source fix into the test PR.

## Sources

- [Original synthesis](.designs/auto-test-pr/synthesis.md) — accessed 2026-06-06
- [Source PRD](.prd-reviews/auto-test-pr/prd-draft.md) — accessed 2026-06-06
- Current implementation under `internal/autotestpr/`, `internal/cmd/auto_test_pr*.go`, `internal/daemon/*` (epic `gu-t8cp`) — inspected 2026-06-06

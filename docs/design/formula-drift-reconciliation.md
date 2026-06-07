# Formula Content Drift Reconciliation

> Companion to [formula-resolution.md](./formula-resolution.md). Records the
> canonical-vs-customized status of every formula name that exists in more than
> one content version across town locations, per bead **gu-m0snh**.

## Scope

A town-wide audit (2026-06-06) found 13 formula names existing in 2–3 distinct
content versions across town locations. This document resolves, for each name,
which copy is canonical and whether each divergent copy is an **intentional
rig-local customization** or a **stale copy that should be re-synced**.

This is a hygiene/clarity record, not a correctness fix: all copies are
individually valid (offline validation + `--dry-run` pass). The drift is a
direct, expected consequence of the three-tier resolution model — it just had
no written canonical-vs-customized ledger until now.

## How drift happens (root cause)

Per [formula-resolution.md](./formula-resolution.md), resolution precedence is
**rig > town > embedded**:

| Tier | Location | Source of truth? |
|------|----------|------------------|
| Embedded (system) | `internal/formula/formulas/` (compiled into the `gt` binary) | **YES — canonical** |
| Town (user) | `~/gt/.beads/formulas/` | provisioned copy / user override |
| Rig (project) | `<rig>/.beads/formulas/` | provisioned copy / project override |

The embedded set in `internal/formula/formulas/` is the **single source of
truth** (51 formulas). The `.beads/formulas/` copies are written by
`ProvisionFormulas` at `gt install` time and refreshed by `UpdateFormulas`
(invoked from `gt upgrade` step 5 and `gt doctor`'s `formulas` check).

Two mechanisms produce the observed drift:

1. **Stale provisioned copies (the common case).** `ProvisionFormulas` never
   overwrites an existing file, and `UpdateFormulas` only refreshes the **town**
   tier (`internal/doctor/formula_check.go` calls `CheckFormulaHealth(ctx.TownRoot)`
   — town root only). A copy installed by an older binary sits frozen at its
   install-time version while the embedded canonical advances. These copies are
   **behind**, not customized.

2. **Intentional rig-local overrides (the rare case).** A rig deliberately edits
   its own `<rig>/.beads/formulas/<name>` to change behavior for that project.
   These copies self-document their rationale (cite a bead id, describe the
   rig-specific motivation).

The discriminator used below: a divergent copy is **STALE** if the canonical has
content the copy lacks (copy is strictly behind, lower or equal `version`, no
rig-specific rationale); it is **INTENTIONAL** if the copy adds rig-specific
content that self-documents why it differs from canonical.

## Per-formula reconciliation

Legend: **C** = canonical (`internal/formula/formulas/`), **T** = town
(`~/gt/.beads/formulas/`), **R** = rig (`<rig>/.beads/formulas/`).
"canon-only lines" = lines present in C but missing from the copy (copy is behind).

| Formula | Canonical | T status | R status | Verdict |
|---------|-----------|----------|----------|---------|
| `mol-deacon-patrol` | C v17 | behind (v15, 189 canon-only lines) | behind (v15) | **Re-sync T+R to C.** C adds heartbeat-spam prevention + memory-check + daemon plugin-run rules the copies predate. Stale. |
| `mol-polecat-work` | C v13 | behind (v10, 106 canon-only lines) | behind (v12) | **Re-sync T+R to C.** C adds the `gt done` non-negotiable block (gs-30s/gs-fqs). Stale. |
| `mol-refinery-patrol` | C v15 | behind (16 canon-only lines) | behind (v14) | **Re-sync T+R to C.** C adds `--merge-commit` hand-merge guidance (gu-xs9na). Stale. |
| `mol-witness-patrol` | C v13 | in sync | talontriage v13: uses legacy `bd list` agent-bead resolve instead of `gt agents resolve` (aa-b2tm) | **Re-sync R to C.** Talontriage copy predates the `gt agents resolve` migration. Stale, not intentional. |
| `mol-casc-wiki-patrol` | C v1 | in sync | casc_cdk v1: folds preflight into publish step, self-documents rationale (cadk-b2bz) | **INTENTIONAL — keep R, document here.** casc_cdk mitigation for gu-kruw; cites the bead and explains the no-code-completion-path workaround. Re-sync the other 10 stale casc copies to C. |
| `build-claude-skill` | C v1 | behind (10 canon-only lines) | behind | **Re-sync T+R to C.** C adds the "--help is not always the arbiter" verification guidance. Stale. |
| `mol-orphan-scan` | C v2 | behind (1 canon-only line: `required = true`) | behind | **Re-sync T+R to C.** Stale. |
| `mol-polecat-code-review` | C v1 | town adds 9 ANTI-WASTE lines | behind | **Re-sync.** The town "ANTI-WASTE RULES" block is the *older* shape; canonical dropped it after consolidating guidance elsewhere. Town copy is behind, not a deliberate override (no rig-specific rationale). Re-sync T+R to C. |
| `mol-polecat-work-monorepo` | C v1 | town adds 9 ANTI-WASTE lines | behind | **Re-sync T+R to C.** Same stale ANTI-WASTE block as above. |
| `mol-dog-reaper` | C v2 | in sync | talontriage: 47 canon-only lines | **Re-sync R to C.** Talontriage copy is heavily behind. Stale. |
| `mol-idea-to-plan` | C v2 | in sync | talontriage: 43 canon-only / 5 rig-only lines | **Re-sync R to C.** Rig-only lines ("Set up dependencies", "Verify the dependency graph") are an older numbered-step structure that canonical already supersedes — not a rig-specific feature. Stale. |
| `beads-release` | **none (removed)** | town v1 leftover | beads-rig copies | **Delete leftovers.** Canonical removed by **gu-hmyi** (closed). Remaining `.beads/formulas/beads-release.formula.toml` copies (town + 3 beads-rig dirs) are orphans of a deleted formula. No canonical to sync to. |
| `casc-webapp-patrol` | **none (rig-local only)** | not present | casc_webapp: refinery/polecats/crew share one version (bc8c7a5d), mayor differs (ffb1236b) | **INTENTIONAL rig-local formula — keep, but converge.** This formula has no embedded canonical by design (it is casc_webapp-specific). The mayor copy is behind the refinery/polecat copy (older route-discovery description). Designate `casc_webapp/refinery/rig` version (bc8c7a5d, v3 Phase A route auto-discovery) as the rig-canonical and re-sync the mayor copy to it. |

## Summary

- **Canonical source of truth confirmed:** `internal/formula/formulas/` (embedded),
  for all 11 names that have an embedded version.
- **Intentional customizations (keep, now documented):**
  - `mol-casc-wiki-patrol` @ casc_cdk — preflight folded into publish (cadk-b2bz).
  - `casc-webapp-patrol` — rig-local-only formula; no embedded canonical by design.
- **Everything else is stale** install-time copies that are strictly behind the
  embedded canonical and should be re-synced.
- **`beads-release`** copies are orphans of a formula deleted by gu-hmyi — delete,
  do not sync.

## Recommended remediation (separate work, not in this bead)

The reconciliation above is the deliverable for gu-m0snh (classification +
canonical identification). Acting on it touches live operational
`.beads/formulas/` directories outside this package and is therefore tracked
separately:

1. **Town tier** already self-heals: `gt upgrade` / `gt doctor --fix` runs
   `UpdateFormulas(townRoot)`, which re-syncs stale, unmodified town copies. The
   audited town drift will clear on the next upgrade for any host that has not
   locally modified those files.
2. **Rig tier has a tooling gap.** `CheckFormulaHealth` / `UpdateFormulas` are
   only ever called with `TownRoot` (`internal/doctor/formula_check.go:32,115`),
   so rig-level `.beads/formulas/` copies are **never** health-checked or
   refreshed. This is why rig copies drift unbounded. Recommend a follow-up bead:
   extend the formula health check to also scan each rig's `.beads/formulas/`,
   honoring the same intentional-modification protection (`.installed.json`
   hashes) so documented overrides like casc_cdk's are preserved.
3. **`beads-release` orphans**: remove the leftover copies as part of gu-hmyi
   cleanup follow-up (the formula itself is already gone from canonical).

## Sources

- [Formula Resolution Architecture](./formula-resolution.md) — accessed 2026-06-07
- `internal/formula/embed.go` (ProvisionFormulas / UpdateFormulas / CheckFormulaHealth) — accessed 2026-06-07
- `internal/doctor/formula_check.go` (town-tier-only health check) — accessed 2026-06-07
- Bead gu-m0snh (this assignment), gu-hmyi (beads-release/gastown-release removal, closed) — accessed 2026-06-07

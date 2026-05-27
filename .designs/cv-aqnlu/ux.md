# User Experience Analysis

## Summary

The automated code quality analysis system sits at the intersection of two
already-shipped subsystems (plugins and formulas), so the core UX challenge is
not "how do I use this?" but rather **"do I even notice it's working?"** The
system runs autonomously via Deacon patrol cycles. Most of the time the user
(an operator or developer consuming quality reports) interacts with *outputs*
(reports, filed beads, digest summaries) rather than *inputs* (configuration,
triggering). This inverts the typical CLI UX problem: instead of discoverability
of commands, the challenge is discoverability of *results* and *status*.

The plugin-and-formula composition pattern already has strong UX precedent in Gas
Town (`gt plugin list/show/run/history`, `gt formula list/show/run`). Users who
understand either subsystem will find the quality-analysis feature predictable.
The key UX risk is cognitive overload: another plugin, another formula, more
beads, more wisps, more digest lines — without clear signal about what requires
human attention versus what is operating normally.

## Analysis

### Key Considerations

- **The primary user is the Mayor/operator**, not the polecat workers. Workers
  execute legs automatically and never interact with the plugin. The operator
  needs to: (a) understand that quality analysis is running, (b) see results
  when they matter, (c) tune frequency and scope, (d) intervene when something
  is wrong.

- **"Set and forget" is the ideal state.** A well-configured quality plugin
  should run silently when everything is fine and only surface when findings
  warrant attention. This is the same pattern as `rebuild-gt` (silent success,
  escalation on failure) and `quality-review` (silent success, BREACH alerts).

- **Progressive disclosure must span three layers:**
  1. Plugin system: `gt plugin list` shows it exists, `gt plugin show` reveals gate/execution config
  2. Formula system: `gt formula show code-quality` reveals the legs and what each analyzes
  3. Output system: Where to find results, what they mean, how to act on them

- **Beads proliferation is the #1 UX anti-pattern.** The quality-review plugin
  creates ephemeral wisps for each run. The code-scout plugin creates P3 beads
  per finding. If the quality-analysis plugin does both (receipt wisps AND
  per-finding beads), the `bd list` output becomes noisy. Auto-dispatch then
  picks up filed beads and slings them — creating fix-it polecats that may
  conflict with deliberate human decisions about technical debt.

- **Gate tuning is the power-user knob.** Beginners want "run once a day,
  tell me if something is bad." Power users want to tweak cooldowns, select
  specific legs, scope to specific directories, and integrate with their
  existing CI pipeline.

- **Error states must be self-diagnosing.** When quality analysis fails (plugin
  gate evaluation errors, formula dispatch failures, polecat slot exhaustion,
  synthesis timeouts), the error must tell the user *what failed* and *what to
  try*. The current plugin system's error messages are reasonable but terse
  (`"Warning: checking gate status: %v"`).

### Options Explored

#### Option 1: Report-Only (Committed File Output)

- **Description**: Quality analysis produces a committed markdown report
  (`.quality/<date>/report.md`) in the rig's source tree. No beads are filed
  for individual findings. Results are passive — discoverable via git log, file
  browsing, or a `gt quality` convenience command.
- **Pros**:
  - Lowest noise: no new beads in `bd list`
  - Reports are version-controlled (can diff trends over time)
  - Familiar format (same as `.reviews/<id>/` for code review)
  - Natural integration with git (reviewable in PRs if committed on a branch)
  - No auto-dispatch interference (beads not created → polecats not spawned for fix-its)
- **Cons**:
  - Passive — user must remember to look
  - Committed to repo, so rig accumulates history (needs `.quality/` cleanup policy)
  - No workflow integration (can't "assign" a finding to someone)
- **Effort**: Low

#### Option 2: Bead-Per-Finding (Active Dispatch)

- **Description**: Each finding from the quality analysis creates a bead. Beads
  are tagged (`quality-finding`, severity, dimension) and auto-dispatch can sling
  them to polecats for automated fix-it.
- **Pros**:
  - Fully automated remediation pipeline (find → file → dispatch → fix)
  - Findings are queryable (`bd list -l quality-finding`)
  - Integrates with existing bead workflow (status tracking, assignments)
  - Power users can `bd list -l quality-finding,dim:security` to slice by dimension
- **Cons**:
  - High noise risk (a quality scan of a large codebase could create 50+ beads)
  - Auto-dispatch spawning fix-it polecats for every style issue is wasteful
  - Duplicates code-scout's role (code-scout already files improvement beads)
  - Operator must actively curate/triage findings or face bead accumulation
- **Effort**: Medium

#### Option 3: Hybrid (Report + Escalation-Only Beads)

- **Description**: Quality analysis produces a committed report (Option 1), but
  ALSO creates beads only for Critical/P0 findings that warrant immediate human
  or automated attention. Sub-critical findings stay in the report. The report
  includes a "Summary" section surfaced in the patrol digest.
- **Pros**:
  - Low noise: only actionable findings become beads
  - Passive report captures full context (trends, minor issues, observations)
  - Critical findings get workflow treatment (dispatch, tracking, closure)
  - Clear signal-to-noise ratio for `bd list`
  - Patrol digest shows "Quality Analysis: 2 critical, 5 major, 12 minor" one-liner
- **Cons**:
  - Slightly more complex logic (threshold for bead creation)
  - Two places to look (report file + beads for criticals)
  - Threshold tuning may need operator iteration
- **Effort**: Medium

#### Option 4: Dashboard Command (`gt quality`)

- **Description**: A dedicated `gt quality` subcommand tree that provides a
  unified view of quality state across rigs. `gt quality status` shows the
  latest report summary, `gt quality trends` shows historical deltas, `gt
  quality findings` shows current open findings. This wraps the underlying
  beads/files and presents them in a curated view.
- **Pros**:
  - Single entry point for quality information
  - Hides implementation details (report files vs beads vs wisps)
  - Enables rich formatting (color-coded severity, trend arrows)
  - Composable with existing `gt` command structure
- **Cons**:
  - Additional CLI surface area to learn
  - Another command to remember (discovery problem)
  - Requires implementation beyond plugin/formula (new Go code)
  - Could be implemented later as a view layer on top of Option 1 or 3
- **Effort**: High

### Recommendation

**Option 3 (Hybrid: Report + Escalation-Only Beads)** combined with an eventual
path toward **Option 4 (`gt quality`)** as a read-only view layer.

Rationale for the UX recommendation:

1. **Signal-to-noise is the critical UX property.** Users already deal with high
   bead volumes from auto-dispatch, code-scout, and convoy legs. Adding 20+ beads
   per quality scan would degrade the "what needs my attention?" question that
   `bd ready` answers. Only Critical findings warrant bead treatment.

2. **The report-as-artifact pattern is already understood.** Code-review
   produces `.reviews/<id>/review-summary.md`. Design produces
   `.designs/<id>/design-doc.md`. Quality analysis producing
   `.quality/<date>/quality-report.md` follows the same mental model.

3. **Patrol digest integration provides the "glanceable" summary.** The existing
   `gt patrol digest` system aggregates plugin runs into daily summaries. The
   quality analysis receipt wisp can include the summary line:
   `"Quality Analysis: 2 critical, 5 major, 12 minor — 0.7/1.0"`. This appears
   in the operator's daily digest without requiring them to open anything.

4. **`gt quality` can be deferred** until the report/bead pattern proves stable.
   A convenience command that reads the latest `.quality/` report and formats it
   nicely is a low-cost addition later, but isn't needed for v1.

## UX Workflow Integration

### Where does this fit in daily use?

```
Operator's Day:
                                                    ┌────────────────────────┐
1. Morning: Check patrol digest ────────────────────│ "Quality: 0 critical,  │
   └─ If 0 critical: carry on                      │  3 major, 8 minor.    │
   └─ If >0 critical: investigate                  │  Score: 0.82 (+0.02)" │
                                                    └────────────────────────┘
2. On-demand: Read full report ─────────────────────│ .quality/2026-05-27/   │
   └─ `cat .quality/latest/quality-report.md`       │  quality-report.md    │
   └─ Or (future): `gt quality status`              └────────────────────────┘

3. React: Critical beads auto-dispatched ───────────│ bd list -l quality-     │
   └─ Polecats fix critical findings                │  critical              │
   └─ Human reviews fixes via code-review           └────────────────────────┘
```

### Minimum Viable Interaction

**For the beginner (day 1):**
```bash
gt plugin list
# Sees: "code-quality-analysis [condition] — Automated codebase quality audit"

gt plugin show code-quality-analysis
# Sees: Gate type, cooldown, next run estimate, last run result
```

**For checking results:**
```bash
# Easiest: check the last report
cat .quality/latest/quality-report.md

# Or: check if there are critical findings to address
bd list -l quality-critical --status=open
```

**For power users:**
```bash
# Force a run right now
gt plugin run code-quality-analysis --force

# Run only specific legs
gt formula run code-quality --legs="security,dead-code"

# Change frequency (edit plugin)
$EDITOR plugins/code-quality-analysis/plugin.md
# → Change gate duration from "24h" to "12h"

# Check history
gt plugin history code-quality-analysis
```

### Learning Curve: Progressive Disclosure

| Level | What they know | What they need |
|-------|---------------|----------------|
| **L0: Unaware** | Nothing | Patrol digest mentions it exists |
| **L1: Aware** | "Quality analysis runs" | `gt plugin show` to see config |
| **L2: Consumer** | Reads reports | `.quality/latest/` or bead list |
| **L3: Tuner** | Adjusts frequency/scope | Edit plugin.md TOML frontmatter |
| **L4: Extender** | Adds custom legs | Edit formula TOML, add new leg |
| **L5: Author** | Creates new quality plugins | Full plugin.md + condition script |

Each level builds on the previous. The system should work without any user
at L3+. Most users should stay at L1-L2 and only advance when they want more.

## Error Experience: What Happens When Things Go Wrong?

| Failure Mode | User-Visible Signal | Action Available |
|---|---|---|
| **Gate script fails** (condition check errors) | Plugin run wisp: `result:failure` + escalation notification | `gt plugin history code-quality-analysis` shows failure; fix condition script |
| **Not enough polecat slots** (convoy legs can't spawn) | Dispatch warning in Deacon patrol log; legs remain queued | Self-heals as slots free; operator can add more polecats |
| **Leg timeout** (analysis takes too long) | Individual leg bead stuck in `in_progress`; Witness nudges | Reduce scope, increase timeout, or split leg |
| **Synthesis fails** (not all legs completed) | Synthesis bead blocked; partial results available in leg outputs | Read individual leg outputs while synthesis is retried |
| **Dolt connectivity** | `bd` commands hang > 5s; standard Dolt escalation | `gt dolt status`, existing Dolt recovery path |
| **Report path conflict** (`.quality/` directory issues) | Git commit fails; run recorded as failure | Standard git resolution; report re-generated next cycle |

### Error message quality recommendations:

1. **Gate failure**: "Quality analysis gate check failed: <error>. The condition
   script at `plugins/code-quality-analysis/gate-check.sh` returned non-zero.
   Run `gt plugin run code-quality-analysis --dry-run` to debug."

2. **Slot exhaustion**: "Quality analysis convoy has 7 legs but only 2 polecat
   slots available. 5 legs queued. Estimated completion: when slots free up.
   Consider: reduce legs with `--preset=gate` (4 legs) or add polecats."

3. **Partial completion**: "Quality analysis completed 5/7 legs. Missing:
   dead-code, dependency-health (timed out). Partial report available at
   `.quality/2026-05-27/quality-report-partial.md`. Will retry on next cycle."

## Feedback: How Does the User Know It's Working?

### Passive feedback (no action required):
- **Patrol digest line**: One-liner in daily summary with score + delta
- **Plugin history**: `gt plugin history code-quality-analysis` shows green checkmarks
- **Git log**: Committed reports visible in `git log -- .quality/`
- **Wisp receipts**: `bd list -l plugin:code-quality-analysis` shows run history

### Active feedback (something needs attention):
- **Escalation on critical findings**: Mail to Mayor + escalation record
- **Bead creation**: Critical findings appear in `bd ready` for the rig
- **Score degradation alert**: When quality score drops > 0.1 between runs,
  trigger a `quality-review`-style alert: "Quality DECLINING: gastown_upstream
  dropped from 0.85 to 0.72 (-0.13)"

### Progress feedback (during long runs):
- Each leg that completes writes its output file → visible in `.quality/<date>/`
- `bd mol current <convoy-id>` shows which legs are in progress vs complete
- Standard convoy/molecule progress tracking already exists

## Discoverability: --help, Docs, Examples

### Plugin discovery:
```bash
gt plugin list
# Shows code-quality-analysis with [condition] type tag
# Description visible at a glance

gt plugin show code-quality-analysis
# Full details: gate, tracking, execution, first 10 lines of instructions
```

### Formula discovery:
```bash
gt formula list
# Shows code-quality with type=convoy, shows leg count

gt formula show code-quality
# Full leg list, presets, input variables
```

### Help text recommendations:

**Plugin `--help` should include:**
```
AUTOMATED QUALITY ANALYSIS

This plugin runs periodic code quality analysis across the codebase.

TRIGGERING:
  Runs automatically when:
  - At least 24h since last analysis
  - New commits exist since last analysis

  Manual trigger:
    gt plugin run code-quality-analysis --force

RESULTS:
  Reports: .quality/<date>/quality-report.md
  Latest:  .quality/latest/quality-report.md (symlink)
  History: gt plugin history code-quality-analysis
  Beads:   bd list -l quality-critical (critical findings only)

TUNING:
  Edit: plugins/code-quality-analysis/plugin.md
  Legs: gt formula show code-quality (see available presets)
```

**Formula presets should be named intuitively:**
```toml
[presets.quick]
description = "Fast check: security + dead-code only (2 legs, ~2 minutes)"
legs = ["security", "dead-code"]

[presets.standard]
description = "Standard analysis: all stability dimensions (5 legs, ~10 minutes)"
legs = ["security", "dead-code", "dependency-health", "test-quality", "architecture-drift"]

[presets.comprehensive]
description = "Full audit: every dimension (7 legs, ~20 minutes, 7 polecat slots)"
legs = ["security", "dead-code", "dependency-health", "test-quality", "architecture-drift", "coverage-trend", "documentation"]
```

## Constraints Identified

1. **Bead noise ceiling**: Any design that creates >5 beads per run will degrade
   the `bd ready` signal for the rig. The `code-scout` cap of 5 beads/rig/cycle
   is a proven threshold.

2. **Polecat slot cost**: A 7-leg convoy consumes 7 polecat slots simultaneously.
   In a rig with 3-4 polecats, this starves all other work for the convoy's
   duration. Preset selection must consider slot budget.

3. **Report commit noise**: Daily quality reports committed to the repo add ~1
   commit/day to the git log. This is manageable but accumulates. A cleanup
   policy (keep last 30 days, archive to bead) prevents unbounded growth.

4. **Condition gate opacity**: Users cannot easily see "why didn't the plugin
   run?" for condition gates. Unlike cooldown gates (which show "ran 2 times
   within 1h"), condition gates are binary (script exited non-zero). A `gt
   plugin status` subcommand showing last gate evaluation result would help.

5. **No composite gates in v1**: Users who want "run daily BUT ONLY if new
   commits exist" must embed both checks in the condition script. This is
   functional but less elegant than a declarative `cooldown + condition` gate.
   Document the pattern clearly in the plugin template.

## Open Questions

1. **Should there be a `gt quality` command namespace?** It would provide a
   curated UX layer (status, trends, findings) on top of the raw plugin/formula
   infrastructure. Cost: new Go code. Benefit: single discoverable entry point
   for quality information. Deferrable to v2?

2. **How should the `.quality/latest` symlink update?** On every run (even
   partial)? Only on full successful runs? What if two rigs run quality analysis
   — do they share `.quality/` or is it per-rig?

3. **Should critical findings auto-create beads silently, or require operator
   confirmation?** Silent creation is fully autonomous but risks noise if the
   analysis has false positives. Confirmation adds friction but ensures signal
   quality. A middle ground: create beads but tag `needs-triage` so they don't
   auto-dispatch until reviewed.

4. **What's the relationship between this and code-scout?** Code-scout files
   individual improvement beads per finding. Quality analysis produces holistic
   reports with optional bead escalation. Should code-scout be deprecated in
   favor of the quality-analysis plugin's findings? Or are they complementary
   (scout = continuous incremental, analysis = periodic holistic)?

## Integration Points

### → Plugin System (`gt plugin list/show/run/history`)
- Users discover and interact with quality analysis through existing plugin commands
- No new commands needed for basic interaction
- `gt plugin show code-quality-analysis` is the primary discovery path

### → Formula System (`gt formula list/show/run`)
- Advanced users interact with the convoy formula directly for custom runs
- Preset selection (`--preset=quick`) is the main power-user knob
- Formula legs define the "what gets analyzed" dimension

### → Patrol Digest (operator daily summary)
- Quality analysis receipt wisps include a summary line for the digest
- Digest is the primary L1/L2 feedback channel
- Score + delta format makes trend direction instantly visible

### → Beads System (findings management)
- Only Critical findings create beads (noise threshold)
- Tagged `quality-critical` for filtering and auto-dispatch scoping
- Auto-dispatch can be opt-out per finding via `status=blocked`

### → Git History (report artifacts)
- Reports committed to `.quality/<date>/` in rig source tree
- Version-controlled analysis enables `git diff` on consecutive reports
- Symlink `.quality/latest/` provides stable path for scripts/monitoring

### → Existing Plugins (code-scout, quality-review, verify-build)
- Quality analysis is complementary to code-scout (holistic vs incremental)
- Quality analysis subsumes some quality-review concerns (broader scope)
- Quality analysis findings may trigger verify-build as a gate check

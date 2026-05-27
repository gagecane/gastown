# API & Interface Design

## Summary

The automated code quality analysis feature composes two existing subsystems —
plugins (scheduling/trigger) and formulas (execution/workflow) — into a unified
developer experience. The interface design must serve three distinct personas:
the **operator** (configures and monitors via CLI), the **Deacon agent**
(evaluates gates and dispatches during patrol), and the **polecat agent**
(executes individual analysis legs). Each persona interacts through existing
interface patterns, keeping the new surface area minimal.

The design favors composition over invention. No new top-level `gt` commands are
needed. The plugin definition (`plugin.md`) is the primary configuration
interface. The formula definition (`.formula.toml`) is the workflow interface.
The CLI interacts through existing commands: `gt plugin list|show|run|history`
for the trigger side and `gt formula list|show|run` for the execution side. The
only new interface is a thin orchestration layer in the plugin instructions that
bridges the two.

## Analysis

### Key Considerations

- **Discoverability through existing commands**: Users already know `gt plugin
  list` and `gt formula list`. The new quality analysis plugin and formula
  appear in these listings with no additional learning curve.

- **Configuration via files, not flags**: Gas Town's plugin system uses
  `plugin.md` files with TOML frontmatter as the primary configuration
  interface. This is declarative, version-controlled, and self-documenting.
  New features should follow this pattern rather than introducing flags or
  env vars.

- **Consistency with code-review convoy**: The `code-review.formula.toml`
  convoy already established the pattern for parallel analysis legs with
  synthesis. The quality analysis formula should mirror its structure (inputs,
  presets, legs, prompts, synthesis) for operator familiarity.

- **Gate ergonomics for condition gates**: The `condition` gate type runs a
  shell command and checks exit code. The check script is the primary
  "programming interface" for controlling when analysis runs. This needs to
  be readable and self-contained in the plugin.md frontmatter.

- **Error messages must be actionable**: When a gate blocks execution or a leg
  fails, the error message should include (a) what happened, (b) why, and (c)
  what to do about it. Existing plugin commands already model this well
  (`"Gate closed: ran 2 time(s) within 6h cooldown"` + `"Use --force to override"`).

- **Naming conventions follow existing patterns**: Plugin names use kebab-case
  (`quality-review`, `stuck-agent-dog`, `dolt-backup`). Formula names use
  dot-separated kebab-case (`code-review.formula.toml`). Legs use short
  kebab-case IDs (`dead-code`, `dep-health`).

### Options Explored

#### Option 1: Minimal Surface — Plugin + Formula, Zero New Commands

- **Description**: One new plugin directory (`plugins/code-quality/plugin.md`)
  and one new formula (`code-quality.formula.toml`). All interaction through
  existing `gt plugin` and `gt formula` commands. The plugin instructions tell
  the Dog to sling the convoy formula.
- **Pros**:
  - Zero new CLI surface — nothing to learn
  - Follows established patterns exactly
  - Plugin `run --force` handles manual triggers
  - Formula `run --rig=X` handles one-off targeted analysis
  - History via `gt plugin history code-quality`
- **Cons**:
  - No unified "quality" entry point (must know plugin vs formula)
  - Result consumption requires finding output files or beads
  - No trending/dashboard command out of the box
- **Effort**: Low

#### Option 2: Convenience Alias — `gt quality` Wrapper Command

- **Description**: Add a thin `gt quality` command that wraps the common
  workflows: `gt quality run` (trigger analysis), `gt quality status` (last
  run results), `gt quality history` (trend over time). Under the hood, these
  delegate to plugin/formula commands.
- **Pros**:
  - Single discoverable entry point
  - Hides plugin/formula composition from casual users
  - Can present results in a unified format
  - `gt quality` → help shows the full workflow
- **Cons**:
  - New command surface to maintain
  - Risks becoming a parallel interface to plugin/formula
  - May become stale as plugin/formula commands evolve
  - Violates YAGNI — operators already know `gt plugin`
- **Effort**: Medium

#### Option 3: Formula Presets as the Primary Interface

- **Description**: The formula defines presets (like code-review's `gate` vs
  `full`), and the plugin instructions select the preset based on trigger
  context. Operators interact primarily through preset selection:
  `gt formula run code-quality --preset=quick` (daily delta analysis) vs
  `gt formula run code-quality --preset=full` (weekly comprehensive audit).
- **Pros**:
  - Presets are already proven in code-review formula
  - Natural mapping: gate trigger → quick preset, manual/weekly → full preset
  - Operators can create custom presets in Tier 1 overrides
  - Self-documenting: `gt formula show code-quality` lists available presets
- **Cons**:
  - Preset design is inflexible for edge cases
  - Requires formula to encode all possible analysis scopes
  - Custom scope (specific directory, specific check) needs escape hatch
- **Effort**: Low-Medium

#### Option 4: Event-Driven Interface with New Gate Composition

- **Description**: Introduce a composite gate syntax (`cooldown + condition`)
  in the TOML frontmatter, and an event-emission interface that lets the
  formula report results as structured events for downstream consumers.
- **Pros**:
  - Cleaner gate expression than embedding cooldown in condition script
  - Structured events enable dashboards, trending, automated remediation
  - Future-proof for more complex triggering scenarios
- **Cons**:
  - Requires changes to gate evaluator (`internal/plugin/types.go`)
  - Event feed consumer infrastructure not yet built
  - Over-engineers the immediate need
  - plugin-dispatch-transport ADR already deferred event migration to Phase 2
- **Effort**: High

### Recommendation

**Option 1 (Minimal Surface) with Option 3 (Presets) as the formula-side
interface.** This delivers maximum value with minimum new surface:

**Plugin Interface (Trigger Side):**
```
plugins/code-quality/
├── plugin.md          # TOML frontmatter + check script + Dog instructions
└── check.sh           # Gate condition script (extracted for readability)
```

**Formula Interface (Execution Side):**
```toml
# code-quality.formula.toml
formula = "code-quality"
type = "convoy"

[inputs.scope]
description = "Analysis scope: 'delta' (since last run) or 'full' (entire codebase)"
type = "string"
default = "delta"

[inputs.rig]
description = "Target rig to analyze"
type = "string"
required = true

[presets.quick]
legs = ["dead-code", "dep-health", "architecture"]
description = "Fast daily delta analysis (3 legs, ~5 min)"

[presets.standard]
legs = ["dead-code", "dep-health", "architecture", "test-quality", "complexity"]
description = "Standard analysis (5 legs, ~10 min)"

[presets.full]
legs = ["dead-code", "dep-health", "architecture", "test-quality", "complexity", "documentation", "debt-trend"]
description = "Comprehensive weekly audit (7 legs, ~20 min)"
```

**CLI Interaction Patterns (all existing commands):**
```bash
# Discovery
gt plugin list                          # Shows code-quality in plugin list
gt formula list                         # Shows code-quality in formula list
gt plugin show code-quality             # Full plugin config + gate status
gt formula show code-quality            # Formula details, presets, legs

# Manual trigger
gt plugin run code-quality              # Run with default preset (respects gate)
gt plugin run code-quality --force      # Bypass gate cooldown
gt formula run code-quality --preset=full --rig=gastown_upstream  # Direct formula run

# Monitoring
gt plugin history code-quality          # Execution history with results
gt plugin history code-quality --json   # Machine-readable for trending

# Dry run / debugging
gt plugin run code-quality --dry-run    # Show gate status without executing
```

**No `gt quality` alias needed.** The existing commands are sufficient. If
demand emerges for a convenience wrapper after the feature ships, it can be
added as a zero-risk follow-up without changing the underlying interface.

## Proposed Interface Details

### Plugin Definition: `plugins/code-quality/plugin.md`

```markdown
+++
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
+++

# Code Quality Analysis

Analyze codebase quality across multiple dimensions. This plugin triggers
the `code-quality` convoy formula, which spawns parallel polecats for each
analysis dimension.

## Step 1: Determine scope and preset

Check trigger context to select the appropriate preset:

- If triggered by gate (Deacon patrol) → use `quick` preset (delta analysis)
- If triggered manually with `--force` → use `standard` preset
- If it's been >7 days since last `full` run → use `full` preset

...
```

### Gate Check Script: `plugins/code-quality/check.sh`

```bash
#!/bin/bash
# Gate condition: run if (a) new commits since last analysis AND (b) cooldown elapsed.
# Exit 0 = gate open (run), exit 1 = gate closed (skip).

TOWN_ROOT="${GT_HOME:-$HOME/gt}"
PLUGIN_NAME="code-quality"
MIN_HOURS=6  # Minimum cooldown between runs

# Check last run time
last_run_json=$(bd list --json --all -l "plugin:$PLUGIN_NAME" --limit=1 2>/dev/null)
last_run_time=$(echo "$last_run_json" | jq -r '.[0].created_at // "1970-01-01T00:00:00Z"')

# Calculate hours since last run
last_epoch=$(date -d "$last_run_time" +%s 2>/dev/null || echo 0)
now_epoch=$(date +%s)
hours_since=$(( (now_epoch - last_epoch) / 3600 ))

[ "$hours_since" -lt "$MIN_HOURS" ] && exit 1

# Check for new commits on main since last run
new_commits=$(git log --oneline --since="$last_run_time" origin/main 2>/dev/null | wc -l)
[ "$new_commits" -eq 0 ] && exit 1

exit 0
```

### Formula Inputs (User-Facing)

| Input | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `scope` | string | no | `"delta"` | `"delta"` or `"full"` — controls what code is analyzed |
| `rig` | string | yes | — | Target rig name |
| `preset` | string | no | `"quick"` | Leg selection preset: quick, standard, full |
| `since` | string | no | last run | ISO timestamp or duration for delta scope |
| `output_dir` | string | no | `.quality/<date>/` | Where to write analysis results |

### Formula Presets

| Preset | Legs | Duration | Use Case |
|--------|------|----------|----------|
| `quick` | dead-code, dep-health, architecture | ~5 min | Daily gate-triggered delta analysis |
| `standard` | + test-quality, complexity | ~10 min | Manual runs, after big merges |
| `full` | + documentation, debt-trend | ~20 min | Weekly comprehensive audit |

### Convoy Legs (Analysis Dimensions)

| Leg ID | Focus | Output |
|--------|-------|--------|
| `dead-code` | Unreachable functions, unused exports, orphan files | Findings + removal candidates |
| `dep-health` | Outdated deps, known vulns, unused imports | Dependency report + urgency |
| `architecture` | Package coupling, circular deps, layering violations | Architecture health score |
| `test-quality` | Coverage gaps, tautological tests, fixture rot | Test health score |
| `complexity` | Cyclomatic hotspots, function length, nesting depth | Complexity report |
| `documentation` | Doc coverage, stale docs, missing API docs | Doc freshness score |
| `debt-trend` | TODO accumulation, hack-markers, tech debt velocity | Trend chart data |

### Output Format

Each leg writes a markdown file to the output directory. The synthesis step
combines them into a unified report:

```
.quality/
└── 2026-05-27/
    ├── dead-code.md         # Individual leg findings
    ├── dep-health.md
    ├── architecture.md
    ├── ...
    └── report.md            # Synthesized unified report
```

The unified report follows a consistent structure:

```markdown
# Code Quality Report — 2026-05-27

## Health Score: 7.2/10 (↑0.3 from last run)

## Critical Findings (action required)
- ...

## Notable Findings (review recommended)
- ...

## Trends
- Dead code: 2.1% (↓0.2% from last run)
- Test coverage: 78% (stable)
- Dependency freshness: 3 outdated (1 critical CVE)

## Per-Dimension Scores
| Dimension | Score | Trend | Notes |
|-----------|-------|-------|-------|
| dead-code | 8/10 | ↑ | Removed 3 orphan files |
| ...
```

### Error Messages (Interface Contracts)

| Scenario | Message | Action Hint |
|----------|---------|-------------|
| Gate closed (cooldown) | `"Gate closed: ran 1 time(s) within 6h (2h ago). Use --force to override."` | `--force` flag |
| Gate closed (no commits) | `"Gate closed: no new commits since last analysis (2026-05-27T09:00)."` | Wait for commits |
| Formula not found | `"Formula 'code-quality' not found. Run 'gt plugin sync' to install latest plugins."` | `gt plugin sync` |
| Polecat slots exhausted | `"All polecat slots in use. Queued 5 legs for dispatch when slots free."` | Wait or add slots |
| Leg timeout | `"Leg 'architecture' timed out after 10m. Partial results saved. Full report synthesized without this leg."` | Review partial results |
| Previous run still active | `"code-quality convoy still running (started 5m ago). Skip or --force to start another."` | Wait or `--force` |

### Environment Variables (Configuration Escape Hatches)

| Variable | Default | Effect |
|----------|---------|--------|
| `GT_QUALITY_COOLDOWN` | `"6h"` | Override minimum cooldown between runs |
| `GT_QUALITY_PRESET` | `"quick"` | Override default preset for gate-triggered runs |
| `GT_QUALITY_OUTPUT` | `.quality/` | Override output directory |
| `GT_QUALITY_LEGS` | (all in preset) | Comma-separated leg override |

These are escape hatches for operators, not primary configuration. The TOML
frontmatter is the source of truth.

### Help Text Design

```
$ gt plugin show code-quality

Plugin: code-quality
Path:   ~/gt/plugins/code-quality/
Description: Periodic codebase quality analysis via parallel specialized reviewers
Location: town
Version: 1

Gate:
  Type: condition
  Check: ./check.sh

Tracking:
  Labels: plugin:code-quality, category:quality
  Digest: true

Execution:
  Timeout: 30m
  Notify on failure: true
  Severity: medium

Instructions:
  # Code Quality Analysis
  
  Analyze codebase quality across multiple dimensions. This plugin triggers
  the `code-quality` convoy formula, which spawns parallel polecats for each
  analysis dimension.
  ... (10 more lines)
```

```
$ gt formula show code-quality

Formula: code-quality
Type:    convoy
Version: 1
Tier:    system (embedded)

Description:
  Comprehensive codebase quality analysis via parallel specialized reviewers.

Inputs:
  scope   string  Analysis scope: 'delta' or 'full'  [default: delta]
  rig     string  Target rig to analyze              [required]
  preset  string  Leg preset: quick|standard|full    [default: quick]

Presets:
  quick     3 legs  ~5 min   Daily delta analysis
  standard  5 legs  ~10 min  After big merges
  full      7 legs  ~20 min  Weekly comprehensive audit

Legs (7):
  dead-code      Unreachable functions, unused exports
  dep-health     Outdated deps, known vulnerabilities
  architecture   Package coupling, layering violations
  test-quality   Coverage gaps, tautological tests
  complexity     Cyclomatic hotspots, function length
  documentation  Doc coverage, stale docs
  debt-trend     TODO accumulation, tech debt velocity
```

## Constraints Identified

1. **No new top-level commands.** Gas Town CLI already has 7 command groups and
   dozens of subcommands. Adding `gt quality` would require justification beyond
   "convenience." The existing `gt plugin` and `gt formula` commands handle all
   interaction patterns.

2. **Condition gate check must be fast (<5s).** The Deacon evaluates gates
   during patrol; slow checks degrade patrol cycle time. The check script
   queries beads (one bd list call) and git log (one git command). Both should
   complete in <2s.

3. **Plugin instructions are the sole interface for Dog agents.** Dogs read
   `FormatMailBody()` output — they have no other context. Instructions must be
   self-contained and unambiguous. The Dog cannot ask clarifying questions.

4. **Formula convoy legs spawn as separate polecats.** Each leg is fully
   independent with its own worktree. Legs cannot communicate during execution.
   All coordination happens at the synthesis step, which runs after all legs
   complete.

5. **Output must be committable.** Results written to the worktree become part
   of the merge request. Non-text outputs (images, binary data) are not
   suitable. Markdown is the canonical output format.

6. **The `gt plugin sync` command is the deployment mechanism.** New plugins in
   the gastown source tree deploy to `~/gt/plugins/` via sync. This means the
   plugin must work as a static file (no build step, no compiled dependencies).

## Open Questions

1. **Should the check script be inlined or extracted?** The plugin.md format
   supports inline `check = "..."` for simple gates. A multi-line condition
   check (like ours) is better as an external `check.sh` file. Does the
   scanner resolve relative paths for check scripts?

2. **How should the synthesis step access leg outputs?** The code-review formula
   uses the convoy's synthesis step convention (read from `.reviews/<id>/`). For
   quality analysis, the synthesis step needs to find `.quality/<date>/*.md`
   files. Should the formula inject the output path as a variable, or should
   synthesis discover files by convention?

3. **Should results persist across runs for trending?** A single run's output
   is committed to the repo. But trending (score-over-time) requires querying
   historical runs. Should each run also emit a pinned bead with structured
   labels (`score:7.2`, `dimension:architecture:8`) for querying, in addition
   to the committed markdown file?

4. **Multi-rig orchestration**: If the operator wants quality analysis across
   all rigs (not just one), should the plugin dispatch one convoy per rig, or
   should the formula support multi-rig as an input? Current convoy formulas
   target a single rig.

## Integration Points

### → Plugin System (`internal/plugin/`)
- Plugin definition follows existing `plugin.md` format exactly
- `condition` gate with external check script (check field points to relative path)
- `agent` execution type — Dog interprets markdown instructions
- Recording via `Recorder.RecordRun()` — labels follow convention
- History and cooldown evaluation via existing `Recorder.CountRunsSince()`

### → Formula System (`internal/formula/`)
- New convoy formula embedded at Tier 3 (`internal/formula/formulas/code-quality.formula.toml`)
- Three presets: quick (3 legs), standard (5 legs), full (7 legs)
- Inputs: scope, rig, preset, since, output_dir
- Follows `code-review.formula.toml` convoy structure exactly
- Three-tier resolution allows per-rig override of legs/presets

### → CLI Commands (`internal/cmd/plugin.go`, `internal/cmd/formula.go`)
- Zero changes to existing commands
- Plugin appears in `gt plugin list` output automatically (scanner discovers it)
- Formula appears in `gt formula list` output automatically (embedded)
- `gt plugin run`, `gt plugin show`, `gt plugin history` work unchanged
- `gt formula run`, `gt formula show` work unchanged

### → Deacon Patrol (gate evaluation)
- Condition gate: Deacon runs `check.sh`, exit 0 → dispatches plugin to Dog
- No changes to gate evaluator — `GateCondition` type already exists
- Check script uses `bd list` (existing command) for cooldown logic

### → Dog Dispatch (plugin execution)
- `FormatMailBody()` formats instructions for Dog consumption
- Dog reads instructions → slings convoy formula → reports result
- No changes to dispatch mechanism — follows `FormatMailBody()` contract

### → Output Convention
- Results written to `.quality/<date>/` in rig worktree
- Follows existing `.reviews/<id>/` pattern from code-review convoy
- Synthesis step produces `report.md` — unified findings document
- Committed via standard polecat `gt done` → merge queue flow

### → Data Model (integration with convoy's data-model leg)
- Quality scores stored as labels on plugin-run receipt beads
- Enables `bd list -l plugin:code-quality --json | jq` queries for trending
- Historical reports committed to git enable `git log --follow .quality/` archaeology

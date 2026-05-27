# Integration Analysis

## Summary

The proposed "automated code quality analysis" plugin-and-formula system fits
cleanly into Gas Town's existing architecture because **both substrates already
exist and are production-proven.** Plugins (periodic Deacon patrol tasks gated
by cooldown/cron/condition) handle the *trigger* and *scheduling* dimension;
formulas (structured workflow templates with dependency tracking, parallel
execution, and synthesis) handle the *execution* dimension. The new feature is
essentially a **specific plugin definition** that, when its gate opens, slings a
**specific convoy formula** to analyze code quality across dimensions in
parallel. No new runtime infrastructure is required — only new `.toml` files and
a thin coordination layer.

The integration surface is narrow: one new plugin definition (with gate type),
one new convoy formula (with legs for different quality aspects), and one new
formula-dispatch pattern in the Deacon patrol that connects them. The existing
`code-review.formula.toml` convoy already demonstrates this exact shape with 10
parallel legs, synthesis, and presets. The existing `github-sheriff` plugin
demonstrates the trigger-to-bead pattern. This feature composes both patterns.

## Analysis

### Key Considerations

- **Plugin + Formula composition is the canonical Gas Town automation pattern.**
  The Deacon patrols plugins; plugins either instruct a Dog directly (markdown
  instructions) or trigger formula slinging. Auto-test-PR already proved this
  with `mol-auto-test-pr-cycle` slung from a standing patrol.

- **The `code-review.formula.toml` convoy already solves 80% of the problem.**
  It has 10 parallel review legs (correctness, performance, security, elegance,
  resilience, style, smells, wiring, commit-discipline, test-quality), presets
  for light vs full review, and synthesis. Automated code quality analysis
  either extends this formula or creates a new one with different legs.

- **Gate type is the key design decision.** The existing gate types are:
  - `cooldown` — time since last run (e.g., "don't run more than once per hour")
  - `cron` — schedule-based (e.g., "every Monday at 9am")
  - `condition` — shell command exit code (e.g., "run only if git log shows new commits")
  - `event` — system events (e.g., "startup")
  - `manual` — never auto-runs

  For automated code quality, the trigger should be **condition-based** (new
  commits on main since last analysis) or **cooldown-based** (run at most once
  per day). A combined approach (cooldown + condition) would be ideal but
  requires either a composite gate type or a condition gate whose check script
  embeds its own cooldown logic.

- **Plugin execution types are well-suited.** Three modes exist:
  - `agent` (default) — Dog interprets markdown instructions
  - `script` — `run.sh` executed directly
  - `exec-wrapper` — wraps session startup

  For quality analysis, `agent` mode is correct: the Dog reads the plugin
  instructions, slings the convoy formula, and reports results. The `script`
  mode could work for a deterministic check, but the analysis itself needs
  agent intelligence to interpret findings.

- **Recording and cooldown evaluation are production-ready.** The
  `plugin.Recorder` creates ephemeral beads with labels like
  `type:plugin-run, plugin:<name>, result:<outcome>` and immediately closes
  them. The cooldown gate evaluator queries these receipts via
  `CountRunsSince()`. No new recording infrastructure needed.

- **Formula resolution is three-tier and allows per-rig customization.**
  Tier 1 (rig-level `.beads/formulas/`) > Tier 2 (town-level
  `.beads/formulas/`) > Tier 3 (embedded in binary). A quality analysis formula
  embedded in the binary provides the default; rigs can override with
  project-specific legs or different presets.

### Options Explored

#### Option 1: New Plugin + Existing Code-Review Formula (Reuse)

- **Description**: Create a new plugin (`code-quality-analysis`) that triggers
  the existing `code-review.formula.toml` convoy with a "full" or custom preset.
  The plugin's gate controls timing; the formula handles execution.
- **Pros**:
  - Zero new formula code — maximum reuse
  - The code-review convoy already has all relevant quality dimensions
  - Presets allow light (gate) vs full (weekly) analysis
  - Battle-tested synthesis step
- **Cons**:
  - code-review formula is designed for PR diffs, not whole-codebase analysis
  - May need `--branch` or `--files` scoping that doesn't fit periodic analysis
  - Preset legs may not match quality-analysis priorities
- **Effort**: Low

#### Option 2: New Plugin + New Quality-Analysis Formula (Dedicated)

- **Description**: Create both a new plugin and a new dedicated convoy formula
  (`code-quality.formula.toml`) with legs specifically designed for periodic
  quality analysis rather than PR review.
- **Pros**:
  - Purpose-built legs (e.g., "dead code", "dependency freshness",
    "documentation coverage", "technical debt trend")
  - Can target whole-codebase or specific directories
  - No awkward fit with PR-centric review legs
  - Can include legs that make no sense for PR review (architecture drift,
    API contract violations, test flakiness trends)
- **Cons**:
  - More code to maintain
  - Some leg overlap with code-review (security, performance, smells)
  - Additional formula file to embed and provision
- **Effort**: Medium

#### Option 3: Extended Plugin Gate + Hybrid Formula Selection

- **Description**: A plugin with a `condition` gate that checks for new commits
  since last analysis. On trigger, the plugin instructions direct the Dog to
  select the appropriate formula based on what changed (targeted review for small
  deltas, full quality audit for large deltas or schedule-based triggers).
- **Pros**:
  - Smart triggering avoids wasted cycles
  - Proportional response (small change → light analysis, weekly → full audit)
  - Reuses formula infrastructure without forcing one formula for all cases
- **Cons**:
  - More complex Dog instructions
  - Relies on Dog agent intelligence for routing
  - Harder to test and validate
- **Effort**: Medium-High

#### Option 4: New Composite Gate Type (Infrastructure Extension)

- **Description**: Extend the gate system to support composite gates
  (`condition AND cooldown`, `cron OR condition`) as a first-class feature.
  Then use this for the quality analysis plugin.
- **Pros**:
  - Enables richer triggering logic for all plugins
  - Clean abstraction — no workaround scripts
  - Useful beyond this feature
- **Cons**:
  - Requires changes to `plugin/types.go`, scanner, and evaluator
  - Scope creep for this design
  - Can be simulated with a condition gate whose check embeds cooldown logic
- **Effort**: High

### Recommendation

**Option 2 (New Plugin + New Dedicated Formula)** with the condition gate
workaround from Option 3's trigger logic embedded in the check script.

Rationale:
1. A dedicated quality-analysis formula is distinct from PR review — different
   legs, different scope, different output expectations.
2. The plugin's condition gate script can embed cooldown logic:
   ```bash
   # Only run if (a) ≥24h since last run AND (b) new commits exist
   last_run=$(bd list --json -l plugin:code-quality --limit=1 | jq -r '.[0].created_at // "1970-01-01"')
   hours_since=$(( ($(date +%s) - $(date -d "$last_run" +%s)) / 3600 ))
   [ "$hours_since" -lt 24 ] && exit 1
   new_commits=$(git log --oneline --since="$last_run" origin/main | wc -l)
   [ "$new_commits" -eq 0 ] && exit 1
   exit 0
   ```
3. This avoids infrastructure changes (no composite gate type needed) while
   delivering smart triggering.
4. Formula legs for quality analysis can include:
   - `coverage-trend` — coverage delta over time
   - `dead-code` — unreachable/unused code
   - `dependency-health` — outdated/vulnerable deps
   - `architecture-drift` — structural violations
   - `test-quality` — tautologies, flakiness (reuse from code-review)
   - `documentation` — doc coverage, staleness
   - `technical-debt` — TODO accumulation, complexity hotspots

## Constraints Identified

1. **Gate evaluation happens in the Deacon's Go code**, not in a shell. The
   `condition` gate type runs a check command and uses exit code. This is
   already implemented in `plugin/types.go` → `GateCondition`. No new gate
   evaluation code is needed for the recommended approach.

2. **Plugin execution is asynchronous.** The Deacon dispatches plugin work to
   Dogs via mail (using `FormatMailBody()`). The Dog executes and records a
   receipt. The Deacon does not wait for results — it checks receipts on next
   patrol via `Recorder.GetLastRun()`. This means quality analysis results are
   available on the *next* patrol cycle, not immediately.

3. **Formula convoy execution requires polecat slots.** Each leg of the convoy
   formula spawns a separate polecat. A 7-leg quality analysis convoy requires
   7 available polecat slots. In a resource-constrained environment, this could
   starve other work. The `auto-dispatch` plugin's scheduler handles this via
   queuing, but latency increases.

4. **The formula registry is embedded at compile time.** New formulas require
   a binary rebuild and reinstall (`gt install`). For rapid iteration, the
   town-level `.beads/formulas/` tier (Tier 2) allows hot-patching without
   rebuild, but production deployment requires embedding.

5. **Plugin sync (`gt plugin sync`) copies from source repo to `~/gt/plugins/`.**
   New plugins in the gastown source tree are deployed via this command. No
   separate deployment mechanism exists.

6. **Deacon patrol frequency limits plugin execution.** The Deacon patrols every
   few minutes (configurable), checking plugin gates on each cycle. A plugin
   cannot run more frequently than the patrol cycle.

## Open Questions

1. **Scope per run: whole codebase or diff-since-last?** A full codebase
   analysis is expensive (7 polecats × N minutes each). A diff-since-last
   analysis is cheaper but misses systemic issues. Should the formula support
   both modes (quick/full), controlled by the trigger context?

2. **Output location: per-rig or centralized?** The code-review formula writes
   to `.reviews/<id>/`. Should quality analysis write to `.quality/<date>/` in
   the rig's worktree (committed to git) or to a pinned bead (queryable via
   `bd` but not in the repo)?

3. **Who consumes the output?** Is this purely informational (digest to
   Mayor/human), or does it feed into automated actions (e.g., creating beads
   for each finding, dispatching fix-it polecats)?

4. **Frequency for pilot rig (gastown_upstream)?** The gastown_upstream rig
   has high commit velocity (~50/day per the hook output). Daily full analysis
   may be too frequent/expensive. Weekly full + daily delta?

5. **Relationship to auto-test-pr?** Auto-test-pr already has quality gates
   (coverage delta, tautology linter, mutation testing). Should quality
   analysis subsume these as its legs, or remain complementary (auto-test-pr
   gates individual PRs, quality analysis audits the codebase holistically)?

## Integration Points

### → Plugin System (`internal/plugin/`)
- New plugin directory: `plugins/code-quality-analysis/plugin.md`
- Gate type: `condition` (with embedded cooldown + new-commit check)
- Execution type: `agent` (Dog interprets instructions to sling formula)
- Recording via existing `Recorder.RecordRun()` — no changes needed

### → Formula System (`internal/formula/`)
- New formula: `code-quality.formula.toml` (convoy type)
- Embedded in binary via `internal/formula/formulas/` directory
- Resolved via three-tier system — rigs can override legs/presets
- Synthesis step combines leg outputs into unified quality report

### → Deacon Patrol (`internal/deacon/`, `mol-deacon-patrol`)
- No code changes — Deacon already discovers and evaluates all plugins
- Plugin gate evaluation already handles `condition` type
- Dog dispatch via `FormatMailBody()` already works for agent-type plugins

### → Sling/Dispatch (`internal/cmd/sling.go`)
- Dog instructions will call `gt sling <formula> <rig>` to dispatch convoy
- `gt sling` already supports convoy formulas (proven by design.formula.toml)
- Each convoy leg creates a bead → slings to available polecat

### → Polecat Work (`mol-polecat-work`)
- Quality analysis legs run inside standard polecat-work formula
- Each leg writes analysis to designated output path
- Standard `gt done` → merge queue flow for committing results

### → Auto-Dispatch Plugin
- Quality analysis beads (convoy legs) are dispatchable by auto-dispatch
- No filter exceptions needed — they look like normal task beads
- Priority can be set lower (P3) to yield to feature work

### → Existing Code-Review Formula
- **Complementary, not competing.** Code-review targets PR diffs (reactive);
  code-quality targets the codebase holistically (proactive)
- Some leg reuse is possible (security, test-quality) via formula composition
  or shared prompt templates

### → Data Dimension (Data Model Design leg)
- Quality analysis results need a schema: findings, severity, location, trend
- Could reuse the code-review output format (Critical/Major/Minor findings)
- Historical trending requires persistent storage (pinned beads or committed files)

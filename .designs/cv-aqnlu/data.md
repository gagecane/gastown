# Data Model Design

## Summary

The automated code quality analysis system needs a data model that bridges two
existing storage substrates: **Dolt** (for operational beads — plugin runs,
receipts, alerts) and **git-committed files** (for quality analysis outputs that
form the auditable historical record). No new storage technology is required.

The key insight is that Gas Town already has a proven data model for plugin
execution tracking (ephemeral beads with labels) and for convoy formula outputs
(files written to `.reviews/<id>/` or `.designs/<id>/` directories). The code
quality analysis system composes both patterns. The only genuinely new data
modeling challenge is **trending** — tracking quality metrics over time so that
regressions can be detected and progress measured.

## Analysis

### Key Considerations

- **Plugin run records** already exist as ephemeral beads with labels
  (`type:plugin-run`, `plugin:<name>`, `result:<outcome>`) via
  `plugin.Recorder`. No new schema needed for trigger tracking.

- **Formula convoy outputs** are git-committed markdown files in a designated
  directory (e.g., `.reviews/<id>/<leg-id>-findings.md`). The code quality
  analysis formula follows this pattern.

- **Trending requires structured data** — markdown findings alone don't support
  time-series queries. We need either (a) structured labels on beads for metric
  extraction, or (b) a dedicated summary file (JSON/TOML) alongside the
  markdown.

- **Dolt is git-for-data** — it supports SQL queries, branching, and
  time-travel. Trending data stored in Dolt beads is queryable via SQL, making
  it ideal for "show me quality score over last 30 days."

- **Data volume**: with daily runs and ~7 legs, one run produces 7 leg findings
  + 1 synthesis + 1 plugin receipt = 9 beads. At daily frequency, that's
  ~3,285 beads/year per rig — well within Dolt's comfort zone.

- **Access patterns** split into two categories:
  1. **Operational** (Deacon patrol): "Has this plugin run recently?" →
     `Recorder.GetLastRun()` / `CountRunsSince()` — already solved
  2. **Analytical** (trending, reports): "Show quality scores over time for rig
     X" → requires either Dolt SQL queries or structured summary files

- **Beads are the universal unit of work in Gas Town.** Every plugin run,
  quality analysis, and finding maps naturally to a bead with typed labels.
  This is not accidental — it's the design philosophy.

### Options Explored

#### Option 1: Beads-Only (Labels as Structured Data)

- **Description**: Store everything as beads with well-structured labels.
  Quality scores, finding counts, and leg results are encoded as labels
  (e.g., `score:0.72`, `critical:2`, `major:5`). Trending is done via
  `bd list --json -l type:quality-run --created-after=-30d`.
- **Pros**:
  - Zero new infrastructure — uses existing Dolt + beads
  - Labels are queryable via `bd list -l <filter>`
  - Automatic time-travel via Dolt branching
  - Receipts and findings coexist in the same substrate
  - Already proven by `quality-review` plugin (labels encode scores)
- **Cons**:
  - Labels have limited expressiveness (key:value strings)
  - Complex queries (e.g., "average score per leg over time") require
    post-processing of `bd list --json` output
  - No native aggregation — must compute trends in plugin instructions
  - Label-based querying doesn't scale to thousands of findings
- **Effort**: Low

#### Option 2: Beads + Git-Committed Summary Files (Hybrid)

- **Description**: Each quality analysis run produces:
  1. Markdown leg findings (git-committed, like code-review outputs)
  2. A structured `summary.json` alongside the findings (git-committed)
  3. An ephemeral receipt bead (Dolt) with key metrics as labels

  The summary.json captures structured data (scores, finding counts per
  severity, per-leg breakdown) that git can track over time via `git log`.
  The bead receipt enables operational queries (cooldown, last-run).
- **Pros**:
  - Human-readable findings in markdown (committed, auditable)
  - Machine-readable summary for trending (committed, diffable)
  - Operational queries via existing beads infrastructure
  - Git history IS the time series — no separate trending store
  - `summary.json` can be read by future automation without Dolt queries
- **Cons**:
  - Two storage substrates to maintain (git + Dolt)
  - `summary.json` format is a schema that must be versioned
  - Git history requires `git log --follow` patterns for querying
  - More files per run = larger repo over time
- **Effort**: Medium

#### Option 3: Dolt-Native Tables (Custom SQL Schema)

- **Description**: Extend Dolt with custom tables for quality metrics:
  `quality_runs (id, rig, timestamp, overall_score)`,
  `quality_findings (run_id, leg, severity, file, line, description)`.
  Direct SQL queries for trending and aggregation.
- **Pros**:
  - Full SQL power for trending (GROUP BY, AVG, window functions)
  - Clean separation of structured data from presentation
  - Efficient aggregation without post-processing
  - Dolt branching gives free "staging" for test runs
- **Cons**:
  - Requires `bd migrate` or manual DDL — new infrastructure
  - Breaks the "everything is a bead" philosophy
  - Beads CLI (`bd`) doesn't support arbitrary tables natively
  - Migration complexity: schema evolution requires careful handling
  - Custom Dolt queries bypass beads abstraction layer
- **Effort**: High

#### Option 4: Pinned Beads as Metric Accumulators

- **Description**: One pinned bead per rig serves as the "quality ledger."
  Each run appends a JSON entry to the bead's description field (or a
  dedicated structured field if beads supports it). The ledger grows over
  time as a single bead with accumulated history.
- **Pros**:
  - Single bead per rig — minimal overhead
  - Pinned beads persist indefinitely (no reaper cleanup)
  - Queryable via `bd show <ledger-id>`
  - Description field supports arbitrary text (including JSON lines)
- **Cons**:
  - Description field was not designed for structured append-only data
  - Concurrency: multiple quality runs could race on the same bead
  - Size growth: after 365 entries, the bead description is large
  - Awkward to query (parse JSON from description text)
  - Violates the expected semantics of a pinned bead
- **Effort**: Low-Medium

### Recommendation

**Option 2: Beads + Git-Committed Summary Files (Hybrid)**

This is the recommended approach because it aligns with how Gas Town already
works for both code reviews (git-committed findings) and plugin tracking
(ephemeral receipt beads), while adding the one thing neither provides alone:
machine-readable trending data.

**Concrete schema:**

```
.quality/<rig>/<YYYY-MM-DD>/
├── correctness.md          # Leg findings (markdown, human-readable)
├── security.md
├── dead-code.md
├── dependency-health.md
├── test-quality.md
├── architecture.md
├── tech-debt.md
├── synthesis.md            # Combined analysis
└── summary.json            # Structured metrics (machine-readable)
```

**summary.json schema (v1):**

```json
{
  "schema_version": 1,
  "run_id": "gu-run-abc123",
  "rig": "gastown_upstream",
  "timestamp": "2026-05-27T09:00:00Z",
  "trigger": "condition",
  "scope": "full",
  "overall_score": 0.78,
  "legs": {
    "correctness": {
      "score": 0.85,
      "findings": { "critical": 0, "major": 1, "minor": 3 }
    },
    "security": {
      "score": 0.72,
      "findings": { "critical": 0, "major": 2, "minor": 1 }
    }
  },
  "totals": {
    "critical": 0,
    "major": 5,
    "minor": 12
  },
  "delta_since_last": {
    "overall_score": +0.03,
    "critical": 0,
    "major": -1,
    "minor": +2
  }
}
```

**Plugin receipt bead (Dolt, ephemeral):**

Labels:
```
type:plugin-run
plugin:code-quality-analysis
result:success
rig:gastown_upstream
score:0.78
critical:0
major:5
minor:12
scope:full
```

This dual-write pattern means:
- **Deacon** checks cooldown via existing `Recorder.CountRunsSince()` — no code changes
- **Trending** reads `summary.json` files via `git log` or directory scan
- **Alerts** use label thresholds (score < 0.45 = BREACH, matching existing quality-review plugin)
- **Human review** reads the markdown findings directly

## Constraints Identified

1. **Beads label values are strings.** Numeric values (scores, counts) must be
   encoded as strings and parsed by consumers. Labels like `score:0.78` work
   but require string-to-float conversion. This is already the pattern used by
   `quality-review` plugin.

2. **Ephemeral beads are closed immediately.** The `Recorder.RecordRun()`
   function creates and closes the receipt in one operation. Trending queries
   must use `--all` flag to include closed beads (already done in
   `queryRuns()`).

3. **Git-committed output directories accumulate.** After 1 year of daily runs,
   `.quality/<rig>/` has ~365 subdirectories. This is manageable but should be
   periodically pruned (keep last 90 days, archive older to a pinned bead or
   external store). The existing `.reviews/` directory doesn't have this problem
   because code reviews are per-PR (bounded), not periodic (unbounded).

4. **Formula convoy legs create beads during execution.** Each leg's polecat
   creates a work bead that passes through the merge queue. For a review-only
   formula (no code commits), legs should be marked `review_only = true` to
   skip MQ processing — this is already supported (see `Formula.ReviewOnly`
   field in `types.go`).

5. **Dolt auto-commit is enabled for Gas Town rigs.** Each bead creation is a
   Dolt commit. With 7 legs + 1 synthesis + 1 receipt = 9 Dolt commits per
   quality run. At daily frequency this is 9/day — negligible for Dolt
   performance.

6. **Plugin gate evaluation happens synchronously in Go.** The `condition` gate
   type runs a shell command and checks exit code. The check script must be
   fast (< 5s) to avoid blocking the patrol cycle. Complex logic (git log
   parsing, last-run lookup) should be optimized or cached.

7. **Formula output directory uses template variables.** The
   `[output] directory = ".quality/{{.rig}}/{{.date}}"` pattern requires these
   variables to be injected at sling time. The existing convoy feed infrastructure
   already supports this (see `convoy_feed_integration_test.go`).

## Open Questions

1. **Retention policy for `.quality/` directories.** How long should historical
   runs persist in git? Options:
   - Keep all (repo grows ~50KB/run × 365 = ~18MB/year per rig)
   - Keep last 90 days, archive older to a compressed tarball
   - Keep last 30 days, rely on Dolt receipt beads for older metrics
   - Git LFS for findings older than N days (probably overkill)

   **Recommendation**: Keep last 90 days in git, let reaper prune older. 18MB/year
   is negligible for most repos; punt retention until it's a real problem.

2. **Schema versioning for `summary.json`.** How do we handle `schema_version`
   bumps? Options:
   - Reader code checks version and migrates in-memory
   - `bd migrate`-style CLI command for format evolution
   - Append-only schema (new fields are always optional)

   **Recommendation**: Append-only schema with `schema_version` field. Readers
   ignore unknown fields and handle missing optional fields with defaults.
   Breaking changes (rare) bump the version and old files remain readable.

3. **Per-leg vs overall score computation.** How is `overall_score` computed
   from leg scores? Options:
   - Simple average of all leg scores
   - Weighted average (security and correctness weighted higher)
   - Worst-leg score (conservative)
   - Custom formula configurable per rig

   **Recommendation**: Weighted average with configurable weights. Default
   weights: security=2x, correctness=2x, others=1x. Configurable in plugin
   instructions or formula input variables.

4. **Relationship to existing `quality-review` plugin.** The existing
   `quality-review` plugin analyzes per-worker merge quality (refinery output).
   The new `code-quality-analysis` plugin analyzes codebase quality (proactive).
   Should they share any data structures?

   **Recommendation**: No shared structures. They serve different purposes:
   - `quality-review`: reactive, per-merge, per-worker trending
   - `code-quality-analysis`: proactive, periodic, per-codebase trending
   Both use the same beads label convention (`score:X`, `result:X`) for
   consistency, but they're independent plugins with independent receipts.

## Integration Points

### → Plugin System (`internal/plugin/`)
- **Receipt creation**: Uses existing `Recorder.RecordRun()` with labels
  encoding quality metrics. No changes to recording infrastructure.
- **Gate evaluation**: Uses existing `condition` gate type. The check script
  queries recent receipts and git log. No new gate type needed.
- **Plugin definition**: Standard `plugin.md` with TOML frontmatter. No
  schema changes to `PluginFrontmatter` struct.

### → Formula System (`internal/formula/`)
- **New convoy formula**: `code-quality.formula.toml` follows the exact pattern
  of `code-review.formula.toml` — legs, synthesis, presets, output config.
- **ReviewOnly flag**: Set `review_only = true` on the formula so legs skip
  the merge queue (analysis-only, no code commits expected).
- **Output directory template**: `.quality/{{.rig}}/{{.date}}` using existing
  template variable injection.

### → Beads (Dolt)
- **Receipts**: Ephemeral beads with labels. Created by plugin execution Dog
  or by the synthesis leg. Standard `bd create --ephemeral`.
- **Trending queries**: `bd list --json --all -l type:plugin-run,plugin:code-quality-analysis --created-after=-30d`
  — same pattern as existing `quality-review` trend analysis.
- **No new tables**: Everything fits in the existing beads schema (id, title,
  description, labels, status, created_at, closed_at).

### → Git Repository
- **Output files**: `.quality/<rig>/<date>/` committed by each leg polecat and
  the synthesis step. Standard `git add` + `git commit` + merge queue flow.
- **Summary.json**: Machine-readable metrics committed alongside findings.
  Enables `git log --oneline .quality/gastown_upstream/` for run history.
- **Growth**: ~50KB per run (7 markdown files + 1 synthesis + 1 JSON). At daily
  frequency: ~18MB/year per rig. Acceptable for most repositories.

### → Deacon Patrol
- **Plugin discovery**: Scanner finds `code-quality-analysis/plugin.md` in the
  plugins directory. No code changes to `Scanner.DiscoverAll()`.
- **Gate evaluation**: Deacon evaluates the `condition` gate by running the
  check script. Existing gate evaluation code handles this.
- **Dog dispatch**: `FormatMailBody()` generates instructions for the Dog to
  sling the quality analysis formula. No changes to dispatch logic.

### → Data Dimension Consumers
- **Quality Review plugin**: Reads quality-analysis receipts to detect
  regressions? Not recommended — keep the two plugins independent.
- **Mayor digest**: Could include quality score in the daily digest via
  label-based querying. Integration point but not a data model dependency.
- **Future automation**: `summary.json` enables programmatic consumption —
  e.g., a plugin that auto-files beads for critical findings, or a dashboard
  that reads committed JSON files.

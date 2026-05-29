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

# Code Quality — Whole-Codebase Periodic Audit

This plugin triggers a parallel multi-leg quality analysis of a rig's codebase.
It composes the existing plugin substrate (gating, dispatch, tracking) with the
existing convoy-formula substrate (parallel legs + synthesis) — no new
infrastructure is required.

The gate is a `condition` check (`check.sh`) that opens only when BOTH:

1. ≥ 6h has elapsed since the last successful run (cooldown), AND
2. New commits have landed on the rig's default branch since the last run
   (skips re-running when the codebase is unchanged).

When the gate opens, the Deacon dispatches a Dog that slings the
`code-quality` convoy formula. The formula spawns up to 7 parallel polecats
(one per analysis dimension), bounded by the formula's `max_parallelism`
hint to avoid starving feature work. A synthesis step then combines the
findings into a unified report.

Output lives at `.quality/<date>/`:
- `<leg>.md` — per-leg findings (markdown, human-readable)
- `quality-report.md` — synthesized report
- `summary.json` — machine-readable summary for trending

Only **critical** findings file beads. Sub-critical findings stay in the
report (signal-to-noise discipline; see code-scout's 5-bead/cycle precedent).

## Manual trigger

```bash
gt plugin run code-quality              # Run if gate allows
gt plugin run code-quality --force      # Bypass gate (force a run)
gt plugin run code-quality --dry-run    # Show what would happen
```

To run a different scope manually, invoke the formula directly:

```bash
gt formula run code-quality --preset=quick      # ~5 min, daily gate
gt formula run code-quality --preset=standard   # ~10 min
gt formula run code-quality --preset=full       # ~20 min, weekly
```

## Step 1: Resolve target rig and date

The Dog runs from town root. Determine which rig to analyze:

- Default: the rig that owns this plugin (typically the rig the plugin lives in).
- If invoked with `--rig=<name>`, use that rig.

Compute the run date as the local date in `YYYY-MM-DD` form:

```bash
RUN_DATE=$(date +%Y-%m-%d)
RIG="${RIG:-gastown_upstream}"
OUTPUT_DIR=".quality/${RUN_DATE}"
```

If `.quality/${RUN_DATE}/quality-report.md` already exists for this rig, the
condition gate (Step 0) should have skipped this run. If it didn't and the
report is from a prior run today, append `-N` to the date directory to avoid
clobbering history.

## Step 2: Sling the code-quality convoy formula

Stage the convoy via the existing formula machinery:

```bash
gt formula run code-quality \
  --rig "$RIG" \
  --preset standard \
  --output-dir "$OUTPUT_DIR"
```

The formula handles:
- Spawning one polecat per selected leg (subject to `max_parallelism`).
- Each leg writes findings to `${OUTPUT_DIR}/<leg-id>.md`.
- The synthesis step reads all leg outputs, produces `quality-report.md` and
  `summary.json`, and files critical-finding beads (if any).

The formula is `review_only = true`, so individual leg work does not pass
through the merge queue. The synthesis step commits `${OUTPUT_DIR}/*` as a
single commit on a normal branch and ships it through the merge queue like
any other code change.

## Step 3: Record the run

After synthesis completes, record an ephemeral receipt bead. The labels
encode the metrics needed for the cooldown gate AND for trend queries:

```bash
SCORE=$(jq -r '.overall_score' "${OUTPUT_DIR}/summary.json")
CRIT=$(jq -r '.totals.critical' "${OUTPUT_DIR}/summary.json")
MAJOR=$(jq -r '.totals.major' "${OUTPUT_DIR}/summary.json")
MINOR=$(jq -r '.totals.minor' "${OUTPUT_DIR}/summary.json")
COMMIT=$(git -C "$RIG_ROOT" rev-parse HEAD)

bd create "code-quality: rig=$RIG score=$SCORE crit=$CRIT major=$MAJOR" \
  -t chore --ephemeral \
  -l "type:plugin-run,plugin:code-quality,result:success,rig:$RIG,score:$SCORE,critical:$CRIT,major:$MAJOR,minor:$MINOR,commit:$COMMIT" \
  -d "Run: $RUN_DATE
Rig: $RIG
Overall score: $SCORE
Findings: $CRIT critical / $MAJOR major / $MINOR minor
Report: $OUTPUT_DIR/quality-report.md" \
  --silent 2>/dev/null || true
```

The `commit:<sha>` label is what `check.sh` reads in the next gate
evaluation to determine whether new commits exist (Step 0 of the gate).

## Step 4: Handle failures

If any leg crashes or the synthesis step fails:

```bash
bd create "code-quality: FAILED rig=$RIG" \
  -t chore --ephemeral \
  -l "type:plugin-run,plugin:code-quality,result:failure,rig:$RIG" \
  -d "Run: $RUN_DATE
Rig: $RIG
Failure: <leg-id or synthesis>
Error: <truncated error>" \
  --silent 2>/dev/null || true

gt escalate "Plugin FAILED: code-quality" \
  --severity medium \
  --reason "code-quality run for rig $RIG failed: <error>"
```

A failure receipt is what causes the cooldown branch of `check.sh` to skip
re-running for the cooldown window — failed runs should not retry on every
patrol tick (the failure is likely deterministic). The escalation path
ensures a human/mayor can investigate.

## Relationship to other plugins

This plugin is **not** the same as `quality-review`. Distinction:

| Plugin            | Scope                          | Trigger    | Output                      |
|-------------------|--------------------------------|------------|-----------------------------|
| `code-quality`    | Whole codebase, periodic       | condition  | `.quality/<date>/` reports  |
| `quality-review`  | Per-merge, per-worker trend    | cooldown 6h| Worker-trend alerts         |
| `code-scout`      | Per-finding bead creation      | cooldown 4h| One bead per improvement    |

`code-quality` is holistic and trend-aware; `code-scout` is incremental and
bead-per-finding; `quality-review` is per-worker reputation. They are
complementary, not competing.

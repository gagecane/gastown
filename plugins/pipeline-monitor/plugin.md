+++
name = "pipeline-monitor"
description = "Check Amazon Pipeline health and file P1 beads for blockers, routed to the package-owning rig, with drift-resistant cross-rig dedupe"
version = 3

[gate]
type = "cooldown"
duration = "1h"

[tracking]
labels = ["plugin:pipeline-monitor", "category:ci"]
digest = true

[execution]
timeout = "5m"
notify_on_failure = true
severity = "high"
+++

# Pipeline Monitor

Check pipeline health and file actionable beads in the rig that **owns the failing package**, so a polecat in that rig can push the fix.

**v3 — Drift-resistant dedupe.** Per-run duplicates are avoided using a stable
fingerprint (pipeline × package × failure_type × root_cause_category) that
ignores build IDs, timestamps, and specific version strings. Dedupe searches
**every rig**, not just the chosen one, so a cycle that routes differently than
a prior cycle still finds the prior bead.

## Pipelines to Monitor

| Pipeline | Default Bead Rig | Default Prefix |
|---|---|---|
| CodegenAgentScheduler-development | codegen_ws | cws |

The default rig is only used for pipeline-level failures that cannot be attributed to a single package (for example, workflow config issues, orchestration errors).

## Package → Rig Routing

Package-scoped build/test failures MUST be filed in the rig whose remote owns that package. Unknown packages fall back to the default rig above.

| Failing Package | Target Rig | Prefix |
|---|---|---|
| CodegenAgentSchedulerCDK | casc_cdk | cadk |
| CodegenAgentSchedulerConstructs | casc_constructs | caco |
| CodegenAgentSchedulerCrudLambda | casc_crud | cacr |
| CodegenAgentSchedulerWebAppE2ETests | casc_e2e | cae2 |
| CodegenAgentSchedulerIntegTests | casc_integ | cait |
| CodegenAgentSchedulerLambda | casc_lambda | cala |
| CodegenAgentSchedulerShared | casc_shared | cass |
| CodegenAgentSchedulerWebApp | casc_webapp | casw |
| _(unknown / pipeline-level)_ | codegen_ws | cws |

## Step 1: Check Health

Use the `GetPipelineHealth` MCP tool:

```
GetPipelineHealth(pipelineNames: ["CodegenAgentScheduler-development"])
```

If `isBlocked` is false and all health metrics are zero → record success and exit.

## Step 2: Diagnose Failures

For each failure type found:

**Failed builds:** Use `GetPipelineDetails` with `includeFailedBuilds: true` to get the build request ID, then `BrazilPackageBuilderAnalyzerTool` with that request ID to identify the **failing package name** and root cause. Capture the package name — it determines where the bead goes.

**Failed deployments:** Use `GetPipelineDetails` with `includeFailedDeployments: true`. Deployment failures are usually pipeline-level; file in the default rig unless the root cause clearly points at a single package.

**Failed tests:** Use `GetPipelineDetails` with `includeFailedTests: true`. Test packages (for example `CodegenAgentSchedulerIntegTests`, `CodegenAgentSchedulerWebAppE2ETests`) route to their own rigs per the table.

## Step 3: Pick the Rig

Look up the failing package in the **Package → Rig Routing** table.

- Exact match → use that rig + prefix
- No match → use the pipeline's default rig (codegen_ws) and include a note in the bead description asking for routing table maintenance

## Step 4: Compute Fingerprint

Before searching for duplicates, derive a stable **fingerprint** from the failure.
The fingerprint must be invariant under build-ID drift, timestamp drift, and
version-string drift. Only the four dimensions below go into it.

### Fingerprint dimensions

| Dimension | Source | Example values |
|---|---|---|
| `pipeline` | pipeline name | `CodegenAgentScheduler-development` |
| `package` | failing package (or `_pipeline_` for pipeline-level failures) | `CodegenAgentSchedulerWebApp`, `_pipeline_` |
| `failure_type` | one of `build`, `deploy`, `test` | `build` |
| `root_cause_category` | coarse bucket derived from analyzer output (see below) | `npm-registry-missing-version` |

### Root cause category taxonomy

Map the analyzer output (or deploy/test error summary) to exactly **one** of these
buckets. If nothing matches, use `unknown` and include the raw analyzer summary
in the bead notes.

| Category | Canonical signals |
|---|---|
| `npm-registry-missing-version` | `ETARGET`, `No matching version found for <pkg>@<ver>`, `not in registry` |
| `npm-install-error` | npm install failures that aren't ETARGET (network, peer deps, etc.) |
| `brazil-build-error` | compile/link errors, `brazil-build` non-zero exit, missing symbol |
| `brazil-version-set-error` | `version-set` resolution failures, cycle detection, missing major version |
| `test-failure` | deterministic test assertion failure |
| `test-flake` | intermittent failure (retry succeeded, timing-dependent pattern) |
| `test-timeout` | test exceeded timeout without failing an assertion |
| `deploy-timeout` | deployment exceeded stage timeout |
| `deploy-rollback` | automatic rollback triggered (alarm, health-check) |
| `deploy-script-failure` | pre/post-deploy script returned non-zero |
| `unknown` | nothing above applied — raw summary goes in notes |

**Important:** Do NOT include the version string, build ID, timestamp, or any
other per-run detail in the root cause category. `postcss@8.5.11 not in registry`
and `postcss@8.5.13 not in registry` both map to
`npm-registry-missing-version`.

### Fingerprint string

Concatenate with `::`:

```
<pipeline>::<package>::<failure_type>::<root_cause_category>
```

Examples:

- `CodegenAgentScheduler-development::CodegenAgentSchedulerWebApp::build::npm-registry-missing-version`
- `CodegenAgentScheduler-development::_pipeline_::deploy::deploy-timeout`
- `CodegenAgentScheduler-development::CodegenAgentSchedulerIntegTests::test::test-flake`

Carry this string through the rest of the run. Store it on any bead you touch
as a label: `fingerprint:<SHA1-first-12>` (SHA1 of the fingerprint string,
truncated to 12 hex chars — keeps the label short and avoids label-length
issues while still being unique in practice).

To compute in shell:

```bash
FP_STRING="<pipeline>::<package>::<failure_type>::<root_cause_category>"
FP_HASH="$(printf '%s' "$FP_STRING" | sha1sum | cut -c1-12)"
FP_LABEL="fingerprint:${FP_HASH}"
```

## Step 5: Dedupe — Cross-Rig Search

Search **every rig** for an existing open bead with the same fingerprint. The
registry of rigs lives in `~/gt/rigs.json`; iterate through its keys plus the
town root (`.`). Missing rigs are skipped.

### 5a. Primary lookup: fingerprint label

For each rig, query for an open bead carrying the fingerprint label:

```bash
FP_HASH="$(printf '%s' "$FP_STRING" | sha1sum | cut -c1-12)"

for RIG in $(jq -r '.rigs | keys[]' ~/gt/rigs.json) .; do
  DIR="$HOME/gt/$RIG"
  [ -d "$DIR/.beads" ] || continue
  cd "$DIR" && bd list \
      --label "fingerprint:${FP_HASH},plugin:pipeline-monitor" \
      --status open \
      --json \
    | jq -r --arg rig "$RIG" '.[] | [$rig, .id, .title] | @tsv'
done
```

(The path `.` catches town-root beads; `rigs.json` paths may be relative to
`~/gt/` — check `cd "$HOME/gt/$(jq -r ".rigs[\"$RIG\"].path // \"$RIG\"" ~/gt/rigs.json)"`
if paths ever diverge from rig names.)

**If exactly one match found in any rig → reuse it.** Jump to Step 6 (append to
existing bead). Record which rig it was found in.

**If multiple matches found** (legacy pre-fingerprint beads from older cycles):
reuse the **newest** one (highest `created_at`) and note the duplicates in the
audit bead so a human can merge them later. Do not close the extras automatically.

### 5b. Fallback lookup: legacy / pre-fingerprint beads

A prior cycle may have filed a bead without a fingerprint label (this plugin's
v1/v2 filings). Before giving up and filing a new bead, look for beads that
*likely* match the same failure but lack the label:

```bash
# Same rig + same pipeline-blocker label, open, mentioning the package:
for RIG in $(jq -r '.rigs | keys[]' ~/gt/rigs.json) .; do
  DIR="$HOME/gt/$RIG"
  [ -d "$DIR/.beads" ] || continue
  cd "$DIR" && bd list \
      --label "pipeline-blocker,plugin:pipeline-monitor" \
      --status open \
      --json \
    | jq --arg pkg "$PACKAGE" --arg ft "$FAILURE_TYPE" --arg rig "$RIG" \
         '.[] | select((.description // "") | test("Package: " + $pkg))
              | select((.description // "") | test("Failure type: " + $ft))
              | [$rig, .id, .title] | @tsv' -r
done
```

If any legacy match is found, treat it as the reused bead (Step 6 path) and
**add the fingerprint label to it** so future cycles hit the fast path:

```bash
cd "$HOME/gt/$FOUND_RIG" && bd update "$FOUND_ID" \
  --add-label "fingerprint:${FP_HASH}"
```

### 5c. Rig-mismatch handling

If the reused bead is in a **different rig** than the one Step 3 selected for
this cycle, do NOT create a new bead in the correct rig. Log a warning in the
audit bead (Step 8) and continue with the existing bead in place:

```
WARN: fingerprint=<hash> pipeline=<p> package=<pkg> cycle_routed_to=<rig_now>
      but existing bead <found_id> is in rig=<rig_existing>. Appending to
      existing; no new bead filed. Routing-table drift may require cleanup.
```

The rationale: the point of dedupe is to avoid duplicate work. Re-filing in the
"correct" rig creates the duplicate we're trying to prevent. A human (or a
follow-up cleanup task) can migrate the bead if the new routing is permanent.

### 5d. No match anywhere → file new bead (Step 7)

## Step 6: Reuse Existing Bead

When Step 5 found a match, append a drift-history note rather than creating a new bead:

```bash
cd "$HOME/gt/$FOUND_RIG" && bd note "$FOUND_ID" \
  "cycle $(date -u +%Y-%m-%dT%H:%M:%SZ): still failing. \
Build/Deploy ID: <new_id>. \
Title-at-cycle: \"<current_summary_line>\". \
Version-at-cycle: <version_string_if_any>. \
fingerprint=${FP_HASH}"
```

**What goes in the note:**
- Current cycle timestamp
- New build/deploy ID (drifts every cycle)
- Current human-readable summary (drifts when specifics change)
- Version string or other drifting specifics, if present
- The fingerprint hash (so the note is self-describing)

**What does NOT go in the note:** anything already captured by the fingerprint
(pipeline, package, failure_type, category) — that's invariant, no need to
repeat it per cycle.

Skip Step 7. Go to Step 8 (audit trail).

## Step 7: File New Bead

Only reached when Step 5 found **no** match in any rig.

```bash
cd "$HOME/gt/$CHOSEN_RIG" && bd create \
  "<short description of failure>" \
  -p P1 \
  -t task \
  -l "pipeline-blocker,plugin:pipeline-monitor,fingerprint:${FP_HASH}" \
  -d "Pipeline: <name>
Failure type: <build|deploy|test>
Package: <package name>
Root-cause category: <category from Step 4 taxonomy>
Fingerprint: ${FP_STRING}
Fingerprint hash: ${FP_HASH}

Current cycle:
  Build/Deploy ID: <id>
  Summary: <one-line summary from analysis>
  URL: <build.amazon.com or pipelines.amazon.dev link>

Routed to ${CHOSEN_RIG} because the failing package is owned by that rig's remote.
(Or: routed to the default rig because the failing package is not in the routing table — add it.)

Subsequent cycles will append drift history as notes; see notes for per-cycle build IDs and titles."
```

## Step 8: Record Audit Trail

Record the plugin-run summary in the **default rig** (codegen_ws) so there is
one canonical audit trail per run. Include the fingerprint and reused bead ID
so runs are traceable and rig-mismatch warnings are preserved:

```bash
cd "$HOME/gt/codegen_ws" && bd create \
  "pipeline-monitor: <N pipelines checked, M blockers (K reused, L new)>" \
  -t chore --ephemeral \
  -l "type:plugin-run,plugin:pipeline-monitor,result:success,fingerprint:${FP_HASH}" \
  -d "Pipelines checked: <list>
Blockers found: <count>
  - Reused bead: <rig>/<id>  fingerprint=${FP_HASH}
  - New bead: <rig>/<id>     fingerprint=${FP_HASH}
Rig-mismatch warnings: <any from Step 5c>" \
  --silent
```

If this run had no failures, the audit bead is still filed (so cooldown gating
has a record) but with `result:success` and no fingerprint label.

Then run `gt dog done`.

## Self-Check Scenarios (test matrix)

The following scenarios exercise the dedupe logic. A future cycle should
produce the stated outcome; if it doesn't, the plugin has regressed.

### S1: Same-rig exact match

- Prior cycle filed `casw-X` in `casc_webapp` with fingerprint `FP-A`
- Current cycle computes the same `FP-A`
- Step 5a finds `casw-X` → reuse. Append note. No new bead.

### S2: Same-rig title drift

- Prior: `casw-X` titled "build blocked: postcss@8.5.11 not in registry",
  fingerprint `FP-A` (category `npm-registry-missing-version`)
- Current: title would be "build blocked: postcss@8.5.12 not in registry"
- Current fingerprint is still `FP-A` (version dropped from category) →
  Step 5a finds `casw-X` → reuse. The new version string goes in the note.

### S3: Same-rig version drift

- Prior: `casw-X` for `CodegenAgentSchedulerWebApp :: build ::
  npm-registry-missing-version`
- Current: same pipeline, same package, same build failure, different version
- Fingerprint unchanged. Reuse.

### S4: Cross-rig same cause (routing table changed mid-cycle)

- Prior: `cws-X` filed in `codegen_ws` (old default-fallback routing)
  with fingerprint `FP-A`
- Routing table updated; the package now routes to `casc_webapp`
- Current cycle would route to `casc_webapp`
- Step 5a searches **all** rigs and finds `cws-X` in `codegen_ws`.
- Step 5c emits a rig-mismatch warning but reuses `cws-X`.
- No new bead in `casc_webapp`. Audit bead logs the warning.

### S5: Open-in-rigA, closed-in-rigB

- `casw-X` is open in `casc_webapp` with `FP-A`
- `cws-Y` (same `FP-A`) was filed-and-closed in `codegen_ws` last week
- Current cycle: Step 5a `--status open` only matches `casw-X`, ignores closed `cws-Y`.
- Reuse `casw-X`. Do not re-open `cws-Y`. Do not file new.

### S6: Legacy bead without fingerprint label

- `casw-Z` was filed by plugin v1/v2, no `fingerprint:*` label, open, same
  pipeline/package/failure type
- Current cycle: Step 5a (label match) returns empty
- Step 5b (description match) finds `casw-Z`.
- Reuse `casw-Z`, **and** add the `fingerprint:${FP_HASH}` label so next cycle
  hits the fast path.

### S7: No prior bead anywhere

- First occurrence of this fingerprint
- Steps 5a and 5b both empty
- Step 7 files a new bead in the chosen rig with the fingerprint label.

### S8: Multiple legacy beads for same cause

- Two or more open beads match Step 5b (legacy duplicates)
- Reuse the newest one, add the fingerprint label to it.
- Note the extras in the audit bead (Step 8) so a human can dedupe.

## Rationale

Filing pipeline-blocker beads in the rig that owns the code lets polecats in
that rig push the actual fix to the upstream Amazon git repo. Filing them in
`codegen_ws` (whose remote is a workspace-metadata repo with no application
code) creates an unfixable loop: the bead cycles hourly, polecats spawn, no
polecat can push a fix, and escalations pile up (for example cws-qw2 →
gt-wisp-tj63).

**Why fingerprints instead of exact-title matching (v3 change):** Failure
summary lines embed build IDs, timestamps, and specific version strings. Any
change defeats an exact-title comparison, so hourly cycles produced duplicates
every time the specifics drifted. The fingerprint strips those details and
keeps only the four invariant dimensions that actually define "same failure."

**Why cross-rig search (v3 change):** The routing table is authoritative but
mutable. A package may have been filed in `codegen_ws` (legacy default-fallback
routing) and later routes to `casc_webapp` when the table is updated. A
rig-local dedupe search misses that and files a duplicate in the "new" rig.
Searching every rig catches that case and emits a warning instead of filing a
duplicate.

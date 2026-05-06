+++
name = "pipeline-monitor"
description = "Check Amazon Pipeline health and file P1 beads for blockers, routed to the package-owning rig, with drift-resistant cross-rig dedupe"
version = 4

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

Check pipeline health and file actionable beads in the rig that **owns the fix** (not necessarily the rig whose package appears on the failure line), so a polecat in that rig can push the change.

**v4 — Runtime-error routing + close-resistant dedupe.** Two additions on top of v3:

- When a *test-package* failure is actually a runtime error in the service-under-test (Lambda invocation error, `Cannot find module` with a Lambda call-site, 5xx from a tested endpoint, etc.), the plugin extracts the FunctionName, resolves it to its owning package, and routes to **that** rig. The test-package rig sees no bead unless the assertion itself is wrong. Prevents the "polecat in the test rig diagnoses, declares wrong-rig, closes" cycle that produced ≥5 duplicate cait-* beads in 24h on 2026-05-06 (see cait-x10 sentinel + mail gt-wisp-qfjla).
- Dedupe matches `open,in_progress` (not just `open`), honors explicit `sentinel` / `do-not-dispatch` / `suppress:pipeline-monitor` labels on **any** status, and checks closed/deferred beads within a 7-day grace window. Prevents the close-and-refile loop where a polecat closes a misrouted bead and the next cycle files a fresh duplicate because `--status open` skipped the closed one.

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

Routing has two paths:

1. **Default path (Step 3a):** look up the failing package in the Package → Rig Routing table.
2. **Override path (Step 3b):** for *test-package* failures where the error signature indicates a runtime failure in the service-under-test, resolve the **owning package** of the failing function and route to that rig instead.

### Step 3a: Default package-based routing

Look up the failing package in the **Package → Rig Routing** table.

- Exact match → use that rig + prefix
- No match → use the pipeline's default rig (codegen_ws) and include a note in the bead description asking for routing table maintenance

### Step 3b: Override — runtime-error-owning-package routing for test packages

Only applies when BOTH are true:

- The failing package from Step 2 is a **test package** (listed below), AND
- The failure's error signature matches the **runtime-error taxonomy** below (not a test-logic failure).

#### Test packages (subject to override)

Packages whose purpose is to *drive* a service-under-test rather than contain production code:

- `CodegenAgentSchedulerIntegTests` → integration tests invoking backend Lambdas
- `CodegenAgentSchedulerWebAppE2ETests` → browser tests driving the deployed WebApp + backend

If a future test package is added, extend this list and the FunctionName → Package table below together.

#### Runtime-error signature taxonomy

Classify the failure as **runtime-in-SUT** (service-under-test) if the analyzer output or test artifacts match any of these signals:

| Signal | Canonical patterns |
|---|---|
| Lambda invocation error | `Lambda invocation error`, `InvocationError`, `FunctionError: Unhandled`, `errorType` in JSON response body |
| Missing module at Lambda runtime | `Cannot find module '<name>'` with a stack frame pointing at a Lambda handler or the Lambda runtime (`/var/task/`, `/var/runtime/`, `node_modules` inside the deployed bundle) |
| Service 5xx from invoked endpoint | HTTP `5xx` from an endpoint whose handler is defined in a non-test package; `InternalServerError`, `ServiceException`, `InternalFailure` surfaced from a tested HTTP call |
| Deployment artifact error | Stack-resource name in the error (e.g., `SchedulerApiCrudApiFunction0666D5B4`), or messages like `ResourceNotFoundException: Function not found` where the function belongs to a non-test package |
| Initialization/import-time error in handler | Error raised before any test assertion runs, originating from the Lambda handler module's top-level code |

Classify as **test-logic** (keep default routing to the test rig) if the failure is:

- An assertion (`AssertionError`, `expect(...).toEqual`, Jest/Mocha failure line), OR
- A fixture/setup error inside the test package's own code (`beforeAll` throws, helper in the test package returns unexpected data), OR
- A flake/timeout with no runtime error from the SUT.

If classification is ambiguous, default to **runtime-in-SUT** and let the owning-package rig triage. Filing at the closer-to-the-fix rig is cheaper to reroute than the opposite direction (which is what created the cait-x10 loop).

#### FunctionName → owning package resolution

When a failure is runtime-in-SUT, extract the FunctionName / stack-resource identifier from the error and resolve it to its owning package.

**Primary resolution (CloudFormation):** Given a FunctionName like `CodegenAgentSchedulerDevStack-SchedulerApiCrudApiFunction0666D5B4-AbCd`, inspect the CloudFormation stack that defines it (via the deployed stack template or `aws cloudformation describe-stack-resource`). The logical ID and its CDK construct path point at the owning package. If the CDK construct path contains `@amzn/codegen-agent-scheduler-crud-lambda` (or the equivalent construct name), the owning package is `CodegenAgentSchedulerCrudLambda`.

**Fallback resolution (lookup table):** When CFN inspection is unavailable or fails, match the FunctionName against the **FunctionName → Package** table below. The logical-ID prefix is stable across deployments of the same stack.

| FunctionName logical-ID prefix | Owning package | Rig | Prefix |
|---|---|---|---|
| `SchedulerApiCrudApiFunction` | CodegenAgentSchedulerCrudLambda | casc_crud | cacr |
| `SchedulerApiLambdaFunction` | CodegenAgentSchedulerLambda | casc_lambda | cala |

Keep this table small and evidence-based. Add a row only when a cycle has produced a real FunctionName → Package resolution (CFN or manual confirmation). Rows without evidence create false-positive routes.

**If resolution fails** (FunctionName missing from the error, CFN lookup fails, no table row matches):

1. Fall back to **Step 3a** (default test-package routing).
2. In the bead description, add the raw FunctionName / stack-resource identifier and an explicit note asking for routing-table maintenance: `FunctionName resolution failed: <raw>. Add a row to plugins/pipeline-monitor/plugin.md FunctionName → Package table.`

#### Record the resolution chain

When Step 3b routes via FunctionName resolution (primary or fallback), the bead filed in Step 7 MUST include the resolution chain so future cycles can verify:

```
Resolution chain:
  failing_package: CodegenAgentSchedulerIntegTests (test-package)
  error_signature: Cannot find module (Lambda runtime)
  FunctionName:    SchedulerApiCrudApiFunction0666D5B4
  resolved_via:    cfn-construct-path | fallback-table
  owning_package:  CodegenAgentSchedulerCrudLambda
  target_rig:      casc_crud
```

This block is the audit trail for why the bead landed where it did, and it's the first thing a human reviewer checks when the cycle reroutes.

## Step 4: Compute Fingerprint

Before searching for duplicates, derive a stable **fingerprint** from the failure.
The fingerprint must be invariant under build-ID drift, timestamp drift, and
version-string drift. Only the four dimensions below go into it.

### Fingerprint dimensions

| Dimension | Source | Example values |
|---|---|---|
| `pipeline` | pipeline name | `CodegenAgentScheduler-development` |
| `package` | **resolved owning package** from Step 3 (may differ from the failing-line package for Step 3b reroutes; or `_pipeline_` for pipeline-level failures) | `CodegenAgentSchedulerWebApp`, `CodegenAgentSchedulerCrudLambda`, `_pipeline_` |
| `failure_type` | one of `build`, `deploy`, `test` | `build` |
| `root_cause_category` | coarse bucket derived from analyzer output (see below) | `npm-registry-missing-version` |

**Important:** always use the resolved owning package from Step 3, not the
raw failing-package name. That way a runtime-in-SUT failure routed via
FunctionName resolution produces the SAME fingerprint regardless of which
test package surfaced it first (integ tests on cycle N, E2E tests on cycle
N+1 → same fingerprint → single bead). Using the raw failing package breaks
this dedupe invariant.

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

### 5a. Primary lookup: fingerprint label (active beads)

For each rig, query for an active bead carrying the fingerprint label:

```bash
FP_HASH="$(printf '%s' "$FP_STRING" | sha1sum | cut -c1-12)"

for RIG in $(jq -r '.rigs | keys[]' ~/gt/rigs.json) .; do
  DIR="$HOME/gt/$RIG"
  [ -d "$DIR/.beads" ] || continue
  cd "$DIR" && bd list \
      --label "fingerprint:${FP_HASH},plugin:pipeline-monitor" \
      --status open,in_progress \
      --json \
    | jq -r --arg rig "$RIG" '.[] | [$rig, .id, .title] | @tsv'
done
```

Note the `--status open,in_progress` — both statuses are "actively tracked
work" and should short-circuit duplicate filing. Using `--status open` alone
(pre-v4 behavior) missed any bead a polecat had claimed via
`bd update --status=in_progress` and caused the cycle to file a fresh
duplicate while the first one was being worked on.

(The path `.` catches town-root beads; `rigs.json` paths may be relative to
`~/gt/` — check `cd "$HOME/gt/$(jq -r ".rigs[\"$RIG\"].path // \"$RIG\"" ~/gt/rigs.json)"`
if paths ever diverge from rig names.)

**If exactly one match found in any rig → reuse it.** Jump to Step 6 (append to
existing bead). Record which rig it was found in.

**If multiple matches found** (legacy pre-fingerprint beads from older cycles):
reuse the **newest** one (highest `created_at`) and note the duplicates in the
audit bead so a human can merge them later. Do not close the extras automatically.

### 5b. Suppression-label lookup: sentinels and do-not-dispatch flags

Some beads exist specifically to **suppress re-dispatch** for a failure that is
being tracked in another rig (typical pattern: a misrouted failure has been
diagnosed and moved to its actual owning rig, and a sentinel is left behind in
the original rig to prevent the plugin from re-filing). These beads may be
deferred (often +365d) or closed — in either case `--status open,in_progress`
in 5a does NOT match them.

Search every rig for beads with the fingerprint label AND any of the
suppression labels, regardless of status:

```bash
for RIG in $(jq -r '.rigs | keys[]' ~/gt/rigs.json) .; do
  DIR="$HOME/gt/$RIG"
  [ -d "$DIR/.beads" ] || continue
  cd "$DIR" && bd list \
      --label "fingerprint:${FP_HASH}" \
      --status open,in_progress,closed,deferred \
      --json \
    | jq -r --arg rig "$RIG" \
        '.[]
         | select((.labels // [])
                  | any(. == "sentinel"
                        or . == "do-not-dispatch"
                        or . == "suppress:pipeline-monitor"))
         | [$rig, .id, .title] | @tsv'
done
```

**If any match is found → treat as a dedupe hit.** Do NOT file a new bead. Do
NOT reopen the sentinel (it's intentionally deferred/closed). Append a note
recording this cycle so the history is preserved:

```bash
cd "$HOME/gt/$FOUND_RIG" && bd note "$FOUND_ID" \
  "pipeline-monitor cycle $(date -u +%Y-%m-%dT%H:%M:%SZ): suppressed by sentinel. \
fingerprint=${FP_HASH} pipeline=<name> build_id=<id>"
```

Record the hit in the audit bead (Step 8) and skip to Step 8. Sentinels are
the **preferred explicit mechanism** for humans to signal "stop dispatching
this failure" — they're cheaper and more auditable than the grace-window
heuristic in 5c.

### 5c. Recent-close grace window (7 days)

A polecat may close a misrouted bead as "wrong rig / tracked elsewhere" without
filing a sentinel. The next cycle would then compute the same fingerprint,
miss 5a (closed → not in `open,in_progress`), miss 5b (no sentinel label),
and file a fresh duplicate. That's the close-and-refile loop the sentinel
`cait-x10` was hand-rolled to suppress.

Before falling through to legacy / new-bead paths, check every rig for
closed or deferred beads with the same fingerprint that were updated within
the last 7 days:

```bash
GRACE_CUTOFF="$(date -u -d '7 days ago' +%Y-%m-%dT%H:%M:%SZ)"

for RIG in $(jq -r '.rigs | keys[]' ~/gt/rigs.json) .; do
  DIR="$HOME/gt/$RIG"
  [ -d "$DIR/.beads" ] || continue
  cd "$DIR" && bd list \
      --label "fingerprint:${FP_HASH},plugin:pipeline-monitor" \
      --status closed,deferred \
      --json \
    | jq -r --arg rig "$RIG" --arg cutoff "$GRACE_CUTOFF" \
        '.[] | select((.updated_at // "") >= $cutoff)
             | [$rig, .id, .title, .status, .close_reason // ""] | @tsv'
done
```

**If any match found → treat as a dedupe hit.** The fingerprint is re-firing
within the grace window, which almost always means either:

1. The fix didn't actually land (regression), OR
2. The bead was closed prematurely / misrouted, OR
3. A sentinel should have been filed but wasn't.

Response:

1. Append a note to the closed/deferred bead describing the re-fire:

   ```bash
   cd "$HOME/gt/$FOUND_RIG" && bd note "$FOUND_ID" \
     "pipeline-monitor cycle $(date -u +%Y-%m-%dT%H:%M:%SZ): \
same fingerprint still failing $DELTA after close. \
Close reason was: '$PRIOR_REASON'. \
fingerprint=${FP_HASH} build_id=<id>. \
If this is a real regression, reopen and dispatch. \
If the close was correct and the failure should be suppressed, add a sentinel \
label ('sentinel' + 'do-not-dispatch') so future cycles honor it explicitly."
   ```

2. Do **NOT** auto-reopen. Reopening is invasive and undoes an intentional
   close decision — a human should make that call. The note makes the
   re-fire visible in the bead's history and on the rig dashboard.
3. Do **NOT** file a new bead in this cycle. The note is the signal.
4. Log the grace-window hit as a warning in the audit bead (Step 8):

   ```
   WARN: grace-window hit. fingerprint=<hash> existing_bead=<rig>/<id> \
         status=<closed|deferred> last_updated=<ts> \
         → appended note, did not refile. Consider sentinel if intentional.
   ```

If **no** grace-window match is found, proceed to Step 5d (legacy lookup).

### 5d. Fallback lookup: legacy / pre-fingerprint beads

A prior cycle may have filed a bead without a fingerprint label (this plugin's
v1/v2 filings). Before giving up and filing a new bead, look for beads that
*likely* match the same failure but lack the label:

```bash
# Same rig + same pipeline-blocker label, active, mentioning the package:
for RIG in $(jq -r '.rigs | keys[]' ~/gt/rigs.json) .; do
  DIR="$HOME/gt/$RIG"
  [ -d "$DIR/.beads" ] || continue
  cd "$DIR" && bd list \
      --label "pipeline-blocker,plugin:pipeline-monitor" \
      --status open,in_progress \
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

### 5e. Rig-mismatch handling

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

### 5f. No match anywhere → file new bead (Step 7)

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

Only reached when Step 5 found **no** match in any rig (active, sentinel, or grace-window).

```bash
cd "$HOME/gt/$CHOSEN_RIG" && bd create \
  "<short description of failure>" \
  -p P1 \
  -t task \
  -l "pipeline-blocker,plugin:pipeline-monitor,fingerprint:${FP_HASH}" \
  -d "Pipeline: <name>
Failure type: <build|deploy|test>
Package: <resolved owning package — see Resolution chain below if differs from failing-line package>
Root-cause category: <category from Step 4 taxonomy>
Fingerprint: ${FP_STRING}
Fingerprint hash: ${FP_HASH}

Current cycle:
  Build/Deploy ID: <id>
  Summary: <one-line summary from analysis>
  URL: <build.amazon.com or pipelines.amazon.dev link>

<If Step 3b (runtime-error-in-test override) was used, include the resolution chain block here:>
Resolution chain:
  failing_package: <raw failing-line package, e.g., CodegenAgentSchedulerIntegTests>
  error_signature: <matched taxonomy row, e.g., 'missing module at Lambda runtime'>
  FunctionName:    <extracted logical ID, e.g., SchedulerApiCrudApiFunction0666D5B4>
  resolved_via:    <cfn-construct-path | fallback-table>
  owning_package:  <e.g., CodegenAgentSchedulerCrudLambda>
  target_rig:      <e.g., casc_crud>

Routed to ${CHOSEN_RIG} because the resolved owning package is owned by that rig's remote.
(Or: routed to the default rig because the failing package is not in the routing table — add it.)
(Or: routed via Step 3b FunctionName resolution — the failing-line package is a test package and the error signature indicates a runtime failure in the service-under-test.)

Subsequent cycles will append drift history as notes; see notes for per-cycle build IDs and titles."
```

If Step 3b's FunctionName resolution fell back (table miss or CFN failure),
the bead was routed via Step 3a default instead. Add a line in the description
asking for routing-table maintenance:

```
Routing-table note: FunctionName resolution failed for <raw_function_name>
during Step 3b. Routed via Step 3a default (test-package rig). Add a row to
plugins/pipeline-monitor/plugin.md FunctionName → Package table if this
function belongs to a non-test package.
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

### S9: Runtime error in test package → reroute to owning Lambda rig

- Pipeline failure: `CodegenAgentSchedulerIntegTests` reports 7/9 tests failed
- Error signature in analyzer output: `Lambda invocation error: Cannot find module '@aws-sdk/signature-v4-universal'`, stack points at `/var/task/` inside the Lambda
- Step 2 returns `failing_package = CodegenAgentSchedulerIntegTests`
- Step 3a would route to `casc_integ` (WRONG — fix lives in the Lambda)
- Step 3b detects the test-package + runtime-in-SUT combination, extracts `FunctionName = SchedulerApiCrudApiFunction0666D5B4`, resolves to `CodegenAgentSchedulerCrudLambda`, routes to `casc_crud`.
- Fingerprint uses the resolved owning package: `... :: CodegenAgentSchedulerCrudLambda :: test :: <category>`.
- Step 5 finds no match (first cycle) → Step 7 files in `casc_crud` with the full resolution chain in the description.

This is the canonical test case. The cait-x10 → cacr-cjx cross-rig handoff
documents the reference resolution.

### S10: Runtime error in test package, unknown FunctionName → fallback to test rig

- Same as S9 but the FunctionName is unknown to the lookup table AND CFN inspection fails (e.g., the stack is in an account the plugin can't reach).
- Step 3b resolution fails → falls back to Step 3a → routes to `casc_integ` (test-package rig).
- Step 7 files in `casc_integ` with a `Routing-table note: FunctionName resolution failed for <raw>` line so the human follow-up is clear.
- This is the safety net. Test-rig routing is wrong but **recoverable**: a polecat there can diagnose and hand off, and the description tells a maintainer exactly which row to add to the FunctionName → Package table so the next cycle routes correctly.

### S11: Close-and-refile loop prevented by grace window

- Prior cycle filed `cait-X` in `casc_integ` with fingerprint `FP-R`
- A polecat in `casc_integ` closed `cait-X` with reason `wrong rig, tracked in cacr-Y (casc_crud)` — 3 hours ago
- Current cycle: the underlying CRUD Lambda hasn't been fixed, so the pipeline fails with the same error again. Fingerprint `FP-R` still matches.
- Step 5a (open,in_progress) → empty (cait-X is closed)
- Step 5b (sentinel labels) → empty (polecat didn't file a sentinel)
- Step 5c (grace window, 7d) → finds `cait-X` (closed 3h ago, within 7d)
- Response: append note to `cait-X` recording the re-fire, log grace-window warning in audit bead, do NOT file new bead, do NOT reopen.
- Next human-review cycle sees the note and can either reopen or file an explicit sentinel.
- Pre-v4 behavior: filed a duplicate cait-* every hour. Observed ≥5 duplicates in 24h on 2026-05-06; the hand-rolled cait-x10 sentinel was the pre-fix workaround.

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

**Why runtime-error-owning-package routing (v4 change):** Package-name routing
is correct for compile / build / test-logic failures — the failing-line
package IS the owning package. It is WRONG when the test is correctly reporting
a runtime regression in the service-under-test: the assertion holds, the fix
lives in the Lambda or HTTP handler the test invokes, not in the test package
itself. Without the Step 3b override, polecats in the test rig diagnose the
issue, declare "wrong rig", close, and the next cycle repeats. Observed on
CodegenAgentScheduler-development 2026-05-06: `CodegenAgentSchedulerCrudLambda`
was missing `@aws-sdk/signature-v4-universal` at runtime, breaking 7/9 integ
tests. The plugin routed by failing-line package (`CodegenAgentSchedulerIntegTests`)
to `casc_integ`. Polecats rictus → slit → furiosa (and two more) each
diagnosed and closed as wrong-rig. Total fix work produced: zero. The cait-x10
sentinel was hand-rolled to stop the bleed. Step 3b is the structural fix:
resolve the actual owning package via FunctionName → CFN construct path (or a
fallback lookup table for the known stack) and route to THAT rig.

**Why close-resistant dedupe (v4 change):** `bd list --status open` matches
only literal `status=open`; it excludes `in_progress`, `closed`, and
`deferred`. In the v3 plugin this produced two related bugs:

- **In-flight claim miss:** a polecat that ran `bd update --status=in_progress`
  on a cycle-N bead was invisible to cycle N+1's dedupe. Cycle N+1 filed a
  fresh duplicate while the first polecat was still pushing the fix. Impact:
  wasted spawn + occasional merge-conflict races on the same fix.
- **Close-and-refile loop:** when a polecat closed a misrouted bead (the Bug A
  case above, before Step 3b existed), the next cycle found nothing in
  `--status open` and filed a fresh duplicate. Five+ cycles observed in 24h on
  the cait-x10 fingerprint. Each duplicate burned a polecat that had no way
  to fix anything (wrong rig, no code push permission), then closed again.

The v4 dedupe fix closes both holes:
1. `--status open,in_progress` matches actively-tracked beads.
2. A suppression-label lookup (`sentinel` / `do-not-dispatch` /
   `suppress:pipeline-monitor`) matches any status. This gives humans an
   explicit, auditable mechanism to stop dispatch for a failure that is
   tracked elsewhere (the cait-x10 pattern — now a first-class feature, no
   longer a workaround).
3. A 7-day grace window matches closed/deferred beads with the same
   fingerprint. Re-fires inside the grace window get noted on the existing
   bead (not auto-reopened — humans decide). This is the safety net for when
   nobody filed a sentinel but the close-and-refile loop is starting.

## Migration Notes

**Removing the cait-x10 workaround:** Once v4 is live in the hot pipeline-monitor
agent and S9+S11 have been observed to work end-to-end (i.e., a Step 3b
reroute has produced a bead in `casc_crud` rather than `casc_integ`, AND a
cycle with a closed-bead-in-grace-window has appended a note rather than
refiled), the cait-x10 sentinel can be closed. Its sole purpose was to suppress
the cait-* close-and-refile loop; Step 5b and Step 5c make that suppression
structural. Close reason: `superseded by pipeline-monitor v4 (Step 3b
FunctionName routing + Step 5b/5c close-resistant dedupe)`.

**Canonical reference for Step 3b:** the cacr-cjx bug (CRUD Lambda bundle
missing `@aws-sdk/signature-v4-universal` at runtime) is the reference test
case for FunctionName resolution. The resolution chain
`helpers.invokeLambda()` → `getCrudFunctionName()` →
`SchedulerApiCrudApiFunction0666D5B4` → `CodegenAgentSchedulerCrudLambda` →
`casc_crud` is documented in cait-x10 and mail gt-wisp-qfjla (rictus, 2026-05-05).
Any future regression in Step 3b routing for this specific failure should
reproduce against that case before landing.

**Adding to the FunctionName → Package table:** prefer evidence over
speculation. Add a row only after either (a) a real cycle produced the
FunctionName and you confirmed the owning package via CFN construct path,
or (b) a human reviewed and approved the mapping. Rows without evidence
create false-positive reroutes that are harder to debug than the
unknown-FunctionName fallback in S10.

+++
name = "pipeline-monitor"
description = "Check Amazon Pipeline health and file P1 beads for blockers, routed to the package-owning rig"
version = 2

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

## Step 4: Dedup Check

Before creating a bead, check for existing open beads **in the chosen rig**:

```bash
cd ~/gt/<chosen_rig> && bd list -l pipeline-blocker --status=open --json | jq -r '.[] | .id + "\t" + .title'
```

If an open bead already exists for the same pipeline + package + failure type, skip creation. Add a comment to the existing bead if the failure details have changed (new build ID, timestamp).

## Step 5: File the Bead

```bash
cd ~/gt/<chosen_rig> && bd create "<short description of failure>" \
  -p P1 \
  -t task \
  -l pipeline-blocker,plugin:pipeline-monitor \
  -d "Pipeline: <name>
Failure type: <build|deploy|test>
Package: <package name>
Build/Deploy ID: <id>
Root cause: <one-line summary from analysis>
URL: <build.amazon.com or pipelines.amazon.dev link>

Routed to <chosen_rig> because the failing package is owned by that rig's remote."
```

## Step 6: Record Result

Record the plugin-run summary in the **default rig** (codegen_ws) so there is one canonical audit trail per run:

```bash
cd ~/gt/codegen_ws && bd create "pipeline-monitor: <N pipelines checked, M blockers found>" \
  -t chore --ephemeral \
  -l type:plugin-run,plugin:pipeline-monitor,result:success \
  --silent
```

Then run `gt dog done`.

## Rationale

Filing pipeline-blocker beads in the rig that owns the code lets polecats in that rig push the actual fix to the upstream Amazon git repo. Filing them in `codegen_ws` (whose remote is a workspace-metadata repo with no application code) creates an unfixable loop: the bead cycles hourly, polecats spawn, no polecat can push a fix, and escalations pile up (for example cws-qw2 → gt-wisp-tj63).

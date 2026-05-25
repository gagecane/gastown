# PRD Review: Automatically keep fixes from gastownhall/gastown (upstream) merged into gagecane/gastown (fork) via this local gastown rig

## Executive Summary

The upstream-sync feature is **technically feasible and largely already implemented** — a working plugin (`plugins/sync-upstream/run.sh`), refinery topology preservation (`internal/refinery/fork_sync.go`), and a pre-merge gate (`scripts/check-upstream-rebased.sh`) already exist. However, there is **no actual PRD** — only a one-line problem statement. The biggest risk isn't technical: it's that we may be building a second system alongside one that just needs a config fix. Five of six review legs flagged the absence of formal requirements as a critical gap, and three legs independently discovered that the existing infrastructure covers the described problem. Before any new implementation begins, we need to answer: **what specifically is broken in the existing flow, and is this a 30-minute config fix or a real project?**

## Before You Build: Critical Questions

### [Category: Diagnosis / Scope Definition]

**Q1: What specifically isn't working in the existing sync-upstream plugin?**
- Why this matters: The plugin exists with 7 safety guards, conflict escalation, receipt tracking, and a 6-hour cooldown. If it's just disabled or misconfigured, the "project" is a config fix, not a PRD. Without diagnosis, we risk building a duplicate system.
- Found by: Scope, Feasibility, Requirements (3 legs)
- Suggested answer options: (a) Plugin is disabled (`.disabled` sentinel or label), (b) `crew/gagecane` worktree doesn't exist, (c) Guard 6 (polecat in-flight) perpetually trips, (d) Credentials expired silently, (e) Something else — describe

**Q2: Is the goal "make the existing plugin work reliably" or "design a new system"?**
- Why this matters: This determines whether we're spending 30 minutes or 30 hours. The existing architecture (plugin + refinery + gate) is sound and well-decomposed. If it just needs operational unblocking, no PRD is needed.
- Found by: Scope, Feasibility (2 legs)
- Suggested answer options: (a) Make existing plugin work end-to-end (config fix), (b) Replace plugin with crew-worker dispatch model, (c) Enhance existing plugin with new capabilities (observability, conflict resolution, etc.)

### [Category: Conflict Resolution Policy]

**Q3: When upstream merges conflict with fork changes, what should happen?**
- Why this matters: The fork is ~350 commits ahead with custom features. Textual conflicts are inevitable. The current plugin aborts and escalates — is that sufficient for v1, or do you want automated resolution for trivial conflicts?
- Found by: Feasibility, Requirements, Missing Requirements (3 legs)
- Suggested answer options: (a) Abort + escalate (current behavior, sufficient for v1), (b) Auto-resolve trivial conflicts (import ordering, go.sum) + escalate hard ones, (c) Always merge (accept upstream wins on conflict), (d) Something else

### [Category: Safety Gate]

**Q4: Should clean merges pass CI before being pushed to the fork?**
- Why this matters: A merge can be conflict-free in git terms but semantically broken (upstream renames a function the fork calls). Without CI gating, broken merges land on main and block ALL polecat work until fixed. With CI, there's a ~20-minute delay per sync.
- Found by: Stakeholder, Missing Requirements (2 legs)
- Suggested answer options: (a) Yes — run `go build ./...` + `go test ./...` after merge, before push, (b) No — push immediately, rely on Refinery to catch breakage, (c) Build-only gate (fast, catches compile errors) but skip full tests

### [Category: Success Criteria]

**Q5: What does "done" look like? How do we know sync is working?**
- Why this matters: Without measurable success criteria, there's no way to verify the implementation works or detect degradation. The current spec has no SLA, no metrics, no monitoring.
- Found by: Requirements, Feasibility (2 legs)
- Suggested answer options: (a) "Fork's main is ≤24h behind upstream at all times (for clean merges)", (b) "Plugin runs without error ≥90% of attempts", (c) "Zero manual merge interventions needed per month for clean merges", (d) Define your own

### [Category: Credentials & Access]

**Q6: What identity pushes to `gagecane/gastown` and what happens when credentials expire?**
- Why this matters: If the push token expires, every sync silently skips and the fork diverges without alerting anyone. The spec never defines the credential type, rotation, or failure alerting. This is the most likely reason the plugin isn't working today.
- Found by: Feasibility, Missing Requirements (2 legs)
- Suggested answer options: (a) GitHub PAT with manual rotation + alert-on-failure, (b) GitHub App token with auto-refresh, (c) SSH key (never expires), (d) Don't know — this needs investigation

## Important But Non-Blocking

These should be answered but implementation can start on the critical questions above:

- **Divergence size limits**: Should the plugin refuse to auto-merge if upstream is >N commits ahead? (Suggested: escalate at >50, refuse at >200). *Found by: Feasibility, Missing Requirements.*

- **Refinery coordination / race condition**: The sync plugin and Refinery both push to origin/main. The merge-path uses plain `git push` (no lease), which can race with a concurrent Refinery push. Need a coordination protocol (lock, queue priority, or quiescence-only window). *Found by: Feasibility, Stakeholder, Missing Requirements.*

- **Rollback procedure**: What happens when a clean merge breaks the build post-push? Who reverts? Is force-push authorized for this case? *Found by: Missing Requirements, Stakeholder.*

- **Observability / status command**: Add `gt sync-upstream status` showing last-success, last-attempt, current divergence, consecutive skips. Add alerting for "no successful sync in >N cooldown periods." *Found by: Missing Requirements, Stakeholder.*

- **Bootstrap procedure**: When enabling sync for a rig for the first time, what setup is needed? (Create branch, configure worktree, handle existing divergence.) *Found by: Missing Requirements.*

- **Polecat notification**: When sync pushes to main, in-flight polecats are silently outdated. Should Witness detect this and notify active polecats to rebase? *Found by: Stakeholder.*

- **Skip-list governance**: Who decides what upstream commits to skip? What's the review cadence? No owner named. *Found by: Stakeholder.*

## Observations and Suggestions

- **The architecture is already well-decomposed into three layers**: plugin (periodic merge), gate (enforcement), refinery helper (topology preservation). Future work fits naturally into phase seams: Phase 1 = make existing work; Phase 2 = improve conflict resolution; Phase 3 = add observability.

- **Guard 6 (polecat in-flight check) is likely the reason sync never fires** — in active rigs, a polecat almost always has a hook_bead. If this guard is too conservative, the plugin perpetually skips. Consider: "skip only if a polecat is actively pushing" rather than "skip if any polecat has hooked work."

- **The `check-upstream-rebased.sh` gate creates cascading failure** — if sync falls behind, ALL polecat MRs fail the rebase check and cannot land. This makes sync reliability a critical-path dependency for the entire rig.

- **The ambiguity leg (gu-leg-pfvh2) analyzed the wrong PRD** (auto-test-pr instead of upstream-sync). Its findings don't apply to this review. A re-run against the correct target would strengthen confidence in ambiguity coverage.

- **Two opt-out mechanisms exist** (`.disabled` file + bead label) with different discovery paths. Consolidate to one to avoid "I disabled it but it kept running" confusion.

- **The `git reset --hard` usage in run.sh** contradicts the project's own CLAUDE.md safety guidelines. Consider `git checkout` or `git switch --discard-changes`.

## Confidence Assessment

| Dimension | Score | Notes |
|-----------|-------|-------|
| Requirements completeness | **Low** | No PRD exists; only a one-line problem statement. Cannot build from this. |
| Technical feasibility | **High** | Already implemented. No technical impossibilities. |
| Scope clarity | **Medium** | Narrow by construction (one remote, one direction), but actual work size depends on diagnosis of existing failure. |
| Ambiguity level | **Medium** | Key terms undefined (what's a "fix"? what's "automatic"?), but manageable once Q1-Q2 are answered. |
| Overall readiness | **Low** | The PRD is not ready for implementation. Answer Q1-Q2 first to determine if this is even a project. |

## Next Steps

- [ ] Human answers critical questions above (especially Q1 and Q2)
- [ ] Diagnose existing plugin: `plugins/sync-upstream/run.sh` — is it disabled? misconfigured? credential issue?
- [ ] If Q2 answer is (a): fix the config, verify it works, close the project
- [ ] If Q2 answer is (b) or (c): write a proper PRD with success criteria, then pour `design` convoy
- [ ] Re-run ambiguity leg against the correct target (upstream-sync, not auto-test-pr)
- [ ] Updated PRD bead with answers
- [ ] Pour `design` convoy to generate implementation plan (only if Q2 ≠ "config fix")

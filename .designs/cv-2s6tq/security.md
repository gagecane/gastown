# Security Analysis

## Summary

The upstream-sync replacement introduces a significant trust boundary change: the old plugin aborts on merge conflicts and escalates to a human; the new system grants autonomous agents the authority to resolve conflicts and push the result to the fork's main branch. This expands the attack surface from "fetch + merge + push of clean merges" to "fetch + agent-generated code + push of conflict resolutions." The primary risk is supply-chain poisoning via crafted upstream commits that produce adversarial merge conflicts, tricking the resolving agent into accepting malicious code through main's CI gates.

The system's existing defense (full `go build` + `go test` gate before push) mitigates accidental regressions but is insufficient against intentional supply-chain attacks where the malicious payload passes tests by design. Defense-in-depth is achievable through commit-scope limiting, resolution-diff review, and conflict-complexity circuit breakers.

## Analysis

### Key Considerations

- **Trust boundary shift**: The upstream remote (`gastownhall/gastown`) is a public open-source repo. Anyone with push access there (or who gets a PR merged there) can influence what arrives on the fork's main branch. Previously, conflicts triggered human review. Now, an agent resolves them autonomously.
- **Push credentials**: The system needs write access to `origin` (gagecane/gastown) main branch. This is the highest-privilege credential in the fork's lifecycle. Compromise here = arbitrary code on main.
- **Agent as code author**: Conflict resolution is creative — the agent generates novel code (not just choosing theirs/ours). This code bypasses human review before landing on main.
- **CI gate ≠ security gate**: `go build` + `go test` passing does not mean code is secure. A well-crafted payload (e.g., exfiltration in a test helper, backdoored dependency, subtle logic change) passes both trivially.
- **Existing remotes**: The crew workspace already has both remotes configured (`origin` = gagecane, `upstream` = gastownhall). No new credential provisioning is needed for v1 — the risk is in expanded *use* of existing credentials, not new credential surface.
- **Frequency amplification**: Moving from a 6h-cooldown plugin to a more aggressive cadence means a compromised upstream commit reaches the fork faster, reducing the window for human detection.

### Options Explored

#### Option A: Agent resolves all conflicts (current design intent)

- **Description**: Agent autonomously resolves any merge conflict, runs CI gates, pushes if green.
- **Pros**: Zero human involvement; maximizes sync velocity; eliminates the "conflict pile-up" problem where unresolved conflicts compound.
- **Cons**: Largest attack surface. Agent can be tricked by adversarial conflicts. No human reviews resolution code before it hits main. Social engineering via crafted conflict markers possible.
- **Effort**: Low (baseline — this is the default if no security constraints are added).

#### Option B: Agent resolves only "simple" conflicts; escalates complex ones

- **Description**: Define a complexity threshold (e.g., ≤N conflicted files, ≤M conflicted hunks, no conflicts in security-sensitive paths). Agent resolves below threshold; escalates above.
- **Pros**: Dramatically reduces the attack surface for crafted conflicts. Most day-to-day drift is trivial (import reordering, adjacent-line changes). Escalation path is already built (existing plugin behavior).
- **Cons**: Defining "simple" is a spec challenge. A sophisticated attacker can craft a "simple-looking" conflict that introduces a vulnerability. Doesn't eliminate the risk — just reduces probability.
- **Effort**: Medium (threshold logic + path sensitivity configuration).

#### Option C: Agent resolves all conflicts, but resolution diff requires human sign-off before push

- **Description**: Agent resolves the conflict, runs CI, then creates a review artifact (PR or bead with diff). A human approves before push to main.
- **Pros**: Maintains human-in-the-loop for the dangerous operation (writing novel code to main). Preserves automation benefit (conflict is resolved promptly; push waits only for ack).
- **Cons**: Reintroduces human latency. If the human is unavailable, conflicts accumulate. Partially defeats the "fully autonomous" goal.
- **Effort**: Medium (review artifact creation + approval gate).

#### Option D: Agent resolves all conflicts with post-push audit trail and rollback capability

- **Description**: Agent resolves and pushes autonomously, but: (1) the resolution commit is tagged/labeled for audit, (2) a post-merge patrol reviews agent-authored resolution diffs within N hours, (3) a one-command rollback is pre-computed at push time.
- **Pros**: No latency; maintains full autonomy. Audit trail enables detection of supply-chain attacks after the fact. Pre-computed rollback reduces MTTR.
- **Cons**: Relies on detection rather than prevention. A sufficiently stealthy payload may not be caught in audit. Time-of-check/time-of-use gap between push and audit.
- **Effort**: Medium (audit tagging + rollback prep + patrol integration).

### Recommendation

**Option B (complexity-gated resolution) + Option D (post-push audit) as defense-in-depth layers.**

Rationale:
1. Most upstream syncs will be clean merges (no conflicts) — these are safe and require no special handling.
2. Simple conflicts (≤3 files, ≤10 hunks, no conflicts in `internal/auth/`, `internal/secrets/`, `*.sh`, `Makefile`, `go.mod`, `go.sum`) can be resolved autonomously with acceptable risk.
3. Complex or security-path conflicts escalate to human review (existing behavior — proven safe).
4. ALL agent-authored resolutions (even simple ones) get tagged for post-push audit by the next patrol cycle.
5. Pre-computed rollback branch is pushed as `rollback/<sync-sha>` alongside the sync commit, enabling one-command revert if audit finds issues.

This gives us: autonomy for 90%+ of real-world cases, human review for the dangerous 10%, and detection/rollback for the residual risk in the autonomous path.

## Threat Model

### T1: Supply-chain poisoning via crafted upstream conflict

- **Attacker**: Anyone who can get code merged to `gastownhall/gastown` (open-source contributors)
- **Attack vector**: Craft a PR to upstream that, when merged, creates a specific conflict pattern in the fork. The conflict resolution the agent produces introduces a vulnerability (e.g., disabling an auth check, adding an exfiltration endpoint).
- **Likelihood**: Low (requires understanding the fork's divergence + agent resolution behavior)
- **Impact**: Critical (arbitrary code on fork's main, bypassing all review)
- **Mitigation**: Complexity gate (Option B) + restricted-path escalation + post-push audit

### T2: Credential theft via malicious upstream code

- **Attacker**: Upstream contributor
- **Attack vector**: Upstream adds code that, when the sync agent runs `go test`, exfiltrates environment variables (which may contain push tokens, API keys).
- **Likelihood**: Medium (tests run in agent sandbox with credentials available for push)
- **Impact**: High (stolen push credentials = direct main compromise)
- **Mitigation**: Run CI gates in an isolated environment with NO access to push credentials. Separate "test" from "push" into distinct privilege domains. The agent should: (1) resolve + commit locally, (2) run tests in a sandboxed env with no network egress and no credential access, (3) only THEN use push credentials in a separate step.

### T3: Denial of service via conflict flooding

- **Attacker**: Upstream contributor or compromised CI
- **Attack vector**: Rapid upstream changes that continuously conflict with fork-specific code, causing the sync mechanism to consume all polecat capacity or flood the escalation queue.
- **Likelihood**: Medium (accidental version of this happens naturally during heavy upstream development)
- **Impact**: Medium (polecat fleet starved of capacity for real work; escalation fatigue)
- **Mitigation**: Rate limiting (existing from old plugin: 6h cooldown). Circuit breaker: if N consecutive syncs conflict, pause and notify. Bounded polecat allocation for sync work (≤1 polecat per cycle, not stealing from the general pool).

### T4: Race condition between sync and polecat work

- **Attacker**: None (accidental)
- **Attack vector**: Sync pushes to main while a polecat's MR is in the refinery queue. The MR was based on pre-sync main. Refinery merges it, potentially reintroducing code that the sync was supposed to bring in (or worse, creating a semantic conflict that passes CI but produces wrong behavior).
- **Likelihood**: Medium (timing-dependent but realistic under load)
- **Impact**: Medium (silent semantic regression)
- **Mitigation**: Existing guard (old plugin checks merge queue empty). New system MUST retain this: no sync push when MRs are pending in refinery. Alternatively, refinery re-gates MRs against post-sync main (already does rebase verification).

### T5: Git history manipulation via tag/ref confusion

- **Attacker**: Upstream contributor with tag-push access
- **Attack vector**: Push a tag or branch to upstream that shadows an expected ref name (e.g., a branch named `main` that points to a different tree). Agent fetches unexpected content.
- **Likelihood**: Low (GitHub protects default branch from deletion but not from tag shadowing)
- **Impact**: High (agent syncs from wrong tree)
- **Mitigation**: Always fetch with explicit refspec (`+refs/heads/main:refs/remotes/upstream/main`). Never use bare `git pull` or unqualified ref names. Validate that the fetched ref is a descendant of the previously-synced ref (no force-push detection).

## Constraints Identified

1. **Push credentials MUST NOT be accessible during test execution.** The test sandbox and the push operation must run in separate privilege domains. If the agent's shell has push tokens available when running `go test`, any upstream test code can steal them.

2. **Conflict resolution MUST NOT touch security-sensitive paths without human review.** Define an allowlist of paths where autonomous resolution is permitted. Sensitive paths (auth, crypto, secrets, CI config, Makefile, go.mod/go.sum, shell scripts) require escalation.

3. **Force-push detection is mandatory.** Before merging upstream content, verify the fetched upstream/main is a descendant of the last-synced upstream/main. If not (force-push or ref rewrite), abort and escalate — never merge non-descendant history.

4. **Agent-authored resolution commits MUST be distinguishable from human commits.** Use a dedicated commit author (e.g., `gt-sync-bot <sync@gastown.local>`) and a structured commit message with metadata (upstream SHA range, conflict files, resolution strategy). This enables audit patrols to find and review them.

5. **No network egress from the test sandbox.** Tests that attempt outbound connections during sync-validation MUST fail (or be run in a network-isolated namespace). This prevents test-time exfiltration.

## Open Questions

1. **Where do push credentials live today?** If they're in environment variables accessible to all shell commands (including `go test`), the T2 threat is active TODAY for the existing plugin. The new system must fix this regardless of the conflict-resolution decision.

2. **What is the exact divergence between the fork and upstream?** If divergence is minimal (a few fork-specific files), the conflict surface is small and Option B covers nearly all cases. If divergence is large and structural, conflicts will be frequent and complex, making the security cost of autonomous resolution higher.

3. **Who has push access to `gastownhall/gastown`?** If it's a small trusted team, T1 risk is much lower than if it's a large open-source community with external contributors. This affects the appropriate paranoia level.

4. **Is there an existing secret-scanning gate?** If `gitleaks` or similar runs before push (as proposed in the auto-test-pr PRD), it should also run on sync-resolution commits. This catches accidental secret introduction during conflict resolution.

## Integration Points

- **Refinery**: Sync-push to main affects all pending MRs in the refinery queue. The refinery's rebase-verification gate (`scripts/check-upstream-rebased.sh`) and the `fork_sync.go` topology preservation logic must be coordinated with the new sync mechanism. A sync push followed by a refinery merge must not create a state where `check-upstream-rebased.sh` fires false positives.

- **Polecat dispatch**: Sync conflict resolution needs a polecat. This polecat must be dispatched with constrained permissions (no ability to modify non-conflicted files, no ability to touch security-sensitive paths). The dispatch payload should include the conflict diff and the restricted-path list.

- **Witness**: The witness must monitor sync-polecat health separately from normal polecats. A stuck sync-polecat holding the "sync in progress" lock blocks all future syncs — the witness needs a timeout-and-release mechanism specific to sync operations.

- **Kill-switch**: The kill-switch proposed in the auto-test-pr PRD (`gt auto-test-pr pause`) sets a precedent. The sync mechanism needs an equivalent: `gt sync-upstream pause --rig=<rig>` and a town-wide `--all` variant. The SEV tree from auto-test-pr's Q6 should be mirrored here (SEV-1: sync introduces failing code; SEV-2: sync introduces security vulnerability).

- **Audit patrol**: A new or extended patrol that reviews agent-authored conflict-resolution commits within 24h of landing. Flags for human review if: (1) resolution touches >N lines, (2) resolution adds new imports or dependencies, (3) resolution modifies control flow in security-sensitive packages.

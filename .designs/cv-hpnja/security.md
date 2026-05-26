# Security Analysis

## Summary

The upstream-sync feature introduces an automated merge path from an
**external, publicly-writable** repository (`gastownhall/gastown`) into
the fork (`gagecane/gastown`) that backs our production rig. This makes
the upstream repository a first-class trust input into Gas Town's merge
queue. The core security question is: **what happens when upstream
commits contain hostile code, and our automated merge pipeline ingests
them without human review?**

The existing `sync-upstream` plugin already performs this merge in a
controlled fashion with seven safety guards. The proposed feature
formalizes and extends this into a continuous flow. The dominant threats
are: (1) **supply-chain poisoning** via upstream commits that introduce
backdoors, credential exfiltration, or subtle logic bugs into our fork;
(2) **merge-time code execution** when `go test ./...` runs upstream
code during gate checks; and (3) **automation abuse** where a
compromised upstream maintainer could time malicious commits to land
during quiescent windows when review attention is lowest.

## Analysis

### Key Considerations

- **Trust boundary: upstream is semi-trusted.** `gastownhall/gastown`
  is open source. Any contributor with merge rights to that repo can
  introduce code that flows into our fork. We do NOT control their
  review process, contributor vetting, or branch protection rules.
- **The merge is non-interactive.** The `sync-upstream/run.sh` plugin
  performs merges with `--no-edit` during quiescent windows. No human
  reviews the upstream diff before it lands in our integration branch.
- **Gates execute upstream code.** The configured gates (`go build
  ./...`, `go test ./...`, `go vet ./...`) compile and execute code
  from upstream commits. A malicious `init()` function, `TestMain()`,
  or build-time code generator runs with the credentials of whatever
  agent executes the gates.
- **The check-upstream-rebased gate creates a hard dependency.** The
  script `scripts/check-upstream-rebased.sh` enforces that upstream/main
  MUST be an ancestor of HEAD. This means we CANNOT selectively reject
  upstream commits without also rejecting all subsequent ones — we must
  either stay current or fall behind entirely.
- **Crew workers have elevated access.** The sync runs in
  `crew/gagecane/<rig>` worktrees. These may have access to Town-level
  resources (Dolt, beads, git push credentials, AWS credentials in the
  environment).
- **Force-with-lease push in fast-forward path.** The plugin uses
  `git push --force-with-lease` for the fast-forward case. While
  force-with-lease is safer than bare force-push, it still allows
  non-fast-forward updates if the race condition is met.
- **Conflict escalation reveals internal state.** When merge conflicts
  occur, `gt escalate` is called with file paths and instructions that
  reference internal directory structure. If escalation messages are
  logged publicly, this leaks internal architecture.

### Options Explored

#### Option 1: Sync-then-gate (current approach)

- **Description**: Merge upstream into the integration branch, then run
  gates on the result. If gates fail, the merge is already on the
  integration branch (or aborted on conflict).
- **Pros**: Simple. Fast. Keeps fork current with minimal latency.
- **Cons**: Gates execute upstream code with ambient credentials. A
  clean-building backdoor passes all gates silently. No human review
  before code enters the fork.
- **Effort**: Low (already implemented).

#### Option 2: Sync-to-staging with mandatory review gate

- **Description**: Merge upstream into a staging branch
  (`upstream-staging/<rig>`), run gates in a sandboxed environment,
  then require a human or AI review of the upstream diff before
  promoting to the integration branch.
- **Pros**: Introduces a review checkpoint. Upstream code never
  reaches the integration branch unreviewed. Sandboxed gates prevent
  credential exposure during test execution.
- **Cons**: Adds latency (review time). Requires reviewer capacity.
  May bottleneck if upstream is active. AI review may miss subtle
  supply-chain attacks.
- **Effort**: Medium.

#### Option 3: Selective cherry-pick with allowlist

- **Description**: Instead of merging all of upstream, maintain an
  allowlist of upstream paths/packages we sync. Only commits touching
  allowed paths are merged; others require explicit approval.
- **Pros**: Limits blast radius. Sensitive paths (CI config, build
  scripts, dependency files) can be excluded from auto-sync.
- **Cons**: Complex to maintain. Divergence grows on non-allowed
  paths. May break if upstream refactors across allowed/non-allowed
  boundaries.
- **Effort**: High.

#### Option 4: Sync with post-merge security scan

- **Description**: Merge upstream freely (current approach) but run
  a post-merge security scan (gitleaks, static analysis, dependency
  audit) before the integration branch is promoted to main or made
  available to polecats.
- **Pros**: Catches secrets, known-vulnerable dependencies, and
  common malicious patterns without blocking the merge flow.
  Lower latency than Option 2.
- **Cons**: Post-hoc — if scan finds something, we must revert.
  Cannot catch novel/subtle supply-chain attacks. Scan tools have
  false-negative rates.
- **Effort**: Medium.

### Recommendation

**Option 2 (staging + review) for high-sensitivity paths; Option 4
(post-merge scan) for the general case.** Concretely:

1. **Sandbox gate execution.** All `go test`, `go build`, `go vet`
   runs during sync MUST execute in a stripped environment (no
   `AWS_*`, `GITHUB_TOKEN`, `BD_*`, `DOLT_*` env vars). Use
   `env -i PATH=... HOME=... GOPATH=... go test ./...` or the `gt
   sandbox` wrapper if available. Network egress should be blocked
   after module cache is warm.

2. **Critical-path review gate.** Upstream commits touching these
   paths require human/mayor review before promotion:
   - `go.mod`, `go.sum` (dependency changes)
   - `.github/`, `.goreleaser.yml`, `Makefile` (CI/build infra)
   - `scripts/` (executable scripts)
   - `internal/auth/`, `internal/crypto/`, any security-sensitive pkg

3. **Post-merge gitleaks + govulncheck.** After every successful
   sync, run `gitleaks detect` on the merge diff and `govulncheck`
   on the updated module graph. Any finding → revert merge, escalate.

4. **Upstream commit signing verification.** If `gastownhall/gastown`
   enforces signed commits, verify signatures during fetch. If not,
   document this as an accepted risk for v1.

## Constraints Identified

- **C-SEC-1 (sandboxed gates):** Gate commands (`go test`, `go build`,
  `go vet`) executed during upstream sync MUST run with credential env
  vars stripped and network egress disabled post-module-cache-warmup.
  Rationale: upstream code is semi-trusted; ambient credentials must
  not be accessible during compilation/test execution.

- **C-SEC-2 (critical-path review):** Changes to dependency manifests
  (`go.mod`, `go.sum`), CI configuration, build scripts, and
  security-sensitive packages MUST NOT auto-merge without review.
  Rationale: these paths have disproportionate blast radius.

- **C-SEC-3 (post-merge scan):** Every successful upstream merge MUST
  trigger `gitleaks detect` on the diff and `govulncheck` on the
  module graph. Any finding → automatic revert + escalation.
  Rationale: defense-in-depth against secrets and known vulns.

- **C-SEC-4 (no credential leakage in escalation):** Escalation
  messages from merge conflicts MUST NOT include absolute paths,
  credentials, or internal hostnames beyond what is already public
  in the upstream repo. Rationale: escalation messages may be logged.

- **C-SEC-5 (force-push prohibition):** The sync mechanism MUST NOT
  use `git push --force` (bare). `--force-with-lease` is acceptable
  ONLY for the fast-forward case where the expected ref is verified.
  Rationale: prevents accidental history rewriting on the fork.

- **C-SEC-6 (upstream remote URL pinning):** The upstream remote URL
  MUST be hardcoded or validated against a known-good value. The sync
  script MUST NOT fetch from a URL derived from untrusted input (e.g.,
  bead description, environment variable without validation).
  Rationale: prevents remote-URL poisoning attacks.

- **C-SEC-7 (quiescent-window-only execution):** The sync MUST only
  execute when the merge queue is empty and no polecats have active
  work. Rationale: reduces blast radius of a bad merge — no in-flight
  work is based on the pre-merge state that suddenly changes.

- **C-SEC-8 (abort-on-conflict):** Merge conflicts MUST be aborted
  immediately (`git merge --abort`). The sync agent MUST NOT attempt
  automated conflict resolution on upstream merges. Rationale:
  automated resolution of untrusted code conflicts could silently
  favor the attacker's version.

- **C-SEC-9 (audit trail):** Every sync attempt (success, skip, or
  failure) MUST be recorded as a bead with outcome, SHA range, and
  gate results. Rationale: enables postmortem investigation of
  when malicious code entered the fork.

- **C-SEC-10 (kill-switch):** A `.disabled` sentinel file or
  equivalent MUST be able to halt all sync operations within one
  plugin-run interval (≤6h per current cooldown). For immediate
  halt, `gt auto-sync pause` or equivalent MUST take effect within
  one Mayor tick.

## Open Questions

1. **What is the upstream's commit signing policy?** If
   `gastownhall/gastown` does not require signed commits, any
   contributor with push access can inject unsigned commits. Do we
   accept this risk for v1, or require signature verification?

2. **Can we scope `check-upstream-rebased.sh` to allow selective
   rejection?** Currently it's all-or-nothing — we must include ALL
   of upstream/main. If a malicious commit lands upstream, we cannot
   skip it without also skipping all subsequent commits. Is there a
   mechanism to "pin" at a known-good upstream SHA while the issue
   is resolved?

3. **Who reviews the upstream diff for critical paths?** If the Mayor
   dispatches a polecat to review, the polecat is an LLM reviewing
   potentially adversarial code. Is this sufficient, or do we need a
   human-in-the-loop for dependency/CI changes?

4. **What is the incident response if malicious code is detected
   post-merge?** Revert the merge commit? Force-push the integration
   branch to pre-merge state? How do we handle polecats that already
   based work on the poisoned commit?

5. **How do we handle upstream force-pushes or history rewrites?**
   If `gastownhall/gastown` force-pushes main (rewriting history),
   our fetch will diverge in unexpected ways. Should we detect and
   refuse non-fast-forward upstream updates?

6. **What is the module-cache warming strategy?** If we block network
   after `go mod download`, how do we handle upstream commits that add
   new dependencies? The first sync after a new dep is added would
   fail. Do we allow network for `go mod download` specifically?

## Integration Points

- **`api` dimension** — The kill-switch (`gt auto-sync pause`,
  `gt auto-sync status`) must be instantly effective. Status should
  show last sync SHA, gate results, and any security findings.

- **`data` dimension** — The sync audit trail (bead per attempt) feeds
  into incident investigation. Schema needs: source SHA range,
  gate pass/fail, gitleaks result, files changed count, and whether
  critical-path files were touched.

- **`integration` dimension** — The Refinery merge queue is downstream
  of the sync. After upstream merges into the integration branch,
  polecat MRs must still pass gates on the merged state. Confirm that
  the merge queue re-runs gates on the post-sync base (not a stale
  cached result).

- **`scale` dimension** — Sync frequency (currently 6h cooldown)
  affects exposure window. Shorter cooldowns = less divergence but more
  frequent execution of untrusted code in gates. Longer cooldowns =
  larger diffs to review but less frequent exposure.

- **`ux` dimension** — When upstream sync introduces a breaking change,
  polecats will encounter build failures they didn't cause. UX needs
  clear signaling: "build failure introduced by upstream sync at SHA
  abc123" vs. "build failure from your changes." The `git log` should
  clearly attribute sync merge commits.

# Design: Upstream Sync for Fork Rigs

> Automatically keep fixes from `gastownhall/gastown` (upstream) merged into
> `gagecane/gastown` (fork) via the local `gastown_upstream` rig.

## Executive Summary

The upstream-sync feature keeps the fork (`gagecane/gastown`, tracked as `origin`)
current with the upstream open-source repo (`gastownhall/gastown`, tracked as
`upstream`) by periodically merging `upstream/main` into the fork's integration
branch. It is designed for **zero-interaction steady state**: once a rig has an
upstream URL configured, the sync plugin runs on a 6-hour cooldown with no human
involvement. Operators interact only during setup, status checks, or conflict
resolution.

The implementation leverages almost entirely existing infrastructure: the
`plugins/sync-upstream/` plugin defines scheduling and safety rails, the
`internal/refinery/fork_sync.go` module preserves merge topology during squash
merges, and `scripts/check-upstream-rebased.sh` enforces fork currency as a
pre-merge gate. The primary new work is a deterministic `run.sh` script that
implements the plugin's documented logic, a `gt upstream` CLI verb for operator
visibility, and security hardening (sandboxed gate execution, post-merge scanning).

The feature biases toward safety over speed: merge-only (never rebase), abort on
conflict (never auto-resolve), quiescent-window-only execution (minimize blast
radius), and defense-in-depth security scanning of upstream code.

## Problem Statement

The fork `gagecane/gastown` diverges from `gastownhall/gastown` over time. Without
automated sync, fixes and improvements from upstream never reach the fork. The
`check-upstream-rebased.sh` pre-merge gate enforces that upstream/main is an
ancestor of HEAD before any polecat MR can land. When upstream advances and no sync
occurs, ALL polecat work on the rig is blocked until someone manually merges
upstream. This creates a hard dependency on manual intervention that scales poorly
and causes unpredictable work stoppages.

**Current state:** 351 fork-only commits, 622 modified files, ~96K insertions ahead
of upstream. Upstream produces ~240 commits/month (~8/day). The gate enforcement is
active. Manual syncs are the bottleneck.

## Proposed Design

### Overview

A **plugin-as-script** (`plugins/sync-upstream/run.sh`) handles the core merge
logic, dispatched by the deacon patrol on a 6-hour cooldown. A **`gt upstream` CLI
verb** provides operator visibility and manual control. **Git itself is the primary
data store** -- no new database, tables, or persistent state beads. Security is
enforced via sandboxed gate execution and post-merge vulnerability scanning.

```
                           +-----------------+
                           | Deacon Patrol   |
                           | (6h cooldown)   |
                           +--------+--------+
                                    |
                                    v
                  +----------------------------------+
                  | plugins/sync-upstream/run.sh     |
                  |                                  |
                  | 1. Check 7 safety guards         |
                  | 2. git fetch upstream            |
                  | 3. git merge upstream/main       |
                  | 4. Post-merge security scan      |
                  | 5. git push origin               |
                  | 6. Record receipt bead           |
                  +----------------------------------+
                        |                    |
                   (success)            (conflict)
                        |                    |
                        v                    v
              +------------------+   +-------------------+
              | Fork is current  |   | git merge --abort |
              | Gate passes      |   | gt escalate       |
              +------------------+   +-------------------+
```

### Key Components

**1. Sync Plugin Script (`plugins/sync-upstream/run.sh`)**

The deterministic execution engine. Implements all 7 safety guards as shell
conditionals, performs the git merge on the crew worktree, records results as
ephemeral beads.

**2. CLI Interface (`gt upstream`)**

Operator-facing commands for status, manual sync trigger, pause/resume, and
history. Implemented in Go as a Cobra command tree.

**3. Rig Configuration**

Extends existing `RigConfig.UpstreamURL` with sync-specific settings (cadence,
integration branch, auto-escalate).

**4. Security Layer**

Sandboxed gate execution (stripped credentials, blocked network), post-merge
gitleaks/govulncheck, critical-path review gate for dependency/CI changes.

**5. Pre-merge Gate (`scripts/check-upstream-rebased.sh`)**

Already implemented. Enforces that upstream/main is an ancestor of HEAD before
any MR can land. This is the forcing function that makes sync necessary.

### Interface

#### CLI Surface (`gt upstream`)

```
gt upstream status   [--rig=<rig>] [--json]           # What's the sync state?
gt upstream sync     [--rig=<rig>] [--dry-run]        # Trigger a sync now
gt upstream pause    [--rig=<rig>] [--duration=<dur>] # Pause syncing
gt upstream resume   [--rig=<rig>]                    # Resume after pause
gt upstream log      [--rig=<rig>] [--limit=N]        # Recent sync history
```

Example output:
```
$ gt upstream status
RIG                UPSTREAM                    BEHIND  STATE   LAST-SYNC  NEXT
gastown_upstream   gastownhall/gastown         6       idle    2h ago     4h
```

**Omitted from v1** (deferred to v2):
- `gt upstream diff` -- show what upstream has that fork doesn't
- `gt upstream conflicts` -- predict merge conflicts before sync
- `gt upstream cherry-pick` -- selective sync of specific commits

#### Rig Status Integration

`gt rig status` shows a one-line fork sync summary:
```
Fork Sync:   ✓ synced 3h ago (upstream/main → main, 0 commits behind)
```

Or when broken:
```
Fork Sync:   ✗ conflict (escalated 2h ago, 3 files)  [gt upstream status]
```

### Data Model

**Git is the source of truth.** The question "is the fork up to date?" is answered
by `git merge-base --is-ancestor upstream/main origin/main`. No separate tracking.

| Data | Storage | Lifecycle |
|------|---------|-----------|
| Upstream URL | `RigConfig.UpstreamURL` (rigs.json) | Permanent, per-rig |
| Sync state (current/behind/conflicted) | Computed from git refs | Derived, real-time |
| Sync history | Merge commits in git log | Permanent (git history) |
| Run receipts | Ephemeral beads (`plugin:sync-upstream`) | Auto-purged by Dolt sync |
| Conflict escalations | Standard beads | Standard lifecycle |
| Pause state | Rig identity bead label or sentinel file | Until resumed |

#### Configuration Schema

```json
{
  "gastown_upstream": {
    "url": "https://github.com/gagecane/gastown",
    "upstream_url": "https://github.com/gastownhall/gastown.git",
    "upstream_sync": {
      "enabled": true,
      "upstream_branch": "main",
      "integration_branch": "main",
      "cadence": "6h",
      "auto_escalate_conflicts": true
    }
  }
}
```

#### Go Package (`internal/upstream/`)

```go
package upstream

type Config struct {
    Enabled              bool          `json:"enabled"`
    UpstreamBranch       string        `json:"upstream_branch"`
    IntegrationBranch    string        `json:"integration_branch"`
    Cadence              time.Duration `json:"cadence"`
    AutoEscalateConflict bool          `json:"auto_escalate_conflicts"`
}

type Status struct {
    RigName        string    `json:"rig_name"`
    UpstreamURL    string    `json:"upstream_url"`
    CommitsBehind  int       `json:"commits_behind"`
    State          SyncState `json:"state"`       // idle|syncing|paused|conflicted|disabled
    LastSyncAt     *time.Time `json:"last_sync_at"`
    LastSyncResult string    `json:"last_sync_result"`
    NextSyncAt     *time.Time `json:"next_sync_at"`
    PausedUntil    *time.Time `json:"paused_until"`
    ConflictFiles  []string  `json:"conflict_files,omitempty"`
}

type SyncResult struct {
    Result      string        `json:"result"` // merged|fast-fwd|skipped|conflicted|error
    CommitRange string        `json:"commit_range,omitempty"`
    CommitCount int           `json:"commit_count"`
    Conflicts   []string      `json:"conflicts,omitempty"`
    SkipReason  string        `json:"skip_reason,omitempty"`
    Duration    time.Duration `json:"duration"`
}
```

## Trade-offs and Decisions

### Decisions Made

| Decision | Rationale | Alternatives Rejected |
|----------|-----------|----------------------|
| **Merge-only, never rebase** | Keeps polecat branches valid; no orphaning | Rebase (breaks all in-flight work) |
| **Plugin-as-script for v1** | Deterministic, fast, no LLM variance, testable | Plugin-as-agent (interpretation variance), Go command (over-engineering for v1) |
| **`gt upstream` top-level verb** | Natural language, discoverable, consistent with `gt patrol` | Nested under `gt rig` (too deep), plugin-only (poor operational UX) |
| **Git as data store (no new schema)** | Zero split-brain risk, authoritative, already working | Pinned state bead (overkill), Dolt table (unnecessary), JSON file (not replicated) |
| **6h cooldown for v1** | Adequate for ~8 commits/day upstream; conservative | Shorter cooldown (unnecessary at current scale), event-driven (needs infra) |
| **Abort + escalate on conflict** | Never auto-resolve untrusted code conflicts | Auto-resolution (security risk) |
| **Quiescent-window execution** | Minimizes blast radius of bad merge | Priority MQ insertion (complex refinery changes) |
| **Crew worktrees for execution** | Long-lived, user-managed, don't interfere with polecats | Polecat worktrees (transient), refinery clones (wrong separation of concerns) |

### Open Questions (Requiring Human Input)

1. **Integration branch name**: Should sync target `main` directly, or a
   dedicated `gagecane/gt` branch? The plugin references both. Current rig
   shows `origin` is `gagecane/gastown` with main branch `main`.
   **Recommendation:** Target `main` directly; make configurable via
   `integration_branch` for rigs that need a separate branch.

2. **Critical-path review: who reviews?** When upstream touches `go.mod`,
   `scripts/`, or security-sensitive packages, should a human review the diff,
   or is an LLM (Mayor-dispatched polecat) sufficient?
   **Recommendation:** Human for v1 (conservative); evaluate LLM review for v2
   based on confidence levels.

3. **Upstream commit signing**: Does `gastownhall/gastown` enforce signed
   commits? If not, do we accept the risk of unsigned code flowing into our
   fork?
   **Recommendation:** Accept for v1 (upstream is semi-trusted), document as
   known risk, add signature verification in v2 if upstream adopts signing.

4. **Selective rejection**: The `check-upstream-rebased.sh` gate is
   all-or-nothing. If a malicious/broken commit lands upstream, can we pin at a
   known-good SHA while the issue is resolved?
   **Recommendation:** Add a `sync-upstream:pin-sha:<sha>` label mechanism that
   tells the plugin to sync only up to that SHA. Implement in v2.

5. **Incident response for post-merge malicious code detection**: Revert merge
   commit? Force-push integration branch? How to handle polecats already based
   on the poisoned commit?
   **Recommendation:** Revert merge commit (creates a new commit, preserves
   history), immediately escalate to Mayor with CRITICAL severity, block further
   syncs via kill-switch.

6. **Should `gt upstream sync` bypass the cooldown?** Manual invocation implies
   operator intent.
   **Recommendation:** Yes. Manual sync always runs if guards pass. The cooldown
   only governs the daemon's automatic dispatch.

### Trade-offs

| What we gain | What we give up |
|--------------|-----------------|
| Zero-interaction steady state | Up to 6h of gate failures between sync attempts |
| Safety (quiescent windows) | Reduced sync frequency at high throughput |
| Simplicity (git as state) | No queryable sync database or dashboards |
| Merge-only strategy | Merge commits accumulate in history |
| Conservative conflict handling | Manual resolution required (~1 per 8 days at current scale) |
| Security scanning overhead | ~5s added to each sync cycle |

## Risks and Mitigations

### Supply-Chain Poisoning (HIGH)

**Risk:** Upstream commits from external contributors may contain backdoors,
credential exfiltration, or subtle logic bugs that auto-merge into our fork.

**Mitigations:**
- C-SEC-1: Gate execution sandboxed (no credentials, no network post-cache)
- C-SEC-2: Critical-path files (go.mod, scripts/, CI config) require review
- C-SEC-3: Post-merge gitleaks + govulncheck on every sync
- C-SEC-10: Kill-switch halts sync within one plugin-run interval

### Fleet-Wide Gate Failure Cascade (MEDIUM)

**Risk:** When upstream advances and sync fails (conflict, quiescence unavailable),
ALL polecat MRs on the rig are blocked by `check-upstream-rebased.sh`.

**Mitigations:**
- 6h cooldown limits maximum blockage window
- Conflict auto-escalation ensures rapid human awareness
- v2: Gate-failure override (relax quiescence when gate has failed >1h)
- v2: Reduce cooldown to 2h for faster recovery

### Fork Divergence Growth (MEDIUM)

**Risk:** Fork-only commits (currently 351, 622 files) increase conflict
probability over time. At current growth rate, conflict probability per sync
cycle is ~3%; at 10x divergence it exceeds 50%.

**Mitigations:**
- Alert at >500 fork-only commits (v2)
- Complementary "upstream fork changes" workflow (out of scope but noted)
- Shorter sync intervals reduce per-cycle conflict size

### Quiescent Window Scarcity (LOW at current scale)

**Risk:** At >50 MRs/day, no quiescent windows occur naturally, preventing sync
from ever firing.

**Mitigations:**
- At current scale (~5 MRs/day): non-issue
- v2: Relax to "no MRs touching upstream-modified files" (semantic check)
- v2: Priority-scheduled sync in MQ (architectural change, deferred)

### Credential Exposure During Gate Execution (MEDIUM)

**Risk:** `go test ./...` compiles and executes upstream code. A malicious
`init()` or `TestMain()` could exfiltrate credentials from the environment.

**Mitigations:**
- C-SEC-1: Gates run with `env -i` (stripped of AWS_*, GITHUB_TOKEN, BD_*, DOLT_*)
- Network egress blocked after module cache is warm
- Module cache warming allowed only for `go mod download` specifically

## Implementation Plan

### Phase 1: MVP (make the existing plugin executable)

1. **Create `plugins/sync-upstream/run.sh`** implementing all 7 safety guards:
   - Rig not parked/docked/disabled
   - Upstream remote exists and is fetchable
   - Integration branch exists
   - Not already up-to-date
   - Merge queue empty
   - No polecats with active work
   - Working tree clean

2. **Core merge logic:**
   - `git fetch upstream main`
   - `git merge upstream/main --no-edit`
   - On conflict: `git merge --abort`, `gt escalate -s medium`, record failure
   - On success: `git push origin`, record success receipt

3. **Security baseline:**
   - Sandboxed gate execution (stripped env vars)
   - Post-merge `gitleaks detect` on merge diff

4. **Verify integration with existing infrastructure:**
   - Deacon patrol dispatches plugin on cooldown expiry
   - `check-upstream-rebased.sh` passes on new branches post-sync
   - `preserveForkSyncTopology()` handles downstream MRs correctly

### Phase 2: Operator CLI (`gt upstream`)

1. **Implement `internal/upstream/` package** (Config, Status, SyncResult types)
2. **Implement `internal/cmd/upstream.go`** (status, sync, pause, resume, log)
3. **Add upstream status line to `gt rig status`**
4. **Add upstream-health check to `gt doctor`**
5. **Environment variable overrides** (GT_UPSTREAM_SYNC_DISABLED, etc.)

### Phase 3: Hardening and Scale Preparation (v2)

1. **Adaptive cooldown** -- shrink to 2h when upstream velocity >5 commits/cycle
2. **Conflict-surface pre-check** -- skip quiescence if no file overlap
3. **Critical-path review gate** -- block auto-merge for go.mod, scripts/, CI
4. **`govulncheck` integration** -- run on updated module graph post-merge
5. **Gate-failure override** -- relax quiescence when gate has failed >1h
6. **Parallelize rig iteration** -- concurrent sync for >3 rigs
7. **SHA-pinning mechanism** -- `sync-upstream:pin-sha:<sha>` for emergency stops
8. **Divergence alerting** -- warn at >500 fork-only commits

## Security Constraints (Mandatory)

| ID | Constraint | Rationale |
|----|-----------|-----------|
| C-SEC-1 | Gate commands run with credential env vars stripped | Upstream code is semi-trusted |
| C-SEC-2 | Critical-path files require review before auto-merge | Disproportionate blast radius |
| C-SEC-3 | Post-merge gitleaks + govulncheck on every sync | Defense-in-depth |
| C-SEC-4 | Escalation messages contain no credentials/internal paths | May be logged |
| C-SEC-5 | No bare `git push --force`; `--force-with-lease` only for FF | Prevent history rewriting |
| C-SEC-6 | Upstream URL validated against known-good value | Prevent URL poisoning |
| C-SEC-7 | Execute only in quiescent windows | Reduce blast radius |
| C-SEC-8 | Abort immediately on conflict; never auto-resolve | Prevent favoring attacker's code |
| C-SEC-9 | Every attempt recorded as bead (success/skip/failure) | Audit trail for investigation |
| C-SEC-10 | Kill-switch effective within one plugin-run interval | Emergency halt capability |

## Appendix: Dimension Analyses

| Dimension | File | Key Insight |
|-----------|------|-------------|
| [API & Interface](api.md) | `gt upstream` verb tree; 5 subcommands; Go package `internal/upstream/` | Operator visibility via intuitive CLI |
| [Data Model](data.md) | Git as source of truth; zero new schema; ephemeral receipts | Simplicity prevents split-brain |
| [Integration](integration.md) | Plugin-as-script; touches plugin system, refinery, gate scripts | Existing infra covers 80% |
| [Scalability](scale.md) | <1% resource ceiling at current load; v2 adaptive cooldown | First bottleneck is quiescent window scarcity |
| [Security](security.md) | 10 mandatory constraints; sandbox gates; post-merge scanning | Upstream is a first-class trust input |
| [UX](ux.md) | Invisible when working; clear diagnostics when broken | Focus UX effort on failure moments |

### Cross-Dimension Conflicts Resolved

| Tension | Resolution |
|---------|-----------|
| API wants `gt upstream` top-level verb; UX wants `gt plugin` namespace | **Both**: `gt upstream` for operators (v2), `gt rig status` line for v1 glance, `gt plugin show` for drill-down |
| Integration recommends script; API recommends Go | **Phased**: Script for v1 execution engine, Go CLI wrapper for v2 operator interface |
| Scale says 6h is fine; Security wants smaller exposure window | **Accept for v1**: 6h is adequate at current velocity; Security mitigated by post-merge scanning |
| Data model says no state bead; Scale wants circuit-breaker state | **Compromise**: Use rig identity bead labels (`sync-upstream:paused-until:<ISO>`) -- no new schema, inspectable via `bd show` |

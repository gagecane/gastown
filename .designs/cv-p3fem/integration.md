# Integration Analysis

## Summary

The false-stale heartbeat problem sits at the intersection of three Gas Town
subsystems that are already wired together but use heartbeats in inconsistent
ways: (1) the polecat heartbeat file/format in `internal/polecat/heartbeat.go`,
(2) the witness/daemon liveness checks in `internal/polecat/manager.go`
(`isSessionProcessDead`) and `internal/daemon/reap_dead_agent_wisps.go`, and
(3) the `stuck-agent-dog` plugin which interprets heartbeats independently
in shell. None of these currently survive a long LLM/build/test call cleanly,
because the only callers that touch heartbeats are foreground `gt` commands
in `persistentPreRun` (`internal/cmd/root.go:227`) and a couple of explicit
hot paths (`gt done`, `gt heartbeat`, polecat session manager allocation).

The integration win is that the heartbeat file format already supports a v2
schema with an agent-reported `state` field (`gu-3vr5`), and the witness was
deliberately written to make exactly one inference: "is the heartbeat fresh?"
That separation is the right substrate. The migration is therefore primarily
a **producer-side** problem (who writes heartbeats, and when) and a **policy
unification** problem (every consumer must agree on what "stale" means,
including the shell-only stuck-agent-dog). No new bead types, no merge-queue
changes, no rig-config schema changes are required for the minimal fix.
Witness/refinery roles are the largest gap: they currently write no
heartbeats at all, which is why the incident at 2026-05-27T23:31 in
gastown_upstream had no auto-recovery surface.

## Analysis

### Key Considerations

- **The v1/v2 heartbeat file format is already backwards-compatible.**
  `SessionHeartbeat.IsV2()` distinguishes by presence of the `state` field,
  and `EffectiveState()` defaults to `working` for v1. Any new producer or
  consumer can extend the schema (e.g., `last_progress_at`,
  `expected_idle_until`, `liveness_token`) without breaking older readers
  because Go's `json.Unmarshal` ignores unknown fields and emits zero values
  for missing ones. This is the only file format on the critical path —
  there is no DB schema migration.

- **Three independent staleness consumers must be reconciled.** Today:
  - `internal/polecat/heartbeat.go` (`SessionHeartbeatStaleThreshold = 3m`,
    `IsSessionHeartbeatStale`) — used by `isSessionProcessDead` for polecat
    reaping.
  - `plugins/stuck-agent-dog/run.sh` (`STUCK_STALLED_THRESHOLD=600s`,
    re-implements freshness logic in jq/bash). Already runs at 2× the Go
    threshold to dampen false positives.
  - `internal/daemon/reap_dead_agent_wisps.go` (uses **`bead.UpdatedAt`**, not
    the heartbeat file, because witness/refinery don't produce heartbeats —
    `DefaultDeadAgentReapTimeout = 2h`). This is the path that failed to
    fire during the 2026-05-27T23:31 refinery-death incident.

  Three thresholds (3m / 10m / 2h), three code paths, three failure modes.
  Any new design must either (a) collapse these into one source of truth,
  or (b) explicitly document the layering with a single inference (witness)
  and policy-only secondary consumers.

- **Witness/refinery currently have zero heartbeat coverage.** The
  `dead_agent_reap_timeout` (2h) is a `bd updated_at` proxy precisely
  because there is no heartbeat file. Adding heartbeat producers to the
  witness/refinery loops is the smallest-surface-area structural change
  that closes the supervision gap (gu-0nmw, gu-rh0g). The reaper
  documentation explicitly calls this out: *"Witness and refinery don't
  have heartbeat files, but they do touch bd at the start of each patrol
  cycle, so updated_at is a reasonable staleness proxy."*

- **The producer-side gap is well-bounded.** Today's heartbeat producers:
  `persistentPreRun → touchPolecatHeartbeat` (every gt command),
  `polecat.SessionManager` allocation, `gt done`, `gt heartbeat` (manual
  state set). Long LLM/build/test/MQ-wait calls are NOT gt commands — they
  are subprocess invocations from inside gt, or claude.ai's own internal
  loop. The fix is straightforward: instrument long-call boundaries
  and/or add a background ticker to claude wrapper sessions.

- **The stuck-agent-dog plugin must be in the loop or it will keep firing
  false positives.** The plugin scope explicitly excludes witness/refinery,
  so coverage extension to those roles does NOT widen the dog's blast
  radius. But **any change to the polecat-side semantic of "fresh"** must
  be reflected in the dog's bash logic, or the dog will continue to flag
  agents the witness considers alive. This is a known cross-language
  coupling and the existing `STUCK_STALLED_THRESHOLD=600s` is a defensive
  workaround for it.

- **Backward compatibility floor for heartbeat-missing files.**
  `IsSessionHeartbeatStale` deliberately returns `(stale=false, exists=false)`
  when no file is present — this avoids reaping pre-rollout sessions. A new
  scheme MUST preserve this fallback or it will mass-reap any session that
  hasn't yet adopted the new producer pattern. The `isSessionProcessDead`
  fallback to PID-signal probing is the safety net.

- **Long-running operations have a structural signature: the agent has an
  open file descriptor / subprocess but no recent gt invocations.** This
  is the core distinguishing feature between "busy LLM call" and "dead
  agent": a dead agent has no live PID; a busy agent does. A heartbeat
  scheme that fuses heartbeat-freshness with PID-liveness (the existing
  fallback already does this for missing files) is information-theoretically
  sufficient.

### Options Explored

#### Option 1: Background heartbeat goroutine inside long-running gt commands (Targeted Producer)

- **Description**: Wrap subprocess invocations and other long-running gt
  operations (build, test, MQ-wait, sling-blocking-wait) with a goroutine
  that touches the heartbeat every N seconds (e.g., 60s) with
  `state=working` and a `context` field describing the operation
  (`"go test ./..."`, `"awaiting MR gu-xxxx"`). Touches stop when the call
  returns. No format change required.
- **Pros**:
  - No new infrastructure — pure producer addition.
  - Preserves the witness's single inference rule.
  - Naturally extends the existing `context` field.
  - Localized — diff is bounded to call sites that emit subprocesses.
- **Cons**:
  - Doesn't help the *primary* reported failure (claude/LLM-thinking calls
    aren't owned by gt — they're owned by the claude harness, which gt
    can't instrument from inside).
  - Risk of leaking goroutines on panic; needs disciplined defer-stop.
- **Effort**: Low for gt-owned subprocesses; **does not solve the LLM-call
  case** without harness-side cooperation.

#### Option 2: Claude harness ticker (Heartbeat-from-Outside-Gt)

- **Description**: Install a per-session shell loop that touches the
  heartbeat directly (writing the file in jq/bash like
  `stuck-agent-dog/run.sh` reads it) every 60s, regardless of what claude
  or gt is doing. Started by the polecat boot wrapper, killed when the
  session exits. Writes `state=working` with a generic context.
- **Pros**:
  - Closes the LLM-call false-positive entirely — heartbeats keep flowing
    while claude is mid-thought, mid-tool-call, or blocked on stdin.
  - No code changes inside hot paths.
  - Easy to disable/tune per-session.
- **Cons**:
  - **Defeats the purpose of agent-reported state if implemented naively**
    — a dumb ticker can mask a hung claude that should be detected stuck.
    Mitigation: only emit when the parent claude PID is alive AND has
    consumed CPU in the last N seconds (cheap `/proc/<pid>/stat` check).
  - Lives in shell, parallel to the Go schema — yet another producer to
    keep in sync (cross-language coupling, like stuck-agent-dog).
  - Background process management (lifecycle, zombies).
- **Effort**: Low-Medium. Mostly polecat-boot wrapper changes
  (`internal/cmd/polecat_kiro_wrapper.go` already exists and is the right
  insertion point).

#### Option 3: Heartbeat schema v3 — `last_progress_at` + `liveness_proof` (Schema Extension)

- **Description**: Add fields to `SessionHeartbeat`:
  - `last_progress_at` — wall-clock of the last *meaningful* change
    (assignee/bead/state transition), updated only on real events.
  - `liveness_proof` — a token populated by a low-cost external probe
    (claude PID + CPU-recent-since), refreshed by the boot wrapper.
  - `expected_idle_until` — agent-declared "I will be quiet until at least
    T" hint, used to suppress stuck detection during known-long ops.

  The witness's single inference becomes: "is `liveness_proof` fresh **and**
  (we are before `expected_idle_until` OR `state=working`)". `gt heartbeat
  --hint=long-llm-call --until=+15m` becomes a first-class command for
  agents about to enter known-long phases.
- **Pros**:
  - Decouples "agent is alive" from "agent made progress" — the original
    conflation that produced this bug.
  - Lets agents declare expected idle windows (LLM call, MQ wait,
    rebase-bisect) and the witness honors them.
  - Forward-compatible: v2 readers ignore new fields cleanly.
  - Fits the existing ZFC pattern (gu-3vr5) — agent reports, witness
    inspects.
- **Cons**:
  - Schema growth; every consumer (incl. stuck-agent-dog jq) must adopt or
    explicitly degrade gracefully.
  - Hint-field is gameable (an agent in tight loop could declare
    `expected_idle_until=+24h` and never be reaped). Need a per-rig cap
    (e.g., max declared idle = `dead_agent_reap_timeout`).
  - Most complex of the options.
- **Effort**: Medium. Pure additive schema changes plus a few consumers.

#### Option 4: Witness/Refinery heartbeat producers (Coverage Extension)

- **Description**: Make witness and refinery write heartbeats on patrol
  cycles (mirroring polecat's `persistentPreRun` touch). Replace
  `reap_dead_agent_wisps.go`'s reliance on `bead.UpdatedAt` with the same
  heartbeat machinery polecats use. Threshold remains role-specific (e.g.,
  10m for witness, 30m for refinery's MQ-batch wait) but the *mechanism*
  is unified.
- **Pros**:
  - Closes the gap that caused the original incident (refinery died, no
    auto-recovery).
  - Single staleness mechanism across all agent roles.
  - Drops the 2h `dead_agent_reap_timeout` floor toward a useful 10-30m
    detection window.
- **Cons**:
  - Witness/refinery patrol loops are also long-running (refinery
    bisects, witness blocks on tmux ops) — same false-stale problem
    transferred to the same agents that are *supposed* to detect it.
    Requires a background ticker (Option 2 mechanism, internalized) on
    those roles too.
  - Threshold-per-role config schema adds knobs.
- **Effort**: Medium.

#### Option 5: Hybrid — Schema v3 + Background Ticker + Witness/Refinery coverage (RECOMMENDED)

- **Description**: Combine the producer mechanism from Option 2, the schema
  extensions from Option 3, and the role-coverage extension from Option 4.
  Layered policy:
  1. **Producer floor**: a per-session shell ticker writes heartbeats every
     60s with a CPU-aliveness liveness proof. This is the "is the process
     wedged?" signal.
  2. **Agent-reported state**: when gt commands run, they update `state`
     and (optionally) `expected_idle_until`. This is the "what does the
     agent think it's doing?" signal.
  3. **Witness inference**: stale = (no liveness proof in N seconds) OR
     (state=stuck) OR (state=idle past expected_idle_until). All other
     transitions are honored as live.
  4. **Coverage**: witness and refinery adopt the same producer pattern,
     replacing the `updated_at` proxy in `reap_dead_agent_wisps.go`.
- **Pros**:
  - Eliminates both incident classes: false-positive nukes during LLM
    calls AND missed-real-deaths of supervisor agents.
  - Each layer fails open (file missing → fall through to PID probe; no
    state field → assume working; ticker missing → fall through to gt
    command touches).
  - Backward-compatible at every layer.
- **Cons**:
  - Most surface area. Requires touching 4-6 files and one shell plugin
    in lockstep.
  - Threshold/policy tuning across rigs.
- **Effort**: Medium-High. ~300-500 LOC across Go and shell.

### Recommendation

**Adopt Option 5 (Hybrid) as a phased rollout, but ship Option 4
(witness/refinery coverage) FIRST and standalone**, because that closes the
incident-causing gap immediately with the smallest blast radius:

**Phase 1 (witness/refinery coverage, ~1 day):**
- Add `TouchSessionHeartbeat` calls to the witness and refinery patrol loops
  at the same level polecats already do (cheapest: hook
  `persistentPreRun → touchPolecatHeartbeat` to also fire for `witness`
  and `refinery` GT_ROLE values — already does for `dog` and `deacon`).
- Modify `reap_dead_agent_wisps.go` to prefer heartbeat-based detection
  with `bead.UpdatedAt` as fallback (same as polecat reaper's pattern with
  PID probe).
- Reduce `dead_agent_reap_timeout` default from 2h → 30m once heartbeat
  coverage is proven (gated on `IsV2()` reading).

**Phase 2 (background ticker, ~1 day):**
- Add a session-boot ticker in `polecat_kiro_wrapper.go` (and the equivalent
  witness/refinery boot path) that writes a `liveness_proof` field every
  60s, gated on the parent claude/gt PID being CPU-active in the last 60s
  (use `/proc/<pid>/stat` field 14+15, utime+stime delta).
- This is what *actually* fixes the false-stale-during-LLM-call class.

**Phase 3 (schema v3 + policy unification, ~2 days):**
- Add `expected_idle_until` and `liveness_proof` to `SessionHeartbeat`.
- Update `IsSessionHeartbeatStale` and the stuck-agent-dog jq logic to
  consult both fields.
- Expose `gt heartbeat --hint=long-call --until=+15m` so agents can declare
  known-long phases (e.g., refinery before MQ-batch wait, polecat before
  go test).

The phasing matters because Phase 1 alone restores supervision of the role
(refinery) whose death triggered this design exercise, even before the more
elegant unified policy lands. Phases 2 and 3 are pure incremental
backwards-compatible improvements thereafter.

## Constraints Identified

1. **Shell plugin parity is required, not optional.** Any schema change in
   `polecat/heartbeat.go` MUST be mirrored in `plugins/stuck-agent-dog/run.sh`
   in the same commit, or the dog will fight the witness. There is no test
   harness today that catches this — it's caught by the
   `STUCK_STALLED_THRESHOLD=600s` defensive ratio. Recommend: add a
   cross-language unit test that round-trips a Go-written heartbeat through
   the dog's `heartbeat_age_seconds` and `heartbeat_state` jq logic.

2. **The witness's "single inference" property is load-bearing.** The
   ZFC design from gu-3vr5 explicitly avoids witness-side timer logic
   beyond freshness. New designs must keep that property — agents report
   state, witness reads. `expected_idle_until` is agent-side; the witness
   only checks "is the timestamp + idle window in the future?".

3. **`IsSessionHeartbeatStale` MUST keep returning `(false, false)` for
   missing files.** Otherwise the rollout will mass-reap pre-upgrade
   sessions. Phase 1 must explicitly preserve this fallback.

4. **Polecat sessions and witness/refinery sessions have different blast
   radii.** Reaping a polecat resets one bead; reaping a refinery
   bead can corrupt an in-flight merge stack. The thresholds should be
   role-specific (polecat: 10m, witness: 15m, refinery: 30m), not unified
   on a single value.

5. **The stuck-agent-dog explicitly excludes witness/refinery from its
   scope.** Phase 1 (witness/refinery heartbeats) does NOT change this —
   the daemon's `reap_dead_agent_wisps.go` is the appropriate consumer for
   those roles. The dog stays polecat+deacon-only.

6. **Heartbeats are best-effort writes (`_ = os.WriteFile`).** Any
   mandatory liveness signal added to heartbeats must tolerate write
   failure (disk full, permission denied) without escalating, OR must use
   a different mechanism (e.g., a unix socket the witness pings).

7. **The fallback PID-signal probe in `isSessionProcessDead` must remain.**
   It is the only mechanism that catches sessions whose ticker died but
   whose tmux pane still exists. Removing it would create a class of
   undetectable-dead sessions.

## Open Questions

1. **What is the minimum CPU-active window for the liveness proof to count?**
   60s of accumulated CPU per minute is too strict (idle-but-alive); 1ms
   may be too lax (zombie-spinning-on-fd). Suggested starting point: any
   CPU delta in the last 90s qualifies as alive — needs validation against
   a real LLM-call trace.

2. **Do we need a per-bead `expected_idle_until` or per-session?** A
   polecat working a long bead might want bead-scoped declarations; a
   refinery doing a 20m bisect doesn't have a single bead. Probably both,
   with bead-scope taking precedence.

3. **Should the merge queue's gate scripts (`go test ./...`,
   `check-upstream-rebased.sh`) emit heartbeats themselves?** They run
   inside polecat/refinery sessions, but invocation is via subprocess; the
   parent gt process is blocked. Phase 2's ticker handles this, but
   instrumenting the gates directly would be a defense in depth.

4. **What signal should the witness use to *confirm* a death decision
   before reaping?** Currently `isSessionProcessDead` is "stale heartbeat
   OR PID gone". Should we require BOTH for high-blast-radius roles
   (refinery)? This trades detection latency for false-positive risk.

5. **How do we handle the claude harness's own restart loop?** When claude
   crashes and restarts within the same tmux session, the heartbeat may
   appear continuous (boot wrapper lives across crashes) but the agent
   has lost mid-flight context. Heartbeat freshness alone won't distinguish
   this from a clean session.

6. **Do we expose heartbeat staleness as a queryable signal in `bd`?**
   E.g., `bd list --stale-heartbeat` to find sessions the witness has
   silently identified as wedged. This would help Mayor-level observation
   without requiring another tool.

## Integration Points

### → Heartbeat substrate (`internal/polecat/heartbeat.go`)
- **Load-bearing for all options.** Schema v2 already exists and is the
  right base. Phase 3 adds `liveness_proof` and `expected_idle_until`
  fields; readers ignoring unknowns means rollout is safe.
- The constant `SessionHeartbeatStaleThreshold = 3 * time.Minute` is the
  source of truth for the polecat reaper. Witness/refinery extension
  needs role-specific thresholds — extract as a function-of-role.

### → Persistent pre-run hook (`internal/cmd/root.go:208 touchPolecatHeartbeat`)
- **Phase 1 insertion point.** Add `witness` and `refinery` to the role
  allowlist (currently polecat/crew/dog/deacon). One-line change.
- Renaming this function to `touchAgentHeartbeat` is consistent with the
  expanded scope.

### → Daemon dead-agent reaper (`internal/daemon/reap_dead_agent_wisps.go`)
- **Phase 1 cutover target.** Currently keys on `bead.UpdatedAt` because
  no heartbeat exists. After Phase 1, prefer
  `polecat.IsSessionHeartbeatStale(townRoot, sessionName)` and fall back
  to `UpdatedAt` for sessions still on the pre-rollout path.
- The 2h default timeout drops to ~30m once heartbeats are reliable.

### → Polecat liveness check (`internal/polecat/manager.go:isSessionProcessDead`)
- Already implements the right policy: heartbeat first, PID probe fallback.
- Phase 3 must extend to honor `expected_idle_until` (don't declare dead
  during agent-declared idle window).

### → Stuck-agent-dog plugin (`plugins/stuck-agent-dog/run.sh`)
- **Cross-language coupling risk.** `heartbeat_age_seconds` and
  `heartbeat_state` jq functions parse the schema in shell. Any v3 field
  used for liveness must be parsed here too.
- Recommend: add a `gt heartbeat-status --session=<name>` Go command that
  the dog can call instead of re-implementing parsing. Single source of
  truth.

### → Polecat boot wrapper (`internal/cmd/polecat_kiro_wrapper.go`)
- **Phase 2 insertion point** for the background ticker. The wrapper
  already manages session lifecycle; adding a goroutine that touches the
  heartbeat with a CPU-liveness proof every 60s is a natural extension.
- Witness/refinery have their own boot paths in `internal/witness/` and
  `internal/refinery/` that need parallel changes.

### → `gt heartbeat` command (`internal/cmd/heartbeat.go`)
- **Phase 3 extension point** for `--until=<timestamp>` flag and
  `--hint=<text>` for free-form context. The existing `--state` flag is
  already the right abstraction.
- Consider `--proof=auto` to invoke the CPU-liveness check inline for
  agents that can't run a background ticker.

### → Operational config (`internal/config/operational.go`,
        `internal/config/types.go`)
- Add `witness_reap_timeout` and `refinery_reap_timeout` fields beside the
  existing `dead_agent_reap_timeout`. Defaults: 15m and 30m respectively.
- Per-rig overrides in rig settings (existing pattern).

### → Witness handlers (`internal/witness/handlers.go:2496`)
- The witness's "single inference" comment is the design principle for
  this dimension. Extend the comment to enumerate the v3 inputs: freshness,
  state, expected_idle_until.

### → Cross-dimension dependencies
- **Data dimension**: defines the schema v3 fields and their semantics
  (especially `liveness_proof` content and `expected_idle_until` cap).
- **Scale dimension**: the ticker frequency (60s × N polecats × M rigs) is
  a write-amplification concern for the file system; needs sizing.
- **Security dimension**: `expected_idle_until` is gameable by a wedged
  agent. Caps and per-rig limits are policy decisions that belong here.
- **API dimension**: the `gt heartbeat-status` and `gt heartbeat
  --until=...` interfaces are the agent-facing contract.
- **UX dimension**: how the operator sees and overrides false-positive
  reaps; how the witness reports declared idle windows in `gt status`.

## Sources

- `internal/polecat/heartbeat.go` (heartbeat v2 schema and stale threshold)
- `internal/cmd/heartbeat.go` (gt heartbeat command and state validation)
- `internal/cmd/root.go:200-228` (touchPolecatHeartbeat persistent pre-run hook)
- `internal/polecat/manager.go:2199-2246` (isSessionProcessDead fallback policy)
- `internal/daemon/reap_dead_agent_wisps.go` (witness/refinery reaper using bead.UpdatedAt proxy)
- `internal/daemon/daemon.go:1190-1191, 3432-3456` (reap orchestration and gu-1x0j context)
- `internal/config/operational.go:50-55, 355-365` (DefaultDeadAgentReapTimeout = 2h)
- `internal/config/types.go:331-337` (DeadAgentReapTimeout config field)
- `plugins/stuck-agent-dog/run.sh` (STUCK_STALLED_THRESHOLD=600s, jq parsers, scope exclusion)
- `internal/witness/handlers.go:2496` (single-inference design principle)
- `bd show gu-leg-dfppe` (assignment context: incident gu-rh0g, gu-0nmw)

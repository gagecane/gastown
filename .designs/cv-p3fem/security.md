# Security Analysis

## Summary

The false-stale-heartbeat problem is *itself* a security/safety incident in
slow motion: when supervision can't tell "dead" from "busy", the system either
**under-acts** (real corpses pile up — the 2026-05-27 refinery death with no
auto-recovery) or **over-acts** (the stuck-agent-dog mass-kill of 13 live
polecats). Any redesign that adds liveness signals, self-reported state, or
external watchdogs *expands the trust surface that supervision relies on*.
The goal of this dimension is to make sure the new heartbeat scheme is at
least as hard to mislead as the existing one — ideally harder — without
inheriting the cure-worse-than-disease pathologies that have already
materialized in this codebase.

Gas Town's deployment model (single-user, single-host, no network-exposed
APIs) means the dominant adversary is **not an external attacker**, it is a
**confused or compromised same-UID agent** combined with **the supervision
system over-reacting to its own bad readings**. The two highest-impact
threats are (1) a heartbeat scheme that lets a runaway agent extend its own
life indefinitely (stuck-but-self-reporting-OK, holding a slot forever),
and (2) a heartbeat scheme that lets one agent or one corrupted file trigger
fleet-wide kill cascades. Both have already happened under the current
design; both are exploitable by any local-file write the same UID can
already perform. Defense-in-depth recommendations below focus on bounding
blast radius (single-bad-signal cannot trigger multi-agent kills) and
making "agent self-reports OK forever" detectable through orthogonal
signals (process liveness, tool-call timestamps, parent supervision).

## Analysis

### Key Considerations

- **The heartbeat is a same-UID file under `~/gt/.runtime/heartbeats/`.** Any
  process running as the user can read or write any session's heartbeat.
  There is no authenticator. The current write is `os.WriteFile(..., 0644)`
  — world-readable, user-writable. This is fine for cross-host isolation
  (none) but means a buggy agent can extend or terminate the apparent life
  of any other agent in the fleet by toggling one JSON file.

- **Self-reported state (heartbeat v2: `working|idle|exiting|stuck`) is a
  trust delegation, not an authentication.** The witness's stuck-agent-dog
  treats `state=exiting` and `state=idle` as "do not flag as stalled" (see
  `plugins/stuck-agent-dog/run.sh` lines 286–303). That means an agent that
  writes `{"state":"exiting"}` once and then stops touching the file is
  *invisible to the supervision system forever*. Today this is mitigated
  only by gravity (no agent has a reason to lie); a future bug — or an
  agent that crashes mid-`gt done` after writing `state=exiting` — produces
  a permanently-suppressed corpse in the slot pool. Slot exhaustion *is*
  the failure mode that lets the convoy synthesis stall (see gu-ybjb).

- **A missing heartbeat is currently treated as "alive".** `IsSessionHeartbeatStale`
  returns `false, false` when the file is absent
  (`internal/polecat/heartbeat.go:121-127`). The comment correctly notes this
  is intentional (rollout-period false-positive avoidance) but as a
  long-term posture it is a denial-of-supervision: *delete the heartbeat
  file and the witness will leave the agent alone*. Once heartbeat-v2 is
  the steady state, fail-open should flip to fail-checked-via-orthogonal-signal,
  not fail-open-forever.

- **Timestamps are agent-claimed and unauthenticated.** Nothing prevents an
  agent (or external `touch`) from writing `{"timestamp":"3025-01-01T..."}`.
  The dog falls back to `stat -c %Y` mtime when JSON parsing fails
  (`run.sh:147-148`), which is also spoofable by `touch -d` without
  changing JSON content. A scheme that only widens the heartbeat record
  (more fields, longer "context" strings) without binding the timestamp
  to something monotonic or process-derived will keep this gap.

- **Mass-death is a CRITICAL escalation gated only on count.** `run.sh:377-385`:
  `TOTAL_ISSUES >= 3` → CRITICAL. Three corrupt heartbeats, three
  simultaneously-stale files from a transient FS issue, or three same-UID
  bad writes in a 5-min window are sufficient to wake the Mayor at any
  hour. The fix family here is "require independent corroboration"
  (process-table check, parent supervision tree, recent Dolt commits) before
  any escalation that has fleet-wide blast radius.

- **`session_name` flows directly into a path join with no validation.**
  `heartbeatFile()` = `filepath.Join(townRoot, ".runtime", "heartbeats",
  sessionName + ".json")`. `filepath.Join` *does not strip `..` segments*.
  If any caller passes a session_name containing `../`, the write/read
  escapes the heartbeats dir. Today every caller derives the session_name
  from a controlled tmux session name, but the contract isn't enforced at
  the heartbeat boundary. A new scheme that introduces "external watchdog
  writes heartbeats" or "heartbeats from mail-derived names" needs a
  validator at the function entry.

- **Heartbeat content may flow into LLM prompts.** The v2 format adds a
  `Context` string ("what the agent is doing") and a `Bead` field. If the
  witness or any planning agent surfaces these into an LLM context — for a
  status report, escalation summary, or a Mayor view — the heartbeat file
  becomes a **prompt-injection vector** that any same-UID writer can
  exploit. Quoting/escaping at the LLM-prompt boundary is the right place
  to defend; pre-sanitizing the field on write is fragile.

- **Out-of-band recovery markers (gu-v5mk) already exist.** `has_recovery_marker`
  in run.sh suppresses RESTART actions when an operator has performed a
  manual recovery. This is a useful precedent: any heartbeat redesign
  should preserve a crisp "operator override beats automation" channel,
  not bury it behind a state machine the operator must reverse-engineer.

### Options Explored

#### Option 1: Multi-signal AND-of-corroboration (recommended)

- **Description**: Replace "heartbeat fresh? → alive" with a vote across
  three orthogonal signals: (a) heartbeat-file age, (b) tmux session +
  pane process is the expected agent binary, (c) at least one Dolt commit
  or `bd update` from this session within a longer window. **An agent is
  "dead" only if all three say dead.** An agent is "stalled" if heartbeat
  is stale but (b) and (c) disagree. Mass-kill thresholds require *each
  agent independently* to fail all three signals; corruption of one
  channel cannot fan out.
- **Pros**: Bounds blast radius. Single-file tampering can't extend life
  (still need Dolt commits) or shorten it (still need tmux session
  missing). Survives fleet-wide FS hiccups (one missing file isn't a kill).
  Reuses signals already collected in `stuck-agent-dog`.
- **Cons**: More complex than today's single check. Requires a small Dolt
  query per polecat per cycle (mitigation: batch with `bd list --json
  --since=`). The "Dolt commit since N min" check needs a session→author
  map that's robust under handoffs.
- **Effort**: Medium. The corroboration logic largely exists in run.sh —
  consolidating it into a shared `internal/agentliveness` package is the
  primary cost.

#### Option 2: Watchdog-touched heartbeats from a separate process

- **Description**: A small, low-overhead supervisor binary (already running
  as the agent's parent — e.g. tmux pane wrapper) emits the heartbeat.
  Agent-internal long calls don't have to remember to touch.
- **Pros**: Eliminates "LLM call too long → false stale" entirely without
  expanding the agent's internal hook surface.
- **Cons**: **Severe security regression unless carefully scoped.** A
  watchdog that emits heartbeats *for the agent process* must observe the
  agent (open FDs, pid, `/proc/<pid>/status`); if it has write access to
  every session's heartbeat file, it becomes a single point of compromise
  whose failure mode is "every polecat appears alive forever". Also
  obscures the truth (heartbeat fresh ≠ agent making progress; just means
  the watchdog is alive). Adds a new daemon to monitor.
- **Effort**: High. Even a "good" version requires per-session sandbox of
  the watchdog's write authority.

#### Option 3: HMAC-signed heartbeats with rotating per-session secret

- **Description**: At session start, the agent generates a secret stored in
  a 0600 file readable only by the agent's UID; heartbeat writes include
  `{"hmac": HMAC(secret, timestamp||state||bead)}`. Witness verifies.
  Cross-agent spoofing within the same UID becomes harder (attacker needs
  the secret file too).
- **Pros**: Detects cross-agent forgery. Defends against a curious or
  buggy agent extending another's life.
- **Cons**: **Limited threat reduction in a single-UID model.** Anything
  that can write the heartbeat file can read the sibling secret file
  (same UID, same dir). Adds key-management complexity and a new failure
  mode (secret-file lost → agent can't heartbeat → false dead). Net
  negative in this deployment model unless we also move to per-agent UIDs,
  which is a much larger architectural change.
- **Effort**: Medium for crypto wiring; High once you add key rotation
  and recovery paths.

#### Option 4: Drop self-reported state; rely on liveness probes only

- **Description**: Roll back v2's `state` field; witness infers state from
  external observations (process running, recent file/Dolt activity,
  recent tmux pane output). Agent is a passive subject of inspection.
- **Pros**: Removes the "agent lies and never gets reaped" class entirely.
  No new attack surface in the file format.
- **Cons**: Reintroduces the original false-stale problem this convoy
  exists to solve (long LLM call has no external symptom for minutes).
  Forces witness to rely on heuristics that get the wrong answer for
  legitimate long ops. Loses the "exiting" signal that meaningfully
  reduces RESTART thrash during gt done.
- **Effort**: Low to implement, High in operational regression.

#### Option 5: State + signed expiry (TTL-bounded self-reports)

- **Description**: Keep self-reported state, but require it to carry an
  explicit expiry `valid_until` set by the agent (e.g. "I'll be in
  `working` until +5m; ask again then"). Stuck-agent-dog treats state as
  authoritative *only inside its TTL*; once expired, fall through to
  liveness corroboration. `state=exiting` gets a hard cap (≤ 60s) so a
  crash mid-`gt done` doesn't permanently suppress detection.
- **Pros**: Closes the "wrote `exiting` once and died" hole without giving
  up self-report ergonomics. Forces the agent to keep affirming, which
  doubles as a liveness signal. Backwards compatible (missing `valid_until`
  → use legacy stale-threshold).
- **Cons**: Adds a clock-skew failure mode (agent and witness must agree
  on time; usually fine on one host). Slightly more bookkeeping for
  agents that must refresh near long-call boundaries.
- **Effort**: Low. Pairs well with Option 1.

### Recommendation

**Adopt Option 1 (multi-signal corroboration) as the trust foundation,
combined with Option 5 (TTL-bounded self-reports) for the agent-friendly
fast path.** Together they:

1. Make single-file tampering insufficient to extend or terminate any
   agent's life — *neither under-action nor over-action can be triggered
   by one bad write*.
2. Cap the worst-case suppression window for any self-reported state to
   the TTL (default ≤ 5 minutes, hard ceiling 15 minutes), so a crash in
   `state=exiting` does not become a permanent invisibility cloak.
3. Keep `IsSessionHeartbeatStale` simple at the call site by lifting the
   corroboration into a shared `agentliveness` package with one entry
   point: `Classify(session) → {Healthy, Working, Stalled, Dead, Unknown}`.
4. Require at least 2 of 3 *independent* signals to flip into `Dead` for a
   given agent before *any* automated kill or escalation fires; require
   ≥ 3 agents in `Dead` *each independently corroborated* before a
   mass-death CRITICAL.

Reject Option 2 (external watchdog) for now: blast radius is too high for
the single-UID deployment model, and Option 1 closes the same false-stale
problem without a new privileged daemon. Reject Option 3 (HMAC) until and
unless we also move to per-agent UIDs (much bigger change).

## Constraints Identified

- **Backward compatibility with heartbeat v1/v2 file format must be
  preserved.** Live polecats are mid-flight today; rolling out a
  schema-breaking change requires a migration (see Integration Points).
- **No new daemons added to the supervision path.** Witness, refinery,
  and stuck-agent-dog cover the supervision surface; adding a watchdog
  process would require new monitoring of *its* liveness — recursion.
- **`IsSessionHeartbeatStale` must remain cheap.** It's called from hot
  paths in the witness loop; the multi-signal version must be O(1)
  per-call with the Dolt corroboration on a slower cadence (cached, e.g.,
  60-second window).
- **Mass-kill threshold must require independent corroboration per agent.**
  The current `TOTAL_ISSUES >= 3` rule is the single-point-of-failure
  most likely to be exploited by a transient FS issue. Any redesign must
  make a single class of bad signal incapable of summing to a CRITICAL.
- **`session_name` validation must happen at the heartbeat-file boundary.**
  Independent of the redesign, restrict to `^[A-Za-z0-9_.-]+$` and reject
  `..` substrings before any path join. Today this is implicit through
  callers; the new design widens the caller set.
- **Operator override (recovery marker, gu-v5mk) must continue to win.**
  Any new automated-action gate has to consult `gt polecat is-recovered`
  before acting. This is non-negotiable; manual recovery has saved fleet
  state more than once.
- **`Context` and `Bead` fields are untrusted input from the perspective
  of any LLM that consumes them.** They must be quoted/escaped at every
  prompt boundary; do not pre-sanitize on write (loses signal for
  debugging) but treat them as adversarial when injecting into a prompt.

## Open Questions

- **Q1: Should we add a session-start nonce to make heartbeat replay
  detectable?** A 64-bit random per session, stored in the file, would
  let the witness detect a stale heartbeat that an attacker copies forward
  across a session restart. Cost: tiny. Value: low in single-UID model
  but real for "agent crashed and restarted, old heartbeat copy still
  there" detection. Needs a decision: yes (cheap insurance) or no (YAGNI).

- **Q2: What is the policy when the orthogonal signals disagree?** E.g.
  heartbeat says `working`, tmux session is alive, but Dolt has no
  commits in 30 minutes. Is that "stalled" or "long-running LLM call"?
  My recommendation: classify as `Working/quiet` and surface a soft
  warning at 60 minutes; only escalate to `Stalled` if heartbeat *also*
  goes stale. But this needs sign-off from whoever owns the witness
  cadence policy.

- **Q3: Should heartbeats live in Dolt instead of/alongside files?**
  Putting them in Dolt would give us atomicity, cross-host visibility (if
  ever needed), and audit history. Cost: every heartbeat is a permanent
  commit (currently nudges are explicitly the cheap channel because mail
  has this exact cost). Probably no — but worth asking explicitly so the
  decision is recorded.

- **Q4: How does this interact with the deacon fall-back to patrol-file
  mtime?** The dog already has a fallback path (`run.sh:357-371`) that
  uses `~/gt/deacon/heartbeat.json` mtime when no session heartbeat
  exists. mtime can be touch-spoofed. Should the deacon's patrol-file
  fall-back move to the same multi-signal classifier, or remain as-is
  with the understanding that deacon is a single agent and the threat
  surface is smaller? My lean: fold deacon into the same classifier so
  there's one liveness story to reason about.

- **Q5: Does heartbeat-v3 (or whatever this becomes) need a `parent_pid`
  field for tree-corroboration?** If the witness can verify "session's
  pane PID has parent = the agent runner I expect", that's a strong
  liveness signal. Cost: one extra field; needs platform-aware code
  (Linux `/proc/<pid>/status`, macOS `sysctl`).

## Integration Points

- **Data dimension**: The new heartbeat schema (additional `valid_until`,
  optional `nonce`, optional `parent_pid` fields) must be coordinated
  with whatever data design is chosen. v3 must remain forward-readable
  by v1/v2 consumers — the existing `IsV2()` test on the `state` field
  is the right shape; we need an analogous `IsV3()` predicate.

- **API dimension**: `IsSessionHeartbeatStale` becomes
  `IsSessionAlive(session) → (alive bool, reason string)` returning the
  tri-state `Healthy/Stalled/Dead`. The shell side
  (`stuck-agent-dog/run.sh`) needs the same logic available without
  re-implementing the multi-signal check; expose via a `gt agent
  liveness <session>` CLI rather than re-grepping JSON in bash.

- **Scale dimension**: The Dolt-corroboration signal must be
  cache-friendly. One cached `bd list --json --since=15m` per dog cycle
  can serve all polecats in a rig; do not query per-polecat. This bounds
  the cost added by the new check to one Dolt round-trip per 5-minute
  cycle rather than N (where N is the polecat count).

- **UX dimension**: When the classifier reports `Stalled` rather than
  killing, surface this in `gt status` / `gt peek` so the operator can
  see "polecat ghoul: heartbeat stale 8m, tmux alive, no Dolt commits
  10m → STALLED (not killed)". This is also where the operator decides
  to manually intervene before the automation does.

- **Integration with stuck-agent-dog plugin**: Mass-death threshold
  (`run.sh:377`) must require *each* agent in the count to be
  `Dead`-per-classifier, not `CRASHED|STUCK|STALLED` as today. Concretely:
  rename the count to `TOTAL_DEAD = ${#CRASHED[@]} + count_of(STUCK |
  STALLED where corroboration also says Dead)`.

- **Integration with `reapDeadAgentWisps` (witness/refinery)**:
  `internal/daemon/reap_dead_agent_wisps.go` currently uses tmux session
  liveness + bead `updated_at` age as its tuple. This is already a
  multi-signal check and is a useful pattern to harmonize with — both
  reapers should call into the same `agentliveness.Classify` so witness
  reaping and polecat reaping share one source of truth.

- **Integration with the existing recovery marker (gu-v5mk)**: The new
  classifier consults the marker as a *short-circuit* — `is-recovered`
  → return `Healthy` regardless of other signals — exactly as run.sh
  does today.

## Sources

- [Polecat heartbeat module](internal/polecat/heartbeat.go) — accessed 2026-05-29
- [stuck-agent-dog plugin run.sh](plugins/stuck-agent-dog/run.sh) — accessed 2026-05-29
- [reapDeadAgentWisps daemon code](internal/daemon/reap_dead_agent_wisps.go) — accessed 2026-05-29
- [Bead gu-leg-pflxi](bd://gu-leg-pflxi) — accessed 2026-05-29
- Referenced beads (per assignment context): gu-rh0g (refinery death), gu-0nmw (auto-recovery gap), gu-v5mk (recovery marker), gu-bfwa (stalled-alive detection), gu-w9or (clean-exit signals), gu-ybjb (slot exhaustion / DEFERRED route), gu-3vr5 (heartbeat v2 design)

# Scalability Analysis

## Summary

The heartbeat-staleness problem (false-stale heartbeats during long LLM/build
calls) is, at today's scale, almost entirely a **correctness** problem rather
than a resource problem. The heartbeat data plane is microscopically cheap:
~64 bytes per file, ~127 files town-wide today (~524KB on disk), one read
per agent per supervisor cycle (every 5m). Even at 100x — ~13,000 heartbeat
files, ~50MB on disk, ~13,000 jq parses per 5m cycle — the file-based
heartbeat layer would still fit on a single laptop without sweat. **Storage
and I/O are not the bottleneck and will not become one within plausible
Gas Town scale.**

The scaling concerns are second-order: (1) the **liveness-decision read path**
in `stuck-agent-dog` shells out per agent (jq + date arithmetic) and is
linear-with-loud-constants; (2) the **heartbeat write path** is implicit —
it piggybacks on `bd`-touched gt commands, so any redesign that requires
agents to emit heartbeats *during* long LLM calls must add a new keepalive
mechanism (background goroutine, separate process, or external pinger) which
multiplies process count by 1.x–2x; (3) **detection latency** vs. **false-
positive rate** trade off non-linearly with fleet size — at 100 polecats,
even a 0.1%/cycle false-positive rate produces a kill every cycle, which
amplifies into mass-death cascades like gs-549 and the 2026-05-19 incident.
The right design must make the liveness inference **agent-pushed and self-
declared** (heartbeat v2 already does this) and keep the supervisor read
path O(N) but with cheap constant factors. Avoid per-agent shellouts and
avoid Dolt as the heartbeat substrate.

## Analysis

### Key Considerations

**Scale dimensions that matter for this feature:**

- **Agent fleet size**: Currently ~13 polecats + ~3 rigs × (witness +
  refinery + deacon) ≈ 22 long-lived sessions. Heartbeat dir holds 127
  files (includes stale entries from past sessions — orphan accumulation
  is real). Headroom target: 100 polecats, 10 rigs, ~150 sessions.
- **Heartbeat write rate**: Today, writes are coupled to `bd`-touched gt
  invocations (claim, status update, mail, etc.). Empirically a working
  polecat touches its heartbeat 0–5 times/min during normal flow and
  **zero times during a long LLM/build call**. This is the entire bug.
- **Heartbeat read rate**: stuck-agent-dog runs every 5 min, scans all
  polecat sessions, parses each heartbeat with jq. Witness patrol cycles
  also peek. Reads are bursty, not steady-state.
- **Liveness-inference granularity**: Today it's "mtime older than
  threshold" with a 3-min witness threshold and 600s plugin threshold.
  v2 (gt-3vr5) added agent-reported state but the *freshness* check is
  unchanged.
- **Detection latency budget**: Design constraint says <10 min to detect
  truly-dead refinery. That's 2 dog cycles at 5-min cadence — tight but
  workable.
- **False-positive cost**: Each false kill consumes a polecat slot (no-op
  kill+spawn), inherits the same load conditions, and risks restarting
  into a still-stalling tool call → cascade. The 2026-05-19 incident drained
  the entire pool from a single false-positive class.

**Resource consumption per heartbeat operation:**

| Operation | Cost | Notes |
|-----------|------|-------|
| Write heartbeat | 1 syscall (~µs) + 64–128 bytes | `os.WriteFile`, no fsync |
| Read heartbeat | 1 read + json.Unmarshal | ~100µs in Go |
| Read via jq (plugin) | fork + exec jq + parse | ~10–30ms per file |
| Liveness inference | time arithmetic | trivial |
| `gt witness` scan | 1 readdir + N reads | O(N) in fleet size |

The jq shellout is **300x more expensive** than the equivalent in-process
parse. At 13 polecats × 1 dog cycle / 5min, this is invisible. At 100
polecats, it's still <3s of wall time per cycle — acceptable, but the
constant factor should be reduced now while it's cheap to do so.

### Options Explored

#### Option 1: Keep file-based, add background keepalive thread
- **Description**: Each agent process spawns a goroutine that touches the
  heartbeat every N seconds (e.g. 30s) regardless of what the main loop
  is doing. Long LLM/build calls are no longer invisible because the
  keepalive thread continues to fire. Heartbeat v2 state field is still
  agent-set on transitions; the keepalive only refreshes the timestamp.
- **Pros**: Backward-compatible with current file format. Decouples
  heartbeat freshness from bd-touch cadence (the bug). Tiny resource
  footprint: 1 extra goroutine + 1 file write/30s/agent (~7 writes/min
  for 200 agents = trivial). Read path unchanged.
- **Cons**: Adds a goroutine to every gt-spawned process. Doesn't help
  if the *whole agent process* is wedged (the main loop and the keepalive
  share fate) — though that's exactly what we want to detect. Requires
  audit of every gt entrypoint to ensure the keepalive starts.
- **Effort**: Low. ~50 LOC in `internal/polecat/heartbeat.go` plus
  startup hook.

#### Option 2: External pinger daemon (per-rig or town-wide)
- **Description**: A separate long-lived `gt-heartbeat-pinger` daemon
  inspects each agent's tmux session liveness and writes heartbeats on
  the agent's behalf. Agents only update the *state* field on transitions.
- **Pros**: Decouples heartbeat liveness from agent process health
  entirely — if the pinger sees the tmux session is alive (pane attached,
  pid running), it pings. Scales O(N) in fleet size at a single point.
- **Cons**: **Inverts the abstraction**: the supervisor is now also the
  signal source. If the pinger dies, *every* agent looks dead. Adds a
  new SPOF. Conflates "tmux session alive" with "agent making progress" —
  exactly the conflation that produced gs-549. **Recommend against.**
- **Effort**: Medium.

#### Option 3: Move heartbeats to Dolt
- **Description**: Replace files with a Dolt table (`heartbeats(session,
  timestamp, state, context, bead)`). Writes via SQL INSERT ... ON
  DUPLICATE.
- **Pros**: Centralized, queryable, durable, replicated.
- **Cons**: **Catastrophically wrong at scale.** Every heartbeat write
  is a Dolt commit (per project convention) — at 200 agents pinging
  every 30s, that's 400 commits/min, ~580k commits/day. Dolt 1.86.5
  is documented as fragile under high commit rates and we already have
  budget hygiene rules ("nudges over mail" exists for exactly this
  reason). Read amplification is also worse: SQL round-trip beats jq
  in Go but loses to direct file read in latency. **Recommend against.**
- **Effort**: Medium-high; scaling cost forever.

#### Option 4: PID + tmux liveness as primary, file freshness as confirmation
- **Description**: Supervisor first checks `kill(pid, 0)` and tmux
  has-session; only if the *process* is gone does it then look at
  heartbeat freshness for context. Heartbeats become a "what was the
  agent doing" record, not a liveness oracle.
- **Pros**: Process-level signal is unambiguous and free (one syscall).
  Eliminates entire false-positive class for "alive but quiet during
  LLM call". Latency is excellent (<1s detection).
- **Cons**: Doesn't catch wedged-but-alive (LLM hung, deadlocked
  goroutine). Need a separate signal for that. Also needs careful PID
  tracking — the existing `.runtime/pids/` directory does this but
  must be canonical.
- **Effort**: Low–medium. Most plumbing exists; the change is in
  `stuck-agent-dog` decision logic.

#### Option 5: Hybrid — Option 1 keepalive + Option 4 PID-first
- **Description**: Recommended. Supervisor decision tree:
  1. PID dead AND tmux session gone → **truly dead**, restart.
  2. PID alive AND heartbeat fresh (state≠stuck) → healthy, ignore.
  3. PID alive AND heartbeat stale → **wedged**, investigate (don't
     auto-kill, escalate to operator).
  4. PID alive AND heartbeat state=stuck → agent self-reported, route
     to recovery flow.

  The Option 1 keepalive ensures (3) is rare, because legitimate long
  calls keep the heartbeat fresh. (3) becoming non-rare is itself
  signal that the keepalive is failing — a meta-health signal.
- **Pros**: No false-positives during LLM calls. <10s detection of
  truly-dead. Backward compatible (v2 file format unchanged). Scales
  linearly with no shared global state.
- **Cons**: Two signal sources to maintain.
- **Effort**: Low.

### Recommendation

**Adopt Option 5 (hybrid).** Specifically:

1. Add a 30s keepalive goroutine in every long-lived gt-spawned process
   (polecat session, witness patrol, refinery loop). This is ~50 LOC
   in `internal/polecat/heartbeat.go` and startup hooks. State field
   stays agent-controlled; only timestamp is refreshed.
2. Reorder `stuck-agent-dog` decision logic to consult `kill(pid, 0)`
   and tmux has-session **before** heartbeat staleness. Process death
   is a stronger and cheaper signal.
3. Replace the `jq` shellout with a small Go helper (`gt heartbeat
   --check <session>`) that returns JSON the plugin can consume in one
   exec instead of three. This drops per-agent inspection cost from
   ~30ms to ~3ms — a 10x headroom improvement, free.
4. **Cap heartbeat directory size**: garbage-collect heartbeat files
   for sessions whose tmux session and PID are both gone, on a daily
   sweep. The 127-file directory today already has stale entries
   (e.g. `af-forge.json` from 2026-04-29 is a month old).

### Performance projections

| Scale | Today | 10x | 100x | 1000x |
|-------|-------|-----|------|-------|
| Sessions | ~25 | 250 | 2,500 | 25,000 |
| Heartbeat files | 127 | 1,270 | 12,700 | 127,000 |
| Disk | 524 KB | 5 MB | 50 MB | 500 MB |
| Writes/min (Opt 5) | ~50 | 500 | 5,000 | 50,000 |
| Dog cycle scan | <1s | <3s | ~30s | minutes |
| Bottleneck at this scale | none | none | jq shellout | readdir+fork pressure |

Cliffs:
- **At 100x**, the jq shellout dominates dog cycle wall time. Recommendation
  3 (single Go helper) eliminates this.
- **At 1000x**, even fork-per-session inspection is too slow. Would need
  a long-lived daemon that maintains an in-memory map of session → state,
  rebuilt on inotify events. Not worth designing now.
- **Storage** never becomes a problem — even 500MB of heartbeat files at
  1000x scale is trivial on any modern disk.

## Constraints Identified

- **Backward compatibility**: heartbeat v1 files (timestamp-only) must
  continue to be readable. Heartbeat v2 already handles this via
  `EffectiveState() == HeartbeatWorking` default. Do not break it.
- **Dolt is off-limits as a heartbeat substrate**: every heartbeat would
  become a Dolt commit at current architecture; this is a 1000x+ commit
  amplification we cannot afford.
- **No new SPOFs**: an external pinger daemon is rejected on this basis.
  The supervision system must remain decentralized.
- **No `sudo` / no system packages**: any keepalive implementation must
  be pure-Go, no helper binaries beyond `gt` itself.
- **Detection latency ≤ 10 min** for refineries (gu-0nmw constraint).
  Option 5 achieves <30s for process death; up to 10m for wedge cases.
- **Mass-death cascades must not be possible from this signal alone**.
  The plugin's existing `STUCK_LOAD_DEFER_RATIO` (gs-549 fix) should be
  preserved and audited under the new decision tree.

## Open Questions

1. **Keepalive interval**: 30s is a guess. Trade-off: shorter = faster
   stuck detection but more file writes; longer = less write pressure
   but slower detection. Suggest 30s default, configurable via
   `operational.polecat.heartbeat_keepalive_interval`. **Needs human
   input on tunable.**
2. **Wedge detection authority**: when PID alive + heartbeat stale,
   should the plugin auto-restart, or only escalate? gs-549 argues
   *escalate*; gu-rh0g argues *auto-restart for refineries*. Likely
   answer: refineries auto-restart, polecats escalate. **Cross-dimension
   discussion with security/api.**
3. **Heartbeat file GC policy**: how aggressive? Files accumulate
   (gs-tested: 127 files, some month-old). Suggest daily sweep that
   removes files for sessions with no live PID and no tmux session,
   older than 24h. **Needs cross-check with audit trail — are stale
   heartbeats useful for forensics?**
4. **Per-agent vs per-rig keepalive**: should witness/refinery share the
   keepalive primitive, or each own theirs? Sharing is DRY but adds
   coupling. Suggest a shared `heartbeat.Keepalive(townRoot, sessionName,
   stop <-chan struct{})` helper.

## Integration Points

- **Data dimension** (data.md): heartbeat file schema is the data model.
  Any state machine additions (e.g. new agent-reported states) need
  data-dimension blessing for forward/back compat. The file is small
  enough that schema additions are cheap.
- **API dimension** (api.md): `gt heartbeat --check <session>` (rec. 3)
  is a new CLI surface. Define exit codes and JSON shape.
- **Security dimension** (security.md): heartbeat files are world-readable
  in `~/.runtime/heartbeats/` today. State field carries `context` and
  `bead` ID — not secrets, but adversarial agents in a multi-user box
  could spoof them. Out of scope per Gas Town single-user model, but
  flag it.
- **UX dimension** (ux.md): `gt witness` and `gt peek` should expose
  the new state machine clearly — operator-facing display of
  "stuck (self-reported)" vs. "wedged (timer)" vs. "dead (pid gone)"
  improves debuggability.
- **Integration dimension** (integration.md): the keepalive goroutine
  must be started uniformly across every gt entrypoint that creates a
  long-lived session. A single `polecat.Bootstrap()` helper called from
  `cmd/polecat.go`, `cmd/witness.go`, `cmd/refinery.go` reduces drift
  risk vs. ad-hoc startup in each.

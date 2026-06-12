# Heartbeats

Gas Town has **three distinct heartbeat stores**. They have different readers
and thresholds, so Deacon heartbeat commands refresh the Deacon-specific stores
together to avoid false "stuck agent" escalations (see hq-qxl9: a Deacon
refreshed its session heartbeat while the file store aged past threshold).

## The three stores

### 1. Deacon heartbeat file — `<townRoot>/deacon/heartbeat.json`

- **Written by:** `gt deacon heartbeat [action]` and `gt heartbeat` when
  `GT_ROLE=deacon` → `deacon.Touch()` / `deacon.TouchWithAction()`
  (`internal/deacon/heartbeat.go`).
- **Read by:** the stuck-agent-dog plugin (parses the JSON `timestamp`, falling
  back to mtime for malformed legacy files, and cross-checks tmux activity
  before escalating) and the Go daemon (`deacon.ReadHeartbeat`; thresholds 5m
  stale / 20m very-stale → poke).
- **Also touches:** the legacy `deacon/.deacon-heartbeat` mtime file for old
  shell scripts.

### 2. Session heartbeat (per-session state store)

- **Written by:** `gt heartbeat [--state=working|idle|exiting|stuck]` →
  `polecat.TouchSessionHeartbeatWithState()`. Requires `GT_SESSION`.
- **Read by:** the Witness, which reads the self-reported state instead of
  inferring liveness from timers (ZFC: gt-3vr5). This is the store polecats
  refresh.

### 3. Agent-bead label — `heartbeat:<EPOCH>` on the agent bead (e.g. `hq-deacon`)

- **Written by:** `gt mol await-signal` on each timeout/signal wake
  (`updateAgentHeartbeat` in `internal/cmd/molecule_await_signal.go`). A
  label rewrite is used because `bd agent heartbeat` was never shipped
  (steveyegge/beads#2828). Deacon heartbeat commands also sync this label when
  it is older than half of the stale threshold.
- **Read by:** Witness second-order monitoring ("who watches the watchers"):
  Witnesses check the Deacon's bead activity and alert the Mayor if it looks
  unresponsive (>5 minutes per the patrol formula).
- **Gotcha:** a session that never reaches `await-signal` (handoff churn,
  session limits, one very long patrol turn) leaves this label stale for
  hours even though the agent is healthy.

## Liveness verdict and signal precedence

Different commands answer "is this session alive?" by reading different signals.
When they disagree, this is the precedence (gu-d5r8c):

1. **PID-corroborated liveness verdict is authoritative.** The typed verdict
   (`polecat.LivenessWithPID`) combines the session heartbeat with the live
   tmux pane PID. A stale heartbeat whose PID is still alive resolves to
   **ALIVE**, not MAYBE_DEAD — this is the intended behavior, not a bug. It
   tolerates transient heartbeat-write lag (high town load, a daemon restart,
   one long agent turn) without false positives. The daemon reaper
   (`manager.isSessionProcessDead`), `gt polecat list` (PID-aware via
   `CheckSessionHealth`), and `gt witness status` all consult the PID, so they
   agree.

2. **`MAYBE_DEAD` is informational, never destructive.** It means the heartbeat
   aged past the stale threshold *and* no corroborating signal confirmed
   liveness. Operators may investigate (log, notify) but supervision must not
   kill or reap on MAYBE_DEAD alone — only **DEAD** (heartbeat stale *and* PID
   provably gone) is actionable. A burst of MAYBE_DEAD rows during high load is
   expected heartbeat lag, not a mass stall.

3. **`gt peek` "session not found" is a point-in-time miss, not a liveness
   verdict.** `peek` (and `gt session capture`) only checks `tmux has-session`
   at the instant it runs; it does not read the heartbeat or PID. If `gt polecat
   list` reports `session: alive` but `gt peek` returns "session not found", the
   session was created or destroyed between the two calls (a TOCTOU race during
   spawn/teardown churn). **Retry `gt peek`** — if `polecat list` still says
   alive, the session exists. Treat the corroborated verdict, not a single
   `peek`, as ground truth for recovery decisions.

## Rules of thumb

- **Deacon sessions:** `gt deacon heartbeat` refreshes the Deacon file and
  throttled bead label. `gt heartbeat` also refreshes the session store and,
  when `GT_ROLE=deacon`, uses the same Deacon file/label sync path.
- **Polecats / Witness / Refinery:** `gt heartbeat` (session store) is the
  one that matters.
- **Monitoring scripts:** never declare an agent stuck from a single store.
  Cross-check tmux session activity (`tmux display-message -p
  '#{window_activity}'`) before escalating — a live session with a stale
  store is *heartbeat-write divergence*, not a stuck agent. The
  stuck-agent-dog plugin does this since hq-qxl9.

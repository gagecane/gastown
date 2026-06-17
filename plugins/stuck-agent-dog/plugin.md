+++
name = "stuck-agent-dog"
description = "Context-aware stuck/crashed agent detection and restart for polecats and deacons"
version = 1

[gate]
type = "cooldown"
duration = "5m"

[tracking]
labels = ["plugin:stuck-agent-dog", "category:health"]
digest = true

[execution]
# 10m (was 5m): scanning ~14 polecats + deacon performs many serial bd/jq/tmux
# queries per session and was hitting the 5m wall (gu-brrm). 10m gives headroom
# for the long tail until parallelization lands.
timeout = "10m"
notify_on_failure = true
severity = "high"
+++

# Stuck Agent Dog

Detects stuck or crashed polecats and deacons by inspecting tmux session context
before taking action. Unlike the daemon's blind kill-and-restart approach, this
plugin checks whether an agent is truly unresponsive before restarting.

**Design principle**: The daemon should NEVER kill workers. It detects and logs.
This plugin (running as a Dog agent with AI judgment) makes the restart decision
after inspecting tmux pane output for signs of life.

Reference: WAR-ROOM-SERIAL-KILLER.md, commit f3d47a96.

## Scope — What You May and May NOT Touch

**IN SCOPE** (these are the ONLY sessions this plugin may inspect or act on):
- Polecat sessions (`<prefix>-<name>`, e.g. `gt-minuteman`)
- Deacon session (`hq-deacon`)

**OUT OF SCOPE — NEVER touch these, under any circumstances:**
- **Crew sessions** (`<rig>-crew-<name>`, e.g. `gastown-crew-bear`). Crew lifecycle
  is managed by the overseer (human), not dogs. Crew members are persistent,
  long-lived, and user-managed. A crew session that looks idle is NOT stuck — it
  is waiting for its human. Killing a crew session destroys the overseer's active
  workspace and is a **critical incident**.
- **Mayor session** (`hq-mayor`)
- **Witness sessions** (`<rig>-witness`)
- **Refinery sessions** (`<rig>-refinery`)
- Any session not explicitly enumerated by the bash scripts in Steps 1-3

**This scope is absolute.** Do NOT extend it based on your own judgment. The bash
scripts enumerate exactly the sessions you should check. If a session does not
appear in `CRASHED[]` or `STUCK[]` arrays, it does not exist for your purposes.

## Per-Rig Skip File (gu-31mpz)

When a systemic bug requires preserving stranded polecat worktrees as forensic
evidence (e.g. gc-hl4lx kept 4 talontriage polecats in forensic-hold for hours),
the dog otherwise regenerates RESTART_POLECAT signals every 5m cycle and the
deacon must manually suppress each one.

`run.sh` reads `${TOWN_ROOT}/.runtime/dog-skip-rigs` at start. Listed rigs are
skipped entirely in the polecat iteration — neither flagged (CRASHED / STUCK /
STALLED / IDENTITY_HOOKED) nor restarted — until the rig is removed.

**Format**: newline-separated rig names. Blank lines and `#` comments ignored.
Whitespace inside lines is stripped.

**Add a rig**:
```bash
echo "talontriage" >> /home/canewiw/gt/.runtime/dog-skip-rigs
```

**Remove a rig** (resume normal monitoring):
```bash
sed -i '/^talontriage$/d' /home/canewiw/gt/.runtime/dog-skip-rigs
```

**Note**: this disables ALL stuck-agent-dog actions on the named rig — including
mass-death corroboration, identity-hook escalation, and stalled-alive restarts.
Use only as a temporary forensic-hold tool, not as a permanent silencer.

## Step 1: Enumerate agents to check

Gather all polecats and the deacon session. We check crashed sessions
(`session-dead`, work on hook) and confirmed zombie sessions (`agent-dead`).
`agent-hung` is observe-only for polecats.

```bash
echo "=== Stuck Agent Dog: Checking agent health ==="

TOWN_ROOT="$HOME/gt"
RIGS_JSON_PATH="${TOWN_ROOT}/rigs.json"

# Fallback for older/runtime-copied layouts that still expose rigs.json under mayor/.
if [ ! -f "$RIGS_JSON_PATH" ] && [ -f "$TOWN_ROOT/mayor/rigs.json" ]; then
  RIGS_JSON_PATH="$TOWN_ROOT/mayor/rigs.json"
fi

# Read rigs.json for rig names and beads prefixes
# CRITICAL: We need both the rig name (for filesystem paths like $TOWN_ROOT/$RIG/polecats/)
# and the beads prefix (for tmux session names like $PREFIX-$NAME).
# These can differ — e.g. rig "cfutons" may have prefix "CF".
if [ ! -f "$RIGS_JSON_PATH" ]; then
  echo "SKIP: rigs.json not found at $RIGS_JSON_PATH"
  exit 0
fi

if ! RIG_PREFIX_MAP=$(jq -r '
  if (.rigs | type) == "object" then
    .rigs | to_entries[] | "\(.key)|\(.value.beads.prefix // .key)"
  else
    empty
  end
' "$RIGS_JSON_PATH" 2>/dev/null); then
  echo "SKIP: could not parse rigs.json"
  exit 0
fi

# Filter out any malformed/blank rows so partial registry state fails safe.
RIG_PREFIX_MAP=$(printf '%s\n' "$RIG_PREFIX_MAP" | awk -F'|' 'NF >= 2 && $1 != "" && $2 != ""')
if [ -z "$RIG_PREFIX_MAP" ]; then
  echo "SKIP: no rigs found in rigs.json"
  exit 0
fi
```

## Step 2: Check polecat health

For each rig, enumerate polecats and check their session status.
A polecat is a concern if:
- It has hooked work (hook_bead is set)
- Its central runtime-aware health is `session-dead` OR `agent-dead`

Polecat liveness must use `gt session health`, which wraps the central
`tmux.CheckSessionHealth` path. That path reads `GT_PROCESS_NAMES`, `GT_AGENT`,
and `GT_PANE_ID`, so opencode/node/bun detection stays in the shared runtime
configuration instead of a plugin-local process regex. Treat `agent-hung` as
observe-only for polecats; quiet OpenCode research can be legitimate live work.

```bash
CRASHED=()
STUCK=()
HEALTHY=0

while IFS='|' read -r RIG PREFIX; do
  [ -z "$RIG" ] && continue
  # List polecat directories
  POLECAT_DIR="$TOWN_ROOT/$RIG/polecats"
  [ -d "$POLECAT_DIR" ] || continue

  for PCAT_PATH in "$POLECAT_DIR"/*/; do
    [ -d "$PCAT_PATH" ] || continue
    PCAT_NAME=$(basename "$PCAT_PATH")
    # Use beads prefix (not rig name) for tmux session name
    SESSION_NAME="${PREFIX}-${PCAT_NAME}"

    HEALTH_STATUS=$(gt session health "$SESSION_NAME" --json --max-inactivity "${GT_STUCK_AGENT_DOG_MAX_INACTIVITY:-0s}" 2>/dev/null \
      | jq -r '.status // empty' 2>/dev/null || true)

    case "$HEALTH_STATUS" in
      healthy)
        HEALTHY=$((HEALTHY + 1))
        ;;
      session-dead)
        # Check hook/status through the target rig workspace before acting.
        # Only open/hooked/in_progress work is restartable.
        HOOK_BEAD=$(rig_hook_bead "$RIG" "$PCAT_NAME")
        if [ -n "$HOOK_BEAD" ] && bead_restartable "$SESSION_NAME" "$RIG" "$HOOK_BEAD"; then
          CRASHED+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD")
          echo "  CRASHED: $SESSION_NAME (hook=$HOOK_BEAD)"
        fi
        ;;
      agent-dead)
        HOOK_BEAD=$(rig_hook_bead "$RIG" "$PCAT_NAME")
        if [ -n "$HOOK_BEAD" ] && bead_restartable "$SESSION_NAME" "$RIG" "$HOOK_BEAD"; then
          STUCK+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD|agent_dead")
          echo "  ZOMBIE: $SESSION_NAME (agent runtime dead, hook=$HOOK_BEAD)"
        fi
        ;;
      agent-hung)
        HEALTHY=$((HEALTHY + 1))
        echo "  OBSERVE: $SESSION_NAME runtime alive but inactive; not restarting"
        ;;
      *)
        echo "  SKIP $SESSION_NAME: central liveness probe inconclusive"
        ;;
    esac
  done
done <<< "$RIG_PREFIX_MAP"

echo ""
echo "Health summary: ${#CRASHED[@]} crashed, ${#STUCK[@]} stuck, ${#STALLED[@]} stalled, ${#IDENTITY_HOOKED[@]} identity-hooked, $HEALTHY healthy"
```

**Detection cases** (see run.sh for the full logic):

| Case | Session | Agent process | Heartbeat | Hook |
|------|---------|---------------|-----------|------|
| CRASHED | dead | — | — | set |
| ZOMBIE (STUCK[]) | alive | dead | — | set |
| STALLED-ALIVE | alive | alive | stale (>30m, state≠exiting/idle/stuck) | set |
| IDENTITY-HOOKED | alive | alive | — | identity bead (`*-refinery`, `*-witness`, `*-mayor`, `*-deacon`) |
| HEALTHY | alive | alive | fresh | any |

The **stalled-alive** case (gu-bfwa) is the subtle one: both tmux session and
pane process are alive, but the agent has stopped making progress. The
heartbeat file (`.runtime/heartbeats/<session>.json`, touched by every `gt`
command) goes stale when the agent is sitting idle at its prompt. A hooked
polecat with a stale heartbeat and state="working" is sitting on work it
isn't making progress on — kill the session and let the witness respawn it
with a fresh context via `gt prime`.

`STUCK_STALLED_THRESHOLD` (env var, default 1800s/30m) tunes the staleness
threshold. It is deliberately more lenient than the 3-min threshold used
inside the gt daemon (`internal/polecat/heartbeat.go`) because this plugin
runs on a 5-min cooldown and we want six cycles of grace for legitimate
long-running operations (deep LLM calls, full `go test ./...`, Brazil
installs). The original 10m default tripped MASS DEATH on routine convoy
execution and was raised to 30m under gu-9ed0; see the comment block in
`run.sh` for the incident history.

## Step 3: Check deacon health

The deacon session is `hq-deacon`. Check heartbeat staleness from the JSON
`timestamp` field in `deacon/heartbeat.json` (fall back to file mtime only if
the timestamp is missing or malformed). A live Deacon with no `in_progress` work
is not an actionable stuck-heartbeat event; log and skip so idle patrol backoff
does not produce escalation noise.

**Heartbeat-age alone is NOT sufficient (gu-wuduc).** The deacon runs legitimate
long SINGLE operations — `gt patrol report` takes 15-20min — during which it
writes no heartbeat (heartbeat is emitted at op boundaries). A stale heartbeat
on a busy deacon therefore does NOT mean stuck. Before firing `stuck_heartbeat`,
`run.sh` corroborates with **live pane progress**: it captures the tmux pane
twice (`STUCK_PROGRESS_SAMPLE_GAP`s apart, default 3s) and only escalates if the
content is byte-for-byte FROZEN (or the session is gone / pane uncapturable). A
changed pane — streaming tokens, an advancing spinner, the "esc to interrupt ·
NmNNs" elapsed counter — proves liveness and suppresses the escalation. This
killed a recurring false-positive flood (mayor closed ~7 in one session, each a
verify+close cycle with cry-wolf risk burying a real deacon freeze).

```bash
echo ""
echo "=== Deacon Health ==="

DEACON_SESSION="hq-deacon"
DEACON_ISSUE=""
DEACON_DIVERGENCE=""
DEACON_PROCESS_ALIVE=0

if ! tmux has-session -t "$DEACON_SESSION" 2>/dev/null; then
  echo "  CRASHED: Deacon session is dead"
  DEACON_ISSUE="crashed"
else
  DEACON_PID=$(tmux list-panes -t "$DEACON_SESSION" -F '#{pane_pid}' 2>/dev/null | head -1 || true)
  DEACON_COMM=$(ps -o comm= -p "$DEACON_PID" 2>/dev/null || true)
  if [ -z "$DEACON_COMM" ]; then
    echo "  ZOMBIE: Deacon process dead (pid=$DEACON_PID), session alive"
    DEACON_ISSUE="zombie"
  else
    echo "  Process alive: pid=$DEACON_PID comm=$DEACON_COMM"
    DEACON_PROCESS_ALIVE=1
  fi

<<<<<<< HEAD
      if [ "$HEARTBEAT_AGE" -gt 2400 ]; then
        echo "  STUCK: Deacon heartbeat stale (${HEARTBEAT_AGE}s old, >40m threshold)"
        DEACON_ISSUE="stuck_heartbeat_${HEARTBEAT_AGE}s"
=======
  HEARTBEAT_FILE="$TOWN_ROOT/deacon/heartbeat.json"
  if [ -z "$DEACON_ISSUE" ] && [ -f "$HEARTBEAT_FILE" ]; then
    HEARTBEAT_TIME=$(heartbeat_epoch "$HEARTBEAT_FILE" || true)
    NOW=$(date +%s)
    HEARTBEAT_AGE=$(( NOW - ${HEARTBEAT_TIME:-0} ))

    if [ "$HEARTBEAT_AGE" -gt "${GT_STUCK_AGENT_DOG_DEACON_STALE_SECONDS:-1200}" ]; then
      ACTIVITY_TIME=$(tmux display-message -t "$DEACON_SESSION" -p '#{window_activity}' 2>/dev/null || true)
      case "$ACTIVITY_TIME" in
        ''|*[!0-9]*) ACTIVITY_AGE="" ;;
        *) ACTIVITY_AGE=$(( NOW - ACTIVITY_TIME )) ;;
      esac
      if [ -n "$ACTIVITY_AGE" ] && [ "$ACTIVITY_AGE" -le "${GT_STUCK_AGENT_DOG_ACTIVITY_GRACE_SECONDS:-1200}" ]; then
        echo "  DIVERGENCE: heartbeat file stale (${HEARTBEAT_AGE}s) but session active ${ACTIVITY_AGE}s ago — write divergence, not stuck"
        DEACON_DIVERGENCE="heartbeat_write_divergence_${HEARTBEAT_AGE}s_active_${ACTIVITY_AGE}s"
      elif [ "$DEACON_PROCESS_ALIVE" -eq 1 ] && ! has_in_progress_work; then
        echo "  SKIP: Deacon heartbeat stale (${HEARTBEAT_AGE}s old) but process is alive and no in_progress work exists"
>>>>>>> upstream/main
      else
        echo "  STUCK: Deacon heartbeat stale (${HEARTBEAT_AGE}s old, no recent session activity)"
        DEACON_ISSUE="stuck_heartbeat_${HEARTBEAT_AGE}s"
      fi
    else
      echo "  OK: Deacon heartbeat ${HEARTBEAT_AGE}s old"
    fi
  fi
fi
```

## Step 4: Inspect context before acting (AI judgment)

**This is the key difference from daemon blind-kill.** For each crashed or stuck
agent, inspect the tmux pane context to determine if restart is appropriate.

**SCOPE REMINDER: You may ONLY act on entries in the `CRASHED[]`, `STUCK[]`,
`STALLED[]`, and `IDENTITY_HOOKED[]` arrays populated by Steps 2-3. These arrays
contain ONLY polecats and deacon. Do NOT inspect, evaluate, or act on ANY other
sessions (crew, mayor, witness, refinery). If you find yourself considering a
session not in these arrays, STOP.**

**You (the dog agent) must evaluate each case:**

For CRASHED agents (session dead, work on hook):
- This is almost always a legitimate crash needing restart
- Exception: if the polecat just ran `gt done` and the hook hasn't cleared yet
- Check bead status: if the root wisp is closed, the polecat completed normally

For STUCK agents (session alive, agent dead):
- Kill the zombie session, then restart
- `agent-hung` is not STUCK for polecats; central health keeps that observe-only.

For STALLED-ALIVE agents (session + process alive, heartbeat stale, hook set):
- Agent is sitting idle at its prompt with hooked work. GUPP violation.
- Kill the session so the witness respawn can reload context via `gt prime`.
- Hook and worktree are preserved — the new session picks up where the old left off.
- Exception: if `heartbeat.state` is "exiting", "idle", or "stuck", the agent
  has self-reported a benign state — do not flag.

For IDENTITY-HOOKED agents (hook bead is `*-refinery`/`*-witness`/`*-mayor`/`*-deacon`):
- Auto-dispatch's filter should have rejected this hook. A leak indicates a
  bug elsewhere (auto-dispatch filter gap, manual hook error, sling-context leak).
- Escalate rather than restart — restarting will just re-load the same bad hook.
- Mayor/human must unhook manually and fix the upstream dispatch path.

For DEACON stuck (stale heartbeat):
- Capture pane output: `tmux capture-pane -t hq-deacon -p -S -20`
- If output shows active work (recent timestamps, command output), the heartbeat
  file may just be stale — nudge instead of kill
- If output shows no recent activity, escalation is warranted
- Use a stable escalation fingerprint (`stuck-agent-dog:deacon:stuck-heartbeat`)
  for stale-heartbeat events; do not include the age seconds in the fingerprint.

**Decision framework:**
<<<<<<< HEAD
1. If agent is clearly dead (no process, no output) → restart
2. If agent shows recent activity in pane → nudge first, check again next cycle
3. If agent has been stuck for >15 minutes with no pane activity → restart
4. If mass death detected (>3 crashes in same cycle) → escalate, don't restart
5. If polecat has an active manual-recovery marker (gu-v5mk) → skip RESTART_POLECAT
   and emit NUKE_PENDING instead. The witness/mayor sets this marker (via
   `gt polecat mark-recovered <rig>/<polecat>`) after performing an out-of-band
   recovery (e.g. manual `--no-verify` push) so we don't blindly re-run
   already-pushed work and re-hit the same hang.

The marker lives at `<town_root>/.runtime/recovery_markers/<session>.json` and
expires after 30 minutes by default so a forgotten flag can't permanently
disable auto-restart for a slot. `run.sh` checks it via
`gt polecat is-recovered <rig>/<polecat>` (exit 0 = active marker) before
issuing each RESTART_POLECAT.
=======
1. If central health is `session-dead` and hook status is actionable → request restart
2. If central health is `agent-dead` and hook status is actionable → clear zombie, request restart
3. If central health is `agent-hung` → observe/report only; do not restart polecat research sessions
4. If mass death detected (threshold default 3) → escalate and skip all per-agent actions
>>>>>>> upstream/main

## Step 5: Mass death check

If multiple agents crashed in the same cycle, this may indicate a systemic
issue (Dolt outage, OOM, etc.). Escalate instead of blindly restarting all.
The executable script checks this before per-agent actions and skips all
restart/kill loops for that cycle.

```bash
TOTAL_ISSUES=$(( ${#CRASHED[@]} + ${#STUCK[@]} ))
MASS_DEATH=0
if [ "$TOTAL_ISSUES" -ge "${GT_STUCK_AGENT_DOG_MASS_DEATH_THRESHOLD:-3}" ]; then
  MASS_DEATH=1
  echo "MASS DEATH: $TOTAL_ISSUES agents down in same cycle — escalating"
  gt escalate "Mass agent death: $TOTAL_ISSUES agents down" -s CRITICAL
  echo "Skipping per-agent restart/kill actions during mass-death escalation"
fi
```

## Step 6: Take action

For each agent requiring restart:

```bash
<<<<<<< HEAD
# For crashed polecats — file restart request into the deacon inbox.
# The daemon's processRestartPolecatRequests handler claims and actions
# RESTART_POLECAT mail every heartbeat (gu-nep2). Earlier versions of this
# plugin addressed the mail to "$RIG/witness" but no Go handler ever
# processed it, so requests piled up indefinitely.
=======
if [ "$MASS_DEATH" -eq 1 ]; then
  echo "Skipping per-agent restart/kill actions during mass-death escalation"
else
# For crashed polecats — notify witness to handle restart
>>>>>>> upstream/main
for ENTRY in "${CRASHED[@]}"; do
  IFS='|' read -r SESSION RIG PCAT HOOK <<< "$ENTRY"

  echo "Requesting restart for $RIG/polecats/$PCAT (hook=$HOOK)"

  gt mail send "deacon/" \
    -s "RESTART_POLECAT: $RIG/$PCAT" \
    --stdin <<BODY
Polecat $PCAT crash confirmed by stuck-agent-dog plugin.
Context-aware inspection completed — agent is genuinely dead.

rig: $RIG
polecat: $PCAT
hook_bead: $HOOK
action: restart requested

The daemon will action this within one heartbeat.
BODY

done

# For zombie polecats — kill zombie session first, then request restart
for ENTRY in "${STUCK[@]}"; do
  IFS='|' read -r SESSION RIG PCAT HOOK REASON <<< "$ENTRY"

  echo "Killing zombie session $SESSION and requesting restart"
  tmux kill-session -t "$SESSION" 2>/dev/null || true

  gt mail send "deacon/" \
    -s "RESTART_POLECAT: $RIG/$PCAT (zombie cleared)" \
    --stdin <<BODY
Polecat $PCAT zombie session cleared by stuck-agent-dog plugin.
Session was alive but agent process was dead.

rig: $RIG
polecat: $PCAT
hook_bead: $HOOK
reason: $REASON
action: restart requested

Please restart this polecat session.
BODY

done
fi

# For deacon issues
if [ -n "$DEACON_ISSUE" ]; then
  echo "Escalating deacon issue: $DEACON_ISSUE"
  DEACON_SEVERITY="HIGH"
  DEACON_FINGERPRINT="stuck-agent-dog:deacon:$DEACON_ISSUE"
  case "$DEACON_ISSUE" in
    stuck_heartbeat_*)
      DEACON_SEVERITY="MEDIUM"
      DEACON_FINGERPRINT="stuck-agent-dog:deacon:stuck-heartbeat"
      ;;
  esac
  gt escalate "Deacon $DEACON_ISSUE detected by stuck-agent-dog" \
    -s "$DEACON_SEVERITY" \
    --source "plugin:stuck-agent-dog" \
    --fingerprint "$DEACON_FINGERPRINT"
fi
```

## Record Result

```bash
SUMMARY="Agent health check: ${#CRASHED[@]} crashed, ${#STUCK[@]} stuck, $HEALTHY healthy"
if [ -n "$DEACON_ISSUE" ]; then
  SUMMARY="$SUMMARY, deacon=$DEACON_ISSUE"
fi
echo "=== $SUMMARY ==="
```

On success (no issues or issues handled):
```bash
gt plugin record-run --plugin stuck-agent-dog --result success \
  --title "stuck-agent-dog: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true
```

On failure:
```bash
gt plugin record-run --plugin stuck-agent-dog --result failure \
  --title "stuck-agent-dog: FAILED" \
  --description "Agent health check failed: $ERROR" >/dev/null 2>&1 || true

gt escalate "Plugin FAILED: stuck-agent-dog" \
  --severity high \
  --reason "$ERROR"
```

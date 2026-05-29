# API & Interface Design

## Summary

Gas Town's heartbeat surface today is a single best-effort file write
(`internal/polecat/heartbeat.go`) that fires only when an agent runs a `gt`
command. Long-running LLM calls, builds, and tests therefore look like death
to the witness/dog/reaper layer. Adding a "busy-but-alive" signal is mostly
**an interface design problem**: the file format, the agent-side touch
contract, the operator-side CLI, and the consumer-side reader API must all
agree on how to distinguish "still working" from "dead."

The recommendation is a **layered v3 heartbeat with a `liveness` channel
distinct from the existing `state` channel**: liveness is a
process-supervision signal (alive/maybe-dead/dead), state is an agent
self-report (working/idle/exiting/stuck). The agent-side API gains a tiny
"keepalive" surface (`gt heartbeat keepalive` for shell, `polecat.Keepalive`
for Go) that long-running operations call from a background goroutine or
trap. The witness/dog read path becomes a single `polecat.Liveness(townRoot,
session)` call returning a typed verdict instead of three call sites
ad-hoc-comparing timestamps. Backward compatibility with v1/v2 heartbeat
files is preserved by treating missing fields as "default to current
behavior."

The design follows existing Gas Town conventions: ZFC thresholds in
`operational.polecat.*`, cobra subcommand under the existing `gt heartbeat`
parent, no new top-level commands, and the same "best-effort, errors
silently ignored" contract that `TouchSessionHeartbeat` uses today.

## Analysis

### Key Considerations

- **Two existing heartbeat shapes already coexist** —
  `internal/polecat/heartbeat.go` (v2 with `state` field) and
  `internal/deacon/heartbeat.go` (different schema with `cycle`, `last_action`,
  `healthy_agents`). v3 must extend the polecat shape without breaking
  `IsV2()`/`EffectiveState()` consumers and without merging the two
  schemas (deacon has its own protocol).
- **Three distinct read sites exist today** that each re-implement
  staleness logic:
    1. `internal/polecat/manager.go::isSessionProcessDead` — uses
       `IsSessionHeartbeatStale`.
    2. `plugins/stuck-agent-dog/run.sh::heartbeat_age_seconds` — shell
       jq-based reader, uses `STUCK_STALLED_THRESHOLD=600s`.
    3. `internal/daemon/reap_dead_agent_wisps.go` — uses bead
       `updated_at`, *not* heartbeat (witness/refinery don't write
       polecat heartbeats).
   These call sites must converge on a single typed verdict API or the
   schema/threshold drift will recur.
- **Witness and refinery don't have heartbeats** — they're bead-based
  liveness today (`updated_at`). The new API must either give them a
  heartbeat surface or explicitly carve them out, because gu-0nmw
  ("escalate when refinery heartbeat is stale > N hours") demands one
  for refinery. UX analysis (sibling leg) is likely to argue for the
  same scheme everywhere; this design accommodates both rigs.
- **Shell-vs-Go duality** — `stuck-agent-dog` is bash. Any new fields
  must be readable with `jq`, and any new agent-side API must have a
  CLI command shells can call (`gt heartbeat keepalive`). Pure Go-only
  APIs leak.
- **"Best-effort" is non-negotiable** — every existing heartbeat call
  swallows errors. The design must preserve this; an API that returns
  errors that *must* be checked breaks the contract.
- **Cardinality of writes** — current `TouchSessionHeartbeat` runs on
  every gt command via `persistentPreRun`. A keepalive that fires every
  30s during a 10-minute LLM call adds ~20 file writes; that's fine
  on disk but the receiver must not flap on the resulting timestamp
  jitter. The reader API must absorb keepalive cadence into the
  staleness calculation.
- **Naming consistency** — existing surface uses `state`, `Touch*`,
  `IsSessionHeartbeatStale`. New surface should use the same verb
  prefixes (`Touch*` for writes, `Read*` for reads, `Is*`/`Liveness*`
  for queries) and noun suffix ordering.
- **Discoverability** — gu-0nmw is filed against witness escalation;
  operators currently run `gt heartbeat --state=stuck` and read raw
  JSON files. The CLI needs `gt heartbeat status` (read) to be the
  obvious answer to "is this agent dead?"

### Options Explored

#### Option 1: Add `last_keepalive` field to existing v2 file

- **Description**: Add a single new optional field
  `last_keepalive: <RFC3339>` to `SessionHeartbeat`. Long-running
  operations periodically write only that field (preserving `state`,
  `context`, `bead`). The `IsSessionHeartbeatStale` check becomes
  `time.Since(max(timestamp, last_keepalive)) >= threshold`. Add a
  new `gt heartbeat keepalive` CLI command and `polecat.Keepalive(...)`
  Go API. v1/v2 readers ignore the new field; v3 readers prefer it
  when present.
- **Pros**:
    - Minimal schema delta; backward compatible by construction.
    - One source of truth (`max(timestamp, last_keepalive)`) collapses
      all three call sites onto one staleness check.
    - Agent semantics stay clean: `state` keeps reporting *what* the
      agent is doing; `last_keepalive` reports *that* the agent is
      alive.
    - Shell `jq` query is trivial: `(.last_keepalive // .timestamp)`.
    - Pairs naturally with a `polecat.Keepalive(townRoot, session)`
      Go helper that long-running call sites (LLM calls, build runners)
      can defer-launch in a goroutine.
- **Cons**:
    - Two timestamps in one file invites confusion ("which one matters?").
      Mitigated by always exposing the typed `Liveness()` verdict —
      callers shouldn't read raw timestamps anyway.
    - Forces everyone touching the file to remember to *not* clear
      `last_keepalive` when updating `state` (or vice versa). The
      Go API hides this with a read-modify-write helper.
- **Effort**: Low.

#### Option 2: Replace `state` with a richer `liveness` enum

- **Description**: Conflate everything into one field. Drop `state`;
  introduce `liveness: alive|busy|idle|exiting|stuck|maybe_dead`.
  The witness/dog read this directly; long operations set `busy`
  with a trailing `last_seen` timestamp.
- **Pros**:
    - Single field is conceptually simple.
    - One enum to teach, one place to look.
- **Cons**:
    - Breaks every existing v2 reader that switches on `state`.
    - Conflates "what the agent is doing" with "is the supervisor's
      timer expired" — these answer different questions and have
      different cadences.
    - `gt done` writes `state=exiting` to signal a clean handoff;
      that's not a liveness fact, it's a lifecycle fact.
    - Forces the witness to interpret agent self-reports as ground
      truth ("`stuck` means dead?"); current design rightly keeps
      these channels separate.
- **Effort**: High (touches every reader; hard rollout).

#### Option 3: Separate "busy file" alongside heartbeat.json

- **Description**: Long-running ops drop a sentinel
  `<session>.busy.json` next to `<session>.json`. Readers treat
  presence-of-busy-file as a stay-of-execution.
- **Pros**:
    - Heartbeat.json stays untouched.
    - Atomic ("no busy file" → fall back to existing logic).
- **Cons**:
    - Two files for one fact. Race conditions: agent dies between
      writing `busy.json` and the operation actually starting; busy
      file becomes a tombstone that lies forever.
    - Need an expiry mechanism in the busy file anyway (otherwise
      forever-busy on crash). At which point you've reinvented
      `last_keepalive` with extra steps.
    - Doubles the surface area for shell/Go readers.
- **Effort**: Medium.

#### Option 4: Process-tree probing (no heartbeat schema change)

- **Description**: Witness/dog walk the tmux pane's process tree and
  consider the session alive if *any* descendent has had recent CPU
  activity (`/proc/<pid>/stat` user+sys time delta).
- **Pros**:
    - No schema change; no agent-side API.
    - Truly knows whether the process is doing work.
- **Cons**:
    - Linux-specific (`/proc`), breaks on macOS dev hosts.
    - Permission-denied false positives — exactly the failure mode
      that `gt-kncti` already burned us on.
    - Doesn't help for "agent is alive but blocked on a hung
      network call" — CPU is zero, but agent is fine.
    - Doesn't help refinery (gu-0nmw): refinery doesn't run in tmux
      uniformly across environments.
    - Punts the "what was the agent doing" diagnostic the operator
      actually wants.
- **Effort**: Medium-High (cross-platform probing is a tar pit).

#### Option 5: Keepalive via heartbeat.json mtime only (no field)

- **Description**: Long-running ops `os.Chtimes()` heartbeat.json to
  bump mtime without rewriting contents. Readers use file mtime
  instead of the embedded `timestamp`.
- **Pros**:
    - Cheapest possible write.
    - Truly backward compatible.
- **Cons**:
    - Loses the diagnostic ("when was the last *real* state change?
      vs the last keepalive?"). With Option 1 the operator can see
      both; here they collapse.
    - File watchers/mtime-based tooling (rsync, backup, nfs) is
      famously inconsistent across filesystems.
    - Shell `jq` users are reading the JSON content; they'd have to
      switch to `stat -c %Y` for staleness — splits the read path.
- **Effort**: Low, but at the cost of debuggability.

### Recommendation

**Option 1 (add `last_keepalive` field)** plus a small typed-verdict
reader API. Concretely:

#### Schema (heartbeat v3)

```go
type SessionHeartbeat struct {
    Timestamp     time.Time      `json:"timestamp"`
    State         HeartbeatState `json:"state,omitempty"`
    Context       string         `json:"context,omitempty"`
    Bead          string         `json:"bead,omitempty"`
    LastKeepalive time.Time      `json:"last_keepalive,omitempty"` // v3 (gu-leg-ty7bm)
    KeepaliveOp   string         `json:"keepalive_op,omitempty"`   // optional: "llm-call", "brazil-build", etc.
}
```

`IsV3()` returns `!h.LastKeepalive.IsZero()`. v1/v2 heartbeats keep
working unchanged.

#### Agent-side write API (Go)

```go
// Keepalive bumps last_keepalive without disturbing state/context/bead.
// Best-effort: errors silently ignored, same contract as Touch*.
func Keepalive(townRoot, sessionName string) {
    KeepaliveWithOp(townRoot, sessionName, "")
}

// KeepaliveWithOp records what the agent is doing during the keepalive,
// for operator diagnostics ("agent is alive, in an LLM call").
func KeepaliveWithOp(townRoot, sessionName, op string) { ... }

// WithKeepalive runs fn with a background keepalive ticker. Cancels on return.
// Used by long-running operations: gt build, llm.Call, etc.
//
//   defer polecat.WithKeepalive(townRoot, session, "llm-call", 30*time.Second)()
func WithKeepalive(townRoot, sessionName, op string, interval time.Duration) (cancel func())
```

The third helper is the ergonomic answer to "every long call site needs
to remember to keepalive." A defer-one-liner trumps a manual goroutine
every time.

#### Agent-side write API (CLI)

Add subcommands under the existing `gt heartbeat` parent:

```
gt heartbeat                          # backward-compat: equivalent to gt heartbeat touch
gt heartbeat touch                    # write state=working
gt heartbeat keepalive [--op=NAME]    # bump last_keepalive only
gt heartbeat status [--session=NAME]  # operator-readable verdict
gt heartbeat watch                    # tail -f of state transitions across rig (diagnostic)
```

`gt heartbeat keepalive` is the bash entry point that long-running
shell scripts (brazil-build wrappers, gate runners) call from a
background loop or `trap`.

`gt heartbeat status` answers gu-0nmw's operator question directly.
Its output is a one-line summary plus optional `--json` for tooling:

```
$ gt heartbeat status --session=polecat-shiny-tmqt
session: polecat-shiny-tmqt
liveness: ALIVE        (heartbeat 12s ago, keepalive 8s ago, op=llm-call)
state:    working      (bead=gu-leg-ty7bm)

$ gt heartbeat status --session=refinery-gastown_upstream
session: refinery-gastown_upstream
liveness: MAYBE_DEAD   (heartbeat 18m ago, no keepalive, escalation in 12m)
state:    (none reported)
```

#### Consumer-side read API

Replace the three ad-hoc staleness checks with one typed verdict
function:

```go
type LivenessVerdict int
const (
    LivenessUnknown   LivenessVerdict = iota // no heartbeat file yet — same as today
    LivenessAlive                            // recent timestamp OR recent keepalive
    LivenessMaybeDead                        // stale but inside grace window
    LivenessDead                             // stale beyond hard threshold
)

type LivenessReport struct {
    Verdict          LivenessVerdict
    LastTimestamp    time.Time
    LastKeepalive    time.Time
    Age              time.Duration  // time since max(LastTimestamp, LastKeepalive)
    State            HeartbeatState
    KeepaliveOp      string
}

func Liveness(townRoot, sessionName string) LivenessReport
```

The dog/reaper/witness all call `Liveness()` and switch on the verdict.
No more raw `time.Since(hb.Timestamp)` comparisons sprinkled across
three languages.

For shell consumers (`stuck-agent-dog`), expose the same verdict via
`gt heartbeat status --json`:

```json
{
  "verdict": "ALIVE",
  "age_seconds": 8,
  "last_keepalive_age_seconds": 8,
  "state": "working",
  "keepalive_op": "llm-call"
}
```

#### Configuration interface

Two new ZFC thresholds, in `operational.polecat`:

```jsonc
{
  "operational": {
    "polecat": {
      "heartbeat_stale_threshold": "3m",         // existing — first dead-zone signal
      "heartbeat_keepalive_grace": "10m",        // NEW — verdict=MAYBE_DEAD until this expires
      "heartbeat_dead_threshold": "20m"          // NEW — verdict=DEAD; safe to reset/reap
    }
  }
}
```

Rationale for these specific defaults: 10m matches the existing
`STUCK_STALLED_THRESHOLD=600` in stuck-agent-dog (zero behavior change
for current operators), and 20m gives gu-0nmw's "<10min detection of
dead refinery" goal margin while still under any reasonable LLM call
ceiling. (LLM calls today timeout at ~10m client-side; a 20m hard-dead
floor is past that.)

#### Error-message and help-text contract

Every command preserves the existing best-effort silence on writes,
but `gt heartbeat status` is *querying*, not writing — it should
return useful errors:

```
$ gt heartbeat status --session=does-not-exist
no heartbeat file at .runtime/heartbeats/does-not-exist.json
hint: run `gt session list` to see active sessions

$ gt heartbeat status     # no GT_SESSION, no --session
GT_SESSION not set and --session not provided
hint: pass --session=NAME or run inside a Gas Town session
```

Error messages follow the existing `gt heartbeat --state=...` pattern:
brief diagnosis on the first line, actionable hint on the second.

## Constraints Identified

- **Best-effort write semantics are mandatory.** All write APIs
  (`Keepalive`, `WithKeepalive`, the CLI `keepalive` command) must
  silently swallow filesystem errors. This is a hard constraint
  inherited from `TouchSessionHeartbeat` and required to avoid
  bringing down `gt` commands when `.runtime/` is briefly read-only.
- **Backward compatibility with v1/v2 heartbeat files.** v3 readers
  must treat a missing `last_keepalive` as "fall back to `timestamp`"
  — never as "session is dead." This is the explicit design intent
  of Option 1.
- **Shell readability.** Any new heartbeat field must be reachable
  via a single `jq` expression — no nested encodings, no protobuf,
  no base64. `stuck-agent-dog` parses the file with jq and that
  pattern is correct.
- **No new top-level commands.** `gt` already has too many top-level
  commands; the new surface goes under `gt heartbeat`.
- **No required Go-error-checks at write sites.** Long-running
  operations cannot afford a `if err := polecat.Keepalive(...);
  err != nil { ... }` ceremony every 30s.
- **Cross-rig compatibility.** Witness/refinery should be able to
  use the same heartbeat schema if the data leg adopts it; but the
  API design must not *require* it. `Liveness()` returning
  `LivenessUnknown` for missing files is the escape valve.
- **No new heartbeat-on-every-syscall pressure.** The keepalive
  default cadence (~30s) is well below filesystem flush thresholds
  but still ≪ the 3m staleness threshold; this gives ~6 keepalives
  of grace before staleness, absorbing brief stalls.

## Open Questions

1. **Should `WithKeepalive` accept a `context.Context` and stop
   keepaliving when the context is cancelled?** Yes is more
   idiomatic Go, but most call sites are not context-plumbed today.
   Recommendation: take the context if present, fall back to a
   manual cancel func; both signatures are fine.
2. **Should `gt heartbeat keepalive` be safe to call without
   `GT_SESSION` (no-op) or should it error?** Existing `gt
   heartbeat` errors. But keepalive is meant to be drop-in safe in
   build wrappers; recommendation: warn-and-noop, don't error.
   *Cross-cutting with UX leg.*
3. **Witness/refinery integration**: should refinery be migrated
   to write a polecat-shaped heartbeat for gu-0nmw, or should the
   data leg keep refinery on bead `updated_at` and just reduce the
   threshold? *Defer to data leg + integration leg.*
4. **Op tagging granularity**: `keepalive_op="llm-call"` vs
   `"llm-call:claude:tool-use:Bash"`. The latter is great for
   diagnostics; the former is what build wrappers will realistically
   plumb. Recommendation: free-form string, no schema; document
   common values in the help text.
5. **Should `Liveness()` accept a `time.Time` "now" for testability,
   or just use `time.Now()`?** Existing
   `IsSessionHeartbeatStale` uses `time.Since`. Recommendation:
   accept `now` (functional option or method on a struct) — this
   is cheap to add and unblocks deterministic tests.
6. **Mtime-based fast-path**: should `Liveness()` check the file
   mtime first and skip JSON parsing if it's clearly fresh? This
   matters if the dog runs every 30s across 50 sessions.
   Recommendation: defer to perf leg; the API doesn't preclude it.

## Integration Points

- **Data Model leg**: agrees on the v3 schema (`last_keepalive`,
  `keepalive_op` field name and semantics) and writes the migration
  story for v1/v2/v3 file detection. This API design depends on
  the data leg confirming "additive, no v2 break."
- **UX leg**: agrees on the `gt heartbeat status` output format,
  error messages, and the `--json` shape. This API doc proposes a
  format; UX leg owns the final wording.
- **Integration leg**: confirms which long-running call sites adopt
  `WithKeepalive` (LLM clients, brazil-build wrapper, gate runner,
  bd dolt sync). Without adopters the API is ornamental; with them
  it solves the problem.
- **Security leg**: confirms that `keepalive_op` is not a sensitive
  string (could leak prompt fragments, build paths, etc.). The
  recommendation is to treat it as opaque and let writers decide;
  if security disagrees, we restrict to a fixed enum.
- **Scalability leg**: confirms that ~30s keepalive cadence × N
  active sessions × M reads-per-dog-cycle stays well under
  `.runtime/` filesystem throughput. The API doesn't prescribe a
  cadence — the config knob does — so scale leg can override.
- **gu-0nmw (witness escalation for stale refinery)**: this design
  provides the typed `Liveness()` verdict that gu-0nmw's
  escalation logic should consume; gu-0nmw becomes a single
  `if Liveness(townRoot, refinerySession).Verdict == LivenessDead
  { escalate() }` against this API.
- **stuck-agent-dog plugin**: migrates from inline jq+timestamp
  arithmetic to `gt heartbeat status --json | jq .verdict`. Keeps
  the plugin a thin policy layer; lifts liveness logic into Go
  where it can be tested.

# Design: False-Stale Heartbeats — Distinguishing Truly-Dead from Busy-but-Alive

> Synthesis of six dimension analyses (api, data, integration, scale, security, ux)
> from `.designs/cv-p3fem/`. Drives gu-0nmw (witness auto-restart of dead
> refineries) and the gu-rh0g class of incidents.

## Executive Summary

Gas Town's supervision layer (witness, stuck-agent-dog, the daemon reaper) cannot
currently tell the difference between an agent that has crashed and an agent
that is simply busy in a long LLM call, build, test, or merge-queue wait. The
single `timestamp` field in `SessionHeartbeat` is being asked to encode three
distinct facts — *when did the file last change*, *is the process alive right
now*, *is the agent making progress* — and conflating them has produced both
**under-action** (the 2026-05-27 refinery silently died with no auto-recovery,
gu-rh0g) and **over-action** (the 2026-05-19 mass-kill of 13 healthy polecats
during long calls). Witness and refinery roles compound the problem because
they emit no heartbeats at all today — the daemon reaper falls back to
`bead.UpdatedAt` with a 2-hour timeout.

The synthesized design is a **layered v3 heartbeat scheme**: an additive,
backward-compatible JSON-schema extension (three optional fields:
`last_keepalive`, `keepalive_op`, `liveness`) combined with a small
agent-side `WithKeepalive` Go helper and a `gt heartbeat keepalive` shell
entry point. A single typed `Liveness()` reader API collapses the three
ad-hoc staleness checks (Go reaper, bash dog, daemon-bead-proxy) onto one
verdict — `ALIVE` / `MAYBE_DEAD` / `DEAD` — that humans, plugins, and
agents all share. Witness and refinery adopt the same heartbeat surface,
closing gu-0nmw. Mass-kill escalations are gated behind per-agent
independent corroboration so a single bad signal cannot fan out.

The work is phased: **Phase 1** (witness/refinery coverage, ~1 day) closes
the gu-rh0g gap with the smallest possible diff; **Phase 2** (background
keepalive ticker, ~1 day) eliminates the false-stale-during-LLM-call class;
**Phase 3** (schema v3 + unified verdict + plugin/witness consumer
migration, ~2 days) hardens the system against the next class of incidents.
Storage stays as JSON files under `<townRoot>/.runtime/heartbeats/` — Dolt
is explicitly rejected as the heartbeat substrate (commit amplification).

## Problem Statement

**The bug.** Long-running operations — LLM calls (5–10m), `brazil-build` /
`go test ./...` (minutes), refinery merge-queue waits (up to 30m),
sling-blocking bisects — produce zero heartbeat writes today, because the
only producer is `persistentPreRun → touchPolecatHeartbeat` in
`internal/cmd/root.go:227`, which fires only on foreground `gt` commands.
A perfectly healthy polecat eight minutes into an LLM call looks identical
to a polecat that crashed eight minutes ago at the start of one. The
witness, stuck-agent-dog, and daemon reaper each see the same stale
timestamp and each independently decide whether to escalate — with three
different thresholds (3m / 10m / 2h), three different code paths, and
three different failure modes.

**The two failure shapes.**

1. **False positives → mass kill.** stuck-agent-dog hits its
   `STUCK_STALLED_THRESHOLD=600s` on a fleet of polecats all simultaneously
   in long LLM calls. The plugin's `TOTAL_ISSUES >= 3` rule trips
   CRITICAL, the Mayor is paged, the supervision system reaps healthy
   agents — which respawn into the same load conditions and stall again
   (gs-549, 2026-05-19 incident).
2. **False negatives → silent corpses.** Witness and refinery write no
   heartbeats. The daemon reaper falls back to `bead.UpdatedAt` with
   `DefaultDeadAgentReapTimeout = 2h`. A refinery that dies leaves no
   detection surface for two hours. gu-0nmw demands < 10-minute
   detection of dead refineries; gu-rh0g is the incident.

**Why the schema is the right place to fix it.** The single-`timestamp`
file conflates *liveness* (the supervisor's question — is the process
running?) with *progress* (the operator's question — is meaningful work
happening?) and with *self-reported state* (the agent's own
classification of what it's doing). These are three independent channels
and they should have three independent fields.

**Constraints that shape the design.**

- Best-effort writes: every existing producer ignores errors
  (`_ = os.WriteFile(...)`). New producers must do the same.
- Backward compatibility: v1 (timestamp-only) and v2 (with `state`,
  added in gt-3vr5) heartbeats are mid-flight in production today. v3
  must extend, not break.
- Two-language readers: Go consumers
  (`internal/polecat/heartbeat.go::IsSessionHeartbeatStale`) and bash
  (`plugins/stuck-agent-dog/run.sh::heartbeat_age_seconds`,
  `heartbeat_state`) parse the same file. New fields must be `jq`-readable.
- Single-host single-UID deployment: no network adversary; the threat
  model is *confused-or-buggy same-UID agent* + *supervision system
  over-reacting to its own bad readings*.
- No new daemons: a privileged external watchdog is rejected on
  blast-radius and SPOF grounds (security + scale legs concur).
- No Dolt for heartbeats: each write would be a permanent commit,
  multiplying churn by 10×–100× against a known-fragile substrate.

## Proposed Design

### Overview

The design has four moving pieces, all backward-compatible by construction:

1. **Heartbeat v3 schema.** Three optional additive fields:
   `last_keepalive` (RFC3339 timestamp), `keepalive_op` (free-form op
   label, e.g. `"llm-call"`, `"brazil-build"`), `liveness` (write
   classification: `alive` | `keepalive` | `exiting`). v1 and v2
   readers ignore them; v3 readers prefer them.
2. **Agent-side keepalive surface.** A tiny Go API
   (`polecat.WithKeepalive(townRoot, session, op, interval) cancel`,
   `polecat.Keepalive(townRoot, session)`) and a matching shell
   entry point (`gt heartbeat keepalive [--op=NAME]`) so long-running
   call sites can `defer` a 30s ticker in one line.
3. **Unified `Liveness()` reader API.** A typed verdict
   (`ALIVE` / `MAYBE_DEAD` / `DEAD` / `UNKNOWN`) that all three current
   ad-hoc staleness checks (the Go polecat reaper, the bash dog, the
   daemon's bead-based reaper) call into. Plugins consume via
   `gt heartbeat status --json`.
4. **Witness and refinery coverage.** Both roles start writing
   heartbeats from their patrol loops. The daemon
   `reap_dead_agent_wisps.go` migrates from `bead.UpdatedAt` to the
   same `Liveness()` verdict. `dead_agent_reap_timeout` drops from 2h
   to per-role values (polecat 10m, witness 15m, refinery 30m).

### Key Components

| Component | Location | Phase | Owner dimension |
|---|---|---|---|
| `SessionHeartbeat` v3 fields | `internal/polecat/heartbeat.go` | 3 | data |
| `polecat.Keepalive(...)` | `internal/polecat/heartbeat.go` | 2 | api |
| `polecat.WithKeepalive(...)` | `internal/polecat/heartbeat.go` | 2 | api |
| `polecat.Liveness(...)` verdict | `internal/polecat/heartbeat.go` | 3 | api + data |
| `gt heartbeat keepalive` CLI | `internal/cmd/heartbeat.go` | 2 | api + ux |
| `gt heartbeat status [--json]` CLI | `internal/cmd/heartbeat.go` | 3 | ux |
| Witness/refinery `persistentPreRun` hook | `internal/cmd/root.go` | 1 | integration |
| Daemon reaper migration | `internal/daemon/reap_dead_agent_wisps.go` | 1 | integration |
| Stuck-agent-dog plugin migration | `plugins/stuck-agent-dog/run.sh` | 3 | integration + ux |
| Mass-kill corroboration gate | `plugins/stuck-agent-dog/run.sh` | 3 | security |
| `gt witness status` Liveness column | `internal/cmd/witness.go` | 3 | ux |
| `gt prime` banner liveness line | `internal/cmd/prime.go` | 3 | ux |

### Interface

**Agent-side write API (Go).**

```go
// Keepalive bumps last_keepalive without disturbing state/context/bead.
// Best-effort: errors silently ignored, same contract as TouchSessionHeartbeat.
func Keepalive(townRoot, sessionName string)

// KeepaliveWithOp records what the agent is doing, for operator diagnostics.
func KeepaliveWithOp(townRoot, sessionName, op string)

// WithKeepalive runs a background keepalive ticker. Returns a cancel func.
// The ergonomic answer for long-running call sites:
//
//   defer polecat.WithKeepalive(townRoot, session, "llm-call", 30*time.Second)()
func WithKeepalive(townRoot, sessionName, op string, interval time.Duration) (cancel func())

// KeepaliveLoop is the context-aware variant for code that already has its own loop.
func KeepaliveLoop(ctx context.Context, townRoot, sessionName, op string, interval time.Duration)
```

**Agent-side write API (shell).**

```
gt heartbeat                           # backward-compat: state=working (unchanged)
gt heartbeat --state=stuck "reason"    # self-report (unchanged)
gt heartbeat keepalive [--op=NAME]     # NEW: bump last_keepalive only
```

Strong opinion (UX leg): `gt heartbeat keepalive` without `GT_SESSION`
**warns and no-ops** rather than erroring. Errors in build wrappers fail
the build; the harm-from-silent-noop is far smaller than the
harm-from-broken-CI.

**Consumer-side read API.**

```go
type LivenessVerdict int
const (
    LivenessUnknown   LivenessVerdict = iota // no heartbeat file (fail-open default)
    LivenessAlive                            // recent timestamp OR keepalive
    LivenessMaybeDead                        // stale but inside grace window
    LivenessDead                             // stale beyond hard threshold
)

type LivenessReport struct {
    Verdict        LivenessVerdict
    VerdictReason  string         // stable enum: keepalive_fresh, heartbeat_fresh,
                                   //              inside_grace_window, past_dead_threshold,
                                   //              no_heartbeat_file
    LastTimestamp  time.Time
    LastKeepalive  time.Time
    Age            time.Duration  // time since max(LastTimestamp, LastKeepalive)
    State          HeartbeatState // agent self-report (working/idle/exiting/stuck)
    KeepaliveOp    string
    Bead           string
}

func Liveness(townRoot, sessionName string) LivenessReport
```

**Operator surface (humans).**

```
$ gt heartbeat status
session: polecat-shiny-tmqt
liveness: ALIVE             (heartbeat 12s ago, keepalive 8s ago)
state:    working            (op=llm-call, bead=gu-leg-xtwu2)

$ gt heartbeat status --session=refinery-gastown_upstream
session: refinery-gastown_upstream
liveness: MAYBE_DEAD        (heartbeat 18m ago, no keepalive)
state:    (none reported)
hint:     auto-restart in 12m unless heartbeat refreshes
          run `gt witness restart gastown_upstream` to act now
```

**Plugin surface (`--json`).** Stable contract — plugin authors lock in
this shape:

```json
{
  "session": "polecat-shiny-tmqt",
  "verdict": "ALIVE",
  "verdict_reason": "keepalive_fresh",
  "age_seconds": 12,
  "last_keepalive_age_seconds": 8,
  "state": "working",
  "keepalive_op": "llm-call",
  "bead": "gu-leg-xtwu2",
  "thresholds": {
    "stale_seconds": 180,
    "grace_seconds": 600,
    "dead_seconds": 1200
  }
}
```

`thresholds` are embedded in the response so plugins don't read ZFC config
separately, which prevents threshold drift between the binary and the dog.

**`gt witness status` integration.** Liveness is the new leftmost data
column (the supervisor question operators scan for first):

```
Polecats:
  SESSION                    LIVENESS     STATE        BEAD              AGE
  polecat-shiny-tmqt         ALIVE        working      gu-leg-xtwu2      12s
  polecat-mighty-zlmn        MAYBE_DEAD   working      gu-leg-axyz       18m
  polecat-curly-bplq         ALIVE        idle         (none)             4s

Refinery: ALIVE   (heartbeat 4s ago)
Deacon:   ALIVE   (heartbeat 22s ago)
```

### Data Model

**v3 schema (additive over v2).**

```go
type SessionHeartbeat struct {
    // v1 (always present)
    Timestamp time.Time `json:"timestamp"`
    // v2 (gt-3vr5)
    State   HeartbeatState `json:"state,omitempty"`
    Context string         `json:"context,omitempty"`
    Bead    string         `json:"bead,omitempty"`
    // v3 (cv-p3fem — this design)
    LastKeepalive time.Time      `json:"last_keepalive,omitempty"`
    KeepaliveOp   string         `json:"keepalive_op,omitempty"`
    Liveness      LivenessSignal `json:"liveness,omitempty"`
}
```

Field semantics:

- `timestamp` — UTC time of the *last write of any kind*. Preserved
  from v1; never rewritten to indicate keepalive only.
- `state` — agent self-reported state (`working` | `idle` | `exiting`
  | `stuck`). Updated on transitions only.
- `last_keepalive` — UTC time the agent most recently asserted "I'm
  alive." Set by `Keepalive`/`WithKeepalive` and bumped on every
  `Touch*` write. Reader rule: **effective freshness =
  `max(timestamp, last_keepalive)`**.
- `keepalive_op` — opaque free-form label for the running operation
  (`"llm-call"`, `"brazil-build"`, `"go-test"`, `"merge-wait"`,
  `"dolt-query"`). Documented common values, not enforced enum.
- `liveness` — write classification. `keepalive` (the write was solely
  a freshness ping; state/context/bead unchanged); `alive` (a normal
  state-bearing touch); `exiting` (the final write before `gt done`
  exits — useful as a tombstone marker for orphan-prune).

**Version inference.** `IsV3()` returns
`!h.LastKeepalive.IsZero()`. Follows the existing v2 pattern
(`IsV2()` checks `state != ""`). No explicit `"v"` field —
debugging-only convention not worth the per-write overhead.

**Storage.** JSON files at
`<townRoot>/.runtime/heartbeats/<session>.json`. ~64–256B per file ×
~150 sessions today = ~24–38KB town-wide; even at 100× scale
(~13K files, ~50MB) trivial. **No SQLite. No Dolt.** The data plane
is a feature, not a bug — embarrassingly parallel single-writer
single-file writes are exactly the right shape.

**Lifecycle.** Written on every gt command (existing
`persistentPreRun`); written by `WithKeepalive` ticker during long
calls; removed by `RemoveSessionHeartbeat` on clean shutdown. The
`liveness="exiting"` signal is a tombstone marker that an
orphan-prune janitor can use to safely delete files even when
`RemoveSessionHeartbeat` didn't run. (Orphan prune itself is
out-of-scope for v3 but the schema accommodates it.)

**Backward compat (verbatim).**

| Reader version | v1 file | v2 file | v3 file |
|---|---|---|---|
| v1 (legacy `IsSessionHeartbeatStale`) | works | works (ignores `state`) | works (ignores all v3 fields) |
| v2 (`EffectiveState()`, `IsV2()`) | works (defaults `state=working`) | works | works (ignores v3 fields) |
| v3 (`Liveness()`) | works (treats as v2) | works (treats as v2) | works (uses keepalive) |

## Trade-offs and Decisions

### Decisions Made

1. **Additive v3 schema (data leg Option D, api leg Option 1).** Three
   new optional fields rather than a breaking rewrite. Old readers see
   a v2-compatible file; new readers get the keepalive channel. The
   minimum-delta alternative (one `last_keepalive` field, data leg
   Option B) is rejected because we're paying schema-bump tax either
   way; getting `keepalive_op` and `liveness` for the same migration
   is a clear win.

2. **Three-state verdict: ALIVE / MAYBE_DEAD / DEAD (ux leg, hard
   ceiling).** Operators conflate states the moment there are more
   than three; gu-rh0g is exactly this failure mode. If a fourth
   distinction matters (e.g. "alive but stuck"), it goes in the
   `state` field and an explanatory parenthetical ("ALIVE
   (self-reported stuck)"), not the verdict.

3. **JSON files, not Dolt or SQLite (scale leg, security leg, data
   leg concur).** ~30KB town-wide today; 50MB at 100×. Adding SQLite
   for 150 rows or Dolt commits for 580k writes/day is wrong-tool-
   for-job. The data plane is a feature.

4. **30s default keepalive cadence (api leg, scale leg, ux leg).**
   Well below the 3m staleness threshold (~6 keepalives of grace
   absorbing brief stalls), well above filesystem flush thresholds.
   Tunable via `operational.polecat.heartbeat_keepalive_interval`.

5. **Phase 1 ships first, alone (integration leg recommendation).**
   Witness/refinery coverage closes the gu-rh0g gap with the smallest
   possible diff — one-line change in the `persistentPreRun` role
   allowlist, plus a Liveness check in
   `reap_dead_agent_wisps.go`. **Restores supervision of the role
   whose death triggered this design exercise** before any of the
   more elegant unified-policy work lands.

6. **`gt heartbeat keepalive` without `GT_SESSION` warns and no-ops
   (ux leg, strong opinion).** Errors fail builds; warnings get
   logged and ignored. Build wrappers should be drop-in safe.

7. **Per-role thresholds (integration leg, security leg).** Polecat
   10m, witness 15m, refinery 30m. Reaping a polecat resets one
   bead; reaping a refinery mid-merge corrupts a stack. Blast
   radii differ; thresholds should too.

8. **Mass-kill requires per-agent independent corroboration
   (security leg, scale leg).** stuck-agent-dog's current
   `TOTAL_ISSUES >= 3` rule fires CRITICAL on three independently-
   stale files — meaning a transient FS issue, three corrupt JSONs,
   or three same-UID bad writes can wake the Mayor. The new rule:
   each of the 3+ agents must independently fail *both* the
   heartbeat-staleness check *and* the PID-liveness check before
   counting toward the total. The 2026-05-19 mass-kill cannot recur.

9. **Operator override (recovery marker, gu-v5mk) wins (security
   leg, non-negotiable).** Any new automated-action gate consults
   `gt polecat is-recovered` first; manual recovery short-circuits
   to `Healthy` regardless of other signals.

10. **`keepalive_op` is opaque free-form (api + ux + security legs).**
    No enforced enum. Documented common values. Treated as
    untrusted input at any LLM-prompt boundary (escape on display,
    don't pre-sanitize on write).

### Open Questions (Need Human Input)

1. **Should `expected_idle_until` (TTL-bounded self-report)
   ship in v3, or be deferred?** Security leg recommends Option 5
   (TTL-bounded self-reports) as a defense against
   `state=exiting` permanently suppressing detection if an agent
   crashes mid-`gt done`. Integration leg's Phase 3 includes it
   (`gt heartbeat --until=+15m`). The cost is one more optional
   field and a clock-skew-on-one-host concern. The benefit is
   capping any self-reported state's suppression window to a
   hard ceiling (e.g. 15m). **Recommendation: ship in v3.** It
   addresses a known security gap (the `state=exiting` corpse
   class), the field is tiny, and adding it later means a v4
   migration. *Decision needed: include or defer?*

2. **Per-role thresholds — exact values.** Integration leg
   suggests polecat 10m / witness 15m / refinery 30m. The
   30m refinery value is a function of legitimate
   merge-queue-bisect duration; if the refinery's own gates can
   take 25m on a slow CI run, 30m is barely enough margin.
   gu-0nmw asks for "<10min detection of dead refinery" — but a
   30m hard-dead floor doesn't conflict with that, because
   `MAYBE_DEAD` (the operator-actionable verdict) fires at the
   grace boundary (10m). **Recommendation: polecat
   stale=3m/grace=10m/dead=20m, witness stale=5m/grace=15m/
   dead=30m, refinery stale=10m/grace=30m/dead=60m.** *Decision
   needed: confirm or adjust per role.*

3. **PID-liveness vs heartbeat-liveness ordering.** Scale leg
   recommends PID-first ("process death is unambiguous and free,
   one syscall"). Security leg recommends multi-signal AND-
   corroboration (heartbeat AND tmux session AND recent Dolt
   commit must all agree before declaring DEAD). They aren't
   incompatible — PID-first short-circuits an obvious case;
   multi-signal corroboration is required when the heartbeat is
   stale-but-PID-alive (the "wedged" class). **Recommendation:
   `Liveness()` consults PID-existence as a fast-path; if the
   process is gone, return DEAD immediately. If process is alive
   but heartbeat is stale, fall through to the 3-signal
   corroboration (heartbeat age, tmux session, bead-update
   recency).** *Decision needed: codify ordering in the
   verdict-computation logic.*

4. **stuck-agent-dog scope on witness/refinery.** The dog
   currently *excludes* witness and refinery (scope is
   polecat+deacon). Phase 1 adds heartbeats to those roles, but
   the dog stays out of their reaping path — `reap_dead_agent_
   wisps.go` is the canonical consumer. Question: should the
   dog *report* (no escalation) on witness/refinery liveness so
   operators see all roles in one tool, or stay strictly
   polecat+deacon? **Recommendation: report-only mode for
   witness/refinery (visibility without authority).** *Decision
   needed: dog scope change in Phase 3.*

5. **HMAC/nonce on heartbeat content.** Security leg considers
   per-session HMAC and rejects in single-UID model (anything
   that can write the heartbeat can read the sibling secret).
   Open question Q1 in security leg asks whether a session-start
   nonce is worth it for replay detection. **Recommendation:
   defer.** YAGNI in single-UID; add when/if Gas Town moves to
   per-agent UIDs. *Decision needed: confirm defer.*

### Trade-offs

- **Schema cost vs diagnostic value.** Three new fields rather than
  one (data leg Option B vs Option D). The minimum-delta fix is one
  field; we chose three because we're paying the migration cost
  either way and `keepalive_op` plus `liveness` are nearly free
  given the schema-bump tax.

- **Read-modify-write race on `Keepalive`.** A `state` transition
  firing concurrently with a keepalive could lose one field's
  update. v2 already has this race; the worst case under v3 is
  identical (`omitempty` plus best-effort writes degrade
  gracefully). We chose this over file-locking because heartbeat
  writes must remain syscall-cheap (best-effort contract).

- **Cross-language coupling cost.** stuck-agent-dog must learn the
  v3 staleness rule (`max(timestamp, last_keepalive)`) and the
  verdict mapping. Bash diff is small but must roll out before
  v3 writers go fleet-wide; otherwise the dog will fight the
  witness. Mitigated by exposing `gt heartbeat status --json` as
  the single source of truth — the dog migrates from inline
  jq+date arithmetic to `gt heartbeat status --json | jq
  .verdict`. Plugin's role narrows from "compute liveness" to
  "act on liveness."

- **Phasing cost vs incident risk.** Phase 1 (witness/refinery
  coverage) is the smallest possible diff that closes gu-rh0g
  but doesn't fix the false-stale-during-LLM-call class. Phase 2
  (background ticker) fixes that class but is wasted without
  Phase 1's coverage extension. Phase 3 (verdict API + plugin
  migration) is pure incremental hardening. We chose this
  ordering because **restoring refinery supervision is the
  single highest-impact change**; the LLM-false-positive class
  is painful but not corruption-causing.

- **`expected_idle_until` is gameable.** A wedged agent could
  declare `expected_idle_until=+24h` and never be reaped.
  Mitigation: a per-rig cap (`max declared idle =
  dead_agent_reap_timeout`) bounds the worst case. Security
  leg's Option 5 calls this out explicitly. Open question 1
  above asks whether to ship this in v3 at all.

- **Missing-heartbeat-file → fail-open today.**
  `IsSessionHeartbeatStale` returns `(stale=false, exists=false)`
  for missing files; this avoids reaping pre-rollout sessions.
  Security leg correctly notes that as a long-term posture this
  is denial-of-supervision (delete the file → invisible
  forever). v3 retains fail-open *during rollout* (the
  Verdict=Unknown path), then post-rollout flips to require
  orthogonal corroboration. Phasing handles this: during
  Phase 2 rollout, missing files default to "trust the existing
  v1/v2 code path." Once witness+refinery are in steady state
  on v3, Phase 3 escalates Unknown → MAYBE_DEAD.

## Risks and Mitigations

### Security risks

| Risk | Mitigation | Owner |
|---|---|---|
| **Mass-kill cascade** from a single bad signal class (transient FS, corrupt JSON, mid-rollout schema mismatch). The 2026-05-19 incident. | Per-agent independent corroboration: each agent in a CRITICAL count must independently fail heartbeat *and* PID checks. Single-class signal cannot fan out. | Phase 3, dog plugin |
| **Agent self-reports `state=exiting` once and dies** → permanently invisible to supervision (slot exhaustion). | TTL-bounded self-reports (open question 1): hard cap on suppression window from any state field. Agent must keep affirming. | Phase 3 (if approved) |
| **`session_name` path traversal** (`../`) — `filepath.Join` does not strip parent segments. Today implicit through controlled tmux session names; new producer set widens callers. | Validator at heartbeat-file boundary: regex `^[A-Za-z0-9_.-]+$`, reject `..` substrings before any path join. | Phase 1, every producer |
| **Heartbeat content as prompt-injection vector.** `Context`, `Bead`, `KeepaliveOp` flow into LLM prompts (status reports, escalation summaries, Mayor views). Same-UID writer can plant strings. | Quote/escape at every LLM-prompt boundary. **Don't** pre-sanitize on write (loses signal for forensics). Treat fields as adversarial input where consumed. | Phase 3, all consumers |
| **Cross-agent forgery** within same UID. Any process can rewrite any heartbeat. | Out of scope for v3 (single-UID deployment model). HMAC option deferred. Documented as a non-goal. | (deferred) |
| **`liveness="exiting"` spoofed** → operator override gone wrong. | Operator override (recovery marker, gu-v5mk) wins over any state field. Recovery marker short-circuits `Liveness()` to `Healthy`. | Phase 3 |

### Reliability risks

| Risk | Mitigation | Owner |
|---|---|---|
| **Goroutine leak on panic** — `WithKeepalive` ticker outlives the call. | Returned cancel func is `defer`-friendly. Internal implementation uses a `select` on `ctx.Done()` and the cancel signal so the ticker exits cleanly. Document the defer pattern as the ergonomic default. | Phase 2 |
| **Schema drift between Go and bash readers.** Dog parses with jq; heartbeat.go parses with encoding/json. v3 fields used for liveness must roll out in bash *before* writers go fleet-wide. | Migrate dog to `gt heartbeat status --json` consumer (single source of truth) rather than re-implementing parsing. Plugin becomes thin policy layer. Until migration ships, dog remains v2-compatible. | Phase 3 |
| **Mid-rollout false reaps.** Old reader sees v3 file with stale `timestamp` but doesn't know about `last_keepalive` → false stale. | Phase ordering: Phase 1 deploys *readers* (witness/refinery already use v2-compatible readers). Phase 2 deploys *writers* (background ticker bumps `timestamp` AND `last_keepalive` together, so old readers see fresh `timestamp`). Phase 3 migrates dog. At every phase boundary, both old and new readers see fresh signals. | All phases |
| **Best-effort write failures** (disk full, permission denied) silently swallow. Repeated failures → all heartbeats stale → mass-kill cascade. | Mass-kill corroboration gate (security mitigation 1) requires PID-aliveness too; FS hiccup affecting all heartbeats simultaneously won't trip CRITICAL. | Phase 3 |
| **Witness/refinery patrol loops are themselves long-running.** Same false-stale problem transferred to the supervisors. | Witness/refinery use `WithKeepalive` from their patrol-loop entry point (Phase 2 covers them too). Dogfooding the keepalive helper is the test. | Phase 2 |

### Operational risks

| Risk | Mitigation | Owner |
|---|---|---|
| **Operator confusion** between "stuck (self-reported)" and "wedged (no keepalive)". They've been conflated in incident threads. | UX leg's verdict words: ALIVE / MAYBE_DEAD / DEAD. State field carries the self-report. Display layer always parenthesizes ("ALIVE (self-reported stuck)"). Hard ceiling: three verdict words. | Phase 3 |
| **Plugin authors lock in JSON shape** — renaming a field is a P0 break. | `verdict`, `verdict_reason`, `age_seconds`, `last_keepalive_age_seconds`, `state`, `keepalive_op`, `bead`, `thresholds` are the v3 contract. Future fields are additive only. | Phase 3 |
| **gt prime banner overflow.** Adding multiple liveness lines per neighbor blows the prime info budget (already ~40KB). | Exactly one line: `liveness: ALIVE (heartbeat 1s ago, keepalive 1s ago)`. Self only, not neighbors. | Phase 3 |
| **Threshold drift between binary and plugin.** Dog reads `STUCK_STALLED_THRESHOLD=600` from env; binary reads ZFC. | `--json` response embeds `thresholds` field; plugin uses it instead of env. Single source of truth. | Phase 3 |

## Implementation Plan

### Phase 1: Witness/Refinery Coverage (~1 day)

**Goal: close gu-rh0g — restore supervision of refineries with the
smallest possible diff. No schema changes; no new code paths.**

1. **Extend `persistentPreRun` role allowlist.** In
   `internal/cmd/root.go::touchPolecatHeartbeat`, the function is
   misnamed (it already handles polecat/crew/dog/deacon). Add
   `witness` and `refinery` to the allowlist. Rename to
   `touchAgentHeartbeat` for clarity. **One-line change** in the
   allowlist test, ~10 LOC for the rename.

2. **Add `session_name` validation at the heartbeat-file boundary.**
   `heartbeatFile()` regex-validates `^[A-Za-z0-9_.-]+$` and
   rejects `..` substrings. Reject (no-op write) on validation
   failure. ~15 LOC.

3. **Migrate daemon reaper to heartbeat-first.**
   `internal/daemon/reap_dead_agent_wisps.go` currently keys on
   `bead.UpdatedAt` because no heartbeat existed for these roles.
   After step 1, prefer
   `polecat.IsSessionHeartbeatStale(townRoot, sessionName)`; fall
   back to `UpdatedAt` for sessions still on the pre-rollout path.
   ~30 LOC.

4. **Drop `DefaultDeadAgentReapTimeout` from 2h to per-role values
   (gated on heartbeat presence).**
   - `WitnessReapTimeout = 15 * time.Minute`
   - `RefineryReapTimeout = 30 * time.Minute`
   - `dead_agent_reap_timeout` (legacy, polecat) stays at current
     value.
   ~20 LOC + ZFC config plumbing.

5. **Tests.** Unit test for the role-allowlist extension; unit
   test for path-traversal rejection; daemon-reaper integration
   test with witness/refinery heartbeat files.

**Exit criteria:** A refinery that dies is detected within 30
minutes by the daemon reaper. The escalation hits gu-0nmw's
`<10min detection` requirement once Phase 2 lands (background
ticker will refresh the heartbeat during legitimate long
operations, eliminating false-positives that would otherwise
force the threshold higher).

### Phase 2: Background Keepalive Ticker (~1 day)

**Goal: eliminate the false-stale-during-LLM-call class. No schema
changes yet — the keepalive ticker bumps the v2 `timestamp` field
during long calls. (Phase 3 introduces `last_keepalive` as a
separate field for diagnostic clarity, but Phase 2 already fixes
the bug.)**

1. **Add `polecat.WithKeepalive` and `polecat.KeepaliveLoop` Go
   helpers.** `WithKeepalive` returns a cancel func; the goroutine
   touches the heartbeat every 30s. Defer-friendly. ~50 LOC in
   `internal/polecat/heartbeat.go`.

2. **Add `gt heartbeat keepalive [--op=NAME]` shell command.**
   Calls into `polecat.Keepalive`. Warns-and-no-ops on missing
   `GT_SESSION`. ~25 LOC in `internal/cmd/heartbeat.go`.

3. **Inventory long-running call sites and adopt the helper:**
   - LLM client wrapper (claude harness — coordinate with
     integration leg's adopter list)
   - `internal/cmd/build.go` and any subprocess-invoking gt commands
   - Refinery merge-queue wait loop (`internal/refinery/...`)
   - Witness patrol loop (`internal/witness/handlers.go`)
   - Polecat boot wrapper (`internal/cmd/polecat_kiro_wrapper.go`)
     — also covers the LLM-call case for sessions that don't have
     a Go-instrumented LLM client.
   ~10 LOC per adopter, ~5 adopters = ~50 LOC.

4. **Build wrapper / gate runner shell adopters.** Add the
   keepalive trap pattern to brazil-build wrappers and the
   `scripts/check-upstream-rebased.sh` gate. ~5 LOC each.

5. **Tests.** End-to-end test: start a polecat session, simulate a
   10-minute LLM call (no foreground gt commands), verify
   stuck-agent-dog does not flag.

**Exit criteria:** stuck-agent-dog reports zero false positives
for sessions in legitimate long calls. Operator can run
`gt heartbeat status` and see a fresh timestamp during a long
LLM call. The 2026-05-19 mass-kill class is no longer
reproducible.

### Phase 3: Schema v3 + Unified Verdict + Consumer Migration (~2 days)

**Goal: harden the system against the next class of incidents.
Adds the verdict API, migrates consumers to it, lands the
diagnostic schema (`last_keepalive`, `keepalive_op`, `liveness`),
and tightens mass-kill gating.**

1. **Land v3 schema.** Add `LastKeepalive`, `KeepaliveOp`,
   `Liveness` fields with `omitempty`. Add `IsV3()`,
   `EffectiveLastKeepalive()` helpers. v1/v2 readers see no
   change. ~30 LOC.

2. **Land typed `Liveness()` reader API.** Computes verdict from
   the v3 file. Embeds thresholds in the report. Consults
   PID-existence as fast-path; multi-signal corroboration on
   stale-but-PID-alive. ~80 LOC.

3. **Add `gt heartbeat status [--session] [--json]` command.**
   Replaces the inline jq parsing. Plugin contract. ~60 LOC.

4. **Migrate consumers to `Liveness()` verdict:**
   - `internal/polecat/manager.go::isSessionProcessDead` —
     replace `IsSessionHeartbeatStale` with `Liveness().Verdict`.
   - `internal/daemon/reap_dead_agent_wisps.go` — replace
     v2-staleness check with `Liveness()`; act only on
     `Verdict == Dead`.
   - `plugins/stuck-agent-dog/run.sh` — replace inline jq+date
     arithmetic with `gt heartbeat status --json | jq .verdict`.
     **Mass-kill gate**: each agent in `TOTAL_DEAD` count must
     have `verdict=DEAD` AND PID-dead, both checked
     independently. ~40 LOC bash diff.

5. **`gt witness status` Liveness column.** Calls `Liveness()`
   per session; renders the table. ~30 LOC.

6. **`gt prime` banner liveness line.** One line:
   `liveness: ALIVE (heartbeat 1s ago, keepalive 1s ago)`. ~10
   LOC.

7. **Update agent docs.** polecat CLAUDE.md, deacon docs,
   refinery docs: replace "check
   `~/.runtime/heartbeats/<session>.json` with jq" guidance
   with "run `gt heartbeat status`."

8. **(Conditional, open question 1) `expected_idle_until` field
   + `gt heartbeat --until=...` flag.** Ships in Phase 3 if
   approved. ~30 LOC.

9. **Tests.**
   - Cross-language test: round-trip a Go-written v3 heartbeat
     through `gt heartbeat status --json` and verify the dog's
     decision logic produces the same answer it would have for
     v2.
   - Mass-kill corroboration test: simulate 5 agents with stale
     heartbeats but live PIDs; verify dog reports MAYBE_DEAD,
     not CRITICAL.
   - Operator-override test: recovery marker → `Liveness()`
     returns `Healthy` regardless of other signals.

**Exit criteria:** Single typed `Liveness()` API across Go and
bash. Mass-kill cannot be triggered by a single bad signal class.
Operators see liveness verdicts in three surfaces (`gt heartbeat
status`, `gt witness status`, `gt prime` banner). Plugin authors
have a stable JSON contract. All five integration legs are on
the same wording.

### Future / Phase 4 (Deferred, file as follow-up beads)

- **Heartbeat orphan-prune janitor.** The
  `liveness="exiting"` tombstone marker enables safe deletion of
  abandoned heartbeat files. Today the dir accumulates
  (af-forge.json from 2026-04-29 is a month old). File as
  `gu-leg-orphan-prune`.
- **`gt heartbeat watch` tail-f diagnostic.** API leg proposed;
  UX leg defers to follow-up. File as `gu-leg-watch`.
- **`bd list --stale-heartbeat` queryable signal.** Integration
  leg open question. File as `gu-leg-bd-stale`.
- **HMAC-signed heartbeats.** When/if Gas Town moves to per-agent
  UIDs. File as `gu-leg-hmac` with explicit dependency on the
  per-agent-UID architecture change.
- **Cross-platform PID-active check.** Scale leg's `/proc/<pid>/stat`
  CPU-delta liveness proof is Linux-only. Currently blocked on
  macOS dev-host support. File as `gu-leg-cpu-liveness`.

## Appendix: Dimension Analyses

Full dimension documents in `.designs/cv-p3fem/`:

- [API & Interface Design](api.md) — read/write verbs, CLI shape,
  Go signatures, error messages.
- [Data Model Design](data.md) — v3 schema, field semantics,
  storage rationale, backward-compat matrix.
- [Integration Analysis](integration.md) — touch points,
  producer/consumer inventory, phased rollout, cross-language
  coupling.
- [Scalability Analysis](scale.md) — write/read rates, projections
  to 1000×, jq-shellout cost analysis, Dolt-as-substrate
  rejection rationale.
- [Security Analysis](security.md) — threat model (single-UID),
  multi-signal corroboration, mass-kill blast-radius gating, TTL
  defense for self-reports.
- [User Experience Analysis](ux.md) — three-state verdict, human
  vs plugin output parity, error-as-docs hints, `gt prime` and
  `gt witness status` integration.

## Sources

- [API & Interface Design](api.md) — accessed 2026-05-29
- [Data Model Design](data.md) — accessed 2026-05-29
- [Integration Analysis](integration.md) — accessed 2026-05-29
- [Scalability Analysis](scale.md) — accessed 2026-05-29
- [Security Analysis](security.md) — accessed 2026-05-29
- [User Experience Analysis](ux.md) — accessed 2026-05-29
- Bead `gu-syn-bqk4o` (synthesis assignment) — accessed 2026-05-29
- Reference beads: gu-0nmw (witness escalation for stale
  refinery), gu-rh0g (refinery death incident), gu-3vr5 (heartbeat
  v2 design), gu-v5mk (recovery marker), gs-549 (mass-kill
  cascade), gu-ybjb (slot exhaustion / DEFERRED route).

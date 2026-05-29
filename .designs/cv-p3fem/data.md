# Data Model Design

## Summary

The "false-stale heartbeat" problem is at heart a **data-model gap**: a single
`timestamp` field is being asked to encode two distinct facts ("when did the
agent last write?" and "is the agent still alive right now?"), and the witness/
dog/reaper layer has no way to distinguish them. Today's
`internal/polecat/heartbeat.go::SessionHeartbeat` carries `timestamp`, and a
v2 layer added `state` / `context` / `bead`. None of those answer
"is the agent currently busy on a long call?" without ambiguity — a `state =
"working"` heartbeat written 12 minutes ago looks identical, schema-wise, to
a process that crashed 12 minutes ago at the start of an LLM call.

The recommendation is **Option D — additive v3 schema with a separate
`liveness` channel**: keep the existing v1/v2 fields exactly as they are, add
three optional fields (`last_keepalive`, `liveness`, `tool_call`) and codify a
typed `LivenessVerdict` that consumers compute from the file rather than
re-deriving staleness ad hoc. Storage stays as JSON files under
`<townRoot>/.runtime/heartbeats/<session>.json` — the data plane is too small
to justify SQLite or Dolt (~64–256B/file × ~150 sessions = ~24–38KB town-wide;
even at 100x scale this fits comfortably). Migration is zero-cost: missing
fields default to current behavior, so v1/v2 readers and writers keep working
unchanged. The only new discipline is that long-running operations call a tiny
`Keepalive` API from a background goroutine to refresh `last_keepalive`
without rewriting `state`.

This dimension's job is to lock the schema, the lifecycle, and the
backwards-compatibility contract. The `api.md` leg owns the read/write
verbs; the `scale.md` leg owns the cadence/cost analysis; this leg owns
**what fields exist, what they mean, and how they evolve.**

## Analysis

### Key Considerations

- **Two distinct facts must be representable**. `timestamp` answers "when
  did the agent last touch this file" (process-supervision liveness).
  `state` answers "what does the agent think it's doing" (agent self-
  report). Conflating them is what produces false-positives: a v2 polecat
  writing `state="working"` at the *start* of an 8-minute LLM call is
  indistinguishable from a polecat that died 8 minutes ago. We need a
  third concept — a **separate freshness signal that the agent can refresh
  even while `state` doesn't change**.

- **Backward compatibility is a hard constraint, not a nice-to-have.**
  Three classes of readers exist today and they all parse the same file:
    1. Go code in `internal/polecat/heartbeat.go::IsSessionHeartbeatStale`
       (called from `internal/polecat/manager.go::isSessionProcessDead`).
    2. Bash + `jq` in `plugins/stuck-agent-dog/run.sh::heartbeat_age_seconds`.
    3. Go code in `internal/daemon/reap_dead_agent_wisps.go` (uses bead
       `updated_at`, NOT the heartbeat — so witness/refinery don't read
       this file at all today).
  Any schema change must preserve `timestamp` semantics for v1/v2 readers.
  Added fields must be optional with sensible defaults when absent.

- **Two physical schemas already coexist.** `internal/polecat/heartbeat.go`
  and `internal/deacon/heartbeat.go` are *different* file shapes — deacon
  writes `cycle`, `last_action`, `healthy_agents`, etc. Polecat writes
  `timestamp`, `state`, `context`, `bead`. Unifying them is out of scope
  and would be a major migration; v3 should extend the polecat schema only,
  and the witness/refinery story should adopt the polecat shape (not the
  deacon one) when they grow heartbeats per gu-0nmw.

- **The file is best-effort, not transactional.** Today's writers ignore
  errors (`_ = os.WriteFile(...)`). The schema design must not introduce
  fields that *require* successful write — e.g. don't make a counter that
  has to monotonically increase or a sequence number that must be unique.
  All new fields must tolerate write loss.

- **Cardinality is small and bounded.** ~150 files town-wide today,
  ~64–256 bytes each. Even at projected 100x scale (~13K files, ~50MB
  on disk, see `scale.md`) the data plane stays trivial. **Do not
  migrate to SQLite, Dolt, or any other store.** The problem is not
  "JSON files don't scale" — the problem is "the JSON schema is
  missing a field."

- **Lifecycle today: write on gt-touch, remove on session cleanup,
  orphan-prune by Witness.** Heartbeats are written on every gt command
  via `internal/cmd/root.go::persistentPreRun` and removed by
  `RemoveSessionHeartbeat`. The directory accumulates orphans (130 files
  in production, many from sessions that died months ago — see
  `af-forge.json` from 2026-04-29). The schema doesn't need to fix this,
  but a well-designed schema makes pruning *easier* by adding a
  termination signal (e.g. `liveness="exiting"` on the `gt done` write).

- **Three classes of agents need liveness**: polecats (have heartbeats
  today), witness/refinery (don't — bead-based via `updated_at`),
  deacon (own schema). gu-0nmw demands witness/refinery get a heartbeat.
  The schema should be **rig-agnostic** — same fields regardless of role.

- **Tool-call observability is the secondary value.** If we're adding a
  freshness channel, including the *current tool/call* (e.g. `"llm_call"`,
  `"go_build"`) costs almost nothing and gives the dog far better
  diagnostics than "agent looks stuck for 12 minutes." This is the only
  net-new field beyond freshness; everything else is plumbing.

### Options Explored

#### Option A: Extend `timestamp` semantics — agents update on every keepalive

- **Description**: No schema change. Long-running operations periodically
  call `TouchSessionHeartbeat` from a background goroutine, refreshing
  `timestamp` every N seconds even during LLM/build calls. Tooling stays
  unchanged. The "fix" is purely in the writer call sites.
- **Pros**:
    - Zero schema migration. v1/v2/v3 readers are all the same code.
    - No new fields, no new bash parsing, no new Go types.
    - Trivially correct on day one.
- **Cons**:
    - **Loses the distinction between "agent did something" and "agent is
      pinging from a goroutine to prove it's alive."** Investigators
      reading the file can no longer tell whether the timestamp reflects
      real progress or just a keepalive. This makes incident analysis
      harder (e.g., "this polecat's timestamp was fresh 30s ago — was it
      actually doing anything?").
    - Conflates user-visible state transitions with liveness pinging:
      every keepalive forces a write that includes the *current*
      `state`/`context`/`bead`, which means either (a) keepalives don't
      update state and we have to read state from somewhere else, or
      (b) keepalives clobber state with stale values from when the
      goroutine started. Both are bad.
    - Closes the door on richer signals (tool-call name, structured
      verdict) that cost nothing now but become hard to add later
      under the "no schema change" rule.
- **Effort**: Low. Schema-wise the cheapest, but it leaves
  observability gaps that compound over time.

#### Option B: Add `last_keepalive` field only

- **Description**: Single new optional field
  `last_keepalive: <RFC3339 timestamp>`. Long-running operations
  periodically refresh only that field; `state`/`context`/`bead` stay
  fixed at last transition. Staleness becomes
  `time.Since(max(timestamp, last_keepalive)) >= threshold`. v1/v2
  readers ignore the new field and continue to use `timestamp` (which
  now lags slightly behind reality during long calls — false positives
  remain for them, but new readers see truth).
- **Pros**:
    - Minimal schema delta — one optional string field.
    - Backwards compatible by construction: missing field → default
      to `timestamp` → identical to current behavior.
    - Cleanly separates "agent did a thing" (`timestamp`) from
      "agent is alive right now" (`last_keepalive`). Investigators
      can read both.
    - Bash parse is one `jq` line: `(.last_keepalive // .timestamp)`.
    - Writer side: `Keepalive(townRoot, session)` only updates the
      one field — no race with concurrent `state` transitions.
- **Cons**:
    - Requires v1/v2 readers to upgrade if we want them to benefit from
      the new freshness channel. Rolling rollout: stuck-agent-dog (bash)
      and `IsSessionHeartbeatStale` (Go) must both learn the new
      `max(...)` rule before false-positives go to zero. During the
      gap, old readers still false-positive.
    - Doesn't carry tool-call diagnostic info (no `tool_call` field).
      We pay the schema-bump cost without getting the diagnostic
      benefit.
    - Doesn't address witness/refinery (gu-0nmw) — still no heartbeat
      file at all for those roles.
- **Effort**: Low. ~20 LOC in `heartbeat.go` (new field + helper),
  ~3 LOC in dog (new jq), ~5 LOC in `IsSessionHeartbeatStale`.

#### Option C: Replace JSON files with SQLite or Dolt

- **Description**: Move heartbeat state into a single SQLite database
  (`<townRoot>/.runtime/heartbeats.db`) or into the existing Dolt
  beads server. One row per session, columns for every field. Writers
  upsert; readers query.
- **Pros**:
    - Strongly typed schema with migrations.
    - Atomic multi-row reads (e.g., "all stale agents in rig X").
    - Indices possible (e.g., `WHERE last_keepalive < now() - 600s`).
- **Cons**:
    - **Massively over-engineered for the data volume.** ~150 rows
      max; 100x growth still fits in a single page.
    - Adds a process dependency: bash dog now needs `sqlite3` binary
      or a Dolt connection just to ask "is this agent alive?"
      Currently it's `jq` on a file — an order of magnitude simpler.
    - Dolt heartbeats would create one commit per write (~13K writes
      per cycle of 100-agent rig). The Dolt fragility section of
      AGENTS.md explicitly warns against this kind of churn.
    - SQLite introduces a single-writer locking surface for what is
      currently an embarrassingly-parallel file write per agent.
      Lock contention possible during heartbeat storms.
    - Migration is a one-way door — once tooling depends on a DB, the
      file fallback story is gone.
- **Effort**: High. ~500 LOC + migration + tooling rewrite + bash
  dog rewrite. **Net negative** given the data is ~30KB.

#### Option D: Additive v3 schema with `last_keepalive`, `liveness`, `tool_call`

- **Description**: Extend `SessionHeartbeat` with three optional fields:
  ```go
  type SessionHeartbeat struct {
      // v1
      Timestamp time.Time `json:"timestamp"`
      // v2 (gt-3vr5)
      State   HeartbeatState `json:"state,omitempty"`
      Context string         `json:"context,omitempty"`
      Bead    string         `json:"bead,omitempty"`
      // v3 (this design)
      LastKeepalive time.Time      `json:"last_keepalive,omitempty"`
      Liveness      LivenessSignal `json:"liveness,omitempty"`
      ToolCall      string         `json:"tool_call,omitempty"`
  }
  ```
  Where `LivenessSignal` is a small typed enum (`alive`, `keepalive`,
  `exiting`) that the agent stamps on each write. Long-running ops use
  the new `Keepalive(townRoot, session, toolCall)` API which writes
  `last_keepalive=now`, `liveness="keepalive"`, and an optional
  `tool_call` label, while leaving `state`/`context`/`bead` untouched
  (read-modify-write on the file with a defensive fallback). Staleness
  computation becomes a typed `LivenessVerdict` produced by a single
  helper `LivenessFor(townRoot, session) Verdict` that all three call
  sites use.
- **Pros**:
    - **Solves the false-stale problem** while preserving v1/v2 reader
      behavior verbatim — missing fields → defaults → current logic.
    - One source of truth for staleness: every reader (Go, bash dog,
      future witness/refinery scanner) goes through the same
      computation.
    - Tool-call diagnostic is free once the schema is open: dog logs
      can say "polecat thunder stuck in tool_call=llm_call for 12m"
      instead of "stale heartbeat 12m." This dramatically improves
      incident triage (compounds over time).
    - Same shape works for witness/refinery: they get the same file
      schema when gu-0nmw lands, no second migration.
    - `liveness="exiting"` provides a clean termination signal that
      the orphan-prune logic can use to expire heartbeats deterministically
      (instead of hoping `RemoveSessionHeartbeat` ran).
- **Cons**:
    - Three new fields instead of one — slight schema surface area cost.
      Mitigated: all optional, all skippable by old readers.
    - Read-modify-write on `Keepalive` introduces a write race if a
      `state` transition fires concurrently with a keepalive. Mitigation:
      since both writers serialize through `os.WriteFile`, the worst case
      is a lost update on one field; everything is `omitempty` so partial
      writes degrade gracefully. (This is the same race as today; v2
      already has it.)
    - Stuck-agent-dog must learn the `max(timestamp, last_keepalive)`
      and `liveness` rules. Bash diff is small but must roll out before
      v3 writers go fleet-wide.
- **Effort**: Low–Medium. ~80 LOC in `heartbeat.go` (new fields, types,
  `Keepalive` helper, `LivenessFor` verdict), ~30 LOC in dog (new jq),
  ~20 LOC in `IsSessionHeartbeatStale` (use verdict). One round of
  test updates in `heartbeat_test.go`.

### Recommendation

**Adopt Option D (additive v3 schema).** Rationale:

1. **The minimum schema delta that fixes the bug is Option B**, but
   we're paying schema-bump tax either way; getting `tool_call` and
   `liveness` for the same migration cost is a clear win.
2. **Option A is tempting but loses a debugging axis** that compounds
   over time. Investigators reading
   `~/.runtime/heartbeats/<session>.json` after an incident need to be
   able to tell whether the timestamp reflects user-visible progress
   or a keepalive ping. That distinction has no other home if we don't
   add a separate field.
3. **Option C is wrong-tool-for-the-job.** The data plane is ~30KB.
   Adding SQLite/Dolt for 150 rows is the same kind of mistake as
   storing every flag in a database.
4. **Option D coexists with v1/v2** — every existing reader keeps
   working with no code change, and new readers progressively migrate.
   No flag day, no rollout coordination beyond the dog plugin.

The schema is small enough to specify completely:

```jsonc
// v3 SessionHeartbeat — backwards compatible with v1 and v2.
{
  // v1 (always present)
  "timestamp": "2026-05-29T05:03:12.000Z",  // RFC3339 UTC, last write of any kind

  // v2 (optional — present iff IsV2()); gt-3vr5
  "state":   "working|idle|exiting|stuck",  // agent-reported state
  "context": "free-form what-am-i-doing",   // optional
  "bead":    "gu-leg-uxj2c",                // optional, current hook

  // v3 (optional — present iff IsV3()); this design
  "last_keepalive": "2026-05-29T05:03:42.000Z",  // RFC3339, last keepalive ping
  "liveness":       "alive|keepalive|exiting",   // last write classification
  "tool_call":      "llm_call|go_build|go_test"  // optional, what's running
}
```

Field semantics, written into godoc:
- `timestamp` — UTC time of the last write, regardless of source.
  Preserved from v1; never rewritten to indicate keepalive only.
- `state` — agent self-reported state. Updated on transitions only.
- `last_keepalive` — UTC time the agent most recently asserted "I'm alive."
  Set by `Keepalive` and by every `Touch*` write (so both bump it).
  Reader rule: effective freshness = `max(timestamp, last_keepalive)`.
- `liveness` — classification of the last write. `"keepalive"` means the
  write was solely a freshness ping (state/context/bead unchanged);
  `"alive"` means a normal touch with state info; `"exiting"` is the
  final write before `gt done` exits.
- `tool_call` — optional diagnostic label for the operation currently
  running. Free-form; dog logs include it verbatim. Cleared on
  transitions out of long calls.

A new helper `LivenessFor(townRoot, session) LivenessVerdict` collapses
the three call sites' staleness logic onto one definition (see api.md).
Verdict is a typed enum: `Fresh`, `Stale`, `Exiting`, `Missing`.

## Constraints Identified

- **Must preserve v1/v2 reader behavior**. Old `IsV2()`,
  `EffectiveState()`, and `IsSessionHeartbeatStale` must keep working
  unchanged on v3 files. Verified by leaving v1/v2 fields exactly as they
  are; new fields are all `,omitempty`.
- **Must not introduce a database**. The data plane is ~30KB town-wide
  and stays JSON files. ZFC discipline (compiled-in defaults, override
  via `operational.polecat.*`) for thresholds.
- **Must remain best-effort**. All writes ignore errors; new fields
  must tolerate write loss without corrupting existing state. Read-
  modify-write keepalive: on read failure, fall back to `Touch*`-style
  blind write.
- **Must support shell + Go readers symmetrically**. Bash dog parses
  with `jq`; Go scanners parse with `encoding/json`. No new types that
  are awkward in either (e.g. nested objects, arrays of structs). All
  v3 fields are scalar.
- **Must accommodate witness/refinery** (gu-0nmw). The schema is rig-
  agnostic: same fields, same shape. The write contract for those roles
  is identical — they call `Keepalive` from their patrol cycle.
- **Migration is forward-only**. v1 → v2 already happened (gt-3vr5);
  v2 → v3 follows the same pattern. **No downgrade path** — if a v3
  field is set, never assume a v2 reader will respect it; just rely on
  `omitempty` so the file stays parseable.
- **No SIGQUIT, no fsync, no flock.** Heartbeat writes must remain
  syscall-cheap. The "best-effort" contract precludes synchronization
  primitives.

## Open Questions

1. **Should `last_keepalive` be merged with `timestamp`, or kept
   separate?** This design recommends *separate* (so investigators can
   distinguish "real activity" from "keepalive ping"). The trade-off is
   a slightly more complex reader rule (`max(...)`). UX leg should
   weigh in: do operators care about the distinction, or do they only
   care about "is the agent alive yes/no?" If only the latter, we
   could collapse to `timestamp` (Option A semantics) but pay the
   schema cost anyway for `liveness` and `tool_call`.

2. **What goes in `tool_call` — a fixed enum or free-form?** Fixed enum
   gives type safety + autocomplete in dog; free-form lets agents
   self-describe novel operations (e.g., `"opus_4.5_completion"`).
   Recommendation: free-form string, but Gas Town conventions document
   common values (`"llm_call"`, `"go_build"`, `"go_test"`,
   `"merge_wait"`, `"dolt_query"`).

3. **Should the schema carry a version field (`"v": 3`)?** Today there
   is none — version is inferred from field presence (`IsV2()` checks
   `state != ""`). This design follows the same pattern (`IsV3()`
   checks `last_keepalive != zero`). Explicit `"v"` would simplify
   debugging but adds noise on every write. **Recommendation: skip the
   explicit version field**, follow existing convention.

4. **Should heartbeat files be committed to the orphan-prune story
   (`liveness="exiting"` => safe-to-delete)?** Today
   `RemoveSessionHeartbeat` is the only delete path. Adding a "tombstone"
   classification would let a janitor process safely prune old files
   even when sessions die without cleanup. Probably out of scope for
   this leg — it's a lifecycle improvement that depends on schema, not
   a schema concern.

5. **Witness and refinery heartbeats — same dir, same schema, but
   what's the session-name convention?** Today `<session>.json` matches
   tmux session names like `gastown_upstream-thunder`. Witness/refinery
   tmux sessions are `<rig-prefix>-witness` / `<rig-prefix>-refinery`,
   so the file naming "just works" — confirm with API leg.

## Integration Points

- **api.md (interface design)** — owns the read/write verbs: agree on
  exactly which functions consume the new fields. This leg specifies
  the schema; api.md specifies `Keepalive(townRoot, session, toolCall)`,
  `LivenessFor(...) LivenessVerdict`, the new `gt heartbeat` subcommand
  surface, and the migration story for `IsSessionHeartbeatStale`.

- **scale.md (scalability)** — confirms that the file-based approach
  stays viable at projected scale and recommends the keepalive cadence
  (write rate analysis). This leg defers cadence/threshold decisions
  to scale.md; the schema accommodates *any* cadence the scale leg
  picks.

- **UX (sibling leg, if it exists)** — operator-facing display of
  liveness verdicts. The `tool_call` field is the most operator-visible
  addition; UX should specify how dog logs and `gt witness` summaries
  surface it.

- **Witness escalation (gu-0nmw)** — the schema accommodates witness/
  refinery heartbeat writes today. The actual *integration* — when do
  those roles touch heartbeats, what triggers escalation — is owned
  by gu-0nmw and the witness design leg if one exists.

- **Stuck-agent-dog plugin** — must learn the v3 staleness rule
  (`max(timestamp, last_keepalive)`) and learn to surface `tool_call`
  in its logs. Schema choice here directly drives that diff.

- **Orphan-prune lifecycle** — the `liveness="exiting"` signal is a
  deterministic tombstone marker that simplifies safe pruning of
  abandoned heartbeat files. Out of scope for this leg, but the
  schema should not foreclose it (and Option D doesn't).

- **Heartbeat config (`internal/config/operational.go::PolecatThresholds`)**
  — `HeartbeatStaleThreshold` already exists. v3 may want to add
  `HeartbeatKeepaliveInterval` (cadence for the background goroutine)
  and `HeartbeatVeryStaleThreshold` (mirror of the deacon
  `VeryStaleThreshold`). Schema leg specifies these as ZFC constants;
  scale leg picks the values.

## Sources

- `internal/polecat/heartbeat.go` — current v2 schema and write API
  (read 2026-05-29 in worktree).
- `internal/polecat/manager.go::isSessionProcessDead` — primary Go
  reader (lines 2199–2246).
- `internal/daemon/reap_dead_agent_wisps.go` — bead-based reaper for
  witness/refinery (no heartbeat read today).
- `internal/deacon/heartbeat.go` — separate deacon schema, kept out
  of scope for this design.
- `plugins/stuck-agent-dog/run.sh` — bash reader; thresholds and jq
  parsing.
- `internal/config/operational.go` — `PolecatThresholds`,
  `DeaconThresholds`, ZFC defaults.
- Sibling leg `.designs/cv-p3fem/api.md` — interface design,
  references this schema.
- Sibling leg `.designs/cv-p3fem/scale.md` — confirms file-based
  approach stays viable at scale.
- Bead `gu-leg-uxj2c` description — assignment, dependencies, and
  reference beads (gu-0nmw, gu-rh0g, gt-3vr5, gt-qjtq).

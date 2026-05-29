# User Experience Analysis

## Summary

The false-stale-heartbeat problem is, from the user's seat, a **trust
problem disguised as a timer problem**. Operators (the human running
`gt witness status`, the agent reading a `<system-reminder>`, and the
dog plugin escalating "MASS DEATH") all currently look at *one* signal
— a timestamp — and answer *three* different questions with it: "is
this process alive?", "is this agent making progress?", "is it safe
to reap?". Conflating those three questions is what produced the
2026-05-19 mass-kill incident and the gu-rh0g refinery silently dying.
The UX fix is to give each question a distinct surface with a distinct
verdict and distinct affordance, while keeping the **default operator
flow exactly as short as it is today** — `gt witness status` for
humans, `gt heartbeat status --json` for plugins.

The recommendation centers on three operator-facing ideas: (1) a typed
**three-tier liveness verdict** — `ALIVE` / `MAYBE_DEAD` / `DEAD` —
that operators can teach in one breath and that maps cleanly onto the
existing `STUCK_STALLED_THRESHOLD=600` mental model; (2) **affordance
parity between humans and plugins** so a bash script and a person see
the same words for the same state (no jq-extracts-a-different-truth-
than-the-CLI surprises); (3) a **drop-in `WithKeepalive` Go helper plus
`gt heartbeat keepalive` shell command** that long-running call sites
adopt as a single defer-line, removing the "I forgot to ping" trap
without requiring authors to think about it. The agent-side learning
curve is one line of code or one shell line; the operator-side learning
curve is one new column in `gt witness status`.

This UX design defers schema/threshold details to the data and scale
legs (whose recommendations align with mine), focuses on the verbs and
output operators actually see, and makes one strong opinion the API
leg left open: **`gt heartbeat keepalive` without `GT_SESSION` must
warn-and-noop, never error**, because every fail-loud keepalive in a
build wrapper is a future P0.

## Analysis

### Key Considerations

- **There are three users, not one.** UX must distinguish:
    1. **The human operator** running `gt witness status`,
       `gt peek <agent>`, or reading escalation mail.
    2. **The agent itself** writing/reading its own heartbeat (every
       polecat, deacon, refinery — and `gt done`/`gt heartbeat
       --state=stuck` flows).
    3. **Automation** (the `stuck-agent-dog` plugin, future witness
       restart logic, log alerts) consuming a JSON contract.
   Optimizing only for one breaks the others. The current design
   accidentally optimized for automation (raw timestamp parses) and
   left humans squinting at JSON.
- **The mental model collision is real.** Today `state=stuck` means
  *agent self-reported stuck* and `staleness>threshold` means *no
  recent gt command*. Operators in incident threads have used "stuck"
  to mean both. The new UX must vocalize the distinction every time
  it surfaces a verdict — never "stuck", always **"stuck (self-
  reported)" vs "wedged (no keepalive)" vs "dead (process gone)"**.
  This is the highest-leverage piece of UX in the whole design.
- **Polecats read `<system-reminder>` blocks; humans read terminals.**
  Both are LLM- and human-readable English. The keepalive subsystem
  must not emit anything operationally noisy that pollutes either
  channel. (Existing best-effort silence on writes is correct; UX
  preserves it.)
- **Error messages are docs.** Gas Town's existing convention —
  one-line diagnosis, one-line hint — is good. Maintain it. New
  error messages must include the **next command to run**, not just
  the symptom.
- **Discoverability is two-tier:**
    - `gt heartbeat --help` (CLI flag-level) for "what can this
      command do?"
    - `gt help heartbeat` / docs page for "why does this exist?"
  The new keepalive subcommand needs both layers, plus a one-line
  example in the parent `gt heartbeat` help.
- **Power users tune; beginners default.** The 30s keepalive cadence,
  10m grace, and 20m hard-dead defaults are fine for 95% of users.
  ZFC keys (`operational.polecat.heartbeat_keepalive_grace`,
  `operational.polecat.heartbeat_dead_threshold`) cover the long
  tail. **Do not surface these to beginners** — they're not in
  `--help` synopsis, only in long-form docs.
- **Surprise and confusion budget is near zero.** Gas Town has
  burned operators with mass-death cascades, false reaps, and
  spawn storms. Any new affordance that introduces a confusing
  new state name, a new escalation source, or a new "is this thing
  on?" question will be rejected on sight. Net-new UX surface must
  pay for itself by collapsing existing surface.
- **Backward-compatible verbs.** `gt heartbeat` (no flags) writes
  state=working today. Keep it. `gt heartbeat --state=stuck` works
  today. Keep it. Adding subcommands (`gt heartbeat keepalive`,
  `gt heartbeat status`) without breaking the bare form is the only
  acceptable migration.
- **Scriptability ⇒ JSON is mandatory.** `gt heartbeat status
  --json` must be the canonical machine surface (replacing the
  shell jq parse in `stuck-agent-dog`). Without it, automation will
  keep parsing files directly and the schema-drift problem recurs.
- **The verdict words must survive a translation chain.** They're
  shown to humans in the terminal, in escalation mail, in
  bd-update notes, in dolt commits, and embedded in
  `<system-reminder>` blocks read by other agents. Pick words that
  read clearly in *all* of those contexts. ALIVE / MAYBE_DEAD /
  DEAD pass that test; "FRESH"/"STALE"/"GONE" do not (STALE
  collides with `bd` issue staleness vocabulary).

### Options Explored

#### Option 1: Single new verb — `gt heartbeat status` — plus `--json` for plugins

- **Description**: One operator-facing read command,
  `gt heartbeat status [--session=NAME] [--json]`. Humans run it
  bare and get a 3-line summary; plugins pipe `--json` through jq.
  Every other surface (`gt witness status`, `gt peek`, escalation
  mail) reuses this verdict internally. Long-running ops adopt
  `gt heartbeat keepalive --op=NAME` (shell) or
  `polecat.WithKeepalive(...)` (Go).
- **Pros**:
    - **One verb to teach.** New operators learn one new command
      ("how do I check if this agent is alive?") that answers the
      question across all roles (polecat, witness, refinery,
      deacon).
    - **One JSON contract** that plugins, dashboards, and future
      tooling build on — kills the schema-drift problem at the
      source.
    - **Composability**: `gt witness status` becomes a thin
      wrapper that lists rigs and calls `Liveness()` per session.
    - **Discoverability**: subcommand under existing `gt
      heartbeat` is exactly where operators look already.
    - **Backward compatibility**: bare `gt heartbeat` still
      works.
- **Cons**:
    - One more subcommand on `gt heartbeat`. Mitigated by ranking
      it first in `--help` (it's the most useful).
    - Operator must remember `status` (not `check`, not `info`,
      not `whois`). Convention nudges toward `status` since
      `gt witness status`, `gt dolt status`, `gt session status`
      all exist.
- **Effort**: Low.

#### Option 2: Promote heartbeat liveness into `gt witness status` directly

- **Description**: No new top-level surface; operators only ever
  use `gt witness status`, which gets a "Liveness" column. No
  `gt heartbeat status` subcommand at all.
- **Pros**:
    - Maximum surface compaction — zero new commands to learn.
    - Operators already type `gt witness status` reflexively.
- **Cons**:
    - **Plugins still need a JSON entry point.** Without a
      programmatic surface, `stuck-agent-dog` keeps parsing files
      directly → schema drift recurs.
    - Doesn't help refinery/deacon/individual-session debugging
      where the operator wants *one agent's* verdict, not a
      whole-rig table.
    - `gt witness status` becomes the kitchen sink, which
      historically rots into an unreadable wall of columns.
- **Effort**: Low for the visible UX, but **doesn't actually
  solve the API surface problem**. Recommend against on its own.

#### Option 3: Auto-keepalive everywhere, no operator surface

- **Description**: Just make `WithKeepalive` automatic for all
  long calls; don't add any operator UX. Trust that the existing
  `gt witness status` will reflect freshness once keepalive
  fires in long-running ops.
- **Pros**:
    - Zero operator-facing UX changes; zero learning curve.
    - All wins are "free" if every long call adopts the helper.
- **Cons**:
    - **Doesn't address gu-0nmw's operator question** ("how do
      I know the refinery is dead?"). Operators still squint at
      raw timestamps in JSON.
    - Hides the verdict logic in code; debugging "why did
      stuck-agent-dog fire?" becomes a code-spelunking exercise.
    - Punts the schema-drift problem entirely.
- **Effort**: Lowest, but only because it solves half the
  problem. Recommend against.

#### Option 4: Make the verdict appear automatically in `gt prime` output

- **Description**: Whenever an agent runs `gt prime` (every
  session start, every recovery), include a one-line summary of
  the agent's own liveness verdict and the rig's witness/refinery
  verdict in the prime banner.
- **Pros**:
    - Highly discoverable for agents (which is half the audience).
    - Works in tandem with Option 1, not against it.
- **Cons**:
    - `gt prime` output is already heavy (~40KB). Adding a
      few-hundred-byte block is fine, but adding one per neighboring
      agent is not.
    - Information overload risk; if every prime nags about a
      borderline-stale heartbeat, operators tune it out.
- **Effort**: Low.
- **Verdict**: **Adopt as an addendum to Option 1**, not a
  replacement. One line: `liveness: ALIVE (heartbeat 12s ago,
  keepalive 8s ago)`. No more.

#### Option 5: Surface verdict in `<system-reminder>` blocks injected to agents

- **Description**: When stuck-agent-dog or witness detects a peer
  agent in MAYBE_DEAD, inject a system-reminder block into nearby
  agents' inboxes ("polecat foo is wedged, consider waiting on it
  with a nudge").
- **Pros**:
    - Cross-agent awareness without dolt commits (no mail
      pollution).
    - Matches the "village" model in the polecat docs.
- **Cons**:
    - Emergent noise. If three polecats all see "polecat foo is
      wedged," they each may try to nudge, escalate, or otherwise
      react. This is the recipe for an emergent storm.
    - Hard to scope correctly without a policy layer that doesn't
      exist today.
- **Effort**: Medium, mostly policy-design rather than code.
- **Verdict**: **Defer**. File a bead for future iteration. Not
  in the v3 scope.

### Recommendation

**Adopt Option 1 (a single `gt heartbeat status` verb plus
`--json`) as the primary UX, augmented by Option 4 (one-line
liveness in `gt prime` banners).** Option 2 is rolled in as a
secondary touch — `gt witness status` gains a Liveness column
that consumes the same `Liveness()` verdict.

#### Operator surface (humans)

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

Three opinions in this layout:

1. **Liveness verdict is line 2 — first thing the operator sees
   after the session name.** That's the question they came to
   answer.
2. **`hint:` line on `MAYBE_DEAD`/`DEAD` shows the operator's
   next command.** Errors-as-docs pattern; matches existing
   `gt heartbeat --state=...` convention. Never just "session
   is dead" with no follow-up.
3. **No raw timestamps in the default output.** Ages
   (`12s ago`, `18m ago`) read at a glance; absolute timestamps
   are noise unless the operator asks for `--verbose` or
   `--json`.

#### Plugin / automation surface

```
$ gt heartbeat status --session=polecat-shiny-tmqt --json
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

Two opinions in this shape:

1. **`verdict_reason` is a stable enum**, not free-form text.
   Possible values: `keepalive_fresh`, `heartbeat_fresh`,
   `inside_grace_window`, `past_dead_threshold`, `no_heartbeat_file`.
   Plugins can branch on these without regex-parsing English.
2. **Thresholds embedded in the response** — plugins don't have
   to read ZFC config separately. Saves a round trip and prevents
   threshold drift between the binary and the plugin.

#### Agent surface (writers)

Three forms, ranked by ergonomics:

```go
// 1. The defer one-liner — what 95% of long-running call sites use.
defer polecat.WithKeepalive(townRoot, session, "llm-call", 30*time.Second)()

// 2. Manual ticker — for code that already has its own loop.
go polecat.KeepaliveLoop(ctx, townRoot, session, "brazil-build", 30*time.Second)

// 3. One-shot — for code that just wants to bump the clock.
polecat.Keepalive(townRoot, session)
```

Shell:

```bash
# In a build wrapper / gate runner:
gt heartbeat keepalive --op=brazil-build &
KEEPALIVE_PID=$!
trap "kill $KEEPALIVE_PID 2>/dev/null" EXIT
brazil-build
```

The shell form is intentionally not as ergonomic as the Go form
because shell wrappers are rarer and operators writing them are
power users who tolerate one extra line. **Don't try to invent
a "smart" shell form** (`gt heartbeat with-keepalive -- bb`) —
quoting bugs alone make it not worth it.

#### Help text contract

```
$ gt heartbeat --help
Update or read agent heartbeat state.

Used by agents to self-report state and by operators to query
liveness. The witness reads heartbeats instead of inferring
liveness from process timers (gt-3vr5).

Usage:
  gt heartbeat                 Touch heartbeat (state=working)
  gt heartbeat --state=stuck   Self-report stuck state
  gt heartbeat keepalive       Refresh liveness without changing state
  gt heartbeat status          Query liveness verdict (operator)

States:
  working  Actively processing (default)
  idle     Waiting for input
  exiting  In gt done flow
  stuck    Self-reporting stuck (triggers witness escalation)

Examples:
  gt heartbeat                                    # touch (working)
  gt heartbeat --state=stuck "blocked on auth"    # self-report
  gt heartbeat keepalive --op=llm-call            # long-call ping
  gt heartbeat status                             # check current session
  gt heartbeat status --session=foo --json        # for plugins
```

This pattern — first usage line is the bare default, last is the
power-user form — matches `gt mail`, `gt session`, and `gt dolt`
help. Operators recognize the shape immediately.

#### `gt witness status` integration

```
$ gt witness status gastown_upstream
Witness for gastown_upstream:
  state: running (pid 12345)

Polecats:
  SESSION                          LIVENESS     STATE        BEAD
  polecat-shiny-tmqt               ALIVE        working      gu-leg-xtwu2 (12s)
  polecat-mighty-zlmn              MAYBE_DEAD   working      gu-leg-axyz  (18m)
  polecat-curly-bplq               ALIVE        idle         (none)

Refinery: ALIVE (heartbeat 4s ago)
Deacon:   ALIVE (heartbeat 22s ago)
```

The Liveness column is **left of State**, because liveness is
the supervisor question and the operator scans for it first. The
ages in trailing parens give context without dominating.

#### `gt prime` integration

Add exactly one line to the prime banner:

```
liveness: ALIVE (heartbeat 1s ago, keepalive 1s ago)
```

— rendered after the session id, before the formula checklist.
Reassures the agent it's been seen; gives a debug breadcrumb if
the prime ran but a subsequent stall produces a false-positive.

#### Error messages

```
$ gt heartbeat status --session=does-not-exist
no heartbeat for session "does-not-exist"
hint: list active sessions with `gt session list`

$ gt heartbeat status
GT_SESSION not set and --session not provided
hint: pass --session=NAME or run inside a Gas Town session

$ gt heartbeat keepalive
warning: GT_SESSION not set; keepalive is a no-op
hint: pass --session=NAME if calling from a build wrapper
```

The third message is the one strong opinion the API leg flagged
as open: **keepalive without GT_SESSION warns and noops, does
not error**. Errors in build wrappers fail the build; warnings
get logged and ignored. The harm-from-silent-noop is far smaller
than the harm-from-broken-CI, and operators *will* call
`gt heartbeat keepalive` from environments where GT_SESSION is
unset (shared dev shells, CI-style scripts, accidental bare
shells).

### Recommendation summary

| Surface | Primary | Secondary |
|---------|---------|-----------|
| Human read | `gt heartbeat status` | `gt witness status` (table form) |
| Plugin read | `gt heartbeat status --json` | (replaces inline jq) |
| Agent write | `polecat.WithKeepalive(...)` Go defer | `gt heartbeat keepalive` shell |
| Agent self-touch | `gt heartbeat` (bare) — unchanged | `gt heartbeat --state=stuck` — unchanged |
| Operator awareness | `gt prime` one-line banner | escalation mail (existing) |

## Constraints Identified

- **Backward-compatible default behavior.** `gt heartbeat`
  (no args) MUST continue to write state=working as it does
  today. Anything else is a hard break for in-flight scripts.
- **Best-effort writes only.** All keepalive paths swallow
  errors silently. UX MUST NOT add error-level output to the
  write path; that breaks builds. Log to debug-only file if at
  all.
- **No new top-level commands.** Everything lives under
  `gt heartbeat`, `gt witness`, or `gt prime`. The `gt` CLI is
  already at the limit of what new operators can scan in
  `gt --help`.
- **Three-word verdict ceiling.** ALIVE / MAYBE_DEAD / DEAD.
  No fourth state. Operators conflate states the moment there
  are more than three; gu-rh0g is exactly this failure mode.
  If a fourth distinction matters (e.g., "alive but stuck"),
  it goes in the `state` field and an explanatory parenthetical,
  not the verdict.
- **JSON shape is API-stable.** `verdict`, `verdict_reason`,
  `age_seconds`, `last_keepalive_age_seconds`, `state`,
  `keepalive_op`, `bead`, `thresholds` are the v3 contract.
  Future fields are additive only. Plugin authors will lock in
  this shape immediately; renaming is a P0.
- **No emoji or color in piped output.** `gt heartbeat status`
  detects TTY; uses plain ASCII when not on a TTY (matches
  existing Gas Town convention). Plugins must not see ANSI codes.
- **Non-English-locale safe.** Verdict words and `verdict_reason`
  enums are ASCII-only. Ages render with `s`/`m`/`h` suffixes,
  not localized strings.
- **Agent-self-report (state=stuck) overrides verdict only as
  display.** Internal verdict logic does NOT change because of
  agent self-report — that's the lifecycle channel, not the
  liveness channel. The display layer can show "ALIVE
  (self-reported stuck)" but `verdict` stays ALIVE.
- **No new escalation paths.** UX changes do not add new mail
  triggers, new dolt commits, or new escalation classes. The
  existing `gt escalate` flow is unchanged. Adding a
  `verdict=DEAD` row to `gt witness status` is enough; humans
  decide when to escalate from there.

## Open Questions

1. **Should `gt heartbeat status` default to the current
   session, or require `--session=NAME`?** Recommendation:
   **default to current session** (`GT_SESSION`); error
   helpfully if neither set. Matches `gt heartbeat --state=...`
   precedent. Power users still pass `--session=NAME` to
   inspect peers. *Resolved within this leg.*
2. **Should `gt witness status` show a `Liveness` column for
   *every* session including healthy ones, or only flag
   anomalies?** Showing every row is more honest (no "is the
   table missing rows?" question) but adds visual noise.
   Recommendation: show every row, but **dim non-ALIVE
   verdicts** with bold/color on TTY only. Plain ASCII on
   pipe. *Cross-cutting with terminal-output style decisions
   elsewhere in gt.*
3. **`keepalive_op` granularity in operator output.** API leg
   asks: free-form string vs enum. Recommendation: free-form,
   but truncate to first 32 chars in human display
   (`op=llm-call:claude:tool-use:...`). JSON gets the full
   string. *Cross-cutting with API leg — should converge on
   "free-form string."*
4. **Should `gt heartbeat status` in prime mode (i.e. inside
   `gt prime` banner) show neighboring agents too?** Tempting
   ("at a glance know if your peers are wedged") but blows the
   info budget. Recommendation: **no**. Operators run
   `gt witness status` for that. Keep prime focused. *Cross-
   cutting with agent ergonomics.*
5. **Color/icon in human output.** Should ALIVE render green,
   MAYBE_DEAD yellow, DEAD red? Recommendation: yes, on TTY
   only, **never in JSON or pipe output**. Match the existing
   `gt session list` color convention if there is one;
   otherwise pick the standard traffic-light scheme. *Defer
   to general gt output style guide if one exists.*
6. **Should the agent's *own* state appear in their `gt prime`
   banner, or just their liveness verdict?** Recommendation:
   **just liveness**. The agent already knows what it's doing;
   showing them the prime-banner copy of their own state is
   noise. The verdict is the new fact. *Resolved within this
   leg.*
7. **`gt heartbeat watch` — proposed by API leg as a tail-f
   diagnostic surface.** UX-wise, this is for forensics, not
   day-to-day. Recommendation: **defer to a follow-up bead**
   so we don't conflate the basic verdict surface with a
   streaming/log surface. Ship the verdict; learn what
   operators ask for. *File as gu-leg-followup.*
8. **Translation: should `liveness:` and verdict words be
   localized?** Gas Town is English-only today by convention.
   Recommendation: **no localization layer**. If that changes,
   verdict enums (ASCII) survive; only the display strings
   need swapping. *Resolved.*

## Integration Points

- **API leg (`api.md`)**: This UX design relies on the typed
  `Liveness()` reader API the API leg specifies. We agree on
  verdict enum (ALIVE / MAYBE_DEAD / DEAD); we add
  `verdict_reason` to the JSON shape, which the API leg should
  pick up in its struct (additive). UX leg owns final wording
  of human output and error hints; API leg owns Go signatures
  and JSON field names.
- **Scale leg (`scale.md`)**: Scale leg recommends Option 5
  (hybrid keepalive + PID-first decision tree). UX leg's
  verdict words map cleanly onto that tree:
  PID-dead → DEAD; PID-alive + heartbeat-stale-but-in-grace
  → MAYBE_DEAD; PID-alive + fresh → ALIVE;
  PID-alive + state=stuck → ALIVE (self-reported stuck).
  No conflict; decision-tree refinement happens in the
  consumer-side reader API.
- **Data leg**: depends on `last_keepalive` and `keepalive_op`
  fields existing in heartbeat v3. UX shows them as
  `keepalive 8s ago, op=llm-call`. If data leg renames the
  fields, UX strings change accordingly. Recommend data leg
  keep the names as proposed.
- **Integration leg**: must inventory which long-running call
  sites adopt `WithKeepalive`. UX leg's contract is: as long
  as those adopt the helper, default operator UX
  ("MAYBE_DEAD never appears under normal load") works. If
  adoption is partial, operators will see scattered
  MAYBE_DEAD verdicts and lose trust in the verb. Integration
  leg owns the rollout list.
- **Security leg**: agrees `keepalive_op` is opaque
  free-form. UX displays it untrusted (truncate to 32 chars,
  strip control chars). If security wants an enum, UX
  switches to dropdown-style display.
- **gu-0nmw (witness escalation for stale refinery)**: This
  UX gives gu-0nmw's escalation logic its operator surface.
  When a refinery hits DEAD verdict, the operator sees it in
  `gt witness status` (rig-level table) AND
  `gt heartbeat status --session=refinery-foo` (session
  detail). The escalation mail subject line should match
  the verb operators just learned: "Refinery DEAD: gastown_upstream"
  (not "Refinery stale" or "Refinery missing heartbeat").
  This is a small but important consistency win — operators
  build trust faster when the words match across surfaces.
- **`stuck-agent-dog` plugin**: replaces inline jq+date
  arithmetic with `gt heartbeat status --json | jq .verdict`.
  Plugin's role narrows to policy ("if verdict=DEAD for >2
  cycles, escalate"). UX leg's contract is: the JSON shape
  is stable; plugin authors can build on it without fear of
  breaking changes.
- **`gt prime` banner**: adds one line — verdict + ages.
  Bounded to one line so as not to overflow the existing
  prime info budget.
- **Agent docs (polecat CLAUDE.md, deacon docs, refinery
  docs)**: update the "if you suspect liveness issues"
  paragraph to point to `gt heartbeat status` first,
  `gt witness status` second. This replaces the current
  "check `~/.runtime/heartbeats/<session>.json` with jq"
  guidance which has been the source of operator confusion.

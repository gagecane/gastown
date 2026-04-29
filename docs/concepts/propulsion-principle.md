# The Propulsion Principle

> **If you find something on your hook, YOU RUN IT.**

Gas Town is a steam engine. Agents are pistons. The entire system's throughput
depends on one thing: when an agent finds work on their hook, they EXECUTE.

## Why This Matters

- There is no supervisor polling asking "did you start yet?"
- The hook IS your assignment - it was placed there deliberately
- Every moment you wait is a moment the engine stalls
- Other agents may be blocked waiting on YOUR output

## The Handoff Contract

When you were spawned, work was hooked for you. The system trusts that:

1. You will find it on your hook
2. You will understand what it is (`bd show` / `gt hook`)
3. You will BEGIN IMMEDIATELY

This isn't about being a good worker. This is physics. Steam engines don't
run on politeness - they run on pistons firing. You are the piston.

## Molecule Navigation: Key Enabler

Molecules enable propulsion by providing clear waypoints. You don't need to
memorize steps or wait for instructions - discover them:

### Orientation Commands

```bash
gt hook              # What's on my hook?
gt prime             # Shows inline formula checklist
bd show <issue-id>   # What's my assigned issue?
```

### The New Workflow: Inline Formula Steps

Formula steps are shown inline at prime time — no step beads to manage:

```bash
gt prime             # See your checklist
# Work through each step in order
gt done              # Submit and self-clean (polecats)
gt patrol report     # Close + next cycle (patrol agents)
```

No step closures, no `bd mol current`, no momentum-killing transitions.

**The new workflow (propulsion):**
```bash
bd close gt-abc.3 --continue
```

One command. Auto-advance. Momentum preserved.

### The Propulsion Loop

```
1. gt hook                   # What's hooked?
2. bd mol current             # Where am I?
3. Execute step
4. bd close <step> --continue # Close and advance
5. GOTO 2
```

## The Failure Mode We're Preventing

```
Polecat restarts with work on hook
  → Polecat announces itself
  → Polecat waits for confirmation
  → Witness assumes work is progressing
  → Nothing happens
  → Gas Town stops
```

## Startup Behavior

1. Check hook (`gt hook`)
2. Work hooked → EXECUTE immediately
3. Hook empty → Check mail for attached work
4. Nothing anywhere → ERROR: escalate to Witness

**Note:** "Hooked" means work assigned to you. This triggers autonomous mode
even if no molecule is attached. Don't confuse with "pinned" which is for
permanent reference beads.

## The Capability Ledger

Every completion is recorded. Every handoff is logged. Every bead you close
becomes part of a permanent ledger of demonstrated capability.

- Your work is visible
- Redemption is real (consistent good work builds over time)
- Every completion is evidence that autonomous execution works
- Your CV grows with every completion

This isn't just about the current task. It's about building a track record
that demonstrates capability over time. Execute with care.

## The Hook Lifecycle Rule: Guaranteed Consumer or Bounded Lifetime

Propulsion works because the hook is an expectation of consumption. But a
hook without a consumer is just a leak. To keep the engine balanced, every
hooked bead must satisfy one of two rules:

1. **Guaranteed consumer**: The producer knows, at creation time, which
   agent or role will consume the hook. Examples:
   - A witness creates a cleanup wisp with a specific polecat assignee —
     the polecat's completion closes the wisp.
   - A refinery creates an MR bead the refinery itself will process.

2. **Bounded lifetime (TTL)**: If the consumer is not guaranteed (opportunistic
   delivery, best-effort handoff, broadcast), the bead MUST have a TTL after
   which the reaper closes it with reason `"ttl-expired"`. The default TTL
   for hooked mail beads is **24 hours**; handoff mail typically lives
   seconds to minutes, so anything past a day is almost certainly orphaned.

**The failure mode this prevents**: patrol agents (witness, refinery, deacon)
create predecessor HANDOFF/wisp beads at each cycle. If a downstream molecule
fails, idles, or reroutes, the predecessor stays `hooked` forever. With no
TTL and no consumer linkage, `bd list --status=hooked` grows unbounded until
the dead-letter backlog is visible in other health checks.

**Exclusions from the TTL reaper**:
- Agent heartbeat beads (`issue_type='agent'`) — long-lived by design
- Beads with labels `gt:standing-orders`, `gt:keep`, `gt:role`, `gt:rig` —
  long-lived by convention
- Pinned beads — not on the hook (`status='pinned'`, not `'hooked'`)

**Operator tools**:
- `gt doctor` — the `hooked-dead-letter` check warns when >10 hooked mail
  beads are older than 30 minutes in a rig
- `gt reaper reap-hooked-mail` — closes stale hooked mail past the TTL
- `mol-dog-reaper` — runs the full sweep (wisps + hooked mail + stale issues)
  on the configured daemon interval

When filing a new bead type that will land on a hook, decide up-front: is
there a guaranteed consumer? If not, add it to the TTL reaper.

### Consumer linkage (gu-ub1l)

TTL is the fallback. The *first* half of the rule — "guaranteed consumer" —
is also enforceable at creation time using the `consumer_bead_id` metadata
convention:

- When a producer knows which bead will consume the hook (for example, a
  session-handoff mail whose recipient is a specific successor polecat
  bead), it may declare this via `--consumer-bead <id>` on `gt mail send`.
- The flag sets `Metadata["consumer_bead_id"]` on the resulting hooked
  bead. The value is a bead ID of the expected consumer.
- The reaper (`reaper.ReapHookedMail`, `reaper.ScanHookedMailCounts`) and
  the `hooked-dead-letter` doctor check apply an exclusion — beads whose
  `consumer_bead_id` points to a still-open bead are **exempt** from the
  TTL sweep and from dead-letter accounting.
- When the consumer is closed, missing, or the metadata is absent, the
  TTL fallback applies as before. No behavior change for producers that
  don't set the metadata.

This layers cleanly on top of TTL: a hooked bead with a live consumer is
never dead-letter (there is a named successor; the engine is balanced).
Once the consumer closes, the bead becomes a normal TTL candidate and will
be reaped after its TTL elapses.

**When to set `--consumer-bead`**: producer-side code that creates a bead
it intends for a specific other bead (or successor session) to consume.
Examples: session handoff mail addressed to a successor polecat bead;
cross-rig coordination where the expected responder is known up-front.

**When NOT to set it**: broadcast/opportunistic delivery, group mail,
agent heartbeats, or anything where the consumer is not known at creation
time — let TTL handle those.

**Data model**: the exclusion is a single SQL fragment
(`reaper.ConsumerAliveClause`) referenced by every hooked-mail query so
the semantics stay consistent across reap, scan, metrics, and doctor.

See `gu-hhqk` for the design history and the lifecycle audit that surfaced
this rule. See `gu-ub1l` for the consumer-linkage design decision.

---
name: crew-sling-work
description: >
  Assign work to a Gas Town agent's hook using gt sling — the unified dispatch
  command. Use when you want to "sling work", "assign this bead", "dispatch this
  issue", "hand work to a polecat/crew/dog", "kick off a formula", or choose a
  merge strategy (mr, direct, local) for how completed work lands. Covers target
  resolution, auto-spawning polecats, passing instructions via --args, and
  formula slinging.
allowed-tools: "Bash(gt *), Bash(bd *)"
version: "1.0.0"
author: "Gas Town"
---

# Crew Sling Work — Dispatching Work in Gas Town

`gt sling` is THE command for assigning work. It attaches a bead (or formula) to
an agent's hook and starts it immediately. Per the propulsion principle: **if
it's on your hook, you run it.**

## When to use this skill

- "Sling gt-abc to crew" / "assign this issue to a polecat"
- "Dispatch this work to greenplace" / "hand this to a dog"
- "Run the release formula" / "kick off code-review on this bead"
- You need to control how the work lands (merge queue vs direct vs local)

## Instructions

### Step 1: Identify the bead and the target

You need two things: a **bead ID** (the work) and a **target** (who runs it).

```bash
bd ready              # find dispatchable work
bd show <id>          # confirm the bead is the right one
```

Target resolution (most common forms):

| Target | Meaning |
|--------|---------|
| `gt sling <bead>` | Self — current agent's hook |
| `gt sling <bead> crew` | A crew worker in the current rig |
| `gt sling <bead> <rig>` | Auto-spawn a polecat in that rig |
| `gt sling <bead> <rig>/<Name>` | A specific polecat |
| `gt sling <bead> <rig> --crew <name>` | A named crew member |
| `gt sling <bead> mayor` | The Mayor |
| `gt sling <bead> deacon/dogs` | Auto-dispatch to an idle dog |

### Step 2: Choose the merge strategy

The strategy is stored on the auto-convoy and controls how finished work lands.

```bash
gt sling <bead> <rig> --merge=mr       # merge queue (DEFAULT, recommended)
gt sling <bead> <rig> --merge=direct   # push branch straight to main
gt sling <bead> <rig> --merge=local    # keep on feature branch, no merge
```

Use `--merge=mr` unless you have a specific reason. It routes through the
Refinery (see the `crew-merge-queue` skill).

### Step 3: Sling it

```bash
gt sling <bead> <target>
```

Expected output: a confirmation line naming the hooked bead and target, plus an
auto-convoy ID (e.g. `Auto-convoy gu-w0n`). Single-issue slings auto-create a
convoy for dashboard visibility unless you pass `--no-convoy`.

### Step 4: Pass instructions (optional)

The executor is an LLM, so natural-language args are interpreted directly.

```bash
gt sling <bead> <rig> --args "focus on the SQL injection paths"

# Multi-line / shell-quoting-safe: use --stdin
gt sling <bead> <rig> --stdin <<'EOF'
Focus on:
1. SQL injection in query builders
2. XSS in template rendering
EOF
```

`--args` is stored on the bead and surfaced via `gt prime` when the agent starts.

### Step 5: Verify the work was placed

```bash
gt convoy list           # the auto-convoy should appear
bd show <bead>           # status moves toward in_progress once the agent primes
```

## Examples

**Example 1 — sling a bead to a crew worker, merge via queue (default)**
User says: "Assign gu-77vjo to crew"
```bash
gt sling gu-77vjo crew
```
Result: bead hooked on a crew worker, auto-convoy created, lands via merge queue.

**Example 2 — auto-spawn a polecat in a rig with focused instructions**
User says: "Send the auth refactor to greenplace, focus on token expiry"
```bash
gt sling gp-abc greenplace --create --args "focus on token expiry edge cases"
```
Result: polecat spawned in greenplace, bead hooked, instructions stored.

**Example 3 — sling a formula (wisp)**
User says: "Run code-review on gt-abc"
```bash
gt sling code-review --on gt-abc crew
```
Result: formula cooked + wisped onto the target, work begins.

## Sling vs hook vs handoff

| Command | Behavior |
|---------|----------|
| `gt hook <bead>` | Just attach — no action |
| `gt sling <bead>` | Attach + start now (keep current context) |
| `gt handoff <bead>` | Attach + restart with fresh context |

## Troubleshooting

**Error: target rig has no polecat / "agent not found"**
Cause: the polecat doesn't exist yet.
Solution: add `--create` to spawn one: `gt sling <bead> <rig> --create`.

**Sling blocked on unread mail**
Cause: target agent has unread mail and sling guards against it.
Solution: clear mail, or override with `--force` if you know it's safe.

**Work slung but agent idle / not running**
Cause: agent needs a nudge or hasn't primed.
Solution: confirm the hook with `gt hook` (as the target) and that the agent
session is alive; the bead runs when the agent primes.

**Wrong merge strategy chosen**
Cause: omitted `--merge`, got the default `mr`.
Solution: re-sling with the intended `--merge=direct` or `--merge=local`, or
update the convoy's strategy.

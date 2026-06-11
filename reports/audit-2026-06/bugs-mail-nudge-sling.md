# Bug Hunt Audit — mail / nudge / sling / dispatch

**Bead:** gu-nid89.5 (epic: gu-nid89 Whole-Repo Gastown Audit)
**Date:** 2026-06-11
**Auditor:** polecat vault (4 parallel deep-read agents + adversarial verification)
**Scope:** `internal/mail/`, `internal/nudge/`, `internal/sling/`, `internal/dispatch/` (~6.8K LOC source, excl. tests)

## Method

One deep-reading agent per subsystem read every source file and traced real
code paths (including consumers outside the audited dirs to confirm
reachability). Findings rated HIGH only when a concrete failing path was
demonstrated. The three HIGH findings below were each independently
re-verified against source before filing.

## Summary of findings

| # | Subsystem | Title | Confidence | Class |
|---|-----------|-------|------------|-------|
| 1 | nudge | Poller drops drained nudges on injection failure (no requeue) | **HIGH** | loss |
| 2 | dispatch | `gt sling <id>` CLI path missing reference-tripwire guard | **HIGH** | eligibility |
| 3 | mail | CC recipient gets N duplicate copies of every fan-out message | **HIGH** | double-delivery |
| 4 | mail | In-process store inbox path silently drops all CC mail | MED (latent) | loss |
| 5 | nudge | Poller delivery skips cross-process nudge flock (garbled/lost) | MED | corruption/misroute |
| 6 | sling | Bead-title text collides with substring failure classification | MED | classification → work-loss |
| 7 | dispatch | Closed molecule re-collected from stale description pointer | MED | eligibility/dispatch-loop |
| 8 | mail | `IsRecipientMuted` checks wrong session for crew workers | MED | routing |
| 9 | nudge | Orphaned-claim sweep can resurrect in-flight claim → double-fire | LOW | double-fire |
| 10 | nudge | TOCTOU between liveness check and SIGTERM in StopPoller | LOW | process-lifecycle |
| 11 | nudge | Watcher goroutine can die silently, stalling ACP-safe path | LOW | error-swallowing |
| 12 | sling | `GenerateShortID` ~24 bits entropy, no uniqueness check | LOW | idempotency |
| 13 | mail | CC'd "hooked" handoff messages dropped (`isOpen` vs `isOpenOrHooked`) | LOW | loss (likely intentional) |

---

## HIGH findings

### Finding 1 — Nudge poller drops drained nudges on injection failure

- **FILE:LINE:** `internal/cmd/nudge_poller.go:135-147`
- **CONFIDENCE:** HIGH — **VERIFIED**
- **CLASS:** loss

`nudge.Drain` permanently removes nudges from the queue (rename to `.claimed`,
then `os.Remove`); the returned slice is the only remaining copy. If
`NudgeSessionWithOpts` returns an error, the nudges are only logged to stderr —
**never requeued**:

```go
drained, err := nudge.Drain(townRoot, sessionName)   // deletes from queue
...
formatted := nudge.FormatForInjection(drained)
if err := t.NudgeSessionWithOpts(sessionName, formatted, nudgeOpts); err != nil {
    fmt.Fprintf(os.Stderr, "nudge-poller: injection error for %s: %v\n", sessionName, err)
}   // drained nudges lost
```

`NudgeSessionWithOpts` has real failure modes (cross-process flock timeout,
in-process lock timeout, send-keys failure, `sendEnterVerified` failure).
The sibling ACP consumer (`internal/acp/propulsion.go:222`) proves intended
design: it calls `nudge.Requeue(...)` on every delivery-failure path.
Verified: `nudge.Requeue` has exactly one caller (the ACP path); the poller
calls it nowhere.

**IMPACT:** For every non-Claude agent (Gemini, Codex, Cursor — the agents this
poller exists to serve), any transient tmux/lock hiccup during delivery
silently drops the nudge batch. The agent never wakes; the sender believes
delivery succeeded. This is the exact "nudges stranded/lost" failure the
subsystem was built to prevent.

**Fix shape:** On injection error, `nudge.Requeue(townRoot, sessionName, drained)`
before continuing (mirroring `propulsion.go`).

---

### Finding 2 — `gt sling <id>` CLI path missing the reference-tripwire guard

- **FILE:LINE:** missing call in `internal/cmd/sling.go` guard block (~808-935); present at `internal/cmd/sling_dispatch.go:307` and `internal/cmd/sling_schedule.go:273`. Predicate itself correct at `internal/dispatch/eligibility.go:307-315`.
- **CONFIDENCE:** HIGH — **VERIFIED**
- **CLASS:** eligibility

The auto-dispatch and convoy-schedule paths both call
`isReferenceTripwireBeadInfo(info)` to refuse dispatch of reference/tripwire
beads. Verified by grep: only `sling_dispatch.go` and `sling_schedule.go`
contain the call — `sling.go` (the interactive `gt sling <id>` path) does not.
Its guard sequence jumps from the awaiting-merge guard straight to the
open-children guard, skipping reference-tripwire entirely.

`IsReferenceTripwireBeadInfo`'s own doc warns: *"scheduling one creates a
sling-context + auto-convoy and lets a polecat hook then CLOSE the tripwire,
taking the gate down."*

**IMPACT:** A bead with `issue_type=reference` or the `do-not-dispatch`/`pinned`
label, dispatched via the direct CLI `gt sling <id>`, is **not refused**. A
polecat hooks it, finds nothing to do, and closes it — taking down a permanent
safety gate that is supposed to stay open forever.

**Fix shape:** Add the same `if isReferenceTripwireBeadInfo(info) { return ... }`
block to `sling.go`'s guard sequence, alongside the other container/identity
guards.

---

### Finding 3 — CC recipient receives N duplicate copies of every fan-out message

- **FILE:LINE:** `internal/mail/router.go:967-970` (`sendToGroup`), `1298-1301` (`sendToList`), `1579-1583` (`sendToChannel`); via `buildLabels` `router.go:250-253` and inbox CC-query `mailbox.go:272-280`
- **CONFIDENCE:** HIGH — **VERIFIED**
- **CLASS:** double-delivery

Every fan-out path shallow-copies the message and rewrites only `To`/`ID`
(verified in all three sites). The CC slice is copied by reference, so each
fan-out bead carries the full CC list. `buildLabels` then writes a
`cc:<identity>` label on **every** fan-out copy. A CC'd recipient's inbox is a
union of an assignee query and a CC query (`--label cc:<identity>`), and dedup
(`appendBeadsMessagesIf`) is keyed on **bead ID**. Because each fan-out copy
gets a distinct bd-generated ID (`msgCopy.ID = ""`), the CC'd recipient matches
N separate beads.

Trigger: `gt mail send list:foo --cc alice` where `foo` = {bob, carol, dave} →
three beads, each labeled `cc:gastown/alice` → Alice sees the message 3×.
Same for `@group` and `channel:` sends.

**IMPACT:** A CC'd agent's inbox is spammed with one copy of a broadcast/list
message per recipient. Because the mail hook re-injects all open mail every
turn, this burns recipient context and can drown real assignments — the exact
failure mode the codebase fights elsewhere. Scales with list/channel size.

**Fix shape:** Strip/clear `CC` on fan-out copies (the canonical message already
carries the CC labels for the CC query), or attach CC labels to only one
canonical copy rather than every fan-out bead.

---

## MED findings

### Finding 4 — In-process store inbox path silently drops all CC mail

- **FILE:LINE:** `internal/mail/store.go:62-98`
- **CONFIDENCE:** MED (latent — activates when store fast-path is wired for `List()`)
- **CLASS:** loss

The subprocess inbox path runs four query families (assignee, CC, wisp-assignee,
wisp-CC) and unions them. The in-process store equivalent (`storeListFromDir`)
queries only by `Assignee` — there is no `cc:<identity>` label query. Any
message where the identity is a CC recipient (not assignee) is invisible in
store mode. No production caller currently wires a store into a Mailbox (only
package + tests), so it is latent today; it becomes live CC message-loss the
moment the store path is enabled for inbox listing.

### Finding 5 — Poller delivery skips the cross-process nudge flock

- **FILE:LINE:** `internal/cmd/nudge_poller.go:90` (builds `NudgeOpts{}` without `TownRoot`) + `internal/tmux/sendkeys.go:884-891`
- **CONFIDENCE:** MED
- **CLASS:** corruption / misroute

The poller builds `nudgeOpts` with `TownRoot` empty, so `NudgeSessionWithOpts`
skips the cross-process flock branch entirely. That flock exists specifically
because "concurrent nudges interleave send-keys/Enter and produce garbled or
empty input (GH#gt-ukl8)." The poller is a separate process from interactive
`gt nudge` and the daemon, so the in-process lock gives no protection. When the
poller injects concurrently with another nudge to the same session, send-keys
sequences interleave → garbled text or a missing Enter (nudge stranded in the
input buffer). Reintroduces gt-ukl8 on the poller path.

**Fix shape:** Set `nudgeOpts.TownRoot = townRoot` so the poller participates in
the cross-process flock.

### Finding 6 — Bead-title text collides with substring-based failure classification

- **FILE:LINE:** `internal/sling/classify.go:28-43, 76-96, 231-250`; producers interpolate `%q` title, e.g. `internal/cmd/sling.go:941`
- **CONFIDENCE:** MED
- **CLASS:** classification → work-loss

`ClassifySlingFailure` matches **substrings** of sling stderr, and producers
interpolate the bead **title** into that stderr. A deferred bead titled e.g.
`fix "not found" error in parser` produces a first line containing both
`not found` and `bead ` → `IsBeadNotFoundError` returns true →
the deferred bead is classified as the **terminal** `NotFound` class instead of
`Deferred`. Same collision for titles containing `do-not-dispatch`,
`epic container`, `has open children`, etc. (route to terminal `DoNotDispatch`
/ `StructuralNonWork`).

In the live consumer (`convoy_manager.go`), the `NotFound` route is currently
saved by a `bd show` existence check, degrading to lost backoff (5-min
re-feed storm). But the `do-not-dispatch`/structural routes call
`handleNonWorkBead`, which untracks **without** an existence guard — dropping a
live deferred bead from convoy tracking. `classify.go`'s header markets these
predicates as the shared contract for "6+ producers," any of which doing
`IsTerminal(...)→drop` inherits direct work loss.

**Fix shape:** Match against the structural prefix of the line (before the
interpolated `%q` title), or classify off a structured error code rather than
free text embedding titles.

### Finding 7 — Closed molecule re-collected from stale description pointer

- **FILE:LINE:** `internal/dispatch/eligibility.go:529-554` (esp. 545-551)
- **CONFIDENCE:** MED
- **CLASS:** eligibility / dispatch-loop

The dependency-bond loop correctly skips closed/tombstone molecules but does
**not** mark them in `seen[]`. The subsequent description-pointer block
(`attached_molecule`) has no status check, so when a closed bond and a stale
`attached_molecule: <same-id>` coexist, the closed molecule is appended anyway —
undoing the status filter. The caller (`sling.go:1235-1249`) then refuses a
legitimate re-sling ("already has N attached molecule(s)") for a phantom
already-closed molecule, starving the bead until `--force` or manual cleanup.

**Fix shape:** Ensure the description pointer is cleared when the bond is closed;
at minimum, don't let the description path contradict the status filter (record
skipped closed IDs in `seen`, or status-check the description pointer).

### Finding 8 — `IsRecipientMuted` checks the wrong session for crew workers

- **FILE:LINE:** `internal/mail/router.go:2104-2130` (`addressToAgentBeadID`), used by `IsRecipientMuted` `router.go:2064-2090`
- **CONFIDENCE:** MED
- **CLASS:** routing

Canonical addresses are normalized to `rig/name` (crew/polecat distinction
stripped). `addressToAgentBeadID` has no reachable crew branch for a normalized
address, so it always returns the polecat session name. For a crew worker,
`IsRecipientMuted` queries DND under the polecat bead ID — wrong/empty result.
**Note:** the live `notifyRecipient` path is unaffected (it calls
`isSessionMuted` per candidate session ID). Impact limited to callers of the
exported `IsRecipientMuted` helper.

---

## LOW findings

### Finding 9 — Orphaned-claim sweep can resurrect an in-flight claim → double-fire
- **FILE:LINE:** `internal/nudge/queue.go:194-214`
- **CLASS:** double-fire. The sweep restores any `*.claimed.*` older than the 5-min `StaleClaimThreshold` to the deterministic `.json` name. A drainer whose delivery legitimately exceeds 5 min still holds its claim; a concurrent sweep renames it back and another drainer re-delivers. Bounded by the threshold; no liveness/owner check ties a claim to its drainer.

### Finding 10 — TOCTOU between liveness check and SIGTERM in StopPoller
- **FILE:LINE:** `internal/nudge/poller.go:162-181`
- **CLASS:** process-lifecycle. Between `pollerProcessAlive(pid)` and `proc.Signal(SIGTERM)`, the poller can exit and the PID be recycled → stray SIGTERM to an unrelated process. Short window, requires fast PID recycle; no pgid/start-time verification.

### Finding 11 — Watcher goroutine can die silently, stalling the ACP-safe path
- **FILE:LINE:** `internal/nudge/poller.go:249-276`
- **CLASS:** error-swallowing. The watch goroutine can terminate on fsnotify init/`Add` failure or a closed channel without signaling `closed`; callers using the Watcher as the "ACP-safe alternative to polling" sit forever believing they're watching a live queue. No supervisor recovery (poller_dog covers only PID-file pollers).

### Finding 12 — `GenerateShortID` ~24 bits entropy, no uniqueness check
- **FILE:LINE:** `internal/sling/convoy.go:49-53`
- **CLASS:** idempotency. 3 random bytes → birthday collisions non-trivial in the low thousands of convoys; `rand.Read` error discarded (biases toward `aaaaa` on short read). Collisions surface at `bd create`; severity depends on bd duplicate-ID behavior (unconfirmed).

### Finding 13 — CC'd "hooked" handoff messages dropped from inbox
- **FILE:LINE:** `internal/mail/mailbox.go:188,198` (CC uses `isOpen`) vs `180,195` (assignee uses `isOpenOrHooked`)
- **CLASS:** loss (likely intentional). A CC recipient on a `hooked` message is filtered out until it transitions to open. Probably intentional (CC shouldn't see in-flight handoffs) — flagged for confirmation.

---

## Categories assessed clean

- **mail:** deadlocks/lock-ordering (single flock per op, no nesting); queue corruption (write-tmp-then-rename); two-phase delivery ack ordering (crash-safe, idempotent).
- **nudge:** misrouting (pane/window resolution correct); supervisor respawn race (idempotent `StartPoller`); zombie/orphan processes (Setpgid detach + Release, init reaps).
- **sling:** `MatchesTarget` double-dispatch (false-negative → friction not re-dispatch); `schedule.go` recycle-on-stale (only fires on open context, success closes it); `target.go` ValidateTarget (empty-segment rejection before early returns).
- **dispatch:** the three target files are pure decision logic — no goroutines/channels/mutexes/package state (verified by grep + `go vet`), so deadlock/race/capacity classes are structurally N/A within scope; `VerifyBeadIDMatch` strictness is intentional and tested.

## New beads filed for confirmed HIGH bugs

Beads filed under epic gu-nid89 for the three verified HIGH bugs:

- **gu-nid89.31** — Finding 1 (nudge): poller drops drained nudges on injection failure
- **gu-nid89.32** — Finding 2 (dispatch/sling): `gt sling <id>` CLI path missing reference-tripwire guard
- **gu-nid89.33** — Finding 3 (mail): CC recipient gets N duplicate copies of every fan-out message

## Sources

- Source audited in-repo: `internal/mail/`, `internal/nudge/`, `internal/sling/`, `internal/dispatch/` at branch `polecat/vault/gu-nid89.5--mqa3sedd` (base `main`).

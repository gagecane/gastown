# Bug Audit: internal/polecat + internal/witness + internal/reaper

- **Bead:** gu-nid89.4 (epic gu-nid89: Whole-Repo Gastown Audit)
- **Date:** 2026-06-11
- **Auditor:** polecat rust (gastown_upstream)
- **Scope:** `internal/polecat/` (8.3K LOC), `internal/witness/` (10.4K LOC), `internal/reaper/` (3.3K LOC) — ~22K LOC combined
- **Method:** Parallel per-subsystem read with focus on lifecycle state machines, stuck-state detection, race conditions in spawn/nuke/reap, predicate logic, and resource leaks. All HIGH findings were re-verified against the source by the synthesizing auditor before filing.

## Summary

| # | Subsystem | Title | Confidence | Class | Bead |
|---|-----------|-------|------------|-------|------|
| 1 | reaper | Sling-context guard omits `'hooked'` → live slung work reaped | **HIGH** | false-positive reap / data loss | gu-6reia |
| 2 | reaper | Parent-exclude guard omits `'hooked'` → child wisp reaped under live parent | **HIGH** | false-positive reap / data loss | gu-gvwqx |
| 3 | witness | SLOT_OPEN nudge/mail dropped every patrol scan (abandoned timer) | **HIGH** | notification leak / throughput | gu-uukrs |
| 4 | witness | `polecatHasAutoSaveCommits` returns after first remote → work-loss nuke | **HIGH** | control-flow / data loss | gu-1ufs3 |
| 5 | polecat | `IsRefineryHeartbeatStale` uses `Timestamp` not `EffectiveLastKeepalive()` | MED | false-positive stale | — |
| 6 | polecat | `ExpectedIdleUntil` suppression precedes PID fast-path | MED | stuck-detection ordering | — |
| 7 | polecat | Pool `Save()` on spawn-failure path runs without pool lock | MED | race | — |
| 8 | polecat | `Stop()` returns spurious error on clean graceful exit | MED | error handling | — |
| 9 | witness | Successful-restart-then-die never trips crash-loop backoff | MED | stuck-detection gap | — |
| 10 | reaper | `SET autocommit=0` + `COMMIT` issued over `*sql.DB` pool, not pinned conn | MED | txn correctness | — |
| 11 | reaper | `ScrubStaleActiveMR` clears `active_mr` without `RequireGitSafe` gate | MED | premature cleanup | — |
| 12 | witness | Empty/reaped hook status bucketed inconsistently across predicates | LOW | predicate consistency | — |
| 13 | witness | Redispatch limiter caches first `maxPerMinute` for process lifetime | LOW | stale config | — |
| 14 | reaper | Mail purge leaves dangling reverse `dependencies` rows | LOW | incomplete cleanup | — |
| 15 | polecat | `RemoveWithOptions` MR-open guard contradicts its comment | LOW | comment/code mismatch | — |
| 16 | polecat | `RunStashAutoPop` iterates by stale index after refetch | LOW | latent fragility | — |

---

## HIGH Confidence

### 1. Sling-context exclude guard omits `'hooked'` — live slung work gets reaped (DATA LOSS)

- **File:** `internal/reaper/reaper.go:287` (`liveTrackedContextExcludeJoin`; used by `Scan`, `Reap`, `purgeClosedWisps`)
- **Class:** false-positive orphan detection / data loss + incorrect predicate
- **Verified:** yes — re-read source + confirmed work-bead status semantics.

`liveTrackedContextExcludeJoin` is meant to spare a sling-context wisp whenever its tracked work bead is still live. Its own doc comment states the intent literally: *"exclude a wisp from reaping when it is a sling-context AND its tracked work bead is open/hooked/in_progress."* The wisp-side check includes `'hooked'`; the issues-side check does not:

```sql
LEFT JOIN wisps tw ON tw.id = wd.depends_on_issue_id
LEFT JOIN issues ti ON ti.id = wd.depends_on_issue_id
WHERE wd.type = 'tracks'
AND (tw.status IN ('open', 'hooked', 'in_progress') OR ti.status IN ('open', 'in_progress'))
                                                       -- ^ 'hooked' MISSING
```

The tracked work bead lives in the **`issues`** table (per the comment at reaper.go:271-272). When a bead is dispatched/slung it is set to **`status=hooked`** (confirmed: `hookBeadWithRetry` runs `bd update <bead> --status=hooked --assignee=...` in `internal/cmd/sling_helpers.go:1357`, and verifies `verifyInfo.Status == "hooked"`; `IssueStatus.IsAssigned()` treats `hooked` as actively assigned). So in the *normal* case — a context whose work bead is hooked onto a running polecat — the `ti.status` branch is false, the wisp is **not** excluded, and the reaper closes/purges the live sling-context. This is exactly the double-dispatch / "issue not found in CloseSlingContext" scenario the guard was added to prevent (gu-i0oaq / gu-ycihb); the guard is inert precisely when it is needed.

**Fix:** add `'hooked'` to the `ti.status` set.

### 2. Parent-exclude guard omits `'hooked'` for issue parents — child wisps reaped under a live parent (DATA LOSS)

- **File:** `internal/reaper/reaper.go:254` (`parentExcludeJoin`)
- **Class:** false-positive orphan detection / data loss + incorrect predicate
- **Verified:** yes — re-read source.

Same root defect as #1, in the sibling helper. `parentExcludeJoin` excludes a wisp from reaping when its parent is still open. The wisp-parent test includes `'hooked'`; the issue-parent test omits it:

```sql
LEFT JOIN wisps pw ON pw.id = wd.depends_on_issue_id
LEFT JOIN issues pi ON pi.id = wd.depends_on_issue_id
WHERE wd.type = 'parent-child'
AND (pw.status IN ('open', 'hooked', 'in_progress') OR pi.status IN ('open', 'in_progress'))
                                                       -- ^ 'hooked' MISSING for issue parents
```

A hooked issue is actively assigned and non-terminal (`IsTerminal()` is false for `hooked`). Result: a wisp whose live parent issue is in `hooked` status is treated as an orphan ("parent already reaped") and closed by `Reap`.

**Fix:** add `'hooked'` to the `pi.status` set. Both #1 and #2 are the same one-token omission and should be fixed together.

### 3. SLOT_OPEN nudge/mail to Mayor dropped on every patrol scan (abandoned timer)

- **File:** `internal/witness/slot_open_coalescer.go:94` (`time.AfterFunc`) + `internal/witness/handlers.go:599` (`Add`) + `internal/cmd/patrol_scan.go` (no `Flush()` before exit at line 1046)
- **Class:** notification leak / pipeline throughput
- **Verified:** yes — confirmed `Flush()` is never called from any production caller; patrol_scan returns at line 1046 with no flush.

`notifyMayorSlotOpen` queues the SLOT_OPEN tmux-nudge + mail-fallback through a coalescer that schedules delivery via a 5-second `time.AfterFunc`. But the witness patrol runs as a short-lived `gt patrol scan` process (`internal/cmd/patrol_scan.go`), which calls `DiscoverCompletions`/zombie detection and then returns. The process exits in well under 5 s, so the `AfterFunc` timer **never fires** and `dispatchSlotOpenBatch` is never invoked. `slotOpenCoalescer.Flush()` exists but has zero production callers.

The synchronous channel-event path (`EmitToTown … SLOT_OPEN`) still fires, so a Mayor sitting in `await-event` recovers. But the tmux-nudge + mail fallback — the path explicitly designed for a Mayor running under ACP / parked at the Claude prompt rather than in await-event — is lost on every cycle. That is the GH#2727 "Mayor sits idle with open beads" failure the coalescer was meant to fix.

**Fix:** call `getSlotOpenCoalescer().Flush()` before `patrol_scan` returns (or deliver the nudge/mail synchronously for the short-process model).

### 4. `polecatHasAutoSaveCommits` only checks the first remote — auto-save WIP nuked as "merged"

- **File:** `internal/witness/handlers.go:1453-1469`
- **Class:** control-flow (premature `return` inside loop) / data loss
- **Verified:** yes — re-read source.

```go
func polecatHasAutoSaveCommits(g *git.Git, remotes []string, defaultBranch string) (bool, error) {
    for _, remote := range remotes {
        messages, err := g.LogOneline(remote + "/" + defaultBranch + "..HEAD")
        if err != nil { continue }
        for _, line := range strings.Split(messages, "\n") {
            if strings.Contains(strings.ToLower(line), autoSaveMarker) { return true, nil }
        }
        return false, nil // BUG: returns after first remote, never checks the rest
    }
    return false, fmt.Errorf("could not check commit messages against any remote")
}
```

The `return false, nil` at the end of the loop body executes on the first iteration whenever `LogOneline` succeeds, so only `remotes[0]` is ever inspected. On multi-remote rigs (this rig has both `origin` and `upstream`), if auto-save commits are only visible against a non-first remote, the function returns `false`. Combined with `classifyPolecatMergeState`, a polecat whose only divergent commits are unpushed gt-pvx auto-save safety-net commits can be classified `MergeCheckMerged` instead of `MergeCheckAutoSave`, leading `handleZombieRestart` to **archive (nuke)** it — losing the WIP the `MergeCheckAutoSave` guard exists to protect.

**Fix:** only `return false` after the loop completes (move it outside the `for`), so all remotes are checked before concluding no marker exists.

---

## MED Confidence

### 5. `IsRefineryHeartbeatStale` uses `Timestamp` instead of `EffectiveLastKeepalive()`

- **File:** `internal/polecat/completion/refinery_guard.go:74`
- **Class:** false-positive stale detection.

Everywhere else freshness is computed from `EffectiveLastKeepalive()` = `max(Timestamp, LastKeepalive)` (e.g. `liveness.go:218`, `heartbeat.go:251`), because the v3 keepalive ticker bumps `LastKeepalive` while `Timestamp` only moves on state-bearing writes. This guard uses `time.Since(hb.Timestamp)` directly. A refinery in a long merge-queue gate run keeps `LastKeepalive` fresh but lets `Timestamp` age past the 5-minute threshold, so the guard declares it stale and pushes polecats onto the dead-refinery / direct-push-blocked path (`MarkAwaitingRefineryRecovery`) even though the refinery is alive — the exact false-positive class the keepalive field was introduced to eliminate.

### 6. `ExpectedIdleUntil` suppression precedes the PID fast-path

- **File:** `internal/polecat/liveness.go:233-246`
- **Class:** stuck-state detection / predicate ordering.

In `LivenessWithPID`, the `ExpectedIdleUntil` block returns `LivenessAlive` *before* the PID fast-path (liveness.go:258-278). The other two self-report hints — exit-tombstone (252) and idle (295) — are deliberately placed *after* the PID probe, with comments noting "a provably-gone PID still yields DEAD." `ExpectedIdleUntil` breaks that invariant: a polecat that declared `expected_idle_until = now+15m` and then had its process die is reported ALIVE (reason `expected_idle_until_future`) until `thresholds.Dead` elapses, even though `pidProbe` could prove it gone now. Mitigated by the `thresholds.Dead` cap (so it is bounded, hence MED not HIGH), but the single-unambiguous-PID-signal-wins property does not hold here. Consider moving the suppression after the PID probe to match the other hints.

### 7. Pool `Save()` on spawn-failure path runs without the pool lock

- **File:** `internal/polecat/manager.go:789-803` (`cleanupOnError`), reached after the pool lock is released at `manager.go:757`
- **Class:** race condition.

`AllocateAndAdd` releases `poolLock` before calling `addWithOptionsLocked`. On worktree/beads creation failure, `cleanupOnError` calls `namePool.Release(name)` + `namePool.Save()` with the pool lock not held. `Save()` serializes shared pool state (`OverflowNext`, `MaxSize`) to the state file; a concurrent `AllocateName` in another process (holding `polecat-pool.lock`) can have its committed `OverflowNext` clobbered last-writer-wins. Window is the spawn-failure rollback path only.

### 8. `Stop()` returns a spurious error on clean graceful exit

- **File:** `internal/polecat/session_manager.go:733-764`
- **Class:** error handling / false failure.

Non-force `Stop` sends `C-c`, waits for exit, then calls `KillSessionWithProcesses(sessionID)`. If the agent exited cleanly (intended), the session is already gone and that kill returns an error, so `Stop` returns `fmt.Errorf("killing session: %w", err)` despite full success. No `HasSession` re-check after the graceful wait. `StopAll` aggregates via `errors.Join`, surfacing spurious failures on the happy path.

### 9. Successful-restart-then-die never trips crash-loop backoff

- **File:** `internal/witness/handlers.go:1080-1098` (`RestartPolecatWithBackoff`), acknowledged in `polecat_session_age.go:24-27`
- **Class:** stuck-state detection gap / spawn-storm backstop weakness.

`RecordPolecatStartFailure` is called only when `RestartPolecatSession` returns an error. A restart that spawns successfully but dies again minutes later is never recorded, so `RestartCount` never increments and `polecatCrashLoopCount` (5) is never reached. The backoff/crash-loop mute protects only against restarts that *fail to spawn*, not restarts that *spawn-then-die* — the more common failure mode. The session-too-young guard mitigates immediate re-kill but not the steady-state loop.

### 10. `SET autocommit=0` + `COMMIT` issued over a `*sql.DB` pool, not a pinned connection

- **File:** `internal/reaper/reaper.go:464-469, 624-629, 942-947, 1112-1117` and mail equivalents in `hooked_mail.go` / `processed_mail.go`
- **Class:** transaction correctness / race.

All mutating reaper functions run `db.ExecContext("SET @@autocommit=0")`, the batched DML, `db.ExecContext("COMMIT")`, `DOLT_COMMIT`, then `SET @@autocommit=1` — all against a `*sql.DB` **pool** (`OpenDB` never sets `MaxOpenConns(1)` or uses `*sql.Conn`/`*sql.Tx`). `database/sql` may route each call to a different pooled connection, so the autocommit-disable, the DML, and the explicit COMMIT can land on different connections; the deferred re-enable may also miss, leaking an autocommit-disabled connection back to the pool. Works today only because serial use tends to reuse one idle connection; not guaranteed under pool growth or concurrent passes. Use `db.Conn(ctx)` or `db.BeginTx`.

### 11. `ScrubStaleActiveMR` clears `active_mr` without the git-safe gate

- **File:** `internal/reaper/active_mr_scrub.go:122-133` with `internal/polecat/active_mr.go:105`
- **Class:** premature cleanup.

The scrubber calls `AssessActiveMR` with `RequireGitSafe` unset. For a rejected/superseded/conflict terminal MR whose source issue is terminal, the assessment skips the `RequireGitSafe && !GitSafe` block and returns `Pending=false`, so the scrubber clears `active_mr` — removing the only pointer to possibly-unpreserved local commits. The on-demand recovery path (`manager.go:2811`) passes `RequireGitSafe: true` for exactly this case. The scrubber leans entirely on the `cleanup_status` WIP check, which depends on a recent/accurate self-report; a stale `cleanup_status` defeats it.

---

## LOW Confidence

### 12. Empty/reaped hook status bucketed inconsistently across predicates

- **File:** `internal/witness/handlers.go:2569-2576` (`detectSubmittedStillRunning`) vs `isOpenHookStatus` at 2620-2627.

`getBeadStatus` returns `("", true)` for a reaped/missing bead. Zombie-detection paths treat `""` + `hookFound` as "closed → not a zombie", but `isOpenHookStatus` only returns true for `open`/`hooked`/`in_progress`, so a reaped bead reads as `false` there. Harmless in the current path (a skip), but the two predicates encode "reaped bead" differently; a future caller relying on `isOpenHookStatus` to mean "not terminal" would mis-bucket.

### 13. Redispatch limiter caches first `maxPerMinute` for process lifetime

- **File:** `internal/witness/redispatch_rate_limiter.go:164-173`.

"First caller's maxPerMinute wins" on a package-level global. Moot for the per-cycle short patrol process, but if witness detection ever runs in a long-lived process (the comment anticipates `gt up` orphan recovery), the first value seen — including a `0` read before config loads — freezes for the lifetime, potentially disabling rate limiting permanently. Dormant in the current model.

### 14. Mail purge leaves dangling reverse `dependencies` rows

- **File:** `internal/reaper/reaper.go:1028-1037` (`batchDeleteRows`), used by `purgeOldMail`.

`batchDeleteRows` deletes aux rows by `issue_id` then runs a hardcoded reverse cleanup only against `wisp_dependencies` (`DELETE … WHERE depends_on_issue_id IN (…)`). For the mail/issues purge the relevant reverse table is `dependencies`, not `wisp_dependencies`; any `dependencies` row pointing at a purged mail bead via `depends_on_issue_id` is left dangling, while the `wisp_dependencies` delete is a no-op for issue IDs. Low impact (mail beads rarely have dependents) but asymmetric with the wisp path.

### 15. `RemoveWithOptions` MR-open guard contradicts its comment

- **File:** `internal/polecat/manager.go:1226-1238`.

The comment says "Even nuclear mode must not delete worktrees with unmerged MRs" / "MR status should always be checked," but the guard is `if !force`, so `Remove(name, true)` / `--force` skips the open-MR check entirely. The error message ("Use --force to override") implies `--force` bypass is the intended contract, so behavior is likely correct and the comment is stale — but worth reconciling, since the comment claims a stronger invariant than the code enforces.

### 16. `RunStashAutoPop` iterates by stale index after refetch

- **File:** `internal/polecat/completion/phases.go:114-163`.

The loop iterates `for i := len(entries)-1; i>=0; i--`, pops `entries[i]`, then reassigns `entries` to a shorter slice from a refetch while continuing to decrement the original `i`. Correct today only because git's stash renumbering keeps the index aligned for suffix pops; fragile if the filtered set ever becomes a non-suffix subset. Latent correctness hazard, not a live bug.

---

## Areas checked and found correct

- polecat: `AllocateName`/`AllocateAndAdd` pending-marker TOCTOU handling; per-polecat/pool flock discipline on success paths; `preserveUnpushedHead`/`preserveAndClearBranchStashes` fail-closed logic; `AssessActiveMR` terminal/merged gating; `DecideWorkstate` fail-open-on-lookup-failure; `WithKeepalive`/`KeepaliveLoop` ticker cancellation (idempotent `sync.Once`, no goroutine leak).
- witness: exponential-backoff schedule in `polecat_startup_backoff.go:233`; rate-limiter `pruneLocked` monotonic-order assumption; stale-rig-agent cooldown/correlation flock discipline; TOCTOU re-checks in `DetectOrphanedBeads`/`DetectStaleInProgressBeads`; teardown-gate fail-closed ordering; `false_deferred.go` fixed-string grep.
- reaper: dry-run early-`break` in mail batch loops; `referentMissing` fail-closed (`dangling_fk_scrub.go:193`); git-evidence reconcile re-reads fresh status before closing and fails closed on `!verified` (`orphan_reconcile_git.go:148`); `ReconcileMergedOrphans` skips missing/non-merged MRs; `EvaluateOpenWispAlert` bucket math; `AutoClose` preserves P0/P1; `closedWispPurgeWhere` placeholder ordering consistent between `Scan` and `purgeClosedWisps`.

## Sources

- `internal/polecat/`, `internal/witness/`, `internal/reaper/` source (gagecane/gastown fork, branch `polecat/rust/gu-nid89.4--mqa3rt7w`) — accessed 2026-06-11
- `internal/cmd/sling_helpers.go`, `internal/cmd/sling.go`, `internal/cmd/patrol_scan.go`, `internal/beads/status.go` — accessed 2026-06-11 (cross-checks for HIGH findings)

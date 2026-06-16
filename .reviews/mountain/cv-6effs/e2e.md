# End-to-End Flow Axis

## Summary

I traced the three core gastown loops as whole journeys — **work** (`gt sling` →
spawn → `gt done` → MR → refinery gate → land → unhook/close), **escalation**
(`gt escalate` → Mayor ack → close), and **mountain** (`gt mountain` → stage →
wave dispatch → ConvoyManager feed → witness skip → rollup → notify). The
dominant theme: the **work and escalation loops are exceptionally well-defended**
against permanent stranding — both have layered recovery (push-fallback ladders,
fail-closed merge-ancestry guards, the `gu-tn0xp` 72h escalation backstop) that
already closes nearly every historically-dangerous gap. The **mountain loop is
the weakest axis**: it has genuine permanent-strand states (a `ship-unverified`
convoy park that no daemon path ever re-evaluates), non-atomic / per-cycle
failure-count accounting that makes the auto-skip threshold unreliable, and a
completion-notification stamp written *before* the notification is sent.

The second, system-wide theme is **test coverage**: not one of the three loops
has an end-to-end / integration test that drives the full journey. Per-stage
unit coverage is dense and high quality, but every inter-stage handoff — exactly
where production incidents originate — is validated only in production. This is
the single highest-leverage gap on this axis.

## Score
score: 0.62

## Critical Findings (P0 — file as beads, fix urgently)

- **Title**: `convoy:ship-unverified` park has no daemon-driven recovery — permanently strands a mountain
  - **Location**: `internal/cmd/convoy.go:1599-1607` (mark), `:2894-2903` and `:2402-2411` (skipped in BOTH the completion scan and the stranded scan), `:1881` (the *only* clear path, `removeShipUnverifiedLabel`, is reachable solely from `checkSingleConvoy` via an explicit `gt convoy check <id>`).
  - **Impact**: When any leg is closed-but-unshipped (no citing commit on `origin/main`, and not a recognized `no_merge`/`review_only`/`mountain:skipped` reason), `closeConvoyIfComplete` refuses to close and stamps `convoy:ship-unverified`. Thereafter the two daemon-driven sweeps — `checkAndCloseCompletedConvoys` (no-arg, called from `engineer.go:3213`) and `findStrandedConvoys` — both explicitly skip the labeled convoy. `removeShipUnverifiedLabel` is only ever called from `checkSingleConvoy` (`convoy.go:1881`), and the daemon never invokes the single-convoy form (confirmed: no `checkSingleConvoy` caller in `internal/daemon/` or `internal/convoy/`). A mountain with one false-closed leg parks open forever — never re-evaluated, never rolled up, never notified — until a human runs `gt convoy check <id>`.
  - **Suggested fix**: On a slow timer, have the daemon re-run the single-convoy check for `convoy:ship-unverified` convoys whose tracked beads' `updated_at` advanced since the label was applied; or fold a ship-unverified re-evaluation pass into the existing completion backstop.
  - **Fingerprint**: `axis:e2e|internal/cmd/convoy.go|ship-unverified-terminal-no-daemon-recovery`

- **Title**: `gt mountain <epic>` builds no fan-in capstone — ad-hoc mountains never roll up
  - **Location**: `internal/cmd/mountain.go:166` (`runMountain` calls `computeWaves(dag)` directly) vs `internal/cmd/convoy_stage.go:333` and `:443` (`appendValidationWave` — the only code that creates a fan-in capstone bead blocked by every leg — is reachable only from `runConvoyStage`, never from `runMountain`).
  - **Impact**: `runMountain` bypasses `runConvoyStage` and assembles the DAG/waves itself, so an ad-hoc `gt mountain <epic>` produces **no synthesis/validation/rollup bead** unless the epic's own children already contain one. "Roll-up" degrades to the convoy auto-close gate (a pure AND over leg statuses); legs complete and the convoy closes with no combined deliverable. NOTE — this does NOT affect *formula-driven* convoys such as `mountain-code-review` (this very review): those declare an explicit synthesis leg wired via dependency edges (`gu-syn-qxz34` here), which dispatches normally. The bug is scoped to the ad-hoc epic-promotion path.
  - **Suggested fix**: Route `runMountain` through the same validation/synthesis-append path as `runConvoyStage`, or have it explicitly create and track a rollup bead blocked by all legs before launch.
  - **Fingerprint**: `axis:e2e|internal/cmd/mountain.go|runMountain-no-validation-wave`

## Major Findings (P1 — track, do not auto-bead)

- **Title**: No end-to-end test exercises any full loop journey (work, escalation, or mountain)
  - **Location**: `Dockerfile.e2e:73` (containerized e2e CMD runs only `-run "TestInstall"`); `internal/cmd/mq_integration_test.go` (isolated error types, not a live merge); `internal/cmd/scheduler_integration_test.go` ("No Claude credentials, no agent sessions" — dispatch dry-run only).
  - **Impact**: The post-merge close-ordering contract is unit-tested only with `bd` shell stubs (`internal/refinery/engineer_close_ordering_test.go`); the escalate→ack→close→mark-mail-read chain has no integrating test (`escalate_test.go` / `escalate_impl_test.go` / `beads_escalation_test.go` are all isolated units); the mountain stage→dispatch→skip→rollup→notify journey is exercised nowhere (`mountain_convoy_test.go` stops at Wave-1 dispatch; `convoy_manager_integration_test.go` stubs `gt` to always exit 0). Every cross-component seam — the exact place stranding happens — is verified only by the `gu-*`/`gc-*` production-incident trail.
  - **Suggested fix**: Add Dolt-backed integration tests (behind the `integration` build tag, wired into `Dockerfile.e2e`) that drive each loop end-to-end against a local bare remote and assert terminal state (source bead closed with `commit_sha`; escalation mail-copy ends `read`; mountain rolls up with one skip-closed leg).
  - **Fingerprint**: `axis:e2e|Dockerfile.e2e|no-full-journey-test`

- **Title**: Escalation mail-copy label is best-effort post-send — if it fails, the copy can never be silenced on ack/close
  - **Location**: `internal/cmd/escalate_impl.go:250-269` (annotation after `router.Send`, errors only `PrintWarning`), consumed by `internal/beads/beads_escalation.go:569-606` (`MarkEscalationMailCopiesRead` matches ONLY the `escalation:<id>` label).
  - **Impact**: If `FindLatestIssueByTitleAndAssignee` or the label `Update` fails (Dolt blip), the mail-copy ships unlabeled; ack/close can then never locate it, so the `gu-fag41` mark-read fix silently no-ops and the stop hook keeps re-firing "unread message" until a backstop reaper sweeps it. There is no fallback match by `ThreadID` (which *is* set to `issue.ID` at `escalate_impl.go:226`).
  - **Suggested fix**: Add a `thread:<escalationID>` fallback match in `MarkEscalationMailCopiesRead`, or set the `escalation:<id>` label at create time in the router rather than annotating after the fact.
  - **Fingerprint**: `axis:e2e|internal/cmd/escalate_impl.go|annotation-best-effort-no-fallback`

- **Title**: Escalation terminal-state guarantee depends on an LLM-driven reaper dog, not a deterministic ticker
  - **Location**: `internal/reaper/stale_escalation.go:209-238` (`ReapStaleEscalations` 72h backstop) wired only into `internal/cmd/reaper.go:1444` (`reaper run`) and `:738`; driven by the `mol-dog-reaper` formula. Contrast `internal/daemon/escalate_stale_dog.go` + `patrol_tickers.go:108` — a hard-coded daemon ticker that only *bumps severity*, never closes.
  - **Impact**: The "every escalation reaches terminal state" guarantee (`gu-tn0xp`) holds only while the reaper dog is alive and chooses to run the close step. If the dog is wedged/disabled or the LLM skips the step, nothing else closes a never-acked escalation — `escalate_stale_dog` only escalates severity. The loop is one disabled patrol away from the original "open forever" failure.
  - **Suggested fix**: Mirror `escalate_stale_dog` with a deterministic in-process ticker (or non-LLM scheduled call) that invokes `gt reaper reap-stale-escalations` directly.
  - **Fingerprint**: `axis:e2e|internal/reaper/stale_escalation.go|backstop-depends-on-llm-dog`

- **Title**: Mountain failure count double-counts the same stranded leg across patrol cycles
  - **Location**: `internal/witness/handlers.go:2202-2298` (`DetectZombiePolecats` scans every cycle, calls `trackConvoyFailures` at `:2295`) → `internal/witness/mountain.go:127-148` (`trackMountainFailure` increments per call, no per-event dedup).
  - **Impact**: A dead polecat with an active hook is detected as `ZombieSessionDeadActive` and restarted while its hook bead stays in_progress. Next patrol cycle re-detects the same hook bead and increments `mountain:failures:N` again. The only cooldown (`polecat_died_cooldown.go`) gates the *notification*, not the increment. A single failing leg can race to the 3-strike auto-skip in three patrol cycles regardless of distinct dispatch attempts — retiring a leg that may not have had 3 real attempts.
  - **Suggested fix**: Key the increment on a distinct failure event (dispatch/attempt id or polecat instance + session epoch); skip re-counting a hook bead whose failure was already recorded for the current assignee.
  - **Fingerprint**: `axis:e2e|internal/witness/mountain.go|trackMountainFailure-per-cycle-recount`

- **Title**: Mountain failure-count is a non-atomic read-modify-write on a label (lost updates / orphan labels)
  - **Location**: `internal/witness/mountain.go:128-137` (read `getMountainFailureCount`, compute, write) and `:213-222` (`updateMountainFailureCount` removes `:oldCount`, adds `:newCount`); `getMountainFailureCount` (`:202-211`) returns the FIRST matching `mountain:failures:N` label.
  - **Impact**: Two concurrent witnesses/passes read the same `oldCount` and both write the same `newCount` (lost update); the remove/add pair can leave multiple `mountain:failures:N` labels, after which the count is read non-deterministically and corrupts permanently. Combined with the prior finding, the skip threshold is unreliable — a failing leg never retires (convoy hangs on it) or a salvageable leg retires too early.
  - **Suggested fix**: Store the count in a single typed field (not a label) with a conditional/atomic update; on read, take the max of all `mountain:failures:N` labels and collapse duplicates.
  - **Fingerprint**: `axis:e2e|internal/witness/mountain.go|updateMountainFailureCount-nonatomic-rmw`

- **Title**: Convoy completion notification stamp is written before the notification is sent — drop is silent and permanent
  - **Location**: `internal/cmd/convoy.go:3042-3050` (stamp `CompletionNotifiedAt` and persist) executes BEFORE `:3078-3110` (best-effort `gt mail`/`gt nudge`, failures only `PrintWarning`). Reopen recovery (`gu-kawd`) at `internal/convoy/operations.go:113-161` clears the stamp but depends on catching the reopen event (skipped for parked rigs; missed during a Dolt outage past the lookback window).
  - **Impact**: If the mail/nudge subprocess fails, the stamp stays set and `notifyConvoyCompletion` returns early on every subsequent call — the "🚚 Convoy landed" notification to owner/notify/mayor is silently lost with no retry.
  - **Suggested fix**: Set `CompletionNotifiedAt` only after at least the mayor/owner notification succeeds; or record per-address delivery and retry unsent addresses on the next completion check.
  - **Fingerprint**: `axis:e2e|internal/cmd/convoy.go|notifyConvoyCompletion-stamp-before-send`

- **Title**: `awaiting_refinery_merge` label is not durability-forced (no DoltAutoCommit) unlike the MR Create
  - **Location**: `internal/polecat/completion/refinery_guard.go:144` (`bd.Update` with no DoltAutoCommit) vs `internal/cmd/done.go:1499-1512` / `internal/cmd/mq_submit.go:435` where MR Create forces `DoltAutoCommit:"on"` (the `gs-onu` defense); `UpdateOptions` has no DoltAutoCommit field (`internal/beads/beads.go:2139` vs `CreateOptions` at `:732`).
  - **Impact**: This label is the sole durable artifact the git-evidence recovery path (`ReconcileMergedOrphansByGitEvidence`) keys on after `gt reaper purge` destroys the MR wisp and clears `active_mr`. The polecat usually self-terminates immediately after writing it (`done.go:1284-1298`). If the write lands only in the session's local Dolt working set and the session dies before commit, both recovery anchors can be lost → merged-but-unhooked bead strands HOOKED. Mitigated (not closed) by the rig default `dolt.auto-commit:"on"`; bites only when that config drifts off — the exact scenario `gs-onu` hardened the MR Create against.
  - **Suggested fix**: Add a `DoltAutoCommit` field to `UpdateOptions` and force `"on"` in `MarkAwaitingRefineryMerge`, matching the MR Create.
  - **Fingerprint**: `axis:e2e|internal/polecat/completion/refinery_guard.go|MarkAwaitingRefineryMerge-no-autocommit`

- **Title**: `gt escalate` is not idempotent without `--signature`/`--fingerprint` — re-runs double-create
  - **Location**: `internal/cmd/escalate_impl.go:103-208` (dedup gated on `--dedup`+`--signature`, or `--fingerprint`, or the narrow dolt-saturation auto-signature) → fall-through to `:205` `CreateEscalationBead`.
  - **Impact**: A bare `gt escalate "..." --severity high` re-run by an automated retry loop creates a new escalation bead every time — no title/source coalescing. The dolt-saturation special-case (`gu-s2l9t`) exists because this generic gap already bit once; other recurring free-form escalations remain exposed and can storm the HQ wisp table.
  - **Suggested fix**: When no signature/fingerprint is supplied, derive a default coalescing key from `(source, related-bead)` or `(normalized-title, source)` and apply the same open-match bump, or at minimum warn the caller.
  - **Fingerprint**: `axis:e2e|internal/cmd/escalate_impl.go|no-default-idempotency-key`

- **Title**: `gt done` idle-sync deletes the local feature branch while the MR still awaits refinery merge
  - **Location**: `internal/cmd/done.go:1248-1254` (`DeleteBranch(oldBranch)` in idle sync) gated by `shouldSyncIdlePolecatWorktree` (`:432-437`), which does NOT consult `awaitingRefineryMerge`.
  - **Impact**: The local delete itself is benign (refinery uses `origin/<branch>` + the shared object DB). But if the refinery later fails the merge and nudges the polecat to "fix and resubmit" (`engineer.go:2288`), the self-terminated polecat's local branch ref is gone; the resubmit-nudge path is effectively dead for self-terminating polecats and recovery falls to witness/mayor. Not a hard strand (origin branch persists), but the documented resubmit loop doesn't actually function.
  - **Suggested fix**: Skip the local old-branch delete when the MR is awaiting refinery merge, or document that MERGE_FAILED resubmit relies on witness recovery rather than the dead polecat.
  - **Fingerprint**: `axis:e2e|internal/cmd/done.go|idle-sync-deletes-branch-while-awaiting-merge`

- **Title**: Re-escalation re-mails on every dog tick with no per-bead cooldown (notification amplifier)
  - **Location**: `internal/cmd/escalate_impl.go:626-664` (`runEscalateStale` re-mail loop) + `internal/beads/beads_escalation.go:699-727` (`ListStaleEscalations` keys only on `createdAt` vs threshold + not-acked, no `last_reescalated_at` gate); `internal/daemon/escalate_stale_dog.go:38-49` (cadence).
  - **Impact**: With dog interval == stale_threshold (both default 4h), a never-acked escalation generates a fresh urgent mail-copy on each severity bump up to critical, each itself an open bead the recipient must process. Not a strand, but inflates the very mail backlog the recipient needs to clear to ack.
  - **Suggested fix**: Gate `ListStaleEscalations` (or the re-mail loop) on `now - LastReescalatedAt >= threshold`, or re-use the existing mail as a thread reply rather than a new bead.
  - **Fingerprint**: `axis:e2e|internal/cmd/escalate_impl.go|reescalate-no-cooldown-remail`

- **Title**: Cross-rig fan-in blocking edges are best-effort — a missing edge yields premature rollup
  - **Location**: `internal/cmd/convoy_stage.go:916-925` (leg→capstone `blocks` edge add: "Cross-rig deps may fail … Non-fatal", warning only).
  - **Impact**: For convoys that DO get a validation/capstone wave, a cross-rig leg whose blocking edge fails to add does not block the capstone; the capstone becomes ready and dispatches before that leg finishes. The rolled-up result silently omits that leg.
  - **Suggested fix**: Treat a failed blocking-edge add as fatal for the stage, or record the leg as an unsatisfiable blocker and refuse to mark the capstone ready until all intended edges exist.
  - **Fingerprint**: `axis:e2e|internal/cmd/convoy_stage.go|appendValidationWave-besteffort-blocks-edge`

## Minor Findings (P2 — informational)

- **Title**: `TriggerSynthesisIfReady` is dead code (defined, documented "called by the witness", zero callers)
  - **Location**: `internal/cmd/synthesis.go:760-806` — confirmed no non-test reference anywhere in `internal/`/`cmd/`. The witness leg-close path (`operations.go CheckConvoysForIssue`) never triggers synthesis.
  - **Impact**: Low in practice — formula-driven convoys dispatch the synthesis leg via dependency edges, so synthesis still fires for them. But the function reads as a live auto-trigger and misleads future maintainers into assuming witness-driven auto-synthesis exists.
  - **Suggested fix**: Either wire it into the leg-close path or delete it and document synthesis dispatch as dependency-edge-driven.
  - **Fingerprint**: `axis:e2e|internal/cmd/synthesis.go|TriggerSynthesisIfReady-no-callers`

- **Title**: Escalation ack/close is a non-atomic update-then-close — failure leaves a half-closed "resolved" open bead
  - **Location**: `internal/beads/beads_escalation.go:510-544` (`CloseEscalation`: description-update then `bd close` are separate writes under one lock); CLI `internal/cmd/escalate_impl.go:533-542`.
  - **Impact**: If `bd close` fails after the description update, the bead carries `resolved`/`closed_by` but status stays open and `MarkEscalationMailCopiesRead` never runs — until the 72h backstop sweeps it. Transient, not a permanent strand.
  - **Suggested fix**: Close the issue before/atomically-with the resolved-label update, or have the reaper treat `resolved`-labeled open beads as immediate close candidates.
  - **Fingerprint**: `axis:e2e|internal/beads/beads_escalation.go|close-nonatomic-update-then-close`

- **Title**: `gt mountain` launch message advertises a Deacon audit that does not exist for mountains
  - **Location**: `internal/cmd/mountain.go:289` and `:384` ("Deacon will audit progress every ~10 minutes") vs `internal/deacon/*` — no `mountain`/convoy-rollup audit (redispatch is generic RECOVERED_BEAD handling).
  - **Impact**: Misleading operator expectation; the daemon ConvoyManager is the sole rollup driver, so there's no second auditor to catch a wedged mountain (compounds the Critical findings).
  - **Suggested fix**: Implement a deacon mountain audit, or correct the launch message to point at the ConvoyManager.
  - **Fingerprint**: `axis:e2e|internal/cmd/mountain.go|deacon-audit-claim-unimplemented`

- **Title**: Empty/0-tracked mountain auto-closes as "landed"
  - **Location**: `internal/daemon/convoy_manager.go:802-813` and `:1517-1520` (`closeEmptyConvoy` closes a 0-tracked convoy after grace); legs auto-untracked via `handleNonWorkBead`/`handleMissingBeadStrike` (`:1319-1407`).
  - **Impact**: A mountain whose legs are all auto-untracked becomes 0-tracked and closes through the completion-notify path — "completing" having shipped nothing, and an untracked-then-rediscovered leg can be counted again in another convoy.
  - **Suggested fix**: Distinguish "completed (all legs closed)" from "emptied (all legs untracked)" and suppress the landed-notification for the emptied case.
  - **Fingerprint**: `axis:e2e|internal/daemon/convoy_manager.go|closeEmptyConvoy-false-landed`

## Verified solid (named failure modes already closed — not findings)

The task brief explicitly called these out; I verified each is defended in-code so synthesis does not re-flag them:

- **done-push gate loop / exact-tip verify / detached-HEAD false "unpushed"**: `pushBranchWithFallbacks` (`done.go:1689`) has the full ladder — bare-repo fallback, mayor/rig fallback, orphan-commit recovery (`gu-0l56`), SHA-refspec for detached HEAD, `recoverPushFromOriginTip` (`gu-epv5`), `recoverNonFFOwnBranch` (`gu-hz3vx`), patch-identical adopt (`gs-y7g`); bounded retries (`pushForDoneMaxAttempts=3`). No strand loop.
- **post-merge unhook leaving bead HOOKED (non-atomic merge+unhook+close)**: fail-closed `verifyMergeLanded` ancestry guard on both entry points (`engineer.go:2003`, `manager.go:768`, shared `verifyMergeCommitLanded` at `manager.go:927`), the `gu-xner6` cleanup-skip-on-close-failure (`engineer.go:2099`), and the two-layer reaper reconcile.
- **merge_commit not recorded → refuses to close**: intentional fail-closed behavior (`manager.go:933`); the hand-merge gap is closed by `RecordMergeCommit` (`manager.go:696`, `gu-xs9na`).
- **Recovery predicate: COMPLETED+open-MR / squash-changed-SHA still hooked**: `AssessActiveMR` (`active_mr.go:48`) treats a proven-merged MR as satisfied without the GitSafe gate (`active_mr.go:100`, `gu-a0uc`); `mrMerged` column check (`active_mr.go:130`, `gu-pfge3`).
- **Idempotency — re-running done/submit double-creates MRs**: `submitToMergeQueue` (`done.go:1436`) and `runMqSubmit` (`mq_submit.go:414`) dedup by branch+SHA (`FindMRForBranchAndSHA`) and supersede stale MRs; checkpoint-resume re-verifies on fresh main.
- **Escalation strands-forever**: the `gu-tn0xp` 72h `ReapStaleEscalations` backstop is real and wired into `reaper run` (the residual risk is its LLM-dog dependency — see Major finding above).
- **Mountain wave-1 dispatch failure / interrupted launch**: `dispatchWave1` records `Success:false` without aborting (stranded scan re-feeds); `reapStaleStagedConvoys` re-opens staged convoys after 15min (re-open only, no double-dispatch); `skipMountainIssue` closes a dead leg with a reason `shippingNotExpected` recognizes so it doesn't re-block rollup.

## Counts
counts: critical=2 major=11 minor=4

# End-to-End Flow Axis

## Summary
The three core loops — Work (`sling→done→mq→refinery→land→close`), Escalation
(`escalate→Mayor→ack→close`), and Mountain (`mountain→stage→dispatch→feed→skip→audit→rollup→notify`)
— are each well unit-tested at the *stage* level but have **no end-to-end harness
anywhere**: no test drives any loop across its cross-stage seams, and exactly
those seams are where work strands. The dominant theme is **missing terminal
states + un-wired recovery**: a step succeeds locally but the closeout (unhook
source bead, close escalation, close convoy) is best-effort, ordered wrong, or
gated on a recovery mechanism that is never scheduled. Several "safety nets"
exist as code but are dead — `gt escalate stale` re-escalation is never invoked,
and a `mountain:skipped` leg permanently blocks its convoy from ever closing.

The loops work on the happy path. They strand on the failure path, and the
failure path is precisely what is untested. Recovery predicates also misclassify
two real states (worktree-gone-with-pending-MR; post-push-verify-flake-after-landing),
both of which cause duplicate work.

## Score
score: 0.45

## Critical Findings (P0 — file as beads, fix urgently)

- **Title**: Mountain skip blocks convoy rollup — convoy can NEVER close
  - **Location**: internal/witness/mountain.go:227-233 (`skipMountainIssue`) + internal/cmd/convoy.go:1404-1417 (`closeConvoyIfComplete`)
  - **Impact**: After 3 polecat failures the Mountain-Eater marks a leg `--status=blocked --add-label mountain:skipped` but never closes/tombstones it. `closeConvoyIfComplete` counts only `closed`/`tombstone` as complete (verified: switch falls to `default: allClosed=false`), so a blocked-skipped leg keeps the convoy open forever — no rollup, no completion notification. Nothing closes a `mountain:skipped` bead (stale_parks refuses it; orphan reaper only touches `hq-wf-*`). `gt mountain status` masks it ("all done except 1 skipped") while the convoy silently never lands.
  - **Suggested fix**: Treat `mountain:skipped` (status=blocked + label) as terminal-equivalent in `closeConvoyIfComplete`, OR have `skipMountainIssue` close the bead with a `skipped:` reason that `shippingNotExpected` recognizes.
  - **Fingerprint**: `e2e:mountain|internal/witness/mountain.go|skipMountainIssue-blocks-rollup`

- **Title**: `gt escalate stale` re-escalation is never invoked automatically
  - **Location**: internal/cmd/escalate_impl.go:556 (`runEscalateStale`); only wired to the CLI at internal/cmd/escalate.go:131 — no daemon/scheduler/cron/formula caller (verified: grep finds only CLI + beads primitives).
  - **Impact**: The entire stale-escalation re-routing mechanism (`stale_threshold`, `max_reescalations`, bump to `email:human`/`sms:human`) is dead unless a human runs it. An escalation created, mailed to the Mayor, then never acked (Mayor offline/crashed/missed mail) sits open forever at original severity. This is the load-bearing "escalation doesn't get orphaned" mechanism and it is unreachable.
  - **Suggested fix**: Register `runEscalateStale` on the daemon heartbeat / scheduled-maintenance loop at the `stale_threshold` cadence (or a patrol formula step). Add a test asserting it is wired into the tick.
  - **Fingerprint**: `e2e:escalation|internal/cmd/escalate_impl.go|runEscalateStale-never-scheduled`

- **Title**: No terminal-state reaper for open escalation beads
  - **Location**: internal/beads/beads_escalation.go:628 (`ListEscalations`); reaper internal/reaper/*.go only produces escalations, never closes abandoned ones.
  - **Impact**: With the above, there is no path guaranteeing an escalation reaches acked+closed. An escalation from an automated source whose condition self-heals stays open indefinitely; the only closer is a human running `gt escalate close`.
  - **Suggested fix**: Auto-close escalations whose dedup signature has cleared, or age open escalations in the reaper/`open_wisp_alert` sweep with a terminal disposition.
  - **Fingerprint**: `e2e:escalation|internal/reaper/reaper.go|no-terminal-state-reaper`

- **Title**: Refinery close ordering — MR bead closed before source bead, active_mr cleared after
  - **Location**: internal/refinery/engineer.go:1994 (MR-bead close) precedes :2017 (source close); :2036 clears `active_mr`. All best-effort with swallowed errors (verified).
  - **Impact**: A crash between :1994 and :2017 leaves MR bead `closed` + source bead OPEN/HOOKED. `ListReadyMRs` only picks OPEN MRs, so the closed MR never re-triggers `HandleMRInfoSuccess`; the source never closes via the refinery. Recovery depends on the reaper keyed on the agent bead's `active_mr` — but if the *close* merely failed (not crashed) the path continues to :2036 and clears active_mr anyway, blinding the reaper. Bead strands HOOKED with a leaked `awaiting_refinery_merge` label.
  - **Suggested fix**: Close source bead first (or same txn); on terminal close failure do NOT clear active_mr / delete branch / close MR — leave OPEN for the next scan. Add a refinery-startup sweep over closed-merged MR beads with non-terminal source issues, independent of active_mr.
  - **Fingerprint**: `e2e:work|engineer.go|HandleMRInfoSuccess-close-ordering`

- **Title**: merge_commit persisted inside HandleMRInfoSuccess, not right after push verify
  - **Location**: internal/refinery/engineer.go:1985-1988 records `merge_commit`; push+verify already happened in `doMerge` (~:1103/1120) with no SHA persistence.
  - **Impact**: Crash after push but before :1988 → MR bead never records `merge_commit`. The manual recovery path `Manager.PostMerge`→`verifyMergeCommitLanded` (manager.go:909-916) fail-closes on empty merge_commit, refusing post-merge close of a genuinely-landed merge until a human runs `RecordMergeCommit`. Merge landed; bookkeeping wedged with no automated exit.
  - **Suggested fix**: Persist `merge_commit` to the MR bead the instant `VerifyPushedCommit` succeeds in `doMerge`, before returning.
  - **Fingerprint**: `e2e:work|engineer.go|merge-commit-persist-after-push`

- **Title**: Worktree-gone orphan reset has no pending-MR guard → re-dispatches a bead whose MR is validly queued
  - **Location**: internal/witness/handlers.go:3850 (`resetAbandonedBead`; only landed-work guard is `verifyCommitOnMain`, handlers.go:1229), reached from `DetectOrphanedBeads` only after the polecat dir is gone (handlers.go:4105-4107) — so `verifyCommitOnMain` opens the deleted worktree and always errors, guard never fires.
  - **Impact**: A polecat that ran `gt done`, pushed, submitted an MR (sitting in queue), then had its worktree torn down → source bead is reset to `open` and re-slung. The MR still merges later → duplicate landing. The live-session zombie path correctly guards (handlers.go:2715 `hasPendingMRFromSnapshot`); the worktree-gone path has no `active_mr`/`awaiting_refinery_merge` check.
  - **Suggested fix**: Before reset, consult the bead's `active_mr` via `polecat.AssessActiveMR` (reader-only, no worktree). If Pending or (Stale && MRMerged), preserve/close-as-merged instead of resetting.
  - **Fingerprint**: `e2e:work|internal/witness/handlers.go|resetAbandonedBead-no-pendingMR-guard`

- **Title**: RECOVERED_BEAD redispatch is Deacon-only with no daemon fallback
  - **Location**: internal/witness/handlers.go:3918 (witness mails RECOVERED_BEAD); only consumer internal/cmd/deacon.go:1686 → internal/deacon/redispatch.go:218.
  - **Impact**: When a polecat dies mid-leg the Witness mails RECOVERED_BEAD to the Deacon; the bead is only re-slung when the Deacon patrol drains mail. No daemon-side consumer exists. If the Deacon is down/crash-looping/paused, recovered legs sit as unprocessed mail and are never re-dispatched. The escalate-to-Mayor backstop (redispatch.go:462) also only fires inside `Redispatch`, so it too never runs. Combined with mountain rollup running independently, the convoy can hang around an ownerless leg.
  - **Suggested fix**: Add a daemon-side fallback that consumes aged RECOVERED_BEAD mail (or detects recovered-but-un-redispatched beads) and re-slings or escalates, so recovery doesn't depend on a live Deacon.
  - **Fingerprint**: `e2e:mountain|internal/deacon/redispatch.go|recovered-bead-no-daemon-fallback`

- **Title**: Capped stuck-in-done escalates but never releases the bead → indefinite strand on a live wedged polecat
  - **Location**: internal/witness/stuckindone.go:23,99 (`MaxStuckInDoneAutoRestarts=2`); consumer handlers.go:2368-2377 (`ZombieStuckInDoneCapped`).
  - **Impact**: After 2 restarts the witness stops restarting (good — no infinite loop) but only sends an escalation mail; it does not unhook/reset the bead, clear done-intent, or kill the wedged session. Because the session is still alive, the dead-session orphan/stale paths never fire. The bead sits in_progress/HOOKED on a provably-wedged polecat until a human acts — an infinite loop converted to an indefinite strand.
  - **Suggested fix**: On `ZombieStuckInDoneCapped`, also kill the wedged session and route the bead through `resetAbandonedBead` (with the pending-MR guard above), or apply a TTL after which a live-but-capped polecat's bead is released.
  - **Fingerprint**: `e2e:work|internal/witness/stuckindone.go|capped-stuck-in-done-strands-bead`

- **Title**: Staged-but-never-launched convoy is invisible to both feed and rollup, with no reaper
  - **Location**: internal/convoy/operations.go:259-265 (`isConvoyStaged` skips feeding); internal/cmd/convoy.go:2721 (completion scan lists only `status="open"`); orphan reaper convoy.go:1195 handles only `gt:workflow` roots.
  - **Impact**: If `gt mountain` creates+stages a convoy but launch is interrupted before `transitionConvoyToOpen` (process death / error after `createStagedConvoy`, mountain.go:218), the convoy + tracking edges exist but work is orphaned: no dispatch, no rollup, no reaper. (Post-transition window self-heals via the daemon feed.)
  - **Suggested fix**: Add a staged-convoy staleness reaper (age-based), or make `gt mountain`/`gt convoy stage` perform create+label+transition atomically with cleanup-on-failure.
  - **Fingerprint**: `e2e:mountain|internal/cmd/convoy.go|staged-convoy-no-reaper`

## Major Findings (P1 — track, do not auto-bead)

- **Direct (non-PR) merge path has no already-merged guard → double-merge on reprocess.** internal/refinery/engineer.go:955-1142 unconditionally MergeSquash/MergeNoFF then pushes; contrast `doMergePR` which has the gs-4uz `IsAncestor` guard (engineer.go:1173-1208). If an MR is reprocessed after a successful push but a failed MR-bead close, the next scan re-merges → empty/duplicate squash on main. Fix: `IsAncestor(branchTip, origin/target)` check before merging in the direct path. Fingerprint: `e2e:work|engineer.go|doMerge-no-already-merged-guard`

- **Post-push-verify failure where the commit actually landed → stale push_failed + re-merge.** engineer.go:1120-1131: if `VerifyPushedCommit` errors after a successful Push, the code ResetHard-rolls-back-local and returns PushFailed:true even though origin/target already has the commit → re-merges already-landed work; flaky verify (replication lag) keeps firing. Fix: re-fetch and re-verify (HEAD ancestor of origin/target?) before classifying PushFailed. Fingerprint: `e2e:work|engineer.go|verify-after-push-stale-pushfailed`

- **Witness push_failed recovery ignores default-branch landing → re-pushes a merged-and-deleted fork branch.** internal/witness/push_failed_recovery.go:313-358 only compares HEAD to `origin/<branch>`, never `origin/<defaultBranch>`. After a squash-merge the fork branch is deleted (originTip==""), so it re-pushes the polecat's branch, resurrecting a merged branch → phantom MR. In-process attempt cap resets on every witness restart. Fix: before the originTip=="" fresh-push, check patch-equivalence/ancestry vs `origin/<defaultBranch>`. Fingerprint: `e2e:work|internal/witness/push_failed_recovery.go|fresh-push-ignores-default-branch-landing`

- **Mountain failure count double-counts per patrol cycle → premature skip.** internal/witness/mountain.go:128-132 read-modify-write of `mountain:failures:N` with no event key; driver handlers.go:2295 runs over every zombie each patrol, and an un-restartable dead session is re-detected every cycle (handlers.go:2774). One failure can increment once per cycle, racing to the skip threshold (3) → premature skip → trips the convoy-never-closes critical. Fix: key the increment to a durable failure identity (dead session/molecule ID). Fingerprint: `e2e:mountain|internal/witness/mountain.go|trackMountainFailure-per-cycle-double-count`

- **`gt mountain <epic>` double-creates convoys on re-run — no overlap guard.** internal/cmd/mountain.go:218 calls `createStagedConvoy` unconditionally; contrast `runConvoyStage` (convoy_stage.go:276-297) which calls `findOverlappingConvoys`/`handleOverlappingConvoys`. Two runs mint two `hq-cv-*` convoys both tracking the same beads and both dispatching Wave 1 → double-dispatch. Fix: reuse the overlap guard in the epic path. Fingerprint: `e2e:mountain|internal/cmd/mountain.go|runMountain-no-overlap-dedup`

- **Re-dispatch double-fire across processes — rate limiter is in-process only.** internal/witness/redispatch_rate_limiter.go:32-33 is explicitly in-memory/single-process; the reset+dispatch in handlers.go:3912-3955 is not under the respawn flock and has no cross-process "already redispatched" guard. Witness patrol and the daemon heartbeat dog can each pass their own limiter and both reset+RECOVERED_BEAD the same bead → two polecats for one bead. Fix: gate reset+dispatch under the spawn flock; re-read bead status inside it. Fingerprint: `e2e:work|internal/witness/redispatch_rate_limiter.go|in-process-limiter-cross-process-double-dispatch`

- **`IsBeadOpen` fails OPEN on lookup error → a real blocker can be treated as closed and the MR merged.** internal/refinery/engineer.go:2527-2530 returns `(false, nil)` on `beads.Show` error ("fail open"), feeding `firstOpenBlocker`→MR readiness gate (:2819). A transient blocker-lookup error merges an MR while a real blocker is still open. Fix: fail CLOSED on blocker lookup errors. Fingerprint: `e2e:work|engineer.go|IsBeadOpen-fail-open`

- **`false_deferred` auto-close fires on any commit merely mentioning the bead ID.** internal/witness/false_deferred.go:177-210 close gate is `git log --grep=<beadID> --fixed-strings` returning any SHA — "follow-up to gu-X", "supersedes gu-X", a deferred-footer reference all satisfy it, then the bead is labeled `false-deferred-recovered:*` so dedup prevents re-detection → sticky false close silently losing deferred work. Fix: require conventional-commit subject `(<beadID>)` form, like the polecat done path. Fingerprint: `e2e:work|internal/witness/false_deferred.go|grep-substring-false-close`

- **Dedup bump ignores severity — a fresh CRITICAL only increments an open lower-severity escalation.** internal/cmd/escalate_impl.go:149-164: a deduped re-fire bumps `occurrence_count` on the existing bead and never re-routes at the higher severity, so the CRITICAL's `email:human`/`sms:human` actions never fire. Also (escalate_impl.go:103-110, 353-372) the auto Dolt-saturation signature can suppress a genuinely new incident within the 1h window. Fix: on a higher-severity deduped re-fire, escalate severity and re-run routing for the delta; exempt critical from window suppression. Fingerprint: `e2e:escalation|internal/cmd/escalate_impl.go|dedup-bump-ignores-severity`

- **Mail-send failure is non-fatal and exits 0 — escalation can reach no human channel while reporting success.** internal/cmd/escalate_impl.go:241-246 + 826-837: per-target send failure only warns and continues; with default empty `Contacts{}` the human channels are skipped. If `mail:mayor` send fails and human channels are unconfigured, the bead exists but no notification reaches anyone, and the command exits 0 (only JSON mode surfaces partial_failure). A daemon caller checking exit code believes delivery succeeded. Fix: return non-zero when zero channels succeed for high/critical severity. Fingerprint: `e2e:escalation|internal/cmd/escalate_impl.go|mail-send-failure-silent-exit0`

- **No end-to-end harness for ANY loop.** No test imports both `internal/cmd` (done/mq) and `internal/refinery`; `runEscalateAck`/`runEscalateClose` are untested; the mountain skip→rollup interaction is never invoked in one test. The only true container e2e (`Dockerfile.e2e`, `e2e.yml`) covers install only. The strongest integration test is `internal/daemon/convoy_manager_integration_test.go` (real store, mocked `gt`). Every cross-stage seam failure above is catchable only in production. Fix: add per-loop integration harnesses with fault injection at the seams (push→close crash window; done→refinery handoff with worktree gone; mountain skip→rollup; escalate create→mayor-receive→ack→close). Fingerprint: `e2e:all|loop|no-cross-stage-e2e-harness`

- **Non-fast-forward push rejection is preserved but never auto-rebased → churn on a dead polecat.** internal/refinery/engineer.go:1103-1119 / batch.go:476-490 reset-hard + return PushFailed; `HandleMRInfoFailure` nudges the polecat. `OnConflict:"auto_rebase"` config (engineer.go:147) is never consulted in the push-failure path, so a dead/idle polecat's MR re-hits non-ff and re-nudges a corpse every cycle. Fix: bounded auto-rebase-onto-target + re-push capped by MaxRetryCount, then escalate. Fingerprint: `e2e:work|engineer.go|push-nonff-no-autorebase`

- **Orphaned MR (branch gone / source already closed) detected read-only but never auto-resolved.** Orphan detection (engineer.go:3052-3091) emits `orphaned-branch` with no action; `doMerge` returns BranchNotFound (engineer.go:792)→nudges the mayor but doesn't close the MR; `ListReadyMRs` never checks source-bead terminality. MR stays OPEN forever, re-nudging each poll. Fix: in `ListReadyMRs`, close MRs whose source bead is already terminal; on BranchNotFound with a terminal source, close as merged-out-of-band. Fingerprint: `e2e:work|engineer.go|orphan-mr-branchgone-churn`

## Minor Findings (P2 — informational)

- **Mountain "Deacon audits every ~10 min" output is documentation with no enforcing code.** internal/cmd/mountain.go:30,280,313,375 print the claim; zero `mountain` references in internal/deacon/. Rollup is daemon-driven and does not depend on a Deacon audit (correct/safe); the printed promise is just unimplemented. Fix: reword the output. Fingerprint: `e2e:mountain|internal/cmd/mountain.go|deacon-audit-claim-unenforced`

- **`isReadyIssue` (cmd stranded-scan path) has no `status=="blocked"` guard.** internal/cmd/convoy.go:2612-2702 — a `mountain:skipped` leg (status=blocked, no blocking dep edge) returns ready=true, so the stranded scan could re-feed a skipped leg. Primary daemon feed path (operations.go:466) is safe (gates on `status=="open"`). Fix: add `if t.Status=="blocked" { return false }`. Fingerprint: `e2e:mountain|internal/cmd/convoy.go|isReadyIssue-no-blocked-guard`

- **`escalate close` is two non-atomic bd ops.** internal/beads/beads_escalation.go:510-543 updates description + `resolved` label, then separately `bd close`. A crash between leaves a `resolved`-labeled but still-open bead that reappears in `ListEscalations`. Recoverable by re-running close. Fix: close first or fold into one op. Fingerprint: `e2e:escalation|internal/beads/beads_escalation.go|CloseEscalation-nonatomic`

- **`retry_count` increment not atomic with conflict-task creation.** internal/refinery/engineer.go:2242/2406 — on a bead-write failure the conflict task exists and the MR is blocked but retry_count doesn't advance, so MaxRetryCount escalation may never trigger. Fix: persist the increment in the same update as the conflict-task linkage. Fingerprint: `e2e:work|engineer.go|retrycount-nonatomic-with-conflict-task`

- **`stale_parks` unblock partial-failure window.** internal/witness/stale_parks.go:163 (dep-remove) and :181 (status flip) are separate calls; a crash between leaves edges removed but status still `blocked`, and the next cycle's `!hasExternalBlocker` check skips it → narrow strand. Fix: flip status open before removing edges. Fingerprint: `e2e:work|internal/witness/stale_parks.go|partial-unblock-strands-on-crash`

- **Escalation Mayor wakeup degrades to an ephemeral nudge (TTL ~2h).** internal/mail/router.go:1693-1835 — the durable mail-copy bead is the real backstop (not dropped), but timely surfacing depends on the nudge unless Mayor boot runs `gt mail inbox`/`gt escalate list`. Fix: guarantee Mayor prime re-surfaces un-acked escalations. Fingerprint: `e2e:escalation|internal/mail/router.go|escalation-wakeup-nudge-ttl`

## Counts
counts: critical=9 major=13 minor=6

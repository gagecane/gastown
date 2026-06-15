# Mountain Code Review — gastown_upstream — cv-ndy3m

## Dashboard
- Overall score: 0.60 (delta vs last: — first run, no prior baseline)
- Findings: 21 critical / 53 major / 28 minor (deduped; 23 raw criticals → 21 distinct after 2 cross-axis merges)
- Top theme: **Boundaries that fail silently (wrong result) instead of loudly (error)** — the daemon main loop blocks on synchronous I/O, cross-stage closeouts are best-effort/un-ordered so work strands HOOKED, and whole abandoned subsystems linger as live-looking traps.
- Recommendation: **act now** — 21 critical findings, several of which strand real work indefinitely with no automated recovery (mountain-skip rollup hang, proof-of-merge loss, escalation never closed).

## Per-Axis Scores
| Axis | Score | Critical | Major | Minor | Delta |
|---|---|---|---|---|---|
| performance | 0.62 | 2 | 6 | 3 | — |
| test-coverage | 0.78 | 1 | 7 | 6 | — |
| integration | 0.55 | 7 | 10 | 6 | — |
| e2e | 0.45 | 9 | 13 | 6 | — |
| duplication | 0.62 | 1 | 7 | 3 | — |
| dead-code | 0.55 | 3 | 10 | 4 | — |
| **mean / totals (raw)** | **0.60** | **23** | **53** | **28** | — |

Critical totals are pre-dedup per axis; the deduped distinct critical count is **21** (two findings were reported by two axes each — see Critical Findings below). Deltas are null: this is the first run under `.reviews/mountain/`, so there is no prior `summary.json` to diff against.

## Critical Findings (P0) — DEDUPLICATED

### Cross-axis (found by 2 axes each)

- **Persist merge_commit/close_reason atomically (proof-of-merge can be lost)** — `internal/refinery/engineer.go:1980-1999` _(axes: integration, e2e)_ — fingerprints `axis:integration|internal/refinery/engineer.go|HandleMRInfoSuccess-merge-commit-persist` + `e2e:work|engineer.go|merge-commit-persist-after-push`
  - Impact: merge_commit SHA + `close_reason: merged` are written to the bead *description* via a best-effort `beads.Update` (failure swallowed) before `CloseWithReason` writes only the column-level reason. `mrMerged()` reads *only* the description fields, so an `Update` failure (or a crash after push but before :1988) makes a genuinely-merged MR read `mrMerged()==false` → reaper skips it → source bead stranded HOOKED to a dead polecat forever (gu-7igu8 signature, un-recoverable).
  - Suggested fix: Persist merge_commit the instant `VerifyPushedCommit` succeeds in `doMerge`; treat the Update as a hard, fail-closed precondition for the close; and/or have `mrMerged()` also consult the column-level `CloseReason`/status.
  - Bead: `gu-pfge3` (newly filed)

- **Mountain skip blocks convoy rollup — convoy can never close** — `internal/witness/mountain.go:225-235` _(axes: integration, e2e)_ — fingerprints `axis:integration|internal/witness/mountain.go|skipMountainIssue-vs-synthesis-blocks` + `e2e:mountain|internal/witness/mountain.go|skipMountainIssue-blocks-rollup`
  - Impact: After 3 leg failures `skipMountainIssue` sets `status=blocked` + label `mountain:skipped` but never closes/tombstones the leg or drops its `blocks` edge. Synthesis dispatches only when every blocker is `closed`/`tombstone` (convoy.go:1804/1404-1417), so the skipped leg is a permanent non-closed blocker: rollup never runs, the convoy never auto-completes — the opposite of "skip and grind around it." Nothing else closes a `mountain:skipped` bead.
  - Suggested fix: Tombstone (or close with a `skipped:` reason) in `skipMountainIssue`, OR have the synthesis blocks-edge / convoy-completion logic treat a `mountain:skipped` blocker as satisfied. Both sides must agree skipped == terminal-for-rollup.
  - Bead: `gu-lcddy` (newly filed)

### Performance

- **mainBranchTest runs full 20-40min CI suite inline on the daemon select loop** — `internal/daemon/daemon.go:944-949` → `main_branch_test_runner.go:343` _(axes: performance)_ — fingerprint `axis:performance|internal/daemon/daemon.go|mainBranchTest-inline-on-select-loop`
  - Impact: `patrols.mainBranchTest` runs each rig's full quality-gate suite synchronously on the select goroutine; while it runs, no heartbeat, no signal handling, no dispatch — up to ~an hour across rigs.
  - Suggested fix: Kick `runMainBranchTests` onto a dedicated worker goroutine with an in-progress flag (mirror `AutoDispatchWatcher`); keep the global gate lock + per-gate timeout.
  - Bead: `gu-23xve` (newly filed)

- **`gt scheduler run` dispatch blocks the heartbeat up to 5 minutes** — `internal/daemon/daemon.go:4602-4624` (heartbeat phase 14 @ :1300) _(axes: performance)_ — fingerprint `axis:performance|internal/daemon/daemon.go|dispatchQueuedWork-5m-blocks-heartbeat`
  - Impact: Phase 14 blocks on `cmd.CombinedOutput()` with a 5m timeout synchronously inside `heartbeat()`; a slow/stuck scheduler stalls the whole heartbeat and all remaining phases for up to 5 minutes — on the exact dispatch hot path.
  - Suggested fix: Run dispatch on its own goroutine with a non-blocking in-flight guard (already self-serializes via `scheduler-dispatch.lock`); at minimum cut the inline timeout below the heartbeat interval.
  - Bead: `gu-qwn0n` (newly filed)

### Test Coverage

- **`autotestpr` integration test writes to production Dolt with no isolation/cleanup** — `internal/autotestpr/branch_gc_integration_test.go:259-266` _(axes: test-coverage)_ — fingerprint `axis:test-coverage|internal/autotestpr/branch_gc_integration_test.go|initBeadsDB-no-port-isolation`
  - Impact: `initBeadsDB` runs `bd init --prefix=…` with no `--server-port`, no `TestMain`, no `requireDoltServer`; bd auto-detects production Dolt on :3307 and creates real `beads_ret<N>` DBs with zero teardown → orphan-DB pollution `gt dolt cleanup` can't remove. The unfixed twin of the crew incident (gs-z76 / gu-4str3).
  - Suggested fix: Add a package `TestMain` (`EnsureDoltContainerForTestMain`/`requireDoltServer`), forward `--server --server-port $GT_DOLT_PORT`, add `t.Cleanup` dropping the `beads_ret*` DBs.
  - Bead: `gu-ke9l2` (newly filed)

### Integration

- **Thread the configured Dolt host through daemon reaper/curio/metrics scanners** — `internal/daemon/wisp_reaper.go:193,213,…` _(axes: integration)_ — fingerprint `axis:integration|internal/daemon/wisp_reaper.go|doltServerHost-hardcoded-127.0.0.1`
  - Impact: SILENT. Scanners hardcode `"127.0.0.1"` though the daemon honors a configurable Dolt host elsewhere; on a remote-Dolt town the reaper reaps nothing, gauges report 0, curio files nothing — no error. No `doltServerHost()` helper exists (only `doltServerPort()`).
  - Suggested fix: Add `doltServerHost()` returning `d.doltServer.config.Host` and thread it through every `DiscoverDatabases`/`OpenDB`/`curio.OpenStore`, as `jsonl_git_backup.go` already does.
  - Bead: `gu-mih6p` (newly filed)

- **Stop converting Dolt connection failure into a fixed one-database list** — `internal/reaper/reaper.go:74-118` _(axes: integration)_ — fingerprint `axis:integration|internal/reaper/reaper.go|DiscoverDatabases-error-as-default-list`
  - Impact: SILENT. `DiscoverDatabases` returns `["hq"]` on *any* error, so "connection refused" is indistinguishable from "only has hq"; the reaper proceeds against a phantom DB and skips every real rig. Compounds the hardcoded-host bug into total silent degradation.
  - Suggested fix: Return `([]string, error)`; only fall back to `DefaultDatabases` on the zero-rows path, log otherwise.
  - Bead: `gu-7c2if` (newly filed)

- **Apply the recovery path's merge-landed verification on the primary merge path too** — `internal/refinery/engineer.go:1960-2027` vs `manager.go:760-780` _(axes: integration)_ — fingerprint `axis:integration|internal/refinery/engineer.go|HandleMRInfoSuccess-missing-verifyMergeLanded`
  - Impact: The recovery path refuses to close beads unless `verifyMergeLanded` confirms ancestry; the primary automated path has no equivalent re-check before closing beads + deleting the branch — it trusts `result.Success`. The two reconcile entry points disagree on the close-time safety contract.
  - Suggested fix: Factor `verifyMergeCommitLanded` into a shared helper and call it in `HandleMRInfoSuccess` immediately before `CloseWithReason`.
  - Bead: `gu-mpgy8` (newly filed)

- **`gt assign` writes the bead to the HQ database, not the assignee's rig DB** — `internal/cmd/assign.go:124-127,147-150` _(axes: integration)_ — fingerprint `axis:integration|internal/cmd/assign.go|runAssign-create-Dir-townRoot`
  - Impact: SILENT wrong-DB. Creates/hooks with `.Dir(townRoot)` so the bead lands in hq while assigned to a rig crew member — invisible to the owning rig's agents (the exact bug `gt bead create` fixed). No test (`assign_test.go` absent).
  - Suggested fix: Resolve the rig `.beads` via `beads.ResolveRepoAliasBeadsDir` and pass `WithBeadsDir`/`Dir(rigDir)` for both create and hook.
  - Bead: `gu-swo07` (newly filed)

- **`gt bead move` creates dest and closes source against the wrong (cwd) database** — `internal/cmd/bead.go:184,196-198` _(axes: integration)_ — fingerprint `axis:integration|internal/cmd/bead.go|runBeadMove-create-close-no-routing`
  - Impact: SILENT wrong-DB. New bead created via `exec.Command("bd",…)` with no `Dir()`/`BEADS_DIR`, only `--prefix`; since bd dropped cross-rig routing in v0.62 the bead lands in the cwd rig's DB. The `bd close sourceID` is also unrouted. Can orphan the moved bead and leave the source open.
  - Suggested fix: Resolve the target prefix's rig dir via `beads.GetRigPathForPrefix` and pin `BEADS_DIR`/`Dir` for the create; route the close via `resolveBeadDir(sourceID)`.
  - Bead: `gu-sycrn` (newly filed)

### End-to-End Flow

- **Refinery close ordering — MR bead closed before source bead, active_mr cleared after** — `internal/refinery/engineer.go:1994/2017/2036` _(axes: e2e)_ — fingerprint `e2e:work|engineer.go|HandleMRInfoSuccess-close-ordering`
  - Impact: A crash between :1994 and :2017 leaves MR `closed` + source OPEN/HOOKED; `ListReadyMRs` only picks OPEN MRs so it never re-triggers. If the close merely failed (not crashed), execution still clears active_mr at :2036, blinding the reaper. Bead strands HOOKED with a leaked `awaiting_refinery_merge` label.
  - Suggested fix: Close source first (or same txn); on terminal close failure do NOT clear active_mr / delete branch / close MR. Add a refinery-startup sweep over closed-merged MRs with non-terminal sources.
  - Bead: `gu-xner6` (newly filed)

- **Worktree-gone orphan reset has no pending-MR guard → re-dispatches a queued-MR bead** — `internal/witness/handlers.go:3850` _(axes: e2e)_ — fingerprint `e2e:work|internal/witness/handlers.go|resetAbandonedBead-no-pendingMR-guard`
  - Impact: The only landed-work guard (`verifyCommitOnMain`) opens the deleted worktree and always errors, so a polecat that pushed + queued an MR then lost its worktree has its bead reset to open and re-slung → duplicate landing when the MR later merges.
  - Suggested fix: Before reset, consult `active_mr` via `polecat.AssessActiveMR` (reader-only); if Pending or (Stale && MRMerged), preserve/close-as-merged.
  - Bead: `gu-oeew4` (newly filed)

- **`gt escalate stale` re-escalation is never invoked automatically** — `internal/cmd/escalate_impl.go:556` _(axes: e2e)_ — fingerprint `e2e:escalation|internal/cmd/escalate_impl.go|runEscalateStale-never-scheduled`
  - Impact: The whole stale-escalation re-routing mechanism is dead unless a human runs the CLI; an un-acked escalation sits open forever at original severity. The load-bearing "escalation doesn't get orphaned" mechanism is unreachable.
  - Suggested fix: Register `runEscalateStale` on the daemon heartbeat / scheduled-maintenance loop at the `stale_threshold` cadence; add a test asserting it is wired in.
  - Bead: `gu-2sepi` (newly filed)

- **No terminal-state reaper for open escalation beads** — `internal/beads/beads_escalation.go:628` _(axes: e2e)_ — fingerprint `e2e:escalation|internal/reaper/reaper.go|no-terminal-state-reaper`
  - Impact: No path guarantees an escalation reaches acked+closed; a self-healing automated escalation stays open indefinitely, closeable only by a human.
  - Suggested fix: Auto-close escalations whose dedup signature cleared, or age open escalations in the reaper sweep with a terminal disposition.
  - Bead: `gu-tn0xp` (newly filed)

- **RECOVERED_BEAD redispatch is Deacon-only with no daemon fallback** — `internal/deacon/redispatch.go:218` _(axes: e2e)_ — fingerprint `e2e:mountain|internal/deacon/redispatch.go|recovered-bead-no-daemon-fallback`
  - Impact: A dead polecat's recovered leg is re-slung only when the Deacon drains mail; if the Deacon is down/paused the leg sits as unprocessed mail forever (the escalate-to-Mayor backstop also only fires inside `Redispatch`). The convoy hangs around an ownerless leg.
  - Suggested fix: Add a daemon-side fallback that consumes aged RECOVERED_BEAD mail (or detects recovered-but-un-redispatched beads) and re-slings/escalates.
  - Bead: `gu-jbcag` (newly filed)

- **Capped stuck-in-done escalates but never releases the bead → indefinite strand** — `internal/witness/stuckindone.go:23,99` _(axes: e2e)_ — fingerprint `e2e:work|internal/witness/stuckindone.go|capped-stuck-in-done-strands-bead`
  - Impact: After 2 restarts the witness only sends an escalation mail; it does not unhook/reset the bead or kill the wedged session, so dead-session orphan/stale paths never fire. An infinite loop converted to an indefinite strand on a provably-wedged polecat.
  - Suggested fix: On `ZombieStuckInDoneCapped`, also kill the session and route the bead through `resetAbandonedBead` (with the pending-MR guard), or apply a TTL.
  - Bead: `gu-lrfqn` (newly filed)

- **Staged-but-never-launched convoy is invisible to feed and rollup, with no reaper** — `internal/convoy/operations.go:259-265` _(axes: e2e)_ — fingerprint `e2e:mountain|internal/cmd/convoy.go|staged-convoy-no-reaper`
  - Impact: If launch is interrupted before `transitionConvoyToOpen`, the convoy + tracking edges exist but work is orphaned — no dispatch, no rollup, no reaper.
  - Suggested fix: Add an age-based staged-convoy staleness reaper, or make `gt mountain`/`gt convoy stage` create+label+transition atomically with cleanup-on-failure.
  - Bead: `gu-eqv21` (newly filed)

### Duplication

- **Extract shared `selectScheduleCandidates` for convoy/epic schedulers (already drifted)** — `internal/cmd/scheduler_convoy.go:63-104` ≈ `scheduler_epic.go:64-100` _(axes: duplication)_ — fingerprint `duplication:internal/cmd/scheduler_convoy.go|scheduler_epic.go|selectScheduleCandidates`
  - Impact: Identical ~40-line filter ladder; the `df7da9bf` fix for the gu-pi35l deferred-guard regression had to be applied to both copies separately (and missed epic-level selection first time). The canonical fix-one-miss-the-other failure — already happened once on this exact block.
  - Suggested fix: A shared `cmd` helper taking a small struct adapter so both convoy `tracked` and epic `children` share one ladder; new guards added once.
  - Bead: `gu-pnrmi` (newly filed)
  - _Note: this is the only duplication critical; it was filed because the leg classified it P0 (demonstrated correctness-adjacent drift). Per the severity gate, only Critical findings are beaded._

### Dead Code

- **Delete orphan ACP layer `internal/agent/provider` (~956 LOC, never imported)** — `internal/agent/provider/acp.go,provider.go` _(axes: dead-code)_ — fingerprint `axis:dead-code|internal/agent/provider|orphan-package`
  - Impact: A full second ACP implementation parallel to live `internal/acp`; no binary links it, no importer in any build tag, import path never in git history. A godoc-only commit (f1a900cc) makes it look maintained — a trap.
  - Suggested fix: `git rm -r internal/agent/provider`; confirm build + vet stay green.
  - Bead: `gu-jxgb6` (newly filed)

- **Delete orphan `internal/protocol` merge-message layer (~1,381 LOC, never imported)** — `internal/protocol/` _(axes: dead-code)_ — fingerprint `axis:dead-code|internal/protocol|orphan-package`
  - Impact: A complete merge-message protocol with witness+refinery handler registries; no importer ever. The single largest dead module; misleads anyone tracing refinery/witness comms (the live path is nudge/mail/beads).
  - Suggested fix: `git rm -r internal/protocol` after confirming zero importers (verified).
  - Bead: `gu-hmtn1` (newly filed)

- **Delete orphan `internal/connection` machine/address layer (~679 LOC, never imported)** — `internal/connection/` _(axes: dead-code)_ — fingerprint `axis:dead-code|internal/connection|orphan-package`
  - Impact: A distributed/remote-host addressing abstraction with no importer ever; Gas Town is single-host today — unimplemented-future / upstream-residue scaffolding masquerading as a live capability.
  - Suggested fix: `git rm -r internal/connection`.
  - Bead: `gu-b544i` (newly filed)

## Major Findings (P1)
_Tracked here only; not beaded (severity gate). Grouped by axis._

### performance (6)
- `git fetch --prune` per rig on every heartbeat is not cancelable — `Git.run`/`runRaw` build `exec.Command` with no context, so the per-rig 30s deadline can't cancel a hung fetch. `axis:performance|internal/git/git.go|runRaw-no-context-on-network-fetch`
- Pre-spawn `git fetch` + `git pull --rebase` (60s each) block the heartbeat during agent spawn (`lifecycle.go:652-700`). `axis:performance|internal/daemon/lifecycle.go|syncWorkspace-60s-fetch-pull-on-spawn`
- `gt-idle-check` subprocess runs with no timeout inside the Boot heartbeat phase (`daemon.go:1810-1813`). `axis:performance|internal/daemon/daemon.go|gt-idle-check-no-timeout-on-heartbeat`
- N+1: full rigs-config reload + two `bd show` subprocesses per bead on the dispatch validation path (`capacity_dispatch.go:518-519`). `axis:performance|internal/cmd/capacity_dispatch.go|per-bead-rigsconfig-reload-and-bd-show`
- Serial connect-per-database hooked-mail scan on every heartbeat (`hooked_beads_metrics.go:59-82`). `axis:performance|internal/daemon/hooked_beads_metrics.go|serial-connect-per-db-on-heartbeat`
- (6th major counted in axis total; see leg bead gu-leg-6dm22 notes for the full enumeration.)

### test-coverage (7)
- Permissive no-op test `TestManager_Queue_NoBeads` verifies nothing (`manager_test.go:106-120`). `axis:test-coverage|internal/refinery/manager_test.go|TestManager_Queue_NoBeads-noop`
- Permissive no-op test `TestManager_IsRunning_NoSession` (`manager_test.go:76-92`). `axis:test-coverage|internal/refinery/manager_test.go|TestManager_IsRunning_NoSession-permissive`
- Flaky sleep-then-assert-negative on Dolt recovery callbacks (`dolt_test.go:337,369`; `convoy_manager_test.go:2738`). `axis:test-coverage|internal/daemon/dolt_test.go|recovery-callback-sleep-negative`
- Flaky fixed 6s sleep waits for a real 5s async poll (`convoy_manager_integration_test.go:81,102`). `axis:test-coverage|internal/daemon/convoy_manager_integration_test.go|sleep6s-poll-wait`
- Flaky keepalive ticker cancellation race (`heartbeat_test.go:601-618`). `axis:test-coverage|internal/polecat/heartbeat_test.go|keepalive-cancel-race`
- Flaky real tmux/pipe/port tests paced by fixed sleeps (tmux/acp/proxy). `axis:test-coverage|internal/tmux,acp,proxy|fixed-sleep-for-async-io`
- Per-push CI excludes the entire integration tier (awareness, not a bug) — `ci.yml:122/178`, 41 integration files nightly-only. `axis:test-coverage|.github/workflows/ci.yml|integration-tier-nightly-only`

### integration (10)
- Daemon vs doltserver disagree on default Dolt port (3306 vs 3307). `axis:integration|internal/daemon/dolt.go|DefaultDoltServerConfig-port-3306-vs-3307`
- `resume_branch` formula var injected on direct sling path but dropped on rig/deferred path (`sling_dispatch.go:617-631`). `axis:integration|internal/cmd/sling_dispatch.go|executeSling-resume_branch-var`
- Non-atomic post-merge reconcile (close MR → close source → clear active_mr) can diverge. `axis:integration|internal/refinery/engineer.go|post-merge-reconcile-nonatomic`
- v2 `MRPhase` state machine defined but never written or read (dead contract). `axis:integration|internal/refinery/types.go|MRPhase-unused-statemachine`
- `gt synthesis` counts the synthesis bead itself as a leg (no production caller). `axis:integration|internal/cmd/synthesis.go|collectLegOutputs-includes-synthesis-bead`
- Documented `--preset`/`scope` axis selection is silently ignored (parsed-and-dropped). `axis:integration|internal/cmd/formula.go|preset-selection-ignored`
- `dolt_remotes.interval` is `time.Duration` while siblings use a duration string → unmarshal fails, disabling the entire patrol config. `axis:integration|internal/daemon/types.go|DoltRemotesConfig.Interval`
- Two divergent `DaemonPatrolConfig` structs over the same `mayor/daemon.json`. `axis:integration|internal/config/types.go|DaemonPatrolConfig-duplicate`
- Two divergent `RigConfig`/`BeadsConfig` structs over the same rig `config.json`. `axis:integration|internal/config/types.go|RigConfig-duplicate`
- `gt close` with mixed-prefix bead IDs routes every bead to the first bead's DB (`close.go:111-116`). `axis:integration|internal/cmd/close.go|runClose-beadIDs[0]-single-dir`

### e2e (13)
- Direct (non-PR) merge path has no already-merged guard → double-merge on reprocess. `e2e:work|engineer.go|doMerge-no-already-merged-guard`
- Post-push-verify failure where the commit actually landed → stale push_failed + re-merge. `e2e:work|engineer.go|verify-after-push-stale-pushfailed`
- Witness push_failed recovery ignores default-branch landing → re-pushes a merged-and-deleted fork branch. `e2e:work|internal/witness/push_failed_recovery.go|fresh-push-ignores-default-branch-landing`
- Mountain failure count double-counts per patrol cycle → premature skip. `e2e:mountain|internal/witness/mountain.go|trackMountainFailure-per-cycle-double-count`
- `gt mountain <epic>` double-creates convoys on re-run — no overlap guard. `e2e:mountain|internal/cmd/mountain.go|runMountain-no-overlap-dedup`
- Re-dispatch double-fire across processes — rate limiter is in-process only. `e2e:work|internal/witness/redispatch_rate_limiter.go|in-process-limiter-cross-process-double-dispatch`
- `IsBeadOpen` fails OPEN on lookup error → a real blocker can be treated as closed and the MR merged. `e2e:work|engineer.go|IsBeadOpen-fail-open`
- `false_deferred` auto-close fires on any commit merely mentioning the bead ID. `e2e:work|internal/witness/false_deferred.go|grep-substring-false-close`
- Dedup bump ignores severity — a fresh CRITICAL only increments an open lower-severity escalation. `e2e:escalation|internal/cmd/escalate_impl.go|dedup-bump-ignores-severity`
- Mail-send failure is non-fatal and exits 0 — escalation can reach no human channel while reporting success. `e2e:escalation|internal/cmd/escalate_impl.go|mail-send-failure-silent-exit0`
- No end-to-end harness for ANY loop. `e2e:all|loop|no-cross-stage-e2e-harness`
- Non-fast-forward push rejection preserved but never auto-rebased → churn on a dead polecat. `e2e:work|engineer.go|push-nonff-no-autorebase`
- Orphaned MR (branch gone / source already closed) detected read-only but never auto-resolved. `e2e:work|engineer.go|orphan-mr-branchgone-churn`

### duplication (7)
- Web API exec wrappers identical except binary name (`api.go` runBdCommand/runGhCommand/runGtCommand). `duplication:internal/web/api.go|runBdCommand+runGhCommand+runGtCommand|runCommand-helper`
- Deacon state load/save triplicated across three state files. `duplication:internal/deacon/redispatch.go|feed_stranded.go|stuck.go|Load+Save-State-helper`
- Polecat worktree provisioning block pasted across two Add paths (`manager.go:840-883` ≈ `1049-1098`). `duplication:internal/polecat/manager.go|addWithOptionsLocked+AddWithOptions|provisionWorktree`
- config loader `BuildStartupCommand` duplicates Windows-script + resolution body. `duplication:internal/config/loader.go|BuildStartupCommand|delegate-to-WithAgentOverride`
- pushlog receipt vs failure log readers duplicated. `duplication:internal/pushlog/pushlog.go|failure.go|readJSONL-helper`
- util/orphan zombie vs orphan SIGTERM/SIGKILL escalation state machine duplicated. `duplication:internal/util/orphan.go|CleanupZombie+CleanupOrphaned|escalateCleanup-helper`
- ci-watcher / pr-watcher rig-context resolver duplicated. `duplication:internal/cmd/ci_watcher.go|pr_watcher.go|resolveWatcherRigContext`

### dead-code (10)
- Orphan `internal/github` PR-client package (~421 LOC, never imported). `axis:dead-code|internal/github|orphan-package`
- Orphan `internal/agent` package (~62 LOC, never imported). `axis:dead-code|internal/agent|orphan-package`
- Orphan `internal/keepalive` package (~137 LOC, superseded by feed-based wake). `axis:dead-code|internal/keepalive|orphan-package`
- Orphan `internal/mq` ID generator (~58 LOC, never imported). `axis:dead-code|internal/mq|orphan-package`
- Orphan `internal/autotestpr` branch-GC/retention package (~618 LOC, never imported). `axis:dead-code|internal/autotestpr|orphan-package`
- Orphan `internal/testpathmap` package (~349 LOC, never imported). `axis:dead-code|internal/testpathmap|orphan-package`
- Dead `*beads.Beads` rig-bead method cluster (5 exported methods, zero refs). `axis:dead-code|internal/beads/beads_rig.go|dead-RigBead-method-cluster`
- Dead exported `FromWorkDir` wrappers in `internal/mayor/cleanup.go`. `axis:dead-code|internal/mayor/cleanup.go|FromWorkDir-wrappers-unused`
- Test-only exported `beads_rig.go` helpers (referenced solely by their own test). `axis:dead-code|internal/beads/beads_rig.go|test-only-rig-helpers`
- (plus the `internal/connection` orphan, beaded as critical above.)

## Minor Findings (P2)
_Informational; not beaded. Brief, grouped by axis._

- **performance (3):** preallocate filter result slices in the dispatch pipeline (`pipeline.go`); `bd list` re-queried per molecule during rig seeding (`manager.go:2090`); process-death poll goroutine has no hard exit if SIGKILL fails (`dolt.go:1205`).
- **test-coverage (6):** weak-error asserts in `TestManager_Status_NotRunning` and `TestManager_FindMR_NoBeads`; `TestScanMu_PreventsConcurrentScans` makes no serialization assertion; `internal/connection` registry lightly tested; `cmd/gt-proxy-client` + `testutil/doltcleanup` have no test; `restart_tracker_test.go` 10ms-margin sleep (no injectable clock).
- **integration (6):** `assertServedData` misclassifies an empty `issue_prefix` value as imposter; deferred dispatch drops `Owned`/convoy-strategy intent; `CloseReason` untyped string with duplicated `"merged"` literals; `GT_DISPATCH_*`/`GT_HEARTBEAT_PROFILE` bypass the env registry; `gt show` routing-dir can pick a flag value as bead ID; `flagValueFromArgs` treats a following flag token as the value.
- **e2e (6):** Mountain "Deacon audits every ~10 min" output is unenforced doc; `isReadyIssue` (stranded-scan) has no `status=="blocked"` guard; `escalate close` is two non-atomic bd ops; `retry_count` increment not atomic with conflict-task creation; `stale_parks` unblock partial-failure window; escalation Mayor wakeup degrades to an ephemeral nudge (TTL ~2h).
- **duplication (3):** mail batch per-message op handlers share a shape; doctor Run/Fix check pairs; misc cross-file 2-copy blocks flagged at threshold 100.
- **dead-code (4):** `PreCheckoutHookCheck` type alias is dead; `const ToolTypeFunction` (sole value of `ToolType`) is dead; `Deprecated:` markers with live callers (migration debt, not dead); `golangci-lint exported-is-used` no-op tooling note.

## Trend Notes
- First run — no prior `summary.json` to diff against, so all deltas are null. `summary.json` written this run becomes the baseline the next run reads for dedup + deltas.
- Verified-clean (checked, NOT findings, per the performance + dead-code legs): the historic convoy event-poll connection leak is remediated (stores reused + query timeout + dedicated `dolt_conn_leak_monitor.go`); all long-lived goroutine loops use `ctx.Done()`+`wg.Wait()`+`defer ticker.Stop()`; unexported-symbol hygiene is excellent (`golangci-lint unused` = 0 across 94 packages); recent history shows active dead-code pruning.

## Recommendations
1. **Fix the strand-forever class first** (highest user-visible blast radius): proof-of-merge persistence (`gu-pfge3`), refinery close ordering (`gu-xner6`), mountain-skip rollup hang (`gu-lcddy`), and worktree-gone re-dispatch guard (`gu-oeew4`). These four are how real work silently dies today.
2. **Unblock the daemon main loop**: move `mainBranchTest` (`gu-23xve`) and `gt scheduler run` dispatch (`gu-qwn0n`) off the select goroutine; plumb context into `Git.run` so network fetches honor deadlines (major `runRaw-no-context`).
3. **Close the escalation/recovery gaps**: schedule `runEscalateStale` and add a terminal-state escalation reaper (`gu-2sepi`, `gu-tn0xp`), plus a daemon-side RECOVERED_BEAD fallback (`gu-jbcag`) so recovery doesn't depend on a live Deacon.
4. **Stop silent wrong-DB writes & remote-Dolt blindness**: route `gt assign`/`gt bead move` (`gu-swo07`, `gu-sycrn`) and thread `doltServerHost()` through the scanners + fix `DiscoverDatabases` error coercion (`gu-mih6p`, `gu-7c2if`).
5. **Reduce future drift and reader-traps**: extract `selectScheduleCandidates` (`gu-pnrmi`) and delete the three orphan packages (`gu-jxgb6`, `gu-hmtn1`, `gu-b544i`, ~3,000 LOC) — confirm `go build`/`go vet` stay green after each removal.

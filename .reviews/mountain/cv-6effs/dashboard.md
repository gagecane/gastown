# Mountain Code Review — gastown_upstream — cv-6effs

## Dashboard
- Overall score: 0.65 (delta vs last: +0.05)
- Findings: 13 critical / 64 major / 45 minor (delta: C −8 / M +11 / m +17)
- Top theme: The fragile shared-Dolt data plane keeps surfacing the same class of bug — silent-wrong-result boundaries (hardcoded loopback hosts, lossy config round-trips), N+1 `bd`/Dolt subprocess fan-out on patrol hot paths, and copy-paste sibling functions that drift one side at a time.
- Recommendation: act now (13 new P0s filed; concentrated in witness patrol fan-out, daemon/Dolt config seams, and the mountain rollup loop)

## Per-Axis Scores
| Axis | Score | Critical | Major | Minor | Delta |
|---|---|---|---|---|---|
| performance | 0.62 | 3 | 10 | 5 | 0.00 |
| test-coverage | 0.80 | 0 | 9 | 6 | +0.02 |
| integration | 0.64 | 3 | 10 | 8 | +0.09 |
| e2e | 0.62 | 2 | 11 | 4 | +0.17 |
| duplication | 0.58 | 3 | 17 | 17 | −0.04 |
| dead-code | 0.62 | 2 | 7 | 5 | +0.07 |

`overall_score` is the unweighted mean of the six axis scores = (0.62+0.80+0.64+0.62+0.58+0.62)/6 = 0.6467 → 0.65.

## Critical Findings (P0) — DEDUPLICATED

All 13 criticals carry distinct fingerprints (no within-run cross-axis collisions). All are NEW vs the prior run (cv-ndy3m): the prior P0 set (daemon select-loop blockers, `autotestpr` test-port isolation, the `agent/provider`+`protocol`+`connection` orphan packages, the prior integration/e2e clusters) is reported fixed/deleted by the legs. The two dead-code orphans below were *major* last run (bead:null) and have escalated to critical this run — no prior bead existed, so new beads were filed.

### Performance (axis: gu-leg-3rwqg)

- **Bound serial per-polecat fan-out in witness zombie scan** — `internal/witness/handlers.go:2222` _(axes: performance)_ — fingerprint `axis:perf|internal/witness/handlers.go|DetectZombiePolecats-serial-fanout`
  - Impact: `DetectZombiePolecats` iterates every polecat serially, each with `HasSession` (tmux) + 1–2 `bd show` subprocesses (fresh Dolt connection each). O(N) serial round-trips per patrol tick; at swarm scale one slow call stalls detection for all polecats behind it.
  - Suggested fix: bounded worker pool (`errgroup` + `SetLimit(4-8)`), position-indexed results, timeout-wrapped tmux/bd calls.
  - Bead: `gu-ges77` (newly filed)

- **Collapse N+1 `bd` calls per zombie in convoy-failure tracking** — `internal/witness/mountain.go:39` _(axes: performance)_ — fingerprint `axis:perf|internal/witness/mountain.go|trackConvoyFailures-N+1-bd`
  - Impact: per zombie with a hook bead, 2–5 more serial subprocess+Dolt round-trips (`bd dep list`, `bd show`×N, `bd update`/`close`), stacked on the scan above. Multiplies Dolt pressure during mass-death incidents.
  - Suggested fix: batch convoy lookups (one query for tracking convoys, one for labels), reuse the already-fetched `agentBeadSnapshot`.
  - Bead: `gu-h7hc0` (newly filed)

- **Eliminate N+1 `bd show` per bead in orphan-molecule/orphan-bead scans** — `internal/witness/handlers.go:4306` & `:4153` _(axes: performance)_ — fingerprint `axis:perf|internal/witness/handlers.go|DetectOrphaned-N+1-bd`
  - Impact: after a bulk `bd list`, each dead-polecat bead is re-read individually (molecule id, molecule status, recursive descendant close). O(beads) serial Dolt round-trips per tick, on top of the two scans above in the same cycle.
  - Suggested fix: enrich the bulk `bd list --json` with description/labels; targeted `bd show` only for apparent orphans; batch molecule-status checks.
  - Bead: `gu-npwwt` (newly filed)

### Integration Contracts (axis: gu-leg-t4buu)

- **Thread `doltServerHost()` through the compactor scanner** — `internal/daemon/compactor_dog.go:592` _(axes: integration)_ — fingerprint `axis:integration|internal/daemon/compactor_dog.go|compactorOpenDB-hardcoded-127.0.0.1`
  - Impact: SILENT. Half-applied fix — the reaper/curio/metrics scanners use `doltServerHost()`, the compactor still hardcodes `127.0.0.1`. On a remote-Dolt town it compacts the wrong/local store while peers reach the real host; GC bloat accumulates unbounded with no obvious cause.
  - Suggested fix: replace the literal at line 592 with `d.doltServerHost()`.
  - Bead: `gu-cigbi` (newly filed)

- **Collapse two divergent `DaemonPatrolConfig` structs (silent field drop on save)** — `internal/config/types.go:679` vs `internal/daemon/types.go:218` _(axes: integration)_ — fingerprint `axis:integration|internal/config/types.go|DaemonPatrolConfig-duplicate`
  - Impact: SILENT, lossy. Saving `daemon.json` through the map-based struct drops every typed patrol block (`dolt_server`, `dolt_remotes`, `curio`, …). The codebase already hand-edits raw JSON in `AddRigToDaemonPatrols` to dodge this.
  - Suggested fix: single source of truth (re-export the typed struct), or make `config.SaveDaemonPatrolConfig` a read-modify-write over `map[string]json.RawMessage`.
  - Bead: `gu-bgqkj` (newly filed)

- **`DoltRemotesConfig.Interval` is `time.Duration`, not a duration string — wrong form nulls the entire patrol config** — `internal/daemon/types.go:163` _(axes: integration)_ — fingerprint `axis:integration|internal/daemon/types.go|DoltRemotesConfig.Interval`
  - Impact: the sibling-consistent `"interval":"15m"` fails `json.Unmarshal` and returns nil for the WHOLE `DaemonPatrolConfig`, silently disabling all patrol configuration. (A stderr warning was added, but the blast radius is unchanged.)
  - Suggested fix: rename to `IntervalStr string` + `time.ParseDuration`, matching the ~20 sibling patrols.
  - Bead: `gu-jlyvk` (newly filed)

### End-to-End Flow (axis: gu-leg-erhb4)

- **`convoy:ship-unverified` park has no daemon-driven recovery — permanently strands a mountain** — `internal/cmd/convoy.go:1599-1607` _(axes: e2e)_ — fingerprint `axis:e2e|internal/cmd/convoy.go|ship-unverified-terminal-no-daemon-recovery`
  - Impact: a closed-but-unshipped leg parks the convoy with `convoy:ship-unverified`; both daemon sweeps explicitly skip the label, and the only clear path (`removeShipUnverifiedLabel`) is reachable solely from a manual `gt convoy check <id>`. The mountain never rolls up or notifies until a human intervenes.
  - Suggested fix: daemon re-runs the single-convoy check for labeled convoys whose tracked beads advanced, or fold a re-evaluation pass into the completion backstop.
  - Bead: `gu-fnd1w` (newly filed)

- **`gt mountain <epic>` builds no fan-in capstone — ad-hoc mountains never roll up** — `internal/cmd/mountain.go:166` _(axes: e2e)_ — fingerprint `axis:e2e|internal/cmd/mountain.go|runMountain-no-validation-wave`
  - Impact: `runMountain` bypasses `runConvoyStage`/`appendValidationWave`, so an ad-hoc epic-promotion mountain produces no synthesis/rollup bead; "roll-up" degrades to a pure AND over leg statuses. (Does NOT affect formula-driven convoys like this review, which wire synthesis via dependency edges.)
  - Suggested fix: route `runMountain` through the same validation/synthesis-append path, or create+track a rollup bead blocked by all legs before launch.
  - Bead: `gu-4513b` (newly filed)

### Duplication (axis: gu-leg-obawe)

- **Consolidate `DoltServerManager` alert senders; migrate the inlined escalation copy** — `internal/daemon/dolt.go:640-741,1643-1676` _(axes: duplication)_ — fingerprint `duplication:daemon|internal/daemon/dolt.go|dolt-alert-dispatch-family`
  - Impact: 4-copy family already DRIFTED — the escalation alert inlines its own `exec.CommandContext` mail send instead of `sendDoltAlertMail`. Any delivery fix (retry/timeout/routing) silently skips the escalation path — the channel Dolt outages get noticed through.
  - Suggested fix: extract `dispatchAlert(subject, body)` for the waitgroup+goroutine+dual-send tail; migrate the escalation copy onto it.
  - Bead: `gu-3v5bh` (newly filed)

- **Unify scattered relative-time formatters (diverged on zero-time case)** — `internal/cmd/dog.go:915-943` vs `internal/cmd/trail.go:481-509` _(axes: duplication)_ — fingerprint `duplication:cmd|internal/cmd/dog.go+trail.go|relative-time-formatter`
  - Impact: byte-identical `switch time.Since(t)` block, already DRIFTED on the zero-value guard (`(unknown)` vs `""`); the formatter exists in 3-4 places so display fixes must be hand-applied N times.
  - Suggested fix: `HumanizeSince(t time.Time, zeroLabel string)` in `internal/util`; sweep `formatAge`/`formatProcessAge` in too.
  - Bead: `gu-av0e9` (newly filed)

- **Extract per-DB reaper close loop; restore consistent OpenDB error handling** — `internal/daemon/wisp_reaper.go:338-414` _(axes: duplication)_ — fingerprint `duplication:daemon|internal/daemon/wisp_reaper.go|per-db-close-step-loop`
  - Impact: three structurally-identical close-stale-across-DBs steps all SILENTLY `continue` on `OpenDB` failure while the adjacent Step-4 loop counts the same failure — open-failures in plugin-receipt/dispatch/hooked-mol reaps are invisible, masking Dolt-access problems during reaping.
  - Suggested fix: `closeStaleAcrossDBs(...)` helper taking a per-step closure; one consistent error-handling policy.
  - Bead: `gu-24imh` (newly filed)

### Dead Code (axis: gu-leg-4rxee)

- **Delete orphan `internal/autotestpr` branch-GC/retention package (~620 LOC, never reachable)** — `internal/autotestpr/` _(axes: dead-code)_ — fingerprint `axis:dead-code|internal/autotestpr|orphan-package`
  - Impact: a complete auto-test-PR branch garbage-collector + attachment-retention subsystem unreachable from any of the four binaries under any build tag; only its own tests reference it. Single largest dead module in the tree. (Was *major* last run; escalated.)
  - Suggested fix: confirm no in-flight auto-test-PR feature, then `git rm -r internal/autotestpr` (build/vet stay green).
  - Bead: `gu-1ifzi` (newly filed)

- **Delete orphan `internal/github` PR-client package (~421 LOC, never reachable)** — `internal/github/` _(axes: dead-code)_ — fingerprint `axis:dead-code|internal/github|orphan-package`
  - Impact: a parallel token-based GitHub REST+GraphQL client with zero importers; live GitHub access goes through `ciwatcher`/`prwatcher`/`pr_provider_github`/`git.go` (shelling to `gh`). Misleads anyone tracing how Gas Town talks to GitHub. (Was *major* last run; escalated.)
  - Suggested fix: confirm no in-flight migration to the typed client, then `git rm -r internal/github`.
  - Bead: `gu-d2p9n` (newly filed)

## Major Findings (P1)
Grouped by axis. Recorded in the dashboard only (not beaded, per anti-flood rule).

### performance (gu-leg-3rwqg) — 10
- Move heavy dogs off the daemon select loop (compactor, branch-sync, JSONL/Dolt backup, checkpoint) — `axis:perf|internal/daemon/daemon.go|select-loop-inline-blocking-dogs`
- Convert per-rig heartbeat/merge-queue-age scans to bounded-parallel off the select loop — `axis:perf|internal/daemon/agent_heartbeat_dog.go|serial-per-rig-scan`
- `processedLifecycleEvents` dedup map grows unbounded for daemon lifetime — `axis:perf|internal/daemon/convoy_manager.go|processedLifecycleEvents-unbounded`
- Batch the three reaper agent-bead scrubbers' per-bead `bd show` fan-out — `axis:perf|internal/reaper|three-scrubbers-per-agent-Show-N+1`
- N+1 `bd list` subprocess fan-out in the failure-classifier inner loop — `axis:perf|internal/daemon/failure_classifier_dog.go|classifierBeadExists-n-plus-1`
- Cap concurrent goroutines in the parallel pre-merge gate runner — `axis:perf|internal/refinery/engineer.go|runGatesForPhase-unbounded-gate-goroutines`
- Heavy per-completion git inspection runs inline in the witness handler — `axis:perf|internal/witness/handlers.go|slotOpenDecision-inline-git-per-completion`
- Batch channel-retention closes instead of one `bd close` per message — `axis:perf|internal/beads/beads_channel.go|PruneAllChannels-per-message-close`
- `ListDelegationsFrom` lists all issues then `bd show` per issue — `axis:perf|internal/beads/beads_delegation.go|ListDelegationsFrom-N+1-show`
- Molecule instantiation forks one `bd create` + one `bd dep add` per step/edge — `axis:perf|internal/beads/molecule.go|instantiate-per-step-subprocess`

### test-coverage (gu-leg-p66u4) — 9
- Theatre cluster: convoy tests `t.Skipf` on the empty result they should assert against — `axis:test-coverage|internal/convoy/operations_test.go|skip-on-empty-result-theatre`
- Permissive no-op test `TestManager_Queue_NoBeads` — `axis:test-coverage|internal/refinery/manager_test.go|TestManager_Queue_NoBeads-noop`
- Permissive no-op test `TestManager_IsRunning_NoSession` — `axis:test-coverage|internal/refinery/manager_test.go|TestManager_IsRunning_NoSession-permissive`
- Flaky: sleep-then-assert-negative on Dolt recovery callbacks — `axis:test-coverage|internal/daemon/dolt_test.go|recovery-callback-sleep-negative`
- Flaky: fixed 6s sleep waits for a real 5s async poll — `axis:test-coverage|internal/daemon/convoy_manager_integration_test.go|sleep6s-poll-wait`
- Flaky: keepalive ticker cancellation race — `axis:test-coverage|internal/polecat/heartbeat_test.go|keepalive-cancel-race`
- Flaky cluster: real tmux/pipe/port tests paced by fixed sleeps — `axis:test-coverage|internal/tmux,acp,proxy|fixed-sleep-for-async-io`
- 11 binary-gated test files run in per-push `-short` CI but skip silently (no integration safety net) — `axis:test-coverage|internal/{beads,cmd,deps,doctor,mail,upstreamsync}|untagged-skip-no-nightly-net`
- Per-push CI excludes the integration tier (awareness) — `axis:test-coverage|.github/workflows/ci.yml|integration-tier-nightly-only`

### integration (gu-leg-t4buu) — 10
- `doctor_dog` health scanner hardcodes `127.0.0.1` — `axis:integration|internal/daemon/doctor_dog.go|doctorDogDatabases-hardcoded-127.0.0.1`
- No port-zero normalization for a deserialized `DoltServer` config — `axis:integration|internal/daemon/daemon.go|DoltServer-config-port-zero-not-normalized`
- `resume_branch` formula var dropped on every deferred/batch/rig path — `axis:integration|internal/cmd/sling_dispatch.go|executeSling-resume_branch-var`
- `Owned` dropped in `ReconstructFromContext`/`DispatchParams` (deferred path stamps `convoy_owned=false`) — `axis:integration|internal/scheduler/capacity/pipeline.go|ReconstructFromContext-owned-omitted`
- Two divergent `RigConfig`/`BeadsConfig` structs over rig-root `config.json` — `axis:integration|internal/config/types.go|RigConfig-duplicate`
- `gt close` with mixed-prefix bead IDs routes every bead to the first bead's DB — `axis:integration|internal/cmd/close.go|runClose-beadIDs[0]-single-dir`
- `flagValueFromArgs` treats a following flag token as the value → silent wrong-DB on `gt bead create` — `axis:integration|internal/cmd/bead_create.go|flagValueFromArgs-next-flag`
- Synthesis bead tracked as a `tracks` dep and counted as a "leg" by `collectLegOutputs` — `axis:integration|internal/cmd/synthesis.go|collectLegOutputs-includes-synthesis-bead`
- Documented `--preset`/`scope` axis selection is silently ignored — `axis:integration|internal/cmd/formula.go|preset-selection-ignored`
- `CheckSynthesisReady`/`TriggerSynthesisIfReady` are dead — advertised auto-trigger does not exist — `axis:integration|internal/cmd/synthesis.go|TriggerSynthesisIfReady-dead`

### e2e (gu-leg-erhb4) — 11
- No end-to-end test exercises any full loop journey (work, escalation, mountain) — `axis:e2e|Dockerfile.e2e|no-full-journey-test`
- Escalation mail-copy label is best-effort post-send — no fallback match on failure — `axis:e2e|internal/cmd/escalate_impl.go|annotation-best-effort-no-fallback`
- Escalation terminal-state guarantee depends on an LLM-driven reaper dog, not a ticker — `axis:e2e|internal/reaper/stale_escalation.go|backstop-depends-on-llm-dog`
- Mountain failure count double-counts the same stranded leg across patrol cycles — `axis:e2e|internal/witness/mountain.go|trackMountainFailure-per-cycle-recount`
- Mountain failure-count is a non-atomic read-modify-write on a label (lost updates) — `axis:e2e|internal/witness/mountain.go|updateMountainFailureCount-nonatomic-rmw`
- Convoy completion notification stamp written before the notification is sent — `axis:e2e|internal/cmd/convoy.go|notifyConvoyCompletion-stamp-before-send`
- `awaiting_refinery_merge` label not durability-forced (no DoltAutoCommit) — `axis:e2e|internal/polecat/completion/refinery_guard.go|MarkAwaitingRefineryMerge-no-autocommit`
- `gt escalate` not idempotent without `--signature`/`--fingerprint` — re-runs double-create — `axis:e2e|internal/cmd/escalate_impl.go|no-default-idempotency-key`
- `gt done` idle-sync deletes the local feature branch while the MR still awaits merge — `axis:e2e|internal/cmd/done.go|idle-sync-deletes-branch-while-awaiting-merge`
- Re-escalation re-mails on every dog tick with no per-bead cooldown — `axis:e2e|internal/cmd/escalate_impl.go|reescalate-no-cooldown-remail`
- Cross-rig fan-in blocking edges are best-effort — a missing edge yields premature rollup — `axis:e2e|internal/cmd/convoy_stage.go|appendValidationWave-besteffort-blocks-edge`

### duplication (gu-leg-obawe) — 17
- `ProcessedRemain` vs `Remain` field split blocks two layers of reaper consolidation — `duplication:reaper|internal/reaper|processed-vs-stale-remain-field-split`
- `config/loader.go` tmux startup-command assembly cloned (~76 lines) — `duplication:config|internal/config/loader.go|startup-command-render`
- `util/orphan.go` graceful-kill escalation state machine cloned (~90 lines) — `duplication:util|internal/util/orphan.go|process-kill-escalation`
- `web/api.go` external-command runners cloned 3× — `duplication:web|internal/web/api.go|external-command-runner`
- deacon JSON state load/save trio — `duplication:deacon|internal/deacon|json-state-load-save`
- doctor `config_check.go` custom-types vs custom-statuses checks — `duplication:doctor|internal/doctor/config_check.go|beads-config-check`
- doctor witness/refinery structure checks — `duplication:doctor|internal/doctor/rig_check.go+claude_settings_check.go|witness-refinery-structure`
- molecule status step-categorization loop cloned — `duplication:cmd|internal/cmd/molecule_status.go|molecule-step-categorize`
- molecule await-event vs await-signal idle/backoff logic — `duplication:cmd|internal/cmd/molecule_await|idle-backoff-read+timeout`
- sling nudge helpers (`nudgeWitness`/`nudgeRefinery`) — `duplication:cmd|internal/cmd/sling_helpers.go|nudge-agent`
- daemon `bd list` by-status candidate gathering cloned — `duplication:daemon|internal/daemon/daemon.go+reap_dead_agent_wisps.go|bd-list-by-status`
- beads list-by-label + parse cloned (`ListChannelBeads`/`ListGroupBeads`) — `duplication:beads|internal/beads|list-by-label-parse`
- polecat worktree setup + clonePath duplication — `duplication:polecat|internal/polecat/manager.go|worktree-setup+clonepath`
- convoy vs epic sling dispatch loop cloned — `duplication:cmd|internal/cmd/scheduler_convoy.go+scheduler_epic.go|sling-dispatch-loop`
- witness restart-count trackers (`blanktools`/`stuckindone`) — `duplication:witness|internal/witness|restart-count-tracker`
- pushlog NDJSON readers cloned — `duplication:pushlog|internal/pushlog|ndjson-read-write`
- orphans process-kill loop cloned 3× — `duplication:cmd|internal/cmd/orphans.go|process-kill-loop`

### dead-code (gu-leg-4rxee) — 7
- Orphan `internal/testpathmap` package (~349 LOC) — `axis:dead-code|internal/testpathmap|orphan-package`
- Orphan `internal/keepalive` package (~137 LOC) — `axis:dead-code|internal/keepalive|orphan-package`
- Orphan `internal/agent` `StateManager[T]` package (~62 LOC) — `axis:dead-code|internal/agent|orphan-package`
- Orphan `internal/mq` ID generator (~58 LOC) — `axis:dead-code|internal/mq|orphan-package`
- Dead `*beads.Beads` rig-bead method cluster (5 exported methods) — `axis:dead-code|internal/beads/beads_rig.go|dead-RigBead-method-cluster`
- Dead exported `FromWorkDir` wrappers in `internal/mayor/cleanup.go` — `axis:dead-code|internal/mayor/cleanup.go|FromWorkDir-wrappers-unused`
- Test-only exported `beads_rig.go` helpers — `axis:dead-code|internal/beads/beads_rig.go|test-only-rig-helpers`

## Minor Findings (P2)
Brief, grouped by axis (informational only).

- **performance (5):** `GetHealthMetrics` multi-connect per poll; per-table `bd sql` schema probes; per-issue cross-rig blocker resolve in convoy feed; `fetchCrossRigBeadStatus` subprocess fallback; `LoadOperationalConfig` re-parse per scan; nudge coalesce timer 100ms idle wakeup.
- **test-coverage (6):** weak-error `Status_NotRunning`; weak-error `FindMR_NoBeads`; no-assertion `ScanMu_PreventsConcurrentScans`; two non-trivial source files with no test (`gt-proxy-client/main.go`, `testutil/doltcleanup`); no-injectable-clock `restart_tracker`; orphaned permanent skip `TestFormulaConvoyIDUsesTownConvoyPrefix`.
- **integration (8):** `DefaultDoltServerConfig` port 3306 vs 3307; `assertServedData` empty-vs-missing row; `gt show` value-flag bead-ID parse; dead `MRPhase` state machine; untyped `"merged"` close-reason literals; `mrMerged` MergeCommit overrides rejected; `SetMRFields` nil-parse field loss; unread `max_parallelism` formula key.
- **e2e (4):** `TriggerSynthesisIfReady` dead code; non-atomic escalation ack/close; `gt mountain` advertises nonexistent Deacon audit; empty/0-tracked mountain auto-closes as "landed".
- **duplication (17):** fingerprint-sibling CLI query; `renderErrors`/`renderWarnings`; escalate email/sms arms; `polecat_spawn` base-branch resolution; `prime_output` patrol-role startup; refinery enable/disable; doctor identity-dir scan; `doltserver` SHOW DATABASES retry; `ci_watcher`/`pr_watcher` rig-context; mail announce/channel render; `mail_inbox` in-file block; reaper wrapper twins; processed_mail wisp-vs-issue; `window_tint` config precedence; `branch_gc` meta-time parse; dog/trail/orphans age-formatter overlap (folded into C2); `web/handler.go` in-file block.
- **dead-code (5):** write-only struct fields (`lock.workerDir`, `wisp.townRoot`, `nudge` `townRoot`/`session`); dead `PreCheckoutHookCheck` alias; `internal/env` flagged NOT-dead (migration destination); 13 `Deprecated:` markers with live callers (migration debt); tooling note (reachability needs `go list`, not the linter).

## Trend Notes
- **Better since last run.** Overall +0.05 (0.60 → 0.65). The biggest gains are e2e (+0.17 — work/escalation loops now layered with push-fallback ladders, fail-closed merge-ancestry guards, the 72h escalation backstop, and the `skipMountainIssue` permanent-strand fix) and integration (+0.09 — refinery merge/reconcile is now fail-closed; `gt assign`/`gt bead move` pin the correct rig DB). Critical count dropped 21 → 13.
- **Resolved P0s (verified by legs).** Prior `autotestpr` production-Dolt test-port pollution (gu-ke9l2) fixed; the three large orphan packages `agent/provider`, `protocol`, `connection` deleted; the refinery merge-loss cluster and the two wrong-DB CLI paths hardened.
- **Worse / persistent.** Duplication slipped −0.04 (more sibling-clone families catalogued, several already drifted: the Dolt-alert escalation copy, the relative-time formatters, the wisp_reaper close-step error handling). Performance flat at 0.62 — the witness-patrol N+1 fan-out theme is unchanged and now carries this run's 3 new P0s.
- **New themes.** (1) Half-applied remote-Dolt host fix — `doltServerHost()` threaded through some scanners but missed in the compactor and doctor dogs. (2) The mountain rollup loop is the weakest journey: `ship-unverified` parks strand, ad-hoc mountains build no capstone, failure-count accounting is non-atomic. (3) Config-struct duplication (`DaemonPatrolConfig`, `RigConfig`) causes silent lossy round-trips.
- **Recurring unifying pattern (unchanged):** boundaries that fail *silently* (wrong result) rather than *loudly* (error) — hardcoded hosts, lossy config round-trips, dropped dispatch vars, and one-sided fixes to cloned code.

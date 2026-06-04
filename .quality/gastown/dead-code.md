# Dead Code Audit

## Summary

The gastown codebase (~596k LOC of Go across ~840 non-test files) is in
reasonable but not pristine shape with respect to dead code. Whole-program
reachability analysis (`golang.org/x/tools/cmd/deadcode -test ./...`, run from a
clean `go build ./...`) flags **167 unreachable non-test functions** (179 total;
12 are test-only mocks, 4 are godoc `Example*` functions that are not actually
dead). Because nearly all of this lives under `internal/`, where no
out-of-tree consumer can import it, an unreachable exported symbol here is
genuinely dead — not "public API kept for downstream users."

The defects cluster around **superseded subsystems that were never deleted after
their replacement landed**, rather than scattered one-off cruft. The dominant
theme is a **legacy witness mail-message handler dispatch path** in
`internal/witness/handlers.go` whose entry points (`HandleMerged`,
`HandlePolecatDone`, `HandleMergeFailed`, `HandleSwarmStart`, `HandleHelp`,
`HandleLifecycleShutdown`, …) are fully shadowed by the live
`internal/protocol` `DefaultWitnessHandler` implementation. Secondary clusters
are a dead terminal-paging/render helper module (`internal/ui/pager.go` +
9 `Render*` helpers in `internal/ui/styles.go`), a dead `autotestpr` rig-state
persistence subsystem, a dead structured-logging API in `internal/cmd/log.go`,
and duplicate free-function copies of `doltserver` worklog queries that the
`WLCommons` interface methods already cover. None of this is reachable, so all
of it can be removed in behavior-preserving PRs.

## Score

score: 0.58

## Critical Findings (P0 — file as beads, fix urgently)

- **Legacy witness message-handler dispatch path is fully dead but presents as
  live infrastructure.**
  - **Location**: `internal/witness/handlers.go` (5331 lines). Unreachable
    exported entry points: `HandlePolecatDone:172`, `TransitionPolecatToIdle:360`,
    `HandleLifecycleShutdown:468`, `HandleHelp:494`, `HandleMerged:520`,
    `HandleMergeFailed:574`, `HandleSwarmStart:606`, `EscalateRecoveryNeeded:1323`,
    plus their exclusive private helpers `isStalePolecatDone:449`,
    `handleMergedCleanupStatus:565`, `createSwarmWisp:671`, `getCleanupStatus:740`,
    `witnessActiveMRBlocker:1216`, and `SwarmWispLabels` in
    `internal/witness/protocol.go:585`.
  - **Impact**: These are exported handler functions named identically to the
    live ones (`HandleMerged`, `HandlePolecatDone`, …) that actually run in
    `internal/protocol/witness_handlers.go` (`DefaultWitnessHandler.HandleMerged`,
    etc.). A maintainer touching witness merge/cleanup/shutdown behavior can
    easily edit the dead copy and see no effect — a real correctness trap on a
    core control-plane path. Verified: the `internal/witness` package is imported
    widely (`internal/cmd/witness.go`, `start.go`, `rig.go`, …) but **none** of
    the message-handler entry points above have a non-test caller; the only
    external references to the names resolve to the separate `internal/protocol`
    interface implementation.
  - **Suggested fix**: Confirm `internal/protocol`'s `DefaultWitnessHandler` is
    the sole live dispatch path, then delete the dead handler functions and their
    now-orphaned private helpers from `handlers.go` / `protocol.go`. This is a
    several-hundred-line removal with no behavior change. Do it as one focused PR
    so the diff is auditable against the protocol-package equivalents.

## Major Findings (P1 — track but do not auto-bead)

- **`internal/ui/pager.go` is an entirely dead file.** All five functions are
  unreachable: `shouldUsePager:20`, `getPagerCommand:35`, `getTerminalHeight:47`,
  `contentHeight:61`, `ToPager:70`. The `internal/ui` package is imported, but no
  caller routes output through the pager. Delete the file.

- **Nine dead `Render*` helpers in `internal/ui/styles.go`.** Unreachable:
  `RenderBold:273`, `RenderSkipIcon:300`, `RenderInfoIcon:305`, `RenderID:317`,
  `RenderStatus:323`, `RenderStatusIcon:342`, `RenderPriority:364`,
  `RenderPriorityCompact:385`, `RenderType:405`. None has an external caller
  (verified by grep). Other symbols in the file remain live, so remove only these.

- **`autotestpr` rig-state persistence subsystem is dead.**
  `internal/autotestpr/rig_state.go`: `LoadRigState:170`, `EnsureRigStateBead:196`;
  `internal/autotestpr/rig_state_store.go`: `NewBeadsRigStateStore:26`,
  `BeadsRigStateStore.LoadRigState:34`, `.SaveRigState:44`, `.AppendTransition:56`.
  The whole store abstraction has no live constructor caller. Also in this package:
  `branch_gc.go` `AttachmentRetentionRunner.Run:415` (+ `.now:405`) and
  `ListBranchesForRig:220` are unreachable — an unwired retention runner.

- **Dead structured-logging API in `internal/cmd/log.go`.** `LogEvent:434`,
  `LogSpawn:456`, `LogWake:461`, `LogCrash:492`, `LogKill:497` are all
  unreachable — an event-logging facade that nothing calls.

- **Duplicate worklog query functions in `internal/doltserver/wl_charsheet.go`.**
  `QueryStampsForSubject:358`, `QueryBadges:404`, `QueryAllSubjects:435`,
  `UpsertLeaderboard:456` are dead free functions that duplicate the live
  `WLCommons` interface methods of the same names in `wl_commons.go` (which are
  used via the interface). Same shadowing hazard as the witness cluster, smaller
  scale. Delete the free-function copies.

- **Dead JSON (de)serialization + local provider in `internal/agent/provider/provider.go`.**
  `RequestToJSON:258`, `ResponseToJSON:262`, `ResponseFromJSON:266`,
  `RequestFromJSON:270`, plus `LocalProvider.CreateMessage:174` and
  `LocalProvider.Close:178`. An unused provider implementation and its codecs.

- **Dead agent-bead-ID constructors in `internal/beads/agent_ids.go`.**
  `AgentBeadID:318`, `WitnessBeadID:328`, `RefineryBeadID:338`, `CrewBeadID:348`.

- **Dead mail router helpers in `internal/mail/router.go`.**
  `ResolveGroupAddress:605`, `ExpandListAddress:1318`, `IsRecipientMuted:2035`,
  and its private `isRecipientMuted:2045` — a group/list-expansion + mute path
  with no caller.

- **Dead `templates` provisioning API in `internal/templates/templates.go`.**
  `MessageNames:179`, `CreateMayorCLAUDEmd:188`, `ProvisionCommandsFor:254`,
  `HasCommands:264`, `HasCommandsFor:269`, `MissingCommandsFor:279`.

- **Dead `reaper` mail-scan entry points.** `internal/reaper/hooked_mail.go`
  `ScanHookedMail:78` and `ScanOpenMail:306`; also
  `internal/reaper/active_mr_scrub.go` `ScrubStaleActiveMRWithBackoff:206`.

- **Dead `upstreamsync` helpers.** `conflict.go` `ParseConflictMarkers:199`,
  `joinGitPath:231`; `state_bead.go` `EnsureStateBead:49`, `AppendAttempt:171`;
  `types.go` `StateBeadTitle:235`. The package is otherwise live (imported by
  `internal/cmd/upstream*.go`), so these are stranded helpers, not a dead package.

- **Dead `wasteland` spider detection.** `internal/wasteland/spider.go`
  `RunSpiderDetection:200` and `runDoltQuery:328`.

- **Dead synthesis triggers in `internal/cmd/synthesis.go`.**
  `CheckSynthesisReady:677` and `TriggerSynthesisIfReady:689` — relevant given
  this very audit feeds a synthesis step; confirm the live trigger path before
  removing.

- **Numerous dead store/registry constructors** (alt constructors that lost their
  caller): `internal/beads/store.go` `NewWithBeadsDirAndStore:43`,
  `internal/mail/store.go` `NewMailboxBeadsWithStore:34` /
  `NewMailboxWithBeadsDirAndStore:45`, `internal/connection/registry.go`
  `NewMachineRegistry:36` (+ `MachineRegistry.load:59`), `internal/beads/catalog.go`
  `LoadCatalog:50`. Plus dead daemon utilities `internal/daemon/dolt.go`
  `CountDoltServers:1515` / `StopAllDoltServers:1522`, and session lifecycle
  `internal/session/lifecycle.go` `StopSession:379` /
  `internal/session/town.go` `StopTownSessionWithCache:47`.

- **Dead deacon/refinery/manager surface**: `internal/deacon/feed_stranded.go`
  `PruneFeedStrandedState:363` / `getConvoyStatus:388`, `redispatch.go`
  `PruneRedispatchState:343`, `stuck.go` `LoadStuckConfig:43`;
  `internal/refinery/engineer.go` `Engineer.Config:559` / `ProcessMRInfo:1279`,
  `manager.go` `SetOutput:72` / `IsHealthy:98`, `types.go`
  `ValidatePhaseTransition:107`. Several `*.IsHealthy` / `*.IsActive` /
  `*.IsRunning` / `*.Stop` accessors across `crew`, `mayor`, `witness`, `dog`
  managers are also unreachable — likely an intended-but-unused health/lifecycle
  interface; worth a deliberate keep-or-cut decision rather than silent removal.

## Minor Findings (P2 — informational)

- **Single dead methods / helpers** (one-line cleanups): `Rule.String`
  (`internal/autotest/tautology/tautology.go:34`), `CycleBudget.Total`
  (`internal/autotest/sandbox/timeout.go:188`), `Config.ConfigPath`
  (`internal/wisp/config.go:52`), `Table.SetIndent` / `Table.SetHeaderSeparator`
  (`internal/style/table.go:48,54`), `ConnectionError.Error` / `.Unwrap`
  (`internal/connection/connection.go:157,161`), `Boot.DeaconDir`
  (`internal/boot/boot.go:248`), `Git.countCommitsAhead` /
  `unpushedFromExactRemoteBranch` (`internal/git/git.go:3083,3098`),
  `RecordPaneOutput` (`internal/telemetry/recorder.go:809`),
  `NotificationManager.ClearStaleSlots` (`internal/daemon/notification.go:239`),
  `redispatchLimiter.Reset` (`internal/witness/redispatch_rate_limiter.go:122`),
  `GetMachineID` / `RecordDoctorRun` (`internal/state/state.go:150,172`),
  `GetTownNameFromCwd` (`internal/workspace/find.go:178`),
  `BeaconPrimeInstruction` (`internal/runtime/runtime.go:362`),
  `TailEvents` (`internal/townlog/logger.go:344`), `DeactivateAgentLogging`
  (`internal/session/agent_logging_unix.go:90`), and the `plugin` accessors
  `Recorder.GetLastRun` / `CountRunsSince`, `Scanner.GetExecWrapper` /
  `ListPluginDirs`.

- **Dead `tui/feed` combined source**: `internal/tui/feed/events.go`
  `NewCombinedSource:576`, `CombinedSource.Events:624`, `.Close:629`,
  `FindBeadsDir:641`.

- **Dead config resolvers**: `internal/config` `DefaultAgentPreset`,
  `NewExampleAgentRegistry`, `GetACPArgs`, `ValidEffortLevels`,
  `ResolveAgentConfigByName`, `GetRuntimeCommandWithAgentOverride`,
  `DefaultOperationalConfig`, `ValidSeverities`.

- **Test-only unreachable (mocks/helpers, low value, leave unless touched)**:
  `MockAgent.GetMessages`, `mockBeads.Show`/`Close`,
  `mockBeadsForStep.Show`, `testDAG.ConditionalBlockedBy`/`WaitsFor`,
  `fired`, `nowFnGuard`.

- **Not dead — excluded from counts (deadcode false positives to be aware of)**:
  the four `Example*` functions (`ExampleTouchInWorkspace`, `ExampleRead`,
  `ExampleState_Age` in `internal/keepalive/keepalive_test.go`,
  `ExamplePrintWarning` in `internal/style/style_test.go`) are godoc testable
  examples executed by `go test` — keep them. Likewise, build-tagged stub files
  (`internal/quota/keychain_stub.go` `//go:build !darwin`,
  `internal/session/agent_logging_unix.go` `//go:build !windows`) report as
  unreachable on this build's platform but their counterparts may be live on
  others — verify per-platform before removing.

## Counts

  counts: critical=1 major=14 minor=4

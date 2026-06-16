# Duplication Axis

## Summary

The gastown tree (~320k LOC of non-test Go under `internal/` + `cmd/`) carries a
large, consistent body of **copy-paste sibling functions**: pairs (and several
triples / 4-copy families) where one concrete variant was cloned from another and
then specialized by a type, a label string, a config key, or a flag global. The
dominant theme is **"parallel entity" duplication** — the same algorithm
re-implemented once per bead-type / agent-role / reaper-target / state-file.
Examples span every major subsystem: deacon state loaders, reaper command
scaffolds, doctor checks, web command runners, dolt alert dispatch, polecat
worktree setup, witness restart trackers, and beads list/parse helpers.

Detection was via `golangci-lint`'s bundled `dupl` linter (no network access to
install standalone `dupl`) at token thresholds 150 and 100, then every non-test
candidate was read and confirmed by hand. The good news: most copies are still in
**lockstep** (no behavioral divergence yet), and several areas are already
half-factored behind generics (`reaperPerDBResults[T]`, `runProcessedMailReap`,
`runMailReapLoop`, shared `inferRigFromCwd`). The risk is structural: each clone is
a place a future one-sided fix silently skips its twins. A handful have **already
drifted** — the dolt escalation alert never migrated onto the extracted mail
helper, the `dog.go`/`trail.go` relative-time formatters diverged on the zero-time
return, and `wisp_reaper` step-loops dropped the `OpenDB`-error counting that the
adjacent step has. The single highest-leverage structural fix is normalizing the
`ProcessedRemain` vs `Remain` result-field split in `internal/reaper`, which
currently blocks clean consolidation of two whole reaper layers.

## Score

score: 0.58

## Critical Findings (P0 — file as beads, fix urgently)

### C1 — Dolt alert dispatch: escalation copy never migrated onto `sendDoltAlertMail` (4-copy family, drifted)
- **Title**: Consolidate `DoltServerManager` alert senders; migrate the inlined escalation copy
- **Location**: `internal/daemon/dolt.go:713-741` (`sendUnhealthyAlert`), `1643-1676` (`sendReadOnlyAlert`), `682-709` (`sendCrashAlert`), and the escalation alert at ~`640-677`
- **Impact**: Four methods share the override-guard + `alertWg.Add(1)` + goroutine +
  dual-send (`sendDoltAlertMail` → `sendDoltAlertToWitnesses`) shape. The escalation
  copy (~640-677) has **already drifted**: it inlines its own `exec.CommandContext`
  mail send instead of calling the extracted `sendDoltAlertMail`, and logs a success
  line the others don't. Any fix to alert delivery (retry, timeout, recipient
  routing) applied to the helper silently skips the escalation path — exactly the
  Dolt-fragility blast radius the town cares about (alerts are how Dolt outages get
  noticed).
- **Suggested fix**: Add `func (m *DoltServerManager) dispatchAlert(subject, body string)`
  in `internal/daemon` for the waitgroup+goroutine+dual-send tail. Each caller keeps
  its own `if m.xAlertFn != nil {...}` guard (signatures differ: `func(error)` vs
  `func(int)`), builds subject/body, then calls `dispatchAlert`. Migrate the
  escalation copy onto the same helper to kill the inline-mail drift.
- **Fingerprint**: `duplication:daemon|internal/daemon/dolt.go|dolt-alert-dispatch-family`

### C2 — Relative-time formatters diverged on the zero-time case (3-4 copies, behavioral drift)
- **Title**: Unify scattered "N minutes ago" formatters into one `HumanizeSince` helper
- **Location**: `internal/cmd/dog.go:915-943` (`dogFormatTimeAgo`), `internal/cmd/trail.go:481-509` (`relativeTime`); related: `internal/cmd/orphans.go:566` (`formatAge`), `~887` (`formatProcessAge`)
- **Impact**: `dogFormatTimeAgo` and `relativeTime` share a byte-identical
  `switch time.Since(t)` block (incl. singular/plural branches) but have **already
  drifted** on the zero-value guard: `dog.go` returns `"(unknown)"`, `trail.go`
  returns `""`. This is a genuine behavioral divergence, and the formatter exists in
  at least 3-4 places, so display fixes (e.g. adding "weeks"/"months", i18n) must be
  hand-applied N times.
- **Suggested fix**: `func HumanizeSince(t time.Time, zeroLabel string) string` in
  `internal/util` (or new `internal/timeutil`). `dog.go` → `HumanizeSince(t, "(unknown)")`,
  `trail.go` → `HumanizeSince(t, "")`. Sweep `formatAge`/`formatProcessAge` into it too.
- **Fingerprint**: `duplication:cmd|internal/cmd/dog.go+trail.go|relative-time-formatter`

### C3 — `wisp_reaper` per-DB close steps dropped the OpenDB-error counting its sibling step has (3-copy, drifted)
- **Title**: Extract per-DB reaper close loop; restore consistent OpenDB error handling
- **Location**: `internal/daemon/wisp_reaper.go:338-360`, `365-387`, `392-414` (steps 3b/3c/3d) vs the Step-4 auto-close loop at `~418+`
- **Impact**: Three consecutive "close stale X across all databases" steps are
  structurally identical (validate → open → schema-check → call reaper → accumulate
  → log). All three **silently `continue` on `OpenDB` failure**, while the adjacent
  Step-4 loop increments an `autoCloseErrors` counter on the same failure. So
  open-failures in the plugin-receipt / dispatch / hooked-mol reaps are invisible —
  an inconsistency that masks Dolt-access problems during reaping (the town's
  documented Dolt-fragility concern).
- **Suggested fix**: `func (d *Daemon) closeStaleAcrossDBs(databases []string, host string, port int, dryRun bool, label string, closeFn func(db *sql.DB, dbName string) (reaper.Result, error)) (totalClosed int)`
  in `internal/daemon`. Callers pass a closure binding the age + specific reaper fn.
  Collapses ~70 lines to ~3 calls and forces one consistent error-handling policy.
- **Fingerprint**: `duplication:daemon|internal/daemon/wisp_reaper.go|per-db-close-step-loop`

## Major Findings (P1 — track, do not auto-bead)

### M1 — `ProcessedRemain` vs `Remain` field split blocks two layers of reaper consolidation
- **Location**: `internal/cmd/reaper.go:602-692` vs `696-790` (~90 lines, RunE + report);
  `internal/reaper/mail_reap.go:247-264` vs `internal/reaper/stale_escalation.go:278-295` (wrapper)
- **Impact**: The processed-mail and stale-escalation reap paths are duplicated at
  two layers (cmd RunE/report + reaper wrapper). The one obstacle to a clean generic
  consolidation is that `ProcessedMailResult.ProcessedRemain` and
  `StaleEscalationResult.Remain` name the same concept differently, so the
  copy-pasted report/merge blocks can't share code without normalization.
- **Suggested fix**: Rename to a common field (or add a `Remain()`/`setRemain(int)`
  method) so both satisfy a `reapReportable` interface, then extract
  `renderReapResults[T reapReportable](...)` (cmd) and `runReap[R any](...)` (reaper).
- **Fingerprint**: `duplication:reaper|internal/reaper|processed-vs-stale-remain-field-split`

### M2 — `internal/config/loader.go` tmux startup-command assembly cloned (~76 lines)
- **Location**: `internal/config/loader.go:2408-2484` (`BuildStartupCommand`) vs `2682-2745` (`BuildStartupCommandWithAgentOverride` tail)
- **Impact**: The Windows `.ps1`-script-write (with inline fallback) + POSIX
  `exec env ...` quoting + `ExecWrapper` splice + prompt/no-prompt tail are duplicated.
  Second copy is comment-stripped. Any change to startup-command rendering (quoting,
  Windows path, wrapper) must hit both. The override-resolution logic *above* the
  block legitimately differs and must NOT be merged.
- **Suggested fix**: Extract `renderStartupCommand(resolvedEnv map[string]string, rc *RuntimeConfig, prompt string) string`; both callers delegate.
- **Fingerprint**: `duplication:config|internal/config/loader.go|startup-command-render`

### M3 — `internal/util/orphan.go` graceful-kill escalation state machine cloned (~90 lines)
- **Location**: `internal/util/orphan.go:770-861` (`CleanupZombieClaudeProcesses`) vs `871-971` (`CleanupOrphanedClaudeProcesses`)
- **Impact**: Identical SIGTERM→grace→SIGKILL→UNKILLABLE escalation incl. the TOCTOU
  `isProcessStillOrphaned` re-verification and `syscall.ESRCH` handling. Differs only
  in result/process types and load/save fn names. Positive: the TOCTOU guard is in
  both today — but this is precisely the high-risk-to-drift safety logic.
- **Suggested fix**: `cleanupProcessesWithEscalation[P,R any](find, pidOf, loadState, saveState, makeResult)`; both publics become ~6-line wrappers.
- **Fingerprint**: `duplication:util|internal/util/orphan.go|process-kill-escalation`

### M4 — `internal/web/api.go` external-command runners cloned 3× (semaphore + timeout + output merge)
- **Location**: `internal/web/api.go:1392-1433` (`runBdCommand`), `1672-1713` (`runGhCommand`), plus `runGtCommand` (same file)
- **Impact**: `context.WithTimeout` + `h.cmdSem` slot + `exec.CommandContext`
  (`Dir=h.workDir`, `Stdin=nil`) + stdout/stderr merge + deadline/error wrap, byte-identical
  except the binary name. A fix to the shared-semaphore contract or timeout handling
  must touch three copies.
- **Suggested fix**: `func (h *APIHandler) runCommand(ctx context.Context, timeout time.Duration, name string, args []string) (string, error)`; three one-line wrappers.
- **Fingerprint**: `duplication:web|internal/web/api.go|external-command-runner`

### M5 — deacon JSON state load/save trio (`feed_stranded`/`redispatch`/`stuck`)
- **Location**: `internal/deacon/feed_stranded.go:99-122` (`LoadFeedStrandedState`), `internal/deacon/redispatch.go:113-136` (`LoadRedispatchState`), `internal/deacon/stuck.go:90-114` (`LoadHealthCheckState`) — plus matching `Save*State` trio
- **Impact**: Identical "read JSON state, empty-struct-on-ENOENT, nil-map guard,
  `//nolint:gosec G304`" loader, 3 copies; save trio (`MkdirAll`/`MarshalIndent`/`WriteFile 0600`)
  also duplicated. A move to atomic writes / schema versioning / corrupt-file handling
  must be applied 3×.
- **Suggested fix**: Generic `loadJSONState[T any](stateFile, label string, newEmpty func() *T) (*T, error)` and `saveJSONState[T any](...)` in `internal/deacon`.
- **Fingerprint**: `duplication:deacon|internal/deacon|json-state-load-save`

### M6 — doctor `config_check.go` custom-types vs custom-statuses checks (two pairs in one file)
- **Location**: `internal/doctor/config_check.go:627-708` vs `774-849` (`Run`), `718-751` vs `852-887` (`Fix`)
- **Impact**: `CustomTypesCheck` and `CustomStatusesCheck` are line-for-line parallel
  (`bd config get`, `parseConfigOutput`, diff vs constants list, merge-and-`set`),
  differing only in config key + constants accessor + message strings. Highest
  in-file ROI (two pairs).
- **Suggested fix**: `beadsConfigSpec` struct + `runBeadsConfigCheck(ctx, name, spec)` and `fixBeadsConfig(beadsDir, key, required)` in `internal/doctor`.
- **Fingerprint**: `duplication:doctor|internal/doctor/config_check.go|beads-config-check`

### M7 — doctor witness/refinery structure checks (`rig_check.go` + `claude_settings_check.go`)
- **Location**: `internal/doctor/rig_check.go:441-494` vs `604-657` (`WitnessExistsCheck.Run` / `RefineryExistsCheck.Run`); `internal/doctor/claude_settings_check.go:297-345` vs `349-397`
- **Impact**: Witness/refinery twins differ only by the literal dir name and the
  `session.WitnessSessionName`/`RefinerySessionName` constructor. Structs already
  identical. Stale-settings detection (incl. the `getGitFileStatus` skip-guard) is
  duplicated.
- **Suggested fix**: `scanAgentDirStructure(rigPath, subdir string)` and
  `collectStaleAgentSettings(files, agentDir, agentType, rigName, sessionName)` in `internal/doctor`. Keep the genuinely-different crew branch separate.
- **Fingerprint**: `duplication:doctor|internal/doctor/rig_check.go+claude_settings_check.go|witness-refinery-structure`

### M8 — molecule status step-categorization loop cloned (~40 lines, identical)
- **Location**: `internal/cmd/molecule_status.go:219-257` vs `642-680`
- **Impact**: Two functions computing molecule progress share a byte-identical
  children-iteration loop (closed→Done / in_progress→InProgress / open→Ready-or-Blocked
  via `Dependencies`) plus the surrounding sort/percent code. Status-classification
  changes must hit both.
- **Suggested fix**: `categorizeMoleculeSteps(children, openStepsMap, closedIDs, progress *MoleculeProgress)` in `internal/cmd`.
- **Fingerprint**: `duplication:cmd|internal/cmd/molecule_status.go|molecule-step-categorize`

### M9 — molecule await-event vs await-signal idle/backoff logic (two block pairs, minor structural drift)
- **Location**: `internal/cmd/molecule_await_event.go:181-208` vs `molecule_await_signal.go:161-193` (agent-bead idle/backoff read); `:454-486` vs `:381-417` (overflow-safe backoff timeout)
- **Impact**: Same `getAgentLabels` + `ErrIDCollision` handling + idle/backoff parse,
  and the same overflow-capped `min(base*mult^idle, max)` loop, transplanted between
  two commands (duplicated because they read different `awaitEvent*`/`awaitSignal*`
  flag globals). Minor drift: await-event additionally gates `agentBeadUsable` on
  `findLocalBeadsDir()` success; the critical ID-collision guard is present in both.
- **Suggested fix**: `readAgentIdleState(agentBead, beadsDir string, quiet bool)` and `backoffTimeout(base, max string, mult, idleCycles int, simpleTimeout string)` in `internal/cmd`; pass flag values in.
- **Fingerprint**: `duplication:cmd|internal/cmd/molecule_await|idle-backoff-read+timeout`

### M10 — sling nudge helpers (`nudgeWitness`/`nudgeRefinery`)
- **Location**: `internal/cmd/sling_helpers.go:937-965` vs `972-1001`
- **Impact**: Identical `GT_TEST_NUDGE_LOG` test-hook + `EmitToTown` + `NudgeSession`
  scaffold; `EmitToTown` args differ by design (`POLECAT_DONE`/`source=polecat` vs
  `MQ_SUBMIT`/`source=sling`). Test-hook/nudge boilerplate should be shared.
- **Suggested fix**: `nudgeAgent(session, recipient, event, source, rigName, message string)` in `internal/cmd`; two thin wrappers.
- **Fingerprint**: `duplication:cmd|internal/cmd/sling_helpers.go|nudge-agent`

### M11 — daemon `bd list` by-status candidate gathering cloned (drifted comments / provenance)
- **Location**: `internal/daemon/daemon.go:3981-4009` vs `internal/daemon/reap_dead_agent_wisps.go:121-146`
- **Impact**: Same `bd list --status=<hooked|in_progress> --json` loop with stderr
  capture + status-stamp. The daemon.go copy carries the gu-vg97 "capture stderr"
  regression rationale; the agent-wisps copy logs the same format string without the
  provenance — a forked copy that risks losing the fix's context.
- **Suggested fix**: `listRigBeadsByStatus[T statusStamped](d *Daemon, rigName, logTag string) []T` in `internal/daemon`.
- **Fingerprint**: `duplication:daemon|internal/daemon/daemon.go+reap_dead_agent_wisps.go|bd-list-by-status`

### M12 — beads list-by-label + parse cloned (`ListChannelBeads`/`ListGroupBeads`); adjacent `Lookup*` already drifted
- **Location**: `internal/beads/beads_channel.go:340-360` vs `internal/beads/beads_group.go:321-341`
- **Impact**: Identical `bd list --label=gt:<x> --json --limit=0` → unmarshal → map-by-Name.
  Note the neighboring `LookupGroupByName` uses `errors.Is(err, ErrNotFound)` handling
  that `LookupChannelByName` lacks — adjacent **drift** worth flagging.
- **Suggested fix**: `listBeadsByLabel[T any](b *Beads, label string, parse func(string)*T, nameOf func(*T) string) (map[string]*T, error)` in `internal/beads`.
- **Fingerprint**: `duplication:beads|internal/beads|list-by-label-parse`

### M13 — polecat worktree setup + clonePath duplication
- **Location**: `internal/polecat/manager.go:924-964` vs `1130-1175` (resume/fresh worktree create); `manager.go:532-551` vs `session_manager.go:179-198` (`clonePath`, + `polecatDir` twins)
- **Impact**: The `RefExists`→`WorktreeAddFromRef` create sequence (with the long
  "default_branch not found" error) and the new-layout/legacy-`.git`-fallback path
  resolver are each duplicated. Worktree-layout changes must touch all copies.
- **Suggested fix**: `(m *Manager) setupWorktree(repoGit, clonePath, branchName, opts) (created bool, err error)` and free `clonePathForRig(rigPath, rigName, polecat string) string` in `internal/polecat`.
- **Fingerprint**: `duplication:polecat|internal/polecat/manager.go|worktree-setup+clonepath`

### M14 — convoy vs epic sling dispatch loop cloned (~31 lines)
- **Location**: `internal/cmd/scheduler_convoy.go:198-228` vs `internal/cmd/scheduler_epic.go:199-229`
- **Impact**: Same `slingMaxConcurrent`-capped dispatch loop + `executeSling` + 500ms
  inter-spawn Dolt-contention sleep + success tally. Differs by `CallerContext` and
  the `NoConvoy` comment. Concurrency/pacing fixes must hit both.
- **Suggested fix**: `dispatchCandidates(candidates, formula, opts, callerContext, townRoot) (successCount int, successfulRigs map[string]bool)` in `internal/cmd`.
- **Fingerprint**: `duplication:cmd|internal/cmd/scheduler_convoy.go+scheduler_epic.go|sling-dispatch-loop`

### M15 — witness restart-count trackers (`blanktools`/`stuckindone`)
- **Location**: `internal/witness/blanktools.go:152-176` (`RecordBlankToolsRestart`) vs `internal/witness/stuckindone.go:110-134` (`RecordStuckInDoneRestart`); plus the `Has*AutoRestarts` reader twins
- **Impact**: Identical mutex+flock load-modify-save restart counter, one per
  category. Locking-discipline or persistence fixes must hit both.
- **Suggested fix**: A `restartTracker{mu, stateFile, max}` type with `Record(workDir, beadID) int` / `Exceeded(...) bool` in `internal/witness`; instantiate per category.
- **Fingerprint**: `duplication:witness|internal/witness|restart-count-tracker`

### M16 — pushlog NDJSON readers cloned (`Read`/`ReadFailures`, + writer twins)
- **Location**: `internal/pushlog/pushlog.go:168-202` (`Read`) vs `internal/pushlog/failure.go:168-201` (`ReadFailures`)
- **Impact**: Identical NDJSON scan (1 MiB buffer, blank-skip, malformed-skip,
  `scanner.Err()`); differs only in record type. `Append`/`AppendFailure` and
  `LogOrWarn`/`LogFailureOrWarn` are parallel twins too.
- **Suggested fix**: `readNDJSON[T any](path, logNoun string) ([]T, error)` in `internal/pushlog`.
- **Fingerprint**: `duplication:pushlog|internal/pushlog|ndjson-read-write`

### M17 — orphans process-kill loop cloned 3× (`orphans.go`)
- **Location**: `internal/cmd/orphans.go:699-719`, `943-964`, `1016-1037`
- **Impact**: Same `os.FindProcess`→`Signal`→tally loop with `os.ErrProcessDone`
  special-case, 3 copies (plus 3 duplicated signal-selection blocks above). Low
  functional drift (one copy missing a comment), but three places to fix kill logic.
- **Suggested fix**: `killPIDs(pids []int, signal syscall.Signal) (killed, failed int)` in `internal/util` (map `.PID` at call sites).
- **Fingerprint**: `duplication:cmd|internal/cmd/orphans.go|process-kill-loop`

## Minor Findings (P2 — informational)

- **m1** — `internal/cmd/capacity_dispatch.go:1938-1962` vs `1993-2017`: identical
  fingerprint-sibling CLI query differing only in the per-issue predicate. Fix:
  `firstFingerprintSibling(townRoot, workBeadID, fpLabel string, match func(*beads.Issue) bool) string`.
  Fingerprint: `duplication:cmd|internal/cmd/capacity_dispatch.go|fingerprint-sibling-query`
- **m2** — `internal/cmd/convoy_stage.go:985-1001` (`renderErrors`) vs `1933-1949`
  (`renderWarnings`): identical finding-formatter differing only in header literal.
  Fix: `renderFindings(header string, findings []StagingFinding) string`.
  Fingerprint: `duplication:cmd|internal/cmd/convoy_stage.go|render-findings`
- **m3** — `internal/cmd/escalate_impl.go:830-847` (email) vs `849-866` (sms):
  guard-then-send-then-append switch arms; consolidation is parameter-heavy.
  Fingerprint: `duplication:cmd|internal/cmd/escalate_impl.go|contact-delivery-arm`
- **m4** — `internal/cmd/polecat_spawn.go:335-358` vs `440-462`: base-branch resolution
  duplicated across allocate vs idle-reuse paths (comment already admits "same logic").
  Fix: `resolveBaseBranch(r *rig.Rig, opts SlingSpawnOptions) string`.
  Fingerprint: `duplication:cmd|internal/cmd/polecat_spawn.go|resolve-base-branch`
- **m5** — `internal/cmd/prime_output.go:507-524` (Witness) vs `539-556` (Refinery):
  parked-guard + startup checklist arms differ only by role label. Fix:
  `printPatrolRoleStartup(ctx, cli, roleName string)`.
  Fingerprint: `duplication:cmd|internal/cmd/prime_output.go|patrol-role-startup`
- **m6** — `internal/cmd/refinery.go:288-303` (disable) vs `305-320` (enable): differ
  only by bool + verb; arguably clearer left as two cobra handlers. Low ROI.
  Fingerprint: `duplication:cmd|internal/cmd/refinery.go|refinery-enable-disable`
- **m7** — `internal/doctor/agent_beads_check.go:489-512` (`listCrewWorkers`) vs
  `555-574` (`listPolecats`): same GH#2767 worktree-skip dir scan. Fix:
  `listCanonicalIdentityDirs(townRoot, rigName, subdir string) []string`.
  Fingerprint: `duplication:doctor|internal/doctor/agent_beads_check.go|identity-dir-scan`
- **m8** — `internal/doltserver/doltserver.go:2617-2633` vs `2700-2726`: identical
  `SHOW DATABASES` closure inside near-identical retry loops. Fix:
  `runShowDatabases(config *Config) ([]byte, error)` (+ shared backoff).
  Fingerprint: `duplication:doltserver|internal/doltserver/doltserver.go|show-databases`
- **m9** — `internal/cmd/ci_watcher.go:128-145` (`resolveRigContext`) vs
  `internal/cmd/pr_watcher.go:88-105` (`resolvePRWatcherContext`): identical except the
  flag global read; `inferRigFromCwd` already shared. Fix:
  `resolveRigContext(rigFlag string) (townRoot, rigName, rigDir string, err error)` in `helpers.go`.
  Fingerprint: `duplication:cmd|internal/cmd/ci_watcher.go+pr_watcher.go|resolve-rig-context`
- **m10** — `internal/cmd/mail_announce.go:141-162` vs `internal/cmd/mail_channel.go:250-271`:
  identical per-message render loop; only the preview field differs (`Description` vs `Body`).
  Fix: `printChannelMessage(id, title, from string, priority int, created time.Time, preview string)`.
  Fingerprint: `duplication:cmd|internal/cmd/mail_announce.go+mail_channel.go|channel-message-render`
- **m11** — `internal/cmd/mail_inbox.go:367-403` vs `667-703`: ~36-line in-file inbox
  duplicate (flagged at threshold 150). Fix: extract shared inbox-render/scan helper.
  Fingerprint: `duplication:cmd|internal/cmd/mail_inbox.go|inbox-block`
- **m12** — `internal/reaper/mail_reap.go:247-264` vs `internal/reaper/stale_escalation.go:278-295`:
  reap wrapper twins; same root cause as M1 (`ProcessedRemain`/`Remain`). Fix with M1.
  Fingerprint: `duplication:reaper|internal/reaper/mail_reap.go+stale_escalation.go|reap-wrapper`
- **m13** — `internal/reaper/processed_mail.go:186-225` (`ReapProcessedWispMail`) vs
  `245-282` (`ReapProcessedMail`): deliberate wisp/issue table twins; already factored via
  `runProcessedMailReap`. Low ROI — differing table/clause params cancel most benefit.
  Fingerprint: `duplication:reaper|internal/reaper/processed_mail.go|wisp-vs-issue-reap`
- **m14** — `internal/session/window_tint.go:107-131` (`ResolveTintFactor`) vs
  `135-159` (`IsWindowTintEnabled`): same per-rig→global config precedence walk; differs
  by leaf field/type. Fix: generic `resolveWindowTintField[T any](rig string, pick func(*config.WindowTint)*T, def T) T`.
  Fingerprint: `duplication:session|internal/session/window_tint.go|tint-config-precedence`
- **m15** — `internal/autotestpr/branch_gc.go:524-542` (`parseTransitionAt`) vs
  `566-582` (`parseCooldownUntil`): same metadata-JSON-then-description timestamp parse;
  differs by JSON key. Fix: `parseMetaTime(iss *beads.Issue, key string) time.Time`.
  Fingerprint: `duplication:autotestpr|internal/autotestpr/branch_gc.go|parse-meta-time`
- **m16** — `internal/cmd/dog.go:915-943` vs `internal/cmd/trail.go:481-509` also surfaces
  as a duplicate of `internal/cmd/orphans.go` age formatters — folded into C2.
- **m17** — `internal/web/handler.go:99-124` vs `132-156`: ~25-line in-file handler
  duplicate. Fix: extract shared handler helper.
  Fingerprint: `duplication:web|internal/web/handler.go|handler-block`

## Counts

  counts: critical=3 major=17 minor=17

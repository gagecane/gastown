# Duplication Axis

## Summary
The gastown tree carries a moderate amount of genuine copy-paste duplication,
concentrated in three recurring shapes: (1) **persistence helpers** — the same
"open file → handle ENOENT → JSON/JSONL decode → skip-malformed" body re-written
per state struct (deacon state files, pushlog readers); (2) **scheduler candidate
selection** — the convoy and epic schedulers share a ~40-line filter ladder that
has already demonstrated drift (a deferred-guard regression had to be patched into
both copies separately); and (3) **exec wrappers** — `runBdCommand`/`runGhCommand`
in the web API are byte-for-byte identical except the binary name, and the worktree
provisioning block in `polecat/manager.go` is pasted across two Add functions. The
top theme is that **bug fixes have to be applied in N places today**, and at least
one such fix (gu-pi35l deferred guard) was applied as parallel edits rather than to
a shared helper — exactly the drift risk this axis exists to catch.

None of these are correctness defects right now; they are maintainability debt with
real, demonstrated drift potential. The codebase is otherwise disciplined about
extracting shared helpers (e.g. `inferRigFromCwd`, `resolveRigForBeadWithLabels`,
`ExtractPrefix` are already shared), so the duplication that remains is tractable
and worth consolidating.

## Score
score: 0.62

## Critical Findings (P0 — file as beads, fix urgently)

### Scheduler candidate-selection ladder duplicated across convoy & epic (already drifted)
- **Title**: Extract shared `selectScheduleCandidates` helper for convoy/epic schedulers
- **Location**: `internal/cmd/scheduler_convoy.go:63-104` ≈ `internal/cmd/scheduler_epic.go:64-100`
- **Impact**: The two functions run an identical filter ladder over tracked
  beads/children: closed/tombstone skip → **deferred guard** → assigned skip →
  already-scheduled skip → rig resolution → append candidate. The in-code comments
  themselves document the drift: the `df7da9bf` fix for the gu-pi35l deferred-guard
  regression had to be added to BOTH copies *separately*, and the comment notes the
  original fix "did not cover this epic-level candidate selection." This is the
  canonical "fix one copy, miss the other" failure — it has already happened once on
  this exact block. A future filter (e.g. a new skip reason or guard) is highly
  likely to be added to one path and forgotten in the other.
- **Suggested fix**: Introduce a shared helper in the `cmd` package, e.g.
  `func selectScheduleCandidates(townRoot string, items []trackedBead, scheduledSet map[string]bool, force bool) ([]scheduleCandidate, scheduleSkipCounts)`,
  taking a small interface/struct (ID, Title, Status, Assignee, Labels, DeferUntil,
  Description) so both convoy `tracked` and epic `children` adapt to it. Both call
  sites then share one filter ladder; new guards are added once.
- **Fingerprint**: `duplication:internal/cmd/scheduler_convoy.go|scheduler_epic.go|selectScheduleCandidates`

## Major Findings (P1 — track, do not auto-bead)

### Web API exec wrappers identical except binary name
- **Location**: `internal/web/api.go:1392-1433` (`runBdCommand`) ≈
  `internal/web/api.go:1672-1713` (`runGhCommand`), and `api.go:233` (`runGtCommand`)
  shares the same body. Three copies of: context timeout → semaphore acquire →
  `exec.CommandContext` → stdout/stderr capture → timeout/error wrapping.
- **Impact**: 3 copies of a non-trivial (~42-line) block with shared semaphore
  semantics. Any change to the command-slot protocol, output merging, or timeout
  error message must be made in three places. The comments already cross-reference
  each other ("shared with runGtCommand/runGhCommand"), signalling the authors know
  these are one concept.
- **Suggested fix**: Single `func (h *APIHandler) runCommand(ctx, timeout, name string, args []string) (string, error)` with the binary name (or `h.gtPath`) passed in; the three named wrappers become one-line shims.
- **Fingerprint**: `duplication:internal/web/api.go|runBdCommand+runGhCommand+runGtCommand|runCommand-helper`

### Deacon state load/save triplicated across three state files
- **Location**: `internal/deacon/redispatch.go:113-167` (Load/Save Redispatch),
  `internal/deacon/feed_stranded.go:99-122+` (Load/Save FeedStranded),
  `internal/deacon/stuck.go:90-114+` (Load/Save HealthCheck).
- **Impact**: Three near-identical pairs: `LoadXState` (ReadFile → ENOENT returns
  empty-with-initialized-map → Unmarshal → nil-map backfill) and `SaveXState`. The
  only differences are the concrete struct type and which map field is initialized.
  3+ copies, high drift risk: a hardening fix (atomic write, fsync, file perms,
  corruption recovery) applied to one would silently miss the other two.
- **Suggested fix**: Generic helpers using Go generics, e.g.
  `func loadJSONState[T any](path string, newEmpty func() *T) (*T, error)` and
  `func saveJSONState[T any](path string, state *T) error`, or a small
  `stateFile[T]` type. The per-struct map backfill can be handled by the
  `newEmpty` constructor.
- **Fingerprint**: `duplication:internal/deacon/redispatch.go|feed_stranded.go|stuck.go|Load+Save-State-helper`

### Polecat worktree provisioning block pasted across two Add paths
- **Location**: `internal/polecat/manager.go:840-883` (in `addWithOptionsLocked`)
  ≈ `internal/polecat/manager.go:1049-1098` (in `AddWithOptions`).
- **Impact**: The resume-branch-vs-fresh-branch worktree creation logic — fetch
  resume branch / `WorktreeAddExistingForce`, else compute startPoint from
  `BaseBranch`/rig default, `RefExists` validation with the identical multi-line
  "configured default_branch not found" diagnostic, `WorktreeAddFromRef` — is
  duplicated verbatim (including the long error string). This is git/worktree
  provisioning, a critical path; the two copies have already diverged slightly in
  comments. A fix to ref validation or the error guidance must be applied twice.
- **Suggested fix**: Extract `func (m *Manager) provisionWorktree(repoGit, clonePath, branchName string, opts AddOptions) (created bool, err error)` and call from both. Note `cleanupOnError` is a closure — pass it in or return an error and let the caller clean up.
- **Fingerprint**: `duplication:internal/polecat/manager.go|addWithOptionsLocked+AddWithOptions|provisionWorktree`

### config loader: BuildStartupCommand duplicates Windows-script + resolution body
- **Location**: `internal/config/loader.go:2325-2501` (`BuildStartupCommand`) and
  `internal/config/loader.go:2555-2745` (`BuildStartupCommandWithAgentOverride`);
  dupl flagged 2408-2484 ≈ 2682-2745.
- **Impact**: The agent-config resolution + `SanitizeAgentEnv` + Windows
  PowerShell-script-vs-send-keys command assembly is largely shared between the two
  builders, with the override variant a superset. The comment at the override path
  even calls out a bug where `withRoleSettingsFlag` "previously only the non-override
  path included it, causing hooks to silently not fire" — i.e. the two paths already
  drifted and caused a real defect. High-value to converge.
- **Suggested fix**: Make `BuildStartupCommand` delegate to
  `BuildStartupCommandWithAgentOverride(envVars, rigPath, prompt, "")` (discarding
  the error or logging it), eliminating the second copy of the resolution + command
  assembly entirely.
- **Fingerprint**: `duplication:internal/config/loader.go|BuildStartupCommand|delegate-to-WithAgentOverride`

### pushlog: receipt vs failure log readers duplicated
- **Location**: `internal/pushlog/pushlog.go:160-202` (`Read`) ≈
  `internal/pushlog/failure.go:160-201` (`ReadFailures`).
- **Impact**: Identical JSONL reader: path resolve → open (nolint G304) → ENOENT
  returns (nil,nil) → 64KB/1MB-buffered scanner → trim/skip-empty → Unmarshal →
  skip-malformed → scanner.Err wrap. Two copies differing only in the record type
  (`Receipt` vs `Failure`) and error-message nouns. A fix to malformed-line handling
  or buffer sizing must be made twice.
- **Suggested fix**: Generic `func readJSONL[T any](path string) ([]T, error)`; both
  `Read` and `ReadFailures` become thin wrappers. (The append-side writers are also
  worth checking for the same shape.)
- **Fingerprint**: `duplication:internal/pushlog/pushlog.go|failure.go|readJSONL-helper`

### util/orphan: zombie vs orphan SIGTERM/SIGKILL escalation state machine duplicated
- **Location**: `internal/util/orphan.go:770-861` (`CleanupZombieClaudeProcesses`)
  ≈ `internal/util/orphan.go:871-971` (`CleanupOrphanedClaudeProcesses`).
- **Impact**: ~90-line graceful-escalation state machine (first encounter → SIGTERM
  + record; next cycle alive → SIGKILL; still alive → unkillable) duplicated. The
  doc comments are explicitly identical ("Uses the same graceful escalation as
  orphan cleanup"). This is process-killing logic — a bug in the escalation timing
  or PID-liveness check fixed in one copy and not the other could leak or wrongly
  kill processes.
- **Suggested fix**: Extract the escalation engine over an abstract
  `(pid int, found bool)` set + a `signal(pid, sig)` callback, e.g.
  `escalateCleanup(targets []procTarget, state map[int]*procState, now time.Time) []CleanupResult`. The two callers differ only in how they enumerate targets and their result struct type.
- **Fingerprint**: `duplication:internal/util/orphan.go|CleanupZombie+CleanupOrphaned|escalateCleanup-helper`

### ci-watcher / pr-watcher rig-context resolver duplicated
- **Location**: `internal/cmd/ci_watcher.go:128-145` (`resolveRigContext`) ≈
  `internal/cmd/pr_watcher.go:88-105` (`resolvePRWatcherContext`).
- **Impact**: Both resolve `(townRoot, rigName, rigDir, err)` via the identical
  `FindFromCwdOrError` → flag-or-`inferRigFromCwd` → `Stat(rigDir)` sequence,
  differing only in which package-level `--rig` flag var they read. The
  `resolvePRWatcherContext` comment already says "Reuses inferRigFromCwd, shared with
  ci-watcher" — the author noted the overlap but copied the wrapper anyway.
- **Suggested fix**: `func resolveWatcherRigContext(rigFlag string) (townRoot, rigName, rigDir string, err error)` taking the flag value as a parameter; both watchers call it.
- **Fingerprint**: `duplication:internal/cmd/ci_watcher.go|pr_watcher.go|resolveWatcherRigContext`

## Minor Findings (P2 — informational)

- **mail batch per-message op handlers**: `internal/cmd/mail_inbox.go` —
  `runMailDelete` (367-403), `runMailMarkUnread` (667-703), and siblings
  (`runMailMarkRead`, etc.) share the "detect sender → getMailbox → loop args
  applying op → collect errors → ⚠/✓ report" shape. A
  `runMailBatchOp(args, opName, fn func(*mail.Mailbox, string) error)` helper would
  collapse ~5 handlers. Low risk (output strings differ), but real repetition.
  Fingerprint: `duplication:internal/cmd/mail_inbox.go|runMailDelete+runMailMarkUnread|runMailBatchOp`
- **doctor Run/Fix check pairs**: `internal/doctor/config_check.go`
  (`CustomTypesCheck` 627-708 / `CustomStatusesCheck` 774-849, plus their Fix
  methods 718-751 / 852-887), `rig_check.go` (`WitnessExistsCheck` 441-494 /
  `RefineryExistsCheck` 604-657), and `claude_settings_check.go` (297-345 / 349-397)
  each have sibling checks with near-identical Run/Fix bodies that differ in the
  config key / agent role. These follow a Check interface, so partial templating
  (shared "run bd config get → diff expected → report" helper) is possible but
  coupling is moderate; lower priority than the cross-package dups above.
  Fingerprint: `duplication:internal/doctor/config_check.go+rig_check.go+claude_settings_check.go|sibling-Check-Run-Fix`
- **misc cross-file 2-copy blocks** flagged at threshold 100 (lower confidence,
  worth a glance, not necessarily actionable): `beads/beads_channel.go:340` ≈
  `beads/beads_group.go:321`; `cmd/dog.go:915` ≈ `cmd/trail.go:481`;
  `cmd/mail_announce.go:141` ≈ `cmd/mail_channel.go:250`;
  `cmd/molecule_await_event.go:181` ≈ `cmd/molecule_await_signal.go:161`;
  `daemon/daemon.go:3934` ≈ `daemon/reap_dead_agent_wisps.go:121`;
  `polecat/manager.go:531` ≈ `polecat/session_manager.go:179`;
  `witness/blanktools.go:152` ≈ `witness/stuckindone.go:110`.
  Fingerprint: `duplication:cross-file-threshold100|misc-2copy-blocks`

## Method note
Tool used: `golangci-lint` built-in `dupl` linter (the standalone `dupl` /
`go install` is unavailable behind the corp proxy). Ran at token threshold 150
(high confidence) and 100 (broader). Each reported pair below was opened and
read to confirm genuine duplication vs. coincidental structural similarity;
test-file duplicates (the majority of raw hits) were excluded as out of scope
for shared-helper consolidation. All findings are in `internal/`.

## Counts
counts: critical=1 major=7 minor=3

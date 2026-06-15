# Integration Contracts Axis

## Summary
The gastown component seams are mostly contract-consistent on the happy path —
prefix/rig routing (`routes.jsonl`), capacity accounting, vars round-trips, and
the convoy `tracks`/`blocks` edge wiring all hold up. The drift clusters in three
high-impact themes. **(1) Remote-Dolt blindness:** the daemon's reaper/curio/
metrics scanners hardcode `127.0.0.1` even though the rest of the daemon honors a
configurable Dolt host, and `DiscoverDatabases` collapses connection failure into
a fixed one-database list — so on a remote-Dolt town these subsystems silently do
nothing. **(2) Merge-landed proof is fragile:** the refinery records the
merge_commit/`close_reason: merged` proof-of-merge fields via a best-effort
`Update` that only warns on failure, then closes the MR anyway — yet that
description field is the *only* thing the post-merge reconcile reads to decide
"did this work land?", so a transient write failure permanently strands the
source bead HOOKED to a dead polecat (the gu-7igu8 signature, un-recoverable).
**(3) Wrong-database writes at the CLI boundary:** `gt assign` and `gt bead move`
create/close beads against the wrong database (town vs rig), reintroducing the
exact bug `gt bead create` was written to fix, now that bd dropped cross-rig
routing in v0.62.

The unifying pattern: **boundaries that fail silently (wrong result) rather than
loudly (error)** — host defaults, error-to-empty-list coercion, best-effort
persists, and cwd/prefix routing that lands data in the wrong DB.

## Score
score: 0.55

## Critical Findings (P0 — file as beads, fix urgently)

- **Title**: Thread the configured Dolt host through daemon reaper/curio/metrics scanners
  - **Location**: `internal/daemon/wisp_reaper.go:193,213,249,289,327,354,381,407`; `internal/daemon/hooked_beads_metrics.go:52,64`; `internal/daemon/curio_dog.go:191`, `curio_dog_reconcile.go:126`, `curio_dog_paging.go:136`
  - **Impact**: SILENT. The daemon supports a remote Dolt host — `DoltServerConfig.Host` is honored in `isRemote()` (dolt.go:224), and the sibling backup path already reads `d.doltServer.config.Host` (jsonl_git_backup.go:311-320). But these scanners pass a literal `"127.0.0.1"` to `reaper.DiscoverDatabases`/`reaper.OpenDB`/`curio.OpenStore`. On a town whose Dolt lives on another host, the reaper reaps nothing, `hooked_beads` gauges report 0, and curio files nothing — with no error surfaced. There is a `doltServerPort()` helper but no matching `doltServerHost()` (confirmed: grep finds none), which is how the host got dropped.
  - **Suggested fix**: Add `func (d *Daemon) doltServerHost() string` returning `d.doltServer.config.Host` (default `127.0.0.1`) and thread it through every `DiscoverDatabases`/`OpenDB`/`curio.OpenStore` call, exactly as `jsonl_git_backup.go` already does.
  - **Fingerprint**: `axis:integration|internal/daemon/wisp_reaper.go|doltServerHost-hardcoded-127.0.0.1`

- **Title**: Stop converting Dolt connection failure into a fixed one-database list
  - **Location**: `internal/reaper/reaper.go:74-118` (callers `wisp_reaper.go:193`, `hooked_beads_metrics.go:52`)
  - **Impact**: SILENT. `DiscoverDatabases` returns `DefaultDatabases` (`["hq"]`) on *any* error — `sql.Open` failure (line 77-79), `SHOW DATABASES` failure (86-88), and the zero-rows case (114-116) are all collapsed to the same fallback. "Connection refused" is therefore indistinguishable from "this town only has hq", so the reaper proceeds against a phantom single DB and skips every real rig. Compounds the host bug above into total silent degradation.
  - **Suggested fix**: Return `([]string, error)` and have callers distinguish "reachable but empty" from "unreachable"; only fall back to `DefaultDatabases` on the zero-rows path, and log the error otherwise.
  - **Fingerprint**: `axis:integration|internal/reaper/reaper.go|DiscoverDatabases-error-as-default-list`

- **Title**: Persist merge_commit/close_reason atomically with the MR close (proof-of-merge can be lost)
  - **Location**: `internal/refinery/engineer.go:1980-1999` (`HandleMRInfoSuccess`); read side `internal/polecat/active_mr.go:119-126` (`mrMerged`)
  - **Impact**: SILENT, high blast radius. The MR's `merge_commit` SHA and `close_reason: merged` are written into the bead *description* via `beads.Update` (engineer.go:1987-1989); that failure is swallowed as a warning and execution falls through to `CloseWithReason("merged", …)` which writes only the bead's column-level `CloseReason` — NOT the description fields. But `mrMerged()` (confirmed) inspects *only* `MRFields.CloseReason` and `MRFields.MergeCommit` parsed from the description. So if the `Update` fails but the close succeeds, a genuinely-merged MR reads as `mrMerged()==false`, the reaper's orphan reconcile (`orphan_reconcile.go:153`) skips it, and the source bead stays HOOKED to a dead polecat forever — the gu-7igu8 signature, un-recoverable because the proof was never persisted.
  - **Suggested fix**: Treat the merge_commit/close_reason `Update` as a hard precondition for the close: retry or abort and leave the MR open (fail-closed) if it fails. Alternatively, make `mrMerged()` also consult the bead's column-level `CloseReason`/status so a successful close alone proves merge.
  - **Fingerprint**: `axis:integration|internal/refinery/engineer.go|HandleMRInfoSuccess-merge-commit-persist`

- **Title**: Apply the recovery path's merge-landed verification on the primary merge path too
  - **Location**: `internal/refinery/engineer.go:1960-2027` (`HandleMRInfoSuccess`) vs `internal/refinery/manager.go:760-780` (`Manager.PostMerge`)
  - **Impact**: The hand-merge/recovery path refuses to close beads unless `verifyMergeLanded` confirms `mr.MergeCommit` is an ancestor of `origin/<target>` (manager.go:768-772). The primary automated path has no equivalent ancestry re-check before closing beads and deleting the branch — it trusts `result.Success`. It is partially protected by `VerifyPushedCommit` in `doMerge` (engineer.go:1120), but the two reconcile entry points disagree on the close-time safety contract; any path producing `Success:true` without that exact sequence closes beads with no fail-closed ancestry guard.
  - **Suggested fix**: Factor `verifyMergeCommitLanded` into a shared helper and call it in `HandleMRInfoSuccess` immediately before `CloseWithReason`, mirroring `PostMerge`.
  - **Fingerprint**: `axis:integration|internal/refinery/engineer.go|HandleMRInfoSuccess-missing-verifyMergeLanded`

- **Title**: Mountain auto-skip permanently strands the synthesis rollup
  - **Location**: `internal/witness/mountain.go:225-235` (`skipMountainIssue`) vs `internal/cmd/formula.go:724-730` (synthesis `blocks` edges) and `internal/cmd/convoy.go:1804` (completion gate)
  - **Impact**: SILENT hang. When a leg fails 3×, `skipMountainIssue` sets `status=blocked` + label `mountain:skipped` — it does NOT close/tombstone the leg or remove its `blocks` edge on the synthesis bead. But synthesis dispatches only when every blocker is `closed`/`tombstone` (convoy.go:1804 treats only those as done; confirmed). So the skipped leg is a permanent non-closed blocker: the synthesis rollup never runs and the convoy never auto-completes — the opposite of the design intent that skipping lets the convoy "continue grinding around it". No code special-cases `mountain:skipped` in the dispatch/completion path.
  - **Suggested fix**: In `skipMountainIssue`, tombstone the leg (or close it with a `mountain:skipped` reason) instead of `status=blocked`; OR have the synthesis blocks-edge / convoy-completion logic treat a blocker carrying `mountain:skipped` as satisfied. Both sides must agree that "skipped == terminal-for-rollup".
  - **Fingerprint**: `axis:integration|internal/witness/mountain.go|skipMountainIssue-vs-synthesis-blocks`

- **Title**: `gt assign` writes the bead to the HQ database, not the assignee's rig database
  - **Location**: `internal/cmd/assign.go:124-127` (create) and `:147-150` (hook update)
  - **Impact**: SILENT wrong-DB. The command resolves a real rig and crew agent (`rigName/crew/crewName`), then creates and hooks the bead with `.Dir(townRoot)` — pinning bd to the hq database. It even computes `rigBeadsDir` at :165 but only feeds it to `updateAgentHookBead`. The bead lands in hq with a town prefix while assigned to a rig crew member — the exact failure mode `gt bead create` was written to fix (bead_create.go:18-32): "Rig-scoped beads created that way become invisible to the owning rig's agents." No test exists (`assign_test.go` absent).
  - **Suggested fix**: Resolve the target rig's `.beads` via `beads.ResolveRepoAliasBeadsDir(townRoot, rigName)` and pass it through `WithBeadsDir(...)`/`Dir(rigDir)` for both the create and the hook update, mirroring `execBdCreate`.
  - **Fingerprint**: `axis:integration|internal/cmd/assign.go|runAssign-create-Dir-townRoot`

- **Title**: `gt bead move` creates the destination and closes the source against the wrong (cwd) database
  - **Location**: `internal/cmd/bead.go:184` (new bead `bd create`) and `:196-198` (`bd close sourceID`)
  - **Impact**: SILENT wrong-DB. The new bead is created via `exec.Command("bd", …)` with no `Dir()`/`BEADS_DIR` — only `--prefix targetPrefix`. Since bd dropped cross-rig routing in v0.62 (confirmed in close.go:102-105 comment), `--prefix bd-` run from the gastown rig writes the new bead into gastown's DB, not beads'. The `bd close sourceID` at :196 also runs unrouted, even though the preceding `bd show` at :121 correctly uses `.Dir(resolveBeadDir(sourceID))`. Can orphan the moved bead in the wrong DB and leave the source open.
  - **Suggested fix**: Resolve the target prefix's rig dir via `beads.GetRigPathForPrefix(townRoot, targetPrefix)` and pin `BEADS_DIR`/`Dir` for the create; route the close via `resolveBeadDir(sourceID)` exactly as the show call does.
  - **Fingerprint**: `axis:integration|internal/cmd/bead.go|runBeadMove-create-close-no-routing`

## Major Findings (P1 — track, do not auto-bead)

- **Daemon vs doltserver disagree on the default Dolt port (3306 vs 3307).** `internal/daemon/dolt.go:86` defaults `DoltServerConfig.Port` to 3306, while `internal/doltserver/doltserver.go` `DefaultPort = 3307` and the whole codebase branches pid/signal logic on `== 3307`. Normal path supplies an explicit port, but a config that enables `dolt_server` without `port` runs at `Port:0`/3306, taking the wrong pid/signal branch. Fingerprint: `axis:integration|internal/daemon/dolt.go|DefaultDoltServerConfig-port-3306-vs-3307`
- **`resume_branch` formula var injected on the direct sling path but dropped on the rig/deferred path.** `internal/cmd/sling.go:1163-1165` appends `resume_branch=<branch>`; the rig/batch chokepoint `internal/cmd/sling_dispatch.go:617-631` only upserts `base_branch` though `params.ResumeBranch` is set. The worktree is correct but `{{resume_branch}}` renders empty on rig/deferred dispatch, so a resume polecat behaves as if starting fresh. Fingerprint: `axis:integration|internal/cmd/sling_dispatch.go|executeSling-resume_branch-var`
- **Non-atomic post-merge reconcile (close MR → close source → clear active_mr) can diverge.** `internal/refinery/engineer.go:1994,2017,2036`; recovery in `orphan_reconcile.go:115-200`. Documented as non-atomic; an interruption between steps leaves MR closed but source bead HOOKED. The reaper safety net only works if finding (merge_commit persist) holds. Consider closing the source + clearing active_mr *before* closing the MR bead (the MR-closed state is the reaper's idempotency anchor). Fingerprint: `axis:integration|internal/refinery/engineer.go|post-merge-reconcile-nonatomic`
- **v2 `MRPhase` state machine is defined but never written or read (dead contract).** `internal/refinery/types.go:64-121` defines phases + `ValidatePhaseTransition`, but `beads.MRFields` has no phase field and the constants are referenced nowhere outside types.go/tests. The real machine is beads `status` + `Assignee` claim + `close_reason`. `MRPhaseFailed`/`Rejected` have no `CloseReason` representation. A live drift trap. Fingerprint: `axis:integration|internal/refinery/types.go|MRPhase-unused-statemachine`
- **`gt synthesis` counts the synthesis bead itself as a leg.** `internal/cmd/synthesis.go:437-483` fills `LegIssues` from all tracked beads with no `-leg-` vs `-syn-` filter; the synthesis bead is tracked too (formula.go:715), so `allComplete` is never true. Held to major because `CheckSynthesisReady`/`TriggerSynthesisIfReady` have no production callers (live path uses blocks-edges) — a broken manual command, not the live rollup. Fingerprint: `axis:integration|internal/cmd/synthesis.go|collectLegOutputs-includes-synthesis-bead`
- **Documented `--preset`/`scope` axis selection is silently ignored.** `mountain-code-review.formula.toml` declares `[presets.quick/standard/full]` and docs advertise `--preset=quick`, but `internal/formula/types.go:28-58` has no `Presets` field, there's no `--preset` flag, and `executeConvoyFormula` (formula.go:574) always creates all legs. The TOML table is parsed-and-dropped. Fingerprint: `axis:integration|internal/cmd/formula.go|preset-selection-ignored`
- **`dolt_remotes.interval` is `time.Duration` while every sibling patrol uses a duration `string`.** `internal/daemon/types.go:162` vs the string form at `:113,:182,:196`. An operator writing `"interval":"15m"` (mirroring other stanzas) makes `json.Unmarshal` fail in `LoadPatrolConfig`, which returns nil and silently disables the *entire* patrol config. Fingerprint: `axis:integration|internal/daemon/types.go|DoltRemotesConfig.Interval`
- **Two divergent `DaemonPatrolConfig` structs over the same `mayor/daemon.json`.** Runtime `daemon.DaemonPatrolConfig` (types.go:216-226, typed `Patrols *PatrolsConfig`) vs `config.DaemonPatrolConfig` (config/types.go:673-678, `Patrols map[string]PatrolConfig`). Config written through the `config` type can't express rich patrol sub-configs (`dolt_remotes`, `jsonl_git_backup`) and would silently drop them. Latent (prod uses the daemon type). Fingerprint: `axis:integration|internal/config/types.go|DaemonPatrolConfig-duplicate`
- **Two divergent `RigConfig`/`BeadsConfig` structs over the same rig-root `config.json`.** `rig.RigConfig` (manager.go:97-119) has `default_branch`/`polecat_*`; `config.RigConfig` (config/types.go:777-787) lacks them; `config.BeadsConfig` has `Repo`, `rig.BeadsConfig` does not. `config.SaveRigConfig` would silently strip fields. Latent (no prod caller of `config.SaveRigConfig`). Fingerprint: `axis:integration|internal/config/types.go|RigConfig-duplicate`
- **`gt close` with mixed-prefix bead IDs routes every bead to the first bead's database.** `internal/cmd/close.go:111-116` pins the dir using only `resolveBeadDir(beadIDs[0])`. `gt close gt-abc bd-xyz` runs both closes against gt-abc's DB; bd-xyz fails or (on ID collision) touches an unrelated bead. The `--cascade` path already resolves per-parentID. Fingerprint: `axis:integration|internal/cmd/close.go|runClose-beadIDs[0]-single-dir`

## Minor Findings (P2 — informational)

- **`assertServedData` misclassifies a legitimately-empty `issue_prefix` value as an imposter.** `internal/daemon/dolt.go:1129-1155` tests value-non-emptiness, not row existence; an empty-but-present row triggers a false imposter restart. Use `SELECT COUNT(*) … WHERE key='issue_prefix'`. Fingerprint: `axis:integration|internal/daemon/dolt.go|assertServedData-empty-value-vs-missing-row`
- **Deferred dispatch drops `Owned`/convoy-strategy intent that the direct path persists.** `internal/scheduler/capacity/pipeline.go:362-383` `ReconstructFromContext` omits `Owned`; intentional (convoy created at schedule time) but undocumented on `DispatchParams`. Fingerprint: `axis:integration|internal/scheduler/capacity/pipeline.go|ReconstructFromContext-owned-omitted`
- **`CloseReason` is a typed enum in refinery but an untyped string with duplicated `"merged"` literals across writers/readers.** `internal/beads/fields.go:611` + literals in engineer.go:1986/1994/2013, manager.go:773, orphan_reconcile.go:172, active_mr.go:124. A single typo silently breaks `mrMerged()`. Export `const CloseReasonMerged = "merged"`. Fingerprint: `axis:integration|internal/beads/fields.go|close-reason-untyped-string-literals`
- **Operator-tunable `GT_DISPATCH_*`/`GT_HEARTBEAT_PROFILE` vars bypass the env registry "single source of truth".** Read via raw `os.Getenv` at `internal/cmd/capacity_dispatch.go:113,304,329,1479` and `daemon.go:1104`; not declared in `internal/env/registry.go`. Invisible to the env doc generator; silently no-op on a typo. Fingerprint: `axis:integration|internal/env/registry.go|missing-GT_DISPATCH-vars`
- **`gt show` routing-dir resolution can pick a flag value as the bead ID.** `internal/cmd/show.go:49-56` `extractBeadIDFromArgs` returns the first non-`-` arg with no value-flag awareness; a space-separated value flag misroutes the dir (usually self-corrects via cwd discovery). Fingerprint: `axis:integration|internal/cmd/show.go|extractBeadIDFromArgs-value-flag`
- **`flagValueFromArgs` treats a following flag token as the flag's value.** `internal/cmd/bead_create.go:125-152` returns `args[i+1]` unconditionally; `--assignee --type bug` resolves assignee to `--type`. Loud-ish (warning + cwd fallback), degenerate input. Fingerprint: `axis:integration|internal/cmd/bead_create.go|flagValueFromArgs-next-flag`

## Counts
  counts: critical=7 major=10 minor=6

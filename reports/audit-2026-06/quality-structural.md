# Code Quality & Structural Audit — gastown_upstream

**Bead:** gu-nid89.12 (epic gu-nid89 — Whole-Repo Gastown Audit)
**Date:** 2026-06-11
**Commit audited:** `a9afe585` (branch `main`)
**Auditor:** polecat dust
**Cross-ref:** epic gu-q30qg (internal/cmd extraction — already decomposed/closed),
prior `/health` report `.quality/2026-06-04/quality-report.md`

---

## How this was measured

All findings are reproducible from the repo with the bundled toolchain
(`go1.26.2`, `golangci-lint 2.11.4`). The repo's installed `staticcheck` is
built against go1.25 and **cannot analyze this tree** (it reports 78 bogus
"package requires newer Go version" compile errors instead of real lint) — so
dead-code/complexity/duplication numbers below come from golangci-lint, not
staticcheck.

```bash
# dead code + complexity + duplication (config: /tmp/audit-lint.yml)
golangci-lint run --config <(cat <<'YML'
version: "2"
run: { timeout: 15m, tests: false }
linters:
  default: 'none'
  enable: [unused, gocyclo, dupl]
  settings:
    gocyclo: { min-complexity: 20 }
    dupl: { threshold: 150 }
issues: { max-issues-per-linter: 0, max-same-issues: 0 }
YML
) ./...
go vet ./...   # CLEAN — 0 findings
```

**Headline numbers (non-test source only):**

| Metric | Value |
|---|---|
| `internal/cmd` LOC (non-test) | 121,098 across 278 files |
| `internal/cmd` LOC (incl. tests) | 206,975 across 555 files |
| `internal/` packages | 88 |
| Dead code findings (`unused`) | 78 |
| Functions with cyclomatic complexity > 20 (`gocyclo`) | 255 |
| Duplicate blocks ≥150 tokens (`dupl`) | 34 (17 distinct pairs) |
| `go vet` findings | 0 (clean) |
| `panic()` in non-test code | 0 |

The shape matches the 2026-06-04 `/health` finding: **quality debt is
concentrated, not diffuse.** A handful of "god" CLI functions carry the
complexity, several superseded subsystems were never deleted, and duplication
clusters in a few copy-paste families.

---

## Prioritized findings

Priority = severity × blast-radius, weighted by **churn** (a complex function on
a frequently-edited file is where the next bug lands). Top-churn `internal/`
non-test files over 6 months: `daemon.go` (301 commits), `done.go` (252),
`sling.go` (244), `polecat/manager.go` (212), `tmux.go` (209), `beads.go` (199),
`witness/handlers.go` (185), `doltserver.go` (177).

### P1 — Complexity hotspots on the hottest paths

These four are the audit's primary concern: extreme cyclomatic complexity **and**
top-tier churn. Bugs in these functions are the most expensive in the system.

| Function | CC | File | 6mo churn |
|---|---|---|---|
| `runDone` | **228** | `internal/cmd/done.go:397` | 252 (#2) |
| `runSling` | **203** | `internal/cmd/sling.go:342` | 244 (#3) |
| `(*Daemon).Run` | 77 | `internal/daemon/daemon.go:580` | 301 (#1) |
| `outputPatrolScanHuman` | 128 | `internal/cmd/patrol_scan.go:700` | — |

- **`runDone` (CC 228)** — the polecat completion path: push, rebase, MR submit,
  bead transition, sandbox teardown. The single most safety-critical control
  flow in the town, and the documented source of repeated production incidents
  (the "stuck-in-done loop" family: gu-hz3vx, gu-0x2be). The `gu-re9a3`
  extraction created `internal/polecat/completion/` (pre_verify, rebase,
  refinery_guard) but **`done.go` is still 3,851 lines** and `runDone` itself
  was not reduced. *Fix:* continue the started extraction — peel `runDone` into
  named phase functions (`resolveBranch`, `pushWithRetry`, `submitToMergeQueue`,
  `transitionBead`, `teardownSandbox`) into `internal/polecat/completion/`, each
  table-tested. → **bead filed**

- **`runSling` (CC 203)** — dispatch entry point. Much work is already delegated;
  the bulk is parse/validate/mode-branch (batch vs explicit-rig) that buries
  validation where it's hard to test. *Fix:* extract the prologue into
  `parseSlingInvocation() (SlingPlan, error)` so `runSling` is parse → plan →
  execute. → **bead filed**

- **`(*Daemon).Run` (CC 77, file is #1 churn)** — the always-on daemon loop;
  highest-churn file in the repo. *Fix:* decompose into per-responsibility tick
  handlers. → **bead filed**

- **The long tail:** 255 functions exceed CC 20; **25 exceed CC 50**. Full list
  in Appendix A. Beyond the four above, the worst are `(*Manager).AddRig` (101),
  `IsPatrolEnabled` (99, in `daemon/types.go` — a predicate should not be CC 99),
  `runInstall` (81), `executeSling` (81), `runMqSubmit` (80). These are
  individually lower-churn; recommend opportunistic refactor when touched, not a
  dedicated sweep.

### P2 — Dead code (78 unused symbols, verified)

Spot-checked and confirmed genuine (not build-tag or test-only false positives).
Clearest example: `internal/daemon/handler.go:559 func findDispatchableDog` — a
free function **superseded by the `(*Daemon).findDispatchableDog` method**; a
code comment literally says callers "should call (*Daemon).findDispatchableDog."
The old one was never deleted.

Notable clusters (full list Appendix B):

- **`internal/beads/beads.go`** — 6 dead funcs (`runWithRouting`,
  `isSubprocessCrash`, `buildRoutingEnv`, `translateDoltPort`,
  `overrideDoltEnvFromBeadsDir`, `doltConnectionFromBeadsDir`). Looks like an
  abandoned Dolt-routing refactor. **#6 churn file** — worth clearing so future
  edits aren't misled by dead routing helpers.
- **`internal/daemon/commit_guard.go`** — 4 of the file's functions
  (`guardDaemonCommit`, `currentBranch`, `branchExistsAtOrigin`, `runGitOutput`)
  are unused → the **entire commit-guard mechanism appears wired out**. Verify
  whether this is intentionally disabled (if so, the gap is a separate bug) or
  truly abandoned, then delete.
- **`internal/cmd/polecat.go`** — `reuseMRShower`, `activeMRBlocksReuse`,
  `polecatReuseStatus`, `mrFinder`, `applyMQCheck`, `nukePolecatFull` (6) — a
  dead "MR reuse" abstraction.
- **`internal/cmd/done.go`** — `autoSaveRefusalReason`, `autoSaveGit`.
- **`internal/witness/handlers.go`** — 5 dead (`agentBeadResponse`,
  `getAgentBeadState`, `getAgentBeadLabels`, `activeMRBlockerFromCLI`,
  `issueStatusFromShowJSON`).

**Caveat — 4 of the 78 are intentional test hooks**, not dead code, and should
be excluded from any auto-delete: `resetCrossRigEscalationStateForTest`,
`resetCapacityExhaustion`, `resetPushRecoveryBudget`,
`resetRedispatchLimitersForTest`. They read as unused because the lint ran with
`tests: false`; their callers live in `_test.go`. Recommend annotating with
`//nolint:unused // test reset hook` rather than deleting. → **bead filed** for
the genuine ~74.

### P2 — Duplication (17 distinct copy-paste pairs)

Highest-value targets (≥80-line clones — real logic, not boilerplate):

| Location | Span | Notes |
|---|---|---|
| `internal/reaper/hooked_mail.go` ↔ `processed_mail.go` | 130–296 / 384–538 / 227–314 | **Largest** — two mail-reaper paths are near-identical ~160-line blocks. Strong extract-shared-helper candidate. |
| `internal/util/orphan.go` | 734–825 ↔ 835–935 | ~90-line clone within one file. |
| `internal/doctor/config_check.go` | 627–708 ↔ 774–849 | ~80-line clone. |
| `internal/cmd/reaper.go` | 445–540 ↔ 544–642 ↔ 646–750 | **Three** ~95-line near-duplicate blocks. |
| `internal/config/loader.go` | 2399–2475 ↔ 2671–2734 | ~75 lines, in the #1 config file. |
| `internal/pushlog/failure.go` ↔ `pushlog.go` | 168–201 / 168–202 | Same logic split across two files in one package. |

Smaller pairs (40–50 lines): `mail_inbox.go`, `scheduler_convoy.go`↔
`scheduler_epic.go`, `polecat/manager.go`, `web/api.go`, `web/handler.go`,
`doctor/rig_check.go`, `doctor/claude_settings_check.go`. Full list Appendix C.
→ **bead filed** for the reaper/mail cluster (highest payoff).

### P3 — Package structure (internal/cmd is 121K LOC / 278 non-test files)

The bead asks to cross-reference epic **gu-q30qg**. That epic was decomposed into
4 extraction children — `gu-y5z8d` (dispatch), `gu-re9a3` (completion),
`gu-yju86` (sling), `gu-i84ew` (refinery) — **all CLOSED and landed**. So the
"big rocks" already have homes:

- `internal/dispatch/`, `internal/sling/`, `internal/polecat/completion/`,
  `internal/refinery/` now exist.
- **But the extractions were partial.** `done.go` (3,851 LOC) and `sling.go`
  (1,825 LOC) remain in `internal/cmd` and their god-functions
  (`runDone`/`runSling`) were not moved — the new packages got the *helpers*, not
  the *state machines*. This is the gap between gu-q30qg's intent and its
  result, and it's why the P1 complexity findings above persist.

**New extraction candidates not covered by gu-q30qg** (clear domain seams still
living entirely in the CLI layer):

1. **`internal/cmd/convoy*.go`** — `convoy.go` (3,804) + `convoy_stage.go`
   (2,290) + `convoy_stage_test.go` + helpers ≈ 13 files. Convoy lifecycle is a
   self-contained domain → `internal/convoy/`.
2. **`internal/cmd/mail_*.go` / `mail.go`** — 23 mail-prefixed files in the CLI
   layer; routing logic already half-lives in `internal/mail/`. Consolidate the
   command-layer mail logic into `internal/mail/`.
3. **`internal/cmd/dolt.go` (1,932)** — Dolt operational commands; domain home
   `internal/doltserver/` already exists.

Recommend these as **incremental, one-per-sprint** extractions following the same
pattern gu-q30qg used (and learning its lesson: **move the state machine, not
just the leaf helpers**). → **bead filed** as a follow-up epic note + convoy as
the first concrete child.

### Informational — error handling

- `go vet` is **clean** and there are **zero `panic()` calls** in non-test code —
  good baseline hygiene.
- 1,283 `_ = …` blank-assignments in non-test `internal/` code. The vast majority
  are legitimate (the `.golangci.yml` errcheck exclusion list documents the
  safe `Close()`/`Remove()`/`Setenv()` patterns). This is **not** flagged as a
  finding — calling it out only so a future auditor doesn't re-derive it. No
  systematic swallowed-error anti-pattern was found; errors are generally
  wrapped with `fmt.Errorf(...%w...)`.
- 5 `XXX` markers, 0 `TODO`/`FIXME`/`HACK` in non-test code. Low debt-marker load.

---

## Recommendation

**Act on P1 first.** The complexity is concentrated in `runDone`/`runSling` on
the two hottest, most incident-prone files. The extraction machinery (gu-q30qg's
packages) already exists — the work is finishing the job by moving the state
machines into it, which simultaneously addresses complexity *and* the package-
structure goal. Dead-code cleanup (P2) is low-risk, high-clarity, and a good
parallel track. Duplication (P2) and new extractions (P3) are opportunistic.

---

## Beads filed

All children of gu-nid89.12 (top extraction/cleanup targets per acceptance):

| Bead | P | Finding |
|---|---|---|
| `gu-nid89.12.1` | P1 | Finish `runDone` extraction (CC 228 → completion pkg) |
| `gu-nid89.12.2` | P1 | Reduce `runSling` CC 203 (extract `parseSlingInvocation`) |
| `gu-nid89.12.3` | P2 | Remove ~74 verified dead-code symbols (keep 4 test hooks) |
| `gu-nid89.12.4` | P2 | Deduplicate reaper mail clones (~160-line copy-paste) |
| `gu-nid89.12.5` | P3 | Extract `internal/convoy` from `internal/cmd` |

---

## Appendix A — All functions with CC > 50 (25 total)

```
228  runDone                              internal/cmd/done.go:397
203  runSling                             internal/cmd/sling.go:342
128  outputPatrolScanHuman                internal/cmd/patrol_scan.go:700
101  (*Manager).AddRig                    internal/rig/manager.go:323
 99  IsPatrolEnabled                      internal/daemon/types.go:265
 81  runInstall                           internal/cmd/install.go:96
 81  executeSling                         internal/cmd/sling_dispatch.go:153
 80  runMqSubmit                          internal/cmd/mq_submit.go:138
 77  runDown                              internal/cmd/down.go:96
 77  (*Daemon).Run                        internal/daemon/daemon.go:580
 70  scheduleBead                         internal/cmd/sling_schedule.go:123
 67  outputStatusText                     internal/cmd/status.go:1064
 66  runMQList                            internal/cmd/mq_list.go:19
 66  runCrewAt                            internal/cmd/crew_at.go:22
 65  updateAgentStateOnDone               internal/cmd/done.go:3296
 62  (*Daemon).reapWispsInline            internal/daemon/wisp_reaper.go:190
 60  Start                                internal/doltserver/doltserver.go:1864
 60  (*ClaudeSettingsCheck).findSettingsFiles  internal/doctor/claude_settings_check.go:172
 58  runRigAdopt                          internal/cmd/rig.go:1128
 58  runNudge                             internal/cmd/nudge.go:392
 57  fillRuntimeDefaults                  internal/config/loader.go:1969
 53  (*Engineer).doMerge                  internal/refinery/engineer.go:648
 52  getLifecycleConfig                   internal/cmd/config.go:1172
 52  gatherStatus                         internal/cmd/status.go:680
 50  runMailSend                          internal/cmd/mail_send.go:20
```
Distribution: CC 20-24: 82 · 25-29: 65 · 30-39: 57 · 40-49: 26 · 50+: 25.
Full 255-row list reproducible via the command in "How this was measured".

## Appendix B — Dead code (78 `unused`; ~74 genuine, 4 test hooks)

Genuine dead code by package:
```
internal/acp/proxy.go            setStreams, agentDone
internal/beads/beads.go          runWithRouting, isSubprocessCrash, buildRoutingEnv,
                                 translateDoltPort, overrideDoltEnvFromBeadsDir,
                                 doltConnectionFromBeadsDir
internal/cmd/agents.go           socketDisplayName
internal/cmd/agents_resolve.go   field fallback
internal/cmd/capacity_dispatch.go listAllScheduledBeadIDs, listBlockedWorkBeadIDs
internal/cmd/compact.go          isReferenced
internal/cmd/convoy.go           runBdJSONAllowStale
internal/cmd/done.go             autoSaveRefusalReason, type autoSaveGit
internal/cmd/escalate_impl.go    detectSenderFallback
internal/cmd/helpers.go          isShellCommand
internal/cmd/hooks_scan.go       parseHooksFile
internal/cmd/polecat.go          reuseMRShower, activeMRBlocksReuse, polecatReuseStatus,
                                 mrFinder, applyMQCheck, nukePolecatFull
internal/cmd/polecat_capacity.go invalidatePolecatCapacityCache,
                                 applyWorkstateDispositionToCapacitySnapshot
internal/cmd/prime_output.go     outputContinuationDirective
internal/cmd/quota.go            type quotaLogger, quotaLogger.Warn
internal/cmd/show.go             stripEnvKey
internal/cmd/sling_convoy.go     printConvoyConflict, createBatchConvoy
internal/cmd/sling_helpers.go    isAgentBead, isEmptyAssignee, bdShowBeadDirectCmd,
                                 bdShowBeadDirectCmdFromTownRoot, bdShowBeadRoutedCmd
internal/cmd/status.go           extractBaseName
internal/cmd/upstream_audit.go   auditCodeRegistry, sortedAuditCodes
internal/daemon/bd_env.go        bdReadOnlyEnv
internal/daemon/checkpoint_dog.go noNewTrackedChangesVsHEAD, headIsWIPCheckpoint
internal/daemon/commit_guard.go  guardDaemonCommit, currentBranch, branchExistsAtOrigin,
                                 runGitOutput   ← whole mechanism appears wired-out
internal/daemon/compactor_dog.go compactorBranchName
internal/daemon/handler.go       dogIdleSessionTimeout, dogIdleRemoveTimeout,
                                 staleWorkingTimeout, maxDogPoolSize, findDispatchableDog
internal/daemon/jsonl_git_backup.go parseLineCount
internal/daemon/lifecycle.go     syncFailureEscalationThreshold
internal/daemon/main_branch_test_runner.go mainBranchTestRigs
internal/doltserver/doltserver.go (*Config).userDSN
internal/git/git.go              countCommitsAhead, unpushedFromExactRemoteBranch,
                                 resolveRemoteBaseline
internal/mail/router.go          isTownLevelAddress
internal/rig/manager.go          isStandardBeadHash
internal/session/lifecycle.go    mapKeysSorted
internal/testpathmap/testpathmap.go (*Store).withClock
internal/util/orphan.go          isInGasTownWorkspace
internal/wisp/io.go              writeJSON
internal/witness/handlers.go     agentBeadResponse, getAgentBeadState, getAgentBeadLabels,
                                 activeMRBlockerFromCLI, issueStatusFromShowJSON
internal/witness/slot_open_coalescer.go flushNotifyCh
```
Intentional test hooks — DO NOT delete (annotate `//nolint:unused`):
```
internal/cmd/capacity_dispatch.go        resetCrossRigEscalationStateForTest, resetCapacityExhaustion
internal/witness/push_failed_recovery.go resetPushRecoveryBudget
internal/witness/redispatch_rate_limiter.go resetRedispatchLimitersForTest
```

## Appendix C — Duplicate blocks (17 distinct pairs, ≥150 tokens)

```
internal/reaper/hooked_mail.go:130-296  ↔ :384-538          (~160 lines, ×2 families)
internal/reaper/hooked_mail.go:174-266  ↔ processed_mail.go:227-314
internal/cmd/reaper.go:445-540 ↔ :544-642 ↔ :646-750        (3-way, ~95 lines each)
internal/util/orphan.go:734-825 ↔ :835-935                  (~90 lines)
internal/doctor/config_check.go:627-708 ↔ :774-849          (~80 lines)
internal/doctor/config_check.go:718-751 ↔ :852-887
internal/config/loader.go:2399-2475 ↔ :2671-2734            (~75 lines)
internal/pushlog/failure.go:168-201 ↔ pushlog.go:168-202    (cross-file, same pkg)
internal/polecat/manager.go:815-858 ↔ :1021-1070
internal/doctor/rig_check.go:435-488 ↔ :598-651
internal/doctor/claude_settings_check.go:297-345 ↔ :349-397
internal/web/api.go:1392-1433 ↔ :1670-1711
internal/web/handler.go:99-124 ↔ :132-156
internal/cmd/mail_inbox.go:367-403 ↔ :667-703
internal/cmd/scheduler_convoy.go:235-265 ↔ scheduler_epic.go:235-265
internal/cmd/reaper.go:211-242 ↔ :380-411
```

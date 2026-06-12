# Bug Hunt Audit — `internal/cmd/`

- **Bead:** gu-nid89.1 (epic gu-nid89: Whole-Repo Gastown Audit)
- **Date:** 2026-06-11
- **Auditor:** polecat ghoul (gastown_upstream)
- **Scope:** `internal/cmd/` — 278 non-test Go source files, ~121K LOC
- **Method:** `go vet` (clean) + staticcheck (unrunnable: dep/toolchain Go-version skew) +
  8 parallel deep-read sub-audits over functional clusters, followed by orchestrator
  re-verification of every HIGH/MED candidate against the actual source (and one
  empirical `bd list` behavior test).

## Summary

| Confidence | Count |
|------------|-------|
| HIGH (verified, clearly wrong) | 4 |
| MED (likely bug, context-dependent) | 16 |
| LOW (suspicious / latent) | 9 |

`go vet ./internal/cmd/` is clean. The package is generally careful — most candidate
races/leaks were ruled out (proper WaitGroup happens-before, flock discipline,
buffered fan-out channels, per-name file locks). The findings below survived
skeptical re-tracing.

New beads were filed for the **4 confirmed HIGH bugs** (per acceptance criteria):
gu-d6q99 (H1), gu-gxyc3 (H2), gu-h63md (H3), gu-any3k (H4).

---

## HIGH — confirmed, beads filed

### H1. Checkpoint step title is always discarded — `gu-d6q99`
- **File:** `internal/cmd/checkpoint_cmd.go:127-135` (+ `internal/checkpoint/checkpoint.go:164-168`)
- **Bug:** When molecule context is auto-detected, line 128 stores the detected
  `stepTitle` via `cp.WithMolecule(mol, step, stepTitle)`, but lines 133-135 then
  unconditionally call `cp.WithMolecule(mol, step, "")` whenever `checkpointMolecule != ""`.
  `WithMolecule` assigns `cp.StepTitle = stepTitle` unconditionally, so the second call
  clobbers the real title back to `""`.
- **Impact:** Every checkpoint written by a polecat/crew worker loses its step title.
  `gt checkpoint read` / crash-recovery consumers relying on `StepTitle` get nothing.
  Silent data loss in the checkpoint payload.
- **Verified:** Read both sites; `WithMolecule` assignment is unconditional. The `if
  stepTitle != ""` guard at 127 is fully defeated by the unguarded 133-135 block.
- **Fix sketch:** Fold the title into the single `WithMolecule` call (pass the detected
  `stepTitle` instead of `""`), or drop the redundant 133-135 block.

### H2. `gt compact report --weekly` re-fires every run (missing `--status=closed`) — `gu-gxyc3`
- **File:** `internal/cmd/compact_report.go:635-639` (vs daily path `:605`)
- **Bug:** The weekly rollup audit bead is created and immediately auto-closed (`:702-705`).
  But `findExistingWeeklyRollup` queries `bd list --type=event --json --limit=20` with **no
  `--status` filter**, while the sibling `findExistingCompactReport` correctly passes
  `--status=closed`. `bd list` defaults to open-only, so the closed weekly bead is invisible
  to its own idempotency check.
- **Impact:** Every `gt compact report --weekly` invocation believes no rollup exists:
  re-aggregates, re-creates an audit bead, and **re-sends the weekly rollup mail to
  `mayor/`** — once per patrol cycle. Duplicate mail + duplicate audit beads.
- **Verified empirically:** `bd list --type=event --json` → 0 results;
  `bd list --type=event --status=closed --json` → 1 result. Confirms the default excludes
  closed events.
- **Fix sketch:** Add `--status=closed` to the weekly query (match the daily path).
  (`queryCompactionReports` at `:494-499` shares the same omission and should be checked too.)

### H3. Agent runtime line bypasses the output writer in `gt status` — `gu-h63md`
- **File:** `internal/cmd/status.go:1378`
- **Bug:** `renderAgentDetails(w io.Writer, …)` writes every line via `fmt.Fprintf(w, …)`
  except the agent-runtime line, which uses `fmt.Printf(…)` — writing directly to
  `os.Stdout` instead of `w`.
- **Impact:** In `--watch` mode the whole frame is composed in a `bytes.Buffer` and written
  atomically after an ANSI clear-screen; this line escapes the buffer and is emitted
  out-of-order, corrupting the watch display. Also misroutes output for any non-stdout
  writer (the function's entire reason for taking `w`).
- **Verified:** Read the function; surrounding lines all use `fmt.Fprintf(w, …)`, only 1378
  uses `fmt.Printf`.
- **Fix sketch:** `fmt.Fprintf(w, "%s  agent: %s\n", indent, agent.AgentInfo)`.

### H4. `validateMoleculePrereqs` picks wrong submit step (dead `break` kills min-search) — `gu-any3k`
- **File:** `internal/cmd/mq_submit.go:566-578`
- **Bug:** The loop intends to find the submit step with the lowest sequence, but `break`s on
  the **first** child whose title contains "submit". The `if seq < submitSeq` minimization is
  therefore dead code — `submitSeq` is taken from the first matching child in arbitrary
  `children` order.
- **Impact:** When a molecule has >1 step with "submit" in the title (e.g. "submit" +
  "resubmit-on-fail") or `children` isn't sequence-ordered, the prerequisite boundary is
  wrong: enforcement can be too lax (lets a polecat submit with incomplete required steps)
  or too strict (wrongly blocks a valid submit).
- **Verified:** Read the loop; the `break` is unconditional inside the `if Contains`, so only
  the first match is ever considered.
- **Fix sketch:** Remove the `break` so the loop scans all "submit" steps for the minimum.

---

## MED — likely bugs, worth maintainer review

### M1. `gt convoy launch` wedges the convoy `open` on any post-transition failure
- **File:** `internal/cmd/convoy_launch.go:290-319`
- **Bug:** `transitionConvoyToOpen` flips `staged_* → open` first, then `collectConvoyBeads`,
  `computeWaves`, `FindFromCwdOrError`, and `checkBlockedRigsForLaunch` run. Any failure
  there returns an error with the convoy already `open`. `validateConvoyStatusTransition`
  rejects `open → staged_*` (convoy.go), so it can't be re-staged; a retry hits "already
  launched" and Wave 1 never dispatches.
- **Impact:** State corruption / wedged convoy needing manual `bd` surgery. The blocked-rig
  check (a normal, expected condition without `--force`) reliably triggers this since it fires
  *after* the status flip.
- **Verified:** Confirmed ordering at 290-313 and that `open→staged` is rejected in
  `validateConvoyStatusTransition`.
- **Fix sketch:** Run the fallible checks (collect/compute/blocked-rigs) *before*
  `transitionConvoyToOpen`, or roll back to staged on dispatch failure.

### M2. `--max-concurrent` batch throttle pauses only ~6s (broken loop)
- **File:** `internal/cmd/sling_batch.go:137-142`
- **Bug:** `for wait := 0; wait < 30; wait++ { time.Sleep(2s); if wait >= 2 { break } }`
  always breaks on the 3rd iteration → fixed ~6s; the `< 30` bound (~60s) is dead.
- **Impact:** Spawn-rate throttle does far less than intended, weakening the Dolt-overload
  protection the flag exists to provide.
- **Verified:** Read the loop. Behavior is a fixed 3×2s sleep.

### M3. `gt assign --force` flag is declared but never consulted
- **File:** `internal/cmd/assign.go:62` (declared), body never reads `assignForce`
- **Bug:** `--force` ("Replace existing hooked work") is registered but never read; the hook
  step runs unconditionally with no guard against clobbering existing hooked work.
- **Impact:** `--force` is a no-op; `gt assign` silently reassigns over existing hooked work
  with no warning, contradicting the flag's purpose.
- **Verified:** `grep assignForce assign.go` → only declaration + flag registration.

### M4. E-stop safety check bypassed when town root resolves via env, not CWD
- **File:** `internal/cmd/mail_check.go:80-91`
- **Bug:** Inject-mode E-stop check uses `workspace.FindFromCwd()`, which returns `("", nil)`
  when CWD has no town marker. `twErr == nil` is then true with `townRoot == ""`, so
  `estop.IsActive("")` runs against a CWD-relative path and essentially always reports "not
  active". The correct `workDir` from `findMailWorkDir()` (which honors `GT_TOWN_ROOT`) is
  ignored — exactly the common polecat case.
- **Impact:** Agents whose CWD lacks a town marker silently skip the E-stop reminder during
  `gt mail check --inject`.
- **Fix sketch:** Use the already-computed `workDir` instead of re-resolving via `FindFromCwd`.

### M5. Channel nudges skip the `InitRegistry` socket fallback
- **File:** `internal/cmd/nudge.go:477-490`
- **Bug:** `runNudge` returns `runNudgeChannel(...)` for `channel:` targets *before* the
  `session.InitRegistry(townRoot)` fallback that fixes the tmux socket in non-agent contexts.
- **Impact:** `gt nudge channel:<name>` from a context where CWD-based detection failed
  connects to the wrong tmux socket and finds no sessions, while direct/role nudges (which get
  the fallback) work.

### M6. DAG tier computation drops nodes with blocking deps outside the molecule
- **File:** `internal/cmd/molecule_dag.go:160-165, 213-262`
- **Bug:** `buildDAG` adds blocking dep IDs to `node.Dependencies` without checking they're in
  `dag.Nodes`. `computeTiers` seeds `inDegree = len(Dependencies)` but only decrements via
  in-molecule `Dependents`. An external blocking dep never decrements → in-degree stays > 0 →
  Kahn loop hits "cycle detected" `break` and drops those nodes + downstream.
- **Impact:** `gt mol dag` renders an incomplete/incorrect DAG whenever a step blocks on a bead
  outside the molecule. Display-only, but misleading.

### M7. `gt dolt flatten` runs the whole flatten (incl. `DOLT_COMMIT`) under one 10s context
- **File:** `internal/cmd/dolt_flatten.go:83-156`
- **Bug:** A single 10s context covers SELECT/count/USE/`DOLT_RESET --soft`/`DOLT_COMMIT`. The
  commit of a freshly soft-reset working set on a large DB — exactly what this "nuclear option"
  targets — can exceed 10s. Contrast `dolt_rebase.go:102` which uses a 10-minute context.
- **Impact:** On large DBs the deadline can fire after the soft-reset moved the branch pointer,
  leaving the DB partially flattened (staged, uncommitted on `main`) needing manual recovery.

### M8. Cascade close silently swallows `bd children` failures
- **File:** `internal/cmd/close.go:167-173`
- **Bug:** Any error from `childCmd.Output()` → `return nil` (treated as "no children"); only a
  non-zero `*exec.ExitError` prints a warning, and even that still returns nil.
- **Impact:** `gt close --cascade` hitting a transient/real query failure on an intermediate
  node skips that whole subtree, leaves those children open, and still reports success.

### M9. Reaper per-DB sub-passes omit the Dolt throttle used elsewhere
- **File:** `internal/cmd/reaper.go` (`reap-hooked-mail` :476, `reap-open-mail` :578,
  `reap-processed-mail` :686, `close-plugin-receipts` :784, `flush-wisps` :878)
- **Bug:** These loops iterate every DB opening a connection per DB with no
  `waitBeforeReaperDatabase(i)` delay, unlike `scan`/`reap`/`purge`/`auto-close`/`run`.
- **Impact:** Burst-queries every Dolt DB back-to-back — a load risk given Dolt's documented
  fragility. Operational, not data-corrupting.

### M10. `--skip-push` records a successful upstream sync that never reached origin
- **File:** `internal/cmd/upstream_sync.go:317-340`
- **Bug:** With `--skip-push`, the code prints the skip notice then falls into the success
  block: sets `LastSyncOutcome="success"`, `LastSyncSHA`, resets `ConsecutiveFailures=0`.
- **Impact:** State bead reports fully-synced when origin was never updated; later
  status/check-due/audit believe the fork is in sync.

### M11. FF ancestry computed against `origin/<branch>` but merge runs on local `<branch>`
- **File:** `internal/cmd/upstream_sync.go:150-151, 214, 467-509`
- **Bug:** Divergence/conflict/FF decisions use `origin/<target>`; the actual merge checks out
  the *local* `<target>` and merges into it. If local ≠ origin, the merge base differs from the
  analyzed one.
- **Impact:** A run deemed FF-able can fail `merge --ff-only`, or the clean-merge path can
  diverge from the reported conflict status — recorded as error and tripping the circuit
  breaker. Reliable only when local target == origin/target.

### M12. Synthesis reports "ready" and proceeds with zero legs
- **File:** `internal/cmd/synthesis.go:450-508, 132-186`
- **Bug:** `collectLegOutputs` starts `allComplete=true` and only flips false for non-closed
  tracked legs. A convoy with no tracked legs and no formula output files returns
  `([], true)`. `runSynthesisStart` then creates+slings a synthesis bead with empty inputs.
- **Impact:** Synthesis can be launched for a convoy with no completed work.

### M13. Data-loss race in `gt costs digest` between read and delete of costs.jsonl
- **File:** `internal/cmd/costs.go:1132` (read) → `:1325` (`deleteSessionCostEntries`)
- **Bug:** Digest reads the day's entries (one ReadFile), builds the bead, then
  `deleteSessionCostEntries` independently re-reads and rewrites the file, dropping the day's
  lines. No lock. A `gt costs record` append for the same day between read and delete is lost.
- **Impact:** Silent loss of cost records (e.g. a late session recording after midnight against
  yesterday during `digest --yesterday`).

### M14. `runConvoyAdd` reports the wrong issue IDs when some additions fail
- **File:** `internal/cmd/convoy.go:1061-1077`
- **Bug:** `addedCount` counts successes, but the summary prints `issuesToAdd[:addedCount]` —
  the first N input elements, not the ones that succeeded. If the first fails and second
  succeeds, it prints the failed one as "Added".
- **Impact:** Misleading output only (tracking relations themselves are correct).

### M15. `git -c <config> push --force` bypasses the dangerous-command force-push guard
- **File:** `internal/cmd/tap_guard_dangerous.go:218-242`
- **Bug:** `matchesDangerousGitPush` only sets `hasPush` when `fields[i-1] == "git"`. With a
  config prefix (`git -c http.proxy=x push --force`) the token before `push` is the config
  value, so the `--force` check is never reached — guard fails open.
- **Impact:** A force push executes when invoked with any `git -c …`/`git -C …` prefix. The
  codebase trains agents to use `git -c` (see `runGitCommit`), making this evasion plausible.
- **Verified:** `"git -c http.proxy=x push --force"` → allowed; `"git push --force"` → blocked.

### M16. `gt status` stale-comment / arg-handling fall-through in `runSling`
- **File:** `internal/cmd/sling.go:961-1007`
- **Bug:** Comments cite wrong line numbers for the batch-exit invariant; more substantively, a
  3+ arg invocation whose last token is neither a rig nor all-bead-IDs falls through and uses
  only `args[1]` as target, silently dropping `args[2:]` instead of erroring.
- **Impact:** Silent wrong-branch on a typo'd multi-arg sling rather than a clear error.

---

## LOW — latent / edge-case / cosmetic

- **L1.** `quiesceMailbox` (`polecat_reuse_mail.go:45-46`) — no nil-check on `msg` before
  `msg.ID`, unlike the defensive sweep in `polecat_work.go:245`. Panic if `ListUnread` yields
  nil entries.
- **L2.** Kiro wrapper (`polecat_kiro_wrapper.go:356-386`) — a timeout-kill returning a
  non-`*exec.ExitError` is misclassified as a hard spawn failure instead of a retry.
- **L3.** `handleMoleculeComplete` (`molecule_step.go:444`) — builds agent identity without the
  empty-string guard its sibling `handleStepContinue` has; empty assignee could unpin the wrong
  bead.
- **L4.** `findOrphanDoltServers` (`down.go:896`) — `strings.HasPrefix(cwdAbs, canonicalDir)`
  lacks a path-separator boundary, so a sibling `.dolt-data-backup` is treated as canonical and
  skipped (guard errs toward not-killing).
- **L5.** `gt worktree` (`worktree.go:185-188`) — sets local `user.name` but never `user.email`
  despite the "identity preservation" comment; cross-rig commits are half-attributed.
- **L6.** `runOrphansKill` (`orphans.go:692-696`) — non-`--force` path sends only SIGTERM with no
  SIGKILL escalation or liveness re-check, yet prints "killed".
- **L7.** `gt quota watch --interval 0` (`quota.go:854`) — `time.NewTicker(0)` panics; no flag
  validation.
- **L8.** `capitalizeFirst` (`status.go:1682-1687`) — `string(s[0]-32)` corrupts non-lowercase-
  ASCII / multi-byte first chars (current agent names are lowercase ASCII, so benign today).
- **L9.** Dedup occurrence-count bump (`escalate_impl.go:132-146`) — `BumpOccurrenceCount` error
  discarded while reporting a locally-computed `newCount`; reported count can diverge from
  stored on failure. Also `wl_done.go:109-134` local-clone resubmit can report success without
  inserting a completion (inconsistent `claimed` vs `in_review` guards).

---

## Categories explicitly checked and found clean

- **Race conditions:** capacity snapshot fan-out, `getWorkersForIssues`, `runBoundedScan`,
  status prefetch, crew parallel start — all correctly mutex-guarded / buffered.
- **Resource leaks:** flock/admission-handle/keepalive cancels and `sql.DB` handles are
  `defer`-released on all traced paths.
- **`done.go` goto ladder:** compiles clean, all paths reach `teardownAfterDone` with
  consistent state.
- **SQL handling:** bead IDs validated before interpolation; `wl_*` local-clone paths use
  `doltserver.EscapeSQL`.

## Sources

- `internal/cmd/` source tree — accessed 2026-06-11
- `go vet ./internal/cmd/` (clean) and `bd list --type=event [--status=closed]` behavior test
  — accessed 2026-06-11

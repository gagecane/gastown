# Upstream PR Candidates — `gastownhall/gastown`

- **Bead:** gu-nid89.13 (parent epic gu-nid89: Whole-Repo Gastown Audit)
- **Date:** 2026-06-11
- **Auditor:** polecat ghoul (gastown_upstream)
- **Fork audited:** `github.com/gagecane/gastown` @ `a9afe585`
- **Upstream target:** `github.com/gastownhall/gastown` @ `ccd16c18` (`upstream/main`, 2026-06-11)

---

## Method & scope

This bead synthesizes the completed audit-dimension reports into concrete, upstreamable
PR proposals. Its distinct value-add over the source reports is the **upstreamability
gate**: our fork is 1140 commits behind / 8 ahead of `upstream/main`, so a bug found in
the fork is not automatically present (or fixable) upstream. **Every proposal below was
re-verified against the actual `upstream/main` source** (`git show upstream/main:<file>`),
and the line numbers cited are the *upstream* line numbers, not the fork's.

### Source reports synthesized

| Report | Bead | Status | Findings used |
|--------|------|--------|---------------|
| `bugs-cmd.md` | gu-nid89.1 | ✅ closed | H1–H4 (HIGH), M2, M15 |
| `perf-runtime.md` | gu-nid89.11 | ✅ closed | C1 (HIGH), F1 (HIGH) |
| `security-injection.md` | gu-nid89.10 | ✅ closed | #1, #2/#6 (MEDIUM) |

### ⚠️ Coverage caveat — this is a *partial* synthesis

Only **3 of the ~10 audit dimensions had filed reports** when this ran. Still **open**:
bug-hunt for `daemon` (gu-nid89.2), `beads/doltserver` (.3), `polecat/witness/reaper`
(.4), `mail/nudge/sling/dispatch` (.5), `config/doctor/hooks/formula/refinery` (.6);
documentation (.7/.8), security-secrets (.9), code-quality (.12), telemetry (.14), log
archaeology (.15/.16), and the two adversarial reviews (.17/.18). When those land, this
report should be **re-run** to fold in their findings. The 10 proposals below already
exceed the acceptance bar (≥5), but are not the complete upstream-PR set the epic will
eventually yield.

### What was deliberately excluded

- **Findings in fork-only files.** e.g. perf Q1 (`internal/reaper/orphan_reconcile_git.go`
  N+1 `bd.Show()`, bead gu-nid89.21) — that file **does not exist in `upstream/main`**, so
  it is not upstreamable. Filed for our fork only.
- **By-design / INFO findings.** Security #5 (`sh -c` gate commands — inside the trust
  boundary by design) and #8 (`govulncheck` tooling, environmental).
- **Display-only MED nits** with low acceptance odds were folded into a single optional
  cleanup PR (#10) rather than spread across many.

---

## Prioritized PR proposals

Ordered by (correctness/security impact) × (acceptance likelihood). The first five are
small, self-contained, high-confidence fixes — the strongest upstream candidates.

---

### PR 1 — fix: ConvoyManager WaitGroup undercount lets shutdown race a live scan

- **Priority:** P1 (correctness — lifecycle race)
- **Branch:** `fix/convoy-manager-waitgroup-undercount`
- **Source:** perf-runtime C1 · fork bead gu-nid89.19
- **Affected files:** `internal/daemon/convoy_manager.go` (upstream :153–158)

**Problem.** `Start()` calls `m.wg.Add(2)` but then launches **three** goroutines:
```go
m.wg.Add(2)
go m.runEventPoll()
go m.runStrandedScan()
go m.runStartupSweep()   // third goroutine — not counted
```
`runStartupSweep` does not `wg.Add(1)` itself, so `Stop()`'s `wg.Wait()` can return while
`runStartupSweep` is still sleeping on its startup timer and about to call `m.scan()`. A
scan firing *after* the manager believes it has stopped can fork `bd`/`gt sling`
subprocesses during shutdown.

**Fix.** Change `m.wg.Add(2)` → `m.wg.Add(3)` (or add `m.wg.Add(1)` immediately before the
`go m.runStartupSweep()` line). One line.

**Test plan.** Unit test that starts the manager, immediately calls `Stop()`, and asserts
no `scan()` runs after `Stop()` returns (inject a counter/flag the startup sweep sets, or a
fake clock; assert `Wait()` does not return until all three goroutines exit). Verify the
race with `go test -race`.

**Upstreamability: HIGH.** Confirmed verbatim in `upstream/main`. One-line, low-risk,
clear correctness win. Strongest candidate.

---

### PR 2 — fix: `gt status` agent-runtime line bypasses output writer (corrupts `--watch`)

- **Priority:** P2 (correctness — output corruption)
- **Branch:** `fix/status-agent-runtime-writer`
- **Source:** bugs-cmd H3 · fork bead gu-h63md
- **Affected files:** `internal/cmd/status.go` (upstream :1281)

**Problem.** `renderAgentDetails(w io.Writer, …)` writes every line via `fmt.Fprintf(w, …)`
except the agent-runtime line, which uses `fmt.Printf(…)` — going straight to `os.Stdout`,
ignoring `w`. In `--watch` mode the frame is composed in a `bytes.Buffer` and written
atomically after an ANSI clear-screen; this one line escapes the buffer and prints
out-of-order, corrupting the watch display. It also misroutes output for any non-stdout
writer.

**Fix.** `fmt.Printf("%s  agent: %s\n", indent, agent.AgentInfo)` →
`fmt.Fprintf(w, "%s  agent: %s\n", indent, agent.AgentInfo)`.

**Test plan.** Call `renderAgentDetails` with a `bytes.Buffer` writer and an agent that has
runtime info; assert the buffer contains the `agent:` line (today it does not — it goes to
stdout). Quick, deterministic.

**Upstreamability: HIGH.** Confirmed at upstream `status.go:1281`. One-line, obviously
correct (every sibling line uses `w`).

---

### PR 3 — fix: `gt compact report --weekly` re-fires every run (idempotency query missing `--status=closed`)

- **Priority:** P1 (duplicate side-effects — re-sends mail + re-creates beads)
- **Branch:** `fix/compact-weekly-rollup-idempotency`
- **Source:** bugs-cmd H2 · fork bead gu-gxyc3
- **Affected files:** `internal/cmd/compact_report.go` (upstream :632–639; check also the
  `queryCompactionReports` path)

**Problem.** The weekly rollup bead is created and immediately auto-closed. But
`findExistingWeeklyRollup` queries `bd list --type=event --json --limit=20` with **no
`--status` filter**, while the sibling daily check `findExistingCompactReport` correctly
passes `--status=closed` (upstream :604–605). `bd list` defaults to open-only, so the
closed weekly bead is invisible to its own idempotency check — every invocation believes no
rollup exists, re-aggregates, re-creates an audit bead, and **re-sends the weekly rollup
mail to `mayor/`**, once per patrol cycle.

**Fix.** Add `"--status=closed"` to the `findExistingWeeklyRollup` query args (match the
daily path). Audit `queryCompactionReports` for the same omission.

**Test plan.** The fork report verified empirically: `bd list --type=event --json` → 0
results; `bd list --type=event --status=closed --json` → 1 result. Add a unit/integration
test that closes a synthetic weekly-rollup event then asserts `findExistingWeeklyRollup`
returns its ID (today returns empty).

**Upstreamability: HIGH.** Confirmed at upstream `compact_report.go:636` (no `--status`),
contrasted with `:604–605` (daily path has it). Self-contained, high user-visible value
(stops duplicate mail spam).

---

### PR 4 — fix: checkpoint step title always discarded (`WithMolecule(…, "")` clobbers detected title)

- **Priority:** P1 (silent data loss in checkpoint payload)
- **Branch:** `fix/checkpoint-step-title-clobber`
- **Source:** bugs-cmd H1 · fork bead gu-d6q99
- **Affected files:** `internal/cmd/checkpoint_cmd.go` (upstream :120–134); confirm
  `WithMolecule` in `internal/checkpoint/checkpoint.go` assigns `StepTitle` unconditionally

**Problem.** When molecule context is auto-detected:
```go
if stepTitle != "" {
    cp.WithMolecule(checkpointMolecule, checkpointStep, stepTitle)   // :128 — sets title
}
...
cp.WithMolecule(checkpointMolecule, checkpointStep, "")              // :134 — clobbers it back to ""
```
The unguarded second call (fires whenever `checkpointMolecule != ""`) overwrites
`StepTitle` with `""`. Every checkpoint written by a polecat/crew worker loses its step
title; `gt checkpoint read` / crash-recovery consumers get nothing.

**Fix.** Pass the detected `stepTitle` into the single `WithMolecule` call (or drop the
redundant second block). Care: preserve behavior when `stepTitle` is genuinely empty.

**Test plan.** Unit test: set a molecule + step + non-empty detected title, write a
checkpoint, read it back, assert `StepTitle` is preserved (today it's `""`). Add a second
case with empty title to confirm no regression.

**Upstreamability: HIGH.** Confirmed at upstream `checkpoint_cmd.go:127–134`. Small, clear
data-loss fix.

---

### PR 5 — fix: `validateMoleculePrereqs` picks wrong submit step (dead `break` kills min-search)

- **Priority:** P2 (incorrect merge-queue prerequisite enforcement)
- **Branch:** `fix/mq-submit-step-min-search`
- **Source:** bugs-cmd H4 · fork bead gu-any3k
- **Affected files:** `internal/cmd/mq_submit.go` (upstream :427–446)

**Problem.** The loop intends to find the submit step with the **lowest** sequence:
```go
submitSeq := 999999
...
if strings.Contains(titleLower, "submit") {
    if seq < submitSeq { submitSeq = seq }
    break                                   // <-- breaks on FIRST match
}
```
The `break` is unconditional inside the `if Contains`, so only the **first** "submit"
child (in arbitrary `children` order) is ever considered — the `seq < submitSeq`
minimization is dead code. With >1 submit-ish step (e.g. "submit" + "resubmit-on-fail") or
unordered children, the prerequisite boundary at `:446` (`if seq >= submitSeq`) is wrong:
enforcement can be too lax (allows submit with incomplete required steps) or too strict
(blocks a valid submit).

**Fix.** Remove the `break` so the loop scans all "submit" steps for the true minimum.

**Test plan.** Unit test with a molecule whose children include two submit-titled steps out
of sequence order; assert the chosen `submitSeq` is the minimum (today it's the first
encountered). Add a single-submit case to confirm no regression.

**Upstreamability: HIGH.** Confirmed at upstream `mq_submit.go:427–446`. Small, correct,
but needs a reviewer who understands the intended prereq semantics — include a clear repro
in the PR body.

---

### PR 6 — perf: eliminate per-candidate `ps` fork storm in orphan/zombie detection

- **Priority:** P2 (fork storm on busy hosts)
- **Branch:** `perf/orphan-detection-single-ps`
- **Source:** perf-runtime F1 · fork bead gu-nid89.20
- **Affected files:** `internal/util/orphan.go` (upstream: `isRealAgentProcess` /
  `isIDEClaudeProcess` at :337+, called per-candidate from :497, :616; 7 `exec.Command`
  sites in the file)

**Problem.** `FindOrphanedClaudeProcesses` / `FindZombieClaudeProcesses` iterate every
candidate process and, per candidate, call `isRealAgentProcess(pid)` and
`isIDEClaudeProcess(pid)` — **each forks `ps -p <pid> -o args=`**. With N candidates that's
up to 2N extra `ps` forks on top of the initial listing. On a busy host (this codebase's
own learnings cite 300+ zombies and load spikes >1000) that is a meaningful fork storm.
Called from deacon orphan patrol, `cmd/orphans`, `cmd/cleanup`, `start_orphan_unix`.

**Fix.** Fork `ps -eo pid,args` **once** before the loop, build a `map[pid]argv`, and have
both helpers look up the map instead of re-forking per pid. Eliminates the 2N forks.

**Test plan.** Refactor the helpers to accept an injected `map[int]string` (or a small
interface) so tests don't fork at all; assert classification matches the current per-pid
behavior across a table of argv strings. Keep the single-`ps` builder behind a thin
seam for unit-testability.

**Upstreamability: HIGH.** Confirmed in `upstream/main`. Performance-only, no behavior
change — easy to justify. Slightly larger diff than #1–#5, so land it after the one-liners.

---

### PR 7 — security: validate Dolt DB name before SQL interpolation in `RemoveDatabase`

- **Priority:** P2 (SQL injection — MEDIUM per fork threat model)
- **Branch:** `fix/doltserver-validate-dbname`
- **Source:** security-injection #2 + #6 (single fix) · fork bead gu-zl25s
- **Affected files:** `internal/doltserver/doltserver.go` (upstream :3313 `DELETE … WHERE
  database = '%s'`; plus the backtick-quoted `DROP DATABASE`/`dolt_log` sites)

**Problem.** `dbName` flows from `os.ReadDir(.dolt-data)` entry names and is interpolated
into SQL with **no escaping**:
```go
serverExecSQL(townRoot, fmt.Sprintf("DELETE FROM dolt_branch_control WHERE `database` = '%s'", dbName))
```
The package already has `validSQLName()` and `EscapeSQL()` but neither is applied here
(`validSQLName` count in upstream `doltserver.go` = 0 at these sites). A directory under
`.dolt-data/` named `x'; DROP DATABASE foo;--` (single quotes are legal in Unix filenames)
would execute as SQL on the shared data plane during cleanup, which runs with the Dolt
server's privileges. MEDIUM (not CRITICAL) because creating that dir requires filesystem
write as the user — but it crosses from filesystem-name to SQL, and violates the package's
own escaping discipline.

**Fix.** Call `validSQLName(dbName)` at `RemoveDatabase` entry (reject anything outside
`^[A-Za-z0-9_-]+$`); this covers both the string-literal (#2) and backtick-identifier (#6)
sites. Validation is preferable to escaping since identifiers can't be parameterized.

**Test plan.** Unit test `RemoveDatabase` (or the validation helper) with names containing
`'`, `` ` ``, `;`, `--`, whitespace → expect rejection before any SQL runs; valid names
pass through. No live Dolt needed if the validation is factored to a pure function.

**Upstreamability: MEDIUM-HIGH.** Confirmed in `upstream/main`. Reuses an existing helper,
self-contained. Frame the PR around the trust model (defense-in-depth + consistency) so
severity isn't over-claimed.

---

### PR 8 — security: reject leading-`-` / control chars in git refs at the `internal/git` choke point

- **Priority:** P2 (git flag-injection — MEDIUM, defense-in-depth at an external boundary)
- **Branch:** `fix/git-ref-validation`
- **Source:** security-injection #1 · fork bead gu-n5dvk
- **Affected files:** `internal/git/git.go` (upstream: `Checkout` :876, `Push` :972,
  `Merge` :1359, `MergeFFOnly`/`MergeSquash`, `Fetch*` family)

**Problem.** The `run`/`runRaw` wrapper uses `exec.Command("git", args...)` (no shell — no
classic injection), but git parses any positional arg beginning with `-` as an **option**.
A ref named `--upload-pack=<cmd>`, `--output=<path>`, etc. changes the command's behavior.
There is no centralized `validateGitRef()` and no `--` end-of-options separator in these
wrappers. The elevated concern is `internal/upstreamsync/` and the GitHub/Bitbucket
integrations, where **branch/ref names originate from a remote repo** the operator doesn't
fully control. No confirmed end-to-end exploit path was found, but no guard exists either.

**Fix.** Add `validateGitRef(s string) error` (reject empty, leading `-`, control chars,
newlines) and call it in every wrapper that accepts a branch/ref/remote from a non-constant
source; and/or insert `"--"` before positional ref args where the subcommand supports it.
Mirror the existing `polecat.ValidatePoolName` / `dog.validateDogName` pattern.

**Test plan.** Unit test feeding `"--upload-pack=x"`, `"-o"`, `"\n"`, empty → expect
rejection; valid refs like `polecat/x/gu-1--mqa1` pass. Add a regression test on at least
one externally-fed path (upstreamsync).

**Upstreamability: MEDIUM.** Confirmed wrappers present in `upstream/main`. Larger surface
(touches many wrappers) and benefits from maintainer agreement on the choke-point design —
propose the single `validateGitRef` helper approach in the PR description and let reviewers
weigh `--`-separator vs validation per call site.

---

### PR 9 — fix: force-push guard fails open with a `git -c …` / `git -C …` prefix

- **Priority:** P2 (safety-guard bypass)
- **Branch:** `fix/force-push-guard-config-prefix`
- **Source:** bugs-cmd M15
- **Affected files:** `internal/cmd/tap_guard_dangerous.go` (upstream :229–240,
  `matchesDangerousGitPush`)

**Problem.** `matchesDangerousGitPush` only sets `hasPush` when `f == "push" && fields[i-1]
== "git"` (upstream :236). With a config prefix — `git -c http.proxy=x push --force` — the
token before `push` is the config value, so `hasPush` is never set, the `--force` check is
never reached, and the guard **fails open**. The codebase itself trains agents to use
`git -c` (see `runGitCommit`), making this evasion plausible in practice, not theoretical.

**Fix.** Detect `push` as a git subcommand robustly: skip a leading `git` plus any
`-c <kv>` / `-C <dir>` / global-option prefix tokens before locating the subcommand, rather
than requiring `push` to be immediately preceded by `git`.

**Test plan.** Table test: `"git push --force"` → blocked (regression), `"git -c
http.proxy=x push --force"` → blocked (the fix), `"git -C /repo push --force"` → blocked,
`"git push"` (no force) → allowed, `"git status"` → allowed.

**Upstreamability: MEDIUM-HIGH.** Confirmed at upstream `tap_guard_dangerous.go:236`. Clear
safety win with an easy repro; small diff. Good standalone PR.

---

### PR 10 (optional) — fix: `--max-concurrent` sling throttle pauses only ~6s (broken loop)

- **Priority:** P3 (weakens Dolt-overload protection)
- **Branch:** `fix/sling-batch-throttle-loop`
- **Source:** bugs-cmd M2
- **Affected files:** `internal/cmd/sling_batch.go` (upstream :137–139)

**Problem.**
```go
for wait := 0; wait < 30; wait++ {
    time.Sleep(2 * time.Second)
    if wait >= 2 { break }   // always breaks on 3rd iteration
}
```
Always breaks after ~6s; the `< 30` bound (~60s) is dead code. The spawn-rate throttle that
exists to protect Dolt from overload does far less than intended.

**Fix.** Replace with the intended pause semantics (single `time.Sleep` of the configured
duration, or a loop whose bound is actually honored). Clarify what the throttle interval is
*meant* to be — confirm with a maintainer, since the original intent (6s? 60s? configurable?)
isn't obvious from the code. **Note (ZFC):** keep the duration as data/config rather than a
new hardcoded heuristic, per the project's Zero Framework Cognition principle.

**Test plan.** Refactor the sleep behind an injectable clock/duration; assert the batch
pauses for the configured interval between groups. Avoid wall-clock sleeps in the test.

**Upstreamability: MEDIUM.** Confirmed at upstream `sling_batch.go:137`. Real bug, but the
*correct* interval is a judgment call — propose, don't assume. Lowest-priority of the set;
include only if bundling a cleanup batch.

---

## Suggested landing order

1. **Batch A — one-line correctness (lowest risk, fastest review):** PR 1, PR 2, PR 3,
   PR 4, PR 5. These are independent files; can be 5 small PRs or, if maintainers prefer, a
   single "bug-fix roundup" PR (per-fix commits). Recommend separate PRs — CONTRIBUTING
   explicitly wants one logical change per PR.
2. **Batch B — security:** PR 7, PR 9 (small, clear), then PR 8 (larger, design discussion).
3. **Batch C — perf:** PR 6.
4. **Optional:** PR 10.

## PR hygiene (from upstream CONTRIBUTING.md)

- Branch off `upstream/main`, never from fork `main`. Naming: `fix/*`, `perf/*` (use
  `fix/*` if `perf/*` isn't recognized), `refactor/*`, `docs/*`.
- Gate before submit: `go build ./...`, `go test ./...`, `go vet ./...`, `gofmt`.
- **ZFC check:** every fix above is transport/plumbing, not cognition — no new thresholds
  or heuristics introduced (PR 10 explicitly keeps the interval as data). Good fit for the
  project's design philosophy; call this out in PR descriptions.
- Each PR body should include: the repro, the upstream file:line, the one-paragraph
  rationale, and the test added.

## Not upstreamable (filed for fork only)

- **perf Q1** — N+1 `bd.Show()` in `internal/reaper/orphan_reconcile_git.go` (bead
  gu-nid89.21). **File absent in `upstream/main`** — fork-only.
- **security #5** (`sh -c` gate commands) — by-design, inside the trust boundary.
- **security #8** (`govulncheck` in CI) — environmental tooling recommendation, not a code
  change; re-run from a proxy-capable host.
- Various display-only MED/LOW nits (M6 DAG render, M14 convoy-add output, L1–L9) —
  low acceptance odds; revisit if a maintainer requests a cleanup batch.

---

## Sources

- `reports/audit-2026-06/bugs-cmd.md` (gu-nid89.1) — accessed 2026-06-11
- `reports/audit-2026-06/perf-runtime.md` (gu-nid89.11) — accessed 2026-06-11
- `reports/audit-2026-06/security-injection.md` (gu-nid89.10) — accessed 2026-06-11
- `upstream/main` @ `ccd16c18` — every cited file:line re-verified via
  `git show upstream/main:<file>` — accessed 2026-06-11
- `CONTRIBUTING.md` @ `upstream/main` (PR conventions, ZFC design philosophy) — accessed
  2026-06-11

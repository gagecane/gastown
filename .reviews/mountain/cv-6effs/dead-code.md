# Dead Code Axis

## Summary
Unexported-symbol hygiene is excellent: the go1.26-built `golangci-lint` 2.11.4
`unused` (staticcheck U1000) reports **0 issues** across all 87 internal
packages, with or without `--tests`, and `go build ./...` is clean. (Plain
`staticcheck` cannot run here — its binary is built against go1.25 while the
module requires go1.26, so every package fails to compile under it. All
unused-analysis was therefore done with `golangci-lint`, which ships a go1.26
toolchain.) The recent P0 cleanup landed: the three largest orphans flagged in
the prior run (cv-ndy3m) — `internal/agent/provider` (~956 LOC),
`internal/protocol` (~1,381 LOC), and `internal/connection` (~679 LOC) — have
all been deleted from the tree.

The remaining debt is, as before, concentrated in **exported code with no
in-tree consumer**, which `unused` deliberately does not flag (it assumes
external importers — but every package here is under `internal/` and so cannot
be imported outside this module, making exported-but-unreferenced symbols
genuinely dead). An authoritative reverse-dependency sweep
(`go list -deps -tags 'e2e integration nightly' ./cmd/...` over all four
binaries, cross-checked against test-only importers) surfaced **six orphan
packages totalling ~1,647 LOC of non-test source** (plus ~1,583 LOC of tests
that only exercise themselves): `agent`, `autotestpr`, `github`, `keepalive`,
`mq`, `testpathmap`. None is reachable from any binary under any build tag, and
none is imported by any other package's tests. A seventh candidate,
`internal/env`, is **NOT dead** — it is documented migration-destination
infrastructure (Phase 3 of a GT_* env-var consolidation epic) that intentionally
has no callers yet. The top theme is unchanged: **whole abandoned subsystems,
not scattered cruft**, plus a stable tail of dead exported method clusters and
write-only struct fields.

## Score
score: 0.62

## Critical Findings (P0 — file as beads, fix urgently)

- **Title**: Delete orphan `internal/autotestpr` branch-GC/retention package (~620 LOC src, never reachable)
- **Location**: `internal/autotestpr/branch_gc.go` (+ `branch_gc_test.go`,
  `branch_gc_integration_test.go`, `testmain_test.go`)
- **Impact**: `BranchGCRunner`, `BranchGCConfig`, `BranchCandidate`,
  `ClassifiedBranch`, `BranchGCResult`, `DefaultBranchGCConfig`,
  `ClassifyBranches`, `DeleteStaleBranches`, `ListBranchesForRig`,
  `AttachmentRetentionConfig` — a complete auto-test-PR branch garbage-collector
  and attachment-retention subsystem. `go list -deps` with `e2e,integration,nightly`
  tags over all four binaries does not reach it; no package imports it in any
  build configuration; the only `//go:build integration` tags in the directory
  are on its own test files. Its own two most recent commits are a dead-code
  removal (`2330750e refactor(autotestpr): delete cut tautology gate and template
  code`) and a test-isolation fix — i.e. it is being maintained as if live while
  having no entry point. At ~620 LOC src + ~823 LOC tests it is now the single
  largest dead module in the tree.
- **Suggested fix**: Confirm there is no in-flight auto-test-PR feature that
  intends to wire this up (the prior run flagged the same uncertainty). If not,
  `git rm -r internal/autotestpr`; `go build ./...` and `go vet ./...` stay green.
- **Fingerprint**: `axis:dead-code|internal/autotestpr|orphan-package`

- **Title**: Delete orphan `internal/github` PR-client package (~421 LOC src, never reachable)
- **Location**: `internal/github/client.go`, `internal/github/pr.go` (+ tests)
- **Impact**: A token-based GitHub REST+GraphQL client (`Client`, `NewClient`,
  `WithHTTPClient`/`WithToken`/`WithRESTBase`/`WithGraphQLBase` options,
  `ReviewState`, `ReviewComment`, `PRResult`). The live GitHub integration goes
  entirely through `internal/ciwatcher/client_gh.go`,
  `internal/prwatcher/client_gh.go`, `internal/refinery/pr_provider_github.go`,
  and `internal/git/git.go` (shelling to `gh`). This is a parallel, unused HTTP
  client that misleads anyone tracing how Gas Town talks to GitHub. Zero
  importers in any build config; last touched only by tree-wide `gofmt` and the
  `65483624` fork-sync split.
- **Suggested fix**: Confirm no in-flight migration intends to adopt the typed
  HTTP client over the `gh` shell-outs, then `git rm -r internal/github`.
- **Fingerprint**: `axis:dead-code|internal/github|orphan-package`

## Major Findings (P1 — track, do not auto-bead)

- **Orphan `internal/testpathmap` package (~349 LOC src, never reachable).**
  `Store`, `Entry`, `New`, record/lookup/compact with TTL decay. Introduced by
  `e6d2a1ed feat(testpathmap): Add test-path → owning-rig map with TTL decay`,
  then immediately pruned by `7b58984d refactor(deadcode): Remove ~60 unused
  symbols` — pruned but never removed, and nothing outside its own `_test.go`
  consumes it. Confirmed orphan via `go list -deps` + test-importer grep (0
  external test importers). Fingerprint:
  `axis:dead-code|internal/testpathmap|orphan-package`

- **Orphan `internal/keepalive` package (~137 LOC src, never reachable).**
  `Touch`, `TouchInWorkspace`, `Read`, `State.Age`. Git history is explicit:
  `108afdbc Remove keepalive file infrastructure for feed-based wake model`. The
  wake model moved to feeds; the package survived the cut (re-landed via the
  `65483624` fork-sync) and now has zero importers anywhere. Confirmed
  superseded — delete. Fingerprint:
  `axis:dead-code|internal/keepalive|orphan-package`

- **Orphan `internal/agent` `StateManager[T]` package (~62 LOC src, never reachable).**
  Generic `StateManager[T]`, `NewStateManager`, `StateFile`, `Load`, `Save` —
  "shared state types for Gas Town agents (witness, refinery, deacon)". History
  notes `78ca8bd5 fix(witness,refinery): remove ZFC-violating state types` and
  `5218102f refactor: ZFC-compliant state management` indicate witness/refinery
  moved off this manager and left it behind. No importer in any build config; no
  external test importer. Fingerprint:
  `axis:dead-code|internal/agent|orphan-package`

- **Orphan `internal/mq` ID generator (~58 LOC src, never reachable).**
  `GenerateMRID` / `GenerateMRIDWithTime` mint `<prefix>-mr-<sha256[:10]>` IDs.
  Live MR beads are created via `bd.Create()` (beads-issue IDs) in
  `internal/cmd/done.go` and `internal/cmd/mq_submit.go` — a different ID scheme.
  Only its own tests reference it. Risk: a dev could wire it up believing it is
  the canonical MR-ID source, diverging from the real format. Fingerprint:
  `axis:dead-code|internal/mq|orphan-package`

- **Dead `*beads.Beads` rig-bead method cluster (5 exported methods, zero references).**
  `GetRigBead`, `GetRigByID`, `UpdateRigBead`, `DeleteRigBead`, `ListRigBeads` at
  `internal/beads/beads_rig.go:191,211,232,259,266`. Re-verified zero references
  outside `beads_rig.go`/`beads_rig_test.go` — not in production, not in tests,
  not via any interface (no interface declares these names; they are concrete
  methods on `*Beads`). Only internally self-referential. Four carry
  `// Deprecated:` notes (gu-r83v / ta-0pk). `unused` does not catch them because
  they are exported methods on an exported type. `RigFields` and
  `RigBeadIDWithPrefix` stay live — only this cluster is dead. Fingerprint:
  `axis:dead-code|internal/beads/beads_rig.go|dead-RigBead-method-cluster`

- **Dead exported `FromWorkDir` wrappers in `internal/mayor/cleanup.go` (zero references).**
  `IsACPActiveInWorkDir` (line 117) and `NewCleanupVetoCheckerFromWorkDir`
  (line 133) — thin `FromWorkDir` variants of `IsACPActive` /
  `NewCleanupVetoChecker`. Re-verified zero references in production or tests.
  The non-WorkDir forms and the `CleanupVetoChecker` type itself remain live.
  Fingerprint: `axis:dead-code|internal/mayor/cleanup.go|FromWorkDir-wrappers-unused`

- **Test-only exported `beads_rig.go` helpers (referenced solely by their own test).**
  `RigBeadID` (`// Deprecated:`), `ValidRigState`, `FormatRigDescription`,
  `ParseRigFields` — each referenced only in `internal/beads/beads_rig_test.go`,
  with no production caller. Either the production callers were removed (making
  the tests vestigial) or these are speculative API. Confirm before removing the
  symbol+test pairs. (`RigBeadIDWithPrefix` is the live replacement.) Fingerprint:
  `axis:dead-code|internal/beads/beads_rig.go|test-only-rig-helpers`

## Minor Findings (P2 — informational)

- **Write-only struct fields (assigned but never read).** Four fields are set in
  constructors/handlers but never read back; `unused` only flags them under
  whole-program `field-writes-are-uses:false`, which is otherwise too noisy to
  enable as a gate:
  - `internal/lock/lock.go:46` `Lock.workerDir` — set in `New`, read only by
    `lock_test.go`. `lockPath` is the field actually used; `workerDir` is
    redundant state.
  - `internal/wisp/config.go:34` `Config.townRoot` — set in `NewConfig`, never
    read (`filePath` is derived inline and stored separately).
  - `internal/nudge/poller.go:212-213` `Watcher.townRoot` / `Watcher.session` —
    set in `NewWatcher`, never read (`dir` is derived once and stored).
  These are zero-behavior-change deletions. Fingerprint:
  `axis:dead-code|cross-package|write-only-struct-fields`

- **`type PreCheckoutHookCheck = BranchProtectionCheck` alias is dead.**
  `internal/doctor/precheckout_hook_check.go:227`, commented "Legacy type alias
  for backwards compatibility." Zero references. Since the package is `internal/`,
  there are no external consumers to be backwards-compatible *with* — the alias
  serves no purpose. `BranchProtectionCheck` and `NewPreCheckoutHookCheck` (2
  refs) remain live. Fingerprint:
  `axis:dead-code|internal/doctor/precheckout_hook_check.go|PreCheckoutHookCheck-alias`

- **`internal/env` is NOT dead — do not delete.** `go list -deps` lists it as
  unreachable, but its own package doc states it is the destination for a GT_*
  env-var consolidation epic ("Callsites are migrated in their own per-package
  beads … this package adds the accessors without touching existing call sites").
  It is intentionally caller-less infrastructure pending Phase 3 migration.
  Flagged so the synthesis step does not mis-bucket it as an orphan. Fingerprint:
  `axis:dead-code|internal/env|not-dead-migration-destination`

- **`Deprecated:` markers with live callers (migration debt, NOT dead).** 13
  `// Deprecated:` markers remain in non-test `internal/` source, including
  `internal/beads/beads.go` (`Type`/`Label` → `Labels`),
  `internal/mayor/manager.go` (`Running` → `Active`),
  `internal/refinery/manager.go` (`FindMR`), `internal/doltserver/sync.go`
  (`FindRemote`), `internal/witness/handlers.go` (`MailSent` → nudge). These
  still have callers — migration debt, not dead code; flagging so a future
  migration leg can retire them once callers move off. Fingerprint:
  `axis:dead-code|cross-package|deprecated-but-still-called`

- **Tooling note.** No `if false` / always-true-or-false unreachable branches and
  no large (>=5 line) commented-out code blocks were found in `internal/` or
  `cmd/`. `golangci-lint unused` with `exported-fields-are-used:false` /
  `parameters-are-used:false` produces mostly false positives here (interface
  method param names, stub-function params, used-elsewhere interfaces like
  `startupPromptSession`) — all exported-dead findings above came from explicit
  reverse-dependency analysis (`go list -deps -tags …` + test-importer grep),
  not from the linter. Fingerprint:
  `axis:dead-code|tooling|exported-reachability-needs-go-list`

## Counts
  counts: critical=2 major=7 minor=5

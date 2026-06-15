# Dead Code Axis

## Summary
The unexported-symbol hygiene of the tree is excellent: `golangci-lint`'s
`unused` (staticcheck U1000) reports **0 issues** across all 94 packages, and
the build is clean. Recent history shows active, deliberate dead-code pruning
(`refactor(deadcode): Remove ~60 unused symbols`, `remove abandoned
beads-routing`, etc.), so the *unexported* surface is well-tended. (Note:
plain staticcheck cannot run here — it was built against go1.25 while the module
requires go1.26 — so all unused-analysis was done with the go1.26-built
`golangci-lint` 2.11.4.)

The debt is concentrated in **exported code with no in-tree consumer**, which
`unused` deliberately does not flag (it assumes external importers). Because
every package here lives under `internal/` and therefore *cannot* be imported
outside this module, exported-but-unreferenced symbols are genuinely dead. A
reverse-dependency sweep (`go list -test -tags 'e2e integration nightly'` +
import-string grep + full git-history `-S` checks) surfaced **eight orphan
packages** — packages no other package imports, in any build configuration, ever
— totalling roughly **4,300 lines of non-test source** (plus their tests). Seven
of the eight were mass-introduced in a single fork-sync commit (`65483624`,
2026-04-30) and have never had an importer in the repo's entire history; they
appear to be an upstream comms/agent layer (`protocol`, `connection`, `agent`,
`agent/provider`, `keepalive`, `mq`, `github`) that the live nudge/mail/beads
and `internal/acp` paths superseded. The top theme: **whole abandoned
subsystems, not scattered cruft.**

## Score
score: 0.55

## Critical Findings (P0 — file as beads, fix urgently)

- **Title**: Delete orphan ACP layer `internal/agent/provider` (~956 LOC, never imported)
- **Location**: `internal/agent/provider/acp.go`, `internal/agent/provider/provider.go` (+ tests)
- **Impact**: A full second Agent-Client-Protocol implementation (`ACPProvider`,
  `BaseProvider`, `LocalProvider`, JSON-RPC types, tool registry) parallel to the
  live `internal/acp` package. `go list -deps` confirms none of the four binaries
  (`gt`, `gt-proxy-client`, `gt-proxy-server`, `curio-proposer`) link it; no
  package in the module imports it under any build tag; `git log -S` shows the
  import path was never present in history. It received a godoc-only commit
  (`f1a900cc`) on 2026-06-12, which makes it *look* maintained — a trap for
  future readers who will assume it is the real ACP layer.
- **Suggested fix**: `git rm -r internal/agent/provider`; confirm `go build ./...`
  and `go vet ./...` stay green (they will — nothing references it).
- **Fingerprint**: `axis:dead-code|internal/agent/provider|orphan-package`

- **Title**: Delete orphan `internal/protocol` merge-message layer (~1,381 LOC, never imported)
- **Location**: `internal/protocol/` (handlers.go, messages.go, refinery_handlers.go, witness_handlers.go, types.go)
- **Impact**: A complete merge-ready/merged/merge-failed/rework message protocol
  with witness + refinery handler registries and payload parsers. No importer in
  the module under any tag; import path never present in git history. The live
  inter-agent path is nudge/mail/beads — this is a superseded comms subsystem.
  At ~1.4k LOC it is the single largest dead module and actively misleads anyone
  tracing how refinery/witness communicate.
- **Suggested fix**: `git rm -r internal/protocol` after a final
  `grep -rn "internal/protocol\"" --include='*.go'` confirms zero importers
  (verified: zero).
- **Fingerprint**: `axis:dead-code|internal/protocol|orphan-package`

- **Title**: Delete orphan `internal/connection` machine/address layer (~679 LOC, never imported)
- **Location**: `internal/connection/` (address.go, connection.go, machine.go, registry.go)
- **Impact**: `Address`/`Machine`/`MachineRegistry`/`Connection`/`LocalConnection`
  — a distributed/remote-host addressing abstraction. No importer anywhere, ever.
  Gas Town is single-host today; this is unimplemented-future or upstream-residue
  scaffolding masquerading as a live capability.
- **Suggested fix**: `git rm -r internal/connection`.
- **Fingerprint**: `axis:dead-code|internal/connection|orphan-package`

## Major Findings (P1 — track, do not auto-bead)

- **Orphan `internal/github` PR-client package (~421 LOC, never imported).** Defines
  `ReviewState`, `ReviewComment`, `PRResult`, and a token-based GitHub client.
  The live GitHub work goes through `internal/ciwatcher/client_gh.go`,
  `internal/prwatcher/client_gh.go`, `internal/refinery/pr_provider_github.go`,
  and `internal/git/git.go` (shelling to `gh`). This package is a parallel,
  unused HTTP client. Verify no in-flight migration intends to adopt it before
  deleting. Fingerprint: `axis:dead-code|internal/github|orphan-package`

- **Orphan `internal/agent` package (~62 LOC, never imported).** Provides a
  generic `StateManager[T]` "shared types for Gas Town agents (witness, refinery,
  deacon)". History note `78ca8bd5 fix(witness,refinery): remove ZFC-violating
  state types` and `5218102f refactor: ZFC-compliant state management` strongly
  suggest the witness/refinery moved *off* this manager and it was left behind.
  No importer in any build config. Fingerprint: `axis:dead-code|internal/agent|orphan-package`

- **Orphan `internal/keepalive` package (~137 LOC, never imported).** `Touch`,
  `TouchInWorkspace`, `Read`, `State.Age`. Git history is explicit:
  `108afdbc Remove keepalive file infrastructure for feed-based wake model
  (gt-vdprb.2)`. The wake model moved to feeds, but the package itself survived
  the cut (re-landed via the 65483624 fork-sync). Confirmed superseded — delete.
  Fingerprint: `axis:dead-code|internal/keepalive|orphan-package`

- **Orphan `internal/mq` ID generator (~58 LOC, never imported).** `GenerateMRID`
  / `GenerateMRIDWithTime` mint `<prefix>-mr-<sha256[:10]>` IDs. The live MR beads
  are created via `bd.Create()` (beads-issue IDs) in `internal/cmd/done.go:1482`
  and `internal/cmd/mq_submit.go:432` — a different ID scheme. This hash-based
  convention is unused; only its own tests reference it. Risk: an operator/dev
  could wire it up believing it is the canonical MR-ID source, diverging from the
  real format. Fingerprint: `axis:dead-code|internal/mq|orphan-package`

- **Orphan `internal/autotestpr` branch-GC/retention package (~618 LOC, never imported).**
  `BranchGCRunner`, `BranchGCConfig`, `AttachmentRetentionConfig`, etc. Its own
  most recent commit was a dead-code removal (`2330750e refactor(autotestpr):
  delete cut tautology gate and template code`), yet the whole package has no
  importer under any build tag. Either an unfinished feature or fully abandoned;
  confirm intent (an in-progress auto-test-PR feature?) before deleting.
  Fingerprint: `axis:dead-code|internal/autotestpr|orphan-package`

- **Orphan `internal/testpathmap` package (~349 LOC, never imported).** `Store`,
  `Entry`, `New`, record/lookup/compact with TTL. Last touched by
  `7b58984d refactor(deadcode): Remove ~60 unused symbols` — i.e. it was *pruned*
  but not *removed*, and nothing (not even tests outside its own package)
  consumes it. Only its own `_test.go` exercises it. Fingerprint:
  `axis:dead-code|internal/testpathmap|orphan-package`

- **Dead `*beads.Beads` rig-bead method cluster (5 exported methods, zero references).**
  `GetRigBead`, `GetRigByID`, `UpdateRigBead`, `DeleteRigBead`, `ListRigBeads`
  at `internal/beads/beads_rig.go:191,211,232,259,266`. Verified zero references
  anywhere in the module — not in production, not in tests, not via any interface
  (no interface declares these names; they are concrete methods on `*Beads`). The
  cluster is only internally self-referential (`UpdateRigBead`→`GetRigBead`) with
  no external entry point. Four carry `// Deprecated:` notes citing the "gt"-prefix
  assumption (gu-r83v / ta-0pk). `unused` does not catch them because they are
  exported methods on an exported type. `RigFields` and `RigBeadIDWithPrefix`
  stay live — only this cluster is dead. Fingerprint:
  `axis:dead-code|internal/beads/beads_rig.go|dead-RigBead-method-cluster`

- **Dead exported wrappers in `internal/mayor/cleanup.go` (zero references).**
  `IsACPActiveInWorkDir` (line 117) and `NewCleanupVetoCheckerFromWorkDir`
  (line 133) — thin `FromWorkDir` variants of `IsACPActive` /
  `NewCleanupVetoChecker`. Zero references in production or tests. The non-WorkDir
  forms and the `CleanupVetoChecker` type itself remain live. Fingerprint:
  `axis:dead-code|internal/mayor/cleanup.go|FromWorkDir-wrappers-unused`

- **Test-only exported `beads_rig.go` helpers (referenced solely by their own test).**
  `RigBeadID` (line 125, `// Deprecated:`), `ValidRigState`, `FormatRigDescription`,
  `ParseRigFields` — each referenced only in `internal/beads/beads_rig_test.go`,
  with no production caller. Either the production callers were removed (making the
  tests vestigial) or these are speculative API. Confirm before removing the
  symbol+test pairs. (`RigBeadIDWithPrefix` is the live replacement, 13 callers.)
  Fingerprint: `axis:dead-code|internal/beads/beads_rig.go|test-only-rig-helpers`

## Minor Findings (P2 — informational)

- **`type PreCheckoutHookCheck = BranchProtectionCheck` alias is dead.**
  `internal/doctor/precheckout_hook_check.go:227`, commented "Legacy type alias
  for backwards compatibility." Zero references. Since the package is `internal/`,
  there are no external consumers to be backwards-compatible *with* — the alias
  serves no purpose. `BranchProtectionCheck` (8 refs) is the live name. Fingerprint:
  `axis:dead-code|internal/doctor/precheckout_hook_check.go|PreCheckoutHookCheck-alias`

- **`const ToolTypeFunction` (the sole value of `type ToolType`) is dead.**
  `internal/agent/provider/acp.go:40`. Moot if the whole `agent/provider` package
  is deleted (P0 above); listed for completeness because the entire `ToolType`
  type + its only const + the const's only value are mutually unreferenced.
  Fingerprint: `axis:dead-code|internal/agent/provider/acp.go|ToolTypeFunction-unused`

- **`Deprecated:` markers worth a cleanup pass (still have live callers — NOT dead).**
  `internal/beads/beads.go:690,703,704` (`Type`/`Label` superseded by `Labels`),
  `internal/mayor/manager.go:46` (`Running` → `Active`),
  `internal/refinery/manager.go:571` (`FindMR`),
  `internal/doltserver/sync.go:81` (`FindRemote`),
  `internal/witness/handlers.go:154` (`MailSent` → nudge). These are migration
  debt, not dead code; flagging so a future migration leg can retire them once
  callers move off. Fingerprint:
  `axis:dead-code|cross-package|deprecated-but-still-called`

- **Method note:** `golangci-lint unused` with `exported-is-used: false` /
  `exported-fields-are-used: false` is a known no-op in staticcheck (whole-program
  export reachability is not computed), so it added no signal beyond the default
  run. All exported-dead findings above came from explicit reverse-dependency
  analysis (`go list -test -tags …` + import-string grep + git-history `-S`),
  not from the linter. Fingerprint:
  `axis:dead-code|tooling|exported-is-used-noop`

## Counts
  counts: critical=3 major=10 minor=4

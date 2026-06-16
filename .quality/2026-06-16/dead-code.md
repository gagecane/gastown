# Dead Code Audit

## Summary
Gas Town's Go codebase (89 packages, ~1,784 `.go` files, ~639k lines) is in
strong shape for this dimension. `staticcheck`'s `unused` analyzer — run across
all packages via `golangci-lint` — reported **zero** unexported dead symbols,
which is a meaningful signal of disciplined maintenance: orphaned helpers,
unreachable branches, and abandoned private functions are not accumulating.

The dead code that does exist is concentrated in **exported** APIs that
`unused` cannot see (it only flags unexported symbols). The clearest case is a
self-referential cluster of rig-bead helpers in `internal/beads/beads_rig.go`
that are explicitly marked `Deprecated`, have no in-tree callers, and partly
call each other — a textbook "appears live but is unreferenced" finding. A
second case is an intentional no-op stub (`CreatePolecatCLAUDEmd`) plus its
345-line embedded template, kept alive only to satisfy the compiler. Both can
be removed in a single behavior-preserving PR. Everything else flagged by
keyword scans (`Deprecated` markers, `nolint:unused`, commented blocks) turned
out to be live, intentional, or doc prose — not dead.

## Score
score: 0.82

## Critical Findings (P0 — file as beads, fix urgently)

_None._ No large dead modules (>200 lines) and no dead exported API that an
external consumer would plausibly still depend on. The largest dead cluster
(below) is `internal/`-scoped, so it can only have in-module callers, and it
has none — making removal safe.

## Major Findings (P1 — track but do not auto-bead)

- **Dead deprecated rig-bead helper cluster in `internal/beads/beads_rig.go`**
  - **Location**: `internal/beads/beads_rig.go`
    - `GetRigBead` (line 191), `GetRigByID` (line 211),
      `UpdateRigBead` (line 232), `DeleteRigBead` (line 259),
      `ListRigBeads` (line 266)
  - **Impact**: Five exported methods on `*Beads`, three explicitly tagged
    `Deprecated: ... assumes the "gt" prefix`. None have any caller in the
    module (`grep` for each name across all `*.go`, excluding the definition
    file, returns 0 references — including zero test references for the
    deprecated four). `UpdateRigBead` and `DeleteRigBead` only call the also-dead
    `GetRigBead`/`RigBeadID`, so the cluster is self-referential dead weight.
    These are `internal/`, so no external consumer can depend on them — the
    "gt-prefix assumption" footgun the Deprecated notes warn about can be
    deleted rather than carried.
  - **Suggested fix**: Delete the five methods (~75 lines incl. doc comments).
    Note that `RigBeadID` (line 304) becomes orphaned once they are gone — its
    only references are the two dead methods plus `TestRigBeadID` in
    `beads_rig_test.go`; remove it and its test in the same PR. The live path
    uses `RigBeadIDWithPrefix` (22 references) and `EnsureRigBead`/
    `CreateRigBead` (still called from `internal/cmd/rig*.go` and
    `internal/doctor/`), so those stay.

- **No-op stub `CreatePolecatCLAUDEmd` + orphan embedded template**
  - **Location**: `internal/templates/templates.go:226-243` (function) and
    `internal/templates/polecat-CLAUDE.md` (345-line `//go:embed` file at
    line 47)
  - **Impact**: `CreatePolecatCLAUDEmd` is a documented no-op that returns
    `(false, nil)` unconditionally and is referenced only by its own tests in
    `templates_test.go`. The 345-line embedded `polecatCLAUDEmd` string is kept
    referenced solely via `_ = polecatCLAUDEmd // keep embed referenced so the
    compiler doesn't complain` (line 241) — i.e. dead data alive only to avoid a
    compile error. The function's own comment says it "will be removed in a
    future cleanup once no external integrations depend on them (gu-k9oj)."
  - **Suggested fix**: Confirm no out-of-tree integration still calls
    `CreatePolecatCLAUDEmd` (it's the only reason this exists per the comment),
    then delete the function, the `//go:embed polecat-CLAUDE.md` directive +
    `polecatCLAUDEmd` var, the `polecat-CLAUDE.md` file, and the associated
    no-op tests. Removes ~360 lines with no behavior change.

## Minor Findings (P2 — informational)

- **`unparam` signals in non-test code (8 occurrences).** Not dead code per se,
  but parameters/results that are always passed the same value or never used —
  candidate cleanups that simplify signatures:
  - `internal/acp/proxy.go:168` `(*Proxy).setStreams` — `in` always nil
    (the source file itself tags this `//nolint:unused // test stream injection
    helper`, so this is intentional test plumbing — leave as-is)
  - `internal/curio/store.go:125` `retryOnSessionRoot` — `maxAttempts` always 3
  - `internal/daemon/daemon.go:2403` `(*Daemon).hasPendingEvents` — `channel`
    always `"refinery"`
  - `internal/daemon/main_branch_test_runner.go:1198` `formatFailureOutput` —
    `tailSize` always 50
  - `internal/daemon/merge_queue_age_dog.go:266-267` `evaluateMergeQueueRig` —
    `oldestThreshold` and `headFrozenMinDepth` always constant
  - `internal/hooks/installer.go:133` `ensureDeny` — `entry` always
    `"AskUserQuestion"`
  - `internal/tmux/bindings.go:77` `(*Tmux).isGTBindingCurrent` — `table`
    always `"prefix"`
  - These are single-call helpers whose generality is unused; inlining the
    constant arg would remove dead flexibility. Low value, low risk.

- **42 additional `unparam` hits in `_test.go` files** (test-helper params
  that are always the same value or unused results). Test cleanup only; no
  production impact. Listed in the raw lint output but not enumerated here.

- **No dead branches found.** Scans for `if false`, `if (false)`,
  unreachable `default`/`return` cruft found nothing (the only `if false…`
  hits in `patrol_scan.go` are a variable named `falseDeferredResult`, a false
  positive).

- **No orphan/build-excluded files.** No `//go:build ignore` files; the
  write-incapable `cmd/curio-proposer` skeleton is intentional and
  test-asserted (TestImportGraph_NoWritePath), not abandoned.

- **Commented-out "code" blocks are doc prose, not dead code.** The files with
  the most `//`-prefixed code-like lines (`done.go`, `daemon.go`, etc.) are
  wrapped explanatory comments, not commented-out implementations. No ≥5-line
  dead code blocks confirmed.

- **Other `Deprecated`-marked exported funcs are still live**, so not dead:
  `HasRemote` (`internal/doltserver/sync.go`, called from `dolthub.go:102`),
  `GetMR` (`internal/refinery/manager.go`, called from `cmd/mq.go:434`). The
  `Type`/`Label` deprecated struct fields in `internal/beads/beads.go` are
  still read on the backward-compat path. Leave all of these.

## Counts
counts: critical=0 major=2 minor=4

---

## Methodology / Reproduction
- `golangci-lint run --no-config --default=none --enable=unused --enable=unparam ./...`
  (go1.26.2, golangci-lint 2.11.4) → 0 `unused`, 50 `unparam` (8 non-test).
- Targeted `grep` reference-counting for exported symbols flagged `Deprecated`
  (which `unused` cannot evaluate), confirming caller counts across all `*.go`
  excluding each symbol's own definition file.
- Keyword sweeps for dead branches (`if false`), build-excluded files
  (`//go:build ignore`), `nolint:unused` markers, and ≥5-line commented code
  blocks.

## Sources
- Local repository analysis of `github.com/steveyegge/gastown` at branch
  `polecat/fury/gu-leg-4csls--mqgynum4` — accessed 2026-06-16

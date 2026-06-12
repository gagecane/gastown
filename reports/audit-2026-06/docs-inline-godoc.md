# Inline Documentation & Godoc Coverage Audit — gastown_upstream

**Bead:** gu-nid89.8 (epic gu-nid89 — Whole-Repo Gastown Audit)
**Date:** 2026-06-11
**Commit audited:** `a9afe585` (branch `main`)
**Auditor:** polecat ghoul
**Scope:** `internal/` packages, with mandated focus on packages > 5K LOC (non-test)

---

## How this was measured

Coverage numbers are produced by a standalone Go AST walker (`go/parser` with
`parser.ParseComments`) over every non-`_test.go` file in `internal/`. For each
package it counts **exported** identifiers (`FuncDecl`, `TypeSpec`,
`ValueSpec`) and whether each carries a non-empty doc comment, plus whether the
package has a package-clause doc comment anywhere. Methods on both exported and
unexported receivers are counted as exported when the method name is exported.

```go
// gist of the analyzer — full source reproducible on request
isExported := name[:1] == strings.ToUpper(name[:1])
// FuncDecl:  decl.Doc != "" ?  GenDecl/TypeSpec: spec.Doc || decl.Doc != "" ?
// package doc: any file's f.Doc != "" ?
```

`go doc ./...` rendering could **not** be exercised in the sandbox — the module
proxy is unreachable (corp DNS sinkhole intercepts `proxy.golang.org` TLS), so
`go doc` fails on dependency resolution. Coverage is therefore from static AST
analysis, which is what `go doc` would surface anyway.

Supporting greps:

```bash
grep -rnE '//\s*(TODO|FIXME|HACK|XXX)\b' internal/ --include='*.go'   # stale annotations
grep -rl 'ABOUTME:' internal --include='*.go'                         # non-standard header convention
find internal -name doc.go                                           # dedicated package-doc files
```

---

## Headline

**Documentation hygiene in this codebase is unusually good.** Every package
over 5K LOC has a package doc comment, exported-function doc coverage in those
packages ranges **87.7%–100%**, exported-type coverage is **96–100%**, and there
is effectively **zero stale-annotation debt** (one real `TODO`, in test fixture
data). The gaps that exist are concentrated in a handful of **small** packages
(`agent/provider`, `acp`, `mayor`, `shell`) — none in the mandated >5K LOC tier.

| Metric | Value |
|---|---|
| `internal/` packages analyzed | 88 |
| Non-test `.go` files | 830 |
| Packages > 5K LOC (the focus tier) | 12 |
| Packages > 5K LOC **with** package doc | 12 / 12 (100%) |
| Exported-func doc coverage, >5K tier | 87.7% – 100% |
| Exported-type doc coverage, >5K tier | 96% – 100% |
| Dedicated `doc.go` files | 3 (`upstreamsync`, `ciwatcher`, `formula`) |
| Real `TODO`/`FIXME`/`HACK` in non-test code | **0** |
| `TODO`/`FIXME`/`HACK` total (incl. tests) | 1 (test fixture string) |

---

## Per-package assessment — the > 5K LOC focus tier

Coverage shown as `documented / exported`.

| Package | LOC | Pkg doc | Func | Type | Var/Const | Grade |
|---|---:|:---:|:---:|:---:|:---:|:---:|
| `cmd` | 121,098 | ✅ | 57/65 (88%) | 153/159 (96%) | 40/70 | A− |
| `daemon` | 23,821 | ✅ | 92/95 (97%) | 60/60 (100%) | 15/15 | A |
| `doctor` | 23,075 | ✅ | 353/363 (97%) | 131/132 (99%) | 16/16 | A |
| `beads` | 12,687 | ✅ | 317/320 (99%) | 38/38 (100%) | 44/64 | A |
| `witness` | 10,391 | ✅ | 72/73 (99%) | 56/56 (100%) | 52/89 | A |
| `config` | 9,755 | ✅ | 283/283 (100%) | 68/68 (100%) | 143/143 | A+ |
| `doltserver` | 7,967 | ✅ | 108/120 (90%) | 28/28 (100%) | 11/11 | A− |
| `polecat` | 7,481 | ✅ | 112/114 (98%) | 30/30 (100%) | 61/66 | A |
| `web` | 6,606 | ✅ | 29/29 (100%) | 52/52 (100%) | 2/2 | A+ |
| `refinery` | 5,644 | ✅ | 62/68 (91%) | 21/22 (95%) | 39/39 | A− |
| `tmux` | 5,617 | ✅ | 126/127 (99%) | 7/7 (100%) | 16/16 | A |
| `mail` | 5,481 | ✅ | 77/77 (100%) | 13/13 (100%) | 21/32 | A |

### Notes per package

- **`cmd` (121K LOC, the giant).** Package doc present and *plural*: **20
  separate files** carry a comment block immediately above `package cmd`, so
  godoc concatenates 20 file-level blurbs into one package doc. This is benign
  (godoc merges them) but slightly noisy. The 8 undocumented exported funcs are
  almost all `Error()`/`Unwrap()` methods on internal error types
  (`*SilentExitError`, `*LandConflictError`, `*errCompactTimeout`,
  `*polecatCapacityAdmissionError`) — interface-satisfying boilerplate where a
  doc comment adds little. The 6 undocumented *types* are real and worth fixing:
  `ServerHealth`, `DatabaseHealth`, `PollutionRecord`, `BackupHealth`,
  `ProcessHealth`, `OrphanDB` — these are health-report DTOs that surface in
  `gt doctor`/dolt output and deserve a one-line purpose comment.
- **`daemon` (23.8K).** Excellent. Package doc (`types.go`) is a genuinely good
  orientation comment ("dumb scheduler — all intelligence is in agents"). The 3
  undocumented funcs are `realPollerSupervisor` adapter methods.
- **`doctor` (23K).** 97% func, 99% type. The 10 undocumented funcs are mostly
  test-double adapters (`*tmuxEnvReaderWriter`, `*realDBPrefixGetter`) and a few
  real check entrypoints (`NewGlobalStateCheck`, `(*GlobalStateCheck).Run`,
  `(*IdentityCollisionCheck).Run`). Minor.
- **`beads` (12.7K).** 99%/100%. Strong package doc explaining the in-process
  store optimization. Only 3 undocumented funcs.
- **`witness` (10.4K).** 99%/100%. The lower var/const ratio (52/89) is mostly
  unexported-adjacent constants and string keys; not a real gap.
- **`config` (9.8K).** **100% across funcs, types, and vars/consts.** Exemplary.
- **`doltserver` (8K).** Lowest func coverage in tier (90%). The undocumented
  cluster is the `*WLCommons` (Wasteland wanted-board) method set —
  `EnsureDB`, `InsertWanted`, `ClaimWanted`, `SubmitCompletion`, `QueryWanted`,
  `QueryWantedFull`, `InsertStamp`, `QueryLastStampForSubject`. This is a
  coherent public-ish API surface for the federation feature and is the single
  best ROI doc-gap in the whole audit. → **bead filed**
- **`polecat` (7.5K).** 98%/100%. The 2 gaps are `*UncommittedWorkError`
  `Error()`/`Unwrap()` boilerplate.
- **`web` (6.6K).** 100%/100%.
- **`refinery` (5.6K).** 91% func, 95% type. Undocumented funcs are the
  `bitbucketPRProvider`/`githubPRProvider` adapter methods (interface impls of a
  documented interface) plus the `GateConfig` type, which **should** be
  documented (it's a config struct read from rig settings). Minor.
- **`tmux` (5.6K).** 99%/100%. One gap: `(*Tmux).WaitForRuntimeReady`.
- **`mail` (5.5K).** 100% func, 100% type.

**Conclusion for the focus tier: no critical documentation gaps.** The one
finding worth a tracked bead is the `doltserver` `WLCommons` method set; the
`cmd` health-DTO types are a nice-to-have.

---

## Gaps outside the focus tier (smaller packages)

These fall below the 5K LOC mandate but are the only places with materially low
coverage, recorded here for completeness:

| Package | LOC | Pkg doc | Func | Notes |
|---|---:|:---:|:---:|---|
| `agent/provider` | 799 | ❌ | 0/47 | ACP wire-protocol types/methods, **zero** doc comments + no package doc. Worst-covered package in the repo. |
| `acp` | 1,678 | ❌ | 2/18 | Agent Client Protocol proxy; no package doc, only 2 of 18 exported funcs documented. |
| `mayor` | 647 | ❌ | 12/28 | No package doc; ~half of exported funcs undocumented. |
| `shell` | 327 | ❌ | 0/4 | Uses `// ABOUTME:` header convention (see below) instead of a `// Package shell` doc — so godoc shows no package synopsis. |
| `quota` | 1,412 | ✅ | 30/39 | 9 undocumented exported funcs. |
| `curio` | 2,417 | ✅ | 33/45 | 12 undocumented exported funcs. |
| `wasteland` | 1,083 | ✅ | 20/27 | 7 gaps. |

`agent/provider` and `acp` together form the ACP integration layer and are the
clearest sub-tier gap. → **bead filed** (P3, below the focus tier).

---

## Convention observations (non-blocking)

1. **`// ABOUTME:` headers (13 files).** Several files (`internal/shell/integration.go`,
   `internal/cmd/{enable,disable,shell,uninstall,rig_detect,rig_quick_add}.go`,
   `internal/mayor/*`, etc.) lead with an Anthropic-style `// ABOUTME:` header
   instead of a Go-standard `// Package <name> ...` doc comment. These are
   human-useful but **godoc does not render them as package synopsis** because
   they don't begin with `Package`. Where a package's *only* leading comment is
   `ABOUTME:` (e.g. `shell`, `mayor`), the package shows no godoc synopsis. Low
   priority, but converting the first `ABOUTME:` line of each package's lead file
   to a `// Package x — ...` form would restore godoc rendering at zero
   information cost.

2. **`cmd` has 20 package-doc fragments.** Not a bug (godoc merges them) but the
   package would read more coherently with a single canonical `// Package cmd`
   block in one file and plain file-purpose comments elsewhere.

3. **`go doc` is unverifiable offline.** Anyone re-running this audit on a
   networked host should confirm `go doc ./internal/...` renders cleanly; the
   AST counts above are the offline-reproducible substitute.

---

## CLAUDE.md / AGENTS.md accuracy

**Checked and found accurate — no drift.** Specifically:

- Root `AGENTS.md` says *"See **CLAUDE.md** for complete agent context."* There
  is **no `CLAUDE.md` at the repo root**, which initially looks like broken
  drift — but `CLAUDE.md` is **deliberately `.gitignore`d** (`.gitignore:61–62`:
  *"Clone-specific CLAUDE.md (regenerated locally per clone)"*). The reference is
  correct *by design*: each clone regenerates its own `CLAUDE.md`, and `gt prime`
  injects the live context. Not a bug. ✅
- `AGENTS.md`'s embedded **beads workflow** section (`bd`/`bv` command examples,
  `bd dep add <a> <b>` semantics, "NEVER run bare `bv`") matches the actual CLIs
  and the dependency-edge direction documented in the team's working memory
  (the `depends_on_id` single-edge model used by this fork).
- Tracked CLAUDE.md *templates* exist and are the source of truth:
  `templates/polecat-CLAUDE.md`, `templates/witness-CLAUDE.md`,
  `internal/templates/townroot/claude.md`, `internal/templates/polecat-CLAUDE.md`.
  `internal/doctor/town_claude_md_check.go` actively validates the town CLAUDE.md
  — so this content is guarded by a doctor check, not left to rot.

No corrective action required for agent-instruction docs.

---

## Stale annotations (TODO / FIXME / HACK)

**Effectively none.** A repo-wide grep for `// TODO|FIXME|HACK|XXX` in
`internal/` returns a single real hit, and it is **test fixture data**, not a
live code annotation:

```
internal/daemon/usage_limit_test.go:71:  input: "// TODO: handle rate limit responses better"
```

All other `XXX` matches are format-string placeholders (`testdb-XXXXXXXX`,
`gt-mr-XXXXXXXXXX`, `CR-XXXX`) in comments/tests, not stale annotations. This is
a remarkably clean result for a 130K+ LOC codebase and indicates active
annotation hygiene.

---

## Complex internal logic — inline comment adequacy

Spot-checked the highest-complexity functions flagged by the sibling code-quality
audit (gu-nid89.12): `runDone` (CC 228, `internal/cmd/done.go:397`) carries
**118 inline comment lines across its ~303-line body** — roughly 1 comment per
2.5 lines, and the comments explain the *why* (rebase ordering, push-verify
races, the documented "stuck-in-done" incident family) rather than restating the
code. The complex paths are well-annotated; the issue there is cyclomatic
complexity (owned by gu-nid89.12), **not** comment scarcity.

---

## Beads filed for gaps

Per the acceptance criterion ("Beads for critical gaps"), gaps were triaged.
**No P1/P2-critical documentation gap exists** — coverage in the mandated tier is
87.7–100%. Two below-threshold, ROI-positive gaps were filed:

| Bead | Pri | Gap |
|---|---|---|
| `gu-94qwj` | P3 | `doltserver` `WLCommons` federation method set undocumented (8 methods) + `cmd` health DTOs (`ServerHealth`, `DatabaseHealth`, `PollutionRecord`, `BackupHealth`, `ProcessHealth`, `OrphanDB`) |
| `gu-4sypf` | P3 | `agent/provider` (0/47) + `acp` (2/18) ACP layer: add package docs and exported-symbol docs |

Both are P3: the codebase's documentation is in good shape, and these are
polish items, not correctness or onboarding blockers.

---

## Bottom line

Gas Town's inline documentation is a **strength**, not a liability. Package docs
are universal in the large packages, exported-symbol coverage is 88–100% where
it matters, stale-annotation debt is essentially nil, complex hot-path functions
are well-commented, and the agent-instruction docs (AGENTS.md / CLAUDE.md
templates) are accurate and doctor-guarded. The only gaps are small,
peripheral packages and a few adapter/DTO types — all filed as P3 polish.

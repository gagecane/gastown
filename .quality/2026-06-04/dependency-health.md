# Dependency Health Audit

_Leg: dependency-health · Module: `github.com/steveyegge/gastown` · Date: 2026-06-04_

## Summary

The Go module is in good overall health. `go mod tidy` is a no-op against the
committed `go.mod`/`go.sum` (no unused direct declarations, no missing implicit
deps), there are **no `replace`/`exclude`/security-override directives** carrying
hidden risk, and direct dependencies are close to upstream tip — the largest gap
is `charmbracelet/glamour` (v0.10.0 → v1.0.0). Dependency drift is actively
managed: `renovate.json` enables the `gomod`, `npm`, `dockerfile`, and
`github-actions` managers, automerges minor/patch for stable deps, and pins
GitHub Action digests.

The dimension's two real weaknesses are process gaps rather than bad pins:
(1) **no vulnerability-scanning gate** — `govulncheck` is absent from
`gates.yaml`, the CI matrix, `scripts/`, and the Makefile, so the project cannot
currently assert it is CVE-free; and (2) a handful of **duplicate major versions
in the resolved graph** (`cenkalti/backoff` v4+v5 are both compiled), which is
upstream-driven and low-impact but worth tracking.

The npm side is minimal: `npm-package/package.json` has no dependencies, and
`gt-model-eval/package.json` carries a single pinned dev tool (`promptfoo`).

## Score

score: 0.85

## Critical Findings (P0 — file as beads, fix urgently)

None. No known-vulnerable dependency was identified, but see the Major finding
below — absence of a scanner means "no CVEs" is **unverified**, not **confirmed**.

## Major Findings (P1 — track but do not auto-bead)

- **No vulnerability-scanning gate (`govulncheck` absent)**
  - **Location**: `gates.yaml` (no entry), `Makefile`, `scripts/`, `.github/`
    (grep for `govulncheck` returns nothing); `govulncheck` is not installed in
    the build environment either.
  - **Impact**: The audit's CVE dimension cannot be satisfied. There is no
    automated detection if a transitive dep (e.g. anything in the large
    `docker`/`testcontainers`/`otel` trees) ships a published Go vulnerability.
    Renovate bumps versions but does not gate on advisories.
  - **Suggested fix**: Add a `required-if-installed` (or `ci-only`) gate to
    `gates.yaml` running `govulncheck ./...`, mirroring the existing
    `go vet`/`golangci-lint` gate pattern. Run it once now to baseline.

## Minor Findings (P2 — informational)

- **Duplicate compiled major versions: `cenkalti/backoff` v4 + v5**
  - **Location**: `go.mod` (`v4 v4.3.0`, `v5 v5.0.3`, both `// indirect`).
  - **Detail**: v4 is pulled by `internal/testutil` and
    `testcontainers/testcontainers-go`; v5 by `internal/telemetry` and
    `otel/exporters/otlp/otlplog/otlploghttp`. Both are compiled. Collapsing is
    blocked upstream (testcontainers still on v4). Low impact — tiny library.
  - **Suggested fix**: Track for collapse to v5 once testcontainers migrates;
    no action needed today.

- **Stale transitive majors present in the module graph (not compiled)**
  - **Location**: build list — `hashicorp/golang-lru` v1.0.2 + v2.0.7;
    `russross/blackfriday` v1.6.0 + v2.1.0.
  - **Detail**: `go mod why -m` reports "main module does not need" for the v1
    lines of both — they survive in `go.sum` as graph artifacts of transitive
    `go.mod` requirements, and are not linked into any gastown binary.
    Informational; removal is not under this module's control.

- **OpenTelemetry log signals pinned on pre-stable v0.x API**
  - **Location**: `go.mod` — `otlp/otlplog/otlploghttp v0.19.0`,
    `otel/log v0.19.0`, `otel/sdk/log v0.19.0` (alongside stable
    `otel` core / metric / trace / sdk at v1.43.0).
  - **Detail**: The OTel **logs** API is still 0.x and may make breaking changes
    on minor bumps. A v0.20.0 is already available. Upgrade the log-signal
    modules together to avoid a split graph, and budget for possible API churn.

- **Mild version drift on direct deps (all ≤ 1 minor behind; none a full major behind)**
  - **Location**: `go.mod`. Available upgrades observed via `go list -m -u`:
    `glamour` v0.10.0→**v1.0.0** (first stable release; largest gap),
    `fsnotify` v1.9.0→v1.10.1, `go-sql-driver/mysql` v1.9.3→v1.10.0,
    `testcontainers-go` (+ dolt module) v0.41.0→v0.42.0,
    `steveyegge/beads` v1.0.0→v1.0.5, OTel stack v1.43.0→v1.44.0 /
    v0.19.0→v0.20.0.
  - **Detail**: No direct dependency is more than one major version behind
    upstream. Renovate's Monday schedule will surface these automatically;
    `glamour` v1.0.0 is the one worth a deliberate review (v0→v1 may carry
    intentional API breaks).

- **npm dev tool pinned and fast-moving**
  - **Location**: `gt-model-eval/package.json` — `devDependencies.promptfoo`
    `0.121.2` (lockfile present). `npm-package/package.json` declares no deps.
  - **Detail**: `promptfoo` is a 0.x, rapidly-iterating tool; the exact pin is
    correct for reproducibility. No production exposure (dev-only, eval harness).
    Renovate's npm manager covers it.

## Questions Answered

- **What deps would break first if upstream rotates?** The OTel **log** signals
  (`otlplog`/`otel/log`/`sdk/log`, all v0.x) are the most likely to break on
  upgrade because their API is not yet stabilized. After that, `glamour` v1.0.0
  (v0→v1 boundary).
- **What deps could be removed with no behavior change?** None at the
  main-module level — `go mod tidy` is already a no-op, so every direct
  declaration is imported. The `golang-lru` v1 / `blackfriday` v1 graph entries
  are not compiled, but they are transitive and cannot be dropped from this
  module's manifest.

## Counts

counts: critical=0 major=1 minor=5

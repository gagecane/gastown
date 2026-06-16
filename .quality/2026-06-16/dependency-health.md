# Dependency Health Audit

## Summary

Gas Town is a Go 1.26.2 application (`module github.com/steveyegge/gastown`) with
a single primary `go.mod` declaring **32 direct dependencies** and ~218 indirect
ones (250 module lines, 1724 `go.sum` entries). A second, self-contained module
lives under `plugins/dolt-snapshots/` (2 deps). Overall the manifest is in **good
health**: it builds and vets cleanly from the local module cache, has **zero unused
direct dependencies**, **zero `replace`/`exclude`/`retract` directives** (no
lingering security-override pins to reason about), and dependency updates are
actively automated via a well-configured `renovate.json` (weekly schedule,
auto-merge for non-major minor/patch, grouped Charm/x-stdlib updates, major
updates held for human review). Test-only direct deps (`testify`, `goleak`,
`testcontainers-go`) are correctly confined to test builds and do **not** leak
into the production `cmd/` binaries (verified: 0 `testcontainers` packages in
`go list -deps ./cmd/...`).

The top theme is **transitive surface area and pinning fragility**, not direct-dep
neglect. The dependency tree is dominated by the `dolthub/*` database ecosystem,
which pulls in a very large transitive closure (AWS SDK v2, Azure SDK, GCP cloud
libs, OCI/Aliyun SDKs, parquet, OpenTelemetry). Within that closure, **38
dependencies are commit-pinned pseudo-versions** (`v0.0.0-<timestamp>-<hash>`),
many in the `dolthub/*` group — these are the deps most exposed to "what breaks
first if upstream rotates." I could **not** run `govulncheck` or `go list -m
-u all` because the build host's network is DNS-sinkholed
(`chalupa-dns-sinkhole.corp.amazon.com`), so CVE and latest-version claims below
are marked for verification rather than asserted.

## Score

score: 0.82

## Critical Findings (P0 — file as beads, fix urgently)

None. No known-vulnerable dependency in a production path could be confirmed, and
no manifest integrity failures were found (both modules build and vet clean). CVE
status is **unverified** (see Major #1) — if a `govulncheck` run on a
network-enabled host surfaces a vulnerability in a production-reachable package,
it should be re-triaged to P0 at that time.

## Major Findings (P1 — track but do not auto-bead)

### M1. CVE / vulnerability scan could not be run — coverage gap
- **Location**: whole tree; tooling — `govulncheck` not installed, `GOPROXY`
  unreachable (`proxy.golang.org` blocked by `chalupa-dns-sinkhole`).
- **Impact**: This dimension's headline question ("any known-vulnerable deps in
  production paths?") is **unanswered**. Security-sensitive transitive deps are
  present and reachable from production code paths: `golang.org/x/crypto v0.52.0`,
  `golang.org/x/net v0.55.0`, `golang.org/x/oauth2 v0.35.0`,
  `google.golang.org/grpc v1.80.0`, `google.golang.org/protobuf v1.36.11`,
  `github.com/golang-jwt/jwt/v5 v5.3.0`, `github.com/go-jose/go-jose/v4 v4.1.4`,
  `github.com/sirupsen/logrus v1.9.4`. None are obviously stale, but none were
  scanned.
- **Suggested fix**: Run `govulncheck ./...` and `go list -m -u all` in CI (or on
  a network-enabled host) and gate on findings. Renovate covers version drift but
  does **not** perform vulnerability analysis; adding `govulncheck` closes the gap.

### M2. Heavy commit-pinned (pseudo-version) surface in the dolthub ecosystem
- **Location**: `go.mod` — 38 `v0.0.0-<ts>-<hash>` / `-0.<ts>-<hash>` pins, e.g.
  `github.com/dolthub/dolt/go v0.40.5-0.20260507221239-14b38e279fc6` (:115),
  `github.com/dolthub/go-mysql-server v0.20.1-0.20260507202550-43d6daf5958b`
  (:121), `github.com/dolthub/vitess v0.0.0-20260505163811-77e5224be390` (:125),
  `github.com/dolthub/ishell` (:123), `github.com/dolthub/go-icu-regex` (:120),
  `github.com/dolthub/eventsapi_schema` (:117).
- **Impact**: These are the deps that **break first if upstream rotates**.
  Commit-pinned pseudo-versions are not semver-tagged, so Renovate's minor/patch
  auto-merge rules (which match `!/^0/` current versions) largely do **not**
  apply to them — they drift only via Dolt's own coordinated bumps. A
  force-pushed or GC'd commit upstream would make `go mod download` fail. The
  whole `dolthub/*` group is the data plane (Dolt server / `go-mysql-server`),
  so a break here is high-blast-radius.
- **Suggested fix**: Track the Dolt ecosystem as a single coordinated bump unit
  (a Renovate `packageRules` group for `github.com/dolthub/**`, mirroring the
  existing Charm/x-stdlib groups) and pin to tagged releases where Dolt publishes
  them. Ensure the module cache is vendored or mirrored so a rotated upstream
  commit cannot break a clean build.

### M3. `go` directive skew between the two modules
- **Location**: `go.mod:3` (`go 1.26.2`) vs `plugins/dolt-snapshots/go.mod:3`
  (`go 1.23`); no `go.work` unifies them.
- **Impact**: The plugin module is two minor Go versions behind the main module.
  Both build today, but the skew means the plugin is developed/tested against an
  older language/stdlib baseline and is easy to forget during toolchain bumps.
  Renovate's `gomod` manager will not reconcile the `go` directive across modules.
- **Suggested fix**: Bump `plugins/dolt-snapshots/go.mod` to `go 1.26.x` to match,
  or document why it is intentionally held back. Confirm Renovate is configured to
  see the second module (its `gomod` config is global, so it should — verify the
  plugin module appears in Renovate's dependency dashboard).

## Minor Findings (P2 — informational)

### m1. Co-existing major versions of two transitive deps
- **Location**: `go.mod` — `github.com/cenkalti/backoff/v4 v4.3.0` (:92) **and**
  `/v5 v5.0.3` (:93); `github.com/hashicorp/golang-lru v1.0.2` (:154) **and**
  `/v2 v2.0.7` (:155).
- **Impact**: Negligible — Go treats `/vN` paths as distinct modules, so both
  versions resolve and link cleanly; this is normal in a large transitive tree.
  Slightly larger binary/closure. Worth noting only so a future reader does not
  mistake it for a conflict.
- **Suggested fix**: No action required. Resolves naturally as upstream
  transitive deps converge on v5 / v2.

### m2. Three YAML libraries in the resolved tree
- **Location**: `gopkg.in/yaml.v3 v3.0.1` (:37, direct), `gopkg.in/yaml.v2 v2.4.0`
  (:258, indirect), `go.yaml.in/yaml/v3 v3.0.4` (:240, indirect).
- **Impact**: Three YAML implementations pulled by different consumers. Only
  `yaml.v3` is a direct dep; the others are transitive. No correctness issue,
  modest closure bloat.
- **Suggested fix**: None now. If closure size becomes a concern, run
  `go mod why gopkg.in/yaml.v2` / `go.yaml.in/yaml/v3` to find the importers and
  see whether a transitive bump collapses them.

### m3. Test-only deps declared in the primary require block (hygiene, not a leak)
- **Location**: `go.mod:19,20,21,31` — `stretchr/testify`, `testcontainers-go`
  (+`/modules/dolt`), `go.uber.org/goleak`.
- **Impact**: None functionally — Go has no separate dev-dependency section, so
  test deps living in `require` is idiomatic. **Verified these do NOT leak into
  production**: `testify`/`goleak` appear only in `_test.go`; `testcontainers-go`
  is used in `internal/testutil/doltserver.go` (guarded `//go:build !windows`,
  consumed only by test code) and contributes **0 packages** to
  `go list -deps ./cmd/...`. Listed purely for completeness.
- **Suggested fix**: None.

### m4. `go-rod/rod` is direct but only reachable under the `browser` build tag
- **Location**: `go.mod:12`; sole importer `internal/web/browser_e2e_test.go:12`
  (`//go:build browser`).
- **Impact**: A naive "is this import referenced in a default build?" scan flags
  `rod` as unused; it is **not** — it is a legitimate e2e test dependency behind a
  build tag (`go mod why` confirms `internal/web → go-rod/rod`). Documented so a
  future automated dead-dep sweep does not remove it.
- **Suggested fix**: None. Optionally add a comment in `go.mod` noting the
  `browser`-tag usage to prevent accidental pruning.

## Counts

  counts: critical=0 major=3 minor=4

---

## Methodology & Caveats

- Tools used: `go build ./...` (PASS), `go vet` (clean), `go mod why`,
  `go list -deps -test ./...` and `go list -deps ./cmd/...`, all with
  `GOPROXY=off` against the local module cache. Manifests read directly.
- **Network was sinkholed** (`chalupa-dns-sinkhole.corp.amazon.com`), so
  `govulncheck`, `go list -m -u all` (latest-version drift), and proxy-backed
  `go mod` queries could not run. "Major-version-behind" and CVE claims are
  therefore **not asserted** — re-run on a network-enabled host to close M1.
- Scope: two Go modules (`/go.mod`, `plugins/dolt-snapshots/go.mod`) and two npm
  manifests (`npm-package/package.json` — runtime deps: none beyond Node engine;
  `gt-model-eval/package.json` — single private devDependency `promptfoo@0.121.2`).
  This is a read-only audit; **no source or manifest files were modified.**

## Sources

- [go.mod](go.mod) — accessed 2026-06-16
- [plugins/dolt-snapshots/go.mod](plugins/dolt-snapshots/go.mod) — accessed 2026-06-16
- [renovate.json](renovate.json) — accessed 2026-06-16
- [npm-package/package.json](npm-package/package.json) — accessed 2026-06-16
- [gt-model-eval/package.json](gt-model-eval/package.json) — accessed 2026-06-16

# Wiring Review

## Summary

The Go codebase has excellent dependency hygiene. `go mod tidy` produces no
diff — every module in go.mod is imported somewhere. All 31 direct dependencies
are actively used in production or test code. Config fields in
`DaemonPatrolConfig` (types.go) are all consumed by their corresponding dog
implementations. No "dead" SDK or library was detected.

The one notable finding is the `upstreamsync` package: config types are defined
and a CLI command exists (`gt upstream status`), but no daemon patrol dog
orchestrates automated sync operations. This appears to be intentional staged
delivery (CLI first, daemon automation later), but it's worth flagging as an
incomplete wiring that could confuse future contributors.

## Critical Issues

(None — no dependency or config is installed but completely unused.)

## Major Issues

(None.)

## Minor Issues

### P2-1: UpstreamSync config defined but no daemon patrol

`internal/config/types.go:764` defines `UpstreamSync *UpstreamSyncConfig` on
the rig settings struct, and `internal/upstreamsync/` implements the state
machine. However, there is no corresponding entry in `PatrolsConfig`
(internal/daemon/types.go) and no `runUpstreamSyncDog()` patrol function.

The CLI (`gt upstream status`) works, but the automated sync described in the
package doc ("automatically merges upstream/main into the fork's origin/main,
using polecat agents for conflict resolution") has no daemon entry point.

**Impact**: Low — clearly staged delivery. The CLI is the Phase 1 surface;
automated patrol is Phase 2.

**Suggested fix**: Add a `// TODO(upstream-sync): daemon patrol lands in Phase 2`
comment near PatrolsConfig, or add a disabled config stub:
```go
UpstreamSync *UpstreamSyncConfig `json:"upstream_sync,omitempty"` // Phase 2
```

### P2-2: `go-rod` only used in test files

`github.com/go-rod/rod v0.116.2` is a direct dependency in go.mod but only
imported in `internal/web/browser_e2e_test.go`. This is technically valid
(Go modules count test imports), but it inflates the dependency tree for
production builds.

**Impact**: Minimal — Go only downloads test deps when running tests. No
binary bloat since test-only imports aren't compiled into the production binary.

**Suggested fix**: No action needed. This is correct Go module behavior.
If the e2e test is rarely run, consider moving it to a `//go:build e2e` tag
to make the dependency conditional.

## Observations

- **go.mod is clean**: `go mod tidy` produces zero diff. No stale deps.
- **All PatrolsConfig fields are consumed**: Every config struct in types.go
  has a corresponding `isPatrolActive("...")` check and a `run*Dog()` function.
- **No dead env vars**: All `GT_*` and `BEADS_*` environment variables read
  via `os.Getenv()` are consumed by their surrounding logic.
- **Config-to-code traceability is strong**: Each daemon.json patrol key maps
  1:1 to a config struct, an interval function, and a runner function.
- **No SDK migration ghosts**: No cases where a new library was added alongside
  an old implementation doing the same thing (e.g., no dual logging frameworks,
  no competing HTTP clients).

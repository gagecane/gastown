# Resilience Review

## Summary

The codebase demonstrates strong resilience engineering overall. Timeouts are
pervasive (55 `WithTimeout`/`WithDeadline` calls in daemon/ alone), retry logic
with backoff is extensively used (163 references), and the daemon has a mature
recovery model (restart tracker, crash-loop guard, exponential backoff). The
system is explicitly designed to survive partial failures — each patrol dog runs
independently, and one dog's failure doesn't block others.

The main weaknesses are: (1) several `exec.Command` subprocess calls without
timeouts that could hang indefinitely, (2) a handful of swallowed errors that
reduce observability, and (3) two `panic()` calls in library code that would
crash the entire process rather than returning errors.

## Critical Issues

### P0-1: Two panics in library packages

**Files**:
- `internal/connection/address.go:135` — `panic(fmt.Sprintf("invalid address %q: %v", s, err))`
- `internal/workspace/find.go:191` — `panic(fmt.Sprintf("failed to get town name: %v", err))`

These panics in non-main packages will crash the entire daemon/CLI if triggered.
In a multi-agent system where processes must be long-lived, panics in utility
code are dangerous — they bypass all graceful shutdown, recovery, and
observability paths.

**Impact**: If a malformed config or filesystem issue triggers either path, the
daemon dies without logging, without escalating, and without cleanup.

**Suggested fix**: Return errors instead. Callers can decide whether to fatal:
```go
func ParseAddress(s string) (Address, error) {
    // ...
    return Address{}, fmt.Errorf("invalid address %q: %w", s, err)
}
```

## Major Issues

### P1-1: Git subprocess calls without timeouts

**File**: `internal/daemon/lifecycle.go:644-712`

The `syncWorkdirToUpstream` function calls `git fetch origin`, `git stash`,
`git pull --rebase`, and `git stash pop` via `exec.Command` (no context, no
timeout). If the git remote is unreachable or DNS hangs, these commands block
indefinitely — the daemon heartbeat stalls.

Specific lines:
- `lifecycle.go:644`: `exec.Command("git", "fetch", "origin")` — no timeout
- `lifecycle.go:667`: `exec.Command("git", "stash", ...)` — no timeout
- `lifecycle.go:686`: `exec.Command("git", "pull", "--rebase", ...)` — no timeout

Similarly in:
- `checkpoint_dog.go:257`: `exec.Command("git", ...)` — no timeout
- `commit_guard.go:101`: `exec.Command("git", ...)` — no timeout
- `dog_done.go:158`: `exec.Command(d.gtPath, "mail", ...)` — no timeout

**Impact**: A network partition or DNS failure could hang the daemon for hours.
The rest of the heartbeat loop (all patrol dogs, health checks) stops.

**Suggested fix**: Replace `exec.Command` with `exec.CommandContext` using a
30-60 second timeout:
```go
ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
defer cancel()
fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin")
```

### P1-2: Silent error swallowing in attribution state write

**File**: `internal/daemon/main_branch_test_attribution.go:140-145`

```go
if err := saveMainBranchTestState(townRoot, state); err != nil {
    // Don't fail the patrol over a state-write hiccup ...
    _ = err
}
```

The comment acknowledges that logging isn't available, but the error is entirely
discarded. If the state file becomes permanently unwriteable (disk full,
permissions), attribution state silently regresses to "unknown" on every patrol
cycle — and no operator ever learns why.

**Impact**: Silent loss of attribution data. D16 auto-revert depends on this
data; if it's consistently "unknown", the revert chain never fires.

**Suggested fix**: The function should return the error so the caller (which
has `d.logger`) can log it:
```go
if err := recordAttributionRun(...); err != nil {
    d.logger.Printf("main_branch_test: attribution state write failed: %v", err)
}
```

## Minor Issues

### P2-1: Best-effort operations without observability

Several daemon operations discard errors with `_ = err` comments like
"best-effort" or "ignore errors":

- `cmd/sling_helpers.go:1040`: `_ = bootCmd.Run()` — "rig might already be running"
- `cmd/start.go:897`: `_ = mayorGit.DeleteBranch(...)` — "Ignore errors"
- `doctor/rig_config_sync_check.go:497`: `_ = cmd.Run()` — "Best effort"

While individually reasonable, these collectively create blind spots. When
something fails silently in a multi-agent system, the failure compounds across
agents before anyone notices.

**Suggested fix**: Log at DEBUG or WARN level instead of discarding entirely.

### P2-2: No circuit breaker on Dolt connectivity

The daemon dogs shell out to `bd` which connects to Dolt. Each dog has its own
20s timeout, but there's no shared circuit-breaker that says "Dolt has been
failing for 5 minutes — stop polling and alert." Currently, each dog
independently retries on every tick even when Dolt is completely down.

**Impact**: Low — the daemon already has `gt dolt status` monitoring and the
doctor dog. But the dogs themselves don't short-circuit their bd calls when Dolt
is known-unhealthy.

## Observations

- **Timeout coverage is excellent** for `bd` subprocess calls (all use
  `exec.CommandContext` with 10-20s timeouts). The gap is specifically in `git`
  subprocess calls and a few `gt` CLI calls.
- **Recovery model is sophisticated**: RestartTracker provides exponential
  backoff with crash-loop detection. Mass-death detection escalates
  automatically. Deacon heartbeat monitoring auto-restarts stale sessions.
- **Graceful shutdown**: The daemon handles SIGINT/SIGTERM properly with
  context cancellation propagating through the system.
- **Partial failure isolation**: Each patrol dog runs in its own goroutine tick
  case — one dog's panic/hang doesn't crash others (modulo the main goroutine
  blocking on the hung dog's channel read).
- **Error messages are generally actionable**: Most error logs include the
  operation, the entity ID, and the underlying error — making triage possible
  from logs alone.

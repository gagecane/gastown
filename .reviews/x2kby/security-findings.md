# Security Review

## Summary

The recent code additions focus on the auto-test-pr pipeline (sandbox, cycle
close handler, CLI verbs, formula), and the upstream-sync substrate. The sandbox
implementation is architecturally sound — credential stripping, path containment,
network namespaces, and process-group kill on timeout form a defense-in-depth
model. The cycle-close handler and CLI verb code handles untrusted input (MR
bodies, bead content) with reasonable care. One notable gap exists in the
label-based rig name extraction, and one informational concern around the
circuit-breaker idempotency note.

## Critical Issues

(None found — no P0 security issues)

## Major Issues

### P1-1: extractRigFromMRLabels does not validate rig name content
**File:** `internal/cmd/auto_test_pr_revise.go` (line ~180)

The `extractRigFromMRLabels` function strips the `rig:` prefix from bead
labels and uses the remainder as a rig name for path construction
(`filepath.Join(townRoot, rigName, ".beads/...")`). If an attacker can
craft a bead label like `rig:../../etc/passwd`, the extracted value would
be a path traversal payload.

In the current system, bead labels are written by trusted polecats and the
Mayor via `bd update --add-label`, so the attack surface is limited to
agent compromise. However, the lack of validation violates defense-in-depth:
if any future codepath allows user-authored labels (e.g., PR comments parsed
into labels), this becomes exploitable.

**Recommended fix:** Validate that the extracted rig name matches `^[a-z][a-z0-9_]{0,63}$`
(or similar alphanumeric+underscore constraint) before using it in path operations.

### P1-2: HandleEvent idempotency gap on circuit-breaker counter
**File:** `internal/autotestpr/cycle_close_handler.go` (line ~115 comment)

The code comments explicitly acknowledge: "The circuit-breaker counter
increment is NOT idempotent." If the same MRCycleCloseEvent is delivered
twice (due to missed ack on the dog side), the counter increments twice,
potentially tripping the circuit breaker prematurely. While the synthesis
documents this trade-off, a premature SEV-2 nudge to the Overseer is a
denial-of-service on operator attention.

**Impact:** Availability concern — false circuit-breaker trips would auto-pause
rigs unnecessarily.

**Recommended fix:** Track processed MR IDs in the rejection log (they're already
appended there). Before incrementing the counter, check if the MR ID is already
in the last N rejection entries. If found, skip the counter increment.

## Minor Issues

### P2-1: Sandbox stripPrefixes does not include HOME or XDG_* env vars
**File:** `internal/autotest/sandbox/sandbox.go` (line 18)

The credential strip list removes `AWS_*`, `BD_*`, `DOLT_*`, `GIT_AUTHOR_*`,
`GIT_COMMITTER_*`, and `GITHUB_TOKEN`. However, `HOME` and `XDG_*` variables
are left intact, giving the sandboxed subprocess access to the user's home
directory paths (including `~/.aws/credentials`, `~/.ssh/`, etc.). The network
namespace prevents exfiltration in the offline case, but if the test code
reads `$HOME/.aws/credentials` and includes its content in test output
(which is captured), the credential could leak into bead NOTES or MR bodies.

**Impact:** Low (network is dropped, so exfiltration to external services is
blocked), but worth noting for defense-in-depth.

### P2-2: WarmUpGoModules runs with network ON under Apply (not ApplyOffline)
**File:** `internal/autotest/sandbox/offline.go` (line ~75)

The warm-up step intentionally runs with network access (to fetch modules from
the Go proxy). This is the correct design for its purpose, but it creates a
window where credential env vars are stripped but network is available. If the
Go module proxy is compromised or a dependency pulls in a `postinstall`-like
hook, it could reach the network during warm-up.

**Impact:** Theoretical — Go module downloads don't execute arbitrary code during
`go mod download`, and the toolchain verifies checksums via `go.sum`. The risk
is bounded by Go's supply-chain guarantees.

### P2-3: fileBugBead uses unsanitized description in bug title
**File:** `internal/autotestpr/cycle_close_handler.go` (line ~365)

```go
title := fmt.Sprintf("Bug from auto-test-pr: %s", truncate(bug.Description, 60))
```

The `bug.Description` is parsed from MR body text (untrusted content written by
the polecat). While `truncate` limits length, no sanitization removes control
characters, newlines, or format specifiers. Bead titles are displayed in CLI
output and could cause terminal injection if they contain ANSI escape sequences.

**Recommended fix:** Strip non-printable characters and newlines from
`bug.Description` before inserting into the title.

## Observations

- **Positive:** The sandbox's `Resolve()` method correctly handles symlink escape
  attacks by calling `filepath.EvalSymlinks` and checking the resolved path is
  still inside the worktree. This prevents a test file from symlinking to
  `/etc/shadow` and having the sandbox serve it.

- **Positive:** The `killProcessGroup` approach in timeout.go ensures child
  processes spawned by `go test` are also killed on timeout, preventing
  resource exhaustion from zombie processes.

- **Positive:** The `applyNetNamespace` implementation uses CLONE_NEWUSER +
  CLONE_NEWNET, which is the correct unprivileged approach. The identity
  uid/gid mappings avoid the privilege escalation pitfalls of broader
  namespace configurations.

- **Positive:** The CAS (compare-and-swap) pattern on state transitions prevents
  TOCTOU races between concurrent event handlers or re-delivered events.

- **Informational:** The `GidMappingsEnableSetgroups = false` setting is critical
  — without it, the kernel rejects gid_map writes in unprivileged user namespaces.
  This is correctly documented in the code comment.

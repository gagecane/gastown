// Package sandbox provides a hardened-execution substrate for the
// auto-test-pr quality gates (coverage-delta, mutant runner, flakiness
// rerun, tautology linter, gitleaks). It strips credential environment
// variables, pins each subprocess's working directory to the polecat
// worktree, and (in later phases) drops network egress and enforces
// per-target wall-clock caps.
//
// # ADR — sandbox substrate (Phase 0 task 5a-pre)
//
// Status: Accepted (2026-05-21).
//
// Context: Phase 0 of auto-test-pr (.designs/auto-test-pr/synthesis.md)
// requires the polecat formula and the gate runners to execute every
// `go test`, mutant, and lint subprocess under a hardened sandbox. The
// synthesis (round 2 fix #5) flagged that "gt sandbox or equivalent"
// phrasing left implementation strategy uncommitted, and that tasks
// 5a/5b/5c could otherwise target three different substrates. This ADR
// resolves that ambiguity before any of 5a/5b/5c implementation lands.
//
// Decision: option (b) — a Go library at internal/autotest/sandbox.
//
// Three options were considered:
//
//   - (a) Wrapper command (`gt sandbox <cmd>...`) — invoked by every
//     gate as a child process. Rejected: spawns an extra exec per gate
//     invocation (≥5 gates × N targets per cycle), inflates wall-clock
//     by O(forks), and forces all sandbox parameters (worktree path,
//     env-strip list, time budget) through CLI flags or env vars,
//     inverting the simpler in-process configuration available in (b).
//   - (b) Go library that decorates an *exec.Cmd in-process. Selected
//     (recommended by synthesis). Composes naturally with the existing
//     internal/beads/exec.go pattern (ConfigureCommand on an exec.Cmd
//     handed in by the caller); zero extra forks; testable without a
//     `gt` binary on PATH; same substrate is reachable from polecat
//     molecule code, gate runners, and the future internal/autotestpr/
//     Mayor cycle code without duplicating logic.
//   - (c) Inline per-gate code. Rejected: would copy-paste the env-
//     strip and CWD-pin policy across at least five gate runners, and
//     each copy would be its own audit surface for the security leg's
//     credential-leakage threat model.
//
// Consequences:
//
//   - 5a, 5b, 5c all extend this package; the substrate decision is
//     stable for the duration of Phase 0. Per the synthesis, deviation
//     requires an ADR amendment in this file before implementation.
//   - The library MUST remain leaf-level inside internal/autotest:
//     it depends only on the Go standard library and other leaf
//     packages (no internal/beads, no internal/cmd) so it can be
//     consumed from any gate runner without creating import cycles.
//   - Callers retain ownership of the *exec.Cmd lifecycle; the
//     sandbox does not Run/Start the command itself, only configures
//     it. This keeps composition with context.Context, cmd.Stdout
//     wiring, and process-group handling under the caller's control.
//
// # Surface area (Phase 0 task 5a)
//
// 5a delivers two primitives:
//
//   - Credential-strip: removes every environment variable matching
//     the prefix list AWS_*, BD_*, DOLT_*, GIT_AUTHOR_*, GIT_COMMITTER_*
//     plus the exact-match name GITHUB_TOKEN. The strip set is taken
//     directly from the synthesis ("5a strips AWS_*, GITHUB_TOKEN,
//     BD_*, DOLT_*, GIT_AUTHOR_*, GIT_COMMITTER_*; pins CWD to the
//     worktree").
//   - CWD-pin: every command runs with cmd.Dir anchored to the
//     polecat's worktree (validated as an absolute, existing
//     directory). The Resolve helper rejects any user-supplied
//     relative path whose cleaned form escapes the worktree (".."
//     traversal, absolute paths, or symlinks that resolve outside
//     the worktree).
//
// # Surface area (Phase 0 task 5b)
//
// 5b adds two primitives, layered on the 5a Sandbox value:
//
//   - Network-drop (ApplyOffline): an Apply variant that additionally
//     starts the subprocess in a fresh user + network namespace
//     (Linux only; netDropSupported reports false on other GOOSes).
//     The user namespace is required because the kernel only permits
//     unprivileged netns creation in combination with userns; identity
//     uid/gid mappings keep the in-namespace user identical to the
//     caller. Inside the namespace, only loopback exists (and starts
//     DOWN), so any TCP/UDP dial returns "network is unreachable".
//     Resolves the security leg's open question 1 and the synthesis
//     Round 2 fix #7 acceptance criterion that gates run with no
//     fresh network fetch.
//   - Module-cache warm-up (WarmUpGoModules): runs `go mod download`
//     followed by `go test -count=1 -run='^$' ./...` (a no-op test
//     pass that compiles the same package graph the real test run
//     will execute), both under Apply (network ON). The compile-only
//     pass is invoked unconditionally rather than as a fallback,
//     because the synthesis Round 2 fix #7 documents that
//     `go mod download` alone does not always populate transitively-
//     missing test-only imports. Doing both up-front guarantees the
//     subsequent ApplyOffline `go test -count=10 ./...` makes zero
//     network calls.
//
// 5c adds the wall-clock cap and an integration test of the combined
// wrapper. It extends the same Sandbox value.
//
// # Usage
//
//	sb, err := sandbox.New(worktreeDir)
//	if err != nil {
//	    return err
//	}
//	// Warm up while network is still available.
//	if err := sb.WarmUpGoModules(ctx, "go"); err != nil {
//	    return err
//	}
//	// Run gates with no network access.
//	cmd := exec.CommandContext(ctx, "go", "test", "-count=10", "./...")
//	if err := sb.ApplyOffline(cmd); err != nil {
//	    return err
//	}
//	if err := cmd.Run(); err != nil {
//	    return err
//	}
//
// # Thread safety
//
// A Sandbox value is immutable after New returns; methods are safe
// for concurrent use across goroutines. Apply, ApplyOffline, and
// WarmUpGoModules mutate only the caller-provided *exec.Cmd (or, in
// the case of WarmUpGoModules, exec.Cmd values it constructs
// internally).
package sandbox

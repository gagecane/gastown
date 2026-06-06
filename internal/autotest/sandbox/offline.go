package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ErrNetDropUnsupported is returned by ApplyOffline on platforms
// without unprivileged user+network namespaces. Callers SHOULD
// treat this as a hard failure on the auto-test-pr critical path
// (the synthesis requires network-drop), and degrade gracefully
// only on platforms that are not part of the v1 substrate (the
// pilot rig is Linux per the synthesis).
var ErrNetDropUnsupported = errors.New("sandbox: network-drop unsupported on this OS")

// ApplyOffline configures cmd identically to Apply (cred-strip,
// CWD-pin) and additionally launches it in a fresh user + network
// namespace, so any TCP/UDP dial inside cmd fails with `network
// is unreachable`. The user namespace is required because creating
// a netns from an unprivileged process is only permitted in
// combination with a userns. Identity uid/gid mappings are used
// so cmd's view of the filesystem and uid is unchanged from the
// caller's view.
//
// On non-Linux builds (or on a kernel where unprivileged userns
// creation is disabled), ApplyOffline returns ErrNetDropUnsupported
// without modifying cmd.
//
// ApplyOffline is the gate-runner-facing primitive. Callers MUST
// call WarmUpGoModules first if cmd resolves to `go test` against
// a module whose dependencies are not already populated in
// GOMODCACHE — see WarmUpGoModules's docs for the contract.
func (s *Sandbox) ApplyOffline(cmd *exec.Cmd) error {
	if cmd == nil {
		return errors.New("sandbox: nil cmd")
	}
	if !netDropSupported() {
		return ErrNetDropUnsupported
	}
	if err := s.Apply(cmd); err != nil {
		return err
	}
	applyNetNamespace(cmd)
	return nil
}

// WarmUpGoModules populates the Go module cache for the worktree
// so a subsequent ApplyOffline `go test` does not need network
// access. It runs two commands under Apply (network ON):
//
//   - `go mod download` — fetches every required module to the
//     module cache; subsequent compilations under -mod=readonly /
//     -mod=mod consume the cache without dialing the proxy.
//   - `go test -count=1 -run='^$' ./...` — a no-op test pass that
//     forces Go to compile the same package graph the real test
//     run will execute. This catches transitively-missing test-
//     only imports that `go mod download` does not always populate
//     (synthesis Round 2 fix #7: "if even one rerun triggers a
//     fetch, the warm-up step is amended to also run [the no-op
//     test pass]"). We perform both steps unconditionally so the
//     "amended" behavior is the default; running it always costs
//     one extra compile pass but eliminates the failure mode
//     entirely.
//
// Both subprocesses inherit the caller's PATH and GOPATH-derived
// settings via Apply (cred env-strip is applied — module proxies
// generally do not require credentials and the strip set in 5a
// does not include GOPROXY/GONOSUMCHECK/etc.).
//
// goBin is the path to the `go` toolchain to use ("" → "go" on
// PATH). Tests pass an explicit binary so they do not depend on
// the host's $PATH.
//
// Output from the warm-up subprocesses is silently discarded on
// success; on failure the error wraps both step-name and the
// underlying CombinedOutput so callers can surface diagnostics.
func (s *Sandbox) WarmUpGoModules(ctx context.Context, goBin string) error {
	if goBin == "" {
		goBin = "go"
	}
	if err := s.disableGoTelemetry(); err != nil {
		return fmt.Errorf("warm-up: disable go telemetry: %w", err)
	}
	steps := []struct {
		name string
		args []string
	}{
		{"go mod download", []string{"mod", "download"}},
		{"go test compile-only", []string{"test", "-count=1", "-run=^$", "./..."}},
	}
	for _, step := range steps {
		cmd := exec.CommandContext(ctx, goBin, step.args...)
		if err := s.Apply(cmd); err != nil {
			return fmt.Errorf("warm-up %s: apply: %w", step.name, err)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("warm-up %s: %w: %s", step.name, err, out)
		}
	}
	return nil
}

// disableGoTelemetry seeds a Go telemetry "mode" file set to "off"
// inside the sandbox's config directory, before any `go` subprocess
// runs.
//
// Apply pins HOME and XDG_CONFIG_HOME to worktree-internal paths, so
// the Go toolchain resolves its telemetry directory to
// <worktree>/.config/go/telemetry. With telemetry in its default
// "local" mode, `go` (a) writes counter files there and (b) forks a
// DETACHED telemetry sidecar child (golang.org/x/telemetry) that
// keeps creating files under that directory asynchronously, AFTER the
// parent `go` process has exited. When the worktree is an ephemeral
// t.TempDir(), that post-exit write races t.Cleanup's RemoveAll and
// surfaces as "TempDir RemoveAll cleanup: ... directory not empty"
// (gu-lawyx). On the production gate path it leaves a stray child
// touching the worktree after the gate run reports complete.
//
// Setting mode "off" makes telemetry.Start return before opening the
// counter file or forking the sidecar, so nothing writes to the
// directory out-of-band. "off" is the authoritative control: the Go
// toolchain reads the mode exclusively from this file and provides no
// environment-variable override (GOTELEMETRY/GOTELEMETRYDIR are
// read-only `go env` values). Writing the file is idempotent and
// cheap; it is the same format `go telemetry off` produces.
func (s *Sandbox) disableGoTelemetry() error {
	dir := filepath.Join(s.worktree, ".config", "go", "telemetry")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "mode"), []byte("off"), 0o600)
}

// NetDropSupported reports whether ApplyOffline produces a real
// network-drop on this build. Callers (e.g. integration tests
// that opt out on unsupported platforms) SHOULD consult this
// before invoking ApplyOffline.
func NetDropSupported() bool {
	return netDropSupported()
}

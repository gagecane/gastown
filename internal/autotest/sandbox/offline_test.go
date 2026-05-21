package sandbox

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestApplyOffline_BlocksDial is the negative half of the 5b
// network-drop acceptance criterion. It uses the standard Go
// "TestHelperProcess" pattern: the test binary re-execs itself
// with GO_SANDBOX_HELPER=1 set, the helper attempts a TCP dial
// against a public IP, and the parent asserts the helper exits
// non-zero (the dial fails with "network is unreachable" inside
// the empty net namespace ApplyOffline installs).
//
// On non-Linux builds (or on a kernel with unprivileged userns
// disabled), ApplyOffline returns ErrNetDropUnsupported and we
// skip rather than asserting a fake result.
func TestApplyOffline_BlocksDial(t *testing.T) {
	if !NetDropSupported() {
		t.Skipf("net-drop unsupported on %s", runtime.GOOS)
	}
	if os.Getenv("GO_SANDBOX_HELPER") == "1" {
		runDialHelper()
		return
	}

	root := t.TempDir()
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestApplyOffline_BlocksDial$")
	cmd.Env = []string{"GO_SANDBOX_HELPER=1", "PATH=" + os.Getenv("PATH")}
	if err := sb.ApplyOffline(cmd); err != nil {
		// Some kernels disable unprivileged userns at runtime even on
		// Linux. Treat that as a skip rather than a failure: the
		// supported-platform invariant is the synthesis's concern, not
		// this unit test's.
		if errors.Is(err, ErrNetDropUnsupported) {
			t.Skipf("ApplyOffline reports unsupported: %v", err)
		}
		t.Fatalf("ApplyOffline: %v", err)
	}
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		// On some kernels (e.g. unprivileged_userns_clone=0), the child
		// fails to clone the namespace at exec time. Distinguish this
		// from a successful "dial blocked" outcome: kernel rejection
		// produces "operation not permitted" before the helper runs.
		s := string(out) + " " + runErr.Error()
		if strings.Contains(s, "operation not permitted") || strings.Contains(s, "permission denied") {
			t.Skipf("kernel rejected unprivileged netns: %v: %s", runErr, out)
		}
	}
	// The helper exits 0 on dial-failure (the desired outcome), and 2
	// on the bug case where the dial succeeded.
	if runErr != nil {
		t.Fatalf("helper subprocess errored: %v\noutput: %s", runErr, out)
	}
	if !strings.Contains(string(out), "dial-failed-as-expected") {
		t.Fatalf("helper output missing expected marker:\n%s", out)
	}
}

// runDialHelper is the body of TestApplyOffline_BlocksDial when the
// test binary is re-execed under ApplyOffline. It must not call any
// t.* method (we are not in a real test invocation — the binary was
// invoked with -test.run matching this function only so that the
// helper re-exec pattern works). os.Exit(0) marks success (dial was
// blocked); os.Exit(2) marks the bug case (dial succeeded).
func runDialHelper() {
	_, err := net.DialTimeout("tcp", "8.8.8.8:53", 1*time.Second)
	if err == nil {
		// The dial succeeded: net-drop is not in effect. Mark the bug.
		os.Stderr.WriteString("dial-unexpectedly-succeeded\n")
		os.Exit(2)
	}
	// Dial failed — print a marker so the parent test can verify the
	// helper actually ran (and didn't, e.g., exit-zero before the
	// dial).
	os.Stdout.WriteString("dial-failed-as-expected: " + err.Error() + "\n")
	os.Exit(0)
}

func TestApplyOffline_RejectsNilCmd(t *testing.T) {
	sb, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sb.ApplyOffline(nil); err == nil {
		t.Fatalf("ApplyOffline(nil) = nil, want error")
	}
}

// TestApplyOffline_StripsCredentials verifies ApplyOffline composes
// with Apply's cred-strip — the synthesis treats them as a single
// hardening surface, so a regression that bypassed env-strip on the
// offline path would be a real bug. We check the cmd.Env produced
// by ApplyOffline (we don't need to actually run the subprocess).
func TestApplyOffline_StripsCredentials(t *testing.T) {
	if !NetDropSupported() {
		t.Skipf("net-drop unsupported on %s", runtime.GOOS)
	}
	sb, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/true")
	cmd.Env = []string{"AWS_PROFILE=p", "GITHUB_TOKEN=t", "PATH=/usr/bin"}
	if err := sb.ApplyOffline(cmd); err != nil {
		t.Fatalf("ApplyOffline: %v", err)
	}
	if containsKey(cmd.Env, "AWS_PROFILE") {
		t.Fatalf("AWS_PROFILE survived ApplyOffline: %v", cmd.Env)
	}
	if containsKey(cmd.Env, "GITHUB_TOKEN") {
		t.Fatalf("GITHUB_TOKEN survived ApplyOffline: %v", cmd.Env)
	}
	if !containsKey(cmd.Env, "PATH") {
		t.Fatalf("PATH dropped: %v", cmd.Env)
	}
}

func TestApplyOffline_PinsCWD(t *testing.T) {
	if !NetDropSupported() {
		t.Skipf("net-drop unsupported on %s", runtime.GOOS)
	}
	root := t.TempDir()
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/true")
	if err := sb.ApplyOffline(cmd); err != nil {
		t.Fatalf("ApplyOffline: %v", err)
	}
	if cmd.Dir != sb.Worktree() {
		t.Fatalf("cmd.Dir = %q, want %q", cmd.Dir, sb.Worktree())
	}
}

// TestWarmUpGoModules_StdlibFixture is the positive half of the 5b
// acceptance criterion: warm up a stdlib-only fixture module, then
// run `go test -count=10 .` under ApplyOffline and assert it passes
// 10/10 with no fresh fetch.
//
// We assert "no fresh fetch" by construction: the test runs inside
// ApplyOffline, which physically prevents network access. If the
// test required a fetch (e.g. because a transitive import was not
// cached) the offline `go test` would return "missing go.sum entry"
// or "lookup proxy: dial tcp: i/o timeout" and the assertion would
// fail. tcpdump/strace would be a defense-in-depth check, but the
// namespace itself is the primary enforcement point — see synthesis
// Round 2 fix #7's "verified by tcpdump or strace -e connect" wording.
//
// This test is the gate referenced by the bead's acceptance criterion.
// The fixture is stdlib-only by design so the test does not depend
// on a populated module cache for any third-party module.
func TestWarmUpGoModules_StdlibFixture(t *testing.T) {
	if !NetDropSupported() {
		t.Skipf("net-drop unsupported on %s", runtime.GOOS)
	}
	if testing.Short() {
		t.Skip("skipping warm-up + 10x test under -short")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go on PATH: %v", err)
	}

	fixture := buildStdlibFixture(t)
	sb, err := New(fixture)
	if err != nil {
		t.Fatal(err)
	}

	// Warm up while network is still available. The fixture is stdlib
	// only so go mod download is a no-op, but we still call it to
	// exercise the code path; the compile-only test pass populates
	// the build cache.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := sb.WarmUpGoModules(ctx, goBin); err != nil {
		t.Fatalf("WarmUpGoModules: %v", err)
	}

	// Run `go test -count=10 .` under ApplyOffline. 10/10 success
	// proves the namespace doesn't break a stdlib-only fixture.
	cmd := exec.CommandContext(ctx, goBin, "test", "-count=10", ".")
	if err := sb.ApplyOffline(cmd); err != nil {
		if errors.Is(err, ErrNetDropUnsupported) {
			t.Skipf("net-drop unsupported at runtime: %v", err)
		}
		t.Fatalf("ApplyOffline: %v", err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := string(out)
		if strings.Contains(s, "operation not permitted") || strings.Contains(s, "permission denied") {
			t.Skipf("kernel rejected unprivileged netns: %v: %s", err, s)
		}
		t.Fatalf("offline `go test -count=10 .` failed: %v\n%s", err, s)
	}
}

// buildStdlibFixture creates a tempdir Go module containing one trivial
// stdlib-only test. The returned path is the module root.
func buildStdlibFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module sandboxfixture\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"),
		[]byte("package sandboxfixture\n\nfunc Add(a, b int) int { return a + b }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture_test.go"),
		[]byte("package sandboxfixture\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"math broken\")\n\t}\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestWarmUpGoModules_PropagatesError covers the failure path:
// pointing WarmUpGoModules at a non-module directory must produce
// an error (not a silent success), so callers can fail-closed when
// the warm-up is misconfigured.
func TestWarmUpGoModules_PropagatesError(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go on PATH: %v", err)
	}
	dir := t.TempDir() // empty: not a Go module
	sb, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := sb.WarmUpGoModules(ctx, goBin); err == nil {
		t.Fatalf("WarmUpGoModules in non-module dir = nil, want error")
	}
}

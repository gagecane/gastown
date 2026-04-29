package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultAllowedSubcmdsIsParseable guards against a regression where the
// built-in default list becomes malformed and silently degrades the proxy's
// allowlist to "nothing".
func TestDefaultAllowedSubcmdsIsParseable(t *testing.T) {
	got := parseAllowedSubcmds(defaultAllowedSubcmds)
	require.NotNil(t, got, "defaultAllowedSubcmds must parse to a non-nil map")

	assert.Contains(t, got, "gt", "gt commands must be in the default allowlist")
	assert.Contains(t, got, "bd", "bd commands must be in the default allowlist")

	// Sanity-check a handful of specific subcommands that polecats rely on for
	// basic operation. If any of these drift out, sandboxed polecats break.
	gtSubs := got["gt"]
	for _, required := range []string{"prime", "hook", "done", "mail", "nudge"} {
		assert.Contains(t, gtSubs, required,
			"gt:%s must remain in defaultAllowedSubcmds", required)
	}

	bdSubs := got["bd"]
	for _, required := range []string{"create", "update", "close", "show", "list", "ready"} {
		assert.Contains(t, bdSubs, required,
			"bd:%s must remain in defaultAllowedSubcmds", required)
	}
}

// TestDefaultAllowedSubcmdsExcludesDangerousCommands guards against someone
// accidentally adding destructive subcommands (e.g. `gt nuke`, `gt rig`,
// `gt polecat`) to the sandbox allowlist.
func TestDefaultAllowedSubcmdsExcludesDangerousCommands(t *testing.T) {
	got := parseAllowedSubcmds(defaultAllowedSubcmds)
	gtSubs := got["gt"]

	dangerous := []string{"nuke", "polecat", "rig", "admin"}
	for _, sub := range dangerous {
		assert.NotContains(t, gtSubs, sub,
			"defaultAllowedSubcmds must NOT include dangerous gt subcommand %q", sub)
	}
}

// ----------------------------------------------------------------------------
// discoverAllowedSubcmds: PATH-shadow the `gt` binary
// ----------------------------------------------------------------------------

// shadowGT creates a temporary directory containing a fake `gt` (or `gt.bat` on
// Windows) whose output is controlled by the caller, then sets PATH so that the
// fake binary is found instead of the real one. t.Setenv restores PATH on
// cleanup automatically.
func shadowGT(t *testing.T, script string) {
	t.Helper()

	dir := t.TempDir()

	if runtime.GOOS == "windows" {
		batPath := filepath.Join(dir, "gt.bat")
		require.NoError(t, os.WriteFile(batPath, []byte(script), 0o755)) //nolint:gosec
	} else {
		shPath := filepath.Join(dir, "gt")
		require.NoError(t, os.WriteFile(shPath, []byte("#!/bin/sh\n"+script), 0o755)) //nolint:gosec
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestDiscoverAllowedSubcmds_UsesFakeGTOutput(t *testing.T) {
	expected := "gt:foo,bar;bd:baz"
	if runtime.GOOS == "windows" {
		shadowGT(t, "@echo "+expected+"\r\n")
	} else {
		shadowGT(t, "echo '"+expected+"'\n")
	}

	got := discoverAllowedSubcmds()
	assert.Equal(t, expected, got,
		"discoverAllowedSubcmds should return the trimmed stdout of `gt proxy-subcmds`")
}

func TestDiscoverAllowedSubcmds_FallsBackOnCommandFailure(t *testing.T) {
	// Fake gt exits non-zero; discoverAllowedSubcmds should fall back to the default.
	if runtime.GOOS == "windows" {
		shadowGT(t, "@exit 1\r\n")
	} else {
		shadowGT(t, "exit 1\n")
	}

	got := discoverAllowedSubcmds()
	assert.Equal(t, defaultAllowedSubcmds, got,
		"failed discovery must fall back to defaultAllowedSubcmds")
}

func TestDiscoverAllowedSubcmds_FallsBackOnEmptyOutput(t *testing.T) {
	// Fake gt exits 0 with empty output; discoverAllowedSubcmds should fall back.
	if runtime.GOOS == "windows" {
		shadowGT(t, "@exit 0\r\n")
	} else {
		shadowGT(t, "exit 0\n")
	}

	got := discoverAllowedSubcmds()
	assert.Equal(t, defaultAllowedSubcmds, got,
		"empty output must fall back to defaultAllowedSubcmds")
}

func TestDiscoverAllowedSubcmds_TrimsWhitespace(t *testing.T) {
	// A real `gt proxy-subcmds` may end with a newline; TrimSpace must remove it.
	payload := "gt:prime"
	if runtime.GOOS == "windows" {
		shadowGT(t, "@echo    "+payload+"   \r\n")
	} else {
		// printf avoids echo's whitespace-collapsing behaviour across shells.
		shadowGT(t, "printf '   "+payload+"   \\n'\n")
	}

	got := discoverAllowedSubcmds()
	assert.Equal(t, payload, got, "output must be TrimSpace'd")
}

func TestDiscoverAllowedSubcmds_FallsBackWhenGTNotOnPATH(t *testing.T) {
	// Point PATH at an empty temp dir so no `gt` binary can be found.
	t.Setenv("PATH", t.TempDir())

	got := discoverAllowedSubcmds()
	assert.Equal(t, defaultAllowedSubcmds, got,
		"missing gt binary must fall back to defaultAllowedSubcmds")
}

// ----------------------------------------------------------------------------
// Integration: build the server binary and exercise the error-exit path
// ----------------------------------------------------------------------------

// buildServer compiles gt-proxy-server once per test and returns its path.
func buildServer(t *testing.T) string {
	t.Helper()

	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available; skipping build-based integration test")
	}

	dir := t.TempDir()
	binName := "gt-proxy-server-test"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(dir, binName)

	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}
	return binPath
}

// TestServerExitsOnBadConfigPath verifies that pointing --config at a file with
// malformed JSON causes a non-zero exit and a log message mentioning the path.
// This exercises the main() error path without starting the actual listener.
func TestServerExitsOnBadConfigPath(t *testing.T) {
	binPath := buildServer(t)

	badCfg := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(badCfg, []byte("{not valid json"), 0o644)) //nolint:gosec

	cmd := exec.Command(binPath, //nolint:gosec // test binary in t.TempDir()
		"--config", badCfg,
		"--listen", "127.0.0.1:0",
		"--admin-listen", "",
		"--ca-dir", t.TempDir(),
		"--town-root", t.TempDir(),
	)

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()

	assert.Error(t, err, "server must exit non-zero on malformed config")

	// slog's default handler writes to stderr. Check both just in case.
	combined := outBuf.String() + errBuf.String()
	assert.Contains(t, combined, badCfg,
		"error output must mention the offending config path\ncombined: %s", combined)
	assert.Contains(t, combined, "failed to load config",
		"error output must include the failure message")
}

// TestServerHelpFlag exercises the `--help` path through flag.Parse. It's a
// cheap smoke test that confirms the binary builds, wires up flags, and prints
// a usage message on stderr.
func TestServerHelpFlag(t *testing.T) {
	binPath := buildServer(t)

	cmd := exec.Command(binPath, "--help") //nolint:gosec // test binary in t.TempDir()
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	_ = cmd.Run() // flag.ExitOnError returns exit 0 for --help, non-zero for unknown

	combined := outBuf.String() + errBuf.String()
	// Every flag defined in main() should show up in the usage output.
	for _, expected := range []string{
		"-config",
		"-listen",
		"-admin-listen",
		"-ca-dir",
		"-allowed-cmds",
		"-allowed-subcmds",
		"-town-root",
	} {
		assert.Contains(t, combined, expected,
			"--help output should mention flag %q", expected)
	}
}

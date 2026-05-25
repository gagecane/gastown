package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// buildGT builds the gt binary and returns its path.
//
// It uses sync.Once to ensure the binary is built at most once per test process,
// and reuses an existing on-disk binary if it was built more recently than the
// most recent source change (avoids ~6s rebuild per `go test` invocation).
var (
	gtBinaryOnce sync.Once
	gtBinaryPath string
	gtBinaryErr  error
)

func buildGT(t *testing.T) string {
	t.Helper()

	gtBinaryOnce.Do(func() {
		gtBinaryPath, gtBinaryErr = buildGTOnce()
	})
	if gtBinaryErr != nil {
		t.Fatalf("buildGT: %v", gtBinaryErr)
	}
	return gtBinaryPath
}

// buildGTOnce builds or reuses the gt integration-test binary.
// It checks whether the existing binary on disk is newer than the most recent
// source modification, allowing cross-process reuse when source hasn't changed.
func buildGTOnce() (string, error) {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return "", err
	}

	binaryName := "gt-integration-test"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	tmpBinary := filepath.Join(os.TempDir(), binaryName)

	// Check if existing binary is fresh (newer than all .go sources).
	if binaryIsFresh(tmpBinary, projectRoot) {
		return tmpBinary, nil
	}

	// Must set BuiltProperly=1 via ldflags, otherwise binary refuses to run.
	ldflags := "-X github.com/steveyegge/gastown/internal/cmd.BuiltProperly=1"
	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", tmpBinary, "./cmd/gt")
	cmd.Dir = projectRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", &buildError{err: err, output: output}
	}
	return tmpBinary, nil
}

// binaryIsFresh reports whether the binary at path exists and is newer than
// the most recently modified .go file (or go.mod/go.sum) under projectRoot.
func binaryIsFresh(binaryPath, projectRoot string) bool {
	binInfo, err := os.Stat(binaryPath)
	if err != nil {
		return false
	}
	binMod := binInfo.ModTime()

	// Walk source tree looking for any file newer than the binary.
	// We check .go, go.mod, and go.sum — the inputs to `go build`.
	stale := false
	_ = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable dirs
		}
		// Skip hidden dirs and vendor.
		name := d.Name()
		if d.IsDir() && (name == ".git" || name == "vendor" || name == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		// Only check Go build inputs.
		ext := filepath.Ext(name)
		if ext != ".go" && name != "go.mod" && name != "go.sum" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(binMod) {
			stale = true
			return filepath.SkipAll
		}
		return nil
	})
	return !stale
}

// findProjectRoot walks up from cwd to find the directory containing go.mod.
func findProjectRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", &buildError{err: os.ErrNotExist, output: []byte("could not find project root (go.mod)")}
		}
		dir = parent
	}
}

// buildError wraps a build failure with its output for diagnostics.
type buildError struct {
	err    error
	output []byte
}

func (e *buildError) Error() string {
	return "failed to build gt: " + e.err.Error() + "\nOutput: " + string(e.output)
}

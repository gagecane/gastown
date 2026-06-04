package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// obsoleteBdSkipMarker is the t.Skip reason that four bead-lifecycle tests used
// to carry while the bd 0.47.2 "database writes don't commit" auto-flush bug was
// unfixed (gs-98w). The bug is fixed in bd >= 1.0 and all four tests are
// re-enabled. Those skips were permanent and invisible — a real regression in
// the paths they cover (finding an agent's hooked bead, nuke→respawn reuse,
// prime's hooked-bead state, session cost accounting) would have shipped green.
const obsoleteBdSkipMarker = `t.Skip("bd CLI 0.47.2`

// TestNoObsoleteBdSkips fails if any Go test re-introduces a
// t.Skip("bd CLI 0.47.2 ...") marker. The underlying bd bug is fixed, so such a
// skip can only be a stale copy-paste that silently disables coverage. If a new
// bd bug genuinely blocks a test, skip it with a reason naming the live tracking
// bead rather than reviving this obsolete string.
func TestNoObsoleteBdSkips(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	var violations []string
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip vendored and VCS directories.
			if name := d.Name(); name == "vendor" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		// Don't flag this guard's own definition of the marker.
		if path == thisFile {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, obsoleteBdSkipMarker) {
				rel, relErr := filepath.Rel(repoRoot, path)
				if relErr != nil {
					rel = path
				}
				violations = append(violations, rel+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", repoRoot, err)
	}

	if len(violations) > 0 {
		t.Fatalf("the bd 0.47.2 auto-flush bug is fixed; remove these obsolete %s skips and re-enable the tests (or skip with a live tracking bead instead):\n%s",
			obsoleteBdSkipMarker, strings.Join(violations, "\n"))
	}
}

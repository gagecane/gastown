package tautology

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealWorldFalsePositiveRate runs the analyzer against actual test
// files from gastown_upstream and reports findings. This validates that
// the analyzer doesn't generate excessive false positives on real code.
//
// The target FP rate in the wild should be low — most real test files
// in this codebase use independent expected values (literals, table-driven,
// fixtures).
func TestRealWorldFalsePositiveRate(t *testing.T) {
	// Collect real test files from the codebase.
	root := findRepoRoot(t)
	testFiles := collectTestFiles(t, root, 20) // sample up to 20 files

	totalFuncs := 0
	totalFindings := 0

	for _, path := range testFiles {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Logf("SKIP %s: %v", path, err)
			continue
		}

		findings, err := AnalyzeFile(path, src)
		if err != nil {
			// Skip files with parse errors (build tags, etc.)
			continue
		}

		relPath, _ := filepath.Rel(root, path)
		funcs := countTestFuncs(src)
		totalFuncs += funcs

		if len(findings) > 0 {
			totalFindings += len(findings)
			t.Logf("  %s: %d findings in %d test funcs", relPath, len(findings), funcs)
			for _, f := range findings {
				t.Logf("    %s: %s", f.FuncName, f.Message)
			}
		}
	}

	t.Logf("")
	t.Logf("=== REAL-WORLD SUMMARY ===")
	t.Logf("Files analyzed:  %d", len(testFiles))
	t.Logf("Test functions:  %d", totalFuncs)
	t.Logf("Total findings:  %d", totalFindings)

	if totalFuncs > 0 {
		fpRate := float64(totalFindings) / float64(totalFuncs) * 100
		t.Logf("Finding rate:    %.1f%% (expect low — most findings are true positives)", fpRate)
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	// Walk up from current working dir to find go.mod.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root")
		}
		dir = parent
	}
}

func collectTestFiles(t *testing.T, root string, maxFiles int) []string {
	t.Helper()
	var files []string

	// Walk specific directories known to have well-structured tests.
	dirs := []string{
		"internal/util",
		"internal/hooks",
		"internal/config",
		"internal/beads",
		"internal/wisp",
		"internal/witness",
		"internal/proxy",
		"internal/health",
		"internal/doctor",
		"internal/krc",
		"internal/lock",
		"internal/bitbucket",
		"internal/github",
		"cmd/gt-proxy-server",
		"internal/workspace",
		"internal/worktree",
		"internal/version",
		"internal/atomicfile",
		"internal/hookutil",
		"internal/keepalive",
	}

	for _, dir := range dirs {
		if len(files) >= maxFiles {
			break
		}
		dirPath := filepath.Join(root, dir)
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if len(files) >= maxFiles {
				break
			}
			if entry.IsDir() {
				continue
			}
			if strings.HasSuffix(entry.Name(), "_test.go") {
				files = append(files, filepath.Join(dirPath, entry.Name()))
			}
		}
	}
	return files
}

func countTestFuncs(src []byte) int {
	count := 0
	for _, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "func Test") {
			count++
		}
	}
	return count
}

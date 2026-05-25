// Copyright (c) The gastown authors.
//
// socket_guard_test.go enforces the invariant that application code outside
// this package uses the socket-aware tmux helpers (BuildCommand /
// BuildCommandContext / Tmux.run) instead of calling `exec.Command("tmux", ...)`
// or `exec.CommandContext(..., "tmux", ...)` directly.
//
// Background: the Go stdlib exec.Command is socket-unaware. When a gastown town
// configures an isolated tmux socket via tmux.SetDefaultSocket, a bare
// exec.Command("tmux", ...) silently talks to the DEFAULT tmux server — which
// is either an empty server (sessions not found) or the user's personal tmux
// (wrong sessions). This bug class is both silent and catastrophic; two
// production regressions (commits 743afe8c and 04fc8cfc) were caused by
// exactly this pattern slipping past review.
//
// This test scans every .go file in the repo and fails if it finds a bare
// tmux exec outside `internal/tmux/`. To opt a callsite out (e.g. tests that
// manage their own isolated -L socket, or helpers that sweep multiple
// sockets on purpose), add `// intentionally bare` on the same line as the
// call, or on one of the three lines immediately preceding it.

package tmux

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// bareTmuxExec matches the two forms we care about:
//
//	exec.Command("tmux", ...)
//	exec.CommandContext(anything, "tmux", ...)
//
// It does NOT match calls where the literal "tmux" appears later in an
// argument list (e.g. `exec.Command("sh", "-c", "tmux ...")`) because those
// are shell invocations that the helper can't meaningfully wrap.
var bareTmuxExec = regexp.MustCompile(
	`exec\.Command\(\s*"tmux"|exec\.CommandContext\([^,]+,\s*"tmux"`,
)

// intentionalBareMarker is the opt-out comment that authors must add on the
// offending line or in the 3 preceding lines to justify a bare call.
const intentionalBareMarker = "intentionally bare"

// repoRootFromHere finds the gastown module root by walking up from the test's
// working directory looking for go.mod.
func repoRootFromHere(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod found walking up from %s", dir)
		}
		dir = parent
	}
}

// skipDirs is the set of directory basenames that can never contain
// relevant .go files. Skipping these avoids descending into large
// irrelevant subtrees (VCS metadata, vendored deps, build artifacts,
// generated code, documentation, and the tmux helper package itself).
var skipDirs = map[string]bool{
	".git":         true,
	".beads":       true,
	".dolt":        true,
	"vendor":       true,
	"node_modules": true,
	"testdata":     true,
	"dist":         true,
	"gen":          true,
	"generated":    true,
	"third_party":  true,
}

// prelimContains are the byte sequences that MUST appear in a file for
// the regex to have any chance of matching. Used as a fast-path filter:
// if none of these substrings appear, we skip the expensive regex scan.
var prelimContains = [][]byte{
	[]byte(`exec.Command`),
	[]byte(`exec.CommandContext`),
}

// TestNoBareTmuxExec walks the repo and fails if any .go file outside
// internal/tmux/ calls exec.Command("tmux", ...) / exec.CommandContext(...,
// "tmux", ...) without an `intentionally bare` annotation nearby.
//
// Placement: this test lives in internal/tmux/ because that's the only
// package allowed to issue bare tmux exec calls (it defines the helpers
// that wrap them). Keeping the guard co-located with the helpers means
// the rule travels with the rule-maker.
//
// Performance: uses preliminary bytes.Contains check to skip files that
// cannot possibly match, and processes matching files in parallel via a
// bounded goroutine pool.
func TestNoBareTmuxExec(t *testing.T) {
	repoRoot := repoRootFromHere(t)
	tmuxDir := filepath.Join("internal", "tmux")

	// Phase 1: Walk the tree and collect candidate .go file paths.
	// The walk itself is fast (directory enumeration only); heavy I/O
	// and regex scanning happen in phase 2.
	var candidates []string

	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			name := info.Name()
			if skipDirs[name] {
				return filepath.SkipDir
			}
			// Skip internal/tmux/ — this is the helper package.
			rel, err := filepath.Rel(repoRoot, path)
			if err == nil && rel == tmuxDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip the guard test itself — it contains the pattern in a regex
		// literal that would otherwise self-match.
		if strings.HasSuffix(path, "socket_guard_test.go") {
			return nil
		}
		candidates = append(candidates, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}

	// Phase 2: Process candidate files in parallel. Each worker reads a
	// file, applies a cheap bytes.Contains prelim filter, and only falls
	// through to regex scanning if the file could possibly match.
	type violation struct {
		msg string
	}

	numWorkers := runtime.GOMAXPROCS(0)
	if numWorkers < 1 {
		numWorkers = 4
	}
	if numWorkers > 8 {
		numWorkers = 8
	}

	work := make(chan string, len(candidates))
	for _, c := range candidates {
		work <- c
	}
	close(work)

	var mu sync.Mutex
	var violations []string
	var wg sync.WaitGroup

	wg.Add(numWorkers)
	for range numWorkers {
		go func() {
			defer wg.Done()
			for path := range work {
				data, err := os.ReadFile(path)
				if err != nil {
					// Non-fatal: file might have been deleted between
					// walk and read (rare but possible in concurrent
					// development).
					continue
				}

				// Fast-path: if the file doesn't contain any of the
				// preliminary byte sequences, it cannot match the regex.
				hasPrelim := false
				for _, needle := range prelimContains {
					if bytes.Contains(data, needle) {
						hasPrelim = true
						break
					}
				}
				if !hasPrelim {
					continue
				}

				// Slow path: line-by-line regex scan.
				lines := strings.Split(string(data), "\n")
				for i, line := range lines {
					if !bareTmuxExec.MatchString(line) {
						continue
					}
					if isCommentLine(line) {
						continue
					}
					if hasIntentionalBareMarker(lines, i) {
						continue
					}
					rel, _ := filepath.Rel(repoRoot, path)
					msg := rel + ":" + itoa(i+1) + ": bare tmux exec without `intentionally bare` annotation: " + strings.TrimSpace(line)
					mu.Lock()
					violations = append(violations, msg)
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if len(violations) > 0 {
		t.Fatalf(
			"found %d bare tmux exec callsite(s) outside internal/tmux/.\n"+
				"Each of these should either:\n"+
				"  (1) be migrated to tmux.BuildCommand / BuildCommandContext\n"+
				"      (preferred — honors tmux.SetDefaultSocket), or\n"+
				"  (2) be annotated with a comment containing %q on the same\n"+
				"      line or in one of the 3 preceding lines, with a brief\n"+
				"      reason (e.g. \"intentionally bare — per-test socket\").\n\n"+
				"Violations:\n  %s",
			len(violations), intentionalBareMarker, strings.Join(violations, "\n  "),
		)
	}
}

// hasIntentionalBareMarker returns true if lines[idx] or any of the 6 lines
// immediately preceding it contain the opt-out marker. Six lines comfortably
// covers a short paragraph of justification plus the preceding blank line.
func hasIntentionalBareMarker(lines []string, idx int) bool {
	start := idx - 6
	if start < 0 {
		start = 0
	}
	for i := start; i <= idx; i++ {
		if strings.Contains(lines[i], intentionalBareMarker) {
			return true
		}
	}
	return false
}

// isCommentLine returns true if line's non-whitespace content starts with "//".
// Used to filter out docstring comments and regex literals that mention the
// bare-exec pattern without actually being a call site.
func isCommentLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "//")
}

// itoa avoids pulling in strconv for a single callsite.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestBareTmuxExecRegex verifies the guard regex matches the two bug-class
// patterns and the classic real-world regressions while ignoring close
// neighbors.
func TestBareTmuxExecRegex(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"plain exec.Command", `cmd := exec.Command("tmux", "display-message")`, true},
		{"exec.Command with arg list", `_ = exec.Command("tmux", args...).Run()`, true},
		{"exec.CommandContext", `cmd := exec.CommandContext(ctx, "tmux", "list-sessions")`, true},
		{"exec.CommandContext with arg var", `cmd := exec.CommandContext(ctx, "tmux", argsVar...)`, true},
		{"bug 743afe8c shape", `exec.Command("tmux", "list-sessions", "-F", "#{session_name}")`, true},
		{"bug 04fc8cfc shape", `exec.Command("tmux", "display-message", "-p", "#{session_name}")`, true},
		{"helper call — must NOT match", `cmd := tmux.BuildCommand("display-message")`, false},
		{"context helper — must NOT match", `cmd := tmux.BuildCommandContext(ctx, "kill-session")`, false},
		{"shell invocation embedding tmux — NOT matched (out of scope)", `exec.Command("sh", "-c", "tmux list-sessions")`, false},
		{"different binary", `exec.Command("bd", "prime")`, false},
		{"Tmux.run — internal, not matched here", `cmd := exec.Command("tmux", allArgs...)`, true}, // still matches; internal/tmux is excluded by path filter
	}

	for _, tc := range cases {
		got := bareTmuxExec.MatchString(tc.input)
		if got != tc.want {
			t.Errorf("%s: got %v, want %v for input %q", tc.name, got, tc.want, tc.input)
		}
	}
}

// TestHasIntentionalBareMarker verifies the proximity window for opt-out
// annotations.
func TestHasIntentionalBareMarker(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		idx   int
		want  bool
	}{
		{
			name: "same line annotation",
			lines: []string{
				`_ = exec.Command("tmux", "kill-server").Run() // intentionally bare — test`,
			},
			idx:  0,
			want: true,
		},
		{
			name: "one line above",
			lines: []string{
				`// intentionally bare — test socket`,
				`_ = exec.Command("tmux", "-L", s, "kill-server").Run()`,
			},
			idx:  1,
			want: true,
		},
		{
			name: "six lines above — within window",
			lines: []string{
				`// intentionally bare — multi-line justification follows`,
				`// line 2 of comment`,
				`// line 3 of comment`,
				`// line 4 of comment`,
				`// line 5 of comment`,
				`// line 6 of comment`,
				`_ = exec.Command("tmux", "-L", s, "kill-server").Run()`,
			},
			idx:  6,
			want: true,
		},
		{
			name: "seven lines above — outside window",
			lines: []string{
				`// intentionally bare — too far`,
				`// filler 1`,
				`// filler 2`,
				`// filler 3`,
				`// filler 4`,
				`// filler 5`,
				`// filler 6`,
				`_ = exec.Command("tmux", "-L", s, "kill-server").Run()`,
			},
			idx:  7,
			want: false,
		},
		{
			name: "no annotation anywhere",
			lines: []string{
				`// ordinary comment`,
				`_ = exec.Command("tmux", "list-sessions").Run()`,
			},
			idx:  1,
			want: false,
		},
	}

	for _, tc := range cases {
		got := hasIntentionalBareMarker(tc.lines, tc.idx)
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestIsCommentLine verifies the pure-comment filter.
func TestIsCommentLine(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"// a comment", true},
		{"    // indented comment", true},
		{"\t// tab-indented comment", true},
		{`code // trailing comment`, false},
		{"", false},
		{"cmd := exec.Command()", false},
	}
	for _, tc := range cases {
		got := isCommentLine(tc.line)
		if got != tc.want {
			t.Errorf("isCommentLine(%q): got %v, want %v", tc.line, got, tc.want)
		}
	}
}

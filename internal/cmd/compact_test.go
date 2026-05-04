package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestGetTTL(t *testing.T) {
	ttls := defaultTTLs

	tests := []struct {
		wispType string
		want     time.Duration
	}{
		{"heartbeat", 6 * time.Hour},
		{"ping", 6 * time.Hour},
		{"patrol", 24 * time.Hour},
		{"gc_report", 24 * time.Hour},
		{"error", 7 * 24 * time.Hour},
		{"recovery", 7 * 24 * time.Hour},
		{"escalation", 7 * 24 * time.Hour},
		{"default", 24 * time.Hour},
		{"", 24 * time.Hour},        // empty falls back to default
		{"unknown", 24 * time.Hour}, // unknown falls back to default
	}

	for _, tc := range tests {
		t.Run(tc.wispType, func(t *testing.T) {
			got := getTTL(ttls, tc.wispType)
			if got != tc.want {
				t.Errorf("getTTL(%q) = %v, want %v", tc.wispType, got, tc.want)
			}
		})
	}
}

func TestWispAge(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		updatedAt string
		wantAge   time.Duration
		wantErr   bool
	}{
		{
			name:      "RFC3339",
			updatedAt: "2026-02-07T06:00:00Z",
			wantAge:   6 * time.Hour,
		},
		{
			name:      "one day old",
			updatedAt: "2026-02-06T12:00:00Z",
			wantAge:   24 * time.Hour,
		},
		{
			name:      "invalid",
			updatedAt: "not-a-date",
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &compactIssue{
				Issue: beads.Issue{UpdatedAt: tc.updatedAt},
			}
			got, err := wispAge(w, now)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantAge {
				t.Errorf("wispAge = %v, want %v", got, tc.wantAge)
			}
		})
	}
}

func TestHasKeepLabel(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"no labels", nil, false},
		{"other labels", []string{"bug", "urgent"}, false},
		{"keep label", []string{"keep"}, true},
		{"gt:keep label", []string{"bug", "gt:keep"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &compactIssue{
				Issue: beads.Issue{Labels: tc.labels},
			}
			if got := hasKeepLabel(w); got != tc.want {
				t.Errorf("hasKeepLabel = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsCompletedPluginRunReceipt(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"no labels", nil, false},
		{"plugin-run + success", []string{"type:plugin-run", "result:success"}, true},
		{"plugin-run + no-op", []string{"type:plugin-run", "result:no-op"}, true},
		{"plugin-run + noop", []string{"type:plugin-run", "result:noop"}, true},
		{"plugin-run + failure (must promote)", []string{"type:plugin-run", "result:failure"}, false},
		{"plugin-run + warning (must promote)", []string{"type:plugin-run", "result:warning"}, false},
		{"plugin-run alone (no result)", []string{"type:plugin-run"}, false},
		{"success alone (no plugin-run)", []string{"result:success"}, false},
		{"unrelated labels", []string{"bug", "urgent"}, false},
		{"plugin-run + plugin name + success", []string{"type:plugin-run", "plugin:auto-dispatch", "result:success"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &compactIssue{
				Issue: beads.Issue{Labels: tc.labels},
			}
			if got := isCompletedPluginRunReceipt(w); got != tc.want {
				t.Errorf("isCompletedPluginRunReceipt(%v) = %v, want %v", tc.labels, got, tc.want)
			}
		})
	}
}

func TestHasComments(t *testing.T) {
	tests := []struct {
		name  string
		count int
		want  bool
	}{
		{"no comments", 0, false},
		{"has comments", 3, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &compactIssue{CommentCount: tc.count}
			if got := hasComments(w); got != tc.want {
				t.Errorf("hasComments = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsReferenced(t *testing.T) {
	tests := []struct {
		name    string
		depCnt  int
		deptCnt int
		want    bool
	}{
		{"no refs", 0, 0, false},
		{"has dependents", 0, 1, true},
		{"has dependencies", 1, 0, true},
		{"both", 2, 3, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &compactIssue{
				Issue: beads.Issue{
					DependencyCount: tc.depCnt,
					DependentCount:  tc.deptCnt,
				},
			}
			if got := isReferenced(w); got != tc.want {
				t.Errorf("isReferenced = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCompactTruncate(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		{"short ASCII", "short", 10, "short"},
		{"exact length", "exactly10!", 10, "exactly10!"},
		{"ASCII too long", "this is too long", 10, "this is..."},
		{"short maxLen", "ab", 3, "ab"},
		{"maxLen 3", "abcdef", 3, "abc"},
		// Multi-byte UTF-8: emoji is 1 rune, not 4 bytes
		{"emoji within limit", "🤝 HANDOFF", 10, "🤝 HANDOFF"},
		{"emoji truncated", "🤝 HANDOFF: Routine cycle for witness", 15, "🤝 HANDOFF: R..."},
		// CJK characters: each is 1 rune, 3 bytes
		{"CJK within limit", "日本語テスト", 10, "日本語テスト"},
		{"CJK truncated", "日本語テストデータ", 6, "日本語..."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactTruncate(tc.s, tc.maxLen); got != tc.want {
				t.Errorf("compactTruncate(%q, %d) = %q, want %q", tc.s, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestExtractJSONArray(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			"clean JSON array",
			`[{"id":"test"}]`,
			`[{"id":"test"}]`,
		},
		{
			"warning prefix before JSON",
			"Warning: no route found for prefix \"gt-\"\n[{\"id\":\"test\"}]",
			`[{"id":"test"}]`,
		},
		{
			"unicode warning prefix",
			"⚠ Warning: something with 🤝 emoji\n[{\"id\":\"test\"}]",
			`[{"id":"test"}]`,
		},
		{
			"no array in data",
			"just some text without json",
			"just some text without json",
		},
		{
			"empty data",
			"",
			"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(extractJSONArray([]byte(tc.data)))
			if got != tc.want {
				t.Errorf("extractJSONArray(%q) = %q, want %q", tc.data, got, tc.want)
			}
		})
	}
}

func TestLoadTTLConfigDefaults(t *testing.T) {
	// With empty town root, should return defaults
	ttls := loadTTLConfig("", "")

	if ttls["heartbeat"] != 6*time.Hour {
		t.Errorf("heartbeat TTL = %v, want 6h", ttls["heartbeat"])
	}
	if ttls["patrol"] != 24*time.Hour {
		t.Errorf("patrol TTL = %v, want 24h", ttls["patrol"])
	}
	if ttls["error"] != 7*24*time.Hour {
		t.Errorf("error TTL = %v, want 168h", ttls["error"])
	}
}

func TestLoadTTLConfigWithRoleDefaults(t *testing.T) {
	// With empty town root, should return hardcoded defaults
	ttls := loadTTLConfigWithRole("", "")

	for k, want := range defaultTTLs {
		if got := ttls[k]; got != want {
			t.Errorf("loadTTLConfigWithRole TTLs[%q] = %v, want %v", k, got, want)
		}
	}
}

func TestLoadTTLConfigWithRoleSkipsInvalidPaths(t *testing.T) {
	// With nonexistent paths, rig bead lookup should gracefully skip
	ttls := loadTTLConfigWithRole("/nonexistent/town", "myrig")

	// Should still have defaults even though lookups failed
	if ttls["patrol"] != defaultTTLs["patrol"] {
		t.Errorf("patrol TTL = %v, want %v", ttls["patrol"], defaultTTLs["patrol"])
	}
	if ttls["error"] != defaultTTLs["error"] {
		t.Errorf("error TTL = %v, want %v", ttls["error"], defaultTTLs["error"])
	}
}

// TestApplyRigBeadTTLOverrides_ResolvesPrefixFromRigsJson verifies that
// applyRigBeadTTLOverrides looks up the rig identity bead using the
// canonical prefix from mayor/rigs.json, not a hardcoded "gt".
//
// Regression for gu-r83v / ta-0pk #5: the original code used
// beads.RigBeadIDWithPrefix("gt", rigName) unconditionally, which produced
// ids like "gt-rig-talontriage" that do not exist in the beads database
// (the real bead is "ta-rig-talontriage"). For non-gt-prefixed rigs this
// silently caused every `gt compact` run to skip the rig's TTL overrides.
//
// The test installs a stub bd that records every `bd show ID` it receives,
// then asserts the stub was queried with the prefix-correct ID.
func TestApplyRigBeadTTLOverrides_ResolvesPrefixFromRigsJson(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping: bd stub uses /bin/sh")
	}

	tmpDir := t.TempDir()
	rigName := "talontriage"
	prefix := "ta"

	// Lay out mayor/rigs.json registering the rig with its canonical
	// beads prefix. This is the source of truth that rig.RigBeadsPrefix
	// consults.
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	rigsJSON := `{"version":1,"rigs":{"talontriage":{"beads":{"prefix":"ta"}}}}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}

	// Town-level beads dir so beads.NewWithBeadsDir has a place to resolve.
	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Stub bd: log every show invocation to a file, respond with
	// "no issue found" so applyRigBeadTTLOverrides hits the error path
	// and returns without modifying ttls. We only care which id was
	// queried, not the response.
	binDir := t.TempDir()
	showLog := filepath.Join(binDir, "bd-show.log")
	script := `#!/bin/sh
# Find the subcommand (skip leading flags).
cmd=""
id=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *)
      if [ -z "$cmd" ]; then
        cmd="$arg"
      elif [ -z "$id" ]; then
        id="$arg"
      fi
      ;;
  esac
done

if [ "$cmd" = "show" ]; then
  echo "$id" >> "` + showLog + `"
  echo "Error: no issue found matching \"$id\"" >&2
  exit 1
fi
# Respond to any other subcommand with success + empty output so
# bookkeeping calls (if any) don't pollute the log.
exit 0
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write stub bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Exercise the code path under test.
	ttls := make(map[string]time.Duration)
	for k, v := range defaultTTLs {
		ttls[k] = v
	}
	applyRigBeadTTLOverrides(ttls, tmpDir, rigName)

	// Verify the stub was asked for the prefix-correct bead id.
	logBytes, err := os.ReadFile(showLog)
	if err != nil {
		t.Fatalf("read show log: %v", err)
	}
	queried := strings.TrimSpace(string(logBytes))
	wantID := beads.RigBeadIDWithPrefix(prefix, rigName) // "ta-rig-talontriage"
	if !strings.Contains(queried, wantID) {
		t.Errorf("applyRigBeadTTLOverrides queried %q, want lookup of %q\n"+
			"(before fix it would have queried \"gt-rig-talontriage\")",
			queried, wantID)
	}
	// Doubly: must NOT have issued the buggy "gt-rig-talontriage" lookup.
	badID := beads.RigBeadIDWithPrefix("gt", rigName)
	if strings.Contains(queried, badID) {
		t.Errorf("applyRigBeadTTLOverrides still issues buggy lookup %q (regressed gu-r83v)", badID)
	}
}

// TestApplyRigBeadTTLOverrides_SkipsWhenPrefixUnresolvable verifies that
// when neither mayor/rigs.json nor the rig's config.json declares a beads
// prefix, applyRigBeadTTLOverrides returns without contacting bd. Prevents
// a regression where the old hardcoded "gt" prefix effectively masked
// missing configuration.
func TestApplyRigBeadTTLOverrides_SkipsWhenPrefixUnresolvable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping: bd stub uses /bin/sh")
	}

	tmpDir := t.TempDir()

	// Stub bd that fails loudly if invoked. If applyRigBeadTTLOverrides
	// tried to look up a bead with a bogus prefix, the stub would record
	// it; here we expect zero invocations.
	binDir := t.TempDir()
	invokedPath := filepath.Join(binDir, "bd-invoked")
	script := `#!/bin/sh
echo "$@" >> "` + invokedPath + `"
exit 1
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write stub bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ttls := make(map[string]time.Duration)
	for k, v := range defaultTTLs {
		ttls[k] = v
	}
	applyRigBeadTTLOverrides(ttls, tmpDir, "unknownrig")

	if _, err := os.Stat(invokedPath); err == nil {
		data, _ := os.ReadFile(invokedPath)
		t.Errorf("applyRigBeadTTLOverrides invoked bd with unresolvable prefix: %q", string(data))
	}

	// Defaults must remain untouched.
	for k, want := range defaultTTLs {
		if got := ttls[k]; got != want {
			t.Errorf("ttls[%q] = %v, want %v (defaults should be intact)", k, got, want)
		}
	}
}

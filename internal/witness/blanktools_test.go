package witness

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestIsBlankToolsSignature(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"empty", "", false},
		{"session blind", "session blind, recommend restart", true},
		{"all tool output empty", "all tool output empty for 20+ calls", true},
		{"channel broken", "tool-result channel broken", true},
		{"blank-tools hyphen", "hit the blank-tools failure", true},
		{"case insensitive", "TOOL OUTPUT EMPTY", true},
		{"embedded in sentence", "I think the tool results are empty and I am blind", true},
		{"normal stuck reason", "blocked on auth issue, need a credential", false},
		{"merge conflict", "stuck on a merge conflict in foo.go", false},
		{"unrelated", "waiting on dolt to come back up", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsBlankToolsSignature(tc.text); got != tc.want {
				t.Errorf("IsBlankToolsSignature(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestRecordBlankToolsRestart_Increments(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	if count := RecordBlankToolsRestart(tmpDir, "bead-1"); count != 1 {
		t.Errorf("first RecordBlankToolsRestart = %d, want 1", count)
	}
	if count := RecordBlankToolsRestart(tmpDir, "bead-1"); count != 2 {
		t.Errorf("second RecordBlankToolsRestart = %d, want 2", count)
	}
	// A different bead is tracked independently.
	if count := RecordBlankToolsRestart(tmpDir, "bead-2"); count != 1 {
		t.Errorf("RecordBlankToolsRestart(bead-2) = %d, want 1", count)
	}
}

func TestShouldEscalateBlankTools_Threshold(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	// Unknown bead: never escalate.
	if ShouldEscalateBlankTools(tmpDir, "unknown") {
		t.Error("ShouldEscalateBlankTools = true for unknown bead")
	}

	// Below the cap: do not escalate.
	for i := 0; i < MaxBlankToolsAutoRestarts-1; i++ {
		RecordBlankToolsRestart(tmpDir, "bead-3")
	}
	if ShouldEscalateBlankTools(tmpDir, "bead-3") {
		t.Errorf("ShouldEscalateBlankTools = true below cap (%d restarts)", MaxBlankToolsAutoRestarts-1)
	}

	// At the cap: escalate.
	RecordBlankToolsRestart(tmpDir, "bead-3")
	if !ShouldEscalateBlankTools(tmpDir, "bead-3") {
		t.Errorf("ShouldEscalateBlankTools = false at cap (%d restarts)", MaxBlankToolsAutoRestarts)
	}
}

func TestRecordBlankToolsRestart_ConcurrentSafe(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	const n = 20
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			RecordBlankToolsRestart(tmpDir, "bead-race")
		}()
	}
	wg.Wait()

	// Final count must equal n: no lost updates under concurrency.
	if count := RecordBlankToolsRestart(tmpDir, "bead-race"); count != n+1 {
		t.Errorf("after %d concurrent increments, next count = %d, want %d", n, count, n+1)
	}
}

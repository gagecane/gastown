package pushlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPath(t *testing.T) {
	tests := []struct {
		name, townRoot, rigName, want string
	}{
		{"normal", "/town", "myrig", "/town/myrig/.runtime/push-receipts.jsonl"},
		{"empty townRoot", "", "myrig", ""},
		{"empty rigName", "/town", "", ""},
		{"both empty", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Path(tt.townRoot, tt.rigName)
			if got != tt.want {
				t.Errorf("Path(%q, %q) = %q, want %q", tt.townRoot, tt.rigName, got, tt.want)
			}
		})
	}
}

func TestAppendAndRead(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "myrig"

	// Read on missing log → nil, nil (not an error).
	got, err := Read(townRoot, rigName)
	if err != nil {
		t.Fatalf("Read on missing log returned err=%v", err)
	}
	if got != nil {
		t.Fatalf("Read on missing log returned %v, want nil", got)
	}

	r1 := Receipt{
		Branch:    "polecat/guzzle/gu-aaa",
		CommitSHA: "abc123",
		Remote:    "origin",
		PushURL:   "git@github.com:org/repo.git",
		Source:    SourceDone,
		Worker:    "myrig/polecats/guzzle",
		IssueID:   "gu-aaa",
	}
	if err := Append(townRoot, rigName, r1); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Verify file landed in expected location.
	if _, err := os.Stat(Path(townRoot, rigName)); err != nil {
		t.Fatalf("expected log file at %s: %v", Path(townRoot, rigName), err)
	}

	r2 := Receipt{
		Branch:    "polecat/guzzle/gu-bbb",
		CommitSHA: "def456",
		Source:    SourceWitnessRecovery,
	}
	if err := Append(townRoot, rigName, r2); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err = Read(townRoot, rigName)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Read returned %d receipts, want 2", len(got))
	}
	if got[0].Branch != r1.Branch {
		t.Errorf("got[0].Branch = %q, want %q", got[0].Branch, r1.Branch)
	}
	if got[1].Branch != r2.Branch {
		t.Errorf("got[1].Branch = %q, want %q", got[1].Branch, r2.Branch)
	}
	if got[0].Timestamp == "" {
		t.Errorf("expected Timestamp to be auto-populated")
	}
	if got[1].Remote != "origin" {
		t.Errorf("expected default Remote=origin, got %q", got[1].Remote)
	}
	if got[0].PushURL != "git@github.com:org/repo.git" {
		t.Errorf("got[0].PushURL = %q, want preserved push URL", got[0].PushURL)
	}
}

func TestAppendValidation(t *testing.T) {
	townRoot := t.TempDir()
	tests := []struct {
		name      string
		townRoot  string
		rigName   string
		receipt   Receipt
		wantErrIn string
	}{
		{"missing town", "", "rig", Receipt{Branch: "b", CommitSHA: "s"}, "empty"},
		{"missing rig", townRoot, "", Receipt{Branch: "b", CommitSHA: "s"}, "empty"},
		{"missing branch", townRoot, "rig", Receipt{CommitSHA: "s"}, "branch"},
		{"missing sha", townRoot, "rig", Receipt{Branch: "b"}, "sha"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Append(tt.townRoot, tt.rigName, tt.receipt)
			if err == nil {
				t.Fatalf("Append should have errored")
			}
			if !strings.Contains(err.Error(), tt.wantErrIn) {
				t.Errorf("err=%q, want substring %q", err.Error(), tt.wantErrIn)
			}
		})
	}
}

func TestReadSkipsMalformedLines(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "myrig"

	// Create the .runtime dir + log with a mix of valid and garbage lines.
	dir := filepath.Join(townRoot, rigName, ".runtime")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, Filename)
	contents := strings.Join([]string{
		`{"at":"2026-05-27T22:00:00Z","branch":"good1","sha":"aaa","remote":"origin","source":"done"}`,
		`not json at all`,
		``,
		`{"at":"2026-05-27T22:01:00Z","branch":"good2","sha":"bbb","remote":"origin","source":"done"}`,
		`{"truncated":`,
	}, "\n") + "\n"
	if err := os.WriteFile(logPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Read(townRoot, rigName)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d receipts, want 2 (malformed lines should be skipped)", len(got))
	}
	if got[0].Branch != "good1" || got[1].Branch != "good2" {
		t.Errorf("got = %+v, want good1 then good2", got)
	}
}

func TestFindByBranch(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "myrig"

	// Empty log: missing branch → nil, nil.
	got, err := FindByBranch(townRoot, rigName, "nope")
	if err != nil || got != nil {
		t.Errorf("FindByBranch on missing log = (%v, %v), want (nil, nil)", got, err)
	}

	// Three receipts with two different branches and an explicit ordering.
	earlier := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	later := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	for _, r := range []Receipt{
		{Timestamp: earlier, Branch: "feature-a", CommitSHA: "old-sha", Source: SourceDone},
		{Timestamp: later, Branch: "feature-a", CommitSHA: "new-sha", Source: SourceDone},
		{Timestamp: later, Branch: "feature-b", CommitSHA: "b-sha", Source: SourceDone},
	} {
		if err := Append(townRoot, rigName, r); err != nil {
			t.Fatal(err)
		}
	}

	got, err = FindByBranch(townRoot, rigName, "feature-a")
	if err != nil {
		t.Fatalf("FindByBranch: %v", err)
	}
	if got == nil {
		t.Fatal("got nil receipt for feature-a")
	}
	if got.CommitSHA != "new-sha" {
		t.Errorf("got CommitSHA = %q, want most-recent new-sha", got.CommitSHA)
	}

	got, err = FindByBranch(townRoot, rigName, "feature-b")
	if err != nil {
		t.Fatalf("FindByBranch: %v", err)
	}
	if got == nil || got.CommitSHA != "b-sha" {
		t.Errorf("got %+v, want feature-b with b-sha", got)
	}

	got, err = FindByBranch(townRoot, rigName, "nope")
	if err != nil {
		t.Fatalf("FindByBranch missing: %v", err)
	}
	if got != nil {
		t.Errorf("got %+v, want nil for missing branch", got)
	}

	// Empty branch → nil, nil (early return).
	got, err = FindByBranch(townRoot, rigName, "")
	if err != nil || got != nil {
		t.Errorf("FindByBranch with empty branch = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestLogOrWarn_NoPanic(t *testing.T) {
	// Just confirm LogOrWarn doesn't panic on bad input.
	LogOrWarn("", "", Receipt{}) // bad: empty town/rig
	LogOrWarn(t.TempDir(), "rig", Receipt{Branch: "b", CommitSHA: "s", Source: SourceDone})
}

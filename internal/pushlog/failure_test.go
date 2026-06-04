package pushlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFailurePath(t *testing.T) {
	tests := []struct {
		name, townRoot, rigName, want string
	}{
		{"normal", "/town", "myrig", "/town/myrig/.runtime/push-failures.jsonl"},
		{"empty townRoot", "", "myrig", ""},
		{"empty rigName", "/town", "", ""},
		{"both empty", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FailurePath(tt.townRoot, tt.rigName)
			if got != tt.want {
				t.Errorf("FailurePath(%q, %q) = %q, want %q", tt.townRoot, tt.rigName, got, tt.want)
			}
		})
	}
}

func TestAppendAndReadFailures(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "myrig"

	// Read on missing log → nil, nil (not an error).
	got, err := ReadFailures(townRoot, rigName)
	if err != nil {
		t.Fatalf("ReadFailures on missing log returned err=%v", err)
	}
	if got != nil {
		t.Fatalf("ReadFailures on missing log returned %v, want nil", got)
	}

	f1 := Failure{
		Branch:    "polecat/guzzle/gu-u8yy5",
		CommitSHA: "d72a5776",
		Remote:    "origin",
		Source:    SourceDone,
		Stage:     StagePush,
		Error:     "fatal: unable to access 'https://...': Recv failure: Connection reset by peer",
		Worker:    "myrig/polecats/guzzle",
		IssueID:   "gu-u8yy5",
	}
	if err := AppendFailure(townRoot, rigName, f1); err != nil {
		t.Fatalf("AppendFailure: %v", err)
	}

	// Verify file landed in expected location.
	if _, err := os.Stat(FailurePath(townRoot, rigName)); err != nil {
		t.Fatalf("expected failure log file at %s: %v", FailurePath(townRoot, rigName), err)
	}

	f2 := Failure{
		Branch: "polecat/guzzle/gu-bbb",
		Source: SourceDoneRelay,
		Stage:  StageVerify,
		Error:  "commit not found on origin/main",
	}
	if err := AppendFailure(townRoot, rigName, f2); err != nil {
		t.Fatalf("AppendFailure: %v", err)
	}

	got, err = ReadFailures(townRoot, rigName)
	if err != nil {
		t.Fatalf("ReadFailures: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ReadFailures returned %d records, want 2", len(got))
	}
	if got[0].Branch != f1.Branch || got[1].Branch != f2.Branch {
		t.Errorf("got branches %q, %q; want %q, %q", got[0].Branch, got[1].Branch, f1.Branch, f2.Branch)
	}
	if got[0].Stage != StagePush || got[1].Stage != StageVerify {
		t.Errorf("got stages %q, %q; want %q, %q", got[0].Stage, got[1].Stage, StagePush, StageVerify)
	}
	if got[0].Error != f1.Error {
		t.Errorf("got[0].Error = %q, want preserved error %q", got[0].Error, f1.Error)
	}
	if got[0].Timestamp == "" {
		t.Errorf("expected Timestamp to be auto-populated")
	}
	if got[1].Remote != "origin" {
		t.Errorf("expected default Remote=origin, got %q", got[1].Remote)
	}
}

func TestAppendFailureValidation(t *testing.T) {
	townRoot := t.TempDir()
	tests := []struct {
		name      string
		townRoot  string
		rigName   string
		failure   Failure
		wantErrIn string
	}{
		{"missing town", "", "rig", Failure{Branch: "b"}, "empty"},
		{"missing rig", townRoot, "", Failure{Branch: "b"}, "empty"},
		{"missing branch", townRoot, "rig", Failure{Stage: StagePush}, "branch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AppendFailure(tt.townRoot, tt.rigName, tt.failure)
			if err == nil {
				t.Fatalf("AppendFailure should have errored")
			}
			if !strings.Contains(err.Error(), tt.wantErrIn) {
				t.Errorf("err=%q, want substring %q", err.Error(), tt.wantErrIn)
			}
		})
	}
}

func TestAppendFailureTruncatesLongError(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "myrig"

	longErr := strings.Repeat("x", maxFailureErrorLen+500)
	if err := AppendFailure(townRoot, rigName, Failure{
		Branch: "b",
		Stage:  StagePush,
		Error:  longErr,
	}); err != nil {
		t.Fatalf("AppendFailure: %v", err)
	}
	got, err := ReadFailures(townRoot, rigName)
	if err != nil {
		t.Fatalf("ReadFailures: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if !strings.HasSuffix(got[0].Error, "...(truncated)") {
		t.Errorf("expected truncated error suffix, got len=%d", len(got[0].Error))
	}
	if len(got[0].Error) > maxFailureErrorLen+len("...(truncated)") {
		t.Errorf("error not bounded: len=%d", len(got[0].Error))
	}
}

func TestReadFailuresSkipsMalformedLines(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "myrig"

	dir := filepath.Join(townRoot, rigName, ".runtime")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, FailureFilename)
	contents := strings.Join([]string{
		`{"at":"2026-06-04T18:00:00Z","branch":"good1","stage":"push","error":"reset","remote":"origin","source":"done"}`,
		`not json at all`,
		``,
		`{"at":"2026-06-04T18:01:00Z","branch":"good2","stage":"verify","error":"absent","remote":"origin","source":"done"}`,
		`{"truncated":`,
	}, "\n") + "\n"
	if err := os.WriteFile(logPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadFailures(townRoot, rigName)
	if err != nil {
		t.Fatalf("ReadFailures: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2 (malformed lines should be skipped)", len(got))
	}
	if got[0].Branch != "good1" || got[1].Branch != "good2" {
		t.Errorf("got = %+v, want good1 then good2", got)
	}
}

func TestLogFailureOrWarn_NoPanic(t *testing.T) {
	LogFailureOrWarn("", "", Failure{})                                                         // bad: empty town/rig
	LogFailureOrWarn(t.TempDir(), "rig", Failure{Branch: "b", Stage: StagePush, Error: "boom"}) // ok
}

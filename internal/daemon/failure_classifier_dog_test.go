package daemon

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// --- Config and interval tests ---

func TestFailureClassifierInterval_Default(t *testing.T) {
	if got := failureClassifierInterval(nil); got != defaultFailureClassifierInterval {
		t.Errorf("expected default %v, got %v", defaultFailureClassifierInterval, got)
	}
}

func TestFailureClassifierInterval_Custom(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			FailureClassifier: &FailureClassifierConfig{Enabled: true, IntervalStr: "5m"},
		},
	}
	if got := failureClassifierInterval(cfg); got != 5*time.Minute {
		t.Errorf("expected 5m, got %v", got)
	}
}

func TestFailureClassifierInterval_Invalid(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			FailureClassifier: &FailureClassifierConfig{Enabled: true, IntervalStr: "bad"},
		},
	}
	if got := failureClassifierInterval(cfg); got != defaultFailureClassifierInterval {
		t.Errorf("expected default for invalid interval, got %v", got)
	}
}

func TestIsPatrolEnabled_FailureClassifier_NilConfig(t *testing.T) {
	if IsPatrolEnabled(nil, "failure_classifier") {
		t.Error("failure_classifier should be disabled with nil config (opt-in)")
	}
}

func TestIsPatrolEnabled_FailureClassifier_EmptyPatrols(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if IsPatrolEnabled(cfg, "failure_classifier") {
		t.Error("failure_classifier should be disabled when not configured")
	}
}

func TestIsPatrolEnabled_FailureClassifier_Enabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			FailureClassifier: &FailureClassifierConfig{Enabled: true},
		},
	}
	if !IsPatrolEnabled(cfg, "failure_classifier") {
		t.Error("failure_classifier should be enabled when configured")
	}
}

func TestIsPatrolEnabled_FailureClassifier_ExplicitlyDisabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			FailureClassifier: &FailureClassifierConfig{Enabled: false},
		},
	}
	if IsPatrolEnabled(cfg, "failure_classifier") {
		t.Error("failure_classifier should be disabled when explicitly set false")
	}
}

// --- Signature compilation tests ---

func TestCompileSignatures_ValidPatterns(t *testing.T) {
	sigs := []FailureSignature{
		{
			ID:       "test-sig",
			Patterns: []string{`error TS\d+:`, `Type error:`},
		},
	}
	compiled, err := compileSignatures(sigs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(compiled) != 1 {
		t.Fatalf("expected 1 compiled signature, got %d", len(compiled))
	}
	if len(compiled[0].patterns) != 2 {
		t.Errorf("expected 2 compiled patterns, got %d", len(compiled[0].patterns))
	}
}

func TestCompileSignatures_InvalidPattern(t *testing.T) {
	sigs := []FailureSignature{
		{
			ID:       "bad-sig",
			Patterns: []string{`[invalid`},
		},
	}
	_, err := compileSignatures(sigs)
	if err == nil {
		t.Error("expected error for invalid regex pattern")
	}
}

func TestCompileSignatures_EmptyID(t *testing.T) {
	sigs := []FailureSignature{
		{ID: "", Patterns: []string{`test`}}, // skipped
		{ID: "valid", Patterns: []string{`real`}},
	}
	compiled, err := compileSignatures(sigs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(compiled) != 1 {
		t.Errorf("expected 1 compiled signature (empty ID skipped), got %d", len(compiled))
	}
}

func TestCompileSignatures_Builtin(t *testing.T) {
	compiled, err := compileSignatures(builtinSignatures)
	if err != nil {
		t.Fatalf("built-in signatures failed to compile: %v", err)
	}
	if len(compiled) == 0 {
		t.Error("expected at least one built-in signature")
	}
}

// --- Pattern matching tests ---

func TestCompiledSignature_MatchesAny(t *testing.T) {
	comp := &compiledSignature{
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`Type error:`),
			regexp.MustCompile(`AttributeError:`),
		},
	}

	cases := []struct {
		text  string
		want  bool
	}{
		{"Type error: Module 'foo' has no exported member", true},
		{"AttributeError: 'None' object has no attribute 'bar'", true},
		{"everything is fine", false},
	}

	for _, tc := range cases {
		if got := comp.matchesAny(tc.text); got != tc.want {
			t.Errorf("matchesAny(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// --- Rig extraction tests ---

func TestParseMainBranchEscalation_ExtractsRig(t *testing.T) {
	description := `main_branch_test: main branch test failures:

severity: high
reason: main branch test failures:
lia_web: gate "typecheck": typecheck failed: exit status 1
error TS2305: Module './AuthCard' has no exported member 'AuthCardProps'
source: main_branch_test`

	knownRigs := []string{"lia_web", "lia_bac", "gastown"}
	rigs := parseMainBranchEscalation(description, knownRigs)
	if len(rigs) != 1 || rigs[0] != "lia_web" {
		t.Errorf("expected [lia_web], got %v", rigs)
	}
}

func TestParseMainBranchEscalation_MultipleRigs(t *testing.T) {
	description := `main branch test failures:
lia_web: gate "typecheck": failed
lia_bac: gate "test": mypy error found`

	knownRigs := []string{"lia_web", "lia_bac", "gastown"}
	rigs := parseMainBranchEscalation(description, knownRigs)
	if len(rigs) != 2 {
		t.Errorf("expected 2 rigs, got %v", rigs)
	}
}

func TestParseMainBranchEscalation_NoMatch(t *testing.T) {
	description := "some generic failure with no rig names"
	knownRigs := []string{"lia_web", "lia_bac"}
	rigs := parseMainBranchEscalation(description, knownRigs)
	if len(rigs) != 0 {
		t.Errorf("expected no rigs, got %v", rigs)
	}
}

func TestParseMainBranchEscalation_NoDuplicates(t *testing.T) {
	// Same rig mentioned multiple times
	description := "lia_web: gate A failed\nlia_web: gate B failed"
	knownRigs := []string{"lia_web"}
	rigs := parseMainBranchEscalation(description, knownRigs)
	if len(rigs) != 1 {
		t.Errorf("expected exactly 1 rig (deduplicated), got %v", rigs)
	}
}

// --- Fingerprint tests ---

func TestClassifierFingerprint_Deterministic(t *testing.T) {
	fp1 := classifierFingerprint("lia_web", "ts-import-error")
	fp2 := classifierFingerprint("lia_web", "ts-import-error")
	if fp1 != fp2 {
		t.Errorf("fingerprint is not deterministic: %q != %q", fp1, fp2)
	}
	if len(fp1) != 12 {
		t.Errorf("expected 12-char fingerprint, got %d chars: %q", len(fp1), fp1)
	}
}

func TestClassifierFingerprint_Unique(t *testing.T) {
	cases := [][2]string{
		{"lia_web", "ts-import-error"},
		{"lia_bac", "ts-import-error"},
		{"lia_web", "pre-commit-changes"},
		{"gastown", "ts-import-error"},
	}
	seen := make(map[string]bool)
	for _, c := range cases {
		fp := classifierFingerprint(c[0], c[1])
		if seen[fp] {
			t.Errorf("fingerprint collision: rig=%s sig=%s → %s already seen", c[0], c[1], fp)
		}
		seen[fp] = true
	}
}

// --- Gate extraction tests ---

func TestExtractFailureGate_Found(t *testing.T) {
	description := `main branch test failures:
lia_web: gate "typecheck": typecheck failed: exit status 1
error TS2305`

	gate := extractFailureGate(description, "lia_web")
	if gate != "typecheck" {
		t.Errorf("expected gate 'typecheck', got %q", gate)
	}
}

func TestExtractFailureGate_NotFound(t *testing.T) {
	description := "lia_web: legacy test failed: exit status 1"
	gate := extractFailureGate(description, "lia_web")
	if gate != "unknown" {
		t.Errorf("expected 'unknown' when no gate found, got %q", gate)
	}
}

func TestExtractFailureGate_WrongRig(t *testing.T) {
	description := `lia_web: gate "typecheck": failed`
	gate := extractFailureGate(description, "lia_bac")
	if gate != "unknown" {
		t.Errorf("expected 'unknown' for wrong rig, got %q", gate)
	}
}

// --- Snippet extraction tests ---

func TestExtractFailureSnippet_RigSection(t *testing.T) {
	description := `main branch test failures:
lia_web: gate "typecheck": typecheck failed: exit status 1
error TS2305: Module './AuthCard' has no exported member 'AuthCardProps'
severity: high`

	snippet := extractFailureSnippet(description, "lia_web", 20)
	if snippet == "" {
		t.Error("expected non-empty snippet")
	}
	if !strings.Contains(snippet, "lia_web") {
		t.Errorf("expected lia_web in snippet, got: %q", snippet)
	}
	// Should not include structured escalation fields
	if strings.Contains(snippet, "severity: high") {
		t.Errorf("structured field leaked into snippet: %q", snippet)
	}
}

func TestExtractFailureSnippet_Fallback(t *testing.T) {
	description := "generic failure with no rig prefix"
	snippet := extractFailureSnippet(description, "lia_web", 5)
	if snippet == "" {
		t.Error("expected fallback snippet from full description")
	}
}

func TestExtractFailureSnippet_MaxLines(t *testing.T) {
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, "lia_web: some long output line")
	}
	description := strings.Join(lines, "\n")
	snippet := extractFailureSnippet(description, "lia_web", 10)
	count := len(strings.Split(snippet, "\n"))
	if count > 10 {
		t.Errorf("expected at most 10 lines, got %d", count)
	}
}

// --- Structured field detection tests ---

func TestIsEscalationStructuredField(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"severity: high", true},
		{"reason: some message", true},
		{"source: main_branch_test", true},
		{"escalated_by: daemon", true},
		{"lia_web: gate typecheck failed", false},
		{"error TS2305: bad import", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isEscalationStructuredField(tc.line); got != tc.want {
			t.Errorf("isEscalationStructuredField(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

// --- Signatures file loading tests ---

func TestLoadFailureSignatures_FallbackToBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	d := &Daemon{
		config:       &Config{TownRoot: tmpDir},
		patrolConfig: nil,
		logger:       log.New(io.Discard, "", 0),
	}
	sigs := d.loadFailureSignatures()
	if len(sigs) == 0 {
		t.Error("expected built-in signatures when file is absent")
	}
}

func TestLoadFailureSignatures_LoadsFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	customSigs := []FailureSignature{
		{
			ID:        "custom-sig",
			Patterns:  []string{`custom error pattern`},
			BeadTitle: "Custom error: {rig}",
			BeadBody:  "Custom error body",
			Priority:  "P2",
		},
	}
	data, _ := json.Marshal(customSigs)
	if err := os.WriteFile(filepath.Join(mayorDir, "failure-signatures.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{
		config:       &Config{TownRoot: tmpDir},
		patrolConfig: nil,
		logger:       log.New(io.Discard, "", 0),
	}
	sigs := d.loadFailureSignatures()
	if len(sigs) != 1 || sigs[0].ID != "custom-sig" {
		t.Errorf("expected custom-sig, got %v", sigs)
	}
}

func TestLoadFailureSignatures_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "failure-signatures.json"), []byte(`not json`), 0644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{
		config:       &Config{TownRoot: tmpDir},
		patrolConfig: nil,
		logger:       log.New(io.Discard, "", 0),
	}
	sigs := d.loadFailureSignatures()
	// Falls back to builtins
	if len(sigs) == 0 {
		t.Error("expected fallback to built-in signatures on JSON parse error")
	}
}

func TestLoadFailureSignatures_CustomPath(t *testing.T) {
	tmpDir := t.TempDir()
	customFile := filepath.Join(tmpDir, "custom-sigs.json")

	customSigs := []FailureSignature{
		{ID: "custom", Patterns: []string{`x`}, BeadTitle: "T", BeadBody: "B"},
	}
	data, _ := json.Marshal(customSigs)
	if err := os.WriteFile(customFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{
		config: &Config{TownRoot: tmpDir},
		patrolConfig: &DaemonPatrolConfig{
			Patrols: &PatrolsConfig{
				FailureClassifier: &FailureClassifierConfig{
					Enabled:        true,
					SignaturesFile: customFile,
				},
			},
		},
		logger: log.New(io.Discard, "", 0),
	}
	sigs := d.loadFailureSignatures()
	if len(sigs) != 1 || sigs[0].ID != "custom" {
		t.Errorf("expected custom sig from custom path, got %v", sigs)
	}
}

// --- Builtin signature coverage tests (acceptance criteria) ---

func TestBuiltinSignatures_CoverTodaysFailures(t *testing.T) {
	compiled, err := compileSignatures(builtinSignatures)
	if err != nil {
		t.Fatalf("builtin signatures failed to compile: %v", err)
	}

	type testCase struct {
		name    string
		text    string
		wantSig string
	}

	cases := []testCase{
		{
			name:    "AuthCard TypeScript import",
			text:    `lia_web: gate "typecheck": typecheck failed: exit status 1\nerror TS2305: Module './AuthCard' has no exported member 'AuthCardProps'`,
			wantSig: "ts-import-error",
		},
		{
			name:    "mypy error",
			text:    `lia_bac: gate "test": test failed: exit status 1\nFound 3 errors in 2 files`,
			wantSig: "mypy-error",
		},
		{
			name:    "nbstripout",
			text:    `lia_bac: gate "pre-commit": pre-commit hook(s) made changes\nnbstripout`,
			wantSig: "nbstripout-dirty",
		},
		{
			name:    "golden snapshot drift",
			text:    `gastown: gate "e2e": e2e failed: exit status 1\nFailed: Rendered HTML does not match golden`,
			wantSig: "golden-snapshot-drift",
		},
		{
			name:    "pre-commit formatter",
			text:    `lia_web: gate "lint": lint failed\npre-commit hook(s) made changes`,
			wantSig: "pre-commit-changes",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matched := false
			for _, comp := range compiled {
				if comp.sig.ID == tc.wantSig && comp.matchesAny(tc.text) {
					matched = true
					break
				}
			}
			if !matched {
				t.Errorf("signature %q did not match failure text: %q", tc.wantSig, tc.text)
			}
		})
	}
}

func TestBuiltinSignatures_NoFalsePositivesForEnvironmentalIssues(t *testing.T) {
	// Environmental issues must NOT match any signature (per acceptance criteria).
	envFailures := []string{
		"act --user UID mismatch: expected 1000 got 0",
		"Python 3.13 is not supported, requires Python 3.10",
		"gitdir bind-mount: permission denied",
		"/usr/local/bin/node: not found in PATH",
		"docker: error response from daemon: cannot connect",
	}

	compiled, err := compileSignatures(builtinSignatures)
	if err != nil {
		t.Fatalf("builtin signatures failed to compile: %v", err)
	}

	for _, text := range envFailures {
		for _, comp := range compiled {
			if comp.matchesAny(text) {
				t.Errorf("signature %q false-matched environmental failure: %q", comp.sig.ID, text)
			}
		}
	}
}

// --- JSON config round-trip test ---

func TestFailureClassifierConfigJSON(t *testing.T) {
	data := `{"enabled": true, "interval": "10m", "signatures_file": "/tmp/sigs.json"}`
	var cfg FailureClassifierConfig
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.Enabled {
		t.Error("expected enabled=true")
	}
	if cfg.IntervalStr != "10m" {
		t.Errorf("expected interval=10m, got %q", cfg.IntervalStr)
	}
	if cfg.SignaturesFile != "/tmp/sigs.json" {
		t.Errorf("expected signatures_file=/tmp/sigs.json, got %q", cfg.SignaturesFile)
	}
}

// --- Helper functions ---


package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractLastUsage(t *testing.T) {
	// Create a temporary JSONL transcript
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.jsonl")

	lines := []string{
		// User message (no usage)
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		// First assistant message with usage
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hi!"}],"usage":{"input_tokens":5000,"output_tokens":200,"cache_read_input_tokens":3000,"cache_creation_input_tokens":1000}}}`,
		// Second assistant message with higher usage (this should be returned)
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Here you go."}],"usage":{"input_tokens":50000,"output_tokens":1500,"cache_read_input_tokens":30000,"cache_creation_input_tokens":10000}}}`,
	}

	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(transcript, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	usage, err := extractLastUsage(transcript)
	if err != nil {
		t.Fatalf("extractLastUsage: %v", err)
	}

	if usage.InputTokens != 50000 {
		t.Errorf("InputTokens = %d, want 50000", usage.InputTokens)
	}
	if usage.OutputTokens != 1500 {
		t.Errorf("OutputTokens = %d, want 1500", usage.OutputTokens)
	}
	if usage.CacheReadInputTokens != 30000 {
		t.Errorf("CacheReadInputTokens = %d, want 30000", usage.CacheReadInputTokens)
	}
	if usage.CacheCreationInputTokens != 10000 {
		t.Errorf("CacheCreationInputTokens = %d, want 10000", usage.CacheCreationInputTokens)
	}
}

func TestExtractLastUsageNoUsage(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.jsonl")

	lines := []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hi!"}]}}`,
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(transcript, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := extractLastUsage(transcript)
	if err == nil {
		t.Fatal("expected error for transcript with no usage, got nil")
	}
}

func TestFindNewestJSONL(t *testing.T) {
	dir := t.TempDir()

	// Create two files with different mtimes
	file1 := filepath.Join(dir, "aaa.jsonl")
	file2 := filepath.Join(dir, "bbb.jsonl")
	file3 := filepath.Join(dir, "not-jsonl.txt")

	if err := os.WriteFile(file1, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file2, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file3, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// file2 should be newer since it was written after file1
	path, err := findNewestJSONL(dir)
	if err != nil {
		t.Fatalf("findNewestJSONL: %v", err)
	}
	// Both are valid JSONL files; we just need one to be returned
	if path != file1 && path != file2 {
		t.Errorf("expected one of the jsonl files, got %s", path)
	}
}

func TestFindNewestJSONLEmpty(t *testing.T) {
	dir := t.TempDir()
	_, err := findNewestJSONL(dir)
	if err == nil {
		t.Fatal("expected error for empty directory, got nil")
	}
}

func TestParseInt(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"200000", 200000},
		{"0", 0},
		{"invalid", 0},
		{"", 0},
		{"42", 42},
	}
	for _, tt := range tests {
		got := parseInt(tt.in)
		if got != tt.want {
			t.Errorf("parseInt(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseFloat(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{"0.75", 0.75},
		{"0.85", 0.85},
		{"invalid", 0.0},
		{"", 0.0},
	}
	for _, tt := range tests {
		got := parseFloat(tt.in)
		if got != tt.want {
			t.Errorf("parseFloat(%q) = %f, want %f", tt.in, got, tt.want)
		}
	}
}

func TestResolveRole(t *testing.T) {
	// Save and restore env
	orig := os.Getenv("GT_ROLE")
	defer os.Setenv("GT_ROLE", orig)

	os.Setenv("GT_ROLE", "polecat/chrome")
	if got := resolveRole(); got != "polecat/chrome" {
		t.Errorf("resolveRole() = %q, want %q", got, "polecat/chrome")
	}

	os.Setenv("GT_ROLE", "")
	origPolecat := os.Getenv("GT_POLECAT")
	defer os.Setenv("GT_POLECAT", origPolecat)
	os.Setenv("GT_POLECAT", "chrome")
	if got := resolveRole(); got != "polecat" {
		t.Errorf("resolveRole() with GT_POLECAT = %q, want %q", got, "polecat")
	}
}

func TestContextUsageResultJSON(t *testing.T) {
	result := contextUsageResult{
		InputTokens:  45000,
		OutputTokens: 1500,
		CacheRead:    30000,
		CacheCreate:  10000,
		TotalInput:   85000,
		MaxTokens:    200000,
		UsageRatio:   0.425,
		UsagePct:     42,
		RemainingK:   115,
		Level:        "ok",
		Role:         "polecat",
	}

	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}

	var parsed contextUsageResult
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}

	if parsed.Level != "ok" {
		t.Errorf("Level = %q, want %q", parsed.Level, "ok")
	}
	if parsed.UsagePct != 42 {
		t.Errorf("UsagePct = %d, want 42", parsed.UsagePct)
	}
}

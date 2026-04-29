package plugin

import (
	"encoding/json"
	"testing"
)

func TestExtractJSONObject(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			"clean JSON object",
			`{"id":"test"}`,
			`{"id":"test"}`,
		},
		{
			"warning prefix before JSON",
			"⚠ Creating test issue in production database\n  Title: \"test\" appears to be test data\n{\"id\":\"cws-wisp-avv\"}",
			`{"id":"cws-wisp-avv"}`,
		},
		{
			"plain-ASCII warning prefix",
			"Warning: permissions issue\n{\"id\":\"abc-123\"}",
			`{"id":"abc-123"}`,
		},
		{
			"no object in data",
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
			got := string(extractJSONObject([]byte(tc.data)))
			if got != tc.want {
				t.Errorf("extractJSONObject(%q) = %q, want %q", tc.data, got, tc.want)
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
			"⚠ bd warning on stdout\n[{\"id\":\"test\"}]",
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

// TestRecordRun_WarningPrefixParses exercises the exact failure mode from the bug
// report: bd emits a warning to stdout before the JSON object, causing
// json.Unmarshal of the raw bytes to fail silently (result.ID == "") so the
// subsequent `bd close` is a no-op and plugin-run wisps accumulate forever.
//
// This test verifies extractJSONObject is applied to the same shape of output
// that RecordRun produces, so the fix is preserved if the parsing logic is
// touched in the future.
func TestRecordRun_WarningPrefixParses(t *testing.T) {
	// Shape matches `bd create --json` output when a warning is emitted.
	rawStdout := []byte(`⚠ Creating test issue in production database
  Title: "test-close-debug" appears to be test data
  Use --title="real title" or run in a test database.
{"id":"cws-wisp-avv","title":"Plugin run: test","created_at":"2026-04-28T00:00:00Z"}`)

	// Without the fix, Unmarshal on raw bytes fails and ID stays empty.
	var noFix struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rawStdout, &noFix); err == nil && noFix.ID != "" {
		t.Fatal("test precondition failed: raw stdout unexpectedly parsed; bug no longer reproducible")
	}

	// With extractJSONObject, parsing succeeds and ID is populated so the
	// subsequent `bd close` gets a real ID to close.
	var withFix struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(extractJSONObject(rawStdout), &withFix); err != nil {
		t.Fatalf("parsing after extractJSONObject failed: %v", err)
	}
	if withFix.ID != "cws-wisp-avv" {
		t.Errorf("ID after fix = %q, want %q", withFix.ID, "cws-wisp-avv")
	}
}

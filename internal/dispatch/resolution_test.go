package dispatch

import (
	"strings"
	"testing"
)

func TestVerifyBeadIDMatch(t *testing.T) {
	tests := []struct {
		name        string
		requested   string
		resolved    string
		wantErr     bool
		wantErrSubs []string
	}{
		{
			name:      "exact full-ID match passes",
			requested: "gt-abc",
			resolved:  "gt-abc",
			wantErr:   false,
		},
		{
			name:        "full-ID prefix collision fails",
			requested:   "gt-74f",
			resolved:    "gt-74fjf",
			wantErr:     true,
			wantErrSubs: []string{"gt-74f", "gt-74fjf", "prefix collision"},
		},
		{
			name:      "different-prefix mismatch also fails",
			requested: "bd-abc",
			resolved:  "gt-abc",
			wantErr:   true,
		},
		{
			name:      "bare hash without prefix is permitted to resolve loosely",
			requested: "74f",
			resolved:  "gt-74fjf",
			wantErr:   false,
		},
		{
			name:      "empty resolved ID (older bd or partial JSON) is permissive",
			requested: "gt-74f",
			resolved:  "",
			wantErr:   false,
		},
		{
			name:      "empty requested ID (no prefix) skips check",
			requested: "",
			resolved:  "gt-anything",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyBeadIDMatch(tt.requested, tt.resolved)
			if (err != nil) != tt.wantErr {
				t.Fatalf("VerifyBeadIDMatch(%q, %q) err = %v, wantErr %v",
					tt.requested, tt.resolved, err, tt.wantErr)
			}
			if err == nil {
				return
			}
			msg := err.Error()
			for _, sub := range tt.wantErrSubs {
				if !strings.Contains(msg, sub) {
					t.Errorf("VerifyBeadIDMatch(%q, %q) err %q missing substring %q",
						tt.requested, tt.resolved, msg, sub)
				}
			}
		})
	}
}

func TestParseBeadInfo(t *testing.T) {
	tests := []struct {
		name      string
		beadID    string
		out       string
		wantErr   bool
		wantID    string
		wantTitle string
	}{
		{
			name:    "empty output is not found",
			beadID:  "gt-abc",
			out:     "",
			wantErr: true,
		},
		{
			name:    "invalid JSON errors",
			beadID:  "gt-abc",
			out:     "not json",
			wantErr: true,
		},
		{
			name:    "empty array is not found",
			beadID:  "gt-abc",
			out:     "[]",
			wantErr: true,
		},
		{
			name:      "single bead parses and returns first element",
			beadID:    "gt-abc",
			out:       `[{"id":"gt-abc","title":"hello","status":"open"}]`,
			wantErr:   false,
			wantID:    "gt-abc",
			wantTitle: "hello",
		},
		{
			name:      "array with dependents takes first element",
			beadID:    "gt-abc",
			out:       `[{"id":"gt-abc","title":"hello","status":"open"},{"id":"gt-dep","title":"dep"}]`,
			wantErr:   false,
			wantID:    "gt-abc",
			wantTitle: "hello",
		},
		{
			name:    "prefix collision rejected via VerifyBeadIDMatch",
			beadID:  "gt-74f",
			out:     `[{"id":"gt-74fjf","title":"wrong bead","status":"closed"}]`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := ParseBeadInfo(tt.beadID, []byte(tt.out))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseBeadInfo(%q, %q) err = %v, wantErr %v",
					tt.beadID, tt.out, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if info.ID != tt.wantID {
				t.Errorf("ParseBeadInfo() ID = %q, want %q", info.ID, tt.wantID)
			}
			if info.Title != tt.wantTitle {
				t.Errorf("ParseBeadInfo() Title = %q, want %q", info.Title, tt.wantTitle)
			}
		})
	}
}

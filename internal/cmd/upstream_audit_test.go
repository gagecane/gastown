package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/upstreamsync"
)

func TestParseAuditDuration(t *testing.T) {
	cases := map[string]struct {
		want    time.Duration
		wantErr bool
	}{
		"":          {0, false},
		"0":         {0, false},
		"24h":       {24 * time.Hour, false},
		"168h":      {168 * time.Hour, false},
		"72h30m":    {72*time.Hour + 30*time.Minute, false},
		"-5h":       {0, true}, // negative rejected
		"not-a-dur": {0, true},
	}
	for input, exp := range cases {
		got, err := parseAuditDuration(input)
		gotErr := err != nil
		if gotErr != exp.wantErr {
			t.Errorf("parseAuditDuration(%q) err=%v, wantErr=%v (err=%v)", input, gotErr, exp.wantErr, err)
		}
		if !exp.wantErr && got != exp.want {
			t.Errorf("parseAuditDuration(%q) = %v, want %v", input, got, exp.want)
		}
	}
}

func TestFilterFindingsByCode(t *testing.T) {
	in := []upstreamsync.AuditFinding{
		{Code: upstreamsync.AuditCodeOutcomeGateFailure, Severity: upstreamsync.SeverityWarn},
		{Code: upstreamsync.AuditCodeResolutionAgentAuthored, Severity: upstreamsync.SeverityInfo},
		{Code: upstreamsync.AuditCodeOutcomeGateFailure, Severity: upstreamsync.SeverityWarn},
	}
	got := filterFindingsByCode(in, upstreamsync.AuditCodeOutcomeGateFailure)
	if len(got) != 2 {
		t.Errorf("expected 2 gate-failure findings, got %d: %+v", len(got), got)
	}
	for _, f := range got {
		if f.Code != upstreamsync.AuditCodeOutcomeGateFailure {
			t.Errorf("filter leaked finding with code %q", f.Code)
		}
	}

	// Empty filter returns input unchanged.
	got = filterFindingsByCode(in, "")
	if len(got) != len(in) {
		t.Errorf("empty filter changed length: got %d, want %d", len(got), len(in))
	}
}

func TestEmitAuditJSON_Envelope(t *testing.T) {
	findings := []upstreamsync.AuditFinding{
		{
			AttemptID: "att-001",
			Severity:  upstreamsync.SeverityCritical,
			Code:      upstreamsync.AuditCodeRigAutoPaused,
			Title:     "rig paused",
		},
		{
			AttemptID: "att-002",
			Severity:  upstreamsync.SeverityWarn,
			Code:      upstreamsync.AuditCodeOutcomeGateFailure,
			Title:     "gate failed",
		},
	}
	now, _ := time.Parse(time.RFC3339, "2026-05-29T22:00:00Z")
	since, _ := time.Parse(time.RFC3339, "2026-05-28T22:00:00Z")
	opts := upstreamsync.AuditOptions{
		Since:               since,
		MinSeverity:         upstreamsync.SeverityWarn,
		StaleNoSuccessAfter: 168 * time.Hour,
		Now:                 now,
	}

	var buf bytes.Buffer
	if err := emitAuditJSON(&buf, "gastown_upstream", findings, opts); err != nil {
		t.Fatalf("emitAuditJSON err: %v", err)
	}

	var got struct {
		Rig         string `json:"rig"`
		GeneratedAt string `json:"generated_at"`
		Since       string `json:"since"`
		StaleAfter  string `json:"stale_after"`
		MinSeverity string `json:"min_severity"`
		Counts      map[string]int
		Findings    []upstreamsync.AuditFinding
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nbuf=%s", err, buf.String())
	}
	if got.Rig != "gastown_upstream" {
		t.Errorf("rig = %q, want gastown_upstream", got.Rig)
	}
	if got.GeneratedAt != "2026-05-29T22:00:00Z" {
		t.Errorf("generated_at = %q, want 2026-05-29T22:00:00Z", got.GeneratedAt)
	}
	if got.Since != "2026-05-28T22:00:00Z" {
		t.Errorf("since = %q", got.Since)
	}
	if got.StaleAfter != "168h0m0s" {
		t.Errorf("stale_after = %q, want 168h0m0s", got.StaleAfter)
	}
	if got.MinSeverity != "warn" {
		t.Errorf("min_severity = %q, want warn", got.MinSeverity)
	}
	if got.Counts["critical"] != 1 || got.Counts["warn"] != 1 {
		t.Errorf("counts = %+v, want critical=1 warn=1", got.Counts)
	}
	if len(got.Findings) != 2 {
		t.Errorf("findings len = %d, want 2", len(got.Findings))
	}
}

func TestEmitAuditTable_NoFindings(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-05-29T22:00:00Z")
	since, _ := time.Parse(time.RFC3339, "2026-05-28T22:00:00Z")

	var buf bytes.Buffer
	err := emitAuditTable(&buf, "gastown_upstream", nil, upstreamsync.AuditOptions{
		Since:       since,
		MinSeverity: upstreamsync.SeverityWarn,
		Now:         now,
	})
	if err != nil {
		t.Fatalf("emitAuditTable err: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "no findings") {
		t.Errorf("expected 'no findings' message; got %q", out)
	}
}

func TestEmitAuditTable_PrintsHeaderAndCounts(t *testing.T) {
	findings := []upstreamsync.AuditFinding{
		{
			AttemptID: "att-001",
			Severity:  upstreamsync.SeverityCritical,
			Code:      upstreamsync.AuditCodeRigAutoPaused,
			Title:     "rig paused",
			Detail:    "single line",
		},
		{
			AttemptID:        "att-002",
			Severity:         upstreamsync.SeverityWarn,
			Code:             upstreamsync.AuditCodeOutcomeGateFailure,
			Title:            "gate failed",
			AttemptStartedAt: "2026-05-29T20:00:00Z",
			Files:            []string{"go.mod", "go.sum"},
		},
	}
	now, _ := time.Parse(time.RFC3339, "2026-05-29T22:00:00Z")
	since, _ := time.Parse(time.RFC3339, "2026-05-28T22:00:00Z")

	var buf bytes.Buffer
	err := emitAuditTable(&buf, "gastown_upstream", findings, upstreamsync.AuditOptions{
		Since:       since,
		MinSeverity: upstreamsync.SeverityWarn,
		Now:         now,
	})
	if err != nil {
		t.Fatalf("emitAuditTable err: %v", err)
	}
	out := buf.String()

	// Header line, counts, and the table headers.
	for _, want := range []string{
		"Upstream audit: gastown_upstream",
		"critical=1, warn=1, info=0",
		"SEVERITY",
		"CODE",
		"ATTEMPT",
		"WHEN",
		"TITLE",
		"CRITICAL",
		"rig paused",
		"WARN",
		"gate failed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nGOT:\n%s", want, out)
		}
	}
	// Files block for the warn finding.
	if !strings.Contains(out, "- go.mod") || !strings.Contains(out, "- go.sum") {
		t.Errorf("expected file list rendered for finding; got:\n%s", out)
	}
}

func TestCountBySeverity(t *testing.T) {
	in := []upstreamsync.AuditFinding{
		{Severity: upstreamsync.SeverityCritical},
		{Severity: upstreamsync.SeverityCritical},
		{Severity: upstreamsync.SeverityWarn},
		{Severity: upstreamsync.SeverityInfo},
	}
	got := countBySeverity(in)
	if got["critical"] != 2 || got["warn"] != 1 || got["info"] != 1 {
		t.Errorf("countBySeverity = %+v, want critical=2 warn=1 info=1", got)
	}
}

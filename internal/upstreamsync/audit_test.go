package upstreamsync

import (
	"strings"
	"testing"
	"time"
)

// fixedTime parses an RFC3339 timestamp or fails the test. Used to
// build deterministic audit fixtures without sprinkling time.Now()
// calls (which would defeat the pure-function contract).
func fixedTime(t *testing.T, ts string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatalf("fixedTime parse %q: %v", ts, err)
	}
	return parsed
}

func TestAuditState_RigPaused_EmitsCriticalFinding(t *testing.T) {
	state := SyncStateMetadata{
		Rig:         "gastown_upstream",
		State:       StatePaused,
		PauseReason: "operator-paused: investigating",
	}
	got := AuditState(state, AuditOptions{
		IncludeRigLevel: true,
		Now:             fixedTime(t, "2026-05-29T23:00:00Z"),
	})

	found := false
	for _, f := range got {
		if f.Code == AuditCodeRigAutoPaused {
			found = true
			if f.Severity != SeverityCritical {
				t.Errorf("paused finding severity = %q, want critical", f.Severity)
			}
			if !strings.Contains(f.Detail, "investigating") {
				t.Errorf("paused finding should include reason; got %q", f.Detail)
			}
		}
	}
	if !found {
		t.Errorf("paused state did not emit AuditCodeRigAutoPaused finding")
	}
}

func TestAuditState_CircuitBreaker_TripsAtThreeFailures(t *testing.T) {
	cases := []struct {
		name     string
		failures int
		wantCode bool
	}{
		{"two-failures-no-trip", 2, false},
		{"three-failures-trips", 3, true},
		{"five-failures-trips", 5, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := SyncStateMetadata{
				Rig:                 "gastown_upstream",
				State:               StateIdle,
				ConsecutiveFailures: tc.failures,
			}
			got := AuditState(state, AuditOptions{IncludeRigLevel: true})
			has := false
			for _, f := range got {
				if f.Code == AuditCodeRigCircuitBreakerTripped {
					has = true
				}
			}
			if has != tc.wantCode {
				t.Errorf("failures=%d: got circuit-breaker finding=%v, want %v",
					tc.failures, has, tc.wantCode)
			}
		})
	}
}

func TestAuditState_ResolutionAgentAuthored_EmitsInfoForCleanFiles(t *testing.T) {
	state := SyncStateMetadata{
		Rig: "gastown_upstream",
		Attempts: []SyncAttempt{
			{
				ID:        "att-001",
				StartedAt: "2026-05-29T22:00:00Z",
				Outcome:   "success",
				Conflicts: []string{"internal/feed/feed.go", "README.md"},
				Actor:     "polecat:brahmin",
			},
		},
	}
	got := AuditState(state, AuditOptions{
		Since:           fixedTime(t, "2026-05-29T00:00:00Z"),
		IncludeRigLevel: false,
	})

	if len(got) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(got), got)
	}
	if got[0].Code != AuditCodeResolutionAgentAuthored {
		t.Errorf("code = %q, want %q", got[0].Code, AuditCodeResolutionAgentAuthored)
	}
	if got[0].Severity != SeverityInfo {
		t.Errorf("severity = %q, want info", got[0].Severity)
	}
	if !strings.Contains(got[0].Detail, "polecat:brahmin") {
		t.Errorf("detail should name the actor; got %q", got[0].Detail)
	}
}

func TestAuditState_ResolutionTouchingRestrictedPaths_EmitsCritical(t *testing.T) {
	cases := []struct {
		name      string
		conflicts []string
		wantCrit  bool
	}{
		{
			name:      "auth-prefix",
			conflicts: []string{"internal/auth/session.go"},
			wantCrit:  true,
		},
		{
			name:      "secrets-prefix",
			conflicts: []string{"internal/secrets/loader.go"},
			wantCrit:  true,
		},
		{
			name:      "github-actions",
			conflicts: []string{".github/workflows/ci.yml"},
			wantCrit:  true,
		},
		{
			name:      "scripts-prefix",
			conflicts: []string{"scripts/check-upstream-rebased.sh"},
			wantCrit:  true,
		},
		{
			name:      "go-mod-exact",
			conflicts: []string{"go.mod"},
			wantCrit:  true,
		},
		{
			name:      "shell-suffix-anywhere",
			conflicts: []string{"docker-entrypoint.sh"},
			wantCrit:  true,
		},
		{
			name:      "regular-go-file",
			conflicts: []string{"internal/feed/feed.go"},
			wantCrit:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := SyncStateMetadata{
				Rig: "gastown_upstream",
				Attempts: []SyncAttempt{
					{
						ID:        "att",
						StartedAt: "2026-05-29T22:00:00Z",
						Outcome:   "success",
						Conflicts: tc.conflicts,
						Actor:     "polecat:brahmin",
					},
				},
			}
			got := AuditState(state, AuditOptions{
				IncludeRigLevel: false,
			})
			hasCrit := false
			for _, f := range got {
				if f.Code == AuditCodeResolutionRestrictedAdj && f.Severity == SeverityCritical {
					hasCrit = true
				}
			}
			if hasCrit != tc.wantCrit {
				t.Errorf("conflicts=%v: got critical finding=%v, want %v",
					tc.conflicts, hasCrit, tc.wantCrit)
			}
		})
	}
}

func TestAuditState_Outcome_NonSuccessAttempts(t *testing.T) {
	state := SyncStateMetadata{
		Rig: "gastown_upstream",
		Attempts: []SyncAttempt{
			{ID: "a1", StartedAt: "2026-05-29T22:00:00Z", Outcome: "gate-failure"},
			{ID: "a2", StartedAt: "2026-05-29T22:05:00Z", Outcome: "conflict-too-complex"},
			{ID: "a3", StartedAt: "2026-05-29T22:10:00Z", Outcome: "conflict-restricted"},
			{ID: "a4", StartedAt: "2026-05-29T22:15:00Z", Outcome: "success"},
		},
	}
	got := AuditState(state, AuditOptions{IncludeRigLevel: false})

	byID := make(map[string]AuditFinding)
	for _, f := range got {
		// Findings may key the same attempt under multiple codes;
		// snapshot the first.
		if _, ok := byID[f.AttemptID]; !ok {
			byID[f.AttemptID] = f
		}
	}

	// Gate failure: warn-level outcome finding.
	if f, ok := byID["a1"]; !ok || f.Severity != SeverityWarn {
		t.Errorf("a1 (gate-failure): got %+v, want warn-severity finding", f)
	}
	// Too-complex: warn-level outcome finding (operator-routed).
	if f, ok := byID["a2"]; !ok || f.Severity != SeverityWarn {
		t.Errorf("a2 (conflict-too-complex): got %+v, want warn", f)
	}
	// Restricted: critical-level — agent dispatched conflict that
	// touched a sensitive path; even though the merge didn't land,
	// the attempt itself signals attacker-shaped surface.
	if f, ok := byID["a3"]; !ok || f.Severity != SeverityCritical {
		t.Errorf("a3 (conflict-restricted): got %+v, want critical", f)
	}
	// Success: no finding.
	if _, ok := byID["a4"]; ok {
		t.Errorf("a4 (success) should produce no finding, got: %+v", byID["a4"])
	}
}

func TestAuditState_SinceFilter_DropsOlderAttempts(t *testing.T) {
	state := SyncStateMetadata{
		Rig: "gastown_upstream",
		Attempts: []SyncAttempt{
			{ID: "old", StartedAt: "2026-05-28T10:00:00Z", Outcome: "gate-failure"},
			{ID: "recent", StartedAt: "2026-05-29T22:00:00Z", Outcome: "gate-failure"},
		},
	}
	got := AuditState(state, AuditOptions{
		Since:           fixedTime(t, "2026-05-29T00:00:00Z"),
		IncludeRigLevel: false,
	})
	for _, f := range got {
		if f.AttemptID == "old" {
			t.Errorf("audit should have filtered out old attempt, got %+v", f)
		}
	}
}

func TestAuditState_StaleNoSuccess_FiresAfterWindow(t *testing.T) {
	now := fixedTime(t, "2026-05-29T22:00:00Z")
	state := SyncStateMetadata{
		Rig:        "gastown_upstream",
		State:      StateIdle,
		LastSyncAt: "2026-05-21T22:00:00Z", // 8 days old
	}
	got := AuditState(state, AuditOptions{
		IncludeRigLevel:     true,
		Now:                 now,
		StaleNoSuccessAfter: 7 * 24 * time.Hour,
	})

	found := false
	for _, f := range got {
		if f.Code == AuditCodeRigStaleNoSuccess {
			found = true
			if f.Severity != SeverityWarn {
				t.Errorf("stale finding severity = %q, want warn", f.Severity)
			}
		}
	}
	if !found {
		t.Errorf("stale audit window not triggered for 8-day-old last sync")
	}
}

func TestAuditState_StaleNoSuccess_NoFireWithinWindow(t *testing.T) {
	now := fixedTime(t, "2026-05-29T22:00:00Z")
	state := SyncStateMetadata{
		Rig:        "gastown_upstream",
		State:      StateIdle,
		LastSyncAt: "2026-05-29T10:00:00Z", // 12h old
	}
	got := AuditState(state, AuditOptions{
		IncludeRigLevel:     true,
		Now:                 now,
		StaleNoSuccessAfter: 7 * 24 * time.Hour,
	})
	for _, f := range got {
		if f.Code == AuditCodeRigStaleNoSuccess {
			t.Errorf("stale finding should not fire within window: %+v", f)
		}
	}
}

func TestAuditState_MinSeverity_Filters(t *testing.T) {
	state := SyncStateMetadata{
		Rig: "gastown_upstream",
		Attempts: []SyncAttempt{
			{ID: "info", StartedAt: "2026-05-29T22:00:00Z", Outcome: "success",
				Conflicts: []string{"internal/feed/feed.go"}, Actor: "polecat:x"},
			{ID: "warn", StartedAt: "2026-05-29T22:05:00Z", Outcome: "gate-failure"},
			{ID: "crit", StartedAt: "2026-05-29T22:10:00Z", Outcome: "conflict-restricted"},
		},
	}
	got := AuditState(state, AuditOptions{
		IncludeRigLevel: false,
		MinSeverity:     SeverityWarn,
	})
	for _, f := range got {
		if f.Severity == SeverityInfo {
			t.Errorf("MinSeverity=warn should drop info finding: %+v", f)
		}
	}
	// Both warn and crit should remain.
	hasWarn, hasCrit := false, false
	for _, f := range got {
		switch f.Severity {
		case SeverityWarn:
			hasWarn = true
		case SeverityCritical:
			hasCrit = true
		}
	}
	if !hasWarn || !hasCrit {
		t.Errorf("expected warn+critical findings, got %+v", got)
	}
}

func TestAuditState_FindingsSortedBySeverityThenTime(t *testing.T) {
	state := SyncStateMetadata{
		Rig: "gastown_upstream",
		Attempts: []SyncAttempt{
			{ID: "info-old", StartedAt: "2026-05-29T20:00:00Z", Outcome: "success",
				Conflicts: []string{"internal/feed/feed.go"}, Actor: "x"},
			{ID: "warn", StartedAt: "2026-05-29T21:00:00Z", Outcome: "gate-failure"},
			{ID: "crit", StartedAt: "2026-05-29T22:00:00Z", Outcome: "conflict-restricted"},
			{ID: "info-new", StartedAt: "2026-05-29T22:30:00Z", Outcome: "success",
				Conflicts: []string{"internal/feed/feed.go"}, Actor: "y"},
		},
	}
	got := AuditState(state, AuditOptions{IncludeRigLevel: false})

	if len(got) < 3 {
		t.Fatalf("expected at least 3 findings, got %d", len(got))
	}
	// First finding must be the critical one.
	if got[0].Severity != SeverityCritical {
		t.Errorf("findings not sorted by severity; first severity = %q", got[0].Severity)
	}
	// Among info findings, the newer should come first.
	infoIdx := []int{}
	for i, f := range got {
		if f.Severity == SeverityInfo {
			infoIdx = append(infoIdx, i)
		}
	}
	if len(infoIdx) >= 2 {
		first, second := got[infoIdx[0]], got[infoIdx[1]]
		ti, _ := time.Parse(time.RFC3339, first.AttemptStartedAt)
		tj, _ := time.Parse(time.RFC3339, second.AttemptStartedAt)
		if !ti.After(tj) {
			t.Errorf("info findings not sorted newest-first: %v vs %v", ti, tj)
		}
	}
}

func TestIsAuditRestricted_Coverage(t *testing.T) {
	cases := map[string]bool{
		"internal/auth/session.go":          true,
		"internal/secrets/loader.go":        true,
		"internal/crypto/rsa.go":            true,
		".github/workflows/ci.yml":          true,
		"scripts/check-upstream-rebased.sh": true,
		"go.mod":                            true,
		"go.sum":                            true,
		"Makefile":                          true,
		"docker-entrypoint.sh":              true, // suffix-only match
		"internal/feed/feed.go":             false,
		"README.md":                         false,
		"docs/design/architecture.md":       false,
		// Ensure prefix matching does not over-match: a file named
		// "go.mod-vendor" should NOT match "go.mod" (we use exact for
		// no-trailing-slash entries).
		"go.mod-vendor":             false,
		"internal/authz/policy.go":  false, // "internal/auth/" not "internal/auth"
		"internal/secrets-old/x.go": false,
	}
	for path, want := range cases {
		got := isAuditRestricted(path)
		if got != want {
			t.Errorf("isAuditRestricted(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestParseAttemptTime_Robust(t *testing.T) {
	cases := map[string]bool{
		"":                          false,
		"   ":                       false,
		"not a date":                false,
		"2026-05-29T22:00:00Z":      true,
		"2026-13-29T22:00:00Z":      false, // invalid month
		"2026-05-29T22:00:00+05:00": true,
	}
	for s, ok := range cases {
		_, gotOK := parseAttemptTime(s)
		if gotOK != ok {
			t.Errorf("parseAttemptTime(%q) ok = %v, want %v", s, gotOK, ok)
		}
	}
}

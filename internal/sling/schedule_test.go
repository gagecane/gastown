package sling

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// TestShouldReattachFormula verifies the gs-am8 GAP 2 re-attach decision: an
// already-scheduled bead's formula is replaced only under --force with a
// genuinely different formula; otherwise the idempotent no-op stands.
func TestShouldReattachFormula(t *testing.T) {
	ctx := func(f string) *capacity.SlingContextFields {
		return &capacity.SlingContextFields{Formula: f}
	}
	cases := []struct {
		name      string
		force     bool
		requested string
		existing  *capacity.SlingContextFields
		want      bool
	}{
		{"force + different formula re-attaches", true, "mol-pw-adversarial-review", ctx("mol-polecat-work"), true},
		{"force + same formula is a no-op", true, "mol-polecat-work", ctx("mol-polecat-work"), false},
		{"no force never re-attaches", false, "mol-pw-adversarial-review", ctx("mol-polecat-work"), false},
		{"force + clearing formula (to default) re-attaches", true, "", ctx("mol-polecat-work"), true},
		{"nil existing fields never re-attaches", true, "mol-pw-adversarial-review", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldReattachFormula(tc.force, tc.requested, tc.existing); got != tc.want {
				t.Errorf("ShouldReattachFormula(%v,%q,%+v) = %v, want %v",
					tc.force, tc.requested, tc.existing, got, tc.want)
			}
		})
	}
}

// TestIsStaleOrFailedContext verifies the gu-rm08l recovery predicate: an open
// sling context is treated as stale/failed (and thus recyclable on re-sling)
// when it recorded any transient dispatch failure OR has aged past the TTL.
// A healthy, fresh, never-failed context must NOT be recycled.
func TestIsStaleOrFailedContext(t *testing.T) {
	now := time.Date(2026, 6, 2, 14, 0, 0, 0, time.UTC)
	fresh := now.Add(-5 * time.Minute).Format(time.RFC3339)
	aged := now.Add(-ContextTTL - time.Minute).Format(time.RFC3339)

	cases := []struct {
		name   string
		ctx    *beads.Issue
		fields *capacity.SlingContextFields
		want   bool
	}{
		{
			name:   "fresh, no failures — healthy in-flight, keep",
			ctx:    &beads.Issue{CreatedAt: fresh},
			fields: &capacity.SlingContextFields{DispatchFailures: 0},
			want:   false,
		},
		{
			name:   "fresh but one transient failure — recycle",
			ctx:    &beads.Issue{CreatedAt: fresh},
			fields: &capacity.SlingContextFields{DispatchFailures: 1},
			want:   true,
		},
		{
			name:   "aged past TTL, no failures — recycle",
			ctx:    &beads.Issue{CreatedAt: aged},
			fields: &capacity.SlingContextFields{DispatchFailures: 0},
			want:   true,
		},
		{
			name:   "nil fields, fresh — keep (fail-closed on age)",
			ctx:    &beads.Issue{CreatedAt: fresh},
			fields: nil,
			want:   false,
		},
		{
			name:   "nil fields, aged — recycle on age alone",
			ctx:    &beads.Issue{CreatedAt: aged},
			fields: nil,
			want:   true,
		},
		{
			name:   "empty created_at, no failures — keep (unknown age fails closed)",
			ctx:    &beads.Issue{CreatedAt: ""},
			fields: &capacity.SlingContextFields{DispatchFailures: 0},
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsStaleOrFailedContext(tc.ctx, tc.fields, now); got != tc.want {
				t.Errorf("IsStaleOrFailedContext(%+v, %+v) = %v, want %v",
					tc.ctx, tc.fields, got, tc.want)
			}
		})
	}
}

// TestStaleContextReslingReason verifies the close reason distinguishes a
// transient-failure expiry from a plain TTL expiry, for operator observability.
func TestStaleContextReslingReason(t *testing.T) {
	if got := StaleContextReslingReason(&capacity.SlingContextFields{DispatchFailures: 2}); !strings.Contains(got, "failed-context-resling") || !strings.Contains(got, "dispatch_failures=2") {
		t.Errorf("failure reason = %q, want failed-context-resling with dispatch_failures=2", got)
	}
	if got := StaleContextReslingReason(&capacity.SlingContextFields{DispatchFailures: 0}); !strings.Contains(got, "stale-context-resling") || !strings.Contains(got, "ttl-expired") {
		t.Errorf("ttl reason = %q, want stale-context-resling ttl-expired", got)
	}
	if got := StaleContextReslingReason(nil); !strings.Contains(got, "stale-context-resling") {
		t.Errorf("nil fields reason = %q, want stale-context-resling", got)
	}
}

// TestContextOlderThan verifies the age predicate fails closed on empty/
// unparseable timestamps and only reports true past the TTL boundary.
func TestContextOlderThan(t *testing.T) {
	now := time.Date(2026, 6, 2, 14, 0, 0, 0, time.UTC)
	ttl := 30 * time.Minute
	cases := []struct {
		name string
		ctx  *beads.Issue
		want bool
	}{
		{"nil context", nil, false},
		{"empty timestamp fails closed", &beads.Issue{CreatedAt: ""}, false},
		{"unparseable timestamp fails closed", &beads.Issue{CreatedAt: "not-a-time"}, false},
		{"fresh within ttl", &beads.Issue{CreatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339)}, false},
		{"exactly at ttl is not older", &beads.Issue{CreatedAt: now.Add(-ttl).Format(time.RFC3339)}, false},
		{"past ttl is older", &beads.Issue{CreatedAt: now.Add(-ttl - time.Minute).Format(time.RFC3339)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContextOlderThan(tc.ctx, now, ttl); got != tc.want {
				t.Errorf("ContextOlderThan(%+v) = %v, want %v", tc.ctx, got, tc.want)
			}
		})
	}
}

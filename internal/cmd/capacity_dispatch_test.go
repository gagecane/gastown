package cmd

import (
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestShouldFireCrossRigEscalation_Debounces(t *testing.T) {
	resetCrossRigEscalationStateForTest()
	t.Cleanup(resetCrossRigEscalationStateForTest)

	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	if !shouldFireCrossRigEscalation("walletui", "hq", now) {
		t.Fatalf("first call must fire")
	}
	// Second call inside the debounce window must NOT fire.
	if shouldFireCrossRigEscalation("walletui", "hq", now.Add(30*time.Minute)) {
		t.Fatalf("second call inside debounce window must not fire")
	}
	// After the debounce window elapses, fire again.
	if !shouldFireCrossRigEscalation("walletui", "hq", now.Add(crossRigEscalationDebounce+time.Minute)) {
		t.Fatalf("call past debounce window must fire")
	}
}

func TestShouldFireCrossRigEscalation_KeyedByRigAndPrefix(t *testing.T) {
	resetCrossRigEscalationStateForTest()
	t.Cleanup(resetCrossRigEscalationStateForTest)

	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)

	if !shouldFireCrossRigEscalation("walletui", "hq", now) {
		t.Fatalf("walletui/hq first call must fire")
	}
	// Different rig — should fire independently.
	if !shouldFireCrossRigEscalation("furiosa", "hq", now) {
		t.Fatalf("furiosa/hq must fire (different rig)")
	}
	// Different prefix on same rig — should fire independently.
	if !shouldFireCrossRigEscalation("walletui", "wisp", now) {
		t.Fatalf("walletui/wisp must fire (different prefix)")
	}
	// Same (rig, prefix) repeats — debounced.
	if shouldFireCrossRigEscalation("walletui", "hq", now.Add(time.Minute)) {
		t.Fatalf("walletui/hq repeat must not fire")
	}
}

// TestIsContextOlderThan covers the TTL helper used by cleanupStaleContexts
// to decide whether a sling-context whose work bead is missing should be
// reaped (gu-hfr3). Fails-closed for unparseable or empty timestamps so
// brand-new contexts with no CreatedAt aren't reaped prematurely.
func TestIsContextOlderThan(t *testing.T) {
	now := time.Date(2026, 5, 1, 14, 0, 0, 0, time.UTC)
	ttl := 30 * time.Minute

	tests := []struct {
		name string
		ctx  *beads.Issue
		want bool
	}{
		{
			name: "nil context",
			ctx:  nil,
			want: false,
		},
		{
			name: "empty created_at",
			ctx:  &beads.Issue{CreatedAt: ""},
			want: false,
		},
		{
			name: "unparseable created_at",
			ctx:  &beads.Issue{CreatedAt: "not-a-timestamp"},
			want: false,
		},
		{
			name: "created now",
			ctx:  &beads.Issue{CreatedAt: now.Format(time.RFC3339)},
			want: false,
		},
		{
			name: "created 15 minutes ago (under TTL)",
			ctx:  &beads.Issue{CreatedAt: now.Add(-15 * time.Minute).Format(time.RFC3339)},
			want: false,
		},
		{
			name: "created exactly TTL ago",
			ctx:  &beads.Issue{CreatedAt: now.Add(-ttl).Format(time.RFC3339)},
			want: false, // strictly older than TTL
		},
		{
			name: "created TTL+1s ago",
			ctx:  &beads.Issue{CreatedAt: now.Add(-ttl - time.Second).Format(time.RFC3339)},
			want: true,
		},
		{
			name: "created 2 hours ago",
			ctx:  &beads.Issue{CreatedAt: now.Add(-2 * time.Hour).Format(time.RFC3339)},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isContextOlderThan(tt.ctx, now, ttl)
			if got != tt.want {
				t.Errorf("isContextOlderThan(%+v) = %v, want %v", tt.ctx, got, tt.want)
			}
		})
	}
}

// TestIsDeferUntilExpired exercises the parser used by the auto-release pass
// (gu-0i09). The pass must (a) treat empty defer_until as "not deferred", (b)
// recognize both RFC3339 and RFC3339Nano formats since beads emits either, and
// (c) flip beads at-or-before now so a bead deferred to "now exactly" doesn't
// linger one tick longer than necessary.
func TestIsDeferUntilExpired(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		deferUntil  string
		wantExpired bool
		wantErr     bool
	}{
		{"empty", "", false, false},
		{"future RFC3339", now.Add(time.Hour).Format(time.RFC3339), false, false},
		{"past RFC3339", now.Add(-time.Hour).Format(time.RFC3339), true, false},
		{"exact now", now.Format(time.RFC3339), true, false},
		{"past RFC3339Nano", now.Add(-time.Minute).Format(time.RFC3339Nano), true, false},
		{"future RFC3339Nano", now.Add(time.Minute).Format(time.RFC3339Nano), false, false},
		{"unparseable", "not-a-timestamp", false, true},
		{"date-only no zone", "2026-05-30", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := isDeferUntilExpired(tt.deferUntil, now)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if got != tt.wantExpired {
				t.Errorf("expired = %v, want %v", got, tt.wantExpired)
			}
		})
	}
}

// TestIsScheduledWorkBeadReady_Deferred guards gs-o5f: the scheduler must not
// dispatch a scheduled bead whose defer_until is still in the future, even
// though `gt done --status DEFERRED` leaves it status=open. An expired or empty
// defer_until still dispatches; an unparseable one falls back to dispatchable.
func TestIsScheduledWorkBeadReady_Deferred(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	nowForDeferRelease = func() time.Time { return now }
	t.Cleanup(func() { nowForDeferRelease = nil })

	tests := []struct {
		name      string
		info      beadStatusInfo
		wantReady bool
	}{
		{"open no defer", beadStatusInfo{Status: "open"}, true},
		{"open future defer", beadStatusInfo{Status: "open", DeferUntil: now.Add(time.Hour).Format(time.RFC3339)}, false},
		{"open expired defer", beadStatusInfo{Status: "open", DeferUntil: now.Add(-time.Hour).Format(time.RFC3339)}, true},
		{"open unparseable defer", beadStatusInfo{Status: "open", DeferUntil: "not-a-timestamp"}, true},
		{"deferred status", beadStatusInfo{Status: "deferred", DeferUntil: now.Add(time.Hour).Format(time.RFC3339)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isScheduledWorkBeadReady("wb-1", tt.info, true, map[string]bool{})
			if got != tt.wantReady {
				t.Errorf("isScheduledWorkBeadReady(%+v) = %v, want %v", tt.info, got, tt.wantReady)
			}
		})
	}
}

func TestIsAlreadyDispatchedError(t *testing.T) {
	tests := []struct {
		name string
		err  string
		want bool
	}{
		{"already hooked", "already hooked (use --force to re-sling)", true},
		{"already in_progress", "already in_progress (use --force to re-sling)", true},
		{"already hooked bare", "already hooked", true},
		{"already in_progress bare", "already in_progress", true},
		{"spawn failure", "polecat spawn failed: timeout", false},
		{"rig parked", "rig parked", false},
		{"identity bead", "identity bead", false},
		{"empty error", "", false},
		{"contains but not prefix", "bead is already hooked to X", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fmt.Errorf("%s", tt.err)
			if got := isAlreadyDispatchedError(err); got != tt.want {
				t.Errorf("isAlreadyDispatchedError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

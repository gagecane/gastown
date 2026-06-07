package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestDispatchScanConcurrency guards the semaphore bound for the per-rig
// sling-context fan-out in listAllSlingContextRecords (gu-1h3ur). That scan runs
// on the dispatch hot path under scheduler-dispatch.lock; serial it was ~21s
// across 33 dirs and blew the heartbeat dispatch budget, stalling auto-dispatch.
// Defaults to 6; GT_DISPATCH_SCAN_FANOUT overrides with a positive int; junk /
// zero / negative fall back to the default so a fat-fingered env can never set
// an invalid width. 1 is allowed for an explicit serial fallback.
func TestDispatchScanConcurrency(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want int
	}{
		{"default when unset", "", 6},
		{"override 4", "4", 4},
		{"override 12", "12", 12},
		{"override 1 (serial allowed)", "1", 1},
		{"zero falls back", "0", 6},
		{"negative falls back", "-2", 6},
		{"junk falls back", "wide", 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env == "" {
				os.Unsetenv("GT_DISPATCH_SCAN_FANOUT")
			} else {
				t.Setenv("GT_DISPATCH_SCAN_FANOUT", tt.env)
			}
			if got := dispatchScanConcurrency(); got != tt.want {
				t.Errorf("dispatchScanConcurrency() with env=%q = %d, want %d", tt.env, got, tt.want)
			}
		})
	}
}

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
		// hq-9jeyo: reference/tripwire beads must never be dispatched.
		{"do-not-dispatch label", beadStatusInfo{Status: "open", Labels: []string{"do-not-dispatch"}}, false},
		{"pinned label", beadStatusInfo{Status: "open", Labels: []string{"pinned"}}, false},
		{"no-auto-dispatch label", beadStatusInfo{Status: "open", Labels: []string{"no-auto-dispatch"}}, false},
		{"no-auto-dispatch with human-investigation (gs-b2a)", beadStatusInfo{Status: "open", Labels: []string{"no-auto-dispatch", "human-investigation"}}, false},
		{"reference type", beadStatusInfo{Status: "open", Type: "reference"}, false},
		{"tripwire all three", beadStatusInfo{Status: "open", Type: "reference", Labels: []string{"do-not-dispatch", "pinned"}}, false},
		// gu-0l7he: operator-reserved beads must never auto-dispatch, even while
		// they stay OPEN in the human/mayor queue. These labels are honored by
		// executeSling and scheduleBead; the readiness scan must match so the
		// bead never reaches dispatch (no circuit-break churn).
		{"mayor-only label", beadStatusInfo{Status: "open", Labels: []string{"mayor-only"}}, false},
		{"no-polecat label", beadStatusInfo{Status: "open", Labels: []string{"no-polecat"}}, false},
		{"human-only label", beadStatusInfo{Status: "open", Labels: []string{"human-only"}}, false},
		// gu-ea25u: a source bead with an MR in flight (awaiting_refinery_merge)
		// must not be re-selected as a dispatch candidate. It stays open only for
		// the refinery's PostMerge close.
		{"awaiting_refinery_merge label", beadStatusInfo{Status: "open", Labels: []string{"awaiting_refinery_merge"}}, false},
		{"awaiting_refinery_merge among others", beadStatusInfo{Status: "open", Labels: []string{"bug", "awaiting_refinery_merge"}}, false},
		{"awaiting_refinery_recovery is NOT filtered here", beadStatusInfo{Status: "open", Labels: []string{"awaiting_refinery_recovery"}}, true},
		{"normal work with unrelated label still ready", beadStatusInfo{Status: "open", Labels: []string{"gt:rig"}}, true},
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

// TestIsAgentBeadInfo is the regression gate for gc-wbk1b / gu-k5sul: the
// dispatch readiness scan stopped fanning `bd ready` across every town dir and
// now identifies agent state beads (which must NEVER be dispatched as work —
// gu-7gm) directly from the targeted bd-show batch via isAgentBeadInfo. The old
// path detected them via the gt:agent label / issue_type=agent in bd-ready's
// output; this verifies the beadStatusInfo form recognizes the same signals so
// the guard swap (agentWorkIDs[...] -> isAgentBeadInfo(info)) is behavior-preserving.
func TestIsAgentBeadInfo(t *testing.T) {
	tests := []struct {
		name string
		info beadStatusInfo
		want bool
	}{
		{"gt:agent label (current standard)", beadStatusInfo{Status: "open", Labels: []string{"gt:agent"}}, true},
		{"legacy issue_type=agent", beadStatusInfo{Status: "open", Type: "agent"}, true},
		{"both label and type", beadStatusInfo{Type: "agent", Labels: []string{"gt:agent"}}, true},
		{"gt:agent among other labels", beadStatusInfo{Status: "open", Labels: []string{"gt:rig", "gt:agent", "foo"}}, true},
		{"normal work bead — not an agent", beadStatusInfo{Status: "open", Type: "task", Labels: []string{"gt:rig"}}, false},
		{"no labels, no type", beadStatusInfo{Status: "open"}, false},
		{"unrelated label only", beadStatusInfo{Status: "open", Labels: []string{"do-not-dispatch"}}, false},
		{"agent substring must not false-match", beadStatusInfo{Status: "open", Labels: []string{"my-agentish-label"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAgentBeadInfo(tt.info); got != tt.want {
				t.Errorf("isAgentBeadInfo(%+v) = %v, want %v", tt.info, got, tt.want)
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

// TestDispatchCloseEscalationArgs pins the gu-i0oaq escalation contract: the
// stranded-context double-dispatch risk escalates HIGH, is sourced to the
// dispatch-close path, and is fingerprinted PER WORK-BEAD so gt escalate's
// dedup collapses repeats for the same stranded context (not across beads).
func TestDispatchCloseEscalationArgs(t *testing.T) {
	closeErr := fmt.Errorf("issue not found")
	args := dispatchCloseEscalationArgs("ta-o19o", "ta-wisp-cl0", "talontriage", closeErr)

	joined := strings.Join(args, " ")
	want := map[string]string{
		"--severity":    "high",
		"--source":      "scheduler:dispatch-close",
		"--fingerprint": "dispatch-close:ta-o19o",
	}
	for flag, val := range want {
		idx := -1
		for i, a := range args {
			if a == flag {
				idx = i
				break
			}
		}
		if idx < 0 || idx+1 >= len(args) {
			t.Fatalf("flag %q missing from args: %q", flag, joined)
		}
		if args[idx+1] != val {
			t.Errorf("%s = %q, want %q", flag, args[idx+1], val)
		}
	}

	if args[0] != "gt" || args[1] != "escalate" {
		t.Errorf("command = %q %q, want gt escalate", args[0], args[1])
	}
	// The work bead, context, and underlying error must all surface for triage.
	for _, must := range []string{"ta-o19o", "ta-wisp-cl0", "issue not found", "double-dispatch"} {
		if !strings.Contains(joined, must) {
			t.Errorf("escalation args missing %q: %s", must, joined)
		}
	}

	// Fingerprint is keyed by work-bead, not context: a different bead must dedup
	// independently even if it strands the same kind of context.
	other := dispatchCloseEscalationArgs("ta-53is", "ta-wisp-4qh", "talontriage", closeErr)
	if strings.Join(other, " ") == joined {
		t.Error("distinct work beads produced identical escalation args; fingerprint not bead-scoped")
	}
}

// TestDispatchMaintenanceDue covers the gu-pjrz3 option-b gate: maintenance
// passes run at most once per interval (so the common dispatch tick skips the
// 4-pass per-rig fan-out that blew the 5m budget). Fail-open on first run / no
// stamp; defers within the interval; runs again after it elapses.
func TestDispatchMaintenanceDue(t *testing.T) {
	town := t.TempDir()
	base := time.Date(2026, 6, 4, 6, 0, 0, 0, time.UTC)
	clock := base
	timeNowForDispatchMaint = func() time.Time { return clock }
	t.Cleanup(func() { timeNowForDispatchMaint = time.Now })

	// First call: no stamp → fail-open → due (stamps `base`).
	if !dispatchMaintenanceDue(town) {
		t.Fatal("first call should be due (no stamp, fail-open)")
	}
	// Same instant: within interval → NOT due.
	if dispatchMaintenanceDue(town) {
		t.Fatal("call at same instant should NOT be due (stamped this tick)")
	}
	// Just under the interval → still NOT due.
	clock = base.Add(dispatchMaintenanceInterval - time.Second)
	if dispatchMaintenanceDue(town) {
		t.Fatal("call just under the interval should NOT be due")
	}
	// Past the interval → due again (and re-stamps at the new clock).
	clock = base.Add(dispatchMaintenanceInterval + time.Second)
	if !dispatchMaintenanceDue(town) {
		t.Fatal("call past the interval should be due")
	}
	// Immediately after that run → NOT due again (re-stamp took).
	if dispatchMaintenanceDue(town) {
		t.Fatal("call right after a due-run should NOT be due (re-stamped)")
	}
}

// TestFoldSlingContextDirResults guards the dispatch self-recovery contract
// (gu-tnmuj): the per-dir sling-context fan-out must distinguish a total Dolt
// outage (every dir errored) from a genuinely empty queue. A silent empty-on-
// error return is what wedged dispatch after a circuit-breaker outage — it
// looked like "no work" forever until a manual daemon restart.
func TestFoldSlingContextDirResults(t *testing.T) {
	ctxA := &beads.Issue{ID: "gu-ctx-a"}
	ctxB := &beads.Issue{ID: "gu-ctx-b"}
	errDolt := errors.New("circuit breaker is open")

	t.Run("all dirs failed returns error", func(t *testing.T) {
		_, err := foldSlingContextDirResults([]slingContextDirResult{
			{dir: "d1", err: errDolt},
			{dir: "d2", err: errDolt},
		})
		if err == nil {
			t.Fatal("expected error when every dir failed (Dolt-outage signature), got nil")
		}
		if !errors.Is(err, errDolt) {
			t.Fatalf("expected wrapped last error, got %v", err)
		}
	})

	t.Run("empty queue is not an error", func(t *testing.T) {
		records, err := foldSlingContextDirResults([]slingContextDirResult{
			{dir: "d1"},
			{dir: "d2"},
		})
		if err != nil {
			t.Fatalf("empty queue must not error (distinct from outage), got %v", err)
		}
		if len(records) != 0 {
			t.Fatalf("expected 0 records, got %d", len(records))
		}
	})

	t.Run("partial failure tolerated", func(t *testing.T) {
		records, err := foldSlingContextDirResults([]slingContextDirResult{
			{dir: "d1", contexts: []*beads.Issue{ctxA}},
			{dir: "d2", err: errDolt},
		})
		if err != nil {
			t.Fatalf("partial failure must not error (some dirs answered), got %v", err)
		}
		if len(records) != 1 || records[0].issue.ID != "gu-ctx-a" {
			t.Fatalf("expected 1 record gu-ctx-a, got %+v", records)
		}
	})

	t.Run("dedup by id across dirs", func(t *testing.T) {
		records, err := foldSlingContextDirResults([]slingContextDirResult{
			{dir: "d1", contexts: []*beads.Issue{ctxA, ctxB}},
			{dir: "d2", contexts: []*beads.Issue{ctxA}}, // prefix-routing duplicate
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(records) != 2 {
			t.Fatalf("expected 2 deduped records, got %d", len(records))
		}
	})

	t.Run("no dirs is not an error", func(t *testing.T) {
		records, err := foldSlingContextDirResults(nil)
		if err != nil {
			t.Fatalf("zero dirs must not error, got %v", err)
		}
		if len(records) != 0 {
			t.Fatalf("expected 0 records, got %d", len(records))
		}
	})
}

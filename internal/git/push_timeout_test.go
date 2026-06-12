package git

import (
	"strconv"
	"testing"
	"time"
)

// TestPushTimeout covers gu-s6wye: the push wall clock must clear the pre-push
// hook's OWN worst-case self-imposed duration (gate-slot wait + gate runtime),
// not a fixed 60s. A 60s wall SIGKILLed contended-but-healthy pushes mid-gate,
// before the branch reached origin, producing the misleading
// "verified_push_failed: branch <b> missing after push" / aa-pushed-no-mr strand.
func TestPushTimeout(t *testing.T) {
	tests := []struct {
		name             string
		pushTimeoutSecs  string // GT_PUSH_TIMEOUT_SECONDS
		gateSlotWaitSecs string // GT_GATE_SLOT_WAIT_SECONDS
		want             time.Duration
	}{
		{
			name: "default clears the hook's 600s gate-slot wait plus gate runtime",
			want: time.Duration(defaultGateSlotWaitSeconds)*time.Second + prePushGateRunBudget,
		},
		{
			name:             "tracks a custom gate-slot wait",
			gateSlotWaitSecs: "300",
			want:             300*time.Second + prePushGateRunBudget,
		},
		{
			name:            "explicit override wins over the computation",
			pushTimeoutSecs: "45",
			want:            45 * time.Second,
		},
		{
			name:             "explicit override wins even when a slot wait is set",
			pushTimeoutSecs:  "90",
			gateSlotWaitSecs: "600",
			want:             90 * time.Second,
		},
		{
			name:             "a tiny slot wait never drops below the base network floor",
			gateSlotWaitSecs: "1",
			// 1s + 120s budget = 121s, still above the 60s floor, so the floor
			// only bites if the budget itself were ever shrunk. Assert the
			// computed value to lock the relationship in.
			want: 1*time.Second + prePushGateRunBudget,
		},
		{
			name:            "non-numeric override falls back to the computed default",
			pushTimeoutSecs: "abc",
			want:            time.Duration(defaultGateSlotWaitSeconds)*time.Second + prePushGateRunBudget,
		},
		{
			name:            "zero/negative override is ignored",
			pushTimeoutSecs: "0",
			want:            time.Duration(defaultGateSlotWaitSeconds)*time.Second + prePushGateRunBudget,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv unsets after the test and forbids parallelism, so each
			// case sees a clean env for the two knobs.
			t.Setenv("GT_PUSH_TIMEOUT_SECONDS", tt.pushTimeoutSecs)
			t.Setenv("GT_GATE_SLOT_WAIT_SECONDS", tt.gateSlotWaitSecs)

			if got := pushTimeout(); got != tt.want {
				t.Fatalf("pushTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPushTimeoutExceedsGateSlotWait is the core regression guard for gu-s6wye:
// whatever the configured gate-slot wait, the push timeout must strictly exceed
// it, or the push is killed before the hook even starts its gates.
func TestPushTimeoutExceedsGateSlotWait(t *testing.T) {
	for _, slotWait := range []int{60, 300, 600, 900} {
		t.Setenv("GT_PUSH_TIMEOUT_SECONDS", "")
		t.Setenv("GT_GATE_SLOT_WAIT_SECONDS", strconv.Itoa(slotWait))

		got := pushTimeout()
		floor := time.Duration(slotWait) * time.Second
		if got <= floor {
			t.Fatalf("pushTimeout()=%v must exceed gate-slot wait %v (else the push is SIGKILLed mid-gate-wait)", got, floor)
		}
	}
}

package upstreamsync

import (
	"testing"
	"time"
)

// fixedNow returns a deterministic timestamp for table tests.
func fixedNow() time.Time {
	t, _ := time.Parse(time.RFC3339, "2026-05-29T12:00:00Z")
	return t
}

// rfc3339 helper for building timestamps relative to fixedNow.
func rfc3339(offset time.Duration) string {
	return fixedNow().Add(offset).UTC().Format(time.RFC3339)
}

func TestIsDue_Paused(t *testing.T) {
	state := SyncStateMetadata{State: StatePaused}
	got := IsDue(state, 6*time.Hour, DefaultCooldownPolicy(), fixedNow())
	if got.Due {
		t.Fatalf("paused rig should never be Due; got %+v", got)
	}
	if got.SkipReason != "paused" {
		t.Errorf("SkipReason = %q, want paused", got.SkipReason)
	}
}

func TestIsDue_Busy(t *testing.T) {
	for _, st := range []SyncState{StateChecking, StateSyncing, StateResolving, StateGating, StatePushing} {
		state := SyncStateMetadata{State: st}
		got := IsDue(state, 6*time.Hour, DefaultCooldownPolicy(), fixedNow())
		if got.Due {
			t.Errorf("state=%s should not be Due", st)
		}
		if got.SkipReason == "" {
			t.Errorf("state=%s should set SkipReason", st)
		}
	}
}

func TestIsDue_FirstRun(t *testing.T) {
	// No attempts → always Due.
	state := SyncStateMetadata{State: StateIdle}
	got := IsDue(state, 6*time.Hour, DefaultCooldownPolicy(), fixedNow())
	if !got.Due {
		t.Fatalf("first-run rig should be Due; got %+v", got)
	}
	if got.EffectiveCadence <= 0 {
		t.Errorf("EffectiveCadence should be positive; got %v", got.EffectiveCadence)
	}
}

func TestIsDue_FailedStateRespected(t *testing.T) {
	// StateFailed is treated as eligible for retry (operator hasn't
	// paused it; deacon may try again on the next tick once cooldown
	// elapses).
	state := SyncStateMetadata{
		State: StateFailed,
		Attempts: []SyncAttempt{
			{Outcome: "gate-failure", CompletedAt: rfc3339(-7 * time.Hour)},
		},
		ConsecutiveFailures: 1,
	}
	got := IsDue(state, 6*time.Hour, DefaultCooldownPolicy(), fixedNow())
	if !got.Due {
		t.Fatalf("Failed rig past cadence should be Due; got %+v", got)
	}
}

func TestIsDue_WithinCooldown(t *testing.T) {
	// Last successful sync 2h ago; cadence 6h → not Due.
	state := SyncStateMetadata{
		State: StateIdle,
		Attempts: []SyncAttempt{
			{
				Outcome:     "success",
				CompletedAt: rfc3339(-2 * time.Hour),
				PreSyncSHA:  "aaa",
				PostSyncSHA: "aaa", // no movement -> not "hot"
			},
		},
	}
	got := IsDue(state, 6*time.Hour, DefaultCooldownPolicy(), fixedNow())
	if got.Due {
		t.Fatalf("rig within cooldown should not be Due; got %+v", got)
	}
	if got.SkipReason == "" {
		t.Errorf("expected SkipReason, got empty")
	}
	// EffectiveCadence ≈ base (no hot, no dormant, no failure).
	if got.EffectiveCadence != 6*time.Hour {
		t.Errorf("EffectiveCadence = %v, want 6h (base)", got.EffectiveCadence)
	}
}

func TestIsDue_ElapsedCooldown(t *testing.T) {
	// Last successful sync 7h ago; cadence 6h → Due.
	state := SyncStateMetadata{
		State: StateIdle,
		Attempts: []SyncAttempt{
			{
				Outcome:     "success",
				CompletedAt: rfc3339(-7 * time.Hour),
				PreSyncSHA:  "aaa",
				PostSyncSHA: "aaa",
			},
		},
	}
	got := IsDue(state, 6*time.Hour, DefaultCooldownPolicy(), fixedNow())
	if !got.Due {
		t.Fatalf("rig past cadence should be Due; got %+v", got)
	}
}

func TestIsDue_HotShortensCadence(t *testing.T) {
	// Last attempt moved HEAD → "hot" → cadence shortened.
	// Default HotMultiplier = 2/3 → 6h * 2/3 = 4h.
	// Last attempt 5h ago → past 4h cadence → Due.
	state := SyncStateMetadata{
		State: StateIdle,
		Attempts: []SyncAttempt{
			{
				Outcome:     "success",
				CompletedAt: rfc3339(-5 * time.Hour),
				PreSyncSHA:  "aaa",
				PostSyncSHA: "bbb", // moved → hot
			},
		},
	}
	got := IsDue(state, 6*time.Hour, DefaultCooldownPolicy(), fixedNow())
	if !got.Due {
		t.Fatalf("hot rig past shortened cadence should be Due; got %+v", got)
	}
	// EffectiveCadence should be < base.
	if got.EffectiveCadence >= 6*time.Hour {
		t.Errorf("hot EffectiveCadence = %v, want < 6h", got.EffectiveCadence)
	}
}

func TestIsDue_DormantLengthensCadence(t *testing.T) {
	// Last attempt was "skipped" → dormant → cadence stretched 1.5x.
	// 6h * 1.5 = 9h. Last attempt 8h ago → not Due.
	state := SyncStateMetadata{
		State: StateIdle,
		Attempts: []SyncAttempt{
			{
				Outcome:     "skipped",
				CompletedAt: rfc3339(-8 * time.Hour),
			},
		},
	}
	got := IsDue(state, 6*time.Hour, DefaultCooldownPolicy(), fixedNow())
	if got.Due {
		t.Fatalf("dormant rig within stretched cadence should not be Due; got %+v", got)
	}
	if got.EffectiveCadence <= 6*time.Hour {
		t.Errorf("dormant EffectiveCadence = %v, want > 6h", got.EffectiveCadence)
	}
}

func TestIsDue_FailureBackoff(t *testing.T) {
	// 3 consecutive failures → exponent=2, factor=4, cadence=24h.
	// Last attempt 10h ago → not Due.
	state := SyncStateMetadata{
		State: StateFailed,
		Attempts: []SyncAttempt{
			{Outcome: "gate-failure", CompletedAt: rfc3339(-10 * time.Hour)},
		},
		ConsecutiveFailures: 3,
	}
	got := IsDue(state, 6*time.Hour, DefaultCooldownPolicy(), fixedNow())
	if got.Due {
		t.Fatalf("rig with failure backoff should not be Due at 10h; got %+v", got)
	}
}

func TestIsDue_FailureBackoffClamped(t *testing.T) {
	// Massive failure count would yield enormous cadence; the ceiling
	// (4*base = 24h) clamps it.
	state := SyncStateMetadata{
		State: StateFailed,
		Attempts: []SyncAttempt{
			{Outcome: "gate-failure", CompletedAt: rfc3339(-25 * time.Hour)},
		},
		ConsecutiveFailures: 100,
	}
	got := IsDue(state, 6*time.Hour, DefaultCooldownPolicy(), fixedNow())
	if !got.Due {
		t.Fatalf("rig past clamped ceiling (24h) should be Due at 25h; got %+v", got)
	}
	// Effective cadence must be clamped to 4*base.
	if got.EffectiveCadence > 24*time.Hour {
		t.Errorf("EffectiveCadence = %v, want <= 24h (4*base ceiling)", got.EffectiveCadence)
	}
}

func TestIsDue_HotClampedToFloor(t *testing.T) {
	// Tight floor: hot multiplier shouldn't push below MinCadence.
	state := SyncStateMetadata{
		State: StateIdle,
		Attempts: []SyncAttempt{
			{
				Outcome:     "success",
				CompletedAt: rfc3339(-2 * time.Hour),
				PreSyncSHA:  "aaa",
				PostSyncSHA: "bbb", // hot
			},
		},
	}
	policy := DefaultCooldownPolicy()
	policy.MinCadence = 5 * time.Hour
	policy.MaxCadence = 24 * time.Hour
	got := IsDue(state, 6*time.Hour, policy, fixedNow())
	if got.EffectiveCadence < 5*time.Hour {
		t.Errorf("EffectiveCadence = %v, want >= MinCadence (5h)", got.EffectiveCadence)
	}
}

func TestIsDue_MalformedTimestampDefaultsDue(t *testing.T) {
	// Malformed CompletedAt → robustness path: rig is Due (don't wedge
	// on bad bookkeeping).
	state := SyncStateMetadata{
		State: StateIdle,
		Attempts: []SyncAttempt{
			{Outcome: "success", CompletedAt: "not-a-real-timestamp"},
		},
	}
	got := IsDue(state, 6*time.Hour, DefaultCooldownPolicy(), fixedNow())
	if !got.Due {
		t.Fatalf("malformed timestamp should default to Due (robustness); got %+v", got)
	}
}

func TestIsDue_AttemptsWithoutCompletedAtIgnored(t *testing.T) {
	// In-progress attempt (no CompletedAt) should not gate the
	// cooldown — only completed attempts count.
	state := SyncStateMetadata{
		State: StateIdle,
		Attempts: []SyncAttempt{
			{Outcome: "success", CompletedAt: rfc3339(-7 * time.Hour)},
			{Outcome: "", CompletedAt: ""}, // in-progress, last in slice
		},
	}
	got := IsDue(state, 6*time.Hour, DefaultCooldownPolicy(), fixedNow())
	if !got.Due {
		t.Fatalf("rig past cadence (with in-progress placeholder) should be Due; got %+v", got)
	}
}

func TestDefaultCooldownPolicy(t *testing.T) {
	p := DefaultCooldownPolicy()
	if p.FailureBackoffFactor != 2.0 {
		t.Errorf("FailureBackoffFactor = %v, want 2.0", p.FailureBackoffFactor)
	}
	if p.DormantMultiplier != 1.5 {
		t.Errorf("DormantMultiplier = %v, want 1.5", p.DormantMultiplier)
	}
	if p.HotCommitThreshold != 3 {
		t.Errorf("HotCommitThreshold = %v, want 3", p.HotCommitThreshold)
	}
}

func TestPolicyResolveBounds(t *testing.T) {
	tests := []struct {
		name    string
		policy  CooldownPolicy
		base    time.Duration
		wantMin time.Duration
		wantMax time.Duration
	}{
		{
			name:    "defaults from base",
			policy:  CooldownPolicy{},
			base:    6 * time.Hour,
			wantMin: 90 * time.Minute, // 6h/4
			wantMax: 24 * time.Hour,   // 6h*4
		},
		{
			name:    "explicit overrides honored",
			policy:  CooldownPolicy{MinCadence: 30 * time.Minute, MaxCadence: 12 * time.Hour},
			base:    6 * time.Hour,
			wantMin: 30 * time.Minute,
			wantMax: 12 * time.Hour,
		},
		{
			name:    "min > max is clamped to max",
			policy:  CooldownPolicy{MinCadence: 48 * time.Hour, MaxCadence: 12 * time.Hour},
			base:    6 * time.Hour,
			wantMin: 12 * time.Hour,
			wantMax: 12 * time.Hour,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			min, max := tt.policy.resolveBounds(tt.base)
			if min != tt.wantMin {
				t.Errorf("min = %v, want %v", min, tt.wantMin)
			}
			if max != tt.wantMax {
				t.Errorf("max = %v, want %v", max, tt.wantMax)
			}
		})
	}
}

func TestCommitsCaughtUp(t *testing.T) {
	tests := []struct {
		name string
		a    *SyncAttempt
		want int
	}{
		{"nil", nil, 0},
		{"empty SHAs", &SyncAttempt{}, 0},
		{"unchanged HEAD", &SyncAttempt{PreSyncSHA: "aaa", PostSyncSHA: "aaa"}, 0},
		{"moved HEAD", &SyncAttempt{PreSyncSHA: "aaa", PostSyncSHA: "bbb"}, 999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commitsCaughtUp(tt.a); got != tt.want {
				t.Errorf("commitsCaughtUp() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPow(t *testing.T) {
	tests := []struct {
		base float64
		exp  int
		want float64
	}{
		{2, 0, 1},
		{2, 1, 2},
		{2, 3, 8},
		{1.5, 2, 2.25},
		{2, -1, 1},
	}
	for _, tt := range tests {
		got := pow(tt.base, tt.exp)
		if got != tt.want {
			t.Errorf("pow(%v, %d) = %v, want %v", tt.base, tt.exp, got, tt.want)
		}
	}
}

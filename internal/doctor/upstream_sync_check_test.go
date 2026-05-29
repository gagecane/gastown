package doctor

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/upstreamsync"
)

func TestNewUpstreamSyncCheck_Metadata(t *testing.T) {
	c := NewUpstreamSyncCheck()
	if c.Name() != "upstream-sync-health" {
		t.Errorf("Name() = %q, want upstream-sync-health", c.Name())
	}
	if c.Category() != CategoryRig {
		t.Errorf("Category() = %q, want %q", c.Category(), CategoryRig)
	}
	if c.CanFix() {
		t.Errorf("CanFix() = true, want false (read-only check)")
	}
}

func TestUpstreamSyncCheck_Run_NoRigName_Skips(t *testing.T) {
	c := NewUpstreamSyncCheck()
	got := c.Run(&CheckContext{TownRoot: "/tmp", RigName: ""})
	if got.Status != StatusOK {
		t.Errorf("status = %v, want OK", got.Status)
	}
	if !strings.Contains(got.Message, "skipped") {
		t.Errorf("expected 'skipped' message; got %q", got.Message)
	}
}

func TestAssessUpstreamSyncState_Healthy(t *testing.T) {
	state := upstreamsync.SyncStateMetadata{
		Rig:                 "gastown_upstream",
		State:               upstreamsync.StateIdle,
		LastSyncAt:          "2026-05-29T20:00:00Z",
		ConsecutiveFailures: 0,
	}
	got := assessUpstreamSyncState("upstream-sync-health", state, &config.UpstreamSyncConfig{})
	if got.Status != StatusOK {
		t.Errorf("status = %v, want OK; details=%v", got.Status, got.Details)
	}
	if !strings.Contains(got.Message, "healthy") {
		t.Errorf("expected 'healthy' in message; got %q", got.Message)
	}
}

func TestAssessUpstreamSyncState_PausedWithoutReason_Warns(t *testing.T) {
	state := upstreamsync.SyncStateMetadata{
		Rig:         "gastown_upstream",
		State:       upstreamsync.StatePaused,
		PauseReason: "",
	}
	got := assessUpstreamSyncState("upstream-sync-health", state, nil)
	if got.Status != StatusWarning {
		t.Errorf("status = %v, want Warning", got.Status)
	}
	joined := strings.Join(got.Details, " ")
	if !strings.Contains(joined, "no PauseReason") {
		t.Errorf("expected paused-no-reason detail; got %v", got.Details)
	}
}

func TestAssessUpstreamSyncState_PausedWithReason_OK(t *testing.T) {
	state := upstreamsync.SyncStateMetadata{
		Rig:         "gastown_upstream",
		State:       upstreamsync.StatePaused,
		PauseReason: "operator paused: investigating",
	}
	got := assessUpstreamSyncState("upstream-sync-health", state, nil)
	if got.Status != StatusOK {
		t.Errorf("paused with recorded reason should be OK; got %v with details %v",
			got.Status, got.Details)
	}
}

func TestAssessUpstreamSyncState_BreakerThresholdMissedTransition_Errors(t *testing.T) {
	state := upstreamsync.SyncStateMetadata{
		Rig:                 "gastown_upstream",
		State:               upstreamsync.StateIdle, // SHOULD be paused at this count
		ConsecutiveFailures: 3,
	}
	got := assessUpstreamSyncState("upstream-sync-health", state, &config.UpstreamSyncConfig{})
	if got.Status != StatusError {
		t.Errorf("status = %v, want Error", got.Status)
	}
	joined := strings.Join(got.Details, " ")
	if !strings.Contains(joined, "ConsecutiveFailures=3") {
		t.Errorf("expected breaker detail; got %v", got.Details)
	}
}

func TestAssessUpstreamSyncState_BreakerAtThresholdAndPaused_OK(t *testing.T) {
	state := upstreamsync.SyncStateMetadata{
		Rig:                 "gastown_upstream",
		State:               upstreamsync.StatePaused,
		PauseReason:         "auto-paused: 3 consecutive failures",
		ConsecutiveFailures: 3,
	}
	got := assessUpstreamSyncState("upstream-sync-health", state, &config.UpstreamSyncConfig{})
	if got.Status != StatusOK {
		t.Errorf("breaker tripped + paused with reason should be OK; got %v: %v",
			got.Status, got.Details)
	}
}

func TestAssessUpstreamSyncState_OrphanDispatchContext_Warns(t *testing.T) {
	cases := []struct {
		name   string
		branch string
		bead   string
	}{
		{"branch-without-bead", "polecat/x/gu-resolve", ""},
		{"bead-without-branch", "", "gu-conflict-001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := upstreamsync.SyncStateMetadata{
				Rig:   "gastown_upstream",
				State: upstreamsync.StateResolving,
				CurrentAttempt: &upstreamsync.CurrentAttempt{
					ID:               "att-1",
					ResolutionBranch: tc.branch,
					PolecatBead:      tc.bead,
				},
			}
			got := assessUpstreamSyncState("upstream-sync-health", state, nil)
			if got.Status != StatusWarning {
				t.Errorf("expected Warning for half-set dispatch context; got %v", got.Status)
			}
			joined := strings.Join(got.Details, " ")
			if !strings.Contains(joined, "half-set dispatch context") {
				t.Errorf("expected orphan-dispatch detail; got %v", got.Details)
			}
		})
	}
}

func TestAssessUpstreamSyncState_FullDispatchContext_OK(t *testing.T) {
	state := upstreamsync.SyncStateMetadata{
		Rig:   "gastown_upstream",
		State: upstreamsync.StateResolving,
		CurrentAttempt: &upstreamsync.CurrentAttempt{
			ID:               "att-1",
			ResolutionBranch: "polecat/x/gu-resolve",
			PolecatBead:      "gu-conflict-001",
		},
	}
	got := assessUpstreamSyncState("upstream-sync-health", state, nil)
	if got.Status != StatusOK {
		t.Errorf("complete dispatch context should be OK; got %v: %v",
			got.Status, got.Details)
	}
}

func TestAssessUpstreamSyncState_RespectsCustomThreshold(t *testing.T) {
	cfg := &config.UpstreamSyncConfig{
		MaxConsecutiveFailures: 5,
	}
	// 3 failures + custom threshold 5 → not yet over the breaker line.
	state := upstreamsync.SyncStateMetadata{
		Rig:                 "gastown_upstream",
		State:               upstreamsync.StateIdle,
		ConsecutiveFailures: 3,
	}
	got := assessUpstreamSyncState("upstream-sync-health", state, cfg)
	if got.Status != StatusOK {
		t.Errorf("3 failures with threshold=5 should be OK; got %v: %v",
			got.Status, got.Details)
	}

	// 5 failures + custom threshold 5 → over the line, still in idle.
	state.ConsecutiveFailures = 5
	got = assessUpstreamSyncState("upstream-sync-health", state, cfg)
	if got.Status != StatusError {
		t.Errorf("5 failures with threshold=5, idle, should be Error; got %v",
			got.Status)
	}
}

func TestDeriveRigPrefix(t *testing.T) {
	// Behavior must mirror cmd.resolveRigPrefix: multi-word names take
	// the first letter of each part and truncate to 2 chars; single-
	// word names take the first 2 chars verbatim. The truncation rule
	// is what makes "gastown_upstream" → "gu" instead of "gu" alone.
	cases := map[string]string{
		"gastown_upstream": "gu",
		"gastown":          "ga",
		"a_b_c":            "ab", // multi-word → first-of-each, truncated to 2
		"x":                "x",
	}
	for in, want := range cases {
		got := deriveRigPrefix(in)
		if got != want {
			t.Errorf("deriveRigPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBumpStatus(t *testing.T) {
	if got := bumpStatus(StatusOK, StatusWarning); got != StatusWarning {
		t.Errorf("bumpStatus(OK, Warning) = %v, want Warning", got)
	}
	if got := bumpStatus(StatusError, StatusWarning); got != StatusError {
		t.Errorf("bumpStatus(Error, Warning) = %v, want Error", got)
	}
	if got := bumpStatus(StatusWarning, StatusError); got != StatusError {
		t.Errorf("bumpStatus(Warning, Error) = %v, want Error", got)
	}
}

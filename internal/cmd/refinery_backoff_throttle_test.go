package cmd

import (
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/rig"
)

// withThrottleSeams swaps the load and queue-depth seams for the duration of a
// test and restores them afterward.
func withThrottleSeams(t *testing.T, loadPerCore float64, depth int, depthErr error) {
	t.Helper()
	origLoad := refineryBackoffLoadPerCore
	origDepth := refineryQueueDepth
	refineryBackoffLoadPerCore = func() float64 { return loadPerCore }
	refineryQueueDepth = func(*rig.Rig) (int, error) { return depth, depthErr }
	t.Cleanup(func() {
		refineryBackoffLoadPerCore = origLoad
		refineryQueueDepth = origDepth
	})
}

// settingsWith builds a RigSettings whose Polecat block carries the given
// throttle config. enabled=nil leaves the flag absent (off).
func settingsWith(enabled *bool, threshold *float64) *config.RigSettings {
	return &config.RigSettings{
		Polecat: &config.PolecatPoolConfig{
			PauseOnRefineryBackoff:     enabled,
			RefineryBackoffLoadPerCore: threshold,
		},
	}
}

func boolPtr(b bool) *bool        { return &b }
func floatPtr(f float64) *float64 { return &f }

func TestCheckRefineryBackoffThrottle(t *testing.T) {
	r := &rig.Rig{Name: "casc_cdk"}

	tests := []struct {
		name        string
		settings    *config.RigSettings
		loadPerCore float64
		depth       int
		depthErr    error
		wantBlock   bool
	}{
		{
			name:        "disabled — never throttles even under deadlock conditions",
			settings:    settingsWith(nil, nil),
			loadPerCore: 5.0,
			depth:       3,
			wantBlock:   false,
		},
		{
			name:        "enabled, high load + queued MRs — throttles (the deadlock)",
			settings:    settingsWith(boolPtr(true), nil),
			loadPerCore: 2.0, // > default 1.0
			depth:       3,
			wantBlock:   true,
		},
		{
			name:        "enabled, high load but empty queue — does not throttle",
			settings:    settingsWith(boolPtr(true), nil),
			loadPerCore: 2.0,
			depth:       0,
			wantBlock:   false,
		},
		{
			name:        "enabled, queued MRs but low load — does not throttle",
			settings:    settingsWith(boolPtr(true), nil),
			loadPerCore: 0.5, // < default 1.0
			depth:       3,
			wantBlock:   false,
		},
		{
			name:        "enabled, load exactly at threshold — does not throttle (strictly greater required)",
			settings:    settingsWith(boolPtr(true), nil),
			loadPerCore: 1.0,
			depth:       3,
			wantBlock:   false,
		},
		{
			name:        "enabled, custom lower threshold crossed — throttles",
			settings:    settingsWith(boolPtr(true), floatPtr(0.4)),
			loadPerCore: 0.5,
			depth:       1,
			wantBlock:   true,
		},
		{
			name:        "enabled, queue query errors — fails open (does not throttle)",
			settings:    settingsWith(boolPtr(true), nil),
			loadPerCore: 2.0,
			depth:       0,
			depthErr:    errors.New("beads unreachable"),
			wantBlock:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withThrottleSeams(t, tt.loadPerCore, tt.depth, tt.depthErr)
			err := checkRefineryBackoffThrottle(r, tt.settings)
			if tt.wantBlock && err == nil {
				t.Fatalf("expected throttle (non-nil error), got nil")
			}
			if !tt.wantBlock && err != nil {
				t.Fatalf("expected no throttle, got: %v", err)
			}
		})
	}
}

// TestCheckRefineryBackoffThrottle_NilSettings ensures the throttle is a no-op
// when settings or the polecat block is absent (default-off, backward compat).
func TestCheckRefineryBackoffThrottle_NilSettings(t *testing.T) {
	withThrottleSeams(t, 9.0, 9, nil) // deadlock conditions
	r := &rig.Rig{Name: "casc_cdk"}

	if err := checkRefineryBackoffThrottle(r, nil); err != nil {
		t.Fatalf("nil settings should never throttle, got: %v", err)
	}
	if err := checkRefineryBackoffThrottle(r, &config.RigSettings{}); err != nil {
		t.Fatalf("absent polecat block should never throttle, got: %v", err)
	}
}

package daemon

import (
	"testing"
	"time"
)

func TestBranchSyncInterval(t *testing.T) {
	// Default
	if got := branchSyncInterval(nil); got != defaultBranchSyncInterval {
		t.Errorf("expected default %v, got %v", defaultBranchSyncInterval, got)
	}

	// Custom
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			BranchSync: &BranchSyncConfig{
				Enabled:     true,
				IntervalStr: "6h",
			},
		},
	}
	if got := branchSyncInterval(config); got != 6*time.Hour {
		t.Errorf("expected 6h, got %v", got)
	}

	// Invalid falls back to default
	config.Patrols.BranchSync.IntervalStr = "nope"
	if got := branchSyncInterval(config); got != defaultBranchSyncInterval {
		t.Errorf("expected default for invalid, got %v", got)
	}
}

func TestIsPatrolEnabled_BranchSync(t *testing.T) {
	// Opt-in: disabled with nil config
	if IsPatrolEnabled(nil, "branch_sync") {
		t.Error("expected branch_sync to be disabled with nil config")
	}

	// Disabled when patrols section exists but BranchSync is nil
	config := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if IsPatrolEnabled(config, "branch_sync") {
		t.Error("expected branch_sync to be disabled by default")
	}

	// Explicitly enabled
	config.Patrols.BranchSync = &BranchSyncConfig{Enabled: true}
	if !IsPatrolEnabled(config, "branch_sync") {
		t.Error("expected branch_sync to be enabled when configured")
	}

	// Explicitly disabled
	config.Patrols.BranchSync = &BranchSyncConfig{Enabled: false}
	if IsPatrolEnabled(config, "branch_sync") {
		t.Error("expected branch_sync to be disabled when explicitly disabled")
	}
}

func TestBranchSyncTargetResolvedFrom(t *testing.T) {
	cases := []struct {
		name string
		from string
		want string
	}{
		{"empty defaults to main", "", "main"},
		{"whitespace defaults to main", "   ", "main"},
		{"strips origin prefix", "origin/main", "main"},
		{"plain branch kept", "develop", "develop"},
		{"strips origin prefix from non-main", "origin/develop", "develop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tgt := BranchSyncTarget{From: tc.from}
			if got := tgt.resolvedFrom(); got != tc.want {
				t.Errorf("resolvedFrom(%q) = %q, want %q", tc.from, got, tc.want)
			}
		})
	}
}

func TestBranchSyncSignatureLabel(t *testing.T) {
	tgt := BranchSyncTarget{Rig: "lia_bac", Branch: "gagecane/gt"}
	got := branchSyncSignatureLabel(tgt, "main")
	want := "branch-sync:lia_bac:gagecane-gt-from-main"
	if got != want {
		t.Errorf("signature label = %q, want %q", got, want)
	}
	// Signature must be slash-free so it is a valid bead label.
	if containsSlash(got) {
		t.Errorf("signature label %q contains a slash", got)
	}
}

func containsSlash(s string) bool {
	for _, r := range s {
		if r == '/' {
			return true
		}
	}
	return false
}

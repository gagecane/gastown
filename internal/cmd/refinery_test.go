package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/rig"
)

func TestRefineryStartAgentFlag(t *testing.T) {
	flag := refineryStartCmd.Flags().Lookup("agent")
	if flag == nil {
		t.Fatal("expected refinery start to define --agent flag")
	}
	if flag.DefValue != "" {
		t.Errorf("expected default agent override to be empty, got %q", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "overrides town default") {
		t.Errorf("expected --agent usage to mention overrides town default, got %q", flag.Usage)
	}
}

func TestRefineryAttachAgentFlag(t *testing.T) {
	flag := refineryAttachCmd.Flags().Lookup("agent")
	if flag == nil {
		t.Fatal("expected refinery attach to define --agent flag")
	}
	if flag.DefValue != "" {
		t.Errorf("expected default agent override to be empty, got %q", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "overrides town default") {
		t.Errorf("expected --agent usage to mention overrides town default, got %q", flag.Usage)
	}
}

func TestRefineryRestartAgentFlag(t *testing.T) {
	flag := refineryRestartCmd.Flags().Lookup("agent")
	if flag == nil {
		t.Fatal("expected refinery restart to define --agent flag")
	}
	if flag.DefValue != "" {
		t.Errorf("expected default agent override to be empty, got %q", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "overrides town default") {
		t.Errorf("expected --agent usage to mention overrides town default, got %q", flag.Usage)
	}
}

// TestRefineryOperatorStopHelpers_RoundTrip verifies the set/clear helpers
// persist and remove the operator-stop flag in the rig's wisp config.
// Regression guard for gu-8ug1: without the flag, the daemon auto-restarted
// operator-stopped refineries and re-triggered git-auth-failed escalations
// every heartbeat until a human ran `mwinit -o`.
func TestRefineryOperatorStopHelpers_RoundTrip(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	rigPath := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{Name: rigName, Path: rigPath}

	// Baseline.
	if rig.IsRefineryStoppedByOperator(townRoot, rigName) {
		t.Fatal("expected baseline not-stopped for fresh rig")
	}

	// Set flag via cmd helper; daemon-side check must observe it.
	setRefineryOperatorStop(r, rigName)
	if !rig.IsRefineryStoppedByOperator(townRoot, rigName) {
		t.Fatal("setRefineryOperatorStop did not persist the flag")
	}

	// Clear flag via cmd helper; daemon-side check must stop observing it.
	clearRefineryOperatorStop(r, rigName)
	if rig.IsRefineryStoppedByOperator(townRoot, rigName) {
		t.Fatal("clearRefineryOperatorStop did not remove the flag")
	}
}

// TestRefineryOperatorStopHelpers_NilRig verifies the helpers are safe to
// call with a nil *rig.Rig — this matches the defensive pattern used in
// other cmd helpers where early-return paths might hand back a nil rig
// alongside a non-nil error.
func TestRefineryOperatorStopHelpers_NilRig(t *testing.T) {
	// Must not panic. No assertion needed beyond "didn't crash".
	setRefineryOperatorStop(nil, "whatever")
	clearRefineryOperatorStop(nil, "whatever")
}

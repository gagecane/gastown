package cmd

import (
	"encoding/json"
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

// TestRefineryStatusOutput_OperatorStoppedField guards the JSON contract
// surfaced by `gt refinery status --json`. Automation (deacon patrol) and
// operators both rely on the `operator_stopped` key to distinguish
// intentionally-stopped refineries from unresponsive/crashed ones. If this
// field ever disappears or is renamed silently, the deacon patrol reverts
// to the gu-i1z2 bug where it "heals" operator-stopped refineries by
// running `gt refinery restart`, re-triggering the SSH-cert escalation
// loop that gu-8ug1 fixed.
//
// Intentionally tests the public JSON shape, not the private Go field:
// downstream consumers (scripts, formulas) parse the JSON and depend on
// the stable key name.
func TestRefineryStatusOutput_OperatorStoppedField(t *testing.T) {
	// Case 1: flag NOT set → JSON key present and false.
	notStopped := RefineryStatusOutput{
		Running:         false,
		RigName:         "foo",
		QueueLength:     0,
		OperatorStopped: false,
	}
	data, err := json.MarshalIndent(notStopped, "", "  ")
	if err != nil {
		t.Fatalf("marshal not-stopped: %v", err)
	}
	if !strings.Contains(string(data), `"operator_stopped": false`) {
		t.Errorf("expected JSON to contain \"operator_stopped\": false, got:\n%s", data)
	}

	// Case 2: flag set → JSON key present and true. This is the signal the
	// deacon patrol keys off of to skip restart.
	stopped := RefineryStatusOutput{
		Running:         false,
		RigName:         "foo",
		QueueLength:     0,
		OperatorStopped: true,
	}
	data, err = json.MarshalIndent(stopped, "", "  ")
	if err != nil {
		t.Fatalf("marshal stopped: %v", err)
	}
	if !strings.Contains(string(data), `"operator_stopped": true`) {
		t.Errorf("expected JSON to contain \"operator_stopped\": true, got:\n%s", data)
	}

	// Case 3: key must be present even when field is false (omitempty would
	// be a subtle regression that makes the deacon's existence check flaky
	// because "key absent" and "key=false" would look different to a shell
	// `jq` script that pipes the field through truthiness).
	if !strings.Contains(string(data), `"operator_stopped"`) {
		t.Errorf("operator_stopped must always be present in JSON output, got:\n%s", data)
	}
}

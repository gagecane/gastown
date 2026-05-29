package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestPrintCheckDueTable verifies that the human-readable formatter
// renders without crashing and includes the expected column headings
// and per-row data. The formatting itself is best-effort tabwriter; we
// only assert on contents that operators actually rely on (the rig
// name, the verdict, the reason).
func TestPrintCheckDueTable(t *testing.T) {
	decisions := []CheckDueDecision{
		{
			Rig:                 "gastown_upstream",
			Enabled:             true,
			Provisioned:         true,
			Due:                 true,
			State:               "idle",
			EffectiveCadenceSec: 21600,
		},
		{
			Rig:                 "other_rig",
			Enabled:             true,
			Provisioned:         true,
			Due:                 false,
			State:               "idle",
			SkipReason:          "cooldown:3h0m remaining",
			EffectiveCadenceSec: 21600,
			NextDueAt:           "2026-05-29T15:00:00Z",
		},
		{
			Rig:        "disabled_rig",
			Enabled:    false,
			SkipReason: "disabled",
		},
		{
			Rig:                 "invoked_rig",
			Enabled:             true,
			Provisioned:         true,
			Due:                 true,
			State:               "idle",
			Invoked:             true,
			EffectiveCadenceSec: 21600,
		},
		{
			Rig:                 "invoke_failed_rig",
			Enabled:             true,
			Provisioned:         true,
			Due:                 true,
			State:               "idle",
			InvokeError:         "exit status 7",
			EffectiveCadenceSec: 21600,
		},
	}

	var buf bytes.Buffer
	printCheckDueTable(&buf, decisions)
	out := buf.String()

	wantSubstrings := []string{
		"RIG", "DUE", "STATE", "CADENCE",
		"gastown_upstream", "yes",
		"other_rig", "cooldown:3h0m remaining",
		"disabled_rig", "disabled",
		"invoked_rig", "invoked",
		"invoke_failed_rig", "invoke-error",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(out, s) {
			t.Errorf("table output missing %q\nGot:\n%s", s, out)
		}
	}
}

// TestCollectCheckDueTargets_FlagConflict ensures we reject incompatible
// flag combinations early so callers get a fast, actionable error.
// The logic is in runUpstreamCheckDue but we inline-check the contract
// at the helper boundary by setting the package vars and verifying the
// helper-resolved single rig.
func TestCollectCheckDueTargets_NoCwdRig(t *testing.T) {
	// Restore globals after test.
	t.Cleanup(func() {
		upstreamCheckDueAll = false
		upstreamCheckDueRig = ""
	})

	// Use a temp directory that is not under any town root so
	// resolveCurrentRig returns "".
	tmp := t.TempDir()
	upstreamCheckDueAll = false
	upstreamCheckDueRig = ""

	_, err := collectCheckDueTargets(tmp)
	if err == nil {
		t.Fatalf("expected error when cwd has no rig, got nil")
	}
	if !strings.Contains(err.Error(), "could not determine rig") {
		t.Errorf("error should explain rig resolution; got %v", err)
	}
}

// TestCollectCheckDueTargets_ExplicitRig verifies that an explicit
// --rig is honored without filesystem lookups.
func TestCollectCheckDueTargets_ExplicitRig(t *testing.T) {
	t.Cleanup(func() {
		upstreamCheckDueAll = false
		upstreamCheckDueRig = ""
	})
	upstreamCheckDueAll = false
	upstreamCheckDueRig = "explicit_rig"

	got, err := collectCheckDueTargets(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "explicit_rig" {
		t.Errorf("got %v, want [explicit_rig]", got)
	}
}

// TestCheckDueDecision_JSONShape locks the JSON shape so deacon patrol
// scripts can rely on field names not silently changing.
func TestCheckDueDecision_JSONShape(t *testing.T) {
	d := CheckDueDecision{
		Rig:                 "x",
		Enabled:             true,
		Provisioned:         true,
		Due:                 true,
		State:               "idle",
		SkipReason:          "",
		EffectiveCadenceSec: 360,
		NextDueAt:           "2026-05-29T15:00:00Z",
		LastSyncAt:          "2026-05-29T09:00:00Z",
		Invoked:             true,
	}
	// The struct tags are the contract — verify reflect-based name access.
	expectedFields := []string{
		"rig", "enabled", "provisioned", "due", "state",
		"skip_reason", "effective_cadence_seconds", "next_due_at",
		"last_sync_at", "invoked", "invoke_error",
	}
	// We rely on the JSON encoder's behavior elsewhere; here a smoke
	// test catches accidental tag drift.
	out := mustMarshalJSON(t, d)
	for _, f := range expectedFields {
		if !strings.Contains(out, "\""+f+"\"") {
			// Some fields are omitempty; only flag non-empty ones.
			switch f {
			case "skip_reason", "invoke_error":
				continue
			}
			t.Errorf("expected JSON field %q in output: %s", f, out)
		}
	}
}

// TestUpstreamCheckDueNowFn_DefaultIsTimeNow guards against accidental
// nil overrides leaking from another test.
func TestUpstreamCheckDueNowFn_DefaultIsTimeNow(t *testing.T) {
	got := upstreamCheckDueNowFn()
	if time.Since(got) > time.Second {
		t.Errorf("upstreamCheckDueNowFn drifted: now=%v", got)
	}
}

// mustMarshalJSON is a tiny helper used only inside the JSON-shape
// guardrail. Keeps the test file independent of helper imports
// elsewhere in the cmd package.
func mustMarshalJSON(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}
	return string(b)
}

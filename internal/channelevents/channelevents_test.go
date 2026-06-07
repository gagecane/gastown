package channelevents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmitToTown(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	path, err := EmitToTown(townRoot, "refinery", "MERGE_READY", []string{
		"source=witness",
		"rig=dashboard",
	})
	if err != nil {
		t.Fatalf("EmitToTown failed: %v", err)
	}

	if !strings.HasSuffix(path, ".event") {
		t.Errorf("expected .event suffix, got %q", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading event file: %v", err)
	}

	var event map[string]interface{}
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("unmarshaling event: %v", err)
	}

	if event["type"] != "MERGE_READY" {
		t.Errorf("type = %v, want MERGE_READY", event["type"])
	}
	if event["channel"] != "refinery" {
		t.Errorf("channel = %v, want refinery", event["channel"])
	}

	payload, ok := event["payload"].(map[string]interface{})
	if !ok {
		t.Fatal("payload is not a map")
	}
	if payload["source"] != "witness" {
		t.Errorf("payload.source = %v, want witness", payload["source"])
	}
	if payload["rig"] != "dashboard" {
		t.Errorf("payload.rig = %v, want dashboard", payload["rig"])
	}
}

func TestEmitToTown_InvalidChannel(t *testing.T) {
	t.Parallel()
	_, err := EmitToTown(t.TempDir(), "../escape", "TEST", nil)
	if err == nil {
		t.Error("expected error for invalid channel name")
	}
}

func TestEmitToTown_UniqueFilenames(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	seen := make(map[string]bool)

	for i := 0; i < 10; i++ {
		path, err := EmitToTown(townRoot, "test", "EVENT", nil)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if seen[path] {
			t.Errorf("duplicate filename: %s", path)
		}
		seen[path] = true
	}
}

func TestValidChannelName(t *testing.T) {
	t.Parallel()
	valid := []string{"refinery", "witness", "my-channel", "test_chan", "abc123"}
	for _, name := range valid {
		if !ValidChannelName.MatchString(name) {
			t.Errorf("%q should be valid", name)
		}
	}

	invalid := []string{"../escape", "has space", "has/slash", "", "has.dot"}
	for _, name := range invalid {
		if ValidChannelName.MatchString(name) {
			t.Errorf("%q should be invalid", name)
		}
	}
}

// writeRigsConfig writes a minimal mayor/rigs.json registering the given rig
// names under townRoot, for exercising the rig-registration emit filter.
func writeRigsConfig(t *testing.T, townRoot string, rigNames ...string) {
	t.Helper()
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("creating mayor dir: %v", err)
	}
	rigs := make(map[string]map[string]any, len(rigNames))
	for _, name := range rigNames {
		rigs[name] = map[string]any{"git_url": "https://example.com/" + name + ".git"}
	}
	data, err := json.Marshal(map[string]any{"version": 1, "rigs": rigs})
	if err != nil {
		t.Fatalf("marshaling rigs.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), data, 0644); err != nil {
		t.Fatalf("writing rigs.json: %v", err)
	}
}

// countEvents returns the number of .event files in the channel directory.
func countEvents(t *testing.T, townRoot, channel string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(townRoot, "events", channel))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("reading channel dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".event") {
			n++
		}
	}
	return n
}

// gu-capht: events tagged with a rig absent from a populated rigs.json are
// rejected at the emit layer so they never reach the refinery patrol loop.
func TestEmitToTown_RejectsUnregisteredRig(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, "gastown_upstream", "casc_webapp")

	_, err := EmitToTown(townRoot, "refinery", "MQ_SUBMIT", []string{
		"source=sling",
		"rig=nonexistent-rig",
		"message=test message",
	})
	if err == nil {
		t.Fatal("expected error rejecting unregistered rig, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent-rig") {
		t.Errorf("error should name the offending rig, got: %v", err)
	}
	if n := countEvents(t, townRoot, "refinery"); n != 0 {
		t.Errorf("rejected event should not be written, found %d event file(s)", n)
	}
}

// A rig that IS registered passes through and is written normally.
func TestEmitToTown_AllowsRegisteredRig(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, "gastown_upstream")

	path, err := EmitToTown(townRoot, "refinery", "MQ_SUBMIT", []string{
		"rig=gastown_upstream",
	})
	if err != nil {
		t.Fatalf("EmitToTown rejected a registered rig: %v", err)
	}
	if !strings.HasSuffix(path, ".event") {
		t.Errorf("expected .event suffix, got %q", path)
	}
}

// Fail-open: when rigs.json is missing, the rig filter must not drop events.
func TestEmitToTown_FailsOpenWithoutRigsConfig(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir() // no mayor/rigs.json

	path, err := EmitToTown(townRoot, "refinery", "MQ_SUBMIT", []string{
		"rig=anything",
	})
	if err != nil {
		t.Fatalf("expected fail-open (no rigs.json), got error: %v", err)
	}
	if !strings.HasSuffix(path, ".event") {
		t.Errorf("expected .event suffix, got %q", path)
	}
}

// An event with no rig payload is never filtered, regardless of registry state.
func TestEmitToTown_NoRigPayloadAlwaysPasses(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, "gastown_upstream")

	path, err := EmitToTown(townRoot, "mayor", "SLOT_OPEN", []string{
		"source=witness",
	})
	if err != nil {
		t.Fatalf("event without rig payload should pass, got error: %v", err)
	}
	if !strings.HasSuffix(path, ".event") {
		t.Errorf("expected .event suffix, got %q", path)
	}
}

func TestEmitToTown_CreatesDirectory(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	channelDir := filepath.Join(townRoot, "events", "newchannel")

	if _, err := os.Stat(channelDir); !os.IsNotExist(err) {
		t.Fatal("channel dir should not exist yet")
	}

	_, err := EmitToTown(townRoot, "newchannel", "TEST", nil)
	if err != nil {
		t.Fatalf("EmitToTown failed: %v", err)
	}

	if _, err := os.Stat(channelDir); err != nil {
		t.Errorf("channel dir should exist after emit: %v", err)
	}
}

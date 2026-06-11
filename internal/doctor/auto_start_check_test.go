package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutoStartCheck_Run(t *testing.T) {
	townRoot := t.TempDir()

	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatal(err)
	}
	routesContent := `{"prefix":"gt-","path":"gastown"}
{"prefix":"bd-","path":"beads"}
`
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	// gastown rig with auto-start disabled (correct)
	gastownBeads := filepath.Join(townRoot, "gastown", ".beads")
	if err := os.MkdirAll(gastownBeads, 0755); err != nil {
		t.Fatal(err)
	}
	gastownConfig := "prefix: gt\nissue-prefix: gt\ndolt.auto-start: \"false\"\n"
	if err := os.WriteFile(filepath.Join(gastownBeads, "config.yaml"), []byte(gastownConfig), 0644); err != nil {
		t.Fatal(err)
	}

	// beads rig WITHOUT auto-start (vulnerable)
	beadsBeads := filepath.Join(townRoot, "beads", ".beads")
	if err := os.MkdirAll(beadsBeads, 0755); err != nil {
		t.Fatal(err)
	}
	beadsConfig := "prefix: bd\nissue-prefix: bd\n"
	if err := os.WriteFile(filepath.Join(beadsBeads, "config.yaml"), []byte(beadsConfig), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := &CheckContext{TownRoot: townRoot}
	result := NewAutoStartCheck().Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %v", result.Status)
	}
	if len(result.Details) != 1 {
		t.Fatalf("expected 1 rig missing auto-start, got %d: %v", len(result.Details), result.Details)
	}
	if result.Details[0] != "beads" {
		t.Errorf("expected 'beads' in details, got %q", result.Details[0])
	}
}

func TestAutoStartCheck_Run_FlagsAutoStartTrue(t *testing.T) {
	townRoot := t.TempDir()

	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"),
		[]byte(`{"prefix":"gt-","path":"gastown"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	gastownBeads := filepath.Join(townRoot, "gastown", ".beads")
	if err := os.MkdirAll(gastownBeads, 0755); err != nil {
		t.Fatal(err)
	}
	// Explicit "true" must be flagged, not treated as configured.
	cfg := "prefix: gt\nissue-prefix: gt\ndolt.auto-start: \"true\"\n"
	if err := os.WriteFile(filepath.Join(gastownBeads, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}

	result := NewAutoStartCheck().Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning for auto-start:true, got %v", result.Status)
	}
}

func TestAutoStartCheck_Run_AllCorrect(t *testing.T) {
	townRoot := t.TempDir()

	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"),
		[]byte(`{"prefix":"gt-","path":"gastown"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	gastownBeads := filepath.Join(townRoot, "gastown", ".beads")
	if err := os.MkdirAll(gastownBeads, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := "prefix: gt\nissue-prefix: gt\ndolt.auto-start: \"false\"\n"
	if err := os.WriteFile(filepath.Join(gastownBeads, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}

	result := NewAutoStartCheck().Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK, got %v: %s", result.Status, result.Message)
	}
}

func TestAutoStartCheck_Fix(t *testing.T) {
	townRoot := t.TempDir()

	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"),
		[]byte(`{"prefix":"gt-","path":"gastown"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	gastownBeads := filepath.Join(townRoot, "gastown", ".beads")
	if err := os.MkdirAll(gastownBeads, 0755); err != nil {
		t.Fatal(err)
	}
	// Missing auto-start; include metadata so Fix preserves the real prefix.
	cfg := "prefix: gt\nissue-prefix: gt\n"
	if err := os.WriteFile(filepath.Join(gastownBeads, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	meta := `{"backend":"dolt","dolt_mode":"server","dolt_database":"gastown","issue_prefix":"gt"}`
	if err := os.WriteFile(filepath.Join(gastownBeads, "metadata.json"), []byte(meta), 0644); err != nil {
		t.Fatal(err)
	}

	if err := NewAutoStartCheck().Fix(&CheckContext{TownRoot: townRoot}); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(gastownBeads, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, `dolt.auto-start: "false"`) {
		t.Errorf("config.yaml should contain dolt.auto-start: \"false\", got:\n%s", content)
	}
	// Fix must not blank the prefix.
	if !strings.Contains(content, "prefix: gt\n") {
		t.Errorf("Fix must preserve prefix, got:\n%s", content)
	}
}

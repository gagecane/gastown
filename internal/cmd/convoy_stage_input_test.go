package cmd

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Input parsing + validation tests (gt-csl.3.1)
// ---------------------------------------------------------------------------

// TestConvoyStageInput_EmptyArgs verifies empty args are rejected.
func TestConvoyStageInput_EmptyArgs(t *testing.T) {
	err := validateStageArgs([]string{})
	if err == nil {
		t.Fatal("expected error for empty args")
	}
}

// TestConvoyStageInput_FlagLikeArg verifies flag-like args are rejected.
func TestConvoyStageInput_FlagLikeArg(t *testing.T) {
	err := validateStageArgs([]string{"--verbose"})
	if err == nil {
		t.Fatal("expected error for flag-like arg")
	}
	if !strings.Contains(err.Error(), "flag") {
		t.Errorf("error should mention flag: %v", err)
	}
}

// TestConvoyStageInput_ValidSingleArg verifies a single bead ID passes.
func TestConvoyStageInput_ValidSingleArg(t *testing.T) {
	err := validateStageArgs([]string{"gt-abc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestConvoyStageInput_ValidMultipleArgs verifies multiple bead IDs pass.
func TestConvoyStageInput_ValidMultipleArgs(t *testing.T) {
	err := validateStageArgs([]string{"gt-abc", "gt-def", "gt-ghi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestConvoyStageInput_ClassifyEpic verifies epic type classification.
func TestConvoyStageInput_ClassifyEpic(t *testing.T) {
	kind := classifyBeadType("epic")
	if kind != StageInputEpic {
		t.Errorf("expected StageInputEpic, got %v", kind)
	}
}

// TestConvoyStageInput_ClassifyConvoy verifies convoy type classification.
func TestConvoyStageInput_ClassifyConvoy(t *testing.T) {
	kind := classifyBeadType("convoy")
	if kind != StageInputConvoy {
		t.Errorf("expected StageInputConvoy, got %v", kind)
	}
}

// TestConvoyStageInput_ClassifyTask verifies task-like types are classified as StageInputTasks.
func TestConvoyStageInput_ClassifyTask(t *testing.T) {
	for _, typ := range []string{"task", "bug", "feature", "chore"} {
		kind := classifyBeadType(typ)
		if kind != StageInputTasks {
			t.Errorf("expected StageInputTasks for %q, got %v", typ, kind)
		}
	}
}

// TestConvoyStageInput_MixedTypes verifies mixed input types are rejected.
func TestConvoyStageInput_MixedTypes(t *testing.T) {
	types := map[string]string{"gt-epic": "epic", "gt-task": "task"}
	_, err := resolveInputKind(types)
	if err == nil {
		t.Fatal("expected error for mixed types")
	}
	if !strings.Contains(err.Error(), "mixed") || !strings.Contains(err.Error(), "separate") {
		t.Errorf("error should mention mixed types and suggest separate invocations: %v", err)
	}
}

// TestConvoyStageInput_MultipleEpicsError verifies multiple epics are rejected.
func TestConvoyStageInput_MultipleEpicsError(t *testing.T) {
	types := map[string]string{"gt-epic1": "epic", "gt-epic2": "epic"}
	_, err := resolveInputKind(types)
	if err == nil {
		t.Fatal("expected error for multiple epics")
	}
}

// TestConvoyStageInput_SingleEpicOK verifies a single epic is accepted.
func TestConvoyStageInput_SingleEpicOK(t *testing.T) {
	types := map[string]string{"gt-epic": "epic"}
	input, err := resolveInputKind(types)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if input.Kind != StageInputEpic {
		t.Errorf("expected StageInputEpic, got %v", input.Kind)
	}
}

// TestConvoyStageInput_MultipleTasksOK verifies multiple tasks are accepted.
func TestConvoyStageInput_MultipleTasksOK(t *testing.T) {
	types := map[string]string{"gt-a": "task", "gt-b": "task", "gt-c": "bug"}
	input, err := resolveInputKind(types)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if input.Kind != StageInputTasks {
		t.Errorf("expected StageInputTasks, got %v", input.Kind)
	}
}


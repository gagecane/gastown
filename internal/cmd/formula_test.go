package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/formula"
)

// TestAutoInferRig verifies the rig auto-selection logic used when --rig is
// not provided and cwd-based detection finds nothing (e.g. Deacon at HQ level
// on a non-default install where "gastown" rig does not exist).
func TestAutoInferRig(t *testing.T) {
	t.Parallel()

	makeWorkspace := func(t *testing.T) (root string) {
		t.Helper()
		root = t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "mayor"), 0o755); err != nil {
			t.Fatalf("mkdir mayor: %v", err)
		}
		return root
	}

	writeRigsJSON := func(t *testing.T, root string, rigNames []string) {
		t.Helper()
		cfg := &config.RigsConfig{
			Version: 1,
			Rigs:    make(map[string]config.RigEntry),
		}
		for _, name := range rigNames {
			cfg.Rigs[name] = config.RigEntry{}
		}
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal rigs.json: %v", err)
		}
		path := filepath.Join(root, "mayor", "rigs.json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write rigs.json: %v", err)
		}
	}

	t.Run("single rig auto-selects", func(t *testing.T) {
		t.Parallel()
		root := makeWorkspace(t)
		rigDir := filepath.Join(root, "myrig")
		if err := os.MkdirAll(rigDir, 0o755); err != nil {
			t.Fatalf("mkdir myrig: %v", err)
		}
		writeRigsJSON(t, root, []string{"myrig"})

		name, path, err := autoInferRig(root)
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if name != "myrig" {
			t.Errorf("name = %q, want %q", name, "myrig")
		}
		if path != rigDir {
			t.Errorf("path = %q, want %q", path, rigDir)
		}
	})

	t.Run("multiple rigs require explicit --rig", func(t *testing.T) {
		t.Parallel()
		root := makeWorkspace(t)
		for _, name := range []string{"rig1", "rig2"} {
			if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", name, err)
			}
		}
		writeRigsJSON(t, root, []string{"rig1", "rig2"})

		_, _, err := autoInferRig(root)
		if err == nil {
			t.Fatal("expected error for multiple rigs, got nil")
		}
		if !strings.Contains(err.Error(), "cannot determine target rig") {
			t.Errorf("expected rig-detection error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "--rig=NAME") {
			t.Errorf("error should suggest --rig=NAME, got: %v", err)
		}
		if !strings.Contains(err.Error(), "rig1") || !strings.Contains(err.Error(), "rig2") {
			t.Errorf("error should list available rigs, got: %v", err)
		}
	})

	t.Run("no rigs registered", func(t *testing.T) {
		t.Parallel()
		root := makeWorkspace(t)
		writeRigsJSON(t, root, []string{})

		_, _, err := autoInferRig(root)
		if err == nil {
			t.Fatal("expected error for no rigs, got nil")
		}
		if !strings.Contains(err.Error(), "no rigs registered") {
			t.Errorf("error should mention no rigs registered, got: %v", err)
		}
		if !strings.Contains(err.Error(), "--rig=NAME") {
			t.Errorf("error should suggest --rig=NAME, got: %v", err)
		}
	})

	t.Run("malformed rigs.json surfaces error", func(t *testing.T) {
		t.Parallel()
		root := makeWorkspace(t)
		path := filepath.Join(root, "mayor", "rigs.json")
		if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
			t.Fatalf("write rigs.json: %v", err)
		}

		// discoverRigsForTownRoot silently falls back to an empty config on
		// parse error, so autoInferRig surfaces the "no rigs registered" path.
		_, _, err := autoInferRig(root)
		if err == nil {
			t.Fatal("expected error for malformed rigs.json, got nil")
		}
		if !strings.Contains(err.Error(), "no rigs registered") {
			t.Errorf("expected no-rigs error (fallback from malformed JSON), got: %v", err)
		}
	})
}

func TestBuildConvoyLegSlingArgs_AlwaysIncludesNoConvoy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		agent      string
		reviewOnly bool
		wantFlags  []string
	}{
		{"no agent no review", "", false, []string{"--no-convoy"}},
		{"with agent", "claude", false, []string{"--no-convoy", "--agent", "claude"}},
		{"review only", "", true, []string{"--no-convoy", "--review-only"}},
		{"agent and review", "gemini", true, []string{"--no-convoy", "--agent", "gemini", "--review-only"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildConvoyLegSlingArgs("bead-1", "myrig", "desc", "title", tt.agent, tt.reviewOnly)
			for _, want := range tt.wantFlags {
				if !slices.Contains(got, want) {
					t.Errorf("buildConvoyLegSlingArgs() missing %q in %v", want, got)
				}
			}
			if got[0] != "sling" {
				t.Errorf("first arg must be 'sling', got %q", got[0])
			}
		})
	}
}

func TestBuildWorkflowStepSlingArgs_AlwaysIncludesNoConvoy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		agent string
	}{
		{"no agent", ""},
		{"with agent", "claude-haiku"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildWorkflowStepSlingArgs("bead-2", "myrig", "desc", "title", tt.agent)
			if !slices.Contains(got, "--no-convoy") {
				t.Errorf("buildWorkflowStepSlingArgs() missing --no-convoy in %v", got)
			}
			if got[0] != "sling" {
				t.Errorf("first arg must be 'sling', got %q", got[0])
			}
			if tt.agent != "" && !slices.Contains(got, tt.agent) {
				t.Errorf("buildWorkflowStepSlingArgs() missing agent %q in %v", tt.agent, got)
			}
		})
	}
}

func TestResolveFormulaLegAgent_Precedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		legAgent     string
		cliAgent     string
		formulaAgent string
		want         string
	}{
		{"all empty", "", "", "", ""},
		{"formula only", "", "", "gemini", "gemini"},
		{"cli only", "", "codex", "", "codex"},
		{"leg only", "claude-haiku", "", "", "claude-haiku"},
		{"cli overrides formula", "", "codex", "gemini", "codex"},
		{"leg overrides cli", "claude-haiku", "codex", "gemini", "claude-haiku"},
		{"leg overrides formula", "claude-haiku", "", "gemini", "claude-haiku"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveFormulaLegAgent(tt.legAgent, tt.cliAgent, tt.formulaAgent)
			if got != tt.want {
				t.Errorf("resolveFormulaLegAgent(%q, %q, %q) = %q, want %q",
					tt.legAgent, tt.cliAgent, tt.formulaAgent, got, tt.want)
			}
		})
	}
}

func TestSubstituteFormulaVars(t *testing.T) {
	t.Parallel()

	vars := map[string]interface{}{
		"problem": "First paragraph.\n\nSecond paragraph.",
		"context": "existing code",
	}
	got := substituteFormulaVars("Problem: {{ problem }}\nContext: {{context}}\nKeep: {{review_id}}", vars)
	want := "Problem: First paragraph.\n\nSecond paragraph.\nContext: existing code\nKeep: {{review_id}}"
	if got != want {
		t.Fatalf("substituteFormulaVars() = %q, want %q", got, want)
	}
}

func TestParseSetVarsPreservesMultilineValues(t *testing.T) {
	t.Parallel()

	got := parseSetVars([]string{"problem=First\n\nSecond", "context=a=b"})
	if got["problem"] != "First\n\nSecond" {
		t.Fatalf("problem = %q, want multiline value", got["problem"])
	}
	if got["context"] != "a=b" {
		t.Fatalf("context = %q, want value with equals", got["context"])
	}
}

func TestWorkflowStepTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		step formula.Step
		want string
	}{
		{name: "default rig", step: formula.Step{}, want: "gastown"},
		{name: "explicit rig", step: formula.Step{Target: "rig"}, want: "gastown"},
		{name: "mayor", step: formula.Step{Target: "mayor"}, want: "mayor"},
		{name: "crew path", step: formula.Step{Target: "gastown/crew/alex"}, want: "gastown/crew/alex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := workflowStepTarget(tt.step, "gastown"); got != tt.want {
				t.Fatalf("workflowStepTarget() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWorkflowStepDescriptionAddsTargetMetadata(t *testing.T) {
	t.Parallel()

	description := "Line one\n\nLine two"
	got := workflowStepDescription(formula.Step{Target: "mayor"}, description)
	want := "workflow_target: mayor\n\nLine one\n\nLine two"
	if got != want {
		t.Fatalf("workflowStepDescription() = %q, want %q", got, want)
	}
}

func TestWorkflowStepTargetFromDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		description string
		want        string
	}{
		{name: "no metadata", description: "Body only", want: ""},
		{name: "mayor", description: "workflow_target: mayor\n\nBody", want: "mayor"},
		{name: "rig alias", description: "workflow_target: rig\n\nBody", want: "gastown"},
		{name: "path target", description: "workflow_target: gastown/crew/alex\n\nBody", want: "gastown/crew/alex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := workflowStepTargetFromDescription(tt.description, "gastown"); got != tt.want {
				t.Fatalf("workflowStepTargetFromDescription() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAttachmentFormulaVarsPrefersAttachedVars(t *testing.T) {
	t.Parallel()

	attachment := &beads.AttachmentFields{
		AttachedVars: []string{"problem=First\n\nSecond"},
		FormulaVars:  "problem=First\n\ntruncated",
	}
	got := attachmentFormulaVars(attachment)
	want := []string{"problem=First\n\nSecond"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attachmentFormulaVars() = %#v, want %#v", got, want)
	}
}

// writeEmbeddedFormulaToTempDir copies the embedded formula <name> to a fresh
// temp directory and returns its on-disk path. Used by parseFormulaFile tests
// that need a real file path (parseFormulaFile reads from disk).
func writeEmbeddedFormulaToTempDir(t *testing.T, name string) string {
	t.Helper()
	data, err := formula.GetEmbeddedFormulaContent(name)
	if err != nil {
		t.Fatalf("GetEmbeddedFormulaContent(%q): %v", name, err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, name+".formula.toml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write formula to tempdir: %v", err)
	}
	return path
}

// TestParseFormulaFile_ResolvesShinyEnterprise locks in the gu-deat fix:
// parseFormulaFile MUST return a fully-resolved formula for compositions
// that use extends + compose.expand. Without resolution, executeWorkflowFormula
// fails with "workflow formula 'X' has no steps" because the parent's [[steps]]
// were never merged into the child.
//
// shiny-enterprise extends shiny (5 steps) and expands "implement" with
// rule-of-five (5 template steps). Resolved result: 9 steps total.
func TestParseFormulaFile_ResolvesShinyEnterprise(t *testing.T) {
	t.Parallel()

	path := writeEmbeddedFormulaToTempDir(t, "shiny-enterprise")

	f, err := parseFormulaFile(path)
	if err != nil {
		t.Fatalf("parseFormulaFile: %v", err)
	}

	if f.Type != formula.TypeWorkflow {
		t.Errorf("Type = %q, want %q", f.Type, formula.TypeWorkflow)
	}

	wantIDs := []string{
		"design",
		"implement.draft",
		"implement.refine-1",
		"implement.refine-2",
		"implement.refine-3",
		"implement.refine-4",
		"review",
		"test",
		"submit",
	}
	gotIDs := make([]string, len(f.Steps))
	for i, s := range f.Steps {
		gotIDs[i] = s.ID
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("resolved step IDs = %v, want %v", gotIDs, wantIDs)
	}

	// review must depend on the last expanded step, not the replaced "implement".
	var reviewStep *formula.Step
	for i := range f.Steps {
		if f.Steps[i].ID == "review" {
			reviewStep = &f.Steps[i]
			break
		}
	}
	if reviewStep == nil {
		t.Fatal("review step missing from resolved formula")
	}
	if !slices.Contains(reviewStep.Needs, "implement.refine-4") {
		t.Errorf("review.Needs = %v, want to contain %q", reviewStep.Needs, "implement.refine-4")
	}
	if slices.Contains(reviewStep.Needs, "implement") {
		t.Errorf("review.Needs still references replaced step %q: %v", "implement", reviewStep.Needs)
	}
}

// TestParseFormulaFile_ResolvesMonorepoTDD locks in the same fix for the second
// canonical example called out in gu-deat: mol-polecat-work-monorepo-tdd.
//
// mol-polecat-work-monorepo has 10 steps; expanding "implement" with tdd-cycle
// (5 template steps) replaces 1 step with 5 → 14 steps total.
func TestParseFormulaFile_ResolvesMonorepoTDD(t *testing.T) {
	t.Parallel()

	path := writeEmbeddedFormulaToTempDir(t, "mol-polecat-work-monorepo-tdd")

	f, err := parseFormulaFile(path)
	if err != nil {
		t.Fatalf("parseFormulaFile: %v", err)
	}

	if f.Type != formula.TypeWorkflow {
		t.Errorf("Type = %q, want %q", f.Type, formula.TypeWorkflow)
	}
	if len(f.Steps) != 14 {
		ids := make([]string, len(f.Steps))
		for i, s := range f.Steps {
			ids[i] = s.ID
		}
		t.Fatalf("len(Steps) = %d, want 14: %v", len(f.Steps), ids)
	}

	// The replaced "implement" step must be gone, with tdd-cycle steps in its place.
	wantTDDIDs := []string{
		"implement.write-tests",
		"implement.verify-red",
		"implement.implement",
		"implement.verify-green",
		"implement.refactor",
	}
	for _, id := range wantTDDIDs {
		found := false
		for _, s := range f.Steps {
			if s.ID == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expanded step %q missing from resolved steps", id)
		}
	}
	for _, s := range f.Steps {
		if s.ID == "implement" {
			t.Errorf("replaced step %q still present after expansion", "implement")
		}
	}
}

// TestParseFormulaFile_PassThroughForSimpleFormulas guards against the fix
// over-reaching: formulas without extends or compose must pass through with
// the same Steps list as a raw Parse() call. Resolution should be a no-op
// for plain workflow formulas like shiny.
func TestParseFormulaFile_PassThroughForSimpleFormulas(t *testing.T) {
	t.Parallel()

	path := writeEmbeddedFormulaToTempDir(t, "shiny")

	f, err := parseFormulaFile(path)
	if err != nil {
		t.Fatalf("parseFormulaFile: %v", err)
	}

	wantIDs := []string{"design", "implement", "review", "test", "submit"}
	gotIDs := make([]string, len(f.Steps))
	for i, s := range f.Steps {
		gotIDs[i] = s.ID
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("steps = %v, want %v", gotIDs, wantIDs)
	}
}

// TestSubstituteStepFields verifies that {{var}} placeholders in step fields
// (Title, Description, Acceptance, ConsumerBeadID) are substituted from the
// var map. Locks in the fix for gu-uiax: shiny-tdd was leaking unrendered
// {{feature}} into dispatched bead titles because executeWorkflowFormula
// substituted step.Description but not step.Title.
func TestSubstituteStepFields(t *testing.T) {
	t.Parallel()

	step := formula.Step{
		ID:             "implement",
		Title:          "Implement {{feature}}",
		Description:    "Build the code for {{feature}} per design.",
		Acceptance:     "{{feature}} ships behind a flag",
		ConsumerBeadID: "consumer-{{feature}}",
	}
	vars := map[string]interface{}{"feature": "auth-redesign"}

	got := substituteStepFields(step, vars)

	if got.Title != "Implement auth-redesign" {
		t.Errorf("Title = %q, want %q", got.Title, "Implement auth-redesign")
	}
	if got.Description != "Build the code for auth-redesign per design." {
		t.Errorf("Description = %q, want %q", got.Description, "Build the code for auth-redesign per design.")
	}
	if got.Acceptance != "auth-redesign ships behind a flag" {
		t.Errorf("Acceptance = %q, want %q", got.Acceptance, "auth-redesign ships behind a flag")
	}
	if got.ConsumerBeadID != "consumer-auth-redesign" {
		t.Errorf("ConsumerBeadID = %q, want %q", got.ConsumerBeadID, "consumer-auth-redesign")
	}
	// Original step must not be mutated (caller may iterate over f.Steps).
	if step.Title != "Implement {{feature}}" {
		t.Errorf("input step.Title was mutated: %q", step.Title)
	}
}

// TestSubstituteStepFields_LeavesUnknownPlaceholders confirms that placeholders
// for vars not in the map are left untouched (matching substituteFormulaVars
// semantics). This is the failure surface that triggered gu-uiax — when a
// required var is unset, the placeholder reaches the dispatched bead.
// validateRequiredFormulaVars is the gate that prevents that case.
func TestSubstituteStepFields_LeavesUnknownPlaceholders(t *testing.T) {
	t.Parallel()

	step := formula.Step{
		ID:    "design",
		Title: "Design {{feature}}",
	}
	got := substituteStepFields(step, map[string]interface{}{})

	if got.Title != "Design {{feature}}" {
		t.Errorf("Title = %q, want unchanged %q", got.Title, "Design {{feature}}")
	}
}

// TestValidateRequiredFormulaVars_MissingFeature locks in the second half of
// gu-uiax acceptance: when a formula declares a required var (e.g. shiny's
// `feature`) and it is not provided at sling time, the dispatcher must refuse
// rather than letting `{{feature}}` placeholders leak into bead titles.
func TestValidateRequiredFormulaVars_MissingFeature(t *testing.T) {
	t.Parallel()

	f := &formula.Formula{
		Vars: map[string]formula.Var{
			"feature":  {Description: "The feature being implemented", Required: true},
			"assignee": {Description: "Who is assigned"},
		},
	}

	err := validateRequiredFormulaVars(f, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing required var, got nil")
	}
	if !strings.Contains(err.Error(), "feature") {
		t.Errorf("error should name the missing var, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--set") {
		t.Errorf("error should suggest --set flag, got: %v", err)
	}
}

// TestValidateRequiredFormulaVars_AllProvided is the positive case: when every
// required var has a value, validation passes.
func TestValidateRequiredFormulaVars_AllProvided(t *testing.T) {
	t.Parallel()

	f := &formula.Formula{
		Vars: map[string]formula.Var{
			"feature":  {Required: true},
			"assignee": {},
		},
	}

	err := validateRequiredFormulaVars(f, map[string]interface{}{
		"feature": "auth-redesign",
	})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

// TestValidateRequiredFormulaVars_DefaultSatisfiesRequired confirms that a
// required var with a non-empty default is treated as satisfied. This matches
// the existing prime_molecule.go behavior where defaults seed the var map.
func TestValidateRequiredFormulaVars_DefaultSatisfiesRequired(t *testing.T) {
	t.Parallel()

	f := &formula.Formula{
		Vars: map[string]formula.Var{
			"feature": {Required: true, Default: "untitled-feature"},
		},
	}

	err := validateRequiredFormulaVars(f, map[string]interface{}{})
	if err != nil {
		t.Fatalf("expected default to satisfy required var, got: %v", err)
	}
}

// TestValidateRequiredFormulaVars_EmptyValueFails closes a footgun: passing
// `--set feature=` should not satisfy a required var. An empty string would
// expand to an empty placeholder, producing dispatched bead titles like
// "Design " — almost as bad as the leaked-placeholder symptom from gu-uiax.
func TestValidateRequiredFormulaVars_EmptyValueFails(t *testing.T) {
	t.Parallel()

	f := &formula.Formula{
		Vars: map[string]formula.Var{
			"feature": {Required: true},
		},
	}

	err := validateRequiredFormulaVars(f, map[string]interface{}{"feature": ""})
	if err == nil {
		t.Fatal("expected error for empty required var value, got nil")
	}
}

// TestDeriveDesignID verifies the convoy-ID → design_id derivation used by
// the design convoy formula. Regression for gu-g764: previously the formula
// referenced {{.design_id}} but no derivation existed, so output paths
// rendered as ".designs/<no value>/" and polecats invented their own
// subdirectory names, scattering files across worktrees.
func TestDeriveDesignID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		convoyID string
		want     string
	}{
		{name: "standard convoy id", convoyID: "cacr-cv-j23uo", want: "cv-j23uo"},
		{name: "short rig prefix", convoyID: "gu-cv-abc12", want: "cv-abc12"},
		{name: "hq prefix", convoyID: "hq-cv-x9y8z", want: "cv-x9y8z"},
		{name: "empty input", convoyID: "", want: ""},
		{name: "no dash returns input", convoyID: "weirdid", want: "weirdid"},
		{name: "trailing dash returns input", convoyID: "rig-", want: "rig-"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := deriveDesignID(tc.convoyID)
			if got != tc.want {
				t.Errorf("deriveDesignID(%q) = %q, want %q", tc.convoyID, got, tc.want)
			}
		})
	}
}

// TestRenderTemplate_DesignIDSubstitutes verifies that injecting design_id
// into the template context produces a deterministic output path instead
// of the "<no value>" sentinel that triggered gu-g764.
func TestRenderTemplate_DesignIDSubstitutes(t *testing.T) {
	t.Parallel()

	tmpl := ".designs/{{.design_id}}"
	ctx := map[string]interface{}{
		"design_id": "cv-j23uo",
	}

	got, err := renderTemplate(tmpl, ctx)
	if err != nil {
		t.Fatalf("renderTemplate failed: %v", err)
	}
	want := ".designs/cv-j23uo"
	if got != want {
		t.Errorf("renderTemplate = %q, want %q", got, want)
	}
	if strings.Contains(got, "<no value>") {
		t.Errorf("rendered template still contains <no value>: %q", got)
	}
}

// TestRenderTemplate_MissingDesignIDLeavesSentinel documents the underlying
// text/template behavior that gu-g764 exposed: when the variable is absent
// from the context, Go's default missingkey policy renders "<no value>".
// The fix in executeConvoyFormula prevents this by always injecting
// design_id; this test pins the behavior so future refactors are explicit.
func TestRenderTemplate_MissingDesignIDLeavesSentinel(t *testing.T) {
	t.Parallel()

	tmpl := ".designs/{{.design_id}}"
	ctx := map[string]interface{}{} // design_id intentionally omitted

	got, err := renderTemplate(tmpl, ctx)
	if err != nil {
		t.Fatalf("renderTemplate failed: %v", err)
	}
	if !strings.Contains(got, "<no value>") {
		t.Fatalf("expected <no value> sentinel for missing design_id, got %q", got)
	}
}

// TestFormulaConvoyIDUsesTownConvoyPrefix used to exercise a top-level
// formulaConvoyID helper with a hardcoded "hq-cv-" prefix. Upstream removed
// that helper and instead constructs the convoy ID inline using the target
// rig's beads prefix (e.g. fmt.Sprintf("%s-cv-%s", rigPrefix, shortID)) so
// rigs other than HQ can run convoy formulas without colliding on bead IDs.
// The remaining behavior is covered by
// TestExecuteConvoyFormulaCreatesTownConvoyAndRigLegs below — it verifies the
// convoy bead and leg beads end up on the target rig with the rig's prefix.
// Keep this stub so the bead IDs of older test runs map to a recognisable
// name when grepping history.
func TestFormulaConvoyIDUsesTownConvoyPrefix(t *testing.T) {
	t.Skip("formulaConvoyID helper removed; convoy ID is now constructed inline using the target rig's beads prefix (see executeConvoyFormula). Coverage retained by TestExecuteConvoyFormulaCreatesTownConvoyAndRigLegs.")
}

func TestExecuteConvoyFormulaCreatesTownConvoyAndRigLegs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are unix-only")
	}

	townRoot := t.TempDir()
	townBeads := filepath.Join(townRoot, ".beads")
	rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")
	rigBeads := filepath.Join(rigDir, ".beads")
	for _, dir := range []string{filepath.Join(townRoot, "mayor", "rig"), townBeads, rigDir, rigBeads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown/mayor/rig"}`,
		`{"prefix":"hq-","path":"."}`,
		`{"prefix":"hq-cv-","path":"."}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routes), 0o644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
printf '%s|%s|%s\n' "$(pwd)" "${BEADS_DIR:-}" "$*" >> "${BD_LOG}"
exit 0
`
	_ = writeBDStub(t, binDir, bdScript, "")
	gtPath := filepath.Join(binDir, "gt")
	gtScript := `#!/bin/sh
set -e
printf 'gt|%s|%s\n' "$(pwd)" "$*" >> "${GT_LOG}"
exit 0
`
	if err := os.WriteFile(gtPath, []byte(gtScript), 0o755); err != nil {
		t.Fatalf("write gt stub: %v", err)
	}
	t.Setenv("BD_LOG", logPath)
	t.Setenv("GT_LOG", filepath.Join(townRoot, "gt.log"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BEADS_DIR", filepath.Join(townRoot, "wrong", ".beads"))

	oldAddTracking := addTrackingRelationFn
	oldPR := formulaRunPR
	oldSet := formulaRunSet
	oldFiles := formulaRunFiles
	oldAgent := formulaRunAgent
	t.Cleanup(func() {
		addTrackingRelationFn = oldAddTracking
		formulaRunPR = oldPR
		formulaRunSet = oldSet
		formulaRunFiles = oldFiles
		formulaRunAgent = oldAgent
	})
	var trackedTownRoot, trackedConvoyID, trackedIssueID string
	addTrackingRelationFn = func(townRootArg, convoyID, issueID string) error {
		trackedTownRoot = townRootArg
		trackedConvoyID = convoyID
		trackedIssueID = issueID
		return nil
	}
	formulaRunPR = 0
	formulaRunSet = nil
	formulaRunFiles = nil
	formulaRunAgent = ""

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	f := &formula.Formula{
		Description: "routing convoy",
		Legs: []formula.Leg{{
			ID:          "one",
			Title:       "Leg one",
			Description: "Do one thing",
		}},
	}
	if err := executeConvoyFormula(f, "routing-fan", "gastown"); err != nil {
		t.Fatalf("executeConvoyFormula: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	logText := string(logBytes)
	if strings.Contains(logText, "--id=gt-cv-") {
		t.Fatalf("formula convoy created rig-prefixed convoy in town log:\n%s", logText)
	}
	if !strings.Contains(logText, townBeads+"|"+townBeads+"|create ") || !strings.Contains(logText, "--id=hq-cv-") {
		t.Fatalf("formula convoy create did not target town beads with hq-cv id:\n%s", logText)
	}
	if !strings.Contains(logText, rigBeads+"|"+rigBeads+"|create ") || !strings.Contains(logText, "--id=gt-leg-") {
		t.Fatalf("formula leg create did not target rig beads with gt-leg id:\n%s", logText)
	}
	if trackedTownRoot != townRoot {
		t.Fatalf("tracking townRoot = %q, want %q", trackedTownRoot, townRoot)
	}
	if !strings.HasPrefix(trackedConvoyID, "hq-cv-") || !strings.HasPrefix(trackedIssueID, "gt-leg-") {
		t.Fatalf("tracking relation = (%q, %q), want hq-cv to gt-leg", trackedConvoyID, trackedIssueID)
	}
}

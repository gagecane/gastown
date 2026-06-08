package formula

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
)

// TestExtractTemplateVariables verifies we can find all {{variable}} patterns.
func TestExtractTemplateVariables(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected []string
	}{
		{
			name:     "single variable",
			text:     "Hello {{name}}!",
			expected: []string{"name"},
		},
		{
			name:     "multiple variables",
			text:     "{{greeting}} {{name}}, you have {{count}} messages",
			expected: []string{"count", "greeting", "name"}, // sorted alphabetically
		},
		{
			name:     "no variables",
			text:     "Hello world!",
			expected: []string{},
		},
		{
			name:     "duplicate variables",
			text:     "{{name}} and {{name}} again",
			expected: []string{"name"}, // should dedupe
		},
		{
			name:     "handlebars helpers ignored",
			text:     "{{#if condition}}{{value}}{{/if}}",
			expected: []string{"value"}, // #if, /if are helpers, not variables
		},
		{
			name:     "each helper ignored",
			text:     "{{#each items}}{{item}}{{/each}}",
			expected: []string{"item"}, // #each, /each are helpers
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractTemplateVariables(tc.text)
			if len(got) != len(tc.expected) {
				t.Errorf("ExtractTemplateVariables(%q) = %v, want %v", tc.text, got, tc.expected)
				return
			}
			for i, v := range tc.expected {
				if got[i] != v {
					t.Errorf("ExtractTemplateVariables(%q)[%d] = %q, want %q", tc.text, i, got[i], v)
				}
			}
		})
	}
}

// TestValidateTemplateVariables verifies that undefined variables are caught.
func TestValidateTemplateVariables(t *testing.T) {
	tests := []struct {
		name      string
		formula   string
		wantError bool
		errorMsg  string
	}{
		{
			name: "all variables defined",
			formula: `
formula = "test"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "Work on {{issue}}"
description = "Process {{issue}}"

[vars.issue]
description = "The issue ID"
required = true
`,
			wantError: false,
		},
		{
			name: "undefined variable",
			formula: `
formula = "test"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "Count: {{ready_count}}"
description = "Process {{issue}}"

[vars.issue]
description = "The issue ID"
required = true
`,
			wantError: true,
			errorMsg:  "ready_count",
		},
		{
			name: "variable with default is ok",
			formula: `
formula = "test"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "Count: {{ready_count}}"

[vars.ready_count]
description = "Computed count"
default = ""
`,
			wantError: false,
		},
		{
			name: "multiple undefined variables",
			formula: `
formula = "test"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "{{a}} {{b}} {{c}}"

[vars.a]
description = "Defined"
required = true
`,
			wantError: true,
			errorMsg:  "b", // Should mention at least one undefined
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Parse([]byte(tc.formula))
			if err != nil {
				t.Fatalf("Parse failed: %v", err)
			}

			err = f.ValidateTemplateVariables()
			if tc.wantError {
				if err == nil {
					t.Errorf("ValidateTemplateVariables() = nil, want error containing %q", tc.errorMsg)
				} else if !strings.Contains(err.Error(), tc.errorMsg) {
					t.Errorf("ValidateTemplateVariables() = %v, want error containing %q", err, tc.errorMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateTemplateVariables() = %v, want nil", err)
				}
			}
		})
	}
}

// TestMolConvoyFeedFormula_VariableValidation is a regression test for issue #1133.
// The mol-convoy-feed formula uses template variables like {{ready_count}} that
// aren't defined in [vars], causing wisp creation to fail.
func TestMolConvoyFeedFormula_VariableValidation(t *testing.T) {
	// Find the formula file
	formulaPath := filepath.Join("formulas", constants.MolConvoyFeed+".formula.toml")
	data, err := os.ReadFile(formulaPath)
	if err != nil {
		t.Skipf("Formula file not found: %v", err)
	}

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Failed to parse mol-convoy-feed formula: %v", err)
	}

	// This test will FAIL until the formula is fixed
	err = f.ValidateTemplateVariables()
	if err != nil {
		t.Errorf("mol-convoy-feed formula has undefined template variables: %v", err)
		t.Log("Fix: Add all computed variables to [vars] with default = \"\"")
	}
}

// TestAllEmbeddedFormulas_VariableValidation ensures no embedded formula
// has undefined template variables. This prevents future regressions.
func TestAllEmbeddedFormulas_VariableValidation(t *testing.T) {
	formulasDir := "formulas"
	entries, err := os.ReadDir(formulasDir)
	if err != nil {
		t.Skipf("Formulas directory not found: %v", err)
	}

	var failures []string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".formula.toml") {
			continue
		}

		path := filepath.Join(formulasDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("Failed to read %s: %v", entry.Name(), err)
			continue
		}

		f, err := Parse(data)
		if err != nil {
			// Skip formulas that don't parse (may have other issues)
			continue
		}

		if err := f.ValidateTemplateVariables(); err != nil {
			failures = append(failures, entry.Name()+": "+err.Error())
		}
	}

	if len(failures) > 0 {
		t.Errorf("Formulas with undefined template variables:\n%s", strings.Join(failures, "\n"))
	}
}

// TestValidateProvidedVars covers the gs-4th0 hard gate: required vars must be
// present, pattern-constrained vars must match their format, defaults satisfy
// requirements, and required inputs are enforced too.
func TestValidateProvidedVars(t *testing.T) {
	jiraPattern := "^[A-Z]+-[0-9]+$"
	tests := []struct {
		name     string
		formula  Formula
		provided map[string]string
		wantErr  string // substring; "" means expect success
	}{
		{
			name: "required var present and matching",
			formula: Formula{Name: "f", Vars: map[string]Var{
				"jira_ticket": {Required: true, Pattern: jiraPattern},
			}},
			provided: map[string]string{"jira_ticket": "LIA-1234"},
			wantErr:  "",
		},
		{
			name: "required var missing",
			formula: Formula{Name: "mol-lia-pr-work", Vars: map[string]Var{
				"jira_ticket": {Required: true, Pattern: jiraPattern},
			}},
			provided: map[string]string{},
			wantErr:  "missing required var \"jira_ticket\"",
		},
		{
			name: "required var present but empty",
			formula: Formula{Name: "f", Vars: map[string]Var{
				"jira_ticket": {Required: true, Pattern: jiraPattern},
			}},
			provided: map[string]string{"jira_ticket": ""},
			wantErr:  "missing required var",
		},
		{
			name: "invalid format rejected",
			formula: Formula{Name: "f", Vars: map[string]Var{
				"jira_ticket": {Required: true, Pattern: jiraPattern},
			}},
			provided: map[string]string{"jira_ticket": "gs-4th0"},
			wantErr:  "does not match required format",
		},
		{
			name: "default satisfies required",
			formula: Formula{Name: "f", Vars: map[string]Var{
				"base_branch": {Required: true, Default: "main"},
			}},
			provided: map[string]string{},
			wantErr:  "",
		},
		{
			name: "pattern only enforced when supplied",
			formula: Formula{Name: "f", Vars: map[string]Var{
				"jira_ticket": {Pattern: jiraPattern},
			}},
			provided: map[string]string{},
			wantErr:  "",
		},
		{
			name: "invalid pattern surfaced as error",
			formula: Formula{Name: "f", Vars: map[string]Var{
				"jira_ticket": {Pattern: "([A-Z"},
			}},
			provided: map[string]string{"jira_ticket": "X"},
			wantErr:  "invalid pattern",
		},
		{
			name: "required input missing",
			formula: Formula{Name: "f", Inputs: map[string]Input{
				"target_repo": {Required: true},
			}},
			provided: map[string]string{},
			wantErr:  "missing required input \"target_repo\"",
		},
		{
			name: "multiple problems collected",
			formula: Formula{Name: "f", Vars: map[string]Var{
				"jira_ticket": {Required: true, Pattern: jiraPattern},
				"other":       {Required: true},
			}},
			provided: map[string]string{},
			wantErr:  "other",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.formula.ValidateProvidedVars(tt.provided)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

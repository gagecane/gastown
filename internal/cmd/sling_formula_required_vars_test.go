package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTownFormula writes a formula TOML into a temp town's town-level
// formulas dir and returns the town root.
func writeTownFormula(t *testing.T, name, toml string) string {
	t.Helper()
	townRoot := t.TempDir()
	dir := filepath.Join(townRoot, ".beads", "formulas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name+".formula.toml")
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatalf("write formula: %v", err)
	}
	return townRoot
}

// TestValidateFormulaRequiredVars exercises the gs-4th0 gate end-to-end:
// resolve formula content -> parse (including the `pattern` field) -> validate.
func TestValidateFormulaRequiredVars(t *testing.T) {
	const formulaTOML = `
formula = "mol-lia-pr-work"
description = "Customer PR work for {{jira_ticket}}"
type = "workflow"

[vars.jira_ticket]
description = "JIRA ticket"
required = true
pattern = "^[A-Z]+-[0-9]+$"

[[steps]]
id = "work"
title = "Do work for {{jira_ticket}}"
description = "Open a PR titled [{{jira_ticket}}]."
`
	townRoot := writeTownFormula(t, "mol-lia-pr-work", formulaTOML)

	tests := []struct {
		name    string
		vars    []string
		wantErr string
	}{
		{name: "valid ticket", vars: []string{"jira_ticket=LIA-1234"}, wantErr: ""},
		{name: "missing ticket", vars: nil, wantErr: "missing required var"},
		{name: "invalid format", vars: []string{"jira_ticket=gs-4th0"}, wantErr: "does not match required format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFormulaRequiredVars("mol-lia-pr-work", townRoot, "", tt.vars)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

// TestValidateFormulaRequiredVars_UnresolvableIsNoOp confirms the gate fails
// open (returns nil) when the formula content cannot be resolved here, so it
// only ever ADDS rejections and never breaks existing slings.
func TestValidateFormulaRequiredVars_UnresolvableIsNoOp(t *testing.T) {
	townRoot := t.TempDir()
	if err := validateFormulaRequiredVars("does-not-exist-anywhere", townRoot, "", nil); err != nil {
		t.Fatalf("expected nil for unresolvable formula, got: %v", err)
	}
}

// TestRigFromWorkDir verifies rig extraction from a resolved beads work dir.
func TestRigFromWorkDir(t *testing.T) {
	town := "/home/x/gt"
	cases := []struct{ workDir, want string }{
		{filepath.Join(town, "gastown", ".beads"), "gastown"},
		{filepath.Join(town, ".beads"), ""},
		{"/somewhere/else/.beads", ""},
	}
	for _, c := range cases {
		if got := rigFromWorkDir(town, c.workDir); got != c.want {
			t.Errorf("rigFromWorkDir(%q,%q)=%q want %q", town, c.workDir, got, c.want)
		}
	}
}

package formula

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// variablePattern matches {{variable}} template placeholders.
// It captures the variable name (alphanumeric + underscore, starting with letter/underscore).
var variablePattern = regexp.MustCompile(`\{\{([a-zA-Z_][a-zA-Z0-9_]*)\}\}`)

// ExtractTemplateVariables finds all {{variable}} patterns in text.
// Returns a deduplicated, sorted list of variable names.
// Handlebars helpers like {{#if}}, {{/each}}, {{else}} are excluded.
func ExtractTemplateVariables(text string) []string {
	matches := variablePattern.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool)
	var vars []string

	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		name := match[1]

		// Skip Handlebars helpers and keywords
		if isHandlebarsKeyword(name) {
			continue
		}

		if !seen[name] {
			seen[name] = true
			vars = append(vars, name)
		}
	}

	sort.Strings(vars)
	return vars
}

// isHandlebarsKeyword returns true for Handlebars control keywords
// that look like variables but aren't (e.g., "else", "this").
func isHandlebarsKeyword(name string) bool {
	switch name {
	case "else", "this", "root", "index", "key", "first", "last",
		"end", "range", "with", "block", "define", "template", "nil":
		return true
	default:
		return false
	}
}

// ValidateProvidedVars enforces a formula's declared var contract against the
// values supplied at dispatch (e.g. `gt sling --var k=v`). It is the hard gate
// that prevents a non-compliant agent from being dispatched without the
// required vars a customer-facing formula relies on (gs-4th0):
//
//   - Every [vars] entry marked required = true must be present with a
//     non-empty value. A var that declares both required = true and a non-empty
//     default is satisfied by the default and need not be supplied.
//   - Every required [inputs] entry must likewise be present (inputs with a
//     non-empty default are satisfied by the default).
//   - Any var declaring a pattern = "..." must have its supplied value fully
//     match that RE2 regular expression.
//
// provided maps var name to the supplied value. All violations are collected
// and reported together so the operator can fix them in one pass. Returns nil
// when the contract is satisfied.
func (f *Formula) ValidateProvidedVars(provided map[string]string) error {
	var problems []string

	// Sort var names for deterministic error ordering.
	varNames := make([]string, 0, len(f.Vars))
	for name := range f.Vars {
		varNames = append(varNames, name)
	}
	sort.Strings(varNames)

	for _, name := range varNames {
		def := f.Vars[name]
		value, supplied := provided[name]

		if def.Required {
			// A non-empty default satisfies the requirement on its own.
			if (!supplied || value == "") && def.Default == "" {
				problems = append(problems, fmt.Sprintf("missing required var %q", name))
				continue
			}
		}

		if def.Pattern != "" && supplied && value != "" {
			re, err := regexp.Compile(def.Pattern)
			if err != nil {
				problems = append(problems, fmt.Sprintf("var %q declares an invalid pattern %q: %v", name, def.Pattern, err))
				continue
			}
			if !re.MatchString(value) {
				problems = append(problems, fmt.Sprintf("var %q value %q does not match required format %q", name, value, def.Pattern))
			}
		}
	}

	// Required inputs (convoy-style) follow the same presence contract.
	inputNames := make([]string, 0, len(f.Inputs))
	for name := range f.Inputs {
		inputNames = append(inputNames, name)
	}
	sort.Strings(inputNames)

	for _, name := range inputNames {
		in := f.Inputs[name]
		if !in.Required {
			continue
		}
		value, supplied := provided[name]
		if (!supplied || value == "") && in.Default == "" {
			problems = append(problems, fmt.Sprintf("missing required input %q", name))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("formula %q var validation failed: %s", f.Name, strings.Join(problems, "; "))
	}
	return nil
}

// ValidateTemplateVariables checks that all {{variable}} placeholders used
// in the formula are defined in the [vars] section.
//
// This catches the bug where formulas use computed variables like {{ready_count}}
// in their text but don't define them in [vars], causing bd mol wisp to fail
// with "missing required variables" error.
//
// Variables with any definition in [vars] (even with default="") are considered valid.
func (f *Formula) ValidateTemplateVariables() error {
	// Collect all text that might contain variables
	var allText strings.Builder

	// Description
	allText.WriteString(f.Description)
	allText.WriteString("\n")

	// Steps (workflow)
	for _, step := range f.Steps {
		allText.WriteString(step.Title)
		allText.WriteString("\n")
		allText.WriteString(step.Description)
		allText.WriteString("\n")
	}

	// Legs (convoy)
	for _, leg := range f.Legs {
		allText.WriteString(leg.Title)
		allText.WriteString("\n")
		allText.WriteString(leg.Description)
		allText.WriteString("\n")
		allText.WriteString(leg.Focus)
		allText.WriteString("\n")
	}

	// Synthesis
	if f.Synthesis != nil {
		allText.WriteString(f.Synthesis.Title)
		allText.WriteString("\n")
		allText.WriteString(f.Synthesis.Description)
		allText.WriteString("\n")
	}

	// Template (expansion)
	for _, tmpl := range f.Template {
		allText.WriteString(tmpl.Title)
		allText.WriteString("\n")
		allText.WriteString(tmpl.Description)
		allText.WriteString("\n")
	}

	// Aspects
	for _, aspect := range f.Aspects {
		allText.WriteString(aspect.Title)
		allText.WriteString("\n")
		allText.WriteString(aspect.Description)
		allText.WriteString("\n")
		allText.WriteString(aspect.Focus)
		allText.WriteString("\n")
	}

	// Prompts
	for _, prompt := range f.Prompts {
		allText.WriteString(prompt)
		allText.WriteString("\n")
	}

	// Inputs (descriptions may contain variable references)
	for _, input := range f.Inputs {
		allText.WriteString(input.Description)
		allText.WriteString("\n")
		allText.WriteString(input.Default)
		allText.WriteString("\n")
	}

	// Output
	if f.Output != nil {
		allText.WriteString(f.Output.Directory)
		allText.WriteString("\n")
		allText.WriteString(f.Output.LegPattern)
		allText.WriteString("\n")
		allText.WriteString(f.Output.Synthesis)
		allText.WriteString("\n")
	}

	// Extract all variables used
	usedVars := ExtractTemplateVariables(allText.String())

	// Check each against defined vars and inputs
	var undefined []string
	for _, v := range usedVars {
		if _, defined := f.Vars[v]; defined {
			continue
		}
		if _, defined := f.Inputs[v]; defined {
			continue
		}
		undefined = append(undefined, v)
	}

	if len(undefined) > 0 {
		return fmt.Errorf("undefined template variables: %s (add to [vars] section with default=\"\" for computed values)",
			strings.Join(undefined, ", "))
	}

	return nil
}

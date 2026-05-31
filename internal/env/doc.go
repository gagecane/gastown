package env

import (
	"fmt"
	"strings"
)

// Markdown renders the registry as a markdown table for ops reference.
// The output is deterministic — entries are sorted by [List].
//
// This is the Phase 4 deliverable from the parent epic (gu-4vfqg): "Add a
// doc generator that prints all vars + types for ops reference." Wiring
// this into a CLI subcommand (e.g. `gt env doc`) is left to a follow-up
// bead so that Phase 3 callsite migrations stay independent of CLI work.
func Markdown() string {
	var b strings.Builder
	b.WriteString("# GT_* environment variables\n\n")
	b.WriteString("Generated from `internal/env`. Source of truth is the registry in that package.\n\n")
	b.WriteString("| Variable | Kind | Default | Description |\n")
	b.WriteString("|----------|------|---------|-------------|\n")
	for _, s := range List() {
		def := s.Default
		if def == "" {
			def = "—"
		}
		// Markdown table cells: collapse newlines defensively.
		desc := strings.ReplaceAll(s.Desc, "\n", " ")
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", s.Var.Name(), s.Kind, def, desc)
	}
	return b.String()
}

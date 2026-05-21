package templates

import (
	_ "embed"
	"strings"

	"github.com/steveyegge/gastown/internal/cli"
)

//go:embed townroot/claude.md
var townRootCLAUDEmdRaw string

// TownRootCLAUDEmdVersion is the version of the embedded town-root CLAUDE.md.
// Increment this when updating the template content with new sections.
//
// History:
//   v1: initial extraction
//   v2: add memory system + war room sections
//   v3: replace footgun "kill -QUIT" Dolt diagnostic with `{{cmd}} dolt dump`
//       (incident gc-wisp-2yc7); add stale-content detection in doctor check.
const TownRootCLAUDEmdVersion = 3

// TownRootCLAUDEmd returns the canonical town-root CLAUDE.md content
// with the CLI command name substituted.
func TownRootCLAUDEmd() string {
	return strings.ReplaceAll(townRootCLAUDEmdRaw, "{{cmd}}", cli.Name())
}

// TownRootRequiredSection describes a section that must be present in the town-root CLAUDE.md.
type TownRootRequiredSection struct {
	Name    string // Human-readable name for reporting
	Heading string // The H2 or H3 heading to look for
}

// TownRootRequiredSections returns the key sections that must be present
// in the town-root CLAUDE.md for proper agent behavior.
func TownRootRequiredSections() []TownRootRequiredSection {
	return []TownRootRequiredSection{
		{
			Name:    "Dolt awareness",
			Heading: "## Dolt Server",
		},
		{
			Name:    "Communication hygiene",
			Heading: "### Communication hygiene",
		},
	}
}

// TownRootStalePattern describes a known-bad substring that indicates a stale
// instruction in the town-root CLAUDE.md and the section it lives in.
type TownRootStalePattern struct {
	Name        string // Human-readable name for reporting
	Substring   string // Literal substring whose presence indicates staleness
	OwningH2    string // The H2 heading whose section should be replaced when this pattern is found
	Description string // Why this pattern is bad (for FixHint / details output)
}

// TownRootStalePatterns returns substrings that, if present in the town-root
// CLAUDE.md, indicate the file carries instructions that have since been
// corrected in the embedded template. Doctor flags these as stale and Fix
// replaces the owning H2 section with the canonical version.
//
// Add new entries here when a previously documented procedure turns out to be
// wrong — the goal is that `{{cmd}} doctor --fix` automatically repairs the
// instruction across all developer machines without requiring each user to
// manually delete and recreate their CLAUDE.md.
//
// IMPORTANT: each Substring must NOT appear anywhere in the canonical
// embedded template — the TestTownRootCanonicalHasNoStalePatterns guard rail
// enforces this. Prefer the actual broken command line over prose mentions
// (the canonical may legitimately reference the bad pattern when warning
// against it).
func TownRootStalePatterns() []TownRootStalePattern {
	return []TownRootStalePattern{
		{
			// Match the actual broken command line. The canonical template
			// contains a "Do NOT use `kill -QUIT`" warning, so a generic
			// "kill -QUIT" substring would re-trigger the warning forever.
			Name:        "kill -QUIT Dolt footgun",
			Substring:   "kill -QUIT $(cat",
			OwningH2:    "## Dolt Server",
			Description: "documented kill -QUIT goroutine dump terminates Dolt 1.86.5 (incident gc-wisp-2yc7)",
		},
	}
}

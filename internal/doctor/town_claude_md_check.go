package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/templates"
)

// TownCLAUDEmdCheck verifies the town-root CLAUDE.md is up to date with
// the version embedded in the binary. This is the highest-value migration
// check — behavioral norms for agents come from CLAUDE.md.
//
// The town-root CLAUDE.md (~/gt/CLAUDE.md) is loaded by Claude Code for
// all agents running from within the town git tree (Mayor, Deacon).
// It must contain operational norms (Dolt awareness, communication hygiene,
// nudge-first) that guide agent behavior.
//
// In addition to verifying that required H2/H3 sections are present, the
// check scans for known-stale patterns (see templates.TownRootStalePatterns)
// — substrings that earlier versions of the template documented but that
// have since been corrected. Stale patterns trigger a Warning and Fix
// replaces the owning H2 section wholesale with the canonical version.
type TownCLAUDEmdCheck struct {
	FixableCheck
	missingSections []templates.TownRootRequiredSection
	stalePatterns   []templates.TownRootStalePattern
	fileMissing     bool
}

// NewTownCLAUDEmdCheck creates a new town-root CLAUDE.md version check.
func NewTownCLAUDEmdCheck() *TownCLAUDEmdCheck {
	return &TownCLAUDEmdCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "town-claude-md",
				CheckDescription: "Verify town-root CLAUDE.md is up to date with embedded version",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run checks the town-root CLAUDE.md for completeness.
func (c *TownCLAUDEmdCheck) Run(ctx *CheckContext) *CheckResult {
	c.missingSections = nil
	c.stalePatterns = nil
	c.fileMissing = false

	claudePath := filepath.Join(ctx.TownRoot, "CLAUDE.md")

	// Check if file exists
	data, err := os.ReadFile(claudePath)
	if err != nil {
		if os.IsNotExist(err) {
			c.fileMissing = true
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusError,
				Message: "Town-root CLAUDE.md is missing",
				FixHint: "Run 'gt doctor --fix' to create it from embedded template",
			}
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Cannot read town-root CLAUDE.md: %v", err),
		}
	}

	content := string(data)

	// Check for required sections
	required := templates.TownRootRequiredSections()
	var missing []templates.TownRootRequiredSection
	var details []string

	for _, section := range required {
		if !strings.Contains(content, section.Heading) {
			missing = append(missing, section)
			details = append(details, fmt.Sprintf("Missing: %s (%s)", section.Name, section.Heading))
		}
	}

	// Check for stale instructions that have been corrected upstream.
	var stale []templates.TownRootStalePattern
	for _, pattern := range templates.TownRootStalePatterns() {
		if strings.Contains(content, pattern.Substring) {
			stale = append(stale, pattern)
			details = append(details, fmt.Sprintf("Stale: %s — %s", pattern.Name, pattern.Description))
		}
	}

	if len(missing) == 0 && len(stale) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Town-root CLAUDE.md has all required sections",
		}
	}

	c.missingSections = missing
	c.stalePatterns = stale

	var msgParts []string
	if len(missing) > 0 {
		msgParts = append(msgParts, fmt.Sprintf("missing %d section(s)", len(missing)))
	}
	if len(stale) > 0 {
		msgParts = append(msgParts, fmt.Sprintf("%d stale instruction(s)", len(stale)))
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("Town-root CLAUDE.md %s", strings.Join(msgParts, ", ")),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to update sections from embedded template",
	}
}

// Fix updates the town-root CLAUDE.md with missing sections from the
// embedded template while preserving user customizations. Stale H2 sections
// are replaced wholesale (this also collapses accidental duplicate sections).
func (c *TownCLAUDEmdCheck) Fix(ctx *CheckContext) error {
	claudePath := filepath.Join(ctx.TownRoot, "CLAUDE.md")
	canonical := templates.TownRootCLAUDEmd()

	// If file is missing, create it from the canonical template
	if c.fileMissing {
		return os.WriteFile(claudePath, []byte(canonical), 0644)
	}

	if len(c.missingSections) == 0 && len(c.stalePatterns) == 0 {
		return nil
	}

	// Read current content
	data, err := os.ReadFile(claudePath)
	if err != nil {
		return fmt.Errorf("reading CLAUDE.md: %w", err)
	}
	current := string(data)

	canonicalSections := parseH2Sections(canonical)

	// First: replace any H2 sections that contain stale patterns. We replace
	// ALL occurrences of the owning H2 with a single canonical copy — this
	// also collapses duplicate sections from earlier merge mishaps.
	replacedH2 := make(map[string]bool)
	for _, pattern := range c.stalePatterns {
		if replacedH2[pattern.OwningH2] {
			continue
		}
		canonicalContent := findH2SectionContent(canonicalSections, pattern.OwningH2)
		if canonicalContent == "" {
			continue // canonical no longer has this section; leave alone
		}
		current = replaceH2Sections(current, pattern.OwningH2, canonicalContent)
		replacedH2[pattern.OwningH2] = true
	}

	// Second: append any sections that are still missing after the replace.
	// Re-derive what's missing because a replace may have introduced sections
	// that were previously absent.
	var toAppend strings.Builder
	for _, missing := range c.missingSections {
		if strings.Contains(current, missing.Heading) {
			continue
		}
		for _, cs := range canonicalSections {
			if strings.Contains(cs.content, missing.Heading) {
				toAppend.WriteString("\n")
				toAppend.WriteString(cs.content)
				break
			}
		}
	}

	if toAppend.Len() > 0 {
		if !strings.HasSuffix(current, "\n") {
			current += "\n"
		}
		current += toAppend.String()
	}

	return os.WriteFile(claudePath, []byte(current), 0644)
}

// h2Section represents a section of markdown delimited by H2 headings.
type h2Section struct {
	heading string // The H2 heading line (e.g., "## Dolt Server — Operational Awareness")
	content string // Full section content including the heading and all sub-content
}

// parseH2Sections splits markdown content into sections by H2 headings.
// The preamble (content before the first H2) is returned as a section with
// an empty heading.
func parseH2Sections(content string) []h2Section {
	var sections []h2Section
	lines := strings.Split(content, "\n")

	var currentHeading string
	var currentContent strings.Builder
	inSection := false

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			// Save previous section
			if inSection || currentContent.Len() > 0 {
				sections = append(sections, h2Section{
					heading: currentHeading,
					content: currentContent.String(),
				})
			}
			// Start new section
			currentHeading = line
			currentContent.Reset()
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
			inSection = true
		} else {
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
		}
	}

	// Save final section
	if currentContent.Len() > 0 {
		sections = append(sections, h2Section{
			heading: currentHeading,
			content: currentContent.String(),
		})
	}

	return sections
}

// findH2SectionContent returns the full content of the first H2 section in
// canonical whose heading begins with the given prefix (e.g.
// "## Dolt Server"). Returns "" if no match is found.
func findH2SectionContent(canonical []h2Section, headingPrefix string) string {
	for _, cs := range canonical {
		if strings.HasPrefix(cs.heading, headingPrefix) {
			return cs.content
		}
	}
	return ""
}

// replaceH2Sections replaces every H2 section in content whose heading begins
// with headingPrefix with replacement (the canonical section content). The
// first occurrence is substituted in place; subsequent occurrences are
// removed entirely so duplicate-section pile-ups collapse into one.
func replaceH2Sections(content, headingPrefix, replacement string) string {
	sections := parseH2Sections(content)
	var rebuilt strings.Builder
	replaced := false
	for _, sec := range sections {
		if strings.HasPrefix(sec.heading, headingPrefix) {
			if !replaced {
				rebuilt.WriteString(replacement)
				replaced = true
			}
			// drop subsequent matches
			continue
		}
		rebuilt.WriteString(sec.content)
	}
	// If the section did not exist (e.g. file had no H2 with this prefix),
	// append the canonical version so the fix still adds the correct text.
	if !replaced {
		out := rebuilt.String()
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		return out + replacement
	}
	return rebuilt.String()
}

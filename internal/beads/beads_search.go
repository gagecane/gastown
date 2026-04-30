// Package beads provides search and duplicate-detection helpers.
package beads

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SearchOptions specifies options for searching issues.
type SearchOptions struct {
	Query        string // Text query to search titles and descriptions
	Status       string // "open", "closed", "all"
	Label        string // Label filter (e.g., "gt:bug")
	Limit        int    // Max results (0 = default)
	DescContains string // Filter by description substring
}

// Search searches issues by text query across title, description, and ID.
func (b *Beads) Search(opts SearchOptions) ([]*Issue, error) {
	if b.store != nil {
		return b.storeSearch(opts)
	}

	args := []string{"search", "--json"}

	if opts.Query != "" {
		args = append(args, opts.Query)
	}
	if opts.Status != "" {
		args = append(args, "--status="+opts.Status)
	}
	if opts.Label != "" {
		args = append(args, "--label="+opts.Label)
	}
	if opts.Limit > 0 {
		args = append(args, fmt.Sprintf("--limit=%d", opts.Limit))
	}
	if opts.DescContains != "" {
		args = append(args, "--desc-contains="+opts.DescContains)
	}

	out, err := b.run(args...)
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd search output: %w", err)
	}

	return issues, nil
}

// FindOpenBugsByTitle searches for existing open bugs with titles similar to the given title.
// Used for duplicate detection before filing new test-failure bugs.
// Returns matching issues sorted by relevance (best match first).
func (b *Beads) FindOpenBugsByTitle(title string) ([]*Issue, error) {
	// Extract key terms from the title for searching.
	// Test failure titles typically contain the test name or error description.
	issues, err := b.Search(SearchOptions{
		Query:  title,
		Status: "open",
		Label:  "gt:bug",
		Limit:  10,
	})
	if err != nil {
		return nil, fmt.Errorf("searching for duplicate bugs: %w", err)
	}

	return issues, nil
}

// CreateIfNoDuplicate creates a new bug only if no existing open bug has a similar title.
// If a duplicate is found, it returns the existing issue and a nil error.
// The returned bool is true if a new issue was created, false if an existing duplicate was found.
func (b *Beads) CreateIfNoDuplicate(opts CreateOptions) (*Issue, bool, error) {
	if opts.Title == "" {
		return nil, false, fmt.Errorf("title is required for duplicate detection")
	}

	// Search for existing open bugs with similar titles
	existing, err := b.FindOpenBugsByTitle(opts.Title)
	if err != nil {
		// If search fails, fall through to create (fail-open)
		issue, createErr := b.Create(opts)
		if createErr != nil {
			return nil, false, createErr
		}
		return issue, true, nil
	}

	// Check for title similarity using normalized comparison
	normalizedTitle := normalizeBugTitle(opts.Title)
	for _, issue := range existing {
		if normalizeBugTitle(issue.Title) == normalizedTitle {
			// Exact normalized match — this is a duplicate
			return issue, false, nil
		}
	}

	// No duplicate found, create the new bug
	issue, err := b.Create(opts)
	if err != nil {
		return nil, false, err
	}
	return issue, true, nil
}

// normalizeBugTitle normalizes a bug title for duplicate comparison.
// Strips common prefixes, whitespace, and case differences so that
// "Pre-existing failure: test_foo fails" matches "pre-existing failure: test_foo fails".
func normalizeBugTitle(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	// Strip common prefixes that the refinery adds
	for _, prefix := range []string{"pre-existing failure: ", "pre-existing: ", "test failure: "} {
		t = strings.TrimPrefix(t, prefix)
	}
	return t
}

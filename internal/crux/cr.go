package crux

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// CRReviewURLHost is the canonical host for CRUX code reviews.
const CRReviewURLHost = "code.amazon.com"

// crIDPattern matches CRUX code review URLs and bare CR IDs in commit messages.
// Examples:
//
//	https://code.amazon.com/reviews/CR-12345678
//	code.amazon.com/reviews/CR-12345678
//	CR-12345678
//
// The regex captures the numeric portion after "CR-".
var crIDPattern = regexp.MustCompile(`CR-(\d+)`)

// ExtractCRID scans text for the first CRUX CR identifier and returns
// its numeric portion. Returns 0 if no CR ID is present.
//
// Callers typically pass git commit messages or PR/CR trailers. Only
// the first match is returned — downstream operations like `cr --update`
// work on a single review.
func ExtractCRID(text string) int {
	matches := crIDPattern.FindStringSubmatch(text)
	if len(matches) < 2 {
		return 0
	}
	id, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0
	}
	return id
}

// FormatCRID returns the display form of a numeric CR ID, e.g. 12345678 → "CR-12345678".
// Returns an empty string for non-positive IDs.
func FormatCRID(id int) string {
	if id <= 0 {
		return ""
	}
	return fmt.Sprintf("CR-%d", id)
}

// BuildCreateArgs returns the command-line arguments for `cr` to create or
// update a review in the refinery's auto-merge workflow.
//
// Inputs:
//   - crID: existing CR ID (0 if creating a new review)
//   - destBranch: destination branch on the remote (e.g., "mainline")
//   - summary, description: review metadata
//
// The arguments always include --publish and --auto-merge so that the
// review is created in a state that CRUX will merge automatically as
// soon as required approvals land.
func BuildCreateArgs(crID int, destBranch, summary, description string) []string {
	args := []string{}
	if crID > 0 {
		args = append(args, "--update-review", FormatCRID(crID))
	} else {
		args = append(args, "--new-review")
	}
	if destBranch != "" {
		args = append(args, "--destination-branch", destBranch)
	}
	if summary != "" {
		args = append(args, "--summary", summary)
	}
	if description != "" {
		args = append(args, "--description", description)
	}
	args = append(args,
		"--publish",
		"--auto-merge",
		"--no-open",
		"--no-amend",
	)
	return args
}

// SummarizeCreateOutput extracts a CR ID from `cr` command output.
// CRUX prints the review URL (code.amazon.com/reviews/CR-XXXX) on success;
// this helper reuses ExtractCRID on the combined output.
//
// Returns 0 if no CR ID can be recovered. Callers should treat this as
// "unknown" rather than a hard failure — the review may still have been
// created or updated successfully.
func SummarizeCreateOutput(output string) int {
	// Trim to a reasonable window so extremely long outputs don't dominate
	// memory; CR URLs are printed near the top/bottom of the transcript.
	if len(output) > 64*1024 {
		output = output[:64*1024] + "\n" + output[len(output)-32*1024:]
	}
	output = strings.TrimSpace(output)
	return ExtractCRID(output)
}

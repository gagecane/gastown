package cmd

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

const (
	convoyStatusOpen           = "open"
	convoyStatusClosed         = "closed"
	convoyStatusStagedReady    = "staged_ready"
	convoyStatusStagedWarnings = "staged_warnings"
)

func normalizeConvoyStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func ensureKnownConvoyStatus(status string) error {
	switch normalizeConvoyStatus(status) {
	case convoyStatusOpen, convoyStatusClosed, convoyStatusStagedReady, convoyStatusStagedWarnings:
		return nil
	default:
		return fmt.Errorf(
			"unsupported convoy status %q (expected %q, %q, %q, or %q)",
			status,
			convoyStatusOpen,
			convoyStatusClosed,
			convoyStatusStagedReady,
			convoyStatusStagedWarnings,
		)
	}
}

// isStagedStatus reports whether the given normalized status is a staged status.
func isStagedStatus(status string) bool {
	return strings.HasPrefix(status, "staged_")
}

func validateConvoyStatusTransition(currentStatus, targetStatus string) error {
	current := normalizeConvoyStatus(currentStatus)
	target := normalizeConvoyStatus(targetStatus)

	if err := ensureKnownConvoyStatus(current); err != nil {
		return err
	}
	if err := ensureKnownConvoyStatus(target); err != nil {
		return err
	}
	if current == target {
		return nil
	}

	// Original open ↔ closed transitions.
	if (current == convoyStatusOpen && target == convoyStatusClosed) ||
		(current == convoyStatusClosed && target == convoyStatusOpen) {
		return nil
	}

	// Staged → open (launch) and staged → closed (cancel) are allowed.
	if isStagedStatus(current) && (target == convoyStatusOpen || target == convoyStatusClosed) {
		return nil
	}

	// Staged ↔ staged transitions (re-stage with different result).
	if isStagedStatus(current) && isStagedStatus(target) {
		return nil
	}

	// REJECT: open → staged_* and closed → staged_* are not allowed.
	// (Falls through to the error below.)

	return fmt.Errorf("illegal convoy status transition %q -> %q", currentStatus, targetStatus)
}

// hasLabel checks if a label exists in a list of labels.
func hasLabel(labels []string, target string) bool { //nolint:unparam // target is always "gt:owned" today but the API is intentionally general
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// convoyMergeFromFields extracts the merge strategy from a convoy description
// using the typed ConvoyFields accessor.
// Returns the strategy string ("direct", "mr", "local") or empty string if not set.
func convoyMergeFromFields(description string) string {
	fields := beads.ParseConvoyFields(&beads.Issue{Description: description})
	if fields == nil {
		return ""
	}
	return fields.Merge
}

// formatYesNo returns "yes" or "no" for a boolean value.
func formatYesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func formatConvoyStatus(status string) string {
	switch status {
	case "open":
		return style.Warning.Render("●")
	case "closed":
		return style.Success.Render("✓")
	case "in_progress":
		return style.Info.Render("→")
	default:
		return status
	}
}

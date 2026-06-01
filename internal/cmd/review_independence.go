package cmd

import (
	"bufio"
	"fmt"
	"strings"
)

// Builder-independence guard (gs-am8 GAP 3 / gs-aoz). A review gate — a bead
// whose job is to adversarially review another bead's work — must never be
// acquired by the same agent that BUILT the work under review. Otherwise the
// builder reviews its own work and the review is worthless (observed: polecat
// 'capable' built lb-yuhl, then re-grabbed its own review gate lb-0kdn).
//
// Review gates are marked STRUCTURALLY, not by a formula-name heuristic:
//   - label  gt:review-gate        marks the bead as a review gate
//   - field  reviews: <build-bead> (a "key: value" line in the description)
//     names the bead whose work is under review
//
// The guard runs on the ACQUISITION path (wherever an agent claims the bead),
// because the violation came from an EXISTING polecat re-grabbing the gate — a
// guard only at fresh dispatch (a freshly-spawned polecat has a new name that
// can never equal the builder) would miss it.

const (
	// labelReviewGate marks a bead as an adversarial review gate.
	labelReviewGate = "gt:review-gate"
	// reviewsFieldKey is the description field naming the reviewed build bead.
	reviewsFieldKey = "reviews"
)

// reviewGateReviewedBead returns the ID of the build bead under review when
// (labels, description) describe a review gate, or "" otherwise. A bead is a
// review gate only when it carries BOTH the gt:review-gate label AND a
// `reviews: <id>` field — the label alone (no target) cannot identify a
// builder to exclude, so it is treated as not-a-gate (fail-open).
func reviewGateReviewedBead(labels []string, description string) string {
	if !hasLabel(labels, labelReviewGate) {
		return ""
	}
	return parseReviewsField(description)
}

// parseReviewsField extracts the `reviews: <bead-id>` value from a bead
// description (a "key: value" line, matching the convoy/attachment field
// style). Returns "" if absent.
func parseReviewsField(description string) string {
	scanner := bufio.NewScanner(strings.NewReader(description))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.ToLower(strings.TrimSpace(key)) == reviewsFieldKey {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// sameAgent reports whether two agent identifiers refer to the same agent,
// tolerating trailing-slash and case differences (e.g. "gastown/polecats/capable"
// vs "gastown/polecats/Capable/"). Empty identifiers never match — an unknown
// builder must not block dispatch (fail-open).
func sameAgent(a, b string) bool {
	na := strings.ToLower(strings.Trim(strings.TrimSpace(a), "/"))
	nb := strings.ToLower(strings.Trim(strings.TrimSpace(b), "/"))
	if na == "" || nb == "" {
		return false
	}
	return na == nb
}

// violatesBuilderIndependence reports whether acquiringAgent building the
// reviewed work would violate independence. Pure decision split out for
// testing: it is the builder-equals-reviewer check, with the fail-open rules
// (no reviewed bead, no builder, no acquirer → not a violation).
func violatesBuilderIndependence(builder, acquiringAgent string) bool {
	return sameAgent(builder, acquiringAgent)
}

// assertReviewerIndependence refuses to let acquiringAgent take beadID when
// beadID is a review gate and acquiringAgent built the work under review
// (gs-aoz). It fails open on every uncertainty — not a review gate, no reviewed
// bead recorded, or an undeterminable builder — so it can only ever BLOCK a
// genuine builder-reviews-own-work case, never stall legitimate dispatch.
func assertReviewerIndependence(townRoot, beadID string, labels []string, description, acquiringAgent string) error {
	reviewed := reviewGateReviewedBead(labels, description)
	if reviewed == "" {
		return nil // not a review gate (or no target recorded) — nothing to enforce
	}
	builder := builderOfReviewedWork(townRoot, reviewed)
	if builder == "" {
		return nil // can't determine the builder — fail open
	}
	if violatesBuilderIndependence(builder, acquiringAgent) {
		return fmt.Errorf(
			"refusing to assign review gate %s to %s: it built the work under review (%s) — a review gate must be worked by an independent agent.\nReassign to a different agent, or remove the %s label if %s is not a review gate",
			beadID, acquiringAgent, reviewed, labelReviewGate, beadID)
	}
	return nil
}

// builderOfReviewedWork returns the agent that produced reviewedBeadID — its
// assignee (the worker the build was dispatched to). Returns "" when the bead
// can't be read or carries no assignee, so the guard fails open.
func builderOfReviewedWork(townRoot, reviewedBeadID string) string {
	info, err := getBeadInfoFromTownRoot(townRoot, reviewedBeadID)
	if err != nil || info == nil {
		return ""
	}
	return info.Assignee
}

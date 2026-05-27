// Branch GC and attachment-bead retention patrol for auto-test-pr.
//
// Phase 0 task 9 (gu-gw35) — Standing patrol with two responsibilities:
//
// (a) Branch GC: list refs/heads/auto-test/*/*  branches across opted-in
//
//	rigs, cross-reference against each rig's state bead and open MRs,
//	delete branches >7 days old with no associated open MR or in-flight
//	bead.
//
// (b) Attachment-bead retention (OQ4 fallback): list attachment beads via
//
//	the gt:auto-test-pr-attachment label query and CLOSE (not delete)
//	attachments outside their retention window:
//	- kind:transition at age > 60d
//	- kind:rejection at cooldown_until + 30d < now
//
// Design context:
//   - .designs/auto-test-pr/synthesis.md §"Phase 0 task 9"
//   - .designs/auto-test-pr/synthesis.md §"OQ4 fallback"
//   - internal/formula/formulas/mol-auto-test-pr-branch-gc.formula.toml
package autotestpr

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	gitpkg "github.com/steveyegge/gastown/internal/git"
)

// BranchGCConfig holds configuration for the branch-GC patrol cycle.
type BranchGCConfig struct {
	// DryRun controls whether deletion candidates are reported only
	// (true) or actually deleted from origin (false).
	DryRun bool

	// StaleDays is the age threshold in days. Branches with tip commit
	// older than this AND no skip condition are deletion candidates.
	StaleDays int

	// BranchPrefix is the branch namespace to scan (e.g., "auto-test/").
	// Must remain anchored per C-SEC-6.
	BranchPrefix string

	// AutoTestLabel is the bead label that identifies auto-test-pr
	// MR/cycle beads.
	AutoTestLabel string

	// MaxDeletesPerCycle is a safety valve — stop deleting after this
	// many candidates in one cycle.
	MaxDeletesPerCycle int

	// Now is the reference time for staleness calculations. Defaults to
	// time.Now() if zero.
	Now time.Time
}

// DefaultBranchGCConfig returns the default configuration matching the
// formula's documented variable defaults.
func DefaultBranchGCConfig() BranchGCConfig {
	return BranchGCConfig{
		DryRun:             true,
		StaleDays:          7,
		BranchPrefix:       "auto-test/",
		AutoTestLabel:      "gt:auto-test-pr",
		MaxDeletesPerCycle: 50,
	}
}

// BranchCandidate is a branch found during the scan phase.
type BranchCandidate struct {
	Rig       string // Rig name (e.g., "gastown_upstream")
	Ref       string // Branch ref without remote prefix (e.g., "auto-test/gastown_upstream/gu-abc")
	BeadID    string // Bead-id segment parsed from the branch name
	Timestamp int64  // Unix timestamp of the branch tip commit
}

// ClassifiedBranch is the result of classifying a candidate.
type ClassifiedBranch struct {
	BranchCandidate
	Keep   bool   // true = skip (do not delete), false = delete candidate
	Reason string // Human-readable reason for keep/delete decision
}

// BranchGCResult is the output of a complete GC cycle.
type BranchGCResult struct {
	Kept    []ClassifiedBranch
	Deleted []ClassifiedBranch
	Errors  []string // Non-fatal errors encountered during the cycle
}

// BranchGCRunner executes the branch-GC patrol logic.
type BranchGCRunner struct {
	Config BranchGCConfig
	Beads  *beads.Beads
}

// now returns the reference time for this run (config.Now or real clock).
func (r *BranchGCRunner) now() time.Time {
	if !r.Config.Now.IsZero() {
		return r.Config.Now
	}
	return time.Now()
}

// ClassifyBranches applies the four skip conditions to each candidate
// and returns keep/delete classifications. The skip conditions are:
//
//  1. Branch tip younger than stale threshold
//  2. Open MR bead with auto-test label for this bead-id
//  3. Bead-id is an open bead (dispatch bead in-flight)
//  4. Rig state bead names this bead-id in an in-flight cycle
//
// Condition ordering follows the formula: age check first (cheap, no
// bead lookup), then bead queries for surviving candidates.
func (r *BranchGCRunner) ClassifyBranches(candidates []BranchCandidate) []ClassifiedBranch {
	now := r.now()
	cutoff := now.Add(-time.Duration(r.Config.StaleDays) * 24 * time.Hour)
	cutoffUnix := cutoff.Unix()

	results := make([]ClassifiedBranch, 0, len(candidates))

	for _, c := range candidates {
		classified := ClassifiedBranch{BranchCandidate: c}

		// Skip condition 1: branch tip younger than stale threshold
		if c.Timestamp > cutoffUnix {
			ageDays := (now.Unix() - c.Timestamp) / 86400
			classified.Keep = true
			classified.Reason = fmt.Sprintf("young (%dd < %dd)", ageDays, r.Config.StaleDays)
			results = append(results, classified)
			continue
		}

		// Skip condition 2: open MR bead with auto-test label for this bead-id
		if r.hasOpenMRBead(c.BeadID) {
			classified.Keep = true
			classified.Reason = fmt.Sprintf("open MR/cycle bead %s", c.BeadID)
			results = append(results, classified)
			continue
		}

		// Skip condition 3: bead-id is an open bead
		if status := r.beadStatus(c.BeadID); isActiveStatus(status) {
			classified.Keep = true
			classified.Reason = fmt.Sprintf("cycle bead %s still %s", c.BeadID, status)
			results = append(results, classified)
			continue
		}

		// Skip condition 4: rig state bead names this bead-id in-flight
		if r.isInFlightInRigState(c.Rig, c.BeadID) {
			classified.Keep = true
			classified.Reason = fmt.Sprintf("rig state bead %s-auto-test-state reports cycle in flight", c.Rig)
			results = append(results, classified)
			continue
		}

		// No skip condition matched — mark for deletion
		ageDays := (now.Unix() - c.Timestamp) / 86400
		classified.Keep = false
		classified.Reason = fmt.Sprintf("%dd", ageDays)
		results = append(results, classified)
	}

	return results
}

// DeleteStaleBranches executes deletion (or dry-run reporting) of
// classified delete-candidates. Returns the result with final counts.
func (r *BranchGCRunner) DeleteStaleBranches(classified []ClassifiedBranch, rigRepos map[string]*gitpkg.Git) *BranchGCResult {
	result := &BranchGCResult{}

	for _, c := range classified {
		if c.Keep {
			result.Kept = append(result.Kept, c)
		} else {
			result.Deleted = append(result.Deleted, c)
		}
	}

	if r.Config.DryRun {
		return result
	}

	// Actually delete, capped by MaxDeletesPerCycle
	deleted := 0
	for i, c := range result.Deleted {
		if deleted >= r.Config.MaxDeletesPerCycle {
			result.Errors = append(result.Errors,
				fmt.Sprintf("max_deletes_per_cycle (%d) reached — stopping at candidate %d/%d",
					r.Config.MaxDeletesPerCycle, i, len(result.Deleted)))
			break
		}

		g, ok := rigRepos[c.Rig]
		if !ok {
			result.Errors = append(result.Errors,
				fmt.Sprintf("cannot delete %s/%s — no git repo for rig", c.Rig, c.Ref))
			continue
		}

		if err := g.DeleteRemoteBranch("origin", c.Ref); err != nil {
			result.Errors = append(result.Errors,
				fmt.Sprintf("push --delete failed for %s/%s: %v", c.Rig, c.Ref, err))
			continue
		}
		deleted++
	}

	return result
}

// ListBranchesForRig lists auto-test/* branches on a rig's origin remote
// and returns candidates. Uses git for-each-ref on remotes/origin/<prefix>*.
func ListBranchesForRig(g *gitpkg.Git, rigName, branchPrefix string) ([]BranchCandidate, error) {
	// Run for-each-ref to get branch refs with committer timestamps
	refPattern := fmt.Sprintf("refs/remotes/origin/%s*", branchPrefix)
	output, err := g.ForEachRef(refPattern, "%(refname:short)\t%(committerdate:unix)")
	if err != nil {
		return nil, fmt.Errorf("for-each-ref %s: %w", refPattern, err)
	}

	if strings.TrimSpace(output) == "" {
		return nil, nil
	}

	var candidates []BranchCandidate
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}

		short := parts[0] // e.g., "origin/auto-test/gastown_upstream/gu-abc"
		tsStr := parts[1]

		// Strip "origin/" prefix to get the bare ref
		ref := strings.TrimPrefix(short, "origin/")

		// Parse bead-id: last segment of the branch path
		segments := strings.Split(ref, "/")
		if len(segments) < 3 {
			continue // Malformed: need at least prefix/rig/bead-id
		}
		beadID := segments[len(segments)-1]

		ts, err := strconv.ParseInt(tsStr, 10, 64)
		if err != nil {
			continue // Skip unparseable timestamps
		}

		candidates = append(candidates, BranchCandidate{
			Rig:       rigName,
			Ref:       ref,
			BeadID:    beadID,
			Timestamp: ts,
		})
	}

	return candidates, nil
}

// hasOpenMRBead checks if there's an open MR bead with the auto-test
// label matching the given bead-id.
func (r *BranchGCRunner) hasOpenMRBead(beadID string) bool {
	if r.Beads == nil {
		return false
	}

	issues, err := r.Beads.List(beads.ListOptions{
		Label:  r.Config.AutoTestLabel,
		Status: "open",
	})
	if err != nil {
		return false // fail-open: if we can't query, don't skip
	}

	for _, iss := range issues {
		if iss.ID == beadID {
			return true
		}
	}
	return false
}

// beadStatus returns the status of a bead by ID, or empty string on error.
func (r *BranchGCRunner) beadStatus(beadID string) string {
	if r.Beads == nil {
		return ""
	}

	iss, err := r.Beads.Show(beadID)
	if err != nil {
		return ""
	}
	return iss.Status
}

// isActiveStatus reports whether a bead status represents an in-flight
// state that should prevent branch deletion.
func isActiveStatus(status string) bool {
	switch status {
	case "open", "in_progress", "hooked", "blocked":
		return true
	}
	return false
}

// isInFlightInRigState checks whether the rig's state bead has this
// bead-id as its current in-flight cycle.
func (r *BranchGCRunner) isInFlightInRigState(rig, beadID string) bool {
	if r.Beads == nil {
		return false
	}

	stateBeadID := rig + "-auto-test-state"
	iss, err := r.Beads.Show(stateBeadID)
	if err != nil {
		return false
	}

	if len(iss.Metadata) == 0 {
		return false
	}

	// Parse metadata to find current_cycle.bead_id
	var meta struct {
		CurrentCycle struct {
			BeadID string `json:"bead_id"`
			State  string `json:"state"`
		} `json:"current_cycle"`
	}
	if err := json.Unmarshal(iss.Metadata, &meta); err != nil {
		return false
	}

	if meta.CurrentCycle.BeadID != beadID {
		return false
	}

	// Only consider in-flight states
	switch meta.CurrentCycle.State {
	case "picking", "dispatched", "mr-pending", "mr-revising":
		return true
	}
	return false
}

// --- Attachment-Bead Retention ---

// AttachmentRetentionConfig holds configuration for the attachment-bead
// retention patrol.
type AttachmentRetentionConfig struct {
	// TransitionRetentionDays is the age (from `at` timestamp) after
	// which transition attachment beads are closed. Default: 60.
	TransitionRetentionDays int

	// RejectionRetentionDays is the number of days after `cooldown_until`
	// at which rejection attachment beads are closed. Default: 30.
	RejectionRetentionDays int

	// AttachmentLabel is the umbrella discriminator for attachment beads.
	AttachmentLabel string

	// Now is the reference time. Defaults to time.Now() if zero.
	Now time.Time
}

// DefaultAttachmentRetentionConfig returns defaults matching the design doc.
func DefaultAttachmentRetentionConfig() AttachmentRetentionConfig {
	return AttachmentRetentionConfig{
		TransitionRetentionDays: 60,
		RejectionRetentionDays:  30,
		AttachmentLabel:         "gt:auto-test-pr-attachment",
	}
}

// AttachmentRetentionResult holds the outcome of the retention patrol.
type AttachmentRetentionResult struct {
	Closed []AttachmentClosure // Beads that were closed (or would be in dry-run)
	Kept   []string            // Bead IDs that remain open (within retention)
	Errors []string            // Non-fatal errors
}

// AttachmentClosure records why a specific attachment bead was closed.
type AttachmentClosure struct {
	BeadID string
	Kind   string // "transition" or "rejection"
	Reason string // e.g., "age 90d > 60d retention"
}

// AttachmentRetentionRunner executes the retention patrol.
type AttachmentRetentionRunner struct {
	Config AttachmentRetentionConfig
	Beads  *beads.Beads
	DryRun bool
}

// now returns the reference time.
func (r *AttachmentRetentionRunner) now() time.Time {
	if !r.Config.Now.IsZero() {
		return r.Config.Now
	}
	return time.Now()
}

// Run executes the attachment-bead retention patrol. Lists all open
// attachment beads, applies retention rules, and closes those outside
// their window. Never deletes — closed beads remain readable for audit.
func (r *AttachmentRetentionRunner) Run() (*AttachmentRetentionResult, error) {
	if r.Beads == nil {
		return nil, fmt.Errorf("AttachmentRetentionRunner: nil beads")
	}

	result := &AttachmentRetentionResult{}
	now := r.now()

	// List all open attachment beads
	issues, err := r.Beads.List(beads.ListOptions{
		Label:  r.Config.AttachmentLabel,
		Status: "open",
	})
	if err != nil {
		return nil, fmt.Errorf("listing attachment beads: %w", err)
	}

	for _, iss := range issues {
		closure := r.evaluateAttachment(iss, now)
		if closure == nil {
			result.Kept = append(result.Kept, iss.ID)
			continue
		}

		result.Closed = append(result.Closed, *closure)

		if !r.DryRun {
			reason := fmt.Sprintf("retention-gc: %s", closure.Reason)
			if err := r.Beads.CloseWithReason(reason, iss.ID); err != nil {
				result.Errors = append(result.Errors,
					fmt.Sprintf("failed to close %s: %v", iss.ID, err))
			}
		}
	}

	return result, nil
}

// evaluateAttachment determines whether an attachment bead should be
// closed based on its kind and retention rules. Returns nil if the bead
// should remain open.
func (r *AttachmentRetentionRunner) evaluateAttachment(iss *beads.Issue, now time.Time) *AttachmentClosure {
	if iss == nil {
		return nil
	}

	isTransition := beads.HasLabel(iss, "kind:transition")
	isRejection := beads.HasLabel(iss, "kind:rejection")

	if !isTransition && !isRejection {
		return nil // Unknown kind — leave alone
	}

	if isTransition {
		return r.evaluateTransition(iss, now)
	}
	return r.evaluateRejection(iss, now)
}

// evaluateTransition checks if a transition attachment is past the 60d
// retention window. The `at` field in metadata determines age.
func (r *AttachmentRetentionRunner) evaluateTransition(iss *beads.Issue, now time.Time) *AttachmentClosure {
	at := r.parseTransitionAt(iss)
	if at.IsZero() {
		// If we can't determine `at`, fall back to CreatedAt
		at = parseBeadTime(iss.CreatedAt)
	}
	if at.IsZero() {
		return nil // Can't determine age — leave open
	}

	retentionCutoff := now.Add(-time.Duration(r.Config.TransitionRetentionDays) * 24 * time.Hour)
	if at.Before(retentionCutoff) {
		ageDays := int(now.Sub(at).Hours() / 24)
		return &AttachmentClosure{
			BeadID: iss.ID,
			Kind:   "transition",
			Reason: fmt.Sprintf("age %dd > %dd retention", ageDays, r.Config.TransitionRetentionDays),
		}
	}
	return nil
}

// evaluateRejection checks if a rejection attachment is past
// cooldown_until + 30d. The `cooldown_until` field in metadata
// determines the retention deadline.
func (r *AttachmentRetentionRunner) evaluateRejection(iss *beads.Issue, now time.Time) *AttachmentClosure {
	cooldownUntil := r.parseCooldownUntil(iss)
	if cooldownUntil.IsZero() {
		return nil // Can't determine cooldown — leave open
	}

	retentionCutoff := cooldownUntil.Add(time.Duration(r.Config.RejectionRetentionDays) * 24 * time.Hour)
	if now.After(retentionCutoff) {
		daysPast := int(now.Sub(retentionCutoff).Hours() / 24)
		return &AttachmentClosure{
			BeadID: iss.ID,
			Kind:   "rejection",
			Reason: fmt.Sprintf("cooldown_until + %dd elapsed (%dd past)",
				r.Config.RejectionRetentionDays, daysPast),
		}
	}
	return nil
}

// parseTransitionAt extracts the `at` timestamp from a transition
// attachment's metadata.
func (r *AttachmentRetentionRunner) parseTransitionAt(iss *beads.Issue) time.Time {
	if len(iss.Metadata) == 0 {
		// Fall back to description for the OQ4 spike-style test beads
		// that store metadata as the Description field.
		return r.parseTransitionAtFromDescription(iss.Description)
	}

	var meta struct {
		At string `json:"at"`
	}
	if err := json.Unmarshal(iss.Metadata, &meta); err != nil {
		return r.parseTransitionAtFromDescription(iss.Description)
	}
	if meta.At == "" {
		return r.parseTransitionAtFromDescription(iss.Description)
	}
	t, _ := time.Parse(time.RFC3339, meta.At)
	return t
}

// parseTransitionAtFromDescription extracts the `at` timestamp from
// a JSON description (used by OQ4 spike-style tests that store
// metadata as the bead description).
func (r *AttachmentRetentionRunner) parseTransitionAtFromDescription(desc string) time.Time {
	if desc == "" {
		return time.Time{}
	}
	var meta struct {
		At string `json:"at"`
	}
	if err := json.Unmarshal([]byte(desc), &meta); err != nil {
		return time.Time{}
	}
	if meta.At == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, meta.At)
	return t
}

// parseCooldownUntil extracts the `cooldown_until` timestamp from a
// rejection attachment's metadata.
func (r *AttachmentRetentionRunner) parseCooldownUntil(iss *beads.Issue) time.Time {
	if len(iss.Metadata) == 0 {
		return r.parseCooldownUntilFromDescription(iss.Description)
	}

	var meta struct {
		CooldownUntil string `json:"cooldown_until"`
	}
	if err := json.Unmarshal(iss.Metadata, &meta); err != nil {
		return r.parseCooldownUntilFromDescription(iss.Description)
	}
	if meta.CooldownUntil == "" {
		return r.parseCooldownUntilFromDescription(iss.Description)
	}
	t, _ := time.Parse(time.RFC3339, meta.CooldownUntil)
	return t
}

// parseCooldownUntilFromDescription extracts the `cooldown_until`
// timestamp from a JSON description.
func (r *AttachmentRetentionRunner) parseCooldownUntilFromDescription(desc string) time.Time {
	if desc == "" {
		return time.Time{}
	}
	var meta struct {
		CooldownUntil string `json:"cooldown_until"`
	}
	if err := json.Unmarshal([]byte(desc), &meta); err != nil {
		return time.Time{}
	}
	if meta.CooldownUntil == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, meta.CooldownUntil)
	return t
}

// parseBeadTime parses a bead timestamp string (various formats).
func parseBeadTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	// Try common formats
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

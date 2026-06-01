// Package beads provides escalation bead management.
package beads

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// EscalationFields holds structured fields for escalation beads.
// These are stored as "key: value" lines in the description.
type EscalationFields struct {
	Severity          string // critical, high, medium, low
	Reason            string // Why this was escalated
	Source            string // Source identifier (e.g., plugin:rebuild-gt, patrol:deacon)
	EscalatedBy       string // Agent address that escalated (e.g., "gastown/Toast")
	EscalatedAt       string // ISO 8601 timestamp
	AckedBy           string // Agent that acknowledged (empty if not acked)
	AckedAt           string // When acknowledged (empty if not acked)
	ClosedBy          string // Agent that closed (empty if not closed)
	ClosedReason      string // Resolution reason (empty if not closed)
	RelatedBead       string // Optional: related bead ID (task, bug, etc.)
	OriginalSeverity  string // Original severity before any re-escalation
	ReescalationCount int    // Number of times severity was bumped due to staleness
	LastReescalatedAt string // When last re-escalated (empty if never)
	LastReescalatedBy string // Who last re-escalated (empty if never)
	// Dedup fields: track recurring alerts without creating N beads per cycle.
	Signature        string // Stable dedup key for recurring alerts (e.g., "main_branch_test")
	OccurrenceCount  int    // How many times this same alert has fired without resolution
	LastOccurrenceAt string // When this alert last fired again (empty on first occurrence)
	Fingerprint      string // Stable duplicate-suppression label (upstream)
}

// FormatEscalationDescription creates a description string from escalation fields.
func FormatEscalationDescription(title string, fields *EscalationFields) string {
	if fields == nil {
		return title
	}

	var lines []string
	lines = append(lines, title)
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("severity: %s", fields.Severity))
	lines = append(lines, fmt.Sprintf("reason: %s", fields.Reason))
	if fields.Source != "" {
		lines = append(lines, fmt.Sprintf("source: %s", fields.Source))
	} else {
		lines = append(lines, "source: null")
	}
	lines = append(lines, fmt.Sprintf("escalated_by: %s", fields.EscalatedBy))
	lines = append(lines, fmt.Sprintf("escalated_at: %s", fields.EscalatedAt))

	if fields.AckedBy != "" {
		lines = append(lines, fmt.Sprintf("acked_by: %s", fields.AckedBy))
	} else {
		lines = append(lines, "acked_by: null")
	}

	if fields.AckedAt != "" {
		lines = append(lines, fmt.Sprintf("acked_at: %s", fields.AckedAt))
	} else {
		lines = append(lines, "acked_at: null")
	}

	if fields.ClosedBy != "" {
		lines = append(lines, fmt.Sprintf("closed_by: %s", fields.ClosedBy))
	} else {
		lines = append(lines, "closed_by: null")
	}

	if fields.ClosedReason != "" {
		lines = append(lines, fmt.Sprintf("closed_reason: %s", fields.ClosedReason))
	} else {
		lines = append(lines, "closed_reason: null")
	}

	if fields.RelatedBead != "" {
		lines = append(lines, fmt.Sprintf("related_bead: %s", fields.RelatedBead))
	} else {
		lines = append(lines, "related_bead: null")
	}

	// Reescalation fields
	if fields.OriginalSeverity != "" {
		lines = append(lines, fmt.Sprintf("original_severity: %s", fields.OriginalSeverity))
	} else {
		lines = append(lines, "original_severity: null")
	}
	lines = append(lines, fmt.Sprintf("reescalation_count: %d", fields.ReescalationCount))
	if fields.LastReescalatedAt != "" {
		lines = append(lines, fmt.Sprintf("last_reescalated_at: %s", fields.LastReescalatedAt))
	} else {
		lines = append(lines, "last_reescalated_at: null")
	}
	if fields.LastReescalatedBy != "" {
		lines = append(lines, fmt.Sprintf("last_reescalated_by: %s", fields.LastReescalatedBy))
	} else {
		lines = append(lines, "last_reescalated_by: null")
	}
	if fields.Fingerprint != "" {
		lines = append(lines, fmt.Sprintf("fingerprint: %s", fields.Fingerprint))
	} else {
		lines = append(lines, "fingerprint: null")
	}

	// Dedup fields
	if fields.Signature != "" {
		lines = append(lines, fmt.Sprintf("signature: %s", fields.Signature))
	} else {
		lines = append(lines, "signature: null")
	}
	lines = append(lines, fmt.Sprintf("occurrence_count: %d", fields.OccurrenceCount))
	if fields.LastOccurrenceAt != "" {
		lines = append(lines, fmt.Sprintf("last_occurrence_at: %s", fields.LastOccurrenceAt))
	} else {
		lines = append(lines, "last_occurrence_at: null")
	}

	return strings.Join(lines, "\n")
}

// ParseEscalationFields extracts escalation fields from an issue's description.
func ParseEscalationFields(description string) *EscalationFields {
	fields := &EscalationFields{}

	for _, line := range strings.Split(description, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		colonIdx := strings.Index(line, ":")
		if colonIdx == -1 {
			continue
		}

		key := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])
		if value == "null" || value == "" {
			value = ""
		}

		switch strings.ToLower(key) {
		case "severity":
			fields.Severity = value
		case "reason":
			fields.Reason = value
		case "source":
			fields.Source = value
		case "escalated_by":
			fields.EscalatedBy = value
		case "escalated_at":
			fields.EscalatedAt = value
		case "acked_by":
			fields.AckedBy = value
		case "acked_at":
			fields.AckedAt = value
		case "closed_by":
			fields.ClosedBy = value
		case "closed_reason":
			fields.ClosedReason = value
		case "related_bead":
			fields.RelatedBead = value
		case "original_severity":
			fields.OriginalSeverity = value
		case "reescalation_count":
			if n, err := strconv.Atoi(value); err == nil {
				fields.ReescalationCount = n
			}
		case "last_reescalated_at":
			fields.LastReescalatedAt = value
		case "last_reescalated_by":
			fields.LastReescalatedBy = value
		case "signature":
			fields.Signature = value
		case "occurrence_count":
			if n, err := strconv.Atoi(value); err == nil {
				fields.OccurrenceCount = n
			}
		case "last_occurrence_at":
			fields.LastOccurrenceAt = value
		case "fingerprint":
			fields.Fingerprint = value
		}
	}

	return fields
}

// FindOpenEscalationBySignature finds the first open escalation bead with a
// matching signature field. Returns nil, nil, nil when no match is found.
func (b *Beads) FindOpenEscalationBySignature(signature string) (*Issue, *EscalationFields, error) {
	if signature == "" {
		return nil, nil, nil
	}
	issues, err := b.ListEscalations()
	if err != nil {
		return nil, nil, err
	}
	for _, issue := range issues {
		fields := ParseEscalationFields(issue.Description)
		if fields.Signature == signature {
			return issue, fields, nil
		}
	}
	return nil, nil, nil
}

// listAllEscalations returns escalation beads in any status (open or closed).
// Used by FindRecentEscalationBySignature for window-based dedup that includes
// recently-closed escalations as suppression anchors.
func (b *Beads) listAllEscalations() ([]*Issue, error) {
	out, err := b.run("list", "--label=gt:escalation", "--status=all", "--json", "--limit=0")
	if err != nil {
		return nil, err
	}
	if len(out) == 0 || !isJSONBytes(out) {
		return nil, nil
	}
	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}
	return filterEscalationRecords(issues), nil
}

// FindRecentEscalationBySignature finds the most recent escalation bead with a
// matching signature, considering both open escalations and escalations that
// were closed within the given window. Returns the matched issue and fields,
// or nil, nil, nil when no match is found.
//
// This is the dedup primitive for `gt escalate --dedup --signature=X`: it
// answers "should we suppress this re-fire?" by checking whether any open or
// recently-closed escalation already covers this signature. Closed escalations
// inside the window count as the suppression anchor (gu-ah40) — without that,
// the moment a deduped escalation is closed the next plugin cycle re-fires it
// and creates a fresh bead.
//
// When the matched escalation is closed, callers should treat the result as
// "suppress, do not bump occurrence count" (the bead is already resolved).
// When open, callers should bump the occurrence count on the existing bead.
//
// window <= 0 means "open-only" (closed escalations are never matched).
func (b *Beads) FindRecentEscalationBySignature(signature string, window time.Duration) (*Issue, *EscalationFields, error) {
	if signature == "" {
		return nil, nil, nil
	}

	// Open-only path: cheaper query, matches legacy behavior.
	if window <= 0 {
		return b.FindOpenEscalationBySignature(signature)
	}

	issues, err := b.listAllEscalations()
	if err != nil {
		return nil, nil, err
	}
	issue, fields := pickRecentEscalationBySignature(issues, signature, window, time.Now())
	return issue, fields, nil
}

// pickRecentEscalationBySignature implements the dedup match logic against a
// pre-fetched issue list. Extracted from FindRecentEscalationBySignature so the
// signature-matching rules (open beats closed; closed must be within window;
// most-recent-closed wins among ties) are unit-testable without a bd stub.
func pickRecentEscalationBySignature(issues []*Issue, signature string, window time.Duration, now time.Time) (*Issue, *EscalationFields) {
	if signature == "" {
		return nil, nil
	}
	cutoff := now.Add(-window)
	var (
		bestOpen     *Issue
		bestOpenF    *EscalationFields
		bestClosed   *Issue
		bestClosedF  *EscalationFields
		bestClosedAt time.Time
	)
	for _, issue := range issues {
		fields := ParseEscalationFields(issue.Description)
		if fields.Signature != signature {
			continue
		}
		if issue.Status != "closed" {
			// Open match wins outright — there can't be a more authoritative
			// suppression anchor than a still-open escalation with the same sig.
			bestOpen = issue
			bestOpenF = fields
			break
		}
		// Closed: keep only if inside the window AND newer than the running pick.
		closedAt := parseEscalationClosedAt(issue)
		if closedAt.IsZero() || closedAt.Before(cutoff) {
			continue
		}
		if bestClosed == nil || closedAt.After(bestClosedAt) {
			bestClosed = issue
			bestClosedF = fields
			bestClosedAt = closedAt
		}
	}
	if bestOpen != nil {
		return bestOpen, bestOpenF
	}
	if bestClosed != nil {
		return bestClosed, bestClosedF
	}
	return nil, nil
}

// parseEscalationClosedAt returns the closed-at timestamp for an escalation
// issue, falling back to UpdatedAt when ClosedAt is unset (older beads or bd
// versions that don't populate ClosedAt). Returns zero time when neither is
// parseable.
func parseEscalationClosedAt(issue *Issue) time.Time {
	if issue == nil {
		return time.Time{}
	}
	if issue.ClosedAt != "" {
		if t, err := time.Parse(time.RFC3339, issue.ClosedAt); err == nil {
			return t
		}
	}
	if issue.UpdatedAt != "" {
		if t, err := time.Parse(time.RFC3339, issue.UpdatedAt); err == nil {
			return t
		}
	}
	return time.Time{}
}

// BumpOccurrenceCount increments the occurrence_count on an escalation bead
// and records the current time in last_occurrence_at. If updatedReason is
// non-empty, the reason field is replaced with the latest occurrence details.
func (b *Beads) BumpOccurrenceCount(id, updatedReason string) error {
	target := b.forIssueID(id)
	issue, err := target.Show(id)
	if err != nil {
		return err
	}
	if !HasLabel(issue, "gt:escalation") {
		return fmt.Errorf("issue %s is not an escalation bead (missing gt:escalation label)", id)
	}
	fields := ParseEscalationFields(issue.Description)
	fields.OccurrenceCount++
	fields.LastOccurrenceAt = time.Now().Format(time.RFC3339)
	if updatedReason != "" {
		fields.Reason = updatedReason
	}
	description := FormatEscalationDescription(issue.Title, fields)
	return target.Update(id, UpdateOptions{Description: &description})
}

// CreateEscalationBead creates an escalation bead for tracking escalations.
// The created_by field is populated from BD_ACTOR env var for provenance tracking.
func (b *Beads) CreateEscalationBead(title string, fields *EscalationFields) (*Issue, error) {
	// Guard against flag-like titles (gt-e0kx5: --help garbage beads)
	if IsFlagLikeTitle(title) {
		return nil, fmt.Errorf("refusing to create escalation bead: %w (got %q)", ErrFlagTitle, title)
	}

	description := FormatEscalationDescription(title, fields)

	// Pass description via stdin (--body-file=-) instead of --description=...
	// to avoid embedding newlines in a flag value. bd 1.0.3+ rejects newline-
	// containing flag values, which broke `gt escalate` for any escalation
	// with structured YAML metadata in the description.
	args := []string{"create", "--json",
		"--title=" + title,
		"--body-file=-",
		"--type=task",
		"--ephemeral",
		"--wisp-type=escalation",
		"--labels=gt:escalation",
	}

	// Add severity as a label for easy filtering
	if fields != nil && fields.Severity != "" {
		args = append(args, fmt.Sprintf("--labels=severity:%s", fields.Severity))
	}
	if fields != nil && fields.Fingerprint != "" {
		args = append(args, "--labels="+fields.Fingerprint)
	}

	// Default actor from BD_ACTOR env var for provenance tracking
	// Uses getActor() to respect isolated mode (tests)
	if actor := b.getActor(); actor != "" {
		args = append(args, "--actor="+actor)
	}

	// Retry on transient Dolt write-contention (gs-skv). Under concurrent load
	// the Dolt server can transiently reject a write ("database is read only",
	// Error 1290, lock/timeout) BEFORE committing — so a retry cannot duplicate
	// the escalation. Without this, an escalation raised during contention is
	// silently lost (empty `gt escalate list`), which is how a polecat's
	// unworkability signal vanished while it churned. Only retried errors that
	// are known-not-committed; any other failure returns immediately.
	out, err := createEscalationWithRetry(func() ([]byte, error) {
		return b.runWithStdin([]byte(description), args...)
	})
	if err != nil {
		return nil, err
	}

	var issue Issue
	if err := json.Unmarshal(out, &issue); err != nil {
		return nil, fmt.Errorf("parsing bd create output: %w", err)
	}

	return &issue, nil
}

// escalationCreateMaxAttempts bounds the contention retry. Four attempts with
// the backoff below spans ~3.5s, long enough to ride out a brief Dolt
// write-lock/replication stall without blocking the caller for long.
const escalationCreateMaxAttempts = 4

// createEscalationWithRetry runs the escalation create, retrying only on
// transient write-contention errors (which reject before commit, so a retry
// cannot create a duplicate bead). create is injected so the retry policy is
// unit-testable; the sleep is overridable via escalationRetrySleep.
func createEscalationWithRetry(create func() ([]byte, error)) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= escalationCreateMaxAttempts; attempt++ {
		out, err := create()
		if err == nil {
			return out, nil
		}
		lastErr = err
		if attempt == escalationCreateMaxAttempts || !isTransientWriteContention(err) {
			return nil, err
		}
		// Backoff: 250ms, 500ms, 1s, ... (capped). Short enough to stay
		// responsive, long enough to clear a transient write lock.
		escalationRetrySleep(time.Duration(250*(1<<(attempt-1))) * time.Millisecond)
	}
	return nil, lastErr
}

// escalationRetrySleep is the backoff sleep, overridable in tests to keep them
// fast.
var escalationRetrySleep = time.Sleep

// isTransientWriteContention reports whether err is a Dolt write-contention
// failure that rejected the write BEFORE committing — safe to retry without
// risking a duplicate. Matches the known not-committed signatures: read-only
// rejection (MySQL Error 1290 under concurrent commit pressure), lock-wait
// timeouts, and "database is locked". Deliberately conservative: an unknown
// error is treated as possibly-committed and NOT retried.
func isTransientWriteContention(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sig := range []string{
		"read only",
		"read-only",
		"error 1290",
		"--read-only",
		"database is locked",
		"table is locked",
		"lock wait timeout",
		"try restarting transaction",
		"deadlock found",
	} {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}

// AckEscalation acknowledges an escalation bead.
// Sets acked_by and acked_at fields, adds "acked" label.
func (b *Beads) AckEscalation(id, ackedBy string) error {
	target := b.forIssueID(id)
	// First get current issue to preserve other fields
	issue, err := target.Show(id)
	if err != nil {
		return err
	}

	// Verify it's an escalation
	if !HasLabel(issue, "gt:escalation") {
		return fmt.Errorf("issue %s is not an escalation bead (missing gt:escalation label)", id)
	}

	// Parse existing fields
	fields := ParseEscalationFields(issue.Description)
	fields.AckedBy = ackedBy
	fields.AckedAt = time.Now().Format(time.RFC3339)

	// Format new description
	description := FormatEscalationDescription(issue.Title, fields)

	return target.Update(id, UpdateOptions{
		Description: &description,
		AddLabels:   []string{"acked"},
	})
}

// CloseEscalation closes an escalation bead with a resolution reason.
// Sets closed_by and closed_reason fields, closes the issue.
func (b *Beads) CloseEscalation(id, closedBy, reason string) error {
	target := b.forIssueID(id)
	// First get current issue to preserve other fields
	issue, err := target.Show(id)
	if err != nil {
		return err
	}

	// Verify it's an escalation
	if !HasLabel(issue, "gt:escalation") {
		return fmt.Errorf("issue %s is not an escalation bead (missing gt:escalation label)", id)
	}

	// Parse existing fields
	fields := ParseEscalationFields(issue.Description)
	fields.ClosedBy = closedBy
	fields.ClosedReason = reason

	// Format new description
	description := FormatEscalationDescription(issue.Title, fields)

	// Update description first
	if err := target.Update(id, UpdateOptions{
		Description: &description,
		AddLabels:   []string{"resolved"},
	}); err != nil {
		return err
	}

	// Close the issue
	_, err = target.run("close", id, "--reason="+reason)
	return err
}

// GetEscalationBead retrieves an escalation bead by ID.
// Returns nil if not found.
func (b *Beads) GetEscalationBead(id string) (*Issue, *EscalationFields, error) {
	issue, err := b.forIssueID(id).Show(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	if !HasLabel(issue, "gt:escalation") {
		return nil, nil, fmt.Errorf("issue %s is not an escalation bead (missing gt:escalation label)", id)
	}

	fields := ParseEscalationFields(issue.Description)
	return issue, fields, nil
}

// ListEscalations returns all open escalation beads.
func (b *Beads) ListEscalations() ([]*Issue, error) {
	out, err := b.run("list", "--label=gt:escalation", "--status=open", "--json", "--limit=0")
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}

	return filterEscalationRecords(issues), nil
}

// ListEscalationsByFingerprint returns open escalation beads matching a stable fingerprint label.
func (b *Beads) ListEscalationsByFingerprint(fingerprintLabel string) ([]*Issue, error) {
	if fingerprintLabel == "" {
		return nil, nil
	}
	out, err := b.run("list",
		"--label=gt:escalation",
		"--label="+fingerprintLabel,
		"--status=open",
		"--json",
	)
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}

	return filterEscalationRecords(issues), nil
}

// ListEscalationsBySeverity returns open escalation beads filtered by severity.
func (b *Beads) ListEscalationsBySeverity(severity string) ([]*Issue, error) {
	out, err := b.run("list",
		"--label=gt:escalation",
		"--label=severity:"+severity,
		"--status=open",
		"--json",
		"--limit=0",
	)
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}

	return filterEscalationRecords(issues), nil
}

func filterEscalationRecords(issues []*Issue) []*Issue {
	filtered := issues[:0]
	for _, issue := range issues {
		if HasLabel(issue, "gt:message") {
			continue
		}
		filtered = append(filtered, issue)
	}
	return filtered
}

// ListStaleEscalations returns escalations older than the given threshold.
// threshold is a duration string like "1h" or "30m".
func (b *Beads) ListStaleEscalations(threshold time.Duration) ([]*Issue, error) {
	// Get all open escalations
	escalations, err := b.ListEscalations()
	if err != nil {
		return nil, err
	}

	cutoff := time.Now().Add(-threshold)
	var stale []*Issue

	for _, issue := range escalations {
		// Skip acknowledged escalations
		if HasLabel(issue, "acked") {
			continue
		}

		// Check if older than threshold
		createdAt, err := time.Parse(time.RFC3339, issue.CreatedAt)
		if err != nil {
			continue // Skip if can't parse
		}

		if createdAt.Before(cutoff) {
			stale = append(stale, issue)
		}
	}

	return stale, nil
}

// ReescalationResult holds the result of a reescalation operation.
type ReescalationResult struct {
	ID              string
	Title           string
	OldSeverity     string
	NewSeverity     string
	ReescalationNum int
	Skipped         bool
	SkipReason      string
}

// ReescalateEscalation bumps the severity of an escalation and updates tracking fields.
// Returns the new severity if successful, or an error.
// reescalatedBy should be the identity of the agent/process doing the reescalation.
// maxReescalations limits how many times an escalation can be bumped (0 = unlimited).
func (b *Beads) ReescalateEscalation(id, reescalatedBy string, maxReescalations int) (*ReescalationResult, error) {
	// Get the escalation
	issue, fields, err := b.GetEscalationBead(id)
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, fmt.Errorf("escalation not found: %s", id)
	}

	result := &ReescalationResult{
		ID:          id,
		Title:       issue.Title,
		OldSeverity: fields.Severity,
	}

	// Check if already at max reescalations
	if maxReescalations > 0 && fields.ReescalationCount >= maxReescalations {
		result.Skipped = true
		result.SkipReason = fmt.Sprintf("already at max reescalations (%d)", maxReescalations)
		return result, nil
	}

	// Check if already at critical (can't bump further)
	if fields.Severity == "critical" {
		result.Skipped = true
		result.SkipReason = "already at critical severity"
		result.NewSeverity = "critical"
		return result, nil
	}

	// Save original severity on first reescalation
	if fields.OriginalSeverity == "" {
		fields.OriginalSeverity = fields.Severity
	}

	// Bump severity
	newSeverity := bumpSeverity(fields.Severity)
	fields.Severity = newSeverity
	fields.ReescalationCount++
	fields.LastReescalatedAt = time.Now().Format(time.RFC3339)
	fields.LastReescalatedBy = reescalatedBy

	result.NewSeverity = newSeverity
	result.ReescalationNum = fields.ReescalationCount

	// Format new description
	description := FormatEscalationDescription(issue.Title, fields)

	// Update the bead with new description and severity label
	if err := b.forIssueID(id).Update(id, UpdateOptions{
		Description:  &description,
		AddLabels:    []string{"reescalated", "severity:" + newSeverity},
		RemoveLabels: []string{"severity:" + result.OldSeverity},
	}); err != nil {
		return nil, fmt.Errorf("updating escalation: %w", err)
	}

	return result, nil
}

// bumpSeverity returns the next higher severity level.
// low -> medium -> high -> critical
func bumpSeverity(severity string) string {
	switch severity {
	case "low":
		return "medium"
	case "medium":
		return "high"
	case "high":
		return "critical"
	default:
		return "critical"
	}
}

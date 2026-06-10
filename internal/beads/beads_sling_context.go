package beads

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// FormatSlingContextDescription serializes SlingContextFields as JSON.
// The context bead description is entirely scheduler-owned, so we use
// JSON instead of key-value lines — no user content collision, no delimiter.
func FormatSlingContextDescription(fields *capacity.SlingContextFields) string {
	b, err := json.Marshal(fields)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ParseSlingContextFields deserialises a context bead description.
// Returns nil if the description is not valid JSON.
func ParseSlingContextFields(description string) *capacity.SlingContextFields {
	var fields capacity.SlingContextFields
	if err := json.Unmarshal([]byte(description), &fields); err != nil {
		return nil
	}
	return &fields
}

// CreateSlingContext creates an ephemeral sling context bead that tracks
// scheduling state for a work bead. The work bead is never modified.
func (b *Beads) CreateSlingContext(workBeadTitle, workBeadID string, fields *capacity.SlingContextFields) (*Issue, error) {
	title := fmt.Sprintf("sling-context: %s", workBeadTitle)
	if len(title) > 200 {
		title = title[:200]
	}

	description := FormatSlingContextDescription(fields)

	args := []string{"create", "--json",
		"--ephemeral",
		"--title=" + title,
		"--description=" + description,
		"--type=task",
		"--labels=" + capacity.LabelSlingContext,
	}

	if actor := b.getActor(); actor != "" {
		args = append(args, "--actor="+actor)
	}

	out, err := b.run(args...)
	if err != nil {
		return nil, fmt.Errorf("creating sling context: %w", err)
	}

	var issue Issue
	if err := json.Unmarshal(out, &issue); err != nil {
		return nil, fmt.Errorf("parsing bd create output: %w", err)
	}

	// Add tracks dependency: context bead → work bead
	_, depErr := b.run("dep", "add", issue.ID, workBeadID, "--type=tracks")
	if depErr != nil {
		// Non-fatal: the context bead was created, just missing the dep link.
		// This can happen if the work bead is in a different DB and external refs aren't set up.
		fmt.Printf("Warning: could not add tracks dep %s → %s: %v\n", issue.ID, workBeadID, depErr)
	}

	return &issue, nil
}

// FindOpenSlingContext finds an open sling context for the given work bead ID.
// Used for idempotency checks. Returns (nil, nil, nil) if none found.
func (b *Beads) FindOpenSlingContext(workBeadID string) (*Issue, *capacity.SlingContextFields, error) {
	contexts, err := b.ListOpenSlingContexts()
	if err != nil {
		return nil, nil, err
	}

	for _, ctx := range contexts {
		fields := ParseSlingContextFields(ctx.Description)
		if fields != nil && fields.WorkBeadID == workBeadID {
			return ctx, fields, nil
		}
	}

	return nil, nil, nil
}

// ListOpenSlingContexts returns all open sling context beads.
func (b *Beads) ListOpenSlingContexts() ([]*Issue, error) {
	out, err := b.run("list",
		"--label="+capacity.LabelSlingContext,
		"--status=open",
		"--json",
		"--limit=0",
	)
	if err != nil {
		return nil, err
	}

	// Handle empty output or non-JSON responses.
	// bd list --json may return plain text like "No issues found." instead
	// of an empty JSON array when there are no results.
	if len(out) == 0 || !isJSONBytes(out) {
		return nil, nil
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing sling context list: %w", err)
	}

	return issues, nil
}

// CloseSlingContext closes a sling context bead with a reason.
// Idempotent: a context that is already closed — or already gone entirely —
// is in the desired state, so both "already closed" and "issue not found"
// (ErrNotFound, e.g. the wisp was TTL-reaped out from under us) are treated
// as success. This keeps the dispatch path from emitting a spurious
// "double-dispatch risk" escalation when the only thing that failed was a
// redundant close of an already-consumed context (gu-1pcst). The real
// double-dispatch guard is the work bead's HOOKED status, not this close.
func (b *Beads) CloseSlingContext(contextID, reason string) error {
	_, err := b.run("close", contextID, "--reason="+reason)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), "already closed") {
		return nil // Idempotent — already in desired state (closed or gone)
	}
	return err
}

// UpdateSlingContextFields updates the description (fields) of a sling context bead.
func (b *Beads) UpdateSlingContextFields(contextID string, fields *capacity.SlingContextFields) error {
	description := FormatSlingContextDescription(fields)
	return b.Update(contextID, UpdateOptions{Description: &description})
}

// ReconcileOpenSlingContexts closes every OPEN sling context that tracks the
// given work bead, except optExcludeID (pass "" to close all). It returns the
// IDs that were closed.
//
// This is the direct-dispatch counterpart to scheduleBead's stale-context
// recovery (gu-afpjj). A failed initial sling leaves a sling context OPEN — the
// context bead carries a `tracks` dependency on the work bead, so `bd close
// <workBead>` then refuses without --force. The deferred path (scheduleBead)
// already recycles such contexts before re-scheduling, but the direct dispatch
// chokepoint (executeSling) and the inline runSling path never touched them, so
// a manual re-sling ran the work yet left the stale context dangling. Calling
// this after a successful direct sling closes the orphans as "superseded" so the
// work bead closes cleanly without --force.
//
// Idempotent and best-effort per context: CloseSlingContext already treats an
// already-closed or TTL-reaped (not-found) context as success, so a redundant
// call is harmless. A list error is returned to the caller, which logs but does
// not fail the dispatch — the work is already hooked and running.
func (b *Beads) ReconcileOpenSlingContexts(workBeadID, optExcludeID, reason string) ([]string, error) {
	contexts, err := b.ListOpenSlingContexts()
	if err != nil {
		return nil, err
	}

	var closed []string
	for _, ctx := range contexts {
		if ctx.ID == optExcludeID {
			continue
		}
		fields := ParseSlingContextFields(ctx.Description)
		if fields == nil || fields.WorkBeadID != workBeadID {
			continue
		}
		if err := b.CloseSlingContext(ctx.ID, reason); err != nil {
			return closed, fmt.Errorf("closing stale sling context %s: %w", ctx.ID, err)
		}
		closed = append(closed, ctx.ID)
	}
	return closed, nil
}

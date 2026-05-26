// OQ4 fallback: metadata-attachment-bead pattern for transition and
// rejection logs.
//
// Phase 0 task 8 (gu-l6xu). The OQ4 spike proved that Issue.Metadata
// is not safe as a multi-writer surface. The solution: each
// transition/rejection is a new bead (append-only, no clobber risk).
// Reads materialize the log by listing attachment beads filtered by
// label and rig, then sorting by timestamp.
//
// Schema and design context:
//   - .designs/auto-test-pr/synthesis.md §"OQ4 fallback"
//   - .designs/auto-test-pr/data.md §"Pinned state bead"
//
// All attachment beads are created by Mayor only (gu-gal8 rule).
package autotestpr

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// Attachment bead label constants.
const (
	// AttachmentLabel is the umbrella discriminator for all
	// auto-test-pr attachment beads.
	AttachmentLabel = "gt:auto-test-pr-attachment"

	// AttachmentParentLabel is the feature umbrella.
	AttachmentParentLabel = "gt:auto-test-pr"

	// KindTransition is the kind label for transition attachments.
	KindTransition = "kind:transition"

	// KindRejection is the kind label for rejection attachments.
	KindRejection = "kind:rejection"
)

// RigLabel returns the label string for a specific rig, e.g.,
// "rig:gastown_upstream".
func RigLabel(rigName string) string {
	return "rig:" + rigName
}

// TransitionRecord is the deserialized form of a transition attachment
// bead's metadata. Matches the shape the synthesis documents for the
// in-blob transition_log[] entries (same fields, same semantics).
type TransitionRecord struct {
	SchemaVersion int               `json:"schema_version"`
	Rig           string            `json:"rig"`
	From          string            `json:"from"`
	To            string            `json:"to"`
	At            time.Time         `json:"at"`
	Actor         string            `json:"actor"`
	Context       map[string]string `json:"context,omitempty"`
}

// RejectionRecord is the deserialized form of a rejection attachment
// bead's metadata. Matches the shape the synthesis documents for the
// in-blob rejection_log[] entries.
type RejectionRecord struct {
	SchemaVersion int       `json:"schema_version"`
	Rig           string    `json:"rig"`
	File          string    `json:"file"`
	RejectedAt    time.Time `json:"rejected_at"`
	Reason        string    `json:"reason"`
	CooldownUntil time.Time `json:"cooldown_until"`
	MRID          string    `json:"mr_id,omitempty"`
}

// transitionMetadata is the internal JSON shape for transition
// attachment metadata (uses string timestamps for storage).
type transitionMetadata struct {
	SchemaVersion int               `json:"schema_version"`
	Rig           string            `json:"rig"`
	From          string            `json:"from"`
	To            string            `json:"to"`
	At            string            `json:"at"`
	Actor         string            `json:"actor"`
	Context       map[string]string `json:"context,omitempty"`
}

// rejectionMetadata is the internal JSON shape for rejection
// attachment metadata.
type rejectionMetadata struct {
	SchemaVersion int    `json:"schema_version"`
	Rig           string `json:"rig"`
	File          string `json:"file"`
	RejectedAt    string `json:"rejected_at"`
	Reason        string `json:"reason"`
	CooldownUntil string `json:"cooldown_until"`
	MRID          string `json:"mr_id,omitempty"`
}

// MaxTransitions is the recency window for materialized transitions.
const MaxTransitions = 50

// MaxRejections is the recency window for materialized rejections.
const MaxRejections = 200

// CreateTransitionAttachment creates a new transition attachment bead.
// Called by the Mayor cycle-close handler on every state transition.
//
// The bead is created with:
//   - Labels: AttachmentLabel, KindTransition, RigLabel(rig), AttachmentParentLabel
//   - Metadata: transitionMetadata JSON
//   - Actor: mayor
//   - Status: open (within retention window)
func CreateTransitionAttachment(b *beads.Beads, rec TransitionRecord) (*beads.Issue, error) {
	if b == nil {
		return nil, fmt.Errorf("CreateTransitionAttachment: nil beads wrapper")
	}
	if rec.Rig == "" {
		return nil, fmt.Errorf("CreateTransitionAttachment: empty rig")
	}

	meta := transitionMetadata{
		SchemaVersion: 1,
		Rig:           rec.Rig,
		From:          rec.From,
		To:            rec.To,
		At:            rec.At.UTC().Format(time.RFC3339),
		Actor:         rec.Actor,
		Context:       rec.Context,
	}
	rawMeta, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshaling transition metadata: %w", err)
	}

	title := fmt.Sprintf("auto-test-pr transition %s: %s → %s @ %s",
		rec.Rig, rec.From, rec.To, rec.At.UTC().Format(time.RFC3339))

	issue, err := b.Create(beads.CreateOptions{
		Title: title,
		Labels: []string{
			AttachmentLabel,
			AttachmentParentLabel,
			KindTransition,
			RigLabel(rec.Rig),
		},
		Priority: 3,
		Metadata: json.RawMessage(rawMeta),
		Actor:    "mayor",
	})
	if err != nil {
		return nil, fmt.Errorf("creating transition attachment for %s: %w", rec.Rig, err)
	}
	return issue, nil
}

// CreateRejectionAttachment creates a new rejection attachment bead.
// Called by the Mayor cycle-close handler on close-unmerged.
func CreateRejectionAttachment(b *beads.Beads, rec RejectionRecord) (*beads.Issue, error) {
	if b == nil {
		return nil, fmt.Errorf("CreateRejectionAttachment: nil beads wrapper")
	}
	if rec.Rig == "" {
		return nil, fmt.Errorf("CreateRejectionAttachment: empty rig")
	}

	meta := rejectionMetadata{
		SchemaVersion: 1,
		Rig:           rec.Rig,
		File:          rec.File,
		RejectedAt:    rec.RejectedAt.UTC().Format(time.RFC3339),
		Reason:        rec.Reason,
		CooldownUntil: rec.CooldownUntil.UTC().Format(time.RFC3339),
		MRID:          rec.MRID,
	}
	rawMeta, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshaling rejection metadata: %w", err)
	}

	title := fmt.Sprintf("auto-test-pr rejection %s: %s @ %s",
		rec.Rig, rec.File, rec.RejectedAt.UTC().Format(time.RFC3339))

	issue, err := b.Create(beads.CreateOptions{
		Title: title,
		Labels: []string{
			AttachmentLabel,
			AttachmentParentLabel,
			KindRejection,
			RigLabel(rec.Rig),
		},
		Priority: 3,
		Metadata: json.RawMessage(rawMeta),
		Actor:    "mayor",
	})
	if err != nil {
		return nil, fmt.Errorf("creating rejection attachment for %s: %w", rec.Rig, err)
	}
	return issue, nil
}

// MaterializeAutoTestState reads the per-rig logs by listing
// attachment beads. Returns the same shape the previous in-blob
// transition_log[] / rejection_log[] returned, so callers don't
// branch on storage form.
//
// Over zero attachment beads, returns empty slices (not nil).
func MaterializeAutoTestState(b *beads.Beads, rig string) (
	transitions []TransitionRecord,
	rejections []RejectionRecord,
	err error,
) {
	if b == nil {
		return nil, nil, fmt.Errorf("MaterializeAutoTestState: nil beads wrapper")
	}
	if rig == "" {
		return nil, nil, fmt.Errorf("MaterializeAutoTestState: empty rig")
	}

	// Query all attachment beads. We use status=all so closed (retired)
	// attachments still surface for audit reads.
	issues, err := b.List(beads.ListOptions{
		Label:  AttachmentLabel,
		Status: "all",
		Limit:  0, // unlimited
	})
	if err != nil {
		return nil, nil, fmt.Errorf("listing attachment beads: %w", err)
	}

	// Initialize to non-nil empty slices so JSON encodes as [] not null.
	transitions = make([]TransitionRecord, 0)
	rejections = make([]RejectionRecord, 0)

	rigLbl := RigLabel(rig)
	for _, issue := range issues {
		if !beads.HasLabel(issue, rigLbl) {
			continue
		}
		switch {
		case beads.HasLabel(issue, KindTransition):
			tr, parseErr := parseTransition(issue.Metadata)
			if parseErr != nil {
				continue // skip schema drift
			}
			transitions = append(transitions, tr)
		case beads.HasLabel(issue, KindRejection):
			rj, parseErr := parseRejection(issue.Metadata)
			if parseErr != nil {
				continue // skip schema drift
			}
			rejections = append(rejections, rj)
		}
	}

	// Newest-first ordering.
	sort.Slice(transitions, func(i, j int) bool {
		return transitions[i].At.After(transitions[j].At)
	})
	sort.Slice(rejections, func(i, j int) bool {
		return rejections[i].RejectedAt.After(rejections[j].RejectedAt)
	})

	// Recency window — keep the same caps the in-blob logs had.
	if len(transitions) > MaxTransitions {
		transitions = transitions[:MaxTransitions]
	}
	if len(rejections) > MaxRejections {
		rejections = rejections[:MaxRejections]
	}

	return transitions, rejections, nil
}

// parseTransition parses a transition attachment's metadata.
func parseTransition(raw json.RawMessage) (TransitionRecord, error) {
	if len(raw) == 0 {
		return TransitionRecord{}, fmt.Errorf("empty transition metadata")
	}
	var meta transitionMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return TransitionRecord{}, fmt.Errorf("unmarshaling transition: %w", err)
	}
	at, err := time.Parse(time.RFC3339, meta.At)
	if err != nil {
		return TransitionRecord{}, fmt.Errorf("parsing transition At: %w", err)
	}
	return TransitionRecord{
		SchemaVersion: meta.SchemaVersion,
		Rig:           meta.Rig,
		From:          meta.From,
		To:            meta.To,
		At:            at,
		Actor:         meta.Actor,
		Context:       meta.Context,
	}, nil
}

// parseRejection parses a rejection attachment's metadata.
func parseRejection(raw json.RawMessage) (RejectionRecord, error) {
	if len(raw) == 0 {
		return RejectionRecord{}, fmt.Errorf("empty rejection metadata")
	}
	var meta rejectionMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return RejectionRecord{}, fmt.Errorf("unmarshaling rejection: %w", err)
	}
	rejAt, err := time.Parse(time.RFC3339, meta.RejectedAt)
	if err != nil {
		return RejectionRecord{}, fmt.Errorf("parsing rejection RejectedAt: %w", err)
	}
	coolUntil, err := time.Parse(time.RFC3339, meta.CooldownUntil)
	if err != nil {
		return RejectionRecord{}, fmt.Errorf("parsing rejection CooldownUntil: %w", err)
	}
	return RejectionRecord{
		SchemaVersion: meta.SchemaVersion,
		Rig:           meta.Rig,
		File:          meta.File,
		RejectedAt:    rejAt,
		Reason:        meta.Reason,
		CooldownUntil: coolUntil,
		MRID:          meta.MRID,
	}, nil
}

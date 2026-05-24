// Production adapters for CycleCloseHandler dependencies.
//
// The handler in cycleclose.go is testable in isolation by depending on
// narrow BeadsClient and Notifier interfaces. This file provides the
// concrete adapters used at daemon-startup wiring time:
//
//   - beadsAdapter wraps *beads.Beads with the small surface the handler
//     needs (ShowMetadata, UpdateMetadata, CreateAttachment, CreateBugBead,
//     ListTransitionsForRig).
//   - nudgeNotifier shells out to `gt nudge mayor/overseer` for the SEV-2
//     circuit-breaker trip notification.
//
// These adapters are intentionally thin. Anything that needs business logic
// (label-string construction, JSON shaping) belongs in cycleclose.go where
// it's covered by the unit tests.
package autotestpr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// beadsAdapter implements BeadsClient on top of *beads.Beads. Construct
// via NewBeadsClient.
type beadsAdapter struct {
	b *beads.Beads
}

// NewBeadsClient wraps a beads handle for the cycle-close handler. The
// handle is borrowed (not copied); callers retain ownership and lifetime.
func NewBeadsClient(b *beads.Beads) BeadsClient {
	return &beadsAdapter{b: b}
}

// ShowMetadata reads the Metadata blob for the given bead ID. Maps the
// beads layer's two not-found surfaces (typed sentinel + CLI string) to
// our typed ErrBeadNotFound so callers don't have to know about the
// subprocess-vs-store distinction.
func (a *beadsAdapter) ShowMetadata(id string) (json.RawMessage, error) {
	issue, err := a.b.Show(id)
	if err != nil {
		if isBeadNotFound(err) {
			return nil, ErrBeadNotFound
		}
		return nil, err
	}
	if issue == nil {
		return nil, ErrBeadNotFound
	}
	return issue.Metadata, nil
}

// UpdateMetadata replaces the entire Metadata blob on the given bead via
// bd update --metadata=<json>. The handler's REPLACE-semantics warning
// applies — see BeadsClient.UpdateMetadata.
func (a *beadsAdapter) UpdateMetadata(id string, raw json.RawMessage) error {
	return a.b.Update(id, beads.UpdateOptions{Metadata: raw})
}

// CreateAttachment files a new attachment bead. Title, labels, parent
// (depends_on), and metadata are passed through verbatim. We mark these
// beads as priority 2 (matches the design's "Mayor-owned bookkeeping
// bead" priority elsewhere in the system) and set actor=mayor so the
// audit trail is consistent with gu-gal8's "no polecat-owned
// bookkeeping beads" rule.
func (a *beadsAdapter) CreateAttachment(title string, labels []string, parentID string, metadata json.RawMessage) (string, error) {
	issue, err := a.b.Create(beads.CreateOptions{
		Title:    title,
		Labels:   labels,
		Priority: 2,
		Parent:   parentID,
		Actor:    "mayor",
		Metadata: metadata,
	})
	if err != nil {
		return "", err
	}
	return issue.ID, nil
}

// CreateBugBead files a P2 bug bead linked to the given MR. The bug bead
// has type=bug (carried via the gt:bug label, which the bd CLI translates
// into the legacy issue_type column at create time). We pass parentID as
// the parent edge so a `bd show <mr>` walk reaches the bug bead naturally.
func (a *beadsAdapter) CreateBugBead(title, body, parentID string, labels []string) (string, error) {
	issue, err := a.b.Create(beads.CreateOptions{
		Title:       title,
		Description: body,
		Labels:      labels,
		Priority:    2,
		Parent:      parentID,
		Actor:       "mayor",
	})
	if err != nil {
		return "", err
	}
	return issue.ID, nil
}

// ListTransitionsForRig lists all transition attachment beads for the
// given rig. The bd ListOptions surface accepts only a single label filter
// at a time, so we list by the umbrella label (gt:auto-test-pr-attachment)
// and filter the kind:transition + rig:<rig> labels client-side. This
// matches the materializer pattern in synthesis.md §"Materialize-from-
// attachments read path" — we accept the extra rig-side filtering cost in
// exchange for not adding a multi-label list surface to bd.
func (a *beadsAdapter) ListTransitionsForRig(rig string) ([]Transition, error) {
	issues, err := a.b.List(beads.ListOptions{
		Label:  labelAttachmentUmbrella,
		Status: "all",
		Limit:  500, // enough for the rolling-7d window plus margin
	})
	if err != nil {
		return nil, err
	}

	rigLabel := labelRigPrefix + rig
	var out []Transition
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		if !beads.HasLabel(issue, labelKindTransition) {
			continue
		}
		if !beads.HasLabel(issue, rigLabel) {
			continue
		}
		var tr Transition
		if err := json.Unmarshal(issue.Metadata, &tr); err != nil {
			// Skip malformed attachments rather than failing the entire
			// list — schema drift between writers and readers is the
			// scenario the synthesis doc's "Schema versioning" section
			// explicitly accepts. A failed unmarshal at materialize time
			// is observable in logs at the call site.
			continue
		}
		out = append(out, tr)
	}
	return out, nil
}

// isBeadNotFound recognizes both the typed sentinel and the CLI's
// "no issue found" stderr substring as not-found.
func isBeadNotFound(err error) bool {
	if err == nil {
		return false
	}
	if isErrNotFound(err) {
		return true
	}
	return strings.Contains(err.Error(), "no issue found")
}

// isErrNotFound is split out so the import surface stays local to the
// adapter file — the handler-side ErrBeadNotFound is the public sentinel.
func isErrNotFound(err error) bool {
	return errors.Is(err, beads.ErrNotFound)
}

// nudgeNotifier shells out to `gt nudge` to deliver the SEV-2 Overseer
// notification on circuit-breaker trip. Uses --mode=immediate because
// breaker-trip is, by definition, an out-of-band incident signal that
// SHOULD interrupt.
type nudgeNotifier struct {
	gtPath string
}

// NewNudgeNotifier wires a Notifier that uses `gt nudge` to deliver
// Overseer notifications. gtPath is the absolute path to the gt binary
// — the daemon already discovers this and stores it on Daemon.gtPath, so
// pass that in at wiring time.
func NewNudgeNotifier(gtPath string) Notifier {
	return &nudgeNotifier{gtPath: gtPath}
}

// NotifyOverseer sends a SEV-2 nudge. We use the role shortcut
// "mayor/overseer" — Mayor is the handler's home, and Overseer is the
// recipient per Q6 SEV-2.
func (n *nudgeNotifier) NotifyOverseer(subject, body string) error {
	if n.gtPath == "" {
		return fmt.Errorf("nudgeNotifier: empty gtPath; pass Daemon.gtPath at wiring")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Body format: "[from cycle-close-handler] <subject>\n\n<body>".
	// The handler caller sets sender attribution by piping the message
	// through `gt nudge --message`; we add the subject line on top so
	// the recipient sees a structured envelope.
	msg := subject + "\n\n" + body

	cmd := exec.CommandContext(ctx, n.gtPath, //nolint:gosec // G204: args constructed internally
		"nudge",
		"mayor/overseer",
		"--mode=immediate",
		"--priority=high",
		"--message", msg,
	)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gt nudge mayor/overseer: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

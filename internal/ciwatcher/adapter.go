package ciwatcher

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// Compile-time assertion that BeadsAdapter satisfies BeadStore.
var _ BeadStore = (*BeadsAdapter)(nil)

// Compile-time assertion that MailAdapter satisfies Mailer.
var _ Mailer = (*MailAdapter)(nil)

// BeadsAdapter wraps internal/beads.Beads to satisfy the BeadStore interface.
// It is a thin shim — the watcher's contract is intentionally narrow so the
// production beads package and a test fake can both satisfy it.
type BeadsAdapter struct {
	B *beads.Beads
}

// NewBeadsAdapter constructs a BeadsAdapter for a given working directory.
// `workDir` is the rig's repo root; the adapter uses the bd CLI's prefix
// routing to reach the right database.
func NewBeadsAdapter(workDir string) *BeadsAdapter {
	return &BeadsAdapter{B: beads.New(workDir)}
}

// Reopen flips a closed bead back to open by clearing the assignee and
// setting status=open via bd update. We deliberately keep the original
// owner intact (status only) so the bead's history is preserved; the
// watcher has no business reassigning. If the bead is already open the
// operation is a no-op (bd update --status=open succeeds idempotently).
func (a *BeadsAdapter) Reopen(beadID string) error {
	open := "open"
	return a.B.Update(beadID, beads.UpdateOptions{Status: &open})
}

// AddLabel adds a label via bd update --add-label.
func (a *BeadsAdapter) AddLabel(beadID, label string) error {
	return a.B.Update(beadID, beads.UpdateOptions{AddLabels: []string{label}})
}

// AppendNote writes a note line via `bd update --notes`. The bd CLI's
// --notes flag REPLACES notes wholesale rather than appending; the audit
// trail of past CI failures lives on the bead's history (each update is a
// Dolt commit) and in the structured events log, so we don't try to merge
// prior notes here. Concurrency: the watcher is the sole writer during a
// poll cycle; collisions with manual `bd update --notes` are possible but
// rare and the lost-update is minor (the operator's note may be replaced).
// Acceptable for a last-line-of-defense audit log.
func (a *BeadsAdapter) AppendNote(beadID, note string) error {
	args := []string{"update", beadID, "--notes=" + note}
	if _, err := a.B.Run(args...); err != nil {
		return err
	}
	return nil
}

// Exists reports whether bd Show returns a non-nil issue. Show returns an
// error when the bead is missing; we treat any error as "doesn't exist" for
// the watcher's purposes (it falls back to the no-bead path), but propagate
// connectivity errors so the caller can decide.
func (a *BeadsAdapter) Exists(beadID string) (bool, error) {
	issue, err := a.B.Show(beadID)
	if err != nil {
		// Distinguish "not found" from "Dolt down".
		if isNotFoundErr(err) {
			return false, nil
		}
		return false, err
	}
	return issue != nil, nil
}

// isNotFoundErr is a heuristic — bd CLI returns "issue not found: <id>" via
// stderr. We can't depend on a typed error here (subprocess exit), so we
// match the substring. False negatives just mean we surface a connectivity
// error to the caller, which is correct behavior.
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not found") || strings.Contains(msg, "no such issue")
}

// MailAdapter shells out to `gt mail send mayor/` for the watcher's mayor
// notifications. Using the `gt` CLI keeps us aligned with the rest of the
// codebase (deacon, refinery, compact_report) — they all call gt mail send
// rather than constructing mail messages directly.
type MailAdapter struct {
	// WorkDir is the directory `gt mail send` runs in. The mail router
	// derives the sender identity from ambient env vars and the cwd's rig.
	WorkDir string

	// Bin is the gt executable name; defaults to "gt".
	Bin string
}

// NewMailAdapter constructs a MailAdapter.
func NewMailAdapter(workDir string) *MailAdapter {
	return &MailAdapter{WorkDir: workDir, Bin: "gt"}
}

// SendMayor sends a mail message to mayor/ using `gt mail send`. The body is
// passed via stdin to avoid quoting issues with multi-line content.
func (m *MailAdapter) SendMayor(subject, body string) error {
	bin := m.Bin
	if bin == "" {
		bin = "gt"
	}
	cmd := exec.Command(bin, "mail", "send", "mayor/", "-s", subject, "--stdin") //nolint:gosec // bin operator-controlled
	cmd.Dir = m.WorkDir
	cmd.Stdin = strings.NewReader(body)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("gt mail send: exit %d: %s", exitErr.ExitCode(), exitErr.Stderr)
		}
		return fmt.Errorf("gt mail send: %w", err)
	}
	return nil
}

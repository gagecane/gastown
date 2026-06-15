package prwatcher

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// Compile-time assertions that the production adapters satisfy the interfaces.
var (
	_ Dispatcher = (*GTDispatcher)(nil)
	_ Mailer     = (*MailAdapter)(nil)
)

// GTDispatcher turns triaged comments into Gas Town work. Bead creation uses
// the in-process beads package; slinging shells out to `gt sling` (there is no
// in-process sling API — sling spawns polecats, creates convoys, and runs
// preflight, all of which live behind the CLI).
type GTDispatcher struct {
	// B is the beads client, anchored at the rig directory so bd prefix routing
	// reaches the right database.
	B *beads.Beads

	// WorkDir is the directory `gt sling` runs in (the rig dir / town root).
	WorkDir string

	// Bin is the gt executable name; defaults to "gt".
	Bin string
}

// NewGTDispatcher constructs a GTDispatcher for a rig working directory.
func NewGTDispatcher(workDir string) *GTDispatcher {
	return &GTDispatcher{B: beads.New(workDir), WorkDir: workDir, Bin: "gt"}
}

// CreateBead creates a work bead carrying the PR-comment label set. Mechanical
// beads carry only LabelPRComment; judgment beads also carry the caller-supplied
// label (LabelNeedsHumanTriage). Priority 2 matches the parent feature's P2.
func (d *GTDispatcher) CreateBead(title, description, label string) (string, error) {
	labels := []string{LabelPRComment}
	if label != "" && label != LabelPRComment {
		labels = append(labels, label)
	}
	issue, err := d.B.Create(beads.CreateOptions{
		Title:       title,
		Description: description,
		Labels:      labels,
		Priority:    2,
	})
	if err != nil {
		return "", fmt.Errorf("bd create: %w", err)
	}
	if issue == nil || issue.ID == "" {
		return "", errors.New("bd create: empty issue returned")
	}
	return issue.ID, nil
}

// Sling shells out to `gt sling <bead> <rig>` so a fresh polecat picks up the
// mechanical fix and re-pushes. We pass --merge=mr (the default PR-town flow)
// implicitly by not overriding it; the rig's PR workflow takes the change back
// to the same PR thread.
func (d *GTDispatcher) Sling(beadID, rig string) error {
	bin := d.Bin
	if bin == "" {
		bin = "gt"
	}
	cmd := exec.Command(bin, "sling", beadID, rig) //nolint:gosec // bin operator-controlled
	if d.WorkDir != "" {
		cmd.Dir = d.WorkDir
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gt sling %s %s: %w (stderr: %s)", beadID, rig, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// MailAdapter shells out to `gt mail send mayor/` for judgment-class
// confirmation mail, mirroring ciwatcher.MailAdapter.
type MailAdapter struct {
	WorkDir string
	Bin     string
}

// NewMailAdapter constructs a MailAdapter.
func NewMailAdapter(workDir string) *MailAdapter {
	return &MailAdapter{WorkDir: workDir, Bin: "gt"}
}

// SendMayor sends mail to mayor/ via `gt mail send`. Body is passed via stdin.
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

package doctor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// MisfiledRigBugCheck detects code-bug beads filed in the town (hq-) database
// that belong in a rig's tracker (gs-/lb-/lw-/li-). The town db is for
// cross-rig coordination — convoys, mail, escalations, agent beads. A
// type=bug is a code defect owned by a specific rig; filing it under hq-
// hides it from that rig's `bd ready`/board and from per-rig ownership.
//
// hq-o1afr class: gastown daemon/scheduler/refinery bugs kept landing in the
// town db (created from the town/HQ context with the hq- prefix instead of the
// rig's). Routing itself is correct — routes.jsonl maps prefixes to dbs — so
// the defect is at create time, which a routing-table check can't catch. This
// check surfaces the result so misfiles are visible, not silent.
//
// No auto-fix: refiling means recreate-as-<rigprefix> + close-the-hq-original
// (no `bd move`), which is a deliberate, lossy data operation a human/mayor
// should drive. The check reports each misfile and its suggested target rig.
type MisfiledRigBugCheck struct {
	BaseCheck
}

// NewMisfiledRigBugCheck creates the misfiled-rig-bug check.
func NewMisfiledRigBugCheck() *MisfiledRigBugCheck {
	return &MisfiledRigBugCheck{
		BaseCheck: BaseCheck{
			CheckName:        "misfiled-rig-bug",
			CheckDescription: "Check for rig code-bug beads misfiled in the town (hq-) db",
			CheckCategory:    CategoryCleanup,
		},
	}
}

// townBug is the minimal shape parsed from `bd list --json` in the town db.
type townBug struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	IssueType string   `json:"issue_type"`
	Status    string   `json:"status"`
	Labels    []string `json:"labels"`
}

// misfiledBug is one suspected misfile plus the rig it most likely belongs to.
type misfiledBug struct {
	ID     string
	Title  string
	Rig    string
	Prefix string
}

// misfileRigHints maps tokens that imply a rig to that rig's prefix. Explicit
// rig names are listed before the gastown core subsystems so that a bug naming
// a specific rig (e.g. "lia_bac") wins over a generic subsystem word.
var misfileRigHints = []struct {
	prefix   string
	rig      string
	keywords []string
}{
	{"lb-", "lia_bac", []string{"lia_bac"}},
	{"lw-", "lia_web", []string{"lia_web"}},
	{"li-", "lia_iac", []string{"lia_iac"}},
	{"gs-", "gastown", []string{
		"gastown", "refinery", "daemon", "scheduler", "deacon", "polecat",
		"witness", "dispatch", "merge queue", "merge-queue", "plugin",
		"main_branch_test", "nudge-poller", "nudge poller",
	}},
}

// suggestRigForBug returns the (rig, prefix) a misfiled town bug most likely
// belongs to, scanning title + labels case-insensitively. Defaults to gastown
// (gs-): a type=bug in the town db with no rig-specific hint is still gastown
// code (the daemon/scheduler/town tooling IS the gastown rig), not cross-rig
// coordination.
func suggestRigForBug(title string, labels []string) (rig, prefix string) {
	hay := strings.ToLower(title + " " + strings.Join(labels, " "))
	for _, h := range misfileRigHints {
		for _, kw := range h.keywords {
			if strings.Contains(hay, kw) {
				return h.rig, h.prefix
			}
		}
	}
	return "gastown", "gs-"
}

// findMisfiledRigBugs filters the town-db listing down to type=bug beads under
// the hq- prefix and tags each with its suggested rig. Pure (no I/O) so the
// classification is unit-testable. Result is sorted by ID for stable output.
func findMisfiledRigBugs(bugs []townBug) []misfiledBug {
	var out []misfiledBug
	for _, b := range bugs {
		if b.IssueType != "bug" || !strings.HasPrefix(b.ID, "hq-") {
			continue
		}
		// Closed/tombstoned misfiles are historical — only flag live ones.
		if b.Status == "closed" || b.Status == "tombstone" {
			continue
		}
		rig, prefix := suggestRigForBug(b.Title, b.Labels)
		out = append(out, misfiledBug{ID: b.ID, Title: b.Title, Rig: rig, Prefix: prefix})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Run lists open beads in the town db and reports any misfiled rig code-bugs.
func (c *MisfiledRigBugCheck) Run(ctx *CheckContext) *CheckResult {
	townBeadsDir := filepath.Join(ctx.TownRoot, ".beads")

	cmd := exec.Command("bd", "list", "--status", "all", "--json", "--limit", "0")
	cmd.Dir = ctx.TownRoot
	cmd.Env = append(cmd.Environ(), "BEADS_DIR="+townBeadsDir)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not list town beads: %v", err),
			Details: []string{strings.TrimSpace(stderr.String())},
		}
	}

	// `bd list` may emit a non-JSON sentinel ("No issues found.") on empty.
	out := bytes.TrimSpace(stdout.Bytes())
	if len(out) == 0 || (out[0] != '[' && out[0] != '{') {
		return &CheckResult{Name: c.Name(), Status: StatusOK, Message: "No misfiled rig bugs in the town db"}
	}

	var bugs []townBug
	if err := json.Unmarshal(out, &bugs); err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not parse town beads listing: %v", err),
		}
	}

	misfiled := findMisfiledRigBugs(bugs)
	if len(misfiled) == 0 {
		return &CheckResult{Name: c.Name(), Status: StatusOK, Message: "No misfiled rig bugs in the town db"}
	}

	details := make([]string, 0, len(misfiled))
	for _, m := range misfiled {
		details = append(details, fmt.Sprintf("%s → should be %s (%s): %s", m.ID, m.Rig, m.Prefix, m.Title))
	}
	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d rig code-bug bead(s) misfiled in the town (hq-) db", len(misfiled)),
		Details: details,
		FixHint: "Refile each to its rig: recreate from the rig context (cd <rig> && bd create --type=bug ...) then close the hq- original with a pointer to the new ID. No auto-fix — refiling is a deliberate data move.",
	}
}

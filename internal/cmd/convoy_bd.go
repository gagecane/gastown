package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	convoyops "github.com/steveyegge/gastown/internal/convoy"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// getTownBeadsDir returns the town root directory for bd commands.
// Convoy commands run bd from town root (not .beads/) so bd discovers
// the correct database via its own workspace detection.
func getTownBeadsDir() (string, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	return townRoot, nil
}

// runBdJSON runs a bd command, captures stdout as JSON output. If the command
// fails, the error includes bd's stderr for diagnostics instead of a bare
// "exit status 1". BEADS_DIR is stripped from the subprocess environment to
// prevent stale overrides from interfering with bd's workspace detection.
func runBdJSON(dir string, args ...string) ([]byte, error) {
	// Strip --allow-stale if bd doesn't support it (version mismatch).
	if !beads.BdSupportsAllowStale() {
		filtered := make([]string, 0, len(args))
		for _, a := range args {
			if a != "--allow-stale" {
				filtered = append(filtered, a)
			}
		}
		args = filtered
	}
	cmd := exec.Command("bd", args...)
	cmd.Dir = dir
	// Strip BEADS_DIR so bd discovers the correct database from cmd.Dir
	// rather than using an inherited (possibly wrong) override.
	cmd.Env = stripEnvKey(os.Environ(), "BEADS_DIR")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errMsg := strings.TrimSpace(stderr.String()); errMsg != "" {
			return nil, fmt.Errorf("bd %s: %s", args[0], errMsg)
		}
		return nil, fmt.Errorf("bd %s: %w", args[0], err)
	}
	return stdout.Bytes(), nil
}

// bdDepListRawIDs queries the raw dependencies table via bd sql to get
// dependency target IDs. Unlike bd dep list, this does NOT join with the
// issues table, so it works for cross-database dependencies where the
// target issues live in a different Dolt database. See GH #2624.
//
// dir should be the town beads directory (.beads) for HQ queries.
// direction is "down" (issue_id → depends_on_id) or "up" (depends_on_id → issue_id).
// depType filters by dependency type (e.g., "tracks", "blocks"); empty means all types.
//
// Returns deduplicated, unwrapped issue IDs (external:prefix:id → id).
func bdDepListRawIDs(dir, issueID, direction, depType string) ([]string, error) {
	// Determine query columns based on direction.
	// "down": issueID depends on targets → SELECT depends_on_id WHERE issue_id = ?
	// "up":   issueID is depended on → SELECT issue_id WHERE depends_on_id = ?
	var selectCol, whereCol string
	if direction == "up" {
		selectCol = "issue_id"
		whereCol = "depends_on_id"
	} else {
		selectCol = "depends_on_id"
		whereCol = "issue_id"
	}

	// Build SQL query. Bead IDs are system-generated alphanumeric strings
	// with hyphens and dots — validate to prevent injection.
	if !isValidBeadID(issueID) {
		return nil, fmt.Errorf("invalid bead ID: %q", issueID)
	}

	query := fmt.Sprintf("SELECT %s FROM dependencies WHERE %s = '%s'", selectCol, whereCol, issueID)
	if depType != "" {
		if !isValidBeadID(depType) {
			return nil, fmt.Errorf("invalid dep type: %q", depType)
		}
		query += fmt.Sprintf(" AND type = '%s'", depType)
	}

	out, err := runBdJSON(dir, "sql", query, "--json")
	if err != nil {
		return nil, fmt.Errorf("bd sql for deps of %s: %w", issueID, err)
	}

	// Parse JSON array of single-column rows
	var rows []map[string]string
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parsing dep sql for %s: %w", issueID, err)
	}

	seen := make(map[string]bool, len(rows))
	var ids []string
	for _, row := range rows {
		rawID := row[selectCol]
		id := beads.ExtractIssueID(rawID)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// collectEpicChildren does a BFS walk of an epic's parent-child hierarchy and
// returns all slingable leaf descendants (task, bug, feature, chore).
func collectEpicChildren(epicID string) ([]string, error) {
	epic, err := bdShow(epicID)
	if err != nil {
		return nil, fmt.Errorf("epic '%s' not found: %w", epicID, err)
	}
	if epic.IssueType != "epic" {
		return nil, fmt.Errorf("'%s' is not an epic (type: %s); --from-epic only works with epic beads", epicID, epic.IssueType)
	}

	var issueIDs []string
	visited := make(map[string]bool)
	queue := []string{epicID}
	visited[epicID] = true

	for len(queue) > 0 {
		parentID := queue[0]
		queue = queue[1:]

		children, err := bdListChildren(parentID)
		if err != nil {
			style.PrintWarning("couldn't list children of %s: %v", parentID, err)
			continue
		}

		for _, child := range children {
			if visited[child.ID] {
				continue
			}
			visited[child.ID] = true

			if convoyops.IsSlingableType(child.IssueType) {
				issueIDs = append(issueIDs, child.ID)
			} else {
				// Non-slingable types (sub-epics, decisions) — recurse to find slingable descendants
				queue = append(queue, child.ID)
			}
		}
	}

	if len(issueIDs) == 0 {
		return nil, fmt.Errorf("epic '%s' has no slingable children (task, bug, feature, chore)", epicID)
	}
	return issueIDs, nil
}

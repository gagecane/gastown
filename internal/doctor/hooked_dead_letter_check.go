package doctor

import (
	"encoding/csv"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/steveyegge/gastown/internal/doltserver"
)

// HookedDeadLetterCheck detects mail beads stuck in the 'hooked' state past
// a dead-letter threshold. HANDOFF and other mail beads are placed in the
// issues table with status='hooked' so 'gt hook' can find them. If a
// successor session never consumes the hook (session died, rerouted, bead
// orphaned), the hook persists forever and accumulates as dead-letter.
//
// The reaper's main sweep targets the wisps table, not issues. This check
// surfaces the missing sweep so operators can see it before it triggers
// other health checks (backlog audits, doctor jsonl bloat, etc.).
//
// Related: gu-hhqk (GUPP: enforce TTL or guaranteed consumer).
type HookedDeadLetterCheck struct {
	BaseCheck
	threshold int // warn when hooked mail count > this (30 min is handled by TTL)
}

// DefaultDeadLetterThreshold is the number of hooked mail beads older than
// 30 minutes that should trigger a warning. Set to 10 per gu-hhqk AC #4.
const DefaultDeadLetterThreshold = 10

// NewHookedDeadLetterCheck creates a new hooked-dead-letter check.
func NewHookedDeadLetterCheck() *HookedDeadLetterCheck {
	return &HookedDeadLetterCheck{
		BaseCheck: BaseCheck{
			CheckName:        "hooked-dead-letter",
			CheckDescription: "Check for hooked mail beads stuck past the dead-letter threshold (gu-hhqk)",
			CheckCategory:    CategoryCleanup,
		},
		threshold: DefaultDeadLetterThreshold,
	}
}

// The SELECT limits the count to hooked mail (gt:message label) older than
// 30 minutes, excluding agent heartbeat beads and long-lived conventional
// labels. Matches the exclusion set used by reaper.ReapHookedMail.
//
// Also excludes beads that declare a still-open consumer via
// metadata.consumer_bead_id (gu-ub1l) — such beads have a guaranteed
// consumer and are exempt from dead-letter accounting. Must stay in sync
// with reaper.ConsumerAliveClause.
const hookedDeadLetterCountQuery = `
SELECT COUNT(DISTINCT i.id)
FROM issues i
INNER JOIN labels mail_l ON i.id = mail_l.issue_id
WHERE i.status = 'hooked'
  AND i.issue_type != 'agent'
  AND i.created_at < DATE_SUB(UTC_TIMESTAMP(), INTERVAL 30 MINUTE)
  AND mail_l.label = 'gt:message'
  AND i.id NOT IN (
    SELECT l2.issue_id FROM labels l2
    WHERE l2.label IN ('gt:standing-orders', 'gt:keep', 'gt:role', 'gt:rig')
  )
  AND NOT EXISTS (
    SELECT 1 FROM issues c
    WHERE c.id = JSON_UNQUOTE(JSON_EXTRACT(i.metadata, '$.consumer_bead_id'))
    AND c.status != 'closed'
  )`

// Run scans all rig databases (Dolt) for hooked mail beads past the dead-letter threshold.
func (c *HookedDeadLetterCheck) Run(ctx *CheckContext) *CheckResult {
	databases, err := doltserver.ListDatabases(ctx.TownRoot)
	if err != nil || len(databases) == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No rig databases found (skipping)",
			Category: c.Category(),
		}
	}

	perDB := make(map[string]int)
	total := 0
	for _, db := range databases {
		rigDir := filepath.Join(ctx.TownRoot, db)
		count, err := queryHookedDeadLetterCount(rigDir)
		if err != nil {
			// Non-fatal: Dolt may be unreachable for this rig.
			continue
		}
		if count > 0 {
			perDB[db] = count
			total += count
		}
	}

	if total <= c.threshold {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("Hooked dead-letter backlog OK (%d <= threshold %d)", total, c.threshold),
			Category: c.Category(),
		}
	}

	details := make([]string, 0, len(perDB))
	for db, n := range perDB {
		details = append(details, fmt.Sprintf("[%s] %d hooked mail bead(s) older than 30 min", db, n))
	}

	return &CheckResult{
		Name:   c.Name(),
		Status: StatusWarning,
		Message: fmt.Sprintf(
			"%d hooked mail bead(s) past dead-letter threshold (%d) — indicates consumer starvation or lifecycle leak",
			total, c.threshold,
		),
		Details: details,
		FixHint: "Run 'gt reaper reap-hooked-mail' or wait for next mol-dog-reaper cycle",
		Category: c.Category(),
	}
}

// queryHookedDeadLetterCount returns the number of hooked mail beads older
// than 30 minutes for the rig at rigDir. Uses 'bd sql --csv' to bypass the
// bd ORM and query Dolt directly.
func queryHookedDeadLetterCount(rigDir string) (int, error) {
	cmd := exec.Command("bd", "sql", "--csv", hookedDeadLetterCountQuery) //nolint:gosec // G204: query is a constant
	cmd.Dir = rigDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("bd sql: %w", err)
	}

	r := csv.NewReader(strings.NewReader(string(output)))
	records, err := r.ReadAll()
	if err != nil || len(records) < 2 {
		return 0, nil
	}

	// Single-column result; skip CSV header row.
	rec := records[1]
	if len(rec) < 1 {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(rec[0]))
	if err != nil {
		return 0, nil
	}
	return n, nil
}

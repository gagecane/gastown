package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/liveness"
)

// OrphanedAdmissionRecordsCheck detects polecat-admission reservation records
// whose owning process is dead. This is the detection counterpart to the
// scheduler's automatic reap (gu-t6jqq): on 2026-06-01 two orphaned admission
// records with dead PIDs counted as occupied scheduler capacity and caused a
// 2+ hour rig-wide dispatch stall. The records live on disk under
// <townRoot>/.runtime/polecat-admission/*.json and are reloaded on daemon
// start, so they survive restarts and require a manual trace+rm to clear.
//
// Surfacing them proactively converts a silent multi-hour "drained capacity
// stays empty" stall into an immediate alert. With --fix, the orphaned
// records are reaped (liveness is re-checked at fix time so a record whose
// process came back is never removed). (gu-r0pkt)
type OrphanedAdmissionRecordsCheck struct {
	FixableCheck
	orphans []orphanedAdmissionRecord
}

// orphanedAdmissionRecord captures the fields needed to report and reap a
// single dead-PID admission record. Mirrors the on-disk reservation written
// by internal/cmd/polecat_capacity.go (only the fields this check needs).
type orphanedAdmissionRecord struct {
	path      string
	id        string
	pid       int
	bead      string
	createdAt time.Time
}

// admissionReservationFile is the subset of the on-disk reservation schema
// (internal/cmd polecatAdmissionReservation) this check parses.
type admissionReservationFile struct {
	ID        string    `json:"id"`
	PID       int       `json:"pid"`
	Bead      string    `json:"bead,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// NewOrphanedAdmissionRecordsCheck creates a new orphaned-admission-records check.
func NewOrphanedAdmissionRecordsCheck() *OrphanedAdmissionRecordsCheck {
	return &OrphanedAdmissionRecordsCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "orphaned-admission-records",
				CheckDescription: "Detect polecat-admission records whose owning process is dead",
				CheckCategory:    CategoryCleanup,
			},
		},
	}
}

// admissionRecordPIDAlive reports whether the given PID is a live process.
// Uses signal 0 as a no-op liveness probe, matching the pattern in
// stale_sql_server_info_check.go. A non-positive PID is treated as dead.
var admissionRecordPIDAlive = liveness.PIDAlive

// Run scans the polecat-admission directory for dead-PID records.
func (c *OrphanedAdmissionRecordsCheck) Run(ctx *CheckContext) *CheckResult {
	c.orphans = nil

	dir := filepath.Join(ctx.TownRoot, ".runtime", "polecat-admission")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// No admission directory yet — nothing reserved, nothing to leak.
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusOK,
				Message: "No polecat-admission records",
			}
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Could not read polecat-admission directory",
			Details: []string{err.Error()},
		}
	}

	now := nowFn().UTC()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path) //nolint:gosec // G304: path derived from trusted townRoot
		if err != nil {
			continue
		}
		var rec admissionReservationFile
		if err := json.Unmarshal(data, &rec); err != nil {
			// Malformed records are the scheduler's cleanup responsibility
			// (readPolecatAdmissionReservations removes them); don't double-report.
			continue
		}
		if rec.PID <= 0 || admissionRecordPIDAlive(rec.PID) {
			continue
		}
		c.orphans = append(c.orphans, orphanedAdmissionRecord{
			path:      path,
			id:        rec.ID,
			pid:       rec.PID,
			bead:      rec.Bead,
			createdAt: rec.CreatedAt,
		})
	}

	if len(c.orphans) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No orphaned polecat-admission records",
		}
	}

	details := make([]string, 0, len(c.orphans))
	for _, o := range c.orphans {
		bead := o.bead
		if bead == "" {
			bead = "(none)"
		}
		age := "unknown"
		if !o.createdAt.IsZero() {
			age = now.Sub(o.createdAt).Round(time.Second).String()
		}
		details = append(details, fmt.Sprintf("dead PID %d: %s (bead=%s, age=%s)",
			o.pid, filepath.Base(o.path), bead, age))
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusError,
		Message: fmt.Sprintf("%d orphaned admission record(s) holding scheduler capacity", len(c.orphans)),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to reap dead-PID admission records (frees the held capacity).",
	}
}

// Fix removes the orphaned admission records found by Run. Liveness is
// re-checked here so a record whose process came back between Run and Fix is
// never removed. Each reaped record is recorded in the returned check details
// via the streaming re-run, providing the audit trail.
func (c *OrphanedAdmissionRecordsCheck) Fix(ctx *CheckContext) error {
	for _, o := range c.orphans {
		// Re-check liveness at fix time: never reap a record whose owning
		// process is alive (the PID may have been reused or the process
		// resumed since Run scanned).
		if admissionRecordPIDAlive(o.pid) {
			continue
		}
		if err := os.Remove(o.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing orphaned admission record %s: %w", filepath.Base(o.path), err)
		}
	}
	return nil
}

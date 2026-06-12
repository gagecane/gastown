package doctor

import (
	"fmt"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/doltserver"
)

// DoltPortDataDirCheck detects a Dolt sql-server holding the town's configured
// port while serving from a data directory OTHER than the town's canonical
// .dolt-data/. This is the imposter condition behind town-wide beads outages
// (gs-2oij): a rogue server auto-started from a rig cwd (e.g. .beads/dolt) with
// no --data-dir flag binds :3307 and serves the legacy/empty scaffold, so every
// bd command fails with 'database "<rig>" not found'.
//
// The existing stale-dolt-port check inspects port FILES and embedded config
// directories on disk; it does not inspect the LIVE process on the port. This
// check closes that gap by comparing the running server's data dir against the
// expected one.
type DoltPortDataDirCheck struct {
	BaseCheck
}

// NewDoltPortDataDirCheck creates a new Dolt port data-dir mismatch check.
func NewDoltPortDataDirCheck() *DoltPortDataDirCheck {
	return &DoltPortDataDirCheck{
		BaseCheck: BaseCheck{
			CheckName:        "dolt-port-data-dir",
			CheckDescription: "Detect a Dolt server on the town port serving the wrong data directory",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

// Run checks whether a foreign Dolt server holds the town's configured port.
func (c *DoltPortDataDirCheck) Run(ctx *CheckContext) *CheckResult {
	if ctx == nil || ctx.TownRoot == "" {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No town root configured",
			Category: c.CheckCategory,
		}
	}

	cfg := doltserver.DefaultConfig(ctx.TownRoot)
	expectedDir, _ := filepath.Abs(cfg.DataDir)

	// CheckPortConflict returns a non-zero PID only when a Dolt server holds the
	// configured port AND does not match this town's expected data dir. It is a
	// no-op (0, "") for remote configs, a free port, or this town's own server.
	conflictPID, conflictDir := doltserver.CheckPortConflict(ctx.TownRoot)
	if conflictPID == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  fmt.Sprintf("Town Dolt port %d serves the canonical data directory", cfg.Port),
			Category: c.CheckCategory,
		}
	}

	if conflictDir == "" {
		conflictDir = "unknown"
	}

	return &CheckResult{
		Name:     c.Name(),
		Status:   StatusError,
		Message:  fmt.Sprintf("Dolt server (PID %d) holds port %d but serves the wrong data directory", conflictPID, cfg.Port),
		Category: c.CheckCategory,
		Details: []string{
			fmt.Sprintf("Serving from: %s", conflictDir),
			fmt.Sprintf("Expected:     %s", expectedDir),
			"This imposter serves stale/empty databases — bd commands town-wide will fail with 'database not found'.",
		},
		FixHint: "Run 'gt dolt kill-imposters' to remove it, then 'gt dolt start' (or 'gt dolt restart')",
	}
}

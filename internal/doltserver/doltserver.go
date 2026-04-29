// Package doltserver manages the Dolt SQL server for Gas Town.
//
// The Dolt server provides multi-client access to beads databases,
// avoiding the single-writer limitation of embedded Dolt mode.
//
// Server configuration:
//   - Port: 3307 (avoids conflict with MySQL on 3306)
//   - User: root (default Dolt user, no password for localhost)
//   - Data directory: ~/gt/.dolt-data/ (contains all rig databases)
//
// Each rig (hq, gastown, beads) has its own database subdirectory:
//
//	~/gt/.dolt-data/
//	├── hq/        # Town beads (hq-*)
//	├── gastown/   # Gastown rig (gt-*)
//	├── beads/     # Beads rig (bd-*)
//	└── ...        # Other rigs
//
// Usage:
//
//	gt dolt start           # Start the server
//	gt dolt stop            # Stop the server
//	gt dolt status          # Check server status
//	gt dolt logs            # View server logs
//	gt dolt sql             # Open SQL shell
//	gt dolt init-rig <name> # Initialize a new rig database
//
// This package is split across multiple files by concern:
//
//   - config.go      — Server configuration (Config, DefaultConfig, env/YAML loading)
//   - identity.go    — Dolt global identity setup (EnsureDoltIdentity)
//   - state.go       — Runtime state persistence (State, LoadState, SaveState)
//   - lifecycle.go   — Start/Stop/IsRunning/WaitForReady, writeServerConfig
//   - port.go        — TCP port utilities (CheckPortAvailable, FindFreePort, DoltListener)
//   - process.go     — Process/PID inspection, imposter detection, StopIdleMonitors
//   - databases.go   — ListDatabases/VerifyDatabases, RemoveDatabase, db cache
//   - rig.go         — InitRig, FindRigBeadsDir, FindOrCreateRigBeadsDir, prefix maps
//   - migration.go   — Legacy beads-dir -> .dolt-data migration (Migration struct)
//   - workspace.go   — BrokenWorkspace/OrphanedDatabase detection and repair
//   - metadata.go    — EnsureMetadata / EnsureAllMetadata (metadata.json sync)
//   - health.go      — HealthMetrics, CheckReadOnly, RecoverReadOnly, helpers
//   - sql.go         — Low-level SQL execution helpers (doltSQL, retries, scripts)
//   - sysproc_unix.go / sysproc_windows.go — OS-specific process helpers
package doltserver

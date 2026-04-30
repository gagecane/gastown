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
package doltserver

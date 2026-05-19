package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// StaleEmbeddeddoltCheck detects vestigial embeddeddolt/ directories that exist
// alongside server-mode metadata.json files. These directories are remnants from
// the migration to shared Dolt sql-server mode and should be removed.
type StaleEmbeddeddoltCheck struct {
	FixableCheck
	staleEmbeddeddolts []embeddeddoltInfo
}

type embeddeddoltInfo struct {
	path     string
	metaPath string
}

// NewStaleEmbeddeddoltCheck creates a new stale embeddeddolt check.
func NewStaleEmbeddeddoltCheck() *StaleEmbeddeddoltCheck {
	return &StaleEmbeddeddoltCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "stale-embeddeddolt",
				CheckDescription: "Detect stale embeddeddolt/ directories left from embedded→server migration",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run checks for embeddeddolt directories alongside server-mode metadata.json files.
func (c *StaleEmbeddeddoltCheck) Run(ctx *CheckContext) *CheckResult {
	c.staleEmbeddeddolts = nil

	// Check town root .beads
	c.checkBeadsDir(ctx.TownRoot, filepath.Join(ctx.TownRoot, ".beads"))

	// Check rigs
	rigsConfig := filepath.Join(ctx.TownRoot, "mayor", "rigs.json")
	if data, err := os.ReadFile(rigsConfig); err == nil {
		var rigs struct {
			Rigs map[string]struct{} `json:"rigs"`
		}
		if json.Unmarshal(data, &rigs) == nil {
			for rigName := range rigs.Rigs {
				rigBeadsPath := filepath.Join(ctx.TownRoot, rigName, ".beads")
				c.checkBeadsDir(ctx.TownRoot, rigBeadsPath)

				// Also check mayor/rig/.beads
				maybeBeadsPath := filepath.Join(ctx.TownRoot, rigName, "mayor", "rig", ".beads")
				c.checkBeadsDir(ctx.TownRoot, maybeBeadsPath)
			}
		}
	}

	if len(c.staleEmbeddeddolts) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No stale embeddeddolt directories found",
		}
	}

	var details []string
	for _, info := range c.staleEmbeddeddolts {
		relPath, _ := filepath.Rel(ctx.TownRoot, info.path)
		relMetaPath, _ := filepath.Rel(ctx.TownRoot, info.metaPath)
		details = append(details, fmt.Sprintf("Stale embeddeddolt/ at %s alongside server-mode %s", relPath, relMetaPath))
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d stale embeddeddolt director(ies) found alongside server-mode metadata.json", len(c.staleEmbeddeddolts)),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to remove stale embeddeddolt directories",
	}
}

// Fix removes stale embeddeddolt directories.
func (c *StaleEmbeddeddoltCheck) Fix(ctx *CheckContext) error {
	for _, info := range c.staleEmbeddeddolts {
		if err := os.RemoveAll(info.path); err != nil {
			return fmt.Errorf("could not remove stale embeddeddolt directory %s: %w", info.path, err)
		}
	}
	return nil
}

// checkBeadsDir checks if a .beads directory has both embeddeddolt/ and server-mode metadata.json.
func (c *StaleEmbeddeddoltCheck) checkBeadsDir(townRoot string, beadsPath string) {
	// Check if .beads directory exists
	if _, err := os.Stat(beadsPath); os.IsNotExist(err) {
		return
	}

	// Check if metadata.json exists and is server-mode
	metadataPath := filepath.Join(beadsPath, "metadata.json")
	if !c.isServerModeMetadata(metadataPath) {
		return
	}

	// Check if embeddeddolt directory exists
	embeddeddoltPath := filepath.Join(beadsPath, "embeddeddolt")
	if _, err := os.Stat(embeddeddoltPath); os.IsNotExist(err) {
		return
	}

	// We found a stale embeddeddolt directory
	c.staleEmbeddeddolts = append(c.staleEmbeddeddolts, embeddeddoltInfo{
		path:     embeddeddoltPath,
		metaPath: metadataPath,
	})
}

// isServerModeMetadata checks if metadata.json is configured for server mode.
func (c *StaleEmbeddeddoltCheck) isServerModeMetadata(metadataPath string) bool {
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return false
	}

	var metadata struct {
		DoltMode string `json:"dolt_mode"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return false
	}

	return metadata.DoltMode == "server"
}

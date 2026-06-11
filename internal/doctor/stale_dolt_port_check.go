package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// StaleDoltPortCheck detects stale Dolt port files that point to wrong ports.
// This can cause bd commands to fail with "database not found" errors when
// they connect to the wrong Dolt server.
// It also checks metadata.json files for port consistency with the running server.
type StaleDoltPortCheck struct {
	FixableCheck
	stalePorts    []stalePortInfo
	staleMetadata []staleMetadataInfo
}

type stalePortInfo struct {
	path        string
	port        int
	correctPort int
}

type staleMetadataInfo struct {
	path        string
	port        int
	correctPort int
}

// NewStaleDoltPortCheck creates a new stale Dolt port check.
func NewStaleDoltPortCheck() *StaleDoltPortCheck {
	return &StaleDoltPortCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "stale-dolt-port",
				CheckDescription: "Detect stale Dolt port files pointing to wrong ports",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run checks for stale Dolt port files and metadata.json port consistency.
func (c *StaleDoltPortCheck) Run(ctx *CheckContext) *CheckResult {
	c.stalePorts = nil
	c.staleMetadata = nil

	// Get the correct port from the main Dolt config
	correctPort := c.getCorrectPort(ctx)
	if correctPort == 0 {
		correctPort = 3307 // default
	}

	// Find all dolt-server.port files
	portFiles := c.findPortFiles(ctx.TownRoot)

	var details []string
	for _, portFile := range portFiles {
		data, err := os.ReadFile(portFile)
		if err != nil {
			continue
		}

		portStr := strings.TrimSpace(string(data))
		port, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}

		// Check if port matches the correct port
		if port != correctPort {
			c.stalePorts = append(c.stalePorts, stalePortInfo{
				path:        portFile,
				port:        port,
				correctPort: correctPort,
			})
			relPath, _ := filepath.Rel(ctx.TownRoot, portFile)
			details = append(details, fmt.Sprintf("Stale port file %s has port %d (should be %d)", relPath, port, correctPort))
		}
	}

	// Check metadata.json files for port consistency
	metadataFiles := c.findMetadataFiles(ctx.TownRoot)
	for _, metaFile := range metadataFiles {
		port := c.getPortFromMetadata(metaFile)
		if port > 0 && port != correctPort {
			c.staleMetadata = append(c.staleMetadata, staleMetadataInfo{
				path:        metaFile,
				port:        port,
				correctPort: correctPort,
			})
			relPath, _ := filepath.Rel(ctx.TownRoot, metaFile)
			details = append(details, fmt.Sprintf("metadata.json %s has port %d (should be %d)", relPath, port, correctPort))
		}
	}

	// Also check for stale embedded dolt config directories. In a server-mode
	// town, no .beads/dolt/ directory should carry its own sql-server config.yaml
	// with a listener port — a stray `bd` invocation can launch an embedded
	// server from it that hijacks the shared port. The most dangerous variant
	// hardcodes the shared port itself (gu-msz5t): when the GT-managed server is
	// momentarily down, the embedded server binds :<correctPort> and serves FRESH
	// EMPTY databases, causing a town-wide bd outage.
	staleConfigs := c.findStaleDoltConfigs(ctx.TownRoot)
	for _, config := range staleConfigs {
		relPath, _ := filepath.Rel(ctx.TownRoot, config.path)
		if config.port == correctPort {
			details = append(details, fmt.Sprintf("Embedded Dolt config %s hardcodes shared port %d — can hijack the GT-managed server when it is down (imposter hazard)", relPath, config.port))
		} else {
			details = append(details, fmt.Sprintf("Stale Dolt config directory: %s (binds port %d, should use shared server on %d)", relPath, config.port, correctPort))
		}
	}

	if len(c.stalePorts) == 0 && len(c.staleMetadata) == 0 && len(staleConfigs) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "All Dolt port files are consistent",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d stale port file(s), %d stale metadata.json(s), %d stale config dir(s)", len(c.stalePorts), len(c.staleMetadata), len(staleConfigs)),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to fix port inconsistencies",
	}
}

// Fix removes stale Dolt port files and fixes metadata.json port mismatches.
func (c *StaleDoltPortCheck) Fix(ctx *CheckContext) error {
	// Remove stale port files
	for _, info := range c.stalePorts {
		if err := os.Remove(info.path); err != nil {
			return fmt.Errorf("could not remove stale port file %s: %w", info.path, err)
		}
	}

	// Fix metadata.json files with wrong ports
	for _, info := range c.staleMetadata {
		if err := c.fixMetadataPort(info.path, info.correctPort); err != nil {
			return fmt.Errorf("could not fix metadata.json %s: %w", info.path, err)
		}
	}

	return nil
}

// getCorrectPort returns the port from the main Dolt server config.
func (c *StaleDoltPortCheck) getCorrectPort(ctx *CheckContext) int {
	// Check the main Dolt server config
	configPath := filepath.Join(ctx.TownRoot, ".dolt-data", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 0
	}

	// Parse port from config.yaml
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "port:" && i+1 < len(lines) {
			portStr := strings.TrimSpace(strings.TrimPrefix(lines[i+1], "port:"))
			port, err := strconv.Atoi(portStr)
			if err == nil {
				return port
			}
		}
		if strings.HasPrefix(line, "  port:") {
			portStr := strings.TrimSpace(strings.TrimPrefix(line, "  port:"))
			port, err := strconv.Atoi(portStr)
			if err == nil {
				return port
			}
		}
	}

	return 0
}

// findPortFiles finds all dolt-server.port files in known locations.
// Avoids filepath.Walk over the entire town root, which is extremely slow
// on Docker bind mounts (macOS VirtioFS).
func (c *StaleDoltPortCheck) findPortFiles(townRoot string) []string {
	var files []string

	// Known locations for port files
	locations := []string{
		filepath.Join(townRoot, ".beads", "dolt-server.port"),
		filepath.Join(townRoot, ".dolt-data", ".beads", "dolt-server.port"),
		filepath.Join(townRoot, "daemon", "dolt.port"),
	}

	// Rig .beads directories (same discovery pattern as findMetadataFiles)
	rigsConfig := filepath.Join(townRoot, "mayor", "rigs.json")
	if data, err := os.ReadFile(rigsConfig); err == nil {
		var rigs struct {
			Rigs map[string]struct{} `json:"rigs"`
		}
		if json.Unmarshal(data, &rigs) == nil {
			for rigName := range rigs.Rigs {
				locations = append(locations,
					filepath.Join(townRoot, rigName, "mayor", "rig", ".beads", "dolt-server.port"),
					filepath.Join(townRoot, rigName, ".beads", "dolt-server.port"),
				)
			}
		}
	}

	for _, loc := range locations {
		if _, err := os.Stat(loc); err == nil {
			files = append(files, loc)
		}
	}

	return files
}

// staleDoltConfig describes an embedded .beads/dolt/ directory that carries its
// own sql-server config.yaml in a server-mode town. port is the listener port
// parsed from that config (0 if none was found).
type staleDoltConfig struct {
	path string
	port int
}

// findStaleDoltConfigs finds embedded Dolt config directories that should not
// exist when the town uses the shared Dolt server. It scans the town root AND
// every rig (the rig-level gap that let gu-msz5t through), and flags ANY
// .beads/dolt/config.yaml that declares a listener port — including one that
// hardcodes the shared port, which is the most dangerous imposter launchpad.
func (c *StaleDoltPortCheck) findStaleDoltConfigs(townRoot string) []staleDoltConfig {
	var staleConfigs []staleDoltConfig

	beadsDirs := []string{filepath.Join(townRoot, ".beads")}

	// Rig .beads directories (same discovery pattern as findPortFiles).
	rigsConfig := filepath.Join(townRoot, "mayor", "rigs.json")
	if data, err := os.ReadFile(rigsConfig); err == nil {
		var rigs struct {
			Rigs map[string]struct{} `json:"rigs"`
		}
		if json.Unmarshal(data, &rigs) == nil {
			for rigName := range rigs.Rigs {
				beadsDirs = append(beadsDirs,
					filepath.Join(townRoot, rigName, ".beads"),
					filepath.Join(townRoot, rigName, "mayor", "rig", ".beads"),
				)
			}
		}
	}

	for _, beadsDir := range beadsDirs {
		// Check for .beads/dolt/ directory which shouldn't exist when using shared Dolt server
		staleDir := filepath.Join(beadsDir, "dolt")
		configPath := filepath.Join(staleDir, "config.yaml")
		data, err := os.ReadFile(configPath)
		if err != nil {
			continue
		}
		// Only an embedded sql-server config declares a listener port. A config
		// without a port can't bind anything, so it's not an imposter hazard.
		if port := parseListenerPort(string(data)); port > 0 {
			staleConfigs = append(staleConfigs, staleDoltConfig{path: staleDir, port: port})
		}
	}

	return staleConfigs
}

// parseListenerPort extracts the listener port from an embedded Dolt
// sql-server config.yaml. It honors YAML comments (lines starting with '#' are
// ignored) so a commented-out "# port: 3307" does not produce a false positive.
// Returns 0 if no active port directive is found.
func parseListenerPort(content string) int {
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(trimmed, "port:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "port:"))
		// Strip any inline comment.
		if idx := strings.Index(value, "#"); idx >= 0 {
			value = strings.TrimSpace(value[:idx])
		}
		if port, err := strconv.Atoi(value); err == nil {
			return port
		}
	}
	return 0
}

// findMetadataFiles finds all metadata.json files that might contain Dolt port config.
func (c *StaleDoltPortCheck) findMetadataFiles(townRoot string) []string {
	var files []string

	// Town root metadata
	townMeta := filepath.Join(townRoot, ".beads", "metadata.json")
	if _, err := os.Stat(townMeta); err == nil {
		files = append(files, townMeta)
	}

	// Rig metadata files
	rigsConfig := filepath.Join(townRoot, "mayor", "rigs.json")
	if data, err := os.ReadFile(rigsConfig); err == nil {
		var rigs struct {
			Rigs map[string]struct{} `json:"rigs"`
		}
		if json.Unmarshal(data, &rigs) == nil {
			for rigName := range rigs.Rigs {
				rigMeta := filepath.Join(townRoot, rigName, "mayor", "rig", ".beads", "metadata.json")
				if _, err := os.Stat(rigMeta); err == nil {
					files = append(files, rigMeta)
				}
			}
		}
	}

	return files
}

// getPortFromMetadata reads the dolt_server_port from a metadata.json file.
func (c *StaleDoltPortCheck) getPortFromMetadata(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}

	var metadata struct {
		DoltServerPort int `json:"dolt_server_port"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return 0
	}

	return metadata.DoltServerPort
}

// fixMetadataPort updates the dolt_server_port in a metadata.json file.
func (c *StaleDoltPortCheck) fixMetadataPort(path string, correctPort int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// Parse as generic map to preserve all fields
	var metadata map[string]interface{}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return err
	}

	// Update the port
	metadata["dolt_server_port"] = correctPort

	// Write back
	newData, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, newData, 0644)
}

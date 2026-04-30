package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// EscalationConfigPath returns the standard path for escalation config in a town.
func EscalationConfigPath(townRoot string) string {
	return filepath.Join(townRoot, "settings", "escalation.json")
}

// LoadEscalationConfig loads and validates an escalation configuration file.
func LoadEscalationConfig(path string) (*EscalationConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed internally, not from user input
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("reading escalation config: %w", err)
	}

	var config EscalationConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing escalation config: %w", err)
	}

	if err := validateEscalationConfig(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

// LoadOrCreateEscalationConfig loads the escalation config, creating a default if not found.
func LoadOrCreateEscalationConfig(path string) (*EscalationConfig, error) {
	config, err := LoadEscalationConfig(path)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return NewEscalationConfig(), nil
		}
		return nil, err
	}
	return config, nil
}

// SaveEscalationConfig saves an escalation configuration to a file.
func SaveEscalationConfig(path string, config *EscalationConfig) error {
	if err := validateEscalationConfig(config); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding escalation config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil { //nolint:gosec // G306: escalation config doesn't contain secrets
		return fmt.Errorf("writing escalation config: %w", err)
	}

	return nil
}

// validateEscalationConfig validates an EscalationConfig.
func validateEscalationConfig(c *EscalationConfig) error {
	if c.Type != "escalation" && c.Type != "" {
		return fmt.Errorf("%w: expected type 'escalation', got '%s'", ErrInvalidType, c.Type)
	}
	if c.Version > CurrentEscalationVersion {
		return fmt.Errorf("%w: got %d, max supported %d", ErrInvalidVersion, c.Version, CurrentEscalationVersion)
	}

	// Validate stale_threshold if specified
	if c.StaleThreshold != "" {
		if _, err := time.ParseDuration(c.StaleThreshold); err != nil {
			return fmt.Errorf("invalid stale_threshold: %w", err)
		}
	}

	// Initialize nil maps
	if c.Routes == nil {
		c.Routes = make(map[string][]string)
	}

	// Validate severity route keys
	for severity := range c.Routes {
		if !IsValidSeverity(severity) {
			return fmt.Errorf("%w: unknown severity '%s' (valid: low, medium, high, critical)", ErrMissingField, severity)
		}
	}

	// Validate max_reescalations is non-negative
	if c.MaxReescalations != nil && *c.MaxReescalations < 0 {
		return fmt.Errorf("%w: max_reescalations must be non-negative", ErrMissingField)
	}

	return nil
}

// GetStaleThreshold returns the stale threshold as a time.Duration.
// Returns 4 hours if not configured or invalid.
func (c *EscalationConfig) GetStaleThreshold() time.Duration {
	if c.StaleThreshold == "" {
		return 4 * time.Hour
	}
	d, err := time.ParseDuration(c.StaleThreshold)
	if err != nil {
		return 4 * time.Hour
	}
	return d
}

// GetRouteForSeverity returns the escalation route actions for a given severity.
// Falls back to ["bead", "mail:mayor"] if no specific route is configured.
func (c *EscalationConfig) GetRouteForSeverity(severity string) []string {
	if route, ok := c.Routes[severity]; ok {
		return route
	}
	// Fallback to default route
	return []string{"bead", "mail:mayor"}
}

// GetMaxReescalations returns the maximum number of re-escalations allowed.
// Returns 2 if not configured (nil). Explicit 0 means "never re-escalate".
func (c *EscalationConfig) GetMaxReescalations() int {
	if c.MaxReescalations == nil {
		return 2
	}
	return *c.MaxReescalations
}

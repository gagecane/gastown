package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// LoadMessagingConfig loads and validates a messaging configuration file.
func LoadMessagingConfig(path string) (*MessagingConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed internally, not from user input
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("reading messaging config: %w", err)
	}

	var config MessagingConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing messaging config: %w", err)
	}

	if err := validateMessagingConfig(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

// SaveMessagingConfig saves a messaging configuration to a file.
func SaveMessagingConfig(path string, config *MessagingConfig) error {
	if err := validateMessagingConfig(config); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding messaging config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil { //nolint:gosec // G306: messaging config doesn't contain secrets
		return fmt.Errorf("writing messaging config: %w", err)
	}

	return nil
}

// validateMessagingConfig validates a MessagingConfig.
func validateMessagingConfig(c *MessagingConfig) error {
	if c.Type != "messaging" && c.Type != "" {
		return fmt.Errorf("%w: expected type 'messaging', got '%s'", ErrInvalidType, c.Type)
	}
	if c.Version > CurrentMessagingVersion {
		return fmt.Errorf("%w: got %d, max supported %d", ErrInvalidVersion, c.Version, CurrentMessagingVersion)
	}

	// Initialize nil maps
	if c.Lists == nil {
		c.Lists = make(map[string][]string)
	}
	if c.Queues == nil {
		c.Queues = make(map[string]QueueConfig)
	}
	if c.Announces == nil {
		c.Announces = make(map[string]AnnounceConfig)
	}
	if c.NudgeChannels == nil {
		c.NudgeChannels = make(map[string][]string)
	}

	// Validate lists have at least one recipient
	for name, recipients := range c.Lists {
		if len(recipients) == 0 {
			return fmt.Errorf("%w: list '%s' has no recipients", ErrMissingField, name)
		}
	}

	// Validate queues have at least one worker
	for name, queue := range c.Queues {
		if len(queue.Workers) == 0 {
			return fmt.Errorf("%w: queue '%s' workers", ErrMissingField, name)
		}
		if queue.MaxClaims < 0 {
			return fmt.Errorf("%w: queue '%s' max_claims must be non-negative", ErrMissingField, name)
		}
	}

	// Validate announces have at least one reader
	for name, announce := range c.Announces {
		if len(announce.Readers) == 0 {
			return fmt.Errorf("%w: announce '%s' readers", ErrMissingField, name)
		}
		if announce.RetainCount < 0 {
			return fmt.Errorf("%w: announce '%s' retain_count must be non-negative", ErrMissingField, name)
		}
	}

	// Validate nudge channels have non-empty names and at least one recipient
	for name, recipients := range c.NudgeChannels {
		if name == "" {
			return fmt.Errorf("%w: nudge channel name cannot be empty", ErrMissingField)
		}
		if len(recipients) == 0 {
			return fmt.Errorf("%w: nudge channel '%s' has no recipients", ErrMissingField, name)
		}
	}

	return nil
}

// MessagingConfigPath returns the standard path for messaging config in a town.
func MessagingConfigPath(townRoot string) string {
	return filepath.Join(townRoot, "config", "messaging.json")
}

// LoadOrCreateMessagingConfig loads the messaging config, creating a default if not found.
func LoadOrCreateMessagingConfig(path string) (*MessagingConfig, error) {
	config, err := LoadMessagingConfig(path)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return NewMessagingConfig(), nil
		}
		return nil, err
	}
	return config, nil
}

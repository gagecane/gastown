package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/style"
)

// setMaintenanceConfig sets a maintenance.* key in daemon.json (patrol config).
func setMaintenanceConfig(townRoot, key, value string) error {
	patrolConfig := daemon.LoadPatrolConfig(townRoot)
	if patrolConfig == nil {
		patrolConfig = &daemon.DaemonPatrolConfig{
			Type:    "daemon-patrol-config",
			Version: 1,
		}
	}
	if patrolConfig.Patrols == nil {
		patrolConfig.Patrols = &daemon.PatrolsConfig{}
	}
	if patrolConfig.Patrols.ScheduledMaintenance == nil {
		patrolConfig.Patrols.ScheduledMaintenance = &daemon.ScheduledMaintenanceConfig{}
	}
	mc := patrolConfig.Patrols.ScheduledMaintenance

	switch key {
	case "maintenance.window":
		// Validate HH:MM format
		parts := strings.SplitN(value, ":", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid window format %q: expected HH:MM (e.g., 03:00)", value)
		}
		hour, err := strconv.Atoi(parts[0])
		if err != nil || hour < 0 || hour > 23 {
			return fmt.Errorf("invalid hour in %q: expected 0-23", value)
		}
		minute, err := strconv.Atoi(parts[1])
		if err != nil || minute < 0 || minute > 59 {
			return fmt.Errorf("invalid minute in %q: expected 0-59", value)
		}
		mc.Window = fmt.Sprintf("%02d:%02d", hour, minute)
		mc.Enabled = true // Setting window enables the patrol

	case "maintenance.interval":
		switch value {
		case "daily", "weekly", "monthly":
			mc.Interval = value
		default:
			// Try parsing as Go duration
			_, err := time.ParseDuration(value)
			if err != nil {
				return fmt.Errorf("invalid interval %q: expected daily, weekly, monthly, or Go duration (e.g., 48h)", value)
			}
			mc.Interval = value
		}

	case "maintenance.threshold":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return fmt.Errorf("invalid threshold %q: expected positive integer", value)
		}
		mc.Threshold = &n
	}

	if err := daemon.SavePatrolConfig(townRoot, patrolConfig); err != nil {
		return fmt.Errorf("saving daemon config: %w", err)
	}

	fmt.Printf("Set %s = %s\n", style.Bold.Render(key), value)
	if key == "maintenance.window" {
		fmt.Printf("Scheduled maintenance enabled (window: %s, interval: %s)\n",
			mc.Window, mc.Interval)
		if mc.Interval == "" {
			fmt.Println("Hint: set interval with: gt config set maintenance.interval daily")
		}
	}
	return nil
}

// getMaintenanceConfig gets a maintenance.* key from daemon.json (patrol config).
func getMaintenanceConfig(townRoot, key string) error {
	patrolConfig := daemon.LoadPatrolConfig(townRoot)

	var value string
	switch key {
	case "maintenance.window":
		if patrolConfig != nil && patrolConfig.Patrols != nil && patrolConfig.Patrols.ScheduledMaintenance != nil {
			value = patrolConfig.Patrols.ScheduledMaintenance.Window
		}
		if value == "" {
			value = "(not set)"
		}

	case "maintenance.interval":
		if patrolConfig != nil && patrolConfig.Patrols != nil && patrolConfig.Patrols.ScheduledMaintenance != nil {
			value = patrolConfig.Patrols.ScheduledMaintenance.Interval
		}
		if value == "" {
			value = "daily"
		}

	case "maintenance.threshold":
		threshold := 1000 // default
		if patrolConfig != nil && patrolConfig.Patrols != nil && patrolConfig.Patrols.ScheduledMaintenance != nil {
			if patrolConfig.Patrols.ScheduledMaintenance.Threshold != nil {
				threshold = *patrolConfig.Patrols.ScheduledMaintenance.Threshold
			}
		}
		value = strconv.Itoa(threshold)
	}

	fmt.Println(value)
	return nil
}

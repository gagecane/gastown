package cmd

import (
	"fmt"
	"strconv"
	"time"

	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/style"
)

// setLifecycleConfig sets a lifecycle.* key in daemon.json.
func setLifecycleConfig(townRoot, key, value string) error {
	patrolConfig := daemon.LoadPatrolConfig(townRoot)
	if patrolConfig == nil {
		patrolConfig = daemon.DefaultLifecycleConfig()
	}
	if patrolConfig.Patrols == nil {
		patrolConfig.Patrols = &daemon.PatrolsConfig{}
	}

	switch key {
	// Reaper
	case "lifecycle.reaper.enabled":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("invalid value for %s: %w (expected true/false)", key, err)
		}
		if patrolConfig.Patrols.WispReaper == nil {
			patrolConfig.Patrols.WispReaper = &daemon.WispReaperConfig{}
		}
		patrolConfig.Patrols.WispReaper.Enabled = b

	case "lifecycle.reaper.interval":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("invalid duration for %s: %w", key, err)
		}
		if patrolConfig.Patrols.WispReaper == nil {
			patrolConfig.Patrols.WispReaper = &daemon.WispReaperConfig{Enabled: true}
		}
		patrolConfig.Patrols.WispReaper.IntervalStr = value

	case "lifecycle.reaper.delete_age":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("invalid duration for %s: %w", key, err)
		}
		if patrolConfig.Patrols.WispReaper == nil {
			patrolConfig.Patrols.WispReaper = &daemon.WispReaperConfig{Enabled: true}
		}
		patrolConfig.Patrols.WispReaper.DeleteAgeStr = value

	// Compactor
	case "lifecycle.compactor.enabled":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("invalid value for %s: %w (expected true/false)", key, err)
		}
		if patrolConfig.Patrols.CompactorDog == nil {
			patrolConfig.Patrols.CompactorDog = &daemon.CompactorDogConfig{}
		}
		patrolConfig.Patrols.CompactorDog.Enabled = b

	case "lifecycle.compactor.interval":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("invalid duration for %s: %w", key, err)
		}
		if patrolConfig.Patrols.CompactorDog == nil {
			patrolConfig.Patrols.CompactorDog = &daemon.CompactorDogConfig{Enabled: true}
		}
		patrolConfig.Patrols.CompactorDog.IntervalStr = value

	case "lifecycle.compactor.threshold":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return fmt.Errorf("invalid threshold for %s: expected positive integer", key)
		}
		if patrolConfig.Patrols.CompactorDog == nil {
			patrolConfig.Patrols.CompactorDog = &daemon.CompactorDogConfig{Enabled: true}
		}
		patrolConfig.Patrols.CompactorDog.Threshold = n

	// Doctor
	case "lifecycle.doctor.enabled":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("invalid value for %s: %w (expected true/false)", key, err)
		}
		if patrolConfig.Patrols.DoctorDog == nil {
			patrolConfig.Patrols.DoctorDog = &daemon.DoctorDogConfig{}
		}
		patrolConfig.Patrols.DoctorDog.Enabled = b

	case "lifecycle.doctor.interval":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("invalid duration for %s: %w", key, err)
		}
		if patrolConfig.Patrols.DoctorDog == nil {
			patrolConfig.Patrols.DoctorDog = &daemon.DoctorDogConfig{Enabled: true}
		}
		patrolConfig.Patrols.DoctorDog.IntervalStr = value

	// Backup (controls both JSONL and Dolt backup)
	case "lifecycle.backup.enabled":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("invalid value for %s: %w (expected true/false)", key, err)
		}
		if patrolConfig.Patrols.JsonlGitBackup == nil {
			patrolConfig.Patrols.JsonlGitBackup = &daemon.JsonlGitBackupConfig{}
		}
		patrolConfig.Patrols.JsonlGitBackup.Enabled = b
		if patrolConfig.Patrols.DoltBackup == nil {
			patrolConfig.Patrols.DoltBackup = &daemon.DoltBackupConfig{}
		}
		patrolConfig.Patrols.DoltBackup.Enabled = b

	case "lifecycle.backup.interval":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("invalid duration for %s: %w", key, err)
		}
		if patrolConfig.Patrols.JsonlGitBackup == nil {
			patrolConfig.Patrols.JsonlGitBackup = &daemon.JsonlGitBackupConfig{Enabled: true}
		}
		patrolConfig.Patrols.JsonlGitBackup.IntervalStr = value
		if patrolConfig.Patrols.DoltBackup == nil {
			patrolConfig.Patrols.DoltBackup = &daemon.DoltBackupConfig{Enabled: true}
		}
		patrolConfig.Patrols.DoltBackup.IntervalStr = value

	default:
		return fmt.Errorf("unknown lifecycle key: %q\n\nSupported lifecycle keys:\n  lifecycle.reaper.enabled\n  lifecycle.reaper.interval\n  lifecycle.reaper.delete_age\n  lifecycle.compactor.enabled\n  lifecycle.compactor.interval\n  lifecycle.compactor.threshold\n  lifecycle.doctor.enabled\n  lifecycle.doctor.interval\n  lifecycle.backup.enabled\n  lifecycle.backup.interval", key)
	}

	if err := daemon.SavePatrolConfig(townRoot, patrolConfig); err != nil {
		return fmt.Errorf("saving daemon config: %w", err)
	}

	fmt.Printf("Set %s = %s\n", style.Bold.Render(key), value)
	return nil
}

// getLifecycleConfig gets a lifecycle.* key from daemon.json.
func getLifecycleConfig(townRoot, key string) error {
	patrolConfig := daemon.LoadPatrolConfig(townRoot)

	var value string
	switch key {
	// Reaper
	case "lifecycle.reaper.enabled":
		if patrolConfig != nil && patrolConfig.Patrols != nil && patrolConfig.Patrols.WispReaper != nil {
			value = strconv.FormatBool(patrolConfig.Patrols.WispReaper.Enabled)
		} else {
			value = "false (not configured)"
		}

	case "lifecycle.reaper.interval":
		if patrolConfig != nil && patrolConfig.Patrols != nil && patrolConfig.Patrols.WispReaper != nil && patrolConfig.Patrols.WispReaper.IntervalStr != "" {
			value = patrolConfig.Patrols.WispReaper.IntervalStr
		} else {
			value = "30m (default)"
		}

	case "lifecycle.reaper.delete_age":
		if patrolConfig != nil && patrolConfig.Patrols != nil && patrolConfig.Patrols.WispReaper != nil && patrolConfig.Patrols.WispReaper.DeleteAgeStr != "" {
			value = patrolConfig.Patrols.WispReaper.DeleteAgeStr
		} else {
			value = "168h (default, 7 days)"
		}

	// Compactor
	case "lifecycle.compactor.enabled":
		if patrolConfig != nil && patrolConfig.Patrols != nil && patrolConfig.Patrols.CompactorDog != nil {
			value = strconv.FormatBool(patrolConfig.Patrols.CompactorDog.Enabled)
		} else {
			value = "false (not configured)"
		}

	case "lifecycle.compactor.interval":
		if patrolConfig != nil && patrolConfig.Patrols != nil && patrolConfig.Patrols.CompactorDog != nil && patrolConfig.Patrols.CompactorDog.IntervalStr != "" {
			value = patrolConfig.Patrols.CompactorDog.IntervalStr
		} else {
			value = "24h (default)"
		}

	case "lifecycle.compactor.threshold":
		if patrolConfig != nil && patrolConfig.Patrols != nil && patrolConfig.Patrols.CompactorDog != nil && patrolConfig.Patrols.CompactorDog.Threshold > 0 {
			value = strconv.Itoa(patrolConfig.Patrols.CompactorDog.Threshold)
		} else {
			value = "500 (default)"
		}

	// Doctor
	case "lifecycle.doctor.enabled":
		if patrolConfig != nil && patrolConfig.Patrols != nil && patrolConfig.Patrols.DoctorDog != nil {
			value = strconv.FormatBool(patrolConfig.Patrols.DoctorDog.Enabled)
		} else {
			value = "false (not configured)"
		}

	case "lifecycle.doctor.interval":
		if patrolConfig != nil && patrolConfig.Patrols != nil && patrolConfig.Patrols.DoctorDog != nil && patrolConfig.Patrols.DoctorDog.IntervalStr != "" {
			value = patrolConfig.Patrols.DoctorDog.IntervalStr
		} else {
			value = "5m (default)"
		}

	// Backup
	case "lifecycle.backup.enabled":
		jsonlEnabled := patrolConfig != nil && patrolConfig.Patrols != nil && patrolConfig.Patrols.JsonlGitBackup != nil && patrolConfig.Patrols.JsonlGitBackup.Enabled
		doltEnabled := patrolConfig != nil && patrolConfig.Patrols != nil && patrolConfig.Patrols.DoltBackup != nil && patrolConfig.Patrols.DoltBackup.Enabled
		if jsonlEnabled || doltEnabled {
			value = fmt.Sprintf("jsonl=%v dolt=%v", jsonlEnabled, doltEnabled)
		} else {
			value = "false (not configured)"
		}

	case "lifecycle.backup.interval":
		if patrolConfig != nil && patrolConfig.Patrols != nil && patrolConfig.Patrols.JsonlGitBackup != nil && patrolConfig.Patrols.JsonlGitBackup.IntervalStr != "" {
			value = patrolConfig.Patrols.JsonlGitBackup.IntervalStr
		} else {
			value = "15m (default)"
		}

	default:
		return fmt.Errorf("unknown lifecycle key: %q\n\nSupported lifecycle keys:\n  lifecycle.reaper.enabled\n  lifecycle.reaper.interval\n  lifecycle.reaper.delete_age\n  lifecycle.compactor.enabled\n  lifecycle.compactor.interval\n  lifecycle.compactor.threshold\n  lifecycle.doctor.enabled\n  lifecycle.doctor.interval\n  lifecycle.backup.enabled\n  lifecycle.backup.interval", key)
	}

	fmt.Println(value)
	return nil
}

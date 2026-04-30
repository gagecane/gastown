package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// configSetCmd sets a town config value by dot-notation key.
var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a configuration value",
	Long: `Set a town configuration value using dot-notation keys.

Supported keys:
  convoy.notify_on_complete   Push notification to Mayor session on convoy
                              completion (true/false, default: false)
  cli_theme                   CLI color scheme ("dark", "light", "auto")
  default_agent               Default agent preset name
  dolt.port                   Dolt SQL server port (default: 3307). Set this when
                              another Gas Town instance is using the same port.
                              Writes GT_DOLT_PORT to mayor/daemon.json env section.
  scheduler.max_polecats      Dispatch mode: -1 = direct (default), N > 0 = deferred
  scheduler.batch_size        Beads per heartbeat (default: 1)
  scheduler.spawn_delay       Delay between spawns (default: 0s)
  maintenance.window          Maintenance window start time in HH:MM (e.g., "03:00")
  maintenance.interval        How often: "daily", "weekly", "monthly", or duration
  maintenance.threshold       Commit count threshold (default: 1000)

  Lifecycle (Dolt data maintenance):
  lifecycle.reaper.enabled     Enable/disable wisp reaper (true/false)
  lifecycle.reaper.interval    Reaper check interval (default: 30m)
  lifecycle.reaper.delete_age  Delete closed wisps after this duration (default: 168h / 7d)
  lifecycle.compactor.enabled  Enable/disable compactor dog (true/false)
  lifecycle.compactor.interval Compactor check interval (default: 24h)
  lifecycle.compactor.threshold Commit count before compaction (default: 500)
  lifecycle.doctor.enabled     Enable/disable doctor dog (true/false)
  lifecycle.doctor.interval    Doctor check interval (default: 5m)
  lifecycle.backup.enabled     Enable/disable JSONL + Dolt backups (true/false)
  lifecycle.backup.interval    Backup interval (default: 15m)

Examples:
  gt config set convoy.notify_on_complete true
  gt config set cli_theme dark
  gt config set default_agent claude
  gt config set dolt.port 3308
  gt config set scheduler.max_polecats 5
  gt config set maintenance.window 03:00
  gt config set maintenance.interval daily
  gt config set lifecycle.reaper.delete_age 336h
  gt config set lifecycle.compactor.threshold 1000`,
	Args: cobra.ExactArgs(2),
	RunE: runConfigSet,
}

// configGetCmd gets a town config value by dot-notation key.
var configGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Get a configuration value",
	Long: `Get a town configuration value using dot-notation keys.

Supported keys:
  convoy.notify_on_complete   Push notification to Mayor session on convoy
                              completion (true/false, default: false)
  cli_theme                   CLI color scheme
  default_agent               Default agent preset name
  scheduler.max_polecats      Dispatch mode (-1 = direct, N > 0 = deferred)
  scheduler.batch_size        Beads per heartbeat
  scheduler.spawn_delay       Delay between spawns
  maintenance.window          Maintenance window start time (HH:MM)
  maintenance.interval        How often: daily, weekly, monthly, or duration
  maintenance.threshold       Commit count threshold

  Lifecycle (Dolt data maintenance):
  lifecycle.reaper.enabled     Wisp reaper enabled (true/false)
  lifecycle.reaper.interval    Reaper check interval
  lifecycle.reaper.delete_age  Duration before closed wisps are deleted
  lifecycle.compactor.enabled  Compactor dog enabled (true/false)
  lifecycle.compactor.interval Compactor check interval
  lifecycle.compactor.threshold Commit count threshold for compaction
  lifecycle.doctor.enabled     Doctor dog enabled (true/false)
  lifecycle.doctor.interval    Doctor check interval
  lifecycle.backup.enabled     JSONL + Dolt backups enabled (true/false)
  lifecycle.backup.interval    Backup interval

Examples:
  gt config get convoy.notify_on_complete
  gt config get cli_theme
  gt config get maintenance.window
  gt config get lifecycle.reaper.delete_age`,
	Args: cobra.ExactArgs(1),
	RunE: runConfigGet,
}

func runConfigSet(cmd *cobra.Command, args []string) error {
	key := args[0]
	value := args[1]

	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	settingsPath := config.TownSettingsPath(townRoot)
	townSettings, err := config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("loading town settings: %w", err)
	}

	switch key {
	case "convoy.notify_on_complete":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("invalid value for %s: %w (expected true/false)", key, err)
		}
		if townSettings.Convoy == nil {
			townSettings.Convoy = &config.ConvoyConfig{}
		}
		townSettings.Convoy.NotifyOnComplete = b

	case "cli_theme":
		switch value {
		case "dark", "light", "auto":
			townSettings.CLITheme = value
		default:
			return fmt.Errorf("invalid cli_theme: %q (expected dark, light, or auto)", value)
		}

	case "default_agent":
		townSettings.DefaultAgent = value

	case "scheduler.max_polecats":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid value for %s: %w (expected integer)", key, err)
		}
		if n < -1 {
			return fmt.Errorf("invalid value for %s: must be >= -1 (-1 = direct dispatch, 0 = direct dispatch, N > 0 = deferred)", key)
		}
		if townSettings.Scheduler == nil {
			townSettings.Scheduler = capacity.DefaultSchedulerConfig()
		}
		townSettings.Scheduler.MaxPolecats = &n

	case "scheduler.batch_size":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return fmt.Errorf("invalid value for %s: expected positive integer", key)
		}
		if townSettings.Scheduler == nil {
			townSettings.Scheduler = capacity.DefaultSchedulerConfig()
		}
		townSettings.Scheduler.BatchSize = &n

	case "scheduler.spawn_delay":
		// Validate it parses as a duration
		_, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid value for %s: %w (expected Go duration, e.g. 2s, 500ms)", key, err)
		}
		if townSettings.Scheduler == nil {
			townSettings.Scheduler = capacity.DefaultSchedulerConfig()
		}
		townSettings.Scheduler.SpawnDelay = value

	case "maintenance.window", "maintenance.interval", "maintenance.threshold":
		return setMaintenanceConfig(townRoot, key, value)

	case "dolt.port":
		port, err := strconv.Atoi(value)
		if err != nil || port < 1024 || port > 65535 {
			return fmt.Errorf("invalid value for %s: expected port number 1024-65535", key)
		}
		patrolCfg := daemon.LoadPatrolConfig(townRoot)
		if patrolCfg == nil {
			patrolCfg = &daemon.DaemonPatrolConfig{Type: "daemon-patrol-config", Version: 1}
		}
		if patrolCfg.Env == nil {
			patrolCfg.Env = make(map[string]string)
		}
		patrolCfg.Env["GT_DOLT_PORT"] = value
		if err := daemon.SavePatrolConfig(townRoot, patrolCfg); err != nil {
			return fmt.Errorf("saving daemon.json: %w", err)
		}
		fmt.Printf("Set GT_DOLT_PORT = %s in mayor/daemon.json\n", style.Bold.Render(value))
		fmt.Printf("  %s\n", style.Dim.Render("Restart the daemon for the change to take effect: gt daemon restart"))
		return nil

	default:
		if strings.HasPrefix(key, "lifecycle.") {
			return setLifecycleConfig(townRoot, key, value)
		}
		return fmt.Errorf("unknown config key: %q\n\nSupported keys:\n  convoy.notify_on_complete\n  cli_theme\n  default_agent\n  dolt.port\n  scheduler.max_polecats\n  scheduler.batch_size\n  scheduler.spawn_delay\n  maintenance.window\n  maintenance.interval\n  maintenance.threshold\n  lifecycle.reaper.*\n  lifecycle.compactor.*\n  lifecycle.doctor.*\n  lifecycle.backup.*", key)
	}

	if err := config.SaveTownSettings(settingsPath, townSettings); err != nil {
		return fmt.Errorf("saving town settings: %w", err)
	}

	fmt.Printf("Set %s = %s\n", style.Bold.Render(key), value)
	return nil
}

func runConfigGet(cmd *cobra.Command, args []string) error {
	key := args[0]

	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	settingsPath := config.TownSettingsPath(townRoot)
	townSettings, err := config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("loading town settings: %w", err)
	}

	var value string
	switch key {
	case "convoy.notify_on_complete":
		if townSettings.Convoy != nil && townSettings.Convoy.NotifyOnComplete {
			value = "true"
		} else {
			value = "false"
		}

	case "cli_theme":
		value = townSettings.CLITheme
		if value == "" {
			value = "auto"
		}

	case "default_agent":
		value = townSettings.DefaultAgent
		if value == "" {
			value = "claude"
		}

	case "scheduler.max_polecats":
		scfg := townSettings.Scheduler
		if scfg == nil {
			scfg = capacity.DefaultSchedulerConfig()
		}
		value = strconv.Itoa(scfg.GetMaxPolecats())

	case "scheduler.batch_size":
		scfg := townSettings.Scheduler
		if scfg == nil {
			scfg = capacity.DefaultSchedulerConfig()
		}
		value = strconv.Itoa(scfg.GetBatchSize())

	case "scheduler.spawn_delay":
		scfg := townSettings.Scheduler
		if scfg == nil {
			scfg = capacity.DefaultSchedulerConfig()
		}
		value = scfg.GetSpawnDelay().String()

	case "maintenance.window", "maintenance.interval", "maintenance.threshold":
		return getMaintenanceConfig(townRoot, key)

	case "dolt.port":
		patrolCfg := daemon.LoadPatrolConfig(townRoot)
		if patrolCfg != nil {
			if v, ok := patrolCfg.Env["GT_DOLT_PORT"]; ok {
				fmt.Println(v)
				return nil
			}
		}
		fmt.Println("3307") // DefaultPort
		return nil

	default:
		if strings.HasPrefix(key, "lifecycle.") {
			return getLifecycleConfig(townRoot, key)
		}
		return fmt.Errorf("unknown config key: %q\n\nSupported keys:\n  convoy.notify_on_complete\n  cli_theme\n  default_agent\n  dolt.port\n  scheduler.max_polecats\n  scheduler.batch_size\n  scheduler.spawn_delay\n  maintenance.window\n  maintenance.interval\n  maintenance.threshold\n  lifecycle.reaper.*\n  lifecycle.compactor.*\n  lifecycle.doctor.*\n  lifecycle.backup.*", key)
	}

	fmt.Println(value)
	return nil
}

// parseBool parses a boolean string (true/false, yes/no, 1/0).
func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "true", "yes", "1", "on":
		return true, nil
	case "false", "no", "0", "off":
		return false, nil
	default:
		return false, fmt.Errorf("cannot parse %q as boolean", s)
	}
}

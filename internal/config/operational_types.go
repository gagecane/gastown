// Operational threshold configuration types. Extracted from types.go.
//
// OperationalConfig and its sub-structs define tunable thresholds that were
// previously hardcoded as Go constants throughout the codebase. Accessor
// methods and parser helpers for these types live in operational.go.
package config

import "time"

// OperationalConfig groups operational thresholds that were previously hardcoded
// as Go constants. All fields are optional — omitted values use compiled-in defaults.
// This enables per-town tuning without code changes (ZFC: Zero Fixed Constants).
type OperationalConfig struct {
	// Session configures session management thresholds.
	Session *SessionThresholds `json:"session,omitempty"`

	// Nudge configures nudge delivery thresholds.
	Nudge *NudgeThresholds `json:"nudge,omitempty"`

	// Daemon configures daemon lifecycle thresholds.
	Daemon *DaemonThresholds `json:"daemon,omitempty"`

	// Deacon configures deacon health-check thresholds.
	Deacon *DeaconThresholds `json:"deacon,omitempty"`

	// Polecat configures polecat session thresholds.
	Polecat *PolecatThresholds `json:"polecat,omitempty"`

	// Dolt configures Dolt server operation thresholds.
	Dolt *DoltThresholds `json:"dolt,omitempty"`

	// Mail configures mail system thresholds.
	Mail *MailThresholds `json:"mail,omitempty"`

	// Web configures web API thresholds.
	Web *WebThresholds `json:"web,omitempty"`

	// Witness configures witness patrol thresholds.
	Witness *WitnessThresholds `json:"witness,omitempty"`
}

// SessionThresholds configures session management timeouts.
type SessionThresholds struct {
	// ClaudeStartTimeout is how long to wait for Claude to start (default "60s").
	ClaudeStartTimeout string `json:"claude_start_timeout,omitempty"`

	// ShellReadyTimeout is how long to wait for shell prompt after command (default "5s").
	ShellReadyTimeout string `json:"shell_ready_timeout,omitempty"`

	// GracefulShutdownTimeout is wait after Ctrl-C before force-kill (default "3s").
	GracefulShutdownTimeout string `json:"graceful_shutdown_timeout,omitempty"`

	// BdCommandTimeout is timeout for bd CLI command execution (default "30s").
	BdCommandTimeout string `json:"bd_command_timeout,omitempty"`

	// BdSubprocessTimeout is timeout for bd subprocess calls in TUI (default "5s").
	BdSubprocessTimeout string `json:"bd_subprocess_timeout,omitempty"`

	// GUPPViolationTimeout is how long an agent can have hooked work
	// without progressing before GUPP violation (default "30m").
	GUPPViolationTimeout string `json:"gupp_violation_timeout,omitempty"`

	// HungSessionThreshold is how long a tmux session can be inactive
	// before considered hung (default "30m").
	HungSessionThreshold string `json:"hung_session_threshold,omitempty"`

	// StartupNudgeVerifyDelay is wait after startup nudge before checking (default "5s").
	StartupNudgeVerifyDelay string `json:"startup_nudge_verify_delay,omitempty"`

	// StartupNudgeMaxRetries is max retries for startup nudge (default 3).
	StartupNudgeMaxRetries *int `json:"startup_nudge_max_retries,omitempty"`
}

// NudgeThresholds configures nudge queue and delivery timeouts.
type NudgeThresholds struct {
	// ReadyTimeout is how long NudgeSession waits for pane to accept input (default "10s").
	ReadyTimeout string `json:"ready_timeout,omitempty"`

	// RetryInterval is base interval between send-keys retry attempts (default "500ms").
	RetryInterval string `json:"retry_interval,omitempty"`

	// LockTimeout is how long to hold the nudge lock (default "30s").
	LockTimeout string `json:"lock_timeout,omitempty"`

	// NormalTTL is time-to-live for normal-priority nudges (default "30m").
	NormalTTL string `json:"normal_ttl,omitempty"`

	// UrgentTTL is time-to-live for urgent-priority nudges (default "2h").
	UrgentTTL string `json:"urgent_ttl,omitempty"`

	// MaxQueueDepth is max pending nudges per session (default 50).
	MaxQueueDepth *int `json:"max_queue_depth,omitempty"`

	// StaleClaimThreshold is how long a .claimed file must be untouched
	// before treated as orphan (default "5m").
	StaleClaimThreshold string `json:"stale_claim_threshold,omitempty"`
}

// DaemonThresholds configures daemon lifecycle and patrol thresholds.
type DaemonThresholds struct {
	// MassDeathWindow is time window for detecting mass session death (default "30s").
	MassDeathWindow string `json:"mass_death_window,omitempty"`

	// MassDeathThreshold is session deaths within window to trigger alert (default 3).
	MassDeathThreshold *int `json:"mass_death_threshold,omitempty"`

	// DogIdleSessionTimeout is how long a dog can be idle with tmux before kill (default "1h").
	DogIdleSessionTimeout string `json:"dog_idle_session_timeout,omitempty"`

	// DogIdleRemoveTimeout is how long a dog can be idle before removal (default "4h").
	DogIdleRemoveTimeout string `json:"dog_idle_remove_timeout,omitempty"`

	// PolecatIdleSessionTimeout is how long a polecat can be idle before its session
	// is killed to prevent API slot burn (default "15m"). Polecats are ephemeral workers;
	// unlike dogs, they should not persist when idle.
	PolecatIdleSessionTimeout string `json:"polecat_idle_session_timeout,omitempty"`

	// DeadPolecatReapTimeout is how long a polecat's tmux session must be dead
	// (with a stale heartbeat) before its in_progress/hooked beads are auto-reset
	// to open for re-dispatch (default "1h"). Protects against stuck patrol wisps
	// accumulating when a polecat hard-crashes (OOM, tmux kill) and cannot run
	// its Stop hook to signal completion. See gu-1x0j.
	DeadPolecatReapTimeout string `json:"dead_polecat_reap_timeout,omitempty"`

	// PolecatSelfTerminate controls whether polecats kill their own session after
	// gt done completes (default false). When true, polecats terminate 3 seconds
	// after work submission instead of transitioning to IDLE. This gives fresh
	// context windows per task, reduces token waste, and eliminates stale state
	// issues at scale. Worktree reuse is preserved — ReuseIdlePolecat creates
	// a fresh branch on the existing worktree.
	PolecatSelfTerminate *bool `json:"polecat_self_terminate,omitempty"`

	// StaleWorkingTimeout is how long a dog in state=working with no activity
	// before considered stuck (default "2h").
	StaleWorkingTimeout string `json:"stale_working_timeout,omitempty"`

	// MaxDogPoolSize is target dog pool size (default 4).
	MaxDogPoolSize *int `json:"max_dog_pool_size,omitempty"`

	// MaxLifecycleMessageAge is max age of lifecycle mail before discard (default "6h").
	MaxLifecycleMessageAge string `json:"max_lifecycle_message_age,omitempty"`

	// SyncFailureEscalationThreshold is consecutive git pull failures before
	// logging escalates from WARN to ERROR (default 3).
	SyncFailureEscalationThreshold *int `json:"sync_failure_escalation_threshold,omitempty"`

	// DoctorMolCooldown is min interval between mol-dog-doctor molecules (default "5m").
	DoctorMolCooldown string `json:"doctor_mol_cooldown,omitempty"`

	// RecoveryHeartbeatInterval is the fixed interval for recovery-focused daemon heartbeat (default "3m").
	RecoveryHeartbeatInterval string `json:"recovery_heartbeat_interval,omitempty"`

	// BootSpawnCooldown prevents Boot from spawning on every daemon heartbeat (default "2m").
	BootSpawnCooldown string `json:"boot_spawn_cooldown,omitempty"`

	// DeaconGracePeriod is time to wait after starting Deacon before checking heartbeat (default "5m").
	DeaconGracePeriod string `json:"deacon_grace_period,omitempty"`

	// PressureCPUThreshold is the per-core load average above which new
	// non-infrastructure spawns are deferred. Disabled by default (0).
	// Recommended starting value: 3.0 (only trips under severe load).
	PressureCPUThreshold *float64 `json:"pressure_cpu_threshold,omitempty"`

	// PressureMemThresholdGB is the minimum available memory (in GB) below
	// which new non-infrastructure spawns are deferred. Disabled by default (0).
	// Recommended starting value: 0.5 (only trips when swapping).
	PressureMemThresholdGB *float64 `json:"pressure_mem_threshold_gb,omitempty"`

	// PressureMaxSessions is the maximum number of concurrent agent tmux
	// sessions before new non-infrastructure spawns are deferred. Disabled by default (0 = unlimited).
	PressureMaxSessions *int `json:"pressure_max_sessions,omitempty"`
}

// DeaconThresholds configures deacon health-check and dispatch thresholds.
type DeaconThresholds struct {
	// PingTimeout is how long to wait for HEALTH_CHECK nudge response (default "30s").
	PingTimeout string `json:"ping_timeout,omitempty"`

	// ConsecutiveFailures is health check failures before force-kill (default 3).
	ConsecutiveFailures *int `json:"consecutive_failures,omitempty"`

	// Cooldown is minimum time between force-kills of same agent (default "5m").
	Cooldown string `json:"cooldown,omitempty"`

	// HeartbeatStaleThreshold is age at which deacon heartbeat is stale (default "5m").
	HeartbeatStaleThreshold string `json:"heartbeat_stale_threshold,omitempty"`

	// HeartbeatVeryStaleThreshold is age at which heartbeat is very stale (default "15m").
	HeartbeatVeryStaleThreshold string `json:"heartbeat_very_stale_threshold,omitempty"`

	// MaxRedispatches is max times a bead can be re-dispatched before escalating (default 3).
	MaxRedispatches *int `json:"max_redispatches,omitempty"`

	// RedispatchCooldown is min time between re-dispatches of same bead (default "5m").
	RedispatchCooldown string `json:"redispatch_cooldown,omitempty"`

	// MaxFeedsPerCycle is max stranded convoys to feed per invocation (default 3).
	MaxFeedsPerCycle *int `json:"max_feeds_per_cycle,omitempty"`

	// FeedCooldown is min time between feeding same convoy (default "10m").
	FeedCooldown string `json:"feed_cooldown,omitempty"`
}

// PolecatThresholds configures polecat session and retry thresholds.
type PolecatThresholds struct {
	// HeartbeatStaleThreshold is age at which polecat heartbeat is stale (default "3m").
	HeartbeatStaleThreshold string `json:"heartbeat_stale_threshold,omitempty"`

	// DoltMaxRetries is max retries for Dolt operations (default 10).
	DoltMaxRetries *int `json:"dolt_max_retries,omitempty"`

	// DoltBaseBackoff is base backoff for Dolt retry loop (default "500ms").
	DoltBaseBackoff string `json:"dolt_base_backoff,omitempty"`

	// DoltBackoffMax is cap for Dolt retry backoff (default "30s").
	DoltBackoffMax string `json:"dolt_backoff_max,omitempty"`

	// PendingMaxAge is max age for .pending reservation marker (default "5m").
	PendingMaxAge string `json:"pending_max_age,omitempty"`

	// NamepoolSize is number of name slots in pool (default 50).
	NamepoolSize *int `json:"namepool_size,omitempty"`
}

// DoltThresholds configures Dolt server operation thresholds.
type DoltThresholds struct {
	// HealthCheckInterval is how often Dolt health check fires (default "30s").
	HealthCheckInterval string `json:"health_check_interval,omitempty"`

	// CmdTimeout is timeout for individual dolt CLI commands (default "15s").
	CmdTimeout string `json:"cmd_timeout,omitempty"`

	// MaxConnections is max concurrent connections (default 1000).
	MaxConnections *int `json:"max_connections,omitempty"`

	// SlowQueryThreshold is duration above which a query is flagged slow (default "1s").
	SlowQueryThreshold string `json:"slow_query_threshold,omitempty"`
}

// MailThresholds configures mail system thresholds.
type MailThresholds struct {
	// IdleNotifyTimeout is how long to wait for idle notify (default "3s").
	IdleNotifyTimeout string `json:"idle_notify_timeout,omitempty"`

	// BdReadTimeout is timeout for bd read operations (default "60s").
	BdReadTimeout string `json:"bd_read_timeout,omitempty"`

	// BdWriteTimeout is timeout for bd write operations (default "60s").
	BdWriteTimeout string `json:"bd_write_timeout,omitempty"`

	// MaxConcurrentAckOps is max concurrent mail acknowledge operations (default 8).
	MaxConcurrentAckOps *int `json:"max_concurrent_ack_ops,omitempty"`

	// ReplyReminderDelay is how long after mail delivery to nudge the recipient
	// to reply via gt mail send rather than in chat (default "30s").
	// Set to "0s" to disable reply reminders entirely.
	ReplyReminderDelay string `json:"reply_reminder_delay,omitempty"`
}

// WebThresholds configures web API thresholds.
type WebThresholds struct {
	// MaxConcurrentCommands is max concurrent gt subprocesses via web API (default 12).
	MaxConcurrentCommands *int `json:"max_concurrent_commands,omitempty"`

	// MaxSubjectLen is max subject length for mail API (default 500).
	MaxSubjectLen *int `json:"max_subject_len,omitempty"`

	// MaxBodyLen is max body length for mail API (default 100000).
	MaxBodyLen *int `json:"max_body_len,omitempty"`
}

// WitnessThresholds configures witness patrol detection thresholds.
type WitnessThresholds struct {
	// StartupStallThreshold is the minimum session age before a session with no
	// recent activity is considered stalled at startup (default "90s").
	StartupStallThreshold string `json:"startup_stall_threshold,omitempty"`

	// StartupActivityGrace is the max time since last activity before a session
	// old enough to be past startup is considered stalled (default "60s").
	StartupActivityGrace string `json:"startup_activity_grace,omitempty"`

	// MaxBeadRespawns is the threshold above which a bead respawn is blocked
	// and escalated to mayor instead of re-dispatched (default 3).
	MaxBeadRespawns *int `json:"max_bead_respawns,omitempty"`

	// DoneIntentStuckTimeout is how long a done-intent can be active before the
	// session is considered stuck and restarted (default "60s").
	DoneIntentStuckTimeout string `json:"done_intent_stuck_timeout,omitempty"`

	// DoneIntentRecentGrace is how recently a done-intent must have been created
	// to be considered still in progress (default "30s").
	DoneIntentRecentGrace string `json:"done_intent_recent_grace,omitempty"`
}

// DefaultOperationalConfig returns an OperationalConfig with all defaults.
func DefaultOperationalConfig() *OperationalConfig {
	return &OperationalConfig{}
}

// ParseDurationOrDefault parses a Go duration string, returning fallback on error or empty input.
func ParseDurationOrDefault(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}

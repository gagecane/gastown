package config

import (
	"path/filepath"
	"time"
)

// Compiled-in defaults for operational thresholds.
// These are the values used when no config override is provided.
// Each was previously a hardcoded const scattered across the codebase.

// Session defaults.
const (
	DefaultClaudeStartTimeout      = 60 * time.Second
	DefaultShellReadyTimeout       = 5 * time.Second
	DefaultGracefulShutdownTimeout = 3 * time.Second
	DefaultBdCommandTimeout        = 30 * time.Second
	DefaultBdSubprocessTimeout     = 5 * time.Second
	DefaultGUPPViolationTimeout    = 30 * time.Minute
	DefaultHungSessionThreshold    = 30 * time.Minute
	DefaultStartupNudgeVerifyDelay = 25 * time.Second
	DefaultStartupNudgeMaxRetries  = 2
)

// Nudge defaults.
const (
	DefaultNudgeReadyTimeout      = 10 * time.Second
	DefaultNudgeRetryInterval     = 500 * time.Millisecond
	DefaultNudgeLockTimeout       = 30 * time.Second
	DefaultNudgeNormalTTL         = 30 * time.Minute
	DefaultNudgeUrgentTTL         = 2 * time.Hour
	DefaultNudgeMaxQueueDepth     = 50
	DefaultNudgeStaleClaimTimeout = 5 * time.Minute
)

// Daemon defaults.
const (
	DefaultMassDeathWindow           = 30 * time.Second
	DefaultMassDeathThreshold        = 3
	DefaultDogIdleSessionTimeout     = 1 * time.Hour
	DefaultPolecatIdleSessionTimeout = 15 * time.Minute
	DefaultDogIdleRemoveTimeout      = 4 * time.Hour
	DefaultStaleWorkingTimeout       = 2 * time.Hour
	// DefaultDeadPolecatReapTimeout is how long a polecat's tmux session must be
	// dead (with a stale heartbeat) before its in_progress/hooked beads are auto-reset
	// to open. Prevents stuck patrol wisps (in_progress issues) from accumulating
	// when a polecat hard-crashes (OOM, tmux kill) and can't run its Stop hook.
	// See gu-1x0j.
	DefaultDeadPolecatReapTimeout = 1 * time.Hour
	// DefaultDeadAgentReapTimeout is the legacy/fallback timeout used when a
	// witness/refinery wisp has no fresh heartbeat file (sessions still on the
	// pre-cv-p3fem path). Roles that do produce heartbeats now use the per-role
	// timeouts below. See gu-s009 (legacy) and cv-p3fem Phase 1 (heartbeat-first).
	DefaultDeadAgentReapTimeout = 2 * time.Hour
	// DefaultWitnessReapTimeout governs the heartbeat-driven reap of a witness's
	// hooked patrol wisp. With per-command heartbeats (cv-p3fem Phase 1) a
	// healthy witness touches its heartbeat several times per patrol cycle, so
	// 15 minutes of staleness reliably indicates a dead session — short enough
	// to satisfy gu-0nmw's <10min detection target once Phase 2's keepalive
	// ticker lands. A value <=0 disables the per-role override and falls back
	// to DefaultDeadAgentReapTimeout.
	DefaultWitnessReapTimeout = 15 * time.Minute
	// DefaultRefineryReapTimeout governs the heartbeat-driven reap of a
	// refinery's hooked patrol wisp. Refineries run longer merge-queue cycles
	// (~5–15min per gate run) so we err on the side of the upper bound from
	// the gu-rh0g exit criteria — a refinery that dies mid-cycle is detected
	// within 30 minutes. Phase 2's keepalive ticker will allow tightening this
	// further. A value <=0 disables the per-role override and falls back to
	// DefaultDeadAgentReapTimeout.
	DefaultRefineryReapTimeout            = 30 * time.Minute
	DefaultMaxDogPoolSize                 = 4
	DefaultMaxLifecycleMessageAge         = 6 * time.Hour
	DefaultSyncFailureEscalationThreshold = 3
	DefaultDoctorMolCooldown              = 5 * time.Minute
	DefaultRecoveryHeartbeatInterval      = 3 * time.Minute
	DefaultBootSpawnCooldown              = 2 * time.Minute
	DefaultBootIdleSuppression            = 15 * time.Minute
	DefaultDeaconGracePeriod              = 5 * time.Minute
	// DefaultDeaconMaxSessionAge is the default for the preventative scheduled
	// deacon restart. Disabled by default (0); operators opt in per-deployment
	// by setting operational.daemon.deacon_max_session_age. See gs-a0x.
	DefaultDeaconMaxSessionAge = 0 * time.Second

	// Pressure check defaults — fully opt-in. All zero = disabled.
	// Configure in settings/config.json under operational.daemon to enable.
	// Example: {"pressure_cpu_threshold": 3.0, "pressure_mem_threshold_gb": 0.5}
	DefaultPressureCPUThreshold   = 0.0
	DefaultPressureMemThresholdGB = 0.0
	DefaultPressureMaxSessions    = 0

	// DefaultPolecatSelfTerminate defaults polecats to self-terminating their
	// session after `gt done` completes. See gu-ci0l: previously default false
	// meant polecats waited on the witness for tmux-kill, exposing them to a
	// post-done wedge loop where witness restarts re-dispatched the still-alive
	// idle polecat into the same gt done path. Self-termination eliminates the
	// dependency on witness liveness for the cleanup path.
	// Operators can opt out by setting operational.daemon.polecat_self_terminate=false.
	DefaultPolecatSelfTerminate = true
)

// Deacon defaults.
const (
	DefaultDeaconPingTimeout         = 30 * time.Second
	DefaultDeaconConsecutiveFailures = 3
	DefaultDeaconCooldown            = 5 * time.Minute
	// Both thresholds MUST exceed the deacon patrol's await-signal backoff-max
	// (15m). See deacon.HeartbeatStaleThreshold for the gu-70rg incident
	// history that drove the bump from 5m/20m to 16m/30m.
	DefaultDeaconHeartbeatStaleThreshold = 16 * time.Minute
	DefaultDeaconHeartbeatVeryStale      = 30 * time.Minute
	// DefaultDeaconCycleStallThreshold is how long the deacon's heartbeat cycle
	// counter may stay UNCHANGED (while wall-clock advances) before the daemon
	// treats the deacon as hung — even if the absolute heartbeat age has not yet
	// crossed the stale threshold. This catches monotonic-age hangs (gu-qwjj3)
	// where the cycle freezes but age climbs slowly. The stall check only fires
	// when active work is in flight, so a legitimate await-signal idle backoff
	// (which also freezes the cycle) does not trip it — preserving the gu-70rg
	// false-positive fix. Set below the 16m absolute stale threshold so a genuine
	// hang is caught sooner than the age gate alone would.
	DefaultDeaconCycleStallThreshold = 7 * time.Minute
	DefaultMaxRedispatches           = 3
	DefaultRedispatchCooldown        = 5 * time.Minute
	DefaultMaxFeedsPerCycle          = 3
	DefaultFeedCooldown              = 10 * time.Minute
)

// Polecat defaults.
const (
	DefaultPolecatHeartbeatStale  = 3 * time.Minute
	DefaultPolecatDoltMaxRetries  = 10
	DefaultPolecatDoltBaseBackoff = 500 * time.Millisecond
	DefaultPolecatDoltBackoffMax  = 30 * time.Second
	DefaultPolecatPendingMaxAge   = 5 * time.Minute
	DefaultPolecatNamepoolSize    = 50
)

// Dolt defaults.
const (
	DefaultDoltHealthCheckInterval = 30 * time.Second
	DefaultDoltCmdTimeout          = 15 * time.Second
	DefaultDoltMaxConnections      = 1000
	DefaultDoltSlowQueryThreshold  = 1 * time.Second
)

// Mail defaults.
const (
	DefaultMailIdleNotifyTimeout  = 3 * time.Second
	DefaultMailBdReadTimeout      = 60 * time.Second
	DefaultMailBdWriteTimeout     = 60 * time.Second
	DefaultMailMaxConcurrentAcks  = 8
	DefaultMailReplyReminderDelay = 30 * time.Second
)

// Web defaults.
const (
	DefaultWebMaxConcurrentCmds = 12
	DefaultWebMaxSubjectLen     = 500
	DefaultWebMaxBodyLen        = 100_000
)

// Witness defaults.
const (
	DefaultWitnessStartupStallThreshold  = 90 * time.Second
	DefaultWitnessStartupActivityGrace   = 60 * time.Second
	DefaultWitnessMaxBeadRespawns        = 3
	DefaultWitnessMaxRedispatchesPerMin  = 10
	DefaultWitnessDoneIntentStuckTimeout = 60 * time.Second
	DefaultWitnessDoneIntentRecentGrace  = 30 * time.Second
	DefaultWitnessHeartbeatStartupGrace  = 5 * time.Minute
	DefaultWitnessStaleInProgressThresh  = 1 * time.Hour
	// DefaultWitnessStaleRigAgentHeartbeat is the threshold above which a
	// rig-level agent (refinery, witness) heartbeat is considered stale and the
	// witness escalates to mayor with a STALE_RIG_AGENT mail. (gu-0nmw)
	// 1h is comfortably longer than even a deeply backed-off await-signal
	// idle (~5 min cap) so a healthy idle agent will never trip this.
	DefaultWitnessStaleRigAgentHeartbeat = 1 * time.Hour
	// DefaultWitnessStaleRigAgentNotifyCooldown is the minimum interval between
	// repeated STALE_RIG_AGENT escalations for the same unchanged wedged agent.
	// 30m balances "stop flooding the Mayor every cycle" (gu-z8qzq) against
	// "don't go silent so long a genuinely stuck agent is forgotten." The alarm
	// still re-fires sooner if the condition materially worsens.
	DefaultWitnessStaleRigAgentNotifyCooldown = 30 * time.Minute
	// DefaultWitnessStaleRigAgentCorrelationWindow is the town-wide window over
	// which STALE_RIG_AGENT escalations from different rigs fold into one
	// thread (gu-nejgh). 15m is short enough that distinct incidents stay
	// distinct (a wedged agent in rig A this hour won't silence a separate
	// wedge in rig B next hour) but long enough to capture the simultaneous
	// fan-out of a single town-wide event across staggered 5m patrol cycles.
	DefaultWitnessStaleRigAgentCorrelationWindow = 15 * time.Minute
)

// LoadOperationalConfig loads operational config from a town root.
// Returns a valid (possibly empty) config — never nil, never errors.
// Callers can use accessor methods that return defaults for nil sub-configs.
func LoadOperationalConfig(townRoot string) *OperationalConfig {
	settingsPath := filepath.Join(townRoot, "settings", "config.json")
	ts, err := LoadOrCreateTownSettings(settingsPath)
	if err != nil || ts == nil || ts.Operational == nil {
		return &OperationalConfig{}
	}
	return ts.Operational
}

// --- Accessor methods ---
// Each method reads from config with fallback to the compiled-in default.
// Nil-safe: works when OperationalConfig or any sub-struct is nil.

// GetSessionConfig returns the session thresholds, never nil.
func (c *OperationalConfig) GetSessionConfig() *SessionThresholds {
	if c != nil && c.Session != nil {
		return c.Session
	}
	return &SessionThresholds{}
}

// ClaudeStartTimeout returns the configured or default Claude start timeout.
func (s *SessionThresholds) ClaudeStartTimeoutD() time.Duration {
	if s != nil {
		return ParseDurationOrDefault(s.ClaudeStartTimeout, DefaultClaudeStartTimeout)
	}
	return DefaultClaudeStartTimeout
}

// ShellReadyTimeoutD returns the configured or default shell ready timeout.
func (s *SessionThresholds) ShellReadyTimeoutD() time.Duration {
	if s != nil {
		return ParseDurationOrDefault(s.ShellReadyTimeout, DefaultShellReadyTimeout)
	}
	return DefaultShellReadyTimeout
}

// GracefulShutdownTimeoutD returns the configured or default graceful shutdown timeout.
func (s *SessionThresholds) GracefulShutdownTimeoutD() time.Duration {
	if s != nil {
		return ParseDurationOrDefault(s.GracefulShutdownTimeout, DefaultGracefulShutdownTimeout)
	}
	return DefaultGracefulShutdownTimeout
}

// BdCommandTimeoutD returns the configured or default bd command timeout.
func (s *SessionThresholds) BdCommandTimeoutD() time.Duration {
	if s != nil {
		return ParseDurationOrDefault(s.BdCommandTimeout, DefaultBdCommandTimeout)
	}
	return DefaultBdCommandTimeout
}

// BdSubprocessTimeoutD returns the configured or default bd subprocess timeout.
func (s *SessionThresholds) BdSubprocessTimeoutD() time.Duration {
	if s != nil {
		return ParseDurationOrDefault(s.BdSubprocessTimeout, DefaultBdSubprocessTimeout)
	}
	return DefaultBdSubprocessTimeout
}

// GUPPViolationTimeoutD returns the configured or default GUPP violation timeout.
func (s *SessionThresholds) GUPPViolationTimeoutD() time.Duration {
	if s != nil {
		return ParseDurationOrDefault(s.GUPPViolationTimeout, DefaultGUPPViolationTimeout)
	}
	return DefaultGUPPViolationTimeout
}

// HungSessionThresholdD returns the configured or default hung session threshold.
func (s *SessionThresholds) HungSessionThresholdD() time.Duration {
	if s != nil {
		return ParseDurationOrDefault(s.HungSessionThreshold, DefaultHungSessionThreshold)
	}
	return DefaultHungSessionThreshold
}

// StartupNudgeVerifyDelayD returns the configured or default startup nudge verify delay.
func (s *SessionThresholds) StartupNudgeVerifyDelayD() time.Duration {
	if s != nil {
		return ParseDurationOrDefault(s.StartupNudgeVerifyDelay, DefaultStartupNudgeVerifyDelay)
	}
	return DefaultStartupNudgeVerifyDelay
}

// StartupNudgeMaxRetriesV returns the configured or default startup nudge max retries.
func (s *SessionThresholds) StartupNudgeMaxRetriesV() int {
	if s != nil && s.StartupNudgeMaxRetries != nil {
		return *s.StartupNudgeMaxRetries
	}
	return DefaultStartupNudgeMaxRetries
}

// --- Nudge accessors ---

// GetNudgeConfig returns the nudge thresholds, never nil.
func (c *OperationalConfig) GetNudgeConfig() *NudgeThresholds {
	if c != nil && c.Nudge != nil {
		return c.Nudge
	}
	return &NudgeThresholds{}
}

// ReadyTimeoutD returns the configured or default nudge ready timeout.
func (n *NudgeThresholds) ReadyTimeoutD() time.Duration {
	if n != nil {
		return ParseDurationOrDefault(n.ReadyTimeout, DefaultNudgeReadyTimeout)
	}
	return DefaultNudgeReadyTimeout
}

// RetryIntervalD returns the configured or default nudge retry interval.
func (n *NudgeThresholds) RetryIntervalD() time.Duration {
	if n != nil {
		return ParseDurationOrDefault(n.RetryInterval, DefaultNudgeRetryInterval)
	}
	return DefaultNudgeRetryInterval
}

// LockTimeoutD returns the configured or default nudge lock timeout.
func (n *NudgeThresholds) LockTimeoutD() time.Duration {
	if n != nil {
		return ParseDurationOrDefault(n.LockTimeout, DefaultNudgeLockTimeout)
	}
	return DefaultNudgeLockTimeout
}

// NormalTTLD returns the configured or default normal nudge TTL.
func (n *NudgeThresholds) NormalTTLD() time.Duration {
	if n != nil {
		return ParseDurationOrDefault(n.NormalTTL, DefaultNudgeNormalTTL)
	}
	return DefaultNudgeNormalTTL
}

// UrgentTTLD returns the configured or default urgent nudge TTL.
func (n *NudgeThresholds) UrgentTTLD() time.Duration {
	if n != nil {
		return ParseDurationOrDefault(n.UrgentTTL, DefaultNudgeUrgentTTL)
	}
	return DefaultNudgeUrgentTTL
}

// MaxQueueDepthV returns the configured or default max queue depth.
func (n *NudgeThresholds) MaxQueueDepthV() int {
	if n != nil && n.MaxQueueDepth != nil {
		return *n.MaxQueueDepth
	}
	return DefaultNudgeMaxQueueDepth
}

// StaleClaimThresholdD returns the configured or default stale claim threshold.
func (n *NudgeThresholds) StaleClaimThresholdD() time.Duration {
	if n != nil {
		return ParseDurationOrDefault(n.StaleClaimThreshold, DefaultNudgeStaleClaimTimeout)
	}
	return DefaultNudgeStaleClaimTimeout
}

// --- Daemon accessors ---

// GetDaemonConfig returns the daemon thresholds, never nil.
func (c *OperationalConfig) GetDaemonConfig() *DaemonThresholds {
	if c != nil && c.Daemon != nil {
		return c.Daemon
	}
	return &DaemonThresholds{}
}

// MassDeathWindowD returns the configured or default mass death window.
func (d *DaemonThresholds) MassDeathWindowD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.MassDeathWindow, DefaultMassDeathWindow)
	}
	return DefaultMassDeathWindow
}

// MassDeathThresholdV returns the configured or default mass death threshold.
func (d *DaemonThresholds) MassDeathThresholdV() int {
	if d != nil && d.MassDeathThreshold != nil {
		return *d.MassDeathThreshold
	}
	return DefaultMassDeathThreshold
}

// DogIdleSessionTimeoutD returns the configured or default dog idle session timeout.
func (d *DaemonThresholds) DogIdleSessionTimeoutD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.DogIdleSessionTimeout, DefaultDogIdleSessionTimeout)
	}
	return DefaultDogIdleSessionTimeout
}

// PolecatIdleSessionTimeoutD returns the configured or default polecat idle session timeout.
// Polecats that have been idle (no hooked work, heartbeat state=idle) longer than this
// threshold are auto-killed to prevent API slot burn. Default 15 minutes — long enough
// for polecats to run gt done after completing work, short enough to prevent hour-long burns.
func (d *DaemonThresholds) PolecatIdleSessionTimeoutD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.PolecatIdleSessionTimeout, DefaultPolecatIdleSessionTimeout)
	}
	return DefaultPolecatIdleSessionTimeout
}

// DeadPolecatReapTimeoutD returns the configured or default dead-polecat reap timeout.
// When a polecat's tmux session has been dead (and heartbeat stale) for longer than
// this threshold, its in_progress/hooked beads are auto-reset to open status with
// cleared assignee so they can be re-dispatched. This prevents stuck patrol wisps
// from accumulating when polecats hard-crash (OOM, tmux kill) and can't run their
// Stop hook. Default 1 hour. See gu-1x0j.
func (d *DaemonThresholds) DeadPolecatReapTimeoutD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.DeadPolecatReapTimeout, DefaultDeadPolecatReapTimeout)
	}
	return DefaultDeadPolecatReapTimeout
}

// DeadAgentReapTimeoutD returns the configured or default dead-agent reap timeout
// for non-polecat agents (witness, refinery). When such an agent's tmux session
// has been dead AND its hooked patrol wisp's last update is older than this
// threshold, the wisp is auto-reset to open with cleared assignee so the role's
// next session can pick up a fresh patrol. Default 2 hours. A value <=0 disables
// the reaper.
//
// In cv-p3fem Phase 1 this is the FALLBACK timeout — used only for sessions
// whose heartbeat file is missing (pre-rollout). Sessions with a heartbeat are
// reaped against WitnessReapTimeoutD/RefineryReapTimeoutD instead. See
// gu-s009 (legacy) and gu-rh0g (heartbeat-first).
func (d *DaemonThresholds) DeadAgentReapTimeoutD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.DeadAgentReapTimeout, DefaultDeadAgentReapTimeout)
	}
	return DefaultDeadAgentReapTimeout
}

// WitnessReapTimeoutD returns the configured or default heartbeat-driven reap
// timeout for a witness's hooked patrol wisp. Used only when a current
// heartbeat file exists for the witness session; otherwise the reaper falls
// back to DeadAgentReapTimeoutD. A value <=0 disables the per-role override.
// See cv-p3fem Phase 1.
func (d *DaemonThresholds) WitnessReapTimeoutD() time.Duration {
	if d != nil {
		dur := ParseDurationOrDefault(d.WitnessReapTimeout, DefaultWitnessReapTimeout)
		if dur <= 0 {
			return d.DeadAgentReapTimeoutD()
		}
		return dur
	}
	return DefaultWitnessReapTimeout
}

// RefineryReapTimeoutD returns the configured or default heartbeat-driven reap
// timeout for a refinery's hooked patrol wisp. Used only when a current
// heartbeat file exists for the refinery session; otherwise the reaper falls
// back to DeadAgentReapTimeoutD. A value <=0 disables the per-role override.
// See cv-p3fem Phase 1.
func (d *DaemonThresholds) RefineryReapTimeoutD() time.Duration {
	if d != nil {
		dur := ParseDurationOrDefault(d.RefineryReapTimeout, DefaultRefineryReapTimeout)
		if dur <= 0 {
			return d.DeadAgentReapTimeoutD()
		}
		return dur
	}
	return DefaultRefineryReapTimeout
}

// DogIdleRemoveTimeoutD returns the configured or default dog idle remove timeout.
func (d *DaemonThresholds) DogIdleRemoveTimeoutD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.DogIdleRemoveTimeout, DefaultDogIdleRemoveTimeout)
	}
	return DefaultDogIdleRemoveTimeout
}

// StaleWorkingTimeoutD returns the configured or default stale working timeout.
func (d *DaemonThresholds) StaleWorkingTimeoutD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.StaleWorkingTimeout, DefaultStaleWorkingTimeout)
	}
	return DefaultStaleWorkingTimeout
}

// MaxDogPoolSizeV returns the configured or default max dog pool size.
func (d *DaemonThresholds) MaxDogPoolSizeV() int {
	if d != nil && d.MaxDogPoolSize != nil {
		return *d.MaxDogPoolSize
	}
	return DefaultMaxDogPoolSize
}

// MaxLifecycleMessageAgeD returns the configured or default max lifecycle message age.
func (d *DaemonThresholds) MaxLifecycleMessageAgeD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.MaxLifecycleMessageAge, DefaultMaxLifecycleMessageAge)
	}
	return DefaultMaxLifecycleMessageAge
}

// SyncFailureEscalationThresholdV returns the configured or default threshold.
func (d *DaemonThresholds) SyncFailureEscalationThresholdV() int {
	if d != nil && d.SyncFailureEscalationThreshold != nil {
		return *d.SyncFailureEscalationThreshold
	}
	return DefaultSyncFailureEscalationThreshold
}

// PolecatSelfTerminateV returns the configured polecat-self-terminate setting,
// or DefaultPolecatSelfTerminate (true) when not explicitly configured. Honors
// an explicit `false` override — only nil falls through to the default. See
// gu-ci0l: default-true eliminates the witness-dependent wedge loop in the
// post-done cleanup path.
func (d *DaemonThresholds) PolecatSelfTerminateV() bool {
	if d != nil && d.PolecatSelfTerminate != nil {
		return *d.PolecatSelfTerminate
	}
	return DefaultPolecatSelfTerminate
}

// DoctorMolCooldownD returns the configured or default doctor mol cooldown.
func (d *DaemonThresholds) DoctorMolCooldownD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.DoctorMolCooldown, DefaultDoctorMolCooldown)
	}
	return DefaultDoctorMolCooldown
}

// RecoveryHeartbeatIntervalD returns the configured or default recovery heartbeat interval.
func (d *DaemonThresholds) RecoveryHeartbeatIntervalD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.RecoveryHeartbeatInterval, DefaultRecoveryHeartbeatInterval)
	}
	return DefaultRecoveryHeartbeatInterval
}

// BootSpawnCooldownD returns the configured or default boot spawn cooldown.
func (d *DaemonThresholds) BootSpawnCooldownD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.BootSpawnCooldown, DefaultBootSpawnCooldown)
	}
	return DefaultBootSpawnCooldown
}

// BootIdleSuppressionD returns the configured or default boot idle suppression duration.
// When Boot's last action was "nothing" (deacon healthy), spawns are suppressed for this long.
func (d *DaemonThresholds) BootIdleSuppressionD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.BootIdleSuppression, DefaultBootIdleSuppression)
	}
	return DefaultBootIdleSuppression
}

// DeaconGracePeriodD returns the configured or default deacon grace period.
func (d *DaemonThresholds) DeaconGracePeriodD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.DeaconGracePeriod, DefaultDeaconGracePeriod)
	}
	return DefaultDeaconGracePeriod
}

// DeaconMaxSessionAgeD returns the configured or default max deacon session
// age before a preventative scheduled restart. Zero means disabled.
func (d *DaemonThresholds) DeaconMaxSessionAgeD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.DeaconMaxSessionAge, DefaultDeaconMaxSessionAge)
	}
	return DefaultDeaconMaxSessionAge
}

// PressureCPUThresholdV returns the configured or default CPU pressure threshold (load per core).
func (d *DaemonThresholds) PressureCPUThresholdV() float64 {
	if d != nil && d.PressureCPUThreshold != nil {
		return *d.PressureCPUThreshold
	}
	return DefaultPressureCPUThreshold
}

// PressureMemThresholdGBV returns the configured or default memory pressure threshold in GB.
func (d *DaemonThresholds) PressureMemThresholdGBV() float64 {
	if d != nil && d.PressureMemThresholdGB != nil {
		return *d.PressureMemThresholdGB
	}
	return DefaultPressureMemThresholdGB
}

// PressureMaxSessionsV returns the configured or default max concurrent sessions (0 = unlimited).
func (d *DaemonThresholds) PressureMaxSessionsV() int {
	if d != nil && d.PressureMaxSessions != nil {
		return *d.PressureMaxSessions
	}
	return DefaultPressureMaxSessions
}

// --- Deacon accessors ---

// GetDeaconConfig returns the deacon thresholds, never nil.
func (c *OperationalConfig) GetDeaconConfig() *DeaconThresholds {
	if c != nil && c.Deacon != nil {
		return c.Deacon
	}
	return &DeaconThresholds{}
}

// PingTimeoutD returns the configured or default deacon ping timeout.
func (d *DeaconThresholds) PingTimeoutD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.PingTimeout, DefaultDeaconPingTimeout)
	}
	return DefaultDeaconPingTimeout
}

// ConsecutiveFailuresV returns the configured or default consecutive failures.
func (d *DeaconThresholds) ConsecutiveFailuresV() int {
	if d != nil && d.ConsecutiveFailures != nil {
		return *d.ConsecutiveFailures
	}
	return DefaultDeaconConsecutiveFailures
}

// CooldownD returns the configured or default deacon cooldown.
func (d *DeaconThresholds) CooldownD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.Cooldown, DefaultDeaconCooldown)
	}
	return DefaultDeaconCooldown
}

// HeartbeatStaleThresholdD returns the configured or default heartbeat stale threshold.
func (d *DeaconThresholds) HeartbeatStaleThresholdD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.HeartbeatStaleThreshold, DefaultDeaconHeartbeatStaleThreshold)
	}
	return DefaultDeaconHeartbeatStaleThreshold
}

// HeartbeatVeryStaleThresholdD returns the configured or default heartbeat very stale threshold.
func (d *DeaconThresholds) HeartbeatVeryStaleThresholdD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.HeartbeatVeryStaleThreshold, DefaultDeaconHeartbeatVeryStale)
	}
	return DefaultDeaconHeartbeatVeryStale
}

// CycleStallThresholdD returns the configured or default cycle-stall threshold.
func (d *DeaconThresholds) CycleStallThresholdD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.CycleStallThreshold, DefaultDeaconCycleStallThreshold)
	}
	return DefaultDeaconCycleStallThreshold
}

// MaxRedispatchesV returns the configured or default max redispatches.
func (d *DeaconThresholds) MaxRedispatchesV() int {
	if d != nil && d.MaxRedispatches != nil {
		return *d.MaxRedispatches
	}
	return DefaultMaxRedispatches
}

// RedispatchCooldownD returns the configured or default redispatch cooldown.
func (d *DeaconThresholds) RedispatchCooldownD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.RedispatchCooldown, DefaultRedispatchCooldown)
	}
	return DefaultRedispatchCooldown
}

// MaxFeedsPerCycleV returns the configured or default max feeds per cycle.
func (d *DeaconThresholds) MaxFeedsPerCycleV() int {
	if d != nil && d.MaxFeedsPerCycle != nil {
		return *d.MaxFeedsPerCycle
	}
	return DefaultMaxFeedsPerCycle
}

// FeedCooldownD returns the configured or default feed cooldown.
func (d *DeaconThresholds) FeedCooldownD() time.Duration {
	if d != nil {
		return ParseDurationOrDefault(d.FeedCooldown, DefaultFeedCooldown)
	}
	return DefaultFeedCooldown
}

// --- Polecat accessors ---

// GetPolecatConfig returns the polecat thresholds, never nil.
func (c *OperationalConfig) GetPolecatConfig() *PolecatThresholds {
	if c != nil && c.Polecat != nil {
		return c.Polecat
	}
	return &PolecatThresholds{}
}

// HeartbeatStaleThresholdD returns the configured or default polecat heartbeat stale threshold.
func (p *PolecatThresholds) HeartbeatStaleThresholdD() time.Duration {
	if p != nil {
		return ParseDurationOrDefault(p.HeartbeatStaleThreshold, DefaultPolecatHeartbeatStale)
	}
	return DefaultPolecatHeartbeatStale
}

// DoltMaxRetriesV returns the configured or default Dolt max retries.
func (p *PolecatThresholds) DoltMaxRetriesV() int {
	if p != nil && p.DoltMaxRetries != nil {
		return *p.DoltMaxRetries
	}
	return DefaultPolecatDoltMaxRetries
}

// DoltBaseBackoffD returns the configured or default Dolt base backoff.
func (p *PolecatThresholds) DoltBaseBackoffD() time.Duration {
	if p != nil {
		return ParseDurationOrDefault(p.DoltBaseBackoff, DefaultPolecatDoltBaseBackoff)
	}
	return DefaultPolecatDoltBaseBackoff
}

// DoltBackoffMaxD returns the configured or default Dolt backoff max.
func (p *PolecatThresholds) DoltBackoffMaxD() time.Duration {
	if p != nil {
		return ParseDurationOrDefault(p.DoltBackoffMax, DefaultPolecatDoltBackoffMax)
	}
	return DefaultPolecatDoltBackoffMax
}

// PendingMaxAgeD returns the configured or default pending max age.
func (p *PolecatThresholds) PendingMaxAgeD() time.Duration {
	if p != nil {
		return ParseDurationOrDefault(p.PendingMaxAge, DefaultPolecatPendingMaxAge)
	}
	return DefaultPolecatPendingMaxAge
}

// NamepoolSizeV returns the configured or default namepool size.
func (p *PolecatThresholds) NamepoolSizeV() int {
	if p != nil && p.NamepoolSize != nil {
		return *p.NamepoolSize
	}
	return DefaultPolecatNamepoolSize
}

// --- Dolt accessors ---

// GetDoltConfig returns the dolt thresholds, never nil.
func (c *OperationalConfig) GetDoltConfig() *DoltThresholds {
	if c != nil && c.Dolt != nil {
		return c.Dolt
	}
	return &DoltThresholds{}
}

// HealthCheckIntervalD returns the configured or default health check interval.
func (dt *DoltThresholds) HealthCheckIntervalD() time.Duration {
	if dt != nil {
		return ParseDurationOrDefault(dt.HealthCheckInterval, DefaultDoltHealthCheckInterval)
	}
	return DefaultDoltHealthCheckInterval
}

// CmdTimeoutD returns the configured or default cmd timeout.
func (dt *DoltThresholds) CmdTimeoutD() time.Duration {
	if dt != nil {
		return ParseDurationOrDefault(dt.CmdTimeout, DefaultDoltCmdTimeout)
	}
	return DefaultDoltCmdTimeout
}

// MaxConnectionsV returns the configured or default max connections.
func (dt *DoltThresholds) MaxConnectionsV() int {
	if dt != nil && dt.MaxConnections != nil {
		return *dt.MaxConnections
	}
	return DefaultDoltMaxConnections
}

// SlowQueryThresholdD returns the configured or default slow query threshold.
func (dt *DoltThresholds) SlowQueryThresholdD() time.Duration {
	if dt != nil {
		return ParseDurationOrDefault(dt.SlowQueryThreshold, DefaultDoltSlowQueryThreshold)
	}
	return DefaultDoltSlowQueryThreshold
}

// --- Mail accessors ---

// GetMailConfig returns the mail thresholds, never nil.
func (c *OperationalConfig) GetMailConfig() *MailThresholds {
	if c != nil && c.Mail != nil {
		return c.Mail
	}
	return &MailThresholds{}
}

// IdleNotifyTimeoutD returns the configured or default idle notify timeout.
func (m *MailThresholds) IdleNotifyTimeoutD() time.Duration {
	if m != nil {
		return ParseDurationOrDefault(m.IdleNotifyTimeout, DefaultMailIdleNotifyTimeout)
	}
	return DefaultMailIdleNotifyTimeout
}

// BdReadTimeoutD returns the configured or default bd read timeout.
func (m *MailThresholds) BdReadTimeoutD() time.Duration {
	if m != nil {
		return ParseDurationOrDefault(m.BdReadTimeout, DefaultMailBdReadTimeout)
	}
	return DefaultMailBdReadTimeout
}

// BdWriteTimeoutD returns the configured or default bd write timeout.
func (m *MailThresholds) BdWriteTimeoutD() time.Duration {
	if m != nil {
		return ParseDurationOrDefault(m.BdWriteTimeout, DefaultMailBdWriteTimeout)
	}
	return DefaultMailBdWriteTimeout
}

// MaxConcurrentAckOpsV returns the configured or default max concurrent ack ops.
func (m *MailThresholds) MaxConcurrentAckOpsV() int {
	if m != nil && m.MaxConcurrentAckOps != nil {
		return *m.MaxConcurrentAckOps
	}
	return DefaultMailMaxConcurrentAcks
}

// ReplyReminderDelayD returns the configured or default reply reminder delay.
// A zero duration means reply reminders are disabled.
func (m *MailThresholds) ReplyReminderDelayD() time.Duration {
	if m != nil {
		return ParseDurationOrDefault(m.ReplyReminderDelay, DefaultMailReplyReminderDelay)
	}
	return DefaultMailReplyReminderDelay
}

// --- Web accessors ---

// GetWebConfig returns the web thresholds, never nil.
func (c *OperationalConfig) GetWebConfig() *WebThresholds {
	if c != nil && c.Web != nil {
		return c.Web
	}
	return &WebThresholds{}
}

// MaxConcurrentCommandsV returns the configured or default max concurrent commands.
func (w *WebThresholds) MaxConcurrentCommandsV() int {
	if w != nil && w.MaxConcurrentCommands != nil {
		return *w.MaxConcurrentCommands
	}
	return DefaultWebMaxConcurrentCmds
}

// MaxSubjectLenV returns the configured or default max subject length.
func (w *WebThresholds) MaxSubjectLenV() int {
	if w != nil && w.MaxSubjectLen != nil {
		return *w.MaxSubjectLen
	}
	return DefaultWebMaxSubjectLen
}

// MaxBodyLenV returns the configured or default max body length.
func (w *WebThresholds) MaxBodyLenV() int {
	if w != nil && w.MaxBodyLen != nil {
		return *w.MaxBodyLen
	}
	return DefaultWebMaxBodyLen
}

// --- Witness accessors ---

// GetWitnessConfig returns the witness thresholds, never nil.
func (c *OperationalConfig) GetWitnessConfig() *WitnessThresholds {
	if c != nil && c.Witness != nil {
		return c.Witness
	}
	return &WitnessThresholds{}
}

// StartupStallThresholdD returns the configured or default startup stall threshold.
func (wt *WitnessThresholds) StartupStallThresholdD() time.Duration {
	if wt != nil {
		return ParseDurationOrDefault(wt.StartupStallThreshold, DefaultWitnessStartupStallThreshold)
	}
	return DefaultWitnessStartupStallThreshold
}

// StartupActivityGraceD returns the configured or default startup activity grace.
func (wt *WitnessThresholds) StartupActivityGraceD() time.Duration {
	if wt != nil {
		return ParseDurationOrDefault(wt.StartupActivityGrace, DefaultWitnessStartupActivityGrace)
	}
	return DefaultWitnessStartupActivityGrace
}

// MaxBeadRespawnsV returns the configured or default max bead respawns.
func (wt *WitnessThresholds) MaxBeadRespawnsV() int {
	if wt != nil && wt.MaxBeadRespawns != nil {
		return *wt.MaxBeadRespawns
	}
	return DefaultWitnessMaxBeadRespawns
}

// MaxRedispatchesPerMinuteV returns the configured or default per-rig
// re-dispatch rate limit (beads per minute). A value of 0 disables rate
// limiting. Negative values fall back to the default (defensive: a negative
// cap is almost certainly a misconfiguration and silently disabling would
// mask it).
func (wt *WitnessThresholds) MaxRedispatchesPerMinuteV() int {
	if wt != nil && wt.MaxRedispatchesPerMinute != nil {
		v := *wt.MaxRedispatchesPerMinute
		if v < 0 {
			return DefaultWitnessMaxRedispatchesPerMin
		}
		return v
	}
	return DefaultWitnessMaxRedispatchesPerMin
}

// DoneIntentStuckTimeoutD returns the configured or default done-intent stuck timeout.
func (wt *WitnessThresholds) DoneIntentStuckTimeoutD() time.Duration {
	if wt != nil {
		return ParseDurationOrDefault(wt.DoneIntentStuckTimeout, DefaultWitnessDoneIntentStuckTimeout)
	}
	return DefaultWitnessDoneIntentStuckTimeout
}

// DoneIntentRecentGraceD returns the configured or default done-intent recent grace.
func (wt *WitnessThresholds) DoneIntentRecentGraceD() time.Duration {
	if wt != nil {
		return ParseDurationOrDefault(wt.DoneIntentRecentGrace, DefaultWitnessDoneIntentRecentGrace)
	}
	return DefaultWitnessDoneIntentRecentGrace
}

// HeartbeatStartupGraceD returns the configured or default heartbeat startup grace period.
// A live polecat with assigned work but no heartbeat file older than this is flagged
// for review as possibly stuck at startup (e.g., auth 401). (gt-uk7)
func (wt *WitnessThresholds) HeartbeatStartupGraceD() time.Duration {
	if wt != nil {
		return ParseDurationOrDefault(wt.HeartbeatStartupGrace, DefaultWitnessHeartbeatStartupGrace)
	}
	return DefaultWitnessHeartbeatStartupGrace
}

// StaleInProgressThresholdD returns the configured or default age threshold above
// which an in_progress bead with a dead-polecat assignee is considered stranded
// and escalated to mayor. (gu-wwyq) The complementary alive→dead transition
// path is handled by DetectZombiePolecats; this threshold drives the
// steady-state path that catches polecats already dead at patrol boot.
func (wt *WitnessThresholds) StaleInProgressThresholdD() time.Duration {
	if wt != nil {
		return ParseDurationOrDefault(wt.StaleInProgressThreshold, DefaultWitnessStaleInProgressThresh)
	}
	return DefaultWitnessStaleInProgressThresh
}

// StaleRigAgentHeartbeatD returns the configured or default age threshold above
// which a rig-level agent (refinery, witness) heartbeat is considered stale
// and escalated to mayor. (gu-0nmw) Set to 0 to disable the stale-agent
// detection scan entirely.
func (wt *WitnessThresholds) StaleRigAgentHeartbeatD() time.Duration {
	if wt != nil {
		return ParseDurationOrDefault(wt.StaleRigAgentHeartbeat, DefaultWitnessStaleRigAgentHeartbeat)
	}
	return DefaultWitnessStaleRigAgentHeartbeat
}

// StaleRigAgentNotifyCooldownD returns the configured or default cooldown
// between repeated STALE_RIG_AGENT escalations for the same unchanged agent.
// (gu-z8qzq) Set to 0 to disable suppression (re-notify every patrol cycle).
func (wt *WitnessThresholds) StaleRigAgentNotifyCooldownD() time.Duration {
	if wt != nil {
		return ParseDurationOrDefault(wt.StaleRigAgentNotifyCooldown, DefaultWitnessStaleRigAgentNotifyCooldown)
	}
	return DefaultWitnessStaleRigAgentNotifyCooldown
}

// StaleRigAgentCorrelationWindowD returns the configured or default town-wide
// window over which STALE_RIG_AGENT escalations from different rigs fold into a
// single thread. (gu-nejgh) Set to 0 to disable correlation (every agent sends).
func (wt *WitnessThresholds) StaleRigAgentCorrelationWindowD() time.Duration {
	if wt != nil {
		return ParseDurationOrDefault(wt.StaleRigAgentCorrelationWindow, DefaultWitnessStaleRigAgentCorrelationWindow)
	}
	return DefaultWitnessStaleRigAgentCorrelationWindow
}

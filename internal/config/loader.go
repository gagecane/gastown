// Package config provides the JSON-backed configuration loaders for gastown.
//
// This file defines the shared errors and synchronization used by the
// domain-specific loaders. The actual loader implementations live in
// sibling files grouped by the config they manage:
//
//   - loader_town.go          — TownConfig
//   - loader_rigs.go          — RigsConfig, RigConfig
//   - loader_rigsettings.go   — RigSettings, MergeQueueConfig
//   - loader_mayor.go         — MayorConfig
//   - loader_daemon.go        — DaemonPatrolConfig
//   - loader_accounts.go      — AccountsConfig
//   - loader_messaging.go     — MessagingConfig
//   - loader_townsettings.go  — TownSettings
//   - loader_agents.go        — agent resolution
//   - loader_runtime.go       — runtime command helpers
//   - loader_startup.go       — startup command assembly
//   - loader_misc.go          — rig prefix helpers
//   - loader_escalation.go    — EscalationConfig
package config

import (
	"errors"
	"sync"
)

// resolveConfigMu serializes agent config resolution across all callers.
// ResolveRoleAgentConfig and ResolveAgentConfig load rig-specific agents
// into a global registry; concurrent calls for different rigs would corrupt
// each other's lookups.
var resolveConfigMu sync.Mutex

var (
	// ErrNotFound indicates the config file does not exist.
	ErrNotFound = errors.New("config file not found")

	// ErrInvalidVersion indicates an unsupported schema version.
	ErrInvalidVersion = errors.New("unsupported config version")

	// ErrInvalidType indicates an unexpected config type.
	ErrInvalidType = errors.New("invalid config type")

	// ErrMissingField indicates a required field is missing.
	ErrMissingField = errors.New("missing required field")
)

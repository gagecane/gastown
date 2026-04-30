// Package config provides loaders, savers, and validators for the configuration
// files that describe a Gas Town deployment — town, rigs, mayor, daemon patrols,
// accounts, messaging, escalation — as well as agent resolution and startup
// command building.
//
// The loader logic is split across several files by concern:
//
//	loader.go                 — shared error sentinels and the resolveConfigMu mutex
//	loader_town.go            — TownConfig, TownSettings
//	loader_rig.go             — RigConfig, RigsConfig, RigSettings, MergeQueueConfig
//	loader_mayor.go           — MayorConfig
//	loader_daemon_patrol.go   — DaemonPatrolConfig and rig patrol membership
//	loader_accounts.go        — AccountsConfig and account resolution
//	loader_messaging.go       — MessagingConfig
//	loader_escalation.go      — EscalationConfig
//	loader_agent_resolve.go   — agent-config resolution and lookup
//	loader_agent_command.go   — startup-command construction
//	loader_misc.go            — small shared helpers (town-root discovery, rig prefixes, formulas)
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

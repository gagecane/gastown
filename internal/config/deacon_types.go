// Deacon configuration types. Extracted from types.go.
package config

// DeaconConfig represents deacon process settings.
type DeaconConfig struct {
	PatrolInterval string `json:"patrol_interval,omitempty"` // e.g., "5m"
}

// CurrentMayorConfigVersion is the current schema version for MayorConfig.
const CurrentMayorConfigVersion = 1

// DefaultCrewName is the default name for crew workspaces when not overridden.
const DefaultCrewName = "max"

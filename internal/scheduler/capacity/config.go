// Package capacity provides types and pure functions for the capacity-controlled
// dispatch scheduler. The impure orchestration (dispatch loop, enqueue, epic/convoy
// resolution) stays in cmd but uses types and pure functions from this package.
package capacity

import "time"

// SchedulerConfig configures the capacity scheduler for polecat dispatch.
// This is a town-wide setting (not per-rig) because capacity control is host-wide:
// API rate limits, memory, and CPU are shared resources across all rigs.
//
// Behavior is driven entirely by MaxPolecats:
//
//	-1 (default): direct dispatch — gt sling works as before, near-zero overhead
//	 0:           direct dispatch (same as -1)
//	 N > 0:       deferred dispatch — labels/metadata applied, daemon dispatches
type SchedulerConfig struct {
	// MaxPolecats is the max concurrent polecats across ALL rigs.
	// Includes both scheduler-dispatched and directly-slung polecats.
	// nil/absent = default (-1, direct dispatch). 0 = direct dispatch (same as -1).
	// N > 0 = deferred dispatch with capacity control.
	MaxPolecats *int `json:"max_polecats,omitempty"`

	// GlobalMaxPolecats is a hard town-wide ceiling on concurrent working
	// polecats across ALL rigs, enforced at admission in EVERY dispatch mode —
	// including direct dispatch (MaxPolecats <= 0), where MaxPolecats provides
	// no global cap at all (it doubles as the direct/deferred switch). This key
	// is orthogonal to that switch: it lets an operator say "never exceed N
	// polecats town-wide" independent of how work is dispatched.
	//
	// nil/absent or <= 0 = unbounded (default — only the dispatch-mode cap and
	// per-rig caps apply, preserving prior behavior). N > 0 = refuse admission
	// once town-wide working polecats reach N. The effective per-rig limit is
	// min(this ceiling, the rig's polecat.max_concurrent if set).
	GlobalMaxPolecats *int `json:"global_max_polecats,omitempty"`

	// BatchSize is the number of beads to dispatch per heartbeat tick.
	// Limits spawn rate per 3-minute cycle.
	// nil/absent = default (1). Explicit 0 is rejected by config setter.
	BatchSize *int `json:"batch_size,omitempty"`

	// SpawnDelay is the delay between spawns to prevent Dolt lock contention.
	// Default: "0s".
	SpawnDelay string `json:"spawn_delay,omitempty"`

	// MaxLoadPerCore is the host 1-minute load average per logical core above
	// which polecat admission is refused — in ALL dispatch modes, including
	// uncapped direct dispatch (MaxPolecats <= 0). nil/absent or <= 0 = disabled
	// (default). This is the host-load backpressure that the capacity cap alone
	// does not provide on the direct-dispatch path, where admission is granted
	// immediately with no load check (gu-5j7p4).
	MaxLoadPerCore *float64 `json:"max_load_per_core,omitempty"`
}

// DefaultSchedulerConfig returns a SchedulerConfig with sensible defaults.
// MaxPolecats=-1 means direct dispatch (no scheduler overhead).
func DefaultSchedulerConfig() *SchedulerConfig {
	defaultMax := -1
	defaultBatch := 1
	return &SchedulerConfig{
		MaxPolecats: &defaultMax,
		BatchSize:   &defaultBatch,
		SpawnDelay:  "0s",
	}
}

// GetMaxPolecats returns MaxPolecats or the default (-1, direct dispatch) if unset.
func (c *SchedulerConfig) GetMaxPolecats() int {
	if c == nil || c.MaxPolecats == nil {
		return -1
	}
	return *c.MaxPolecats
}

// GetGlobalMaxPolecats returns the configured town-wide polecat ceiling, or 0
// (unbounded) when unset or non-positive. When > 0, polecat admission is
// refused in all dispatch modes once town-wide working polecats reach it.
func (c *SchedulerConfig) GetGlobalMaxPolecats() int {
	if c == nil || c.GlobalMaxPolecats == nil || *c.GlobalMaxPolecats <= 0 {
		return 0
	}
	return *c.GlobalMaxPolecats
}

// GetBatchSize returns BatchSize or the default (1) if unset.
func (c *SchedulerConfig) GetBatchSize() int {
	if c == nil || c.BatchSize == nil {
		return 1
	}
	return *c.BatchSize
}

// GetSpawnDelay returns SpawnDelay as a duration, defaulting to 0s.
func (c *SchedulerConfig) GetSpawnDelay() time.Duration {
	if c == nil || c.SpawnDelay == "" {
		return 0
	}
	return ParseDurationOrDefault(c.SpawnDelay, 0)
}

// GetMaxLoadPerCore returns the configured host load-per-core admission
// threshold, or 0 (disabled) when unset or non-positive. When > 0, polecat
// admission is refused in all dispatch modes once host load/core exceeds it.
func (c *SchedulerConfig) GetMaxLoadPerCore() float64 {
	if c == nil || c.MaxLoadPerCore == nil || *c.MaxLoadPerCore <= 0 {
		return 0
	}
	return *c.MaxLoadPerCore
}

// IsDeferred returns true when the scheduler is configured for deferred dispatch
// (max_polecats > 0). Returns false for direct dispatch (-1) and disabled (0).
func (c *SchedulerConfig) IsDeferred() bool {
	return c.GetMaxPolecats() > 0
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

// Web-dashboard and status/feed types. Extracted from types.go.
package config

// WebTimeoutsConfig configures command execution timeouts for the web dashboard.
type WebTimeoutsConfig struct {
	// CmdTimeout is the timeout for bd (beads) commands. Default: "15s".
	CmdTimeout string `json:"cmd_timeout,omitempty"`
	// GhCmdTimeout is the timeout for GitHub API commands. Default: "10s".
	GhCmdTimeout string `json:"gh_cmd_timeout,omitempty"`
	// TmuxCmdTimeout is the timeout for tmux queries. Default: "2s".
	TmuxCmdTimeout string `json:"tmux_cmd_timeout,omitempty"`
	// FetchTimeout is the maximum time for all dashboard data fetches. Default: "8s".
	FetchTimeout string `json:"fetch_timeout,omitempty"`
	// DefaultRunTimeout is the default timeout for /api/run commands. Default: "30s".
	DefaultRunTimeout string `json:"default_run_timeout,omitempty"`
	// MaxRunTimeout is the maximum allowed timeout for /api/run commands. Default: "60s".
	MaxRunTimeout string `json:"max_run_timeout,omitempty"`
}

// DefaultWebTimeoutsConfig returns a WebTimeoutsConfig with sensible defaults.
func DefaultWebTimeoutsConfig() *WebTimeoutsConfig {
	return &WebTimeoutsConfig{
		CmdTimeout:        "15s",
		GhCmdTimeout:      "10s",
		TmuxCmdTimeout:    "2s",
		FetchTimeout:      "8s",
		DefaultRunTimeout: "30s",
		MaxRunTimeout:     "60s",
	}
}

// WorkerStatusConfig configures activity-age thresholds for worker status classification.
type WorkerStatusConfig struct {
	// StaleThreshold is the activity age after which a worker is considered "stale".
	// Default: "5m".
	StaleThreshold string `json:"stale_threshold,omitempty"`
	// StuckThreshold is the activity age after which a worker is considered "stuck".
	// Default: "30m".
	StuckThreshold string `json:"stuck_threshold,omitempty"`
	// HeartbeatFreshThreshold is the max age for a Deacon heartbeat to be considered fresh.
	// Default: "5m".
	HeartbeatFreshThreshold string `json:"heartbeat_fresh_threshold,omitempty"`
	// MayorActiveThreshold is the max session inactivity for the Mayor to be considered active.
	// Default: "5m".
	MayorActiveThreshold string `json:"mayor_active_threshold,omitempty"`
}

// DefaultWorkerStatusConfig returns a WorkerStatusConfig with sensible defaults.
func DefaultWorkerStatusConfig() *WorkerStatusConfig {
	return &WorkerStatusConfig{
		StaleThreshold:          "5m",
		StuckThreshold:          "30m",
		HeartbeatFreshThreshold: "5m",
		MayorActiveThreshold:    "5m",
	}
}

// FeedCuratorConfig configures event deduplication and aggregation windows.
type FeedCuratorConfig struct {
	// DoneDedupeWindow is the time window for deduplicating repeated done events.
	// Default: "10s".
	DoneDedupeWindow string `json:"done_dedupe_window,omitempty"`
	// SlingAggregateWindow is the time window for aggregating sling events.
	// Default: "30s".
	SlingAggregateWindow string `json:"sling_aggregate_window,omitempty"`
	// MinAggregateCount is the minimum number of events to trigger aggregation.
	// Default: 3.
	MinAggregateCount int `json:"min_aggregate_count,omitempty"`
}

// DefaultFeedCuratorConfig returns a FeedCuratorConfig with sensible defaults.
func DefaultFeedCuratorConfig() *FeedCuratorConfig {
	return &FeedCuratorConfig{
		DoneDedupeWindow:     "10s",
		SlingAggregateWindow: "30s",
		MinAggregateCount:    3,
	}
}

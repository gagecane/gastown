package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RestartTrackerConfig holds configurable parameters for restart tracking.
// All fields have sensible defaults if zero-valued.
type RestartTrackerConfig struct {
	// InitialBackoff is the delay before the first retry (default 30s).
	InitialBackoff time.Duration `json:"initial_backoff,omitempty"`

	// MaxBackoff is the maximum backoff delay (default 10m).
	MaxBackoff time.Duration `json:"max_backoff,omitempty"`

	// BackoffMultiplier scales the backoff on each retry (default 2.0).
	BackoffMultiplier float64 `json:"backoff_multiplier,omitempty"`

	// CrashLoopWindow is the time window for counting crash-loop restarts (default 15m).
	CrashLoopWindow time.Duration `json:"crash_loop_window,omitempty"`

	// CrashLoopCount is how many restarts within the window trigger crash-loop state (default 5).
	CrashLoopCount int `json:"crash_loop_count,omitempty"`

	// StabilityPeriod is how long an agent must run without restarting
	// before its backoff resets (default 30m).
	StabilityPeriod time.Duration `json:"stability_period,omitempty"`

	// PauseBackoff is the fixed delay applied when an agent is paused due
	// to a transient external limit (e.g., Claude usage-limit reached)
	// rather than a true crash. Does not escalate and does not count toward
	// the crash-loop fault budget. Default 60s — long enough for the
	// quota_dog patrol to rotate accounts (5m cadence), short enough to
	// recover quickly when the limit resets.
	PauseBackoff time.Duration `json:"pause_backoff,omitempty"`
}

// DefaultRestartTrackerConfig returns the default restart tracker configuration.
func DefaultRestartTrackerConfig() RestartTrackerConfig {
	return RestartTrackerConfig{
		InitialBackoff:    30 * time.Second,
		MaxBackoff:        10 * time.Minute,
		BackoffMultiplier: 2.0,
		CrashLoopWindow:   15 * time.Minute,
		CrashLoopCount:    5,
		StabilityPeriod:   30 * time.Minute,
		PauseBackoff:      60 * time.Second,
	}
}

// withDefaults returns a config with zero fields filled from defaults.
func (c RestartTrackerConfig) withDefaults() RestartTrackerConfig {
	d := DefaultRestartTrackerConfig()
	if c.InitialBackoff <= 0 {
		c.InitialBackoff = d.InitialBackoff
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = d.MaxBackoff
	}
	if c.BackoffMultiplier <= 0 {
		c.BackoffMultiplier = d.BackoffMultiplier
	}
	if c.CrashLoopWindow <= 0 {
		c.CrashLoopWindow = d.CrashLoopWindow
	}
	if c.CrashLoopCount <= 0 {
		c.CrashLoopCount = d.CrashLoopCount
	}
	if c.StabilityPeriod <= 0 {
		c.StabilityPeriod = d.StabilityPeriod
	}
	if c.PauseBackoff <= 0 {
		c.PauseBackoff = d.PauseBackoff
	}
	return c
}

// RestartTracker tracks agent restart attempts with exponential backoff.
// This prevents runaway restart loops when an agent keeps crashing.
type RestartTracker struct {
	mu       sync.RWMutex
	townRoot string
	config   RestartTrackerConfig
	state    *RestartState
}

// RestartState persists restart tracking data.
type RestartState struct {
	Agents map[string]*AgentRestartInfo `json:"agents"`
}

// AgentRestartInfo tracks restart info for a single agent.
type AgentRestartInfo struct {
	LastRestart    time.Time `json:"last_restart"`
	RestartCount   int       `json:"restart_count"`
	BackoffUntil   time.Time `json:"backoff_until"`
	CrashLoopSince time.Time `json:"crash_loop_since,omitempty"`
}

// NewRestartTracker creates a new restart tracker with the given config.
// Zero-valued config fields are filled with defaults.
func NewRestartTracker(townRoot string, cfg RestartTrackerConfig) *RestartTracker {
	return &RestartTracker{
		townRoot: townRoot,
		config:   cfg.withDefaults(),
		state:    &RestartState{Agents: make(map[string]*AgentRestartInfo)},
	}
}

// restartStateFile returns the path to the restart state file.
func (rt *RestartTracker) restartStateFile() string {
	return filepath.Join(rt.townRoot, "daemon", "restart_state.json")
}

// Load loads the restart state from disk.
func (rt *RestartTracker) Load() error {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	data, err := os.ReadFile(rt.restartStateFile())
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No state file yet
		}
		return err
	}

	return json.Unmarshal(data, rt.state)
}

// Save persists the restart state to disk.
func (rt *RestartTracker) Save() error {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	data, err := json.MarshalIndent(rt.state, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(rt.restartStateFile(), data, 0600)
}

// CanRestart checks if an agent can be restarted (not in backoff or crash loop).
func (rt *RestartTracker) CanRestart(agentID string) bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	info, exists := rt.state.Agents[agentID]
	if !exists {
		return true
	}

	// Check if in crash loop (respects auto-expiry)
	if !info.CrashLoopSince.IsZero() {
		crashLoopExpiry := 2 * rt.config.StabilityPeriod
		if time.Since(info.CrashLoopSince) <= crashLoopExpiry {
			return false
		}
		// Crash loop expired — fall through to backoff check
	}

	// Check backoff period
	return time.Now().After(info.BackoffUntil)
}

// RecordRestart records a restart attempt and calculates next backoff.
func (rt *RestartTracker) RecordRestart(agentID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	now := time.Now()
	info, exists := rt.state.Agents[agentID]
	if !exists {
		info = &AgentRestartInfo{}
		rt.state.Agents[agentID] = info
	}

	// Check if previous restart was stable (long ago)
	if !info.LastRestart.IsZero() && now.Sub(info.LastRestart) > rt.config.StabilityPeriod {
		// Reset backoff - agent was stable
		info.RestartCount = 0
		info.CrashLoopSince = time.Time{}
	}

	info.LastRestart = now
	info.RestartCount++

	// Calculate backoff with exponential increase
	backoffDuration := rt.config.InitialBackoff
	for i := 1; i < info.RestartCount && backoffDuration < rt.config.MaxBackoff; i++ {
		backoffDuration = time.Duration(float64(backoffDuration) * rt.config.BackoffMultiplier)
	}
	if backoffDuration > rt.config.MaxBackoff {
		backoffDuration = rt.config.MaxBackoff
	}
	info.BackoffUntil = now.Add(backoffDuration)

	// Check for crash loop
	if info.RestartCount >= rt.config.CrashLoopCount {
		windowStart := now.Add(-rt.config.CrashLoopWindow)
		if info.LastRestart.After(windowStart) {
			info.CrashLoopSince = now
		}
	}
}

// RecordPause records that an agent is paused due to a transient external
// limit (e.g., Claude usage-limit reached) rather than a crashing.
//
// Applies the fixed PauseBackoff delay without escalating, and does NOT
// increment RestartCount or set CrashLoopSince. Use this when the agent's
// failure is a rate-limit response — restarts under these conditions are
// not a sign of instability and should not count against the crash-loop
// fault budget.
func (rt *RestartTracker) RecordPause(agentID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	now := time.Now()
	info, exists := rt.state.Agents[agentID]
	if !exists {
		info = &AgentRestartInfo{}
		rt.state.Agents[agentID] = info
	}

	info.LastRestart = now
	info.BackoffUntil = now.Add(rt.config.PauseBackoff)
}

// RecordSuccess records that an agent is running successfully.
// Call this periodically for healthy agents to reset their backoff.
func (rt *RestartTracker) RecordSuccess(agentID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	info, exists := rt.state.Agents[agentID]
	if !exists {
		return
	}

	// If agent has been stable for the stability period, reset tracking
	if time.Since(info.LastRestart) > rt.config.StabilityPeriod {
		info.RestartCount = 0
		info.CrashLoopSince = time.Time{}
		info.BackoffUntil = time.Time{}
	}
}

// IsInCrashLoop returns true if the agent is detected as crash-looping.
//
// A crash-loop flag automatically expires after 2× StabilityPeriod (default: 1h).
// This prevents a stale flag from permanently blocking restarts when the agent
// has recovered externally (e.g., manual restart) but nothing in the daemon
// called RecordSuccess or ClearCrashLoop. See gc-1obg9.
func (rt *RestartTracker) IsInCrashLoop(agentID string) bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	info, exists := rt.state.Agents[agentID]
	if !exists {
		return false
	}
	if info.CrashLoopSince.IsZero() {
		return false
	}

	// Auto-expire: crash-loop flag older than 2× StabilityPeriod is stale.
	crashLoopExpiry := 2 * rt.config.StabilityPeriod
	if time.Since(info.CrashLoopSince) > crashLoopExpiry {
		return false
	}
	return true
}

// GetBackoffRemaining returns how long until the agent can be restarted.
func (rt *RestartTracker) GetBackoffRemaining(agentID string) time.Duration {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	info, exists := rt.state.Agents[agentID]
	if !exists {
		return 0
	}

	remaining := time.Until(info.BackoffUntil)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// ClearCrashLoop manually clears the crash loop state for an agent.
func (rt *RestartTracker) ClearCrashLoop(agentID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	info, exists := rt.state.Agents[agentID]
	if exists {
		info.CrashLoopSince = time.Time{}
		info.RestartCount = 0
		info.BackoffUntil = time.Time{}
	}
}

// ClearAgentBackoff clears the crash loop and backoff state for an agent on disk.
// Used by 'gt daemon clear-backoff' to reset an agent stuck in crash loop.
// The daemon reloads this on next heartbeat (or immediately on SIGUSR2).
func ClearAgentBackoff(townRoot, agentID string) error {
	rt := NewRestartTracker(townRoot, RestartTrackerConfig{})
	if err := rt.Load(); err != nil {
		return fmt.Errorf("loading restart state: %w", err)
	}
	rt.ClearCrashLoop(agentID)
	return rt.Save()
}

// PruneResult holds the result of a state pruning operation.
type PruneResult struct {
	Pruned []string // Agent IDs removed from state
	Kept   []string // Agent IDs retained (session still exists)
	Total  int      // Total entries before pruning
}

// PruneStaleAgents removes entries from restart_state.json for agents
// whose tmux sessions no longer exist. This prevents unbounded growth
// of the state file from ephemeral polecat sessions.
//
// The activeSessions set should contain all current tmux session names.
// Agent IDs in the tracker are matched against sessions using a provided
// resolver function that maps agent IDs to session names.
func (rt *RestartTracker) PruneStaleAgents(activeSessions map[string]bool, resolveSession func(string) string) *PruneResult {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	result := &PruneResult{
		Total: len(rt.state.Agents),
	}

	for agentID := range rt.state.Agents {
		sessionName := resolveSession(agentID)
		if sessionName == "" || !activeSessions[sessionName] {
			result.Pruned = append(result.Pruned, agentID)
		} else {
			result.Kept = append(result.Kept, agentID)
		}
	}

	for _, agentID := range result.Pruned {
		delete(rt.state.Agents, agentID)
	}

	return result
}

// PruneStaleState removes entries from restart_state.json for agents whose
// tmux sessions no longer exist. Called by 'gt daemon prune-state'.
// The resolveSession function maps agent IDs (as stored in the tracker)
// to tmux session names for liveness checking.
func PruneStaleState(townRoot string, activeSessions []string, resolveSession func(string) string) (*PruneResult, error) {
	rt := NewRestartTracker(townRoot, RestartTrackerConfig{})
	if err := rt.Load(); err != nil {
		return nil, fmt.Errorf("loading restart state: %w", err)
	}

	sessionSet := make(map[string]bool, len(activeSessions))
	for _, s := range activeSessions {
		sessionSet[s] = true
	}

	result := rt.PruneStaleAgents(sessionSet, resolveSession)

	if len(result.Pruned) > 0 {
		if err := rt.Save(); err != nil {
			return result, fmt.Errorf("saving pruned state: %w", err)
		}
	}

	return result, nil
}

// PreviewPruneStaleState returns what PruneStaleState would prune without
// modifying state on disk. Used for --dry-run.
func PreviewPruneStaleState(townRoot string, activeSessions []string, resolveSession func(string) string) (*PruneResult, error) {
	rt := NewRestartTracker(townRoot, RestartTrackerConfig{})
	if err := rt.Load(); err != nil {
		return nil, fmt.Errorf("loading restart state: %w", err)
	}

	sessionSet := make(map[string]bool, len(activeSessions))
	for _, s := range activeSessions {
		sessionSet[s] = true
	}

	// Compute what would be pruned without modifying the tracker
	result := &PruneResult{
		Total: len(rt.state.Agents),
	}

	rt.mu.RLock()
	defer rt.mu.RUnlock()

	for agentID := range rt.state.Agents {
		sessionName := resolveSession(agentID)
		if sessionName == "" || !sessionSet[sessionName] {
			result.Pruned = append(result.Pruned, agentID)
		} else {
			result.Kept = append(result.Kept, agentID)
		}
	}

	return result, nil
}

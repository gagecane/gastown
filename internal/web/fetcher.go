// Package web implements the Gas Town dashboard HTTP server and its data
// fetchers. The LiveConvoyFetcher in this file exposes a Fetch* method per
// dashboard panel; each Fetch* implementation lives in a domain-specific
// sibling file:
//
//	fetcher_convoys.go      — FetchConvoys + tracked-issue helpers
//	fetcher_mergequeue.go   — FetchMergeQueue
//	fetcher_workers.go      — FetchWorkers + worker status helpers
//	fetcher_mail.go         — FetchMail + formatting helpers
//	fetcher_rigs.go         — FetchRigs, FetchDogs, FetchEscalations,
//	                          FetchHealth, FetchQueues
//	fetcher_sessions.go     — FetchSessions, FetchHooks
//	fetcher_mayor.go        — FetchMayor + runtime label helpers
//	fetcher_issues.go       — FetchIssues
//	fetcher_activity.go     — FetchActivity + event-formatting helpers
//
// This file holds the LiveConvoyFetcher type, constructor, shared
// infrastructure (runCmd, runBdCmd, tmux arg prefixing), and the
// circuit breaker used by FetchConvoys.
package web

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// runCmd executes a command with a timeout and returns stdout.
// Returns empty buffer on timeout or error.
// Security: errors from this function are logged server-side only (via log.Printf
// in callers) and never included in HTTP responses. The handler renders templates
// with whatever data was successfully fetched; fetch failures result in empty panels.
func runCmd(timeout time.Duration, name string, args ...string) (*bytes.Buffer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var cmd *exec.Cmd
	if name == "tmux" {
		// Route tmux through BuildCommandContext so the town socket (-L) is applied.
		// Without this, the dashboard hits the default tmux server and sees 0 sessions.
		cmd = tmux.BuildCommandContext(ctx, args...)
	} else {
		cmd = exec.CommandContext(ctx, name, args...)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%s timed out after %v", name, timeout)
		}
		return nil, err
	}
	return &stdout, nil
}

// fetcherRunCmd is the test seam for runCmd. Tests override this to inject
// canned bd/tmux output without spawning real subprocesses.
var fetcherRunCmd = runCmd

// fetcherGetSessionEnv is the test seam for tmux.GetEnvironment. Tests override
// this to simulate session environment lookups.
var fetcherGetSessionEnv = func(sessionName, key string) (string, error) {
	return tmux.NewTmux().GetEnvironment(sessionName, key)
}

// runBdCmd executes a bd command with the configured cmdTimeout in the specified beads directory.
func (f *LiveConvoyFetcher) runBdCmd(beadsDir string, args ...string) (*bytes.Buffer, error) {
	// bd v0.59+ requires --flat for list --json to produce JSON output
	args = beads.InjectFlatForListJSON(args)

	ctx, cancel := context.WithTimeout(context.Background(), f.cmdTimeout)
	defer cancel()

	bin := f.bdBin
	if bin == "" {
		bin = "bd"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = beadsDir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	err := cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("bd timed out after %v", f.cmdTimeout)
		}
		// If we got some output, return it anyway (bd may exit non-zero with warnings)
		if stdout.Len() > 0 {
			return &stdout, nil
		}
		return nil, err
	}
	return &stdout, nil
}

// fetchCircuitBreaker tracks consecutive failures for a fetch operation
// and applies exponential backoff to prevent process storms.
type fetchCircuitBreaker struct {
	mu          sync.Mutex
	failures    int
	lastAttempt time.Time
	backoff     time.Duration
}

// maxBackoff is the maximum backoff duration for the circuit breaker.
const maxBackoff = 5 * time.Minute

// allow returns true if enough time has passed since the last failure to permit
// a new attempt. Always allows the first attempt (zero failures).
func (cb *fetchCircuitBreaker) allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.failures == 0 {
		return true
	}
	return time.Since(cb.lastAttempt) >= cb.backoff
}

// recordFailure increments the failure count and sets exponential backoff.
// Backoff doubles from 10s up to maxBackoff.
func (cb *fetchCircuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures++
	cb.lastAttempt = time.Now()
	// Exponential backoff: 10s, 20s, 40s, 80s, 160s, capped at maxBackoff
	cb.backoff = time.Duration(1<<min(cb.failures, 10)) * 5 * time.Second
	if cb.backoff > maxBackoff {
		cb.backoff = maxBackoff
	}
}

// recordSuccess resets the circuit breaker on a successful fetch.
func (cb *fetchCircuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures = 0
	cb.backoff = 0
}

// LiveConvoyFetcher fetches convoy data from beads.
type LiveConvoyFetcher struct {
	townRoot  string
	townBeads string

	// tmuxSocket is the tmux socket name (-L flag) for multi-instance isolation.
	tmuxSocket string

	// bdBin is the bd binary name or path. Defaults to "bd" if empty.
	bdBin string

	// registry is a prefix registry built from the town's rigs.json.
	// Used for parsing tmux session names instead of relying on the
	// package-level DefaultRegistry, which may not be initialized in
	// the dashboard process context.
	registry *session.PrefixRegistry

	// Configurable timeouts (from TownSettings.WebTimeouts)
	cmdTimeout     time.Duration
	ghCmdTimeout   time.Duration
	tmuxCmdTimeout time.Duration

	// Configurable worker status thresholds (from TownSettings.WorkerStatus)
	staleThreshold          time.Duration
	stuckThreshold          time.Duration
	heartbeatFreshThreshold time.Duration
	mayorActiveThreshold    time.Duration

	// Circuit breaker for FetchConvoys — prevents process storms when
	// bd list --type=convoy fails persistently (e.g., schema mismatch).
	convoyBreaker fetchCircuitBreaker
}

// NewLiveConvoyFetcher creates a fetcher for the current workspace.
// Loads timeout and threshold config from TownSettings; falls back to defaults if missing.
func NewLiveConvoyFetcher() (*LiveConvoyFetcher, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	webCfg := config.DefaultWebTimeoutsConfig()
	workerCfg := config.DefaultWorkerStatusConfig()
	if ts, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot)); err == nil {
		// Replace entire defaults — individual fields fall back via ParseDurationOrDefault
		// (empty string → hardcoded default). Add explicit zero-value guards for non-duration fields.
		if ts.WebTimeouts != nil {
			webCfg = ts.WebTimeouts
		}
		if ts.WorkerStatus != nil {
			workerCfg = ts.WorkerStatus
		}
	}

	// Build a local prefix registry from the town's rigs.json so session
	// name parsing works regardless of whether the package-level
	// DefaultRegistry was initialized (gt-y24).
	registry, regErr := session.BuildPrefixRegistryFromTown(townRoot)
	if regErr != nil {
		log.Printf("dashboard: failed to build prefix registry: %v (falling back to default)", regErr)
		registry = session.DefaultRegistry()
	}

	return &LiveConvoyFetcher{
		townRoot:                townRoot,
		townBeads:               filepath.Join(townRoot, ".beads"),
		registry:                registry,
		tmuxSocket:              tmux.GetDefaultSocket(),
		cmdTimeout:              config.ParseDurationOrDefault(webCfg.CmdTimeout, 15*time.Second),
		ghCmdTimeout:            config.ParseDurationOrDefault(webCfg.GhCmdTimeout, 10*time.Second),
		tmuxCmdTimeout:          config.ParseDurationOrDefault(webCfg.TmuxCmdTimeout, 2*time.Second),
		staleThreshold:          config.ParseDurationOrDefault(workerCfg.StaleThreshold, 5*time.Minute),
		stuckThreshold:          config.ParseDurationOrDefault(workerCfg.StuckThreshold, constants.GUPPViolationTimeout),
		heartbeatFreshThreshold: config.ParseDurationOrDefault(workerCfg.HeartbeatFreshThreshold, 5*time.Minute),
		mayorActiveThreshold:    config.ParseDurationOrDefault(workerCfg.MayorActiveThreshold, 5*time.Minute),
	}, nil
}

// tmuxArgs prepends -L socketName to tmux args when a custom socket is configured.
func (f *LiveConvoyFetcher) tmuxArgs(args ...string) []string {
	if f.tmuxSocket != "" {
		return append([]string{"-L", f.tmuxSocket}, args...)
	}
	return args
}

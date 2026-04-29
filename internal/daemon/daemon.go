package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/beads"
	agentconfig "github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/feed"
	gitpkg "github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Daemon is the town-level background service.
// It ensures patrol agents (Deacon, Witnesses) are running and detects failures.
// This is recovery-focused: normal wake is handled by feed subscription (bd activity --follow).
// The daemon is the safety net for dead sessions, GUPP violations, and orphaned work.
type Daemon struct {
	config        *Config
	patrolConfig  *DaemonPatrolConfig
	tmux          *tmux.Tmux
	logger        *log.Logger
	ctx           context.Context
	cancel        context.CancelFunc
	curator       *feed.Curator
	convoyManager *ConvoyManager
	autoDispatch  *AutoDispatchWatcher
	beadsStores   map[string]beadsdk.Storage
	doltServer    *DoltServerManager
	krcPruner     *KRCPruner

	// disabledPatrols is loaded from town settings (disabled_patrols field).
	// Provides a simple way to disable individual patrol dogs without editing
	// mayor/daemon.json. Checked by isPatrolActive alongside patrolConfig.
	disabledPatrols map[string]bool

	// Mass death detection: track recent session deaths
	deathsMu     sync.Mutex
	recentDeaths []sessionDeath

	// Deacon startup tracking: prevents race condition where newly started
	// sessions are immediately killed by the heartbeat check.
	// See: https://github.com/steveyegge/gastown/issues/567
	// Note: Only accessed from heartbeat loop goroutine - no sync needed.
	deaconLastStarted time.Time

	// syncFailures tracks consecutive git pull failures per workdir.
	// Used to escalate logging from WARN to ERROR after repeated failures.
	// Only accessed from heartbeat loop goroutine - no sync needed.
	syncFailures map[string]int

	// PATCH-006: Resolved binary paths to avoid PATH issues in subprocesses.
	gtPath string
	bdPath string

	// Boot spawn cooldown: prevents Boot from spawning on every heartbeat tick.
	// Only accessed from heartbeat loop goroutine - no sync needed.
	bootLastSpawned time.Time

	// Restart tracking with exponential backoff to prevent crash loops
	restartTracker *RestartTracker

	// telemetry exports metrics and logs to VictoriaMetrics / VictoriaLogs.
	// Nil when telemetry is disabled (GT_OTEL_METRICS_URL / GT_OTEL_LOGS_URL not set).
	otelProvider *telemetry.Provider
	metrics      *daemonMetrics

	// jsonlPushFailures tracks consecutive git push failures for JSONL backup.
	// Only accessed from heartbeat loop goroutine - no sync needed.
	jsonlPushFailures int

	// lastDoctorMolTime tracks when the last mol-dog-doctor molecule was poured.
	// Option B throttling: only pour when anomaly detected AND cooldown elapsed.
	// Only accessed from heartbeat loop goroutine - no sync needed.
	lastDoctorMolTime time.Time

	// lastMaintenanceRun tracks when scheduled maintenance last ran.
	// Only accessed from heartbeat loop goroutine - no sync needed.
	lastMaintenanceRun time.Time

	// mayorZombieCount tracks consecutive patrol cycles where the Mayor tmux
	// session exists but the agent process is not detected. A count >= 3
	// triggers a zombie restart, debouncing transient gaps during handoffs.
	// Only accessed from heartbeat loop goroutine - no sync needed.
	mayorZombieCount int

	// rigPool runs per-rig heartbeat operations (witness checks, refinery checks,
	// polecat health, idle reaping, branch pruning) with bounded concurrency and
	// per-rig context timeouts so one slow rig cannot block all others.
	rigPool *RigWorkerPool

	// knownRigsCache memoizes the result of reading mayor/rigs.json for the
	// duration of a single heartbeat tick. ~10 call sites per tick otherwise
	// re-read and re-parse the same file. Invalidated at the start of each
	// heartbeat so rigs.json changes between ticks are picked up.
	// Only accessed from heartbeat loop goroutine - no sync needed.
	knownRigsCache      []string
	knownRigsCacheValid bool
}

// sessionDeath records a detected session death for mass death analysis.
type sessionDeath struct {
	sessionName string
	timestamp   time.Time
}

// Mass death detection parameters — these are fallback defaults.
// Prefer config.OperationalConfig.GetDaemonConfig() accessors when
// a TownSettings is available (loaded via d.loadOperationalConfig()).
const (
	massDeathWindow    = 30 * time.Second // Time window to detect mass death
	massDeathThreshold = 3                // Number of deaths to trigger alert

	// doctorMolCooldown is the minimum interval between mol-dog-doctor molecules.
	// Configurable via operational.daemon.doctor_mol_cooldown.
	doctorMolCooldown = 5 * time.Minute
)

// New creates a new daemon instance.
func New(config *Config) (*Daemon, error) {
	// Ensure daemon directory exists
	daemonDir := filepath.Dir(config.LogFile)
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		return nil, fmt.Errorf("creating daemon directory: %w", err)
	}

	// Open log file with rotation (100MB max, 3 backups, 7 days, compressed)
	logWriter := &lumberjack.Logger{
		Filename:   config.LogFile,
		MaxSize:    100, // megabytes
		MaxBackups: 3,
		MaxAge:     7, // days
		Compress:   true,
	}

	logger := log.New(logWriter, "", log.LstdFlags)
	ctx, cancel := context.WithCancel(context.Background())

	// Initialize session prefix and agent registries from town root.
	if err := session.InitRegistry(config.TownRoot); err != nil {
		logger.Printf("Warning: failed to initialize town registry: %v", err)
	}

	// Set GT_TOWN_ROOT in the daemon process env so Go code (e.g.,
	// sessionPrefixPattern) can read it without relying on GT_ROOT.
	os.Setenv("GT_TOWN_ROOT", config.TownRoot)

	// Also set GT_TOWN_ROOT in tmux global environment so run-shell subprocesses
	// (e.g., gt cycle next/prev) can find the workspace even when CWD is $HOME.
	// Non-fatal: tmux server may not be running yet — daemon creates sessions shortly.
	t := tmux.NewTmux()
	if err := t.SetGlobalEnvironment("GT_TOWN_ROOT", config.TownRoot); err != nil {
		logger.Printf("Warning: failed to set GT_TOWN_ROOT in tmux global env: %v", err)
	}

	// Clear any agent identity vars that leaked into tmux global env.
	// Only GT_TOWN_ROOT should be global. Leaked identity vars cause sessions
	// without their own session-level overrides to inherit a stale identity,
	// misattributing beads and mail. GH#3006.
	identityVars := agentconfig.IdentityEnvVars
	for _, k := range identityVars {
		_ = t.UnsetGlobalEnvironment(k)
	}

	// Load patrol config from mayor/daemon.json, ensuring lifecycle defaults
	// are populated for any missing data maintenance tickers. Without this,
	// opt-in patrols (compactor, reaper, doctor, JSONL backup, dolt backup)
	// remain disabled if the file was created before they were implemented.
	if err := EnsureLifecycleConfigFile(config.TownRoot); err != nil {
		logger.Printf("Warning: failed to ensure lifecycle config: %v", err)
	}
	patrolConfig := LoadPatrolConfig(config.TownRoot)
	if patrolConfig != nil {
		logger.Printf("Loaded patrol config from %s", PatrolConfigFile(config.TownRoot))
		// Propagate env vars from daemon.json to this process and all spawned sessions.
		for k, v := range patrolConfig.Env {
			os.Setenv(k, v)
			logger.Printf("Set env %s=%s from daemon.json", k, v)
		}
	}

	// Load disabled_patrols from town settings (settings/config.json).
	// This provides a simpler way to disable patrols than editing daemon.json.
	disabledPatrols := loadDisabledPatrolsFromTownSettings(config.TownRoot)
	if len(disabledPatrols) > 0 {
		names := make([]string, 0, len(disabledPatrols))
		for k := range disabledPatrols {
			names = append(names, k)
		}
		logger.Printf("Patrols disabled via town settings: %v", names)
	}

	// Initialize Dolt server manager if configured
	var doltServer *DoltServerManager
	if patrolConfig != nil && patrolConfig.Patrols != nil && patrolConfig.Patrols.DoltServer != nil {
		doltServer = NewDoltServerManager(config.TownRoot, patrolConfig.Patrols.DoltServer, logger.Printf)
		if doltServer.IsEnabled() {
			logger.Printf("Dolt server management enabled (port %d)", patrolConfig.Patrols.DoltServer.Port)
			// Propagate Dolt connection info to process env so AgentEnv() passes it to
			// all spawned agent sessions. Without this, bd in agent sessions
			// auto-starts rogue Dolt instances or connects to localhost. (GH#2412)
			portStr := strconv.Itoa(patrolConfig.Patrols.DoltServer.Port)
			os.Setenv("GT_DOLT_PORT", portStr)
			os.Setenv("BEADS_DOLT_PORT", portStr)
			if patrolConfig.Patrols.DoltServer.Host != "" {
				os.Setenv("GT_DOLT_HOST", patrolConfig.Patrols.DoltServer.Host)
				os.Setenv("BEADS_DOLT_SERVER_HOST", patrolConfig.Patrols.DoltServer.Host)
			}
		}
	}

	// Fallback: if GT_DOLT_PORT still isn't set (no DoltServerManager, daemon
	// started independently of gt up), detect the port from dolt config.
	// This ensures AgentEnv() always has the port for spawned sessions. (GH#2412)
	if os.Getenv("GT_DOLT_PORT") == "" {
		doltCfg := doltserver.DefaultConfig(config.TownRoot)
		if doltCfg.Port > 0 {
			portStr := strconv.Itoa(doltCfg.Port)
			os.Setenv("GT_DOLT_PORT", portStr)
			os.Setenv("BEADS_DOLT_PORT", portStr)
			logger.Printf("Set GT_DOLT_PORT=%s from Dolt config (fallback)", portStr)
		}
	}

	// Propagate Dolt host to process env so bd doesn't fall back to 127.0.0.1
	// when the server runs on a remote machine (e.g., mini2 over Tailscale).
	if os.Getenv("BEADS_DOLT_SERVER_HOST") == "" {
		doltCfg := doltserver.DefaultConfig(config.TownRoot)
		if doltCfg.Host != "" {
			os.Setenv("BEADS_DOLT_SERVER_HOST", doltCfg.Host)
			logger.Printf("Set BEADS_DOLT_SERVER_HOST=%s from Dolt config", doltCfg.Host)
		}
	}

	// PATCH-006: Resolve binary paths at startup.
	gtPath, err := exec.LookPath("gt")
	if err != nil {
		gtPath = "gt"
		logger.Printf("Warning: gt not found in PATH, subprocess calls may fail")
	}
	bdPath, err := exec.LookPath("bd")
	if err != nil {
		bdPath = "bd"
		logger.Printf("Warning: bd not found in PATH, subprocess calls may fail")
	}

	// Initialize restart tracker with exponential backoff.
	// Parameters are configurable via patrols.restart_tracker in daemon.json.
	var rtCfg RestartTrackerConfig
	if patrolConfig != nil && patrolConfig.Patrols != nil && patrolConfig.Patrols.RestartTracker != nil {
		rtCfg = *patrolConfig.Patrols.RestartTracker
	}
	restartTracker := NewRestartTracker(config.TownRoot, rtCfg)
	if err := restartTracker.Load(); err != nil {
		logger.Printf("Warning: failed to load restart state: %v", err)
	}

	// Initialize OpenTelemetry (best-effort — telemetry failure never blocks startup).
	// Activate by setting GT_OTEL_METRICS_URL and/or GT_OTEL_LOGS_URL.
	otelProvider, otelErr := telemetry.Init(ctx, "gastown-daemon", "")
	if otelErr != nil {
		logger.Printf("Warning: telemetry init failed: %v", otelErr)
	}
	var dm *daemonMetrics
	if otelProvider != nil {
		dm, err = newDaemonMetrics()
		if err != nil {
			logger.Printf("Warning: failed to register daemon metrics: %v", err)
			dm = nil
		} else {
			metricsURL := os.Getenv(telemetry.EnvMetricsURL)
			if metricsURL == "" {
				metricsURL = telemetry.DefaultMetricsURL
			}
			logsURL := os.Getenv(telemetry.EnvLogsURL)
			if logsURL == "" {
				logsURL = telemetry.DefaultLogsURL
			}
			logger.Printf("Telemetry active (metrics → %s, logs → %s)",
				metricsURL, logsURL)
		}
	}

	return &Daemon{
		config:          config,
		patrolConfig:    patrolConfig,
		disabledPatrols: disabledPatrols,
		tmux:            tmux.NewTmux(),
		logger:          logger,
		ctx:             ctx,
		cancel:          cancel,
		doltServer:      doltServer,
		gtPath:          gtPath,
		bdPath:          bdPath,
		restartTracker:  restartTracker,
		otelProvider:    otelProvider,
		metrics:         dm,
		rigPool:         newRigWorkerPool(0, 0, logger), // defaults: 10 workers, 30s timeout
	}, nil
}

// Run starts the daemon main loop.
func (d *Daemon) Run() (err error) {
	pid := os.Getpid()
	d.logger.Printf("Daemon starting (PID %d)", pid)
	startupComplete := false
	defer func() {
		if err == nil {
			return
		}
		if startupComplete {
			d.logger.Printf("Daemon exiting with error (PID %d): %v", pid, err)
			return
		}
		d.logger.Printf("Daemon startup failed (PID %d): %v", pid, err)
	}()

	// Acquire exclusive lock to prevent multiple daemons from running.
	// This prevents the TOCTOU race condition where multiple concurrent starts
	// can all pass the IsRunning() check before any writes the PID file.
	// Uses gofrs/flock for cross-platform compatibility (Unix + Windows).
	lockFile := filepath.Join(d.config.TownRoot, "daemon", "daemon.lock")
	fileLock := flock.New(lockFile)

	// Try to acquire exclusive lock (non-blocking)
	locked, err := fileLock.TryLock()
	if err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("daemon already running (lock held by another process)")
	}
	defer func() { _ = fileLock.Unlock() }()

	// Pre-flight check: all rigs must be on Dolt backend.
	if err := d.checkAllRigsDolt(); err != nil {
		return err
	}

	// Repair metadata.json for all rigs on startup.
	// This ensures all rigs have proper Dolt server configuration.
	if _, errs := doltserver.EnsureAllMetadata(d.config.TownRoot); len(errs) > 0 {
		for _, e := range errs {
			d.logger.Printf("Warning: metadata repair: %v", e)
		}
	}

	// Write PID file with nonce for ownership verification
	if _, err := writePIDFile(d.config.PidFile, os.Getpid()); err != nil {
		return fmt.Errorf("writing PID file: %w", err)
	}
	defer func() { _ = os.Remove(d.config.PidFile) }() // best-effort cleanup

	// Update state
	state := &State{
		Running:   true,
		PID:       os.Getpid(),
		StartedAt: time.Now(),
	}
	if err := SaveState(d.config.TownRoot, state); err != nil {
		d.logger.Printf("Warning: failed to save state: %v", err)
	}

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, daemonSignals()...)

	// Fixed recovery-focused heartbeat (no activity-based backoff)
	// Normal wake is handled by feed subscription (bd activity --follow)
	timer := time.NewTimer(d.recoveryHeartbeatInterval())
	defer timer.Stop()

	d.logger.Printf("Daemon running, recovery heartbeat interval %v", d.recoveryHeartbeatInterval())

	// Start feed curator goroutine
	d.curator = feed.NewCurator(d.config.TownRoot)
	if err := d.curator.Start(); err != nil {
		d.logger.Printf("Warning: failed to start feed curator: %v", err)
	} else {
		d.logger.Println("Feed curator started")
	}

	// Start convoy manager (event-driven + periodic stranded scan)
	// Try opening beads stores eagerly; if Dolt isn't ready yet,
	// pass the opener as a callback for lazy retry on each poll tick.
	d.beadsStores, err = d.openBeadsStores()
	if err != nil {
		return err
	}
	isRigParked := func(rigName string) bool {
		ok, _ := d.isRigOperational(rigName)
		return !ok
	}
	var storeOpener func() map[string]beadsdk.Storage
	if len(d.beadsStores) == 0 {
		storeOpener = func() map[string]beadsdk.Storage {
			stores, err := d.openBeadsStores()
			if err != nil {
				d.logger.Printf("Convoy: beads compatibility check failed: %v", err)
				return nil
			}
			return stores
		}
	}
	d.convoyManager = NewConvoyManager(d.config.TownRoot, d.logger.Printf, d.gtPath, 0, d.beadsStores, storeOpener, isRigParked)
	if err := d.convoyManager.Start(); err != nil {
		d.logger.Printf("Warning: failed to start convoy manager: %v", err)
	} else {
		d.logger.Println("Convoy manager started")
	}

	// Wire a recovery callback so that when Dolt transitions from unhealthy
	// back to healthy, the convoy manager runs a sweep to catch any convoys
	// that completed during the outage and were missed by the event poller.
	if d.doltServer != nil {
		cm := d.convoyManager
		d.doltServer.SetRecoveryCallback(func() {
			d.logger.Printf("Dolt recovery detected: triggering convoy recovery sweep")
			cm.scan()
		})
	}

	// Start the auto-dispatch watcher: tails .events.jsonl and triggers
	// event-driven auto-dispatch on planned polecat completions (gt done),
	// bypassing the auto-dispatch plugin's cooldown gate. The cooldown-gated
	// periodic pass in dispatchPlugins() continues to run as the fallback.
	// Only active when the "handler" patrol is active — if plugins aren't
	// being dispatched at all, event-driven refill would have nothing to do.
	if d.isPatrolActive("handler") {
		d.autoDispatch = NewAutoDispatchWatcher(
			d.config.TownRoot,
			d.logger,
			newHandlerAutoDispatchConsumer(d),
		)
		if err := d.autoDispatch.Start(); err != nil {
			d.logger.Printf("Warning: failed to start auto-dispatch watcher: %v", err)
		} else {
			d.logger.Println("Auto-dispatch watcher started")
		}
	} else {
		d.logger.Println("Handler patrol disabled, auto-dispatch watcher not started")
	}

	// Start KRC pruner for automatic ephemeral data cleanup
	krcPruner, err := NewKRCPruner(d.config.TownRoot, d.logger.Printf)
	if err != nil {
		d.logger.Printf("Warning: failed to create KRC pruner: %v", err)
	} else {
		d.krcPruner = krcPruner
		if err := d.krcPruner.Start(); err != nil {
			d.logger.Printf("Warning: failed to start KRC pruner: %v", err)
		} else {
			d.logger.Println("KRC pruner started")
		}
	}

	// Start dedicated Dolt health check ticker if Dolt server is configured.
	// This runs at a much higher frequency (default 30s) than the general
	// heartbeat (3 min) so Dolt crashes are detected quickly.
	var doltHealthTicker *time.Ticker
	var doltHealthChan <-chan time.Time
	if d.doltServer != nil && d.doltServer.IsEnabled() {
		interval := d.doltServer.HealthCheckInterval()
		doltHealthTicker = time.NewTicker(interval)
		doltHealthChan = doltHealthTicker.C
		defer doltHealthTicker.Stop()
		d.logger.Printf("Dolt health check ticker started (interval %v)", interval)
	}

	// Start dedicated Dolt remotes push ticker if configured.
	// This runs at a lower frequency (default 15 min) than the heartbeat (3 min)
	// to periodically push databases to their git remotes.
	var doltRemotesTicker *time.Ticker
	var doltRemotesChan <-chan time.Time
	if d.isPatrolActive("dolt_remotes") {
		interval := doltRemotesInterval(d.patrolConfig)
		doltRemotesTicker = time.NewTicker(interval)
		doltRemotesChan = doltRemotesTicker.C
		defer doltRemotesTicker.Stop()
		d.logger.Printf("Dolt remotes push ticker started (interval %v)", interval)
	}

	// Start dedicated Dolt backup ticker if configured.
	// Runs filesystem backup sync (dolt backup sync) for production databases.
	var doltBackupTicker *time.Ticker
	var doltBackupChan <-chan time.Time
	if d.isPatrolActive("dolt_backup") {
		interval := doltBackupInterval(d.patrolConfig)
		doltBackupTicker = time.NewTicker(interval)
		doltBackupChan = doltBackupTicker.C
		defer doltBackupTicker.Stop()
		d.logger.Printf("Dolt backup ticker started (interval %v)", interval)
	}

	// Start JSONL git backup ticker if configured.
	// Exports issues to JSONL, scrubs ephemeral data, pushes to git repo.
	var jsonlGitBackupTicker *time.Ticker
	var jsonlGitBackupChan <-chan time.Time
	if d.isPatrolActive("jsonl_git_backup") {
		interval := jsonlGitBackupInterval(d.patrolConfig)
		jsonlGitBackupTicker = time.NewTicker(interval)
		jsonlGitBackupChan = jsonlGitBackupTicker.C
		defer jsonlGitBackupTicker.Stop()
		d.logger.Printf("JSONL git backup ticker started (interval %v)", interval)
	}

	// Start wisp reaper ticker if configured.
	// Closes stale wisps (abandoned molecule steps, old patrol data) across all databases.
	var wispReaperTicker *time.Ticker
	var wispReaperChan <-chan time.Time
	if d.isPatrolActive("wisp_reaper") {
		interval := wispReaperInterval(d.patrolConfig)
		wispReaperTicker = time.NewTicker(interval)
		wispReaperChan = wispReaperTicker.C
		defer wispReaperTicker.Stop()
		d.logger.Printf("Wisp reaper ticker started (interval %v)", interval)
	}

	// Start doctor dog ticker if configured.
	// Health monitor: TCP check, latency, DB count, gc, zombie detection, backup/disk checks.
	var doctorDogTicker *time.Ticker
	var doctorDogChan <-chan time.Time
	if d.isPatrolActive("doctor_dog") {
		interval := doctorDogInterval(d.patrolConfig)
		doctorDogTicker = time.NewTicker(interval)
		doctorDogChan = doctorDogTicker.C
		defer doctorDogTicker.Stop()
		d.logger.Printf("Doctor dog ticker started (interval %v)", interval)
	}

	// Start compactor dog ticker if configured.
	// Flattens Dolt commit history to reclaim graph storage (daily).
	var compactorDogTicker *time.Ticker
	var compactorDogChan <-chan time.Time
	if d.isPatrolActive("compactor_dog") {
		interval := compactorDogInterval(d.patrolConfig)
		compactorDogTicker = time.NewTicker(interval)
		compactorDogChan = compactorDogTicker.C
		defer compactorDogTicker.Stop()
		d.logger.Printf("Compactor dog ticker started (interval %v)", interval)
	}

	// Start checkpoint dog ticker if configured.
	// Auto-commits WIP changes in active polecat worktrees to prevent data loss.
	var checkpointDogTicker *time.Ticker
	var checkpointDogChan <-chan time.Time
	if d.isPatrolActive("checkpoint_dog") {
		interval := checkpointDogInterval(d.patrolConfig)
		checkpointDogTicker = time.NewTicker(interval)
		checkpointDogChan = checkpointDogTicker.C
		defer checkpointDogTicker.Stop()
		d.logger.Printf("Checkpoint dog ticker started (interval %v)", interval)
	}

	// Start scheduled maintenance ticker if configured.
	// Checks periodically whether we're in the maintenance window and
	// runs `gt maintain --force` when commit counts exceed threshold.
	var scheduledMaintenanceTicker *time.Ticker
	var scheduledMaintenanceChan <-chan time.Time
	if d.isPatrolActive("scheduled_maintenance") {
		interval := maintenanceCheckInterval(d.patrolConfig)
		scheduledMaintenanceTicker = time.NewTicker(interval)
		scheduledMaintenanceChan = scheduledMaintenanceTicker.C
		defer scheduledMaintenanceTicker.Stop()
		window := maintenanceWindow(d.patrolConfig)
		d.logger.Printf("Scheduled maintenance ticker started (check interval %v, window %s)", interval, window)
	}

	// Start main-branch test runner ticker if configured.
	// Periodically runs quality gates on each rig's main branch to catch regressions.
	var mainBranchTestTicker *time.Ticker
	var mainBranchTestChan <-chan time.Time
	if d.isPatrolActive("main_branch_test") {
		interval := mainBranchTestInterval(d.patrolConfig)
		mainBranchTestTicker = time.NewTicker(interval)
		mainBranchTestChan = mainBranchTestTicker.C
		defer mainBranchTestTicker.Stop()
		d.logger.Printf("Main branch test ticker started (interval %v)", interval)
	}

	// Start quota dog ticker if configured.
	// Scans for rate-limited sessions and automatically rotates credentials.
	var quotaDogTicker *time.Ticker
	var quotaDogChan <-chan time.Time
	if d.isPatrolActive("quota_dog") {
		interval := quotaDogInterval(d.patrolConfig)
		quotaDogTicker = time.NewTicker(interval)
		quotaDogChan = quotaDogTicker.C
		defer quotaDogTicker.Stop()
		d.logger.Printf("Quota dog ticker started (interval %v)", interval)
	}

	// Note: PATCH-010 uses per-session hooks in deacon/manager.go (SetAutoRespawnHook).
	// Global pane-died hooks don't fire reliably in tmux 3.2a, so we rely on the
	// per-session approach which has been tested to work for continuous recovery.

	// Initial heartbeat
	d.heartbeat(state)
	startupComplete = true

	for {
		select {
		case <-d.ctx.Done():
			d.logger.Println("Daemon context canceled, shutting down")
			return d.shutdown(state)

		case sig := <-sigChan:
			if isLifecycleSignal(sig) {
				// Lifecycle signal: immediate lifecycle processing (from gt handoff)
				d.logger.Println("Received lifecycle signal, processing lifecycle requests immediately")
				d.processLifecycleRequests()
			} else if isReloadRestartSignal(sig) {
				// Reload restart tracker from disk (from 'gt daemon clear-backoff')
				d.logger.Println("Received reload-restart signal, reloading restart tracker from disk")
				if d.restartTracker != nil {
					if err := d.restartTracker.Load(); err != nil {
						d.logger.Printf("Warning: failed to reload restart tracker: %v", err)
					}
				}
			} else {
				d.logger.Printf("Received signal %v, shutting down", sig)
				return d.shutdown(state)
			}

		case <-doltHealthChan:
			// Dedicated Dolt health check — fast crash detection independent
			// of the 3-minute general heartbeat.
			if !d.isShutdownInProgress() {
				d.ensureDoltServerRunning()
			}

		case <-doltRemotesChan:
			// Periodic Dolt remote push — pushes databases to their configured
			// git remotes on a 15-minute cadence (independent of heartbeat).
			if !d.isShutdownInProgress() {
				d.pushDoltRemotes()
			}

		case <-doltBackupChan:
			// Periodic Dolt filesystem backup — syncs production databases to
			// local backup directory on a 15-minute cadence.
			if !d.isShutdownInProgress() {
				d.syncDoltBackups()
			}

		case <-jsonlGitBackupChan:
			// Periodic JSONL git backup — exports issues, scrubs ephemeral data,
			// commits and pushes to git repo.
			if !d.isShutdownInProgress() {
				d.syncJsonlGitBackup()
			}

		case <-wispReaperChan:
			// Periodic wisp reaper — closes stale wisps (abandoned molecule steps,
			// old patrol data) to prevent unbounded table growth (Clown Show audit).
			if !d.isShutdownInProgress() {
				d.reapWisps()
			}

		case <-doctorDogChan:
			// Doctor dog — comprehensive Dolt health monitor: connectivity, latency,
			// gc, zombie detection, backup staleness, and disk usage checks.
			if !d.isShutdownInProgress() {
				d.runDoctorDog()
			}

		case <-compactorDogChan:
			// Compactor dog — flattens Dolt commit history on production databases.
			// Reclaims commit graph storage, then runs gc to reclaim chunks.
			if !d.isShutdownInProgress() {
				d.runCompactorDog()
			}

		case <-checkpointDogChan:
			// Checkpoint dog — auto-commits WIP changes in active polecat
			// worktrees to prevent data loss from session crashes.
			if !d.isShutdownInProgress() {
				d.runCheckpointDog()
			}

		case <-scheduledMaintenanceChan:
			// Scheduled maintenance — checks if we're in the maintenance window
			// and runs `gt maintain --force` when commit counts exceed threshold.
			if !d.isShutdownInProgress() {
				d.runScheduledMaintenance()
			}

		case <-mainBranchTestChan:
			// Main branch test runner — periodically runs quality gates on each
			// rig's main branch to catch regressions from merges or direct pushes.
			if !d.isShutdownInProgress() {
				d.runMainBranchTests()
			}

		case <-quotaDogChan:
			// Quota dog — scans for rate-limited sessions and automatically
			// rotates credentials to available accounts via keychain swap.
			if !d.isShutdownInProgress() {
				d.runQuotaDog()
			}

		case <-timer.C:
			d.heartbeat(state)

			// Fixed recovery interval (no activity-based backoff)
			timer.Reset(d.recoveryHeartbeatInterval())
		}
	}
}


// rotateOversizedLogs, ensureDoltServerRunning, pourDoctorMolecule, and
// checkAllRigsDolt live in dolt_health.go.

// readBeadsBackend, beads-store compatibility helpers, and openBeadsStores
// live in beads_store.go.


// openBeadsStores lives in beads_store.go.

// getKnownRigs, invalidateKnownRigsCache, readKnownRigsFromDisk,
// getPatrolRigs, and isRigOperational live in rigs.go.

// checkPolecatSessionHealth proactively validates polecat tmux sessions.
// This detects crashed polecats that:
// 1. Have work-on-hook (assigned work)
// 2. Report state=running/working in their agent bead
// 3. But the tmux session is actually dead
//
// When a crash is detected, the polecat is automatically restarted.
// This provides faster recovery than waiting for GUPP timeout or Witness detection.
func (d *Daemon) checkPolecatSessionHealth() {
	d.rigPool.runPerRig(d.ctx, d.getKnownRigs(), func(ctx context.Context, rigName string) error {
		d.checkRigPolecatHealth(rigName)
		return nil
	})
}

// checkRigPolecatHealth checks polecat session health for a specific rig.
func (d *Daemon) checkRigPolecatHealth(rigName string) {
	// Get polecat directories for this rig
	polecatsDir := filepath.Join(d.config.TownRoot, rigName, "polecats")
	polecats, err := listPolecatWorktrees(polecatsDir)
	if err != nil {
		return // No polecats directory - rig might not have polecats
	}

	for _, polecatName := range polecats {
		d.checkPolecatHealth(rigName, polecatName)
	}
}

func listPolecatWorktrees(polecatsDir string) ([]string, error) {
	entries, err := os.ReadDir(polecatsDir)
	if err != nil {
		return nil, err
	}

	polecats := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		polecats = append(polecats, name)
	}

	return polecats, nil
}

// checkPolecatHealth checks a single polecat's session health.
// If the polecat has work-on-hook but the tmux session is dead, it's restarted.
func (d *Daemon) checkPolecatHealth(rigName, polecatName string) {
	// Build the expected tmux session name
	sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)

	// Check if tmux session exists
	sessionAlive, err := d.tmux.HasSession(sessionName)
	if err != nil {
		d.logger.Printf("Error checking session %s: %v", sessionName, err)
		return
	}

	if sessionAlive {
		// Session is alive - nothing to do
		return
	}

	// Session is dead. Check if the polecat has work-on-hook.
	prefix := beads.GetPrefixForRig(d.config.TownRoot, rigName)
	agentBeadID := beads.PolecatBeadIDWithPrefix(prefix, rigName, polecatName)
	info, err := d.getAgentBeadInfo(agentBeadID)
	if err != nil {
		// Agent bead doesn't exist or error - polecat might not be registered
		return
	}

	// Check if polecat has hooked work
	if info.HookBead == "" {
		// No hooked work - this polecat is orphaned (should have self-nuked).
		// Self-cleaning model: polecats nuke themselves on completion.
		// An orphan with a dead session doesn't need restart - it needs cleanup.
		// Let the Witness handle orphan detection/cleanup during patrol.
		return
	}

	// Terminal state guard: skip polecats in intentional shutdown states.
	// agent_state='done' means normal completion; agent_state='nuked' means forced shutdown.
	// Their sessions being dead is expected, not a crash. Without this check,
	// the dead session + open hook_bead combination can fire false CRASHED_POLECAT
	// alerts during the race window before the hook_bead is closed.
	// This check is pure in-memory (info.State is already populated), so it runs before
	// the more expensive isBeadClosed subprocess call.
	agentState := beads.AgentState(info.State)
	if agentState == beads.AgentStateDone || agentState == beads.AgentStateNuked {
		d.logger.Printf("Skipping crash detection for %s/%s: agent_state=%s (intentional shutdown, not a crash)",
			rigName, polecatName, info.State)
		return
	}

	// Stale hook guard: skip polecats whose hook_bead is already closed.
	// When a polecat completes work normally (gt done), the hook_bead gets closed
	// but may not be cleared from the agent bead before the session stops.
	// Without this check, every heartbeat cycle fires a false CRASHED_POLECAT alert
	// for the dead session + non-empty hook_bead combination.
	if d.isBeadClosed(info.HookBead) {
		d.logger.Printf("Skipping crash detection for %s/%s: hook_bead %s is already closed (work completed normally)",
			rigName, polecatName, info.HookBead)
		return
	}

	// Spawning guard: skip polecats being actively started by gt sling.
	// agent_state='spawning' means the polecat bead was created (with hook_bead
	// set atomically) but the tmux session hasn't been launched yet. Restarting
	// here would create a second Claude process alongside the one gt sling is
	// about to start, causing the double-spawn bug (issue #1752).
	//
	// Time-bound: only skip if the bead was updated recently (within 5 minutes).
	// If gt sling crashed during spawn, the polecat would be stuck in 'spawning'
	// indefinitely. The Witness patrol also catches spawning-as-zombie, but a
	// time-bound here makes the daemon self-sufficient for this edge case.
	if beads.AgentState(info.State) == beads.AgentStateSpawning {
		if updatedAt, err := time.Parse(time.RFC3339, info.LastUpdate); err == nil {
			if time.Since(updatedAt) < 5*time.Minute {
				d.logger.Printf("Skipping restart for %s/%s: agent_state=spawning (gt sling in progress, updated %s ago)",
					rigName, polecatName, time.Since(updatedAt).Round(time.Second))
				return
			}
			d.logger.Printf("Spawning guard expired for %s/%s: agent_state=spawning but last updated %s ago (>5m), proceeding with crash detection",
				rigName, polecatName, time.Since(updatedAt).Round(time.Second))
		} else {
			// Can't parse timestamp — be safe, skip restart during spawning
			d.logger.Printf("Skipping restart for %s/%s: agent_state=spawning (gt sling in progress, unparseable updated_at)",
				rigName, polecatName)
			return
		}
	}

	// TOCTOU guard: re-verify session is still dead before restarting.
	// Between the initial check and now, the session may have been restarted
	// by another heartbeat cycle, witness, or the polecat itself.
	sessionRevived, err := d.tmux.HasSession(sessionName)
	if err == nil && sessionRevived {
		return // Session came back - no restart needed
	}

	// Polecat has work but session is dead - this is a crash!
	d.logger.Printf("CRASH DETECTED: polecat %s/%s has hook_bead=%s but session %s is dead",
		rigName, polecatName, info.HookBead, sessionName)

	// Track this death for mass death detection
	d.recordSessionDeath(sessionName)

	// Emit session_death event for audit trail / feed visibility
	_ = events.LogFeed(events.TypeSessionDeath, sessionName,
		events.SessionDeathPayload(sessionName, rigName+"/polecats/"+polecatName, "crash detected by daemon health check", "daemon"))

	// Notify witness — stuck-agent-dog plugin handles context-aware restart
	d.notifyWitnessOfCrashedPolecat(rigName, polecatName, info.HookBead)
}

// recordSessionDeath records a session death and checks for mass death pattern.
func (d *Daemon) recordSessionDeath(sessionName string) {
	d.deathsMu.Lock()
	defer d.deathsMu.Unlock()

	now := time.Now()

	// Add this death
	d.recentDeaths = append(d.recentDeaths, sessionDeath{
		sessionName: sessionName,
		timestamp:   now,
	})

	// Prune deaths outside the window
	cutoff := now.Add(-massDeathWindow)
	var recent []sessionDeath
	for _, death := range d.recentDeaths {
		if death.timestamp.After(cutoff) {
			recent = append(recent, death)
		}
	}
	d.recentDeaths = recent

	// Check for mass death
	if len(d.recentDeaths) >= massDeathThreshold {
		d.emitMassDeathEvent()
	}
}

// emitMassDeathEvent logs a mass death event when multiple sessions die in a short window.
func (d *Daemon) emitMassDeathEvent() {
	// Collect session names
	var sessions []string
	for _, death := range d.recentDeaths {
		sessions = append(sessions, death.sessionName)
	}

	count := len(sessions)
	window := massDeathWindow.String()

	d.logger.Printf("MASS DEATH DETECTED: %d sessions died in %s: %v", count, window, sessions)

	// Emit feed event
	_ = events.LogFeed(events.TypeMassDeath, "daemon",
		events.MassDeathPayload(count, window, sessions, ""))

	// Clear the deaths to avoid repeated alerts
	d.recentDeaths = nil
}

// isBeadClosed checks if a bead's status is "closed" by querying bd show --json.
// Returns true if the bead exists and has status "closed", false otherwise.
// On any error (bead not found, bd failure), returns false to err on the side
// of crash detection rather than silently suppressing alerts.
func (d *Daemon) isBeadClosed(beadID string) bool {
	cmd := exec.Command(d.bdPath, "show", beadID, "--json") //nolint:gosec // G204: args are constructed internally
	setSysProcAttr(cmd)
	cmd.Dir = d.config.TownRoot
	cmd.Env = os.Environ()

	output, err := cmd.Output()
	if err != nil {
		return false
	}

	var issues []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(output, &issues); err != nil || len(issues) == 0 {
		return false
	}

	return issues[0].Status == "closed"
}

// hasAssignedOpenWork checks if any work bead is assigned to the given polecat
// with a non-terminal status (hooked, in_progress, or open). This is the
// authoritative source of polecat work — the sling code sets status=hooked +
// assignee on the work bead, but no longer maintains the agent bead's hook_bead
// field (updateAgentHookBead is a no-op). Without this fallback, the idle reaper
// kills working polecats whose agent bead hook_bead is stale.
func (d *Daemon) hasAssignedOpenWork(rigName, assignee string) bool {
	for _, status := range []string{"hooked", "in_progress", "open"} {
		cmd := exec.Command(d.bdPath, "list", "--rig="+rigName, "--assignee="+assignee, "--status="+status, "--json") //nolint:gosec // G204: args are constructed internally
		cmd.Dir = d.config.TownRoot
		cmd.Env = os.Environ()
		output, err := cmd.Output()
		if err != nil {
			continue
		}
		var issues []json.RawMessage
		if json.Unmarshal(output, &issues) == nil && len(issues) > 0 {
			return true
		}
	}
	return false
}

// notifyWitnessOfCrashedPolecat notifies the witness when a polecat crash is detected.
// The stuck-agent-dog plugin handles context-aware restart decisions.
func (d *Daemon) notifyWitnessOfCrashedPolecat(rigName, polecatName, hookBead string) {
	witnessAddr := rigName + "/witness"
	subject := fmt.Sprintf("CRASHED_POLECAT: %s/%s detected", rigName, polecatName)
	body := fmt.Sprintf(`Polecat %s crash detected (session dead, work on hook).

hook_bead: %s

Restart deferred to stuck-agent-dog plugin for context-aware recovery.`,
		polecatName, hookBead)

	cmd := exec.Command(d.gtPath, "mail", "send", witnessAddr, "-s", subject, "-m", body) //nolint:gosec // G204: args are constructed internally
	setSysProcAttr(cmd)
	cmd.Dir = d.config.TownRoot
	cmd.Env = append(os.Environ(), "BD_ACTOR=daemon")// Identify as daemon, not overseer
	if err := cmd.Run(); err != nil {
		d.logger.Printf("Warning: failed to notify witness of crashed polecat: %v", err)
	}
}

// reapDeadPolecatWisps resets in_progress/hooked beads assigned to polecats
// whose tmux sessions have been dead (with a stale heartbeat) for longer than
// the configured timeout. This complements checkPolecatSessionHealth, which
// detects crashes and notifies the witness but does NOT reset the stuck beads.
//
// Why this exists: when a polecat hard-crashes (OOM, tmux kill, machine reboot),
// its Stop hook never fires, so the bead it was working on stays in_progress
// forever. That triggers gt doctor patrol-not-stuck warnings on every run and
// requires manual `bd update --status=open` or `gt sling` to recover. The
// stuck-agent-dog plugin was supposed to handle this but its context-aware
// restart path does not reset beads. See gu-1x0j.
//
// The reap is conservative by design:
//   - Only runs for rigs with a polecats/ directory.
//   - Skips polecats whose directory is already gone (DetectOrphanedBeads
//     handles that case from the witness side).
//   - Requires BOTH a dead tmux session AND a stale heartbeat file — either
//     alone is insufficient evidence of a permanent crash (sessions briefly
//     disappear during rebuilds; heartbeats can go stale during long-running
//     commands).
//   - Requires the heartbeat file to actually exist. No heartbeat means we
//     can't prove liveness, so we defer to the witness orphan scan.
//   - Reverifies the session and heartbeat staleness immediately before reset
//     to narrow the TOCTOU window.
func (d *Daemon) reapDeadPolecatWisps() {
	opCfg := d.loadOperationalConfig().GetDaemonConfig()
	timeout := opCfg.DeadPolecatReapTimeoutD()
	if timeout <= 0 {
		// Explicitly disabled via config — treat <=0 as "off" to preserve the
		// escape hatch for operators who want to rely solely on DetectOrphanedBeads.
		return
	}

	d.rigPool.runPerRig(d.ctx, d.getKnownRigs(), func(ctx context.Context, rigName string) error {
		d.reapRigDeadPolecatWisps(rigName, timeout)
		return nil
	})
}

// reapRigDeadPolecatWisps scans a single rig for in_progress/hooked beads
// assigned to dead polecats and resets them.
func (d *Daemon) reapRigDeadPolecatWisps(rigName string, timeout time.Duration) {
	polecatsDir := filepath.Join(d.config.TownRoot, rigName, "polecats")
	if _, err := os.Stat(polecatsDir); err != nil {
		return // Rig has no polecats — nothing to reap.
	}

	polecatPrefix := rigName + "/polecats/"

	type beadInfo struct {
		ID       string `json:"id"`
		Assignee string `json:"assignee"`
		Status   string `json:"status"`
	}

	// List candidate beads in both hooked and in_progress states. The sling
	// flow leaves slung work as hooked; polecats flip to in_progress on claim.
	var candidates []beadInfo
	for _, status := range []string{"hooked", "in_progress"} {
		cmd := exec.Command(d.bdPath, "list", "--rig="+rigName, "--status="+status, "--json", "--limit=0") //nolint:gosec // G204: args are constructed internally
		setSysProcAttr(cmd)
		cmd.Dir = d.config.TownRoot
		cmd.Env = os.Environ()
		output, err := cmd.Output()
		if err != nil {
			d.logger.Printf("reap-dead-polecat-wisps: list %s for %s failed: %v", status, rigName, err)
			continue
		}
		if len(output) == 0 {
			continue
		}
		var batch []beadInfo
		if err := json.Unmarshal(output, &batch); err != nil {
			d.logger.Printf("reap-dead-polecat-wisps: parse %s for %s failed: %v", status, rigName, err)
			continue
		}
		for i := range batch {
			batch[i].Status = status
		}
		candidates = append(candidates, batch...)
	}

	if len(candidates) == 0 {
		return
	}

	for _, bead := range candidates {
		if bead.Assignee == "" || !strings.HasPrefix(bead.Assignee, polecatPrefix) {
			continue // Not assigned to a polecat in this rig.
		}
		polecatName := strings.TrimPrefix(bead.Assignee, polecatPrefix)
		if polecatName == "" || strings.Contains(polecatName, "/") {
			continue // Malformed assignee (nested path, etc.) — skip defensively.
		}

		d.maybeReapDeadPolecatBead(rigName, polecatName, bead.ID, bead.Status, polecatsDir, timeout)
	}
}

// maybeReapDeadPolecatBead resets a single bead if the owning polecat is
// provably dead (session gone + heartbeat stale) and the polecat directory
// still exists (so this is a crashed session, not a deleted polecat).
func (d *Daemon) maybeReapDeadPolecatBead(rigName, polecatName, beadID, status, polecatsDir string, timeout time.Duration) {
	polecatDir := filepath.Join(polecatsDir, polecatName)
	if _, err := os.Stat(polecatDir); err != nil {
		// Directory is gone — DetectOrphanedBeads (witness) handles this case
		// and knows how to distinguish truly orphaned beads from rename races.
		return
	}

	sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)

	alive, err := d.tmux.HasSession(sessionName)
	if err != nil {
		// Transient tmux error — err on the side of not reaping.
		return
	}
	if alive {
		return
	}

	// Heartbeat must exist AND be stale by at least `timeout`. Missing heartbeat
	// is not proof of death: it might just mean the polecat never touched one
	// (e.g. fresh install before heartbeat rollout), in which case we defer to
	// the witness orphan scanner instead of guessing.
	hb := polecat.ReadSessionHeartbeat(d.config.TownRoot, sessionName)
	if hb == nil {
		return
	}
	staleFor := time.Since(hb.Timestamp)
	if staleFor < timeout {
		return
	}

	// TOCTOU guard: re-check session + heartbeat immediately before reset.
	// A polecat could have been respawned between the initial checks and here.
	if alive2, err := d.tmux.HasSession(sessionName); err != nil || alive2 {
		return
	}
	hb2 := polecat.ReadSessionHeartbeat(d.config.TownRoot, sessionName)
	if hb2 == nil || time.Since(hb2.Timestamp) < timeout {
		return
	}

	// Reset bead to open with cleared assignee so the scheduler/sling flow
	// can re-dispatch it. We use bd update directly rather than routing
	// through the witness RECOVERED_BEAD pathway because:
	//   - The witness spawn-count ledger is intended for same-polecat respawn
	//     loops; a crashed polecat + fresh dispatch is a different failure mode.
	//   - The daemon already has authority to run bd update (see updateAgentHookBead).
	//   - Keeping the reset local avoids extra mail traffic and permanent Dolt
	//     commits on every heartbeat cycle.
	cmd := exec.Command(d.bdPath, "update", beadID, "--rig="+rigName, "--status=open", "--assignee=") //nolint:gosec // G204: args are constructed internally
	setSysProcAttr(cmd)
	cmd.Dir = d.config.TownRoot
	cmd.Env = append(os.Environ(), "BD_ACTOR=daemon")
	if output, err := cmd.CombinedOutput(); err != nil {
		d.logger.Printf("reap-dead-polecat-wisps: failed to reset %s (rig=%s polecat=%s): %v: %s",
			beadID, rigName, polecatName, err, strings.TrimSpace(string(output)))
		return
	}

	d.logger.Printf("reap-dead-polecat-wisps: reset %s (rig=%s polecat=%s prev_status=%s session=%s heartbeat_stale=%v threshold=%v)",
		beadID, rigName, polecatName, status, sessionName, staleFor.Truncate(time.Second), timeout)

	// Emit a session-death event so the activity feed and audit log capture the reap.
	_ = events.LogFeed(events.TypeSessionDeath, fmt.Sprintf("%s/%s", rigName, polecatName),
		events.SessionDeathPayload(sessionName, fmt.Sprintf("%s/polecats/%s", rigName, polecatName),
			fmt.Sprintf("dead-polecat-wisp-reap: bead=%s prev_status=%s heartbeat_stale=%v (threshold=%v)",
				beadID, status, staleFor.Truncate(time.Second), timeout),
			"daemon"))
}

// reapIdlePolecats kills polecat tmux sessions that have been idle too long.
// The persistent polecat model (gt-4ac) keeps sessions alive after gt done for reuse,
// but idle sessions consume API slots (Claude Code process stays alive at 0% CPU).
// This reaper checks heartbeat state and kills sessions idle longer than the threshold.
func (d *Daemon) reapIdlePolecats() {
	opCfg := d.loadOperationalConfig().GetDaemonConfig()
	idleTimeout := opCfg.PolecatIdleSessionTimeoutD()

	d.rigPool.runPerRig(d.ctx, d.getKnownRigs(), func(ctx context.Context, rigName string) error {
		d.reapRigIdlePolecats(rigName, idleTimeout)
		return nil
	})
}

// reapRigIdlePolecats checks all polecats in a rig and kills idle sessions.
func (d *Daemon) reapRigIdlePolecats(rigName string, timeout time.Duration) {
	polecatsDir := filepath.Join(d.config.TownRoot, rigName, "polecats")
	polecats, err := listPolecatWorktrees(polecatsDir)
	if err != nil {
		return // No polecats directory
	}

	for _, polecatName := range polecats {
		d.reapIdlePolecat(rigName, polecatName, timeout)
	}
}

// reapIdlePolecat checks a single polecat and kills it if idle too long.
// A polecat is considered idle if:
//   - Heartbeat state is "exiting" or "idle" and timestamp exceeds threshold, OR
//   - Heartbeat state is "working" but timestamp is stale AND the polecat has no
//     hooked work (agent_state=idle in beads). This catches polecats that completed
//     gt done — persistentPreRun resets heartbeat to "working" on every gt sub-command,
//     so after gt done finishes the heartbeat shows "working" with a stale timestamp.
func (d *Daemon) reapIdlePolecat(rigName, polecatName string, timeout time.Duration) {
	sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)

	// Only check sessions that are actually alive
	alive, err := d.tmux.HasSession(sessionName)
	if err != nil || !alive {
		return
	}

	// Read heartbeat to check state and idle duration
	hb := polecat.ReadSessionHeartbeat(d.config.TownRoot, sessionName)
	if hb == nil {
		return // No heartbeat file — can't determine state
	}

	staleDuration := time.Since(hb.Timestamp)
	if staleDuration < timeout {
		return // Heartbeat is fresh — polecat is active
	}

	state := hb.EffectiveState()

	// Explicitly idle or exiting — safe to reap
	if state == polecat.HeartbeatIdle || state == polecat.HeartbeatExiting {
		d.killIdlePolecat(rigName, polecatName, sessionName, staleDuration, timeout, string(state))
		return
	}

	// Heartbeat says "working" but is stale — check if polecat actually has hooked work.
	// If agent_state=idle in beads and no hook_bead, the polecat finished gt done
	// and is sitting idle (heartbeat wasn't updated to "idle" because persistentPreRun
	// resets to "working" on every gt sub-command during gt done).
	if state == polecat.HeartbeatWorking {
		prefix := beads.GetPrefixForRig(d.config.TownRoot, rigName)
		agentBeadID := beads.PolecatBeadIDWithPrefix(prefix, rigName, polecatName)
		info, err := d.getAgentBeadInfo(agentBeadID)
		if err != nil {
			// Agent bead lookup failed — polecat has no provable work.
			// If heartbeat is stale enough (2x timeout), reap anyway to prevent
			// indefinite API burn when bead infrastructure is degraded.
			// But first check if the agent is actually running (GH#3342).
			if staleDuration >= timeout*2 && !d.tmux.IsAgentRunning(sessionName) {
				d.killIdlePolecat(rigName, polecatName, sessionName, staleDuration, timeout, "working-bead-lookup-failed")
			}
			return
		}

		// If polecat has hooked work that is still open, it might be stuck (not idle).
		// Don't reap — let checkPolecatSessionHealth handle stuck polecats.
		// But if the hook_bead is closed, the work is done and this is just an idle
		// polecat with a stale hook reference — safe to reap.
		if info.HookBead != "" && !d.isBeadClosed(info.HookBead) {
			return
		}

		// Fallback: agent bead hook_bead may be stale (updateAgentHookBead is a
		// no-op since the sling code declared work bead assignee as authoritative).
		// Before killing, check if any work bead is assigned to this polecat with
		// a non-terminal status. This prevents the reaper from killing polecats
		// whose agent bead hook_bead points to a closed bead from a previous swarm
		// while the polecat is actively working on a newly-slung bead.
		assignee := fmt.Sprintf("%s/polecats/%s", rigName, polecatName)
		if d.hasAssignedOpenWork(rigName, assignee) {
			return
		}

		// No hooked work + stale heartbeat — but check if the agent process
		// is still actively running before reaping. A failed gt sling rollback
		// can clear the hook while the agent is still working (GH#3342).
		if d.tmux.IsAgentRunning(sessionName) {
			return
		}
		d.killIdlePolecat(rigName, polecatName, sessionName, staleDuration, timeout, "working-no-hook")
	}
}

// killIdlePolecat terminates an idle polecat session and cleans up.
func (d *Daemon) killIdlePolecat(rigName, polecatName, sessionName string, idleDuration, timeout time.Duration, reason string) {
	d.logger.Printf("Reaping idle polecat %s/%s (state=%s, idle %v, threshold %v)",
		rigName, polecatName, reason, idleDuration.Truncate(time.Second), timeout)

	// Kill the tmux session (and all descendant processes)
	if err := d.tmux.KillSessionWithProcesses(sessionName); err != nil {
		d.logger.Printf("Warning: failed to kill idle polecat session %s: %v", sessionName, err)
		return
	}

	// Clean up heartbeat file
	polecat.RemoveSessionHeartbeat(d.config.TownRoot, sessionName)

	d.logger.Printf("Reaped idle polecat %s/%s — session killed, API slot freed", rigName, polecatName)

	// Emit feed event so the activity feed shows the reap
	_ = events.LogFeed(events.TypeSessionDeath, fmt.Sprintf("%s/%s", rigName, polecatName),
		events.SessionDeathPayload(sessionName, fmt.Sprintf("%s/polecats/%s", rigName, polecatName),
			fmt.Sprintf("idle-reap: %s, idle %v (threshold %v)", reason, idleDuration.Truncate(time.Second), timeout),
			"daemon"))
}

// cleanupOrphanedProcesses kills orphaned claude subagent processes.
// These are Task tool subagents that didn't clean up after completion.
// Detection uses TTY column: processes with TTY "?" have no controlling terminal.
// This is a safety net fallback - Deacon patrol also runs this more frequently.
func (d *Daemon) cleanupOrphanedProcesses() {
	results, err := util.CleanupOrphanedClaudeProcesses()
	if err != nil {
		d.logger.Printf("Warning: orphan process cleanup failed: %v", err)
		return
	}

	if len(results) > 0 {
		d.logger.Printf("Orphan cleanup: processed %d process(es)", len(results))
		for _, r := range results {
			if r.Signal == "UNKILLABLE" {
				d.logger.Printf("  WARNING: PID %d (%s) survived SIGKILL", r.Process.PID, r.Process.Cmd)
			} else {
				d.logger.Printf("  Sent %s to PID %d (%s)", r.Signal, r.Process.PID, r.Process.Cmd)
			}
		}
	}
}

// pruneStaleBranches removes stale local polecat tracking branches from all rig clones.
// This runs in every heartbeat but is very fast when there are no stale branches.
func (d *Daemon) pruneStaleBranches() {
	// pruneInDir prunes stale polecat branches in a single git directory.
	pruneInDir := func(dir, label string) {
		g := gitpkg.NewGit(dir)
		if !g.IsRepo() {
			return
		}

		// Fetch --prune first to clean up stale remote tracking refs
		_ = g.FetchPrune("origin")

		pruned, err := g.PruneStaleBranches("polecat/*", false)
		if err != nil {
			d.logger.Printf("Warning: branch prune failed for %s: %v", label, err)
			return
		}

		if len(pruned) > 0 {
			d.logger.Printf("Branch prune: removed %d stale polecat branch(es) in %s", len(pruned), label)
			for _, b := range pruned {
				d.logger.Printf("  %s (%s)", b.Name, b.Reason)
			}
		}
	}

	// Prune in each rig's git directory (parallel — each rig is independent).
	d.rigPool.runPerRig(d.ctx, d.getKnownRigs(), func(ctx context.Context, rigName string) error {
		rigPath := filepath.Join(d.config.TownRoot, rigName)
		pruneInDir(rigPath, rigName)
		return nil
	})

	// Also prune in the town root itself (mayor clone)
	pruneInDir(d.config.TownRoot, "town-root")
}

// dispatchQueuedWork shells out to `gt scheduler run` to dispatch scheduled beads.
// This avoids circular import between the daemon and cmd packages.
// Uses a 5m timeout to allow multi-bead dispatch with formula cooking and hook retries.
//
// Timeout safety: if the timeout fires mid-dispatch, a bead may be left with
// metadata written but label not yet swapped (or vice versa). The dispatch flock
// is released on process death, and dispatchSingleBead's label swap retry logic
// prevents double-dispatch on the next cycle. The batch_size config (default: 1)
// limits how many beads are in-flight per heartbeat, reducing the timeout window.
func (d *Daemon) dispatchQueuedWork() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gt", "scheduler", "run")
	setSysProcAttr(cmd)
	cmd.Dir = d.config.TownRoot
	cmd.Env = append(os.Environ(), "GT_DAEMON=1", "BD_DOLT_AUTO_COMMIT=off")
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		d.logger.Printf("Scheduler dispatch timed out after 5m")
	} else if err != nil {
		d.logger.Printf("Scheduler dispatch failed: %v (output: %s)", err, string(out))
	} else if len(out) > 0 {
		d.logger.Printf("Scheduler dispatch: %s", string(out))
	}
}

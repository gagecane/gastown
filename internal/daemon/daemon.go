package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/gofrs/flock"
	beadsdk "github.com/steveyegge/beads"
	agentconfig "github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/feed"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/tmux"
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

// sessionDeath and the mass-death constants live in polecat_health.go.

// doctorMolCooldown is the minimum interval between mol-dog-doctor molecules.
// Configurable via operational.daemon.doctor_mol_cooldown. Lives here because
// the Daemon struct's lastDoctorMolTime field is in daemon.go.
const (
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

// This file used to hold every Daemon method. After the gu-80i refactor it
// keeps only the struct definition, constants, New(), and Run(). Methods
// moved out as follows:
//
//   agents.go          Boot/Deacon/Witness/Refinery/Mayor ensure* / kill*
//   beads_store.go     beads-store compatibility + open/close
//   dolt_health.go     Dolt server health + startup validation + log rotation
//   heartbeat.go       heartbeat loop + interval accessor
//   maintenance.go     orphan process cleanup, branch prune, scheduler dispatch
//   polecat_health.go  polecat crash detection + mass-death + witness notify
//   reaper.go          dead-polecat wisp reaper + idle-polecat reaper
//   rigs.go            rig registry cache + operational-state query
//   shutdown.go        graceful shutdown + IsRunning/Stop*/*OrphanedDaemons


package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/dog"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/plugin"
	"github.com/steveyegge/gastown/internal/tmux"
)

// Dog lifecycle defaults — now config-driven via operational.daemon thresholds.
// These vars are still used as fallbacks and for tests; production code
// should prefer d.daemonCfg() accessors loaded from TownSettings.
var (
	// dogIdleSessionTimeout is how long a dog can be idle with a live tmux
	// session before the session is killed (default 1h).
	// Configurable via operational.daemon.dog_idle_session_timeout.
	//nolint:unused // default-value fallback asserted by handler_test.go (lint runs tests:false)
	dogIdleSessionTimeout = config.DefaultDogIdleSessionTimeout

	// dogIdleRemoveTimeout is how long a dog can be idle before it is removed
	// from the kennel entirely (only when pool is oversized, default 4h).
	// Configurable via operational.daemon.dog_idle_remove_timeout.
	//nolint:unused // default-value fallback asserted by handler_test.go (lint runs tests:false)
	dogIdleRemoveTimeout = config.DefaultDogIdleRemoveTimeout

	// staleWorkingTimeout is how long a dog can be in state=working with no
	// activity updates before it is considered stuck (default 2h).
	// Configurable via operational.daemon.stale_working_timeout.
	//nolint:unused // default-value fallback asserted by handler_test.go (lint runs tests:false)
	staleWorkingTimeout = config.DefaultStaleWorkingTimeout

	// maxDogPoolSize is the target pool size (default 4).
	// Configurable via operational.daemon.max_dog_pool_size.
	//nolint:unused // default-value fallback asserted by handler_test.go (lint runs tests:false)
	maxDogPoolSize = config.DefaultMaxDogPoolSize
)

// handleDogs manages Dog lifecycle: cleanup stuck dogs, reap idle dogs, then dispatch plugins.
// This is the main entry point called from heartbeat.
func (d *Daemon) handleDogs() {
	rigsConfig, err := d.loadRigsConfig()
	if err != nil {
		d.logger.Printf("Handler: failed to load rigs config: %v", err)
		return
	}

	opCfg := d.loadOperationalConfig().GetDaemonConfig()

	mgr := dog.NewManager(d.config.TownRoot, rigsConfig)
	t := tmux.NewTmux()
	sm := dog.NewSessionManager(t, d.config.TownRoot, mgr)

	// Process DOG_DONE mail first so dogs that finished their work are idle
	// before downstream cleanup decides whether to kill their sessions or
	// flag them as stale. See gu-7537.
	d.processDogDoneMessages(mgr)
	d.cleanupStuckDogs(mgr, sm)
	d.detectStaleWorkingDogs(mgr, sm, opCfg)
	d.reapIdleDogs(mgr, sm, opCfg)
	d.dispatchPlugins(mgr, sm, rigsConfig)
}

// handleDogsCleanupOnly runs dog lifecycle cleanup (stuck, stale, idle) without
// dispatching new work. Used when pressure checks block new spawns.
func (d *Daemon) handleDogsCleanupOnly() {
	rigsConfig, err := d.loadRigsConfig()
	if err != nil {
		d.logger.Printf("Handler: failed to load rigs config: %v", err)
		return
	}

	opCfg := d.loadOperationalConfig().GetDaemonConfig()

	mgr := dog.NewManager(d.config.TownRoot, rigsConfig)
	t := tmux.NewTmux()
	sm := dog.NewSessionManager(t, d.config.TownRoot, mgr)

	// Process DOG_DONE mail first (see handleDogs for rationale).
	d.processDogDoneMessages(mgr)
	d.cleanupStuckDogs(mgr, sm)
	d.detectStaleWorkingDogs(mgr, sm, opCfg)
	d.reapIdleDogs(mgr, sm, opCfg)
	// Skip dispatchPlugins — under pressure
}

// cleanupStuckDogs finds dogs in state=working whose tmux session is dead and
// clears their work so they return to idle.
func (d *Daemon) cleanupStuckDogs(mgr *dog.Manager, sm *dog.SessionManager) {
	dogs, err := mgr.List()
	if err != nil {
		d.logger.Printf("Handler: failed to list dogs: %v", err)
		return
	}

	for _, dg := range dogs {
		if dg.State != dog.StateWorking {
			continue
		}

		running, err := sm.IsRunning(dg.Name)
		if err != nil {
			d.logger.Printf("Handler: error checking session for dog %s: %v", dg.Name, err)
			continue
		}

		if running {
			continue
		}

		// Dog is marked working but session is dead — clean it up.
		d.logger.Printf("Handler: dog %s is working but session is dead, clearing work", dg.Name)
		if err := mgr.ClearWork(dg.Name); err != nil {
			d.logger.Printf("Handler: failed to clear work for dog %s: %v", dg.Name, err)
		}
	}
}

// detectStaleWorkingDogs finds dogs in state=working whose last_active exceeds
// staleWorkingTimeout. These dogs have live tmux sessions sitting idle at a
// prompt — neither cleanupStuckDogs (needs dead session) nor reapIdleDogs
// (needs state=idle) will catch them.
func (d *Daemon) detectStaleWorkingDogs(mgr *dog.Manager, sm *dog.SessionManager, daemonCfg *config.DaemonThresholds) {
	dogs, err := mgr.List()
	if err != nil {
		d.logger.Printf("Handler: failed to list dogs for stale-working check: %v", err)
		return
	}

	blanket := daemonCfg.StaleWorkingTimeoutD()
	// Per-plugin stuck thresholds: a dog holding a short-cadence critical plugin
	// slot (e.g. dolt-backup, 15m) must be reclaimed within a couple of intervals,
	// not after the multi-hour blanket timeout — otherwise one hung dog silently
	// halts that plugin's entire dispatch cadence (gu-9jmd3).
	pluginThresholds := d.pluginStuckThresholds(blanket)
	now := time.Now()
	for _, dg := range dogs {
		if dg.State != dog.StateWorking {
			continue
		}

		threshold := blanket
		if pluginName, ok := pluginWorkName(dg.Work); ok {
			if t, found := pluginThresholds[pluginName]; found {
				threshold = t
			}
		}

		staleDuration := now.Sub(dg.LastActive)
		if staleDuration < threshold {
			continue
		}

		d.logger.Printf("Handler: dog %s stuck in working state (inactive %v >= threshold %v, work: %s), clearing",
			dg.Name, staleDuration.Truncate(time.Minute), threshold, dg.Work)

		if err := mgr.ClearWork(dg.Name); err != nil {
			d.logger.Printf("Handler: failed to clear work for stale dog %s: %v", dg.Name, err)
			continue
		}

		// Kill the tmux session — it's not doing anything useful. Use
		// SessionExists (not IsRunning) so a zombie session whose agent
		// already died is still torn down rather than leaked (gs-49s).
		running, err := sm.SessionExists(dg.Name)
		if err != nil {
			d.logger.Printf("Handler: error checking session for stale dog %s: %v", dg.Name, err)
			continue
		}
		if running {
			if err := sm.Stop(dg.Name, true); err != nil {
				d.logger.Printf("Handler: failed to stop session for stale dog %s: %v", dg.Name, err)
			}
		}
	}
}

// pluginWorkName extracts the plugin name from a dog's work descriptor. Plugin
// work is assigned as "plugin:<name>" (optionally with a trailing suffix, e.g.
// the event-driven watcher's "plugin:<name> (event-driven, rig=<r>)"). Returns
// the bare plugin name and true when the descriptor is plugin work.
func pluginWorkName(work string) (string, bool) {
	const prefix = "plugin:"
	if !strings.HasPrefix(work, prefix) {
		return "", false
	}
	name := strings.TrimPrefix(work, prefix)
	if i := strings.IndexByte(name, ' '); i >= 0 {
		name = name[:i]
	}
	if name == "" {
		return "", false
	}
	return name, true
}

// pluginStuckThresholds maps each discovered plugin's name to the stuck-clear
// threshold the daemon should apply to a dog holding that plugin's slot. The
// blanket timeout is used as both the fallback and the upper clamp, so a missing
// or non-cooldown plugin keeps today's behavior and no plugin is ever given a
// LONGER leash than the daemon-wide default. See Plugin.StuckThreshold and
// gu-9jmd3. Discovery failures degrade gracefully to an empty map (blanket only).
func (d *Daemon) pluginStuckThresholds(blanket time.Duration) map[string]time.Duration {
	rigNames := d.rigNamesForPluginScan()
	scanner := plugin.NewScanner(d.config.TownRoot, rigNames)
	plugins, err := scanner.DiscoverAll()
	if err != nil {
		d.logger.Printf("Handler: failed to discover plugins for stuck thresholds: %v", err)
		return nil
	}
	thresholds := make(map[string]time.Duration, len(plugins))
	for _, p := range plugins {
		thresholds[p.Name] = p.StuckThreshold(blanket)
	}
	return thresholds
}

// reapIdleDogs kills tmux sessions for dogs that have been idle too long, and
// removes long-idle dogs from the kennel when the pool is oversized.
func (d *Daemon) reapIdleDogs(mgr *dog.Manager, sm *dog.SessionManager, daemonCfg *config.DaemonThresholds) {
	dogs, err := mgr.List()
	if err != nil {
		d.logger.Printf("Handler: failed to list dogs for reaping: %v", err)
		return
	}

	idleSessionTimeout := daemonCfg.DogIdleSessionTimeoutD()
	idleRemoveTimeout := daemonCfg.DogIdleRemoveTimeoutD()
	poolMax := daemonCfg.MaxDogPoolSizeV()

	now := time.Now()
	poolSize := len(dogs)

	for _, dg := range dogs {
		if dg.State != dog.StateIdle {
			continue
		}

		idleDuration := now.Sub(dg.LastActive)

		// Phase 1: kill stale tmux sessions for idle dogs. Use SessionExists
		// (not IsRunning) so a zombie session — tmux alive, agent dead — is
		// still reaped instead of leaking until orphan cleanup (gs-49s).
		if idleDuration >= idleSessionTimeout {
			running, err := sm.SessionExists(dg.Name)
			if err != nil {
				d.logger.Printf("Handler: error checking session for idle dog %s: %v", dg.Name, err)
				continue
			}
			if running {
				d.logger.Printf("Handler: reaping idle dog %s session (idle %v)", dg.Name, idleDuration.Truncate(time.Minute))
				if err := sm.Stop(dg.Name, true); err != nil {
					d.logger.Printf("Handler: failed to stop session for idle dog %s: %v", dg.Name, err)
				}
			}
		}

		// Phase 2: remove long-idle dogs when pool is oversized.
		if poolSize > poolMax && idleDuration >= idleRemoveTimeout {
			d.logger.Printf("Handler: removing long-idle dog %s from kennel (idle %v, pool %d/%d)",
				dg.Name, idleDuration.Truncate(time.Minute), poolSize, poolMax)

			// Ensure session is dead before removing. SessionExists (not
			// IsRunning) so a lingering zombie session is also torn down
			// before the dog is removed from the kennel (gs-49s).
			running, _ := sm.SessionExists(dg.Name)
			if running {
				_ = sm.Stop(dg.Name, true)
			}

			if err := mgr.Remove(dg.Name); err != nil {
				d.logger.Printf("Handler: failed to remove idle dog %s: %v", dg.Name, err)
				continue
			}
			poolSize--
		}
	}
}

// dispatchPlugins scans for plugins, evaluates cooldown gates, and dispatches
// eligible plugins to idle dogs.
func (d *Daemon) dispatchPlugins(mgr *dog.Manager, sm *dog.SessionManager, rigsConfig *config.RigsConfig) {
	// Get rig names for scanner
	var rigNames []string
	if rigsConfig != nil {
		for name := range rigsConfig.Rigs {
			rigNames = append(rigNames, name)
		}
	}

	scanner := plugin.NewScanner(d.config.TownRoot, rigNames)
	plugins, err := scanner.DiscoverAll()
	if err != nil {
		d.logger.Printf("Handler: failed to discover plugins: %v", err)
		return
	}

	if len(plugins) == 0 {
		return
	}

	recorder := plugin.NewRecorder(d.config.TownRoot)
	router := mail.NewRouterWithTownRoot(d.config.TownRoot, d.config.TownRoot)

	// Pre-pass (bounded-parallel): evaluate each plugin's cooldown/cron gate. The
	// gate check is a read-only `bd list` per plugin (one ~0.85s cold-start each),
	// and running it serially per plugin dominated handleDogs latency (~60s,
	// gu-10nch). Because the checks are side-effect-free we fan them out behind a
	// semaphore (same pattern as gu-el5bx/gu-1h3ur) so even under contention they
	// cannot storm the single shared Dolt server. Dispatch itself — dog claim,
	// mail, tmux start, record — stays strictly SERIAL below so two plugins can
	// never double-claim the same idle dog.
	eligible := d.filterDispatchablePlugins(plugins, recorder)

	for _, p := range eligible {
		// The trigger label recorded on the dispatch audit event, distinguishing
		// the gate path (cooldown vs cron). filterDispatchablePlugins only returns
		// plugins with a non-nil cooldown or cron gate, so p.Gate is safe here.
		trigger := string(p.Gate.Type)

		// Find an idle dog that doesn't already have a live tmux session.
		// A leaked session (dog marked idle before its tmux terminated) would
		// cause sm.Start to fail with "session already running", and since
		// mgr.List() returns dogs in directory order, GetIdleDog would always
		// pick the same first idle dog — infinite-looping the same failed
		// dispatch instead of advancing to the next idle dog in the pack.
		// See gt-o24.
		//
		// Also skip dogs in startup backoff (gu-ro75): when a dog's session
		// repeatedly dies during startup, the restart tracker mutes it for
		// an exponentially increasing window instead of hot-looping.
		idleDog := d.findDispatchableDog(mgr, sm)
		if idleDog == nil {
			d.logger.Printf("Handler: no dispatchable idle dogs available, deferring remaining plugins")
			return
		}

		// Assign work and start session.
		workDesc := fmt.Sprintf("plugin:%s", p.Name)
		if err := mgr.AssignWork(idleDog.Name, workDesc); err != nil {
			d.logger.Printf("Handler: failed to assign work to dog %s: %v", idleDog.Name, err)
			continue
		}

		// Purge ALL stale plugin mails before re-dispatch. A dog that crashed
		// before reading its mail retains old messages; re-dispatching alongside
		// them lets the dog execute stale (pre-edit) content, and read-but-open
		// mail from earlier plugins accumulates indefinitely because the mail
		// hook re-injects every open message each turn (gs-7yk). Purging only
		// the current subject left both leaks open. Best-effort.
		staleMailAddr := fmt.Sprintf("deacon/dogs/%s", idleDog.Name)
		if staleMBox, mboxErr := router.GetMailbox(staleMailAddr); mboxErr == nil {
			_, _ = staleMBox.ArchivePluginDispatchMail()
		}

		// Send mail with plugin instructions BEFORE starting the session
		// so the dog finds work in its inbox on first check.
		msg := mail.NewMessage(
			"daemon",
			fmt.Sprintf("deacon/dogs/%s", idleDog.Name),
			fmt.Sprintf("Plugin: %s", p.Name),
			p.FormatMailBody(),
		)
		msg.Type = mail.TypeTask
		msg.Timestamp = time.Now()
		if err := router.Send(msg); err != nil {
			d.logger.Printf("Handler: failed to send mail to dog %s: %v", idleDog.Name, err)
			// Roll back assignment — no point starting a session without instructions.
			if clearErr := mgr.ClearWork(idleDog.Name); clearErr != nil {
				d.logger.Printf("Handler: failed to clear work after mail failure for dog %s: %v", idleDog.Name, clearErr)
			}
			continue
		}

		// Emit daemon.plugin.dispatch audit event (additive — transport-split
		// foundation). Best-effort; errors are swallowed (events are audit-only
		// and must never fail a dispatch). See gu-zwui / gt-to45a and
		// docs/design/plugin-dispatch-transport.md.
		_ = events.LogAudit(
			events.TypeDaemonPluginDispatch,
			"daemon",
			events.DaemonPluginDispatchPayload(
				p.Name,
				p.RigName,
				fmt.Sprintf("deacon/dogs/%s", idleDog.Name),
				trigger,
			),
		)

		if err := sm.Start(idleDog.Name, dog.SessionStartOptions{
			WorkDesc: workDesc,
		}); err != nil {
			d.logger.Printf("Handler: failed to start session for dog %s: %v", idleDog.Name, err)
			// Record the failed startup so the next heartbeat applies
			// exponential backoff instead of immediately retrying. See gu-ro75.
			d.recordDogStartFailure(idleDog.Name)
			// Roll back assignment on session start failure.
			if clearErr := mgr.ClearWork(idleDog.Name); clearErr != nil {
				d.logger.Printf("Handler: failed to clear work after start failure for dog %s: %v", idleDog.Name, clearErr)
			}
			continue
		}
		// Successful start — let the tracker reset backoff once the dog has
		// been stable for StabilityPeriod (noop otherwise).
		d.recordDogStartSuccess(idleDog.Name)

		d.logger.Printf("Handler: dispatched plugin %s to dog %s", p.Name, idleDog.Name)

		// Record the dispatch so the cooldown gate suppresses immediate
		// re-dispatch (the daemon heartbeat would otherwise storm a
		// freshly-dispatched plugin — dogs don't reliably write the
		// gate's completion label; see 055747cd). This is a ResultInflight
		// record, NOT ResultSuccess: it only satisfies the gate within the
		// in-flight grace window. If the dog dies before running, the gate
		// re-opens after grace instead of re-arming the full cooldown on false
		// pretenses — which had let backups drift unbounded (gu-50nbo). The
		// dog's own run.sh records the terminal ResultSuccess on real
		// completion, which then satisfies the full cooldown.
		if _, err := recorder.RecordRun(plugin.PluginRunRecord{
			PluginName: p.Name,
			Result:     plugin.ResultInflight,
			Body:       fmt.Sprintf("Dispatched to dog %s", idleDog.Name),
		}); err != nil {
			d.logger.Printf("Handler: failed to record dispatch for plugin %s: %v", p.Name, err)
		}
	}
}

// filterDispatchablePlugins returns, in the input order, the plugins whose
// cooldown or cron gate is currently OPEN (eligible for dispatch). Manual-gate
// and gateless plugins are dropped. The per-plugin gate check is a read-only
// `bd list` (one ~0.85s cold-start each); evaluating them serially dominated
// handleDogs latency (~60s, gu-10nch), so the checks are fanned out behind a
// semaphore (same pattern as gu-el5bx/gu-1h3ur). The checks have no side
// effects, so concurrency is safe here; the caller dispatches serially.
//
// A plugin whose gate check errors is conservatively skipped (logged), matching
// the prior serial behavior.
func (d *Daemon) filterDispatchablePlugins(plugins []*plugin.Plugin, recorder *plugin.Recorder) []*plugin.Plugin {
	// candidates keeps only auto-dispatchable cooldown/cron plugins, preserving
	// the discovery order so dispatch remains deterministic.
	candidates := make([]*plugin.Plugin, 0, len(plugins))
	for _, p := range plugins {
		// Never auto-dispatch manual-gate plugins — they require an explicit trigger.
		if p.Gate != nil && p.Gate.Type == plugin.GateManual {
			d.logger.Printf("Handler: skipping plugin %s (gate=manual, requires explicit trigger)", p.Name)
			continue
		}
		// Only dispatch plugins with cooldown or cron gates.
		if p.Gate == nil || (p.Gate.Type != plugin.GateCooldown && p.Gate.Type != plugin.GateCron) {
			continue
		}
		candidates = append(candidates, p)
	}

	// open[i] reports whether candidates[i]'s cooldown gate is open. Indexed
	// writes need no lock (disjoint slots), so the semaphore alone bounds Dolt load.
	open := make([]bool, len(candidates))
	sem := make(chan struct{}, dogDispatchGateConcurrency())
	var wg sync.WaitGroup
	for i, p := range candidates {
		// A cooldown plugin with no duration is always eligible (no gate query).
		if p.Gate.Type == plugin.GateCooldown && p.Gate.Duration == "" {
			open[i] = true
			continue
		}
		// A cron plugin with no schedule can never fire — skip it (open stays false)
		// without spawning a query goroutine.
		if p.Gate.Type == plugin.GateCron && p.Gate.Schedule == "" {
			d.logger.Printf("Handler: skipping cron-gate plugin %s (empty schedule)", p.Name)
			continue
		}
		wg.Add(1)
		go func(i int, p *plugin.Plugin) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			switch p.Gate.Type {
			case plugin.GateCron:
				// Evaluate cron: dispatch only when a scheduled fire has elapsed
				// that no terminal run has serviced yet. The same in-flight grace
				// guard as cooldown prevents the heartbeat from storming a
				// freshly-dispatched plugin (gu-50nbo).
				grace := p.DispatchGrace(0)
				due, err := recorder.CronDue(p.Name, p.Gate.Schedule, grace.String())
				if err != nil {
					d.logger.Printf("Handler: error checking cron schedule for plugin %s: %v", p.Name, err)
					return // conservatively skip (open[i] stays false)
				}
				open[i] = due // gate open only when a scheduled fire is due

			default: // plugin.GateCooldown
				// Evaluate cooldown: skip if plugin ran recently. A bare dispatch
				// record (dog handed work but not yet executed) only suppresses
				// re-dispatch within an in-flight grace window — after that the gate
				// re-opens so a silently-dead dog can't re-arm the full cooldown and
				// drift backups unbounded (gu-50nbo).
				cooldownDur, _ := time.ParseDuration(p.Gate.Duration)
				grace := p.DispatchGrace(cooldownDur)
				satisfied, err := recorder.CooldownSatisfied(p.Name, p.Gate.Duration, grace.String())
				if err != nil {
					d.logger.Printf("Handler: error checking cooldown for plugin %s: %v", p.Name, err)
					return // conservatively skip (open[i] stays false)
				}
				open[i] = !satisfied // gate open only when NOT still in cooldown/in-flight
			}
		}(i, p)
	}
	wg.Wait()

	eligible := make([]*plugin.Plugin, 0, len(candidates))
	for i, p := range candidates {
		if open[i] {
			eligible = append(eligible, p)
		}
	}
	return eligible
}

// dogDispatchGateConcurrency bounds how many plugin cooldown-gate `bd list`
// checks run concurrently in the dog-dispatch pre-pass (gu-10nch). These reads
// hit the single shared Dolt server, so the semaphore keeps a large plugin set
// from storming it. Default 6 — matching the dispatch-scan fan-out, which runs
// on the same heartbeat budget. Tunable via GT_DOG_DISPATCH_GATE_FANOUT.
func dogDispatchGateConcurrency() int {
	const def = 6
	if v := os.Getenv("GT_DOG_DISPATCH_GATE_FANOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	return def
}

// findDispatchableDog returns the first dog in the kennel whose registry
// state is idle AND whose tmux session is NOT currently running. Returns nil
// when no dog satisfies both conditions.
//
// This exists because a dog can be marked idle (via gt dog done or the reaper)
// before its tmux session fully terminates, producing a transient window where
// sm.Start would fail with "session already running". Picking that dog every
// dispatch tick infinite-loops the same failed dispatch instead of advancing
// to another genuinely-free dog in the pack. See gt-o24.
//
// IsRunning errors are logged and treated as "not dispatchable" so a flaky
// tmux check can't wedge the whole dispatch cycle.
//
// This is the free-function flavor with no daemon/backoff awareness — it is
// kept for unit tests that construct a bare SessionManager. Production code
// should call (*Daemon).findDispatchableDog so dogs in startup backoff are
// also skipped (see gu-ro75).
//
//nolint:unused // test seam: exercised by handler_test.go (lint runs tests:false)
func findDispatchableDog(mgr *dog.Manager, sm *dog.SessionManager, logger *log.Logger) *dog.Dog {
	dogs, err := mgr.List()
	if err != nil {
		logger.Printf("Handler: failed to list dogs while picking dispatch target: %v", err)
		return nil
	}
	for _, d := range dogs {
		if d.State != dog.StateIdle {
			continue
		}
		running, err := sm.IsRunning(d.Name)
		if err != nil {
			logger.Printf("Handler: IsRunning check failed for dog %s: %v; skipping", d.Name, err)
			continue
		}
		if running {
			continue
		}
		return d
	}
	return nil
}

// findDispatchableDog is the daemon-aware variant of the free function above.
// In addition to the idle+not-running filter, it skips dogs whose startup is
// currently in exponential backoff due to prior failures. Every dog whose
// backoff is active gets a single-line log explaining why it was skipped so
// the log trail makes the loop protection visible. See gu-ro75.
func (d *Daemon) findDispatchableDog(mgr *dog.Manager, sm *dog.SessionManager) *dog.Dog {
	dogs, err := mgr.List()
	if err != nil {
		d.logger.Printf("Handler: failed to list dogs while picking dispatch target: %v", err)
		return nil
	}
	for _, dg := range dogs {
		if dg.State != dog.StateIdle {
			continue
		}
		running, err := sm.IsRunning(dg.Name)
		if err != nil {
			d.logger.Printf("Handler: IsRunning check failed for dog %s: %v; skipping", dg.Name, err)
			continue
		}
		if running {
			continue
		}
		if skip, reason := d.isDogInStartupBackoff(dg.Name); skip {
			d.logger.Printf("Handler: skipping dispatch — %s", reason)
			continue
		}
		return dg
	}
	return nil
}

// loadRigsConfig loads the rigs configuration from mayor/rigs.json.
func (d *Daemon) loadRigsConfig() (*config.RigsConfig, error) {
	rigsPath := filepath.Join(d.config.TownRoot, "mayor", "rigs.json")
	return config.LoadRigsConfig(rigsPath)
}

// loadOperationalConfig loads operational thresholds from town settings.
// Returns a valid (never nil) config — accessors return defaults for nil fields.
func (d *Daemon) loadOperationalConfig() *config.OperationalConfig {
	return config.LoadOperationalConfig(d.config.TownRoot)
}

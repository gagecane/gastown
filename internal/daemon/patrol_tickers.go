package daemon

import "time"

// patrolTickers holds the tick channels for every optional patrol ticker the
// daemon's main loop selects on. A nil channel never fires in a select, so an
// inactive patrol is simply a nil field — the main loop's select cases need no
// per-patrol guards.
type patrolTickers struct {
	doltHealth           <-chan time.Time
	doltRemotes          <-chan time.Time
	doltBackup           <-chan time.Time
	doltBackupWatcher    <-chan time.Time
	jsonlGitBackup       <-chan time.Time
	wispReaper           <-chan time.Time
	doctorDog            <-chan time.Time
	compactorDog         <-chan time.Time
	checkpointDog        <-chan time.Time
	scheduledMaintenance <-chan time.Time
	mainBranchTest       <-chan time.Time
	quotaDog             <-chan time.Time
	pollerDog            <-chan time.Time
	failureClassifier    <-chan time.Time
	curio                <-chan time.Time
	nudgeQueueGC         <-chan time.Time
	restartPending       <-chan time.Time
	circuitBreak         <-chan time.Time
	schedulerStuck       <-chan time.Time
	eventChannelGC       <-chan time.Time
	circuitBreakerGC     <-chan time.Time
	branchSync           <-chan time.Time
}

// setupPatrolTickers creates a ticker for each active patrol and returns their
// tick channels plus a stop function that halts every created ticker. The
// caller is expected to `defer stop()`. Patrols that are inactive (disabled in
// daemon config or listed in town disabled_patrols) leave their channel nil so
// the main loop's select silently ignores them.
//
// This was extracted verbatim from Daemon.Run to keep the main loop readable:
// the per-patrol setup is a long run of near-identical blocks, so the only
// meaningful decisions live in isPatrolActive and the interval helpers — both
// independently tested.
func (d *Daemon) setupPatrolTickers() (patrolTickers, func()) {
	var pt patrolTickers
	var tickers []*time.Ticker

	// add registers a patrol ticker when the patrol is active, returning its
	// channel (nil — and therefore never selectable — when inactive).
	add := func(patrol string, interval time.Duration, label string) <-chan time.Time {
		if !d.isPatrolActive(patrol) {
			return nil
		}
		t := time.NewTicker(interval)
		tickers = append(tickers, t)
		d.logger.Printf("%s ticker started (interval %v)", label, interval)
		return t.C
	}

	// Dolt health check is gated on the server being enabled (not a patrol
	// flag) and runs at a much higher frequency than the general heartbeat so
	// Dolt crashes are detected quickly.
	if d.doltServer != nil && d.doltServer.IsEnabled() {
		interval := d.doltServer.HealthCheckInterval()
		t := time.NewTicker(interval)
		tickers = append(tickers, t)
		pt.doltHealth = t.C
		d.logger.Printf("Dolt health check ticker started (interval %v)", interval)
	}

	pt.doltRemotes = add("dolt_remotes", doltRemotesInterval(d.patrolConfig), "Dolt remotes push")
	pt.doltBackup = add("dolt_backup", doltBackupInterval(d.patrolConfig), "Dolt backup")
	pt.doltBackupWatcher = add("dolt_backup_watcher", doltBackupWatcherInterval(d.patrolConfig), "Dolt backup watcher")
	pt.jsonlGitBackup = add("jsonl_git_backup", jsonlGitBackupInterval(d.patrolConfig), "JSONL git backup")
	pt.wispReaper = add("wisp_reaper", wispReaperInterval(d.patrolConfig), "Wisp reaper")
	pt.doctorDog = add("doctor_dog", doctorDogInterval(d.patrolConfig), "Doctor dog")
	pt.compactorDog = add("compactor_dog", compactorDogInterval(d.patrolConfig), "Compactor dog")
	pt.checkpointDog = add("checkpoint_dog", checkpointDogInterval(d.patrolConfig), "Checkpoint dog")

	// Scheduled maintenance logs its window alongside the interval, so it does
	// not use the generic add() helper.
	if d.isPatrolActive("scheduled_maintenance") {
		interval := maintenanceCheckInterval(d.patrolConfig)
		t := time.NewTicker(interval)
		tickers = append(tickers, t)
		pt.scheduledMaintenance = t.C
		window := maintenanceWindow(d.patrolConfig)
		d.logger.Printf("Scheduled maintenance ticker started (check interval %v, window %s)", interval, window)
	}

	pt.mainBranchTest = add("main_branch_test", mainBranchTestInterval(d.patrolConfig), "Main branch test")
	pt.quotaDog = add("quota_dog", quotaDogInterval(d.patrolConfig), "Quota dog")
	pt.pollerDog = add("poller_dog", pollerDogInterval(d.patrolConfig), "Poller dog")
	pt.failureClassifier = add("failure_classifier", failureClassifierInterval(d.patrolConfig), "Failure classifier")
	pt.curio = add("curio", curioInterval(d.patrolConfig), "Curio")
	pt.nudgeQueueGC = add("nudge_queue_gc", nudgeQueueGCInterval(d.patrolConfig), "Nudge queue GC")
	pt.restartPending = add("restart_pending", restartPendingInterval(d.patrolConfig), "Restart-pending dog")
	pt.circuitBreak = add("circuit_break", circuitBreakInterval(d.patrolConfig), "Circuit-break dog")
	pt.schedulerStuck = add("scheduler_stuck", schedulerStuckInterval(d.patrolConfig), "Scheduler-stuck dog")
	pt.eventChannelGC = add("event_channel_gc", eventChannelGCInterval(d.patrolConfig), "Event channel GC")
	pt.circuitBreakerGC = add("circuit_breaker_gc", circuitBreakerGCInterval(d.patrolConfig), "Circuit-breaker GC")
	pt.branchSync = add("branch_sync", branchSyncInterval(d.patrolConfig), "Branch sync")

	stop := func() {
		for _, t := range tickers {
			t.Stop()
		}
	}
	return pt, stop
}

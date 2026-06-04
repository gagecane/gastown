package daemon

import (
	"io"
	"log"
	"testing"
)

// newTickerTestDaemon builds a minimal Daemon suitable for exercising
// setupPatrolTickers: it only needs a logger plus the patrol/disabled config
// that drive the active/inactive branch decisions. doltServer is left nil so
// the dolt-health branch stays inactive unless a test sets it.
func newTickerTestDaemon(cfg *DaemonPatrolConfig, disabled map[string]bool) *Daemon {
	return &Daemon{
		logger:          log.New(io.Discard, "", 0),
		patrolConfig:    cfg,
		disabledPatrols: disabled,
	}
}

func TestSetupPatrolTickers_InactivePatrolsLeaveNilChannels(t *testing.T) {
	// Nil config: opt-in patrols default to disabled, so their channels must
	// be nil (a nil channel never fires in the main loop's select).
	d := newTickerTestDaemon(nil, nil)
	pt, stop := d.setupPatrolTickers()
	defer stop()

	if pt.doctorDog != nil {
		t.Error("doctor_dog is opt-in and should be nil with nil config")
	}
	if pt.doltRemotes != nil {
		t.Error("dolt_remotes is opt-in and should be nil with nil config")
	}
	if pt.curio != nil {
		t.Error("curio is opt-in and should be nil with nil config")
	}
	// doltServer is nil, so the dolt-health branch must not produce a channel.
	if pt.doltHealth != nil {
		t.Error("doltHealth should be nil when doltServer is nil")
	}
}

func TestSetupPatrolTickers_DefaultEnabledPatrolsGetChannels(t *testing.T) {
	// nudge_queue_gc and restart_pending are default-enabled even with nil
	// config, so their channels must be live.
	d := newTickerTestDaemon(nil, nil)
	pt, stop := d.setupPatrolTickers()
	defer stop()

	if pt.nudgeQueueGC == nil {
		t.Error("nudge_queue_gc is default-enabled and should have a live channel")
	}
	if pt.restartPending == nil {
		t.Error("restart_pending is default-enabled and should have a live channel")
	}
}

func TestSetupPatrolTickers_ExplicitlyEnabledPatrolGetsChannel(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			DoctorDog: &DoctorDogConfig{Enabled: true},
		},
	}
	d := newTickerTestDaemon(cfg, nil)
	pt, stop := d.setupPatrolTickers()
	defer stop()

	if pt.doctorDog == nil {
		t.Error("doctor_dog enabled in config should produce a live channel")
	}
}

func TestSetupPatrolTickers_DisabledListOverridesConfig(t *testing.T) {
	// Enabled in daemon config but listed in town disabled_patrols → inactive.
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			DoctorDog: &DoctorDogConfig{Enabled: true},
		},
	}
	d := newTickerTestDaemon(cfg, map[string]bool{"doctor_dog": true})
	pt, stop := d.setupPatrolTickers()
	defer stop()

	if pt.doctorDog != nil {
		t.Error("doctor_dog in disabled_patrols should be nil even when enabled in config")
	}

	// A default-enabled patrol can also be suppressed via the disabled list.
	d2 := newTickerTestDaemon(nil, map[string]bool{"nudge_queue_gc": true})
	pt2, stop2 := d2.setupPatrolTickers()
	defer stop2()
	if pt2.nudgeQueueGC != nil {
		t.Error("nudge_queue_gc in disabled_patrols should be nil")
	}
}

func TestSetupPatrolTickers_StopHaltsTickersWithoutPanic(t *testing.T) {
	// Enable a patrol so at least one real ticker is created, then ensure the
	// returned stop function runs cleanly (the defer-stop contract from Run).
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			DoctorDog: &DoctorDogConfig{Enabled: true},
		},
	}
	d := newTickerTestDaemon(cfg, nil)
	_, stop := d.setupPatrolTickers()
	stop() // must not panic
}

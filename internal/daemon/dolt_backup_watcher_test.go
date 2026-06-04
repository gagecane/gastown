package daemon

import (
	"testing"
	"time"
)

func TestEvaluateBackupHeartbeat_MissingFileAlarms(t *testing.T) {
	v := evaluateBackupHeartbeat(nil, false, time.Now(), time.Hour)
	if !v.Alarm {
		t.Fatal("missing heartbeat must alarm")
	}
}

func TestEvaluateBackupHeartbeat_FreshSuccessNoAlarm(t *testing.T) {
	now := time.Now()
	data := []byte(`{"timestamp":"` + now.Add(-5*time.Minute).UTC().Format(time.RFC3339) + `","status":"success","failed":0}`)
	v := evaluateBackupHeartbeat(data, true, now, 45*time.Minute)
	if v.Alarm {
		t.Fatalf("fresh heartbeat must not alarm, got reason: %s", v.Reason)
	}
}

func TestEvaluateBackupHeartbeat_StaleAlarms(t *testing.T) {
	now := time.Now()
	// 90 minutes old, threshold 45m → stale.
	data := []byte(`{"timestamp":"` + now.Add(-90*time.Minute).UTC().Format(time.RFC3339) + `","status":"success","failed":0}`)
	v := evaluateBackupHeartbeat(data, true, now, 45*time.Minute)
	if !v.Alarm {
		t.Fatal("stale heartbeat must alarm")
	}
}

func TestEvaluateBackupHeartbeat_JustUnderThresholdNoAlarm(t *testing.T) {
	now := time.Now()
	// 44 minutes old, threshold 45m → still fresh.
	data := []byte(`{"timestamp":"` + now.Add(-44*time.Minute).UTC().Format(time.RFC3339) + `","status":"success","failed":0}`)
	v := evaluateBackupHeartbeat(data, true, now, 45*time.Minute)
	if v.Alarm {
		t.Fatalf("heartbeat just under threshold must not alarm, got: %s", v.Reason)
	}
}

func TestEvaluateBackupHeartbeat_UnparseableAlarms(t *testing.T) {
	v := evaluateBackupHeartbeat([]byte("not json"), true, time.Now(), time.Hour)
	if !v.Alarm {
		t.Fatal("unparseable heartbeat must alarm")
	}
}

func TestEvaluateBackupHeartbeat_MissingTimestampAlarms(t *testing.T) {
	v := evaluateBackupHeartbeat([]byte(`{"status":"success","failed":0}`), true, time.Now(), time.Hour)
	if !v.Alarm {
		t.Fatal("heartbeat with no timestamp must alarm")
	}
}

func TestEvaluateBackupHeartbeat_BadTimestampFormatAlarms(t *testing.T) {
	v := evaluateBackupHeartbeat([]byte(`{"timestamp":"yesterday","status":"success"}`), true, time.Now(), time.Hour)
	if !v.Alarm {
		t.Fatal("heartbeat with non-RFC3339 timestamp must alarm")
	}
}

func TestEvaluateBackupHeartbeat_FreshFailedStatusDoesNotAlarm(t *testing.T) {
	// A recent FAILED run is the plugin's job to escalate (it already does HIGH).
	// The watcher only catches SILENCE, so a fresh failed heartbeat is NOT a
	// watcher alarm — that would double-page.
	now := time.Now()
	data := []byte(`{"timestamp":"` + now.Add(-2*time.Minute).UTC().Format(time.RFC3339) + `","status":"failed","failed":3}`)
	v := evaluateBackupHeartbeat(data, true, now, 45*time.Minute)
	if v.Alarm {
		t.Fatalf("fresh failed heartbeat must not double-alarm, got: %s", v.Reason)
	}
}

func TestDoltBackupWatcherInterval_Default(t *testing.T) {
	if got := doltBackupWatcherInterval(nil); got != defaultDoltBackupWatcherInterval {
		t.Fatalf("got %v, want %v", got, defaultDoltBackupWatcherInterval)
	}
}

func TestDoltBackupWatcherInterval_Configured(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{
		DoltBackupWatcher: &DoltBackupWatcherConfig{Enabled: true, IntervalStr: "5m"},
	}}
	if got := doltBackupWatcherInterval(cfg); got != 5*time.Minute {
		t.Fatalf("got %v, want 5m", got)
	}
}

func TestDoltBackupWatcherInterval_InvalidFallsBack(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{
		DoltBackupWatcher: &DoltBackupWatcherConfig{Enabled: true, IntervalStr: "nonsense"},
	}}
	if got := doltBackupWatcherInterval(cfg); got != defaultDoltBackupWatcherInterval {
		t.Fatalf("got %v, want default", got)
	}
}

func TestDoltBackupStalenessThreshold_DerivedFromBackupInterval(t *testing.T) {
	// No explicit staleness → factor * backup interval. Backup default is 15m.
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{
		DoltBackup:        &DoltBackupConfig{Enabled: true, IntervalStr: "10m"},
		DoltBackupWatcher: &DoltBackupWatcherConfig{Enabled: true},
	}}
	want := doltBackupStalenessFactor * 10 * time.Minute
	if got := doltBackupStalenessThreshold(cfg); got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDoltBackupStalenessThreshold_ExplicitOverride(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{
		DoltBackup:        &DoltBackupConfig{Enabled: true, IntervalStr: "10m"},
		DoltBackupWatcher: &DoltBackupWatcherConfig{Enabled: true, StalenessStr: "90m"},
	}}
	if got := doltBackupStalenessThreshold(cfg); got != 90*time.Minute {
		t.Fatalf("got %v, want 90m", got)
	}
}

func TestIsPatrolEnabled_DoltBackupWatcher(t *testing.T) {
	// Opt-in semantics: nil config → disabled.
	if IsPatrolEnabled(nil, "dolt_backup_watcher") {
		t.Fatal("nil config must report watcher disabled")
	}
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{
		DoltBackupWatcher: &DoltBackupWatcherConfig{Enabled: true},
	}}
	if !IsPatrolEnabled(cfg, "dolt_backup_watcher") {
		t.Fatal("enabled config must report watcher enabled")
	}
}

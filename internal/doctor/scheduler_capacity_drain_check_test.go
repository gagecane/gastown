package doctor

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// withStubbedSchedulerStatus replaces the scheduler-status subprocess with a
// fixed envelope and restores the original after the test.
func withStubbedSchedulerStatus(t *testing.T, env schedulerStatusEnvelope, returnErr error) {
	t.Helper()
	orig := runSchedulerStatusJSON
	runSchedulerStatusJSON = func(string) (schedulerStatusEnvelope, error) {
		return env, returnErr
	}
	t.Cleanup(func() { runSchedulerStatusJSON = orig })
}

// withStubbedNow pins the check's clock to a fixed instant.
func withStubbedNow(t *testing.T, instant time.Time) {
	t.Helper()
	orig := nowFn
	nowFn = func() time.Time { return instant }
	t.Cleanup(func() { nowFn = orig })
}

func makeEnv(max, blocked int) schedulerStatusEnvelope {
	var env schedulerStatusEnvelope
	env.Capacity.Max = max
	env.Capacity.RecoveryBlocked = blocked
	return env
}

func TestSchedulerCapacityDrainCheck_Healthy(t *testing.T) {
	tmpDir := t.TempDir()
	withStubbedSchedulerStatus(t, makeEnv(50, 5), nil) // 10% — well under threshold
	withStubbedNow(t, time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC))

	// Pre-seed a stale drain marker; the healthy path must clear it.
	stateFile := schedulerCapacityDrainStateFile(tmpDir)
	preState := drainState{FirstDetectedAt: time.Date(2026, 5, 31, 11, 0, 0, 0, time.UTC)}
	if err := saveDrainState(stateFile, preState); err != nil {
		t.Fatalf("saveDrainState: %v", err)
	}

	check := NewSchedulerCapacityDrainCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})

	if result.Status != StatusOK {
		t.Errorf("Status = %v, want OK; msg=%q", result.Status, result.Message)
	}
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Errorf("expected stale drain state file to be cleared, stat err=%v", err)
	}
}

func TestSchedulerCapacityDrainCheck_TransientDrain_WarnsButDoesNotError(t *testing.T) {
	tmpDir := t.TempDir()
	withStubbedSchedulerStatus(t, makeEnv(50, 38), nil) // 76% — over threshold
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	withStubbedNow(t, now)

	check := NewSchedulerCapacityDrainCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})

	if result.Status != StatusWarning {
		t.Errorf("first drain should be Warning, got %v: %s", result.Status, result.Message)
	}
	state, err := loadDrainState(schedulerCapacityDrainStateFile(tmpDir))
	if err != nil {
		t.Fatalf("loadDrainState: %v", err)
	}
	if !state.FirstDetectedAt.Equal(now) {
		t.Errorf("FirstDetectedAt = %v, want %v", state.FirstDetectedAt, now)
	}
	if state.LastBlocked != 38 || state.LastMax != 50 {
		t.Errorf("LastBlocked/LastMax = %d/%d, want 38/50", state.LastBlocked, state.LastMax)
	}
}

func TestSchedulerCapacityDrainCheck_SustainedDrain_Errors(t *testing.T) {
	tmpDir := t.TempDir()
	withStubbedSchedulerStatus(t, makeEnv(50, 38), nil)

	// Seed with a drain that started long enough ago to cross drainErrorAge.
	first := time.Date(2026, 5, 31, 11, 0, 0, 0, time.UTC)
	stateFile := schedulerCapacityDrainStateFile(tmpDir)
	if err := saveDrainState(stateFile, drainState{FirstDetectedAt: first}); err != nil {
		t.Fatalf("seed saveDrainState: %v", err)
	}
	withStubbedNow(t, first.Add(drainErrorAge+time.Minute))

	check := NewSchedulerCapacityDrainCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})

	if result.Status != StatusError {
		t.Errorf("sustained drain should be Error, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "drained for") {
		t.Errorf("message should mention drain duration, got: %s", result.Message)
	}
	if result.FixHint == "" {
		t.Error("error result should include a fix hint")
	}
}

func TestSchedulerCapacityDrainCheck_DirectDispatchSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	// max=-1 means direct-dispatch mode — there is no pool to drain.
	env := schedulerStatusEnvelope{}
	env.Capacity.Max = -1
	env.Capacity.RecoveryBlocked = 99 // would otherwise trip warn/error
	withStubbedSchedulerStatus(t, env, nil)
	withStubbedNow(t, time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC))

	check := NewSchedulerCapacityDrainCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})

	if result.Status != StatusOK {
		t.Errorf("direct dispatch should be OK, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(strings.ToLower(result.Message), "direct dispatch") {
		t.Errorf("message should mention direct dispatch, got: %s", result.Message)
	}
}

func TestSchedulerCapacityDrainCheck_SubprocessError_Warns(t *testing.T) {
	tmpDir := t.TempDir()
	withStubbedSchedulerStatus(t, schedulerStatusEnvelope{}, errors.New("boom"))
	withStubbedNow(t, time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC))

	check := NewSchedulerCapacityDrainCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})

	if result.Status != StatusWarning {
		t.Errorf("subprocess error should be Warning, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "Could not read scheduler status") {
		t.Errorf("message should mention scheduler status read failure, got: %s", result.Message)
	}
}

func TestSchedulerCapacityDrainCheck_ReadOnlyDoesNotPersist(t *testing.T) {
	tmpDir := t.TempDir()
	withStubbedSchedulerStatus(t, makeEnv(50, 38), nil)
	withStubbedNow(t, time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC))

	check := NewSchedulerCapacityDrainCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir, ReadOnly: true})

	if result.Status != StatusWarning {
		t.Errorf("Status = %v, want Warning; msg=%q", result.Status, result.Message)
	}
	if _, err := os.Stat(schedulerCapacityDrainStateFile(tmpDir)); !os.IsNotExist(err) {
		t.Errorf("ReadOnly run should not persist state file, stat err=%v", err)
	}
}

func TestSchedulerCapacityDrainCheck_Properties(t *testing.T) {
	check := NewSchedulerCapacityDrainCheck()
	if check.Name() != "scheduler-capacity-drain" {
		t.Errorf("Name() = %q", check.Name())
	}
	if check.Category() != CategoryPatrol {
		t.Errorf("Category() = %q, want %q", check.Category(), CategoryPatrol)
	}
	if check.CanFix() {
		t.Error("CanFix() should be false — no auto-fix for stuck polecats from this check")
	}
}

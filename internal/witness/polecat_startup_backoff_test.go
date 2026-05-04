package witness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withTightPolecatBackoff swaps the package-level backoff tunables for short
// test-friendly values and returns a restore function. Keeps the tests fast
// and deterministic while exercising the real code paths.
func withTightPolecatBackoff(t *testing.T) func() {
	t.Helper()

	prev := struct {
		initial    time.Duration
		max        time.Duration
		mult       float64
		window     time.Duration
		count      int
		stability  time.Duration
	}{
		initial:   polecatBackoffInitial,
		max:       polecatBackoffMax,
		mult:      polecatBackoffMultiplier,
		window:    polecatCrashLoopWindow,
		count:     polecatCrashLoopCount,
		stability: polecatStabilityPeriod,
	}

	polecatBackoffInitial = 100 * time.Millisecond
	polecatBackoffMax = 1 * time.Second
	polecatBackoffMultiplier = 2.0
	polecatCrashLoopWindow = 1 * time.Second
	polecatCrashLoopCount = 3
	polecatStabilityPeriod = 200 * time.Millisecond

	return func() {
		polecatBackoffInitial = prev.initial
		polecatBackoffMax = prev.max
		polecatBackoffMultiplier = prev.mult
		polecatCrashLoopWindow = prev.window
		polecatCrashLoopCount = prev.count
		polecatStabilityPeriod = prev.stability
	}
}

// newPolecatBackoffTempDir returns a fresh town-root-like tmpdir with the
// witness subdirectory pre-created so state-file writes never fail the
// mkdir step.
func newPolecatBackoffTempDir(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "witness"), 0o755); err != nil {
		t.Fatalf("mkdir witness: %v", err)
	}
	return tmp
}

// TestPolecatBackoff_FirstAttemptNotThrottled documents the baseline: a
// polecat that has never failed to start is never in backoff. If this
// changes, the very first restart attempt would be delayed — which would
// be a regression because the witness patrol relies on the initial restart
// actually taking effect to distinguish healthy from stuck polecats.
func TestPolecatBackoff_FirstAttemptNotThrottled(t *testing.T) {
	defer withTightPolecatBackoff(t)()
	tmp := newPolecatBackoffTempDir(t)

	skip, reason := IsPolecatInStartupBackoff(tmp, "rig-a", "alpha")
	if skip {
		t.Fatalf("expected new polecat to bypass backoff, got skip=true reason=%q", reason)
	}
}

// TestPolecatBackoff_AfterFailureSkipsUntilExpires is the core behaviour
// gu-mkz7 mitigation (3) requests: once a polecat's startup fails, the
// next patrol cycle sees it as "in backoff" and must skip the restart
// rather than hot-looping and flooding POLECAT_DIED mail.
func TestPolecatBackoff_AfterFailureSkipsUntilExpires(t *testing.T) {
	defer withTightPolecatBackoff(t)()
	tmp := newPolecatBackoffTempDir(t)

	RecordPolecatStartFailure(tmp, "rig-a", "bravo")

	skip, reason := IsPolecatInStartupBackoff(tmp, "rig-a", "bravo")
	if !skip {
		t.Fatalf("expected bravo to be in backoff after failure, got skip=false")
	}
	if reason == "" {
		t.Fatal("expected a non-empty backoff reason")
	}
	if got := GetPolecatBackoffRemaining(tmp, "rig-a", "bravo"); got <= 0 {
		t.Fatalf("expected positive backoff remaining, got %v", got)
	}

	// After the backoff window elapses the gate reopens.
	time.Sleep(250 * time.Millisecond)
	skip, _ = IsPolecatInStartupBackoff(tmp, "rig-a", "bravo")
	if skip {
		t.Fatalf("expected bravo backoff to expire, still skip=true")
	}
}

// TestPolecatBackoff_ExponentialGrowth verifies the key symptom fix:
// repeated startup failures produce progressively longer backoff windows,
// not the same constant delay on every heartbeat (gu-rq8i observed 14
// POLECAT_DIED mails in 30min from a single broken polecat).
func TestPolecatBackoff_ExponentialGrowth(t *testing.T) {
	defer withTightPolecatBackoff(t)()
	tmp := newPolecatBackoffTempDir(t)

	_, first := RecordPolecatStartFailure(tmp, "rig-a", "charlie")
	if first <= 0 {
		t.Fatalf("expected positive backoff after 1st failure, got %v", first)
	}

	_, second := RecordPolecatStartFailure(tmp, "rig-a", "charlie")
	if second <= first {
		t.Fatalf("expected backoff to grow exponentially, got %v → %v", first, second)
	}
}

// TestPolecatBackoff_CrashLoopMutesPolecat verifies the "after N consecutive
// failures, mute the polecat until manually cleared" acceptance criterion
// from gu-rq8i mitigation (3). With the test config's CrashLoopCount=3,
// the third failure within the window flips the polecat into crash-loop
// state and the reason tells the operator how to clear it.
func TestPolecatBackoff_CrashLoopMutesPolecat(t *testing.T) {
	defer withTightPolecatBackoff(t)()
	tmp := newPolecatBackoffTempDir(t)

	for i := 0; i < polecatCrashLoopCount; i++ {
		RecordPolecatStartFailure(tmp, "rig-a", "delta")
	}

	skip, reason := IsPolecatInStartupBackoff(tmp, "rig-a", "delta")
	if !skip {
		t.Fatalf("expected delta to be muted after crash-loop threshold, got skip=false")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason for crash-looped polecat")
	}
	if !strings.Contains(reason, "clear-polecat-backoff rig-a/delta") {
		t.Errorf("expected reason to instruct operator on clearing backoff, got %q", reason)
	}
	if !IsPolecatInCrashLoop(tmp, "rig-a", "delta") {
		t.Error("expected IsPolecatInCrashLoop = true after muting")
	}
}

// TestPolecatBackoff_SuccessResetsAfterStability verifies that a polecat
// which runs stably for the stability period clears its backoff counter.
// Without this a formerly-flaky polecat would accumulate state forever
// and eventually hit the crash-loop cap on perfectly healthy restarts —
// the same bug that motivated the dog tracker's stability-reset path.
func TestPolecatBackoff_SuccessResetsAfterStability(t *testing.T) {
	defer withTightPolecatBackoff(t)()
	tmp := newPolecatBackoffTempDir(t)

	RecordPolecatStartFailure(tmp, "rig-a", "echo")
	if GetPolecatBackoffRemaining(tmp, "rig-a", "echo") <= 0 {
		t.Fatal("expected initial backoff after failure")
	}

	// Stability period in the test config is 200ms.
	time.Sleep(250 * time.Millisecond)
	RecordPolecatStartSuccess(tmp, "rig-a", "echo")

	if got := GetPolecatBackoffRemaining(tmp, "rig-a", "echo"); got > 0 {
		t.Errorf("expected backoff cleared after stable success, got %v remaining", got)
	}
	if IsPolecatInCrashLoop(tmp, "rig-a", "echo") {
		t.Error("expected crash-loop state cleared after stable success")
	}
}

// TestPolecatBackoff_SuccessBeforeStabilityIsNoop verifies the contract
// that matches the dog tracker: calling RecordPolecatStartSuccess on a
// polecat that restarted within the stability period does NOT clear the
// counter. We want a polecat that died shortly after a successful start
// to keep its previous backoff intact, not reset to zero and re-enter
// the spawn storm.
func TestPolecatBackoff_SuccessBeforeStabilityIsNoop(t *testing.T) {
	defer withTightPolecatBackoff(t)()
	tmp := newPolecatBackoffTempDir(t)

	RecordPolecatStartFailure(tmp, "rig-a", "foxtrot")
	before := GetPolecatBackoffRemaining(tmp, "rig-a", "foxtrot")
	if before <= 0 {
		t.Fatal("setup: expected non-zero backoff")
	}

	// No sleep — stability period has NOT elapsed.
	RecordPolecatStartSuccess(tmp, "rig-a", "foxtrot")

	after := GetPolecatBackoffRemaining(tmp, "rig-a", "foxtrot")
	// Allow some drift from time.Until but assert it is roughly unchanged
	// (certainly not reset to zero).
	if after == 0 {
		t.Errorf("RecordPolecatStartSuccess unexpectedly reset backoff before stability; before=%v after=%v", before, after)
	}
}

// TestPolecatBackoff_ClearPolecatBackoff verifies the operator-facing
// escape hatch: once the operator has diagnosed and fixed the underlying
// crash, clearing the backoff removes it from the state file entirely so
// the next patrol cycle redispatches normally.
func TestPolecatBackoff_ClearPolecatBackoff(t *testing.T) {
	defer withTightPolecatBackoff(t)()
	tmp := newPolecatBackoffTempDir(t)

	for i := 0; i < polecatCrashLoopCount; i++ {
		RecordPolecatStartFailure(tmp, "rig-a", "golf")
	}
	if skip, _ := IsPolecatInStartupBackoff(tmp, "rig-a", "golf"); !skip {
		t.Fatal("setup: expected golf to be muted")
	}

	if err := ClearPolecatBackoff(tmp, "rig-a", "golf"); err != nil {
		t.Fatalf("ClearPolecatBackoff error: %v", err)
	}
	if skip, _ := IsPolecatInStartupBackoff(tmp, "rig-a", "golf"); skip {
		t.Error("expected ClearPolecatBackoff to re-enable restarts")
	}
	if IsPolecatInCrashLoop(tmp, "rig-a", "golf") {
		t.Error("expected crash-loop state cleared after ClearPolecatBackoff")
	}
}

// TestPolecatBackoff_AgentIDNamespacing ensures that per-rig + per-name
// namespacing prevents cross-rig polecat name collisions from muting an
// unrelated polecat. Two rigs can independently name polecats "dom"
// (different themes) and their backoff state must stay separate.
func TestPolecatBackoff_AgentIDNamespacing(t *testing.T) {
	if got := polecatBackoffID("rig-a", "dom"); got != "polecat:rig-a/dom" {
		t.Errorf("polecatBackoffID = %q, want polecat:rig-a/dom", got)
	}
	if polecatBackoffID("rig-a", "dom") == polecatBackoffID("rig-b", "dom") {
		t.Error("cross-rig polecats with the same name must NOT share backoff state")
	}
}

// TestPolecatBackoff_CrossRigIsolation verifies the invariant the
// namespacing test argues for: recording a failure in rig-a does not
// affect rig-b's state for the same polecat name.
func TestPolecatBackoff_CrossRigIsolation(t *testing.T) {
	defer withTightPolecatBackoff(t)()
	tmp := newPolecatBackoffTempDir(t)

	RecordPolecatStartFailure(tmp, "rig-a", "hotel")

	if skip, _ := IsPolecatInStartupBackoff(tmp, "rig-a", "hotel"); !skip {
		t.Fatal("setup: expected rig-a/hotel in backoff")
	}
	if skip, _ := IsPolecatInStartupBackoff(tmp, "rig-b", "hotel"); skip {
		t.Error("rig-b/hotel incorrectly in backoff after rig-a/hotel failure — cross-rig bleed")
	}
}

// TestPolecatBackoff_StabilityResetOnNextFailure verifies the dog tracker
// parity: a failure recorded long after the previous failure resets the
// restart counter before bumping it. Otherwise a polecat that fails once
// every few days would eventually hit the crash-loop cap despite being
// mostly healthy.
func TestPolecatBackoff_StabilityResetOnNextFailure(t *testing.T) {
	defer withTightPolecatBackoff(t)()
	tmp := newPolecatBackoffTempDir(t)

	RecordPolecatStartFailure(tmp, "rig-a", "india")
	firstCount, _ := RecordPolecatStartFailure(tmp, "rig-a", "india")
	if firstCount != 2 {
		t.Fatalf("setup: expected count=2 after 2 failures, got %d", firstCount)
	}

	// Wait past the stability period so the NEXT failure resets the counter.
	time.Sleep(250 * time.Millisecond)
	afterCount, _ := RecordPolecatStartFailure(tmp, "rig-a", "india")
	if afterCount != 1 {
		t.Errorf("expected counter reset to 1 after stability period, got %d", afterCount)
	}
}

// TestPolecatBackoff_StateFilePersistsAcrossCalls exercises the on-disk
// persistence: counters survive process-equivalent boundaries (each call
// re-opens the state file). Regression guard against accidentally making
// the state process-local.
func TestPolecatBackoff_StateFilePersistsAcrossCalls(t *testing.T) {
	defer withTightPolecatBackoff(t)()
	tmp := newPolecatBackoffTempDir(t)

	RecordPolecatStartFailure(tmp, "rig-a", "juliet")

	// Verify the file exists on disk with the expected path.
	path := polecatBackoffStateFile(tmp)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file not found at %s: %v", path, err)
	}

	// A fresh load (different in-process read path — mimics a restart)
	// still sees the recorded failure.
	state := loadPolecatBackoffState(tmp)
	if _, ok := state.Polecats[polecatBackoffID("rig-a", "juliet")]; !ok {
		t.Error("expected state file to contain rig-a/juliet after RecordPolecatStartFailure")
	}
}

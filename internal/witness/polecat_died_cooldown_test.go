package witness

import (
	"strings"
	"testing"
	"time"
)

// --- shouldNotifyPolecatDied: the dedup decision -----------------------------

// TestShouldNotifyPolecatDied_DisabledCooldown verifies cooldown<=0 always
// notifies (pre-gu-b4b39 behavior / operator opt-out), regardless of prior state.
func TestShouldNotifyPolecatDied_DisabledCooldown(t *testing.T) {
	now := time.Now().UTC()
	prev := &polecatDiedNotifyState{LastNotifiedAt: now, LastSignature: "x|gu-1"}
	if !shouldNotifyPolecatDied(prev, now, 0, "x|gu-1") {
		t.Errorf("cooldown=0 must always notify")
	}
}

// TestShouldNotifyPolecatDied_FirstObservation verifies the absence of a prior
// record always notifies — the first time we see a dead polecat we must alarm.
func TestShouldNotifyPolecatDied_FirstObservation(t *testing.T) {
	now := time.Now().UTC()
	if !shouldNotifyPolecatDied(nil, now, 30*time.Minute, "x|gu-1") {
		t.Errorf("first observation (prev=nil) must notify")
	}
}

// TestShouldNotifyPolecatDied_SuppressWithinCooldown verifies the core fix: the
// same death (same signature) reported inside the cooldown window is suppressed.
func TestShouldNotifyPolecatDied_SuppressWithinCooldown(t *testing.T) {
	start := time.Now().UTC()
	prev := &polecatDiedNotifyState{LastNotifiedAt: start, LastSignature: "ZombieSessionDeadActive|gu-1"}
	if shouldNotifyPolecatDied(prev, start.Add(10*time.Minute), 30*time.Minute, "ZombieSessionDeadActive|gu-1") {
		t.Errorf("same death within cooldown must be suppressed")
	}
}

// TestShouldNotifyPolecatDied_ReNotifyAfterCooldown verifies the alarm re-fires
// once the cooldown window elapses, so a genuinely stuck polecat is not forgotten.
func TestShouldNotifyPolecatDied_ReNotifyAfterCooldown(t *testing.T) {
	start := time.Now().UTC()
	prev := &polecatDiedNotifyState{LastNotifiedAt: start, LastSignature: "x|gu-1"}
	if !shouldNotifyPolecatDied(prev, start.Add(31*time.Minute), 30*time.Minute, "x|gu-1") {
		t.Errorf("must re-notify after cooldown elapses")
	}
}

// TestShouldNotifyPolecatDied_ReNotifyOnSignatureChange verifies a materially
// different death (new classification or hook bead) re-fires inside the cooldown.
func TestShouldNotifyPolecatDied_ReNotifyOnSignatureChange(t *testing.T) {
	start := time.Now().UTC()
	prev := &polecatDiedNotifyState{LastNotifiedAt: start, LastSignature: "x|gu-1"}
	// Within cooldown, but signature changed (new hook bead).
	if !shouldNotifyPolecatDied(prev, start.Add(5*time.Minute), 30*time.Minute, "x|gu-2") {
		t.Errorf("signature change must re-notify even within cooldown")
	}
}

// --- polecatDiedSignature ----------------------------------------------------

func TestPolecatDiedSignature_DistinguishesStates(t *testing.T) {
	a := polecatDiedSignature(ZombieResult{Classification: "ZombieSessionDeadActive", HookBead: "gu-1"})
	b := polecatDiedSignature(ZombieResult{Classification: "ZombieSessionDeadActive", HookBead: "gu-2"})
	c := polecatDiedSignature(ZombieResult{Classification: "ZombieAgentDead", HookBead: "gu-1"})
	if a == b || a == c || b == c {
		t.Errorf("distinct (classification, hook) must yield distinct signatures: %q %q %q", a, b, c)
	}
	// Same inputs -> same signature (stable).
	if a != polecatDiedSignature(ZombieResult{Classification: "ZombieSessionDeadActive", HookBead: "gu-1"}) {
		t.Errorf("signature must be stable for identical inputs")
	}
}

// --- FilterFreshPolecatDeaths: integration over persisted state --------------

func activeZombie(name, class, hook string) ZombieResult {
	return ZombieResult{PolecatName: name, Classification: ZombieClassification(class), HookBead: hook, WasActive: true, Action: "restarted"}
}

// TestFilterFreshPolecatDeaths_SuppressesRepeatCycle verifies the end-to-end fix:
// the first cycle escalates, an immediate second cycle (same stuck state) is
// suppressed, and after the cooldown elapses it re-escalates with a rollup note.
func TestFilterFreshPolecatDeaths_SuppressesRepeatCycle(t *testing.T) {
	town := t.TempDir()
	rig := "talontriage"
	cooldown := 30 * time.Minute
	res := &DetectZombiePolecatsResult{Zombies: []ZombieResult{activeZombie("capable", "ZombieSessionDeadActive", "ta-bujl")}}

	t0 := time.Now().UTC()
	first := FilterFreshPolecatDeaths(town, town, rig, res, cooldown, t0)
	if len(first) != 1 {
		t.Fatalf("first cycle must escalate, got %d notices", len(first))
	}
	if first[0].SuppressionNote != "" {
		t.Errorf("first escalation should have no suppression note, got %q", first[0].SuppressionNote)
	}

	// Second cycle a few minutes later, same stuck state — must suppress.
	second := FilterFreshPolecatDeaths(town, town, rig, res, cooldown, t0.Add(5*time.Minute))
	if len(second) != 0 {
		t.Fatalf("repeat cycle within cooldown must be suppressed, got %d notices", len(second))
	}
	third := FilterFreshPolecatDeaths(town, town, rig, res, cooldown, t0.Add(12*time.Minute))
	if len(third) != 0 {
		t.Fatalf("second repeat within cooldown must be suppressed, got %d notices", len(third))
	}

	// After cooldown elapses — re-escalate, and the note must roll up the
	// suppressed cycles.
	fourth := FilterFreshPolecatDeaths(town, town, rig, res, cooldown, t0.Add(31*time.Minute))
	if len(fourth) != 1 {
		t.Fatalf("must re-escalate after cooldown, got %d notices", len(fourth))
	}
	if !strings.Contains(fourth[0].SuppressionNote, "Suppressed 2 duplicate") {
		t.Errorf("re-escalation must report 2 suppressed cycles, got %q", fourth[0].SuppressionNote)
	}
}

// TestFilterFreshPolecatDeaths_SignatureChangeReEscalates verifies that when the
// polecat's stuck state changes (different hook bead) inside the cooldown, the
// alarm re-fires rather than being suppressed.
func TestFilterFreshPolecatDeaths_SignatureChangeReEscalates(t *testing.T) {
	town := t.TempDir()
	rig := "talontriage"
	cooldown := 30 * time.Minute
	t0 := time.Now().UTC()

	res1 := &DetectZombiePolecatsResult{Zombies: []ZombieResult{activeZombie("capable", "ZombieSessionDeadActive", "ta-bujl")}}
	if got := FilterFreshPolecatDeaths(town, town, rig, res1, cooldown, t0); len(got) != 1 {
		t.Fatalf("first escalation expected, got %d", len(got))
	}

	// 5m later, same polecat but now hooked on a different bead — material change.
	res2 := &DetectZombiePolecatsResult{Zombies: []ZombieResult{activeZombie("capable", "ZombieSessionDeadActive", "ta-NEW")}}
	got := FilterFreshPolecatDeaths(town, town, rig, res2, cooldown, t0.Add(5*time.Minute))
	if len(got) != 1 {
		t.Fatalf("signature change must re-escalate within cooldown, got %d", len(got))
	}
}

// TestFilterFreshPolecatDeaths_InactiveClearsState verifies that a non-active
// zombie (no recent work) clears any prior cooldown record so a future active
// death re-notifies immediately rather than being silenced by stale state.
func TestFilterFreshPolecatDeaths_InactiveClearsState(t *testing.T) {
	town := t.TempDir()
	rig := "talontriage"
	cooldown := 30 * time.Minute
	t0 := time.Now().UTC()

	active := &DetectZombiePolecatsResult{Zombies: []ZombieResult{activeZombie("capable", "ZombieSessionDeadActive", "ta-bujl")}}
	if got := FilterFreshPolecatDeaths(town, town, rig, active, cooldown, t0); len(got) != 1 {
		t.Fatalf("first escalation expected, got %d", len(got))
	}

	// Polecat recovered (not active) — should clear state.
	inactive := &DetectZombiePolecatsResult{Zombies: []ZombieResult{{PolecatName: "capable", WasActive: false}}}
	if got := FilterFreshPolecatDeaths(town, town, rig, inactive, cooldown, t0.Add(2*time.Minute)); len(got) != 0 {
		t.Fatalf("inactive zombie must not escalate, got %d", len(got))
	}
	if readPolecatDiedState(town, rig, "capable") != nil {
		t.Errorf("inactive zombie must clear prior cooldown state")
	}

	// Dies again shortly after — must escalate immediately (state was cleared),
	// not be suppressed by the original cooldown window.
	if got := FilterFreshPolecatDeaths(town, town, rig, active, cooldown, t0.Add(3*time.Minute)); len(got) != 1 {
		t.Fatalf("re-death after recovery must escalate immediately, got %d", len(got))
	}
}

// TestFilterFreshPolecatDeaths_DisabledCooldownAlwaysEscalates verifies the
// operator opt-out: cooldown=0 re-escalates every cycle (pre-gu-b4b39 behavior).
func TestFilterFreshPolecatDeaths_DisabledCooldownAlwaysEscalates(t *testing.T) {
	town := t.TempDir()
	rig := "talontriage"
	res := &DetectZombiePolecatsResult{Zombies: []ZombieResult{activeZombie("capable", "ZombieSessionDeadActive", "ta-bujl")}}
	t0 := time.Now().UTC()
	for i := 0; i < 3; i++ {
		got := FilterFreshPolecatDeaths(town, town, rig, res, 0, t0.Add(time.Duration(i)*time.Minute))
		if len(got) != 1 {
			t.Fatalf("cooldown=0 must escalate every cycle, cycle %d got %d", i, len(got))
		}
	}
}

// TestFilterFreshPolecatDeaths_NilResult verifies a nil detection result is safe.
func TestFilterFreshPolecatDeaths_NilResult(t *testing.T) {
	town := t.TempDir()
	if got := FilterFreshPolecatDeaths(town, town, "rig", nil, time.Minute, time.Now().UTC()); got != nil {
		t.Errorf("nil result must yield nil notices, got %v", got)
	}
}

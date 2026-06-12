package daemon

import (
	"errors"
	"testing"
	"time"
)

// TestFailureSignature covers the hq-6qnct dedup key: failing-gate names are
// the signature (order-independent, deduped), with a digit-normalized first
// line as the fallback for gate-less errors.
func TestFailureSignature(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{
			"single gate",
			errors.New(`gate "test": exit status 1`),
			"gates:test",
		},
		{
			"multiple gates sorted+deduped",
			errors.New(`gate "vet": boom; gate "test": bad; gate "test": bad again`),
			"gates:test,vet",
		},
		{
			"gate order does not change signature",
			errors.New(`gate "test": x; gate "build": y`),
			"gates:build,test",
		},
		{
			"no gate name falls back to digit-normalized first line",
			errors.New("test failed: exit status 137\nsome long pytest tail"),
			"msg:test failed: exit status 000",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := failureSignature(tc.err); got != tc.want {
				t.Errorf("failureSignature() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFlakeWatermarkAndDedup proves the full hq-6qnct lifecycle against the
// real on-disk state: a single flake never pages, a sustained red pages exactly
// once, a recovery re-arms, and a new signature re-pages.
func TestFlakeWatermarkAndDedup(t *testing.T) {
	town := t.TempDir()
	rigName := "lia_bac"
	const threshold = 2
	now := time.Now()
	sigA := `gates:test_patient_watcher`
	sigB := `gates:test_sandbox`

	fail := func(sig string) (bool, int) {
		return recordFailureAndShouldEscalate(town, rigName, sig, "deadbeef", threshold, false, now)
	}
	pass := func() {
		recordAttributionRun(town, rigName, "deadbeef", true, now)
	}

	// The bead's "passes 3 of 4 cycles" flake: a lone failure between passes
	// must stay at streak 1 and never page.
	pass()
	if esc, streak := fail(sigA); esc || streak != 1 {
		t.Fatalf("single flake: escalate=%v streak=%d, want false/1", esc, streak)
	}
	pass()
	if got := loadMainBranchTestState(town).Rigs[rigName].ConsecutiveFailures; got != 0 {
		t.Fatalf("a pass must reset the streak; got %d", got)
	}

	// Sustained red: fail twice in a row → page exactly once at the watermark,
	// then dedup the same signature on the next cycle.
	if esc, streak := fail(sigA); esc || streak != 1 {
		t.Fatalf("first sustained fail: escalate=%v streak=%d, want false/1", esc, streak)
	}
	if esc, streak := fail(sigA); !esc || streak != 2 {
		t.Fatalf("watermark fail: escalate=%v streak=%d, want true/2", esc, streak)
	}
	if esc, streak := fail(sigA); esc || streak != 3 {
		t.Fatalf("dedup same signature: escalate=%v streak=%d, want false/3", esc, streak)
	}

	// A NEW failing signature while already red is a different break → re-page.
	if esc, _ := fail(sigB); !esc {
		t.Fatalf("new signature must re-page even mid-red")
	}

	// Recovery clears the escalated-signature; a later re-break of the SAME
	// original signature pages again (not suppressed across a recovery).
	pass()
	if esc, _ := fail(sigA); esc {
		t.Fatalf("first fail after recovery must be below watermark")
	}
	if esc, _ := fail(sigA); !esc {
		t.Fatalf("re-break after recovery must page again at the watermark")
	}
}

// TestRedMainBackoff proves the gs-3pe circuit-breaker: once main is confirmed
// red on a SHA (streak reaches the watermark), the runner backs off re-running
// the heavyweight gate suite on that same SHA — but a new commit, or a recovery,
// re-arms a real run.
func TestRedMainBackoff(t *testing.T) {
	town := t.TempDir()
	rigName := "lia_bac"
	const threshold = 2
	now := time.Now()
	const shaX = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const shaY = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	sig := "gates:test"

	failX := func() { recordFailureAndShouldEscalate(town, rigName, sig, shaX, threshold, false, now) }

	// Below the watermark we must NOT back off — the first `threshold` cycles
	// run so a single flake never wedges the runner into a permanent skip.
	failX()
	if shouldBackOffOnRedMain(town, rigName, shaX, threshold) {
		t.Fatalf("backed off at streak 1 (below watermark) — would never confirm red")
	}

	// At the watermark, main is confirmed red at shaX → back off on the same SHA.
	failX()
	if !shouldBackOffOnRedMain(town, rigName, shaX, threshold) {
		t.Fatalf("did not back off after confirmed red at shaX")
	}

	// A NEW commit (shaY) must re-arm a real run even while we're red.
	if shouldBackOffOnRedMain(town, rigName, shaY, threshold) {
		t.Fatalf("backed off on a NEW commit — would never re-check a fix")
	}

	// An empty SHA (attribution capture failed) fails open: run, don't skip.
	if shouldBackOffOnRedMain(town, rigName, "", threshold) {
		t.Fatalf("backed off with no SHA to anchor on")
	}

	// Recovery clears the anchor: a subsequent failure at shaX is back below the
	// watermark and runs again rather than being mistaken for still-red.
	recordAttributionRun(town, rigName, shaX, true, now)
	if shouldBackOffOnRedMain(town, rigName, shaX, threshold) {
		t.Fatalf("backed off immediately after a recovery pass")
	}
	failX()
	if shouldBackOffOnRedMain(town, rigName, shaX, threshold) {
		t.Fatalf("backed off at streak 1 after recovery (below watermark)")
	}
}

// TestRevertEscalationMarkersUnburiesFailedPage proves the gu-yl2av fix: when
// the batched escalation fails AFTER recordFailureAndShouldEscalate has already
// persisted a rig's page markers, reverting to the start-of-cycle snapshot lets
// the NEXT cycle both re-escalate (dedup undone) and re-run the gate suite
// (gs-3pe backoff undone) instead of burying the still-red main forever.
func TestRevertEscalationMarkersUnburiesFailedPage(t *testing.T) {
	town := t.TempDir()
	rigName := "lia_bac"
	const threshold = 2
	now := time.Now()
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sig := "gates:test"

	// Cycle 1: first red, below the watermark — no page attempted, streak 1.
	if esc, _ := recordFailureAndShouldEscalate(town, rigName, sig, sha, threshold, false, now); esc {
		t.Fatalf("cycle 1 below watermark must not escalate")
	}

	// Snapshot the state as runMainBranchTests does at the START of cycle 2,
	// before recordFailureAndShouldEscalate mutates it.
	preCycle := loadMainBranchTestState(town).Rigs

	// Cycle 2: reaches the watermark → would page. Markers (LastEscalatedSignature,
	// streak=2, LastFailedSHA) are persisted here, BEFORE the batched escalate.
	if esc, streak := recordFailureAndShouldEscalate(town, rigName, sig, sha, threshold, false, now); !esc || streak != 2 {
		t.Fatalf("cycle 2 at watermark: escalate=%v streak=%d, want true/2", esc, streak)
	}
	// With markers persisted, a SECOND attempt this cycle would be deduped AND the
	// backoff would now skip the suite — exactly the burial the bug describes.
	if !shouldBackOffOnRedMain(town, rigName, sha, threshold) {
		t.Fatalf("precondition: confirmed-red SHA should back off before the revert")
	}

	// The batched escalate fails → revert the escalated rig to the snapshot.
	revertEscalationMarkers(town, []string{rigName}, preCycle)

	// The revert must restore the pre-cycle entry exactly (streak back to 1,
	// LastEscalatedSignature cleared).
	entry := loadMainBranchTestState(town).Rigs[rigName]
	if entry.ConsecutiveFailures != 1 {
		t.Fatalf("after revert ConsecutiveFailures=%d, want 1 (restored to pre-cycle)", entry.ConsecutiveFailures)
	}
	if entry.LastEscalatedSignature != "" {
		t.Fatalf("after revert LastEscalatedSignature=%q, want \"\" (page never landed)", entry.LastEscalatedSignature)
	}
	// gs-3pe backoff must NOT fire — streak is back below the watermark, so the
	// next cycle re-runs the suite instead of skipping a genuinely-red main.
	if shouldBackOffOnRedMain(town, rigName, sha, threshold) {
		t.Fatalf("after revert the runner must NOT back off — the failure was never paged")
	}

	// Next cycle (cycle 2 retried): the same red re-reaches the watermark and
	// re-escalates, proving the failure was unburied rather than deduped away.
	if esc, streak := recordFailureAndShouldEscalate(town, rigName, sig, sha, threshold, false, now); !esc || streak != 2 {
		t.Fatalf("retry cycle: escalate=%v streak=%d, want true/2 (re-pages after revert)", esc, streak)
	}
}

// TestRevertEscalationMarkersFirstEverFailure proves the snapshot restore is
// correct even when the escalated rig had NO prior on-disk state — its
// pre-cycle entry is the zero value, so the revert wipes the rig back to
// "never seen", which is what the next cycle would observe had this cycle never
// run. Guards against a nil-map / missing-key panic in the revert path.
func TestRevertEscalationMarkersFirstEverFailure(t *testing.T) {
	town := t.TempDir()
	rigName := "lia_bac"
	const threshold = 1 // watermark 1 → first failure pages immediately
	now := time.Now()
	const sha = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	sig := "gates:test"

	// No prior state on disk — snapshot is empty.
	preCycle := loadMainBranchTestState(town).Rigs
	if _, ok := preCycle[rigName]; ok {
		t.Fatalf("precondition: rig must have no pre-cycle entry")
	}

	if esc, _ := recordFailureAndShouldEscalate(town, rigName, sig, sha, threshold, false, now); !esc {
		t.Fatalf("first failure at threshold 1 must escalate")
	}

	revertEscalationMarkers(town, []string{rigName}, preCycle)

	entry := loadMainBranchTestState(town).Rigs[rigName]
	if entry.ConsecutiveFailures != 0 || entry.LastEscalatedSignature != "" || entry.LastFailedSHA != "" {
		t.Fatalf("first-ever failure must revert to a zero entry, got %+v", entry)
	}
}

// TestRevertEscalationMarkersEmptyIsNoOp ensures the revert is a safe no-op when
// no rig escalated (the common case: failures all below the watermark), so the
// helper never disturbs accumulating streaks.
func TestRevertEscalationMarkersEmptyIsNoOp(t *testing.T) {
	town := t.TempDir()
	rigName := "lia_bac"
	const threshold = 2
	now := time.Now()
	const sha = "cccccccccccccccccccccccccccccccccccccccc"
	sig := "gates:test"

	recordFailureAndShouldEscalate(town, rigName, sig, sha, threshold, false, now)
	before := loadMainBranchTestState(town).Rigs[rigName]

	revertEscalationMarkers(town, nil, loadMainBranchTestState(town).Rigs)

	after := loadMainBranchTestState(town).Rigs[rigName]
	if before != after {
		t.Fatalf("empty revert must not change state: before=%+v after=%+v", before, after)
	}
}

// TestFlakeThresholdConfig verifies the tunable + default resolution.
func TestFlakeThresholdConfig(t *testing.T) {
	if got := mainBranchTestFlakeThreshold(nil); got != defaultMainBranchTestFlakeThreshold {
		t.Errorf("nil config = %d, want default %d", got, defaultMainBranchTestFlakeThreshold)
	}
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{MainBranchTest: &MainBranchTestConfig{FlakeThreshold: 5}}}
	if got := mainBranchTestFlakeThreshold(cfg); got != 5 {
		t.Errorf("configured threshold = %d, want 5", got)
	}
	// A nonsense threshold (<1) falls back to the default rather than wedging
	// escalation off entirely.
	cfg.Patrols.MainBranchTest.FlakeThreshold = 0
	if got := mainBranchTestFlakeThreshold(cfg); got != defaultMainBranchTestFlakeThreshold {
		t.Errorf("zero threshold = %d, want default %d", got, defaultMainBranchTestFlakeThreshold)
	}
}

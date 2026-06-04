package daemon

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/curio"
)

func newCurioTestDaemon(t *testing.T, pageForReal bool) *Daemon {
	t.Helper()
	return &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(io.Discard, "", 0),
		patrolConfig: &DaemonPatrolConfig{Patrols: &PatrolsConfig{
			Curio: &CurioConfig{Enabled: true, PageForReal: pageForReal},
		}},
		curioPaging: curio.NewPagingEngine(),
	}
}

func TestCurioPageForReal_DefaultsShadow(t *testing.T) {
	// Absent config => shadow mode (no real paging).
	d := &Daemon{}
	if d.curioPageForReal() {
		t.Error("PageForReal must default to false (shadow mode) with no config")
	}
	// Curio config present but page_for_real unset => still shadow.
	d2 := &Daemon{patrolConfig: &DaemonPatrolConfig{Patrols: &PatrolsConfig{Curio: &CurioConfig{Enabled: true}}}}
	if d2.curioPageForReal() {
		t.Error("PageForReal must default to false when unset in config")
	}
}

func TestEmitCurioPages_ShadowModeDoesNotPage(t *testing.T) {
	d := newCurioTestDaemon(t, false /* shadow */)

	// Swap the page hook to detect any real page attempt.
	orig := pageOverseer
	paged := 0
	pageOverseer = func(_ *Daemon, _ PageAction) { paged++ }
	defer func() { pageOverseer = orig }()

	actions := []PageAction{{
		Kind: curio.ActionVerifiedPage, Lane: curio.LaneVerified,
		Severity: "critical", DedupKey: "curio:verified:x", Summary: "leak holds",
	}}
	// Shadow-ledger write will fail (no Dolt in unit test) but must not panic or
	// page; doltBreaker is nil-safe (Allow returns true), OpenStore errors out.
	d.doltBreaker = NewDoltCircuitBreaker()
	d.emitCurioPages("win-1", actions)

	if paged != 0 {
		t.Errorf("SHADOW MODE must NOT page the Overseer, paged=%d", paged)
	}
}

func TestEmitCurioPages_PageForRealPages(t *testing.T) {
	d := newCurioTestDaemon(t, true /* live */)
	d.doltBreaker = NewDoltCircuitBreaker()

	orig := pageOverseer
	var got []PageAction
	pageOverseer = func(_ *Daemon, a PageAction) { got = append(got, a) }
	defer func() { pageOverseer = orig }()

	actions := []PageAction{
		{Kind: curio.ActionVerifiedPage, Lane: curio.LaneVerified, Severity: "critical", DedupKey: "k1", Summary: "leak"},
		{Kind: curio.ActionJudgmentTrip, Lane: curio.LaneJudgment, Severity: "high", DedupKey: "k2", Summary: "judgment"},
	}
	d.emitCurioPages("win-1", actions)

	if len(got) != 2 {
		t.Fatalf("PageForReal must raise one page per action, got %d", len(got))
	}
	if got[0].DedupKey != "k1" || got[1].DedupKey != "k2" {
		t.Errorf("page dedup keys mismatch: %+v", got)
	}
}

func TestEmitCurioPages_RefreshesHeartbeat(t *testing.T) {
	d := newCurioTestDaemon(t, false)
	d.doltBreaker = NewDoltCircuitBreaker()

	orig := pageOverseer
	pageOverseer = func(_ *Daemon, _ PageAction) {}
	defer func() { pageOverseer = orig }()

	// Even a zero-action cycle must refresh the heartbeat.
	d.emitCurioPages("win-empty", nil)

	hbPath := filepath.Join(d.config.TownRoot, ".runtime", curioHeartbeatFile)
	data, err := os.ReadFile(hbPath) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("heartbeat must be written even on a zero-action cycle: %v", err)
	}
	var hb curioHeartbeat
	if err := json.Unmarshal(data, &hb); err != nil {
		t.Fatalf("heartbeat must be valid JSON: %v", err)
	}
	if hb.WindowID != "win-empty" {
		t.Errorf("heartbeat window mismatch: %q", hb.WindowID)
	}
	if !hb.ShadowMode {
		t.Error("heartbeat should record shadow mode when PageForReal is off")
	}
	if hb.BreakerState != "armed" {
		t.Errorf("fresh engine breaker should be armed, got %q", hb.BreakerState)
	}
}

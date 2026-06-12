package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

// Feed-storm rate monitor (gc-wwpw2). Follow-up to gu-q1wzq.
//
// THE GAP IT CLOSES: the 2026-06-04 convoy re-dispatch storm re-fed beads that
// could never succeed (~6,508 sling-failures/day), burning ~154 Dolt CPU-hrs and
// briefly STARVING THE DOLT DATA PLANE TO DEATH (load peaked ~1200, TCP connects
// refused) — and NOTHING noticed until a human investigated hours later. The two
// existing safety monitors are both structurally blind to it:
//   - monitorCapacityExhaustion watches a DEAD pool (no free slots). A re-feed
//     loop that never PLACES work doesn't move pool metrics, so it never trips.
//   - the per-bead respawn circuit breaker counts on RecordBeadRespawn, which
//     runs AFTER spawn. 100% of storm failures were PRE-spawn rejections
//     (already-hooked/in_progress, do-not-dispatch tripwire, not-found, closed),
//     so the breaker's counter never incremented.
//
// This monitor watches the one signal that actually moved: the per-scan convoy
// sling-FAILURE count. A single noisy scan is normal (a bead closes mid-scan, a
// rig parks); a SUSTAINED high failure count across consecutive scans is the
// storm signature. It mirrors the proven evaluateCapacityExhaustion state machine
// (pure fn + JSON state persisted across daemon restarts + threshold + HIGH
// escalate with a stable fingerprint so gt escalate's close-aware dedup suppresses
// repeats within an episode).

// feedStormFailureThreshold is the per-scan sling-failure count above which a
// scan is considered "storming". At ~9:1 sling:unique amplification observed in
// gu-q1wzq, even a handful of stuck beads produces double-digit failures per
// scan; a healthy town sees 0-2 (occasional TOCTOU closes). 10 is well clear of
// normal noise but trips long before the 6,500/day runaway. Tunable for tests.
const feedStormFailureThreshold = 10

// feedStormConsecutiveThreshold is the number of CONSECUTIVE storming scans
// before escalating. The stranded scan runs ~every 30s, so 4 consecutive scans
// is ~2min of sustained failure — long enough to ignore a transient spike (a
// Dolt restart making many beads briefly invisible), short enough to catch a real
// storm in minutes rather than the hours gu-q1wzq ran undetected.
const feedStormConsecutiveThreshold = 4

// feedStormState persists across daemon restarts in
// <town>/.runtime/feed-storm.json so a storm that survives a restart (gu-q1wzq
// was structural — it did) keeps accumulating toward escalation rather than
// resetting its counter every restart.
type feedStormState struct {
	Consecutive int    `json:"consecutive"`
	FirstSeen   string `json:"first_seen,omitempty"`
	PeakPerScan int    `json:"peak_per_scan,omitempty"`
	Escalated   bool   `json:"escalated"`
}

// evaluateFeedStorm is the pure state machine: given the prior state, this scan's
// sling-failure count, and a timestamp for a fresh episode, it returns the next
// state and whether THIS scan should fire an escalation (true only on the scan
// that first crosses the consecutive threshold within an episode). A scan below
// the per-scan failure threshold re-arms the monitor (resets to zero), so only
// SUSTAINED storms escalate, and a recovered town immediately re-arms to catch
// the next episode.
func evaluateFeedStorm(prev feedStormState, slingFailures int, now string) (feedStormState, bool) {
	if slingFailures < feedStormFailureThreshold {
		return feedStormState{}, false // not storming this scan → re-arm
	}
	next := prev
	next.Consecutive++
	if next.FirstSeen == "" {
		next.FirstSeen = now
	}
	if slingFailures > next.PeakPerScan {
		next.PeakPerScan = slingFailures
	}
	escalate := next.Consecutive >= feedStormConsecutiveThreshold && !next.Escalated
	if escalate {
		next.Escalated = true
	}
	return next, escalate
}

func feedStormStatePath(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "feed-storm.json")
}

func loadFeedStormState(path string) feedStormState {
	var st feedStormState
	data, err := os.ReadFile(path) //nolint:gosec // G304: path constructed internally
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st)
	return st
}

func saveFeedStormState(path string, st feedStormState) {
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	if data, err := json.Marshal(st); err == nil {
		_ = os.WriteFile(path, data, 0644) //nolint:gosec // G306: non-secret monitor state
	}
}

// monitorFeedStorm advances the consecutive-storm counter for this scan and
// escalates HIGH when convoy sling-failures stay above threshold for
// feedStormConsecutiveThreshold consecutive scans. Best-effort: state and
// escalation failures never block the scan. Overridable escalation hook
// (fireFeedStormEscalation) lets tests assert without shelling out.
func (m *ConvoyManager) monitorFeedStorm(slingFailures int) {
	path := feedStormStatePath(m.townRoot)
	next, escalate := evaluateFeedStorm(loadFeedStormState(path), slingFailures, m.now().UTC().Format(time.RFC3339))
	if escalate {
		if err := m.fireFeedStormEscalation(next, slingFailures); err != nil {
			// Escalation failed — clear the Escalated marker so the next sustained
			// scan retries instead of silently burying the storm (gu-nid89.43).
			// The consecutive counter and FirstSeen are preserved so the episode
			// keeps building toward the next escalation attempt.
			m.logger("Convoy feed-storm escalation failed, will retry: %s", err)
			next.Escalated = false
		}
	}
	saveFeedStormState(path, next)
}

// fireFeedStormEscalation raises a HIGH escalation to the Mayor describing the
// sustained convoy feed storm. The fingerprint lets gt escalate's close-aware
// dedup suppress repeats within an open episode. A package-level var so tests can
// stub it.
// Returns an error when `gt escalate` fails so the caller can avoid marking the
// storm handled and retry on the next sustained scan (gu-nid89.43).
var fireFeedStormEscalation = func(m *ConvoyManager, st feedStormState, slingFailures int) error {
	msg := fmt.Sprintf("Convoy feed storm: %d sling-failures this scan, sustained for %d consecutive scans (since %s, peak %d/scan). Likely terminal-fail beads being re-fed every cycle (tripwire/already-hooked/not-found) — the gu-q1wzq signature. Inspect: grep 'sling .* failed' daemon.log | sort | uniq -c.",
		slingFailures, st.Consecutive, st.FirstSeen, st.PeakPerScan)
	cmd := exec.CommandContext(m.ctx, m.gtPath, "escalate",
		"--severity", "high",
		"--source", "convoy:feed-storm",
		"--fingerprint", "convoy:feed-storm",
		"--reason", "Re-dispatch storm burns CPU + Dolt connections and can starve the data plane (gu-q1wzq killed it once). No pool/respawn monitor catches it — failures are pre-spawn rejections. Find the offending beads and untrack/close them.",
		msg)
	cmd.Dir = m.townRoot
	util.SetProcessGroup(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s", util.FirstLine(stderr.String()))
	}
	return nil
}

// fireFeedStormEscalation calls the package-level hook (indirection for tests).
func (m *ConvoyManager) fireFeedStormEscalation(st feedStormState, slingFailures int) error {
	return fireFeedStormEscalation(m, st, slingFailures)
}

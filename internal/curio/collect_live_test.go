package curio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- (c) alarm_rate_spike: events.jsonl rate collector ---

func TestCollectEventCounts_WindowBoundAndSeriesMap(t *testing.T) {
	start := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	lines := strings.Join([]string{
		// in-window, mapped series
		`{"ts":"2026-05-20T01:00:00Z","type":"sling","actor":"scheduler"}`,
		`{"ts":"2026-05-20T02:00:00Z","type":"sling","actor":"scheduler"}`,
		`{"ts":"2026-05-20T03:00:00Z","type":"escalation_sent","actor":"witness"}`,
		`{"ts":"2026-05-20T04:00:00Z","type":"scheduler_dispatch_failed","actor":"daemon"}`,
		`{"ts":"2026-05-20T05:00:00Z","type":"dispatch.stuck_agent","actor":"deacon"}`,
		// before window — excluded
		`{"ts":"2026-05-19T23:00:00Z","type":"sling","actor":"scheduler"}`,
		// at end (exclusive) — excluded
		`{"ts":"2026-05-21T00:00:00Z","type":"sling","actor":"scheduler"}`,
		// unmapped type — excluded
		`{"ts":"2026-05-20T06:00:00Z","type":"session_death","actor":"daemon"}`,
	}, "\n")

	counts := CollectEventCounts(strings.NewReader(lines), start, end)
	got := map[string]int{}
	for _, c := range counts {
		got[c.Series] = c.Observed
	}
	if got["sling"] != 2 {
		t.Errorf("sling: want 2 (window-bound), got %d", got["sling"])
	}
	if got["escalation"] != 1 {
		t.Errorf("escalation (from escalation_sent): want 1, got %d", got["escalation"])
	}
	if got["sched_fail"] != 1 {
		t.Errorf("sched_fail (from scheduler_dispatch_failed): want 1, got %d", got["sched_fail"])
	}
	if got["dispatch.stuck_agent"] != 1 {
		t.Errorf("dispatch.stuck_agent: want 1, got %d", got["dispatch.stuck_agent"])
	}
	if _, ok := got["session_death"]; ok {
		t.Error("unmapped series should not be counted")
	}
}

// Loop-breaker: Curio's own events are excluded so it cannot detect itself.
func TestCollectEventCounts_ExcludesCurioActor(t *testing.T) {
	start := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	lines := strings.Join([]string{
		`{"ts":"2026-05-20T01:00:00Z","type":"sling","actor":"curio"}`,
		`{"ts":"2026-05-20T02:00:00Z","type":"sling","actor":"scheduler"}`,
	}, "\n")
	counts := CollectEventCounts(strings.NewReader(lines), start, end)
	for _, c := range counts {
		if c.Series == "sling" && c.Observed != 1 {
			t.Errorf("curio's own event should be excluded; want 1 sling, got %d", c.Observed)
		}
	}
}

// Failure mode: truncation / rotation mid-read. A torn/partial final line must
// be skipped (not abort the scan), and counts must never exceed real events.
func TestCollectEventCounts_TruncationCannotInflate(t *testing.T) {
	start := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	// Two valid lines then a torn JSON line (rotation cut it mid-write).
	lines := `{"ts":"2026-05-20T01:00:00Z","type":"done","actor":"refinery"}
{"ts":"2026-05-20T02:00:00Z","type":"done","actor":"refinery"}
{"ts":"2026-05-20T03:00:00Z","type":"do`
	counts := CollectEventCounts(strings.NewReader(lines), start, end)
	var done int
	for _, c := range counts {
		if c.Series == "done" {
			done = c.Observed
		}
	}
	if done != 2 {
		t.Errorf("torn final line must be skipped; want 2 done (raw count, never inflated), got %d", done)
	}
}

func TestCollectEventCounts_SkipsUndatedLines(t *testing.T) {
	start := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	lines := `{"type":"mail","actor":"x"}
{"ts":"2026-05-20T01:00:00Z","type":"mail","actor":"x"}`
	counts := CollectEventCounts(strings.NewReader(lines), start, end)
	var mail int
	for _, c := range counts {
		if c.Series == "mail" {
			mail = c.Observed
		}
	}
	if mail != 1 {
		t.Errorf("undated line should be skipped; want 1 mail, got %d", mail)
	}
}

func TestCollectEventCountsFromFile_MissingIsEmpty(t *testing.T) {
	counts, err := collectEventCountsFromFile(filepath.Join(t.TempDir(), "nope.jsonl"),
		time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("want 0 counts for missing file, got %d", len(counts))
	}
}

// End-to-end: the alarm-flood anchor pattern fires the rate rule.
func TestCollectEventCounts_AnchorFloodFiresRule(t *testing.T) {
	start := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	var sb strings.Builder
	// 123 dispatch.stuck_agent events (rare-event series, threshold 0).
	for i := 0; i < 123; i++ {
		sb.WriteString(`{"ts":"2026-05-22T01:00:00Z","type":"dispatch.stuck_agent","actor":"deacon"}` + "\n")
	}
	counts := CollectEventCounts(strings.NewReader(sb.String()), start, end)
	in := Input{Window: Window{ID: "w"}, EventCounts: counts}
	cands := Evaluate(DefaultRules(), in)
	if len(cands) != 1 || cands[0].RuleID != "alarm_rate_spike" {
		t.Fatalf("expected 1 alarm_rate_spike candidate, got %+v", cands)
	}
	if cands[0].Observed != 123 {
		t.Errorf("expected observed=123, got %d", cands[0].Observed)
	}
}

// --- (b) kill_signal_near_dolt: dog-log collector ---

func TestCollectKillSignals_AnchorLineFires(t *testing.T) {
	dir := t.TempDir()
	content := strings.Join([]string{
		"routine heartbeat ok",
		"sending kill -QUIT to dolt sql-server pid 12345 per documented protocol",
		"another normal line",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "reaper.log"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	lines, err := CollectKillSignals(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("want 1 kill-signal line, got %d: %+v", len(lines), lines)
	}
	if !lines[0].NearDoltPID {
		t.Error("anchor line names dolt sql-server pid → NearDoltPID should be true")
	}
	if lines[0].Source != "reaper" {
		t.Errorf("source should be the log basename, got %q", lines[0].Source)
	}

	// End-to-end: it fires the rule.
	in := Input{Window: Window{ID: "w"}, LogLines: lines}
	cands := Evaluate(DefaultRules(), in)
	if len(cands) != 1 || cands[0].RuleID != "kill_signal_near_dolt" {
		t.Errorf("expected 1 kill_signal_near_dolt candidate, got %+v", cands)
	}
}

func TestCollectKillSignals_KillSignalNotNearDolt(t *testing.T) {
	dir := t.TempDir()
	// A kill signal that is NOT about Dolt — collected (it's a kill signal) but
	// NearDoltPID=false, so the rule does not fire.
	content := "received SIGKILL for stale polecat session gt-foo"
	if err := os.WriteFile(filepath.Join(dir, "deacon.log"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	lines, err := CollectKillSignals(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].NearDoltPID {
		t.Fatalf("kill-not-near-dolt: want 1 line w/ NearDoltPID=false, got %+v", lines)
	}
	cands := Evaluate(DefaultRules(), Input{Window: Window{ID: "w"}, LogLines: lines})
	if len(cands) != 0 {
		t.Errorf("rule must not fire when signal is not near a Dolt PID, got %+v", cands)
	}
}

func TestCollectKillSignals_KnownPIDPrecision(t *testing.T) {
	dir := t.TempDir()
	content := strings.Join([]string{
		"kill -QUIT dolt server pid 999",   // dolt mention but PID not in known set
		"kill -QUIT dolt server pid 12345", // matches known PID
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "reaper.log"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	lines, err := CollectKillSignals(dir, []int{12345})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("both kill lines collected, got %d", len(lines))
	}
	var near, notNear int
	for _, l := range lines {
		if l.NearDoltPID {
			near++
		} else {
			notNear++
		}
	}
	if near != 1 || notNear != 1 {
		t.Errorf("with known PID 12345: want exactly 1 near / 1 not-near, got near=%d notNear=%d", near, notNear)
	}
}

func TestLineReferencesPID_TokenBoundary(t *testing.T) {
	if lineReferencesPID("pid 12345 killed", 1234) {
		t.Error("123 should not match inside 12345 — needs token boundary")
	}
	if !lineReferencesPID("pid 1234 killed", 1234) {
		t.Error("standalone 1234 should match")
	}
	if !lineReferencesPID("1234", 1234) {
		t.Error("PID at string bounds should match")
	}
}

// Failure mode: missing log dir / unreadable files. Missing dir → no error.
func TestCollectKillSignals_MissingDirIsEmpty(t *testing.T) {
	lines, err := CollectKillSignals(filepath.Join(t.TempDir(), "nope"), nil)
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("want 0 lines, got %d", len(lines))
	}
}

// Failure mode: non-.log files and subdirs are ignored (log rotation often
// leaves .log.1 / .gz siblings — we scan only live *.log).
func TestCollectKillSignals_IgnoresNonLogAndDirs(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "reaper.log.1"), []byte("kill -QUIT dolt pid 1"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("kill -QUIT dolt pid 1"), 0644)
	_ = os.MkdirAll(filepath.Join(dir, "sub.log"), 0755)
	lines, err := CollectKillSignals(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 0 {
		t.Errorf("only live *.log files scanned; want 0, got %d: %+v", len(lines), lines)
	}
}

// --- (a) bead_merged_not_landed: dual-source collector ---

func TestResolveMergedBeads_DedupAcrossSources(t *testing.T) {
	// Same bead present in both sources (straddles the 05-25 boundary). Must be
	// counted ONCE (no double-count), first-source-wins.
	doltSrc := []MergedBeadObservation{{ID: "gu-1", Rig: "gastown", Commit: "deadbeef"}}
	otelSrc := []MergedBeadObservation{{ID: "gu-1", Rig: "gastown", Commit: "deadbeef"}}
	resolve := func(rig, commit string) bool { return false } // not landed

	recs := ResolveMergedBeads([][]MergedBeadObservation{doltSrc, otelSrc}, resolve)
	if len(recs) != 1 {
		t.Fatalf("dedup across sources: want 1 record, got %d", len(recs))
	}
	if recs[0].CloseReason != "merged" || recs[0].CommitInMainAncestry {
		t.Errorf("record mis-resolved: %+v", recs[0])
	}
}

func TestResolveMergedBeads_UnionNoGap(t *testing.T) {
	// A pre-05-25 bead present only in bead Dolt (absent from OTel) must still
	// be observed — the union prevents a gap at the instrumentation boundary.
	doltSrc := []MergedBeadObservation{{ID: "gu-old", Rig: "gastown", Commit: "abc"}}
	otelSrc := []MergedBeadObservation{{ID: "gu-new", Rig: "gastown", Commit: "def"}}
	resolve := func(rig, commit string) bool { return false }

	recs := ResolveMergedBeads([][]MergedBeadObservation{doltSrc, otelSrc}, resolve)
	if len(recs) != 2 {
		t.Fatalf("union of sources: want 2 records, got %d", len(recs))
	}
	// Sorted by ID for determinism.
	if recs[0].ID != "gu-new" || recs[1].ID != "gu-old" {
		t.Errorf("records should be sorted by ID, got %s,%s", recs[0].ID, recs[1].ID)
	}
}

func TestResolveMergedBeads_AncestryGate(t *testing.T) {
	src := []MergedBeadObservation{
		{ID: "gu-landed", Rig: "gastown", Commit: "in-main"},
		{ID: "gu-lost", Rig: "gastown", Commit: "not-in-main"},
		{ID: "gu-nocommit", Rig: "gastown", Commit: ""}, // empty commit → suspicious
	}
	resolve := func(rig, commit string) bool { return commit == "in-main" }
	recs := ResolveMergedBeads([][]MergedBeadObservation{src}, resolve)

	in := Input{Window: Window{ID: "w"}, Beads: recs}
	cands := Evaluate(DefaultRules(), in)
	fired := map[string]bool{}
	for _, c := range cands {
		fired[c.Target] = true
	}
	if fired["gu-landed"] {
		t.Error("a merged bead whose commit IS in main ancestry must not fire")
	}
	if !fired["gu-lost"] {
		t.Error("a merged bead whose commit is NOT in main ancestry must fire")
	}
	if !fired["gu-nocommit"] {
		t.Error("a merged bead with no recorded commit must fire (suspicious)")
	}
}

func TestResolveMergedBeads_NilResolverTreatsAllNotLanded(t *testing.T) {
	src := []MergedBeadObservation{{ID: "gu-1", Rig: "gastown", Commit: "abc"}}
	recs := ResolveMergedBeads([][]MergedBeadObservation{src}, nil)
	if len(recs) != 1 || recs[0].CommitInMainAncestry {
		t.Errorf("nil resolver should treat commit as not-landed (conservative): %+v", recs)
	}
}

func TestResolveMergedBeads_SkipsEmptyIDs(t *testing.T) {
	src := []MergedBeadObservation{{ID: "", Rig: "gastown", Commit: "abc"}}
	recs := ResolveMergedBeads([][]MergedBeadObservation{src}, nil)
	if len(recs) != 0 {
		t.Errorf("empty-ID observation should be skipped, got %+v", recs)
	}
}

func TestGitAncestryResolver_EmptyCommitAndUnknownRig(t *testing.T) {
	resolve := GitAncestryResolver(func(rig string) string { return "" })
	if resolve("gastown", "") {
		t.Error("empty commit must resolve to false")
	}
	if resolve("unknown-rig", "deadbeef") {
		t.Error("unknown rig (empty dir) must resolve to false")
	}
}

// --- CollectInputWith wiring ---

func TestCollectInputWith_WiresAllSources(t *testing.T) {
	townRoot := t.TempDir()

	// events.jsonl with one in-window mapped event
	start := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	// dispatch.stuck_agent keeps a threshold-0 floor post-calibration (gc-e2uvyr.3),
	// so a single in-window event still fires the rate rule end-to-end. escalation
	// now carries a calibrated ceiling and would not fire on one event.
	evLine := `{"ts":"2026-05-20T01:00:00Z","type":"dispatch.stuck_agent","actor":"deacon"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".events.jsonl"), []byte(evLine), 0644); err != nil {
		t.Fatal(err)
	}

	// daemon/<dog>.log with an anchor kill-signal line
	logDir := filepath.Join(townRoot, "daemon")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "reaper.log"),
		[]byte("kill -QUIT to dolt sql-server pid 12345"), 0644); err != nil {
		t.Fatal(err)
	}

	opts := CollectOptions{
		Start: start,
		End:   end,
		MergedBeadSources: [][]MergedBeadObservation{{
			{ID: "gu-lost", Rig: "gastown", Commit: "not-landed"},
		}},
		Ancestry: func(rig, commit string) bool { return false },
	}
	in, err := CollectInputWith(townRoot, "win-all", opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.EventCounts) != 1 || in.EventCounts[0].Series != "dispatch.stuck_agent" {
		t.Errorf("event counts not wired: %+v", in.EventCounts)
	}
	if len(in.LogLines) != 1 || !in.LogLines[0].NearDoltPID {
		t.Errorf("log lines not wired: %+v", in.LogLines)
	}
	if len(in.Beads) != 1 || in.Beads[0].ID != "gu-lost" {
		t.Errorf("merged beads not wired: %+v", in.Beads)
	}

	// End-to-end: all three rules fire.
	cands := Evaluate(DefaultRules(), in)
	fired := map[string]bool{}
	for _, c := range cands {
		fired[c.RuleID] = true
	}
	for _, want := range []string{"alarm_rate_spike", "kill_signal_near_dolt", "bead_merged_not_landed"} {
		if !fired[want] {
			t.Errorf("expected rule %s to fire, candidates: %+v", want, cands)
		}
	}
}

func TestCollectInputWith_DefaultWindow(t *testing.T) {
	// Zero Start/End → default last-24h window ending now; window stamped.
	in, err := CollectInputWith(t.TempDir(), "w", CollectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if in.Window.Start.IsZero() || in.Window.End.IsZero() {
		t.Error("default window should stamp Start and End")
	}
	if d := in.Window.End.Sub(in.Window.Start); d < 23*time.Hour || d > 25*time.Hour {
		t.Errorf("default window should be ~24h, got %s", d)
	}
}

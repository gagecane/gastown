package daemon

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- Config and interval tests ---

func TestMRCycleCloseInterval_Default(t *testing.T) {
	if got := mrCycleCloseInterval(nil); got != defaultMRCycleCloseInterval {
		t.Errorf("expected default %v, got %v", defaultMRCycleCloseInterval, got)
	}
}

func TestMRCycleCloseInterval_Custom(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MRCycleClose: &MRCycleCloseConfig{Enabled: true, IntervalStr: "30s"},
		},
	}
	if got := mrCycleCloseInterval(cfg); got != 30*time.Second {
		t.Errorf("expected 30s, got %v", got)
	}
}

func TestMRCycleCloseInterval_Invalid(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MRCycleClose: &MRCycleCloseConfig{Enabled: true, IntervalStr: "not-a-duration"},
		},
	}
	if got := mrCycleCloseInterval(cfg); got != defaultMRCycleCloseInterval {
		t.Errorf("expected default for invalid interval, got %v", got)
	}
}

func TestIsPatrolEnabled_MRCycleClose_NilConfig(t *testing.T) {
	if IsPatrolEnabled(nil, "mr_cycle_close") {
		t.Error("mr_cycle_close should be disabled with nil config (opt-in)")
	}
}

func TestIsPatrolEnabled_MRCycleClose_EmptyPatrols(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if IsPatrolEnabled(cfg, "mr_cycle_close") {
		t.Error("mr_cycle_close should be disabled when not configured")
	}
}

func TestIsPatrolEnabled_MRCycleClose_Enabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MRCycleClose: &MRCycleCloseConfig{Enabled: true},
		},
	}
	if !IsPatrolEnabled(cfg, "mr_cycle_close") {
		t.Error("mr_cycle_close should be enabled when configured true")
	}
}

func TestIsPatrolEnabled_MRCycleClose_ExplicitlyDisabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MRCycleClose: &MRCycleCloseConfig{Enabled: false},
		},
	}
	if IsPatrolEnabled(cfg, "mr_cycle_close") {
		t.Error("mr_cycle_close should be disabled when explicitly set false")
	}
}

// --- Parsing helper tests ---

func TestExtractRigFromLabels(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{"no labels", nil, ""},
		{"no rig label", []string{"gt:auto-test-pr", "gt:merge-request"}, ""},
		{"rig label first", []string{"rig:gastown_upstream", "gt:auto-test-pr"}, "gastown_upstream"},
		{"rig label later", []string{"gt:auto-test-pr", "rig:beads"}, "beads"},
		{"compact form", []string{"rig:longeye"}, "longeye"},
		{"empty value", []string{"rig:"}, ""},
		{"trailing whitespace", []string{"rig:gastown_upstream  "}, "gastown_upstream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractRigFromLabels(tt.labels); got != tt.want {
				t.Errorf("extractRigFromLabels(%v) = %q, want %q", tt.labels, got, tt.want)
			}
		})
	}
}

func TestExtractCloseReasonFromBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"empty body", "", ""},
		{
			"close_reason on its own line",
			"branch: polecat/foo\ntarget: main\nclose_reason: merged\n",
			"merged",
		},
		{
			"close_reason mid-body",
			"branch: polecat/foo\nclose_reason: rejected\nrig: gastown_upstream",
			"rejected",
		},
		{
			"case-insensitive key",
			"Close_Reason: superseded",
			"superseded",
		},
		{
			"missing field",
			"branch: polecat/foo\ntarget: main",
			"",
		},
		{
			"close_reason value with trailing spaces",
			"close_reason:   conflict   ",
			"conflict",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractCloseReasonFromBody(tt.body); got != tt.want {
				t.Errorf("extractCloseReasonFromBody(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestSanitizeLabelValue(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"merged", "merged"},
		{"Merged", "merged"},
		{"closed-unmerged", "closed-unmerged"},
		{"weird value!", "weird-value-"},
		{"  trim  ", "trim"},
		{"", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := sanitizeLabelValue(tt.in); got != tt.want {
				t.Errorf("sanitizeLabelValue(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMRCycleCloseAckLabel(t *testing.T) {
	if got := mrCycleCloseAckLabel("merged"); got != "fingerprint:cycle-close-merged" {
		t.Errorf("ack label for merged = %q", got)
	}
	if got := mrCycleCloseAckLabel("Closed-Unmerged"); got != "fingerprint:cycle-close-closed-unmerged" {
		t.Errorf("ack label for Closed-Unmerged = %q", got)
	}
}

func TestMRCycleCloseFingerprint_StableAcrossInputs(t *testing.T) {
	a := mrCycleCloseFingerprint("gt-mr1", "merged")
	b := mrCycleCloseFingerprint("gt-mr1", "merged")
	if a != b {
		t.Errorf("fingerprint should be stable: %q != %q", a, b)
	}
	if got := mrCycleCloseFingerprint("gt-mr1", "rejected"); got == a {
		t.Errorf("fingerprint should differ by close_reason: %q == %q", got, a)
	}
	if got := mrCycleCloseFingerprint("gt-mr2", "merged"); got == a {
		t.Errorf("fingerprint should differ by mr id: %q == %q", got, a)
	}
}

// --- classifyAndDispatchMRCycleCloseRows tests ---

// mrCycleCaptureLogger collects log lines for assertion.
type mrCycleCaptureLogger struct {
	lines []string
}

func (c *mrCycleCaptureLogger) log(format string, args ...interface{}) {
	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

// recordingHandler captures MRCycleCloseEvents handed to it.
type recordingHandler struct {
	events []MRCycleCloseEvent
}

func (r *recordingHandler) handle(ev MRCycleCloseEvent) {
	r.events = append(r.events, ev)
}

// recordingAck captures (mrID, label) pairs handed to the ack writer.
type recordingAck struct {
	calls []ackCall
	err   error
}

type ackCall struct {
	mrID  string
	label string
}

func (r *recordingAck) ack(mrID, label string) error {
	r.calls = append(r.calls, ackCall{mrID, label})
	return r.err
}

// TestMRCycleCloseDog_AC2_DispatchesOnceAcrossNTicks is the headline
// acceptance test for gu-h1fn: a closed MR with the right labels is
// dispatched exactly once across N ticks. The fixture matches the
// acceptance criterion verbatim — gt:auto-test-pr + rig:gastown_upstream
// + close_reason=merged.
func TestMRCycleCloseDog_AC2_DispatchesOnceAcrossNTicks(t *testing.T) {
	mr := mrCycleCloseRow{
		ID:          "gt-mr-fixture",
		Title:       "Merge polecat/test/gt-foo into main",
		Status:      "closed",
		Description: "branch: polecat/test/gt-foo\ntarget: main\nsource_issue: gt-foo\nrig: gastown_upstream\nclose_reason: merged\n",
		Labels: []string{
			MRCycleCloseAutoTestPRLabel,
			"gt:merge-request",
			"rig:gastown_upstream",
		},
	}

	handler := &recordingHandler{}
	cap := &mrCycleCaptureLogger{}

	// On first tick the ack label is absent, so dispatch fires.
	ack := &recordingAck{}
	d, _, m := classifyAndDispatchMRCycleCloseRows(
		[]mrCycleCloseRow{mr}, handler.handle, ack.ack, cap.log)
	if d != 1 || m != 0 {
		t.Fatalf("first tick: dispatched=%d malformed=%d, want dispatched=1 malformed=0", d, m)
	}
	if len(handler.events) != 1 {
		t.Fatalf("first tick: handler got %d events, want 1", len(handler.events))
	}
	got := handler.events[0]
	if got.MRID != "gt-mr-fixture" || got.TargetRig != "gastown_upstream" || got.CloseReason != "merged" {
		t.Errorf("first tick event: %+v", got)
	}
	if !strings.Contains(got.Body, "close_reason: merged") {
		t.Errorf("event body should include close_reason line: %q", got.Body)
	}
	if len(ack.calls) != 1 {
		t.Fatalf("first tick: %d ack calls, want 1", len(ack.calls))
	}
	if ack.calls[0].mrID != "gt-mr-fixture" {
		t.Errorf("ack mrID = %q", ack.calls[0].mrID)
	}
	if ack.calls[0].label != "fingerprint:cycle-close-merged" {
		t.Errorf("ack label = %q", ack.calls[0].label)
	}

	// Simulate the ack label having been written back: subsequent ticks
	// see the label and skip the bead.
	mrAcked := mr
	mrAcked.Labels = append(append([]string{}, mr.Labels...), "fingerprint:cycle-close-merged")

	for i := 0; i < 5; i++ {
		d, deduped, m := classifyAndDispatchMRCycleCloseRows(
			[]mrCycleCloseRow{mrAcked}, handler.handle, ack.ack, cap.log)
		if d != 0 || deduped != 1 || m != 0 {
			t.Fatalf("tick %d: dispatched=%d deduped=%d malformed=%d, want 0/1/0", i+2, d, deduped, m)
		}
	}
	if len(handler.events) != 1 {
		t.Errorf("after %d follow-up ticks: handler events = %d, want 1 (idempotency lost)",
			5, len(handler.events))
	}
	if len(ack.calls) != 1 {
		t.Errorf("after %d follow-up ticks: ack calls = %d, want 1", 5, len(ack.calls))
	}
}

// TestMRCycleCloseDog_AC3_EventStructHasAllFields ensures the dispatched
// event has the four fields Phase 0 task 3c (gu-xrxm6) needs to consume:
// MRID, TargetRig, CloseReason, Body. The body must include the full
// description so 3c's BUG-DISCOVERED parser has something to work with.
func TestMRCycleCloseDog_AC3_EventStructHasAllFields(t *testing.T) {
	body := strings.Join([]string{
		"branch: polecat/foo/gt-bar",
		"target: main",
		"source_issue: gt-bar",
		"rig: gastown_upstream",
		"close_reason: merged",
		"",
		"BUG-DISCOVERED: foo_test.go::TestFoo encodes buggy behavior",
	}, "\n")

	mr := mrCycleCloseRow{
		ID:          "gt-mr-bug",
		Status:      "closed",
		Description: body,
		Labels:      []string{MRCycleCloseAutoTestPRLabel, "rig:gastown_upstream"},
	}

	handler := &recordingHandler{}
	ack := &recordingAck{}
	cap := &mrCycleCaptureLogger{}
	classifyAndDispatchMRCycleCloseRows(
		[]mrCycleCloseRow{mr}, handler.handle, ack.ack, cap.log)

	if len(handler.events) != 1 {
		t.Fatalf("handler events = %d, want 1", len(handler.events))
	}
	ev := handler.events[0]
	if ev.MRID != "gt-mr-bug" {
		t.Errorf("MRID = %q", ev.MRID)
	}
	if ev.TargetRig != "gastown_upstream" {
		t.Errorf("TargetRig = %q", ev.TargetRig)
	}
	if ev.CloseReason != "merged" {
		t.Errorf("CloseReason = %q", ev.CloseReason)
	}
	if !strings.Contains(ev.Body, "BUG-DISCOVERED:") {
		t.Errorf("Body should include BUG-DISCOVERED line for 3c parser; got %q", ev.Body)
	}
}

func TestMRCycleCloseDog_MalformedNoCloseReason(t *testing.T) {
	mr := mrCycleCloseRow{
		ID:          "gt-mr-malformed",
		Status:      "closed",
		Description: "branch: polecat/foo\ntarget: main\nrig: gastown_upstream\n",
		Labels:      []string{MRCycleCloseAutoTestPRLabel, "rig:gastown_upstream"},
	}

	handler := &recordingHandler{}
	ack := &recordingAck{}
	cap := &mrCycleCaptureLogger{}
	d, deduped, m := classifyAndDispatchMRCycleCloseRows(
		[]mrCycleCloseRow{mr}, handler.handle, ack.ack, cap.log)

	if d != 0 || deduped != 0 || m != 1 {
		t.Errorf("dispatched=%d deduped=%d malformed=%d, want 0/0/1", d, deduped, m)
	}
	if len(handler.events) != 0 {
		t.Errorf("handler should not have been called for malformed bead")
	}
	if len(ack.calls) != 0 {
		t.Errorf("ack should not have been called for malformed bead")
	}
	if !strings.Contains(strings.Join(cap.lines, "\n"), "no close_reason field") {
		t.Errorf("expected malformed-no-close_reason log line, got: %v", cap.lines)
	}
}

func TestMRCycleCloseDog_MalformedNoRigLabel(t *testing.T) {
	mr := mrCycleCloseRow{
		ID:          "gt-mr-norig",
		Status:      "closed",
		Description: "close_reason: merged\n",
		Labels:      []string{MRCycleCloseAutoTestPRLabel},
	}

	handler := &recordingHandler{}
	ack := &recordingAck{}
	cap := &mrCycleCaptureLogger{}
	d, deduped, m := classifyAndDispatchMRCycleCloseRows(
		[]mrCycleCloseRow{mr}, handler.handle, ack.ack, cap.log)

	if d != 0 || deduped != 0 || m != 1 {
		t.Errorf("dispatched=%d deduped=%d malformed=%d, want 0/0/1", d, deduped, m)
	}
	if len(handler.events) != 0 {
		t.Errorf("handler should not have been called for missing rig label")
	}
	if !strings.Contains(strings.Join(cap.lines, "\n"), "no rig:<target_rig> label") {
		t.Errorf("expected no-rig-label log line, got: %v", cap.lines)
	}
}

func TestMRCycleCloseDog_AckWriteFailureStillCountsDispatched(t *testing.T) {
	// The handler ran successfully but the ack-label write failed (e.g.,
	// transient bd flakiness). We still count the dispatch, log the
	// failure, and rely on the next tick re-dispatching — the handler
	// must be idempotent for this case to be safe.
	mr := mrCycleCloseRow{
		ID:          "gt-mr-flaky",
		Status:      "closed",
		Description: "close_reason: merged\nrig: gastown_upstream\n",
		Labels:      []string{MRCycleCloseAutoTestPRLabel, "rig:gastown_upstream"},
	}

	handler := &recordingHandler{}
	ack := &recordingAck{err: errors.New("bd flaky")}
	cap := &mrCycleCaptureLogger{}
	d, _, _ := classifyAndDispatchMRCycleCloseRows(
		[]mrCycleCloseRow{mr}, handler.handle, ack.ack, cap.log)

	if d != 1 {
		t.Errorf("dispatched = %d, want 1 (handler ran)", d)
	}
	if len(handler.events) != 1 {
		t.Errorf("handler events = %d, want 1", len(handler.events))
	}
	if !strings.Contains(strings.Join(cap.lines, "\n"), "failed to write ack label") {
		t.Errorf("expected ack-failure log line, got: %v", cap.lines)
	}
}

func TestMRCycleCloseDog_MultipleMRsInOneTick(t *testing.T) {
	rows := []mrCycleCloseRow{
		{
			ID:          "gt-mr1",
			Status:      "closed",
			Description: "close_reason: merged\nrig: gastown_upstream\n",
			Labels:      []string{MRCycleCloseAutoTestPRLabel, "rig:gastown_upstream"},
		},
		{
			ID:          "gt-mr2",
			Status:      "closed",
			Description: "close_reason: rejected\nrig: beads\n",
			Labels:      []string{MRCycleCloseAutoTestPRLabel, "rig:beads"},
		},
		{
			ID:          "gt-mr3-already-acked",
			Status:      "closed",
			Description: "close_reason: merged\nrig: gastown_upstream\n",
			Labels: []string{
				MRCycleCloseAutoTestPRLabel,
				"rig:gastown_upstream",
				"fingerprint:cycle-close-merged",
			},
		},
	}

	handler := &recordingHandler{}
	ack := &recordingAck{}
	cap := &mrCycleCaptureLogger{}
	d, deduped, m := classifyAndDispatchMRCycleCloseRows(
		rows, handler.handle, ack.ack, cap.log)

	if d != 2 || deduped != 1 || m != 0 {
		t.Errorf("dispatched=%d deduped=%d malformed=%d, want 2/1/0", d, deduped, m)
	}
	if len(handler.events) != 2 {
		t.Fatalf("handler events = %d, want 2", len(handler.events))
	}
	rigs := []string{handler.events[0].TargetRig, handler.events[1].TargetRig}
	want := []string{"gastown_upstream", "beads"}
	for i, w := range want {
		if rigs[i] != w {
			t.Errorf("event[%d].TargetRig = %q, want %q", i, rigs[i], w)
		}
	}
}

func TestMRCycleCloseDog_NoRowsIsBenign(t *testing.T) {
	handler := &recordingHandler{}
	ack := &recordingAck{}
	cap := &mrCycleCaptureLogger{}
	d, deduped, m := classifyAndDispatchMRCycleCloseRows(
		nil, handler.handle, ack.ack, cap.log)

	if d != 0 || deduped != 0 || m != 0 {
		t.Errorf("dispatched=%d deduped=%d malformed=%d, want 0/0/0", d, deduped, m)
	}
	if len(handler.events) != 0 {
		t.Errorf("handler should not have been called")
	}
}

// TestSetMRCycleCloseHandler verifies the wire-up point Phase 0 task 3c
// will use: the daemon ships with no handler (events drop with a log
// line); SetMRCycleCloseHandler installs the real one.
func TestSetMRCycleCloseHandler(t *testing.T) {
	d := &Daemon{}
	if d.mrCycleCloseHandler != nil {
		t.Fatalf("default handler should be nil")
	}

	called := 0
	d.SetMRCycleCloseHandler(func(MRCycleCloseEvent) { called++ })
	if d.mrCycleCloseHandler == nil {
		t.Fatalf("handler should be set")
	}

	d.mrCycleCloseHandler(MRCycleCloseEvent{MRID: "gt-mr-x"})
	if called != 1 {
		t.Errorf("handler called %d times, want 1", called)
	}

	d.SetMRCycleCloseHandler(nil)
	if d.mrCycleCloseHandler != nil {
		t.Errorf("SetMRCycleCloseHandler(nil) should reset to nil")
	}
}

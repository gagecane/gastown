package reaper

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestDefaultStaleEscalationTTL pins the default terminal-state TTL and asserts
// it is far longer than the processed-mail audit window — the two sweeps must
// not overlap in intent (gu-tn0xp).
func TestDefaultStaleEscalationTTL(t *testing.T) {
	if DefaultStaleEscalationTTL != 72*time.Hour {
		t.Errorf("DefaultStaleEscalationTTL = %v, want 72h", DefaultStaleEscalationTTL)
	}
	if DefaultStaleEscalationTTL <= DefaultProcessedMailTTL {
		t.Errorf("stale-escalation TTL (%v) must be much longer than the processed-mail audit TTL (%v) — they target different lifecycle stages",
			DefaultStaleEscalationTTL, DefaultProcessedMailTTL)
	}
}

// TestStaleEscalationResultZeroValue documents the zero-value shape.
func TestStaleEscalationResultZeroValue(t *testing.T) {
	var r StaleEscalationResult
	if r.Closed != 0 || r.Remain != 0 || r.DryRun || r.ClosedEntries != nil || r.Anomalies != nil {
		t.Errorf("unexpected non-zero StaleEscalationResult zero value: %+v", r)
	}
}

// TestStaleEscalationLabelsAndReason verifies the type label, terminal reason,
// and preserve set match the documented behavior.
func TestStaleEscalationLabelsAndReason(t *testing.T) {
	if staleEscalationLabel != "gt:escalation" {
		t.Errorf("staleEscalationLabel = %q, want gt:escalation", staleEscalationLabel)
	}
	if !strings.HasPrefix(staleEscalationCloseReason, "stale:") {
		t.Errorf("staleEscalationCloseReason should be a terminal stale: reason, got %q", staleEscalationCloseReason)
	}
	want := map[string]bool{"gt:standing-orders": true, "gt:keep": true, "gt:role": true, "gt:rig": true}
	if len(staleEscalationPreserveLabels) != len(want) {
		t.Fatalf("preserve labels = %v, want %v", staleEscalationPreserveLabels, want)
	}
	for _, l := range staleEscalationPreserveLabels {
		if !want[l] {
			t.Errorf("unexpected preserve label %q", l)
		}
	}
}

// TestStaleEscalationArgsOrder confirms the bound-arg order matches the
// placeholder order the queries emit: [cutoff?], type-label, preserve-labels.
func TestStaleEscalationArgsOrder(t *testing.T) {
	cutoff := time.Now()
	args := staleEscalationArgs(cutoff)
	if len(args) != 1+1+len(staleEscalationPreserveLabels) {
		t.Fatalf("got %d args, want %d", len(args), 1+1+len(staleEscalationPreserveLabels))
	}
	if args[0] != cutoff {
		t.Errorf("arg[0] should be the cutoff, got %v", args[0])
	}
	if args[1] != staleEscalationLabel {
		t.Errorf("arg[1] should be the type label %q, got %v", staleEscalationLabel, args[1])
	}
	for i, l := range staleEscalationPreserveLabels {
		if args[2+i] != l {
			t.Errorf("arg[%d] = %v, want preserve label %q", 2+i, args[2+i], l)
		}
	}

	// Without a cutoff (total/remain count): type-label then preserve-labels.
	noCut := staleEscalationArgs()
	if len(noCut) != 1+len(staleEscalationPreserveLabels) {
		t.Fatalf("no-cutoff args = %d, want %d", len(noCut), 1+len(staleEscalationPreserveLabels))
	}
	if noCut[0] != staleEscalationLabel {
		t.Errorf("no-cutoff arg[0] should be the type label, got %v", noCut[0])
	}
}

// TestCountQueryCutoffPlaceholder confirms the count-query builders add the
// created_at cutoff placeholder only when requested.
func TestStaleEscalationCountQueryCutoffPlaceholder(t *testing.T) {
	for _, tc := range []struct {
		name    string
		builder func(bool) string
		col     string
	}{
		{"issues", staleEscalationIssuesCountQuery, "i.created_at"},
		{"wisp", staleEscalationWispCountQuery, "w.created_at"},
	} {
		with := tc.builder(true)
		without := tc.builder(false)
		if !strings.Contains(with, tc.col+" < ?") {
			t.Errorf("%s: withCutoff query must contain %q, got:\n%s", tc.name, tc.col+" < ?", with)
		}
		if strings.Contains(without, tc.col+" < ?") {
			t.Errorf("%s: no-cutoff query must NOT contain a created_at filter, got:\n%s", tc.name, without)
		}
	}
}

// TestReapStaleEscalationsTargetsIssuesTable is the central guard for the
// issues sweep: gated on gt:escalation, excludes agents, targets
// open/in_progress (never hooked), records a terminal close_reason, and honors
// the live-consumer exclusion.
func TestReapStaleEscalationsTargetsIssuesTable(t *testing.T) {
	body := funcBody(t, "ReapStaleEscalations")

	if !strings.Contains(body, "FROM issues i") {
		t.Error("ReapStaleEscalations must SELECT FROM the issues table")
	}
	if !strings.Contains(body, "INNER JOIN labels type_l") {
		t.Error("ReapStaleEscalations must join the labels table on the type label")
	}
	if strings.Contains(body, "FROM wisps") {
		t.Error("ReapStaleEscalations must NOT touch the wisps table — that is ReapStaleWispEscalations' job")
	}
	if !strings.Contains(body, "type_l.label = ?") {
		t.Error("ReapStaleEscalations must gate on the gt:escalation type label")
	}
	if !strings.Contains(body, "'open'") || !strings.Contains(body, "'in_progress'") {
		t.Error("ReapStaleEscalations must target status='open' and 'in_progress'")
	}
	if strings.Contains(body, "'hooked'") {
		t.Error("ReapStaleEscalations must NOT target status='hooked'")
	}
	if !strings.Contains(body, "issue_type != 'agent'") {
		t.Error("ReapStaleEscalations must exclude agent beads")
	}
	if !strings.Contains(body, "close_reason=") {
		t.Error("ReapStaleEscalations must record a terminal close_reason so closures are auditable")
	}
	if !strings.Contains(body, "ConsumerAliveClause") {
		t.Error("ReapStaleEscalations must honor the live-consumer exclusion")
	}
}

// TestReapStaleWispEscalationsTargetsWispTables is the central guard for the
// wisp sweep: same gate/exclusions but resolved against the wisp tables.
func TestReapStaleWispEscalationsTargetsWispTables(t *testing.T) {
	body := funcBody(t, "ReapStaleWispEscalations")

	if !strings.Contains(body, "FROM wisps w") {
		t.Error("ReapStaleWispEscalations must SELECT FROM the wisps table")
	}
	if !strings.Contains(body, "wisp_labels") {
		t.Error("ReapStaleWispEscalations must join the wisp_labels table")
	}
	if !strings.Contains(body, "UPDATE wisps SET status='closed'") {
		t.Error("ReapStaleWispEscalations must close rows in the wisps table")
	}
	if strings.Contains(body, "FROM issues") {
		t.Error("ReapStaleWispEscalations must NOT touch the issues table")
	}
	if !strings.Contains(body, "'open'") || !strings.Contains(body, "'in_progress'") {
		t.Error("ReapStaleWispEscalations must target status='open' and 'in_progress'")
	}
	if strings.Contains(body, "'hooked'") {
		t.Error("ReapStaleWispEscalations must NOT target status='hooked'")
	}
	if !strings.Contains(body, "issue_type != 'agent'") {
		t.Error("ReapStaleWispEscalations must exclude agent beads")
	}
	if !strings.Contains(body, "close_reason=") {
		t.Error("ReapStaleWispEscalations must record a terminal close_reason")
	}
	if !strings.Contains(body, "wispProcessedMailConsumerAliveClause") {
		t.Error("ReapStaleWispEscalations must honor the wisp live-consumer exclusion")
	}
}

// queriesFile returns the full stale_escalation.go source.
func queriesFile(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("stale_escalation.go")
	if err != nil {
		t.Fatalf("read stale_escalation.go: %v", err)
	}
	return string(data)
}

// funcBody extracts the body of the named top-level function from
// stale_escalation.go for source-inspection assertions.
func funcBody(t *testing.T, name string) string {
	t.Helper()
	src := queriesFile(t)
	start := strings.Index(src, "func "+name+"(")
	if start < 0 {
		t.Fatalf("function %s not found", name)
	}
	end := strings.Index(src[start+1:], "\nfunc ")
	if end < 0 {
		return src[start:]
	}
	return src[start : start+1+end]
}

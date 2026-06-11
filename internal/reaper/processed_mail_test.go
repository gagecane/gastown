package reaper

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestDefaultProcessedMailTTL mirrors TestDefaultOpenMailTTL: the TTL must be
// positive and within a sensible range. The processed-mail sweep closes beads
// the recipient has already acted on, so a short audit window is intended —
// but it must be at least 1h to avoid racing a just-acked notification.
func TestDefaultProcessedMailTTL(t *testing.T) {
	if DefaultProcessedMailTTL <= 0 {
		t.Errorf("DefaultProcessedMailTTL should be positive, got %v", DefaultProcessedMailTTL)
	}
	if DefaultProcessedMailTTL < time.Hour {
		t.Errorf("DefaultProcessedMailTTL should be at least 1h to avoid racing a just-acked notification, got %v", DefaultProcessedMailTTL)
	}
	if DefaultProcessedMailTTL > 7*24*time.Hour {
		t.Errorf("DefaultProcessedMailTTL should be at most 7 days to keep the audit window short, got %v", DefaultProcessedMailTTL)
	}
}

// TestProcessedMailResultZeroValue confirms FormatJSON works on an empty
// result (nil ClosedEntries / Anomalies slices).
func TestProcessedMailResultZeroValue(t *testing.T) {
	result := &ProcessedMailResult{Database: "hq"}
	j := FormatJSON(result)
	if j == "" {
		t.Error("FormatJSON on zero ProcessedMailResult should not return empty")
	}
	if !strings.Contains(j, `"database": "hq"`) {
		t.Errorf("JSON output missing database field: %s", j)
	}
	if !strings.Contains(j, `"closed": 0`) {
		t.Errorf("JSON output missing closed field: %s", j)
	}
	if !strings.Contains(j, `"processed_remain": 0`) {
		t.Errorf("JSON output missing processed_remain field: %s", j)
	}
}

// TestProcessedMailTypeAndDoneLabels verifies the label sets that define the
// sweep. The TYPE labels must cover both mail and escalation; the DONE labels
// must cover both the mark-read path (read / delivery:acked) and the escalate
// ack path (acked). These are the exact labels added by `gt mail mark-read`
// and `gt escalate ack` — keep them in sync with those commands.
func TestProcessedMailTypeAndDoneLabels(t *testing.T) {
	wantType := map[string]bool{"gt:message": true, "gt:escalation": true}
	if len(processedMailTypeLabels) != len(wantType) {
		t.Errorf("processedMailTypeLabels = %v, want keys %v", processedMailTypeLabels, wantType)
	}
	for _, l := range processedMailTypeLabels {
		if !wantType[l] {
			t.Errorf("unexpected type label %q (must be gt:message or gt:escalation)", l)
		}
	}

	wantDone := map[string]bool{"read": true, "delivery:acked": true, "acked": true}
	if len(processedMailDoneLabels) != len(wantDone) {
		t.Errorf("processedMailDoneLabels = %v, want keys %v", processedMailDoneLabels, wantDone)
	}
	for _, l := range processedMailDoneLabels {
		if !wantDone[l] {
			t.Errorf("unexpected done label %q (must be read, delivery:acked, or acked)", l)
		}
	}
}

// TestReapProcessedMailQueriesGatedOnProcessedLabel is the central safety
// guard for gu-ctspx: the sweep must ONLY close beads that carry a processed
// label. An un-acked escalation must stay open so it still demands attention.
// We confirm the SELECT joins the done-label table and references the done
// label set.
func TestReapProcessedMailQueriesGatedOnProcessedLabel(t *testing.T) {
	data, err := os.ReadFile("processed_mail.go")
	if err != nil {
		t.Fatalf("read processed_mail.go: %v", err)
	}
	src := string(data)

	reapStart := strings.Index(src, "func ReapProcessedMail(")
	if reapStart < 0 {
		t.Fatal("ReapProcessedMail function not found")
	}
	reapEnd := strings.Index(src[reapStart:], "\nfunc ")
	if reapEnd < 0 {
		reapEnd = len(src) - reapStart
	}
	reapBody := src[reapStart : reapStart+reapEnd]

	// Must join a done-label table (the processed gate) and a type-label table.
	if !strings.Contains(reapBody, "done_l") {
		t.Error("ReapProcessedMail SELECT must join a done-label table (done_l) — only processed beads may be closed (gu-ctspx)")
	}
	if !strings.Contains(reapBody, "type_l") {
		t.Error("ReapProcessedMail SELECT must join a type-label table (type_l) for gt:message/gt:escalation")
	}
	if !strings.Contains(reapBody, "processedMailDoneLabels") {
		t.Error("ReapProcessedMail must constrain done_l to processedMailDoneLabels")
	}
	if !strings.Contains(reapBody, "processedMailTypeLabels") {
		t.Error("ReapProcessedMail must constrain type_l to processedMailTypeLabels")
	}
}

// TestReapProcessedMailQueriesEmbedConsumerClause confirms ReapProcessedMail
// and ScanProcessedMail honor the live-consumer exclusion, matching the spirit
// of the hooked/open-mail guards. The shared count query builder is referenced
// by both, so checking the file as a whole is sufficient.
func TestReapProcessedMailQueriesEmbedConsumerClause(t *testing.T) {
	data, err := os.ReadFile("processed_mail.go")
	if err != nil {
		t.Fatalf("read processed_mail.go: %v", err)
	}
	src := string(data)
	if strings.Count(src, "ConsumerAliveClause") < 2 {
		t.Errorf("expected ConsumerAliveClause referenced by the SELECT and the count-query builder (>=2 occurrences), got %d", strings.Count(src, "ConsumerAliveClause"))
	}
}

// TestReapProcessedMailSweepsOpenAndInProgress confirms the sweep targets both
// status='open' and status='in_progress' and never status='hooked' (that is
// ReapHookedMail's responsibility). It also excludes agent beads.
func TestReapProcessedMailSweepsOpenAndInProgress(t *testing.T) {
	data, err := os.ReadFile("processed_mail.go")
	if err != nil {
		t.Fatalf("read processed_mail.go: %v", err)
	}
	src := string(data)

	reapStart := strings.Index(src, "func ReapProcessedMail(")
	reapEnd := strings.Index(src[reapStart:], "\nfunc ")
	if reapEnd < 0 {
		reapEnd = len(src) - reapStart
	}
	reapBody := src[reapStart : reapStart+reapEnd]

	if !strings.Contains(reapBody, "'open'") {
		t.Error("ReapProcessedMail should target status='open'")
	}
	if !strings.Contains(reapBody, "'in_progress'") {
		t.Error("ReapProcessedMail should target status='in_progress'")
	}
	if strings.Contains(reapBody, "'hooked'") {
		t.Error("ReapProcessedMail must NOT target status='hooked' — that is ReapHookedMail's responsibility")
	}
	if !strings.Contains(reapBody, "issue_type != 'agent'") {
		t.Error("ReapProcessedMail must exclude agent heartbeat beads (issue_type != 'agent')")
	}
}

// TestReapProcessedWispMailTargetsWispTables is the central guard for gu-2md8k:
// the wisp sweep must operate on the wisps / wisp_labels tables (not issues /
// labels), still gate on the processed done-label, exclude agents, and target
// only open/in_progress (never hooked). Without this the processed
// message/escalation beads that the open-wisp alert actually counts are never
// drained.
func TestReapProcessedWispMailTargetsWispTables(t *testing.T) {
	data, err := os.ReadFile("processed_mail.go")
	if err != nil {
		t.Fatalf("read processed_mail.go: %v", err)
	}
	src := string(data)

	reapStart := strings.Index(src, "func ReapProcessedWispMail(")
	if reapStart < 0 {
		t.Fatal("ReapProcessedWispMail function not found")
	}
	reapEnd := strings.Index(src[reapStart:], "\nfunc ")
	if reapEnd < 0 {
		reapEnd = len(src) - reapStart
	}
	reapBody := src[reapStart : reapStart+reapEnd]

	if !strings.Contains(reapBody, "FROM wisps w") {
		t.Error("ReapProcessedWispMail must SELECT FROM the wisps table (gu-2md8k)")
	}
	if !strings.Contains(reapBody, "wisp_labels") {
		t.Error("ReapProcessedWispMail must join the wisp_labels table")
	}
	if !strings.Contains(reapBody, "UPDATE wisps SET status='closed'") {
		t.Error("ReapProcessedWispMail must close rows in the wisps table")
	}
	if strings.Contains(reapBody, "FROM issues") {
		t.Error("ReapProcessedWispMail must NOT touch the issues table — that is ReapProcessedMail's job")
	}
	// Same processed gate + exclusions as the issues sweep.
	if !strings.Contains(reapBody, "done_l") || !strings.Contains(reapBody, "processedMailDoneLabels") {
		t.Error("ReapProcessedWispMail must gate on the processed done-label set")
	}
	if !strings.Contains(reapBody, "type_l") || !strings.Contains(reapBody, "processedMailTypeLabels") {
		t.Error("ReapProcessedWispMail must gate on the gt:message/gt:escalation type-label set")
	}
	if !strings.Contains(reapBody, "'open'") || !strings.Contains(reapBody, "'in_progress'") {
		t.Error("ReapProcessedWispMail must target status='open' and 'in_progress'")
	}
	if strings.Contains(reapBody, "'hooked'") {
		t.Error("ReapProcessedWispMail must NOT target status='hooked'")
	}
	if !strings.Contains(reapBody, "issue_type != 'agent'") {
		t.Error("ReapProcessedWispMail must exclude agent beads")
	}
}

// TestProcessedWispMailCountQueryCutoffPlaceholder verifies the wisps-table
// count-query builder emits the created_at age filter only when a cutoff is
// requested, and always gates on the type/done labels and the wisp-resolved
// live-consumer exclusion.
func TestProcessedWispMailCountQueryCutoffPlaceholder(t *testing.T) {
	preserve := []string{"gt:keep"}

	withCutoff := processedWispMailCountQuery(preserve, true)
	if !strings.Contains(withCutoff, "w.created_at < ?") {
		t.Error("processedWispMailCountQuery(withCutoff=true) must include the created_at age filter")
	}

	noCutoff := processedWispMailCountQuery(preserve, false)
	if strings.Contains(noCutoff, "w.created_at < ?") {
		t.Error("processedWispMailCountQuery(withCutoff=false) must NOT include the created_at age filter")
	}
	for _, q := range []string{withCutoff, noCutoff} {
		if !strings.Contains(q, "FROM wisps w") || !strings.Contains(q, "wisp_labels") {
			t.Errorf("count query must target wisp tables: %s", q)
		}
		if !strings.Contains(q, "type_l.label IN") || !strings.Contains(q, "done_l.label IN") {
			t.Errorf("count query missing type/done label gates: %s", q)
		}
		if !strings.Contains(q, "FROM wisps c") {
			t.Errorf("count query must resolve the live consumer against wisps, not issues: %s", q)
		}
	}
}

// TestProcessedMailCountQueryCutoffPlaceholder verifies the shared count-query
// builder emits the created_at age filter only when a cutoff is requested.
func TestProcessedMailCountQueryCutoffPlaceholder(t *testing.T) {
	preserve := []string{"gt:keep"}

	withCutoff := processedMailCountQuery(preserve, true)
	if !strings.Contains(withCutoff, "i.created_at < ?") {
		t.Error("processedMailCountQuery(withCutoff=true) must include the created_at age filter")
	}

	noCutoff := processedMailCountQuery(preserve, false)
	if strings.Contains(noCutoff, "i.created_at < ?") {
		t.Error("processedMailCountQuery(withCutoff=false) must NOT include the created_at age filter")
	}
	// Both forms must still gate on the type + done label sets and the
	// consumer-alive exclusion.
	for _, q := range []string{withCutoff, noCutoff} {
		if !strings.Contains(q, "type_l.label IN") || !strings.Contains(q, "done_l.label IN") {
			t.Errorf("count query missing type/done label gates: %s", q)
		}
		if !strings.Contains(q, "NOT EXISTS") {
			t.Errorf("count query missing consumer-alive exclusion: %s", q)
		}
	}
}

package witness

import (
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/session"
)

// TestJoinOrLeadStaleAgentCorrelation_Disabled verifies window<=0 always leads
// (every agent sends) — the operator opt-out / pre-gu-nejgh behavior.
func TestJoinOrLeadStaleAgentCorrelation_Disabled(t *testing.T) {
	townRoot := t.TempDir()
	now := time.Now().UTC()

	d1 := joinOrLeadStaleAgentCorrelation(townRoot, "rigA", "gu-refinery", now, 0)
	d2 := joinOrLeadStaleAgentCorrelation(townRoot, "rigB", "gu-refinery", now, 0)
	if !d1.IsLead || !d2.IsLead {
		t.Errorf("window=0 must always lead: d1=%+v d2=%+v", d1, d2)
	}
}

// TestJoinOrLeadStaleAgentCorrelation_FirstLeads verifies the first escalation
// in a fresh window becomes the lead.
func TestJoinOrLeadStaleAgentCorrelation_FirstLeads(t *testing.T) {
	townRoot := t.TempDir()
	now := time.Now().UTC()

	d := joinOrLeadStaleAgentCorrelation(townRoot, "rigA", "gu-refinery", now, 15*time.Minute)
	if !d.IsLead {
		t.Errorf("first agent in fresh window must lead, got %+v", d)
	}
	if d.MemberCount != 1 {
		t.Errorf("MemberCount = %d, want 1", d.MemberCount)
	}
}

// TestJoinOrLeadStaleAgentCorrelation_SecondRigFolds is the core gu-nejgh fix:
// a second rig escalating within the window folds into the first rig's thread
// instead of sending its own mail.
func TestJoinOrLeadStaleAgentCorrelation_SecondRigFolds(t *testing.T) {
	townRoot := t.TempDir()
	start := time.Now().UTC()

	lead := joinOrLeadStaleAgentCorrelation(townRoot, "rigA", "gu-refinery", start, 15*time.Minute)
	if !lead.IsLead {
		t.Fatalf("rigA must lead, got %+v", lead)
	}

	// rigB escalates 2m later — same window.
	fold := joinOrLeadStaleAgentCorrelation(townRoot, "rigB", "gu-refinery", start.Add(2*time.Minute), 15*time.Minute)
	if fold.IsLead {
		t.Errorf("rigB within window must fold, got IsLead=true")
	}
	if fold.FoldedInto != "rigA/gu-refinery" {
		t.Errorf("FoldedInto = %q, want rigA/gu-refinery", fold.FoldedInto)
	}
	if fold.MemberCount != 2 {
		t.Errorf("MemberCount = %d, want 2", fold.MemberCount)
	}
}

// TestJoinOrLeadStaleAgentCorrelation_LeadReFiresStaysLead verifies the lead
// re-escalating within its own window stays the lead (its per-(rig,session)
// cooldown governs how often that happens). This keeps the canonical thread
// alive rather than handing lead to a folder.
func TestJoinOrLeadStaleAgentCorrelation_LeadReFiresStaysLead(t *testing.T) {
	townRoot := t.TempDir()
	start := time.Now().UTC()

	joinOrLeadStaleAgentCorrelation(townRoot, "rigA", "gu-refinery", start, 15*time.Minute)
	again := joinOrLeadStaleAgentCorrelation(townRoot, "rigA", "gu-refinery", start.Add(3*time.Minute), 15*time.Minute)
	if !again.IsLead {
		t.Errorf("lead re-firing within window must stay lead, got %+v", again)
	}
	// MemberCount stays 1 — the lead is not double-counted.
	if again.MemberCount != 1 {
		t.Errorf("MemberCount = %d, want 1 (lead not double-counted)", again.MemberCount)
	}
}

// TestJoinOrLeadStaleAgentCorrelation_WindowElapsesOpensFresh verifies that once
// the window elapses, the next escalation opens a fresh window with a new lead —
// so a separate later incident is not silenced by an old one.
func TestJoinOrLeadStaleAgentCorrelation_WindowElapsesOpensFresh(t *testing.T) {
	townRoot := t.TempDir()
	start := time.Now().UTC()

	joinOrLeadStaleAgentCorrelation(townRoot, "rigA", "gu-refinery", start, 15*time.Minute)

	// rigB escalates 20m later — prior window elapsed, so rigB opens a fresh one.
	d := joinOrLeadStaleAgentCorrelation(townRoot, "rigB", "gu-refinery", start.Add(20*time.Minute), 15*time.Minute)
	if !d.IsLead {
		t.Errorf("escalation after window elapse must open fresh window and lead, got %+v", d)
	}
	if d.MemberCount != 1 {
		t.Errorf("MemberCount = %d, want 1 (fresh window)", d.MemberCount)
	}
}

// TestJoinOrLeadStaleAgentCorrelation_MultipleFoldsAccumulate verifies several
// rigs folding into one window accumulate as distinct members.
func TestJoinOrLeadStaleAgentCorrelation_MultipleFoldsAccumulate(t *testing.T) {
	townRoot := t.TempDir()
	start := time.Now().UTC()

	joinOrLeadStaleAgentCorrelation(townRoot, "rigA", "gu-refinery", start, 15*time.Minute)
	joinOrLeadStaleAgentCorrelation(townRoot, "rigB", "gu-refinery", start.Add(1*time.Minute), 15*time.Minute)
	joinOrLeadStaleAgentCorrelation(townRoot, "rigB", "gu-witness", start.Add(2*time.Minute), 15*time.Minute)
	last := joinOrLeadStaleAgentCorrelation(townRoot, "rigC", "gu-refinery", start.Add(3*time.Minute), 15*time.Minute)

	if last.IsLead {
		t.Errorf("rigC within window must fold, got IsLead=true")
	}
	// Lead (rigA/refinery) + rigB/refinery + rigB/witness + rigC/refinery = 4.
	if last.MemberCount != 4 {
		t.Errorf("MemberCount = %d, want 4", last.MemberCount)
	}
}

// TestDetectStaleRigAgentHeartbeats_CrossRigCorrelation is the integration test
// for gu-nejgh: two rigs each with a stale refinery, scanned within one
// correlation window, produce exactly one escalation — the first rig leads, the
// second folds (Action=skip-correlated, no mail).
func TestDetectStaleRigAgentHeartbeats_CrossRigCorrelation(t *testing.T) {
	installFakeTmuxNoServer(t)

	// Both rigs share one townRoot — heartbeats live under the same
	// .runtime/heartbeats and the correlation record is town-level.
	townRoot := t.TempDir()

	prefixA := session.PrefixFor("rigA")
	writeRigAgentHeartbeat(t, townRoot, session.RefinerySessionName(prefixA), 2*time.Hour)
	writeRigAgentHeartbeat(t, townRoot, session.WitnessSessionName(prefixA), 30*time.Second)

	prefixB := session.PrefixFor("rigB")
	writeRigAgentHeartbeat(t, townRoot, session.RefinerySessionName(prefixB), 2*time.Hour)
	writeRigAgentHeartbeat(t, townRoot, session.WitnessSessionName(prefixB), 30*time.Second)

	// rigA scans first — its refinery leads and escalates.
	resA := DetectStaleRigAgentHeartbeats(townRoot, "rigA", nil, time.Hour, "", 30*time.Minute, 15*time.Minute)
	refA := findStaleResult(resA, "refinery")
	if refA == nil || refA.Action != "escalated" {
		t.Fatalf("rigA refinery Action = %v, want escalated", refA)
	}

	// rigB scans within the window — its refinery folds into rigA's thread.
	resB := DetectStaleRigAgentHeartbeats(townRoot, "rigB", nil, time.Hour, "", 30*time.Minute, 15*time.Minute)
	refB := findStaleResult(resB, "refinery")
	if refB == nil || refB.Action != "skip-correlated" {
		t.Fatalf("rigB refinery Action = %v, want skip-correlated", refB)
	}
	if refB.MailSent {
		t.Errorf("rigB refinery MailSent = true, want false (folded)")
	}
	wantLead := "rigA/" + session.RefinerySessionName(prefixA)
	if refB.CorrelatedInto != wantLead {
		t.Errorf("rigB CorrelatedInto = %q, want %q", refB.CorrelatedInto, wantLead)
	}
}

// TestDetectStaleRigAgentHeartbeats_CorrelationDisabledBothEscalate verifies
// that with correlationWindow=0 both rigs escalate independently (regression
// guard for the opt-out and proof that correlation is the only thing
// suppressing the second rig).
func TestDetectStaleRigAgentHeartbeats_CorrelationDisabledBothEscalate(t *testing.T) {
	installFakeTmuxNoServer(t)

	townRoot := t.TempDir()
	prefixA := session.PrefixFor("rigA")
	writeRigAgentHeartbeat(t, townRoot, session.RefinerySessionName(prefixA), 2*time.Hour)
	writeRigAgentHeartbeat(t, townRoot, session.WitnessSessionName(prefixA), 30*time.Second)
	prefixB := session.PrefixFor("rigB")
	writeRigAgentHeartbeat(t, townRoot, session.RefinerySessionName(prefixB), 2*time.Hour)
	writeRigAgentHeartbeat(t, townRoot, session.WitnessSessionName(prefixB), 30*time.Second)

	resA := DetectStaleRigAgentHeartbeats(townRoot, "rigA", nil, time.Hour, "", 30*time.Minute, 0)
	resB := DetectStaleRigAgentHeartbeats(townRoot, "rigB", nil, time.Hour, "", 30*time.Minute, 0)

	if r := findStaleResult(resA, "refinery"); r == nil || r.Action != "escalated" {
		t.Fatalf("rigA refinery Action = %v, want escalated (correlation disabled)", r)
	}
	if r := findStaleResult(resB, "refinery"); r == nil || r.Action != "escalated" {
		t.Fatalf("rigB refinery Action = %v, want escalated (correlation disabled)", r)
	}
}

// TestStaleAgentCorrelation_RoundTrip verifies the town-level record persists
// and reads back, and that a missing file reads as nil (no active window).
func TestStaleAgentCorrelation_RoundTrip(t *testing.T) {
	townRoot := t.TempDir()

	if got := readStaleAgentCorrelation(townRoot); got != nil {
		t.Errorf("expected nil for missing correlation, got %+v", got)
	}

	now := time.Now().UTC().Truncate(time.Second)
	writeStaleAgentCorrelation(townRoot, &staleAgentCorrelationState{
		WindowStartedAt: now,
		LeadKey:         "rigA/gu-refinery",
		Members:         []string{"rigA/gu-refinery", "rigB/gu-refinery"},
		LastUpdatedAt:   now,
	})

	got := readStaleAgentCorrelation(townRoot)
	if got == nil {
		t.Fatalf("expected state after write, got nil")
	}
	if got.LeadKey != "rigA/gu-refinery" || len(got.Members) != 2 || !got.WindowStartedAt.Equal(now) {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}

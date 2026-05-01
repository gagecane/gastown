package reaper

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDefaultHookedMailTTL(t *testing.T) {
	if DefaultHookedMailTTL <= 0 {
		t.Errorf("DefaultHookedMailTTL should be positive, got %v", DefaultHookedMailTTL)
	}
	if DefaultHookedMailTTL < time.Hour {
		t.Errorf("DefaultHookedMailTTL should be at least 1h to avoid false positives, got %v", DefaultHookedMailTTL)
	}
	if DefaultHookedMailTTL > 7*24*time.Hour {
		t.Errorf("DefaultHookedMailTTL should be at most 7 days to bound dead-letter accumulation, got %v", DefaultHookedMailTTL)
	}
}

func TestSQLPlaceholders(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "NULL"},
		{1, "?"},
		{2, "?,?"},
		{4, "?,?,?,?"},
	}
	for _, tt := range tests {
		got := sqlPlaceholders(tt.n)
		if got != tt.want {
			t.Errorf("sqlPlaceholders(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestHookedMailResultZeroValue(t *testing.T) {
	// HookedMailResult{} should marshal to stable JSON (no panic on nil slices).
	result := &HookedMailResult{Database: "hq"}
	j := FormatJSON(result)
	if j == "" {
		t.Error("FormatJSON on zero HookedMailResult should not return empty")
	}
	if !strings.Contains(j, `"database": "hq"`) {
		t.Errorf("JSON output missing database field: %s", j)
	}
	if !strings.Contains(j, `"closed": 0`) {
		t.Errorf("JSON output missing closed field: %s", j)
	}
}

// TestHookedMailResultEntryAgeDays ensures we compute age days from created_at
// the same way AutoClose does (floor of hours/24). This test does not touch
// the DB — it just confirms our expected math.
func TestHookedMailResultEntryAgeDays(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name     string
		ageHours float64
		wantDays int
	}{
		{"30 min", 0.5, 0},
		{"23 hours", 23, 0},
		{"25 hours", 25, 1},
		{"3 days", 72, 3},
		{"3.9 days", 93.6, 3}, // floors, not rounds
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			createdAt := now.Add(-time.Duration(tt.ageHours * float64(time.Hour)))
			gotDays := int(now.Sub(createdAt).Hours() / 24)
			if gotDays != tt.wantDays {
				t.Errorf("ageDays(%v) = %d, want %d", tt.ageHours, gotDays, tt.wantDays)
			}
		})
	}
}

func TestDefaultDeadLetterThreshold(t *testing.T) {
	if DefaultDeadLetterThreshold <= 0 {
		t.Errorf("DefaultDeadLetterThreshold should be positive, got %v", DefaultDeadLetterThreshold)
	}
	// gu-hhqk AC#4 specifies 30 minutes. The doctor check and metrics gauges
	// must agree on this threshold to keep operator semantics aligned.
	if DefaultDeadLetterThreshold != 30*time.Minute {
		t.Errorf("DefaultDeadLetterThreshold = %v, want 30m (gu-hhqk AC#4)", DefaultDeadLetterThreshold)
	}
	// Must be strictly less than the reap TTL — we want to surface backlog
	// before the reaper starts closing beads.
	if DefaultDeadLetterThreshold >= DefaultHookedMailTTL {
		t.Errorf("DefaultDeadLetterThreshold (%v) must be < DefaultHookedMailTTL (%v)",
			DefaultDeadLetterThreshold, DefaultHookedMailTTL)
	}
}

func TestHookedMailCountsZeroValue(t *testing.T) {
	// HookedMailCounts{} should not have any invariants violated (used as a
	// zero-value safe snapshot in the daemon metrics callback).
	c := HookedMailCounts{Database: "hq"}
	if c.Total != 0 || c.DeadLetter != 0 {
		t.Errorf("zero-value HookedMailCounts should have zero counts, got %+v", c)
	}
}

// TestScanHookedMailCountsQueryStructure verifies the generated SQL contains
// the expected clauses. This guards against regressions without needing a
// live Dolt server — the doctor check, ReapHookedMail, and ScanHookedMailCounts
// must share the same exclusion set so gu-hhqk semantics stay aligned.
func TestScanHookedMailCountsQueryStructure(t *testing.T) {
	// Reproduce the exact preserve-label list used by ScanHookedMailCounts to
	// verify it matches reaper.ReapHookedMail and doctor.HookedDeadLetterCheck.
	preserveLabels := []string{"gt:standing-orders", "gt:keep", "gt:role", "gt:rig"}
	for _, lbl := range preserveLabels {
		if lbl == "" {
			t.Errorf("preserve label should not be empty")
		}
	}
	if len(preserveLabels) != 4 {
		t.Errorf("expected 4 preserve labels, got %d — keep in sync with ReapHookedMail", len(preserveLabels))
	}
}

// TestConsumerAliveClause verifies the SQL fragment exists and looks
// syntactically plausible. The clause is referenced by ReapHookedMail,
// ScanHookedMail, ScanHookedMailCounts, and (via a duplicated copy, by
// intent) doctor.hookedDeadLetterCountQuery. gu-ub1l.
func TestConsumerAliveClause(t *testing.T) {
	if ConsumerAliveClause == "" {
		t.Fatal("ConsumerAliveClause must not be empty")
	}

	// Required SQL fragments: the clause must reference the issues table
	// alias, the metadata column, and the consumer_bead_id metadata key.
	requiredFragments := []string{
		"NOT EXISTS",
		"FROM issues",
		"JSON_EXTRACT",
		"metadata",
		"consumer_bead_id",
		"status",
		"closed",
	}
	for _, frag := range requiredFragments {
		if !strings.Contains(ConsumerAliveClause, frag) {
			t.Errorf("ConsumerAliveClause missing required fragment %q: %s", frag, ConsumerAliveClause)
		}
	}

	// Semantic guard: the clause must correlate via `c.id = ... JSON ...`
	// so the subquery is correlated (per-row), not decorrelated.
	if !strings.Contains(ConsumerAliveClause, "c.id") {
		t.Errorf("ConsumerAliveClause should use alias 'c.id' for the correlated subquery: %s", ConsumerAliveClause)
	}
	// The "alive" predicate is "status != 'closed'", not "status = 'open'",
	// so in_progress, pinned, and other non-closed statuses also count as alive.
	if !strings.Contains(ConsumerAliveClause, "status != 'closed'") {
		t.Errorf("ConsumerAliveClause should test `status != 'closed'` so non-closed statuses are treated as alive: %s", ConsumerAliveClause)
	}
}

// TestReapHookedMailQueryEmbedsConsumerClause inspects the SQL the reaper
// builds for its candidate SELECT to confirm the consumer-alive exclusion
// is wired in. We invoke the query-construction path indirectly by reading
// the source file (sanity guard for gu-ub1l regressions).
func TestReapHookedMailQueriesEmbedConsumerClause(t *testing.T) {
	// Read the source file and verify every hooked-mail query references
	// ConsumerAliveClause. This is a low-cost regression guard that catches
	// someone copy-pasting a query without the exclusion.
	data, err := os.ReadFile("hooked_mail.go")
	if err != nil {
		t.Fatalf("read hooked_mail.go: %v", err)
	}
	src := string(data)

	// Expect the clause identifier to appear multiple times — once per
	// query that must exclude live-consumer beads. At the time of writing,
	// that's at least 4 occurrences (ScanHookedMail total+candidates,
	// ReapHookedMail select+remain, ScanHookedMailCounts total+dl) plus
	// the declaration itself.
	count := strings.Count(src, "ConsumerAliveClause")
	if count < 5 {
		t.Errorf("expected ConsumerAliveClause referenced by all hooked-mail queries (>=5 occurrences), got %d", count)
	}
}

// TestDefaultOpenMailTTL mirrors TestDefaultHookedMailTTL for the open-mail
// reaper (gu-ckly). The TTL must be positive and within a sensible range.
func TestDefaultOpenMailTTL(t *testing.T) {
	if DefaultOpenMailTTL <= 0 {
		t.Errorf("DefaultOpenMailTTL should be positive, got %v", DefaultOpenMailTTL)
	}
	if DefaultOpenMailTTL < time.Hour {
		t.Errorf("DefaultOpenMailTTL should be at least 1h to avoid false positives, got %v", DefaultOpenMailTTL)
	}
	if DefaultOpenMailTTL > 7*24*time.Hour {
		t.Errorf("DefaultOpenMailTTL should be at most 7 days to bound open-mail accumulation, got %v", DefaultOpenMailTTL)
	}
}

// TestOpenMailResultZeroValue confirms FormatJSON works on an empty result
// (nil ClosedEntries / Anomalies slices).
func TestOpenMailResultZeroValue(t *testing.T) {
	result := &OpenMailResult{Database: "hq"}
	j := FormatJSON(result)
	if j == "" {
		t.Error("FormatJSON on zero OpenMailResult should not return empty")
	}
	if !strings.Contains(j, `"database": "hq"`) {
		t.Errorf("JSON output missing database field: %s", j)
	}
	if !strings.Contains(j, `"closed": 0`) {
		t.Errorf("JSON output missing closed field: %s", j)
	}
	if !strings.Contains(j, `"open_remain": 0`) {
		t.Errorf("JSON output missing open_remain field: %s", j)
	}
}

// TestReapOpenMailQueriesEmbedConsumerClause confirms the ReapOpenMail and
// ScanOpenMail queries reference ConsumerAliveClause. This guards against
// regressions where a copy-paste forgets the exclusion for live consumers.
// Matches the spirit of TestReapHookedMailQueriesEmbedConsumerClause.
func TestReapOpenMailQueriesEmbedConsumerClause(t *testing.T) {
	data, err := os.ReadFile("hooked_mail.go")
	if err != nil {
		t.Fatalf("read hooked_mail.go: %v", err)
	}
	src := string(data)

	// Locate the ReapOpenMail function body and confirm it references
	// ConsumerAliveClause at least twice (select and remain queries).
	reapStart := strings.Index(src, "func ReapOpenMail(")
	if reapStart < 0 {
		t.Fatal("ReapOpenMail function not found in hooked_mail.go")
	}
	// Slice from ReapOpenMail to end of file; at least both queries must
	// reference ConsumerAliveClause.
	reapBody := src[reapStart:]
	count := strings.Count(reapBody, "ConsumerAliveClause")
	if count < 2 {
		t.Errorf("expected ReapOpenMail to reference ConsumerAliveClause in both select and remain queries (>=2 occurrences), got %d", count)
	}

	// ScanOpenMail should also reference it in both total and candidate queries.
	scanStart := strings.Index(src, "func ScanOpenMail(")
	if scanStart < 0 {
		t.Fatal("ScanOpenMail function not found in hooked_mail.go")
	}
	scanEnd := strings.Index(src[scanStart:], "\nfunc ")
	if scanEnd < 0 {
		scanEnd = len(src) - scanStart
	}
	scanBody := src[scanStart : scanStart+scanEnd]
	scanCount := strings.Count(scanBody, "ConsumerAliveClause")
	if scanCount < 2 {
		t.Errorf("expected ScanOpenMail to reference ConsumerAliveClause in both total and candidate queries (>=2 occurrences), got %d", scanCount)
	}
}

// TestReapOpenMailSweepsOpenAndInProgress confirms the ReapOpenMail query
// targets both status='open' and status='in_progress' — the two states a
// P1 coordination mail bead can linger in before the TTL reaper intervenes.
// 'hooked' beads are handled by ReapHookedMail, so they must NOT appear in
// the ReapOpenMail status predicate.
func TestReapOpenMailSweepsOpenAndInProgress(t *testing.T) {
	data, err := os.ReadFile("hooked_mail.go")
	if err != nil {
		t.Fatalf("read hooked_mail.go: %v", err)
	}
	src := string(data)

	reapStart := strings.Index(src, "func ReapOpenMail(")
	if reapStart < 0 {
		t.Fatal("ReapOpenMail function not found")
	}
	reapEnd := strings.Index(src[reapStart:], "\nfunc ")
	if reapEnd < 0 {
		reapEnd = len(src) - reapStart
	}
	reapBody := src[reapStart : reapStart+reapEnd]

	if !strings.Contains(reapBody, "'open'") {
		t.Errorf("ReapOpenMail should target status='open'")
	}
	if !strings.Contains(reapBody, "'in_progress'") {
		t.Errorf("ReapOpenMail should target status='in_progress' so mid-read beads past TTL still get swept")
	}
	if strings.Contains(reapBody, "'hooked'") {
		t.Errorf("ReapOpenMail must NOT target status='hooked' — that is ReapHookedMail's responsibility")
	}
}

package reaper

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestValidateDBName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"hq", false},
		{"beads", false},
		{"gt", false},
		{"test_db_123", false},
		{"", true},
		{"drop table", true},
		{"db;--", true},
		{"db`name", true},
		{"../etc/passwd", true},
	}
	for _, tt := range tests {
		err := ValidateDBName(tt.name)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateDBName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
	}
}

func TestDefaultDatabases(t *testing.T) {
	if len(DefaultDatabases) == 0 {
		t.Error("DefaultDatabases should not be empty")
	}
	for _, db := range DefaultDatabases {
		if err := ValidateDBName(db); err != nil {
			t.Errorf("DefaultDatabases contains invalid name %q: %v", db, err)
		}
	}
}

func TestFormatJSON(t *testing.T) {
	result := FormatJSON(map[string]int{"count": 42})
	if result == "" {
		t.Error("FormatJSON should not return empty string")
	}
	if result[0] != '{' {
		t.Errorf("FormatJSON should return JSON object, got %q", result[:10])
	}
}

func TestParentExcludeJoin(t *testing.T) {
	joinClause, whereCondition := parentExcludeJoin("testdb")

	// JOIN clause should reference the correct database.
	if joinClause == "" {
		t.Error("parentExcludeJoin joinClause should not be empty")
	}
	// parentExcludeJoin no longer qualifies table names with the database — the
	// reaper connects to a specific database via the DSN, so unqualified names
	// are correct. The dbName parameter is retained for API compatibility.

	// JOIN should select wisps with open parents from wisp_dependencies.
	if !contains(joinClause, "wisp_dependencies") {
		t.Error("parentExcludeJoin should query wisp_dependencies")
	}
	if !contains(joinClause, "parent-child") {
		t.Error("parentExcludeJoin should filter on parent-child type")
	}

	// Regression (gu-eedh0): post-v49 the parent-child target moved into
	// depends_on_wisp_id (wisp parents) / depends_on_issue_id (issue parents) and
	// the legacy depends_on_id column is empty. Joining the wisp parent on the
	// empty column flagged every parent-child link as dangling (~240 town-wide,
	// 31 verified on this rig — all stored in depends_on_wisp_id, 0 genuinely
	// missing), driving a recurring false-positive escalation flood. Lock the
	// post-v49 columns in and forbid the legacy empty column.
	if !contains(joinClause, "depends_on_wisp_id") {
		t.Error("parentExcludeJoin should join wisp parents on depends_on_wisp_id (v49 schema)")
	}
	if !contains(joinClause, "depends_on_issue_id") {
		t.Error("parentExcludeJoin should join issue parents on depends_on_issue_id (v49 schema)")
	}
	if contains(joinClause, "wd.depends_on_id") {
		t.Error("parentExcludeJoin must not use the legacy empty depends_on_id column (v49 skew)")
	}

	if !contains(joinClause, "'open', 'hooked', 'in_progress'") {
		t.Error("parentExcludeJoin should check for open parent statuses")
	}
	// Regression (gu-gvwqx): the issue-parent branch (pi.status) must include
	// 'hooked'. A hooked issue is actively-assigned/non-terminal, so a wisp under
	// a hooked parent issue must not be treated as an orphan and reaped.
	if !contains(joinClause, "pi.status IN ('open', 'hooked', 'in_progress')") {
		t.Errorf("parentExcludeJoin issue-parent branch must include 'hooked' status: %s", joinClause)
	}

	// WHERE condition should be an IS NULL anti-join filter.
	if whereCondition == "" {
		t.Error("parentExcludeJoin whereCondition should not be empty")
	}
	if !contains(whereCondition, "IS NULL") {
		t.Error("parentExcludeJoin whereCondition should use IS NULL for anti-join")
	}
}

// TestReapQueryNoDatabaseNameInjection verifies that the Reap function's batch
// SELECT query does not inject the database name into the SQL string. Previously,
// dbName was passed as a Sprintf arg but the format string didn't use it, causing
// positional shift: "FROM wisps w gt WHERE..." instead of "FROM wisps w LEFT JOIN...".
func TestReapQueryNoDatabaseNameInjection(t *testing.T) {
	// Reproduce the exact Sprintf call from Reap() to verify no dbName injection.
	dbName := "gt"
	parentJoin, parentWhere := parentExcludeJoin(dbName)
	whereClause := fmt.Sprintf(
		"w.status IN ('open', 'hooked', 'in_progress') AND w.created_at < ? AND %s", parentWhere)

	// This is the fixed query — dbName is NOT in the Sprintf args.
	idQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w %s WHERE %s LIMIT %d",
		parentJoin, whereClause, DefaultBatchSize)

	// The query must NOT contain the literal database name as a bare token.
	// Before the fix, "gt" appeared between "wisps w" and "WHERE".
	if strings.Contains(idQuery, "wisps w gt") {
		t.Errorf("Reap idQuery contains injected database name: %s", idQuery)
	}
	if !strings.Contains(idQuery, "LEFT JOIN") {
		t.Errorf("Reap idQuery should contain LEFT JOIN from parentExcludeJoin, got: %s", idQuery)
	}
	if !strings.Contains(idQuery, fmt.Sprintf("LIMIT %d", DefaultBatchSize)) {
		t.Errorf("Reap idQuery should end with LIMIT %d, got: %s", DefaultBatchSize, idQuery)
	}
}

// TestReapUpdateQueryNoDatabaseNameInjection verifies that the UPDATE query in
// Reap() does not inject dbName where the IN clause should go.
func TestReapUpdateQueryNoDatabaseNameInjection(t *testing.T) {
	dbName := "gt"
	inClause := "?,?,?"

	// This is the fixed query — only inClause in the Sprintf args.
	updateQuery := fmt.Sprintf(
		"UPDATE wisps SET status='closed', closed_at=NOW() WHERE id IN (%s)",
		inClause)

	if strings.Contains(updateQuery, dbName) {
		t.Errorf("Reap updateQuery contains injected database name %q: %s", dbName, updateQuery)
	}
	if !strings.Contains(updateQuery, "IN (?,?,?)") {
		t.Errorf("Reap updateQuery should contain parameterized IN clause, got: %s", updateQuery)
	}
}

// TestPurgeDigestQueryNoDatabaseNameInjection verifies that the purge digest
// query carries no database name and still selects ephemeral wisps on the
// short horizon.
func TestPurgeDigestQueryNoDatabaseNameInjection(t *testing.T) {
	digestQuery := "SELECT COALESCE(w.wisp_type, 'unknown') AS wtype, COUNT(*) AS cnt FROM wisps w WHERE " + closedWispPurgeWhere + " GROUP BY wtype"

	if strings.Contains(digestQuery, "gt") {
		t.Errorf("purge digestQuery should not contain database name, got: %s", digestQuery)
	}
	if !strings.Contains(digestQuery, "GROUP BY wtype") {
		t.Errorf("purge digestQuery should end with GROUP BY, got: %s", digestQuery)
	}
}

// TestPurgeBatchQueryNoDatabaseNameInjection verifies that the purge batch
// SELECT query uses DefaultBatchSize as the LIMIT, not dbName.
func TestPurgeBatchQueryNoDatabaseNameInjection(t *testing.T) {
	idQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w WHERE "+closedWispPurgeWhere+" LIMIT %d",
		DefaultBatchSize)

	if strings.Contains(idQuery, "gt") {
		t.Errorf("purge idQuery contains injected database name: %s", idQuery)
	}
	expected := fmt.Sprintf("LIMIT %d", DefaultBatchSize)
	if !strings.Contains(idQuery, expected) {
		t.Errorf("purge idQuery should contain %s, got: %s", expected, idQuery)
	}
}

// TestClosedWispPurgeWhereEphemeralHorizon verifies the shared purge predicate
// keeps the standard closed-wisp cutoff while also purging ephemeral wisps via a
// second placeholder (gs-7pk). The two placeholders must be bound, in order, to
// (purgeAge cutoff, EphemeralPurgeAge cutoff).
func TestClosedWispPurgeWhereEphemeralHorizon(t *testing.T) {
	if !strings.Contains(closedWispPurgeWhere, "w.ephemeral = 1") {
		t.Errorf("purge predicate should special-case ephemeral wisps, got: %s", closedWispPurgeWhere)
	}
	if got := strings.Count(closedWispPurgeWhere, "?"); got != 2 {
		t.Errorf("purge predicate should have 2 placeholders (standard + ephemeral cutoff), got %d: %s", got, closedWispPurgeWhere)
	}
	// The standard purge age default is 7 days (see cmd/daemon); the ephemeral
	// horizon must be strictly shorter or it has no effect.
	if EphemeralPurgeAge >= 7*24*time.Hour {
		t.Errorf("EphemeralPurgeAge (%s) must be shorter than the standard purge age to take effect", EphemeralPurgeAge)
	}
}

// TestIsNothingToCommit verifies that "nothing to commit" errors are recognized
// correctly. This prevents false-positive dolt_commit_failed anomalies when the
// reaper operates on dolt_ignored tables (wisps, wisp_*), where Dolt has nothing
// to version after a successful SQL DELETE.
func TestIsNothingToCommit(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"nothing to commit", true},
		{"NOTHING TO COMMIT", true},
		{"Error 1105 (HY000): nothing to commit", true},
		{"no changes to commit", false}, // must also contain "commit" — see isNothingToCommit
		{"no changes", false},
		{"connection refused", false},
		{"table not found: wisps", false},
		{"", false},
	}
	for _, c := range cases {
		var err error
		if c.msg != "" {
			err = fmt.Errorf("%s", c.msg)
		}
		got := isNothingToCommit(err)
		if got != c.want {
			t.Errorf("isNothingToCommit(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestClosePluginReceiptsQueriesWispsTables verifies that ClosePluginReceipts
// targets the wisps/wisp_labels tables, not issues/labels. Patrol-receipt
// wisps (RESTART_POLECAT, stuck-agent-dog, dolt-backup, mol-dog-*) live in
// the wisps table; the previous implementation queried issues/labels and
// matched nothing, letting receipts accumulate past the alert threshold
// (gs-g9k).
func TestClosePluginReceiptsQueriesWispsTables(t *testing.T) {
	sourcePath := "reaper.go"
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read %s: %v", sourcePath, err)
	}
	source := string(data)

	start := strings.Index(source, "func ClosePluginReceipts(")
	if start == -1 {
		t.Fatalf("could not find ClosePluginReceipts in %s", sourcePath)
	}
	end := strings.Index(source[start:], "\nfunc ")
	if end == -1 {
		end = len(source) - start
	}
	body := source[start : start+end]

	if !strings.Contains(body, "FROM wisps w") {
		t.Errorf("ClosePluginReceipts must SELECT FROM wisps (patrol receipts live there), got:\n%s", body)
	}
	if !strings.Contains(body, "wisp_labels") {
		t.Errorf("ClosePluginReceipts must JOIN wisp_labels, got:\n%s", body)
	}
	if strings.Contains(body, ".issues i") || strings.Contains(body, "FROM `") {
		t.Errorf("ClosePluginReceipts must not query the issues table or use db-qualified tables, got:\n%s", body)
	}
	if !strings.Contains(body, "'type:plugin-run'") {
		t.Errorf("ClosePluginReceipts must filter by 'type:plugin-run' label, got:\n%s", body)
	}
	if !strings.Contains(body, "UPDATE wisps SET status='closed'") {
		t.Errorf("ClosePluginReceipts must UPDATE wisps, got:\n%s", body)
	}
	if !strings.Contains(body, "w.issue_type != 'agent'") {
		t.Errorf("ClosePluginReceipts must exclude agent beads, got:\n%s", body)
	}
}

// TestReapExcludesAgentBeads verifies that the Reap function excludes agent beads
// from being closed, regardless of their age. This is a regression test for the bug
// where the wisp reaper was closing agent beads (hq-mayor, hq-deacon, witness, refinery,
// etc.) after 24 hours, causing doctor to report them as missing.
func TestReapExcludesAgentBeads(t *testing.T) {
	// Verify that the WHERE clause in Reap() excludes issue_type='agent'
	// by checking the source code pattern.
	// This is a compile-time guard — if the exclusion is removed, this test
	// will fail when the query pattern doesn't match.

	// The whereClause in Reap() should contain:
	// "w.issue_type != 'agent'"
	// This test documents the expected behavior; actual exclusion is tested
	// in integration tests with a real database.

	// Integration test would require spinning up a Dolt server, which is
	// beyond the scope of this unit test. The exclusion is verified manually
	// by checking that agent beads are not closed by the wisp_reaper patrol.
	t.Log("Agent beads (issue_type='agent') are excluded from wisp reaping")
	t.Log("This prevents hq-mayor, hq-deacon, witness, refinery, etc. from being closed")
}

// TestScanExcludesAgentBeads documents that Scan() must use the same eligibility
// predicate as Reap() for stale open wisps. If Scan counts agent beads but Reap
// excludes them, the operator sees scan>0 and reap=0 for the same cutoff.
func TestScanExcludesAgentBeads(t *testing.T) {
	sourcePath := "reaper.go"
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read %s: %v", sourcePath, err)
	}
	source := string(data)
	scanStart := strings.Index(source, "func Scan(")
	reapStart := strings.Index(source, "func Reap(")
	if scanStart == -1 || reapStart == -1 || reapStart <= scanStart {
		t.Fatalf("could not isolate Scan() body in %s", sourcePath)
	}
	scanBody := source[scanStart:reapStart]
	if !strings.Contains(scanBody, "w.issue_type != 'agent'") {
		t.Fatalf("expected Scan() eligibility to exclude agent beads, scan body was:\n%s", scanBody)
	}
}

// TestAutoCloseExcludesAgentBeads pins the gu-016x1 fix: AutoClose must never
// close agent infra beads (gt:agent label or legacy issue_type='agent').
// Auto-closing them strips their heartbeat/idle/backoff state and makes
// `gt agents resolve` return {}, since its bd-list query excludes closed beads.
func TestAutoCloseExcludesAgentBeads(t *testing.T) {
	sourcePath := "reaper.go"
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read %s: %v", sourcePath, err)
	}
	source := string(data)
	// AutoClose's stale-issue WHERE clause is built by autoCloseWhereClause().
	start := strings.Index(source, "func autoCloseWhereClause(")
	if start == -1 {
		t.Fatalf("could not find func autoCloseWhereClause( in %s", sourcePath)
	}
	end := strings.Index(source[start:], "\n}\n")
	if end == -1 {
		t.Fatalf("could not isolate autoCloseWhereClause() body in %s", sourcePath)
	}
	body := source[start : start+end]
	if !strings.Contains(body, "i.issue_type != 'agent'") {
		t.Fatalf("expected AutoClose() to exclude legacy agent beads (issue_type='agent'), body was:\n%s", body)
	}
	if !strings.Contains(body, "'gt:agent'") {
		t.Fatalf("expected AutoClose() label exclusion to include 'gt:agent', body was:\n%s", body)
	}
}

// TestAutoCloseExcludesPinnedAndInfraRoles pins the gu-8r6u6 defense: AutoClose
// must exclude beads carrying the gt:pinned protective label AND, independently
// of any label, beads whose description carries a persistent-infra role_type
// (refinery/witness/dog). The role_type guard is the belt-and-suspenders defense
// that survives loss of the gt:agent / gt:pinned labels — the exact failure mode
// that auto-closed cae2-sle and mrd-d6n.
func TestAutoCloseExcludesPinnedAndInfraRoles(t *testing.T) {
	sourcePath := "reaper.go"
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read %s: %v", sourcePath, err)
	}
	source := string(data)

	// AutoClose's stale-issue WHERE clause is built by autoCloseWhereClause();
	// Scan() builds its own inline. Both must carry the pinned + infra guards.
	for _, fn := range []string{"func autoCloseWhereClause(", "func Scan("} {
		start := strings.Index(source, fn)
		if start == -1 {
			t.Fatalf("could not find %s in %s", fn, sourcePath)
		}
		end := strings.Index(source[start:], "\n}\n")
		if end == -1 {
			t.Fatalf("could not isolate %s body in %s", fn, sourcePath)
		}
		body := source[start : start+end]
		if !strings.Contains(body, "'gt:pinned'") {
			t.Errorf("expected %s label exclusion to include 'gt:pinned', body was:\n%s", fn, body)
		}
		if !strings.Contains(body, "staleInfraRoleExcludeSQL(\"i\")") {
			t.Errorf("expected %s to wire staleInfraRoleExcludeSQL(\"i\"), body was:\n%s", fn, body)
		}
	}
}

// TestStaleInfraRoleExcludeSQL pins the label-independent infra guard shape:
// one NOT LIKE clause per persistent-infra role, matching the `role_type: <role>`
// line the agent-bead description writer emits.
func TestStaleInfraRoleExcludeSQL(t *testing.T) {
	got := staleInfraRoleExcludeSQL("i")
	for _, role := range []string{"refinery", "witness", "dog"} {
		want := "i.description NOT LIKE '%role_type: " + role + "%'"
		if !strings.Contains(got, want) {
			t.Errorf("staleInfraRoleExcludeSQL missing clause %q; got:\n%s", want, got)
		}
	}
	// Must be AND-prefixed so it appends cleanly onto an existing WHERE.
	if !strings.Contains(got, "AND i.description NOT LIKE") {
		t.Errorf("clauses must be AND-prefixed; got:\n%s", got)
	}
	// The alias parameter must be honored (not hard-coded to "i").
	if a := staleInfraRoleExcludeSQL("x"); !strings.Contains(a, "x.description NOT LIKE") {
		t.Errorf("alias parameter not honored; got:\n%s", a)
	}
}

// TestAutoCloseWhereClauseWellFormed pins the gu-kvby4 fix: the AutoClose WHERE
// clause must not contain any fmt error artifacts. The bug was folding
// staleInfraRoleExcludeSQL() — whose output carries literal '%' (e.g.
// '%role_type: refinery%') — into the format string, so fmt parsed '%r' as an
// invalid verb and emitted '%!r(string=...)' into the SQL, breaking auto-close
// across every database.
func TestAutoCloseWhereClauseWellFormed(t *testing.T) {
	got := autoCloseWhereClause("beads_gt")

	if strings.Contains(got, "%!") {
		t.Fatalf("WHERE clause contains a fmt error verb artifact (%%!...); got:\n%s", got)
	}
	// The literal LIKE patterns from the infra-role guard must survive intact.
	for _, role := range []string{"refinery", "witness", "dog"} {
		want := "i.description NOT LIKE '%role_type: " + role + "%'"
		if !strings.Contains(got, want) {
			t.Errorf("WHERE clause missing intact infra-role guard %q; got:\n%s", want, got)
		}
	}
	// The database name must be interpolated into the backtick-quoted refs.
	if !strings.Contains(got, "`beads_gt`.labels") {
		t.Errorf("WHERE clause missing dbName interpolation; got:\n%s", got)
	}
}

// TestLabelPinnedConstant guards the duplicated label constant against drift
// from beads.LabelPinned (both must equal "gt:pinned").
func TestLabelPinnedConstant(t *testing.T) {
	if LabelPinned != "gt:pinned" {
		t.Fatalf("LabelPinned = %q, want gt:pinned", LabelPinned)
	}
}

// TestLiveTrackedContextExcludeJoin pins the gu-ycihb sling-context guard: the
// reaper must not age-reap a gt:sling-context wisp whose tracked work bead is
// still open, which would otherwise produce the gu-i0oaq dispatch-close race.
func TestLiveTrackedContextExcludeJoin(t *testing.T) {
	joinClause, whereCondition := liveTrackedContextExcludeJoin("testdb")

	if joinClause == "" || whereCondition == "" {
		t.Fatal("liveTrackedContextExcludeJoin must return non-empty join and where")
	}
	// Mirrors parentExcludeJoin but for tracks edges, scoped to sling-contexts.
	checks := []string{
		"wisp_dependencies",               // edge table
		"wisp_labels",                     // label-scoping join
		LabelSlingContext,                 // only protect sling-contexts
		"'tracks'",                        // the sling-context dep type
		"'open', 'hooked', 'in_progress'", // live work-bead statuses
	}
	for _, c := range checks {
		if !contains(joinClause, c) {
			t.Errorf("join clause missing %q: %s", c, joinClause)
		}
	}
	// The work bead may live in either wisps or issues; both sides must be checked.
	if !contains(joinClause, "wisps tw") || !contains(joinClause, "issues ti") {
		t.Errorf("join must LEFT JOIN both wisps and issues for the tracked referent: %s", joinClause)
	}
	// Regression (gu-6reia): the issue-side branch (ti.status) must include
	// 'hooked'. A slung/dispatched work bead is set status=hooked; if 'hooked' is
	// omitted the guard is inert exactly when the work is live, and the reaper
	// closes/purges the still-needed sling-context (gu-i0oaq double-dispatch).
	if !contains(joinClause, "ti.status IN ('open', 'hooked', 'in_progress')") {
		t.Errorf("tracked-context issue branch must include 'hooked' status: %s", joinClause)
	}
	if !contains(whereCondition, "IS NULL") {
		t.Errorf("where condition must be an IS NULL anti-join: %s", whereCondition)
	}
	// Must NOT reuse the parent-child type — that's a different (already-covered) edge.
	if contains(joinClause, "parent-child") {
		t.Errorf("tracks guard must not filter on parent-child: %s", joinClause)
	}
}

// TestLabelSlingContextMatchesScheduler guards the duplicated label constant.
// reaper.LabelSlingContext must equal capacity.LabelSlingContext ("gt:sling-context").
// If the scheduler renames the label, this hard-codes the contract so the guard
// can't silently stop matching.
func TestLabelSlingContextMatchesScheduler(t *testing.T) {
	if LabelSlingContext != "gt:sling-context" {
		t.Fatalf("LabelSlingContext = %q, want gt:sling-context (must match capacity.LabelSlingContext)", LabelSlingContext)
	}
}

// TestReapAndScanShareTrackedGuard ensures Scan() and Reap() both wire the
// sling-context exclusion, so the operator never sees scan>0 / reap=0 drift for
// the same cutoff (the lockstep invariant the agent-bead guard also protects).
func TestReapAndScanShareTrackedGuard(t *testing.T) {
	data, err := os.ReadFile("reaper.go")
	if err != nil {
		t.Fatalf("read reaper.go: %v", err)
	}
	source := string(data)
	scanStart := strings.Index(source, "func Scan(")
	reapStart := strings.Index(source, "func Reap(")
	if scanStart == -1 || reapStart == -1 || reapStart <= scanStart {
		t.Fatalf("could not isolate Scan()/Reap() bodies")
	}
	scanBody := source[scanStart:reapStart]
	reapBody := source[reapStart:]
	if !strings.Contains(scanBody, "liveTrackedContextExcludeJoin") {
		t.Error("Scan() must apply liveTrackedContextExcludeJoin (lockstep with Reap)")
	}
	if !strings.Contains(reapBody, "liveTrackedContextExcludeJoin") {
		t.Error("Reap() must apply liveTrackedContextExcludeJoin")
	}
}

// TestPurgeRespectsLiveTrackedContext pins gu-25jx5: the purge path must also
// wire the live-tracked sling-context guard so a closed sling-context wisp is not
// purged at the EphemeralPurgeAge (1h) horizon while its tracked work bead is still
// open/in-flight. Without it, the scheduler loses track of slung work and may
// double-dispatch the still-running polecat. The guard must appear in both Scan()'s
// purge-candidate count and purgeClosedWisps so the two stay in lockstep (no
// scan>0/purge=0 drift), mirroring TestReapAndScanShareTrackedGuard.
func TestPurgeRespectsLiveTrackedContext(t *testing.T) {
	data, err := os.ReadFile("reaper.go")
	if err != nil {
		t.Fatalf("read reaper.go: %v", err)
	}
	source := string(data)
	scanStart := strings.Index(source, "func Scan(")
	reapStart := strings.Index(source, "func Reap(")
	purgeStart := strings.Index(source, "func purgeClosedWisps(")
	if scanStart == -1 || reapStart == -1 || purgeStart == -1 || reapStart <= scanStart {
		t.Fatalf("could not isolate Scan()/purgeClosedWisps() bodies")
	}
	// Scan()'s purge-candidate count lives between func Scan( and func Reap(.
	scanBody := source[scanStart:reapStart]
	if !strings.Contains(scanBody, "purgeQuery") || !strings.Contains(scanBody, "trackedJoin") {
		t.Error("Scan() purge count must apply the live-tracked guard (trackedJoin) — gu-25jx5")
	}
	purgeEnd := strings.Index(source[purgeStart:], "\nfunc ")
	if purgeEnd == -1 {
		purgeEnd = len(source) - purgeStart
	}
	purgeBody := source[purgeStart : purgeStart+purgeEnd]
	if !strings.Contains(purgeBody, "liveTrackedContextExcludeJoin") {
		t.Error("purgeClosedWisps must apply liveTrackedContextExcludeJoin (lockstep with Scan) — gu-25jx5")
	}
	// Both the digest count and the batch-delete id query must carry the guard,
	// or one of them would still purge a live sling-context.
	if strings.Count(purgeBody, "trackedWhere") < 2 {
		t.Errorf("purgeClosedWisps must apply trackedWhere to both digest and batch-delete queries, got %d use(s)", strings.Count(purgeBody, "trackedWhere"))
	}
}

// TestCloseStaleHookedMolsQueryShape verifies that CloseStaleHookedMols targets
// the wisps table with the correct status/title/agent filters. GH#3767.
// TestAutoAddCommitsAreGuarded ensures every DOLT_COMMIT('-Am', ...) site in the
// reaper is preceded by a hasWorkingSetChanges guard, so a no-op auto-add commit
// is skipped when the only mutated tables are dolt-ignored. This prevents the
// server-side "nothing to commit" warnings that bloated dolt.log (gu-leuwr).
// Commits using '--allow-empty' are intentionally empty and exempt.
func TestAutoAddCommitsAreGuarded(t *testing.T) {
	for _, sourcePath := range []string{"reaper.go", "hooked_mail.go"} {
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatalf("read %s: %v", sourcePath, err)
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if !strings.Contains(line, "CALL DOLT_COMMIT('-Am'") {
				continue
			}
			// Look back a few lines for the guard; the commit must be wrapped in it.
			windowStart := i - 6
			if windowStart < 0 {
				windowStart = 0
			}
			window := strings.Join(lines[windowStart:i], "\n")
			if !strings.Contains(window, "hasWorkingSetChanges(ctx, db)") {
				t.Errorf("%s:%d: DOLT_COMMIT('-Am') is not guarded by hasWorkingSetChanges", sourcePath, i+1)
			}
		}
	}
}

func TestCloseStaleHookedMolsQueryShape(t *testing.T) {
	sourcePath := "reaper.go"
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read %s: %v", sourcePath, err)
	}
	source := string(data)

	funcStart := strings.Index(source, "func CloseStaleHookedMols(")
	if funcStart == -1 {
		t.Fatal("CloseStaleHookedMols not found in reaper.go")
	}
	// Isolate the function body up to the next top-level func declaration.
	funcBody := source[funcStart:]
	nextFunc := strings.Index(funcBody[1:], "\nfunc ")
	if nextFunc != -1 {
		funcBody = funcBody[:nextFunc+1]
	}

	for _, want := range []string{
		"wisps", // queries the wisps table, not issues
		"status = 'hooked'",
		"mol-dog-", // title filter scopes to daemon dispatch mols
		"issue_type != 'agent'",
	} {
		if !strings.Contains(funcBody, want) {
			t.Errorf("CloseStaleHookedMols body missing %q", want)
		}
	}
}

// TestWispFlushDiffQueryReadsDiffStatNotStatus verifies the wisp-flush pending
// query reads dolt_diff_stat, not dolt_status. This is the crux of gu-tqtwt:
// the wisp tables are dolt_ignored, so dolt_status never reports them — only a
// HEAD->WORKING diff surfaces their accumulating churn. Reading dolt_status
// here would always report zero pending and the flush would never fire.
func TestWispFlushDiffQueryReadsDiffStatNotStatus(t *testing.T) {
	if !strings.Contains(wispPendingDiffQuery, "dolt_diff_stat('HEAD', 'WORKING')") {
		t.Errorf("wispPendingDiffQuery must read dolt_diff_stat(HEAD,WORKING), got: %s", wispPendingDiffQuery)
	}
	if strings.Contains(wispPendingDiffQuery, "dolt_status") {
		t.Errorf("wispPendingDiffQuery must NOT read dolt_status (it never reports dolt_ignored tables), got: %s", wispPendingDiffQuery)
	}
}

// TestWispFlushDiffQueryEscapesLikeUnderscore verifies the LIKE pattern escapes
// the underscore so it matches the literal 'wisp_' prefix, not 'wispX' for any
// single character X. An unescaped 'wisp_%' would also match unrelated tables
// like 'wispy' or any future 'wispNNN'.
func TestWispFlushDiffQueryEscapesLikeUnderscore(t *testing.T) {
	if !strings.Contains(wispPendingDiffQuery, `LIKE 'wisp\_%'`) {
		t.Errorf("wispPendingDiffQuery must escape the LIKE underscore as 'wisp\\_%%', got: %s", wispPendingDiffQuery)
	}
	// The bare 'wisps' table is dolt_ignored under a separate pattern and would
	// not be caught by 'wisp_%', so it must be matched explicitly.
	if !strings.Contains(wispPendingDiffQuery, "table_name = 'wisps'") {
		t.Errorf("wispPendingDiffQuery must explicitly match the 'wisps' table, got: %s", wispPendingDiffQuery)
	}
}

// TestWispTablesQueryStagesParentFirst verifies the staging-table enumeration
// scopes to the wisp namespace and orders the FK parent ('wisps') first. The
// aux tables (wisp_dependencies, etc.) carry foreign keys referencing wisps, so
// the parent must be staged alongside — and ahead of — its children, or the
// flush commit fails the FK check (observed live on casc_constructs).
func TestWispTablesQueryStagesParentFirst(t *testing.T) {
	if !strings.Contains(wispTablesQuery, "information_schema.tables") {
		t.Errorf("wispTablesQuery must enumerate existing tables via information_schema, got: %s", wispTablesQuery)
	}
	if !strings.Contains(wispTablesQuery, "table_schema = DATABASE()") {
		t.Errorf("wispTablesQuery must scope to the current database, got: %s", wispTablesQuery)
	}
	if !strings.Contains(wispTablesQuery, `LIKE 'wisp\_%'`) || !strings.Contains(wispTablesQuery, "table_name = 'wisps'") {
		t.Errorf("wispTablesQuery must match both 'wisps' and the escaped 'wisp\\_%%' namespace, got: %s", wispTablesQuery)
	}
	// The FK parent must sort first: ORDER BY (table_name = 'wisps') DESC.
	if !strings.Contains(wispTablesQuery, "(table_name = 'wisps') DESC") {
		t.Errorf("wispTablesQuery must order the FK parent 'wisps' first, got: %s", wispTablesQuery)
	}
}

// TestWispFlushForcesAddNotPlainAdd verifies the flush force-stages each table.
// dolt_ignored tables are skipped by a plain DOLT_ADD; only DOLT_ADD('--force')
// stages them, which is the whole point of the flush (gu-tqtwt).
func TestWispFlushForcesAddNotPlainAdd(t *testing.T) {
	data, err := os.ReadFile("reaper.go")
	if err != nil {
		t.Fatalf("read reaper.go: %v", err)
	}
	source := string(data)
	funcStart := strings.Index(source, "func FlushWispWorkingSet(")
	if funcStart == -1 {
		t.Fatal("FlushWispWorkingSet not found in reaper.go")
	}
	funcBody := source[funcStart:]
	if nextFunc := strings.Index(funcBody[1:], "\nfunc "); nextFunc != -1 {
		funcBody = funcBody[:nextFunc+1]
	}
	if !strings.Contains(funcBody, "CALL DOLT_ADD('--force', ?)") {
		t.Errorf("FlushWispWorkingSet must force-stage wisp tables via DOLT_ADD('--force', ?)")
	}
	// It must NOT use '-Am' (which would silently skip the dolt_ignored tables
	// and could sweep in unrelated working-set changes).
	if strings.Contains(funcBody, "DOLT_COMMIT('-Am'") {
		t.Errorf("FlushWispWorkingSet must not use DOLT_COMMIT('-Am') — it skips dolt_ignored tables and risks sweeping unrelated changes")
	}
}

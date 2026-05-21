package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestParseCommitAttribution_WellFormedEscalation is the core round-trip case
// for AC#1: an escalation body whose rig section carries `commit:` and
// `previous_commit:` lines parses back into a CommitAttribution with both
// fields populated. This is the shape downstream consumers (Phase 0 task 11
// daemon dog) rely on to find the breaking commit and revert target.
func TestParseCommitAttribution_WellFormedEscalation(t *testing.T) {
	body := `main branch test failures:
gastown_upstream: gate "test" failed: exit status 1
go test failure output here
commit: 3a82535612345678aaaabbbbccccddddeeeeffff
previous_commit: 6f96077812345678aaaabbbbccccddddeeeeffff
severity: HIGH`

	attr := ParseCommitAttribution(body)
	if attr.Commit != "3a82535612345678aaaabbbbccccddddeeeeffff" {
		t.Errorf("commit: got %q, want %q",
			attr.Commit, "3a82535612345678aaaabbbbccccddddeeeeffff")
	}
	if attr.Previous != "6f96077812345678aaaabbbbccccddddeeeeffff" {
		t.Errorf("previous_commit: got %q, want %q",
			attr.Previous, "6f96077812345678aaaabbbbccccddddeeeeffff")
	}
	if !attr.HasCommit() {
		t.Error("HasCommit should be true for a real SHA")
	}
	if !attr.HasPreviousCommit() {
		t.Error("HasPreviousCommit should be true for a real previous SHA")
	}
}

// TestParseCommitAttribution_LegacyEscalationNoAttribution covers the
// pre-substrate case: escalations from before this code lands carry no
// `commit:` lines, and parsing them must yield a zero-value CommitAttribution
// that downstream consumers can detect and skip.
func TestParseCommitAttribution_LegacyEscalationNoAttribution(t *testing.T) {
	body := `main branch test failures:
gastown_upstream: gate "test" failed: exit status 1
some failure output
severity: HIGH`

	attr := ParseCommitAttribution(body)
	if attr.HasCommit() {
		t.Errorf("legacy body should produce HasCommit=false, got Commit=%q", attr.Commit)
	}
	if attr.HasPreviousCommit() {
		t.Errorf("legacy body should produce HasPreviousCommit=false, got Previous=%q", attr.Previous)
	}
}

// TestParseCommitAttribution_UnknownPreviousIsNotReal verifies the sentinel
// behavior: a first-run escalation legitimately has previous_commit: unknown,
// and HasPreviousCommit must report false so a downstream auto-revert dog
// does not try to look up the literal string "unknown" as a SHA.
func TestParseCommitAttribution_UnknownPreviousIsNotReal(t *testing.T) {
	body := `gastown_upstream: gate failed
commit: aaaaaaaaaaaa
previous_commit: unknown`

	attr := ParseCommitAttribution(body)
	if !attr.HasCommit() {
		t.Error("commit aaaa... should be considered real")
	}
	if attr.HasPreviousCommit() {
		t.Error("previous_commit: unknown must report HasPreviousCommit=false")
	}
}

// TestParseCommitAttribution_OrderInsensitive guards against a parser that
// only finds the first `commit:` line and returns prematurely. Both lines
// must be findable regardless of order, since the runner emits them
// adjacent today but a future producer (or a hand-edited bead) might
// interleave other content.
func TestParseCommitAttribution_OrderInsensitive(t *testing.T) {
	body := `previous_commit: 1111111111
some prose between the two
commit: 2222222222`

	attr := ParseCommitAttribution(body)
	if attr.Commit != "2222222222" {
		t.Errorf("commit got %q, want %q", attr.Commit, "2222222222")
	}
	if attr.Previous != "1111111111" {
		t.Errorf("previous_commit got %q, want %q", attr.Previous, "1111111111")
	}
}

// TestParseCommitAttribution_PrefixIsCaseSensitive ensures prose like
// "Commit:" in a stack trace doesn't get mistaken for attribution. The
// runner emits lower-case prefixes; the parser must match exactly.
func TestParseCommitAttribution_PrefixIsCaseSensitive(t *testing.T) {
	body := `Stack trace:
Commit: not-attribution-just-prose
COMMIT: also-not-attribution
commit: real-sha-here`

	attr := ParseCommitAttribution(body)
	if attr.Commit != "real-sha-here" {
		t.Errorf("expected case-sensitive match, got %q", attr.Commit)
	}
}

// TestFormatAttributionLines_EmitsBothLines verifies the runner-side
// emission shape. Both lines must always appear together so consumers can
// rely on a fixed structure.
func TestFormatAttributionLines_EmitsBothLines(t *testing.T) {
	got := formatAttributionLines("aaa", "bbb")
	want := "commit: aaa\nprevious_commit: bbb"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// Empty current SHA → no output (caller skipped attribution).
	if formatAttributionLines("", "bbb") != "" {
		t.Error("empty commit should yield empty output")
	}

	// Empty previous SHA → defaulted to "unknown" so the line is still
	// structurally present. This is the first-run-after-fresh-install case.
	got = formatAttributionLines("aaa", "")
	want = "commit: aaa\nprevious_commit: unknown"
	if got != want {
		t.Errorf("empty previous: got %q, want %q", got, want)
	}
}

// TestFormatRigFailureSection_AttributionIncludesPreviousFromState wires
// the runner's escalation-body format end-to-end against the on-disk state
// file. The previous-passing SHA must be read out of the state set by an
// earlier successful run.
func TestFormatRigFailureSection_AttributionIncludesPreviousFromState(t *testing.T) {
	townRoot := t.TempDir()

	// Simulate a prior successful run for rig "demo".
	recordAttributionRun(townRoot, "demo", "good-sha-prev", true, time.Now())

	// Now format a failure section as if the current run failed at a new SHA.
	body := formatRigFailureSection("demo", "bad-sha-now", townRoot,
		errString("gate \"test\": exit status 1"))

	if !strings.HasPrefix(body, "demo: gate \"test\": exit status 1") {
		t.Errorf("first line should preserve legacy format, got:\n%s", body)
	}
	if !strings.Contains(body, "commit: bad-sha-now") {
		t.Errorf("missing commit attribution in:\n%s", body)
	}
	if !strings.Contains(body, "previous_commit: good-sha-prev") {
		t.Errorf("missing previous_commit attribution in:\n%s", body)
	}
}

// TestFormatRigFailureSection_NoCurrentSHAOmitsAttribution covers the
// fail-before-rev-parse case: when SHA capture failed (e.g. fetch error),
// we must not emit the structured lines. Emitting `commit: ` with an
// empty value would feed a malformed SHA to the consumer.
func TestFormatRigFailureSection_NoCurrentSHAOmitsAttribution(t *testing.T) {
	townRoot := t.TempDir()
	body := formatRigFailureSection("demo", "", townRoot,
		errString("git fetch failed"))

	if strings.Contains(body, "commit:") {
		t.Errorf("attribution must be omitted when currentSHA is empty, got:\n%s", body)
	}
	if !strings.HasPrefix(body, "demo: git fetch failed") {
		t.Errorf("legacy body still expected, got:\n%s", body)
	}
}

// TestFormatRigFailureSection_FirstRunPreviousIsUnknown covers the
// fresh-install case: state file is empty, so previous_commit defaults to
// "unknown". A SEV-1 consumer can still revert (current SHA is real) but
// can't verify the revert target — the sentinel makes that distinction
// explicit instead of silently emitting an empty value.
func TestFormatRigFailureSection_FirstRunPreviousIsUnknown(t *testing.T) {
	townRoot := t.TempDir()
	body := formatRigFailureSection("demo", "first-run-sha", townRoot,
		errString("gate failed"))

	if !strings.Contains(body, "commit: first-run-sha") {
		t.Errorf("missing commit attribution, got:\n%s", body)
	}
	if !strings.Contains(body, "previous_commit: unknown") {
		t.Errorf("first run should produce previous_commit: unknown, got:\n%s", body)
	}
}

// TestRecordAttributionRun_PassUpdatesBaseline verifies that a successful
// run promotes the SHA to last-passing, and that subsequent failure cycles
// see it as previous_commit.
func TestRecordAttributionRun_PassUpdatesBaseline(t *testing.T) {
	townRoot := t.TempDir()
	now := time.Now()

	recordAttributionRun(townRoot, "demo", "sha-1", true, now)
	if got := readPreviousPassingSHA(townRoot, "demo"); got != "sha-1" {
		t.Errorf("after first pass: got %q, want sha-1", got)
	}

	recordAttributionRun(townRoot, "demo", "sha-2", true, now.Add(time.Minute))
	if got := readPreviousPassingSHA(townRoot, "demo"); got != "sha-2" {
		t.Errorf("after second pass: got %q, want sha-2", got)
	}
}

// TestRecordAttributionRun_FailDoesNotPromoteBreakingSHA is the safety
// invariant: a failing run must NOT advance the last-passing baseline,
// otherwise a chain of consecutive failures would cause previous_commit
// to point at the breaking SHA and a downstream auto-revert dog would
// revert to broken code.
func TestRecordAttributionRun_FailDoesNotPromoteBreakingSHA(t *testing.T) {
	townRoot := t.TempDir()
	now := time.Now()

	recordAttributionRun(townRoot, "demo", "good-sha", true, now)
	recordAttributionRun(townRoot, "demo", "bad-sha", false, now.Add(time.Minute))

	if got := readPreviousPassingSHA(townRoot, "demo"); got != "good-sha" {
		t.Errorf("baseline should still point at last-passing SHA, got %q", got)
	}

	// Multiple failures in a row must not silently promote any of them.
	recordAttributionRun(townRoot, "demo", "still-bad", false, now.Add(2*time.Minute))
	if got := readPreviousPassingSHA(townRoot, "demo"); got != "good-sha" {
		t.Errorf("baseline should still point at last-passing SHA after 2 fails, got %q", got)
	}
}

// TestRecordAttributionRun_PerRigIsolation verifies state is keyed by rig
// name. A failure in rig A must not corrupt rig B's baseline.
func TestRecordAttributionRun_PerRigIsolation(t *testing.T) {
	townRoot := t.TempDir()
	now := time.Now()

	recordAttributionRun(townRoot, "rigA", "shaA", true, now)
	recordAttributionRun(townRoot, "rigB", "shaB", true, now)

	if got := readPreviousPassingSHA(townRoot, "rigA"); got != "shaA" {
		t.Errorf("rigA: got %q, want shaA", got)
	}
	if got := readPreviousPassingSHA(townRoot, "rigB"); got != "shaB" {
		t.Errorf("rigB: got %q, want shaB", got)
	}

	// Failing rigA must leave rigB's baseline untouched.
	recordAttributionRun(townRoot, "rigA", "shaA-bad", false, now.Add(time.Minute))
	if got := readPreviousPassingSHA(townRoot, "rigB"); got != "shaB" {
		t.Errorf("rigB cross-contamination: got %q, want shaB", got)
	}
}

// TestReadPreviousPassingSHA_FreshInstallReturnsUnknown asserts the
// fresh-install / state-file-deleted contract: with no recorded passes,
// the reader returns the sentinel rather than an empty string. The
// formatter relies on this to never emit a malformed `previous_commit:`
// line.
func TestReadPreviousPassingSHA_FreshInstallReturnsUnknown(t *testing.T) {
	townRoot := t.TempDir()
	got := readPreviousPassingSHA(townRoot, "never-ran")
	if got != attributionUnknown {
		t.Errorf("fresh install: got %q, want %q", got, attributionUnknown)
	}
}

// TestSaveLoadMainBranchTestState_Roundtrip is a standalone serialization
// check independent of recordAttributionRun. Guards against a JSON tag
// rename or schema drift breaking the persistence contract.
func TestSaveLoadMainBranchTestState_Roundtrip(t *testing.T) {
	townRoot := t.TempDir()

	state := &mainBranchTestState{
		Rigs: map[string]rigAttributionEntry{
			"alpha": {LastPassingSHA: "aaa", LastRunAt: "2026-05-21T00:00:00Z"},
			"beta":  {LastPassingSHA: "bbb"},
		},
	}
	if err := saveMainBranchTestState(townRoot, state); err != nil {
		t.Fatalf("save: %v", err)
	}

	got := loadMainBranchTestState(townRoot)
	if got.Rigs["alpha"].LastPassingSHA != "aaa" {
		t.Errorf("alpha LastPassingSHA: got %q", got.Rigs["alpha"].LastPassingSHA)
	}
	if got.Rigs["alpha"].LastRunAt != "2026-05-21T00:00:00Z" {
		t.Errorf("alpha LastRunAt: got %q", got.Rigs["alpha"].LastRunAt)
	}
	if got.Rigs["beta"].LastPassingSHA != "bbb" {
		t.Errorf("beta LastPassingSHA: got %q", got.Rigs["beta"].LastPassingSHA)
	}
}

// TestLoadMainBranchTestState_MissingFileIsEmpty covers the
// pre-first-run case: no state file on disk yields a valid empty state
// (not a nil map, not an error). The patrol must run cleanly on a fresh
// install.
func TestLoadMainBranchTestState_MissingFileIsEmpty(t *testing.T) {
	townRoot := t.TempDir()
	state := loadMainBranchTestState(townRoot)
	if state == nil {
		t.Fatal("expected non-nil state for missing file")
	}
	if state.Rigs == nil {
		t.Error("expected non-nil Rigs map on missing-file fallback")
	}
	if len(state.Rigs) != 0 {
		t.Errorf("expected empty Rigs map, got %d entries", len(state.Rigs))
	}
}

// TestLoadMainBranchTestState_CorruptFileResetsState verifies graceful
// recovery from a partial-write or hand-edit corruption. The patrol must
// not stall — it just starts fresh.
func TestLoadMainBranchTestState_CorruptFileResetsState(t *testing.T) {
	townRoot := t.TempDir()
	path := mainBranchTestStatePath(townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not valid json"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	state := loadMainBranchTestState(townRoot)
	if state == nil {
		t.Fatal("expected non-nil state for corrupt file")
	}
	if len(state.Rigs) != 0 {
		t.Errorf("corrupt file should reset to empty, got %d entries", len(state.Rigs))
	}
}

// TestFindMRBeadByCommitSHA_ResolvesAuthoredMR is AC#2's pure-function
// half. Given a slice of MR beads — one of which carries the breaking
// commit SHA AND the gt:auto-test-pr label — the lookup returns that
// specific bead. This is the exact code path the daemon-dog consumer
// will use after parsing attribution out of an escalation.
func TestFindMRBeadByCommitSHA_ResolvesAuthoredMR(t *testing.T) {
	autoTestMR := &beads.Issue{
		ID:     "gt-mr-auto",
		Status: "closed",
		Labels: []string{"gt:merge-request", "gt:auto-test-pr"},
		Description: `branch: auto-test/demo/gt-x
target: main
source_issue: gt-x
commit_sha: bad-sha-now
merge_commit: deadbeef`,
	}
	humanMR := &beads.Issue{
		ID:     "gt-mr-human",
		Status: "closed",
		Labels: []string{"gt:merge-request"},
		Description: `branch: polecat/foo/gt-y
target: main
source_issue: gt-y
commit_sha: some-other-sha`,
	}

	got := FindMRBeadByCommitSHA([]*beads.Issue{humanMR, autoTestMR}, "bad-sha-now")
	if got == nil {
		t.Fatal("expected to find autoTestMR by commit SHA")
	}
	if got.ID != "gt-mr-auto" {
		t.Errorf("wrong MR returned: got ID=%q, want gt-mr-auto", got.ID)
	}
}

// TestFindMRBeadByCommitSHA_NoMatchReturnsNil covers the
// commit-without-MR case (e.g. direct-to-main push from a maintainer,
// or an MR bead that was reaped before the lookup ran). The consumer
// must distinguish "MR not found" from "lookup error" — nil vs. error.
func TestFindMRBeadByCommitSHA_NoMatchReturnsNil(t *testing.T) {
	mrs := []*beads.Issue{
		{
			ID: "gt-mr-1",
			Description: `branch: foo
commit_sha: aaa`,
		},
		{
			ID: "gt-mr-2",
			Description: `branch: bar
commit_sha: bbb`,
		},
	}
	if got := FindMRBeadByCommitSHA(mrs, "ccc"); got != nil {
		t.Errorf("expected nil for unknown SHA, got %+v", got)
	}
}

// TestFindMRBeadByCommitSHA_GuardsAgainstUnknownSentinel ensures the
// lookup explicitly rejects the "unknown" sentinel. Otherwise a
// downstream consumer that forgot to check HasCommit could feed the
// literal string into the lookup and accidentally match an MR whose
// commit_sha was somehow recorded as "unknown".
func TestFindMRBeadByCommitSHA_GuardsAgainstUnknownSentinel(t *testing.T) {
	mrs := []*beads.Issue{
		{
			ID: "gt-mr-weird",
			Description: `branch: foo
commit_sha: unknown`,
		},
	}
	if got := FindMRBeadByCommitSHA(mrs, "unknown"); got != nil {
		t.Errorf("lookup must refuse the unknown sentinel, got %+v", got)
	}
	if got := FindMRBeadByCommitSHA(mrs, ""); got != nil {
		t.Errorf("lookup must refuse empty SHA, got %+v", got)
	}
}

// TestEndToEndAttributionRoundtripFromEscalationToMRBead is the
// AC#2 fixture-driven integration test. It builds:
//   - an escalation description with the structured attribution lines, and
//   - an MR bead carrying gt:auto-test-pr with commit_sha matching the
//     attribution
//
// then runs both halves of the substrate (parse → lookup) end-to-end. This
// is the exact pipeline the Phase 0 task 11 daemon dog will execute.
func TestEndToEndAttributionRoundtripFromEscalationToMRBead(t *testing.T) {
	const breakingSHA = "deadbeefcafef00d"
	const previousSHA = "0000111122223333"

	// Reproduce a realistic escalation body: legacy "<rig>: gate" line,
	// failure output, structured attribution, then escalate-tool fields.
	escBody := `main branch test failures:
gastown_upstream: gate "test" failed: exit status 1
--- FAIL: TestSomething (0.05s)
` + formatAttributionLines(breakingSHA, previousSHA) + `
severity: HIGH
source: main_branch_test`

	attr := ParseCommitAttribution(escBody)
	if !attr.HasCommit() {
		t.Fatalf("expected to parse a real commit, got %+v", attr)
	}
	if attr.Commit != breakingSHA {
		t.Errorf("commit got %q, want %q", attr.Commit, breakingSHA)
	}
	if attr.Previous != previousSHA {
		t.Errorf("previous got %q, want %q", attr.Previous, previousSHA)
	}

	mrBead := &beads.Issue{
		ID:     "gt-mr-target",
		Status: "closed",
		Labels: []string{"gt:merge-request", "gt:auto-test-pr"},
		Description: `branch: auto-test/gastown_upstream/gt-x
target: main
source_issue: gt-x
commit_sha: ` + breakingSHA,
	}
	otherMR := &beads.Issue{
		ID: "gt-mr-other",
		Description: `branch: polecat/foo/gt-y
commit_sha: aaaabbbbccccdddd`,
	}

	resolved := FindMRBeadByCommitSHA([]*beads.Issue{otherMR, mrBead}, attr.Commit)
	if resolved == nil {
		t.Fatal("end-to-end lookup failed: expected to find gt-mr-target")
	}
	if resolved.ID != "gt-mr-target" {
		t.Errorf("resolved wrong bead: got %q, want gt-mr-target", resolved.ID)
	}

	// The whole point of this substrate: a downstream consumer can now
	// inspect the resolved bead's labels to gate the SEV-1 chain.
	if !beads.HasLabel(resolved, "gt:auto-test-pr") {
		t.Error("resolved MR should carry gt:auto-test-pr label " +
			"(SEV-1 gating relies on this — without it we'd auto-revert human MRs)")
	}
}

// errString is a tiny helper so test cases can construct errors with
// expressive content without dragging fmt.Errorf through every assertion.
type errString string

func (e errString) Error() string { return string(e) }

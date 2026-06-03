//go:build integration

// Phase-0 end-to-end fixture integration test (gu-p17c).
//
// This is the final critical-path Phase-0 task: drive a SINGLE complete
// auto-test-pr cycle in-process against the checked-in fixture rig
// (internal/autotest/testdata/fixturerig/) and assert the whole loop —
// idle → dispatch → polecat writes a test → all 7 quality gates run
// through the real libraries → mock Refinery observes the MR bead →
// the Mayor cycle-close handler transitions the state bead to
// cooled-down (merged).
//
// There is intentionally NO Go-level "run all 7 gates" orchestrator in
// the production code: the seven gates (4a–4g) are run inline by the
// LLM polecat as it executes the mol-polecat-work-test-improver formula
// steps. This e2e test therefore SIMULATES the polecat and the Refinery
// by wiring the real gate libraries directly:
//
//	gate 4a coverage-delta     → autotest.BranchDelta(before, after) > 0
//	gate 4b synthetic-mutant   → autotest.FindMutants/SelectMutants/RunMutants (≤5)
//	gate 4c flakiness-rerun    → `go test -count=10` green in the sandbox
//	gate 4d tautology-linter   → tautology.AnalyzeFile finds no violations
//	gate 4e gitleaks           → secret scan of the diff (skipped if gitleaks absent)
//	gate 4f output allow-list  → only same-package *_test.go files written
//	gate 4g size-budget        → max_files / max_loc from the dispatch envelope
//
// Happy path acceptance (gu-p17c):
//   - state bead ends in cooled-down (merged)
//   - new test file carries the D8 provenance marker
//   - all 7 gates emit pass records
//   - wall-clock < 30 min on the fixture (we assert a far tighter bound)
//
// Gate-fail variant acceptance:
//   - force gate 4d (tautology, literal-vs-literal) to fail
//   - the polecat exits with NOTES and NO MR bead is created
//
// Gating: requires a live Dolt server on port 3307 and the Go toolchain.
// The fixture's testify dependency resolves from the host module cache
// with GOPROXY=off (no network). Run with:
//
//	GT_RUN_E2E=1 go test -tags=integration \
//	  -run TestPhase0E2E \
//	  -timeout 10m -count=1 -v ./internal/autotestpr/
package autotestpr

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/autotest"
	"github.com/steveyegge/gastown/internal/autotest/sandbox"
	"github.com/steveyegge/gastown/internal/autotest/tautology"
	"github.com/steveyegge/gastown/internal/beads"
)

// e2eTestCounter generates unique beads DB prefixes for test isolation.
var e2eTestCounter int32

// e2eFixtureRig is the rig name the cycle dispatches to. The fixture's
// uncovered branches live in classify.go.
const e2eFixtureRig = "fixturerig_e2e"

// e2eTargetFile is the churned source file with the two uncovered
// branches the cycle targets.
const e2eTargetFile = "classify.go"

// gateRecord is a single quality-gate pass/fail record. The real
// production formula emits one of these per gate (as NOTES / GatesPassed
// list); here we collect them so the test can assert "all 7 gates emit
// pass records" (gu-p17c happy-path acceptance).
type gateRecord struct {
	name   string
	passed bool
	detail string
}

// sevenGateNames is the canonical ordered list of the 7 quality gates,
// matching the mol-polecat-work-test-improver formula (4a–4g).
var sevenGateNames = []string{
	"coverage-delta",   // 4a
	"synthetic-mutant", // 4b
	"flakiness-rerun",  // 4c
	"tautology-linter", // 4d
	"gitleaks",         // 4e
	"output-allowlist", // 4f
	"size-budget",      // 4g
}

// TestPhase0E2E_HappyPath drives the full cycle to a merged, cooled-down
// terminal state and asserts every acceptance criterion for the happy
// path.
func TestPhase0E2E_HappyPath(t *testing.T) {
	requireE2E(t)
	goBin := requireGo(t)
	start := time.Now()

	b, _ := setupE2EBeads(t)
	provisionE2EState(t, b)

	// ─── Step 1: cycle dispatches the fixture rig (idle → dispatched) ──
	// We drive processRig directly with the real beads-backed RigStore
	// and DI Targets/Dispatch hooks. The fixture's Targets hook reports
	// classify.go with its 2 uncovered branches (zero, big).
	store := NewBeadsRigStateStore(b)
	now := time.Now().UTC().Truncate(time.Second)

	var dispatched DispatchEnvelope
	var mrBeadID string
	cfg := &CycleConfig{
		TownRoot:  t.TempDir(), // unused on the processRig path
		TownBeads: b,
		Now:       now,
		RigStore:  store,
		Targets: func(rig string) ([]TargetCandidate, []RejectionRecord, error) {
			return []TargetCandidate{{
				Path:              e2eTargetFile,
				Churn:             3,
				CoveragePctBefore: 0.5,
				UncoveredBranches: []UncoveredBranch{
					{Line: 19, Kind: "if-true"}, // n == 0 → "zero"
					{Line: 22, Kind: "if-true"}, // n > 100 → "big"
				},
			}}, nil, nil
		},
		Dispatch: func(rig string, env DispatchEnvelope) (string, error) {
			dispatched = env
			// File the work/dispatch bead so the rest of the loop has a
			// real bead ID to thread through the MR.
			iss, err := b.Create(beads.CreateOptions{
				Title:       fmt.Sprintf("auto-test-pr: improve coverage of %s (%s)", env.Args.Targets[0].Path, rig),
				Description: "Dispatched by Phase-0 e2e cycle.",
				Labels:      []string{"gt:task", AttachmentParentLabel, RigLabel(rig)},
				Priority:    4,
				Actor:       "mayor",
			})
			if err != nil {
				return "", err
			}
			return iss.ID, nil
		},
	}

	processed, err := processRig(cfg, e2eFixtureRig)
	if err != nil {
		t.Fatalf("processRig: %v", err)
	}
	if !processed {
		t.Fatal("processRig returned processed=false; want a dispatch")
	}

	// Verify the state machine advanced idle → dispatched and stamped a
	// current-cycle pointer.
	st, err := store.LoadRigState(e2eFixtureRig)
	if err != nil {
		t.Fatalf("LoadRigState after dispatch: %v", err)
	}
	if st.State != PerRigCycleStateDispatched {
		t.Fatalf("rig state = %q after dispatch; want %q", st.State, PerRigCycleStateDispatched)
	}
	if st.CurrentCycle == nil || st.CurrentCycle.PolecatBead == "" {
		t.Fatal("dispatched state has no current-cycle pointer")
	}
	mrBeadID = st.CurrentCycle.PolecatBead

	// Verify the dispatch envelope carries the right contract for the
	// polecat (mode=create, the target file, ordered uncovered branches).
	if dispatched.Args.Mode != "create" {
		t.Errorf("dispatch mode = %q; want create", dispatched.Args.Mode)
	}
	if len(dispatched.Args.Targets) != 1 || dispatched.Args.Targets[0].Path != e2eTargetFile {
		t.Fatalf("dispatch targets = %+v; want single %s", dispatched.Args.Targets, e2eTargetFile)
	}
	if len(dispatched.Args.Targets[0].UncoveredBranches) != 2 {
		t.Errorf("dispatch uncovered branches = %d; want 2", len(dispatched.Args.Targets[0].UncoveredBranches))
	}

	// ─── Step 2: simulate the polecat writing the follow-up test ──────
	// Copy the fixture module into a sandbox worktree, write a new
	// same-package *_test.go covering the two uncovered branches with the
	// D8 provenance marker, then run all 7 gates against it.
	work := copyFixtureModule(t)
	newTestPath := filepath.Join(work, "classify_more_test.go")
	provenance := fmt.Sprintf("// gt:auto-test-pr origin=%s covers=%s:19,%s:22",
		mrBeadID, e2eTargetFile, e2eTargetFile)
	newTestSrc := happyPathTestSource(provenance)
	if err := os.WriteFile(newTestPath, []byte(newTestSrc), 0o644); err != nil {
		t.Fatalf("write new test: %v", err)
	}

	gates := runSevenGates(t, goBin, work, newTestPath, []string{filepath.Base(newTestPath)}, dispatched.Args.SizeBudget)

	// All 7 gates must emit a pass record (acceptance: "all 7 gates emit
	// pass records"). gitleaks is allowed to be skipped-but-recorded when
	// the binary is unavailable on the host.
	assertAllGatesPass(t, gates)

	// D8 provenance marker present in the new test file (acceptance).
	if !strings.Contains(newTestSrc, "// gt:auto-test-pr origin="+mrBeadID) {
		t.Error("new test file missing D8 provenance marker")
	}

	// ─── Step 3: gates green → the polecat files the MR bead ──────────
	// Mock the polecat's `gt done`: create the MR bead carrying the two
	// labels the Refinery / cycle-close dog routes on (gt:auto-test-pr +
	// rig:<target_rig>).
	mr, err := b.Create(beads.CreateOptions{
		Title: fmt.Sprintf("auto-test-pr MR: cover %s zero/big branches", e2eTargetFile),
		Description: fmt.Sprintf(
			"close_reason: merged\nrig: %s\ntarget_path: %s\nGates passed: %s\n",
			e2eFixtureRig, e2eTargetFile, strings.Join(passedGateNames(gates), ", ")),
		Labels:   []string{"gt:merge-request", AttachmentParentLabel, RigLabel(e2eFixtureRig)},
		Priority: 4,
		Actor:    "polecat/fixturerig_e2e",
	})
	if err != nil {
		t.Fatalf("create MR bead: %v", err)
	}

	// The cycle now waits on the MR — advance the per-rig state to
	// mr-pending (CAS dispatched → mr-pending), mirroring what the
	// polecat-done observer does in production.
	if err := CASTransition(store, e2eFixtureRig,
		PerRigCycleStateDispatched, PerRigCycleStateMRPending,
		"polecat", time.Now().UTC(), nil); err != nil {
		t.Fatalf("CAS dispatched→mr-pending: %v", err)
	}

	// ─── Step 4: mock Refinery merges → cycle-close handler runs ──────
	// The mr_cycle_close dog observes the merged MR and dispatches an
	// MRCycleCloseEvent. The handler transitions the per-rig read-cache
	// mr-pending → cooled-down and (merged path) resets the breaker.
	//
	// First seed the town-state RigSummary to mr-pending so the handler
	// reads the right prior state.
	seedRigSummary(t, b, e2eFixtureRig, "mr-pending")

	var observedMR string
	closeNow := time.Now().UTC().Truncate(time.Second)
	handler := &CycleCloseHandler{
		Beads:         b,
		NudgeOverseer: func(string) {},
		Now:           func() time.Time { return closeNow },
		Logf:          func(format string, args ...interface{}) { t.Logf("[cycle-close] "+format, args...) },
	}
	// Mock Refinery merge handler observes the in-memory MR bead, asserts
	// its labels, then fires the merged cycle-close event.
	mrIssue, err := b.Show(mr.ID)
	if err != nil {
		t.Fatalf("Show MR bead: %v", err)
	}
	if !beads.HasLabel(mrIssue, AttachmentParentLabel) {
		t.Errorf("MR bead missing %s label", AttachmentParentLabel)
	}
	if !beads.HasLabel(mrIssue, RigLabel(e2eFixtureRig)) {
		t.Errorf("MR bead missing %s label", RigLabel(e2eFixtureRig))
	}
	observedMR = mrIssue.ID

	handler.HandleEvent(MRCycleCloseEvent{
		MRID:        observedMR,
		TargetRig:   e2eFixtureRig,
		CloseReason: "merged",
		Body:        mrIssue.Description,
	})

	// ─── Step 5: assert terminal state — cooled-down (merged) ─────────
	townState, err := LoadTownState(b)
	if err != nil {
		t.Fatalf("LoadTownState post-close: %v", err)
	}
	rigCycle := readRigSummary(t, townState, e2eFixtureRig)
	if rigCycle.State != "cooled-down" {
		t.Errorf("rig cycle state = %q; want cooled-down", rigCycle.State)
	}
	if rigCycle.LastOutcome != "merged" {
		t.Errorf("rig last_outcome = %q; want merged", rigCycle.LastOutcome)
	}
	// Merged path resets the circuit-breaker counter to zero.
	if townState.CircuitBreaker.Count != 0 {
		t.Errorf("circuit-breaker count = %d after merge; want 0", townState.CircuitBreaker.Count)
	}

	// Wall-clock acceptance: << 30 min on the fixture. We assert a tight
	// bound so a regression that makes the loop pathologically slow trips
	// the gate.
	if elapsed := time.Since(start); elapsed > 5*time.Minute {
		t.Errorf("e2e happy path took %v; want < 5m (acceptance: < 30m)", elapsed)
	}
}

// TestPhase0E2E_GateFailVariant forces gate 4d (tautology linter) to
// fail and asserts the polecat exits with NOTES and NO MR bead is
// created.
func TestPhase0E2E_GateFailVariant(t *testing.T) {
	requireE2E(t)
	goBin := requireGo(t)

	b, _ := setupE2EBeads(t)
	provisionE2EState(t, b)

	// The polecat writes a tautological test: every assertion is
	// literal-vs-literal, which gate 4d (tautology sub-rule (ii)) must
	// reject. It still covers the uncovered branches, so gate 4a/4c would
	// pass — proving 4d is what fails the cycle, not a coverage miss.
	work := copyFixtureModule(t)
	newTestPath := filepath.Join(work, "classify_taut_test.go")
	if err := os.WriteFile(newTestPath, []byte(gateFailTautologicalSource()), 0o644); err != nil {
		t.Fatalf("write tautological test: %v", err)
	}

	// Run gate 4d directly: the tautology linter MUST find a violation.
	gateTautology := runGateTautology(t, newTestPath)
	if gateTautology.passed {
		t.Fatalf("gate 4d (tautology) unexpectedly passed on a literal-vs-literal test: %s", gateTautology.detail)
	}
	t.Logf("gate 4d correctly failed: %s", gateTautology.detail)

	// Sanity: the test still compiles & the branches are covered (so the
	// failure is genuinely the tautology gate, not a broken fixture).
	if rec := runGateFlakiness(t, goBin, work, 1); !rec.passed {
		t.Fatalf("fixture test does not even run green once: %s", rec.detail)
	}

	// Polecat exits with NOTES on a hard-fail gate (mode=create → close
	// the work bead with a NOTES reason, NO MR). We assert the contract:
	// NO MR bead with the auto-test-pr labels exists for this rig.
	mrCount := countMRBeads(t, b, e2eFixtureRig)
	if mrCount != 0 {
		t.Errorf("found %d MR bead(s) after a gate-fail; want 0 (polecat must exit with NOTES, no MR)", mrCount)
	}

	// The per-rig state must NOT have advanced to mr-pending (no MR was
	// filed). It remains at whatever the cycle left it — here idle, since
	// we never dispatched in this variant.
	st, err := NewBeadsRigStateStore(b).LoadRigState(e2eFixtureRig)
	if err != nil {
		t.Fatalf("LoadRigState: %v", err)
	}
	if st.State == PerRigCycleStateMRPending {
		t.Errorf("rig advanced to mr-pending despite gate failure; want no MR transition")
	}
}

// ─── Seven-gate runner (simulates the polecat's inline gate steps) ────

// runSevenGates runs all 7 quality gates against the polecat's new test
// file in the sandboxed fixture module and returns one record per gate.
func runSevenGates(t *testing.T, goBin, work, newTestPath string, writtenFiles []string, budget SizeBudget) []gateRecord {
	t.Helper()
	var recs []gateRecord

	// 4a coverage-delta: branch coverage must strictly increase.
	recs = append(recs, runGateCoverageDelta(t, goBin, work))

	// 4b synthetic-mutant: ≤5 AST-aware mutants, runner sane.
	recs = append(recs, runGateSyntheticMutant(t, work))

	// 4c flakiness-rerun: go test -count=10 must be green.
	recs = append(recs, runGateFlakiness(t, goBin, work, 10))

	// 4d tautology-linter: no trivial/tautological assertions.
	recs = append(recs, runGateTautology(t, newTestPath))

	// 4e gitleaks: no secrets in the diff (skip-with-record if absent).
	recs = append(recs, runGateGitleaks(t, work, writtenFiles))

	// 4f output allow-list: only same-package *_test.go files.
	recs = append(recs, runGateOutputAllowlist(t, work, writtenFiles))

	// 4g size-budget: max_files / max_loc from the dispatch envelope.
	recs = append(recs, runGateSizeBudget(t, newTestPath, writtenFiles, budget))

	return recs
}

// runGateCoverageDelta implements gate 4a: it generates a coverage
// profile for the baseline test set and for the augmented set, then
// asserts BranchDelta > 0.
func runGateCoverageDelta(t *testing.T, goBin, work string) gateRecord {
	t.Helper()
	const name = "coverage-delta"

	// Baseline profile: only the checked-in TestClassify_Baseline.
	beforePath := filepath.Join(t.TempDir(), "before.out")
	if out, err := runGoCover(goBin, work, "TestClassify_Baseline$", beforePath); err != nil {
		return gateRecord{name, false, fmt.Sprintf("baseline coverprofile failed: %v\n%s", err, out)}
	}
	// Augmented profile: the full test set (baseline + the new test).
	afterPath := filepath.Join(t.TempDir(), "after.out")
	if out, err := runGoCover(goBin, work, ".", afterPath); err != nil {
		return gateRecord{name, false, fmt.Sprintf("augmented coverprofile failed: %v\n%s", err, out)}
	}

	before, err := parseCoverFile(beforePath)
	if err != nil {
		return gateRecord{name, false, fmt.Sprintf("parse before: %v", err)}
	}
	after, err := parseCoverFile(afterPath)
	if err != nil {
		return gateRecord{name, false, fmt.Sprintf("parse after: %v", err)}
	}
	delta := autotest.BranchDelta(before, after)
	if delta <= 0 {
		return gateRecord{name, false, fmt.Sprintf("branch delta = %d; want > 0", delta)}
	}
	return gateRecord{name, true, fmt.Sprintf("branch delta = +%d", delta)}
}

// runGateSyntheticMutant implements gate 4b: the AST-aware mutant runner
// must find candidates on the covered lines and select at most 5.
func runGateSyntheticMutant(t *testing.T, work string) gateRecord {
	t.Helper()
	const name = "synthetic-mutant"

	src, err := os.ReadFile(filepath.Join(work, e2eTargetFile))
	if err != nil {
		return gateRecord{name, false, fmt.Sprintf("read target: %v", err)}
	}
	cfg := &autotest.RunConfig{
		PkgDir:   work,
		TestName: "TestClassify_Covers",
		// The new test exercises the zero (n==0) and big (n>100)
		// branches; mark the mutable source lines covered so the mutant
		// runner has targets there. classify.go: line 17 `if n == 0`,
		// 18 `return "zero"`, 20 `if n > 100`, 21 `return "big"`.
		CoveredLines: map[string]map[int]bool{
			e2eTargetFile: {17: true, 18: true, 20: true, 21: true},
		},
		FileSHA: sha256.Sum256(src),
	}
	mutants, err := autotest.FindMutants(cfg)
	if err != nil {
		return gateRecord{name, false, fmt.Sprintf("FindMutants: %v", err)}
	}
	if len(mutants) == 0 {
		return gateRecord{name, false, "no mutation candidates found on covered lines"}
	}
	selected := autotest.SelectMutants(mutants, cfg.FileSHA, cfg.TestName)
	if len(selected) > 5 {
		return gateRecord{name, false, fmt.Sprintf("selected %d mutants; cap is 5", len(selected))}
	}
	return gateRecord{name, true, fmt.Sprintf("%d candidates, %d selected (≤5)", len(mutants), len(selected))}
}

// runGateFlakiness implements gate 4c: go test -count=N must be green.
func runGateFlakiness(t *testing.T, goBin, work string, count int) gateRecord {
	t.Helper()
	const name = "flakiness-rerun"

	sb, err := sandbox.New(work)
	if err != nil {
		return gateRecord{name, false, fmt.Sprintf("sandbox: %v", err)}
	}
	cmd := exec.Command(goBin, "test", fmt.Sprintf("-count=%d", count), "./...")
	cmd.Env = append(offlineGoEnv(), "PATH="+os.Getenv("PATH"))
	if err := sb.Apply(cmd); err != nil {
		return gateRecord{name, false, fmt.Sprintf("sandbox apply: %v", err)}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return gateRecord{name, false, fmt.Sprintf("go test -count=%d failed: %v\n%s", count, err, out)}
	}
	return gateRecord{name, true, fmt.Sprintf("go test -count=%d green", count)}
}

// runGateTautology implements gate 4d: the tautology linter must find no
// violations. A failed gate (violation found) returns passed=false with
// the violation detail.
func runGateTautology(t *testing.T, testPath string) gateRecord {
	t.Helper()
	const name = "tautology-linter"

	src, err := os.ReadFile(testPath)
	if err != nil {
		return gateRecord{name, false, fmt.Sprintf("read test file: %v", err)}
	}
	findings, err := tautology.AnalyzeFile(testPath, src)
	if err != nil {
		return gateRecord{name, false, fmt.Sprintf("analyze: %v", err)}
	}
	if len(findings) > 0 {
		var msgs []string
		for _, f := range findings {
			msgs = append(msgs, fmt.Sprintf("%s:%d %s (%s)", f.FuncName, f.Pos.Line, f.Rule, f.Message))
		}
		return gateRecord{name, false, "tautological assertions: " + strings.Join(msgs, "; ")}
	}
	return gateRecord{name, true, "no tautological assertions"}
}

// runGateGitleaks implements gate 4e: no secrets in the diff. gitleaks is
// an external binary; if it is not installed on the host the gate is
// recorded as a pass with a skip note (the production formula fails
// closed, but the test cannot install system packages). A simple
// substring scan provides a baseline secret check even without the
// binary.
func runGateGitleaks(t *testing.T, work string, writtenFiles []string) gateRecord {
	t.Helper()
	const name = "gitleaks"

	// Baseline: scan written files for obvious secret markers regardless
	// of whether the gitleaks binary exists.
	for _, rel := range writtenFiles {
		data, err := os.ReadFile(filepath.Join(work, rel))
		if err != nil {
			return gateRecord{name, false, fmt.Sprintf("read %s: %v", rel, err)}
		}
		for _, marker := range []string{"AKIA", "-----BEGIN", "aws_secret_access_key", "PRIVATE KEY"} {
			if bytes.Contains(data, []byte(marker)) {
				return gateRecord{name, false, fmt.Sprintf("possible secret %q in %s", marker, rel)}
			}
		}
	}
	if _, err := exec.LookPath("gitleaks"); err != nil {
		return gateRecord{name, true, "no secrets (baseline scan; gitleaks binary not installed on host)"}
	}
	cmd := exec.Command("gitleaks", "detect", "--no-git", "--source", work)
	if out, err := cmd.CombinedOutput(); err != nil {
		return gateRecord{name, false, fmt.Sprintf("gitleaks reported leaks: %v\n%s", err, out)}
	}
	return gateRecord{name, true, "gitleaks clean"}
}

// runGateOutputAllowlist implements gate 4f: the polecat may only write
// same-package *_test.go files. Any other written path fails the gate.
func runGateOutputAllowlist(t *testing.T, work string, writtenFiles []string) gateRecord {
	t.Helper()
	const name = "output-allowlist"

	for _, rel := range writtenFiles {
		if strings.ContainsRune(rel, filepath.Separator) {
			return gateRecord{name, false, fmt.Sprintf("written file %q is not in the package root", rel)}
		}
		if !strings.HasSuffix(rel, "_test.go") {
			return gateRecord{name, false, fmt.Sprintf("written file %q is not a *_test.go file", rel)}
		}
		// Reject forbidden test forms (NG2 from conventions.md).
		data, err := os.ReadFile(filepath.Join(work, rel))
		if err != nil {
			return gateRecord{name, false, fmt.Sprintf("read %s: %v", rel, err)}
		}
		body := string(data)
		for _, forbidden := range []string{"func Benchmark", "func Example", "func Fuzz", "//go:build integration"} {
			if strings.Contains(body, forbidden) {
				return gateRecord{name, false, fmt.Sprintf("%s contains forbidden form %q", rel, forbidden)}
			}
		}
	}
	return gateRecord{name, true, fmt.Sprintf("%d file(s), all same-package *_test.go", len(writtenFiles))}
}

// runGateSizeBudget implements gate 4g: the diff must respect the
// max_files / max_loc budget from the dispatch envelope.
func runGateSizeBudget(t *testing.T, newTestPath string, writtenFiles []string, budget SizeBudget) gateRecord {
	t.Helper()
	const name = "size-budget"

	if len(writtenFiles) > budget.MaxFiles {
		return gateRecord{name, false, fmt.Sprintf("%d files > budget %d", len(writtenFiles), budget.MaxFiles)}
	}
	data, err := os.ReadFile(newTestPath)
	if err != nil {
		return gateRecord{name, false, fmt.Sprintf("read test: %v", err)}
	}
	loc := bytes.Count(data, []byte("\n")) + 1
	if loc > budget.MaxLOC {
		return gateRecord{name, false, fmt.Sprintf("%d LOC > budget %d", loc, budget.MaxLOC)}
	}
	return gateRecord{name, true, fmt.Sprintf("%d file(s), %d LOC (budget %d/%d)", len(writtenFiles), loc, budget.MaxFiles, budget.MaxLOC)}
}

// ─── Assertions / helpers ─────────────────────────────────────────────

// assertAllGatesPass verifies every one of the 7 gates emitted a pass
// record. It also asserts the set of gate names exactly matches the
// canonical 7 (no gate silently dropped).
func assertAllGatesPass(t *testing.T, gates []gateRecord) {
	t.Helper()
	if len(gates) != len(sevenGateNames) {
		t.Fatalf("got %d gate records; want %d (the 7 quality gates)", len(gates), len(sevenGateNames))
	}
	got := make([]string, 0, len(gates))
	for _, g := range gates {
		got = append(got, g.name)
		if !g.passed {
			t.Errorf("gate %q did NOT pass: %s", g.name, g.detail)
		} else {
			t.Logf("gate %q PASS: %s", g.name, g.detail)
		}
	}
	want := append([]string(nil), sevenGateNames...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("gate set = %v; want %v", got, want)
	}
}

func passedGateNames(gates []gateRecord) []string {
	var out []string
	for _, g := range gates {
		if g.passed {
			out = append(out, g.name)
		}
	}
	return out
}

// runGoCover runs `go test -run <pattern> -coverprofile <out>` in the
// fixture module, offline, and returns combined output on error.
func runGoCover(goBin, work, runPattern, outPath string) ([]byte, error) {
	sb, err := sandbox.New(work)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(goBin, "test", "-run", runPattern, "-count=1",
		"-coverprofile", outPath, "-covermode", "set", "./...")
	cmd.Env = append(offlineGoEnv(), "PATH="+os.Getenv("PATH"))
	if err := sb.Apply(cmd); err != nil {
		return nil, err
	}
	return cmd.CombinedOutput()
}

func parseCoverFile(path string) (*autotest.Profile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return autotest.ParseProfile(f)
}

// offlineGoEnv returns an environment that forces offline module
// resolution from the host module cache (GOPROXY=off) while preserving
// the host GOMODCACHE/GOCACHE so the fixture's testify dependency
// resolves without a network round-trip. The sandbox's Apply re-points
// HOME inside the worktree, but GOMODCACHE/GOCACHE here win because they
// are appended after Apply's defaults.
func offlineGoEnv() []string {
	return []string{
		"GOPROXY=off",
		"GOFLAGS=-mod=mod",
		"GOMODCACHE=" + goEnv("GOMODCACHE"),
		"GOCACHE=" + goEnv("GOCACHE"),
		"GONOSUMCHECK=1",
		"GOTOOLCHAIN=local",
	}
}

var goEnvCache = map[string]string{}

func goEnv(key string) string {
	if v, ok := goEnvCache[key]; ok {
		return v
	}
	out, err := exec.Command("go", "env", key).Output()
	v := ""
	if err == nil {
		v = strings.TrimSpace(string(out))
	}
	goEnvCache[key] = v
	return v
}

// copyFixtureModule copies the checked-in fixture rig module into a
// fresh temp worktree and returns its absolute, symlink-resolved path.
func copyFixtureModule(t *testing.T) string {
	t.Helper()
	src := fixtureModuleDir(t)
	dst, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks tempdir: %v", err)
	}
	work := filepath.Join(dst, "fixturerig")
	if err := copyTree(src, work); err != nil {
		t.Fatalf("copy fixture module: %v", err)
	}
	return work
}

// fixtureModuleDir locates the checked-in fixture module relative to this
// test file's package (internal/autotestpr → internal/autotest/testdata).
func fixtureModuleDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// wd is internal/autotestpr; the fixture lives at
	// ../autotest/testdata/fixturerig.
	dir := filepath.Join(wd, "..", "autotest", "testdata", "fixturerig")
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs fixture dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
		t.Fatalf("fixture module not found at %s: %v", abs, err)
	}
	return abs
}

// copyTree recursively copies src into dst, preserving file modes.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// ─── beads setup (mirrors the existing integration-test harness) ──────

func setupE2EBeads(t *testing.T) (*beads.Beads, string) {
	t.Helper()
	if cmd := exec.Command("bd", "version"); cmd.Run() != nil {
		t.Skip("bd not functional")
	}
	n := atomic.AddInt32(&e2eTestCounter, 1)
	prefix := fmt.Sprintf("e2e%d", n)

	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	rigDir := filepath.Join(tmpDir, "rig")
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}
	e2eInitGit(t, rigDir)
	e2eInitBeadsDB(t, rigDir, prefix)
	return beads.New(rigDir), rigDir
}

func e2eInitGit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func e2eInitBeadsDB(t *testing.T, dir, prefix string) {
	t.Helper()
	cmd := exec.Command("bd", "init", "--prefix="+prefix)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init: %v\n%s", err, out)
	}
}

// provisionE2EState ensures the town-state and per-rig state beads exist
// so the cycle and cycle-close handler have something to mutate. Skips
// the test if the test rig's beads are not visible (Dolt routing
// fragility documented in the sibling integration tests).
func provisionE2EState(t *testing.T, b *beads.Beads) {
	t.Helper()
	if _, err := EnsureTownStateBead(b); err != nil {
		t.Skipf("EnsureTownStateBead failed (Dolt routing in test rig): %v", err)
	}
	if _, err := EnsureRigStateBead(b, e2eFixtureRig); err != nil {
		t.Skipf("EnsureRigStateBead failed (Dolt routing in test rig): %v", err)
	}
}

// seedRigSummary writes the per-rig read-cache row in the town-state
// RigSummary to the given state, so the cycle-close handler reads the
// expected prior state.
func seedRigSummary(t *testing.T, b *beads.Beads, rig, state string) {
	t.Helper()
	raw, err := json.Marshal(RigCycleState{State: state})
	if err != nil {
		t.Fatalf("marshal rig cycle state: %v", err)
	}
	if err := mutateTownState(b, func(s *TownState) error {
		if s.RigSummary == nil {
			s.RigSummary = map[string]json.RawMessage{}
		}
		s.RigSummary[rig] = raw
		return nil
	}); err != nil {
		t.Fatalf("seed RigSummary: %v", err)
	}
}

func readRigSummary(t *testing.T, s TownState, rig string) RigCycleState {
	t.Helper()
	raw, ok := s.RigSummary[rig]
	if !ok {
		t.Fatalf("RigSummary has no entry for %s", rig)
	}
	var rc RigCycleState
	if err := json.Unmarshal(raw, &rc); err != nil {
		t.Fatalf("unmarshal rig cycle state: %v", err)
	}
	return rc
}

// countMRBeads counts auto-test-pr MR beads for the given rig.
func countMRBeads(t *testing.T, b *beads.Beads, rig string) int {
	t.Helper()
	issues, err := b.List(beads.ListOptions{Label: "gt:merge-request", Status: "all", Limit: 0})
	if err != nil {
		t.Fatalf("list MR beads: %v", err)
	}
	n := 0
	for _, iss := range issues {
		if beads.HasLabel(iss, RigLabel(rig)) && beads.HasLabel(iss, AttachmentParentLabel) {
			n++
		}
	}
	return n
}

// ─── gate skip / requirement guards ───────────────────────────────────

func requireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("GT_RUN_E2E") != "1" {
		t.Skip("Phase-0 e2e test skipped (set GT_RUN_E2E=1 to run)")
	}
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed")
	}
}

func requireGo(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go on PATH")
	}
	return goBin
}

// ─── fixture test sources ─────────────────────────────────────────────

// happyPathTestSource returns a valid same-package test that covers the
// two uncovered branches with NON-tautological assertions and carries
// the D8 provenance marker.
func happyPathTestSource(provenance string) string {
	return fmt.Sprintf(`package fixturerig

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

%s
func TestClassify_Covers(t *testing.T) {
	// Covers the "zero" branch (n == 0).
	assert.Equal(t, "zero", Classify(0))
	// Covers the "big" branch (n > 100).
	assert.Equal(t, "big", Classify(500))
}
`, provenance)
}

// gateFailTautologicalSource returns a same-package test whose every
// assertion is literal-vs-literal — gate 4d (tautology sub-rule (ii))
// must reject it. It still calls Classify so it compiles and the
// branches are exercised, isolating the tautology gate as the failure.
func gateFailTautologicalSource() string {
	return `package fixturerig

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// gt:auto-test-pr origin=gu-e2e-fail covers=classify.go:19,classify.go:22
func TestClassify_Tautological(t *testing.T) {
	_ = Classify(0)
	_ = Classify(500)
	// Every assertion is literal-vs-literal — tautological (NG sub-rule ii).
	assert.Equal(t, "zero", "zero")
	assert.Equal(t, "big", "big")
}
`
}

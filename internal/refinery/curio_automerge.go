package refinery

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/curio"
	"github.com/steveyegge/gastown/internal/events"
)

// curioReplayFixtureSubpath is the repo-relative location of the replay corpus
// the grade-A re-verification runs over, resolved against the refinery worktree.
const curioReplayFixtureSubpath = "internal/curio/testdata/replay"

// Curio P3 B7 — Precision-gate auto-merge policy (default OFF).
//
// A threshold-tune CR proposed by the Curio Retrospect lane may auto-merge (no
// human approval) ONLY when the P2 conjunction holds (design-doc Q4):
//
//  1. The CR touches ONLY daemon.json `patrols.curio.rate_thresholds` keys.
//  2. The CR body asserts measured precision < 0.80 for the tuned series.
//  3. The replay harness grades A with the tuned overlay applied (B3).
//
// Mechanism (the bead requires naming one): a re-verified label. The polecat
// stamps `curio-auto-eligible` on its proposal, but the label only makes the CR
// *subject* to this policy — it is NEVER trusted. This policy independently
// re-derives conjuncts 1 and 3 from git and the replay harness; only conjunct 2
// (a human-authored assertion of measured precision) is read from the body, and
// it is a NECESSARY condition the gate cannot itself measure, not a merge
// authorization. Diff-scope and replay-grade are re-verified by the gate, never
// asserted by the proposer (design-doc Q4 / B7 invariant 4).
//
// Default OFF: AutoMergeConfig.Enabled defaults to false. With the policy off,
// a labeled curio CR is HELD for human review; an unlabeled CR is untouched.
// Enable only after observing several cycles of human-reviewed tunes that the
// human would have approved anyway (the Phase-2 shadow→live discipline).

// CurioAutoEligibleLabel is the polecat-applied marker that makes a CR subject
// to the precision-gate auto-merge policy. Its presence is necessary but never
// sufficient — the gate re-verifies every conjunct independently.
const CurioAutoEligibleLabel = "curio-auto-eligible"

// CurioThresholdConfigPath is the repo-relative path of the daemon patrol config
// a threshold-tune CR edits. A qualifying CR touches ONLY this file. It mirrors
// the live daemon's `mayor/daemon.json` projection that carries
// `patrols.curio.rate_thresholds` (internal/daemon/curio_dog.go).
const CurioThresholdConfigPath = "mayor/daemon.json"

// curioPrecisionAssertionRe matches the CR body's measured-precision assertion
// in either canonical form:
//   - an inequality "precision < 0.80" (design-doc Q4's phrasing — asserts the
//     measured precision is below the captured bound); or
//   - a concrete value "precision: 0.5" / "measured precision 0.74".
//
// Group 1 captures the intervening text (where a "<" operator, if any, lives);
// group 2 captures the number. The gap is bounded to keep the match local to
// the assertion. Case-insensitive.
var curioPrecisionAssertionRe = regexp.MustCompile(`(?i)precision([^0-9]{0,15})(0?\.[0-9]+)`)

// curioPrecisionCeiling is the P2 precision bar: a tune is only auto-eligible
// when the proposer asserts the tuned series' measured precision is below this
// (i.e. the series is noisy enough to justify an unsupervised tune).
const curioPrecisionCeiling = 0.80

// AutoMergeConfig holds the Curio precision-gate auto-merge policy settings.
// Loaded from merge_queue.curio_auto_merge in the rig config. The zero value
// (Enabled=false) is the shipped default: the policy is OFF.
type AutoMergeConfig struct {
	// Enabled turns the auto-merge path on. Default false — every labeled curio
	// CR is held for human review until an operator opts in.
	Enabled bool `json:"enabled"`

	// FixtureDir overrides the replay fixture corpus location used for the
	// grade-A re-verification. Empty uses the harness default
	// (internal/curio/testdata/replay), resolved relative to the refinery
	// worktree. Tests point this at a temp corpus.
	FixtureDir string `json:"fixture_dir,omitempty"`

	// NormalBound overrides the replay precision-proxy bound (max candidate
	// volume on a normal window). Zero uses CurioReplayNormalBound.
	NormalBound int `json:"normal_bound,omitempty"`
}

// CurioReplayNormalBound is the Phase-0 go/no-go candidate-volume bound the
// replay grade enforces on normal windows (mirrors curio's internal
// normalCandidateBound). Kept here so the refinery does not depend on a curio
// test-only constant.
const CurioReplayNormalBound = 20

// AutoMergeInputs is everything the policy decision needs, gathered at the edge
// (git + bead) by the Engineer and handed to the pure decision function. Keeping
// the decision pure makes each conjunct trivially unit-testable (the B7
// acceptance criteria) without a git fixture.
type AutoMergeInputs struct {
	// Labeled reports whether the source bead carries CurioAutoEligibleLabel.
	Labeled bool

	// ChangedFiles is the CR's changed-file set (base...head), repo-relative.
	ChangedFiles []string

	// BaseConfig is the daemon.json content at the merge base (origin/target),
	// empty if the file did not exist on base.
	BaseConfig string

	// HeadConfig is the daemon.json content on the CR branch, empty if absent.
	HeadConfig string

	// Body is the CR description / commit message the precision assertion is
	// read from.
	Body string

	// Fixtures is the loaded replay corpus the grade runs over.
	Fixtures []curio.Fixture

	// NormalBound is the replay precision-proxy bound (CurioReplayNormalBound
	// unless overridden).
	NormalBound int
}

// AutoMergeDecision is the outcome of evaluating the precision gate.
type AutoMergeDecision struct {
	// Eligible is true only when the policy is ON and all three conjuncts hold.
	Eligible bool

	// Applicable is true when the CR is labeled curio-auto-eligible (i.e. the
	// policy applies to it at all). An unlabeled CR is not applicable and the
	// refinery treats it exactly as before.
	Applicable bool

	// Reason is a human-readable explanation of the decision — which conjunct
	// failed, or why it passed. Always populated.
	Reason string
}

// EvaluateAutoMerge is the pure policy decision. It re-verifies the three
// conjuncts from the supplied inputs and the policy's enabled flag. It performs
// NO I/O: the Engineer gathers git/bead state and passes it in.
//
//   - Not labeled            → Applicable=false (refinery behaves as before).
//   - Labeled, policy OFF    → Applicable=true, Eligible=false (HOLD for human).
//   - Labeled, policy ON, all three conjuncts hold → Eligible=true (auto-merge).
//   - Labeled, policy ON, any conjunct fails        → Eligible=false (human).
func EvaluateAutoMerge(cfg AutoMergeConfig, in AutoMergeInputs) AutoMergeDecision {
	if !in.Labeled {
		return AutoMergeDecision{
			Applicable: false,
			Reason:     "not a curio-auto-eligible CR; standard merge path",
		}
	}

	// The label makes the policy applicable. From here, the default is HOLD:
	// every exit that is not the fully-verified ON path returns Eligible=false.
	if !cfg.Enabled {
		return AutoMergeDecision{
			Applicable: true,
			Eligible:   false,
			Reason:     "curio auto-merge policy is OFF (default) — human approval required",
		}
	}

	// Conjunct 1: diff scope. Re-verified from git, never trusted from the label.
	if scopeReason, ok := curioDiffScopeOK(in.ChangedFiles, in.BaseConfig, in.HeadConfig); !ok {
		return AutoMergeDecision{
			Applicable: true,
			Eligible:   false,
			Reason:     "diff-scope check failed: " + scopeReason,
		}
	}

	// Conjunct 2: the CR body asserts measured precision < 0.80.
	if precReason, ok := curioPrecisionAssertionOK(in.Body); !ok {
		return AutoMergeDecision{
			Applicable: true,
			Eligible:   false,
			Reason:     "precision assertion check failed: " + precReason,
		}
	}

	// Conjunct 3: replay grades A with the tuned overlay. Re-run here, never
	// trusted from the proposer's CR body.
	bound := in.NormalBound
	if bound <= 0 {
		bound = CurioReplayNormalBound
	}
	if len(in.Fixtures) == 0 {
		return AutoMergeDecision{
			Applicable: true,
			Eligible:   false,
			Reason:     "replay grade check failed: empty fixture corpus (cannot verify grade A)",
		}
	}
	overlay, err := projectRateThresholds(in.HeadConfig)
	if err != nil {
		return AutoMergeDecision{
			Applicable: true,
			Eligible:   false,
			Reason:     "replay grade check failed: head daemon.json overlay unparseable: " + err.Error(),
		}
	}
	rep := curio.GradeWithThresholds(overlay, in.Fixtures)
	if !rep.GradeA(bound) {
		return AutoMergeDecision{
			Applicable: true,
			Eligible:   false,
			Reason: fmt.Sprintf("replay grade check failed: tuned overlay does not grade A "+
				"(worst normal window %q=%d candidates, bound %d; anchors hit=%v)",
				rep.WorstNormalWindow, rep.NormalCandidates, bound, rep.AnchorsHit),
		}
	}

	return AutoMergeDecision{
		Applicable: true,
		Eligible:   true,
		Reason:     "all conjuncts verified: daemon.json rate_thresholds-only diff, precision<0.80 asserted, replay grades A",
	}
}

// curioDiffScopeOK re-verifies conjunct 1: the CR touches ONLY the daemon config
// path, AND the structural diff of that file is confined to
// patrols.curio.rate_thresholds. Any other changed file, or any change to the
// daemon config outside the rate_thresholds block, fails — a source change or a
// fixture deletion can never be auto-eligible.
func curioDiffScopeOK(changedFiles []string, baseConfig, headConfig string) (reason string, ok bool) {
	if len(changedFiles) == 0 {
		return "no changed files (nothing to merge)", false
	}
	for _, f := range changedFiles {
		if f != CurioThresholdConfigPath {
			return fmt.Sprintf("CR changes %q; only %s may change", f, CurioThresholdConfigPath), false
		}
	}
	// Exactly the config file changed. Now confirm the change is confined to the
	// rate_thresholds block: everything else in the file must be byte-identical
	// once that one block is excised. This catches a CR that edits an unrelated
	// daemon.json key (e.g. a patrol schedule) in the same file.
	baseRest, err := stripRateThresholds(baseConfig)
	if err != nil {
		return "base daemon.json is not valid JSON: " + err.Error(), false
	}
	headRest, err := stripRateThresholds(headConfig)
	if err != nil {
		return "head daemon.json is not valid JSON: " + err.Error(), false
	}
	if baseRest != headRest {
		return "daemon.json change extends beyond patrols.curio.rate_thresholds", false
	}
	return "", true
}

// stripRateThresholds parses daemon.json, deletes patrols.curio.rate_thresholds,
// and re-serializes canonically (sorted keys) so the remainder of two configs
// can be compared byte-for-byte independent of key ordering or whitespace. An
// empty input (file absent on one side) canonicalizes to "null".
func stripRateThresholds(cfg string) (string, error) {
	if strings.TrimSpace(cfg) == "" {
		return "null", nil
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(cfg), &root); err != nil {
		return "", err
	}
	if patrols, ok := root["patrols"].(map[string]any); ok {
		if c, ok := patrols["curio"].(map[string]any); ok {
			delete(c, "rate_thresholds")
		}
	}
	// json.Marshal sorts map keys, giving a canonical form for comparison.
	out, err := json.Marshal(root)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// projectRateThresholds reads patrols.curio.rate_thresholds from a daemon.json
// string into the overlay map the replay grade is computed against. It mirrors
// curio.LoadRateThresholdOverlay but takes the branch's config content directly
// (we already fetched it via `git show <branch>:daemon.json`), avoiding a
// worktree checkout of the proposed branch. Empty content → nil overlay (grade
// the calibrated defaults).
func projectRateThresholds(cfg string) (map[string]int, error) {
	if strings.TrimSpace(cfg) == "" {
		return nil, nil
	}
	var parsed struct {
		Patrols struct {
			Curio struct {
				RateThresholds map[string]int `json:"rate_thresholds"`
			} `json:"curio"`
		} `json:"patrols"`
	}
	if err := json.Unmarshal([]byte(cfg), &parsed); err != nil {
		return nil, err
	}
	return parsed.Patrols.Curio.RateThresholds, nil
}

// curioPrecisionAssertionOK re-checks conjunct 2: the CR body must assert a
// measured precision below the 0.80 ceiling for the tuned series. The numeric
// value is parsed and compared; a body with no precision assertion, or one that
// asserts precision >= 0.80, fails.
func curioPrecisionAssertionOK(body string) (reason string, ok bool) {
	m := curioPrecisionAssertionRe.FindStringSubmatch(body)
	if m == nil {
		return "CR body asserts no measured precision (expected e.g. 'precision < 0.80')", false
	}
	hasLT := strings.Contains(m[1], "<")
	var val float64
	if _, err := fmt.Sscanf(m[2], "%f", &val); err != nil {
		return "could not parse asserted precision value " + m[2], false
	}
	// Inequality form "precision < X": the assertion holds iff X <= ceiling
	// (asserting precision is below X, where X is at most the 0.80 bar, asserts
	// precision < 0.80). Concrete form "precision = X": holds iff X < ceiling.
	if hasLT {
		if val > curioPrecisionCeiling {
			return fmt.Sprintf("asserted bound 'precision < %.3f' is looser than the %.2f ceiling", val, curioPrecisionCeiling), false
		}
		return "", true
	}
	if val >= curioPrecisionCeiling {
		return fmt.Sprintf("asserted precision %.3f is not below the %.2f ceiling", val, curioPrecisionCeiling), false
	}
	return "", true
}

// checkCurioAutoMerge applies the precision-gate auto-merge policy to an MR.
// It gathers git + bead inputs at the edge and delegates the decision to the
// pure EvaluateAutoMerge.
//
// Return semantics:
//   - nil       → the policy does not hold this MR; proceed with the normal
//     merge path (CR is unlabeled, OR labeled+policy-ON+all
//     conjuncts verified — auto-merge is just "let it merge").
//   - non-nil   → HOLD the MR for human review (labeled but not eligible). The
//     returned ProcessResult mirrors the PR-approval hold:
//     NeedsApproval=true keeps the MR in the queue without treating
//     it as a failure, and a refinery_paused event surfaces the hold.
//
// Diff-scope and replay-grade are re-derived here from git and the harness; the
// label is never trusted as authorization.
func (e *Engineer) checkCurioAutoMerge(branch, target, sourceIssue string) *ProcessResult {
	// Resolve the policy config (nil → OFF default).
	cfg := AutoMergeConfig{}
	if e.config.CurioAutoMerge != nil {
		cfg = *e.config.CurioAutoMerge
	}

	// Read the source bead's labels. If we cannot read it, the CR cannot be
	// confirmed auto-eligible — treat as unlabeled (standard path). A missing
	// label can never make a CR LESS safe than the default human-gated flow.
	labeled := false
	if sourceIssue != "" {
		if si, err := e.beads.Show(sourceIssue); err == nil && si != nil {
			labeled = beads.HasLabel(si, CurioAutoEligibleLabel)
		}
	}
	if !labeled {
		// Not a curio-auto-eligible CR: the policy is inapplicable. Proceed
		// normally without any I/O on the diff or fixtures.
		return nil
	}

	in := AutoMergeInputs{Labeled: true, NormalBound: cfg.NormalBound}

	// Conjunct-1 inputs: changed-file set and the daemon.json base/head content.
	if files, err := e.git.DiffNameOnly("origin/"+target, branch); err != nil {
		// Fail closed: if we cannot compute the diff scope, hold for human.
		_, _ = fmt.Fprintf(e.output, "[Engineer] curio-auto-merge: diff failed for %s (%v) — holding for human\n", branch, err)
		return e.holdCurioForHuman(branch, sourceIssue, "diff-scope unverifiable: "+err.Error())
	} else {
		in.ChangedFiles = files
	}
	if base, err := e.git.ShowFile("origin/"+target, CurioThresholdConfigPath); err == nil {
		in.BaseConfig = base
	}
	if head, err := e.git.ShowFile(branch, CurioThresholdConfigPath); err == nil {
		in.HeadConfig = head
	}

	// Conjunct-2 input: the CR body (the branch tip commit message).
	if body, err := e.git.GetBranchCommitMessage(branch); err == nil {
		in.Body = body
	}

	// Conjunct-3 input: the replay fixture corpus. The tuned overlay is
	// projected from in.HeadConfig inside EvaluateAutoMerge (no daemon dep).
	fixtureDir := cfg.FixtureDir
	if fixtureDir == "" {
		fixtureDir = filepath.Join(e.git.WorkDir(), curioReplayFixtureSubpath)
	}
	if fixtures, err := curio.LoadFixtures(fixtureDir); err == nil {
		in.Fixtures = fixtures
	}

	decision := EvaluateAutoMerge(cfg, in)
	if decision.Eligible {
		_, _ = fmt.Fprintf(e.output, "[Engineer] curio-auto-merge: %s — auto-merging %s\n", decision.Reason, branch)
		return nil
	}
	// Applicable but not eligible → HOLD for human review.
	_, _ = fmt.Fprintf(e.output, "[Engineer] curio-auto-merge: holding %s for human review — %s\n", branch, decision.Reason)
	return e.holdCurioForHuman(branch, sourceIssue, decision.Reason)
}

// holdCurioForHuman returns the HOLD ProcessResult and emits a refinery_paused
// telemetry event so the witness can surface the indefinite hold (mirrors the
// PR-needs-approval path).
func (e *Engineer) holdCurioForHuman(branch, sourceIssue, reason string) *ProcessResult {
	_ = events.LogFeed(events.TypeRefineryPaused, e.rig.Name+"/refinery",
		events.RefineryPausedPayload(
			e.rig.Name,
			"",
			branch,
			sourceIssue,
			"curio_needs_approval",
			reason,
			"curio_precision_gate",
		))
	return &ProcessResult{
		Success:       false,
		NeedsApproval: true,
		Error:         "curio auto-merge held for human review: " + reason,
	}
}

package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// --- Interval tests ---

func TestPushStrandedInterval_Default(t *testing.T) {
	if got := pushStrandedInterval(nil); got != defaultPushStrandedInterval {
		t.Errorf("expected default %v, got %v", defaultPushStrandedInterval, got)
	}
}

func TestPushStrandedInterval_Custom(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			PushStranded: &PushStrandedConfig{Enabled: true, IntervalStr: "2m"},
		},
	}
	if got := pushStrandedInterval(cfg); got != 2*time.Minute {
		t.Errorf("expected 2m, got %v", got)
	}
}

func TestPushStrandedInterval_Invalid(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			PushStranded: &PushStrandedConfig{Enabled: true, IntervalStr: "nonsense"},
		},
	}
	if got := pushStrandedInterval(cfg); got != defaultPushStrandedInterval {
		t.Errorf("expected default for invalid interval, got %v", got)
	}
}

// --- MaxAttempts tests ---

func TestPushStrandedMaxAttempts_Default(t *testing.T) {
	if got := pushStrandedMaxAttempts(nil); got != defaultPushStrandedMaxAttempts {
		t.Errorf("expected default %d, got %d", defaultPushStrandedMaxAttempts, got)
	}
}

func TestPushStrandedMaxAttempts_Custom(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			PushStranded: &PushStrandedConfig{Enabled: true, MaxAttempts: 7},
		},
	}
	if got := pushStrandedMaxAttempts(cfg); got != 7 {
		t.Errorf("expected 7, got %d", got)
	}
}

func TestPushStrandedMaxAttempts_ZeroFallsBackToDefault(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			PushStranded: &PushStrandedConfig{Enabled: true, MaxAttempts: 0},
		},
	}
	if got := pushStrandedMaxAttempts(cfg); got != defaultPushStrandedMaxAttempts {
		t.Errorf("expected default for zero max_attempts, got %d", got)
	}
}

// --- IsPatrolEnabled tests (push_stranded is DEFAULT-ON) ---

func TestIsPatrolEnabled_PushStranded_NilConfigDefaultsOn(t *testing.T) {
	if !IsPatrolEnabled(nil, "push_stranded") {
		t.Error("push_stranded should default ON with nil config")
	}
}

func TestIsPatrolEnabled_PushStranded_EmptyPatrolsDefaultsOn(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if !IsPatrolEnabled(cfg, "push_stranded") {
		t.Error("push_stranded should default ON when not explicitly configured")
	}
}

func TestIsPatrolEnabled_PushStranded_ExplicitlyDisabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			PushStranded: &PushStrandedConfig{Enabled: false},
		},
	}
	if IsPatrolEnabled(cfg, "push_stranded") {
		t.Error("push_stranded should be disabled when explicitly set false")
	}
}

func TestIsPatrolEnabled_PushStranded_ExplicitlyEnabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			PushStranded: &PushStrandedConfig{Enabled: true},
		},
	}
	if !IsPatrolEnabled(cfg, "push_stranded") {
		t.Error("push_stranded should be enabled when explicitly set true")
	}
}

// --- Lifecycle defaults ---

func TestEnsureLifecycleDefaults_PopulatesPushStranded(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	changed := EnsureLifecycleDefaults(cfg)
	if !changed {
		t.Fatal("expected EnsureLifecycleDefaults to report a change")
	}
	if cfg.Patrols.PushStranded == nil {
		t.Fatal("expected PushStranded to be populated")
	}
	if !cfg.Patrols.PushStranded.Enabled {
		t.Error("expected populated PushStranded to be enabled")
	}
}

func TestDefaultLifecycleConfig_IncludesPushStranded(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	if cfg.Patrols.PushStranded == nil {
		t.Fatal("expected DefaultLifecycleConfig to include PushStranded")
	}
	if !cfg.Patrols.PushStranded.Enabled {
		t.Error("expected default PushStranded to be enabled")
	}
	if cfg.Patrols.PushStranded.MaxAttempts != defaultPushStrandedMaxAttempts {
		t.Errorf("expected default MaxAttempts %d, got %d", defaultPushStrandedMaxAttempts, cfg.Patrols.PushStranded.MaxAttempts)
	}
}

// --- decidePushStrandedAction decision core ---

// validWisp is a wisp with the minimum fields required to be recoverable.
func validWisp() strandedWisp {
	return strandedWisp{
		WispID:    "wisp-1",
		Branch:    "polecat/foo",
		IssueID:   "gu-abc1",
		CommitSHA: "deadbeef",
		Worker:    "p1",
		Rig:       "myrig",
	}
}

// recoverableFacts are the facts under which a valid wisp should be submitted:
// session dead, no open MR, branch on origin, attempts under the cap.
func recoverableFacts() pushStrandedFacts {
	return pushStrandedFacts{
		SessionLive:    false,
		OpenMRExists:   false,
		BranchOnOrigin: true,
		Attempts:       0,
		MaxAttempts:    3,
	}
}

func TestDecidePushStranded_SubmitWhenStrandedAndSafe(t *testing.T) {
	if got := decidePushStrandedAction(validWisp(), recoverableFacts()); got != pushStrandedSubmit {
		t.Errorf("expected pushStrandedSubmit, got %v", got)
	}
}

func TestDecidePushStranded_SkipInvalidWhenNoBranch(t *testing.T) {
	w := validWisp()
	w.Branch = ""
	if got := decidePushStrandedAction(w, recoverableFacts()); got != pushStrandedSkipInvalid {
		t.Errorf("expected pushStrandedSkipInvalid for empty branch, got %v", got)
	}
}

func TestDecidePushStranded_SkipInvalidWhenNoIssue(t *testing.T) {
	w := validWisp()
	w.IssueID = ""
	if got := decidePushStrandedAction(w, recoverableFacts()); got != pushStrandedSkipInvalid {
		t.Errorf("expected pushStrandedSkipInvalid for empty issue, got %v", got)
	}
}

// Safety acceptance #3: a live polecat session must never be recovered under,
// and liveness must win even when every other fact says "recoverable".
func TestDecidePushStranded_SkipLiveBeatsEverything(t *testing.T) {
	f := recoverableFacts()
	f.SessionLive = true
	if got := decidePushStrandedAction(validWisp(), f); got != pushStrandedSkipLive {
		t.Errorf("expected pushStrandedSkipLive when session is live, got %v", got)
	}
}

func TestDecidePushStranded_SkipHasMR(t *testing.T) {
	f := recoverableFacts()
	f.OpenMRExists = true
	if got := decidePushStrandedAction(validWisp(), f); got != pushStrandedSkipHasMR {
		t.Errorf("expected pushStrandedSkipHasMR when an open MR exists, got %v", got)
	}
}

func TestDecidePushStranded_SkipNoBranchOnOrigin(t *testing.T) {
	f := recoverableFacts()
	f.BranchOnOrigin = false
	if got := decidePushStrandedAction(validWisp(), f); got != pushStrandedSkipNoBranch {
		t.Errorf("expected pushStrandedSkipNoBranch when branch not on origin, got %v", got)
	}
}

func TestDecidePushStranded_GiveUpAtMaxAttempts(t *testing.T) {
	f := recoverableFacts()
	f.Attempts = 3
	f.MaxAttempts = 3
	if got := decidePushStrandedAction(validWisp(), f); got != pushStrandedGiveUp {
		t.Errorf("expected pushStrandedGiveUp at attempt cap, got %v", got)
	}
}

func TestDecidePushStranded_SubmitWhenUnderMaxAttempts(t *testing.T) {
	f := recoverableFacts()
	f.Attempts = 2
	f.MaxAttempts = 3
	if got := decidePushStrandedAction(validWisp(), f); got != pushStrandedSubmit {
		t.Errorf("expected pushStrandedSubmit when under attempt cap, got %v", got)
	}
}

// A MaxAttempts of 0 (unset) must never trigger give-up — the dog keeps trying.
func TestDecidePushStranded_ZeroMaxAttemptsNeverGivesUp(t *testing.T) {
	f := recoverableFacts()
	f.Attempts = 100
	f.MaxAttempts = 0
	if got := decidePushStrandedAction(validWisp(), f); got != pushStrandedSubmit {
		t.Errorf("expected pushStrandedSubmit when MaxAttempts is 0, got %v", got)
	}
}

// Guard ordering: live-session must be evaluated before has-MR/no-branch/give-up
// so we never act on a fact gathered for a branch a live polecat still owns.
func TestDecidePushStranded_LiveWinsOverGiveUp(t *testing.T) {
	f := recoverableFacts()
	f.SessionLive = true
	f.Attempts = 99
	f.MaxAttempts = 3
	if got := decidePushStrandedAction(validWisp(), f); got != pushStrandedSkipLive {
		t.Errorf("expected pushStrandedSkipLive to win over give-up, got %v", got)
	}
}

// --- parseStrandedWisp ---

func TestParseStrandedWisp_AllFields(t *testing.T) {
	issue := &beads.Issue{
		ID: "wisp-42",
		Description: strings.Join([]string{
			"branch: polecat/foo",
			"source_issue: gu-abc1",
			"commit_sha: deadbeef",
			"worker: p3",
			"rig: myrig",
		}, "\n"),
	}
	sw := parseStrandedWisp(issue)
	if sw.WispID != "wisp-42" {
		t.Errorf("WispID: got %q", sw.WispID)
	}
	if sw.Branch != "polecat/foo" {
		t.Errorf("Branch: got %q", sw.Branch)
	}
	if sw.IssueID != "gu-abc1" {
		t.Errorf("IssueID: got %q", sw.IssueID)
	}
	if sw.CommitSHA != "deadbeef" {
		t.Errorf("CommitSHA: got %q", sw.CommitSHA)
	}
	if sw.Worker != "p3" {
		t.Errorf("Worker: got %q", sw.Worker)
	}
	if sw.Rig != "myrig" {
		t.Errorf("Rig: got %q", sw.Rig)
	}
}

func TestParseStrandedWisp_NilIssue(t *testing.T) {
	sw := parseStrandedWisp(nil)
	if sw != (strandedWisp{}) {
		t.Errorf("expected zero-value strandedWisp for nil issue, got %+v", sw)
	}
}

func TestParseStrandedWisp_AltKeyFormsAndNull(t *testing.T) {
	issue := &beads.Issue{
		ID: "wisp-7",
		Description: strings.Join([]string{
			"Branch: polecat/bar", // case-insensitive key
			"source-issue: gu-xyz9",
			"commit-sha: null", // explicit null is ignored
			"unrelated: noise",
		}, "\n"),
	}
	sw := parseStrandedWisp(issue)
	if sw.Branch != "polecat/bar" {
		t.Errorf("Branch (case-insensitive key): got %q", sw.Branch)
	}
	if sw.IssueID != "gu-xyz9" {
		t.Errorf("IssueID (hyphen key): got %q", sw.IssueID)
	}
	if sw.CommitSHA != "" {
		t.Errorf("CommitSHA: expected empty for null value, got %q", sw.CommitSHA)
	}
}

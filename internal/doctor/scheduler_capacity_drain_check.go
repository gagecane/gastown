package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/cli"
)

// SchedulerCapacityDrainCheck flags the failure mode from gu-01ef:
// recovery_blocked polecats accumulating until the scheduler runs out
// of dispatchable slots, while gt scheduler status surfaces the number
// but no automation reads it.
//
// The check shells out to `gt scheduler status --json`, computes the
// recovery_blocked / max_polecats ratio, and persists the timestamp of
// first detection at <town>/.runtime/doctor/scheduler-capacity-drain.json
// so a transient drain (single dispatch wave) doesn't trip the alarm —
// only a sustained drain past the threshold does.
//
// Thresholds are conservative: WARN at >50% recovery_blocked, ESCALATE to
// ERROR once that condition has held for >drainErrorAge (default 5 min).
// Healthy snapshots clear the persisted state so the check auto-recovers.
type SchedulerCapacityDrainCheck struct {
	BaseCheck
}

// drainRatioThreshold is the fraction of max_polecats that must be in
// recovery_blocked for the check to report a drain. Above this the pool
// is degraded enough that newly-dispatched work starts to starve.
const drainRatioThreshold = 0.50

// drainErrorAge is how long a drain must persist before the check
// escalates from WARNING to ERROR. The scheduler dispatch cycle and a
// single recovery patrol both run on the order of a minute; a real
// stuck-pool incident lasts much longer than that, so 5m separates
// transient back-pressure from a genuine wedge.
const drainErrorAge = 5 * time.Minute

// schedulerCapacityDrainStateFile is the persisted "drain started at"
// marker. Lives under .runtime so it's wiped with other ephemeral state.
func schedulerCapacityDrainStateFile(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "doctor", "scheduler-capacity-drain.json")
}

type drainState struct {
	FirstDetectedAt time.Time `json:"first_detected_at"`
	LastRatio       float64   `json:"last_ratio,omitempty"`
	LastBlocked     int       `json:"last_blocked,omitempty"`
	LastMax         int       `json:"last_max,omitempty"`
}

// schedulerStatusEnvelope mirrors the relevant subset of `gt scheduler
// status --json`. Kept narrow so unrelated schema additions don't break
// the doctor check.
type schedulerStatusEnvelope struct {
	Capacity struct {
		Max             int `json:"max"`
		RecoveryBlocked int `json:"recovery_blocked"`
		Working         int `json:"working"`
		ReusableIdle    int `json:"reusable_idle"`
		Free            int `json:"free"`
	} `json:"capacity"`
}

// NewSchedulerCapacityDrainCheck creates a new scheduler-capacity-drain check.
func NewSchedulerCapacityDrainCheck() *SchedulerCapacityDrainCheck {
	return &SchedulerCapacityDrainCheck{
		BaseCheck: BaseCheck{
			CheckName:        "scheduler-capacity-drain",
			CheckDescription: "Detect sustained recovery_blocked majority in scheduler capacity",
			CheckCategory:    CategoryPatrol,
		},
	}
}

// runSchedulerStatusJSON shells out to `gt scheduler status --json` and
// parses the capacity envelope. Declared as a package var so tests can
// stub it without spawning a subprocess.
var runSchedulerStatusJSON = func(townRoot string) (schedulerStatusEnvelope, error) {
	cmd := exec.Command(cli.Name(), "scheduler", "status", "--json") //nolint:gosec // G204: argv is constants and a fixed CLI name
	cmd.Dir = townRoot
	out, err := cmd.Output()
	if err != nil {
		return schedulerStatusEnvelope{}, fmt.Errorf("running %s scheduler status --json: %w", cli.Name(), err)
	}
	var env schedulerStatusEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return schedulerStatusEnvelope{}, fmt.Errorf("parsing scheduler status JSON: %w", err)
	}
	return env, nil
}

// nowFn is the time source for the check. Declared as a var so tests can
// inject a deterministic clock.
var nowFn = time.Now

// Run evaluates whether the scheduler pool is drained.
func (c *SchedulerCapacityDrainCheck) Run(ctx *CheckContext) *CheckResult {
	env, err := runSchedulerStatusJSON(ctx.TownRoot)
	if err != nil {
		// Don't fail loud here: the doctor must keep running even if the
		// scheduler subprocess flakes. Return a Warning so it shows up
		// without blocking other checks.
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not read scheduler status: %v", err),
		}
	}

	max := env.Capacity.Max
	if max <= 0 {
		// Direct-dispatch mode (max_polecats=-1) has no pool to drain.
		// Nothing to check — report OK with context.
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Direct dispatch mode (no pool capacity to drain)",
		}
	}

	blocked := env.Capacity.RecoveryBlocked
	ratio := float64(blocked) / float64(max)

	stateFile := schedulerCapacityDrainStateFile(ctx.TownRoot)

	if ratio <= drainRatioThreshold {
		// Pool is healthy. Clear any persisted drain marker so the next
		// drain starts the timer fresh, and don't fail closed if the
		// remove can't write — a stale marker is harmless until the
		// next degraded snapshot.
		if !ctx.ReadOnly {
			_ = os.Remove(stateFile)
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("Pool healthy: recovery_blocked=%d/%d (%.0f%%)", blocked, max, ratio*100),
		}
	}

	// Drain detected. Load or create the persisted timestamp.
	now := nowFn().UTC()
	state, loadErr := loadDrainState(stateFile)
	if loadErr != nil || state.FirstDetectedAt.IsZero() {
		state = drainState{FirstDetectedAt: now}
	}
	state.LastRatio = ratio
	state.LastBlocked = blocked
	state.LastMax = max

	if !ctx.ReadOnly {
		// Best-effort persist. A write failure makes the check transient
		// (drain will re-arm at next run) but must not fail the doctor.
		_ = saveDrainState(stateFile, state)
	}

	age := now.Sub(state.FirstDetectedAt)
	details := []string{
		fmt.Sprintf("recovery_blocked: %d (%.0f%% of max=%d)", blocked, ratio*100, max),
		fmt.Sprintf("working: %d, reusable_idle: %d, free: %d",
			env.Capacity.Working, env.Capacity.ReusableIdle, env.Capacity.Free),
		fmt.Sprintf("first detected at: %s (age=%s)", state.FirstDetectedAt.Format(time.RFC3339), age.Round(time.Second)),
	}
	if age >= drainErrorAge {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Scheduler capacity drained for %s (recovery_blocked=%d/%d)", age.Round(time.Second), blocked, max),
			Details: details,
			FixHint: "Investigate stuck polecats: gt scheduler status --json; gt polecat list --all --json. Resolve recovery-needed polecats or raise scheduler.max_polecats.",
		}
	}
	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("Scheduler capacity drain forming: recovery_blocked=%d/%d (%.0f%%, age=%s)", blocked, max, ratio*100, age.Round(time.Second)),
		Details: details,
		FixHint: fmt.Sprintf("Watching: will escalate to error if drain persists past %s.", drainErrorAge),
	}
}

func loadDrainState(path string) (drainState, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path derived from trusted townRoot
	if err != nil {
		return drainState{}, err
	}
	var s drainState
	if err := json.Unmarshal(data, &s); err != nil {
		return drainState{}, err
	}
	return s, nil
}

func saveDrainState(path string, state drainState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating doctor state dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling drain state: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}

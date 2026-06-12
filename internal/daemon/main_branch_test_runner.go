package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	gitpkg "github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/lock"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/util"
)

const (
	defaultMainBranchTestInterval = 30 * time.Minute
	defaultMainBranchTestTimeout  = 10 * time.Minute

	// defaultMainBranchTestFlakeThreshold is the number of consecutive failing
	// patrol cycles required before main_branch_test pages HIGH (hq-6qnct). A
	// gate that fails one cycle and passes the surrounding ones is flaky under
	// host load (hq-0qszq/hq-5em9k), not a regression — only a sustained red
	// warrants the overseer. The merge-queue gates and main_ci_break_dog catch
	// genuine breaks at merge time; this patrol is a slower backstop, so trading
	// one extra cycle of latency for silence on single flakes is the right call.
	defaultMainBranchTestFlakeThreshold = 2
)

// gateLockPollInterval is how often acquireGlobalGateLock retries the
// non-blocking flock while another rig's gate suite holds it. A few seconds is
// negligible against the 20-40min act/Docker runs the lock serializes.
const gateLockPollInterval = 2 * time.Second

// acquireGlobalGateLock serializes main_branch_test gate execution across the
// ENTIRE town — across rigs, overlapping patrol cycles, and separate daemon
// processes. The 2026-06-04 load-174 estop (gs-b1l) happened when act-based
// (GitHub-Actions-in-Docker) CI suites for multiple rigs ran simultaneously:
// lia_bac and lia_iac each ran ~17 act suites in a short window (42 total), and
// several overlapping 20-40min Docker-heavy runs on 12 cores drove load to 174,
// triggering an operator emergency-stop.
//
// A town-global flock guarantees at most one rig's gate suite runs at a time.
// We poll a NON-blocking try-acquire rather than a blocking flock so the wait
// honors the daemon context: on shutdown the waiter bails instead of pinning
// the patrol loop. flock auto-releases when the holding process dies, so a
// crashed daemon never wedges the lock — the next poll acquires it.
//
// The lock file lives under the daemon state directory alongside
// main_branch_test_state.json.
func acquireGlobalGateLock(ctx context.Context, townRoot string) (func(), error) {
	lockDir := filepath.Join(townRoot, "daemon")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating gate lock dir: %w", err)
	}
	lockPath := filepath.Join(lockDir, "main_branch_test.lock")
	for {
		release, ok, err := lock.FlockTryAcquire(lockPath)
		if err != nil {
			return nil, fmt.Errorf("acquiring gate lock: %w", err)
		}
		if ok {
			return release, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(gateLockPollInterval):
		}
	}
}

// MainBranchTestConfig holds configuration for the main_branch_test patrol.
// This patrol periodically runs quality gates on each rig's main branch to
// catch regressions from direct-to-main pushes, bad merges, or sequential
// merge conflicts that individually pass but break together.
type MainBranchTestConfig struct {
	// Enabled controls whether the main-branch test runner runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "30m").
	IntervalStr string `json:"interval,omitempty"`

	// TimeoutStr is the maximum time each rig's test run can take.
	// Default: "10m".
	TimeoutStr string `json:"timeout,omitempty"`

	// Rigs limits testing to specific rigs. If empty, all rigs are tested.
	Rigs []string `json:"rigs,omitempty"`

	// FlakeThreshold is the number of consecutive failing cycles required
	// before a HIGH escalation fires (hq-6qnct). 0 (or unset) uses the default
	// (defaultMainBranchTestFlakeThreshold). Set to 1 to restore the legacy
	// "page on every failure" behavior.
	FlakeThreshold int `json:"flake_threshold,omitempty"`
}

// mainBranchTestInterval returns the configured interval, or the default (30m).
func mainBranchTestInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.MainBranchTest != nil {
		if config.Patrols.MainBranchTest.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.MainBranchTest.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultMainBranchTestInterval
}

// mainBranchTestTimeout returns the configured per-rig timeout, or the default (10m).
func mainBranchTestTimeout(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.MainBranchTest != nil {
		if config.Patrols.MainBranchTest.TimeoutStr != "" {
			if d, err := time.ParseDuration(config.Patrols.MainBranchTest.TimeoutStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultMainBranchTestTimeout
}

// mainBranchTestFlakeThreshold returns the configured consecutive-failure
// watermark, or the default. A configured value < 1 is ignored (a threshold of
// 0 would never escalate; negatives are nonsense) and falls back to the default.
func mainBranchTestFlakeThreshold(config *DaemonPatrolConfig) int {
	if config != nil && config.Patrols != nil && config.Patrols.MainBranchTest != nil {
		if n := config.Patrols.MainBranchTest.FlakeThreshold; n >= 1 {
			return n
		}
	}
	return defaultMainBranchTestFlakeThreshold
}

// mainBranchTestRigs returns the configured rig filter, or nil (all rigs).
func mainBranchTestRigs(config *DaemonPatrolConfig) []string {
	if config != nil && config.Patrols != nil && config.Patrols.MainBranchTest != nil {
		return config.Patrols.MainBranchTest.Rigs
	}
	return nil
}

// rigGate captures a single merge_queue gate's executable command, its
// lifecycle phase, and its optional per-gate timeout. Phase mirrors
// refinery's GatePhase ("pre-merge" or "post-squash"); empty string means
// "pre-merge" per refinery's default. Timeout=0 means "no per-gate budget,
// inherit the parent context deadline".
type rigGate struct {
	Cmd     string
	Phase   string        // "" or "pre-merge" = pre-merge (default), "post-squash" = post-squash
	Timeout time.Duration // 0 = inherit parent ctx deadline
}

// rigGateConfig holds the gate/test configuration extracted from a rig's config.json.
type rigGateConfig struct {
	TestCommand   string
	SetupCommand  string             // Optional pre-build install command (e.g., "pnpm install")
	Gates         map[string]rigGate // gate name → cmd + phase + timeout
	GatesParallel bool               // run pre-merge gates concurrently
}

// mainBranchTestPhase is the gate phase that main_branch_test is allowed to
// run. Post-squash gates are, by definition, tied to the squash-merge
// lifecycle and run on the merged result after the refinery has combined
// branches. Running them in a fresh origin/main worktree is a category error:
// many such gates assume ambient state (checked-out brazil workspace, merged
// version-set, etc.) that a cold drift-detection worktree does not provide.
// See gu-j1f7 for the originating failure — a casc_constructs post-squash
// gate that fails with "Could not determine current package by 'finding up'
// Config" when invoked outside its merge context.
const mainBranchTestPhase = "pre-merge"

// defaultInstallTimeout bounds auto-detected package-manager installs so a wedged
// install (network issues, lock contention) does not consume the whole per-rig
// main_branch_test budget.
const defaultInstallTimeout = 5 * time.Minute

// loadRigGateConfig reads the merge_queue section from a rig's config to
// discover what test/gate commands to run.
//
// Resolution order (first file with a merge_queue block wins):
//  1. <rigPath>/settings/config.json — canonical behavioral config (RigSettings)
//  2. <rigPath>/config.json — legacy rig-root config
//
// The rig-root config.json is migrating to identity-only; behavioral config
// (including merge_queue) belongs in settings/config.json. We check the
// canonical location first but keep the legacy fallback so removing
// merge_queue from config.json after migration does not silently disable the
// main_branch_test patrol.
func loadRigGateConfig(rigPath string) (*rigGateConfig, error) {
	candidates := []string{
		filepath.Join(rigPath, "settings", "config.json"),
		filepath.Join(rigPath, "config.json"),
	}

	var data []byte
	var readErr error
	for _, path := range candidates {
		d, err := os.ReadFile(path) //nolint:gosec // G304: path constructed from rigPath
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			readErr = err
			break
		}

		// Peek for a merge_queue block. If absent, fall through to the next
		// candidate so removing merge_queue from one file does not mask config
		// that still lives in the other.
		var peek struct {
			MergeQueue json.RawMessage `json:"merge_queue"`
		}
		if err := json.Unmarshal(d, &peek); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		if peek.MergeQueue == nil {
			continue
		}

		data = d
		break
	}

	if readErr != nil {
		return nil, readErr
	}
	if data == nil {
		return nil, nil // No config with a merge_queue block, skip
	}

	var raw struct {
		MergeQueue json.RawMessage `json:"merge_queue"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing config.json: %w", err)
	}

	if raw.MergeQueue == nil {
		return nil, nil // No merge_queue section
	}

	var mq struct {
		TestCommand   *string                    `json:"test_command"`
		SetupCommand  *string                    `json:"setup_command"`
		Gates         map[string]json.RawMessage `json:"gates"`
		GatesParallel *bool                      `json:"gates_parallel"`
	}
	if err := json.Unmarshal(raw.MergeQueue, &mq); err != nil {
		return nil, fmt.Errorf("parsing merge_queue: %w", err)
	}

	cfg := &rigGateConfig{}

	// Extract gates (preferred over legacy test_command). Per-gate timeout
	// is parsed as a duration string ("5m", "30s"); a malformed value is
	// logged-by-omission (Timeout stays 0, gate inherits parent deadline)
	// rather than failing the whole config load — the runner still works,
	// the operator just doesn't get the budget they asked for.
	if len(mq.Gates) > 0 {
		cfg.Gates = make(map[string]rigGate, len(mq.Gates))
		for name, rawGate := range mq.Gates {
			var gate struct {
				Cmd     string `json:"cmd"`
				Phase   string `json:"phase"`
				Timeout string `json:"timeout"`
			}
			if err := json.Unmarshal(rawGate, &gate); err == nil && gate.Cmd != "" {
				rg := rigGate{Cmd: gate.Cmd, Phase: gate.Phase}
				if gate.Timeout != "" {
					if d, err := time.ParseDuration(gate.Timeout); err == nil && d > 0 {
						rg.Timeout = d
					}
				}
				cfg.Gates[name] = rg
			}
		}
	}

	if mq.GatesParallel != nil {
		cfg.GatesParallel = *mq.GatesParallel
	}

	// Fall back to legacy test_command
	if mq.TestCommand != nil && *mq.TestCommand != "" {
		cfg.TestCommand = *mq.TestCommand
	}

	// Optional setup/install command (e.g., "pnpm install"). When set, this
	// runs before gates/tests so dependency-hungry commands like build gates
	// find node_modules / .venv / etc. already populated.
	if mq.SetupCommand != nil && *mq.SetupCommand != "" {
		cfg.SetupCommand = *mq.SetupCommand
	}

	if len(cfg.Gates) == 0 && cfg.TestCommand == "" {
		return nil, nil // No runnable commands
	}

	return cfg, nil
}

// runMainBranchTests runs quality gates on each rig's main branch.
// It fetches the latest main, runs configured gates/tests, and escalates failures.
//
// On failure, each rig's section in the escalation body carries structured
// `commit:` / `previous_commit:` lines naming the SHA that broke and the most
// recent SHA that previously passed. A downstream daemon dog (Phase 0 task 11
// of the auto-test-pr design) parses those lines to resolve the breaking
// commit back to its merge-request bead and decide whether a SEV-1
// auto-revert chain fires.
func (d *Daemon) runMainBranchTests() {
	if !d.isPatrolActive("main_branch_test") {
		return
	}

	d.logger.Printf("main_branch_test: starting patrol cycle")

	rigNames := d.getPatrolRigs("main_branch_test")
	if len(rigNames) == 0 {
		d.logger.Printf("main_branch_test: no rigs found")
		return
	}

	// Sweep act CI containers leaked by prior cycles whose post-step cleanup
	// was SIGKILLed at the deadline before act could tear them down (gs-rd8).
	// Runs once per cycle, before any new gate run, so the host stops
	// re-accumulating leaked containers.
	d.reapLeakedActContainers()

	timeout := mainBranchTestTimeout(d.patrolConfig)
	flakeThreshold := mainBranchTestFlakeThreshold(d.patrolConfig)

	var tested, failed int
	var failures []string
	var escalatedRigs []string

	// gu-yl2av: snapshot each rig's attribution state at the START of the cycle,
	// before the loop mutates it. recordFailureAndShouldEscalate persists each
	// escalated rig's LastEscalatedSignature (and the streak/anchor the gs-3pe
	// backoff keys off) BEFORE the single batched escalate below. If that batched
	// escalate then fails (gt missing, Dolt degraded, timeout), none of the rigs
	// actually paged — yet their this-cycle markers would dedup the failures out,
	// and the backoff would skip re-running the suite, burying the red main
	// forever. On escalate failure we restore the escalated rigs to this snapshot,
	// undoing this cycle's bookkeeping so the next cycle re-runs and re-escalates.
	// Same class as D5/D12 (gu-nid89.43, a9d4a6f4); structurally harder here
	// because the marker is per-rig while the escalate is batched across rigs.
	preCycleEntries := loadMainBranchTestState(d.config.TownRoot).Rigs

	for _, rigName := range rigNames {
		rigPath := filepath.Join(d.config.TownRoot, rigName)

		// Serialize each rig's gate suite behind a town-global flock so two
		// act/Docker CI suites never run at once (gs-b1l). The lock is held
		// only for the gate run, not the subsequent attribution/escalation
		// bookkeeping. A lock error means the daemon context was canceled
		// (shutdown) — abort the patrol cleanly rather than escalate.
		release, lockErr := acquireGlobalGateLock(d.ctx, d.config.TownRoot)
		if lockErr != nil {
			d.logger.Printf("main_branch_test: aborting patrol — global gate lock unavailable: %v", lockErr)
			return
		}
		currentSHA, runErr := d.testRigMainBranch(rigName, rigPath, timeout, flakeThreshold)
		release()

		// gs-3pe: the runner backed off — main is confirmed red at this SHA and
		// no new commit has landed. Skip without escalating and without touching
		// the baseline, exactly like the host-kill skip below; a later cycle on a
		// NEW commit records the real pass/fail. This is the circuit-breaker for
		// the load-174 retry storm.
		if runErr != nil && errors.Is(runErr, errMainRedBackoff) {
			d.logger.Printf("main_branch_test: %s: SKIPPED — main still red at %s (already confirmed), waiting for a new commit", rigName, currentSHA)
			tested++
			continue
		}

		// hq-0qszq: a host-load SIGKILL is NOT a regression and NOT a pass.
		// Skip it without escalating and without touching the attribution
		// baseline, so a load spike can't masquerade as a main-branch failure
		// (the overseer was paged on exactly this). A later clean cycle records
		// the real pass/fail.
		if runErr != nil && errors.Is(runErr, errGateHostKilled) {
			d.logger.Printf("main_branch_test: %s: SKIPPED — transient host kill, not a regression: %v", rigName, runErr)
			tested++
			continue
		}

		// Persist outcome BEFORE escalation emission so a crash between the
		// gate suite and the bd-call still advances the per-rig baseline.
		// State updates are best-effort: a write failure does not stall the
		// patrol — the next successful cycle re-establishes the baseline and
		// any intermediate failure escalates with previous_commit: unknown
		// until then.
		now := time.Now()

		if runErr == nil {
			recordAttributionRun(d.config.TownRoot, rigName, currentSHA, true, now)
			d.logger.Printf("main_branch_test: %s: passed", rigName)
			tested++
			continue
		}

		// hq-6qnct: a single failing cycle that passes the surrounding cycles is
		// a host-load flake, not a regression. Only escalate HIGH once the gate
		// has failed `threshold` cycles in a row, and dedup repeat pages for the
		// same failing-gate signature. The failure path never promotes the
		// breaking SHA to the last-passing baseline; recordFailureAndShouldEscalate
		// advances LastRunAt and the streak.
		sig := failureSignature(runErr)
		isTimeout := isTimeoutFailure(runErr)
		shouldEscalate, streak := recordFailureAndShouldEscalate(d.config.TownRoot, rigName, sig, currentSHA, flakeThreshold, isTimeout, now)
		// gs-iz2: a timeout-classified red is held to a higher watermark than an
		// assertion red, so log the effective watermark (not the raw assertion
		// threshold) when below it.
		watermark := flakeThreshold
		if isTimeout {
			watermark = timeoutEscalationThreshold(flakeThreshold)
		}
		if shouldEscalate {
			d.logger.Printf("main_branch_test: %s: FAILED (streak=%d, signature=%s, timeout=%t) — escalating: %v", rigName, streak, sig, isTimeout, runErr)
			failures = append(failures, formatRigFailureSection(rigName, currentSHA, d.config.TownRoot, runErr))
			escalatedRigs = append(escalatedRigs, rigName)
			failed++
		} else {
			d.logger.Printf("main_branch_test: %s: FAILED (streak=%d/%d, signature=%s, timeout=%t) — below flake watermark or already paged, not escalating: %v",
				rigName, streak, watermark, sig, isTimeout, runErr)
		}
		tested++
	}

	if len(failures) > 0 {
		msg := fmt.Sprintf("main branch test failures:\n%s", strings.Join(failures, "\n"))
		d.logger.Printf("main_branch_test: escalating %d failure(s)", len(failures))
		// gu-yl2av: gate this cycle's per-rig handled-markers on escalate success.
		// recordFailureAndShouldEscalate already persisted LastEscalatedSignature
		// (and the streak/LastFailedSHA the gs-3pe backoff reads) for each escalated
		// rig BEFORE this batched call. If the escalate fails, none of them actually
		// paged, so roll the escalated rigs back to their start-of-cycle snapshot —
		// otherwise the dedup marker would suppress the page forever AND the backoff
		// would skip re-running the suite, burying a genuinely-red main.
		if err := d.escalate("main_branch_test", msg); err != nil {
			d.logger.Printf("main_branch_test: escalation failed: %v — reverting %d rig marker(s) so the next cycle retries", err, len(escalatedRigs))
			revertEscalationMarkers(d.config.TownRoot, escalatedRigs, preCycleEntries)
		}
	}

	d.logger.Printf("main_branch_test: patrol cycle complete (%d tested, %d failed)", tested, failed)
}

// formatRigFailureSection renders a single rig's entry in the
// main_branch_test escalation body. The shape is:
//
//	<rig>: <error>
//	commit: <sha>
//	previous_commit: <sha or "unknown">
//
// The first line preserves the legacy "<rig>: <err>" format so existing
// consumers (failure_classifier_dog parses the rig name from this prefix,
// downstream signature regexes match against the failure body) keep working
// unchanged. The trailing attribution lines are additive.
//
// When the runner could not capture a current SHA (e.g. fetch failed before
// rev-parse, missing bare repo), attribution lines are omitted entirely.
// Emitting `commit: ` with an empty value would create a false positive for
// downstream parsers that treat prefix presence as "we have a SHA to work
// with".
func formatRigFailureSection(rigName, currentSHA, townRoot string, runErr error) string {
	body := fmt.Sprintf("%s: %v", rigName, runErr)
	if currentSHA == "" {
		return body
	}
	previousSHA := readPreviousPassingSHA(townRoot, rigName)
	attribution := formatAttributionLines(currentSHA, previousSHA)
	if attribution == "" {
		return body
	}
	return body + "\n" + attribution
}

// resolveMainBranchTestBranch resolves which branch the main_branch_test patrol
// fetches, worktrees, and runs gates against for a rig. Resolution order:
//
//  1. rig config default_branch (source of truth, set at rig creation)
//  2. the bare repo's actual default branch (symbolic-ref HEAD) — bare repos
//     have no refs/remotes/origin/HEAD, so DefaultBranch() (not
//     RemoteDefaultBranch()) is the correct probe; clone --bare points HEAD at
//     the remote's default branch.
//  3. "main" as the final fallback when neither source answers.
//
// This mirrors resolveRigDefaultBranch in internal/cmd/done.go (gu-wcb37) so
// the patrol and the merge path agree on the branch for "mainline"-default
// rigs. See gu-ez4as.
func resolveMainBranchTestBranch(rigPath, bareRepoPath string) string {
	if rigCfg, err := rig.LoadRigConfig(rigPath); err == nil && rigCfg.DefaultBranch != "" {
		return rigCfg.DefaultBranch
	}
	if b := gitpkg.NewGitWithDir(bareRepoPath, "").DefaultBranch(); b != "" {
		return b
	}
	return "main"
}

// testRigMainBranch tests a single rig's main branch. Returns the
// origin/<default_branch> SHA the patrol ran against (empty when the SHA
// could not be captured — typically because the bare repo or fetch step
// failed before rev-parse) and the gate-suite outcome.
//
// The SHA is returned regardless of pass/fail so callers can: (a) emit
// `commit:` attribution on failure, and (b) update the per-rig last-passing
// baseline on success.
func (d *Daemon) testRigMainBranch(rigName, rigPath string, timeout time.Duration, flakeThreshold int) (string, error) {
	// Load gate config from the rig's config.json
	gateCfg, err := loadRigGateConfig(rigPath)
	if err != nil {
		return "", fmt.Errorf("loading gate config: %w", err)
	}
	if gateCfg == nil {
		d.logger.Printf("main_branch_test: %s: no test commands configured, skipping", rigName)
		return "", nil
	}

	// Create a temporary worktree for testing to avoid interfering with
	// the refinery's working directory.
	worktreePath := filepath.Join(rigPath, ".main-test-worktree")
	bareRepoPath := filepath.Join(rigPath, ".repo.git")

	// Verify bare repo exists
	if _, err := os.Stat(bareRepoPath); os.IsNotExist(err) {
		return "", fmt.Errorf("bare repo not found at %s", bareRepoPath)
	}

	// Determine the branch to test. The rig config's default_branch is the
	// source of truth; when it is unreadable or empty, detect the bare repo's
	// actual default branch (symbolic-ref HEAD) rather than hardcoding "main".
	// A hardcoded "main" produced false-positive gate failures on rigs whose
	// default_branch is "mainline" (gu-ez4as): the fetch/worktree-add targeted
	// a branch that does not exist, so every cycle escalated a phantom red.
	// "main" is kept only as the final fallback when neither source answers.
	// Mirrors resolveRigDefaultBranch in internal/cmd/done.go (gu-wcb37).
	defaultBranch := resolveMainBranchTestBranch(rigPath, bareRepoPath)

	if err := cleanupStaleWorktree(bareRepoPath, worktreePath); err != nil {
		return "", err
	}

	// Effective parent timeout grows to fit declared per-gate budgets so a
	// rig that declares build=5m + test=10m doesn't get clamped to a 10m
	// rig-level ceiling and SIGTERM'd mid-pytest. See gu-z76g for the
	// real-world failure mode (talontriage gc-vqwud).
	effectiveTimeout := computeEffectiveTimeout(timeout, gateCfg)

	ctx, cancel := context.WithTimeout(d.ctx, effectiveTimeout)
	defer cancel()

	// Fetch latest main
	fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin", defaultBranch)
	fetchCmd.Dir = bareRepoPath
	util.SetDetachedProcessGroup(fetchCmd)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git fetch failed: %v (%s)", err, strings.TrimSpace(string(output)))
	}

	// Capture the SHA we're about to test against. This is the value that
	// will appear in `commit:` attribution if the gate suite fails. Capture
	// AFTER fetch so it reflects post-fetch HEAD, BEFORE worktree creation
	// so a worktree-add failure doesn't poison attribution. A capture
	// failure here is non-fatal — we still run the gates and emit the
	// failure escalation, just without attribution lines.
	currentSHA, shaErr := captureRigHeadSHA(ctx, rigPath, defaultBranch)
	if shaErr != nil {
		d.logger.Printf("main_branch_test: %s: warning: could not capture HEAD SHA for attribution: %v",
			rigName, shaErr)
		currentSHA = ""
	}

	// gs-3pe: back off on confirmed-red main. The fetch above is cheap; the
	// worktree add + gate suite below is the heavyweight act/Docker work. If
	// main is already confirmed red at this SHA (streak past the flake
	// watermark) and HEAD hasn't advanced, skip that work entirely — re-running
	// it just spikes host load and manufactures the next "failure". Returns the
	// SHA so the caller can log which commit we're waiting to move past.
	if shouldBackOffOnRedMain(d.config.TownRoot, rigName, currentSHA, flakeThreshold) {
		return currentSHA, errMainRedBackoff
	}

	// Create temporary worktree at origin/<default_branch>
	addCmd := exec.CommandContext(ctx, "git", "worktree", "add", "--detach", worktreePath, "origin/"+defaultBranch)
	addCmd.Dir = bareRepoPath
	util.SetDetachedProcessGroup(addCmd)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return currentSHA, fmt.Errorf("git worktree add failed: %v (%s)", err, strings.TrimSpace(string(output)))
	}

	// Always clean up the worktree. Belt-and-suspenders: `git worktree remove`
	// handles the registered case; `os.RemoveAll` mops up if the gate run
	// left files git refused to remove (e.g., crashed mid-run leaving a
	// detached target/ tree). Without the unconditional RemoveAll, an
	// orphaned directory blocks the next run — see gu-dob2f.
	defer func() {
		removeCmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
		removeCmd.Dir = bareRepoPath
		util.SetDetachedProcessGroup(removeCmd)
		if err := removeCmd.Run(); err != nil {
			d.logger.Printf("main_branch_test: %s: warning: worktree cleanup failed: %v", rigName, err)
		}
		if err := os.RemoveAll(worktreePath); err != nil {
			d.logger.Printf("main_branch_test: %s: warning: worktree dir removal failed: %v", rigName, err)
		}
	}()

	// Run the rig's opt-in setup_command (if any) BEFORE gates so gates that
	// genuinely need pre-populated deps (pnpm install, uv sync, etc.) find
	// them. Rigs that self-install via their gate scripts (brazil-build, go,
	// cargo) simply leave setup_command empty and this is a no-op. See
	// gu-hl5w for the original install-step motivation and gu-pcm5 for the
	// opt-in inversion that made auto-detect fail for brazil-build rigs.
	if err := d.runPreBuildInstall(ctx, rigName, worktreePath, gateCfg); err != nil {
		return currentSHA, err
	}

	// Run gates or legacy test command
	if len(gateCfg.Gates) > 0 {
		return currentSHA, d.runGatesOnWorktree(ctx, rigName, worktreePath, gateCfg.Gates, gateCfg.GatesParallel)
	}
	return currentSHA, d.runCommandOnWorktree(ctx, rigName, worktreePath, "test", gateCfg.TestCommand)
}

// cleanupStaleWorktree removes any pre-existing content at worktreePath so a
// subsequent `git worktree add` succeeds. Two cases must be handled:
//
//  1. A registered worktree from a clean prior run — `git worktree remove
//     --force` deregisters it and deletes the directory.
//  2. An orphan directory from a killed prior run — no `.git` link, not
//     registered with the bare repo, just leftover build artifacts (e.g.,
//     multi-GB Cargo target/ trees). `git worktree remove` is a no-op here,
//     so without an unconditional RemoveAll the path persists and the next
//     `git worktree add` fails with "already exists". See gu-dob2f for the
//     real-world failure where 3.7GB of orphaned Cargo artifacts blocked all
//     subsequent main_branch_test runs for a rig.
//
// Returns nil when worktreePath did not exist. Returns an error only if the
// path exists and final RemoveAll fails — that's the unrecoverable case
// where the next `git worktree add` is guaranteed to fail.
func cleanupStaleWorktree(bareRepoPath, worktreePath string) error {
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat stale worktree %s: %w", worktreePath, err)
	}
	cleanupCmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
	cleanupCmd.Dir = bareRepoPath
	util.SetDetachedProcessGroup(cleanupCmd)
	_ = cleanupCmd.Run()
	if err := os.RemoveAll(worktreePath); err != nil {
		return fmt.Errorf("removing stale worktree directory %s: %w", worktreePath, err)
	}
	return nil
}

// computeEffectiveTimeout returns the parent context budget for a rig's
// gate run. The base is the rig-level timeout (`mainBranchTestTimeout`,
// default 10m). When per-gate timeouts are declared, the parent is widened
// so it cannot be the bottleneck:
//
//   - sequential mode: parent = max(base, sum of per-gate timeouts)
//   - parallel mode:   parent = max(base, max per-gate timeout)
//
// Gates with an unset timeout (Timeout==0) contribute nothing — the loose
// idea is "if the operator declared a budget, honor it; if they didn't,
// the rig-level ceiling still applies". The base stays the lower bound so
// a rig that declares only one cheap 30s gate doesn't accidentally lose
// the safety net the rig-level timeout provides.
func computeEffectiveTimeout(base time.Duration, cfg *rigGateConfig) time.Duration {
	if cfg == nil || len(cfg.Gates) == 0 {
		return base
	}
	var (
		sum     time.Duration
		maxGate time.Duration
	)
	for _, gate := range cfg.Gates {
		if gate.Timeout <= 0 {
			continue
		}
		sum += gate.Timeout
		if gate.Timeout > maxGate {
			maxGate = gate.Timeout
		}
	}
	candidate := sum
	if cfg.GatesParallel {
		candidate = maxGate
	}
	if candidate > base {
		return candidate
	}
	return base
}

// runPreBuildInstall runs the dependency install step for a rig's worktree
// before its gates/tests execute. Resolution is opt-in:
//
//  1. If the rig config sets merge_queue.setup_command, run that verbatim.
//     This is the operator's declaration that this rig needs a dedicated
//     pre-build install (pnpm install, npm ci, uv sync, etc.).
//  2. Otherwise, skip silently. This is the correct default for rigs whose
//     gate commands install their own deps (brazil-build, cargo, go build,
//     bun/deno on demand) — auto-installing from a lockfile in those rigs
//     either does redundant work or (worse) fails with E404 because the
//     package.json references Brazil-only @amzn/* deps that aren't published
//     to any public registry. See gu-pcm5 for the failure mode.
//
// The previous implementation auto-detected a package manager from lockfiles
// (package.json + package-lock.json → npm ci, etc.) and ran it unconditionally.
// That guessed wrong for brazil-build rigs (8 casc_* rigs in this town) and
// had to be worked around with setup_command=":" — an explicit no-op that
// proves the default was inverted. Now the default IS no-op; rigs that want
// a pre-build install declare it explicitly.
//
// A failed install is reported as a clearly labeled "install" failure so
// operators see "install failed" instead of a downstream build gate whining
// about missing deps.
func (d *Daemon) runPreBuildInstall(ctx context.Context, rigName, workDir string, cfg *rigGateConfig) error {
	if cfg == nil || cfg.SetupCommand == "" {
		return nil // No setup_command configured — opt-in only.
	}

	// Bound install with its own timeout so a wedged install doesn't eat the
	// whole per-rig budget. Clamp to parent ctx so overall timeout still wins.
	installCtx, cancel := context.WithTimeout(ctx, defaultInstallTimeout)
	defer cancel()

	return d.runCommandOnWorktree(installCtx, rigName, workDir, "install (setup_command)", cfg.SetupCommand)
}

// runGatesOnWorktree runs all pre-merge gates on the given worktree, either
// sequentially (the safe default — preserves implicit ordering deps like
// install→test) or concurrently when the rig declares
// merge_queue.gates_parallel=true.
//
// Post-squash gates are skipped: they are defined to run on the squash-merged
// result in refinery's pipeline and frequently rely on ambient state
// (checked-out brazil workspace, merged version-set) that a cold origin/main
// worktree lacks. Running them in main_branch_test produces spurious
// escalations that obscure real regressions. See gu-j1f7.
//
// Gates are iterated/started in a deterministic order (alphabetical by name)
// so that rigs whose gates have implicit ordering dependencies (e.g. "install"
// must run before "test" populates node_modules) get stable behavior instead
// of ~50% false-failures from random Go map iteration. See gu-i0mb. Rigs that
// need a non-alphabetical order should split their work across explicit
// lifecycle hooks (e.g. a "pretest" script) rather than rely on gate naming.
//
// Per-gate timeouts (rigGate.Timeout) are honored independently of the parent
// context: a gate with timeout=5m gets context.WithTimeout(parent, 5m); a gate
// with no declared timeout inherits the parent deadline. See gu-z76g.
func (d *Daemon) runGatesOnWorktree(ctx context.Context, rigName, workDir string, gates map[string]rigGate, parallel bool) error {
	var skipped []string
	names := make([]string, 0, len(gates))
	for name := range gates {
		names = append(names, name)
	}
	sort.Strings(names)

	// Filter to runnable (pre-merge) gates while logging skips deterministically.
	runNames := make([]string, 0, len(names))
	for _, name := range names {
		phase := gates[name].Phase
		if phase == "" {
			phase = "pre-merge"
		}
		if phase != mainBranchTestPhase {
			skipped = append(skipped, fmt.Sprintf("%s(%s)", name, phase))
			continue
		}
		runNames = append(runNames, name)
	}
	if len(skipped) > 0 {
		d.logger.Printf("main_branch_test: %s: skipped non-pre-merge gates: %s",
			rigName, strings.Join(skipped, ", "))
	}

	failures := make([]string, len(runNames))
	if parallel && len(runNames) > 1 {
		var wg sync.WaitGroup
		for i, name := range runNames {
			wg.Add(1)
			go func(idx int, gateName string) {
				defer wg.Done()
				if err := d.runGateWithTimeout(ctx, rigName, workDir, gateName, gates[gateName]); err != nil {
					failures[idx] = fmt.Sprintf("gate %q: %v", gateName, err)
				}
			}(i, name)
		}
		wg.Wait()
	} else {
		for i, name := range runNames {
			if err := d.runGateWithTimeout(ctx, rigName, workDir, name, gates[name]); err != nil {
				failures[i] = fmt.Sprintf("gate %q: %v", name, err)
			}
		}
	}

	var msgs []string
	for _, f := range failures {
		if f != "" {
			msgs = append(msgs, f)
		}
	}
	if len(msgs) > 0 {
		return fmt.Errorf("%s", strings.Join(msgs, "; "))
	}
	return nil
}

// runGateWithTimeout runs a single gate command, applying the gate's optional
// per-gate timeout on top of the parent context. When Timeout==0 the gate
// inherits the parent deadline unchanged.
func (d *Daemon) runGateWithTimeout(ctx context.Context, rigName, workDir, name string, gc rigGate) error {
	if gc.Timeout > 0 {
		gateCtx, cancel := context.WithTimeout(ctx, gc.Timeout)
		defer cancel()
		return d.runCommandOnWorktree(gateCtx, rigName, workDir, name, gc.Cmd)
	}
	return d.runCommandOnWorktree(ctx, rigName, workDir, name, gc.Cmd)
}

// gateDoltEnvDenyPrefixes lists env-var name prefixes scrubbed from the gate
// subprocess environment so that `go test ./...` does NOT inherit the
// production daemon's Dolt-routing variables (gu-5ja0e).
//
// The daemon process runs with GT_DOLT_PORT/BEADS_DOLT_PORT pinned to the
// shared production Dolt server (3307). Passing those through to the gate's
// `go test` defeats the beads test-isolation safety net: PreventTestDoltLeak
// (internal/beads/database.go) only pins a test fixture to an isolated embedded
// data dir when NO Dolt-routing var is set — when it sees an inherited
// GT_DOLT_PORT/BEADS_DOLT_PORT it assumes a legitimate test container and bails,
// so any beads-backed test then connects to production :3307 and leaks orphan
// databases into .dolt-data/. Container-backed integration tests are unaffected:
// they start their own container and set GT_DOLT_PORT process-wide from inside
// the test (testutil.StartIsolatedDoltContainer / RequireDoltContainer), so
// scrubbing the inherited value cannot route them to production.
var gateDoltEnvDenyPrefixes = []string{
	"GT_DOLT_",    // GT_DOLT_PORT, GT_DOLT_HOST
	"BEADS_DOLT_", // BEADS_DOLT_PORT, BEADS_DOLT_SERVER_HOST, BEADS_DOLT_SERVER_PORT
	"DOLT_",       // raw dolt client overrides
}

// stripGateDoltEnv returns a copy of env with Dolt-routing variables removed
// per gateDoltEnvDenyPrefixes. Pure function for unit-testing the filter.
func stripGateDoltEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			out = append(out, kv)
			continue
		}
		key := kv[:eq]
		denied := false
		for _, p := range gateDoltEnvDenyPrefixes {
			if strings.HasPrefix(key, p) {
				denied = true
				break
			}
		}
		if !denied {
			out = append(out, kv)
		}
	}
	return out
}

// gateEnv builds the subprocess environment for gate commands.
// PATH is augmented with common user tool directories so commands like yarn,
// act, etc. are discoverable even when the daemon runs with a minimal
// inherited PATH (e.g. when started as a background service).
//
// Dolt-routing variables are scrubbed (gu-5ja0e) so gate `go test` runs cannot
// connect to the production Dolt server; see gateDoltEnvDenyPrefixes.
func gateEnv(townRoot string) []string {
	home, _ := os.UserHomeDir()
	dirs := []string{
		filepath.Join(townRoot, "bin"),
		filepath.Join(home, "go", "bin"),
		filepath.Join(home, ".local", "bin"),
		"/usr/local/bin",
	}
	enriched := strings.Join(append(dirs, os.Getenv("PATH")), string(os.PathListSeparator))
	return append(stripGateDoltEnv(os.Environ()), "CI=true", "PATH="+enriched)
}

// failureOutputTailSize is the number of trailing output lines kept verbatim
// in runCommandOnWorktree failure messages. Signal lines (--- FAIL:, FAIL\t,
// bare FAIL) above this window are additionally preserved by
// formatFailureOutput.
const failureOutputTailSize = 50

// errGateHostKilled marks a gate that was SIGKILLed by the host (OOM killer /
// jetsam under load/swap pressure) rather than failing on its own. It is a
// TRANSIENT condition — the code under test is not a regression — so callers
// must NOT escalate it as a main-branch failure. See hq-0qszq.
var errGateHostKilled = errors.New("gate killed by host load (transient, not a regression)")

// errMainRedBackoff marks a cycle the runner deliberately SKIPPED because main
// is already confirmed red at the current SHA and no new commit has landed
// (gs-3pe). It is NOT a pass and NOT a failure — callers must neither escalate
// it nor touch the attribution baseline; the next NEW commit re-arms a real
// run. Backing off here breaks the retry storm where re-running the heavyweight
// act/Docker suite on a known-red SHA manufactured the load that timed out the
// gates and read as a fresh failure.
var errMainRedBackoff = errors.New("main confirmed red at current SHA, backing off until a new commit")

// errGateTimeout marks a gate that exceeded its deadline (context deadline
// exceeded) rather than failing an assertion. A timeout is AMBIGUOUS: under
// host load it's a transient slowdown (like a host-kill), but a genuine
// regression that HANGS also times out. So unlike errGateHostKilled we do NOT
// silently skip it — masking timeouts would hide real hangs (gs-iz2). Instead
// the patrol requires a higher confirmation watermark before paging a
// timeout-red (a one/two-cycle load timeout never false-pages), while a
// sustained timeout still escalates as a real hang.
var errGateTimeout = errors.New("gate exceeded deadline (timeout, not an assertion failure)")

// gateHostKillRetries is how many extra times a host-killed gate is re-run
// before giving up and reporting it transient. A short backoff between attempts
// lets a load/swap spike subside.
const gateHostKillRetries = 2

// gateHostKillBackoff is the pause between host-kill retries. A var so tests can
// shrink it; production lets a load/swap spike subside before re-running.
var gateHostKillBackoff = 20 * time.Second

// isHostKill reports whether a gate error is an external host signal (SIGKILL
// from OOM/jetsam under load, or SIGTERM from contention/parent-death cascades)
// rather than a genuine test FAIL or our own deadline cancellation.
//
// Two signal classes count:
//
//   - SIGKILL surfaces as "signal: killed" — typically OOM killer / jetsam
//     under load/swap pressure (hq-0qszq).
//   - SIGTERM surfaces as "signal: terminated" — observed in production when
//     brazil-build under shared-workspace contention (the casc_* rigs share
//     /tmp/codegen-agent-scheduler-gate.lock) emits its own SIGTERM, or when a
//     PR_SET_PDEATHSIG cascade from a sibling process delivers SIGTERM to the
//     gate group. The gt cancel path itself sends SIGKILL via
//     util.SetProcessGroup's cmd.Cancel hook, so a SIGTERM observed without an
//     expired context is necessarily external. See gu-13y6 for the SIGTERM-class
//     evidence (18s and 1m38s "signal: terminated" mid-build, well under the
//     20m budget).
//
// A go test that genuinely fails exits non-zero with "FAIL" output; an
// external host signal terminates with no FAIL marker, while our context did
// NOT hit its deadline (we didn't cancel it).
func isHostKill(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() == context.DeadlineExceeded {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "signal: killed") ||
		strings.Contains(msg, "signal: terminated")
}

// isTimeoutFailure reports whether a gate failure is a context-deadline
// timeout (errGateTimeout) rather than a deterministic assertion/exit failure
// (gs-iz2). It matches BOTH the wrapped sentinel — the legacy test_command
// path propagates the error chain — AND the flattened string form, because the
// gates path joins per-gate errors into a plain string (fmt.Sprintf with %v),
// dropping the chain. String-matching the sentinel's message keeps the
// classification working through both runner paths.
func isTimeoutFailure(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, errGateTimeout) || strings.Contains(err.Error(), errGateTimeout.Error())
}

// runCommandOnWorktree runs a single shell command in the given worktree
// directory, retrying transient host-load SIGKILLs (hq-0qszq) so they don't
// masquerade as main-branch regressions.
func (d *Daemon) runCommandOnWorktree(ctx context.Context, rigName, workDir, label, command string) error {
	d.logger.Printf("main_branch_test: %s: running %s: %s", rigName, label, command)

	var err error
	var output []byte
	for attempt := 0; ; attempt++ {
		cmd := exec.CommandContext(ctx, "sh", "-c", command) //nolint:gosec // G204: command is from trusted rig config
		cmd.Dir = workDir
		cmd.Env = gateEnv(d.config.TownRoot)
		// gs-lfr: use the process-group variant that ALSO installs a cancel hook
		// killing the whole group, not the detached variant (no cancel hook).
		// CommandContext's default cancel SIGKILLs only the leader PID, but `sh -c`
		// forks the gate command as a child that keeps the output pipe open, so
		// CombinedOutput blocks until the child exits on its own — making per-gate
		// timeouts (gateCtx) run the full command duration instead of canceling at
		// the deadline (da058e57/gu-z76g added the timeout plumbing but kept the
		// detached, no-cancel variant). Killing the group (-pgid) terminates the
		// whole tree at the deadline.
		util.SetProcessGroup(cmd)

		output, err = cmd.CombinedOutput()
		if err == nil {
			return nil
		}

		// Detect false-positive: the gate command succeeded but post-step
		// cleanup (container teardown, cache save) ran past the deadline.
		// act and pre-commit both emit success markers before starting
		// cleanup, so if the context expired AND the output shows success,
		// this is noise — not a real regression. See gs-llj.
		if ctx.Err() == context.DeadlineExceeded && isCleanupOnlyTimeout(output) {
			d.logger.Printf("main_branch_test: %s: %s: timed out during post-step cleanup after successful run (ignored)", rigName, label)
			return nil
		}

		// hq-0qszq: a SIGKILL with our context still alive is a HOST kill
		// (OOM/jetsam under load), not a test result. Retry after a backoff so a
		// load spike doesn't masquerade as a regression. A genuine FAIL (non-zero
		// exit, FAIL output) is NOT retried — it fails deterministically.
		if isHostKill(ctx, err) && attempt < gateHostKillRetries {
			d.logger.Printf("main_branch_test: %s: %s: SIGKILLed (host load/OOM, not a regression) — retry %d/%d after %v",
				rigName, label, attempt+1, gateHostKillRetries, gateHostKillBackoff)
			select {
			case <-ctx.Done():
				return fmt.Errorf("%w: %s canceled during load-kill backoff", errGateHostKilled, label)
			case <-time.After(gateHostKillBackoff):
			}
			continue
		}
		break
	}

	if isHostKill(ctx, err) {
		// Still killed after retries — surface as transient so the patrol does
		// NOT escalate it as a main-branch regression (hq-0qszq).
		d.logger.Printf("main_branch_test: %s: %s: still SIGKILLed after %d attempts — host load, marking transient (NOT a regression)",
			rigName, label, gateHostKillRetries+1)
		return fmt.Errorf("%w: %s (%v)", errGateHostKilled, label, err)
	}

	// gs-iz2: our own deadline fired (not a host kill, not a cleanup-only
	// false positive handled above) — the gate exceeded its budget. Mark it as
	// a timeout so the patrol can hold it to a higher confirmation watermark
	// before paging: a one-off slowdown under host load shouldn't false-page,
	// while a sustained timeout still escalates as a real hang.
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%w: %s (%v)\n%s", errGateTimeout, label, err, formatFailureOutput(string(output), failureOutputTailSize))
	}

	return fmt.Errorf("%s failed: %v\n%s", label, err, formatFailureOutput(string(output), failureOutputTailSize))
}

// formatFailureOutput renders command output for inclusion in an error
// message, preserving actionable go-test failure signal lines even when the
// tail-truncation window would otherwise chop them off.
//
// Why: `go test ./...` on a large module (gastown has ~70 test packages)
// emits one line per package ("ok <pkg>" / "FAIL <pkg>") and the
// "--- FAIL: TestName" marker identifying the actually-failing test can sit
// well above the last-N-lines window. Returning only the tail therefore
// strips the single most important piece of information — which test broke —
// leaving operators staring at a wall of "ok" plus a naked "FAIL". See
// gu-m5w9 for the original diagnosis.
//
// Strategy: always include the last tailSize lines; additionally prepend any
// go-test failure signal lines that appear in the truncated prefix so their
// identity survives the window. A clearly labeled separator marks where the
// tail starts so operators can tell prepended signals from in-window content.
//
// Signals are capped independently of the tail so a catastrophic failure
// with thousands of FAIL lines doesn't produce an unbounded escalation body.
func formatFailureOutput(raw string, tailSize int) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")

	tailStart := 0
	if tailSize > 0 && len(lines) > tailSize {
		tailStart = len(lines) - tailSize
	}
	tail := lines[tailStart:]

	// Only lines BEFORE the tail window need rescuing; anything already in
	// the tail reaches the operator unmodified.
	var signals []string
	for _, l := range lines[:tailStart] {
		if isGoTestFailSignal(l) {
			signals = append(signals, l)
		}
	}

	if len(signals) == 0 {
		return strings.Join(tail, "\n")
	}

	const signalCap = 50
	extraOmitted := 0
	if len(signals) > signalCap {
		extraOmitted = len(signals) - signalCap
		signals = signals[:signalCap]
	}

	var b strings.Builder
	for _, s := range signals {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	if extraOmitted > 0 {
		fmt.Fprintf(&b, "... [%d additional FAIL signal line(s) omitted] ...\n", extraOmitted)
	}
	fmt.Fprintf(&b, "... [truncated %d line(s) above; showing last %d] ...\n", tailStart, len(tail))
	b.WriteString(strings.Join(tail, "\n"))
	return b.String()
}

// isGoTestFailSignal reports whether line identifies a failing test or
// package in `go test` output and should survive tail truncation. Matched
// patterns:
//
//	"--- FAIL: TestName (0.00s)"       top-level failing test
//	"FAIL\tgithub.com/x/y\t0.005s"     per-package failure summary
//	"FAIL github.com/x/y 0.005s"       space-separated package summary
//	"FAIL"                             overall go-test bailout line
//
// Indented lines (subtest "--- FAIL:" markers) are intentionally not matched:
// their parent top-level "--- FAIL:" line is always emitted by the go test
// runner and carries sufficient identity for operators. Keeping the pattern
// strict avoids pulling in unrelated indented prose that happens to start
// with "FAIL".
func isGoTestFailSignal(line string) bool {
	if strings.HasPrefix(line, "--- FAIL:") {
		return true
	}
	if line == "FAIL" {
		return true
	}
	// "FAIL<whitespace>..." — package-level summary.
	if strings.HasPrefix(line, "FAIL") && len(line) > 4 {
		c := line[4]
		if c == ' ' || c == '\t' {
			return true
		}
	}
	return false
}

// isCleanupOnlyTimeout returns true when a deadline-exceeded kill looks like a
// false positive: the actual gate work finished successfully and the SIGKILL
// only hit post-step cleanup (container teardown, cache saves, etc.).
//
// Detection relies on success markers that CI tools (act, pre-commit) emit
// before starting cleanup. If a success marker appears in the output and no
// failure marker follows it, the deadline fired during cleanup, not work.
func isCleanupOnlyTimeout(output []byte) bool {
	text := string(output)

	// Locate the last success marker. act emits "✅  Success - <job>" and
	// "[<job>] Done in <N>s"; pre-commit emits "Passed" at end of hook lines.
	successPatterns := []string{
		"✅  Success",  // act: per-job success line
		"\nDone in ",  // act: final timing summary (e.g. "\nDone in 133s")
		"...Passed\n", // pre-commit: hook passed suffix
		"..Passed\n",  // pre-commit: shorter separator variant
	}
	lastSuccess := -1
	for _, pat := range successPatterns {
		if idx := strings.LastIndex(text, pat); idx > lastSuccess {
			lastSuccess = idx
		}
	}
	if lastSuccess < 0 {
		return false // no success marker — real timeout or real failure
	}

	// If any failure marker appears AFTER the last success marker, the gate
	// actually failed (later job/hook failed after an earlier one passed).
	failurePatterns := []string{
		"❌  Failure",
		"...Failed\n",
		"..Failed\n",
		"FAILED\n",
	}
	tail := text[lastSuccess:]
	for _, pat := range failurePatterns {
		if strings.Contains(tail, pat) {
			return false // real failure occurred after the last success
		}
	}

	return true
}

// leakedActContainerAge is how long an `act` CI container may run before
// main_branch_test treats it as a leak and force-removes it. act spins up
// Docker containers for local CI gates; when a gate exceeds its deadline the
// gate's process group is SIGKILLed (util.SetProcessGroup's cancel hook —
// see runCommandOnWorktree) mid-teardown, so act never `docker rm`s the
// containers it started and they leak (observed Up 3-5h, compounding host
// resource pressure across cycles — gs-rd8, same family as the Dolt leak in
// gs-vxt). No single main_branch_test gate run approaches two hours (the
// rig-level timeout defaults to 10m and declared per-gate budgets sum to
// minutes), so any act container older than this is provably from a dead run
// and safe to remove even while other rigs' gates run concurrently on the
// shared host daemon.
const leakedActContainerAge = 2 * time.Hour

// reapDockerTimeout bounds each docker subprocess reapLeakedActContainers
// spawns. reapLeakedActContainers runs inline in the daemon main select loop
// (runMainBranchTests → daemon.go), not in a goroutine, so an unresponsive
// Docker daemon — exactly the host-overload condition this reaper exists to
// mitigate (gs-rd8) — would otherwise hang a plain exec.Command indefinitely
// and stall the whole daemon: no heartbeat, no Dolt health probe, no other
// patrol. With a bounded context the worst case is one stalled cycle of up to
// this budget, after which the reap bails (see reapLeakedActContainers).
const reapDockerTimeout = 30 * time.Second

// isActContainerName reports whether a Docker container name belongs to an
// `act` CI run. act names every job container `act-<workflow>-<job>-<hash>`;
// Docker reports names with a leading slash. Anchoring on the `act-` prefix
// (rather than a substring match) avoids reaping unrelated containers whose
// name merely contains "act" (e.g. "react-app", "compact-db").
func isActContainerName(name string) bool {
	name = strings.TrimPrefix(name, "/")
	return strings.HasPrefix(name, "act-")
}

// shouldReapLeakedContainer reports whether an act container last started at
// `started` is old enough to be treated as a leak at `now`. Split out so the
// age decision is unit-testable without a Docker daemon.
func shouldReapLeakedContainer(started, now time.Time) bool {
	return now.Sub(started) > leakedActContainerAge
}

// reapLeakedActContainers force-removes `act` CI containers left behind by
// gate runs whose post-step cleanup was SIGKILLed at the deadline before act
// could tear them down. It is the periodic self-healing leak check the patrol
// runs once per cycle so the host stops re-accumulating leaked act containers
// across runs (gs-rd8).
//
// Targets ONLY containers whose name carries the `act-` prefix (act's job
// naming scheme) and whose start time is older than leakedActContainerAge —
// the age gate guarantees it never removes a concurrently-running gate's live
// container. Best-effort: all Docker errors are ignored (worst case is the
// pre-existing leak persists one more cycle). A missing `docker` binary or
// unreachable daemon makes this a silent no-op.
func (d *Daemon) reapLeakedActContainers() {
	// Each docker call is bounded by reapDockerTimeout and derived from d.ctx
	// so a wedged Docker daemon cannot stall the daemon main loop this runs in.
	// runDocker reports whether the call hit its deadline (or d.ctx was
	// canceled) separately from ordinary docker errors so the caller can bail
	// on the first timeout: a hung daemon will hang every subsequent call too,
	// so there is nothing to gain from continuing this cycle.
	runDocker := func(args ...string) (out []byte, timedOut bool, err error) {
		ctx, cancel := context.WithTimeout(d.ctx, reapDockerTimeout)
		defer cancel()
		out, err = exec.CommandContext(ctx, "docker", args...).Output()
		return out, ctx.Err() != nil, err
	}

	out, _, err := runDocker("ps", "-a", "--no-trunc", "--format", "{{.ID}}\t{{.Names}}")
	if err != nil {
		return // docker missing/unreachable/timed out — nothing to do
	}
	now := time.Now()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 {
			continue
		}
		id, name := fields[0], fields[1]
		if !isActContainerName(name) {
			continue
		}
		ts, timedOut, err := runDocker("inspect", "-f", "{{.State.StartedAt}}", id)
		if timedOut {
			return // docker daemon unresponsive — bail; retry next cycle
		}
		if err != nil {
			continue
		}
		started, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(ts)))
		if err != nil {
			continue
		}
		if shouldReapLeakedContainer(started, now) {
			d.logger.Printf("main_branch_test: reaping leaked act container %s (%s, up %s)",
				name, id[:min(12, len(id))], now.Sub(started).Round(time.Minute))
			rmCtx, cancel := context.WithTimeout(d.ctx, reapDockerTimeout)
			_ = exec.CommandContext(rmCtx, "docker", "rm", "-f", id).Run()
			cancel()
		}
	}
}

// contains checks if a string slice contains a value.
func sliceContains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

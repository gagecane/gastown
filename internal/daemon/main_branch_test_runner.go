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

	timeout := mainBranchTestTimeout(d.patrolConfig)
	flakeThreshold := mainBranchTestFlakeThreshold(d.patrolConfig)

	var tested, failed int
	var failures []string

	for _, rigName := range rigNames {
		rigPath := filepath.Join(d.config.TownRoot, rigName)
		currentSHA, runErr := d.testRigMainBranch(rigName, rigPath, timeout)

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
		shouldEscalate, streak := recordFailureAndShouldEscalate(d.config.TownRoot, rigName, sig, flakeThreshold, now)
		if shouldEscalate {
			d.logger.Printf("main_branch_test: %s: FAILED (streak=%d, signature=%s) — escalating: %v", rigName, streak, sig, runErr)
			failures = append(failures, formatRigFailureSection(rigName, currentSHA, d.config.TownRoot, runErr))
			failed++
		} else {
			d.logger.Printf("main_branch_test: %s: FAILED (streak=%d/%d, signature=%s) — below flake watermark or already paged, not escalating: %v",
				rigName, streak, flakeThreshold, sig, runErr)
		}
		tested++
	}

	if len(failures) > 0 {
		msg := fmt.Sprintf("main branch test failures:\n%s", strings.Join(failures, "\n"))
		d.logger.Printf("main_branch_test: escalating %d failure(s)", len(failures))
		d.escalate("main_branch_test", msg)
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

// testRigMainBranch tests a single rig's main branch. Returns the
// origin/<default_branch> SHA the patrol ran against (empty when the SHA
// could not be captured — typically because the bare repo or fetch step
// failed before rev-parse) and the gate-suite outcome.
//
// The SHA is returned regardless of pass/fail so callers can: (a) emit
// `commit:` attribution on failure, and (b) update the per-rig last-passing
// baseline on success.
func (d *Daemon) testRigMainBranch(rigName, rigPath string, timeout time.Duration) (string, error) {
	// Load gate config from the rig's config.json
	gateCfg, err := loadRigGateConfig(rigPath)
	if err != nil {
		return "", fmt.Errorf("loading gate config: %w", err)
	}
	if gateCfg == nil {
		d.logger.Printf("main_branch_test: %s: no test commands configured, skipping", rigName)
		return "", nil
	}

	// Determine default branch
	defaultBranch := "main"
	if rigCfg, err := rig.LoadRigConfig(rigPath); err == nil && rigCfg.DefaultBranch != "" {
		defaultBranch = rigCfg.DefaultBranch
	}

	// Create a temporary worktree for testing to avoid interfering with
	// the refinery's working directory.
	worktreePath := filepath.Join(rigPath, ".main-test-worktree")
	bareRepoPath := filepath.Join(rigPath, ".repo.git")

	// Verify bare repo exists
	if _, err := os.Stat(bareRepoPath); os.IsNotExist(err) {
		return "", fmt.Errorf("bare repo not found at %s", bareRepoPath)
	}

	// Clean up stale worktree if it exists
	if _, err := os.Stat(worktreePath); err == nil {
		cleanupCmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
		cleanupCmd.Dir = bareRepoPath
		util.SetDetachedProcessGroup(cleanupCmd)
		_ = cleanupCmd.Run()
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

	// Create temporary worktree at origin/<default_branch>
	addCmd := exec.CommandContext(ctx, "git", "worktree", "add", "--detach", worktreePath, "origin/"+defaultBranch)
	addCmd.Dir = bareRepoPath
	util.SetDetachedProcessGroup(addCmd)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return currentSHA, fmt.Errorf("git worktree add failed: %v (%s)", err, strings.TrimSpace(string(output)))
	}

	// Always clean up the worktree
	defer func() {
		removeCmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
		removeCmd.Dir = bareRepoPath
		util.SetDetachedProcessGroup(removeCmd)
		if err := removeCmd.Run(); err != nil {
			d.logger.Printf("main_branch_test: %s: warning: worktree cleanup failed: %v", rigName, err)
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

// gateEnv builds the subprocess environment for gate commands.
// PATH is augmented with common user tool directories so commands like yarn,
// act, etc. are discoverable even when the daemon runs with a minimal
// inherited PATH (e.g. when started as a background service).
func gateEnv(townRoot string) []string {
	home, _ := os.UserHomeDir()
	dirs := []string{
		filepath.Join(townRoot, "bin"),
		filepath.Join(home, "go", "bin"),
		filepath.Join(home, ".local", "bin"),
		"/usr/local/bin",
	}
	enriched := strings.Join(append(dirs, os.Getenv("PATH")), string(os.PathListSeparator))
	return append(os.Environ(), "CI=true", "PATH="+enriched)
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

// contains checks if a string slice contains a value.
func sliceContains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

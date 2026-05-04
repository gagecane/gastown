package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/util"
)

const (
	defaultMainBranchTestInterval = 30 * time.Minute
	defaultMainBranchTestTimeout  = 10 * time.Minute
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

// mainBranchTestRigs returns the configured rig filter, or nil (all rigs).
func mainBranchTestRigs(config *DaemonPatrolConfig) []string {
	if config != nil && config.Patrols != nil && config.Patrols.MainBranchTest != nil {
		return config.Patrols.MainBranchTest.Rigs
	}
	return nil
}

// rigGate captures a single merge_queue gate's executable command and its
// lifecycle phase. Phase mirrors refinery's GatePhase ("pre-merge" or
// "post-squash"); empty string means "pre-merge" per refinery's default.
type rigGate struct {
	Cmd   string
	Phase string // "" or "pre-merge" = pre-merge (default), "post-squash" = post-squash
}

// rigGateConfig holds the gate/test configuration extracted from a rig's config.json.
type rigGateConfig struct {
	TestCommand  string
	SetupCommand string             // Optional pre-build install command (e.g., "pnpm install")
	Gates        map[string]rigGate // gate name → cmd + phase
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
		TestCommand  *string                    `json:"test_command"`
		SetupCommand *string                    `json:"setup_command"`
		Gates        map[string]json.RawMessage `json:"gates"`
	}
	if err := json.Unmarshal(raw.MergeQueue, &mq); err != nil {
		return nil, fmt.Errorf("parsing merge_queue: %w", err)
	}

	cfg := &rigGateConfig{}

	// Extract gates (preferred over legacy test_command)
	if len(mq.Gates) > 0 {
		cfg.Gates = make(map[string]rigGate, len(mq.Gates))
		for name, rawGate := range mq.Gates {
			var gate struct {
				Cmd   string `json:"cmd"`
				Phase string `json:"phase"`
			}
			if err := json.Unmarshal(rawGate, &gate); err == nil && gate.Cmd != "" {
				cfg.Gates[name] = rigGate{Cmd: gate.Cmd, Phase: gate.Phase}
			}
		}
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

	var tested, failed int
	var failures []string

	for _, rigName := range rigNames {
		rigPath := filepath.Join(d.config.TownRoot, rigName)
		if err := d.testRigMainBranch(rigName, rigPath, timeout); err != nil {
			d.logger.Printf("main_branch_test: %s: FAILED: %v", rigName, err)
			failures = append(failures, fmt.Sprintf("%s: %v", rigName, err))
			failed++
		} else {
			d.logger.Printf("main_branch_test: %s: passed", rigName)
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

// testRigMainBranch tests a single rig's main branch.
func (d *Daemon) testRigMainBranch(rigName, rigPath string, timeout time.Duration) error {
	// Load gate config from the rig's config.json
	gateCfg, err := loadRigGateConfig(rigPath)
	if err != nil {
		return fmt.Errorf("loading gate config: %w", err)
	}
	if gateCfg == nil {
		d.logger.Printf("main_branch_test: %s: no test commands configured, skipping", rigName)
		return nil
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
		return fmt.Errorf("bare repo not found at %s", bareRepoPath)
	}

	// Clean up stale worktree if it exists
	if _, err := os.Stat(worktreePath); err == nil {
		cleanupCmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
		cleanupCmd.Dir = bareRepoPath
		util.SetDetachedProcessGroup(cleanupCmd)
		_ = cleanupCmd.Run()
	}

	ctx, cancel := context.WithTimeout(d.ctx, timeout)
	defer cancel()

	// Fetch latest main
	fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin", defaultBranch)
	fetchCmd.Dir = bareRepoPath
	util.SetDetachedProcessGroup(fetchCmd)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch failed: %v (%s)", err, strings.TrimSpace(string(output)))
	}

	// Create temporary worktree at origin/<default_branch>
	addCmd := exec.CommandContext(ctx, "git", "worktree", "add", "--detach", worktreePath, "origin/"+defaultBranch)
	addCmd.Dir = bareRepoPath
	util.SetDetachedProcessGroup(addCmd)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add failed: %v (%s)", err, strings.TrimSpace(string(output)))
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
		return err
	}

	// Run gates or legacy test command
	if len(gateCfg.Gates) > 0 {
		return d.runGatesOnWorktree(ctx, rigName, worktreePath, gateCfg.Gates)
	}
	return d.runCommandOnWorktree(ctx, rigName, worktreePath, "test", gateCfg.TestCommand)
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

// runGatesOnWorktree runs all pre-merge gates sequentially on the given worktree.
//
// Post-squash gates are skipped: they are defined to run on the squash-merged
// result in refinery's pipeline and frequently rely on ambient state
// (checked-out brazil workspace, merged version-set) that a cold origin/main
// worktree lacks. Running them in main_branch_test produces spurious
// escalations that obscure real regressions. See gu-j1f7.
func (d *Daemon) runGatesOnWorktree(ctx context.Context, rigName, workDir string, gates map[string]rigGate) error {
	var failures []string
	var skipped []string
	for name, gc := range gates {
		phase := gc.Phase
		if phase == "" {
			phase = "pre-merge"
		}
		if phase != mainBranchTestPhase {
			skipped = append(skipped, fmt.Sprintf("%s(%s)", name, phase))
			continue
		}
		if err := d.runCommandOnWorktree(ctx, rigName, workDir, name, gc.Cmd); err != nil {
			failures = append(failures, fmt.Sprintf("gate %q: %v", name, err))
		}
	}
	if len(skipped) > 0 {
		d.logger.Printf("main_branch_test: %s: skipped non-pre-merge gates: %s",
			rigName, strings.Join(skipped, ", "))
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
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

// runCommandOnWorktree runs a single shell command in the given worktree directory.
func (d *Daemon) runCommandOnWorktree(ctx context.Context, rigName, workDir, label, command string) error {
	d.logger.Printf("main_branch_test: %s: running %s: %s", rigName, label, command)

	cmd := exec.CommandContext(ctx, "sh", "-c", command) //nolint:gosec // G204: command is from trusted rig config
	cmd.Dir = workDir
	cmd.Env = gateEnv(d.config.TownRoot)
	util.SetDetachedProcessGroup(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Truncate output to last 50 lines for the error message
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		tail := lines
		if len(tail) > 50 {
			tail = tail[len(tail)-50:]
		}
		return fmt.Errorf("%s failed: %v\n%s", label, err, strings.Join(tail, "\n"))
	}
	return nil
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

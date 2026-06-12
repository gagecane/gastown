package completion

// Pre-verification gate enforcement (gu-xp5f).
//
// `gt done --pre-verified` is an attestation that the polecat ran the rig's
// pre-merge gate suite on its rebased branch and observed all gates green.
// The refinery uses this attestation to fast-path-merge without re-running
// gates (engineer.go: skipGates branch in ProcessMRInfo). The pre-push hook
// is also skipped on a --pre-verified push (pushForDone wires GT_SKIP_PREPUSH=1)
// so the witness idle timeout doesn't fire mid-push (gu-d416).
//
// Trust bypass before this guard: nothing inside `gt done` re-ran the gates.
// A polecat (LLM agent) that observed a red gate in step 6/7 of the
// mol-polecat-work formula could rationalize "X is also failing on mainline,
// not my fault" and submit with --pre-verified anyway. The bypass took the
// red gate out of every downstream check and silently shipped a (potentially
// regressing) branch into the merge queue's fast path. The benign instance
// that surfaced this hole is documented in the bead: ta-g0amz.7 / gu-xp5f.
//
// What this file does:
//   - Re-runs the rig's configured pre-merge gates (merge_queue.gates with
//     phase=="pre-merge" or empty) inside `gt done`, after the auto-rebase
//     onto target and before the branch is pushed.
//   - On the first gate failure: returns ok=false, leaving the caller to
//     drop the pre-verified attestation. The branch is NOT rejected — the
//     polecat's commits still get pushed and an MR bead still gets created.
//     The refinery falls back to its normal gate run, which is the correct
//     authority for deciding what to do with a red gate (matches the
//     gs-4bn auto-rebase invalidation pattern).
//   - On a clean run or when no pre-merge gates are configured: returns
//     ok=true, attestation stays.
//
// We deliberately do NOT fail the polecat on a red gate. Failing here would
// strand the polecat's work locally and require an escalate cycle — strictly
// worse than letting the refinery process the MR with full gates. The point
// of this guard is to remove the *fast-path* trust, not to add a new
// failure mode.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/lock"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/util"
)

// preVerifyGate is a single pre-merge gate command resolved from rig settings.
type preVerifyGate struct {
	name    string
	cmd     string
	timeout time.Duration
}

// loadPreVerifyGates returns the pre-merge gates configured on the rig,
// merging repo defaults with rig-local overrides (local wins by name).
// Post-squash gates are excluded — those run on the merged stack inside
// the refinery, not on the polecat's branch.
//
// Returns an empty slice if the rig has no gates configured. Callers should
// treat that as "no verification possible, attestation stays".
func loadPreVerifyGates(townRoot, rigName string) []preVerifyGate {
	if townRoot == "" || rigName == "" {
		return nil
	}

	merged := make(map[string]*config.GateConfig)

	// Repo defaults (committed to git)
	repoRoot := filepath.Join(townRoot, rigName, "mayor", "rig")
	if repoSettings, err := config.LoadRepoSettings(repoRoot); err == nil &&
		repoSettings != nil && repoSettings.MergeQueue != nil {
		for name, gate := range repoSettings.MergeQueue.Gates {
			merged[name] = gate
		}
	}

	// Rig-local overrides (operator tuning)
	settingsPath := filepath.Join(townRoot, rigName, "settings", "config.json")
	if localSettings, err := config.LoadRigSettings(settingsPath); err == nil &&
		localSettings != nil && localSettings.MergeQueue != nil {
		for name, gate := range localSettings.MergeQueue.Gates {
			merged[name] = gate
		}
	}

	if len(merged) == 0 {
		return nil
	}

	gates := make([]preVerifyGate, 0, len(merged))
	for name, gate := range merged {
		if gate == nil {
			continue
		}
		// Treat empty phase as "pre-merge" (matches engineer.go semantics).
		if gate.Phase != "" && gate.Phase != "pre-merge" {
			continue
		}
		if strings.TrimSpace(gate.Cmd) == "" {
			continue
		}
		// Parse timeout string ("30s", "5m"); zero on error means "no timeout".
		var to time.Duration
		if gate.Timeout != "" {
			if parsed, err := time.ParseDuration(gate.Timeout); err == nil {
				to = parsed
			}
		}
		gates = append(gates, preVerifyGate{name: name, cmd: gate.Cmd, timeout: to})
	}

	// Deterministic ordering matches gates_commands generation in
	// loadRigCommandVars so the polecat sees the same sequence twice.
	sort.Slice(gates, func(i, j int) bool { return gates[i].name < gates[j].name })

	return gates
}

// preVerifyGateRunner is the subset of os/exec needed by runPreVerifyGates,
// indirected so tests can stub command execution. Real runs use execGate;
// tests inject a fake.
type preVerifyGateRunner func(ctx context.Context, workDir, cmd string) ([]byte, error)

// gateWaitDelay bounds how long Run() may block AFTER the gate's context is
// canceled, as belt-and-suspenders behind the process-group SIGKILL installed
// by util.SetProcessGroup. See execGate for the deadlock this prevents.
const gateWaitDelay = 2 * time.Second

// defaultGateTimeout caps a pre-merge gate that has no explicit timeout
// configured in rig settings. Generous enough for a full `go test ./...`
// (engineer.go gates run in the minutes), but finite so a wedged subprocess
// can never hang gt done forever (gc-utizk7). Rigs that need longer should set
// merge_queue.gates[].timeout explicitly.
const defaultGateTimeout = 10 * time.Minute

// execGate runs a shell command in workDir and returns combined stderr/stdout.
//
// Deadlock note (gc-utizk7): a gate command is `sh -c "go test ./..."` (etc.),
// and the shell forks compiler/test children that inherit the stdout/stderr
// pipe write end. The earlier implementation used exec.CommandContext +
// CombinedOutput with no process-group kill: on the gate timeout,
// CommandContext SIGKILLs ONLY the sh leader; the children reparent to PID 1
// and keep the pipe write end open, so CombinedOutput's internal Wait() blocks
// FOREVER in futex_wait_queue (the fd7/fd9 read-pipe-no-writer signature with
// zero children). This is the same os/exec inherited-pipe class fixed for the
// git push helpers in a0a7bdb9 (gu-4mj2 precedent) — on an uncovered call site.
//
// Fix: own the stdout/stderr buffers (so we control the pipe), use
// util.SetProcessGroup so the Cancel hook SIGKILLs the WHOLE process group on
// timeout (Setpgid survives reparenting, so the reparented children die and the
// pipe write end closes), plus WaitDelay to bound any residual pipe I/O. We do
// NOT mirror engineer.runGate here: that path still uses SetDetachedProcessGroup
// (no Cancel hook) and is subject to the same bug.
func execGate(ctx context.Context, workDir, cmd string) ([]byte, error) {
	c := exec.CommandContext(ctx, "sh", "-c", cmd) //nolint:gosec // G204: gates from trusted rig config
	c.Dir = workDir
	// Route TMPDIR/GOTMPDIR off any small /tmp tmpfs onto disk-backed storage so a
	// gate's `go test ./...` link step can't fail with ENOSPC when concurrent
	// full-suite gate runs fill /tmp (gu-l4aue). nil leaves env inherited.
	c.Env = util.WithGateTmpEnv(os.Environ())
	util.SetProcessGroup(c)
	c.WaitDelay = gateWaitDelay
	// Combine stdout+stderr into one buffer (preserving CombinedOutput's
	// contract) but with buffers we own so the timeout-kill above can unblock us.
	var combined bytes.Buffer
	c.Stdout = &combined
	c.Stderr = &combined
	err := c.Run()
	return combined.Bytes(), err
}

// runPreVerifyGates executes each gate in order, stopping on first failure.
// Returns:
//   - ok=true when every gate exited 0 (or when there are no gates).
//   - ok=false plus a non-nil failure summary when any gate fails or the
//     timeout fires. The error message is suitable for surfacing on stderr;
//     it intentionally truncates command output so a noisy test failure
//     doesn't blow out the polecat's terminal.
//
// runPreVerifyGates does NOT print anything; the caller decides how to
// surface progress and failure (so tests can assert on return values
// without competing with stdout).
func runPreVerifyGates(ctx context.Context, workDir string, gates []preVerifyGate, run preVerifyGateRunner) (bool, error) {
	if run == nil {
		run = execGate
	}
	for _, gate := range gates {
		// Always bound the gate: a gate with no configured timeout would
		// otherwise inherit ctx (context.Background() from gt done) and could
		// hang unbounded if its subprocess wedges — the second half of the
		// gc-utizk7 deadlock. Fall back to defaultGateTimeout when unset.
		timeout := gate.timeout
		if timeout <= 0 {
			timeout = defaultGateTimeout
		}
		gateCtx, cancel := context.WithTimeout(ctx, timeout)
		out, err := run(gateCtx, workDir, gate.cmd)
		cancel()
		if err == nil {
			continue
		}
		// Truncate output to keep error messages bounded.
		excerpt := strings.TrimSpace(string(out))
		const maxExcerpt = 800
		if len(excerpt) > maxExcerpt {
			excerpt = excerpt[:maxExcerpt] + "...(truncated)"
		}
		summary := fmt.Sprintf("gate %q failed: %v", gate.name, err)
		if excerpt != "" {
			summary = fmt.Sprintf("%s\n%s", summary, excerpt)
		}
		return false, fmt.Errorf("%s", summary)
	}
	return true, nil
}

// VerifyPreVerifiedAttestation is the high-level entry point used by
// runDone. It runs pre-merge gates locally to validate the polecat's
// --pre-verified claim. Returns:
//   - keep=true: attestation stays valid; refinery may fast-path.
//   - keep=false: attestation must be dropped; refinery should run gates.
//
// On gate failure, this function prints a warning explaining the downgrade
// but does NOT return an error — the caller continues with submission so
// the polecat's work isn't stranded.
//
// When the rig has no pre-merge gates configured, keep=true (nothing to
// verify, attestation stays — same behavior as before this guard).
func VerifyPreVerifiedAttestation(ctx context.Context, townRoot, rigName, workDir string) bool {
	gates := loadPreVerifyGates(townRoot, rigName)
	if len(gates) == 0 {
		// No gates configured to verify against. Keep attestation — refinery's
		// fast-path will also be a no-op since it shares the same config.
		return true
	}

	fmt.Printf("%s Verifying --pre-verified attestation: running %d pre-merge gate(s)\n",
		style.Bold.Render("→"), len(gates))

	// gu-0x2be: keep the session heartbeat fresh while the gate runs. gt done
	// wrote a one-shot state="exiting" heartbeat before entering this path; with
	// nothing refreshing it the heartbeat goes stale after 3min
	// (SessionHeartbeatStaleThreshold), yet a real pre-merge gate (e.g.
	// `brazil-build release`) legitimately runs 10-15min. When the exiting
	// heartbeat goes stale the witness falls through to its legacy
	// done-intent-age path and RESTARTS the polecat mid-gate — discarding the
	// in-progress gate run plus the whole session context. The keepalive ticker
	// bumps the heartbeat on its cadence (preserving the exiting state via
	// KeepaliveWithOp) so the witness's fresh-exiting guard keeps treating the
	// polecat as healthily grinding, not stuck. If gt done actually dies the
	// ticker stops and the heartbeat correctly goes stale. Mirrors the
	// witness's own runRecoveryGates, which already wraps its gate run this way.
	defer polecat.WithKeepalive(townRoot, os.Getenv("GT_SESSION"), "pre-verify-gate", polecat.DefaultKeepaliveInterval)()

	// Cap concurrent full-suite gate runs host-wide (gu-0iyrn). Each gate run
	// (`go test ./...`) burns 110-198% CPU; under bulk completion several fire
	// near-together and spike host load avg to 19-25, starving the dispatch
	// heartbeat. A cross-process counting semaphore bounds how many run at once.
	// Acquiring the slot is best-effort: on timeout or error we proceed
	// unthrottled rather than strand the polecat's submission.
	if release := acquireGateSlot(townRoot); release != nil {
		defer release()
	}

	ok, err := runPreVerifyGates(ctx, workDir, gates, nil)
	if ok {
		fmt.Printf("%s All pre-merge gates passed — attestation valid\n", style.Bold.Render("✓"))
		return true
	}

	style.PrintWarning("--pre-verified attestation failed local verification (gu-xp5f); dropping attestation")
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "  The branch will still be submitted; the refinery will re-run gates normally.\n")
	fmt.Fprintf(os.Stderr, "  If the failure is real, refinery will reject; fix the regression and resubmit.\n")
	return false
}

const (
	// defaultGateConcurrency caps host-wide concurrent full-suite gate runs.
	// 2 keeps load avg sane while letting two batches drain in parallel.
	defaultGateConcurrency = 2
	// gateSlotWaitTimeout bounds how long we wait for a free slot before
	// proceeding unthrottled. Generous: a full `go test ./...` can take
	// minutes, and we'd rather queue than skip the cap under bulk load.
	gateSlotWaitTimeout = 10 * time.Minute
)

// resolveGateConcurrency returns the host-wide cap on concurrent gate runs,
// honoring GT_GATE_CONCURRENCY (positive integer) and falling back to the
// default otherwise.
func resolveGateConcurrency() int {
	if v := os.Getenv("GT_GATE_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultGateConcurrency
}

// acquireGateSlot takes a slot from the host-wide gate-run semaphore (gu-0iyrn).
// Returns a release func on success, or nil if no town root is known, the slot
// could not be acquired within the timeout, or the semaphore dir is unusable.
// Callers proceed unthrottled when nil is returned — the cap is an optimization,
// not a correctness gate.
func acquireGateSlot(townRoot string) func() {
	if townRoot == "" {
		return nil
	}
	slotDir := filepath.Join(townRoot, ".runtime", "locks", "gate-slots")
	sem := lock.NewFlockSemaphore(slotDir, resolveGateConcurrency())
	release, err := sem.Acquire(gateSlotWaitTimeout)
	if err != nil {
		// Timed out or dir error — don't strand the submission, just run.
		return nil
	}
	return release
}

package cmd

// The `gt prime` command entry point and core orchestration.
//
// This file owns the cobra command definition, flag parsing, workspace/role
// resolution, the main runPrime() control flow, and the small set of helpers
// that wire the pieces together. Heavier concerns live in sibling files:
//
//   prime_session.go     — session ID, hook JSON, handoff markers, state detection
//   prime_output.go      — role-specific prime context and startup directives
//   prime_molecule.go    — formula rendering and patrol context
//   prime_identity.go    — agent identity, locking, agent bead ID resolution
//   prime_work.go        — hooked work discovery and work-context injection
//   prime_autonomous.go  — AUTONOMOUS WORK MODE output (hooked-bead directive)
//   prime_external.go    — bd prime, memory injection, mail check injection
//   prime_escalation.go  — Mayor escalation display

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/cli"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/state"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

var primeHookMode bool
var primeDryRun bool
var primeState bool
var primeStateJSON bool
var primeExplain bool
var primeStructuredSessionStartOutput bool

// primeHookSource stores the SessionStart source ("startup", "resume", "clear", "compact")
// when running in hook mode. Used to provide lighter output on compaction/resume.
var primeHookSource string

// primeHandoffReason stores the reason from the handoff marker (e.g., "compaction").
// Set by checkHandoffMarker when a marker with a reason field is found.
var primeHandoffReason string

// Role represents a detected agent role.
type Role string

const (
	RoleMayor    Role = "mayor"
	RoleDeacon   Role = "deacon"
	RoleBoot     Role = "boot"
	RoleWitness  Role = "witness"
	RoleRefinery Role = "refinery"
	RolePolecat  Role = "polecat"
	RoleCrew     Role = "crew"
	RoleDog      Role = "dog"
	RoleUnknown  Role = "unknown"
)

var primeCmd = &cobra.Command{
	Use:         "prime",
	GroupID:     GroupDiag,
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Output role context for current directory",
	Long: `Detect the agent role from the current directory and output context.

Role detection:
  - Town root → Neutral (no role inferred; use GT_ROLE)
  - mayor/ or <rig>/mayor/ → Mayor context
  - <rig>/witness/rig/ → Witness context
  - <rig>/refinery/rig/ → Refinery context
  - <rig>/polecats/<name>/ → Polecat context

This command is typically used in shell prompts or agent initialization.

HOOK MODE (--hook):
  When called as an LLM runtime hook, use --hook to enable session ID handling,
  agent-ready signaling, and session persistence.

  Session ID resolution (first match wins):
    1. GT_SESSION_ID env var
    2. CLAUDE_SESSION_ID env var
    3. Persisted .runtime/session_id (from prior SessionStart)
    4. Stdin JSON (Claude Code format)
    5. Auto-generated UUID

  Source resolution: GT_HOOK_SOURCE env var, then stdin JSON "source" field.

  Claude Code integration (in .claude/settings.json):
    "SessionStart": [{"hooks": [{"type": "command", "command": "gt prime --hook"}]}]
    Claude sends JSON on stdin: {"session_id":"uuid","source":"startup|resume|compact"}

  Gemini CLI / other runtimes (in .gemini/settings.json):
    "SessionStart": "export GT_SESSION_ID=$(uuidgen) GT_HOOK_SOURCE=startup && gt prime --hook"
    "PreCompress":  "export GT_HOOK_SOURCE=compact && gt prime --hook"
    Set GT_SESSION_ID + GT_HOOK_SOURCE as env vars to skip the stdin read entirely.`,
	RunE: runPrime,
}

func init() {
	primeCmd.Flags().BoolVar(&primeHookMode, "hook", false,
		"Hook mode: read session ID from stdin JSON (for LLM runtime hooks)")
	primeCmd.Flags().BoolVar(&primeDryRun, "dry-run", false,
		"Show what would be injected without side effects (no marker removal, no bd prime, no mail)")
	primeCmd.Flags().BoolVar(&primeState, "state", false,
		"Show detected session state only (normal/post-handoff/crash/autonomous)")
	primeCmd.Flags().BoolVar(&primeStateJSON, "json", false,
		"Output state as JSON (requires --state)")
	primeCmd.Flags().BoolVar(&primeExplain, "explain", false,
		"Show why each section was included")
	rootCmd.AddCommand(primeCmd)
}

// RoleContext is an alias for RoleInfo for backward compatibility.
// New code should use RoleInfo directly.
type RoleContext = RoleInfo

func runPrime(cmd *cobra.Command, args []string) (retErr error) {
	defer func() { telemetry.RecordPrime(context.Background(), os.Getenv("GT_ROLE"), primeHookMode, retErr) }()
	if err := validatePrimeFlags(); err != nil {
		return err
	}

	cwd, townRoot, err := resolvePrimeWorkspace()
	if err != nil {
		return err
	}
	if townRoot == "" {
		return nil // Silent exit - not in workspace and not enabled
	}

	if primeHookMode {
		handlePrimeHookMode(townRoot, cwd)
	}

	// Check for handoff marker (prevents handoff loop bug)
	if primeDryRun {
		checkHandoffMarkerDryRun(cwd)
	} else {
		checkHandoffMarker(cwd)
	}

	roleInfo, err := GetRoleWithContext(cwd, townRoot)
	if err != nil {
		return fmt.Errorf("detecting role: %w", err)
	}

	warnRoleMismatch(roleInfo, cwd)

	ctx := RoleContext{
		Role:     roleInfo.Role,
		Rig:      roleInfo.Rig,
		Polecat:  roleInfo.Polecat,
		TownRoot: townRoot,
		WorkDir:  cwd,
	}

	// --state mode: output state only and exit
	if primeState {
		outputState(ctx, primeStateJSON)
		return nil
	}

	// Compact/resume: fast path that skips setupPrimeSession and the
	// retry-heavy findAgentWork. The agent already has role context and
	// work state in compressed memory — just confirm identity and inject
	// any new mail. This keeps PreCompress hooks under 1s for non-Claude
	// runtimes that have short hook timeouts (Gemini CLI).
	if isCompactResume() {
		runPrimeCompactResume(ctx)
		return nil
	}

	if err := setupPrimeSession(ctx, roleInfo); err != nil {
		return err
	}

	// P0: Fetch work context once — used for both OTel attribution and output.
	// injectWorkContext sets GT_WORK_RIG/BEAD/MOL in the current process env and
	// in the tmux session env so all subsequent subprocesses (bd, mail, …) carry
	// the correct work attribution until the next gt prime overwrites it.
	hookedBead, hookErr := findAgentWork(ctx)
	if hookErr != nil {
		// Database error during hook query — NOT the same as "no work assigned".
		// Emit a loud warning so the agent does NOT run gt done / close the bead.
		// This prevents the destructive cycle: DB error → "no work" → gt done → bead lost. (GH#2638)
		fmt.Fprintf(os.Stderr, "\n%s\n", style.Bold.Render("## ⚠️  DATABASE ERROR — DO NOT RUN gt done ⚠️"))
		fmt.Fprintf(os.Stderr, "Hook query failed: %v\n", hookErr)
		fmt.Fprintf(os.Stderr, "This is a database connectivity error, NOT an empty hook.\n")
		fmt.Fprintf(os.Stderr, "Your work may still be assigned. Do NOT close any beads.\n")
		fmt.Fprintf(os.Stderr, "Escalate to witness/mayor and wait for resolution.\n\n")
	}
	injectWorkContext(ctx, hookedBead)

	formula, err := outputRoleContext(ctx)
	if err != nil {
		return err
	}
	// Log the rendered formula to OTEL so it's visible in VictoriaLogs alongside
	// Claude's API calls, letting operators see exactly what context each agent
	// started with. Only emitted when GT telemetry is active (GT_OTEL_LOGS_URL set).
	telemetry.RecordPrimeContext(context.Background(), formula, os.Getenv("GT_ROLE"), primeHookMode)

	hasSlungWork := checkSlungWork(ctx, hookedBead)
	explain(hasSlungWork, "Autonomous mode: hooked/in-progress work detected")

	outputMoleculeContext(ctx)
	outputCheckpointContext(ctx)
	runPrimeExternalTools(cwd)

	if ctx.Role == RoleMayor {
		checkPendingEscalations(ctx)
	}

	if !hasSlungWork {
		explain(true, "Startup directive: normal mode (no hooked work)")
		outputStartupDirective(ctx)
	}

	return nil
}

// runPrimeCompactResume runs a lighter prime after compaction or resume.
// The agent already has full role context in compressed memory. This just
// restores identity and injects any new mail. It deliberately skips
// setupPrimeSession and findAgentWork (which hit Dolt) to stay fast
// enough for non-Claude runtimes with short hook timeouts.
//
// Unlike the full prime path, this outputs a brief recovery line instead of
// the full AUTONOMOUS WORK MODE block. This prevents agents from re-announcing
// and re-initializing after compaction. (GH#1965)
func runPrimeCompactResume(ctx RoleContext) {
	// Brief identity confirmation
	actor := getAgentIdentity(ctx)
	source := primeHookSource
	if source == "" && primeHandoffReason != "" {
		source = "handoff-" + primeHandoffReason
	}
	fmt.Printf("\n> **Recovery**: Context %s complete. You are **%s** (%s).\n",
		source, actor, ctx.Role)

	// Session metadata for seance
	outputSessionMetadata(ctx)

	fmt.Println("\n---")
	fmt.Println()
	fmt.Println("**Continue your current task.** If you've lost context, run `gt prime` for full reload.")

	// Remind polecats about gt done — after compaction the agent may have lost
	// the formula checklist and forgotten that gt done is required to submit work.
	// Without this, polecats finish implementation and sit at the prompt forever.
	if ctx.Role == RolePolecat {
		fmt.Printf("\n**IMPORTANT**: When all work is complete (code committed, tests pass), run `%s done` to submit to the merge queue.\n", cli.Name())
	}
}

// validatePrimeFlags checks that CLI flag combinations are valid.
func validatePrimeFlags() error {
	if primeState && (primeHookMode || primeDryRun || primeExplain) {
		return fmt.Errorf("--state cannot be combined with other flags (except --json)")
	}
	if primeStateJSON && !primeState {
		return fmt.Errorf("--json requires --state")
	}
	return nil
}

// resolvePrimeWorkspace finds the cwd and town root for prime.
// Returns empty townRoot (not an error) when not in a workspace and not enabled.
func resolvePrimeWorkspace() (cwd, townRoot string, err error) {
	cwd, err = os.Getwd()
	if err != nil {
		return "", "", fmt.Errorf("getting current directory: %w", err)
	}

	townRoot, err = workspace.FindFromCwd()
	if err != nil {
		return "", "", fmt.Errorf("finding workspace: %w", err)
	}

	// "Discover, Don't Track" principle:
	// If in a workspace, proceed. If not, check global enabled state.
	if townRoot == "" {
		if !state.IsEnabled() {
			return cwd, "", nil // Signal caller to exit silently
		}
		return "", "", fmt.Errorf("not in a Gas Town workspace")
	}

	return cwd, townRoot, nil
}

// handlePrimeHookMode reads session ID from stdin and persists it.
// Called when --hook flag is set for LLM runtime hook integration.
func handlePrimeHookMode(townRoot, cwd string) {
	sessionID, source := readHookSessionID()
	if !primeDryRun {
		persistSessionID(townRoot, sessionID)
		if cwd != townRoot {
			persistSessionID(cwd, sessionID)
		}
	}
	_ = os.Setenv("GT_SESSION_ID", sessionID)
	_ = os.Setenv("CLAUDE_SESSION_ID", sessionID) // Legacy compatibility

	// ZFC: Signal agent readiness via tmux env var (gt-sk5u).
	// WaitForCommand polls for this instead of probing the process tree.
	// This handles agents wrapped in shell scripts where pane_current_command
	// remains "bash" even though the agent is running as a descendant.
	signalAgentReady()

	// Store source for compact/resume detection in runPrime
	primeHookSource = source

	explain(true, "Session beacon: hook mode enabled, session ID from stdin")
	for _, line := range hookSessionBeaconLines(sessionID, source) {
		fmt.Println(line)
	}
}

// hookSessionBeaconLines returns the bracketed session/source markers used by
// the normal hook path. Structured SessionStart output skips them because Codex
// tries to auto-detect JSON, sees the leading '[', and misclassifies the startup
// stream as JSON instead of plain text metadata.
func hookSessionBeaconLines(sessionID, source string) []string {
	if primeStructuredSessionStartOutput {
		return nil
	}
	lines := []string{fmt.Sprintf("[session:%s]", sessionID)}
	if source != "" {
		lines = append(lines, fmt.Sprintf("[source:%s]", source))
	}
	return lines
}

// signalAgentReady sets GT_AGENT_READY=1 in the current tmux session environment.
// Called from the agent's SessionStart hook to signal that the agent has started.
// WaitForCommand polls for this variable as a ZFC-compliant alternative to
// probing the process tree via IsAgentAlive.
// Uses ResolveCurrentSession to find our session on the town socket — raw
// exec.Command("tmux", ...) would use the default socket and miss the gastown server.
func signalAgentReady() {
	t := tmux.NewTmux()
	name, err := t.ResolveCurrentSession()
	if err != nil || name == "" {
		return
	}
	_ = t.SetEnvironment(name, tmux.EnvAgentReady, "1")
}

// isCompactResume returns true if the current prime is running after compaction or resume.
// In these cases, the agent already has role context in compressed memory and only needs
// a brief identity confirmation plus hook/work status.
//
// This also returns true for compaction-triggered handoff cycles (crew workers).
// When PreCompact runs "gt handoff --cycle --reason compaction", the new session
// gets source="startup" but the handoff marker carries reason="compaction".
// Without this, the new session runs full prime with AUTONOMOUS WORK MODE,
// causing the agent to re-initialize instead of continuing. (GH#1965)
func isCompactResume() bool {
	return primeHookSource == "compact" || primeHookSource == "resume" || primeHandoffReason == "compaction"
}

// warnRoleMismatch outputs a prominent warning if GT_ROLE disagrees with cwd detection.
func warnRoleMismatch(roleInfo RoleInfo, cwd string) {
	if !roleInfo.Mismatch {
		return
	}
	fmt.Printf("\n%s\n", style.Bold.Render("⚠️  ROLE/LOCATION MISMATCH"))
	fmt.Printf("You are %s (from $GT_ROLE) but your cwd suggests %s.\n",
		style.Bold.Render(string(roleInfo.Role)),
		style.Bold.Render(string(roleInfo.CwdRole)))
	fmt.Printf("Expected home: %s\n", roleInfo.Home)
	fmt.Printf("Actual cwd:    %s\n", cwd)
	fmt.Println()
	fmt.Println("This can cause commands to misbehave. Either:")
	fmt.Println("  1. cd to your home directory, OR")
	fmt.Println("  2. Use absolute paths for gt/bd commands")
	fmt.Println()
}

// setupPrimeSession handles identity locking, beads redirect, and session events.
// Skipped entirely in dry-run mode.
func setupPrimeSession(ctx RoleContext, roleInfo RoleInfo) error {
	if primeDryRun {
		return nil
	}
	if err := acquireIdentityLock(ctx); err != nil {
		return err
	}
	if !roleInfo.Mismatch {
		ensureBeadsRedirect(ctx)
	}
	repairSessionEnv(ctx, roleInfo)
	// Only emit session_start when gt prime is running as a SessionStart or
	// PreCompact hook. Bare gt prime calls (e.g. an agent reading another
	// agent's context) must not emit session_start — doing so logs a spurious
	// event with the target agent's persisted session_id, which pollutes the
	// event stream and can confuse gt seance discovery.
	if primeHookMode {
		emitSessionEvent(ctx)
	}
	return nil
}

// repairSessionEnv checks if the tmux session is missing identity env vars
// and re-injects them from the current role context. This self-heals sessions
// that were created through non-standard paths or older gt versions. GH#3006.
func repairSessionEnv(ctx RoleContext, roleInfo RoleInfo) {
	if os.Getenv("TMUX") == "" {
		return
	}

	t := tmux.NewTmux()
	session, err := t.ResolveCurrentSession()
	if err != nil || session == "" {
		return
	}

	// Quick check: if GT_ROLE is already set in the session env, assume healthy.
	if _, err := t.GetEnvironment(session, "GT_ROLE"); err == nil {
		return
	}

	// Map prime Role type to config.AgentEnv role constant.
	var agentName string
	switch ctx.Role {
	case RoleCrew:
		agentName = roleInfo.Polecat // RoleInfo.Polecat holds crew member name too
	case RolePolecat:
		agentName = roleInfo.Polecat
	case RoleDog:
		agentName = roleInfo.Polecat
	}

	envVars := config.AgentEnv(config.AgentEnvConfig{
		Role:        string(ctx.Role),
		Rig:         ctx.Rig,
		AgentName:   agentName,
		TownRoot:    ctx.TownRoot,
		SessionName: session,
	})

	// Only inject identity-related vars that are missing, not the full AgentEnv
	// output (which includes Dolt ports, OTEL config, etc. that may have been
	// intentionally overridden per-session).
	identitySet := make(map[string]bool, len(config.IdentityEnvVars))
	for _, k := range config.IdentityEnvVars {
		identitySet[k] = true
	}
	// Also include GT_ROOT and GT_SESSION — core session identity.
	identitySet["GT_ROOT"] = true
	identitySet["GT_SESSION"] = true

	var repaired int
	for k, v := range envVars {
		if !identitySet[k] {
			continue
		}
		if _, err := t.GetEnvironment(session, k); err == nil {
			continue // already set at session level
		}
		if err := t.SetEnvironment(session, k, v); err == nil {
			repaired++
		}
	}

	if repaired > 0 {
		fmt.Printf("\n%s Injected %d missing identity vars into session %s\n",
			style.Bold.Render("⚠️  SESSION ENV REPAIR:"), repaired, session)
		// Also set in the current process so this prime run uses the correct identity.
		for k, v := range envVars {
			if identitySet[k] {
				os.Setenv(k, v)
			}
		}
	}
}

// outputRoleContext emits session metadata and all role/context output sections.
// Returns the rendered formula content for OTEL telemetry (empty if using fallback path).
func outputRoleContext(ctx RoleContext) (string, error) {
	explain(true, "Session metadata: always included for seance discovery")
	outputSessionMetadata(ctx)

	explain(true, fmt.Sprintf("Role context: detected role is %s", ctx.Role))
	formula, err := outputPrimeContext(ctx)
	if err != nil {
		return "", err
	}

	outputRoleDirectives(ctx, os.Stdout, primeExplain)
	outputContextFile(ctx)
	outputHandoffContent(ctx)
	outputAttachmentStatus(ctx)
	return formula, nil
}

// getGitRoot returns the root of the current git repository.
//
// Defined here because it's a generic utility used across cmd/. molecule_step.go
// is a consumer; it does not re-implement it to preserve a single source of truth.
func getGitRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

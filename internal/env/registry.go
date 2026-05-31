package env

// This file is the single source of truth for every recognised GT_*
// environment variable. The list was bootstrapped from a HEAD-of-main
// inventory at commit 18da030a:
//
//	$ grep -rohE 'os\.Getenv\("GT_[A-Z_0-9]+"\)' --include='*.go' . | sort -u
//
// 90 unique names across 297 callsites.
//
// Adding a new GT_* var:
//  1. Declare it as a `const` Var below, in alphabetical order within its
//     section, with a comment explaining the value.
//  2. Register it in init() with the right Kind, default, and a one-line
//     description. Doc generators and operators will read that description.
//  3. Use String/Bool/Int/Duration at callsites — never os.Getenv directly.
//
// Kind classification rules:
//  - KindBool     — interpreted via Bool() (truthy: true/1/yes/on/t/y).
//  - KindInt      — base-10 integer (e.g. counters, port numbers, byte caps).
//  - KindDuration — Go duration string. NOTE: a few existing callsites parse
//    integer seconds (e.g. GT_DOLT_WAIT_TIMEOUT=300). Those are registered
//    here as KindInt with a "_SEC" hint in the description; per-package
//    migration polecats decide whether to keep integer-seconds or move
//    operators to Go duration strings.
//  - KindString   — everything else (hostnames, paths, role names, opaque
//    selectors). Presence-only flags (GT_DEGRADED, GT_AGENT_MODE, etc.)
//    that callers test with `!= ""` are KindString; only register as
//    KindBool if the existing convention parses the value.

// Identity / role / session.
const (
	GTAccount       Var = "GT_ACCOUNT"
	GTAgent         Var = "GT_AGENT"
	GTAgentMode     Var = "GT_AGENT_MODE"
	GTBranch        Var = "GT_BRANCH"
	GTCommand       Var = "GT_COMMAND"
	GTCostTier      Var = "GT_COST_TIER"
	GTCrew          Var = "GT_CREW"
	GTCwd           Var = "GT_CWD"
	GTDaemon        Var = "GT_DAEMON"
	GTDeacon        Var = "GT_DEACON"
	GTDogName       Var = "GT_DOG_NAME"
	GTHome          Var = "GT_HOME"
	GTHookSource    Var = "GT_HOOK_SOURCE"
	GTIssue         Var = "GT_ISSUE"
	GTMayor         Var = "GT_MAYOR"
	GTPolecat       Var = "GT_POLECAT"
	GTPolecatPath   Var = "GT_POLECAT_PATH"
	GTRefinery      Var = "GT_REFINERY"
	GTRefineryWorker Var = "GT_REFINERY_WORKER"
	GTRig           Var = "GT_RIG"
	GTRole          Var = "GT_ROLE"
	GTRoot          Var = "GT_ROOT"
	GTRun           Var = "GT_RUN"
	GTSession       Var = "GT_SESSION"
	GTSessionID     Var = "GT_SESSION_ID"
	GTSessionIDEnv  Var = "GT_SESSION_ID_ENV"
	GTTown          Var = "GT_TOWN"
	GTTownRoot      Var = "GT_TOWN_ROOT"
	GTTownSocket    Var = "GT_TOWN_SOCKET"
	GTTmuxSocket    Var = "GT_TMUX_SOCKET"
	GTWitness       Var = "GT_WITNESS"
	GTWorkBead      Var = "GT_WORK_BEAD"
	GTWorkMol       Var = "GT_WORK_MOL"
	GTWorkRig       Var = "GT_WORK_RIG"
)

// Behaviour / mode flags.
const (
	GTAllowDirectPush  Var = "GT_ALLOW_DIRECT_PUSH"
	GTDebug            Var = "GT_DEBUG"
	GTDebugSession     Var = "GT_DEBUG_SESSION"
	GTDegraded         Var = "GT_DEGRADED"
	GTFeedTUI          Var = "GT_FEED_TUI"
	GTNoEmoji          Var = "GT_NO_EMOJI"
	GTNoPager          Var = "GT_NO_PAGER"
	GTNukeAcknowledged Var = "GT_NUKE_ACKNOWLEDGED"
	GTPager            Var = "GT_PAGER"
	GTSkipPrepushReason Var = "GT_SKIP_PREPUSH_REASON"
	GTSkipVerifyReason  Var = "GT_SKIP_VERIFY_REASON"
	GTStaleWarn         Var = "GT_STALE_WARN"
	GTStaleWarned       Var = "GT_STALE_WARNED"
	GTTheme             Var = "GT_THEME"
	GTACPDebug          Var = "GT_ACP_DEBUG"
	GTEscalateDebugDedup Var = "GT_ESCALATE_DEBUG_DEDUP"
)

// Logging / observability.
const (
	GTLogAgentOutput  Var = "GT_LOG_AGENT_OUTPUT"
	GTLogBdOutput     Var = "GT_LOG_BD_OUTPUT"
	GTLogMailBody     Var = "GT_LOG_MAIL_BODY"
	GTLogPrimeContext Var = "GT_LOG_PRIME_CONTEXT"
	GTLogPromptKeys   Var = "GT_LOG_PROMPT_KEYS"
	GTOtelLogsURL     Var = "GT_OTEL_LOGS_URL"
	GTOtelMetricsURL  Var = "GT_OTEL_METRICS_URL"
)

// Beads / Dolt.
const (
	GTBdReadThrottleTimeoutSec Var = "GT_BD_READ_THROTTLE_TIMEOUT_SEC"
	GTBdTimeoutSec             Var = "GT_BD_TIMEOUT_SEC"
	GTDoltHost                 Var = "GT_DOLT_HOST"
	GTDoltIgnoreConfig         Var = "GT_DOLT_IGNORE_CONFIG"
	GTDoltLogLevel             Var = "GT_DOLT_LOGLEVEL"
	GTDoltPassword             Var = "GT_DOLT_PASSWORD"
	GTDoltPort                 Var = "GT_DOLT_PORT"
	GTDoltReapHelper           Var = "GT_DOLT_REAP_HELPER"
	GTDoltUser                 Var = "GT_DOLT_USER"
	GTDoltWaitTimeout          Var = "GT_DOLT_WAIT_TIMEOUT"
)

// Context budget / agent runtime.
const (
	GTContextBudgetHardGate  Var = "GT_CONTEXT_BUDGET_HARD_GATE"
	GTContextBudgetMaxTokens Var = "GT_CONTEXT_BUDGET_MAX_TOKENS"
	GTContextBudgetSoftGate  Var = "GT_CONTEXT_BUDGET_SOFT_GATE"
	GTContextBudgetTokens    Var = "GT_CONTEXT_BUDGET_TOKENS"
	GTContextBudgetWarn      Var = "GT_CONTEXT_BUDGET_WARN"
	GTCursorAgentBin         Var = "GT_CURSOR_AGENT_BIN"
	GTKiroMaxIterations      Var = "GT_KIRO_MAX_ITERATIONS"
)

// Proxy / networking.
const (
	GTProxyCA   Var = "GT_PROXY_CA"
	GTProxyCert Var = "GT_PROXY_CERT"
	GTProxyKey  Var = "GT_PROXY_KEY"
	GTProxyURL  Var = "GT_PROXY_URL"
	GTRealBin   Var = "GT_REAL_BIN"
)

// Process management.
const (
	GTProcessNames Var = "GT_PROCESS_NAMES"
)

// Test hooks (used by integration / e2e tests).
const (
	GTAgentStubBinDir         Var = "GT_AGENT_STUB_BIN_DIR"
	GTFakeKiroCounter         Var = "GT_FAKE_KIRO_COUNTER"
	GTFakeKiroMode            Var = "GT_FAKE_KIRO_MODE"
	GTFakeKiroSuccessOn       Var = "GT_FAKE_KIRO_SUCCESS_ON"
	GTRunOQ4Spike             Var = "GT_RUN_OQ4_SPIKE"
	GTTestAttachedMoleculeLog Var = "GT_TEST_ATTACHED_MOLECULE_LOG"
	GTTestExternalDolt        Var = "GT_TEST_EXTERNAL_DOLT"
	GTTestNoNudge             Var = "GT_TEST_NO_NUDGE"
	GTTestNudgeLog            Var = "GT_TEST_NUDGE_LOG"
	GTTestSkipHookVerify      Var = "GT_TEST_SKIP_HOOK_VERIFY"
)

func init() {
	for _, s := range allSpecs() {
		Register(s)
	}
}

// allSpecs returns every recognised GT_* var. Split out from init() so
// tests can call Reset()+Register(allSpecs()...) and so the registry is
// inspectable from a single function.
func allSpecs() []Spec {
	return []Spec{
		// Identity / role / session.
		{GTAccount, KindString, "", "AWS account id (used by mayor/orchestration helpers)."},
		{GTAgent, KindString, "", "Agent runtime selector (claude, kiro, codex, ...)."},
		{GTAgentMode, KindString, "", "Set to '1' when running under an autonomous agent runtime."},
		{GTBranch, KindString, "", "Current git branch name (set by tooling, not git itself)."},
		{GTCommand, KindString, "", "Subcommand path being executed (used for telemetry / process labels)."},
		{GTCostTier, KindString, "", "Cost tier hint for the active agent (e.g. 'cheap', 'premium')."},
		{GTCrew, KindString, "", "Crew name in town/<rig>/crew/<name> hierarchy."},
		{GTCwd, KindString, "", "Working directory recorded at session start."},
		{GTDaemon, KindBool, "", "Set to '1' when running inside the gt daemon."},
		{GTDeacon, KindString, "", "Deacon name; presence implies the deacon role."},
		{GTDogName, KindString, "", "Patrol dog name (witness/refinery/deacon background workers)."},
		{GTHome, KindString, "", "Override for the gt home directory (default: $HOME/.gt)."},
		{GTHookSource, KindString, "", "How the current hook was attached (cli, mail, formula, ...)."},
		{GTIssue, KindString, "", "Bead id of the currently-hooked issue."},
		{GTMayor, KindString, "", "Mayor name; presence implies the mayor role."},
		{GTPolecat, KindString, "", "Polecat name; presence implies the polecat role."},
		{GTPolecatPath, KindString, "", "Filesystem path of the active polecat worktree."},
		{GTRefinery, KindString, "", "Refinery name; presence implies the refinery role."},
		{GTRefineryWorker, KindString, "", "Refinery worker id within a refinery process."},
		{GTRig, KindString, "", "Rig name (e.g. gastown_upstream)."},
		{GTRole, KindString, "", "Canonical role identifier (witness, polecat, refinery, ...)."},
		{GTRoot, KindString, "", "Filesystem root for the current rig/worktree."},
		{GTRun, KindString, "", "Telemetry run id propagated across child processes."},
		{GTSession, KindString, "", "Session name (typically the tmux session)."},
		{GTSessionID, KindString, "", "Stable session id used by mail/nudge protocols."},
		{GTSessionIDEnv, KindString, "", "Override for the env var that holds the session id."},
		{GTTown, KindString, "", "Town name (top-level workspace)."},
		{GTTownRoot, KindString, "", "Filesystem root of the town."},
		{GTTownSocket, KindString, "", "Path to the town's IPC socket."},
		{GTTmuxSocket, KindString, "", "Override for the tmux socket path used by gt."},
		{GTWitness, KindString, "", "Witness name; presence implies the witness role."},
		{GTWorkBead, KindString, "", "Bead id of the work being executed (formula step bead)."},
		{GTWorkMol, KindString, "", "Molecule id of the active workflow."},
		{GTWorkRig, KindString, "", "Rig name that owns the active work bead."},

		// Behaviour / mode flags.
		{GTAllowDirectPush, KindBool, "", "Allow `git push` directly to main from within gt-managed worktrees."},
		{GTDebug, KindString, "", "Debug toggle / category selector. Presence enables generic debug output."},
		{GTDebugSession, KindString, "", "Session-scoped debug selector."},
		{GTDegraded, KindBool, "", "Run in degraded mode — disable optional integrations (Dolt patrols, etc.)."},
		{GTFeedTUI, KindString, "", "When set, render the live activity feed TUI instead of agent-mode output."},
		{GTNoEmoji, KindBool, "", "Suppress emoji in CLI output."},
		{GTNoPager, KindBool, "", "Disable pager wrapping for long output."},
		{GTNukeAcknowledged, KindString, "", "Confirms the operator has acknowledged a nuke prompt."},
		{GTPager, KindString, "", "Override pager command (default: $PAGER, then less -FRX)."},
		{GTSkipPrepushReason, KindString, "", "Documented reason for skipping pre-push gates (audit trail)."},
		{GTSkipVerifyReason, KindString, "", "Documented reason for skipping verification gates."},
		{GTStaleWarn, KindString, "", "Stale-binary warning override (set by self-update tooling)."},
		{GTStaleWarned, KindBool, "", "Sentinel set after the stale-binary banner has been printed once."},
		{GTTheme, KindString, "", "Color theme name for terminal output."},
		{GTACPDebug, KindString, "", "ACP (Agent Control Protocol) debug selector."},
		{GTEscalateDebugDedup, KindString, "", "Escalation dedup-debug toggle (off|log|trace)."},

		// Logging / observability.
		{GTLogAgentOutput, KindBool, "", "Stream agent stdout/stderr into the OTEL log endpoint."},
		{GTLogBdOutput, KindBool, "", "Log every bd subprocess invocation."},
		{GTLogMailBody, KindBool, "", "Include the full mail body in mail debug logs."},
		{GTLogPrimeContext, KindBool, "", "Log the rendered prime context as it's emitted."},
		{GTLogPromptKeys, KindBool, "", "Log the keys (not values) of variables passed to prompt rendering."},
		{GTOtelLogsURL, KindString, "", "OTLP/HTTP endpoint for log export."},
		{GTOtelMetricsURL, KindString, "", "OTLP/HTTP endpoint for metric export."},

		// Beads / Dolt.
		{GTBdReadThrottleTimeoutSec, KindInt, "", "Per-call timeout for read-throttled bd subprocesses, in seconds."},
		{GTBdTimeoutSec, KindInt, "", "Default timeout for bd subprocesses, in seconds."},
		{GTDoltHost, KindString, "127.0.0.1", "Dolt server host."},
		{GTDoltIgnoreConfig, KindBool, "", "Ignore the per-rig Dolt config file (use env / defaults only)."},
		{GTDoltLogLevel, KindString, "", "Dolt server log level (info, debug, warn, ...)."},
		{GTDoltPassword, KindString, "", "Password for the Dolt connection (development only)."},
		{GTDoltPort, KindInt, "3307", "Dolt server port."},
		{GTDoltReapHelper, KindString, "", "Helper binary path used by the Dolt reaper."},
		{GTDoltUser, KindString, "root", "Username for the Dolt connection."},
		{GTDoltWaitTimeout, KindInt, "", "Dolt sql-server wait_timeout, in seconds (0 = use server default)."},

		// Context budget / agent runtime.
		{GTContextBudgetHardGate, KindInt, "", "Hard-gate context-budget threshold in tokens (forces handoff)."},
		{GTContextBudgetMaxTokens, KindInt, "", "Maximum context-budget tokens for the runtime."},
		{GTContextBudgetSoftGate, KindInt, "", "Soft-gate context-budget threshold in tokens (warn-only)."},
		{GTContextBudgetTokens, KindInt, "", "Current observed token count (set by the runtime, read by gates)."},
		{GTContextBudgetWarn, KindInt, "", "Warn threshold in tokens for context-budget messages."},
		{GTCursorAgentBin, KindString, "", "Path to the cursor-agent binary (overrides $PATH lookup)."},
		{GTKiroMaxIterations, KindInt, "", "Maximum iterations for the kiro agent runtime."},

		// Proxy / networking.
		{GTProxyCA, KindString, "", "Path to a CA bundle for the HTTPS proxy."},
		{GTProxyCert, KindString, "", "Path to the client certificate for the HTTPS proxy."},
		{GTProxyKey, KindString, "", "Path to the client key for the HTTPS proxy."},
		{GTProxyURL, KindString, "", "URL of the HTTPS proxy that gt should route through."},
		{GTRealBin, KindString, "", "Path to the real underlying binary when gt is acting as a wrapper."},

		// Process management.
		{GTProcessNames, KindString, "", "Comma-separated list of process names treated as gt-owned by patrols."},

		// Test hooks.
		{GTAgentStubBinDir, KindString, "", "Test-only: bin directory containing stub agent binaries."},
		{GTFakeKiroCounter, KindString, "", "Test-only: filesystem path used by the fake-kiro counter."},
		{GTFakeKiroMode, KindString, "", "Test-only: behaviour selector for the fake-kiro stub."},
		{GTFakeKiroSuccessOn, KindInt, "", "Test-only: iteration index on which fake-kiro should succeed."},
		{GTRunOQ4Spike, KindBool, "", "Test-only: enable the OQ4 spike harness."},
		{GTTestAttachedMoleculeLog, KindString, "", "Test-only: log file for attached-molecule observations."},
		{GTTestExternalDolt, KindBool, "", "Test-only: skip the embedded Dolt server and use an external one."},
		{GTTestNoNudge, KindBool, "", "Test-only: suppress real nudges."},
		{GTTestNudgeLog, KindString, "", "Test-only: log file for observed nudges."},
		{GTTestSkipHookVerify, KindBool, "", "Test-only: skip hook verification at session start."},
	}
}

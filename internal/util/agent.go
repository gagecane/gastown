package util

import "os"

// IsAgentContext returns true when the current process is running as a Gas
// Town agent (polecat, crew, witness, refinery, mayor, deacon) rather than
// an interactive human.
//
// Background: under kiro-cli and other LLM runtimes, stdin is a pty, so
// term.IsTerminal(os.Stdin.Fd()) returns true even though there's no human
// there to type. That means any command whose "am I interactive?" guard
// relies solely on isatty will fall open for the wrong population and
// freeze the agent on confirmation prompts, $EDITOR launches, and TUIs.
//
// The canonical non-interactive signal in Gas Town is the GT_ROLE env var,
// which is set by every spawn path that launches an agent session. Callers
// that need to decide whether to prompt / launch an editor / start a TUI
// should treat `IsAgentContext() == true` as "non-interactive, use
// defaults, do not block on stdin".
//
// See gu-pkf3 / gt-ube24 for the production failure pattern this helper
// guards against: refineries and polecats frozen indefinitely on prompts
// and vi invocations because isatty alone can't distinguish humans from
// pty-backed agent sessions.
func IsAgentContext() bool {
	return os.Getenv("GT_ROLE") != ""
}

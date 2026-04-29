package tmux

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
)

// SwitchClient switches the current tmux client to a different session.
// Used after remote recycle to move the user's view to the recycled session.
func (t *Tmux) SwitchClient(targetSession string) error {
	_, err := t.run("switch-client", "-t", targetSession)
	return err
}

// SetCrewCycleBindings sets up C-b n/p to cycle through sessions.
// This is now an alias for SetCycleBindings - the unified command detects
// session type automatically.
//
// IMPORTANT: We pass #{session_name} to the command because run-shell doesn't
// reliably preserve the session context. tmux expands #{session_name} at binding
// resolution time (when the key is pressed), giving us the correct session.
func (t *Tmux) SetCrewCycleBindings(session string) error {
	return t.SetCycleBindings(session)
}

// SetTownCycleBindings sets up C-b n/p to cycle through sessions.
// This is now an alias for SetCycleBindings - the unified command detects
// session type automatically.
func (t *Tmux) SetTownCycleBindings(session string) error {
	return t.SetCycleBindings(session)
}

// isGTBinding checks if the given key already has a Gas Town binding.
// Used to skip redundant re-binding on repeated ConfigureGasTownSession /
// EnsureBindingsOnSocket calls, preserving the user's original fallback.
//
// Two forms are recognized:
//  1. Guarded form (set by SetAgentsBinding/SetFeedBinding): uses if-shell
//     with a "gt " command — detects both old and new guarded bindings.
//  2. Unguarded form (set by EnsureBindingsOnSocket): direct run-shell
//     invoking "gt agents menu" or "gt feed --window".
func (t *Tmux) isGTBinding(table, key string) bool {
	output, err := t.run("list-keys", "-T", table, key)
	if err != nil || output == "" {
		return false
	}
	// Guarded form: if-shell + "gt ".
	if strings.Contains(output, "if-shell") && strings.Contains(output, "gt ") {
		return true
	}
	// Unguarded form: direct GT commands set by EnsureBindingsOnSocket.
	return strings.Contains(output, "gt agents menu") ||
		strings.Contains(output, "gt feed --window") ||
		strings.Contains(output, "gt rig menu")
}

// isGTBindingWithClient checks if the given key has a GT binding that includes
// --client for multi-client support. Older GT bindings without --client cause
// switch-client to target the wrong client when multiple clients are attached.
func (t *Tmux) isGTBindingWithClient(table, key string) bool {
	output, err := t.run("list-keys", "-T", table, key)
	if err != nil || output == "" {
		return false
	}
	return strings.Contains(output, "if-shell") && strings.Contains(output, "gt ") &&
		strings.Contains(output, "--client")
}

// isGTBindingCurrent checks whether the existing GT cycle binding has the
// current prefix pattern. Returns false if the binding is stale (e.g., after
// gt rig add introduces a new prefix not yet in the grep pattern).
func (t *Tmux) isGTBindingCurrent(table, key, currentPattern string) bool {
	output, err := t.run("list-keys", "-T", table, key)
	if err != nil || output == "" {
		return false
	}
	return strings.Contains(output, currentPattern)
}

// getKeyBinding returns the current tmux command bound to the given key in the
// specified key table. Returns empty string if no binding exists or if querying
// fails. This is used to capture user bindings before overwriting them, so the
// original binding can be preserved in the else branch of an if-shell guard.
//
// The returned string is a tmux command (e.g., "next-window", "run-shell 'lazygit'")
// suitable for use as a command argument to bind-key or if-shell.
//
// If the existing binding is already a Gas Town if-shell binding (detected by
// the presence of both "if-shell" and "gt " in the output), it is treated as
// no prior binding to avoid recursive wrapping on repeated calls.
func (t *Tmux) getKeyBinding(table, key string) string {
	// tmux list-keys -T <table> <key> outputs a line like:
	//   bind-key -T prefix g if-shell "..." "run-shell 'gt agents menu'" ":"
	// We need to extract just the command portion.
	//
	// Assumed format (tested with tmux 3.3+):
	//   bind-key [-r] -T <table> <key> <command...>
	// If tmux changes this format, parsing fails safely (returns ""),
	// which causes the caller to use its default fallback.
	output, err := t.run("list-keys", "-T", table, key)
	if err != nil || output == "" {
		return ""
	}

	// Don't capture existing GT bindings as "user bindings to preserve" —
	// that would wrap our own command in another layer.
	// Check both guarded (if-shell) and unguarded (direct run-shell) forms.
	if strings.Contains(output, "if-shell") && strings.Contains(output, "gt ") {
		return ""
	}
	if strings.Contains(output, "gt agents menu") ||
		strings.Contains(output, "gt feed --window") {
		return ""
	}

	// Parse the binding command from list-keys output.
	// Format: "bind-key [-r] -T <table> <key> <command...>"
	// We need everything after the key name.
	// Find the key in the output and take everything after it.
	fields := strings.Fields(output)
	keyIdx := -1
	for i, f := range fields {
		if f == "-T" && i+2 < len(fields) {
			// Skip table name, the next field is the key
			keyIdx = i + 2
			break
		}
	}
	if keyIdx < 0 || keyIdx >= len(fields)-1 {
		return ""
	}

	// Everything after the key is the command
	// Rejoin from keyIdx+1 onward, but we need to preserve the original spacing.
	// Find the key token in the original string and take everything after it.
	idx := strings.Index(output, " "+fields[keyIdx]+" ")
	if idx < 0 {
		return ""
	}
	cmd := strings.TrimSpace(output[idx+len(" "+fields[keyIdx]+" "):])
	if cmd == "" {
		return ""
	}

	return cmd
}

// safePrefixRe matches the character set guaranteed by beadsPrefixRegexp in
// internal/rig/manager.go.  Used as defense-in-depth: if rigs.json is
// hand-edited with regex metacharacters or shell-special chars, we skip the
// entry rather than injecting it into a grep -Eq / tmux if-shell fragment.
var safePrefixRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]{0,19}$`)

// sessionPrefixPattern returns a grep -Eq pattern that matches any registered
// Gas Town session name.  The pattern is built dynamically from rigs.json
// (via config.AllRigPrefixes) so that rigs beyond gastown/hq are recognized.
// "hq" is always included because it lives outside the rig registry
// (town-level services).
//
// Example output: "^(bd|db|fa|gl|gt|hq|la|lc)-"
func sessionPrefixPattern() string {
	seen := map[string]bool{"hq": true, "gt": true} // always include HQ + gastown fallback
	townRoot := os.Getenv("GT_ROOT")
	if townRoot == "" {
		townRoot = os.Getenv("GT_TOWN_ROOT")
	}
	if townRoot != "" {
		for _, p := range config.AllRigPrefixes(townRoot) {
			if safePrefixRe.MatchString(p) {
				seen[p] = true
			}
		}
	}
	sorted := make([]string, 0, len(seen))
	for p := range seen {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	return "^(" + strings.Join(sorted, "|") + ")-"
}

// SetCycleBindings sets up C-b n/p to cycle through related sessions.
// The gt cycle command automatically detects the session type and cycles
// within the appropriate group:
// - Town sessions: Mayor ↔ Deacon
// - Crew sessions: All crew members in the same rig
// - Rig ops sessions: Witness + Refinery + Polecats in the same rig
//
// IMPORTANT: These bindings are conditional - they only run gt cycle for
// Gas Town sessions (those matching a registered rig prefix or "hq-").
// For non-GT sessions, the user's original binding is preserved. If no
// prior binding existed, the tmux defaults (next-window/previous-window)
// are used.
// See: https://github.com/steveyegge/gastown/issues/13
// See: https://github.com/steveyegge/gastown/issues/1548
//
// IMPORTANT: We pass #{session_name} to the command because run-shell doesn't
// reliably preserve the session context. tmux expands #{session_name} at binding
// resolution time (when the key is pressed), giving us the correct session.
func (t *Tmux) SetCycleBindings(session string) error {
	// Skip if already correctly configured:
	// 1. Has --client for multi-client support
	// 2. Has the current prefix pattern (not stale from before a gt rig add)
	// We must re-bind if an older GT binding exists without --client, or if the
	// prefix pattern is stale (missing newly added rig prefixes).
	// See: https://github.com/steveyegge/gastown/issues/2299
	pattern := sessionPrefixPattern()
	if t.isGTBindingWithClient("prefix", "n") && t.isGTBindingCurrent("prefix", "n", pattern) {
		return nil
	}
	ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", pattern)

	// Capture existing bindings before overwriting, falling back to tmux defaults
	nextFallback := t.getKeyBinding("prefix", "n")
	if nextFallback == "" {
		nextFallback = "next-window"
	}
	prevFallback := t.getKeyBinding("prefix", "p")
	if prevFallback == "" {
		prevFallback = "previous-window"
	}

	// C-b n → gt cycle next for Gas Town sessions, original binding otherwise
	// Pass --client #{client_tty} so switch-client targets the correct client
	// when multiple tmux clients are attached (e.g., gastown + beads rigs).
	if _, err := t.run("bind-key", "-T", "prefix", "n",
		"if-shell", ifShell,
		"run-shell 'gt cycle next --session #{session_name} --client #{client_tty}'",
		nextFallback); err != nil {
		return err
	}
	// C-b p → gt cycle prev for Gas Town sessions, original binding otherwise
	if _, err := t.run("bind-key", "-T", "prefix", "p",
		"if-shell", ifShell,
		"run-shell 'gt cycle prev --session #{session_name} --client #{client_tty}'",
		prevFallback); err != nil {
		return err
	}
	return nil
}

// SetFeedBinding configures C-b a to jump to the activity feed window.
// This creates the feed window if it doesn't exist, or switches to it if it does.
// Uses `gt feed --window` which handles both creation and switching.
//
// IMPORTANT: This binding is conditional - it only runs for Gas Town sessions
// (those matching a registered rig prefix or "hq-"). For non-GT sessions, the
// user's original binding is preserved. If no prior binding existed, the key
// press is silently ignored.
// See: https://github.com/steveyegge/gastown/issues/13
// See: https://github.com/steveyegge/gastown/issues/1548
func (t *Tmux) SetFeedBinding(session string) error {
	pattern := sessionPrefixPattern()
	// Skip if already configured with the current rig prefix pattern.
	// Must re-bind if the pattern is stale (e.g., after gt rig add adds a new prefix).
	if t.isGTBinding("prefix", "a") && t.isGTBindingCurrent("prefix", "a", pattern) {
		return nil
	}
	ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", pattern)
	fallback := t.getKeyBinding("prefix", "a")
	if fallback == "" {
		// No prior binding — do nothing in non-GT sessions
		fallback = ":"
	}
	_, err := t.run("bind-key", "-T", "prefix", "a",
		"if-shell", ifShell,
		"run-shell 'gt feed --window'",
		fallback)
	return err
}

// SetAgentsBinding configures C-b g to open the agent switcher popup menu.
// This runs `gt agents menu` which displays a tmux popup with all Gas Town agents.
//
// IMPORTANT: This binding is conditional - it only runs for Gas Town sessions
// (those matching a registered rig prefix or "hq-"). For non-GT sessions, the
// user's original binding is preserved. If no prior binding existed, the key
// press is silently ignored.
// See: https://github.com/steveyegge/gastown/issues/1548
func (t *Tmux) SetAgentsBinding(session string) error {
	pattern := sessionPrefixPattern()
	// Skip if already configured with the current rig prefix pattern.
	// Must re-bind if the pattern is stale (e.g., after gt rig add adds a new prefix).
	if t.isGTBinding("prefix", "g") && t.isGTBindingCurrent("prefix", "g", pattern) {
		return nil
	}
	ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", pattern)
	fallback := t.getKeyBinding("prefix", "g")
	if fallback == "" {
		// No prior binding — do nothing in non-GT sessions
		fallback = ":"
	}
	_, err := t.run("bind-key", "-T", "prefix", "g",
		"if-shell", ifShell,
		"run-shell 'gt agents menu'",
		fallback)
	return err
}

// SetRigMenuBinding configures C-b r to open the rig menu popup.
// This runs `gt rig menu` which displays a tmux display-menu with all rigs
// and per-rig actions (start, stop, park, etc.).
func (t *Tmux) SetRigMenuBinding(session string) error {
	if t.isGTBinding("prefix", "r") {
		return nil
	}
	ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", sessionPrefixPattern())
	fallback := t.getKeyBinding("prefix", "r")
	if fallback == "" {
		fallback = ":"
	}
	_, err := t.run("bind-key", "-T", "prefix", "r",
		"if-shell", ifShell,
		"run-shell 'gt rig menu'",
		fallback)
	return err
}

// EnsureBindingsOnSocket sets the gt agents menu and feed keybindings on a
// specific tmux socket. This is used during gt up to ensure the bindings work
// even when the user is on a different socket than the town socket.
//
// townSocket is the socket name where GT agents live (e.g. "gt-a1b2c3"). When
// non-empty it is embedded in the binding command as GT_TOWN_SOCKET=<name>
// so that gt agents menu can locate agent sessions even when invoked from a
// directory outside the town root (e.g. a personal tmux session where
// workspace.FindFromCwd fails and InitRegistry is never called).
// Pass "" for test-socket use where InitRegistry is already called.
//
// Unlike SetAgentsBinding/SetFeedBinding (called during gt prime), this method:
//   - Targets a specific socket regardless of the Tmux instance's default
//   - Skips the session-name guard when there is no pre-existing user binding,
//     since the user may be in a personal session (not matching GT prefixes)
//     and still wants the agent menu for cross-socket navigation
//
// Safe to call multiple times; skips if bindings already exist.
func EnsureBindingsOnSocket(socket, townSocket string) error {
	t := NewTmuxWithSocket(socket)

	// Build the command strings, optionally prefixed with GT_TOWN_SOCKET so
	// gt agents menu / gt feed can find the right tmux server even when called
	// from a non-town directory.
	agentsCmd := "gt agents menu"
	feedCmd := "gt feed --window"
	if townSocket != "" {
		agentsCmd = fmt.Sprintf("GT_TOWN_SOCKET=%s gt agents menu", townSocket)
		feedCmd = fmt.Sprintf("GT_TOWN_SOCKET=%s gt feed --window", townSocket)
	}

	// Agents binding (prefix + g)
	if !t.isGTBinding("prefix", "g") {
		ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", sessionPrefixPattern())
		fallback := t.getKeyBinding("prefix", "g")
		if fallback == "" || fallback == ":" {
			// No user binding to preserve — always show the GT agent menu.
			// This is critical for cross-socket use: on the default socket,
			// no session names match GT prefixes, so an if-shell guard would
			// prevent the menu from ever appearing.
			_, _ = t.run("bind-key", "-T", "prefix", "g",
				"run-shell", agentsCmd)
		} else {
			// User has a custom binding — guard with GT pattern, preserve theirs.
			_, _ = t.run("bind-key", "-T", "prefix", "g",
				"if-shell", ifShell,
				"run-shell '"+agentsCmd+"'",
				fallback)
		}
	}

	// Feed binding (prefix + a)
	if !t.isGTBinding("prefix", "a") {
		ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", sessionPrefixPattern())
		fallback := t.getKeyBinding("prefix", "a")
		if fallback == "" || fallback == ":" {
			_, _ = t.run("bind-key", "-T", "prefix", "a",
				"run-shell", feedCmd)
		} else {
			_, _ = t.run("bind-key", "-T", "prefix", "a",
				"if-shell", ifShell,
				"run-shell '"+feedCmd+"'",
				fallback)
		}
	}

	// Rig menu binding (prefix + r)
	rigMenuCmd := "gt rig menu"
	if townSocket != "" {
		rigMenuCmd = fmt.Sprintf("GT_TOWN_SOCKET=%s gt rig menu", townSocket)
	}
	if !t.isGTBinding("prefix", "r") {
		ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", sessionPrefixPattern())
		fallback := t.getKeyBinding("prefix", "r")
		if fallback == "" || fallback == ":" {
			_, _ = t.run("bind-key", "-T", "prefix", "r",
				"run-shell", rigMenuCmd)
		} else {
			_, _ = t.run("bind-key", "-T", "prefix", "r",
				"if-shell", ifShell,
				"run-shell '"+rigMenuCmd+"'",
				fallback)
		}
	}

	return nil
}

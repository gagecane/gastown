// Package tmux provides a wrapper for tmux session operations via subprocess.
package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// validSessionNameRe validates session names to prevent shell injection
var validSessionNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Common errors
var (
	ErrNoServer           = errors.New("no tmux server running")
	ErrSessionExists      = errors.New("session already exists")
	ErrSessionNotFound    = errors.New("session not found")
	ErrSessionRunning     = errors.New("session already running with healthy agent")
	ErrInvalidSessionName = errors.New("invalid session name")
	ErrIdleTimeout        = errors.New("agent not idle before timeout")
)

// validateSessionName checks that a session name contains only safe characters.
// Returns ErrInvalidSessionName if the name contains dots, colons, or other
// characters that cause tmux to silently fail or produce cryptic errors.
func validateSessionName(name string) error {
	if name == "" || !validSessionNameRe.MatchString(name) {
		return fmt.Errorf("%w %q: must match %s", ErrInvalidSessionName, name, validSessionNameRe.String())
	}
	return nil
}

// validateCommandBinary extracts the binary path from a tmux session command
// and verifies it exists on disk. Handles common patterns:
//   - "exec env VAR=val /path/to/binary --args"
//   - "/path/to/binary --args"
//   - "sh -c '...'" (skipped — shell will handle resolution)
//
// Only checks absolute paths to avoid false positives on shell builtins.
func validateCommandBinary(command string) error {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil
	}

	// Skip past "exec" and "env" prefixes, KEY=VAL assignments,
	// and PowerShell $env: assignments and call operator (&).
	i := 0
	for i < len(fields) {
		f := fields[i]
		if f == "exec" || f == "env" || f == "&" {
			i++
			continue
		}
		// POSIX: KEY=VAL
		if strings.Contains(f, "=") && !strings.HasPrefix(f, "/") && !strings.HasPrefix(f, "-") {
			i++
			continue
		}
		// PowerShell: $env:KEY='val'; (may span multiple fields if value has spaces)
		if strings.HasPrefix(f, "$env:") {
			i++
			// Skip continuation fields until we see a semicolon-terminated one
			for i < len(fields) && !strings.HasSuffix(fields[i-1], ";") {
				i++
			}
			continue
		}
		break
	}

	if i >= len(fields) {
		return nil
	}

	binary := fields[i]
	// Only validate absolute paths — relative or bare names are resolved by shell.
	if !strings.HasPrefix(binary, "/") {
		return nil
	}
	if _, err := os.Stat(binary); err != nil {
		return fmt.Errorf("command binary not found: %s", binary)
	}
	return nil
}

// defaultSocket is the tmux socket name (-L flag) for multi-instance isolation.
// When set, all tmux commands use this socket instead of the default server.
// Access is protected by defaultSocketMu for concurrent test safety.
var (
	defaultSocket   string
	defaultSocketMu sync.RWMutex
)

// SetDefaultSocket sets the package-level default tmux socket name.
// Called during init to scope tmux to the current town.
func SetDefaultSocket(name string) {
	defaultSocketMu.Lock()
	defaultSocket = name
	defaultSocketMu.Unlock()
}

// GetDefaultSocket returns the current default tmux socket name.
func GetDefaultSocket() string {
	defaultSocketMu.RLock()
	defer defaultSocketMu.RUnlock()
	return defaultSocket
}

// SocketDir returns the directory where tmux stores its socket files.
// On macOS, tmux uses /tmp (not $TMPDIR which points to /var/folders/...),
// so we must use /tmp directly rather than os.TempDir().
func SocketDir() string {
	return filepath.Join("/tmp", fmt.Sprintf("tmux-%d", os.Getuid()))
}

// pruneDialTimeout bounds the kernel-level Unix-socket dial used by
// PruneDeadTestSockets. A dead socket's ECONNREFUSED is returned immediately,
// and a live server answers the dial well within this window even under load.
const pruneDialTimeout = 200 * time.Millisecond

// PruneDeadTestSockets removes leftover gt-test-* socket files in SocketDir
// whose tmux server is no longer running, returning the count removed.
//
// Integration tests create sockets named "gt-test-*"; their t.Cleanup normally
// kills the server and removes the file. But a test process SIGKILLed mid-run
// (context limit, crash, scheduler nuke) never runs cleanup, orphaning the
// socket file. Tens of thousands accumulate in /tmp/tmux-UID and slow every
// gt-test-* socket scan because findTestSockets probes each one (gu-wb67v,
// follow-on to the gu-erfce per-probe timeout).
//
// A socket is reaped only when a kernel-level dial returns ECONNREFUSED — the
// file exists but nothing is listening. A successful dial (live server, even
// with zero sessions) or any ambiguous error (ENOTSOCK, timeout, permission)
// leaves the file untouched, matching the conservative stance of
// probeServerHealth.
func PruneDeadTestSockets() int {
	entries, err := os.ReadDir(SocketDir())
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "gt-test-") {
			continue
		}
		socketPath := filepath.Join(SocketDir(), name)
		conn, err := net.DialTimeout("unix", socketPath, pruneDialTimeout)
		if err == nil {
			// Live listener — leave it alone.
			_ = conn.Close()
			continue
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			// No live listener: benign leftover from a killed test process.
			if rmErr := os.Remove(socketPath); rmErr == nil {
				removed++
			}
		}
	}
	return removed
}

// IsInSameSocket checks if the current process is inside a tmux session on the
// same socket as the default town socket. Used to decide between switch-client
// (same socket) and attach-session (different socket or outside tmux).
func IsInSameSocket() bool {
	tmuxEnv := os.Getenv("TMUX")
	if tmuxEnv == "" {
		return false
	}
	// TMUX format: /tmp/tmux-UID/socketname,pid,index
	parts := strings.SplitN(tmuxEnv, ",", 2)
	currentSocket := filepath.Base(parts[0])

	targetSocket := GetDefaultSocket()
	if targetSocket == "" {
		targetSocket = "default"
	}
	return currentSocket == targetSocket
}

// BuildCommand creates an exec.Cmd for tmux with the default socket applied.
// Use this instead of exec.Command("tmux", ...) for code outside the Tmux struct.
func BuildCommand(args ...string) *exec.Cmd {
	return BuildCommandContext(context.Background(), args...)
}

// BuildCommandContext is like BuildCommand but honors a context for cancellation.
func BuildCommandContext(ctx context.Context, args ...string) *exec.Cmd {
	allArgs := []string{"-u"}
	if sock := GetDefaultSocket(); sock != "" {
		allArgs = append(allArgs, "-L", sock)
	}
	allArgs = append(allArgs, args...)
	cmd := exec.CommandContext(ctx, "tmux", allArgs...)
	hideConsoleWindow(cmd)
	return cmd
}

// Tmux wraps tmux operations.
type Tmux struct {
	socketName string // tmux socket name (-L flag), empty = default socket
}

// noTownSocket is a sentinel socket name used when no town socket is configured.
// Using a non-existent socket causes tmux operations to fail with a clear
// "no server running" error instead of silently connecting to the wrong server.
const noTownSocket = "gt-no-town-socket"

// EnvAgentReady is the tmux session environment variable set by the agent's
// SessionStart hook (gt prime --hook) to signal that the agent has started.
// Used by WaitForCommand as a ZFC-compliant fallback for detecting wrapped
// agents (where pane_current_command remains a shell). See gt-sk5u.
const EnvAgentReady = "GT_AGENT_READY"

// NewTmux creates a new Tmux wrapper using the initialized town socket.
// Falls back to GT_TOWN_SOCKET env var (set by cross-socket tmux bindings).
// Empty socket means use the default tmux server.
func NewTmux() *Tmux {
	sock := GetDefaultSocket()
	if sock == "" {
		// GT_TOWN_SOCKET is embedded in tmux bindings created by EnsureBindingsOnSocket
		// so that "gt agents menu" / "gt feed" invoked from a personal terminal still
		// target the correct town server even when InitRegistry was not called.
		sock = os.Getenv("GT_TOWN_SOCKET")
	}
	return &Tmux{socketName: sock}
}

// NewTmuxWithSocket creates a Tmux wrapper that targets a named socket.
// This creates/connects to an isolated tmux server, separate from the user's
// default server. Primarily used in tests to prevent session name collisions
// and keystroke leaks (e.g. Escape from NudgeSession hitting the user's prefix table).
func NewTmuxWithSocket(socket string) *Tmux {
	return &Tmux{socketName: socket}
}

// run executes a tmux command and returns stdout.
// All commands include -u flag for UTF-8 support regardless of locale settings.
// See: https://github.com/steveyegge/gastown/issues/1219
func (t *Tmux) run(args ...string) (string, error) {
	return t.runContext(context.Background(), args...)
}

// runContext is like run but honors a context for cancellation/timeout. A
// hung tmux server (e.g. behind a stale socket) cannot wedge the caller
// forever — when the context expires the subprocess is killed and the
// context error is returned.
func (t *Tmux) runContext(ctx context.Context, args ...string) (string, error) {
	// Prepend global flags: -u (UTF-8 mode, PATCH-004) and optionally -L (socket).
	// The -L flag must come before the subcommand, so it goes in the prefix.
	allArgs := []string{"-u"}
	if t.socketName != "" {
		allArgs = append(allArgs, "-L", t.socketName)
	}
	allArgs = append(allArgs, args...)
	cmd := exec.CommandContext(ctx, "tmux", allArgs...)
	hideConsoleWindow(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// Surface a timeout/cancellation as the context error so callers can
		// distinguish a wedged probe from a real tmux failure.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", t.wrapError(err, stderr.String(), args)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// probeServerHealth guards against the gt-h9z clobber: tmux's internal
// "is a server already bound to this socket?" probe is tight enough that
// a momentarily-unresponsive server can be misclassified as absent, and
// tmux's recovery path unlinks the socket and binds a fresh server at
// the same path — silently orphaning the existing server and any
// sessions/clients running inside it.
//
// Two-stage check, designed to bail ONLY in the wedge case and not on
// the common "tmux exited cleanly after its last session was killed,
// but left the socket file behind" pattern that produces a benign
// stale socket:
//
//  1. Kernel-level Unix-socket dial. ECONNREFUSED (no live listener) →
//     benign stale; tmux will safely recreate. Successful connect →
//     listener is alive; proceed to stage 2. Any other dial error
//     (e.g. ENOTSOCK from a regular file in the way, timeout) → bail.
//  2. App-level `list-sessions` probe with a tight timeout. Success or
//     ErrNoServer (kernel said yes but tmux already finished exiting
//     between stages) → safe. Anything else (wedge, slow response,
//     protocol error) → bail.
//
// Default-socket Tmux (empty socketName) is left untouched: that path
// is shared with the user's interactive tmux and not part of the gt
// crew start workflow that produced the captured evidence.
func (t *Tmux) probeServerHealth() error {
	if t.socketName == "" {
		return nil
	}
	socketPath := filepath.Join(SocketDir(), t.socketName)
	if _, err := os.Stat(socketPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat tmux socket %s: %w", socketPath, err)
	}
	conn, err := net.DialTimeout("unix", socketPath, probeDialTimeout)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) || os.IsNotExist(err) {
			// No live listener — kernel has no one bound. The socket
			// file is a benign leftover from a clean tmux exit; tmux
			// will recreate it.
			return nil
		}
		return fmt.Errorf(
			"tmux socket %s exists but is not a connectable Unix socket (%v); "+
				"refusing to start a new session — investigate the file "+
				"holder (e.g. `ss -lxp src %s` or `lsof %s`) before retrying (see gt-h9z)",
			socketPath, err, socketPath, socketPath,
		)
	}
	_ = conn.Close()
	// Kernel-level listener is up. Now confirm tmux itself is responsive.
	if _, err := t.run("list-sessions", "-F", ""); err != nil {
		if errors.Is(err, ErrNoServer) {
			// Listener was up at stage 1 but tmux has since shut down
			// cleanly — also benign. tmux will recreate.
			return nil
		}
		return fmt.Errorf(
			"tmux socket %s has a live listener but `list-sessions` failed (%v); "+
				"refusing to start a new session — tmux's recovery path "+
				"would unlink and rebind, orphaning the existing server (see gt-h9z)",
			socketPath, err,
		)
	}
	return nil
}

// probeDialTimeout bounds the kernel-level Unix-socket probe used by
// probeServerHealth. Generous enough to ride out brief scheduling
// hiccups on a loaded host, tight enough that the clobber-guard does
// not add user-visible latency on the happy path.
const probeDialTimeout = 200 * time.Millisecond

// wrapError wraps tmux errors with context.
func (t *Tmux) wrapError(err error, stderr string, args []string) error {
	stderr = strings.TrimSpace(stderr)

	// Detect specific error types
	if strings.Contains(stderr, "no server running") ||
		strings.Contains(stderr, "error connecting to") ||
		strings.Contains(stderr, "no current target") ||
		strings.Contains(stderr, "server exited unexpectedly") {
		return ErrNoServer
	}
	if strings.Contains(stderr, "duplicate session") {
		return ErrSessionExists
	}
	if strings.Contains(stderr, "session not found") ||
		strings.Contains(stderr, "can't find session") {
		return ErrSessionNotFound
	}

	if stderr != "" {
		return fmt.Errorf("tmux %s: %s", args[0], stderr)
	}
	return fmt.Errorf("tmux %s: %w", args[0], err)
}

// IsAvailable checks if tmux is installed and can be invoked.
func (t *Tmux) IsAvailable() bool {
	cmd := exec.Command("tmux", "-V")
	hideConsoleWindow(cmd)
	return cmd.Run() == nil
}

// SocketFromEnv extracts the tmux socket name from the TMUX environment variable.
// TMUX format: /path/to/socket,server_pid,session_index
// Returns the basename of the socket path (e.g., "default", "gt"), or empty if
// not in tmux or the env variable is not set.
func SocketFromEnv() string {
	tmuxEnv := os.Getenv("TMUX")
	if tmuxEnv == "" {
		return ""
	}
	// Extract socket path (everything before first comma)
	parts := strings.SplitN(tmuxEnv, ",", 2)
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	return filepath.Base(parts[0])
}

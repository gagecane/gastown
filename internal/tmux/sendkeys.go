package tmux

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/telemetry"
)

// sessionNudgeLocks serializes nudges to the same session.
// This prevents interleaving when multiple nudges arrive concurrently,
// which can cause garbled input and missed Enter keys.
// Uses channel-based semaphores instead of sync.Mutex to support
// timed lock acquisition — preventing permanent lockout if a nudge hangs.
var sessionNudgeLocks sync.Map // map[string]chan struct{}

// nudgeLockTimeout is how long to wait to acquire the per-session nudge lock.
// If a previous nudge is still holding the lock after this duration, we give up
// rather than blocking forever. This prevents a hung tmux from permanently
// blocking all future nudges to that session.
const nudgeLockTimeout = 30 * time.Second

// SendKeys sends keystrokes to a session and presses Enter.
// Always sends Enter as a separate command for reliability.
// Uses a debounce delay between paste and Enter to ensure paste completes.
func (t *Tmux) SendKeys(session, keys string) error {
	return t.SendKeysDebounced(session, keys, constants.DefaultDebounceMs) // 100ms default debounce
}

// SendKeysDebounced sends keystrokes with a configurable delay before Enter.
// The debounceMs parameter controls how long to wait after paste before sending Enter.
// This prevents race conditions where Enter arrives before paste is processed.
func (t *Tmux) SendKeysDebounced(session, keys string, debounceMs int) (retErr error) {
	defer func() { telemetry.RecordPromptSend(context.Background(), session, keys, debounceMs, retErr) }()
	// Send text using literal mode (-l) to handle special chars
	if _, err := t.run("send-keys", "-t", session, "-l", keys); err != nil {
		return err
	}
	// Wait for paste to be processed
	if debounceMs > 0 {
		time.Sleep(time.Duration(debounceMs) * time.Millisecond)
	}
	// Send Enter separately - more reliable than appending to send-keys
	_, retErr = t.run("send-keys", "-t", session, "Enter")
	return retErr
}

// SendKeysRaw sends keystrokes without adding Enter.
func (t *Tmux) SendKeysRaw(session, keys string) error {
	_, err := t.run("send-keys", "-t", session, keys)
	return err
}

// SendKeysReplace sends keystrokes, clearing any pending input first.
// This is useful for "replaceable" notifications where only the latest matters.
// Uses Ctrl-U to clear the input line before sending the new message.
// The delay parameter controls how long to wait after clearing before sending (ms).
func (t *Tmux) SendKeysReplace(session, keys string, clearDelayMs int) error {
	// Send Ctrl-U to clear any pending input on the line
	if _, err := t.run("send-keys", "-t", session, "C-u"); err != nil {
		return err
	}

	// Small delay to let the clear take effect
	if clearDelayMs > 0 {
		time.Sleep(time.Duration(clearDelayMs) * time.Millisecond)
	}

	// Now send the actual message
	return t.SendKeys(session, keys)
}

// SendKeysDelayed sends keystrokes after a delay (in milliseconds).
// Useful for waiting for a process to be ready before sending input.
func (t *Tmux) SendKeysDelayed(session, keys string, delayMs int) error {
	time.Sleep(time.Duration(delayMs) * time.Millisecond)
	return t.SendKeys(session, keys)
}

// SendKeysDelayedDebounced sends keystrokes after a pre-delay, with a custom debounce before Enter.
// Use this when sending input to a process that needs time to initialize AND the message
// needs extra time between paste and Enter (e.g., Claude prompt injection).
// preDelayMs: time to wait before sending text (for process readiness)
// debounceMs: time to wait between text paste and Enter key (for paste completion)
func (t *Tmux) SendKeysDelayedDebounced(session, keys string, preDelayMs, debounceMs int) error {
	if preDelayMs > 0 {
		time.Sleep(time.Duration(preDelayMs) * time.Millisecond)
	}
	return t.SendKeysDebounced(session, keys, debounceMs)
}

// getSessionNudgeSem returns the channel semaphore for serializing nudges to a session.
// Creates a new semaphore if one doesn't exist for this session.
// The semaphore is a buffered channel of size 1 — send to acquire, receive to release.
func getSessionNudgeSem(session string) chan struct{} {
	sem := make(chan struct{}, 1)
	actual, _ := sessionNudgeLocks.LoadOrStore(session, sem)
	return actual.(chan struct{})
}

// acquireNudgeLock attempts to acquire the per-session nudge lock with a timeout.
// Returns true if the lock was acquired, false if the timeout expired.
func acquireNudgeLock(session string, timeout time.Duration) bool {
	sem := getSessionNudgeSem(session)
	select {
	case sem <- struct{}{}:
		return true
	case <-time.After(timeout):
		return false
	}
}

// releaseNudgeLock releases the per-session nudge lock.
func releaseNudgeLock(session string) {
	sem := getSessionNudgeSem(session)
	select {
	case <-sem:
	default:
		// Lock wasn't held — shouldn't happen, but don't block
	}
}

// nudgeFlockPath returns the filesystem lock path for cross-process nudge serialization.
// Lock files live alongside the nudge queue directory for self-documentation and cleanup.
func nudgeFlockPath(townRoot, session string) string {
	safe := strings.ReplaceAll(session, "/", "_")
	return filepath.Join(townRoot, constants.DirRuntime, "nudge_queue", safe, ".lock")
}

// IsSessionAttached returns true if the session has any clients attached.
func (t *Tmux) IsSessionAttached(target string) bool {
	attached, err := t.run("display-message", "-t", target, "-p", "#{session_attached}")
	return err == nil && attached == "1"
}

// WakePane triggers a SIGWINCH in a pane by resizing it slightly then restoring.
// This wakes up Claude Code's event loop by simulating a terminal resize.
//
// When Claude runs in a detached tmux session, its TUI library may not process
// stdin until a terminal event occurs. Attaching triggers SIGWINCH which wakes
// the event loop. This function simulates that by doing a resize dance.
//
// Note: This always performs the resize. Use WakePaneIfDetached to skip
// attached sessions where the wake is unnecessary.
func (t *Tmux) WakePane(target string) {
	// Use resize-window to trigger SIGWINCH. resize-pane doesn't work on
	// single-pane sessions because the pane already fills the window.
	// resize-window changes the window dimensions, which sends SIGWINCH to
	// all processes in all panes of that window.
	//
	// Get current width, bump +1, then restore. This avoids permanent size
	// changes even if the second resize fails.
	widthStr, err := t.run("display-message", "-p", "-t", target, "#{window_width}")
	if err != nil {
		return // session may be dead
	}
	width := strings.TrimSpace(widthStr)
	if width == "" {
		return
	}
	// Parse width to compute +1
	var w int
	if _, err := fmt.Sscanf(width, "%d", &w); err != nil || w < 1 {
		return
	}
	_, _ = t.run("resize-window", "-t", target, "-x", fmt.Sprintf("%d", w+1))
	time.Sleep(50 * time.Millisecond)
	_, _ = t.run("resize-window", "-t", target, "-x", width)

	// Reset window-size to "latest" after the resize dance. tmux automatically
	// sets window-size to "manual" whenever resize-window is called, which
	// permanently locks the window at the current dimensions. This prevents
	// the window from auto-sizing to a client when a human later attaches,
	// causing dots around the edges as if another smaller client is viewing.
	_, _ = t.run("set-option", "-w", "-t", target, "window-size", "latest")
}

// WakePaneIfDetached triggers a SIGWINCH only if the session is detached.
// This avoids unnecessary latency on attached sessions where Claude is
// already processing terminal events.
func (t *Tmux) WakePaneIfDetached(target string) {
	if t.IsSessionAttached(target) {
		return
	}
	t.WakePane(target)
}

// isTransientSendKeysError returns true if the error from tmux send-keys is
// transient and safe to retry. "not in a mode" occurs when the target pane's
// TUI hasn't initialized its input handling yet (common during cold startup).
func isTransientSendKeysError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not in a mode")
}

// sanitizeNudgeMessage removes control characters that corrupt tmux send-keys
// delivery. ESC (0x1b) triggers terminal escape sequences, CR (0x0d) acts as
// premature Enter, BS (0x08) deletes characters. TAB is replaced with a space
// to avoid triggering shell completion. Printable characters (including quotes,
// backticks, and Unicode) are preserved.
func sanitizeNudgeMessage(msg string) string {
	var b strings.Builder
	b.Grow(len(msg))
	for _, r := range msg {
		switch {
		case r == '\t': // TAB → space (avoid triggering completion)
			b.WriteRune(' ')
		case r == '\n': // preserve newlines (send-keys -l treats as Enter, known limitation)
			b.WriteRune(r)
		case r < 0x20: // strip all other control chars (ESC, CR, BS, etc.)
			continue
		case r == 0x7f: // DEL
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isInRewindMode checks if a tmux target is displaying Claude Code's Rewind
// conversation history browser. When Rewind is active, the session ignores
// typed text and only responds to Enter (accept rewind) or Escape (cancel).
// This can happen when a stray or deliberate Escape keystroke combines with
// a previous Escape to form the double-Escape sequence that activates Rewind.
//
// Detection is based on pane content analysis. Returns false on any error
// (defensive — don't block nudge delivery on detection failure).
func (t *Tmux) isInRewindMode(target string) bool {
	content, err := t.CapturePane(target, 15)
	if err != nil {
		return false
	}
	return containsRewindIndicators(content)
}

// containsRewindIndicators checks pane content for Claude Code Rewind menu
// patterns. The Rewind UI takes over the terminal and shows distinctive
// action prompts (Enter to act, Esc to cancel/exit). We require multiple
// co-occurring indicators to avoid false positives from conversation text.
func containsRewindIndicators(content string) bool {
	lower := strings.ToLower(content)

	// Primary: "rewind" appears alongside both Enter and Esc action prompts.
	if strings.Contains(lower, "rewind") {
		if strings.Contains(lower, "enter") && strings.Contains(lower, "esc") {
			return true
		}
	}

	// Secondary: specific action prompt pairs characteristic of the Rewind UI.
	rewindActionPairs := [][2]string{
		{"enter to continue", "esc to exit"},
		{"enter to accept", "esc to cancel"},
		{"enter to select", "esc to go back"},
		{"enter to select", "esc to cancel"},
	}
	for _, pair := range rewindActionPairs {
		if strings.Contains(lower, pair[0]) && strings.Contains(lower, pair[1]) {
			return true
		}
	}

	return false
}

// dismissRewindMode sends Escape to cancel Claude Code's Rewind menu,
// then waits briefly for the UI to return to normal.
func (t *Tmux) dismissRewindMode(target string) {
	_, _ = t.run("send-keys", "-t", target, "Escape")
	time.Sleep(300 * time.Millisecond)
}

// sendEnterVerified sends Enter to a tmux target and verifies it was processed
// by checking that the pane content changes. Under load, tmux may buffer
// keystrokes, causing Enter to race with text delivery — Enter arrives while
// tmux is still processing text/Escape and gets treated as part of the text
// stream rather than a separate submit action.
//
// After sending Enter, polls the pane content with exponential backoff. If the
// content hasn't changed (Enter wasn't processed), retries the Enter keystroke.
// Max 3 retries before returning an error.
//
// Falls back to best-effort (no verification) if pane capture fails.
func (t *Tmux) sendEnterVerified(target string) error {
	const (
		maxRetries     = 3
		initialBackoff = 500 * time.Millisecond
		verifyLines    = 5 // capture last N lines for comparison
	)

	// Snapshot pane content before Enter so we can detect processing.
	preSnapshot, preErr := t.CapturePane(target, verifyLines)

	// Send Enter
	if _, err := t.run("send-keys", "-t", target, "Enter"); err != nil {
		return fmt.Errorf("send Enter: %w", err)
	}

	// If we can't snapshot, fall back to unverified delivery (old behavior).
	if preErr != nil {
		return nil
	}

	backoff := initialBackoff
	for retry := 0; retry < maxRetries; retry++ {
		time.Sleep(backoff)

		postSnapshot, err := t.CapturePane(target, verifyLines)
		if err != nil {
			// Can't verify — assume success.
			return nil
		}

		if postSnapshot != preSnapshot {
			// Content changed — Enter was processed.
			return nil
		}

		// Content unchanged — Enter may not have been processed. Retry.
		if _, err := t.run("send-keys", "-t", target, "Enter"); err != nil {
			return fmt.Errorf("send Enter (retry %d): %w", retry+1, err)
		}

		// Exponential backoff: 500ms → 1000ms → 2000ms
		backoff *= 2
	}

	// Final verification after last retry.
	time.Sleep(500 * time.Millisecond)
	postSnapshot, err := t.CapturePane(target, verifyLines)
	if err != nil || postSnapshot != preSnapshot {
		return nil // Can't verify or content changed — consider success.
	}

	return fmt.Errorf("nudge Enter not processed after %d retries: pane content unchanged", maxRetries)
}

// adaptiveTextDelay returns the post-text-delivery delay for a message.
// Base 500ms + 25ms per chunk beyond the first, capped at 2s.
// Longer messages need more time for tmux to process all chunks under load.
func adaptiveTextDelay(messageLen int) time.Duration {
	numChunks := (messageLen + sendKeysChunkSize - 1) / sendKeysChunkSize
	delay := 500*time.Millisecond + time.Duration(max(0, numChunks-1))*25*time.Millisecond
	if delay > 2*time.Second {
		delay = 2 * time.Second
	}
	return delay
}

// sendMessageToTarget sends a sanitized message to a tmux target. For small
// messages (< sendKeysChunkSize), uses send-keys -l. For larger messages,
// sends in chunks with delays to avoid overwhelming the TTY input buffer.
//
// NOTE: The Linux TTY canonical mode buffer is 4096 bytes. Messages longer
// than ~4000 bytes may be truncated by the kernel's line discipline when
// delivered to programs using line-buffered input (readline, read, etc.).
// This is a fundamental kernel limit, not a tmux limitation. Programs reading
// raw stdin (like Claude Code's TUI) are not affected.
const sendKeysChunkSize = 512

func (t *Tmux) sendMessageToTarget(target, text string) error {
	if len(text) <= sendKeysChunkSize {
		return t.sendKeysLiteralWithRetry(target, text, constants.NudgeReadyTimeout)
	}
	// Send in chunks to avoid tmux send-keys argument length limits.
	// Each chunk is sent with a small delay to let the terminal process it.
	for i := 0; i < len(text); i += sendKeysChunkSize {
		end := i + sendKeysChunkSize
		if end > len(text) {
			end = len(text)
		}
		chunk := text[i:end]
		if i == 0 {
			// First chunk uses retry logic for startup race
			if err := t.sendKeysLiteralWithRetry(target, chunk, constants.NudgeReadyTimeout); err != nil {
				return err
			}
		} else {
			if _, err := t.run("send-keys", "-t", target, "-l", chunk); err != nil {
				return err
			}
		}
		// Small delay between chunks to let the terminal process
		if end < len(text) {
			time.Sleep(10 * time.Millisecond)
		}
	}
	return nil
}

// sendKeysLiteralWithRetry sends literal text to a tmux target, retrying on
// transient errors (e.g., "not in a mode" during agent TUI startup).
// This is the core retry loop used by both NudgeSession and NudgePane.
//
// Returns nil on success, or the last error after all retries are exhausted.
// Non-transient errors (session not found, no server) fail immediately.
//
// Related upstream issues:
//   - #1216: Nudge delivery reliability (input collision — NOT addressed here)
//   - #1275: Graceful nudge delivery (work interruption — NOT addressed here)
//
// This function ONLY addresses the startup race where the agent TUI hasn't
// initialized yet, causing tmux send-keys to fail with "not in a mode".
func (t *Tmux) sendKeysLiteralWithRetry(target, text string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	interval := constants.NudgeRetryInterval
	var lastErr error

	for time.Now().Before(deadline) {
		_, err := t.run("send-keys", "-t", target, "-l", text)
		if err == nil {
			return nil
		}
		if !isTransientSendKeysError(err) {
			return err // non-transient (session gone, no server) — fail fast
		}
		lastErr = err
		// Clamp sleep to remaining time so we don't overshoot the deadline.
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		sleep := interval
		if sleep > remaining {
			sleep = remaining
		}
		time.Sleep(sleep)
		// Grow interval by 1.5x, capped at 2s to stay responsive.
		// 500ms → 750ms → 1125ms → 1687ms → 2s (capped)
		interval = interval * 3 / 2
		if interval > 2*time.Second {
			interval = 2 * time.Second
		}
	}
	return fmt.Errorf("agent not ready for input after %s: %w", timeout, lastErr)
}

// NudgeSession sends a message to a Claude Code session reliably.
// This is the canonical way to send messages to Claude sessions.
// Uses: literal mode + 500ms debounce + ESC (for vim mode) + separate Enter.
// After sending, triggers SIGWINCH to wake Claude in detached sessions.
// Verification is the Witness's job (AI), not this function.
//
// If the agent TUI hasn't initialized yet (cold startup), retries with backoff
// up to NudgeReadyTimeout before giving up. See sendKeysLiteralWithRetry.
//
// IMPORTANT: Nudges to the same session are serialized to prevent interleaving.
// If multiple goroutines try to nudge the same session concurrently, they will
// queue up and execute one at a time. This prevents garbled input when
// SessionStart hooks and nudges arrive simultaneously.
func (t *Tmux) NudgeSession(session, message string) error {
	return t.NudgeSessionWithOpts(session, message, NudgeOpts{})
}

// NudgeOpts controls optional behavior for nudge delivery.
type NudgeOpts struct {
	// SkipEscape omits the Escape keystroke (step 5) and the 600ms readline
	// timeout (step 6) from the delivery protocol. Set this for agents where
	// Escape cancels in-flight generation (e.g., Gemini CLI) rather than
	// harmlessly exiting vim INSERT mode.
	SkipEscape bool

	// TownRoot, if set, enables flock-based cross-process serialization of
	// nudge delivery. Each `gt nudge` CLI invocation is a separate OS process,
	// so the in-process channel semaphore alone cannot prevent interleaving.
	// When TownRoot is provided, a filesystem lock is acquired at
	// <townRoot>/.runtime/nudge_queue/<session>/.lock before delivery.
	// When empty, only in-process locking is used (backward-compatible).
	TownRoot string
}

// canonicalPaneTarget converts a pane identifier like "%23" into a tmux target
// that send-keys can resolve reliably. Bare pane IDs work for display-message,
// but for send-keys we prefer an explicit session:window.pane target.
func (t *Tmux) canonicalPaneTarget(session, pane string) string {
	if pane == "" {
		return session
	}

	out, err := t.run("display-message", "-t", pane, "-p", "#{session_name}:#{window_index}.#{pane_index}")
	if err == nil {
		target := strings.TrimSpace(out)
		if target != "" {
			return target
		}
	}

	return pane
}

// NudgeSessionWithOpts is like NudgeSession but accepts delivery options.
// See NudgeOpts for available options.
func (t *Tmux) NudgeSessionWithOpts(session, message string, opts NudgeOpts) error {
	// Cross-process lock: serialize nudges across OS processes via flock(2).
	// Each `gt nudge` CLI invocation is a separate process, so the in-process
	// channel semaphore below provides no cross-process protection. Without
	// this, concurrent nudges interleave send-keys/Enter and produce garbled
	// or empty input. (GH#gt-ukl8)
	if opts.TownRoot != "" {
		lockPath := nudgeFlockPath(opts.TownRoot, session)
		unlock, err := acquireFlockLock(lockPath, nudgeLockTimeout)
		if err != nil {
			return fmt.Errorf("cross-process nudge lock for session %q: %w", session, err)
		}
		defer unlock()
	}

	// In-process lock: serialize nudges within a single process (goroutine fast path).
	if !acquireNudgeLock(session, nudgeLockTimeout) {
		return fmt.Errorf("nudge lock timeout for session %q: previous nudge may be hung", session)
	}
	defer releaseNudgeLock(session)

	// Resolve the correct target: in multi-pane sessions, find the pane
	// running the agent rather than sending to the focused pane.
	target := session
	if agentPane, err := t.FindAgentPane(session); err == nil && agentPane != "" {
		target = t.canonicalPaneTarget(session, agentPane)
	}

	// 0. Pre-delivery: dismiss Rewind menu if the session is stuck in it.
	// A previous nudge or user action may have triggered Claude Code's
	// double-Escape Rewind UI, which captures all input. Dismiss it first
	// so the nudge can be delivered normally. (GH#gt-8el)
	if t.isInRewindMode(target) {
		t.dismissRewindMode(target)
	}

	// 1. Exit copy/scroll mode if active — copy mode intercepts input,
	//    preventing delivery to the underlying process.
	if inMode, _ := t.run("display-message", "-p", "-t", target, "#{pane_in_mode}"); strings.TrimSpace(inMode) == "1" {
		_, _ = t.run("send-keys", "-t", target, "-X", "cancel")
		time.Sleep(50 * time.Millisecond)
	}

	// 2. Sanitize control characters that corrupt delivery
	sanitized := sanitizeNudgeMessage(message)

	// 3. Send text via send-keys -l. Messages > 512 bytes are chunked
	//    with 10ms inter-chunk delays to avoid argument length limits.
	if err := t.sendMessageToTarget(target, sanitized); err != nil {
		return err
	}

	// 4. Adaptive post-text delay: scales with message length to give tmux
	// enough time to process all chunks under load. (GH#gt-0b5)
	time.Sleep(adaptiveTextDelay(len(sanitized)))

	if !opts.SkipEscape {
		// Auto-skip Escape for Copilot CLI sessions. Escape cancels in-flight
		// generation in Copilot CLI (like Gemini), leaving the nudge text
		// stranded in the input field without Enter being processed. (hq-isz)
		agentType, _ := t.GetEnvironment(session, "GT_AGENT")
		if agentType == "copilot" {
			opts.SkipEscape = true
		}
	}

	if !opts.SkipEscape {
		// 5. Send Escape to exit vim INSERT mode if enabled (harmless in normal mode)
		// See: https://github.com/anthropics/gastown/issues/307
		_, _ = t.run("send-keys", "-t", target, "Escape")

		// 6. Wait 600ms — must exceed bash readline's keyseq-timeout (500ms default)
		// so ESC is processed alone, not as a meta prefix for the subsequent Enter.
		// Without this, ESC+Enter within 500ms becomes M-Enter (meta-return) which
		// does NOT submit the line.
		time.Sleep(600 * time.Millisecond)

		// 6.5. Post-Escape: check if our Escape triggered Rewind mode.
		// This happens when a previous Escape was still in the input buffer,
		// combining with ours to form the double-Escape that activates Rewind.
		// If triggered, dismiss Rewind and re-send the message (Rewind
		// consumed the original input). Skip the second Escape to avoid
		// re-triggering. (GH#gt-8el)
		if t.isInRewindMode(target) {
			t.dismissRewindMode(target)
			// Re-send message text — Rewind consumed the original input.
			_ = t.sendMessageToTarget(target, sanitized)
			time.Sleep(adaptiveTextDelay(len(sanitized)))
		}
	}

	// 7. Send Enter with verification — polls pane content to confirm Enter
	// was processed, retrying with exponential backoff under load. (GH#gt-0b5)
	if err := t.sendEnterVerified(target); err != nil {
		return fmt.Errorf("nudge to session %q: %w", session, err)
	}

	// 8. Wake the pane to trigger SIGWINCH for detached sessions
	t.WakePaneIfDetached(session)
	return nil
}

// NudgePane sends a message to a specific pane reliably.
// Same pattern as NudgeSession but targets a pane ID (e.g., "%9") instead of session name.
// After sending, triggers SIGWINCH to wake Claude in detached sessions.
// Nudges to the same pane are serialized to prevent interleaving.
func (t *Tmux) NudgePane(pane, message string) error {
	// Serialize nudges to this pane to prevent interleaving.
	// Use a timed lock to avoid permanent blocking if a previous nudge hung.
	if !acquireNudgeLock(pane, nudgeLockTimeout) {
		return fmt.Errorf("nudge lock timeout for pane %q: previous nudge may be hung", pane)
	}
	defer releaseNudgeLock(pane)

	// 0. Pre-delivery: dismiss Rewind menu if active. (GH#gt-8el)
	if t.isInRewindMode(pane) {
		t.dismissRewindMode(pane)
	}

	// 1. Exit copy/scroll mode if active — copy mode intercepts input,
	//    preventing delivery to the underlying process.
	if inMode, _ := t.run("display-message", "-p", "-t", pane, "#{pane_in_mode}"); strings.TrimSpace(inMode) == "1" {
		_, _ = t.run("send-keys", "-t", pane, "-X", "cancel")
		time.Sleep(50 * time.Millisecond)
	}

	// 2. Sanitize control characters that corrupt delivery
	sanitized := sanitizeNudgeMessage(message)

	// 3. Send text via send-keys -l. Messages > 512 bytes are chunked
	//    with 10ms inter-chunk delays to avoid argument length limits.
	if err := t.sendMessageToTarget(pane, sanitized); err != nil {
		return err
	}

	// 4. Adaptive post-text delay: scales with message length. (GH#gt-0b5)
	time.Sleep(adaptiveTextDelay(len(sanitized)))

	// 5. Send Escape to exit vim INSERT mode if enabled (harmless in normal mode)
	// See: https://github.com/anthropics/gastown/issues/307
	_, _ = t.run("send-keys", "-t", pane, "Escape")

	// 6. Wait 600ms — must exceed bash readline's keyseq-timeout (500ms default)
	time.Sleep(600 * time.Millisecond)

	// 6.5. Post-Escape: check if our Escape triggered Rewind mode. (GH#gt-8el)
	if t.isInRewindMode(pane) {
		t.dismissRewindMode(pane)
		_ = t.sendMessageToTarget(pane, sanitized)
		time.Sleep(adaptiveTextDelay(len(sanitized)))
	}

	// 7. Send Enter with verification — polls pane content to confirm Enter
	// was processed, retrying with exponential backoff under load. (GH#gt-0b5)
	if err := t.sendEnterVerified(pane); err != nil {
		return fmt.Errorf("nudge to pane %q: %w", pane, err)
	}

	// 8. Wake the pane to trigger SIGWINCH for detached sessions
	t.WakePaneIfDetached(pane)
	return nil
}

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

// sendEnterVerified sends Enter to a tmux target and verifies it was processed.
//
// Under load, tmux may buffer keystrokes, causing Enter to race with text
// delivery — Enter arrives while tmux is still processing text/Escape and
// gets treated as part of the text stream rather than a separate submit
// action. Worse, the Escape keystroke sent at the end of a Claude Code
// "turn" can be consumed by the TUI state machine if async turn-end UI
// (e.g., hooks-finished footer, Credits line) is still rendering — in that
// case, the pane content changes due to unrelated async rendering, and a
// naïve "any change = success" check declares victory while the nudge text
// sits stranded on the prompt.
//
// This function addresses both problems:
//
//  1. Quiescence pre-check: wait up to ~1s for pane content to stabilize
//     before sending Enter, so async turn-end rendering (credits, hooks)
//     does not race with our submit.
//
//  2. Specific post-verification: declare success only when there is
//     evidence Enter was actually processed — one of:
//     a) The nudgeText no longer appears in the prompt/input lines
//     (it was submitted and cleared from the input buffer), or
//     b) A busy indicator ("esc to interrupt") appeared (agent is
//     now working on the nudge), or
//     c) The pane scrolled substantially (many lines of new content
//     beyond what async footer rendering would produce).
//     Arbitrary cosmetic changes (footer re-renders, cursor blinks)
//     alone are NOT sufficient to declare success.
//
// Retries Enter up to 3 times before returning an error. Falls back to
// best-effort (no verification) if pane capture fails.
//
// nudgeText should be the sanitized nudge message we expect to find on the
// prompt line BEFORE submit and expect to be gone AFTER. Callers should
// pass the sanitized message text; an empty string disables the text-based
// signal (a) but leaves signals (b) and (c) active.
func (t *Tmux) sendEnterVerified(target, nudgeText string) error {
	const (
		maxRetries  = 3
		verifyLines = 10 // capture last N lines for comparison
	)

	// Quiescence pre-check: wait for pane to stabilize before sending Enter.
	// Ignores failure — if we can't capture, proceed anyway (best-effort).
	_ = t.waitForPaneQuiescence(target, verifyLines, paneQuiescenceStable, paneQuiescenceTimeout)

	// Snapshot pane content before Enter so we can compute diffs later.
	preSnapshot, preErr := t.CapturePane(target, verifyLines)

	// Send Enter.
	if _, err := t.run("send-keys", "-t", target, "Enter"); err != nil {
		return fmt.Errorf("send Enter: %w", err)
	}

	// If we can't snapshot, fall back to unverified delivery (old behavior).
	if preErr != nil {
		return nil
	}

	// Derive a short "probe" substring of the nudge text to search for on
	// input/prompt lines. Nudge text can be long and chunked across lines
	// in the pane; a short distinctive prefix is much more reliable to
	// find than the entire message.
	probe := nudgeProbe(nudgeText)

	backoff := paneVerifyInitialBackoff
	for retry := 0; retry < maxRetries; retry++ {
		time.Sleep(backoff)

		processed, err := t.verifyEnterProcessed(target, preSnapshot, probe, verifyLines)
		if err != nil {
			// Can't verify — assume success (best-effort fallback).
			return nil
		}
		if processed {
			return nil
		}

		// Enter not processed. Retry.
		if _, err := t.run("send-keys", "-t", target, "Enter"); err != nil {
			return fmt.Errorf("send Enter (retry %d): %w", retry+1, err)
		}

		// Exponential backoff: 500ms → 1000ms → 2000ms
		backoff *= 2
	}

	// Final verification after last retry.
	time.Sleep(500 * time.Millisecond)
	processed, err := t.verifyEnterProcessed(target, preSnapshot, probe, verifyLines)
	if err != nil || processed {
		return nil // Can't verify or success — consider delivered.
	}

	return fmt.Errorf("nudge Enter not processed after %d retries: nudge text still stranded on prompt", maxRetries)
}

// Pane verification tuning parameters.
const (
	// paneQuiescenceStable is the duration of no-change required to declare
	// the pane has stopped rendering async content (e.g., turn-end footer).
	paneQuiescenceStable = 300 * time.Millisecond

	// paneQuiescenceTimeout is the upper bound we will wait for quiescence
	// before giving up and proceeding anyway. Must be small enough not to
	// delay normal nudge delivery perceptibly.
	paneQuiescenceTimeout = 1 * time.Second

	// paneQuiescencePoll is the polling interval used while waiting for
	// quiescence.
	paneQuiescencePoll = 100 * time.Millisecond

	// paneVerifyInitialBackoff is the initial post-Enter wait before the
	// first verification attempt.
	paneVerifyInitialBackoff = 500 * time.Millisecond

	// paneScrollSignificantLines is how many lines of new content (beyond
	// the previous snapshot's last line) we treat as evidence that the
	// pane genuinely scrolled due to Enter being processed, not just
	// cosmetic re-rendering of a footer.
	paneScrollSignificantLines = 3
)

// waitForPaneQuiescence polls the pane until its captured content has not
// changed across two successive samples separated by `stable`, or until
// `timeout` elapses. Returns nil once quiescent, or the last capture error
// if the pane could not be captured at all.
//
// This protects the Enter-submit sequence from races with async TUI
// rendering at turn-end (hooks-finished footer, credits line) — we wait
// for the footer to settle before sending Escape+Enter so our Escape is
// not consumed by the state machine and our Enter is not misattributed
// to unrelated content changes.
func (t *Tmux) waitForPaneQuiescence(target string, lines int, stable, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	last, err := t.CapturePane(target, lines)
	if err != nil {
		return err
	}
	stableSince := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(paneQuiescencePoll)
		cur, err := t.CapturePane(target, lines)
		if err != nil {
			return err
		}
		if cur != last {
			last = cur
			stableSince = time.Now()
			continue
		}
		if time.Since(stableSince) >= stable {
			return nil
		}
	}
	// Timed out waiting for full quiescence — return nil anyway so caller
	// can proceed with best-effort delivery.
	return nil
}

// verifyEnterProcessed checks whether Enter appears to have been processed
// since preSnapshot was captured. Returns true on any of the specific
// positive signals described on sendEnterVerified. Returns an error only
// if the pane capture itself fails.
func (t *Tmux) verifyEnterProcessed(target, preSnapshot, probe string, lines int) (bool, error) {
	post, err := t.CapturePane(target, lines)
	if err != nil {
		return false, err
	}

	// Signal (b): busy indicator appeared.
	for _, line := range strings.Split(post, "\n") {
		if hasBusyIndicator(line) {
			return true, nil
		}
	}

	// Signal (a): probe text no longer on any input-like (prompt) line.
	if probe != "" {
		if !paneContainsProbeOnPrompt(post, probe) {
			return true, nil
		}
	}

	// Signal (c): pane genuinely scrolled. Compute how many lines of the
	// post-snapshot are NOT prefixes of the pre-snapshot. A large number
	// of new lines indicates real scroll, not cosmetic footer re-rendering.
	if paneNewLineCount(preSnapshot, post) >= paneScrollSignificantLines {
		return true, nil
	}

	// If nudge text is empty (nothing to probe for) AND no scroll AND no
	// busy indicator, fall back to the legacy any-change check so we
	// don't regress behavior on paths that never pass nudge text in. This
	// keeps the old contract for callers that never set probe.
	if probe == "" && post != preSnapshot {
		return true, nil
	}

	return false, nil
}

// nudgeProbe returns a short distinctive substring of the nudge text,
// suitable for searching pane content. Returns "" if nudgeText is empty
// or too short to be distinctive.
func nudgeProbe(nudgeText string) string {
	// Strip leading/trailing whitespace and pick the first non-whitespace
	// word boundary run of reasonable length. We intentionally avoid very
	// short probes (< 6 chars) because they are likely to false-match on
	// unrelated pane content.
	s := strings.TrimSpace(nudgeText)
	if len(s) < 6 {
		return ""
	}
	// Use up to the first 48 chars, but cut at a newline if one appears
	// sooner — multiline messages render across lines in the pane and
	// a shorter single-line probe is more reliable.
	if nl := strings.IndexByte(s, '\n'); nl >= 0 && nl < 48 {
		s = s[:nl]
	} else if len(s) > 48 {
		s = s[:48]
	}
	s = strings.TrimSpace(s)
	if len(s) < 6 {
		return ""
	}
	return s
}

// paneContainsProbeOnPrompt reports whether probe appears on a line that
// also contains a recognizable prompt marker (the stranded-text scenario).
//
// We deliberately require a prompt marker on the same line so that text
// which has been submitted and scrolled into history (or echoed out of
// the input buffer) does not false-positive. Matching anywhere in the
// pane is wrong for shell-style panes that echo typed text; matching
// only the very last line is wrong for Claude Code panes whose prompt
// line is followed by status/footer lines during turn-end finalization.
//
// Prompt markers recognized:
//   - "❯" (U+276F) — Claude Code's default prompt character
//   - "!>" — Claude Code's status-bar-adorned prompt (e.g. "[gastown] 11% !>")
//   - "$" preceded by a space at start — shell-style prompt
//
// False positives are harmless (retry Enter); false negatives (missing a
// real stranded nudge) are what the other signals (b busy, c scroll) are
// for.
func paneContainsProbeOnPrompt(paneContent, probe string) bool {
	if probe == "" {
		return false
	}
	for _, line := range strings.Split(paneContent, "\n") {
		if !strings.Contains(line, probe) {
			continue
		}
		if linelooksLikePrompt(line) {
			return true
		}
	}
	return false
}

// linelooksLikePrompt heuristically detects whether a pane line is an input
// prompt line (as opposed to scroll history or a footer line).
func linelooksLikePrompt(line string) bool {
	// Fast path: Claude Code's default prompt character.
	if strings.Contains(line, "❯") {
		return true
	}
	// Claude Code status-bar prompt (e.g. "[gastown] 11% !> ...").
	if strings.Contains(line, "!>") {
		return true
	}
	// Bash/zsh style "$ " near the start.
	trimmed := strings.TrimLeft(line, " \t")
	if strings.HasPrefix(trimmed, "$ ") || strings.HasPrefix(trimmed, "# ") {
		return true
	}
	return false
}

// paneNewLineCount returns the number of lines in `post` that do not appear
// anywhere in `pre`. This is a coarse measure of how much genuinely new
// content the pane gained. Cosmetic re-renders (footer lines being replaced
// in place) produce 0–2 new lines; a genuine Enter submit with agent output
// typically produces ≥3.
//
// Note: we use a set-based comparison (ignoring line order) rather than a
// suffix/prefix overlap, because async TUI rendering can change lines in
// the middle of the pane (e.g., a Credits footer line) without changing
// surrounding lines — which defeats any contiguous-overlap approach.
func paneNewLineCount(pre, post string) int {
	if pre == post {
		return 0
	}
	preSet := make(map[string]struct{})
	for _, line := range strings.Split(pre, "\n") {
		preSet[line] = struct{}{}
	}
	newLines := 0
	for _, line := range strings.Split(post, "\n") {
		if _, ok := preSet[line]; !ok {
			newLines++
		}
	}
	return newLines
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

	// Target, if non-empty, overrides the default send-keys target derived
	// from FindAgentPane(session). Set by NudgePane to pass an explicit pane
	// ID; the session argument is still used for locking, env lookup, and
	// WakePaneIfDetached.
	Target string
}

// shouldSkipEscapeForAgent returns true when the agent's CLI interprets a
// bare Escape keystroke as "cancel in-flight generation" (or similar), which
// would strand nudge text in the input field instead of letting Enter submit
// it. Matching is case-insensitive to tolerate casing drift in GT_AGENT values.
//
// Known agents that require SkipEscape:
//   - copilot    (GitHub Copilot CLI) — Escape cancels generation (hq-isz)
//   - kiro-cli   (Kiro CLI; any GT_AGENT containing "kiro") — Escape
//     cancels generation (gu-flq9)
//
// Other agents (claude, gemini-as-vim, cursor, etc.) either ignore Escape or
// rely on it to exit vim INSERT mode, so it must be preserved.
func shouldSkipEscapeForAgent(agentType string) bool {
	a := strings.ToLower(strings.TrimSpace(agentType))
	if a == "" {
		return false
	}
	if a == "copilot" {
		return true
	}
	// Any agent value containing "kiro" (kiro, kiro-cli, future kiro-* variants).
	if strings.Contains(a, "kiro") {
		return true
	}
	return false
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

	// Resolve the send-keys target. opts.Target wins when set (callers like
	// NudgePane pre-resolve a pane ID); otherwise, in multi-pane sessions
	// find the pane running the agent rather than the focused pane.
	target := opts.Target
	if target == "" {
		target = session
		if agentPane, err := t.FindAgentPane(session); err == nil && agentPane != "" {
			target = t.canonicalPaneTarget(session, agentPane)
		}
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
		// Auto-skip Escape for agents where Escape cancels in-flight generation
		// (Copilot CLI, kiro-cli) rather than harmlessly exiting vim INSERT mode.
		// Leaving the Escape in the protocol strands the nudge text in the input
		// buffer without Enter being processed. (hq-isz, gu-flq9)
		agentType, _ := t.GetEnvironment(session, "GT_AGENT")
		if shouldSkipEscapeForAgent(agentType) {
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
	if err := t.sendEnterVerified(target, sanitized); err != nil {
		return fmt.Errorf("nudge to session %q: %w", session, err)
	}

	// 8. Wake the pane to trigger SIGWINCH for detached sessions
	t.WakePaneIfDetached(session)
	return nil
}

// NudgePane sends a message to a specific pane reliably.
// Delegates to NudgeSessionWithOpts with the pane pre-resolved as opts.Target,
// deriving the session name from the pane so locking, GT_AGENT lookup, and
// cross-process flock all apply. Falls back to the pane string as the lock
// key when the session cannot be resolved (no tmux server, stale pane ID).
func (t *Tmux) NudgePane(pane, message string) error {
	session := pane
	if idx := strings.Index(pane, ":"); idx > 0 && !strings.HasPrefix(pane, "%") {
		session = pane[:idx]
	} else if out, err := t.run("display-message", "-t", pane, "-p", "#{session_name}"); err == nil {
		if s := strings.TrimSpace(out); s != "" {
			session = s
		}
	}
	return t.NudgeSessionWithOpts(session, message, NudgeOpts{Target: pane})
}

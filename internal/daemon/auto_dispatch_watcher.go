package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/steveyegge/gastown/internal/dog"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/plugin"
	"github.com/steveyegge/gastown/internal/tmux"
)

// autoDispatchPluginName is the plugin name event-driven refill dispatches.
const autoDispatchPluginName = "auto-dispatch"

// defaultAutoDispatchRateLimit is the minimum interval between event-driven
// dispatches for a single rig. Prevents thundering-herd on rapid completions.
const defaultAutoDispatchRateLimit = 30 * time.Second

// defaultAutoDispatchPollInterval is how often the watcher checks the events
// file for new lines. Small enough for sub-second dispatch latency, large
// enough to avoid burning CPU.
const defaultAutoDispatchPollInterval = 100 * time.Millisecond

// sessionDeathEvent is a minimal representation of a session_death event
// parsed from .events.jsonl.
type sessionDeathEvent struct {
	Timestamp string                 `json:"ts"`
	Type      string                 `json:"type"`
	Actor     string                 `json:"actor"`
	Payload   map[string]interface{} `json:"payload"`
}

// eventLine is an untyped event envelope used during parsing so the watcher
// can skip non-session_death lines with minimal allocation.
type eventLine struct {
	Type string `json:"type"`
}

// AutoDispatchWatcher tails .events.jsonl and triggers event-driven
// auto-dispatch on planned session terminations (gt done), bypassing the
// cooldown gate on the auto-dispatch plugin.
//
// Design notes:
//   - Only fires on "planned" session deaths: caller="gt done"
//   - Skips crash/reap/zombie/shutdown callers — those indicate problems that
//     should be investigated by Witness, not masked by a replacement dispatch
//   - Per-rig rate-limit prevents thundering-herd on rapid completions
//   - Existing cooldown-gated pass in dispatchPlugins() still runs as fallback
//   - Records each dispatch with the plugin recorder so the cooldown gate sees
//     it and doesn't double-dispatch on the next heartbeat cycle
type AutoDispatchWatcher struct {
	townRoot string
	logger   *log.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// started guards against double Start() which would spawn duplicate goroutines.
	started atomic.Bool

	// Rate-limit state: last dispatch time per rig.
	rateLimitMu sync.Mutex
	lastByRig   map[string]time.Time

	// Tunables (exposed for tests).
	rateLimit    time.Duration
	pollInterval time.Duration

	// Event consumer — receives eligible session_death events and invokes
	// the auto-dispatch plugin for the associated rig. Extracted for testability.
	dispatcher AutoDispatchConsumer
}

// AutoDispatchConsumer is the interface the watcher uses to trigger an
// actual auto-dispatch run. Production uses a handler-backed implementation;
// tests supply a fake.
type AutoDispatchConsumer interface {
	// DispatchAutoDispatchForRig triggers the auto-dispatch plugin for the
	// given rig, bypassing the cooldown gate. trigger is a human-readable
	// reason (e.g., "gt done") for observability.
	DispatchAutoDispatchForRig(rig, trigger, triggerSession, triggerAgent string) error
}

// NewAutoDispatchWatcher creates a new watcher. dispatcher receives events
// once they pass the eligibility and rate-limit gates. If dispatcher is nil,
// the watcher logs and swallows events (useful only for tests).
func NewAutoDispatchWatcher(townRoot string, logger *log.Logger, dispatcher AutoDispatchConsumer) *AutoDispatchWatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &AutoDispatchWatcher{
		townRoot:     townRoot,
		logger:       logger,
		ctx:          ctx,
		cancel:       cancel,
		lastByRig:    make(map[string]time.Time),
		rateLimit:    defaultAutoDispatchRateLimit,
		pollInterval: defaultAutoDispatchPollInterval,
		dispatcher:   dispatcher,
	}
}

// SetRateLimit overrides the per-rig rate-limit window. Intended for tests.
func (w *AutoDispatchWatcher) SetRateLimit(d time.Duration) {
	w.rateLimit = d
}

// SetPollInterval overrides the tail poll interval. Intended for tests.
func (w *AutoDispatchWatcher) SetPollInterval(d time.Duration) {
	w.pollInterval = d
}

// Start begins tailing the events file. Safe to call multiple times;
// subsequent calls are no-ops.
func (w *AutoDispatchWatcher) Start() error {
	if !w.started.CompareAndSwap(false, true) {
		return nil
	}
	w.wg.Add(1)
	go w.run()
	return nil
}

// Stop gracefully stops the watcher.
func (w *AutoDispatchWatcher) Stop() {
	w.cancel()
	w.wg.Wait()
}

// run is the main tail loop.
//
// Note on log rotation: the watcher holds a single file descriptor for the
// duration of its run. If the events file is rotated or truncated via rename
// (KRC pruner does this with os.Rename on a fresh temp file), this fd will
// point at the unlinked old inode and stop seeing new events. We deliberately
// accept this trade-off: event-driven refill is an optimization, and the
// cooldown-gated pass in dispatchPlugins() provides the safety net for any
// missed events until the next daemon restart reopens the file.
func (w *AutoDispatchWatcher) run() {
	defer w.wg.Done()

	eventsPath := filepath.Join(w.townRoot, events.EventsFile)

	// Ensure the file exists so we can open it. Zero-length file is fine —
	// appending writers create it with O_APPEND|O_CREATE. We just need an inode.
	if _, err := os.Stat(eventsPath); os.IsNotExist(err) {
		// Create empty file so Open() succeeds. Best-effort; if this fails
		// we'll retry below.
		if f, createErr := os.OpenFile(eventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); createErr == nil {
			_ = f.Close()
		}
	}

	file, err := os.Open(eventsPath)
	if err != nil {
		w.logf("AutoDispatchWatcher: cannot open events file %s: %v — watcher disabled", eventsPath, err)
		return
	}
	defer file.Close()

	// Seek to end — only process new events emitted after startup.
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		w.logf("AutoDispatchWatcher: seek to end of %s failed: %v — watcher disabled", eventsPath, err)
		return
	}

	reader := bufio.NewReader(file)
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	w.logf("AutoDispatchWatcher: tailing %s for session_death events (rate-limit %s per rig)", eventsPath, w.rateLimit)

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			w.drainReader(reader)
		}
	}
}

// drainReader reads all currently-available lines and processes each one.
// Separate from the ticker loop for testability.
func (w *AutoDispatchWatcher) drainReader(reader *bufio.Reader) {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			// EOF is the common case (no more data available right now) —
			// bail and wait for the next tick. Any real I/O error is also
			// terminal for this drain; next tick will retry.
			return
		}
		w.handleLine(line)
	}
}

// handleLine parses one raw JSONL line and fires dispatch if eligible.
func (w *AutoDispatchWatcher) handleLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	// Fast-path filter: only parse fully when we know the line is a session_death.
	var envelope eventLine
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		return // malformed line — silently skip
	}
	if envelope.Type != events.TypeSessionDeath {
		return
	}

	var ev sessionDeathEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return
	}

	w.processSessionDeath(ev)
}

// processSessionDeath is the eligibility + rate-limit gate. Public-ish name
// because it's called from tests via the line-based handler.
func (w *AutoDispatchWatcher) processSessionDeath(ev sessionDeathEvent) {
	caller, _ := ev.Payload["caller"].(string)
	reason, _ := ev.Payload["reason"].(string)
	session, _ := ev.Payload["session"].(string)
	agent, _ := ev.Payload["agent"].(string)

	// Only planned polecat exits trigger event-driven refill.
	// Crash/reap/zombie/shutdown explicitly should NOT trigger a replacement —
	// those indicate problems that need investigation by Witness.
	if !isPlannedPolecatExit(caller, reason) {
		return
	}

	rig := rigFromAgent(agent)
	if rig == "" {
		// Cannot determine rig — skip (e.g., deacon, witness, mayor sessions
		// dying don't free a polecat slot).
		return
	}

	// Per-rig rate-limit: at most one event-driven dispatch per rig per window.
	if !w.tryClaim(rig) {
		return
	}

	if w.dispatcher == nil {
		w.logf("AutoDispatchWatcher: would trigger auto-dispatch for rig=%s (no dispatcher wired)", rig)
		return
	}

	if err := w.dispatcher.DispatchAutoDispatchForRig(rig, caller, session, agent); err != nil {
		w.logf("AutoDispatchWatcher: dispatch for rig=%s failed: %v", rig, err)
		return
	}

	w.logf("AutoDispatchWatcher: dispatched auto-dispatch for rig=%s (trigger=%s session=%s agent=%s)",
		rig, caller, session, agent)
}

// tryClaim returns true if the rig has not had an event-driven dispatch in
// the last rateLimit window, and records this as the new most-recent dispatch.
// Returns false if the rig is still within the rate-limit window.
func (w *AutoDispatchWatcher) tryClaim(rig string) bool {
	w.rateLimitMu.Lock()
	defer w.rateLimitMu.Unlock()

	now := time.Now()
	if last, ok := w.lastByRig[rig]; ok && now.Sub(last) < w.rateLimit {
		return false
	}
	w.lastByRig[rig] = now
	return true
}

// isPlannedPolecatExit returns true for session_death events that represent a
// polecat completing work cleanly via `gt done` (either COMPLETED or DEFERRED).
//
// The current SessionDeathPayload schema does not include a discrete exit_type
// field — we determine planned-ness from the caller + reason fields written
// by `gt done` in internal/cmd/done.go. Crash detection, zombie cleanup, idle
// reap, orphan cleanup, and town shutdown all use different callers/reasons
// and are deliberately excluded.
func isPlannedPolecatExit(caller, reason string) bool {
	switch caller {
	case "gt done":
		// Polecat completed normally. reason is "self-clean: done means idle"
		// for both COMPLETED and DEFERRED exits. The distinction between
		// those two doesn't matter here: both free a slot and warrant a
		// refill attempt.
		return true
	case "daemon":
		// Daemon-originated deaths are all problems — crash detection, idle
		// reap, mass death. The idle-reap path will naturally be picked up
		// by the cooldown fallback; we don't want to mask crashes by
		// immediately replacing them.
		return false
	case "gt doctor", "gt down":
		// Operator-initiated cleanup. Do not auto-refill.
		return false
	default:
		// Unknown caller — be conservative and skip. New callers should be
		// added explicitly so behavior is auditable.
		return false
	}
}

// rigFromAgent extracts the rig name from an agent identity string of the
// form "rig/polecats/name" or "rig/role" or similar. Returns "" if the
// input does not look like a polecat agent identity.
//
// We only return a rig when the agent is clearly a polecat — deacon, witness,
// mayor, refinery sessions dying do NOT free a polecat slot.
func rigFromAgent(agent string) string {
	if agent == "" {
		return ""
	}
	parts := strings.Split(agent, "/")
	// Expected polecat form: "<rig>/polecats/<name>"
	if len(parts) < 3 || parts[1] != "polecats" {
		return ""
	}
	rig := parts[0]
	if rig == "" {
		return ""
	}
	return rig
}

// handlerAutoDispatchConsumer is the production AutoDispatchConsumer that
// dispatches the auto-dispatch plugin via the daemon's handler plumbing.
// Built once per heartbeat; cheaper to rebuild than to share state across
// goroutines.
type handlerAutoDispatchConsumer struct {
	daemon *Daemon
}

// newHandlerAutoDispatchConsumer wires a consumer that uses the daemon's
// handler to dispatch the auto-dispatch plugin to an idle dog.
func newHandlerAutoDispatchConsumer(d *Daemon) *handlerAutoDispatchConsumer {
	return &handlerAutoDispatchConsumer{daemon: d}
}

// DispatchAutoDispatchForRig implements AutoDispatchConsumer.
func (c *handlerAutoDispatchConsumer) DispatchAutoDispatchForRig(rig, trigger, triggerSession, triggerAgent string) error {
	return c.daemon.dispatchAutoDispatchForRig(rig, trigger, triggerSession, triggerAgent)
}

// dispatchAutoDispatchForRig is the production path for event-driven
// auto-dispatch. It assigns work to an idle dog with a rig-scoped mail body
// that instructs the dog to only look at the specified rig, bypassing the
// cooldown gate entirely. The existing cooldown path in dispatchPlugins()
// still runs on heartbeat as the fallback.
//
// This is exported-to-package (lowercase but visible to the handler/daemon
// package) so the watcher can invoke it without circular imports.
func (d *Daemon) dispatchAutoDispatchForRig(rig, trigger, triggerSession, triggerAgent string) error {
	// Respect E-stop and shutdown — don't spawn new work during either.
	if d.isShutdownInProgress() {
		return fmt.Errorf("shutdown in progress")
	}

	// Check pressure: event-driven dispatch still respects the "dog" pressure
	// budget since it spawns a dog session. If pressure is blocking, defer to
	// the next planned heartbeat.
	if p := d.checkPressure("dog"); !p.OK {
		return fmt.Errorf("pressure deferral: %s", p.Reason)
	}

	rigsConfig, err := d.loadRigsConfig()
	if err != nil {
		return fmt.Errorf("loading rigs config: %w", err)
	}

	// Locate the auto-dispatch plugin.
	var rigNames []string
	if rigsConfig != nil {
		for name := range rigsConfig.Rigs {
			rigNames = append(rigNames, name)
		}
	}
	scanner := plugin.NewScanner(d.config.TownRoot, rigNames)
	p, err := scanner.GetPlugin(autoDispatchPluginName)
	if err != nil {
		// Plugin missing is not fatal for the overall system — some towns
		// may not have auto-dispatch installed.
		return fmt.Errorf("plugin %s not found: %w", autoDispatchPluginName, err)
	}

	// Build dog manager + session manager.
	mgr := dog.NewManager(d.config.TownRoot, rigsConfig)
	t := tmux.NewTmux()
	sm := dog.NewSessionManager(t, d.config.TownRoot, mgr)

	// Pick an idle dog using the same filter as the cooldown-gated
	// dispatchPlugins() path in handler.go: skip dogs whose state is idle
	// but whose tmux session is still live (the gt-o24 race where a dog is
	// reaped and marked idle before its session fully tears down), and skip
	// dogs currently in startup backoff (gu-ro75). Without these filters
	// sm.Start would either fail with "session already running" and lose the
	// event-driven dispatch without retry, or thrash a known-failing dog.
	idleDog := d.findDispatchableDog(mgr, sm)
	if idleDog == nil {
		// No dispatchable dog — classify the reason for observability so
		// operators can distinguish "pack is full" from "idle slot exists
		// but is wedged on a live session or backoff". Classification uses
		// the same state the filter inspected, so a race between the two is
		// harmless; the worst case is an outcome label that's one tick stale.
		outcome := classifyNoDispatchable(d, mgr, sm)
		_ = events.LogAudit(events.TypeAutoDispatchEventTriggered, "daemon",
			withExtra(events.AutoDispatchEventTriggeredPayload(rig, trigger, triggerSession, triggerAgent),
				"outcome", outcome))
		return fmt.Errorf("no dispatchable dogs available: %s", outcome)
	}

	// Assign work and send mail with a rig-scoped auto-dispatch body.
	workDesc := fmt.Sprintf("plugin:%s (event-driven, rig=%s)", p.Name, rig)
	if err := mgr.AssignWork(idleDog.Name, workDesc); err != nil {
		return fmt.Errorf("assigning work to dog %s: %w", idleDog.Name, err)
	}

	router := mail.NewRouterWithTownRoot(d.config.TownRoot, d.config.TownRoot)
	msg := mail.NewMessage(
		"daemon",
		fmt.Sprintf("deacon/dogs/%s", idleDog.Name),
		fmt.Sprintf("Plugin: %s (event-driven, rig=%s)", p.Name, rig),
		formatRigScopedAutoDispatchBody(p, rig, trigger),
	)
	msg.Type = mail.TypeTask
	msg.Timestamp = time.Now()
	if err := router.Send(msg); err != nil {
		if clearErr := mgr.ClearWork(idleDog.Name); clearErr != nil {
			d.logger.Printf("AutoDispatchWatcher: failed to roll back work assignment after mail failure: %v", clearErr)
		}
		return fmt.Errorf("sending mail to dog %s: %w", idleDog.Name, err)
	}

	// Emit daemon.plugin.dispatch audit event (additive — transport-split
	// foundation). Best-effort; errors are swallowed. See gu-zwui / gt-to45a
	// and docs/design/plugin-dispatch-transport.md.
	_ = events.LogAudit(
		events.TypeDaemonPluginDispatch,
		"daemon",
		events.DaemonPluginDispatchPayload(
			p.Name,
			rig,
			fmt.Sprintf("deacon/dogs/%s", idleDog.Name),
			"event-driven",
		),
	)

	if err := sm.Start(idleDog.Name, dog.SessionStartOptions{
		WorkDesc: workDesc,
	}); err != nil {
		// Track the failure so subsequent attempts (including the cooldown
		// heartbeat pass) back off. See gu-ro75.
		d.recordDogStartFailure(idleDog.Name)
		if clearErr := mgr.ClearWork(idleDog.Name); clearErr != nil {
			d.logger.Printf("AutoDispatchWatcher: failed to roll back work assignment after start failure: %v", clearErr)
		}
		return fmt.Errorf("starting session for dog %s: %w", idleDog.Name, err)
	}
	d.recordDogStartSuccess(idleDog.Name)

	// Record the dispatch so the cooldown gate in dispatchPlugins() sees it.
	// This prevents the next heartbeat from double-dispatching the same
	// auto-dispatch plugin globally after we already handled it event-driven.
	recorder := plugin.NewRecorder(d.config.TownRoot)
	if _, err := recorder.RecordRun(plugin.PluginRunRecord{
		PluginName: p.Name,
		Result:     plugin.ResultSuccess,
		Body:       fmt.Sprintf("Event-driven dispatch to dog %s (rig=%s, trigger=%s)", idleDog.Name, rig, trigger),
	}); err != nil {
		d.logger.Printf("AutoDispatchWatcher: failed to record dispatch for plugin %s: %v", p.Name, err)
	}

	// Emit observability event.
	_ = events.LogAudit(events.TypeAutoDispatchEventTriggered, "daemon",
		withExtra(events.AutoDispatchEventTriggeredPayload(rig, trigger, triggerSession, triggerAgent),
			"dog", idleDog.Name))

	return nil
}

// formatRigScopedAutoDispatchBody produces a mail body that tells the dog to
// run the auto-dispatch plugin but only for the specified rig. This is a
// best-effort scoping — the dog is an LLM agent and may still iterate all
// rigs. The per-rig recording of the dispatch (via recorder) ensures we
// don't over-fire even if the dog does more than we asked.
func formatRigScopedAutoDispatchBody(p *plugin.Plugin, rig, trigger string) string {
	var sb strings.Builder
	sb.WriteString("Execute the following plugin (event-driven, single-rig scope):\n\n")
	sb.WriteString(fmt.Sprintf("**Plugin**: %s\n", p.Name))
	sb.WriteString(fmt.Sprintf("**Description**: %s\n", p.Description))
	sb.WriteString(fmt.Sprintf("**Target rig**: %s\n", rig))
	sb.WriteString(fmt.Sprintf("**Trigger**: %s\n", trigger))
	if p.Execution != nil && p.Execution.Timeout != "" {
		sb.WriteString(fmt.Sprintf("**Timeout**: %s\n", p.Execution.Timeout))
	}
	sb.WriteString("\n---\n\n")
	sb.WriteString("## Instructions (rig-scoped)\n\n")
	sb.WriteString(fmt.Sprintf("A polecat in rig %q just completed work (%s). "+
		"Refill that rig's idle-slot now, bypassing the periodic cooldown gate.\n\n", rig, trigger))
	sb.WriteString(fmt.Sprintf("1. Run `gt polecat list %s` — count polecats in `idle` state\n", rig))
	sb.WriteString(fmt.Sprintf("2. If zero idle polecats, stop and record result:no_idle\n"))
	sb.WriteString(fmt.Sprintf("3. Run `cd ~/gt/%s && bd ready` to find open unblocked tasks\n", rig))
	sb.WriteString(fmt.Sprintf("4. If no ready tasks, stop and record result:no_work\n"))
	sb.WriteString("5. Pick the highest-priority ready task (P1 > P2 > P3). Skip beads where ANY of:\n")
	sb.WriteString("   - issue_type is `epic` or `convoy` (containers, not work)\n")
	sb.WriteString("   - title starts with `EPIC:` or `Epic:` (mis-typed container — gu-smr1)\n")
	sb.WriteString("   - title matches an identity/agent naming convention\n")
	sb.WriteString("   `gt sling` also enforces these filters and will reject bad beads with a clear error.\n")
	sb.WriteString(fmt.Sprintf("6. Run `gt sling <task-id> %s` — this auto-selects an idle polecat\n", rig))
	sb.WriteString("7. Dispatch at most ONE task\n\n")
	sb.WriteString("Do NOT iterate other rigs — this is a targeted event-driven refill for one rig only.\n\n")
	sb.WriteString("---\n\n")
	sb.WriteString("After completion:\n")
	sb.WriteString("1. Record a plugin-run receipt with `bd create --ephemeral` using labels " +
		"`type:plugin-run`, `plugin:" + p.Name + "`, `event-driven:1`, and `result:<outcome>`.\n")
	sb.WriteString("2. Run `gt dog done` — this clears your work and auto-terminates the session.\n")
	return sb.String()
}

// withExtra returns a copy of payload with an additional key/value merged in.
// Used to decorate auto-dispatch event payloads without mutating the helper.
func withExtra(payload map[string]interface{}, key, value string) map[string]interface{} {
	out := make(map[string]interface{}, len(payload)+1)
	for k, v := range payload {
		out[k] = v
	}
	out[key] = value
	return out
}

// classifyNoDispatchable explains *why* findDispatchableDog returned nil so
// the auto-dispatch observability event can distinguish these outcomes:
//
//   - "no_idle_dog":      no dog is in StateIdle at all (pack is full).
//   - "idle_session_live": at least one idle dog exists but its tmux session
//     is still running (the gt-o24 race). A retry on the next heartbeat is
//     expected to succeed once the session tears down.
//   - "dog_in_backoff":   every idle slot without a live session is currently
//     muted by the startup-failure backoff (gu-ro75).
//   - "unknown":          list failed or a transient classification race.
//
// The classifier is best-effort and intentionally ignores errors — it runs
// only on the nil path and its output is pure telemetry.
func classifyNoDispatchable(d *Daemon, mgr *dog.Manager, sm *dog.SessionManager) string {
	dogs, err := mgr.List()
	if err != nil {
		return "unknown"
	}
	var sawIdle, sawRunning, sawBackoff bool
	for _, dg := range dogs {
		if dg.State != dog.StateIdle {
			continue
		}
		sawIdle = true
		running, err := sm.IsRunning(dg.Name)
		if err == nil && running {
			sawRunning = true
			continue
		}
		if skip, _ := d.isDogInStartupBackoff(dg.Name); skip {
			sawBackoff = true
		}
	}
	switch {
	case !sawIdle:
		return "no_idle_dog"
	case sawRunning:
		return "idle_session_live"
	case sawBackoff:
		return "dog_in_backoff"
	default:
		return "unknown"
	}
}

// logf logs via the watcher's logger or stderr if unset.
func (w *AutoDispatchWatcher) logf(format string, args ...interface{}) {
	if w.logger == nil {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		return
	}
	w.logger.Printf(format, args...)
}

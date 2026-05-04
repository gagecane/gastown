package witness

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// SlotOpenCoalesceWindow is the time window over which SLOT_OPEN events are
// batched into a single Mayor nudge. The thundering-herd scenario (gu-ltqk /
// gt-7z4r0) sends 10+ SLOT_OPEN nudges in under a minute, which overwhelms
// the Mayor's per-session nudge flock (30s timeout) and causes dropped
// dispatches. Coalescing over a short window collapses bursts into a single
// nudge while keeping end-to-end latency far below the "5 minutes idle"
// acceptance bar.
const SlotOpenCoalesceWindow = 5 * time.Second

// slotOpenEvent is a single witness → Mayor SLOT_OPEN notification, buffered
// inside the coalescer until the debounce window elapses.
type slotOpenEvent struct {
	WorkDir     string // used by dispatch to resolve townRoot; not part of the dedup key
	RigName     string
	PolecatName string
	ExitType    string
}

// key returns the dedup key used to collapse repeated Add calls for the same
// polecat within one window. Exit type is included so a DEFERRED-then-
// COMPLETED transition (unlikely, but cheap to preserve) is not silently
// dropped. WorkDir is deliberately excluded: a single witness process is
// bound to one rig, so WorkDir is constant within a window.
func (e slotOpenEvent) key() string {
	return e.RigName + "/" + e.PolecatName + "|" + e.ExitType
}

// slotOpenDispatchFunc is the downstream delivery function invoked by the
// coalescer when the debounce window fires. It receives the ordered list of
// unique events collected since the window opened. The dispatch function is
// responsible for the actual tmux nudge, mail fallback, and town.log entries.
type slotOpenDispatchFunc func(events []slotOpenEvent)

// slotOpenCoalescer debounces SLOT_OPEN notifications so the Mayor receives
// at most one nudge per SlotOpenCoalesceWindow regardless of how many
// polecats complete in a burst.
//
// Thread-safe: Add may be called concurrently from multiple witness goroutines.
// The coalescer uses a single timer per active window — additional Add calls
// within the window buffer events without starting new timers.
type slotOpenCoalescer struct {
	window   time.Duration
	dispatch slotOpenDispatchFunc

	mu      sync.Mutex
	buf     map[string]slotOpenEvent // dedup by event.key()
	order   []string                 // insertion order of keys
	timer   *time.Timer              // nil when no window is open
	flushCh chan struct{}            // closed after each flush (tests only)
}

// newSlotOpenCoalescer constructs a coalescer with the given debounce window
// and dispatch callback.
func newSlotOpenCoalescer(window time.Duration, dispatch slotOpenDispatchFunc) *slotOpenCoalescer {
	return &slotOpenCoalescer{
		window:   window,
		dispatch: dispatch,
		buf:      make(map[string]slotOpenEvent),
	}
}

// Add records a SLOT_OPEN event. The first Add in an idle coalescer starts a
// timer; subsequent Adds within the window buffer the event and reuse the
// existing timer. When the timer fires, all buffered events are dispatched
// in insertion order and the coalescer returns to the idle state.
func (c *slotOpenCoalescer) Add(workDir, rigName, polecatName, exitType string) {
	ev := slotOpenEvent{
		WorkDir:     workDir,
		RigName:     rigName,
		PolecatName: polecatName,
		ExitType:    exitType,
	}
	key := ev.key()

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.buf[key]; !ok {
		c.order = append(c.order, key)
	}
	c.buf[key] = ev

	if c.timer == nil {
		c.timer = time.AfterFunc(c.window, c.flush)
	}
}

// Flush immediately dispatches any buffered events and resets state. Intended
// for shutdown paths and deterministic tests.
func (c *slotOpenCoalescer) Flush() {
	c.mu.Lock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	events := c.drainLocked()
	flushCh := c.flushCh
	c.flushCh = nil
	c.mu.Unlock()

	if len(events) > 0 && c.dispatch != nil {
		c.dispatch(events)
	}
	if flushCh != nil {
		close(flushCh)
	}
}

// flush is the timer callback. Separate from Flush so we can distinguish
// "timer fired" from "explicit flush" when reading stack traces, and so the
// timer callback never blocks dispatch behind the coalescer mutex.
func (c *slotOpenCoalescer) flush() {
	c.mu.Lock()
	c.timer = nil
	events := c.drainLocked()
	flushCh := c.flushCh
	c.flushCh = nil
	c.mu.Unlock()

	if len(events) > 0 && c.dispatch != nil {
		c.dispatch(events)
	}
	if flushCh != nil {
		close(flushCh)
	}
}

// drainLocked extracts buffered events in insertion order and clears the
// buffer. Caller must hold c.mu.
func (c *slotOpenCoalescer) drainLocked() []slotOpenEvent {
	if len(c.order) == 0 {
		return nil
	}
	events := make([]slotOpenEvent, 0, len(c.order))
	for _, key := range c.order {
		if ev, ok := c.buf[key]; ok {
			events = append(events, ev)
		}
	}
	c.buf = make(map[string]slotOpenEvent)
	c.order = c.order[:0]
	return events
}

// flushNotifyCh returns a channel that will be closed the next time the
// coalescer flushes (either via timer or Flush). Tests use this to wait for
// dispatch completion without sleeping. Only one waiter is supported per
// flush; later calls replace the channel.
func (c *slotOpenCoalescer) flushNotifyCh() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan struct{})
	c.flushCh = ch
	return ch
}

// slotOpenBatchMessage formats the nudge body for a coalesced batch of
// SLOT_OPEN events. For a single event it matches slotOpenMessage exactly so
// downstream Mayor parsing and log readability are preserved. For multiple
// events it produces a deduplicated, sorted summary.
func slotOpenBatchMessage(events []slotOpenEvent) string {
	switch len(events) {
	case 0:
		return ""
	case 1:
		ev := events[0]
		return slotOpenMessage(ev.RigName, ev.PolecatName, ev.ExitType)
	}

	// Multiple events: sorted, deduped identifiers for stable log output.
	seen := make(map[string]struct{}, len(events))
	ids := make([]string, 0, len(events))
	for _, ev := range events {
		id := fmt.Sprintf("%s/%s(exit=%s)", ev.RigName, ev.PolecatName, ev.ExitType)
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return fmt.Sprintf("SLOT_OPEN batch: %d slots opened (%s) — run `gt polecat list` to verify and sling next beads.",
		len(ids), strings.Join(ids, ", "))
}

// Package-level coalescer lazily initialized by notifyMayorSlotOpen. Tests
// typically construct their own coalescer via newSlotOpenCoalescer to
// exercise isolated timings rather than mutating the package-level
// instance.
var (
	slotOpenCoalescerOnce sync.Once
	slotOpenCoalescerInst *slotOpenCoalescer
)

// getSlotOpenCoalescer returns the package-level coalescer, creating it on
// first use with the production dispatch function.
func getSlotOpenCoalescer() *slotOpenCoalescer {
	slotOpenCoalescerOnce.Do(func() {
		slotOpenCoalescerInst = newSlotOpenCoalescer(SlotOpenCoalesceWindow, dispatchSlotOpenBatch)
	})
	return slotOpenCoalescerInst
}

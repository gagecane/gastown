package witness

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSlotOpenCoalescer_SingleEventFlushes verifies that a lone Add triggers
// exactly one dispatch after the window elapses.
func TestSlotOpenCoalescer_SingleEventFlushes(t *testing.T) {
	t.Parallel()

	var (
		batches [][]slotOpenEvent
		mu      sync.Mutex
	)
	dispatch := func(events []slotOpenEvent) {
		mu.Lock()
		defer mu.Unlock()
		batches = append(batches, append([]slotOpenEvent{}, events...))
	}

	c := newSlotOpenCoalescer(20*time.Millisecond, dispatch)
	done := c.flushNotifyCh()
	c.Add("/w", "rig-a", "polecat-1", "COMPLETED")

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("coalescer did not flush within 1s")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(batches) != 1 {
		t.Fatalf("want 1 dispatch batch, got %d", len(batches))
	}
	if len(batches[0]) != 1 {
		t.Fatalf("want 1 event in batch, got %d", len(batches[0]))
	}
	if got, want := batches[0][0].PolecatName, "polecat-1"; got != want {
		t.Errorf("polecat = %q, want %q", got, want)
	}
}

// TestSlotOpenCoalescer_BurstCollapses is the core acceptance-criteria test
// for gu-ltqk: a burst of 10 Adds within the window must produce exactly one
// dispatch with all 10 events, not 10 dispatches.
func TestSlotOpenCoalescer_BurstCollapses(t *testing.T) {
	t.Parallel()

	var dispatches int32
	var batchSize int32
	dispatch := func(events []slotOpenEvent) {
		atomic.AddInt32(&dispatches, 1)
		atomic.AddInt32(&batchSize, int32(len(events)))
	}

	c := newSlotOpenCoalescer(30*time.Millisecond, dispatch)
	done := c.flushNotifyCh()

	const burst = 10
	for i := 0; i < burst; i++ {
		c.Add("/w", "rig-a", polecatName(i), "COMPLETED")
	}

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("coalescer did not flush within 1s")
	}

	if got := atomic.LoadInt32(&dispatches); got != 1 {
		t.Errorf("dispatches = %d, want 1 (burst of %d must collapse to 1 nudge)", got, burst)
	}
	if got := atomic.LoadInt32(&batchSize); got != burst {
		t.Errorf("batchSize = %d, want %d", got, burst)
	}
}

// TestSlotOpenCoalescer_DedupWithinWindow verifies that repeated Adds for the
// same polecat within one window collapse to a single event. Prevents
// accidental double-counting if the witness handler fires twice (e.g.,
// mail-path + bead-path for the same completion).
func TestSlotOpenCoalescer_DedupWithinWindow(t *testing.T) {
	t.Parallel()

	var received []slotOpenEvent
	var mu sync.Mutex
	dispatch := func(events []slotOpenEvent) {
		mu.Lock()
		defer mu.Unlock()
		received = append([]slotOpenEvent{}, events...)
	}

	c := newSlotOpenCoalescer(20*time.Millisecond, dispatch)
	done := c.flushNotifyCh()

	// Same polecat, same exit: should collapse to one event.
	c.Add("/w", "rig-a", "pc-1", "COMPLETED")
	c.Add("/w", "rig-a", "pc-1", "COMPLETED")
	c.Add("/w", "rig-a", "pc-1", "COMPLETED")

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("coalescer did not flush within 1s")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Errorf("len(events) = %d, want 1 (dedup by polecat)", len(received))
	}
}

// TestSlotOpenCoalescer_SequentialWindows verifies that after a window flushes
// the coalescer returns to idle state and a subsequent Add starts a new
// window rather than being swallowed.
func TestSlotOpenCoalescer_SequentialWindows(t *testing.T) {
	t.Parallel()

	var dispatches int32
	dispatch := func(events []slotOpenEvent) {
		atomic.AddInt32(&dispatches, 1)
	}

	c := newSlotOpenCoalescer(15*time.Millisecond, dispatch)

	// First window.
	done1 := c.flushNotifyCh()
	c.Add("/w", "rig-a", "pc-1", "COMPLETED")
	select {
	case <-done1:
	case <-time.After(1 * time.Second):
		t.Fatal("first window did not flush")
	}

	// Second window — starts from idle.
	done2 := c.flushNotifyCh()
	c.Add("/w", "rig-a", "pc-2", "COMPLETED")
	select {
	case <-done2:
	case <-time.After(1 * time.Second):
		t.Fatal("second window did not flush")
	}

	if got := atomic.LoadInt32(&dispatches); got != 2 {
		t.Errorf("dispatches = %d, want 2 (one per window)", got)
	}
}

// TestSlotOpenCoalescer_ExplicitFlush verifies Flush() drains synchronously.
func TestSlotOpenCoalescer_ExplicitFlush(t *testing.T) {
	t.Parallel()

	var dispatches int32
	dispatch := func(events []slotOpenEvent) {
		atomic.AddInt32(&dispatches, 1)
	}

	// Long window so only Flush can trigger dispatch.
	c := newSlotOpenCoalescer(10*time.Minute, dispatch)
	c.Add("/w", "rig-a", "pc-1", "COMPLETED")

	c.Flush()

	if got := atomic.LoadInt32(&dispatches); got != 1 {
		t.Errorf("dispatches after Flush = %d, want 1", got)
	}

	// Second Flush on empty buffer is a no-op.
	c.Flush()
	if got := atomic.LoadInt32(&dispatches); got != 1 {
		t.Errorf("dispatches after empty Flush = %d, want 1 (must be no-op)", got)
	}
}

// TestSlotOpenCoalescer_ConcurrentAdds checks that concurrent Add calls are
// safe and all events land in the flushed batch.
func TestSlotOpenCoalescer_ConcurrentAdds(t *testing.T) {
	t.Parallel()

	var batches [][]slotOpenEvent
	var mu sync.Mutex
	dispatch := func(events []slotOpenEvent) {
		mu.Lock()
		defer mu.Unlock()
		batches = append(batches, append([]slotOpenEvent{}, events...))
	}

	c := newSlotOpenCoalescer(40*time.Millisecond, dispatch)
	done := c.flushNotifyCh()

	const workers = 20
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			c.Add("/w", "rig-a", polecatName(i), "COMPLETED")
		}(i)
	}
	wg.Wait()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("coalescer did not flush within 2s")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(batches) != 1 {
		t.Fatalf("dispatches = %d, want 1", len(batches))
	}
	if len(batches[0]) != workers {
		t.Errorf("events = %d, want %d (all concurrent Adds must land in one batch)", len(batches[0]), workers)
	}
}

// TestSlotOpenBatchMessage_Single reuses slotOpenMessage for single-event
// batches so the existing Mayor parsers and log formatters keep working.
func TestSlotOpenBatchMessage_Single(t *testing.T) {
	t.Parallel()
	batch := []slotOpenEvent{
		{RigName: "rig-a", PolecatName: "pc-1", ExitType: "COMPLETED"},
	}
	got := slotOpenBatchMessage(batch)
	want := slotOpenMessage("rig-a", "pc-1", "COMPLETED")
	if got != want {
		t.Errorf("single-event batch = %q, want %q (must match canonical slotOpenMessage)", got, want)
	}
}

// TestSlotOpenBatchMessage_Multiple verifies the batch format carries the
// count and a sorted, deduped list of polecats so log-readers can spot
// thundering-herd events at a glance.
func TestSlotOpenBatchMessage_Multiple(t *testing.T) {
	t.Parallel()
	batch := []slotOpenEvent{
		{RigName: "rig-b", PolecatName: "zeta", ExitType: "COMPLETED"},
		{RigName: "rig-a", PolecatName: "alpha", ExitType: "DEFERRED"},
		{RigName: "rig-a", PolecatName: "alpha", ExitType: "DEFERRED"}, // dup
	}
	got := slotOpenBatchMessage(batch)

	if !strings.HasPrefix(got, "SLOT_OPEN batch: 2 slots opened") {
		t.Errorf("batch prefix wrong: %q", got)
	}
	// Alphabetic sort: rig-a/alpha < rig-b/zeta.
	idxA := strings.Index(got, "rig-a/alpha")
	idxB := strings.Index(got, "rig-b/zeta")
	if idxA < 0 || idxB < 0 {
		t.Fatalf("batch missing ids: %q", got)
	}
	if idxA > idxB {
		t.Errorf("batch not sorted: rig-a appears after rig-b in %q", got)
	}
	if !strings.Contains(got, "gt polecat list") {
		t.Errorf("batch missing operator guidance: %q", got)
	}
}

// TestSlotOpenBatchMessage_Empty returns an empty string for empty input
// (defensive; the dispatch path never invokes with zero events but the
// helper should not panic).
func TestSlotOpenBatchMessage_Empty(t *testing.T) {
	t.Parallel()
	if got := slotOpenBatchMessage(nil); got != "" {
		t.Errorf("empty batch = %q, want empty", got)
	}
}

// TestSlotOpenMailBody_Multiple verifies the mail fallback body lists every
// polecat in the batch so the Mayor can reconstruct all freed slots from
// mail if the nudge path was unavailable.
func TestSlotOpenMailBody_Multiple(t *testing.T) {
	t.Parallel()
	batch := []slotOpenEvent{
		{RigName: "rig-a", PolecatName: "alpha", ExitType: "COMPLETED"},
		{RigName: "rig-b", PolecatName: "beta", ExitType: "DEFERRED"},
	}
	body := slotOpenMailBody(batch)
	if !strings.Contains(body, "2 polecat slots") {
		t.Errorf("body missing count: %q", body)
	}
	if !strings.Contains(body, "rig-a/alpha") || !strings.Contains(body, "rig-b/beta") {
		t.Errorf("body missing polecats: %q", body)
	}
}

// TestLogSlotOpenNudgeFailure_WritesTownLog is the regression test for the
// gu-ltqk observability gap: nudge delivery failures must appear in
// town.log, not only in `gt nudge` stderr.
//
// townlog truncates EventNudge context to 50 characters in its
// human-readable output, so we verify the front-loaded markers (FAILED,
// err=, and the underlying error class) survive truncation.
func TestLogSlotOpenNudgeFailure_WritesTownLog(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	logSlotOpenNudgeFailure(townRoot, "hq-mayor", "SLOT_OPEN: rig-a/pc-1 completed (exit=COMPLETED)", errFake("deadline exceeded"))

	logPath := filepath.Join(townRoot, "logs", "town.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading %s: %v", logPath, err)
	}
	content := string(data)
	if !strings.Contains(content, "[nudge]") {
		t.Errorf("town.log missing [nudge] marker: %q", content)
	}
	if !strings.Contains(content, "FAILED") {
		t.Errorf("town.log missing FAILED marker: %q", content)
	}
	if !strings.Contains(content, "deadline exceeded") {
		t.Errorf("town.log missing underlying error (front-loaded so truncation does not hide it): %q", content)
	}
}

// TestLogSlotOpenMailFailure_WritesTownLog covers the hard-failure path:
// both nudge AND mail fallback failed. Operators must see this in audit.
func TestLogSlotOpenMailFailure_WritesTownLog(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	logSlotOpenMailFailure(townRoot, "hq-mayor", "SLOT_OPEN batch: 3 slots opened", errFake("mail send failed"))

	logPath := filepath.Join(townRoot, "logs", "town.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading %s: %v", logPath, err)
	}
	content := string(data)
	if !strings.Contains(content, "FAILED") {
		t.Errorf("town.log missing FAILED marker: %q", content)
	}
	if !strings.Contains(content, "mail") {
		t.Errorf("town.log missing mail marker: %q", content)
	}
	if !strings.Contains(content, "mail send failed") {
		t.Errorf("town.log missing underlying mail error: %q", content)
	}
}

// TestLogSlotOpenMailFallback_WritesTownLog covers the soft-failure path
// where the nudge path was skipped (no tmux / ACP) but mail delivery
// succeeded.
func TestLogSlotOpenMailFallback_WritesTownLog(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	logSlotOpenMailFallback(townRoot, "hq-mayor", "SLOT_OPEN: rig-a/pc-1 completed (exit=COMPLETED)")

	logPath := filepath.Join(townRoot, "logs", "town.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading %s: %v", logPath, err)
	}
	content := string(data)
	if !strings.Contains(content, "mail-fallback") {
		t.Errorf("town.log missing mail-fallback marker: %q", content)
	}
	// Not a FAILED entry — distinguishes success-via-mail from true failure.
	if strings.Contains(content, "FAILED") {
		t.Errorf("town.log erroneously marked success as FAILED: %q", content)
	}
}

// TestLogSlotOpenFailure_EmptyTownRootIsNoop mirrors the empty-townRoot
// safety invariant already asserted for logSlotOpenNudge.
func TestLogSlotOpenFailure_EmptyTownRootIsNoop(t *testing.T) {
	t.Parallel()
	// Should not panic or create files in a bogus location.
	logSlotOpenNudgeFailure("", "hq-mayor", "msg", errFake("x"))
	logSlotOpenMailFallback("", "hq-mayor", "subject")
	logSlotOpenMailFailure("", "hq-mayor", "subject", errFake("y"))
}

// Helpers.

func polecatName(i int) string {
	names := []string{
		"alpha", "bravo", "charlie", "delta", "echo",
		"foxtrot", "golf", "hotel", "india", "juliet",
		"kilo", "lima", "mike", "november", "oscar",
		"papa", "quebec", "romeo", "sierra", "tango",
	}
	if i < 0 || i >= len(names) {
		return "unknown"
	}
	return names[i]
}

type errFake string

func (e errFake) Error() string { return string(e) }

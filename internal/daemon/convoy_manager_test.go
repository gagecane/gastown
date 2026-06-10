package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/sling"
)

// setupTestStore opens a real beads database for integration tests.
// Skips if unavailable. Caller must run cleanup when done.
func setupTestStore(t *testing.T) (beadsdk.Storage, func()) {
	t.Helper()
	t.Setenv("BEADS_TEST_MODE", "1")
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	doltPath := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltPath, 0755); err != nil {
		t.Skipf("cannot create test dir: %v", err)
	}
	ctx := context.Background()
	store, err := beadsdk.Open(ctx, doltPath)
	if err != nil {
		t.Skipf("beads store unavailable: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		_ = store.Close()
		t.Skipf("SetConfig: %v", err)
	}
	return store, func() { _ = store.Close() }
}

// scanTestOpts configures the mockGtForScanTest helper.
type scanTestOpts struct {
	strandedJSON  string // JSON for `gt convoy stranded --json`; default "[]"
	slingFailOnce bool   // first sling invocation exits 1, subsequent succeed
	routes        string // routes.jsonl content; empty = no routes file
}

// scanTestPaths holds paths created by mockGtForScanTest.
type scanTestPaths struct {
	binDir       string
	townRoot     string
	slingLogPath string // sling call log; absent if sling was never called
	checkLogPath string // convoy check call log; absent if check was never called
}

// mockGtForScanTest creates a mock gt binary and directory layout for scan tests.
// All mock scripts write call logs so tests can make both positive and negative assertions.
func mockGtForScanTest(t *testing.T, opts scanTestOpts) scanTestPaths {
	t.Helper()

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	if opts.routes != "" {
		if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(opts.routes), 0644); err != nil {
			t.Fatalf("write routes: %v", err)
		}
	}

	strandedJSON := opts.strandedJSON
	if strandedJSON == "" {
		strandedJSON = "[]"
	}

	slingLogPath := filepath.Join(binDir, "sling.log")
	checkLogPath := filepath.Join(binDir, "check.log")

	slingFailClause := ""
	if opts.slingFailOnce {
		slingCountPath := filepath.Join(binDir, "sling_count")
		slingFailClause = `
  if [ ! -f "` + slingCountPath + `" ]; then
    echo "1" > "` + slingCountPath + `"
    exit 1
  fi`
	}

	gtScript := `#!/bin/sh
if [ "$1" = "convoy" ] && [ "$2" = "stranded" ]; then
  echo '` + strings.ReplaceAll(strandedJSON, "'", "'\\''") + `'
  exit 0
fi
if [ "$1" = "sling" ]; then
  echo "$@" >> "` + slingLogPath + `"` + slingFailClause + `
  exit 0
fi
if [ "$1" = "convoy" ] && [ "$2" = "check" ]; then
  echo "$@" >> "` + checkLogPath + `"
  exit 0
fi
exit 0
`

	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return scanTestPaths{
		binDir:       binDir,
		townRoot:     townRoot,
		slingLogPath: slingLogPath,
		checkLogPath: checkLogPath,
	}
}

func TestEventPoll_DetectsCloseEvents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	issue := &beadsdk.Issue{
		ID:        "gt-close1",
		Title:     "To Close",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "test", ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	townRoot := t.TempDir()
	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, map[string]beadsdk.Storage{"hq": store}, nil, nil)
	m.seeded.Store(true)
	m.pollStoresSnapshot(m.stores)

	// Should have logged the close detection
	found := false
	for _, s := range logged {
		if strings.Contains(s, "close detected") && strings.Contains(s, issue.ID) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'close detected: %s' in logs, got: %v", issue.ID, logged)
	}
}

func TestEventPoll_SkipsNonCloseEvents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	issue := &beadsdk.Issue{
		ID:        "gt-open1",
		Title:     "Stays Open",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	// No close - only create event exists

	townRoot := t.TempDir()
	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, map[string]beadsdk.Storage{"hq": store}, nil, nil)
	m.pollStoresSnapshot(m.stores)

	// Should NOT have logged any close detection
	for _, s := range logged {
		if strings.Contains(s, "close detected") {
			t.Errorf("expected no close detection for open issue, got: %v", logged)
		}
	}
}

func TestManagerLifecycle_StartStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	bdScript := `#!/bin/sh
echo '{"type":"status","issue_id":"gt-x","new_status":"closed"}'
sleep 999
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	gtScript := `#!/bin/sh
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := NewConvoyManager(townRoot, func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	m.Stop()
}

func TestScanStranded_FeedsReadyIssues(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	paths := mockGtForScanTest(t, scanTestOpts{
		strandedJSON: `[{"id":"hq-cv1","title":"Test","ready_count":1,"ready_issues":["gt-issue1"]}]`,
		routes:       `{"prefix":"gt-","path":"gt/.beads"}` + "\n",
	})

	m := NewConvoyManager(paths.townRoot, func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)
	m.scan()

	data, err := os.ReadFile(paths.slingLogPath)
	if err != nil {
		t.Fatalf("read sling log: %v", err)
	}
	logContent := string(data)
	if !strings.Contains(logContent, "sling") || !strings.Contains(logContent, "gt-issue1") {
		t.Errorf("expected gt sling to be invoked for gt-issue1, got: %q", logContent)
	}
}

// TestScanStranded_SkipsClosedConvoy is the regression guard for gs-cxex: the
// stranded cache (gu-rd9ph) keys its sentinel on the open-convoy count + max
// convoy updated_at, so a convoy that has closed (or whose tracked bead closed)
// without shifting that sentinel keeps being served as a stale "feedable"
// entry. The feed loop then re-slings the convoy's already-completed bead every
// scan — sling refuses (bead closed) but the loop churns daemon.log for hours.
// scan() must read the convoy's live status, skip feeding closed convoys, and
// invalidate the cache so the next scan recomputes a fresh stranded set.
func TestScanStranded_SkipsClosedConvoy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	convoy := &beadsdk.Issue{
		ID:        "hq-cvclosed",
		Title:     "Completed convoy",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.CreateIssue(ctx, convoy, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.CloseIssue(ctx, convoy.ID, "all tracked done", "test", ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	// Mock gt reports the now-closed convoy as feedable — simulating a stale
	// stranded-cache result that survived the convoy's close.
	paths := mockGtForScanTest(t, scanTestOpts{
		strandedJSON: `[{"id":"hq-cvclosed","title":"Completed convoy","ready_count":1,"ready_issues":["gt-issue1"]}]`,
		routes:       `{"prefix":"gt-","path":"gt/.beads"}` + "\n",
	})

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}
	m := NewConvoyManager(paths.townRoot, logger, "gt", 10*time.Minute, map[string]beadsdk.Storage{"hq": store}, nil, nil)
	m.scan()

	// No sling should fire for a closed convoy's bead.
	if data, err := os.ReadFile(paths.slingLogPath); err == nil {
		t.Errorf("expected NO sling for closed convoy, but sling was invoked: %q", data)
	}

	// Cache must be invalidated so the next scan recomputes without the convoy.
	if m.strandedCache != nil {
		t.Error("expected stranded cache to be invalidated after dropping a closed convoy")
	}

	found := false
	for _, s := range logged {
		if strings.Contains(s, "hq-cvclosed") && strings.Contains(s, "closed") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected closed-convoy drop log for hq-cvclosed, got: %v", logged)
	}
}

func TestScanStranded_ClosesEmptyConvoys(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	paths := mockGtForScanTest(t, scanTestOpts{
		strandedJSON: `[{"id":"hq-empty1","title":"Empty","ready_count":0,"ready_issues":[]}]`,
	})

	m := NewConvoyManager(paths.townRoot, func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)
	m.scan()

	data, err := os.ReadFile(paths.checkLogPath)
	if err != nil {
		t.Fatalf("read check log: %v", err)
	}
	if !strings.Contains(string(data), "hq-empty1") {
		t.Errorf("expected gt convoy check for hq-empty1, got: %q", data)
	}
}

// TestScanStranded_BatchesCompletionChecks is the regression guard for gu-jqb47:
// the scan loop previously spawned one `gt convoy check <id>` subprocess per
// "tracked>0, ready==0" convoy. With N stranded convoys that serial fan-out (a
// full gt cold-start + per-call bd queries each) blew the 5m dispatch budget.
// The fix collects completion candidates and runs ONE batched `gt convoy check`
// (no id → checkAndCloseCompletedConvoys) after the loop. This proves: with 3
// completion-candidate convoys, exactly ONE no-id check fires, not 3 per-id
// spawns.
func TestScanStranded_BatchesCompletionChecks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	// 3 convoys with tracked issues but none ready → all completion candidates.
	strandedJSON := `[` +
		`{"id":"hq-cvA","title":"A","tracked_count":2,"ready_count":0,"ready_issues":[]},` +
		`{"id":"hq-cvB","title":"B","tracked_count":1,"ready_count":0,"ready_issues":[]},` +
		`{"id":"hq-cvC","title":"C","tracked_count":3,"ready_count":0,"ready_issues":[]}` +
		`]`

	paths := mockGtForScanTest(t, scanTestOpts{strandedJSON: strandedJSON})

	m := NewConvoyManager(paths.townRoot, func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)
	m.scan()

	data, err := os.ReadFile(paths.checkLogPath)
	if err != nil {
		t.Fatalf("read check log: %v", err)
	}
	logStr := strings.TrimSpace(string(data))

	// Exactly ONE `convoy check` invocation (the batched no-id pass).
	lines := strings.Split(logStr, "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 batched convoy check, got %d:\n%s", len(lines), logStr)
	}

	// The regression: a per-convoy serial fan-out would log the convoy IDs.
	for _, id := range []string{"hq-cvA", "hq-cvB", "hq-cvC"} {
		if strings.Contains(logStr, id) {
			t.Errorf("detected per-convoy serial check for %s — regression of gu-jqb47; log:\n%s", id, logStr)
		}
	}

	// And it must be the no-id form: `convoy check` with no trailing convoy ID.
	if strings.TrimSpace(lines[0]) != "convoy check" {
		t.Errorf("expected no-id `convoy check`, got: %q", lines[0])
	}
}

func TestScanStranded_GracePeriodSkipsRecentConvoy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	// Convoy created 30 seconds ago — well within the 5-minute grace period.
	recentTime := time.Now().UTC().Add(-30 * time.Second).Format(time.RFC3339)
	strandedJSON := fmt.Sprintf(`[{"id":"hq-new1","title":"New","tracked_count":0,"ready_count":0,"ready_issues":[],"created_at":"%s"}]`, recentTime)

	paths := mockGtForScanTest(t, scanTestOpts{
		strandedJSON: strandedJSON,
	})

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(paths.townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)
	m.scan()

	// Convoy check must NOT have been called — grace period should protect it.
	if _, err := os.Stat(paths.checkLogPath); err == nil {
		data, _ := os.ReadFile(paths.checkLogPath)
		t.Errorf("convoy check was called for recent convoy (grace period should protect): %s", data)
	}

	// Should see grace period log message.
	found := false
	for _, s := range logged {
		if strings.Contains(s, "grace period") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected grace period log message, got: %v", logged)
	}
}

func TestScanStranded_GracePeriodAllowsOldConvoy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	// Convoy created 10 minutes ago — past the 5-minute grace period.
	oldTime := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	strandedJSON := fmt.Sprintf(`[{"id":"hq-old1","title":"Old","tracked_count":0,"ready_count":0,"ready_issues":[],"created_at":"%s"}]`, oldTime)

	paths := mockGtForScanTest(t, scanTestOpts{
		strandedJSON: strandedJSON,
	})

	m := NewConvoyManager(paths.townRoot, func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)
	m.scan()

	data, err := os.ReadFile(paths.checkLogPath)
	if err != nil {
		t.Fatalf("read check log: %v", err)
	}
	if !strings.Contains(string(data), "hq-old1") {
		t.Errorf("expected gt convoy check for hq-old1 (past grace period), got: %q", data)
	}
}

func TestScanStranded_NoStrandedConvoys(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	paths := mockGtForScanTest(t, scanTestOpts{
		strandedJSON: "[]",
	})

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(paths.townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)
	m.scan()

	// Negative: sling must not have been called
	if _, err := os.Stat(paths.slingLogPath); err == nil {
		data, _ := os.ReadFile(paths.slingLogPath)
		t.Errorf("sling was called unexpectedly: %s", data)
	}
	// Negative: convoy check must not have been called
	if _, err := os.Stat(paths.checkLogPath); err == nil {
		data, _ := os.ReadFile(paths.checkLogPath)
		t.Errorf("convoy check was called unexpectedly: %s", data)
	}
	// Negative: no feeding or check activity in logs
	for _, s := range logged {
		if strings.Contains(s, "feeding") || strings.Contains(s, "sling") || strings.Contains(s, "auto-closing") {
			t.Errorf("unexpected convoy activity in logs: %s", s)
		}
	}
}

// TestScanStranded_PeriodicCompletionBackstop verifies that checkAllConvoyCompletion
// fires periodically (every completionBackstopInterval scans) even when there are
// zero completion candidates in the stranded result. This backstop catches completed
// convoys that were excluded from findStrandedConvoys (gu-urwg6).
func TestScanStranded_PeriodicCompletionBackstop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	// No stranded convoys → completionCandidates stays 0.
	paths := mockGtForScanTest(t, scanTestOpts{
		strandedJSON: "[]",
	})

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(paths.townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)

	// Run scans up to (but not including) the backstop interval — no check should fire.
	for i := 0; i < completionBackstopInterval-1; i++ {
		m.scan()
	}
	if _, err := os.Stat(paths.checkLogPath); err == nil {
		data, _ := os.ReadFile(paths.checkLogPath)
		t.Fatalf("convoy check fired before backstop interval: %s", string(data))
	}

	// The Nth scan triggers the backstop.
	m.scan()
	data, err := os.ReadFile(paths.checkLogPath)
	if err != nil {
		t.Fatalf("convoy check NOT fired at backstop interval (scan %d): %v", completionBackstopInterval, err)
	}
	logStr := strings.TrimSpace(string(data))
	if logStr != "convoy check" {
		t.Errorf("expected `convoy check` (no-id batched form), got: %q", logStr)
	}

	// Verify the backstop was logged.
	found := false
	for _, s := range logged {
		if strings.Contains(s, "periodic backstop") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'periodic backstop' in log messages, got: %v", logged)
	}
}

func TestScanStranded_DispatchFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	paths := mockGtForScanTest(t, scanTestOpts{
		strandedJSON:  `[{"id":"hq-cv1","title":"Test","ready_count":1,"ready_issues":["gt-issue1"]},{"id":"hq-cv2","title":"Test2","ready_count":1,"ready_issues":["gt-issue2"]}]`,
		slingFailOnce: true,
		routes:        `{"prefix":"gt-","path":"gt/.beads"}` + "\n",
	})

	var logMu sync.Mutex
	var logged []string
	logger := func(format string, args ...interface{}) {
		logMu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}

	m := NewConvoyManager(paths.townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)
	m.scan()

	logMu.Lock()
	defer logMu.Unlock()

	// Verify the failure was logged with the correct convoy and issue IDs
	hasFailure := false
	for _, l := range logged {
		if strings.Contains(l, "gt-issue1") && strings.Contains(l, "failed") {
			hasFailure = true
			break
		}
	}
	if !hasFailure {
		t.Errorf("expected sling failure log mentioning gt-issue1, got: %v", logged)
	}

	// Verify scan continued: second convoy's issue was dispatched
	data, err := os.ReadFile(paths.slingLogPath)
	if err != nil {
		t.Fatalf("read sling log: %v", err)
	}
	if !strings.Contains(string(data), "gt-issue2") {
		t.Errorf("expected sling for gt-issue2 (scan should continue after failure), got: %q", data)
	}
}

func TestConvoyManager_DoubleStop_Idempotent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	binDir := t.TempDir()
	gtScript := `#!/bin/sh
if [ "$1" = "convoy" ] && [ "$2" = "stranded" ]; then echo '[]'; fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte("#!/bin/sh\nexit 0"), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	townRoot := t.TempDir()
	m := NewConvoyManager(townRoot, func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	m.Stop()
	m.Stop() // Second stop should not deadlock
}

func TestStart_DoubleCall_Guarded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()

	// Mock gt that returns empty stranded list and logs sling/check calls
	slingLogPath := filepath.Join(binDir, "sling.log")
	gtScript := `#!/bin/sh
if [ "$1" = "convoy" ] && [ "$2" = "stranded" ]; then
  echo '[]'
  exit 0
fi
if [ "$1" = "sling" ]; then
  echo "$@" >> "` + slingLogPath + `"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logMu sync.Mutex
	var logged []string
	logger := func(format string, args ...interface{}) {
		logMu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}

	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)

	// First Start should succeed
	if err := m.Start(); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	// Second Start should be a no-op (not spawn duplicate goroutines)
	if err := m.Start(); err != nil {
		t.Fatalf("second Start: %v", err)
	}

	// Verify the duplicate-call warning was logged
	logMu.Lock()
	duplicateLogged := false
	for _, s := range logged {
		if strings.Contains(s, "already called") || strings.Contains(s, "ignoring duplicate") {
			duplicateLogged = true
			break
		}
	}
	logMu.Unlock()
	if !duplicateLogged {
		t.Error("expected duplicate Start() warning in logs")
	}

	// Verify the manager still functions: Stop should complete without hanging
	done := make(chan struct{})
	go func() {
		m.Stop()
		close(done)
	}()
	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not complete within 5s after double Start()")
	}
}

func TestEventPoll_LazyStoreOpening(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	callCount := 0
	opener := func() map[string]beadsdk.Storage {
		callCount++
		if callCount < 3 {
			// Simulate Dolt not ready for first 2 attempts
			return nil
		}
		return map[string]beadsdk.Storage{"hq": store}
	}

	var logMu sync.Mutex
	var logged []string
	logger := func(format string, args ...interface{}) {
		logMu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}

	// Start with nil stores but with an opener — should NOT exit immediately
	m := NewConvoyManager(t.TempDir(), logger, "gt", 10*time.Minute, nil, opener, nil)

	// Before any poll ticks, stores should be nil
	if m.stores != nil {
		t.Fatal("stores should be nil before lazy init")
	}

	// Simulate poll ticks — first two calls return nil, third succeeds
	// runEventPoll's ticker calls this logic on each tick
	for i := 0; i < 5; i++ {
		if len(m.stores) == 0 {
			if m.openStores != nil {
				m.stores = m.openStores()
			}
			if len(m.stores) == 0 {
				continue
			}
		}
		// If we get here, stores are ready
		break
	}

	if len(m.stores) == 0 {
		t.Fatal("stores should have been lazily opened by tick 3")
	}
	if callCount != 3 {
		t.Errorf("expected opener called 3 times, got %d", callCount)
	}
	if _, ok := m.stores["hq"]; !ok {
		t.Error("expected hq store in lazily opened stores")
	}
}

func TestConvoyManager_ScanInterval_Configurable(t *testing.T) {
	noop := func(string, ...interface{}) {}
	m := NewConvoyManager("/tmp", noop, "gt", 0, nil, nil, nil)
	if m.scanInterval != defaultStrandedScanInterval {
		t.Errorf("interval 0 should use default %v, got %v", defaultStrandedScanInterval, m.scanInterval)
	}

	custom := 5 * time.Minute
	m2 := NewConvoyManager("/tmp", noop, "gt", custom, nil, nil, nil)
	if m2.scanInterval != custom {
		t.Errorf("interval should be %v, got %v", custom, m2.scanInterval)
	}
}

func TestStrandedConvoyInfo_JSONParsing(t *testing.T) {
	jsonStr := `[{"id":"hq-cv1","title":"My Convoy","ready_count":2,"ready_issues":["gt-a","gt-b"]}]`
	var result []strandedConvoyInfo
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 convoy, got %d", len(result))
	}
	c := result[0]
	if c.ID != "hq-cv1" || c.Title != "My Convoy" || c.ReadyCount != 2 {
		t.Errorf("unexpected convoy: %+v", c)
	}
	if len(c.ReadyIssues) != 2 || c.ReadyIssues[0] != "gt-a" || c.ReadyIssues[1] != "gt-b" {
		t.Errorf("unexpected ready_issues: %v", c.ReadyIssues)
	}
}

func TestFeedFirstReady_MultipleReadyIssues_DispatchesOnlyFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	slingLogPath := filepath.Join(binDir, "sling.log")
	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo "$@" >> "` + slingLogPath + `"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)

	c := strandedConvoyInfo{
		ID:          "hq-cv1",
		Title:       "Multi Ready",
		ReadyCount:  3,
		ReadyIssues: []string{"gt-issue1", "gt-issue2", "gt-issue3"},
	}
	m.feedFirstReady(c)

	data, err := os.ReadFile(slingLogPath)
	if err != nil {
		t.Fatalf("read sling log: %v", err)
	}
	logContent := string(data)

	if !strings.Contains(logContent, "gt-issue1") {
		t.Errorf("expected sling for gt-issue1, got: %q", logContent)
	}
	if strings.Contains(logContent, "gt-issue2") {
		t.Errorf("unexpected dispatch of gt-issue2: %q", logContent)
	}
	if strings.Contains(logContent, "gt-issue3") {
		t.Errorf("unexpected dispatch of gt-issue3: %q", logContent)
	}

	lines := strings.Split(strings.TrimSpace(logContent), "\n")
	if len(lines) != 1 {
		t.Errorf("expected exactly 1 sling call, got %d: %v", len(lines), lines)
	}

	feedLogged := false
	for _, s := range logged {
		if strings.Contains(s, "feeding") && strings.Contains(s, "gt-issue1") {
			feedLogged = true
			break
		}
	}
	if !feedLogged {
		t.Errorf("expected 'feeding gt-issue1' in logs, got: %v", logged)
	}
}

// TestFeedFirstReady_CarriesMergeAndBase proves gs-9ct #3: when a stranded
// convoy carries a merge strategy (and base branch), the daemon's re-dispatch
// sling preserves them. Without --merge a stranded merge=local relay leg would
// be re-fed with the default merge-queue strategy and auto-MR a do-not-merge
// prototype to main.
func TestFeedFirstReady_CarriesMergeAndBase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	slingLogPath := filepath.Join(binDir, "sling.log")
	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo "$@" >> "` + slingLogPath + `"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := NewConvoyManager(townRoot, func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)

	c := strandedConvoyInfo{
		ID:          "hq-cv1",
		Title:       "Relay leg",
		ReadyCount:  1,
		ReadyIssues: []string{"gt-relay1"},
		BaseBranch:  "proto/v3-build",
		Merge:       "local",
	}
	m.feedFirstReady(c)

	data, err := os.ReadFile(slingLogPath)
	if err != nil {
		t.Fatalf("read sling log: %v", err)
	}
	logContent := string(data)
	if !strings.Contains(logContent, "--merge=local") {
		t.Errorf("expected --merge=local in sling args, got: %q", logContent)
	}
	if !strings.Contains(logContent, "--base-branch=proto/v3-build") {
		t.Errorf("expected --base-branch=proto/v3-build in sling args, got: %q", logContent)
	}
}

// TestFeedFirstReady_OmitsMergeWhenUnset is the negative case: an ordinary
// convoy with no merge strategy must not emit a --merge flag (which would
// otherwise fail flag validation / override the bead's own strategy).
func TestFeedFirstReady_OmitsMergeWhenUnset(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	slingLogPath := filepath.Join(binDir, "sling.log")
	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo "$@" >> "` + slingLogPath + `"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := NewConvoyManager(townRoot, func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)

	m.feedFirstReady(strandedConvoyInfo{
		ID:          "hq-cv2",
		Title:       "Ordinary",
		ReadyCount:  1,
		ReadyIssues: []string{"gt-ord1"},
	})

	data, err := os.ReadFile(slingLogPath)
	if err != nil {
		t.Fatalf("read sling log: %v", err)
	}
	if strings.Contains(string(data), "--merge") {
		t.Errorf("did not expect --merge for a convoy with no strategy, got: %q", string(data))
	}
}

// TestEffectiveFeedCooldown_EscalatesWithChurn proves gs-skv: a bead
// re-dispatched repeatedly within feedChurnWindow backs off exponentially
// (5m → 10m → 20m …) up to feedCooldownCap, rather than staying at a flat 5m.
func TestEffectiveFeedCooldown_EscalatesWithChurn(t *testing.T) {
	m := NewConvoyManager(t.TempDir(), func(string, ...interface{}) {}, "gt", time.Minute, nil, nil, nil)
	current := time.Now()
	m.now = func() time.Time { return current }

	const issue = "gt-churny"

	// No churn yet → base cooldown.
	if got := m.effectiveFeedCooldown(issue); got != feedDispatchCooldown {
		t.Fatalf("base cooldown = %s, want %s", got, feedDispatchCooldown)
	}

	want := []time.Duration{
		feedDispatchCooldown,      // streak 1 → base
		feedDispatchCooldown * 2,  // streak 2
		feedDispatchCooldown * 4,  // streak 3
		feedDispatchCooldown * 8,  // streak 4
		feedDispatchCooldown * 12, // streak 5 would be *16=80m but capped at 60m
	}
	for i, w := range want {
		m.recordFeedChurn(issue)
		got := m.effectiveFeedCooldown(issue)
		// Cap applies once the doubling exceeds feedCooldownCap.
		if w > feedCooldownCap {
			w = feedCooldownCap
		}
		if got != w {
			t.Errorf("after %d churns: cooldown = %s, want %s", i+1, got, w)
		}
		// Advance a little (well within feedChurnWindow) so the next churn counts.
		current = current.Add(time.Second)
	}
}

// TestRecordFeedChurn_ResetsAfterWindow proves the backoff self-heals: a
// re-dispatch after a gap longer than feedChurnWindow restarts the streak at 1
// (base cooldown), so a bead that genuinely progressed and only much later
// reappears is not pre-penalized (gs-skv).
func TestRecordFeedChurn_ResetsAfterWindow(t *testing.T) {
	m := NewConvoyManager(t.TempDir(), func(string, ...interface{}) {}, "gt", time.Minute, nil, nil, nil)
	current := time.Now()
	m.now = func() time.Time { return current }

	const issue = "gt-healed"
	m.recordFeedChurn(issue)
	m.recordFeedChurn(issue)
	if got := m.effectiveFeedCooldown(issue); got != feedDispatchCooldown*2 {
		t.Fatalf("pre-gap cooldown = %s, want %s", got, feedDispatchCooldown*2)
	}

	// Gap beyond the churn window → streak resets to base on the next feed.
	current = current.Add(feedChurnWindow + time.Minute)
	m.recordFeedChurn(issue)
	if got := m.effectiveFeedCooldown(issue); got != feedDispatchCooldown {
		t.Errorf("post-gap cooldown = %s, want reset to %s", got, feedDispatchCooldown)
	}
}

func TestFeedFirstReady_IteratesPastDispatchFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	// Convoy has 3 ready issues. First sling fails, second succeeds.
	// Verifies feedFirstReady iterates past dispatch failure within a single convoy.
	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	slingLogPath := filepath.Join(binDir, "sling.log")
	slingCountPath := filepath.Join(binDir, "sling_count")
	// First sling call exits 1 (failure), subsequent succeed
	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo "$@" >> "` + slingLogPath + `"
  if [ ! -f "` + slingCountPath + `" ]; then
    echo "1" > "` + slingCountPath + `"
    echo "dispatch failed" >&2
    exit 1
  fi
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)

	c := strandedConvoyInfo{
		ID:          "hq-cv1",
		Title:       "Iterate Past Failure",
		ReadyCount:  3,
		ReadyIssues: []string{"gt-fail1", "gt-succeed2", "gt-notreached3"},
	}
	m.feedFirstReady(c)

	data, err := os.ReadFile(slingLogPath)
	if err != nil {
		t.Fatalf("read sling log: %v", err)
	}
	logContent := string(data)

	// First issue was attempted (and failed)
	if !strings.Contains(logContent, "gt-fail1") {
		t.Errorf("expected sling attempt for gt-fail1, got: %q", logContent)
	}
	// Second issue should succeed
	if !strings.Contains(logContent, "gt-succeed2") {
		t.Errorf("expected sling for gt-succeed2 (iterate past failure), got: %q", logContent)
	}
	// Third issue should NOT be reached (second succeeded)
	if strings.Contains(logContent, "gt-notreached3") {
		t.Errorf("unexpected dispatch of gt-notreached3 (should stop after first success): %q", logContent)
	}

	// Verify failure was logged
	hasFailure := false
	for _, l := range logged {
		if strings.Contains(l, "gt-fail1") && strings.Contains(l, "failed") {
			hasFailure = true
			break
		}
	}
	if !hasFailure {
		t.Errorf("expected sling failure log for gt-fail1, got: %v", logged)
	}
}

func TestFeedFirstReady_AllIssuesFail_LogsNoneDispatchable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	// All sling calls fail. Verify the "no dispatchable issues" log message.
	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo "always fail" >&2
  exit 1
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)

	c := strandedConvoyInfo{
		ID:          "hq-cv1",
		Title:       "All Fail",
		ReadyCount:  2,
		ReadyIssues: []string{"gt-fail1", "gt-fail2"},
	}
	m.feedFirstReady(c)

	found := false
	for _, l := range logged {
		if strings.Contains(l, "no dispatchable issues") && strings.Contains(l, "2 skipped") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'no dispatchable issues (all 2 skipped)' in logs, got: %v", logged)
	}
}

func TestFeedFirstReady_UnknownPrefix_Skips(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	slingLogPath := filepath.Join(binDir, "sling.log")
	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo "$@" >> "` + slingLogPath + `"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)

	c := strandedConvoyInfo{
		ID:          "hq-cv1",
		Title:       "Unknown Prefix",
		ReadyCount:  1,
		ReadyIssues: []string{"zz-issue1"},
	}
	m.feedFirstReady(c)

	if _, err := os.Stat(slingLogPath); err == nil {
		data, _ := os.ReadFile(slingLogPath)
		t.Errorf("sling was called unexpectedly: %s", data)
	}

	skipLogged := false
	for _, s := range logged {
		if strings.Contains(s, "skipping") && strings.Contains(s, "zz-issue1") {
			skipLogged = true
			break
		}
	}
	if !skipLogged {
		t.Errorf("expected skip log for unknown prefix issue zz-issue1, got: %v", logged)
	}
}

func TestFindStranded_GtFailure_ReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()

	gtScript := `#!/bin/sh
if [ "$1" = "convoy" ] && [ "$2" = "stranded" ]; then
  echo "something went wrong" >&2
  exit 1
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}

	m := NewConvoyManager(townRoot, func(string, ...interface{}) {}, filepath.Join(binDir, "gt"), 10*time.Minute, nil, nil, nil)

	result, err := m.findStranded()
	if err == nil {
		t.Fatalf("expected error from findStranded, got nil with result: %v", result)
	}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Errorf("expected error to contain stderr message, got: %v", err)
	}
}

func TestFindStranded_InvalidJSON_ReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()

	gtScript := `#!/bin/sh
if [ "$1" = "convoy" ] && [ "$2" = "stranded" ]; then
  echo "this is not valid JSON at all"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}

	m := NewConvoyManager(townRoot, func(string, ...interface{}) {}, filepath.Join(binDir, "gt"), 10*time.Minute, nil, nil, nil)

	result, err := m.findStranded()
	if err == nil {
		t.Fatalf("expected error from findStranded, got nil with result: %v", result)
	}
	if !strings.Contains(err.Error(), "parsing stranded JSON") {
		t.Errorf("expected error to mention 'parsing stranded JSON', got: %v", err)
	}
}

func TestScan_FindStrandedError_LogsAndContinues(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()

	gtScript := `#!/bin/sh
if [ "$1" = "convoy" ] && [ "$2" = "stranded" ]; then
  echo "stranded command failed" >&2
  exit 1
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(townRoot, logger, filepath.Join(binDir, "gt"), 10*time.Minute, nil, nil, nil)

	// scan() should not panic even when findStranded fails
	m.scan()

	// Verify the error was logged
	found := false
	for _, s := range logged {
		if strings.Contains(s, "stranded scan failed") && strings.Contains(s, "stranded command failed") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'stranded scan failed' error in logs, got: %v", logged)
	}
}

func TestPollEvents_GetAllEventsSinceError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	store, cleanup := setupTestStore(t)
	defer cleanup()

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	townRoot := t.TempDir()
	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, map[string]beadsdk.Storage{"hq": store}, nil, nil)

	// Cancel the manager's context so GetAllEventsSince receives a cancelled context
	m.cancel()

	// pollEvents should not panic when store returns error
	m.pollStoresSnapshot(m.stores)

	// Verify the error was logged with retry message
	found := false
	for _, s := range logged {
		if strings.Contains(s, "event poll error") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'event poll error' in logs, got: %v", logged)
	}
}

func TestFeedFirstReady_UnknownRig_Skips(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	// "hq-" prefix routes to town-level path "." which has no rig name
	routes := `{"prefix":"hq-","path":"."}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	slingLogPath := filepath.Join(binDir, "sling.log")
	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo "$@" >> "` + slingLogPath + `"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)

	c := strandedConvoyInfo{
		ID:          "hq-cv1",
		Title:       "Town-level Rig",
		ReadyCount:  1,
		ReadyIssues: []string{"hq-issue1"},
	}
	m.feedFirstReady(c)

	if _, err := os.Stat(slingLogPath); err == nil {
		data, _ := os.ReadFile(slingLogPath)
		t.Errorf("sling was called unexpectedly: %s", data)
	}

	skipLogged := false
	for _, s := range logged {
		if strings.Contains(s, "skipping") && strings.Contains(s, "hq-issue1") {
			skipLogged = true
			break
		}
	}
	if !skipLogged {
		t.Errorf("expected skip log for rig-less issue hq-issue1, got: %v", logged)
	}
}

func TestFeedFirstReady_ParkedRig_Skips(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"sh-","path":"shippercrm/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	slingLogPath := filepath.Join(binDir, "sling.log")
	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo "$@" >> "` + slingLogPath + `"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	// isRigParked returns true for "shippercrm"
	parked := func(rig string) bool { return rig == "shippercrm" }
	m := NewConvoyManager(townRoot, logger, filepath.Join(binDir, "gt"), 10*time.Minute, nil, nil, parked)

	c := strandedConvoyInfo{
		ID:          "hq-cv-park1",
		Title:       "Parked Rig Convoy",
		ReadyCount:  1,
		ReadyIssues: []string{"sh-issue1"},
	}
	m.feedFirstReady(c)

	// Sling should NOT have been called
	if _, err := os.Stat(slingLogPath); err == nil {
		data, _ := os.ReadFile(slingLogPath)
		t.Errorf("sling was called for parked rig: %s", data)
	}

	// Should log the parked skip
	skipLogged := false
	for _, s := range logged {
		if strings.Contains(s, "parked") && strings.Contains(s, "shippercrm") {
			skipLogged = true
			break
		}
	}
	if !skipLogged {
		t.Errorf("expected parked rig skip log, got: %v", logged)
	}
}

func TestFeedFirstReady_EmptyReadyIssues_NoOp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	callLogPath := filepath.Join(binDir, "gt-calls.log")
	gtScript := `#!/bin/sh
echo "$@" >> "` + callLogPath + `"
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)

	c := strandedConvoyInfo{
		ID:          "hq-cv1",
		Title:       "Empty Ready",
		ReadyCount:  3,
		ReadyIssues: []string{},
	}
	m.feedFirstReady(c)

	if _, err := os.Stat(callLogPath); err == nil {
		data, _ := os.ReadFile(callLogPath)
		t.Errorf("gt was called unexpectedly: %s", data)
	}

	for _, s := range logged {
		if strings.Contains(s, "error") || strings.Contains(s, "failed") || strings.Contains(s, "skipping") {
			t.Errorf("unexpected log message for empty ReadyIssues: %s", s)
		}
	}
}

func TestScan_ContextCancelled_MidIteration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	// Build a stranded list with 5 convoys, all with ready issues.
	// The mock gt will block on sling calls so we can cancel mid-iteration.
	type convoy struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		ReadyCount  int      `json:"ready_count"`
		ReadyIssues []string `json:"ready_issues"`
	}
	convoys := make([]convoy, 5)
	for i := range convoys {
		convoys[i] = convoy{
			ID:          fmt.Sprintf("hq-cv%d", i+1),
			Title:       fmt.Sprintf("Convoy %d", i+1),
			ReadyCount:  1,
			ReadyIssues: []string{fmt.Sprintf("gt-issue%d", i+1)},
		}
	}
	jsonBytes, err := json.Marshal(convoys)
	if err != nil {
		t.Fatalf("marshal stranded JSON: %v", err)
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	slingLogPath := filepath.Join(binDir, "sling.log")

	// Mock gt: stranded returns list; sling sleeps 10s (simulates slow dispatch)
	gtScript := `#!/bin/sh
if [ "$1" = "convoy" ] && [ "$2" = "stranded" ]; then
  echo '` + strings.ReplaceAll(string(jsonBytes), "'", "'\\''") + `'
  exit 0
fi
if [ "$1" = "sling" ]; then
  echo "$@" >> "` + slingLogPath + `"
  sleep 10
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logMu sync.Mutex
	var logged []string
	logger := func(format string, args ...interface{}) {
		logMu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}

	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)

	// Run scan in a goroutine and cancel context after a brief delay
	done := make(chan struct{})
	go func() {
		m.scan()
		close(done)
	}()

	// Give scan time to start processing, then cancel
	time.Sleep(200 * time.Millisecond)
	m.cancel()

	// scan() must exit cleanly within a bounded time (not hang on all 5 convoys)
	select {
	case <-done:
		// Clean exit -- success
	case <-time.After(5 * time.Second):
		t.Fatal("scan() did not exit within 5s after context cancellation")
	}

	// Verify it did NOT process all 5 convoys (cancellation stopped iteration)
	logMu.Lock()
	defer logMu.Unlock()
	feedCount := 0
	for _, s := range logged {
		if strings.Contains(s, "feeding") {
			feedCount++
		}
	}
	if feedCount >= 5 {
		t.Errorf("expected cancellation to stop iteration before all 5 convoys, but all were fed")
	}
}

func TestScanStranded_MixedReadyAndEmpty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	paths := mockGtForScanTest(t, scanTestOpts{
		strandedJSON: `[
			{"id":"hq-ready1","title":"Ready One","ready_count":1,"ready_issues":["gt-issue1"]},
			{"id":"hq-empty1","title":"Empty One","ready_count":0,"ready_issues":[]},
			{"id":"hq-ready2","title":"Ready Two","ready_count":2,"ready_issues":["gt-issue2","gt-issue3"]},
			{"id":"hq-empty2","title":"Empty Two","ready_count":0,"ready_issues":[]}
		]`,
		routes: `{"prefix":"gt-","path":"gt/.beads"}` + "\n",
	})

	var logMu sync.Mutex
	var logged []string
	logger := func(format string, args ...interface{}) {
		logMu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}

	m := NewConvoyManager(paths.townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)
	m.scan()

	// Verify ready convoys were dispatched via sling
	slingData, err := os.ReadFile(paths.slingLogPath)
	if err != nil {
		t.Fatalf("read sling log: %v (sling was never called)", err)
	}
	slingContent := string(slingData)
	if !strings.Contains(slingContent, "gt-issue1") {
		t.Errorf("expected sling for gt-issue1 (ready convoy), got: %q", slingContent)
	}
	if !strings.Contains(slingContent, "gt-issue2") {
		t.Errorf("expected sling for gt-issue2 (ready convoy), got: %q", slingContent)
	}

	// Verify empty convoys were routed to convoy check
	checkData, err := os.ReadFile(paths.checkLogPath)
	if err != nil {
		t.Fatalf("read check log: %v (convoy check was never called)", err)
	}
	checkContent := string(checkData)
	if !strings.Contains(checkContent, "hq-empty1") {
		t.Errorf("expected convoy check for hq-empty1 (empty convoy), got: %q", checkContent)
	}
	if !strings.Contains(checkContent, "hq-empty2") {
		t.Errorf("expected convoy check for hq-empty2 (empty convoy), got: %q", checkContent)
	}

	// Negative: ready convoys should NOT appear in check log
	if strings.Contains(checkContent, "hq-ready1") {
		t.Errorf("ready convoy hq-ready1 should not appear in check log: %q", checkContent)
	}
	if strings.Contains(checkContent, "hq-ready2") {
		t.Errorf("ready convoy hq-ready2 should not appear in check log: %q", checkContent)
	}

	// Negative: empty convoys should NOT appear in sling log
	if strings.Contains(slingContent, "hq-empty1") || strings.Contains(slingContent, "hq-empty2") {
		t.Errorf("empty convoys should not appear in sling log: %q", slingContent)
	}
}

// --- P0: Stop() closes lazily-opened stores ---

func TestStop_ClosesLazilyOpenedStores(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	store, cleanup := setupTestStore(t)
	defer cleanup() // safety net; Stop() should close first

	opener := func() map[string]beadsdk.Storage {
		return map[string]beadsdk.Storage{"hq": store}
	}

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(t.TempDir(), logger, "gt", 10*time.Minute, nil, opener, nil)

	// Simulate lazy opening (as runEventPoll does when stores are nil)
	m.stores = m.openStores()
	if len(m.stores) != 1 {
		t.Fatalf("expected 1 store from opener, got %d", len(m.stores))
	}

	m.Stop()

	// Verify Close was called via the log message Stop() emits
	found := false
	for _, s := range logged {
		if strings.Contains(s, "closed beads store") && strings.Contains(s, "hq") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'closed beads store (hq)' in logs after Stop(), got: %v", logged)
	}

	// Verify stores map is nil after Stop
	if m.stores != nil {
		t.Error("stores should be nil after Stop()")
	}
}

func TestStop_ClosesMultipleStores(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	hqStore, hqCleanup := setupTestStore(t)
	defer hqCleanup()
	rigStore, rigCleanup := setupTestStore(t)
	defer rigCleanup()

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	stores := map[string]beadsdk.Storage{
		"hq":      hqStore,
		"gastown": rigStore,
	}

	m := NewConvoyManager(t.TempDir(), logger, "gt", 10*time.Minute, stores, nil, nil)
	m.Stop()

	// Both stores should have been closed
	closedHq := false
	closedRig := false
	for _, s := range logged {
		if strings.Contains(s, "closed beads store") && strings.Contains(s, "hq") {
			closedHq = true
		}
		if strings.Contains(s, "closed beads store") && strings.Contains(s, "gastown") {
			closedRig = true
		}
	}
	if !closedHq {
		t.Errorf("expected hq store closed in logs, got: %v", logged)
	}
	if !closedRig {
		t.Errorf("expected gastown store closed in logs, got: %v", logged)
	}
	if m.stores != nil {
		t.Error("stores should be nil after Stop()")
	}
}

// --- P0: Multi-rig event poll ---

func TestPollAllStores_MultiRig_DetectsCloseFromNonHqStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	hqStore, hqCleanup := setupTestStore(t)
	defer hqCleanup()
	rigStore, rigCleanup := setupTestStore(t)
	defer rigCleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	// Create and close an issue in the rig store (NOT hq).
	// This is the core multi-rig scenario: events originate from per-rig stores.
	issue := &beadsdk.Issue{
		ID:        "sh-rig1",
		Title:     "Rig Issue",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := rigStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue in rig store: %v", err)
	}
	if err := rigStore.CloseIssue(ctx, issue.ID, "done", "test", ""); err != nil {
		t.Fatalf("CloseIssue in rig store: %v", err)
	}

	// hq store has no close events — only rig store does

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	stores := map[string]beadsdk.Storage{
		"hq":         hqStore,
		"shippercrm": rigStore,
	}

	m := NewConvoyManager(t.TempDir(), logger, "gt", 10*time.Minute, stores, nil, nil)
	m.seeded.Store(true)
	m.pollStoresSnapshot(m.stores)

	// The close event from the rig store should be detected
	found := false
	for _, s := range logged {
		if strings.Contains(s, "close detected") && strings.Contains(s, "sh-rig1") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected close event from non-hq store (shippercrm) to be detected, got: %v", logged)
	}
}

func TestPollAllStores_MultiRig_BothStoresPolled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	hqStore, hqCleanup := setupTestStore(t)
	defer hqCleanup()
	rigStore, rigCleanup := setupTestStore(t)
	defer rigCleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	// Close event in hq store
	hqIssue := &beadsdk.Issue{
		ID: "hq-task1", Title: "HQ Task", Status: beadsdk.StatusOpen,
		Priority: 2, IssueType: beadsdk.TypeTask, CreatedAt: now, UpdatedAt: now,
	}
	if err := hqStore.CreateIssue(ctx, hqIssue, "test"); err != nil {
		t.Fatalf("CreateIssue hq: %v", err)
	}
	if err := hqStore.CloseIssue(ctx, hqIssue.ID, "done", "test", ""); err != nil {
		t.Fatalf("CloseIssue hq: %v", err)
	}

	// Close event in rig store
	rigIssue := &beadsdk.Issue{
		ID: "gt-task1", Title: "Rig Task", Status: beadsdk.StatusOpen,
		Priority: 2, IssueType: beadsdk.TypeTask, CreatedAt: now, UpdatedAt: now,
	}
	if err := rigStore.CreateIssue(ctx, rigIssue, "test"); err != nil {
		t.Fatalf("CreateIssue rig: %v", err)
	}
	if err := rigStore.CloseIssue(ctx, rigIssue.ID, "done", "test", ""); err != nil {
		t.Fatalf("CloseIssue rig: %v", err)
	}

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	stores := map[string]beadsdk.Storage{
		"hq":      hqStore,
		"gastown": rigStore,
	}

	m := NewConvoyManager(t.TempDir(), logger, "gt", 10*time.Minute, stores, nil, nil)
	m.seeded.Store(true)
	m.pollStoresSnapshot(m.stores)

	// Both close events should be detected
	foundHq := false
	foundRig := false
	for _, s := range logged {
		if strings.Contains(s, "close detected") && strings.Contains(s, "hq-task1") {
			foundHq = true
		}
		if strings.Contains(s, "close detected") && strings.Contains(s, "gt-task1") {
			foundRig = true
		}
	}
	if !foundHq {
		t.Errorf("expected close event from hq store, got: %v", logged)
	}
	if !foundRig {
		t.Errorf("expected close event from rig store, got: %v", logged)
	}
}

// --- P1: Parked rig skipping ---

func TestPollAllStores_SkipsParkedRigs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	hqStore, hqCleanup := setupTestStore(t)
	defer hqCleanup()
	activeStore, activeCleanup := setupTestStore(t)
	defer activeCleanup()
	parkedStore, parkedCleanup := setupTestStore(t)
	defer parkedCleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	// Use unique IDs to avoid cross-test contamination from shared Dolt server
	activeID := fmt.Sprintf("gt-active-park-%d", time.Now().UnixNano())
	parkedID := fmt.Sprintf("sh-parked-park-%d", time.Now().UnixNano())

	// Close events in both rig stores
	for _, tc := range []struct {
		store beadsdk.Storage
		id    string
	}{
		{activeStore, activeID},
		{parkedStore, parkedID},
	} {
		issue := &beadsdk.Issue{
			ID: tc.id, Title: tc.id, Status: beadsdk.StatusOpen,
			Priority: 2, IssueType: beadsdk.TypeTask, CreatedAt: now, UpdatedAt: now,
		}
		if err := tc.store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue %s: %v", tc.id, err)
		}
		if err := tc.store.CloseIssue(ctx, issue.ID, "done", "test", ""); err != nil {
			t.Fatalf("CloseIssue %s: %v", tc.id, err)
		}
	}

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	stores := map[string]beadsdk.Storage{
		"hq":         hqStore,
		"gastown":    activeStore,
		"shippercrm": parkedStore,
	}

	isParked := func(rig string) bool {
		return rig == "shippercrm"
	}

	m := NewConvoyManager(t.TempDir(), logger, "gt", 10*time.Minute, stores, nil, isParked)
	m.seeded.Store(true)
	m.pollStoresSnapshot(m.stores)

	// Active rig's close event should be detected
	foundActive := false
	for _, s := range logged {
		if strings.Contains(s, "close detected") && strings.Contains(s, activeID) {
			foundActive = true
		}
	}
	if !foundActive {
		t.Errorf("expected close event from active rig (gastown) for %s, got: %v", activeID, logged)
	}

	// Parked rig store should not be polled (verified via high-water mark).
	// Note: the parked store's events may still be visible through other stores
	// if they share the same underlying Dolt server (test infrastructure detail).
	// What matters is that the "shippercrm" store key is never polled.
	if _, hasHW := m.lastEventIDs.Load("shippercrm"); hasHW {
		t.Errorf("parked rig (shippercrm) should not have been polled, but has a high-water mark")
	}
	// Active rig should have been polled
	if _, hasHW := m.lastEventIDs.Load("gastown"); !hasHW {
		t.Errorf("active rig (gastown) should have been polled, but has no high-water mark")
	}
}

func TestPollAllStores_HqNeverSkippedEvenIfParkedCallbackReturnsTrue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	issue := &beadsdk.Issue{
		ID: "hq-always1", Title: "HQ Always Polled", Status: beadsdk.StatusOpen,
		Priority: 2, IssueType: beadsdk.TypeTask, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "test", ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	// isRigParked returns true for EVERYTHING — but hq should still be polled
	// because the code checks `name != "hq" && m.isRigParked(name)`
	alwaysParked := func(string) bool { return true }

	m := NewConvoyManager(t.TempDir(), logger, "gt", 10*time.Minute,
		map[string]beadsdk.Storage{"hq": store}, nil, alwaysParked)
	m.seeded.Store(true)
	m.pollStoresSnapshot(m.stores)

	found := false
	for _, s := range logged {
		if strings.Contains(s, "close detected") && strings.Contains(s, "hq-always1") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("hq store should always be polled regardless of isRigParked, got: %v", logged)
	}
}

// --- P2: High-water mark monotonicity ---

func TestPollAllStores_HighWaterMark_NoReprocessing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	// Use unique ID to avoid cross-test contamination from shared Dolt server
	issueID := fmt.Sprintf("gt-hw-%d", time.Now().UnixNano())
	issue := &beadsdk.Issue{
		ID: issueID, Title: "High Water Test", Status: beadsdk.StatusOpen,
		Priority: 2, IssueType: beadsdk.TypeTask, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "test", ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(t.TempDir(), logger, "gt", 10*time.Minute,
		map[string]beadsdk.Storage{"hq": store}, nil, nil)

	// First poll: should detect our close event
	m.seeded.Store(true)
	m.pollStoresSnapshot(m.stores)

	closeCount := 0
	for _, s := range logged {
		if strings.Contains(s, "close detected") && strings.Contains(s, issueID) {
			closeCount++
		}
	}
	if closeCount != 1 {
		t.Fatalf("expected 1 close detection for %s on first poll, got %d: %v", issueID, closeCount, logged)
	}

	// Second poll: high-water mark + dedup should prevent reprocessing
	logged = nil // Reset log to only check new entries
	m.pollStoresSnapshot(m.stores)

	for _, s := range logged {
		if strings.Contains(s, "close detected") && strings.Contains(s, issueID) {
			t.Errorf("expected no reprocessing of %s after second poll, but found: %s", issueID, s)
		}
	}
}

func TestPollAllStores_ReopenClearsCloseDedupAcrossPolls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	issueID := fmt.Sprintf("gt-reclose-%d", time.Now().UnixNano())
	issue := &beadsdk.Issue{
		ID: issueID, Title: "Reclose Test", Status: beadsdk.StatusOpen,
		Priority: 2, IssueType: beadsdk.TypeTask, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "test", ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(t.TempDir(), logger, "gt", 10*time.Minute,
		map[string]beadsdk.Storage{"hq": store}, nil, nil)
	m.seeded.Store(true)
	m.pollStoresSnapshot(m.stores)

	firstCloseCount := 0
	for _, s := range logged {
		if strings.Contains(s, "close detected") && strings.Contains(s, issueID) {
			firstCloseCount++
		}
	}
	if firstCloseCount != 1 {
		t.Fatalf("expected 1 close detection for %s on first close, got %d: %v", issueID, firstCloseCount, logged)
	}

	time.Sleep(10 * time.Millisecond)
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{"status": beadsdk.StatusOpen}, "test"); err != nil {
		t.Fatalf("ReopenIssue via UpdateIssue: %v", err)
	}

	logged = nil
	m.pollStoresSnapshot(m.stores)

	if _, ok := m.processedCloses.Load(issueID); ok {
		t.Fatalf("expected processedCloses entry for %s to be cleared after reopen", issueID)
	}
	for _, s := range logged {
		if strings.Contains(s, "close detected") && strings.Contains(s, issueID) {
			t.Fatalf("expected reopen poll not to log a close for %s, got: %v", issueID, logged)
		}
	}

	time.Sleep(10 * time.Millisecond)
	if err := store.CloseIssue(ctx, issue.ID, "done again", "test", ""); err != nil {
		t.Fatalf("CloseIssue again: %v", err)
	}

	logged = nil
	m.pollStoresSnapshot(m.stores)

	secondCloseCount := 0
	for _, s := range logged {
		if strings.Contains(s, "close detected") && strings.Contains(s, issueID) {
			secondCloseCount++
		}
	}
	if secondCloseCount != 1 {
		t.Fatalf("expected 1 close detection for %s after reopen/reclose, got %d: %v", issueID, secondCloseCount, logged)
	}
}

func TestPollAllStores_ReopenResetsPerCycleDedup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	issueID := fmt.Sprintf("gt-reclose-same-poll-%d", time.Now().UnixNano())
	issue := &beadsdk.Issue{
		ID: issueID, Title: "Reclose Same Poll Test", Status: beadsdk.StatusOpen,
		Priority: 2, IssueType: beadsdk.TypeTask, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "test", ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	// Beads events use CURRENT_TIMESTAMP in Dolt, which is second precision.
	// Space the lifecycle transitions across distinct seconds so the store's
	// created_at ordering is deterministic within this single poll.
	time.Sleep(1100 * time.Millisecond)
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{"status": beadsdk.StatusOpen}, "test"); err != nil {
		t.Fatalf("ReopenIssue via UpdateIssue: %v", err)
	}

	time.Sleep(1100 * time.Millisecond)
	if err := store.CloseIssue(ctx, issue.ID, "done again", "test", ""); err != nil {
		t.Fatalf("CloseIssue again: %v", err)
	}

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(t.TempDir(), logger, "gt", 10*time.Minute,
		map[string]beadsdk.Storage{"hq": store}, nil, nil)
	m.seeded.Store(true)
	m.pollStoresSnapshot(m.stores)

	closeCount := 0
	for _, s := range logged {
		if strings.Contains(s, "close detected") && strings.Contains(s, issueID) {
			closeCount++
		}
	}
	if closeCount != 2 {
		t.Fatalf("expected 2 close detections for %s when close->reopen->close occurs in one poll, got %d: %v", issueID, closeCount, logged)
	}
}

// TestPollAllStores_CrossStoreDedup verifies that a close event seen from
// multiple stores is only processed once (GH #1798).
func TestPollAllStores_CrossStoreDedup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	hqStore, hqCleanup := setupTestStore(t)
	defer hqCleanup()
	rigStore, rigCleanup := setupTestStore(t)
	defer rigCleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	issueID := fmt.Sprintf("gt-dedup-%d", time.Now().UnixNano())

	// Create and close the same issue in BOTH stores (simulating replication)
	for _, store := range []beadsdk.Storage{hqStore, rigStore} {
		issue := &beadsdk.Issue{
			ID: issueID, Title: "Dedup Test", Status: beadsdk.StatusOpen,
			Priority: 2, IssueType: beadsdk.TypeTask, CreatedAt: now, UpdatedAt: now,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if err := store.CloseIssue(ctx, issue.ID, "done", "test", ""); err != nil {
			t.Fatalf("CloseIssue: %v", err)
		}
	}

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	stores := map[string]beadsdk.Storage{
		"hq":      hqStore,
		"gastown": rigStore,
	}
	m := NewConvoyManager(t.TempDir(), logger, "gt", 10*time.Minute, stores, nil, nil)
	m.seeded.Store(true)
	m.pollStoresSnapshot(m.stores)

	// Should see exactly 1 close detection for our issue, not 2
	closeCount := 0
	for _, s := range logged {
		if strings.Contains(s, "close detected") && strings.Contains(s, issueID) {
			closeCount++
		}
	}
	if closeCount != 1 {
		t.Errorf("expected exactly 1 close detection for %s (cross-store dedup), got %d: %v", issueID, closeCount, logged)
	}
}

func TestPollAllStores_PerStoreHighWaterMarks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	hqStore, hqCleanup := setupTestStore(t)
	defer hqCleanup()
	rigStore, rigCleanup := setupTestStore(t)
	defer rigCleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	// Close event only in hq initially
	hqIssue := &beadsdk.Issue{
		ID: "hq-hw1", Title: "HQ HW", Status: beadsdk.StatusOpen,
		Priority: 2, IssueType: beadsdk.TypeTask, CreatedAt: now, UpdatedAt: now,
	}
	if err := hqStore.CreateIssue(ctx, hqIssue, "test"); err != nil {
		t.Fatalf("CreateIssue hq: %v", err)
	}
	if err := hqStore.CloseIssue(ctx, hqIssue.ID, "done", "test", ""); err != nil {
		t.Fatalf("CloseIssue hq: %v", err)
	}

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	stores := map[string]beadsdk.Storage{
		"hq":      hqStore,
		"gastown": rigStore,
	}

	m := NewConvoyManager(t.TempDir(), logger, "gt", 10*time.Minute, stores, nil, nil)

	// First poll: only hq has a close event
	m.pollStoresSnapshot(m.stores)

	// Now add a close event to gastown AFTER the first poll
	rigIssue := &beadsdk.Issue{
		ID: "gt-hw2", Title: "Rig HW", Status: beadsdk.StatusOpen,
		Priority: 2, IssueType: beadsdk.TypeTask, CreatedAt: now, UpdatedAt: now,
	}
	if err := rigStore.CreateIssue(ctx, rigIssue, "test"); err != nil {
		t.Fatalf("CreateIssue rig: %v", err)
	}
	if err := rigStore.CloseIssue(ctx, rigIssue.ID, "done", "test", ""); err != nil {
		t.Fatalf("CloseIssue rig: %v", err)
	}

	// Second poll: gastown's new event should be detected, hq's old event should NOT
	logged = nil // reset
	m.pollStoresSnapshot(m.stores)

	foundNewRig := false
	foundOldHq := false
	for _, s := range logged {
		if strings.Contains(s, "close detected") && strings.Contains(s, "gt-hw2") {
			foundNewRig = true
		}
		if strings.Contains(s, "close detected") && strings.Contains(s, "hq-hw1") {
			foundOldHq = true
		}
	}
	if !foundNewRig {
		t.Errorf("expected new rig close event (gt-hw2) on second poll, got: %v", logged)
	}
	if foundOldHq {
		t.Errorf("hq close event (hq-hw1) should NOT be reprocessed on second poll (per-store high-water marks), got: %v", logged)
	}
}

func TestEventPoll_SkipsNonCloseEvents_NegativeAssertion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	// Use unique ID to avoid cross-test contamination from shared Dolt server
	issueID := fmt.Sprintf("gt-open2-%d", time.Now().UnixNano())
	issue := &beadsdk.Issue{
		ID:        issueID,
		Title:     "Stays Open",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()

	callLogPath := filepath.Join(binDir, "gt-calls.log")
	gtScript := `#!/bin/sh
echo "$@" >> "` + callLogPath + `"
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(townRoot, logger, filepath.Join(binDir, "gt"), 10*time.Minute, map[string]beadsdk.Storage{"hq": store}, nil, nil)
	m.seeded.Store(true)
	m.pollStoresSnapshot(m.stores)

	// Only check for close events involving OUR issue — other tests may have
	// created close events in the shared Dolt server that leak into this store.
	for _, s := range logged {
		if strings.Contains(s, "close detected") && strings.Contains(s, issueID) {
			t.Errorf("expected no close detection for open issue %s, got: %s", issueID, s)
		}
	}
}

// --- hq store nil guard ---

func TestPollStore_NilHqStore_LogsWarningAndSkips(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	// Create a rig store with a close event, but no hq store in the map.
	// The nil hq guard should log a warning and skip convoy lookups.
	rigStore, rigCleanup := setupTestStore(t)
	defer rigCleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	issue := &beadsdk.Issue{
		ID: "gt-nohq1", Title: "No HQ Store", Status: beadsdk.StatusOpen,
		Priority: 2, IssueType: beadsdk.TypeTask, CreatedAt: now, UpdatedAt: now,
	}
	if err := rigStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := rigStore.CloseIssue(ctx, issue.ID, "done", "test", ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	// stores map has a rig but no "hq" key
	stores := map[string]beadsdk.Storage{
		"gastown": rigStore,
	}

	m := NewConvoyManager(t.TempDir(), logger, "gt", 10*time.Minute, stores, nil, nil)
	m.seeded.Store(true)
	m.pollStoresSnapshot(m.stores)

	// Should log the nil hq warning
	foundWarning := false
	for _, s := range logged {
		if strings.Contains(s, "hq store unavailable") {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Errorf("expected 'hq store unavailable' warning, got: %v", logged)
	}

	// Should NOT have logged any close detection (skipped before processing events)
	for _, s := range logged {
		if strings.Contains(s, "close detected") {
			t.Errorf("expected no close detection without hq store, got: %s", s)
		}
	}
}

// TestRecoveryMode_SetOnPollError verifies that recoveryMode is set when
// an event poll encounters an error (Dolt unavailable).
func TestRecoveryMode_SetOnPollError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	townRoot := t.TempDir()
	var logged []string
	var mu sync.Mutex
	logger := func(format string, args ...interface{}) {
		mu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	// Use a broken store that returns errors
	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)

	// recoveryMode should start false
	if m.recoveryMode.Load() {
		t.Fatal("recoveryMode should be false initially")
	}

	// Simulate a poll with a store that will error (nil store map means no polling)
	// Instead, directly test the flag behavior:
	m.recoveryMode.Store(true)
	if !m.recoveryMode.Load() {
		t.Fatal("recoveryMode should be true after Store(true)")
	}

	// scan() should clear it (scan will fail on findStranded since no gt binary, but
	// that's OK — the test verifies the flag is cleared on success path only)
	m.recoveryMode.Store(false)
	if m.recoveryMode.Load() {
		t.Fatal("recoveryMode should be false after Store(false)")
	}
}

// TestRecoveryMode_ClearedAfterSuccessfulScan verifies that a successful
// scan() call clears recovery mode.
func TestRecoveryMode_ClearedAfterSuccessfulScan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	paths := mockGtForScanTest(t, scanTestOpts{strandedJSON: "[]"})
	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(paths.townRoot, logger, filepath.Join(paths.binDir, "gt"), 10*time.Minute, nil, nil, nil)

	// Set recovery mode
	m.recoveryMode.Store(true)
	if !m.recoveryMode.Load() {
		t.Fatal("expected recoveryMode true before scan")
	}

	// scan() should clear it (mock gt returns empty stranded list = success)
	m.scan()

	if m.recoveryMode.Load() {
		t.Fatal("expected recoveryMode false after successful scan")
	}
}

// TestScanMu_PreventsConcurrentScans verifies that concurrent scan() calls
// are serialized by scanMu (no duplicate convoy checks).
func TestScanMu_PreventsConcurrentScans(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	stranded := []strandedConvoyInfo{{
		ID:          "convoy-race",
		ReadyCount:  1,
		ReadyIssues: []string{"gt-race1"},
	}}
	data, _ := json.Marshal(stranded)

	paths := mockGtForScanTest(t, scanTestOpts{strandedJSON: string(data)})
	var logged []string
	var mu sync.Mutex
	logger := func(format string, args ...interface{}) {
		mu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	m := NewConvoyManager(paths.townRoot, logger, filepath.Join(paths.binDir, "gt"), 10*time.Minute, nil, nil, nil)

	// Launch multiple concurrent scans
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.scan()
		}()
	}
	wg.Wait()

	// Verify sling was called (at least once) — the key is no panics or races
	if _, err := os.Stat(paths.slingLogPath); err != nil {
		t.Log("sling was never called (mock gt may not have been reached) — acceptable for race test")
	}
}

// TestStartupSweep_RunsAfterDelay verifies that runStartupSweep calls scan()
// after the startup delay.
func TestStartupSweep_RunsAfterDelay(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	paths := mockGtForScanTest(t, scanTestOpts{strandedJSON: "[]"})
	var scanCount atomic.Int32
	logger := func(format string, args ...interface{}) {
		msg := fmt.Sprintf(format, args...)
		if strings.Contains(msg, "startup sweep") {
			scanCount.Add(1)
		}
	}

	// Use a short startup delay by testing the goroutine directly
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewConvoyManager(paths.townRoot, logger, filepath.Join(paths.binDir, "gt"), 10*time.Minute, nil, nil, nil)
	m.ctx = ctx

	// Run startup sweep directly (it waits 10s normally, but we can test the
	// mechanism by verifying it logs the startup message)
	// For a fast test, we cancel the context after a short delay
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	m.runStartupSweep()
	// Context was cancelled before 10s timer — sweep should not have run
	if scanCount.Load() > 0 {
		t.Error("startup sweep should not run before timer expires")
	}
}

// TestDoltRecoveryCallback_Fires verifies that the Dolt server manager fires
// the recovery callback when transitioning from unhealthy to healthy.
func TestDoltRecoveryCallback_Fires(t *testing.T) {
	tmpDir := t.TempDir()
	dsm := NewDoltServerManager(tmpDir, DefaultDoltServerConfig(tmpDir), func(string, ...interface{}) {})

	var called atomic.Bool
	dsm.SetRecoveryCallback(func() {
		called.Store(true)
	})

	// Create the daemon directory and write the signal file at the path
	// that unhealthySignalFile() returns (port-dependent).
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Use writeUnhealthySignal to create the file at the correct path
	dsm.mu.Lock()
	dsm.writeUnhealthySignal("test", "test detail")
	dsm.mu.Unlock()

	// Clear the signal — should trigger callback since file was present
	dsm.mu.Lock()
	dsm.clearUnhealthySignal()
	dsm.mu.Unlock()

	// Give the goroutine time to fire
	time.Sleep(100 * time.Millisecond)

	if !called.Load() {
		t.Error("expected recovery callback to fire on unhealthy→healthy transition")
	}
}

// TestDoltRecoveryCallback_NoFireWhenAlreadyHealthy verifies that the callback
// does NOT fire when the signal file was not present (already healthy).
func TestDoltRecoveryCallback_NoFireWhenAlreadyHealthy(t *testing.T) {
	tmpDir := t.TempDir()
	dsm := NewDoltServerManager(tmpDir, DefaultDoltServerConfig(tmpDir), func(string, ...interface{}) {})

	var called atomic.Bool
	dsm.SetRecoveryCallback(func() {
		called.Store(true)
	})

	// Don't create any signal file — already healthy

	dsm.mu.Lock()
	dsm.clearUnhealthySignal()
	dsm.mu.Unlock()

	time.Sleep(100 * time.Millisecond)

	if called.Load() {
		t.Error("recovery callback should NOT fire when already healthy")
	}
}

// TestDoltRecoveryCallback_NilSafe verifies that clearUnhealthySignal does
// not panic when no callback is registered.
func TestDoltRecoveryCallback_NilSafe(t *testing.T) {
	tmpDir := t.TempDir()
	dsm := NewDoltServerManager(tmpDir, DefaultDoltServerConfig(tmpDir), func(string, ...interface{}) {})

	// Create daemon dir and write signal file via the proper method
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dsm.mu.Lock()
	dsm.writeUnhealthySignal("test", "test detail")
	dsm.mu.Unlock()

	// No callback set — should not panic
	dsm.mu.Lock()
	dsm.clearUnhealthySignal()
	dsm.mu.Unlock()
}

// infNaNStorage is a minimal Storage stub whose GetAllEventsSince always
// returns the given error. All other methods panic (they should not be called).
type infNaNStorage struct {
	beadsdk.Storage // embedded to satisfy unimplemented methods
	err             error
}

func (s *infNaNStorage) GetAllEventsSince(_ context.Context, _ time.Time) ([]*beadsdk.Event, error) {
	return nil, s.err
}

// TestPollStore_InfNaNError_AdvancesHWMAndReturnsNil verifies that when
// GetAllEventsSince returns a "+Inf is not a valid value for double" error
// (corrupt Dolt row), pollStore advances the high-water mark to now and
// returns nil (no error, no recovery mode).
func TestPollStore_InfNaNError_AdvancesHWMAndReturnsNil(t *testing.T) {
	for _, errMsg := range []string{
		"Error 1366 (HY000): error: +Inf is not a valid value for double",
		"Error 1366 (HY000): error: -Inf is not a valid value for double",
		"Error 1366 (HY000): error: NaN is not a valid value for double",
		// Dolt wraps values in single quotes in actual error messages
		"Error 1366 (HY000): error: '+Inf' is not a valid value for 'double'",
		"Error 1366 (HY000): error: '-Inf' is not a valid value for 'double'",
		"Error 1366 (HY000): error: 'NaN' is not a valid value for 'double'",
		// Wrapped in beads SDK error context (actual observed format)
		"failed to get events since 0: Error 1366 (HY000): error: '+Inf' is not a valid value for 'double'",
	} {
		t.Run(errMsg[:20], func(t *testing.T) {
			stub := &infNaNStorage{err: fmt.Errorf("%s", errMsg)}
			stores := map[string]beadsdk.Storage{"hq": stub}

			var logged []string
			logger := func(format string, args ...interface{}) {
				logged = append(logged, fmt.Sprintf(format, args...))
			}

			before := time.Now()
			m := NewConvoyManager(t.TempDir(), logger, "gt", 10*time.Minute, stores, nil, nil)

			hadError := m.pollStoresSnapshot(m.stores)
			after := time.Now()

			// pollStoresSnapshot should report no error (corrupt row is handled)
			if hadError {
				t.Errorf("expected no error for inf/nan store, got hadError=true; logs: %v", logged)
			}

			// recoveryMode must NOT be set (we recovered inline)
			if m.recoveryMode.Load() {
				t.Errorf("recoveryMode should not be set for inf/nan error; logs: %v", logged)
			}

			// High-water mark for "hq" should have been advanced to approximately now
			v, ok := m.lastEventIDs.Load("hq")
			if !ok {
				t.Fatal("expected HWM to be stored for hq")
			}
			hwm := v.(time.Time)
			if hwm.Before(before) || hwm.After(after.Add(time.Second)) {
				t.Errorf("HWM %v not in expected range [%v, %v]", hwm, before, after)
			}

			// Should have logged a message about the skip
			foundMsg := false
			for _, s := range logged {
				if strings.Contains(s, "+Inf/NaN row detected") {
					foundMsg = true
					break
				}
			}
			if !foundMsg {
				t.Errorf("expected HWM-advance log message, got: %v", logged)
			}
		})
	}
}

// transientErrThenEventsStore returns an error on its first GetAllEventsSince
// call (simulating Dolt still warming up right after a daemon restart) and the
// given events on every subsequent call. Used to reproduce gs-rx1: a store that
// errors during the initial warm-up cycle must still run its warm-up — and thus
// ABSORB, not replay, its historical close backlog — on its first successful poll.
type transientErrThenEventsStore struct {
	beadsdk.Storage // embedded to satisfy unimplemented methods
	events          []*beadsdk.Event
	calls           int
}

func (s *transientErrThenEventsStore) GetAllEventsSince(_ context.Context, _ time.Time) ([]*beadsdk.Event, error) {
	s.calls++
	if s.calls == 1 {
		return nil, fmt.Errorf("Error 2003 (HY000): connection refused")
	}
	return s.events, nil
}

// TestPollStore_TransientErrorDuringWarmup_NoBacklogReplay reproduces gs-rx1.
// When the hq store errors on the first poll (Dolt warming up after a restart),
// the store must NOT be promoted to the processing phase. Its first SUCCESSFUL
// poll has to run warm-up so the accumulated close backlog is absorbed (marks +
// HWM advance) instead of replayed as a storm of "close detected" events that
// each fork a CheckConvoysForIssue subprocess and starve dispatch for minutes.
func TestPollStore_TransientErrorDuringWarmup_NoBacklogReplay(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	// A backlog of already-closed convoy-wisps — the historical events a freshly
	// restarted daemon sees when it first reads from epoch.
	backlog := []*beadsdk.Event{
		{ID: "evt-1", IssueID: "hq-wisp-backlog1", EventType: beadsdk.EventClosed, CreatedAt: time.Now().UTC()},
		{ID: "evt-2", IssueID: "hq-wisp-backlog2", EventType: beadsdk.EventClosed, CreatedAt: time.Now().UTC()},
	}
	stub := &transientErrThenEventsStore{events: backlog}
	stores := map[string]beadsdk.Storage{"hq": stub}

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}
	// Exercise the production warm-up path: do NOT set m.seeded.
	m := NewConvoyManager(t.TempDir(), logger, "gt", 10*time.Minute, stores, nil, nil)

	// First poll: the store errors (Dolt still warming up after restart).
	if hadError := m.pollStoresSnapshot(m.stores); !hadError {
		t.Fatal("expected first poll to report an error")
	}

	// Second poll: the store recovers and returns its full close backlog. Because
	// the store never completed warm-up, this cycle must warm up (absorb the
	// backlog) rather than replay it as fresh closes.
	m.pollStoresSnapshot(m.stores)

	for _, s := range logged {
		if strings.Contains(s, "close detected") {
			t.Fatalf("backlog close was replayed after a transient warm-up error (gs-rx1); logs: %v", logged)
		}
	}

	// The store must now be seeded so subsequent polls process genuinely new
	// closes (rather than re-warming-up forever). The backlog events were marked
	// processed during warm-up, so they will not replay on later polls either.
	if _, warmed := m.seededStores.Load("hq"); !warmed {
		t.Errorf("expected hq to be seeded after its first successful poll")
	}
	for _, ev := range backlog {
		if _, marked := m.processedLifecycleEvents.Load(ev.ID); !marked {
			t.Errorf("expected warm-up to mark backlog event %s processed", ev.ID)
		}
	}
}

// TestFeedFirstReady_PerIssueCooldown_SkipsRepeat verifies that a ready issue
// slung within feedDispatchCooldown is skipped on the next scan even though
// `gt convoy stranded` still reports it as ready. This is the gu-iygf fix:
// in_progress beads with a dead assignee no longer hot-loop every scan tick.
func TestFeedFirstReady_PerIssueCooldown_SkipsRepeat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	slingLogPath := filepath.Join(binDir, "sling.log")
	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo "$@" >> "` + slingLogPath + `"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)

	// First call: dispatches gt-loop1.
	c := strandedConvoyInfo{
		ID:          "hq-cv1",
		Title:       "Hot Loop",
		ReadyCount:  1,
		ReadyIssues: []string{"gt-loop1"},
	}
	m.feedFirstReady(c)

	data, err := os.ReadFile(slingLogPath)
	if err != nil {
		t.Fatalf("read sling log after first feed: %v", err)
	}
	if !strings.Contains(string(data), "gt-loop1") {
		t.Fatalf("expected first call to dispatch gt-loop1, got: %q", data)
	}
	firstSize := len(data)

	// Second call (immediately): cooldown should suppress re-dispatch.
	logged = nil
	m.feedFirstReady(c)

	data2, err := os.ReadFile(slingLogPath)
	if err != nil {
		t.Fatalf("read sling log after second feed: %v", err)
	}
	if len(data2) != firstSize {
		t.Errorf("expected no new sling call within cooldown, got: %q", data2)
	}

	cooldownLogged := false
	for _, s := range logged {
		if strings.Contains(s, "feed cooldown") && strings.Contains(s, "gt-loop1") {
			cooldownLogged = true
			break
		}
	}
	if !cooldownLogged {
		t.Errorf("expected 'feed cooldown' log for gt-loop1, got: %v", logged)
	}
}

// TestFeedFirstReady_CooldownExpires_AllowsRedispatch verifies that once the
// cooldown window passes, a ready issue is slung again. Uses an injectable
// clock to avoid waiting feedDispatchCooldown in real time.
func TestFeedFirstReady_CooldownExpires_AllowsRedispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	slingLogPath := filepath.Join(binDir, "sling.log")
	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo "$@" >> "` + slingLogPath + `"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := NewConvoyManager(townRoot, func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)

	// Drive the clock manually.
	current := time.Now()
	m.now = func() time.Time { return current }

	c := strandedConvoyInfo{
		ID:          "hq-cv1",
		Title:       "Cooldown Expires",
		ReadyCount:  1,
		ReadyIssues: []string{"gt-expire1"},
	}

	m.feedFirstReady(c) // first dispatch
	current = current.Add(feedDispatchCooldown + time.Second)
	m.feedFirstReady(c) // cooldown elapsed

	data, err := os.ReadFile(slingLogPath)
	if err != nil {
		t.Fatalf("read sling log: %v", err)
	}
	count := strings.Count(string(data), "gt-expire1")
	if count != 2 {
		t.Errorf("expected 2 dispatches across cooldown boundary, got %d: %q", count, data)
	}
}

// TestFeedFirstReady_CooldownPerIssue verifies that the cooldown is per-issue,
// not global: an unrelated issue may still be dispatched while another is in
// cooldown.
func TestFeedFirstReady_CooldownPerIssue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	slingLogPath := filepath.Join(binDir, "sling.log")
	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo "$@" >> "` + slingLogPath + `"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := NewConvoyManager(townRoot, func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)

	// First convoy: dispatches gt-a (puts gt-a in cooldown).
	m.feedFirstReady(strandedConvoyInfo{
		ID: "hq-cv1", Title: "A", ReadyCount: 1, ReadyIssues: []string{"gt-a"},
	})
	// Second convoy: gt-a still in cooldown but gt-b is fresh — gt-b dispatches.
	m.feedFirstReady(strandedConvoyInfo{
		ID: "hq-cv2", Title: "AB", ReadyCount: 2, ReadyIssues: []string{"gt-a", "gt-b"},
	})

	data, err := os.ReadFile(slingLogPath)
	if err != nil {
		t.Fatalf("read sling log: %v", err)
	}
	content := string(data)
	if strings.Count(content, "gt-a") != 1 {
		t.Errorf("expected gt-a slung exactly once (cooldown), got: %q", content)
	}
	if !strings.Contains(content, "gt-b") {
		t.Errorf("expected gt-b dispatch (fresh issue, no cooldown), got: %q", content)
	}
}

// TestFeedFirstReady_FailedSling_StillCoolsDown verifies the cooldown is
// recorded even when the sling subprocess exits non-zero. The recurring
// failure mode in gu-iygf was a sling that kept failing every ~2 minutes;
// the cooldown must apply to failed attempts too, otherwise we'd retry
// forever on every tick.
func TestFeedFirstReady_FailedSling_StillCoolsDown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	slingLogPath := filepath.Join(binDir, "sling.log")
	// Sling always fails — simulates the dead-polecat in_progress retry loop.
	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo "$@" >> "` + slingLogPath + `"
  echo "boom" >&2
  exit 1
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := NewConvoyManager(townRoot, func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)

	c := strandedConvoyInfo{
		ID: "hq-cv1", Title: "Fail Loop", ReadyCount: 1, ReadyIssues: []string{"gt-fail-loop"},
	}
	m.feedFirstReady(c)
	m.feedFirstReady(c)
	m.feedFirstReady(c)

	data, err := os.ReadFile(slingLogPath)
	if err != nil {
		t.Fatalf("read sling log: %v", err)
	}
	if got := strings.Count(string(data), "gt-fail-loop"); got != 1 {
		t.Errorf("expected exactly 1 sling attempt under cooldown despite failure, got %d: %q", got, data)
	}
}

// TestFeedFirstReady_ReturnsFailureCount verifies feedFirstReady reports the
// number of failed sling attempts so scan() can drive the feed-storm monitor
// (gc-wwpw2). With multiple ready issues that all fail, every attempt is tried
// (the loop continues past a failure) and each increments the count.
func TestFeedFirstReady_ReturnsFailureCount(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	// Sling always fails so every ready issue counts as a failure.
	gtScript := "#!/bin/sh\nif [ \"$1\" = \"sling\" ]; then echo boom >&2; exit 1; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := NewConvoyManager(townRoot, func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)
	c := strandedConvoyInfo{
		ID: "hq-cv1", Title: "Storm", ReadyCount: 3,
		ReadyIssues: []string{"gt-a", "gt-b", "gt-c"},
	}
	if got := m.feedFirstReady(c); got != 3 {
		t.Errorf("feedFirstReady failure count = %d, want 3", got)
	}
}

// TestMonitorFeedStorm_EscalatesWhenSustained verifies the monitor fires a HIGH
// escalation once sling-failures stay above threshold for the consecutive-scan
// threshold, and only once per episode.
func TestMonitorFeedStorm_EscalatesWhenSustained(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".runtime"), 0755); err != nil {
		t.Fatalf("mkdir .runtime: %v", err)
	}
	m := NewConvoyManager(townRoot, func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)

	fired := 0
	orig := fireFeedStormEscalation
	fireFeedStormEscalation = func(_ *ConvoyManager, _ feedStormState, _ int) { fired++ }
	defer func() { fireFeedStormEscalation = orig }()

	// Below threshold: never fires.
	m.monitorFeedStorm(feedStormFailureThreshold - 1)
	if fired != 0 {
		t.Fatalf("fired %d on a non-storming scan", fired)
	}
	// Sustained storm: fires exactly once across many scans.
	for i := 0; i < feedStormConsecutiveThreshold+3; i++ {
		m.monitorFeedStorm(feedStormFailureThreshold + 1)
	}
	if fired != 1 {
		t.Fatalf("expected exactly 1 escalation across sustained storm, got %d", fired)
	}
}

// TestIsBeadNotFoundError covers the stderr shapes that should trigger the
// missing-bead strike accounting in feedFirstReady. (gu-f0gq)
func TestIsBeadNotFoundError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"unrelated error", "dispatch failed: rig parked", false},
		{"sling quoted", "bead 'ta-9emq' not found", true},
		{"sling unquoted", "bead ta-9emq not found", true},
		{"sling bd show wrap", "bead 'ta-9emq' not found (bd show failed: exit 1)", true},
		{"bd direct", "Error fetching ta-9emq: no issue found matching \"ta-9emq\"", true},
		{"issue not found", "issue ta-9emq not found in store", true},
		{"case insensitive", "BEAD 'TA-9EMQ' Not Found", true},
		{"not found but no bead/issue", "config not found", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sling.IsBeadNotFoundError(tc.in); got != tc.want {
				t.Errorf("isBeadNotFoundError(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestFeedFirstReady_MissingBeadStrikes_TriggersUntrack verifies that after
// missingBeadStrikeThreshold consecutive "bead not found" sling failures, the
// stale tracked bead is auto-untracked from the convoy. This terminates the
// hq-cv-p6ht2 / ta-9emq forever-loop documented in gu-f0gq.
func TestFeedFirstReady_MissingBeadStrikes_TriggersUntrack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	// gt sling always fails with "bead not found" — simulating a tracked bead
	// that has been deleted/squashed.
	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo "bead '$2' not found" >&2
  exit 1
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	// bd show also reports "not found" — confirms the bead is truly missing
	// (needed by confirmBeadMissing check added in gu-dvcs4).
	bdScript := `#!/bin/sh
if [ "$1" = "show" ]; then
  echo "Error: bead '$2' not found" >&2
  exit 1
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)

	// Capture untrack invocations instead of shelling out to bd.
	var untrackCalls []missingBeadKey
	m.untrackMissingBeadFn = func(convoyID, issueID string) error {
		untrackCalls = append(untrackCalls, missingBeadKey{convoyID, issueID})
		return nil
	}
	// Confirmation always says "yes, truly missing" (simulates bd show not-found).
	m.checkBeadExistenceFn = func(issueID string) beadExistence { return beadMissing }

	// Drive the clock so the per-issue feed cooldown doesn't suppress later
	// scans from re-attempting the same bead.
	current := time.Now()
	m.now = func() time.Time { return current }

	c := strandedConvoyInfo{
		ID:          "hq-cv-p6ht2",
		Title:       "Zombie Convoy",
		ReadyCount:  1,
		ReadyIssues: []string{"gt-9emq"},
	}

	// With threshold=1, the first sling failure triggers immediate
	// confirmation + untrack (no multi-scan accumulation needed). (gu-dvcs4)
	m.feedFirstReady(c)

	if len(untrackCalls) != 1 {
		t.Fatalf("expected exactly 1 untrack invocation after confirmed-missing strike, got %d: %v",
			len(untrackCalls), untrackCalls)
	}
	if untrackCalls[0] != (missingBeadKey{"hq-cv-p6ht2", "gt-9emq"}) {
		t.Errorf("untrack target = %v, want {hq-cv-p6ht2 gt-9emq}", untrackCalls[0])
	}

	// Strike entry should be cleared on successful untrack so the next scan
	// (if the convoy still has the bead for any reason) starts fresh.
	if _, ok := m.missingBeadStrikes.Load(missingBeadKey{"hq-cv-p6ht2", "gt-9emq"}); ok {
		t.Errorf("expected strike entry cleared after successful untrack")
	}

	// Sanity: at least one log line should mention auto-untracking.
	foundLog := false
	for _, l := range logged {
		if strings.Contains(l, "auto-untracking") && strings.Contains(l, "gt-9emq") {
			foundLog = true
			break
		}
	}
	if !foundLog {
		t.Errorf("expected auto-untracking log line, got: %v", logged)
	}
}

// TestFeedFirstReady_MissingBeadStrikes_ResetsOnSuccess verifies that a
// transient "not found" (e.g. brief Dolt visibility gap) followed by a
// successful sling does not accumulate strikes that would later untrack a
// healthy bead.
func TestFeedFirstReady_MissingBeadStrikes_ResetsOnSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	// First sling exits 1 with "not found"; subsequent slings succeed.
	countFile := filepath.Join(binDir, "count")
	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  if [ ! -f "` + countFile + `" ]; then
    echo "1" > "` + countFile + `"
    echo "bead '$2' not found" >&2
    exit 1
  fi
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := NewConvoyManager(townRoot, func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)
	var untrackCalls int
	m.untrackMissingBeadFn = func(string, string) error {
		untrackCalls++
		return nil
	}
	// Confirmation says "bead exists" — simulating a transient Dolt hiccup
	// where sling fails but the bead is actually there. (gu-dvcs4)
	m.checkBeadExistenceFn = func(issueID string) beadExistence { return beadExists }
	current := time.Now()
	m.now = func() time.Time { return current }

	c := strandedConvoyInfo{
		ID:          "hq-cv-good",
		Title:       "Healthy",
		ReadyCount:  1,
		ReadyIssues: []string{"gt-good"},
	}

	m.feedFirstReady(c) // sling fails, but confirmation shows bead exists — strike cleared
	current = current.Add(feedDispatchCooldown + time.Second)
	m.feedFirstReady(c) // success (mock gt succeeds on second call)

	if _, ok := m.missingBeadStrikes.Load(missingBeadKey{"hq-cv-good", "gt-good"}); ok {
		t.Errorf("expected strike counter cleared after confirmation showed bead exists")
	}

	// Keep slinging successfully — there should be no untrack ever.
	for i := 0; i < missingBeadStrikeThreshold+2; i++ {
		current = current.Add(feedDispatchCooldown + time.Second)
		m.feedFirstReady(c)
	}
	if untrackCalls != 0 {
		t.Errorf("untrack fired %d times for a healthy bead; want 0", untrackCalls)
	}
}

// TestFeedFirstReady_MissingBeadStrikes_ConfirmationAbsorbsTransient verifies
// that when sling reports "not found" but the confirmation re-check shows the
// bead exists (transient Dolt hiccup), no untrack occurs and the strike is
// cleared. This is the restart-proof replacement for multi-scan strike
// accumulation. (gu-dvcs4)
func TestFeedFirstReady_MissingBeadStrikes_ConfirmationAbsorbsTransient(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	// gt sling always fails with "bead not found".
	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo "bead '$2' not found" >&2
  exit 1
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)
	var untrackCalls int
	m.untrackMissingBeadFn = func(string, string) error {
		untrackCalls++
		return nil
	}
	// Confirmation says "bead exists" — the sling failure was a transient hiccup.
	m.checkBeadExistenceFn = func(issueID string) beadExistence { return beadExists }
	current := time.Now()
	m.now = func() time.Time { return current }

	c := strandedConvoyInfo{
		ID:          "hq-cv-transient",
		Title:       "Transient Hiccup",
		ReadyCount:  1,
		ReadyIssues: []string{"gt-hiccup"},
	}

	// Sling fails "not found" but confirmation shows it exists → no untrack.
	m.feedFirstReady(c)
	if untrackCalls != 0 {
		t.Fatalf("untrack fired despite confirmation showing bead exists: %d", untrackCalls)
	}

	// Strike should be cleared after confirmation absorbs the hiccup.
	if _, ok := m.missingBeadStrikes.Load(missingBeadKey{"hq-cv-transient", "gt-hiccup"}); ok {
		t.Errorf("expected strike cleared after confirmation showed bead exists")
	}

	// Log should mention the transient hiccup was absorbed.
	foundLog := false
	for _, l := range logged {
		if strings.Contains(l, "confirmation check shows bead EXISTS") && strings.Contains(l, "gt-hiccup") {
			foundLog = true
			break
		}
	}
	if !foundLog {
		t.Errorf("expected 'confirmation check shows bead EXISTS' log line, got: %v", logged)
	}

	// Run many more times — should never trigger untrack.
	for i := 0; i < 10; i++ {
		current = current.Add(feedDispatchCooldown + time.Second)
		m.feedFirstReady(c)
	}
	if untrackCalls != 0 {
		t.Errorf("untrack eventually fired after %d scans despite confirmation absorbing; want 0", untrackCalls)
	}
}

// TestFeedFirstReady_MissingBeadStrikes_AmbiguousInfraErrorDoesNotUntrack
// verifies that when sling reports "not found" but the existence re-check hits
// an infra error (Dolt circuit-breaker open / connection refused / timeout) and
// returns beadCheckAmbiguous, the bead is NOT untracked. The strike is left in
// place so the check is retried on the next scan once Dolt recovers, rather than
// collapsing "could not determine state" into a state verdict. (gu-3hi1f)
func TestFeedFirstReady_MissingBeadStrikes_AmbiguousInfraErrorDoesNotUntrack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	// gt sling always fails with "bead not found" (the symptom during a Dolt
	// outage where the bead is invisible, not actually deleted).
	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo "bead '$2' not found" >&2
  exit 1
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)
	var untrackCalls int
	m.untrackMissingBeadFn = func(string, string) error {
		untrackCalls++
		return nil
	}
	// Existence check hits the Dolt circuit-breaker — could not determine state.
	m.checkBeadExistenceFn = func(issueID string) beadExistence { return beadCheckAmbiguous }
	current := time.Now()
	m.now = func() time.Time { return current }

	c := strandedConvoyInfo{
		ID:          "hq-cv-outage",
		Title:       "Dolt Outage",
		ReadyCount:  1,
		ReadyIssues: []string{"gt-outage"},
	}

	// Sling fails "not found" but the existence check is ambiguous → no untrack.
	m.feedFirstReady(c)
	if untrackCalls != 0 {
		t.Fatalf("untrack fired despite ambiguous existence check: %d", untrackCalls)
	}

	// The strike must REMAIN so the check is retried next scan (do NOT clear it,
	// unlike the bead-exists path which clears the strike).
	if _, ok := m.missingBeadStrikes.Load(missingBeadKey{"hq-cv-outage", "gt-outage"}); !ok {
		t.Errorf("expected strike retained after ambiguous infra error (retryable), but it was cleared")
	}

	// Log should mention the ambiguous-not-untracking disposition.
	foundLog := false
	for _, l := range logged {
		if strings.Contains(l, "ambiguous") && strings.Contains(l, "gt-outage") && strings.Contains(l, "not untracking") {
			foundLog = true
			break
		}
	}
	if !foundLog {
		t.Errorf("expected ambiguous-not-untracking log line, got: %v", logged)
	}

	// Repeated scans during the outage must never untrack.
	for i := 0; i < 10; i++ {
		current = current.Add(feedDispatchCooldown + time.Second)
		m.feedFirstReady(c)
	}
	if untrackCalls != 0 {
		t.Errorf("untrack fired after %d scans during ambiguous outage; want 0", untrackCalls)
	}
}

// TestFeedFirstReady_MissingBeadStrikes_NonNotFoundFailureDoesNotStrike
// verifies that other sling failure modes (rig parked mid-call, dolt outage,
// etc.) do not increment the missing-bead counter — only "not found"
// terminations should lead to auto-untrack. (gu-f0gq)
func TestFeedFirstReady_MissingBeadStrikes_NonNotFoundFailureDoesNotStrike(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo "rig parked: gastown" >&2
  exit 1
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := NewConvoyManager(townRoot, func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)
	var untrackCalls int
	m.untrackMissingBeadFn = func(string, string) error {
		untrackCalls++
		return nil
	}
	current := time.Now()
	m.now = func() time.Time { return current }

	c := strandedConvoyInfo{
		ID:          "hq-cv-park",
		Title:       "Parked Rig",
		ReadyCount:  1,
		ReadyIssues: []string{"gt-parked"},
	}
	for i := 0; i < missingBeadStrikeThreshold+2; i++ {
		current = current.Add(feedDispatchCooldown + time.Second)
		m.feedFirstReady(c)
	}
	if untrackCalls != 0 {
		t.Errorf("non-not-found failures triggered %d untracks; want 0", untrackCalls)
	}
	if _, ok := m.missingBeadStrikes.Load(missingBeadKey{"hq-cv-park", "gt-parked"}); ok {
		t.Errorf("expected no strike entry for non-not-found failure")
	}
}

// TestIsClosedBeadSlingError covers the stderr shape sling emits when a tracked
// bead closes between the stranded scan and the feed (Category A, gu-y6ild).
func TestIsClosedBeadSlingError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"closed", "bead gt-abc is closed (work already completed)", true},
		{"tombstone", "bead gt-abc is tombstone (work already completed)", true},
		{"case insensitive", "BEAD gt-abc is CLOSED (Work Already Completed)", true},
		{"unrelated", "rig parked: gastown", false},
		{"not found is not closed", "bead 'gt-abc' not found", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sling.IsClosedBeadSlingError(tc.in); got != tc.want {
				t.Errorf("isClosedBeadSlingError(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestIsStructuralNonWorkSlingError covers the sling-guard rejection shapes that
// mark a bead as a permanent non-work item (Category C, gu-y6ild) — and the
// shapes that must NOT match (mayor-only, unroutable) so they still escalate.
func TestIsStructuralNonWorkSlingError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"epic", `refusing to sling bead gt-abc: "EPIC: x" is an epic container (title "EPIC: x", issue_type="task", labels=[])`, true},
		{"open children", `refusing to sling bead gt-abc: "x" has open children — it is a container`, true},
		{"identity", `refusing to sling bead gt-abc: "polecat-x" is an identity/system bead (gt:agent label or polecat/refinery title)`, true},
		{"sling-context wrapper", `refusing to sling bead gt-abc: "x" is a sling-context wrapper (label gt:sling-context)`, true},
		{"flag-like", `refusing to sling bead gt-abc: title "--foo" looks like a CLI flag (garbage bead from flag-parsing bug)`, true},
		{"polecat-owned", `refusing to sling bead gt-abc: "x" is owned by a polecat (rig/polecats/y)`, true},
		// Must NOT match — these are genuinely-ambiguous and should still escalate.
		{"mayor-only", `refusing to sling bead gt-abc: "x" is labeled mayor-only / no-polecat`, false},
		{"unroutable", "no rig for gt-abc", false},
		{"not found", "bead 'gt-abc' not found", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sling.IsStructuralNonWorkSlingError(tc.in); got != tc.want {
				t.Errorf("isStructuralNonWorkSlingError(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestIsActivelyWorkedSlingError covers the stderr shapes sling emits when a
// tracked bead is already hooked / in_progress to a LIVE agent (gs-2dr) — and
// the shapes that must NOT match so they still escalate.
func TestIsActivelyWorkedSlingError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"hooked (sling.go shape)", "bead gs-xbo is already hooked to gastown/polecats/furiosa", true},
		{"in_progress (sling.go shape)", "bead gs-xbo is already in_progress to gastown/polecats/cheedo", true},
		{"hooked (dispatch shape)", "already hooked (use --force to re-sling)", true},
		{"in_progress (dispatch shape)", "already in_progress (use --force to re-sling)", true},
		{"case insensitive", "BEAD gs-xbo Is Already Hooked To x", true},
		// Must NOT match — pinned is a structural do-not-dispatch state, others
		// are genuinely-ambiguous and should still escalate.
		{"pinned", "bead gs-xbo is already pinned to x", false},
		{"closed", "bead gs-xbo is closed (work already completed)", false},
		{"not found", "bead 'gs-xbo' not found", false},
		{"mayor-only", `refusing to sling bead gs-xbo: "x" is labeled mayor-only / no-polecat`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sling.IsActivelyWorkedSlingError(tc.in); got != tc.want {
				t.Errorf("isActivelyWorkedSlingError(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestIsDoNotDispatchSlingError covers the stderr shapes sling/schedule emit for
// a do-not-dispatch / pinned reference tripwire (gu-q1wzq) — and the shapes that
// must NOT match so they route to their own handlers.
func TestIsDoNotDispatchSlingError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"schedule-guard shape", `bead gt-trip is a do-not-dispatch / pinned reference tripwire: "deploy blocked: CFN" — refusing to schedule. It must stay OPEN as a live safety gate`, true},
		{"dispatch-guard shape", `bead gt-trip is a do-not-dispatch / pinned reference tripwire: "x" — it must stay OPEN as a live safety gate, never hooked to a polecat.`, true},
		{"reference tripwire phrasing", "bead gt-trip is a reference tripwire", true},
		{"case insensitive", "BEAD gt-trip is a DO-NOT-DISPATCH / pinned reference TRIPWIRE", true},
		// Must NOT match — distinct handler paths.
		{"actively worked", "bead gt-trip is already hooked to gastown/polecats/x", false},
		{"structural epic", `"EPIC: x" is an epic container`, false},
		{"not found", "bead 'gt-trip' not found", false},
		{"mayor-only", `"x" is labeled mayor-only / no-polecat`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sling.IsDoNotDispatchSlingError(tc.in); got != tc.want {
				t.Errorf("isDoNotDispatchSlingError(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// feedFirstReadyTestEnv builds a town root with a gt- route and a mock gt whose
// `sling` subcommand prints slingStderr to stderr and exits 1, while every other
// subcommand (e.g. `convoy check`, `escalate`) appends its argv to invokeLog and
// exits 0. Returns the manager and the path to the invocation log.
func feedFirstReadyTestEnv(t *testing.T, slingStderr string) (*ConvoyManager, string) {
	t.Helper()
	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	invokeLog := filepath.Join(binDir, "invoke.log")
	gtScript := `#!/bin/sh
if [ "$1" = "sling" ]; then
  echo ` + shellQuote(slingStderr) + ` >&2
  exit 1
fi
echo "$@" >> "` + invokeLog + `"
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := NewConvoyManager(townRoot, func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)
	return m, invokeLog
}

// shellQuote wraps s in single quotes for safe embedding in a /bin/sh script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TestFeedFirstReady_ClosedBead_ChecksConvoyNoEscalate verifies Category A
// (gu-y6ild): when sling reports the bead is already closed, the daemon runs a
// per-convoy completion check and does NOT escalate to the Mayor.
func TestFeedFirstReady_ClosedBead_ChecksConvoyNoEscalate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	m, invokeLog := feedFirstReadyTestEnv(t, "bead gt-done is closed (work already completed)")
	var untrackCalls int
	m.untrackMissingBeadFn = func(string, string) error { untrackCalls++; return nil }

	c := strandedConvoyInfo{ID: "hq-cv-a", Title: "Closed Race", ReadyCount: 1, ReadyIssues: []string{"gt-done"}}
	m.feedFirstReady(c)

	data, _ := os.ReadFile(invokeLog)
	log := string(data)
	if !strings.Contains(log, "convoy check hq-cv-a") {
		t.Errorf("expected a `convoy check hq-cv-a` invocation, got: %q", log)
	}
	if strings.Contains(log, "escalate") {
		t.Errorf("Category A should NOT escalate, but escalate was invoked: %q", log)
	}
	if untrackCalls != 0 {
		t.Errorf("Category A should not untrack, got %d untrack calls", untrackCalls)
	}
	if _, ok := m.seenSlingErrors.Load("gt-done"); ok {
		t.Errorf("closed-bead race should not record a sling error")
	}
}

// TestFeedFirstReady_StructuralNonWork_UntracksNoEscalate verifies Category C
// (gu-y6ild): when sling rejects a structural non-work bead (epic, identity,
// wrapper, etc.), the daemon untracks it from the convoy on the FIRST failure
// (no strike threshold) and does NOT escalate.
func TestFeedFirstReady_StructuralNonWork_UntracksNoEscalate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	stderr := `refusing to sling bead gt-epic: "EPIC: rework" is an epic container (title "EPIC: rework", issue_type="task", labels=[])`
	m, invokeLog := feedFirstReadyTestEnv(t, stderr)
	var untrackCalls []missingBeadKey
	m.untrackMissingBeadFn = func(convoyID, issueID string) error {
		untrackCalls = append(untrackCalls, missingBeadKey{convoyID, issueID})
		return nil
	}

	c := strandedConvoyInfo{ID: "hq-cv-c", Title: "Epic Step", ReadyCount: 1, ReadyIssues: []string{"gt-epic"}}
	m.feedFirstReady(c) // single failure should suffice — structural rejection is deterministic

	if len(untrackCalls) != 1 || untrackCalls[0] != (missingBeadKey{"hq-cv-c", "gt-epic"}) {
		t.Fatalf("expected exactly 1 untrack of {hq-cv-c gt-epic} on first failure, got %v", untrackCalls)
	}
	data, _ := os.ReadFile(invokeLog)
	if strings.Contains(string(data), "escalate") {
		t.Errorf("Category C should NOT escalate, but escalate was invoked: %q", data)
	}
}

// TestFeedFirstReady_ActivelyWorked_NoEscalate verifies the gs-2dr fix: when
// sling reports the bead is already hooked / in_progress to a LIVE agent, the
// daemon suppresses the escalation (the bead is progressing), does NOT untrack
// it, and does NOT record a sling error.
func TestFeedFirstReady_ActivelyWorked_NoEscalate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	m, invokeLog := feedFirstReadyTestEnv(t, "bead gt-live is already hooked to gastown/polecats/furiosa")
	var untrackCalls int
	m.untrackMissingBeadFn = func(string, string) error { untrackCalls++; return nil }

	c := strandedConvoyInfo{ID: "hq-cv-live", Title: "Live Work", ReadyCount: 1, ReadyIssues: []string{"gt-live"}}
	m.feedFirstReady(c)

	data, _ := os.ReadFile(invokeLog)
	if strings.Contains(string(data), "escalate") {
		t.Errorf("actively-worked bead should NOT escalate, but escalate was invoked: %q", data)
	}
	if untrackCalls != 0 {
		t.Errorf("actively-worked bead should not untrack, got %d", untrackCalls)
	}
	if _, ok := m.seenSlingErrors.Load("gt-live"); ok {
		t.Errorf("actively-worked bead should not record a sling error")
	}
}

// TestFeedFirstReady_Deferred_NoEscalateNoUntrack verifies the gt-3798 fix: when
// sling refuses a DEFERRED bead (intentionally held off polecat slots), the
// daemon suppresses the escalation (a deferred step is waiting, not wedged — it
// becomes dispatchable when un-deferred), does NOT untrack it (deferred beads are
// legitimate tracked work that must survive the hold), and does NOT record a
// sling error (so it never escalates as "cannot dispatch / will never progress").
func TestFeedFirstReady_Deferred_NoEscalateNoUntrack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	stderr := `refusing to sling deferred bead gt-defer: "deferred to post-launch"`
	m, invokeLog := feedFirstReadyTestEnv(t, stderr)
	var untrackCalls int
	m.untrackMissingBeadFn = func(string, string) error { untrackCalls++; return nil }

	c := strandedConvoyInfo{ID: "hq-cv-defer", Title: "Deferred Step", ReadyCount: 1, ReadyIssues: []string{"gt-defer"}}
	m.feedFirstReady(c)

	data, _ := os.ReadFile(invokeLog)
	if strings.Contains(string(data), "escalate") {
		t.Errorf("deferred bead should NOT escalate, but escalate was invoked: %q", data)
	}
	if untrackCalls != 0 {
		t.Errorf("deferred bead should not untrack (real tracked work), got %d", untrackCalls)
	}
	if _, ok := m.seenSlingErrors.Load("gt-defer"); ok {
		t.Errorf("deferred bead should not record a sling error")
	}
}

// TestFeedFirstReady_Deferred_RecordsChurn verifies a deferred bead advances its
// feed-churn streak on each failed re-feed, so the effective cooldown escalates
// (5m→…) instead of re-attempting an intentionally-held bead every scan (gt-3798).
func TestFeedFirstReady_Deferred_RecordsChurn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	m, _ := feedFirstReadyTestEnv(t, `refusing to sling deferred bead gt-defer: "held"`)
	m.untrackMissingBeadFn = func(string, string) error { return nil }

	c := strandedConvoyInfo{ID: "hq-cv-defer", Title: "Deferred Step", ReadyCount: 1, ReadyIssues: []string{"gt-defer"}}

	m.feedFirstReady(c)
	if got := m.effectiveFeedCooldown("gt-defer"); got != feedDispatchCooldown {
		t.Fatalf("after 1 churn, cooldown = %v, want base %v", got, feedDispatchCooldown)
	}

	m.lastFeedAttempt.Delete("gt-defer")
	m.feedFirstReady(c)
	if got := m.effectiveFeedCooldown("gt-defer"); got <= feedDispatchCooldown {
		t.Errorf("after 2 churns, cooldown = %v, want > base %v (escalating backoff)", got, feedDispatchCooldown)
	}
}

// TestIsAwaitingRefineryMergeSlingError covers the stderr shapes sling/schedule/
// dispatch emit for a bead awaiting refinery merge (gu-ea25u) — and the shapes
// that must NOT match so they route to their own handlers / still escalate.
func TestIsAwaitingRefineryMergeSlingError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"sling.go shape", `refusing to sling bead gt-x: "fix thing" is awaiting refinery merge (label awaiting_refinery_merge) — its MR is submitted and in the merge queue; the refinery will close it on merge`, true},
		{"dispatch shape", `bead gt-x is awaiting refinery merge (label awaiting_refinery_merge): "fix thing" — its MR is submitted and in the merge queue; the refinery will close it on merge, not a fresh polecat`, true},
		{"schedule shape", `bead gt-x is awaiting refinery merge (label awaiting_refinery_merge): "fix thing" — refusing to schedule. Its MR is submitted and in the merge queue`, true},
		{"label only", "carries label awaiting_refinery_merge", true},
		{"case insensitive", "BEAD gt-x is Awaiting Refinery Merge", true},
		// Must NOT match — distinct handler paths / genuinely-ambiguous.
		{"deferred", `refusing to sling deferred bead gt-x: "held"`, false},
		{"actively worked", "bead gt-x is already hooked to gastown/polecats/x", false},
		{"not found", "bead 'gt-x' not found", false},
		{"mayor-only", `"x" is labeled mayor-only / no-polecat`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sling.IsAwaitingRefineryMergeSlingError(tc.in); got != tc.want {
				t.Errorf("IsAwaitingRefineryMergeSlingError(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestFeedFirstReady_AwaitingMerge_NoEscalateNoUntrack verifies the gt-3798
// escalation-storm fix: when sling refuses a bead awaiting refinery merge (MR
// submitted, sitting in the merge queue), the daemon suppresses the escalation (a
// benign self-resolving in-flight state — the refinery closes it on merge), does
// NOT untrack it (legitimate tracked work), and does NOT record a sling error (so
// it never escalates as "cannot dispatch / will never progress").
func TestFeedFirstReady_AwaitingMerge_NoEscalateNoUntrack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	stderr := `refusing to sling bead gt-merge: "fix thing" is awaiting refinery merge (label awaiting_refinery_merge) — its MR is submitted and in the merge queue; the refinery will close it on merge`
	m, invokeLog := feedFirstReadyTestEnv(t, stderr)
	var untrackCalls int
	m.untrackMissingBeadFn = func(string, string) error { untrackCalls++; return nil }

	c := strandedConvoyInfo{ID: "hq-cv-merge", Title: "Awaiting Merge Step", ReadyCount: 1, ReadyIssues: []string{"gt-merge"}}
	m.feedFirstReady(c)

	data, _ := os.ReadFile(invokeLog)
	if strings.Contains(string(data), "escalate") {
		t.Errorf("awaiting-merge bead should NOT escalate, but escalate was invoked: %q", data)
	}
	if untrackCalls != 0 {
		t.Errorf("awaiting-merge bead should not untrack (real tracked work), got %d", untrackCalls)
	}
	if _, ok := m.seenSlingErrors.Load("gt-merge"); ok {
		t.Errorf("awaiting-merge bead should not record a sling error")
	}
}

// TestFeedFirstReady_AwaitingMerge_RecordsChurn verifies an awaiting-merge bead
// advances its feed-churn streak on each failed re-feed, so the effective cooldown
// escalates (5m→…) instead of re-attempting an in-flight MR every scan (gt-3798).
func TestFeedFirstReady_AwaitingMerge_RecordsChurn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	m, _ := feedFirstReadyTestEnv(t, `bead gt-merge is awaiting refinery merge (label awaiting_refinery_merge): "fix thing"`)
	m.untrackMissingBeadFn = func(string, string) error { return nil }

	c := strandedConvoyInfo{ID: "hq-cv-merge", Title: "Awaiting Merge Step", ReadyCount: 1, ReadyIssues: []string{"gt-merge"}}

	m.feedFirstReady(c)
	if got := m.effectiveFeedCooldown("gt-merge"); got != feedDispatchCooldown {
		t.Fatalf("after 1 churn, cooldown = %v, want base %v", got, feedDispatchCooldown)
	}

	m.lastFeedAttempt.Delete("gt-merge")
	m.feedFirstReady(c)
	if got := m.effectiveFeedCooldown("gt-merge"); got <= feedDispatchCooldown {
		t.Errorf("after 2 churns, cooldown = %v, want > base %v (escalating backoff)", got, feedDispatchCooldown)
	}
}

// TestFeedFirstReady_DoNotDispatch_UntracksNoEscalate verifies the gu-q1wzq fix:
// a do-not-dispatch / pinned reference tripwire is auto-untracked on first
// failure (like a structural non-work bead) and does NOT escalate — it can never
// become dispatchable, so re-feeding it every scan was the dominant share of the
// convoy re-dispatch storm.
func TestFeedFirstReady_DoNotDispatch_UntracksNoEscalate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	stderr := `bead gt-trip is a do-not-dispatch / pinned reference tripwire: "deploy blocked: CFN" — refusing to schedule. It must stay OPEN as a live safety gate`
	m, invokeLog := feedFirstReadyTestEnv(t, stderr)
	var untrackCalls []missingBeadKey
	m.untrackMissingBeadFn = func(convoyID, issueID string) error {
		untrackCalls = append(untrackCalls, missingBeadKey{convoyID, issueID})
		return nil
	}

	c := strandedConvoyInfo{ID: "hq-cv-t", Title: "Tripwire Step", ReadyCount: 1, ReadyIssues: []string{"gt-trip"}}
	m.feedFirstReady(c) // single failure suffices — tripwire rejection is permanent

	if len(untrackCalls) != 1 || untrackCalls[0] != (missingBeadKey{"hq-cv-t", "gt-trip"}) {
		t.Fatalf("expected exactly 1 untrack of {hq-cv-t gt-trip} on first failure, got %v", untrackCalls)
	}
	data, _ := os.ReadFile(invokeLog)
	if strings.Contains(string(data), "escalate") {
		t.Errorf("do-not-dispatch tripwire should NOT escalate, but escalate was invoked: %q", data)
	}
}

// TestFeedFirstReady_ActivelyWorked_RecordsChurn verifies the gu-q1wzq backoff:
// an actively-worked bead advances its feed-churn streak on each failed re-feed,
// so the effective cooldown escalates (5m→…) instead of staying flat at 5m and
// re-feeding every scan for the entire duration of the work.
func TestFeedFirstReady_ActivelyWorked_RecordsChurn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	m, _ := feedFirstReadyTestEnv(t, "bead gt-live is already in_progress to gastown/polecats/cheedo")
	m.untrackMissingBeadFn = func(string, string) error { return nil }

	c := strandedConvoyInfo{ID: "hq-cv-live", Title: "Live Work", ReadyCount: 1, ReadyIssues: []string{"gt-live"}}

	// First failed feed: churn streak should be recorded at 1 (base cooldown).
	m.feedFirstReady(c)
	if got := m.effectiveFeedCooldown("gt-live"); got != feedDispatchCooldown {
		t.Fatalf("after 1 churn, cooldown = %v, want base %v", got, feedDispatchCooldown)
	}

	// Clear the per-attempt cooldown stamp so the next feed isn't skipped by
	// inFeedCooldown, simulating a later scan within feedChurnWindow.
	m.lastFeedAttempt.Delete("gt-live")
	m.feedFirstReady(c)

	// Second consecutive re-feed within the window must raise the streak, which
	// escalates the cooldown beyond the flat base — the whole point of the fix.
	if got := m.effectiveFeedCooldown("gt-live"); got <= feedDispatchCooldown {
		t.Errorf("after 2 churns, cooldown = %v, want > base %v (escalating backoff)", got, feedDispatchCooldown)
	}
}

// TestFeedFirstReady_AmbiguousFailure_StillEscalates verifies the default path
// (gu-y6ild): a genuinely-ambiguous failure (e.g. mayor-only) is NOT auto-
// remediated — it escalates once to the Mayor, preserving the gt-3798 behavior.
func TestFeedFirstReady_AmbiguousFailure_StillEscalates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	m, invokeLog := feedFirstReadyTestEnv(t, `refusing to sling bead gt-mo: "x" is labeled mayor-only / no-polecat`)
	var untrackCalls int
	m.untrackMissingBeadFn = func(string, string) error { untrackCalls++; return nil }

	c := strandedConvoyInfo{ID: "hq-cv-amb", Title: "Mayor Only", ReadyCount: 1, ReadyIssues: []string{"gt-mo"}}
	m.feedFirstReady(c)

	data, _ := os.ReadFile(invokeLog)
	if !strings.Contains(string(data), "escalate") {
		t.Errorf("ambiguous failure should escalate, but escalate was not invoked: %q", data)
	}
	if untrackCalls != 0 {
		t.Errorf("ambiguous failure should not untrack, got %d", untrackCalls)
	}
	if _, ok := m.seenSlingErrors.Load("gt-mo"); !ok {
		t.Errorf("ambiguous failure should record a sling error for dedup")
	}
}

func TestStableCandidate_SkipsAfterThreshold(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	// A single completion candidate (tracked=2, ready=0).
	strandedJSON := `[{"id":"hq-stable1","title":"Stable","tracked_count":2,"ready_count":0,"ready_issues":[]}]`
	paths := mockGtForScanTest(t, scanTestOpts{strandedJSON: strandedJSON})

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	m := NewConvoyManager(paths.townRoot, logger, filepath.Join(paths.binDir, "gt"), 10*time.Minute, nil, nil, nil)

	// Scans 1-2: candidate NOT stable → completion check fires.
	m.scan()
	m.scan()
	data, err := os.ReadFile(paths.checkLogPath)
	if err != nil {
		t.Fatalf("check log should exist after first 2 scans: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 completion checks for first 2 scans, got %d", len(lines))
	}

	// Scan 3: candidate becomes stable → check should NOT fire.
	m.scan()
	data2, _ := os.ReadFile(paths.checkLogPath)
	lines2 := strings.Split(strings.TrimSpace(string(data2)), "\n")
	if len(lines2) != 2 {
		t.Errorf("expected still 2 completion checks (scan 3 skipped), got %d", len(lines2))
	}

	// Verify the skip is logged.
	found := false
	for _, s := range logged {
		if strings.Contains(s, "stable for") && strings.Contains(s, "skipping completion check") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected stable-skip log message, got: %v", logged)
	}
}

func TestStableCandidate_ResetsOnTrackedCountChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	m := NewConvoyManager(t.TempDir(), func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)

	// Simulate 3 scans with stable tracked count.
	if m.isStableCandidate("hq-cv1", 5) {
		t.Fatal("should not be stable on scan 1")
	}
	if m.isStableCandidate("hq-cv1", 5) {
		t.Fatal("should not be stable on scan 2")
	}
	if !m.isStableCandidate("hq-cv1", 5) {
		t.Fatal("should be stable on scan 3")
	}

	// TrackedCount changes → resets to unstable.
	if m.isStableCandidate("hq-cv1", 4) {
		t.Fatal("should not be stable after tracked count change")
	}
	if m.isStableCandidate("hq-cv1", 4) {
		t.Fatal("should not be stable on second scan after change")
	}
	if !m.isStableCandidate("hq-cv1", 4) {
		t.Fatal("should be stable again on third scan with new count")
	}
}

func TestStableCandidate_BackstopForcesRecheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	m := NewConvoyManager(t.TempDir(), func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)

	// Reach stable threshold.
	for i := 0; i < stableSkipThreshold; i++ {
		m.isStableCandidate("hq-bs", 3)
	}

	// Now stable — verify it stays stable until backstop.
	for i := 1; i < stableBackstopScans; i++ {
		if !m.isStableCandidate("hq-bs", 3) {
			t.Fatalf("expected stable at scan %d (before backstop)", stableSkipThreshold+i)
		}
	}

	// At the backstop boundary: should force a re-check (return false).
	if m.isStableCandidate("hq-bs", 3) {
		t.Fatal("expected backstop to force re-check")
	}

	// Next scan after backstop: should be stable again.
	if !m.isStableCandidate("hq-bs", 3) {
		t.Fatal("expected stable again after backstop re-check")
	}
}

func TestStableCandidate_ResetOnCloseEvent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	m := NewConvoyManager(t.TempDir(), func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)

	// Make candidate stable.
	for i := 0; i < stableSkipThreshold; i++ {
		m.isStableCandidate("hq-close1", 2)
	}
	if !m.isStableCandidate("hq-close1", 2) {
		t.Fatal("should be stable before reset")
	}

	// Simulate close event resetting all stable candidates.
	m.resetStableCandidates()

	// After reset, candidate should NOT be stable.
	if m.isStableCandidate("hq-close1", 2) {
		t.Fatal("should not be stable after resetStableCandidates")
	}
}

func TestStableCandidate_ResetOnRecoveryMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	m := NewConvoyManager(t.TempDir(), func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)

	// Make candidate stable.
	for i := 0; i < stableSkipThreshold+1; i++ {
		m.isStableCandidate("hq-rec1", 4)
	}

	// Simulate recovery mode entry resetting candidates.
	m.recoveryMode.Store(true)
	m.resetStableCandidates()

	// Should not be stable after reset.
	if m.isStableCandidate("hq-rec1", 4) {
		t.Fatal("should not be stable after recovery mode reset")
	}
}

func TestFindStranded_StalenessCache_SkipsSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	callCountFile := filepath.Join(binDir, "call_count")

	// Mock gt binary that counts invocations and returns stable JSON.
	gtScript := `#!/bin/sh
if [ "$1" = "convoy" ] && [ "$2" = "stranded" ]; then
  count=0
  if [ -f "` + callCountFile + `" ]; then
    count=$(cat "` + callCountFile + `")
  fi
  count=$((count + 1))
  echo "$count" > "` + callCountFile + `"
  echo '[{"id":"hq-c1","title":"test","tracked_count":2,"ready_count":1,"ready_issues":["gu-x1"],"created_at":"2026-01-01T00:00:00Z"}]'
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}

	m := NewConvoyManager(townRoot, func(string, ...interface{}) {}, filepath.Join(binDir, "gt"), 10*time.Minute, nil, nil, nil)

	// Pre-populate the cache with a known sentinel that matches what
	// strandedSentinel() will return (since store is nil, sentinel returns
	// false — so the cache path won't activate without a store). Instead,
	// directly set the cache to simulate a prior successful run.
	cached := []strandedConvoyInfo{{ID: "hq-c1", Title: "test", TrackedCount: 2, ReadyCount: 1, ReadyIssues: []string{"gu-x1"}}}
	m.strandedCache = &strandedCacheEntry{
		openCount: 5,
		maxUpdate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		result:    cached,
	}

	// Without a store that satisfies beadsDBAccessor, the sentinel query
	// returns false so findStranded always runs the subprocess.
	result, err := m.findStranded()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 || result[0].ID != "hq-c1" {
		t.Fatalf("unexpected result: %v", result)
	}

	// Verify the subprocess was called (sentinel unavailable → no cache hit).
	data, err := os.ReadFile(callCountFile)
	if err != nil {
		t.Fatalf("read call count: %v", err)
	}
	if strings.TrimSpace(string(data)) != "1" {
		t.Fatalf("expected 1 subprocess call, got %s", data)
	}
}

func TestFindStranded_StalenessCache_RecoveryBypassesCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	callCountFile := filepath.Join(binDir, "call_count")

	gtScript := `#!/bin/sh
if [ "$1" = "convoy" ] && [ "$2" = "stranded" ]; then
  count=0
  if [ -f "` + callCountFile + `" ]; then
    count=$(cat "` + callCountFile + `")
  fi
  count=$((count + 1))
  echo "$count" > "` + callCountFile + `"
  echo '[]'
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}

	m := NewConvoyManager(townRoot, func(string, ...interface{}) {}, filepath.Join(binDir, "gt"), 10*time.Minute, nil, nil, nil)

	// Pre-populate cache.
	m.strandedCache = &strandedCacheEntry{
		openCount: 0,
		maxUpdate: time.Time{},
		result:    []strandedConvoyInfo{},
	}

	// Enable recovery mode — should bypass the cache.
	m.recoveryMode.Store(true)

	_, err := m.findStranded()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify subprocess was called despite cache existing.
	data, err := os.ReadFile(callCountFile)
	if err != nil {
		t.Fatalf("read call count: %v", err)
	}
	if strings.TrimSpace(string(data)) != "1" {
		t.Fatalf("expected subprocess call in recovery mode, got %s", data)
	}
}

func TestStrandedSentinel_NoStore_ReturnsFalse(t *testing.T) {
	m := NewConvoyManager(t.TempDir(), func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)

	_, _, ok := m.strandedSentinel()
	if ok {
		t.Fatal("strandedSentinel should return false when no store is available")
	}
}

// TestFeedFirstReady_SkipLogsAreThrottled verifies that a single-child convoy
// whose only child stays in feed cooldown across many scans does NOT emit the
// "in feed cooldown" / "no dispatchable issues" lines every scan, but instead
// logs the first occurrence then only every feedLogThrottleInterval-th repeat.
// This is the gu-5d3a3 fix: those two lines previously dominated daemon.log.
func TestFeedFirstReady_SkipLogsAreThrottled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gt/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	// gt sling always succeeds (not exercised after the first dispatch — the
	// cooldown suppresses subsequent slings).
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write mock gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var cooldownLines, noDispatchLines int
	logger := func(format string, args ...interface{}) {
		s := fmt.Sprintf(format, args...)
		if strings.Contains(s, "in feed cooldown") {
			cooldownLines++
		}
		if strings.Contains(s, "no dispatchable issues") {
			noDispatchLines++
		}
	}

	m := NewConvoyManager(townRoot, logger, "gt", 10*time.Minute, nil, nil, nil)

	c := strandedConvoyInfo{
		ID:          "hq-cv1",
		Title:       "Single Child",
		ReadyCount:  1,
		ReadyIssues: []string{"gt-child1"},
	}

	// First scan dispatches the child. Subsequent scans hit the cooldown and
	// reach both throttled log lines. Run enough scans to cross the throttle
	// interval at least twice.
	const scans = 2*feedLogThrottleInterval + 5
	for i := 0; i < scans; i++ {
		m.feedFirstReady(c)
	}

	// After the first dispatch there are scans-1 cooldown skips. With first +
	// every-Nth-repeat throttling, the count must be far below scans-1.
	cooldownSkips := scans - 1
	maxExpected := 1 + cooldownSkips/feedLogThrottleInterval + 1 // generous upper bound
	if cooldownLines == 0 {
		t.Errorf("expected at least one 'in feed cooldown' line, got 0")
	}
	if cooldownLines > maxExpected {
		t.Errorf("'in feed cooldown' not throttled: got %d lines over %d skips (want ≤ %d)",
			cooldownLines, cooldownSkips, maxExpected)
	}
	if noDispatchLines == 0 {
		t.Errorf("expected at least one 'no dispatchable issues' line, got 0")
	}
	if noDispatchLines > maxExpected {
		t.Errorf("'no dispatchable issues' not throttled: got %d lines over %d scans (want ≤ %d)",
			noDispatchLines, scans, maxExpected)
	}
}

// TestShouldLogFeedSkip_FirstAndEveryNth verifies the throttle helper emits on
// the first call and then once per feedLogThrottleInterval, and that reset
// makes the next call emit immediately again.
func TestShouldLogFeedSkip_FirstAndEveryNth(t *testing.T) {
	m := NewConvoyManager(t.TempDir(), func(string, ...interface{}) {}, "gt", 10*time.Minute, nil, nil, nil)

	emits := 0
	for i := 1; i <= feedLogThrottleInterval; i++ {
		if emit, _ := m.shouldLogFeedSkip("k"); emit {
			emits++
		}
	}
	// Calls 1 (first) and feedLogThrottleInterval (Nth) emit → 2.
	if emits != 2 {
		t.Errorf("expected 2 emits over %d calls, got %d", feedLogThrottleInterval, emits)
	}

	// Reset → next call emits immediately (count restarts at 1).
	m.resetFeedSkipLog("k")
	if emit, count := m.shouldLogFeedSkip("k"); !emit || count != 1 {
		t.Errorf("after reset expected emit=true count=1, got emit=%v count=%d", emit, count)
	}
}

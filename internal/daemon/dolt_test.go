package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================================================
// Config and constructor tests
// ============================================================================

func TestDefaultDoltServerConfig_Defaults(t *testing.T) {
	townRoot := "/tmp/test-town"
	cfg := DefaultDoltServerConfig(townRoot)

	if cfg == nil {
		t.Fatal("DefaultDoltServerConfig returned nil")
	}

	if cfg.Enabled {
		t.Error("expected Enabled to default to false (opt-in)")
	}
	if cfg.Port != 3306 {
		t.Errorf("expected default Port 3306, got %d", cfg.Port)
	}
	if cfg.Host != "127.0.0.1" {
		t.Errorf("expected default Host 127.0.0.1, got %q", cfg.Host)
	}
	if cfg.User != "root" {
		t.Errorf("expected default User root, got %q", cfg.User)
	}
	expectedDataDir := filepath.Join(townRoot, "dolt")
	if cfg.DataDir != expectedDataDir {
		t.Errorf("expected DataDir %q, got %q", expectedDataDir, cfg.DataDir)
	}
	expectedLogFile := filepath.Join(townRoot, "daemon", "dolt-server.log")
	if cfg.LogFile != expectedLogFile {
		t.Errorf("expected LogFile %q, got %q", expectedLogFile, cfg.LogFile)
	}
	if !cfg.AutoRestart {
		t.Error("expected AutoRestart to default to true")
	}
	if cfg.RestartDelay != 5*time.Second {
		t.Errorf("expected RestartDelay 5s, got %v", cfg.RestartDelay)
	}
}

func TestNewDoltServerManager_NilConfig(t *testing.T) {
	// Passing nil config should populate with defaults.
	townRoot := "/tmp/test-town"
	m := NewDoltServerManager(townRoot, nil, nil)

	if m == nil {
		t.Fatal("NewDoltServerManager returned nil")
	}
	if m.config == nil {
		t.Fatal("expected config to be populated with defaults")
	}
	if m.config.Port != 3306 {
		t.Errorf("expected default port 3306, got %d", m.config.Port)
	}
	if m.townRoot != townRoot {
		t.Errorf("expected townRoot %q, got %q", townRoot, m.townRoot)
	}
}

func TestNewDoltServerManager_CustomConfig(t *testing.T) {
	cfg := &DoltServerConfig{
		Enabled: true,
		Port:    3308,
		Host:    "custom.host",
	}
	m := NewDoltServerManager("/tmp/t", cfg, nil)

	if m.config != cfg {
		t.Error("expected manager to use the provided config")
	}
	if m.config.Port != 3308 {
		t.Errorf("expected port 3308, got %d", m.config.Port)
	}
}

// ============================================================================
// IsEnabled / IsExternal
// ============================================================================

func TestIsEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  *DoltServerConfig
		want bool
	}{
		{"nil config", nil, false},
		{"disabled", &DoltServerConfig{Enabled: false}, false},
		{"enabled", &DoltServerConfig{Enabled: true}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &DoltServerManager{config: tt.cfg}
			if got := m.IsEnabled(); got != tt.want {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsExternal(t *testing.T) {
	tests := []struct {
		name string
		cfg  *DoltServerConfig
		want bool
	}{
		{"nil config", nil, false},
		{"not external", &DoltServerConfig{External: false}, false},
		{"external", &DoltServerConfig{External: true}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &DoltServerManager{config: tt.cfg}
			if got := m.IsExternal(); got != tt.want {
				t.Errorf("IsExternal() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ============================================================================
// isRemote: host classification
// ============================================================================

func TestIsRemote(t *testing.T) {
	tests := []struct {
		name string
		host string
		want bool
	}{
		{"empty", "", false},
		{"loopback IPv4", "127.0.0.1", false},
		{"localhost name", "localhost", false},
		{"localhost upper", "LOCALHOST", false},
		{"loopback IPv6", "::1", false},
		{"loopback IPv6 bracketed", "[::1]", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &DoltServerManager{
				config: &DoltServerConfig{Host: tt.host},
			}
			if got := m.isRemote(); got != tt.want {
				t.Errorf("isRemote() for host %q = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestIsRemote_NilConfig(t *testing.T) {
	m := &DoltServerManager{config: nil}
	if m.isRemote() {
		t.Error("expected isRemote() false with nil config")
	}
}

// ============================================================================
// pidFile path
// ============================================================================

func TestPidFile_ProductionPort(t *testing.T) {
	// Port 3307 uses the canonical name for gt dolt start/stop compatibility.
	m := &DoltServerManager{
		townRoot: "/tmp/town",
		config:   &DoltServerConfig{Port: 3307},
	}
	want := filepath.Join("/tmp/town", "daemon", "dolt.pid")
	if got := m.pidFile(); got != want {
		t.Errorf("pidFile() = %q, want %q", got, want)
	}
}

func TestPidFile_OtherPort(t *testing.T) {
	// Non-3307 ports get a port-specific name to avoid collisions.
	m := &DoltServerManager{
		townRoot: "/tmp/town",
		config:   &DoltServerConfig{Port: 3308},
	}
	want := filepath.Join("/tmp/town", "daemon", "dolt-3308.pid")
	if got := m.pidFile(); got != want {
		t.Errorf("pidFile() = %q, want %q", got, want)
	}
}

// ============================================================================
// filterEnvKey
// ============================================================================

func TestFilterEnvKey_RemovesAllMatches(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"DOLT_CLI_PASSWORD=secret1",
		"HOME=/home/me",
		"DOLT_CLI_PASSWORD=secret2", // Duplicate (should also be removed)
		"USER=me",
	}

	got := filterEnvKey(env, "DOLT_CLI_PASSWORD")

	for _, e := range got {
		if strings.HasPrefix(e, "DOLT_CLI_PASSWORD=") {
			t.Errorf("filterEnvKey left an entry for DOLT_CLI_PASSWORD: %q", e)
		}
	}

	// All other entries should be preserved
	want := []string{"PATH=/usr/bin", "HOME=/home/me", "USER=me"}
	if len(got) != len(want) {
		t.Fatalf("expected %d entries, got %d: %v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("entry[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestFilterEnvKey_NoMatch(t *testing.T) {
	env := []string{"PATH=/usr/bin", "HOME=/home/me"}
	got := filterEnvKey(env, "NONEXISTENT")
	if len(got) != 2 {
		t.Errorf("expected all entries preserved, got %v", got)
	}
}

func TestFilterEnvKey_EmptyInput(t *testing.T) {
	got := filterEnvKey(nil, "ANY")
	if len(got) != 0 {
		t.Errorf("expected empty result, got %v", got)
	}
}

func TestFilterEnvKey_DoesNotMatchKeyAsValue(t *testing.T) {
	// "DOLT_CLI_PASSWORD" appearing as a value should not be matched.
	env := []string{"OTHER=DOLT_CLI_PASSWORD=foo", "DOLT_CLI_PASSWORD=real"}
	got := filterEnvKey(env, "DOLT_CLI_PASSWORD")

	if len(got) != 1 {
		t.Fatalf("expected 1 entry remaining, got %d: %v", len(got), got)
	}
	if got[0] != "OTHER=DOLT_CLI_PASSWORD=foo" {
		t.Errorf("expected OTHER entry preserved, got %q", got[0])
	}
}

// ============================================================================
// SetRecoveryCallback
// ============================================================================

func TestSetRecoveryCallback_SetAndReplace(t *testing.T) {
	m := &DoltServerManager{
		config: &DoltServerConfig{},
		logger: func(format string, v ...interface{}) {},
	}

	var called1 atomic.Bool
	var called2 atomic.Bool

	m.SetRecoveryCallback(func() { called1.Store(true) })

	// Replace with a second callback — only the most recent is used.
	m.SetRecoveryCallback(func() { called2.Store(true) })

	// Fire the callback via clearUnhealthySignal after writing a signal file.
	tmpDir := t.TempDir()
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}
	m.townRoot = tmpDir
	m.config.Port = 3307 // Use canonical path for DOLT_UNHEALTHY
	m.writeUnhealthySignal("test", "detail")

	// clearUnhealthySignal must be called with mu held per its contract.
	m.mu.Lock()
	m.clearUnhealthySignal()
	m.mu.Unlock()

	// Give the goroutine a moment to execute.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if called2.Load() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if called1.Load() {
		t.Error("first callback should not fire after being replaced")
	}
	if !called2.Load() {
		t.Error("second (most recent) callback should fire")
	}
}

func TestSetRecoveryCallback_Clear(t *testing.T) {
	m := &DoltServerManager{
		config: &DoltServerConfig{},
		logger: func(format string, v ...interface{}) {},
	}

	var called atomic.Bool
	m.SetRecoveryCallback(func() { called.Store(true) })
	m.SetRecoveryCallback(nil) // Clear

	tmpDir := t.TempDir()
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}
	m.townRoot = tmpDir
	m.config.Port = 3307
	m.writeUnhealthySignal("test", "detail")

	m.mu.Lock()
	m.clearUnhealthySignal()
	m.mu.Unlock()

	// Give any (erroneous) goroutine a chance to run.
	time.Sleep(100 * time.Millisecond)

	if called.Load() {
		t.Error("cleared callback should not fire")
	}
}

// ============================================================================
// clearUnhealthySignal: no callback fires if no signal file existed
// ============================================================================

func TestClearUnhealthySignal_NoFireWhenNotUnhealthy(t *testing.T) {
	tmpDir := t.TempDir()
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	var called atomic.Bool
	m := &DoltServerManager{
		config:   DefaultDoltServerConfig(tmpDir),
		townRoot: tmpDir,
		logger:   func(format string, v ...interface{}) {},
	}
	m.config.Port = 3307
	m.SetRecoveryCallback(func() { called.Store(true) })

	// Do NOT write the signal file first.
	m.mu.Lock()
	m.clearUnhealthySignal()
	m.mu.Unlock()

	time.Sleep(100 * time.Millisecond)

	if called.Load() {
		t.Error("recovery callback should not fire if server was not previously unhealthy")
	}
}

// ============================================================================
// writeUnhealthySignal: overwrite behavior
// ============================================================================

func TestWriteUnhealthySignal_Overwrites(t *testing.T) {
	tmpDir := t.TempDir()
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	m := &DoltServerManager{
		config:   DefaultDoltServerConfig(tmpDir),
		townRoot: tmpDir,
		logger:   func(format string, v ...interface{}) {},
	}

	m.writeUnhealthySignal("reason_one", "detail_one")
	m.writeUnhealthySignal("reason_two", "detail_two")

	data, err := os.ReadFile(m.unhealthySignalFile())
	if err != nil {
		t.Fatalf("expected signal file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "reason_two") {
		t.Errorf("expected latest reason 'reason_two' in file, got: %s", content)
	}
	if strings.Contains(content, "reason_one") {
		t.Errorf("old signal content should be overwritten, got: %s", content)
	}
}

// ============================================================================
// formatDiskSize
// ============================================================================

func TestFormatDiskSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{int64(1024) * 1024 * 1024 * 1024, "1.0 TB"},
	}

	for _, tt := range tests {
		got := formatDiskSize(tt.bytes)
		if got != tt.want {
			t.Errorf("formatDiskSize(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

// ============================================================================
// writeDaemonDoltConfig
// ============================================================================

func TestWriteDaemonDoltConfig_ContainsTimeouts(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := &DoltServerConfig{
		Port:    3307,
		Host:    "127.0.0.1",
		DataDir: tmpDir,
	}

	if err := writeDaemonDoltConfig(cfg, configPath); err != nil {
		t.Fatalf("writeDaemonDoltConfig: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	content := string(data)

	// CLOSE_WAIT fix: timeouts must be set (CLI flags cannot do this).
	if !strings.Contains(content, "read_timeout_millis: 30000") {
		t.Error("expected read_timeout_millis: 30000 in config")
	}
	if !strings.Contains(content, "write_timeout_millis: 30000") {
		t.Error("expected write_timeout_millis: 30000 in config")
	}

	// Required fields
	if !strings.Contains(content, "port: 3307") {
		t.Error("expected port: 3307 in config")
	}
	if !strings.Contains(content, "host: 127.0.0.1") {
		t.Error("expected host: 127.0.0.1 in config")
	}
	if !strings.Contains(content, "auto_gc_behavior") {
		t.Error("expected auto_gc_behavior in config (GC path coverage)")
	}
	if !strings.Contains(content, "enable: true") {
		t.Error("expected GC enable: true")
	}
}

func TestWriteDaemonDoltConfig_EmptyHostOmitsHostLine(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := &DoltServerConfig{
		Port:    3307,
		Host:    "", // no explicit host
		DataDir: tmpDir,
	}

	if err := writeDaemonDoltConfig(cfg, configPath); err != nil {
		t.Fatalf("writeDaemonDoltConfig: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	content := string(data)

	// host: <line> should be absent when Host is empty.
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "host:") {
			t.Errorf("expected no host line when Host empty, found: %q", line)
		}
	}
}

func TestWriteDaemonDoltConfig_Permissions(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := &DoltServerConfig{
		Port:    3307,
		DataDir: tmpDir,
	}

	if err := writeDaemonDoltConfig(cfg, configPath); err != nil {
		t.Fatalf("writeDaemonDoltConfig: %v", err)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	mode := info.Mode().Perm()
	if mode != 0600 {
		t.Errorf("expected config file mode 0600, got %o", mode)
	}
}

// ============================================================================
// HealthCheckInterval default constant
// ============================================================================

func TestDefaultDoltHealthCheckInterval(t *testing.T) {
	// Contract: the default interval must support fast crash detection (<1 min).
	if DefaultDoltHealthCheckInterval > time.Minute {
		t.Errorf("DefaultDoltHealthCheckInterval = %v, want <= 1m for fast crash detection", DefaultDoltHealthCheckInterval)
	}
	if DefaultDoltHealthCheckInterval < time.Second {
		t.Errorf("DefaultDoltHealthCheckInterval = %v, want >= 1s", DefaultDoltHealthCheckInterval)
	}
}

// ============================================================================
// Status: uses test hooks to avoid real Dolt invocation
// ============================================================================

func TestStatus_NotRunning(t *testing.T) {
	m := &DoltServerManager{
		config: &DoltServerConfig{
			Port: 3307,
			Host: "127.0.0.1",
		},
		logger:    func(format string, v ...interface{}) {},
		runningFn: func() (int, bool) { return 0, false },
	}

	st := m.Status()
	if st == nil {
		t.Fatal("Status() returned nil")
	}
	if st.Running {
		t.Error("expected Running=false")
	}
	if st.Port != 3307 {
		t.Errorf("expected Port=3307, got %d", st.Port)
	}
	if st.Host != "127.0.0.1" {
		t.Errorf("expected Host=127.0.0.1, got %q", st.Host)
	}
}

func TestStatus_Running(t *testing.T) {
	// Status() calls listDatabases() and getDoltVersion() which invoke the
	// real dolt binary and a backing server. In unit tests, those will fail
	// gracefully and return empty values. We only verify the process-level
	// bookkeeping that doesn't touch the server.
	started := time.Now().Add(-5 * time.Minute)
	// Use a townRoot with no dolt data so listDatabases falls through cleanly.
	m := &DoltServerManager{
		config: &DoltServerConfig{
			Port:    3307,
			Host:    "127.0.0.1",
			DataDir: t.TempDir(),
		},
		townRoot:  t.TempDir(),
		startedAt: started,
		logger:    func(format string, v ...interface{}) {},
		runningFn: func() (int, bool) { return 4242, true },
	}

	st := m.Status()
	if !st.Running {
		t.Error("expected Running=true")
	}
	if st.PID != 4242 {
		t.Errorf("expected PID=4242, got %d", st.PID)
	}
	if !st.StartedAt.Equal(started) {
		t.Errorf("expected StartedAt=%v, got %v", started, st.StartedAt)
	}
	if st.Port != 3307 {
		t.Errorf("expected Port=3307, got %d", st.Port)
	}
	if st.Host != "127.0.0.1" {
		t.Errorf("expected Host=127.0.0.1, got %q", st.Host)
	}
}

func TestStatus_ReturnsNonNil(t *testing.T) {
	// Smoke test: Status must never return nil, even on edge configs.
	m := &DoltServerManager{
		config:    &DoltServerConfig{Port: 3307, Host: "127.0.0.1"},
		townRoot:  t.TempDir(),
		logger:    func(format string, v ...interface{}) {},
		runningFn: func() (int, bool) { return 7777, true },
	}
	if st := m.Status(); st == nil {
		t.Fatal("Status() returned nil")
	}
}

// ============================================================================
// checkDiskUsage
// ============================================================================

func TestCheckDiskUsage_EmptyDataDir(t *testing.T) {
	m := &DoltServerManager{
		config: &DoltServerConfig{DataDir: ""},
		logger: func(format string, v ...interface{}) {},
	}
	// No DataDir configured: returns no warning.
	if w := m.checkDiskUsage(); w != "" {
		t.Errorf("expected no warning for empty DataDir, got %q", w)
	}
}

func TestCheckDiskUsage_BelowThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	// Write a small file (<1 GB).
	if err := os.WriteFile(filepath.Join(tmpDir, "small.bin"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}

	m := &DoltServerManager{
		config: &DoltServerConfig{DataDir: tmpDir},
		logger: func(format string, v ...interface{}) {},
	}

	if w := m.checkDiskUsage(); w != "" {
		t.Errorf("expected no warning for <1GB usage, got %q", w)
	}
}

func TestCheckDiskUsage_NonexistentDir(t *testing.T) {
	m := &DoltServerManager{
		config: &DoltServerConfig{DataDir: "/nonexistent/path/to/nowhere"},
		logger: func(format string, v ...interface{}) {},
	}
	// Nonexistent dir: Walk silently errors out, total remains 0, no warning.
	if w := m.checkDiskUsage(); w != "" {
		t.Errorf("expected no warning for nonexistent dir, got %q", w)
	}
}

// ============================================================================
// countDataDirDatabases
// ============================================================================

func TestCountDataDirDatabases_Empty(t *testing.T) {
	m := &DoltServerManager{
		config: &DoltServerConfig{DataDir: ""},
	}
	if got := m.countDataDirDatabases(); got != 0 {
		t.Errorf("expected 0 for empty DataDir, got %d", got)
	}
}

func TestCountDataDirDatabases_Counts(t *testing.T) {
	tmpDir := t.TempDir()
	// Create 3 database dirs + a hidden dir + a regular file.
	dbs := []string{"db-a", "db-b", "db-c"}
	for _, name := range dbs {
		if err := os.MkdirAll(filepath.Join(tmpDir, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, ".hidden"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "readme.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	m := &DoltServerManager{
		config: &DoltServerConfig{DataDir: tmpDir},
	}
	got := m.countDataDirDatabases()
	if got != 3 {
		t.Errorf("expected 3 database dirs (hidden and file excluded), got %d", got)
	}
}

func TestCountDataDirDatabases_NonexistentDir(t *testing.T) {
	m := &DoltServerManager{
		config: &DoltServerConfig{DataDir: "/nonexistent/path"},
	}
	if got := m.countDataDirDatabases(); got != 0 {
		t.Errorf("expected 0 for nonexistent dir, got %d", got)
	}
}

// ============================================================================
// checkBackupFreshness
// ============================================================================

func TestCheckBackupFreshness_NoBackupDir(t *testing.T) {
	m := &DoltServerManager{
		townRoot: t.TempDir(), // .dolt-backup does not exist
		config:   &DoltServerConfig{},
		logger:   func(format string, v ...interface{}) {},
	}
	if got := m.checkBackupFreshness(); got != nil {
		t.Errorf("expected nil warnings when no backup dir, got %v", got)
	}
}

func TestCheckBackupFreshness_FreshBackup(t *testing.T) {
	tmpDir := t.TempDir()
	backupDir := filepath.Join(tmpDir, ".dolt-backup")
	dbBackup := filepath.Join(backupDir, "mydb-backup")
	if err := os.MkdirAll(dbBackup, 0755); err != nil {
		t.Fatal(err)
	}

	m := &DoltServerManager{
		townRoot: tmpDir,
		config:   &DoltServerConfig{},
		logger:   func(format string, v ...interface{}) {},
	}

	warnings := m.checkBackupFreshness()
	if len(warnings) != 0 {
		t.Errorf("expected no warnings for fresh backup, got %v", warnings)
	}
}

func TestCheckBackupFreshness_StaleBackup(t *testing.T) {
	tmpDir := t.TempDir()
	backupDir := filepath.Join(tmpDir, ".dolt-backup")
	dbBackup := filepath.Join(backupDir, "stale-db-backup")
	if err := os.MkdirAll(dbBackup, 0755); err != nil {
		t.Fatal(err)
	}

	// Back-date the directory's mtime to 3h ago (threshold is 2h).
	oldTime := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(dbBackup, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	m := &DoltServerManager{
		townRoot: tmpDir,
		config:   &DoltServerConfig{},
		logger:   func(format string, v ...interface{}) {},
	}

	warnings := m.checkBackupFreshness()
	if len(warnings) != 1 {
		t.Fatalf("expected 1 stale warning, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "stale-db-backup") {
		t.Errorf("warning should mention the stale db: %q", warnings[0])
	}
}

func TestCheckBackupFreshness_IgnoresFiles(t *testing.T) {
	tmpDir := t.TempDir()
	backupDir := filepath.Join(tmpDir, ".dolt-backup")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Write a regular file — should be ignored (only dirs are db backups).
	if err := os.WriteFile(filepath.Join(backupDir, "README"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	m := &DoltServerManager{
		townRoot: tmpDir,
		config:   &DoltServerConfig{},
		logger:   func(format string, v ...interface{}) {},
	}

	warnings := m.checkBackupFreshness()
	if len(warnings) != 0 {
		t.Errorf("expected no warnings (files are ignored), got %v", warnings)
	}
}

// ============================================================================
// LastWarnings
// ============================================================================

func TestLastWarnings_InitiallyNil(t *testing.T) {
	m := &DoltServerManager{
		config: &DoltServerConfig{},
		logger: func(format string, v ...interface{}) {},
	}
	if w := m.LastWarnings(); w != nil {
		t.Errorf("expected nil warnings on fresh manager, got %v", w)
	}
}

func TestLastWarnings_ReturnsStored(t *testing.T) {
	m := &DoltServerManager{
		config:       &DoltServerConfig{},
		logger:       func(format string, v ...interface{}) {},
		lastWarnings: []string{"warn1", "warn2"},
	}
	got := m.LastWarnings()
	if len(got) != 2 || got[0] != "warn1" || got[1] != "warn2" {
		t.Errorf("LastWarnings() = %v, want [warn1 warn2]", got)
	}
}

// TestLastWarnings_ConcurrentAccess verifies LastWarnings is safe to call
// from multiple goroutines (grabs the mutex).
func TestLastWarnings_ConcurrentAccess(t *testing.T) {
	m := &DoltServerManager{
		config:       &DoltServerConfig{},
		logger:       func(format string, v ...interface{}) {},
		lastWarnings: []string{"w1"},
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.LastWarnings()
		}()
	}
	wg.Wait()
	// Should not panic or race (run with -race to detect)
}

// ============================================================================
// doSleep / now: test hooks
// ============================================================================

func TestNow_UsesHook(t *testing.T) {
	fixed := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	m := &DoltServerManager{
		config: &DoltServerConfig{},
		logger: func(format string, v ...interface{}) {},
		nowFn:  func() time.Time { return fixed },
	}
	if got := m.now(); !got.Equal(fixed) {
		t.Errorf("now() = %v, want %v", got, fixed)
	}
}

func TestNow_DefaultIsRealTime(t *testing.T) {
	m := &DoltServerManager{
		config: &DoltServerConfig{},
		logger: func(format string, v ...interface{}) {},
	}
	before := time.Now()
	got := m.now()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Errorf("now() = %v, expected between %v and %v", got, before, after)
	}
}

func TestDoSleep_UsesHook(t *testing.T) {
	var called atomic.Int32
	var sawDuration atomic.Int64
	m := &DoltServerManager{
		config: &DoltServerConfig{},
		logger: func(format string, v ...interface{}) {},
		sleepFn: func(d time.Duration) {
			called.Add(1)
			sawDuration.Store(int64(d))
		},
	}

	m.doSleep(42 * time.Millisecond)

	if called.Load() != 1 {
		t.Errorf("expected sleep hook to be called once, got %d", called.Load())
	}
	if sawDuration.Load() != int64(42*time.Millisecond) {
		t.Errorf("expected duration 42ms, got %v", time.Duration(sawDuration.Load()))
	}
}

// ============================================================================
// unhealthySignalFile path variations
// ============================================================================

func TestUnhealthySignalFile_PortZero(t *testing.T) {
	// Edge case: uninitialized port should still produce a sensible path.
	m := &DoltServerManager{
		townRoot: "/tmp/town",
		config:   &DoltServerConfig{Port: 0},
		logger:   func(format string, v ...interface{}) {},
	}
	want := filepath.Join("/tmp/town", "daemon", "DOLT_UNHEALTHY_0")
	if got := m.unhealthySignalFile(); got != want {
		t.Errorf("unhealthySignalFile() = %q, want %q", got, want)
	}
}

// ============================================================================
// IsDoltUnhealthy (package-level)
// ============================================================================

func TestIsDoltUnhealthy_WithSignal(t *testing.T) {
	tmpDir := t.TempDir()
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create DOLT_UNHEALTHY signal file
	if err := os.WriteFile(filepath.Join(daemonDir, "DOLT_UNHEALTHY"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	if !IsDoltUnhealthy(tmpDir) {
		t.Error("expected IsDoltUnhealthy=true when signal file exists")
	}
}

func TestIsDoltUnhealthy_NoSignal(t *testing.T) {
	tmpDir := t.TempDir()
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}
	// No signal file exists
	if IsDoltUnhealthy(tmpDir) {
		t.Error("expected IsDoltUnhealthy=false when no signal file")
	}
}

// ============================================================================
// doltBackupInterval (from dolt_backup.go)
// ============================================================================

func TestDoltBackupInterval_DefaultsWhenNil(t *testing.T) {
	if got := doltBackupInterval(nil); got != defaultDoltBackupInterval {
		t.Errorf("doltBackupInterval(nil) = %v, want %v", got, defaultDoltBackupInterval)
	}
}

func TestDoltBackupInterval_DefaultsWhenPatrolsNil(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: nil}
	if got := doltBackupInterval(cfg); got != defaultDoltBackupInterval {
		t.Errorf("doltBackupInterval(patrols=nil) = %v, want %v", got, defaultDoltBackupInterval)
	}
}

func TestDoltBackupInterval_DefaultsWhenDoltBackupNil(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{DoltBackup: nil}}
	if got := doltBackupInterval(cfg); got != defaultDoltBackupInterval {
		t.Errorf("doltBackupInterval(doltBackup=nil) = %v, want %v", got, defaultDoltBackupInterval)
	}
}

func TestDoltBackupInterval_CustomInterval(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			DoltBackup: &DoltBackupConfig{IntervalStr: "30m"},
		},
	}
	if got := doltBackupInterval(cfg); got != 30*time.Minute {
		t.Errorf("doltBackupInterval(30m) = %v, want 30m", got)
	}
}

func TestDoltBackupInterval_EmptyIntervalStr(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			DoltBackup: &DoltBackupConfig{IntervalStr: ""},
		},
	}
	if got := doltBackupInterval(cfg); got != defaultDoltBackupInterval {
		t.Errorf("doltBackupInterval(empty) = %v, want %v", got, defaultDoltBackupInterval)
	}
}

func TestDoltBackupInterval_InvalidDuration(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			DoltBackup: &DoltBackupConfig{IntervalStr: "not-a-duration"},
		},
	}
	if got := doltBackupInterval(cfg); got != defaultDoltBackupInterval {
		t.Errorf("doltBackupInterval(invalid) = %v, want default %v", got, defaultDoltBackupInterval)
	}
}

func TestDoltBackupInterval_ZeroDuration(t *testing.T) {
	// Zero duration should fall back to the default.
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			DoltBackup: &DoltBackupConfig{IntervalStr: "0s"},
		},
	}
	if got := doltBackupInterval(cfg); got != defaultDoltBackupInterval {
		t.Errorf("doltBackupInterval(0s) = %v, want default %v", got, defaultDoltBackupInterval)
	}
}

// ============================================================================
// dolt_remotes: escapeSQL (simple pure function)
// ============================================================================

func TestEscapeSQL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"simple", "simple"},
		{"with'quote", "with''quote"},
		{"two''quotes", "two''''quotes"},
		{"no_special_chars", "no_special_chars"},
	}
	for _, tt := range tests {
		if got := escapeSQL(tt.in); got != tt.want {
			t.Errorf("escapeSQL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ============================================================================
// doltRemotesInterval (dolt_remotes.go)
// ============================================================================

func TestDoltRemotesInterval_Defaults(t *testing.T) {
	// Call with nil and verify a positive default is returned.
	got := doltRemotesInterval(nil)
	if got <= 0 {
		t.Errorf("expected positive default remotes interval, got %v", got)
	}
}

// verify_doltBackupIntervalSanity is a sanity check on the default constant
// itself: 15-minute default should be reasonable.
func TestDefaultDoltBackupInterval_Sanity(t *testing.T) {
	if defaultDoltBackupInterval < time.Minute {
		t.Errorf("defaultDoltBackupInterval = %v, want >= 1m", defaultDoltBackupInterval)
	}
	if defaultDoltBackupInterval > time.Hour {
		t.Errorf("defaultDoltBackupInterval = %v, want <= 1h", defaultDoltBackupInterval)
	}
}

// Confirm doltBackupTimeout is positive and sensible.
func TestDoltBackupTimeout_Sanity(t *testing.T) {
	if doltBackupTimeout <= 0 {
		t.Errorf("doltBackupTimeout = %v, want > 0", doltBackupTimeout)
	}
}

// ============================================================================
// Benchmark smoke: filterEnvKey
// ============================================================================

func BenchmarkFilterEnvKey(b *testing.B) {
	env := []string{
		"PATH=/usr/bin",
		"HOME=/home/me",
		"DOLT_CLI_PASSWORD=secret",
		"USER=me",
		"LANG=C.UTF-8",
		"TERM=xterm",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = filterEnvKey(env, "DOLT_CLI_PASSWORD")
	}
}

// Ensure fmt is actually used (prevents accidental import removal).
var _ = fmt.Sprintf

package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/quota"
)

// setupTestTownForQuota creates a minimal Gas Town workspace with the given
// accounts already persisted to mayor/accounts.json. It returns the town root
// and the accounts config path. Accounts is a map of handle -> email.
// If defaultHandle is non-empty, it is set as the default account.
func setupTestTownForQuota(t *testing.T, accounts map[string]string, defaultHandle string) (townRoot, accountsPath string) {
	t.Helper()

	townRoot, _ = setupTestTownForAccount(t)
	accountsPath = constants.MayorAccountsPath(townRoot)

	acctCfg := config.NewAccountsConfig()
	for handle, email := range accounts {
		acctCfg.Accounts[handle] = config.Account{
			Email:     email,
			ConfigDir: filepath.Join(t.TempDir(), handle),
		}
	}
	if defaultHandle != "" {
		acctCfg.Default = defaultHandle
	}

	if len(acctCfg.Accounts) > 0 {
		if err := config.SaveAccountsConfig(accountsPath, acctCfg); err != nil {
			t.Fatalf("save accounts: %v", err)
		}
	}

	return townRoot, accountsPath
}

// chdirForTest changes to dir for the duration of the test, restoring cwd on
// cleanup.
func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWd)
	})
}

// resetQuotaFlags clears package-level command flag state so tests don't leak.
func resetQuotaFlags(t *testing.T) {
	t.Helper()
	oldJSON := quotaJSON
	oldScanUpdate := scanUpdate
	oldRotateDryRun := rotateDryRun
	oldRotateFrom := rotateFrom
	oldRotateIdle := rotateIdle
	t.Cleanup(func() {
		quotaJSON = oldJSON
		scanUpdate = oldScanUpdate
		rotateDryRun = oldRotateDryRun
		rotateFrom = oldRotateFrom
		rotateIdle = oldRotateIdle
	})
}

// --- accountHandles ---

func TestAccountHandles(t *testing.T) {
	t.Run("returns sorted handles", func(t *testing.T) {
		cfg := &config.AccountsConfig{
			Accounts: map[string]config.Account{
				"zeta":    {Email: "z@x"},
				"alpha":   {Email: "a@x"},
				"mike":    {Email: "m@x"},
			},
		}
		got := accountHandles(cfg)
		want := []string{"alpha", "mike", "zeta"}
		if len(got) != len(want) {
			t.Fatalf("len = %d, want %d", len(got), len(want))
		}
		for i, h := range got {
			if h != want[i] {
				t.Errorf("index %d: got %q, want %q", i, h, want[i])
			}
		}
	})

	t.Run("empty accounts returns empty slice", func(t *testing.T) {
		cfg := &config.AccountsConfig{
			Accounts: map[string]config.Account{},
		}
		got := accountHandles(cfg)
		if len(got) != 0 {
			t.Errorf("expected empty slice, got %v", got)
		}
	})

	t.Run("single account", func(t *testing.T) {
		cfg := &config.AccountsConfig{
			Accounts: map[string]config.Account{
				"only": {Email: "o@x"},
			},
		}
		got := accountHandles(cfg)
		if len(got) != 1 || got[0] != "only" {
			t.Errorf("got %v, want [only]", got)
		}
	})
}

// --- printQuotaStatusJSON ---

func TestPrintQuotaStatusJSON(t *testing.T) {
	resetQuotaFlags(t)

	t.Run("emits one item per account in sorted order", func(t *testing.T) {
		cfg := &config.AccountsConfig{
			Accounts: map[string]config.Account{
				"zeta":  {Email: "z@example.com"},
				"alpha": {Email: "a@example.com"},
			},
			Default: "alpha",
		}
		state := &config.QuotaState{
			Accounts: map[string]config.AccountQuotaState{
				"alpha": {Status: config.QuotaStatusAvailable, LastUsed: "2024-01-01T00:00:00Z"},
				"zeta": {
					Status:    config.QuotaStatusLimited,
					LimitedAt: "2024-01-02T00:00:00Z",
					ResetsAt:  "7pm (America/Los_Angeles)",
				},
			},
		}

		out := captureStdout(t, func() {
			if err := printQuotaStatusJSON(cfg, state); err != nil {
				t.Fatalf("printQuotaStatusJSON: %v", err)
			}
		})

		var items []QuotaStatusItem
		if err := json.Unmarshal([]byte(out), &items); err != nil {
			t.Fatalf("unmarshal: %v\noutput: %s", err, out)
		}
		if len(items) != 2 {
			t.Fatalf("expected 2 items, got %d", len(items))
		}
		if items[0].Handle != "alpha" {
			t.Errorf("items[0].Handle = %q, want alpha", items[0].Handle)
		}
		if items[1].Handle != "zeta" {
			t.Errorf("items[1].Handle = %q, want zeta", items[1].Handle)
		}
		if !items[0].IsDefault {
			t.Error("items[0] should be marked IsDefault")
		}
		if items[1].IsDefault {
			t.Error("items[1] should NOT be marked IsDefault")
		}
		if items[0].Status != string(config.QuotaStatusAvailable) {
			t.Errorf("items[0].Status = %q, want available", items[0].Status)
		}
		if items[1].Status != string(config.QuotaStatusLimited) {
			t.Errorf("items[1].Status = %q, want limited", items[1].Status)
		}
		if items[1].ResetsAt == "" {
			t.Error("items[1].ResetsAt should be populated")
		}
	})

	t.Run("missing quota state defaults to available", func(t *testing.T) {
		cfg := &config.AccountsConfig{
			Accounts: map[string]config.Account{
				"solo": {Email: "s@x"},
			},
		}
		state := &config.QuotaState{
			Accounts: map[string]config.AccountQuotaState{}, // no entry for "solo"
		}

		out := captureStdout(t, func() {
			if err := printQuotaStatusJSON(cfg, state); err != nil {
				t.Fatalf("printQuotaStatusJSON: %v", err)
			}
		})

		var items []QuotaStatusItem
		if err := json.Unmarshal([]byte(out), &items); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(items) != 1 {
			t.Fatalf("want 1 item, got %d", len(items))
		}
		if items[0].Status != string(config.QuotaStatusAvailable) {
			t.Errorf("status defaulted to %q, want available", items[0].Status)
		}
	})
}

// --- printQuotaStatusText ---

func TestPrintQuotaStatusText(t *testing.T) {
	resetQuotaFlags(t)

	t.Run("renders status badges and summary", func(t *testing.T) {
		cfg := &config.AccountsConfig{
			Accounts: map[string]config.Account{
				"work":     {Email: "w@x"},
				"personal": {Email: "p@x"},
				"side":     {Email: "s@x"},
			},
			Default: "work",
		}
		state := &config.QuotaState{
			Accounts: map[string]config.AccountQuotaState{
				"work":     {Status: config.QuotaStatusAvailable},
				"personal": {Status: config.QuotaStatusLimited, ResetsAt: "7pm"},
				"side":     {Status: config.QuotaStatusCooldown},
			},
		}

		out := captureStdout(t, func() {
			if err := printQuotaStatusText(cfg, state); err != nil {
				t.Fatalf("printQuotaStatusText: %v", err)
			}
		})

		// Check handles render
		for _, h := range []string{"work", "personal", "side"} {
			if !strings.Contains(out, h) {
				t.Errorf("output missing handle %q: %s", h, out)
			}
		}
		// Status badges
		for _, badge := range []string{"available", "limited", "cooldown"} {
			if !strings.Contains(out, badge) {
				t.Errorf("output missing badge %q", badge)
			}
		}
		// Summary: 1 available, 2 limited
		if !strings.Contains(out, "1 available") {
			t.Errorf("output missing '1 available': %s", out)
		}
		if !strings.Contains(out, "2 limited") {
			t.Errorf("output missing '2 limited': %s", out)
		}
		// Reset time for limited
		if !strings.Contains(out, "7pm") {
			t.Error("output missing reset time '7pm'")
		}
	})

	t.Run("unknown status renders as unknown", func(t *testing.T) {
		cfg := &config.AccountsConfig{
			Accounts: map[string]config.Account{
				"weird": {Email: "w@x"},
			},
		}
		state := &config.QuotaState{
			Accounts: map[string]config.AccountQuotaState{
				"weird": {Status: config.AccountQuotaStatus("mystery")},
			},
		}

		out := captureStdout(t, func() {
			if err := printQuotaStatusText(cfg, state); err != nil {
				t.Fatalf("printQuotaStatusText: %v", err)
			}
		})
		if !strings.Contains(out, "unknown") {
			t.Errorf("unknown status should render as 'unknown', got: %s", out)
		}
	})

	t.Run("default account marked with asterisk", func(t *testing.T) {
		cfg := &config.AccountsConfig{
			Accounts: map[string]config.Account{
				"main": {Email: "m@x"},
				"alt":  {Email: "a@x"},
			},
			Default: "main",
		}
		state := &config.QuotaState{
			Accounts: map[string]config.AccountQuotaState{},
		}

		out := captureStdout(t, func() {
			if err := printQuotaStatusText(cfg, state); err != nil {
				t.Fatalf("printQuotaStatusText: %v", err)
			}
		})
		// Verify both accounts render and the default marker "*" appears
		// on the same line as "main".
		lines := strings.Split(out, "\n")
		var mainLine string
		for _, line := range lines {
			if strings.Contains(line, " main ") {
				mainLine = line
				break
			}
		}
		if mainLine == "" {
			t.Fatalf("no line found for 'main' in output:\n%s", out)
		}
		if !strings.Contains(mainLine, "*") {
			t.Errorf("main line should contain default marker '*': %q", mainLine)
		}
	})
}

// --- printScanJSON / printScanText ---

func TestPrintScanJSON(t *testing.T) {
	t.Run("encodes scan results", func(t *testing.T) {
		results := []quota.ScanResult{
			{Session: "gt-boot", AccountHandle: "work", RateLimited: true, ResetsAt: "7pm"},
			{Session: "gt-dust", AccountHandle: "personal", RateLimited: false},
		}
		out := captureStdout(t, func() {
			if err := printScanJSON(results); err != nil {
				t.Fatalf("printScanJSON: %v", err)
			}
		})

		var decoded []quota.ScanResult
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if len(decoded) != 2 {
			t.Fatalf("want 2 results, got %d", len(decoded))
		}
		if decoded[0].Session != "gt-boot" || !decoded[0].RateLimited {
			t.Errorf("first result corrupt: %+v", decoded[0])
		}
	})

	t.Run("empty results emit valid JSON", func(t *testing.T) {
		out := captureStdout(t, func() {
			if err := printScanJSON(nil); err != nil {
				t.Fatalf("printScanJSON: %v", err)
			}
		})
		trimmed := strings.TrimSpace(out)
		if trimmed != "null" && trimmed != "[]" {
			t.Errorf("expected null or [], got %q", trimmed)
		}
	})
}

func TestPrintScanText(t *testing.T) {
	t.Run("no problems prints clean summary", func(t *testing.T) {
		results := []quota.ScanResult{
			{Session: "gt-dust", AccountHandle: "work", RateLimited: false, NearLimit: false},
			{Session: "gt-boot", AccountHandle: "personal", RateLimited: false, NearLimit: false},
		}
		out := captureStdout(t, func() {
			if err := printScanText(results); err != nil {
				t.Fatalf("printScanText: %v", err)
			}
		})
		if !strings.Contains(out, "No rate-limited sessions") {
			t.Errorf("expected clean summary, got: %s", out)
		}
		if !strings.Contains(out, "2 scanned") {
			t.Errorf("expected scan count, got: %s", out)
		}
	})

	t.Run("limited session shown with account", func(t *testing.T) {
		results := []quota.ScanResult{
			{Session: "gt-boot", AccountHandle: "work", RateLimited: true, ResetsAt: "7pm"},
		}
		out := captureStdout(t, func() {
			if err := printScanText(results); err != nil {
				t.Fatalf("printScanText: %v", err)
			}
		})
		if !strings.Contains(out, "gt-boot") {
			t.Errorf("output missing session: %s", out)
		}
		if !strings.Contains(out, "work") {
			t.Errorf("output missing account: %s", out)
		}
		if !strings.Contains(out, "7pm") {
			t.Errorf("output missing reset time: %s", out)
		}
		if !strings.Contains(out, "1 limited") {
			t.Errorf("summary should list 1 limited: %s", out)
		}
	})

	t.Run("near-limit session shown with match detail", func(t *testing.T) {
		results := []quota.ScanResult{
			{
				Session:       "gt-dust",
				AccountHandle: "personal",
				NearLimit:     true,
				MatchedLine:   "Approaching your usage limit",
			},
		}
		out := captureStdout(t, func() {
			if err := printScanText(results); err != nil {
				t.Fatalf("printScanText: %v", err)
			}
		})
		if !strings.Contains(out, "gt-dust") {
			t.Errorf("output missing session: %s", out)
		}
		if !strings.Contains(out, "personal") {
			t.Errorf("output missing account: %s", out)
		}
		if !strings.Contains(out, "Approaching your usage limit") {
			t.Errorf("output missing matched line: %s", out)
		}
		if !strings.Contains(out, "1 near-limit") {
			t.Errorf("summary should list 1 near-limit: %s", out)
		}
	})

	t.Run("unknown account shown as (unknown)", func(t *testing.T) {
		results := []quota.ScanResult{
			{Session: "gt-boot", AccountHandle: "", RateLimited: true},
		}
		out := captureStdout(t, func() {
			if err := printScanText(results); err != nil {
				t.Fatalf("printScanText: %v", err)
			}
		})
		if !strings.Contains(out, "(unknown)") {
			t.Errorf("expected (unknown) account marker, got: %s", out)
		}
	})

	t.Run("mixed limited and near-limit", func(t *testing.T) {
		results := []quota.ScanResult{
			{Session: "gt-a", AccountHandle: "work", RateLimited: true},
			{Session: "gt-b", AccountHandle: "personal", NearLimit: true, MatchedLine: "warning"},
			{Session: "gt-c", AccountHandle: "side", RateLimited: false},
		}
		out := captureStdout(t, func() {
			if err := printScanText(results); err != nil {
				t.Fatalf("printScanText: %v", err)
			}
		})
		if !strings.Contains(out, "1 limited") {
			t.Errorf("missing '1 limited': %s", out)
		}
		if !strings.Contains(out, "1 near-limit") {
			t.Errorf("missing '1 near-limit': %s", out)
		}
	})
}

// --- updateQuotaState ---

func TestUpdateQuotaState(t *testing.T) {
	t.Run("marks limited accounts and preserves LastUsed", func(t *testing.T) {
		townRoot, _ := setupTestTownForQuota(t, map[string]string{
			"work":     "w@x",
			"personal": "p@x",
		}, "work")

		// Seed existing state with a LastUsed value we expect to survive.
		mgr := quota.NewManager(townRoot)
		state, err := mgr.Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		state.Accounts = map[string]config.AccountQuotaState{
			"work":     {Status: config.QuotaStatusAvailable, LastUsed: "2024-01-01T00:00:00Z"},
			"personal": {Status: config.QuotaStatusAvailable, LastUsed: "2024-02-02T00:00:00Z"},
		}
		if err := mgr.Save(state); err != nil {
			t.Fatalf("save: %v", err)
		}

		acctCfg, err := config.LoadAccountsConfig(constants.MayorAccountsPath(townRoot))
		if err != nil {
			t.Fatalf("load accounts: %v", err)
		}

		results := []quota.ScanResult{
			{Session: "gt-a", AccountHandle: "work", RateLimited: true, ResetsAt: "7pm"},
			{Session: "gt-b", AccountHandle: "personal", RateLimited: false},
		}

		if err := updateQuotaState(townRoot, results, acctCfg); err != nil {
			t.Fatalf("updateQuotaState: %v", err)
		}

		// Re-load and verify
		newState, err := mgr.Load()
		if err != nil {
			t.Fatalf("reload: %v", err)
		}

		workState := newState.Accounts["work"]
		if workState.Status != config.QuotaStatusLimited {
			t.Errorf("work status = %q, want limited", workState.Status)
		}
		if workState.ResetsAt != "7pm" {
			t.Errorf("work ResetsAt = %q, want 7pm", workState.ResetsAt)
		}
		if workState.LimitedAt == "" {
			t.Error("work LimitedAt should be populated")
		}
		if _, err := time.Parse(time.RFC3339, workState.LimitedAt); err != nil {
			t.Errorf("work LimitedAt not RFC3339: %q (%v)", workState.LimitedAt, err)
		}
		if workState.LastUsed != "2024-01-01T00:00:00Z" {
			t.Errorf("LastUsed not preserved: got %q", workState.LastUsed)
		}

		personalState := newState.Accounts["personal"]
		if personalState.Status == config.QuotaStatusLimited {
			t.Error("personal should NOT be marked limited (not in rate-limited results)")
		}
	})

	t.Run("ignores scan results without account handle", func(t *testing.T) {
		townRoot, _ := setupTestTownForQuota(t, map[string]string{
			"work": "w@x",
		}, "work")
		acctCfg, err := config.LoadAccountsConfig(constants.MayorAccountsPath(townRoot))
		if err != nil {
			t.Fatalf("load accounts: %v", err)
		}

		results := []quota.ScanResult{
			{Session: "gt-unknown", AccountHandle: "", RateLimited: true},
		}
		if err := updateQuotaState(townRoot, results, acctCfg); err != nil {
			t.Fatalf("updateQuotaState: %v", err)
		}

		mgr := quota.NewManager(townRoot)
		state, err := mgr.Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		workState := state.Accounts["work"]
		if workState.Status == config.QuotaStatusLimited {
			t.Error("work should not be marked limited when no account handle in result")
		}
	})

	t.Run("empty results is no-op", func(t *testing.T) {
		townRoot, _ := setupTestTownForQuota(t, map[string]string{
			"work": "w@x",
		}, "work")
		acctCfg, err := config.LoadAccountsConfig(constants.MayorAccountsPath(townRoot))
		if err != nil {
			t.Fatalf("load accounts: %v", err)
		}

		if err := updateQuotaState(townRoot, nil, acctCfg); err != nil {
			t.Fatalf("updateQuotaState: %v", err)
		}

		mgr := quota.NewManager(townRoot)
		state, err := mgr.Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		// Accounts should be tracked even with no results.
		if _, ok := state.Accounts["work"]; !ok {
			t.Error("EnsureAccountsTracked should have added 'work'")
		}
	})
}

// --- runQuotaStatus integration ---

func TestRunQuotaStatus(t *testing.T) {
	t.Run("no accounts configured", func(t *testing.T) {
		resetQuotaFlags(t)
		townRoot, _ := setupTestTownForQuota(t, nil, "")
		chdirForTest(t, townRoot)

		out := captureStdout(t, func() {
			err := runQuotaStatus(&cobra.Command{}, nil)
			if err != nil {
				t.Fatalf("runQuotaStatus: %v", err)
			}
		})
		if !strings.Contains(out, "No accounts configured") {
			t.Errorf("expected 'No accounts configured', got: %s", out)
		}
	})

	t.Run("text output with accounts", func(t *testing.T) {
		resetQuotaFlags(t)
		townRoot, _ := setupTestTownForQuota(t, map[string]string{
			"work": "w@x",
		}, "work")
		chdirForTest(t, townRoot)

		quotaJSON = false
		out := captureStdout(t, func() {
			err := runQuotaStatus(&cobra.Command{}, nil)
			if err != nil {
				t.Fatalf("runQuotaStatus: %v", err)
			}
		})
		if !strings.Contains(out, "work") {
			t.Errorf("expected 'work' in output: %s", out)
		}
		if !strings.Contains(out, "Account Quota Status") {
			t.Errorf("expected header in output: %s", out)
		}
	})

	t.Run("json output with accounts", func(t *testing.T) {
		resetQuotaFlags(t)
		townRoot, _ := setupTestTownForQuota(t, map[string]string{
			"work": "w@x",
		}, "work")
		chdirForTest(t, townRoot)

		quotaJSON = true
		out := captureStdout(t, func() {
			err := runQuotaStatus(&cobra.Command{}, nil)
			if err != nil {
				t.Fatalf("runQuotaStatus: %v", err)
			}
		})
		var items []QuotaStatusItem
		if err := json.Unmarshal([]byte(out), &items); err != nil {
			t.Fatalf("json unmarshal failed: %v\n%s", err, out)
		}
		if len(items) != 1 {
			t.Fatalf("want 1 item, got %d", len(items))
		}
		if items[0].Handle != "work" {
			t.Errorf("handle = %q, want work", items[0].Handle)
		}
		if !items[0].IsDefault {
			t.Error("work should be the default account")
		}
	})

	t.Run("preserves limited state with future reset time", func(t *testing.T) {
		resetQuotaFlags(t)
		townRoot, _ := setupTestTownForQuota(t, map[string]string{
			"work": "w@x",
		}, "work")
		chdirForTest(t, townRoot)

		// Seed quota state with a reset time well in the future.
		// Format "11:59pm" is parsed as today at 23:59 local; unless the test
		// runs at exactly 11:59pm it will be in the future OR the past for
		// "today". To make this deterministic we just verify the command
		// runs without error and returns JSON-decodable output.
		mgr := quota.NewManager(townRoot)
		state, err := mgr.Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		state.Accounts = map[string]config.AccountQuotaState{
			"work": {
				Status:    config.QuotaStatusLimited,
				LimitedAt: time.Now().UTC().Format(time.RFC3339),
				ResetsAt:  "11:59pm",
			},
		}
		if err := mgr.Save(state); err != nil {
			t.Fatalf("save: %v", err)
		}

		quotaJSON = true
		out := captureStdout(t, func() {
			if err := runQuotaStatus(&cobra.Command{}, nil); err != nil {
				t.Fatalf("runQuotaStatus: %v", err)
			}
		})

		var items []QuotaStatusItem
		if err := json.Unmarshal([]byte(out), &items); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if len(items) != 1 {
			t.Fatalf("want 1 item, got %d", len(items))
		}
		if items[0].Handle != "work" {
			t.Errorf("handle = %q, want work", items[0].Handle)
		}
		// Status is either "limited" (if 11:59pm hasn't passed yet) or
		// "available" (if it has — rare but possible on this machine).
		// Either way, the ResetsAt should be preserved when still limited.
		if items[0].Status == string(config.QuotaStatusLimited) {
			if items[0].ResetsAt != "11:59pm" {
				t.Errorf("ResetsAt = %q, want 11:59pm", items[0].ResetsAt)
			}
		}
	})

	t.Run("unparseable reset time is preserved", func(t *testing.T) {
		resetQuotaFlags(t)
		townRoot, _ := setupTestTownForQuota(t, map[string]string{
			"work": "w@x",
		}, "work")
		chdirForTest(t, townRoot)

		mgr := quota.NewManager(townRoot)
		state, err := mgr.Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		// Unparseable reset time — ClearExpired should not touch it.
		state.Accounts = map[string]config.AccountQuotaState{
			"work": {
				Status:    config.QuotaStatusLimited,
				LimitedAt: time.Now().UTC().Format(time.RFC3339),
				ResetsAt:  "not-a-valid-time-format",
			},
		}
		if err := mgr.Save(state); err != nil {
			t.Fatalf("save: %v", err)
		}

		quotaJSON = true
		out := captureStdout(t, func() {
			if err := runQuotaStatus(&cobra.Command{}, nil); err != nil {
				t.Fatalf("runQuotaStatus: %v", err)
			}
		})

		var items []QuotaStatusItem
		if err := json.Unmarshal([]byte(out), &items); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if len(items) != 1 {
			t.Fatalf("want 1 item, got %d", len(items))
		}
		if items[0].Status != string(config.QuotaStatusLimited) {
			t.Errorf("status = %q, want limited (unparseable reset should NOT clear)",
				items[0].Status)
		}
	})
}

// --- runQuotaClear integration ---

func TestRunQuotaClear(t *testing.T) {
	t.Run("no args clears all limited and cooldown", func(t *testing.T) {
		resetQuotaFlags(t)
		townRoot, _ := setupTestTownForQuota(t, map[string]string{
			"work":     "w@x",
			"personal": "p@x",
			"side":     "s@x",
		}, "work")
		chdirForTest(t, townRoot)

		// Seed state with some limited/cooldown
		mgr := quota.NewManager(townRoot)
		state, err := mgr.Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		state.Accounts = map[string]config.AccountQuotaState{
			"work":     {Status: config.QuotaStatusLimited, LimitedAt: "2024-01-01T00:00:00Z"},
			"personal": {Status: config.QuotaStatusCooldown},
			"side":     {Status: config.QuotaStatusAvailable},
		}
		if err := mgr.Save(state); err != nil {
			t.Fatalf("save: %v", err)
		}

		out := captureStdout(t, func() {
			if err := runQuotaClear(&cobra.Command{}, nil); err != nil {
				t.Fatalf("runQuotaClear: %v", err)
			}
		})
		// Should have cleared 'work' and 'personal'
		if !strings.Contains(out, "work") {
			t.Errorf("expected 'work' cleared in output: %s", out)
		}
		if !strings.Contains(out, "personal") {
			t.Errorf("expected 'personal' cleared in output: %s", out)
		}
		// Should NOT mention 'side' since it was already available.
		if strings.Contains(out, "side → available") {
			t.Errorf("'side' should not be cleared (was already available): %s", out)
		}

		newState, err := mgr.Load()
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		if newState.Accounts["work"].Status != config.QuotaStatusAvailable {
			t.Errorf("work status = %q, want available", newState.Accounts["work"].Status)
		}
		if newState.Accounts["personal"].Status != config.QuotaStatusAvailable {
			t.Errorf("personal status = %q, want available", newState.Accounts["personal"].Status)
		}
	})

	t.Run("no limited accounts produces helpful message", func(t *testing.T) {
		resetQuotaFlags(t)
		townRoot, _ := setupTestTownForQuota(t, map[string]string{
			"work": "w@x",
		}, "work")
		chdirForTest(t, townRoot)

		out := captureStdout(t, func() {
			if err := runQuotaClear(&cobra.Command{}, nil); err != nil {
				t.Fatalf("runQuotaClear: %v", err)
			}
		})
		if !strings.Contains(out, "No limited accounts to clear") {
			t.Errorf("expected no-op message, got: %s", out)
		}
	})

	t.Run("specific handles get cleared", func(t *testing.T) {
		resetQuotaFlags(t)
		townRoot, _ := setupTestTownForQuota(t, map[string]string{
			"work":     "w@x",
			"personal": "p@x",
		}, "work")
		chdirForTest(t, townRoot)

		mgr := quota.NewManager(townRoot)
		state, err := mgr.Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		state.Accounts = map[string]config.AccountQuotaState{
			"work":     {Status: config.QuotaStatusLimited},
			"personal": {Status: config.QuotaStatusLimited},
		}
		if err := mgr.Save(state); err != nil {
			t.Fatalf("save: %v", err)
		}

		out := captureStdout(t, func() {
			if err := runQuotaClear(&cobra.Command{}, []string{"work"}); err != nil {
				t.Fatalf("runQuotaClear: %v", err)
			}
		})
		if !strings.Contains(out, "work") {
			t.Errorf("expected 'work' in output: %s", out)
		}

		newState, err := mgr.Load()
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		if newState.Accounts["work"].Status != config.QuotaStatusAvailable {
			t.Errorf("work status = %q, want available", newState.Accounts["work"].Status)
		}
		// Personal should NOT be cleared (not named).
		if newState.Accounts["personal"].Status != config.QuotaStatusLimited {
			t.Errorf("personal status = %q, want limited (not named)",
				newState.Accounts["personal"].Status)
		}
	})

	t.Run("multiple handles", func(t *testing.T) {
		resetQuotaFlags(t)
		townRoot, _ := setupTestTownForQuota(t, map[string]string{
			"a": "a@x",
			"b": "b@x",
			"c": "c@x",
		}, "a")
		chdirForTest(t, townRoot)

		mgr := quota.NewManager(townRoot)
		state, err := mgr.Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		state.Accounts = map[string]config.AccountQuotaState{
			"a": {Status: config.QuotaStatusLimited},
			"b": {Status: config.QuotaStatusLimited},
			"c": {Status: config.QuotaStatusLimited},
		}
		if err := mgr.Save(state); err != nil {
			t.Fatalf("save: %v", err)
		}

		if err := runQuotaClear(&cobra.Command{}, []string{"a", "b"}); err != nil {
			t.Fatalf("runQuotaClear: %v", err)
		}

		newState, err := mgr.Load()
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		if newState.Accounts["a"].Status != config.QuotaStatusAvailable {
			t.Errorf("a status = %q, want available", newState.Accounts["a"].Status)
		}
		if newState.Accounts["b"].Status != config.QuotaStatusAvailable {
			t.Errorf("b status = %q, want available", newState.Accounts["b"].Status)
		}
		if newState.Accounts["c"].Status != config.QuotaStatusLimited {
			t.Errorf("c status = %q, want limited (not named)",
				newState.Accounts["c"].Status)
		}
	})
}

// --- quotaLogger adapter ---

func TestQuotaLogger(t *testing.T) {
	// quotaLogger wraps style.PrintWarning which writes to stderr.
	// This test just verifies the Warn call doesn't panic and handles args.
	t.Run("Warn does not panic", func(t *testing.T) {
		logger := quotaLogger{}
		logger.Warn("simple")
		logger.Warn("with format: %s", "value")
		logger.Warn("multiple args: %s=%d", "key", 42)
	})
}

// --- runQuotaRotate argument validation ---

func TestRunQuotaRotate_ArgValidation(t *testing.T) {
	t.Run("no accounts configured returns error", func(t *testing.T) {
		resetQuotaFlags(t)
		townRoot, _ := setupTestTownForQuota(t, nil, "")
		chdirForTest(t, townRoot)

		err := runQuotaRotate(&cobra.Command{}, nil)
		if err == nil {
			t.Error("expected error when no accounts configured")
		}
		if err != nil && !strings.Contains(err.Error(), "no accounts configured") {
			t.Errorf("error message = %q, want 'no accounts configured'", err.Error())
		}
	})

	t.Run("single account returns error", func(t *testing.T) {
		resetQuotaFlags(t)
		townRoot, _ := setupTestTownForQuota(t, map[string]string{
			"solo": "s@x",
		}, "solo")
		chdirForTest(t, townRoot)

		err := runQuotaRotate(&cobra.Command{}, nil)
		if err == nil {
			t.Error("expected error when only 1 account configured")
		}
		if err != nil && !strings.Contains(err.Error(), "at least 2 accounts") {
			t.Errorf("error message = %q, want 'at least 2 accounts'", err.Error())
		}
	})

	t.Run("unknown --from account returns error", func(t *testing.T) {
		resetQuotaFlags(t)
		townRoot, _ := setupTestTownForQuota(t, map[string]string{
			"work":     "w@x",
			"personal": "p@x",
		}, "work")
		chdirForTest(t, townRoot)

		rotateFrom = "nonexistent"
		err := runQuotaRotate(&cobra.Command{}, nil)
		if err == nil {
			t.Error("expected error for unknown --from account")
		}
		if err != nil && !strings.Contains(err.Error(), "not found") {
			t.Errorf("error message = %q, want 'not found'", err.Error())
		}
		// Error message should list available accounts.
		if err != nil && !strings.Contains(err.Error(), "available:") {
			t.Errorf("error should list available: %s", err.Error())
		}
	})
}

// --- runQuotaWatch argument validation ---

func TestRunQuotaWatch_ArgValidation(t *testing.T) {
	t.Run("no accounts returns error", func(t *testing.T) {
		resetQuotaFlags(t)
		townRoot, _ := setupTestTownForQuota(t, nil, "")
		chdirForTest(t, townRoot)

		err := runQuotaWatch(&cobra.Command{}, nil)
		if err == nil {
			t.Error("expected error when no accounts")
		}
		if err != nil && !strings.Contains(err.Error(), "no accounts configured") {
			t.Errorf("error message = %q, want 'no accounts configured'", err.Error())
		}
	})

	t.Run("single account returns error", func(t *testing.T) {
		resetQuotaFlags(t)
		townRoot, _ := setupTestTownForQuota(t, map[string]string{
			"solo": "s@x",
		}, "solo")
		chdirForTest(t, townRoot)

		err := runQuotaWatch(&cobra.Command{}, nil)
		if err == nil {
			t.Error("expected error when only 1 account")
		}
		if err != nil && !strings.Contains(err.Error(), "at least 2 accounts") {
			t.Errorf("error message = %q, want 'at least 2 accounts'", err.Error())
		}
	})
}

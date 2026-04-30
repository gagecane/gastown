package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/session"
)

// setupDeaconTestRegistry installs a test PrefixRegistry with well-known rigs
// so agentAddressToIDs produces deterministic session/bead IDs.
//
// Maps:
//
//	"gastown"   -> "gt"
//	"beads"     -> "bd"
//	"longeye"   -> "le"
//
// Unknown rigs fall through to session.DefaultPrefix ("gt").
func setupDeaconTestRegistry(t *testing.T) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("bd", "beads")
	reg.Register("le", "longeye")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

// TestGetDeaconSessionName verifies the wrapper returns the town-level
// singleton session name.
func TestGetDeaconSessionName(t *testing.T) {
	got := getDeaconSessionName()
	want := session.DeaconSessionName()
	if got != want {
		t.Errorf("getDeaconSessionName() = %q, want %q", got, want)
	}
	// Must be the HQ-prefixed singleton.
	if !strings.HasPrefix(got, "hq-") {
		t.Errorf("getDeaconSessionName() = %q, want hq- prefix", got)
	}
	if !strings.Contains(got, "deacon") {
		t.Errorf("getDeaconSessionName() = %q, want to contain 'deacon'", got)
	}
}

// TestAgentAddressToIDs exercises every structural branch of the
// address-to-IDs resolver: role shortcuts, rig/role pairs, rig/type/name
// triples, and invalid inputs.
func TestAgentAddressToIDs(t *testing.T) {
	setupDeaconTestRegistry(t)

	tests := []struct {
		name        string
		address     string
		wantBeadID  string
		wantSession string
		wantErr     bool
		errContains string
	}{
		// Role shortcuts (town-level singletons).
		{
			name:        "deacon role shortcut",
			address:     "deacon",
			wantBeadID:  "hq-deacon",
			wantSession: "hq-deacon",
		},
		{
			name:        "mayor role shortcut",
			address:     "mayor",
			wantBeadID:  "hq-mayor",
			wantSession: "hq-mayor",
		},

		// rig/role (2 parts).
		{
			name:        "rig witness known prefix",
			address:     "gastown/witness",
			wantBeadID:  "gt-witness",
			wantSession: "gt-witness",
		},
		{
			name:        "rig refinery known prefix",
			address:     "gastown/refinery",
			wantBeadID:  "gt-refinery",
			wantSession: "gt-refinery",
		},
		{
			name:        "rig witness other prefix",
			address:     "beads/witness",
			wantBeadID:  "bd-witness",
			wantSession: "bd-witness",
		},
		{
			name:        "rig refinery other prefix",
			address:     "longeye/refinery",
			wantBeadID:  "le-refinery",
			wantSession: "le-refinery",
		},
		{
			name:        "rig witness unknown rig falls back to default prefix",
			address:     "unknown-rig/witness",
			wantBeadID:  session.DefaultPrefix + "-witness",
			wantSession: session.DefaultPrefix + "-witness",
		},
		{
			name:        "rig/role unknown role",
			address:     "gastown/bogus",
			wantErr:     true,
			errContains: "unknown role",
		},

		// rig/type/name (3 parts).
		{
			name:        "polecat in gastown",
			address:     "gastown/polecats/dust",
			wantBeadID:  "gt-dust",
			wantSession: "gt-dust",
		},
		{
			name:        "polecat in beads",
			address:     "beads/polecats/max",
			wantBeadID:  "bd-max",
			wantSession: "bd-max",
		},
		{
			name:        "crew in gastown",
			address:     "gastown/crew/alpha",
			wantBeadID:  "gt-crew-alpha",
			wantSession: "gt-crew-alpha",
		},
		{
			name:        "crew in longeye",
			address:     "longeye/crew/bravo",
			wantBeadID:  "le-crew-bravo",
			wantSession: "le-crew-bravo",
		},
		{
			name:        "unknown agent type",
			address:     "gastown/dogs/rex",
			wantErr:     true,
			errContains: "unknown agent type",
		},
		{
			name:        "rig/type/name unknown rig uses default prefix",
			address:     "unknown-rig/polecats/foo",
			wantBeadID:  session.DefaultPrefix + "-foo",
			wantSession: session.DefaultPrefix + "-foo",
		},

		// Invalid formats.
		{
			name:        "empty string",
			address:     "",
			wantErr:     true,
			errContains: "invalid agent address format",
		},
		{
			name:        "too many parts",
			address:     "gastown/polecats/dust/extra",
			wantErr:     true,
			errContains: "invalid agent address format",
		},
		{
			name:        "single unknown word",
			address:     "witness",
			wantErr:     true,
			errContains: "invalid agent address format",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			beadID, sessionName, err := agentAddressToIDs(tc.address)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("agentAddressToIDs(%q) returned no error, want error containing %q",
						tc.address, tc.errContains)
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Errorf("agentAddressToIDs(%q) err = %v, want containing %q",
						tc.address, err, tc.errContains)
				}
				if beadID != "" || sessionName != "" {
					t.Errorf("agentAddressToIDs(%q) returned non-empty ids on error: bead=%q session=%q",
						tc.address, beadID, sessionName)
				}
				return
			}
			if err != nil {
				t.Fatalf("agentAddressToIDs(%q) unexpected error: %v", tc.address, err)
			}
			if beadID != tc.wantBeadID {
				t.Errorf("agentAddressToIDs(%q) beadID = %q, want %q",
					tc.address, beadID, tc.wantBeadID)
			}
			if sessionName != tc.wantSession {
				t.Errorf("agentAddressToIDs(%q) sessionName = %q, want %q",
					tc.address, sessionName, tc.wantSession)
			}
		})
	}
}

// TestDeaconCmdMetadata verifies the top-level `gt deacon` command is wired
// with the expected aliases, group, and help text.
func TestDeaconCmdMetadata(t *testing.T) {
	if deaconCmd.Use != "deacon" {
		t.Errorf("deaconCmd.Use = %q, want %q", deaconCmd.Use, "deacon")
	}
	if deaconCmd.GroupID != GroupAgents {
		t.Errorf("deaconCmd.GroupID = %q, want %q", deaconCmd.GroupID, GroupAgents)
	}
	if deaconCmd.Short == "" {
		t.Error("deaconCmd.Short is empty")
	}
	foundAlias := false
	for _, a := range deaconCmd.Aliases {
		if a == "dea" {
			foundAlias = true
			break
		}
	}
	if !foundAlias {
		t.Errorf("deaconCmd.Aliases = %v, want to contain %q", deaconCmd.Aliases, "dea")
	}
}

// TestDeaconRequiresSubcommand verifies the deacon command returns an error
// when invoked without a subcommand.
func TestDeaconRequiresSubcommand(t *testing.T) {
	if deaconCmd.RunE == nil {
		t.Fatal("deaconCmd.RunE is nil; expected requireSubcommand wiring")
	}
	err := deaconCmd.RunE(deaconCmd, []string{})
	if err == nil {
		t.Fatal("deaconCmd.RunE with no args returned no error")
	}
	if !strings.Contains(err.Error(), "requires a subcommand") {
		t.Errorf("deaconCmd.RunE error = %v, want containing 'requires a subcommand'", err)
	}
}

// TestDeaconSubcommandsRegistered verifies every expected subcommand is
// registered on the deacon command.
func TestDeaconSubcommandsRegistered(t *testing.T) {
	expected := []string{
		"start",
		"stop",
		"attach",
		"status",
		"restart",
		"heartbeat",
		"health-check",
		"force-kill",
		"health-state",
		"stale-hooks",
		"pause",
		"resume",
		"cleanup-orphans",
		"zombie-scan",
		"redispatch",
		"redispatch-state",
		"feed-stranded",
		"feed-stranded-state",
	}

	got := make(map[string]*cobra.Command)
	for _, sub := range deaconCmd.Commands() {
		got[sub.Name()] = sub
	}

	for _, name := range expected {
		if _, ok := got[name]; !ok {
			t.Errorf("deaconCmd missing subcommand %q", name)
		}
	}

	// Light sanity: ensure we didn't drop any — expected count matches the
	// init() wiring so future additions are an explicit call-site change.
	if len(deaconCmd.Commands()) < len(expected) {
		t.Errorf("deaconCmd has %d subcommands, want >= %d",
			len(deaconCmd.Commands()), len(expected))
	}
}

// TestDeaconSubcommandAliases verifies subcommand aliases (short forms).
func TestDeaconSubcommandAliases(t *testing.T) {
	cases := []struct {
		name    string
		cmd     *cobra.Command
		aliases []string
	}{
		{"start", deaconStartCmd, []string{"spawn"}},
		{"attach", deaconAttachCmd, []string{"at"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range tc.aliases {
				found := false
				for _, a := range tc.cmd.Aliases {
					if a == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("%q command aliases = %v, want to contain %q",
						tc.name, tc.cmd.Aliases, want)
				}
			}
		})
	}
}

// TestDeaconZombieScanSuggestions verifies the zombie-scan command exposes
// SuggestFor entries so typos like "orphan" surface the right command.
func TestDeaconZombieScanSuggestions(t *testing.T) {
	want := map[string]bool{
		"orphan-scan": false,
		"orphan_scan": false,
		"orphan":      false,
	}
	for _, s := range deaconZombieScanCmd.SuggestFor {
		if _, ok := want[s]; ok {
			want[s] = true
		}
	}
	for name, ok := range want {
		if !ok {
			t.Errorf("deaconZombieScanCmd.SuggestFor missing %q", name)
		}
	}
}

// TestDeaconSubcommandArgs verifies subcommands that require positional args
// enforce that via cobra.ExactArgs(1).
func TestDeaconSubcommandArgs(t *testing.T) {
	cases := []struct {
		name string
		cmd  *cobra.Command
	}{
		{"health-check", deaconHealthCheckCmd},
		{"force-kill", deaconForceKillCmd},
		{"redispatch", deaconRedispatchCmd},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/zero args errors", func(t *testing.T) {
			if tc.cmd.Args == nil {
				t.Fatalf("%s Args validator is nil, expected cobra.ExactArgs(1)", tc.name)
			}
			if err := tc.cmd.Args(tc.cmd, []string{}); err == nil {
				t.Errorf("%s Args([]) returned no error, want ExactArgs(1) failure", tc.name)
			}
		})
		t.Run(tc.name+"/two args errors", func(t *testing.T) {
			if err := tc.cmd.Args(tc.cmd, []string{"a", "b"}); err == nil {
				t.Errorf("%s Args(a,b) returned no error, want ExactArgs(1) failure", tc.name)
			}
		})
		t.Run(tc.name+"/one arg accepted", func(t *testing.T) {
			if err := tc.cmd.Args(tc.cmd, []string{"only"}); err != nil {
				t.Errorf("%s Args(only) = %v, want nil", tc.name, err)
			}
		})
	}
}

// TestDeaconStatusFlags verifies flag definitions and defaults on `deacon status`.
func TestDeaconStatusFlags(t *testing.T) {
	f := deaconStatusCmd.Flags().Lookup("json")
	if f == nil {
		t.Fatal("deacon status missing --json flag")
	}
	if f.DefValue != "false" {
		t.Errorf("deacon status --json default = %q, want %q", f.DefValue, "false")
	}
}

// TestDeaconHealthCheckFlags verifies flag definitions and defaults on
// `deacon health-check`.
func TestDeaconHealthCheckFlags(t *testing.T) {
	cases := []struct {
		flag    string
		def     string
		usage   string
		isDurFn bool // when true, also assert the Go default via bound var
	}{
		{"timeout", "30s", "wait for agent response", true},
		{"failures", "3", "consecutive failures", false},
		{"cooldown", "5m0s", "between force-kills", true},
	}
	for _, tc := range cases {
		t.Run(tc.flag, func(t *testing.T) {
			f := deaconHealthCheckCmd.Flags().Lookup(tc.flag)
			if f == nil {
				t.Fatalf("deacon health-check missing --%s flag", tc.flag)
			}
			if f.DefValue != tc.def {
				t.Errorf("--%s default = %q, want %q", tc.flag, f.DefValue, tc.def)
			}
			if !strings.Contains(strings.ToLower(f.Usage), strings.ToLower(tc.usage)) {
				t.Errorf("--%s usage = %q, want to contain %q", tc.flag, f.Usage, tc.usage)
			}
		})
	}
	// Go-level defaults on the bound variables (round-trip check).
	if healthCheckTimeout != 30*time.Second {
		t.Errorf("healthCheckTimeout default = %v, want 30s", healthCheckTimeout)
	}
	if healthCheckFailures != 3 {
		t.Errorf("healthCheckFailures default = %d, want 3", healthCheckFailures)
	}
	if healthCheckCooldown != 5*time.Minute {
		t.Errorf("healthCheckCooldown default = %v, want 5m", healthCheckCooldown)
	}
}

// TestDeaconForceKillFlags verifies flag definitions and defaults on
// `deacon force-kill`.
func TestDeaconForceKillFlags(t *testing.T) {
	reason := deaconForceKillCmd.Flags().Lookup("reason")
	if reason == nil {
		t.Fatal("deacon force-kill missing --reason flag")
	}
	if reason.DefValue != "" {
		t.Errorf("--reason default = %q, want empty", reason.DefValue)
	}

	skipNotify := deaconForceKillCmd.Flags().Lookup("skip-notify")
	if skipNotify == nil {
		t.Fatal("deacon force-kill missing --skip-notify flag")
	}
	if skipNotify.DefValue != "false" {
		t.Errorf("--skip-notify default = %q, want false", skipNotify.DefValue)
	}
}

// TestDeaconStaleHooksFlags verifies flag definitions and defaults on
// `deacon stale-hooks`.
func TestDeaconStaleHooksFlags(t *testing.T) {
	maxAge := deaconStaleHooksCmd.Flags().Lookup("max-age")
	if maxAge == nil {
		t.Fatal("deacon stale-hooks missing --max-age flag")
	}
	if maxAge.DefValue != "1h0m0s" {
		t.Errorf("--max-age default = %q, want 1h0m0s", maxAge.DefValue)
	}
	if staleHooksMaxAge != time.Hour {
		t.Errorf("staleHooksMaxAge default = %v, want 1h", staleHooksMaxAge)
	}

	dryRun := deaconStaleHooksCmd.Flags().Lookup("dry-run")
	if dryRun == nil {
		t.Fatal("deacon stale-hooks missing --dry-run flag")
	}
	if dryRun.DefValue != "false" {
		t.Errorf("--dry-run default = %q, want false", dryRun.DefValue)
	}
}

// TestDeaconPauseFlags verifies the `deacon pause --reason` flag.
func TestDeaconPauseFlags(t *testing.T) {
	reason := deaconPauseCmd.Flags().Lookup("reason")
	if reason == nil {
		t.Fatal("deacon pause missing --reason flag")
	}
	if reason.DefValue != "" {
		t.Errorf("--reason default = %q, want empty", reason.DefValue)
	}
}

// TestDeaconZombieScanFlags verifies the `deacon zombie-scan --dry-run` flag.
func TestDeaconZombieScanFlags(t *testing.T) {
	dryRun := deaconZombieScanCmd.Flags().Lookup("dry-run")
	if dryRun == nil {
		t.Fatal("deacon zombie-scan missing --dry-run flag")
	}
	if dryRun.DefValue != "false" {
		t.Errorf("--dry-run default = %q, want false", dryRun.DefValue)
	}
}

// TestDeaconRedispatchFlags verifies flag definitions on `deacon redispatch`.
// Note: the numeric/duration defaults are 0 by design — the runtime fills in
// real defaults (documented in the command Long as "default: 3" / "5m").
func TestDeaconRedispatchFlags(t *testing.T) {
	cases := []struct {
		flag string
		def  string
	}{
		{"rig", ""},
		{"max-attempts", "0"},
		{"cooldown", "0s"},
	}
	for _, tc := range cases {
		t.Run(tc.flag, func(t *testing.T) {
			f := deaconRedispatchCmd.Flags().Lookup(tc.flag)
			if f == nil {
				t.Fatalf("deacon redispatch missing --%s flag", tc.flag)
			}
			if f.DefValue != tc.def {
				t.Errorf("--%s default = %q, want %q", tc.flag, f.DefValue, tc.def)
			}
		})
	}
}

// TestDeaconFeedStrandedFlags verifies flag definitions on `deacon feed-stranded`.
// Same sentinel-zero pattern as redispatch.
func TestDeaconFeedStrandedFlags(t *testing.T) {
	cases := []struct {
		flag string
		def  string
	}{
		{"max-feeds", "0"},
		{"cooldown", "0s"},
		{"json", "false"},
	}
	for _, tc := range cases {
		t.Run(tc.flag, func(t *testing.T) {
			f := deaconFeedStrandedCmd.Flags().Lookup(tc.flag)
			if f == nil {
				t.Fatalf("deacon feed-stranded missing --%s flag", tc.flag)
			}
			if f.DefValue != tc.def {
				t.Errorf("--%s default = %q, want %q", tc.flag, f.DefValue, tc.def)
			}
		})
	}
}

// TestDeaconLifecycleAgentFlag verifies the --agent override flag is wired on
// start/attach/restart and defaults to empty (i.e., use town default).
func TestDeaconLifecycleAgentFlag(t *testing.T) {
	cases := []struct {
		name string
		cmd  *cobra.Command
	}{
		{"start", deaconStartCmd},
		{"attach", deaconAttachCmd},
		{"restart", deaconRestartCmd},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.cmd.Flags().Lookup("agent")
			if f == nil {
				t.Fatalf("deacon %s missing --agent flag", tc.name)
			}
			if f.DefValue != "" {
				t.Errorf("deacon %s --agent default = %q, want empty", tc.name, f.DefValue)
			}
			if !strings.Contains(strings.ToLower(f.Usage), "overrides town default") {
				t.Errorf("deacon %s --agent usage = %q, want to mention 'overrides town default'",
					tc.name, f.Usage)
			}
		})
	}
}

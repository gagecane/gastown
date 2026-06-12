package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/rig"
)

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stderr = w

	fn()

	_ = w.Close()
	os.Stderr = old

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	_ = r.Close()

	return buf.String()
}

func TestDiscoverRigAgents_UsesRigPrefix(t *testing.T) {
	townRoot := t.TempDir()
	writeTestRoutes(t, townRoot, []beads.Route{
		{Prefix: "bd-", Path: "beads/mayor/rig"},
	})

	r := &rig.Rig{
		Name:       "beads",
		Path:       filepath.Join(townRoot, "beads"),
		HasWitness: true,
	}

	allAgentBeads := map[string]*beads.Issue{
		"bd-beads-witness": {
			ID:         "bd-beads-witness",
			AgentState: "running",
			HookBead:   "bd-hook",
		},
	}
	allHookBeads := map[string]*beads.Issue{
		"bd-hook": {ID: "bd-hook", Title: "Pinned"},
	}

	agents := discoverRigAgents(map[string]bool{}, r, nil, allAgentBeads, allHookBeads, nil, true)
	if len(agents) != 1 {
		t.Fatalf("discoverRigAgents() returned %d agents, want 1", len(agents))
	}

	if agents[0].State != "running" {
		t.Fatalf("agent state = %q, want %q", agents[0].State, "running")
	}
	if !agents[0].HasWork {
		t.Fatalf("agent HasWork = false, want true")
	}
	if agents[0].WorkTitle != "Pinned" {
		t.Fatalf("agent WorkTitle = %q, want %q", agents[0].WorkTitle, "Pinned")
	}
}

func TestRenderAgentDetails_UsesRigPrefix(t *testing.T) {
	townRoot := t.TempDir()
	writeTestRoutes(t, townRoot, []beads.Route{
		{Prefix: "bd-", Path: "beads/mayor/rig"},
	})

	agent := AgentRuntime{
		Name:    "witness",
		Address: "beads/witness",
		Role:    "witness",
		Running: true,
	}

	var buf bytes.Buffer
	renderAgentDetails(&buf, agent, "", nil, townRoot)
	output := buf.String()

	if !strings.Contains(output, "bd-beads-witness") {
		t.Fatalf("output %q does not contain rig-prefixed bead ID", output)
	}
}

func TestDiscoverRigAgents_ZombieSessionNotRunning(t *testing.T) {
	// Verify that a session in allSessions with value=false (zombie: tmux alive,
	// agent dead) results in agent.Running=false. This is the core fix for gt-bd6i3.
	townRoot := t.TempDir()
	writeTestRoutes(t, townRoot, []beads.Route{
		{Prefix: "gt-", Path: "gastown/mayor/rig"},
	})

	r := &rig.Rig{
		Name:       "gastown",
		Path:       filepath.Join(townRoot, "gastown"),
		HasWitness: true,
	}

	// allSessions has the witness session but marked as zombie (false).
	// This simulates a tmux session that exists but whose agent process has died.
	allSessions := map[string]bool{
		"gt-gastown-witness": false, // zombie: tmux exists, agent dead
	}

	agents := discoverRigAgents(allSessions, r, nil, nil, nil, nil, true)
	for _, a := range agents {
		if a.Role == "witness" {
			if a.Running {
				t.Fatal("zombie witness session (allSessions=false) should show as not running")
			}
			return
		}
	}
	t.Fatal("witness agent not found in results")
}

func TestDiscoverRigAgents_MissingSessionNotRunning(t *testing.T) {
	// Verify that a session not in allSessions at all results in agent.Running=false.
	townRoot := t.TempDir()
	writeTestRoutes(t, townRoot, []beads.Route{
		{Prefix: "gt-", Path: "gastown/mayor/rig"},
	})

	r := &rig.Rig{
		Name:       "gastown",
		Path:       filepath.Join(townRoot, "gastown"),
		HasWitness: true,
	}

	// Empty sessions map - no tmux sessions exist at all
	allSessions := map[string]bool{}

	agents := discoverRigAgents(allSessions, r, nil, nil, nil, nil, true)
	for _, a := range agents {
		if a.Role == "witness" {
			if a.Running {
				t.Fatal("witness with no tmux session should show as not running")
			}
			return
		}
	}
	t.Fatal("witness agent not found in results")
}

func TestBuildStatusIndicator_ZombieShowsStopped(t *testing.T) {
	// Verify that a zombie agent (Running=false) shows ○ (stopped), not ● (running)
	agent := AgentRuntime{Running: false}
	indicator := buildStatusIndicator(agent)
	if strings.Contains(indicator, "●") {
		t.Fatal("zombie agent (Running=false) should not show ● indicator")
	}
}

func TestBuildStatusIndicator_AliveShowsRunning(t *testing.T) {
	// Verify that an alive agent (Running=true) shows ● (running)
	agent := AgentRuntime{Running: true}
	indicator := buildStatusIndicator(agent)
	if strings.Contains(indicator, "○") {
		t.Fatal("alive agent (Running=true) should not show ○ indicator")
	}
}

// TestClassifyLifecycle_AllStates verifies the (Running, HasWork, Role)
// truth table for the gu-r9g1 lifecycle taxonomy.
func TestClassifyLifecycle_AllStates(t *testing.T) {
	cases := []struct {
		name     string
		agent    AgentRuntime
		expected AgentLifecycle
	}{
		{
			name:     "running polecat without work → running",
			agent:    AgentRuntime{Running: true, Role: "polecat"},
			expected: LifecycleRunning,
		},
		{
			name:     "running polecat with work → working",
			agent:    AgentRuntime{Running: true, HasWork: true, Role: "polecat"},
			expected: LifecycleWorking,
		},
		{
			name:     "dead polecat with work → dead (emergency)",
			agent:    AgentRuntime{Running: false, HasWork: true, Role: "polecat"},
			expected: LifecycleDead,
		},
		{
			name:     "dead crew with work → dead (emergency)",
			agent:    AgentRuntime{Running: false, HasWork: true, Role: "crew"},
			expected: LifecycleDead,
		},
		{
			name:     "exited polecat without work → free",
			agent:    AgentRuntime{Running: false, HasWork: false, Role: "polecat"},
			expected: LifecycleFree,
		},
		{
			name:     "idle crew without work → idle",
			agent:    AgentRuntime{Running: false, HasWork: false, Role: "crew"},
			expected: LifecycleIdle,
		},
		{
			name:     "idle witness without work → idle",
			agent:    AgentRuntime{Running: false, HasWork: false, Role: "witness"},
			expected: LifecycleIdle,
		},
		{
			name:     "idle refinery without work → idle",
			agent:    AgentRuntime{Running: false, HasWork: false, Role: "refinery"},
			expected: LifecycleIdle,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyLifecycle(tc.agent); got != tc.expected {
				t.Fatalf("classifyLifecycle = %q, want %q", got, tc.expected)
			}
		})
	}
}

// TestBuildStatusIndicator_DistinguishesStoppedStates is the core gu-r9g1
// regression: a stopped polecat with no work (free) MUST render a different
// glyph than a stopped polecat with pinned work (dead). Before the fix both
// rendered as ○ and the dead-with-active-work case was invisible.
func TestBuildStatusIndicator_DistinguishesStoppedStates(t *testing.T) {
	dead := buildStatusIndicator(AgentRuntime{Running: false, HasWork: true, Role: "polecat"})
	free := buildStatusIndicator(AgentRuntime{Running: false, HasWork: false, Role: "polecat"})
	idle := buildStatusIndicator(AgentRuntime{Running: false, HasWork: false, Role: "crew"})

	if !strings.Contains(dead, "❌") {
		t.Fatalf("dead-with-work should include ❌, got %q", dead)
	}
	if strings.Contains(free, "❌") || strings.Contains(idle, "❌") {
		t.Fatalf("only the dead state should include ❌; free=%q idle=%q", free, idle)
	}
	if !strings.Contains(free, "◌") {
		t.Fatalf("free polecat should include ◌, got %q", free)
	}
	if !strings.Contains(idle, "○") {
		t.Fatalf("idle crew should include ○, got %q", idle)
	}
	// Make sure the three glyphs don't collide pairwise.
	if dead == free || free == idle || dead == idle {
		t.Fatalf("three stopped states must render distinctly: dead=%q free=%q idle=%q", dead, free, idle)
	}
}

// TestBuildStatusIndicator_LegacyZombieGlyph guards backward compatibility
// for callers asserting on ○ for crew/witness/refinery (the original glyph).
// Polecats now render ◌ when free; long-lived agents still render ○ when idle.
func TestBuildStatusIndicator_LegacyZombieGlyph(t *testing.T) {
	for _, role := range []string{"crew", "witness", "refinery"} {
		ind := buildStatusIndicator(AgentRuntime{Running: false, Role: role})
		if !strings.Contains(ind, "○") {
			t.Fatalf("idle %s should still render ○, got %q", role, ind)
		}
	}
}

func TestParseLifecycleFilter(t *testing.T) {
	cases := []struct {
		spec    string
		want    map[AgentLifecycle]bool
		wantErr bool
	}{
		{spec: "", want: nil},
		{spec: "all", want: nil},
		{spec: "  all  ", want: nil},
		{spec: "running,dead", want: map[AgentLifecycle]bool{LifecycleRunning: true, LifecycleDead: true}},
		{spec: "DEAD,Free", want: map[AgentLifecycle]bool{LifecycleDead: true, LifecycleFree: true}},
		{spec: "idle", want: map[AgentLifecycle]bool{LifecycleIdle: true}},
		{spec: "working,idle,all", want: nil}, // 'all' wins
		{spec: "bogus", wantErr: true},
		{spec: "running,nope", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			got, err := parseLifecycleFilter(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.want == nil {
				if got != nil {
					t.Fatalf("expected nil set, got %v", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch: got %d (%v), want %d (%v)", len(got), got, len(tc.want), tc.want)
			}
			for k := range tc.want {
				if _, ok := got[k]; !ok {
					t.Fatalf("missing %q in result", k)
				}
			}
		})
	}
}

func TestFilterAgents_ScopesToAllowSet(t *testing.T) {
	agents := []AgentRuntime{
		{Name: "alive", Running: true, Role: "polecat"},
		{Name: "working", Running: true, HasWork: true, Role: "polecat"},
		{Name: "dead-pol", Running: false, HasWork: true, Role: "polecat"},
		{Name: "free-pol", Running: false, Role: "polecat"},
		{Name: "idle-crew", Running: false, Role: "crew"},
	}

	// nil allow set → identity
	if out := filterAgents(agents, nil); len(out) != len(agents) {
		t.Fatalf("nil allow should pass through, got %d", len(out))
	}

	// dead only — surfaces the emergency case
	deadOnly := filterAgents(agents, map[AgentLifecycle]struct{}{LifecycleDead: {}})
	if len(deadOnly) != 1 || deadOnly[0].Name != "dead-pol" {
		t.Fatalf("dead-only filter wrong, got %+v", deadOnly)
	}

	// dead + working — actionable view
	combo := filterAgents(agents, map[AgentLifecycle]struct{}{
		LifecycleDead:    {},
		LifecycleWorking: {},
	})
	if len(combo) != 2 {
		t.Fatalf("dead+working filter want 2, got %d (%+v)", len(combo), combo)
	}

	// idle + free — both stopped-no-work cases distinguishable
	stopped := filterAgents(agents, map[AgentLifecycle]struct{}{
		LifecycleFree: {},
		LifecycleIdle: {},
	})
	if len(stopped) != 2 {
		t.Fatalf("free+idle filter want 2, got %d (%+v)", len(stopped), stopped)
	}
}

// TestOutputStatusText_FilterScopesAgents verifies that the --filter flag,
// when set on the package-level statusFilter var, scopes the rendered list.
func TestOutputStatusText_FilterScopesAgents(t *testing.T) {
	old := statusFilter
	t.Cleanup(func() { statusFilter = old })
	statusFilter = "dead"

	status := TownStatus{
		Name:     "gt",
		Location: "/tmp/gt",
		Agents: []AgentRuntime{
			{Name: "mayor", Address: "mayor", Role: "mayor", Running: true},
		},
		Rigs: []RigStatus{{
			Name: "gastown",
			Agents: []AgentRuntime{
				{Name: "alive", Address: "gastown/alive", Role: "polecat", Running: true},
				{Name: "dead-pol", Address: "gastown/dead-pol", Role: "polecat", Running: false, HasWork: true},
				{Name: "free-pol", Address: "gastown/free-pol", Role: "polecat", Running: false},
			},
		}},
	}

	var buf bytes.Buffer
	if err := outputStatusText(&buf, status); err != nil {
		t.Fatalf("outputStatusText error: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "Filter:") {
		t.Fatalf("expected Filter: notice, got: %q", out)
	}
	if !strings.Contains(out, "dead-pol") {
		t.Fatalf("expected dead-pol in filtered output, got: %q", out)
	}
	if strings.Contains(out, "free-pol") {
		t.Fatalf("filter=dead should hide free-pol, got: %q", out)
	}
	if strings.Contains(out, "  alive ") || strings.Contains(out, " alive\n") {
		t.Fatalf("filter=dead should hide running 'alive' polecat, got: %q", out)
	}
	if strings.Contains(out, "mayor") {
		t.Fatalf("filter=dead should hide running global mayor, got: %q", out)
	}
}

func TestBuildStatusIndicator_DNDMutedShowsBadge(t *testing.T) {
	agent := AgentRuntime{Running: true, NotificationLevel: beads.NotifyMuted}
	indicator := buildStatusIndicator(agent)
	if !strings.Contains(indicator, "🔕") {
		t.Fatalf("expected muted indicator to include 🔕, got %q", indicator)
	}
}

func TestOutputStatusText_IncludesDNDSection(t *testing.T) {
	status := TownStatus{
		Name:     "gt",
		Location: "/tmp/gt",
		DND: &DNDInfo{
			Enabled: true,
			Level:   beads.NotifyMuted,
			Agent:   "hq-mayor",
		},
	}

	var buf bytes.Buffer
	if err := outputStatusText(&buf, status); err != nil {
		t.Fatalf("outputStatusText error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "DND:") {
		t.Fatalf("expected DND section in status output, got: %q", out)
	}
	if !strings.Contains(out, "on") {
		t.Fatalf("expected DND state 'on' in status output, got: %q", out)
	}
}

func TestRunStatusWatch_RejectsZeroInterval(t *testing.T) {
	oldInterval := statusInterval
	oldWatch := statusWatch
	defer func() {
		statusInterval = oldInterval
		statusWatch = oldWatch
	}()

	statusInterval = 0
	statusWatch = true

	err := runStatusWatch(nil, nil)
	if err == nil {
		t.Fatal("expected error for zero interval, got nil")
	}
	if !strings.Contains(err.Error(), "positive") {
		t.Errorf("error %q should mention 'positive'", err.Error())
	}
}

func TestRunStatusWatch_RejectsNegativeInterval(t *testing.T) {
	oldInterval := statusInterval
	oldWatch := statusWatch
	defer func() {
		statusInterval = oldInterval
		statusWatch = oldWatch
	}()

	statusInterval = -5
	statusWatch = true

	err := runStatusWatch(nil, nil)
	if err == nil {
		t.Fatal("expected error for negative interval, got nil")
	}
	if !strings.Contains(err.Error(), "positive") {
		t.Errorf("error %q should mention 'positive'", err.Error())
	}
}

func TestRunStatusWatch_RejectsJSONCombo(t *testing.T) {
	oldJSON := statusJSON
	oldWatch := statusWatch
	oldInterval := statusInterval
	defer func() {
		statusJSON = oldJSON
		statusWatch = oldWatch
		statusInterval = oldInterval
	}()

	statusJSON = true
	statusWatch = true
	statusInterval = 2

	err := runStatusWatch(nil, nil)
	if err == nil {
		t.Fatal("expected error for --json + --watch, got nil")
	}
	if !strings.Contains(err.Error(), "cannot be used together") {
		t.Errorf("error %q should mention 'cannot be used together'", err.Error())
	}
}

func TestTryStatusDetailLockContention(t *testing.T) {
	townRoot := t.TempDir()

	release, ok := tryStatusDetailLock(townRoot)
	if !ok {
		t.Fatal("first status detail lock should be acquired")
	}

	if release2, ok := tryStatusDetailLock(townRoot); ok {
		release2()
		t.Fatal("second status detail lock should fail while first is held")
	}

	release()

	release3, ok := tryStatusDetailLock(townRoot)
	if !ok {
		t.Fatal("status detail lock should be reusable after release")
	}
	release3()
}

func TestIsKnownAgent(t *testing.T) {
	t.Parallel()

	// All agent presets should be recognized
	for _, name := range config.ListAgentPresets() {
		t.Run(name+"_known", func(t *testing.T) {
			if !isKnownAgent(name) {
				t.Errorf("isKnownAgent(%q) = false, want true", name)
			}
		})
	}

	// Non-agents should not be recognized
	for _, name := range []string{"bash", "node", ""} {
		t.Run(name+"_unknown", func(t *testing.T) {
			if isKnownAgent(name) {
				t.Errorf("isKnownAgent(%q) = true, want false", name)
			}
		})
	}
}

func TestIsAgentWrapper(t *testing.T) {
	t.Parallel()
	tests := []struct {
		base string
		want bool
	}{
		{"node", true},
		{"bun", true},
		{"npx", true},
		{"bunx", true},
		{"claude", false},
		{"pi", false},
		{"bash", false},
	}

	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			if got := isAgentWrapper(tt.base); got != tt.want {
				t.Errorf("isAgentWrapper(%q) = %v, want %v", tt.base, got, tt.want)
			}
		})
	}
}

func TestParseRuntimeInfo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cmdline string
		want    string
	}{
		{
			name:    "claude with model",
			cmdline: "claude\x00--model\x00opus\x00--dangerously-skip-permissions",
			want:    "claude/opus",
		},
		{
			name:    "pi with model",
			cmdline: "pi\x00-e\x00gastown-hooks.js\x00--model\x00google-antigravity/gemini-3-flash",
			want:    "pi/google-antigravity/gemini-3-flash",
		},
		{
			name:    "cgroup-wrap then claude",
			cmdline: "cgroup-wrap\x00claude\x00--model\x00opus\x00--dangerously-skip-permissions",
			want:    "claude/opus",
		},
		{
			name:    "opencode with -m flag",
			cmdline: "opencode\x00-m\x00kimi-for-coding/kimi-k2.5",
			want:    "opencode/kimi-for-coding/kimi-k2.5",
		},
		{
			name:    "empty cmdline",
			cmdline: "",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRuntimeInfo(tt.cmdline)
			if got != tt.want {
				t.Errorf("parseRuntimeInfo(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestParseRuntimeInfo_PiBare(t *testing.T) {
	t.Parallel()
	// Bare pi (no --model flag) calls readPiDefaults() which reads
	// ~/.pi/agent/settings.json. The result is either "pi" (if no settings)
	// or "pi/<default-model>" (if settings exist). Both are valid.
	cmdline := "pi\x00-e\x00gastown-hooks.js"
	got := parseRuntimeInfo(cmdline)
	if !strings.HasPrefix(got, "pi") {
		t.Errorf("parseRuntimeInfo(pi bare) = %q, want prefix 'pi'", got)
	}
}

func TestBuildInfoFromConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rc   *config.RuntimeConfig
		want string
	}{
		{
			name: "claude with model",
			rc:   &config.RuntimeConfig{Command: "claude", Args: []string{"--model", "opus"}},
			want: "claude/opus",
		},
		{
			name: "cgroup-wrap claude",
			rc:   &config.RuntimeConfig{Command: "cgroup-wrap", Args: []string{"claude", "--model", "opus"}},
			want: "claude/opus",
		},
		{
			name: "pi bare",
			rc:   &config.RuntimeConfig{Command: "pi", Args: []string{"-e", "hooks.js"}},
			want: "pi",
		},
		{
			name: "opencode with -m",
			rc:   &config.RuntimeConfig{Command: "opencode", Args: []string{"-m", "gpt-5"}},
			want: "opencode/gpt-5",
		},
		{
			name: "empty command",
			rc:   &config.RuntimeConfig{Command: ""},
			want: "claude",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildInfoFromConfig(tt.rc)
			if got != tt.want {
				t.Errorf("buildInfoFromConfig(%s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestIsAgentCmdline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{"claude direct", "claude\x00--model\x00opus", true},
		{"pi direct", "pi\x00-e\x00hooks.js", true},
		{"node wrapper with pi", "node\x00/path/to/pi\x00-e\x00hooks.js", true},
		{"bun wrapper with opencode", "bun\x00/path/to/opencode", true},
		{"bash not agent", "bash\x00-c\x00echo hi", false},
		{"node without agent", "node\x00/path/to/server.js", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAgentCmdline(tt.cmdline)
			if got != tt.want {
				t.Errorf("isAgentCmdline(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestCountRunningAgents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status TownStatus
		want   int
	}{
		{
			name:   "empty status",
			status: TownStatus{},
			want:   0,
		},
		{
			name: "global agents only",
			status: TownStatus{
				Agents: []AgentRuntime{
					{Name: "mayor", Running: true},
					{Name: "deacon", Running: false},
				},
			},
			want: 1,
		},
		{
			name: "rig agents only",
			status: TownStatus{
				Rigs: []RigStatus{
					{
						Agents: []AgentRuntime{
							{Name: "polecat-1", Running: true},
							{Name: "witness", Running: true},
						},
					},
				},
			},
			want: 2,
		},
		{
			name: "mixed global and rig agents",
			status: TownStatus{
				Agents: []AgentRuntime{
					{Name: "mayor", Running: true},
				},
				Rigs: []RigStatus{
					{
						Agents: []AgentRuntime{
							{Name: "polecat-1", Running: true},
							{Name: "witness", Running: false},
						},
					},
					{
						Agents: []AgentRuntime{
							{Name: "polecat-2", Running: true},
						},
					},
				},
			},
			want: 3,
		},
		{
			name: "all not running",
			status: TownStatus{
				Agents: []AgentRuntime{
					{Name: "mayor", Running: false},
				},
				Rigs: []RigStatus{
					{
						Agents: []AgentRuntime{
							{Name: "polecat-1", Running: false},
						},
					},
				},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countRunningAgents(tt.status)
			if got != tt.want {
				t.Errorf(
					"countRunningAgents() = %d, want %d",
					got, tt.want,
				)
			}
		})
	}
}

func TestExtractBaseName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		cmdline string
		want    string
	}{
		{"claude\x00--model\x00opus", "claude"},
		{"/usr/bin/node\x00/path/pi", "node"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := extractBaseName(tt.cmdline)
			if got != tt.want {
				t.Errorf("extractBaseName(%q) = %q, want %q", tt.cmdline, got, tt.want)
			}
		})
	}
}

func TestFormatMCPFootprint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		names []string
		want  string
	}{
		{"empty", nil, ""},
		{"single", []string{"serena"}, "1 MCP server: serena"},
		{"multiple", []string{"builder-mcp", "serena"}, "2 MCP servers: builder-mcp, serena"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatMCPFootprint(tt.names); got != tt.want {
				t.Errorf("formatMCPFootprint(%v) = %q, want %q", tt.names, got, tt.want)
			}
		})
	}
}

func TestResolveMCPServers(t *testing.T) {
	t.Parallel()

	townRoot := t.TempDir()
	rigName := "testrig"

	// Polecat shared settings live under <rig>/polecats/.claude/settings.json.
	polecatSettings := filepath.Join(townRoot, rigName, "polecats", ".claude")
	if err := os.MkdirAll(polecatSettings, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"mcpServers":{"serena":{"command":"x"},"builder-mcp":{"command":"y"}}}`
	if err := os.WriteFile(filepath.Join(polecatSettings, "settings.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// Mayor settings live under <town>/mayor/.claude/settings.json.
	mayorSettings := filepath.Join(townRoot, "mayor", ".claude")
	if err := os.MkdirAll(mayorSettings, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mayorSettings, "settings.json"), []byte(`{"mcpServers":{"serena":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("polecat sorted names", func(t *testing.T) {
		got := resolveMCPServers(townRoot, rigName, "polecat")
		want := []string{"builder-mcp", "serena"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("resolveMCPServers polecat = %v, want %v", got, want)
		}
	})

	t.Run("mayor town-level", func(t *testing.T) {
		got := resolveMCPServers(townRoot, "", "mayor")
		if strings.Join(got, ",") != "serena" {
			t.Errorf("resolveMCPServers mayor = %v, want [serena]", got)
		}
	})

	t.Run("missing settings returns nil", func(t *testing.T) {
		if got := resolveMCPServers(townRoot, rigName, "witness"); got != nil {
			t.Errorf("resolveMCPServers witness = %v, want nil", got)
		}
	})

	t.Run("rig role without rig name returns nil", func(t *testing.T) {
		if got := resolveMCPServers(townRoot, "", "polecat"); got != nil {
			t.Errorf("resolveMCPServers polecat no-rig = %v, want nil", got)
		}
	})

	t.Run("unknown role returns nil", func(t *testing.T) {
		if got := resolveMCPServers(townRoot, rigName, "bogus"); got != nil {
			t.Errorf("resolveMCPServers bogus = %v, want nil", got)
		}
	})
}

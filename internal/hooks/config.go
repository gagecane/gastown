// Package hooks provides centralized Claude Code hook management for Gas Town.
//
// It manages a base hook configuration and per-role/per-rig overrides,
// generating .claude/settings.json files for all agents in the workspace.
package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/constants"
)

// HookEntry represents a single hook matcher with its associated hooks.
type HookEntry struct {
	Matcher string `json:"matcher"`
	Hooks   []Hook `json:"hooks"`
}

// Hook represents an individual hook command.
type Hook struct {
	Type    string `json:"type"` // "command"
	Command string `json:"command"`
}

// HooksConfig represents the hooks section of a Claude Code settings.json.
type HooksConfig struct {
	PreToolUse       []HookEntry `json:"PreToolUse,omitempty"`
	PostToolUse      []HookEntry `json:"PostToolUse,omitempty"`
	SessionStart     []HookEntry `json:"SessionStart,omitempty"`
	Stop             []HookEntry `json:"Stop,omitempty"`
	PreCompact       []HookEntry `json:"PreCompact,omitempty"`
	UserPromptSubmit []HookEntry `json:"UserPromptSubmit,omitempty"`
	WorktreeCreate   []HookEntry `json:"WorktreeCreate,omitempty"`
	WorktreeRemove   []HookEntry `json:"WorktreeRemove,omitempty"`
}

// SettingsJSON represents the full Claude Code settings.json structure.
// Unknown fields are preserved during sync via the Extra map.
type SettingsJSON struct {
	EditorMode     string          `json:"-"`
	EnabledPlugins map[string]bool `json:"-"`
	Hooks          HooksConfig     `json:"-"`
	// Extra holds all raw fields for roundtrip preservation.
	Extra map[string]json.RawMessage `json:"-"`
}

// SettingsIntegrityError indicates a malformed settings.json that should be
// treated as a fail-closed integrity violation by callers.
type SettingsIntegrityError struct {
	Path string
	Err  error
}

func (e *SettingsIntegrityError) Error() string {
	return fmt.Sprintf("settings integrity violation at %s: %v", e.Path, e.Err)
}

func (e *SettingsIntegrityError) Unwrap() error {
	return e.Err
}

// IsSettingsIntegrityError reports whether an error chain contains a
// SettingsIntegrityError.
func IsSettingsIntegrityError(err error) bool {
	var integrityErr *SettingsIntegrityError
	return errors.As(err, &integrityErr)
}

// UnmarshalSettings parses a settings.json file, preserving all fields.
func UnmarshalSettings(data []byte) (*SettingsJSON, error) {
	s := &SettingsJSON{
		Extra: make(map[string]json.RawMessage),
	}

	// Capture everything into the raw map
	if err := json.Unmarshal(data, &s.Extra); err != nil {
		return nil, err
	}

	// Extract known fields
	if raw, ok := s.Extra["editorMode"]; ok {
		if err := json.Unmarshal(raw, &s.EditorMode); err != nil {
			return nil, fmt.Errorf("unmarshaling editorMode: %w", err)
		}
	}
	if raw, ok := s.Extra["enabledPlugins"]; ok {
		if err := json.Unmarshal(raw, &s.EnabledPlugins); err != nil {
			return nil, fmt.Errorf("unmarshaling enabledPlugins: %w", err)
		}
	}
	if raw, ok := s.Extra["hooks"]; ok {
		if err := json.Unmarshal(raw, &s.Hooks); err != nil {
			return nil, fmt.Errorf("unmarshaling hooks: %w", err)
		}
	}

	return s, nil
}

// MarshalSettings serializes a SettingsJSON, preserving unknown fields.
// Does not mutate the input — works on a copy of Extra.
func MarshalSettings(s *SettingsJSON) ([]byte, error) {
	// Copy Extra to avoid mutating the input
	out := make(map[string]json.RawMessage, len(s.Extra))
	for k, v := range s.Extra {
		out[k] = v
	}
	addClaudePromptDefaults(out)

	// Write known fields back into the map, or delete if zero-valued
	if s.EditorMode != "" {
		raw, _ := json.Marshal(s.EditorMode)
		out["editorMode"] = raw
	} else {
		delete(out, "editorMode")
	}
	if s.EnabledPlugins != nil {
		raw, _ := json.Marshal(s.EnabledPlugins)
		out["enabledPlugins"] = raw
	} else {
		delete(out, "enabledPlugins")
	}

	// Always write hooks (even if empty, it's the managed section)
	raw, err := json.Marshal(s.Hooks)
	if err != nil {
		return nil, err
	}
	out["hooks"] = raw

	return json.MarshalIndent(out, "", "  ")
}

// HasClaudePromptDefaults reports whether settings already contain the Claude
// startup defaults Gas Town needs for non-interactive agent sessions.
func HasClaudePromptDefaults(s *SettingsJSON) bool {
	if s == nil {
		return false
	}
	if !rawBoolEquals(s.Extra, "skipDangerousModePermissionPrompt", true) {
		return false
	}
	if !rawBoolEquals(s.Extra, "hasCompletedOnboarding", true) {
		return false
	}
	if _, ok := s.Extra["theme"]; !ok {
		return false
	}
	permissions := map[string]json.RawMessage{}
	if raw, ok := s.Extra["permissions"]; !ok || json.Unmarshal(raw, &permissions) != nil {
		return false
	}
	return rawStringEquals(permissions, "defaultMode", "bypassPermissions")
}

func addClaudePromptDefaults(out map[string]json.RawMessage) {
	setRaw(out, "skipDangerousModePermissionPrompt", []byte(`true`))
	setRaw(out, "hasCompletedOnboarding", []byte(`true`))
	setRawDefault(out, "theme", []byte(`"dark"`))

	permissions := map[string]json.RawMessage{}
	if raw, ok := out["permissions"]; ok {
		_ = json.Unmarshal(raw, &permissions)
	}
	permissions["defaultMode"] = json.RawMessage(`"bypassPermissions"`)
	if raw, err := json.Marshal(permissions); err == nil {
		out["permissions"] = raw
	}
}

// neutralBeadsPlugin is the one plugin key the shared (upstream) code disables
// for every managed role: the beads marketplace plugin, which conflicts with
// Gas Town's own `bd`. It carries no Amazon/AIM coupling, so it stays in shared
// source. Any town-specific plugin policy (e.g. disabling Amazon AIM plugins to
// prevent the MCP-sidecar OOM documented in the 2026-06-05 post-mortem) is
// supplied out-of-band by the on-disk override layer — see ExpectedPlugins.
const neutralBeadsPlugin = "beads@beads-marketplace"

// isFleetRole returns true for roles that run as long-lived unattended daemons
// (witnesses, refineries, deacon, boot, polecats, dogs). Fleet roles get
// enableAllProjectMcpServers pinned false so no MCP auto-discovery can load
// unexpected servers; the specific plugin disable-list is town-configured via
// the override layer rather than hardcoded here.
func isFleetRole(role string) bool {
	switch role {
	case constants.RoleWitness, constants.RoleRefinery, constants.RoleDeacon, constants.RoleBoot, constants.RolePolecat, constants.RoleDog:
		return true
	}
	return false
}

// ExpectedPlugins computes the authoritative enabledPlugins map for a role by
// layering the town's on-disk plugin overrides on top of the shared neutral
// default. The neutral default disables only the beads marketplace plugin. Each
// override key applicable to the target (role, then rig/role) may supply an
// enabledPlugins map; later (more specific) keys win per-plugin.
//
// This replaces the previously hardcoded, Amazon-specific aimPluginsToDisable
// list: a town that needs to disable AIM plugins (to avoid the fleet OOM)
// supplies them via ~/.gt/hooks-overrides/<target>.json, and the shared binary
// ships no Amazon plugin names. Applies to ALL managed roles, including the
// interactive mayor/crew, so crew can be given the lean single-builder-mcp list.
func ExpectedPlugins(target string) (map[string]bool, error) {
	result := map[string]bool{neutralBeadsPlugin: false}
	for _, overrideKey := range GetApplicableOverrides(target) {
		ov, err := LoadOverridePlugins(overrideKey)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("loading plugin override %q: %w", overrideKey, err)
		}
		for plugin, enabled := range ov {
			result[plugin] = enabled
		}
	}
	return result, nil
}

// ApplyExpectedPlugins writes the resolved plugin map into the settings,
// merging into any existing enabledPlugins (override-supplied entries win).
// For fleet roles it also pins enableAllProjectMcpServers=false to block MCP
// auto-discovery. This is the write-side counterpart to ExpectedPlugins and
// replaces the role-branching EnsurePluginDefaults.
func ApplyExpectedPlugins(s *SettingsJSON, role string, expected map[string]bool) {
	if s.EnabledPlugins == nil {
		s.EnabledPlugins = make(map[string]bool)
	}
	for plugin, enabled := range expected {
		s.EnabledPlugins[plugin] = enabled
	}
	if isFleetRole(role) {
		if s.Extra == nil {
			s.Extra = make(map[string]json.RawMessage)
		}
		s.Extra["enableAllProjectMcpServers"] = json.RawMessage(`false`)
	}
}

// HasExpectedPlugins reports whether the settings already carry every entry in
// the expected plugin map (matching value), and — for fleet roles —
// enableAllProjectMcpServers=false. Extra plugin entries beyond the expected
// set do not count as drift (additive policy, matching HasPermissionDefaults).
func HasExpectedPlugins(s *SettingsJSON, role string, expected map[string]bool) bool {
	if s == nil || s.EnabledPlugins == nil {
		return false
	}
	for plugin, want := range expected {
		got, ok := s.EnabledPlugins[plugin]
		if !ok || got != want {
			return false
		}
	}
	if isFleetRole(role) {
		if !rawBoolEquals(s.Extra, "enableAllProjectMcpServers", false) {
			return false
		}
	}
	return true
}

// ExpectedMCPServers computes the authoritative mcpServers map for a target by
// layering the town's on-disk mcpServers overrides in order of specificity.
//
// Like ExpectedPlugins, this ships NO server names in shared source: the names
// builder-mcp/serena and their invocations are Amazon-/tool-specific and live
// in ~/.gt/hooks-overrides/<target>.json, supplied by the on-disk override
// layer. The shared binary therefore starts from an empty map and layers
// override-supplied servers on top (later, more specific keys win per-server).
//
// A town with no mcpServers override resolves to an empty map, which
// HasExpectedMCPServers treats as "nothing required" — so towns that do not
// configure MCP servers see no behavior change (existing mcpServers blocks are
// preserved verbatim through the settings roundtrip).
//
// This is the durable fix for gu-2nmnt: before it, the settings generator only
// managed hooks/plugins/permissions, so mcpServers (builder-mcp) was never
// emitted or restored and a `gt up --restore` that recreated a settings file
// silently dropped Claude agents' tool access.
func ExpectedMCPServers(target string) (map[string]json.RawMessage, error) {
	result := map[string]json.RawMessage{}
	for _, overrideKey := range GetApplicableOverrides(target) {
		ov, err := LoadOverrideMCPServers(overrideKey)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("loading mcpServers override %q: %w", overrideKey, err)
		}
		for name, def := range ov {
			result[name] = def
		}
	}
	return result, nil
}

// ApplyExpectedMCPServers writes the resolved mcpServers map into the settings,
// merging into any existing mcpServers block (override-supplied entries win).
// It is additive: existing servers beyond the expected set are preserved. This
// is the write-side counterpart to ExpectedMCPServers, mirroring
// ApplyExpectedPlugins. A no-op when expected is empty.
func ApplyExpectedMCPServers(s *SettingsJSON, expected map[string]json.RawMessage) {
	if len(expected) == 0 {
		return
	}
	if s.Extra == nil {
		s.Extra = make(map[string]json.RawMessage)
	}
	current := map[string]json.RawMessage{}
	if raw, ok := s.Extra["mcpServers"]; ok {
		_ = json.Unmarshal(raw, &current)
	}
	for name, def := range expected {
		current[name] = def
	}
	if raw, err := json.Marshal(current); err == nil {
		s.Extra["mcpServers"] = raw
	}
}

// HasExpectedMCPServers reports whether the settings already carry every entry
// in the expected mcpServers map (matching definition). Extra servers beyond
// the expected set do not count as drift (additive policy, matching
// HasExpectedPlugins). An empty expected map is trivially satisfied.
func HasExpectedMCPServers(s *SettingsJSON, expected map[string]json.RawMessage) bool {
	if len(expected) == 0 {
		return true
	}
	if s == nil {
		return false
	}
	current := map[string]json.RawMessage{}
	if raw, ok := s.Extra["mcpServers"]; !ok || json.Unmarshal(raw, &current) != nil {
		return false
	}
	for name, want := range expected {
		got, ok := current[name]
		if !ok || !rawJSONEqual(got, want) {
			return false
		}
	}
	return true
}

// CurrentMCPServers returns the mcpServers map currently present in the
// settings, or nil if absent/unparseable. Used by the diff command to compute
// which managed servers are missing.
func CurrentMCPServers(s *SettingsJSON) map[string]json.RawMessage {
	if s == nil {
		return nil
	}
	current := map[string]json.RawMessage{}
	if raw, ok := s.Extra["mcpServers"]; !ok || json.Unmarshal(raw, &current) != nil {
		return nil
	}
	return current
}

// taskToolDenials are the Claude Code tools every unattended Gas Town agent
// must deny. TodoWrite and the Task* family produce interactive/agent-spawning
// behavior that has no place in autonomous patrol/work loops and has caused
// agents to wedge. These are denied for ALL managed roles except the mayor,
// who is the human-facing interactive operator.
var taskToolDenials = []string{
	"TodoWrite",
	"TaskCreate",
	"TaskUpdate",
	"TaskList",
	"TaskGet",
	"TaskOutput",
	"TaskStop",
}

// requiredDenyForRole returns the permissions.deny entries that the managed
// config must guarantee for a role, in canonical order.
//
// The deny list is safety-critical: AskUserQuestion is the tool that parks an
// unattended agent at an interactive "1/2/3?" prompt and freezes its patrol
// cycle (gc-wisp-mgb9). Before gu-5gj68 the deny list lived ONLY in deployed
// settings.json files — neither base config nor overrides carried it — so
// `gt hooks sync` could not generate or restore it, and `gt hooks diff` was
// blind to its drift. Encoding it here makes it part of the source of truth.
//
// Policy (matches the pre-existing deployed configs surveyed in gu-5gj68):
//   - mayor: human-facing interactive operator — no denials.
//   - crew: semi-interactive workers — deny Task tools, but NOT AskUserQuestion
//     (crew may legitimately prompt the human).
//   - all other managed roles (witness, refinery, deacon, boot, polecat, dog):
//     fully unattended — deny AskUserQuestion AND the Task tools.
//
// RequiredDenyForRole is the exported accessor for requiredDenyForRole, used by
// the diff command to report which managed deny entries are missing.
func RequiredDenyForRole(role string) []string {
	return requiredDenyForRole(role)
}

func requiredDenyForRole(role string) []string {
	switch role {
	case constants.RoleMayor:
		return nil
	case constants.RoleCrew:
		// Task tools only; crew is allowed to ask the human.
		return append([]string(nil), taskToolDenials...)
	default:
		deny := make([]string, 0, len(taskToolDenials)+1)
		deny = append(deny, "AskUserQuestion")
		deny = append(deny, taskToolDenials...)
		return deny
	}
}

// EnsurePermissionDefaults guarantees the role's safety-critical permissions
// block (defaultMode + deny list) is present in the settings, restoring any
// drifted/dropped entries. It is additive: existing extra deny entries and
// other permissions fields (allow, ask, etc.) are preserved; only the required
// entries are guaranteed present and defaultMode is pinned to bypassPermissions.
//
// This mirrors EnsurePluginDefaults: a role-based managed field that sync
// generates and restores so it can no longer silently drift (gu-5gj68).
func EnsurePermissionDefaults(s *SettingsJSON, role string) {
	if s.Extra == nil {
		s.Extra = make(map[string]json.RawMessage)
	}

	permissions := map[string]json.RawMessage{}
	if raw, ok := s.Extra["permissions"]; ok {
		_ = json.Unmarshal(raw, &permissions)
	}

	// Pin defaultMode (same invariant addClaudePromptDefaults relies on).
	permissions["defaultMode"] = json.RawMessage(`"bypassPermissions"`)

	required := requiredDenyForRole(role)
	if len(required) > 0 {
		var deny []string
		if raw, ok := permissions["deny"]; ok {
			_ = json.Unmarshal(raw, &deny)
		}
		deny = unionPreservingOrder(required, deny)
		if rawDeny, err := json.Marshal(deny); err == nil {
			permissions["deny"] = rawDeny
		}
	}

	if raw, err := json.Marshal(permissions); err == nil {
		s.Extra["permissions"] = raw
	}
}

// HasPermissionDefaults reports whether settings already contain the correct
// permissions block for the role: defaultMode=bypassPermissions and every
// required deny entry present. Extra deny entries beyond the required set do
// not count as drift (additive policy).
func HasPermissionDefaults(s *SettingsJSON, role string) bool {
	if s == nil {
		return false
	}
	permissions := map[string]json.RawMessage{}
	if raw, ok := s.Extra["permissions"]; !ok || json.Unmarshal(raw, &permissions) != nil {
		return false
	}
	if !rawStringEquals(permissions, "defaultMode", "bypassPermissions") {
		return false
	}

	required := requiredDenyForRole(role)
	if len(required) == 0 {
		return true
	}

	var deny []string
	if raw, ok := permissions["deny"]; !ok || json.Unmarshal(raw, &deny) != nil {
		return false
	}
	present := make(map[string]bool, len(deny))
	for _, d := range deny {
		present[d] = true
	}
	for _, req := range required {
		if !present[req] {
			return false
		}
	}
	return true
}

// CurrentDenyList returns the permissions.deny entries currently present in the
// settings, or nil if absent/unparseable. Used by the diff command to compute
// which managed entries are missing.
func CurrentDenyList(s *SettingsJSON) []string {
	if s == nil {
		return nil
	}
	permissions := map[string]json.RawMessage{}
	if raw, ok := s.Extra["permissions"]; !ok || json.Unmarshal(raw, &permissions) != nil {
		return nil
	}
	var deny []string
	if raw, ok := permissions["deny"]; ok {
		_ = json.Unmarshal(raw, &deny)
	}
	return deny
}

// unionPreservingOrder returns required entries first (in their canonical
// order), followed by any existing entries not already in required. This keeps
// the managed entries deterministic for diffing while preserving operator- or
// machine-added extras.
func unionPreservingOrder(required, existing []string) []string {
	seen := make(map[string]bool, len(required)+len(existing))
	out := make([]string, 0, len(required)+len(existing))
	for _, r := range required {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	for _, e := range existing {
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}

func setRaw(out map[string]json.RawMessage, key string, value []byte) {
	out[key] = json.RawMessage(value)
}

func setRawDefault(out map[string]json.RawMessage, key string, value []byte) {
	if _, ok := out[key]; ok {
		return
	}
	out[key] = json.RawMessage(value)
}

func rawBoolEquals(raw map[string]json.RawMessage, key string, want bool) bool {
	var got bool
	if value, ok := raw[key]; !ok || json.Unmarshal(value, &got) != nil {
		return false
	}
	return got == want
}

// rawJSONEqual reports whether two raw JSON messages are semantically equal,
// ignoring key order and insignificant whitespace. Used to compare mcpServer
// definitions so a re-serialized override (different key order) does not read
// as drift.
func rawJSONEqual(a, b json.RawMessage) bool {
	var av, bv interface{}
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	ab, err1 := json.Marshal(av)
	bb, err2 := json.Marshal(bv)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(ab) == string(bb)
}

func rawStringEquals(raw map[string]json.RawMessage, key, want string) bool {
	var got string
	if value, ok := raw[key]; !ok || json.Unmarshal(value, &got) != nil {
		return false
	}
	return got == want
}

// LoadSettings reads and parses a settings.json file, preserving unknown fields.
// Returns a zero-value SettingsJSON if the file doesn't exist.
func LoadSettings(path string) (*SettingsJSON, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &SettingsJSON{}, nil
		}
		return nil, err
	}
	settings, err := UnmarshalSettings(data)
	if err != nil {
		return nil, &SettingsIntegrityError{
			Path: path,
			Err:  err,
		}
	}
	return settings, nil
}

// HooksEqual returns true if two HooksConfigs are structurally equal.
// Compares by serializing to JSON for reliable deep equality.
func HooksEqual(a, b *HooksConfig) bool {
	aj, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bj, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(aj) == string(bj)
}

// Target represents a managed settings.json location.
type Target struct {
	Path     string // Full path to .claude/settings.json or .gemini/settings.json
	Key      string // Override key: "gastown/crew", "mayor", etc.
	Rig      string // Rig name or empty for town-level
	Role     string // Informational only — does NOT participate in override resolution (Key does). Singular form matching RoleSettingsDir: crew, witness, refinery, polecat, mayor, deacon.
	Provider string // Hook provider: "claude" (default/empty) or "gemini", etc.
}

// DisplayKey returns a human-readable label for the target.
// For targets with a rig, shows "rig/role"; for town-level targets, shows the role.
func (t Target) DisplayKey() string {
	return t.Key
}

// Merge merges an override config into a base config using per-matcher merging.
// For each hook type present in the override:
//   - Same matcher: override replaces the base entry entirely
//   - Different matcher: both entries are included (base first, then override)
//   - Empty hooks list on a matcher: removes that entry (explicit disable)
//
// Hook types not present in the override are preserved from the base.
func Merge(base, override *HooksConfig) *HooksConfig {
	if base == nil {
		base = &HooksConfig{}
	}
	result := cloneConfig(base)
	return applyOverride(result, override)
}

// DefaultOverrides returns built-in role-specific hook overrides.
// On-disk overrides (in ~/.gt/hooks-overrides/) layer on top of these.
//
// Crew workers get auto-session-cycling on PreCompact: instead of compacting
// context (which degrades quality), the session is replaced with a fresh one.
// The successor picks up hooked work via SessionStart hook (gt prime --hook).
func DefaultOverrides() map[string]*HooksConfig {
	return map[string]*HooksConfig{
		// Polecats: auto-run gt done on session Stop (gas-lob, gs-lrz).
		// Catches the "idle polecat" problem: polecats that finish work but
		// forget to call gt done before the session ends. The polecat-stop-check
		// command is idempotent — it checks heartbeat state and branch commits
		// before deciding whether to run gt done.
		//
		// gt costs record is preserved here because same-matcher entries in the
		// override replace the base entirely (see mergeEntries in merge.go). Without
		// it, the polecat override silently dropped autonomous cost accounting that
		// the base Stop hook provides for every other role.
		//
		// The polecat-slack-notify hook posts a session-end Slack summary
		// (commits, bead, duration) via an optional local script. It's gated
		// on [ -x ... ] so polecats on hosts without MeshClaw installed are
		// unaffected, and trailing `|| true` ensures it never blocks exit.
		"polecats": {
			Stop: []HookEntry{
				{
					Matcher: "",
					Hooks: []Hook{
						{
							Type:    "command",
							Command: gtCommand("gt tap polecat-stop-check"),
						},
						{
							Type:    "command",
							Command: gtCommand("gt costs record &"),
						},
						{
							Type:    "command",
							Command: "[ -x /home/canewiw/.meshclaw/bin/polecat-slack-notify.sh ] && /home/canewiw/.meshclaw/bin/polecat-slack-notify.sh || true",
						},
					},
				},
			},
		},
		// Crew workers: auto-cycle session on context compaction (gt-op78).
		// Instead of compacting (lossy), replace with fresh session that
		// inherits hooked work. The --cycle flag does: collect state →
		// send handoff mail → respawn pane with fresh Claude instance.
		"crew": {
			PreCompact: []HookEntry{
				{
					Matcher: "",
					Hooks: []Hook{
						{
							Type:    "command",
							Command: gtCommand("gt handoff --cycle --reason compaction"),
						},
					},
				},
			},
		},
		// Witness roles: patrol-formula-guard (gt-e47hxn).
		// Blocks patrol formulas from using persistent molecules — must use wisps.
		// Without this, witnesses could accidentally create permanent patrol molecules
		// that survive session restarts and accumulate unbounded.
		"witness": {
			UserPromptSubmit: []HookEntry{{Matcher: ""}},
			PreToolUse: []HookEntry{
				{
					Matcher: "Bash(*bd mol pour*patrol*)",
					Hooks: []Hook{{
						Type:    "command",
						Command: "echo '❌ BLOCKED: Patrol formulas must use wisps, not persistent molecules.' && echo 'Use: bd mol wisp mol-*-patrol' && echo 'Not:  bd mol pour mol-*-patrol' && exit 2",
					}},
				},
				{
					Matcher: "Bash(*bd mol pour *mol-witness*)",
					Hooks: []Hook{{
						Type:    "command",
						Command: "echo '❌ BLOCKED: Patrol formulas must use wisps, not persistent molecules.' && echo 'Use: bd mol wisp mol-*-patrol' && echo 'Not:  bd mol pour mol-*-patrol' && exit 2",
					}},
				},
				{
					Matcher: "Bash(*bd mol pour *mol-deacon*)",
					Hooks: []Hook{{
						Type:    "command",
						Command: "echo '❌ BLOCKED: Patrol formulas must use wisps, not persistent molecules.' && echo 'Use: bd mol wisp mol-*-patrol' && echo 'Not:  bd mol pour mol-*-patrol' && exit 2",
					}},
				},
				{
					Matcher: "Bash(*bd mol pour *mol-refinery*)",
					Hooks: []Hook{{
						Type:    "command",
						Command: "echo '❌ BLOCKED: Patrol formulas must use wisps, not persistent molecules.' && echo 'Use: bd mol wisp mol-*-patrol' && echo 'Not:  bd mol pour mol-*-patrol' && exit 2",
					}},
				},
			},
		},
		"boot": {
			UserPromptSubmit: []HookEntry{{Matcher: ""}},
		},
		// Deacon roles: patrol-formula-guard (same as witness).
		// Deacons also run patrols and must use wisps, not persistent molecules.
		"deacon": {
			UserPromptSubmit: []HookEntry{{Matcher: ""}},
			PreToolUse: []HookEntry{
				{
					Matcher: "Bash(*for *seq*)",
					Hooks: []Hook{{
						Type:    "command",
						Command: "echo '❌ BLOCKED: Deacon must not batch patrol cycles with for/seq loops.' && echo 'Run one patrol cycle, then use gt patrol report or gt handoff.' && exit 2",
					}},
				},
				{
					Matcher: "Bash(*while true*)",
					Hooks: []Hook{{
						Type:    "command",
						Command: "echo '❌ BLOCKED: Deacon must not run open-ended patrol loops.' && echo 'Run one patrol cycle, then use gt patrol report or gt handoff.' && exit 2",
					}},
				},
				{
					Matcher: "Bash(*while :*)",
					Hooks: []Hook{{
						Type:    "command",
						Command: "echo '❌ BLOCKED: Deacon must not run open-ended patrol loops.' && echo 'Run one patrol cycle, then use gt patrol report or gt handoff.' && exit 2",
					}},
				},
				{
					Matcher: "Bash(*bd mol pour*patrol*)",
					Hooks: []Hook{{
						Type:    "command",
						Command: "echo '❌ BLOCKED: Patrol formulas must use wisps, not persistent molecules.' && echo 'Use: bd mol wisp mol-*-patrol' && echo 'Not:  bd mol pour mol-*-patrol' && exit 2",
					}},
				},
				{
					Matcher: "Bash(*bd mol pour *mol-witness*)",
					Hooks: []Hook{{
						Type:    "command",
						Command: "echo '❌ BLOCKED: Patrol formulas must use wisps, not persistent molecules.' && echo 'Use: bd mol wisp mol-*-patrol' && echo 'Not:  bd mol pour mol-*-patrol' && exit 2",
					}},
				},
				{
					Matcher: "Bash(*bd mol pour *mol-deacon*)",
					Hooks: []Hook{{
						Type:    "command",
						Command: "echo '❌ BLOCKED: Patrol formulas must use wisps, not persistent molecules.' && echo 'Use: bd mol wisp mol-*-patrol' && echo 'Not:  bd mol pour mol-*-patrol' && exit 2",
					}},
				},
				{
					Matcher: "Bash(*bd mol pour *mol-refinery*)",
					Hooks: []Hook{{
						Type:    "command",
						Command: "echo '❌ BLOCKED: Patrol formulas must use wisps, not persistent molecules.' && echo 'Use: bd mol wisp mol-*-patrol' && echo 'Not:  bd mol pour mol-*-patrol' && exit 2",
					}},
				},
			},
		},
		// Refinery roles: patrol-formula-guard (same as witness).
		// Refineries also run patrols and must use wisps, not persistent molecules.
		"refinery": {
			UserPromptSubmit: []HookEntry{{Matcher: ""}},
			PreToolUse: []HookEntry{
				{
					Matcher: "Bash(*bd mol pour*patrol*)",
					Hooks: []Hook{{
						Type:    "command",
						Command: "echo '❌ BLOCKED: Patrol formulas must use wisps, not persistent molecules.' && echo 'Use: bd mol wisp mol-*-patrol' && echo 'Not:  bd mol pour mol-*-patrol' && exit 2",
					}},
				},
				{
					Matcher: "Bash(*bd mol pour *mol-witness*)",
					Hooks: []Hook{{
						Type:    "command",
						Command: "echo '❌ BLOCKED: Patrol formulas must use wisps, not persistent molecules.' && echo 'Use: bd mol wisp mol-*-patrol' && echo 'Not:  bd mol pour mol-*-patrol' && exit 2",
					}},
				},
				{
					Matcher: "Bash(*bd mol pour *mol-deacon*)",
					Hooks: []Hook{{
						Type:    "command",
						Command: "echo '❌ BLOCKED: Patrol formulas must use wisps, not persistent molecules.' && echo 'Use: bd mol wisp mol-*-patrol' && echo 'Not:  bd mol pour mol-*-patrol' && exit 2",
					}},
				},
				{
					Matcher: "Bash(*bd mol pour *mol-refinery*)",
					Hooks: []Hook{{
						Type:    "command",
						Command: "echo '❌ BLOCKED: Patrol formulas must use wisps, not persistent molecules.' && echo 'Use: bd mol wisp mol-*-patrol' && echo 'Not:  bd mol pour mol-*-patrol' && exit 2",
					}},
				},
			},
		},
	}
}

// ComputeExpected computes the expected HooksConfig for a target by loading
// the base config and applying all applicable overrides in order of specificity.
// If no base config exists, uses DefaultBase().
//
// When an on-disk base exists, DefaultBase() is merged underneath it so that
// new hook types (e.g., SessionStart added after the base was created) are
// automatically backfilled. User customizations in the on-disk base take
// precedence. Hook types absent from the on-disk base inherit from DefaultBase.
//
// For each override key, built-in defaults (from DefaultOverrides)
// are merged first, then on-disk overrides layer on top. On-disk overrides can
// replace or extend base hooks by providing matching PreToolUse entries.
func ComputeExpected(target string) (*HooksConfig, error) {
	base, err := LoadBase()
	if err != nil {
		if os.IsNotExist(err) {
			base = DefaultBase()
		} else {
			return nil, fmt.Errorf("loading base config: %w", err)
		}
	} else {
		// Backfill: merge DefaultBase as floor, then on-disk base on top.
		// This ensures new hook types added to DefaultBase are always present,
		// while preserving user customizations from the on-disk base.
		base = Merge(DefaultBase(), base)
	}

	defaults := DefaultOverrides()
	result := base
	for _, overrideKey := range GetApplicableOverrides(target) {
		// Always apply built-in defaults first
		if def, ok := defaults[overrideKey]; ok {
			result = Merge(result, def)
		}

		// Then layer on-disk overrides on top
		override, err := LoadOverride(overrideKey)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("loading override %q: %w", overrideKey, err)
		}
		result = Merge(result, override)
	}

	return result, nil
}

// DiscoverTargets finds all managed .claude/settings.json locations in the workspace.
// Settings are installed in gastown-managed parent directories and passed to Claude Code
// via --settings flag. Crew members in a rig share one settings file, as do polecats.
// Returns Target structs with path, override key, rig, and role information.
func DiscoverTargets(townRoot string) ([]Target, error) {
	var targets []Target

	// Town-level targets (mayor/deacon cwd IS the settings dir)
	targets = append(targets, Target{
		Path: filepath.Join(townRoot, "mayor", ".claude", "settings.json"),
		Key:  "mayor",
		Role: "mayor",
	})
	targets = append(targets, Target{
		Path: filepath.Join(townRoot, "deacon", ".claude", "settings.json"),
		Key:  "deacon",
		Role: "deacon",
	})

	// Dogs — town-level single-instance Claude agents in deacon/dogs/<name>/.
	// Each dog's directory IS its working directory and settings dir. They are
	// unattended fleet daemons, so they must be managed targets (plugin-disabled)
	// to avoid inheriting the user's global AIM plugin config and OOMing the host
	// (see 2026-06-05 OOM post-mortem). Boot is a well-known dog and keeps its
	// dedicated role; all other dogs use Role "dog". Only added when the dogs
	// directory exists (gitignored and optional). Hidden entries are skipped.
	dogsDir := filepath.Join(townRoot, "deacon", "dogs")
	if entries, err := os.ReadDir(dogsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			role := constants.RoleDog
			key := "dog"
			if entry.Name() == "boot" {
				role = constants.RoleBoot
				key = "boot"
			}
			targets = append(targets, Target{
				Path: filepath.Join(dogsDir, entry.Name(), ".claude", "settings.json"),
				Key:  key,
				Role: role,
			})
		}
	}

	// Scan rigs
	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "mayor" || entry.Name() == "deacon" ||
			entry.Name() == ".beads" || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		rigName := entry.Name()
		rigPath := filepath.Join(townRoot, rigName)

		// Skip directories that aren't rigs (no crew/ or witness/ or polecats/ subdirs)
		if !isRig(rigPath) {
			continue
		}

		// Crew — one shared settings file in the crew parent directory.
		// All crew members share this via --settings flag.
		crewDir := filepath.Join(rigPath, "crew")
		if info, err := os.Stat(crewDir); err == nil && info.IsDir() {
			targets = append(targets, Target{
				Path: filepath.Join(crewDir, ".claude", "settings.json"),
				Key:  rigName + "/crew",
				Rig:  rigName,
				Role: "crew",
			})
		}

		// Polecats — one shared settings file in the polecats parent directory.
		// All polecats share this via --settings flag.
		polecatsDir := filepath.Join(rigPath, "polecats")
		if info, err := os.Stat(polecatsDir); err == nil && info.IsDir() {
			targets = append(targets, Target{
				Path: filepath.Join(polecatsDir, ".claude", "settings.json"),
				Key:  rigName + "/polecats",
				Rig:  rigName,
				Role: "polecat",
			})
		}

		// Witness — settings in the witness parent directory
		witnessDir := filepath.Join(rigPath, "witness")
		if info, err := os.Stat(witnessDir); err == nil && info.IsDir() {
			targets = append(targets, Target{
				Path: filepath.Join(witnessDir, ".claude", "settings.json"),
				Key:  rigName + "/witness",
				Rig:  rigName,
				Role: "witness",
			})
		}

		// Refinery — settings in the refinery parent directory
		refineryDir := filepath.Join(rigPath, "refinery")
		if info, err := os.Stat(refineryDir); err == nil && info.IsDir() {
			targets = append(targets, Target{
				Path: filepath.Join(refineryDir, ".claude", "settings.json"),
				Key:  rigName + "/refinery",
				Rig:  rigName,
				Role: "refinery",
			})
		}

		// Per-rig mayor — <rig>/mayor/rig is the canonical rig checkout
		// that crew/polecat worktrees are created from. Without this,
		// its .claude/settings.json drifts indefinitely because gt hooks
		// sync never reaches it. Distinguished from the town-level mayor
		// (Key="mayor") by the "<rig>/mayor" key. See gu-jq0q audit.
		mayorRigDir := constants.RigMayorPath(rigPath)
		if info, err := os.Stat(mayorRigDir); err == nil && info.IsDir() {
			targets = append(targets, Target{
				Path: filepath.Join(mayorRigDir, ".claude", "settings.json"),
				Key:  rigName + "/mayor",
				Rig:  rigName,
				Role: "mayor",
			})
		}

	}

	return targets, nil
}

// RoleLocation represents a discovered role directory in the workspace,
// independent of any specific agent. Used by callers that need to resolve
// agent configuration for each location (e.g., syncing non-Claude agents).
type RoleLocation struct {
	Dir  string // Absolute path to the role's parent directory (e.g., .../rig/crew)
	Rig  string // Rig name, or empty for town-level roles
	Role string // Role name: crew, polecat, witness, refinery, mayor, deacon, dog
}

// DiscoverRoleLocations finds all role directories in a workspace.
// Unlike DiscoverTargets (which returns Claude-specific paths), this returns
// agent-agnostic directory locations that callers can use with any agent config.
func DiscoverRoleLocations(townRoot string) ([]RoleLocation, error) {
	var locations []RoleLocation

	// Town-level roles
	for _, role := range []string{"mayor", "deacon"} {
		dir := filepath.Join(townRoot, role)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			locations = append(locations, RoleLocation{Dir: dir, Role: role})
		}
	}

	// Dogs: town-level workers nested under deacon/dogs/. Each dog is a
	// single-instance daemon whose directory IS the working directory —
	// analogous to witness/refinery, but scoped to the town rather than a rig.
	// Without this discovery, per-dog .kiro/agents/gastown.json (and other
	// agent configs) drift indefinitely because gt hooks sync never reaches
	// them, and InstallForRole only writes on first creation.
	//
	// Subdirectories under a dog (e.g., deacon/dogs/alpha/gastown_upstream/)
	// are project worktrees for separate repos and receive their own agent
	// configs via rig-level sync — we must NOT descend into them here.
	dogsDir := filepath.Join(townRoot, "deacon", "dogs")
	if entries, err := os.ReadDir(dogsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			dogDir := filepath.Join(dogsDir, entry.Name())
			locations = append(locations, RoleLocation{Dir: dogDir, Role: "dog"})
		}
	}

	// Scan rigs
	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "mayor" || entry.Name() == "deacon" ||
			entry.Name() == ".beads" || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		rigName := entry.Name()
		rigPath := filepath.Join(townRoot, rigName)

		if !isRig(rigPath) {
			continue
		}

		// Map subdirectories to roles
		for _, sub := range []struct{ dir, role string }{
			{"crew", "crew"},
			{"polecats", "polecat"},
			{"witness", "witness"},
			{"refinery", "refinery"},
		} {
			dir := filepath.Join(rigPath, sub.dir)
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				locations = append(locations, RoleLocation{Dir: dir, Rig: rigName, Role: sub.role})
			}
		}

		// Per-rig mayor — <rig>/mayor/rig is the canonical rig checkout,
		// paired with DiscoverTargets for Claude-specific settings. Without
		// enumeration here, gt hooks sync never reaches
		// <rig>/mayor/rig/.kiro/agents/gastown.json and related non-Claude
		// agent configs, so they drift indefinitely after InstallForRole
		// writes them on first creation.
		//
		// Note: town-level mayor already produces
		// RoleLocation{Dir: <townRoot>/mayor, Rig: "", Role: "mayor"} above.
		// Per-rig mayor has Rig=<rigName>, so callers that branch on Rig
		// empty-vs-set continue to distinguish the two cases.
		mayorRigDir := constants.RigMayorPath(rigPath)
		if info, err := os.Stat(mayorRigDir); err == nil && info.IsDir() {
			locations = append(locations, RoleLocation{
				Dir:  mayorRigDir,
				Rig:  rigName,
				Role: "mayor",
			})
		}
	}

	return locations, nil
}

// DiscoverWorktrees returns subdirectories within a role parent directory that
// are individual worktrees (e.g., crew/alice, crew/bob).
// Skips hidden directories and non-directories.
//
// NOTE: For polecats, use DiscoverPolecatWorktrees instead. Polecat worktrees
// are nested one level deeper (polecats/<name>/<rigName>/), so this function
// returns the polecat *state dir*, not the actual git worktree.
//
// For non-polecat roles, some (e.g., crew with a nested repo clone) keep the
// git worktree one level below the agent slot directory. When an immediate
// child contains nested git worktree roots, prefer those nested directories so
// hooks are synced into the real repo root instead of the slot parent.
func DiscoverWorktrees(roleDir string) []string {
	entries, err := os.ReadDir(roleDir)
	if err != nil {
		return nil
	}

	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		path := filepath.Join(roleDir, entry.Name())
		nested := nestedWorktreeRoots(path)
		if len(nested) > 0 {
			dirs = append(dirs, nested...)
			continue
		}

		dirs = append(dirs, path)
	}
	return dirs
}

// DiscoverPolecatWorktrees returns the actual git worktree directories for
// every polecat under a polecats/ parent directory.
//
// Polecats have a two-level layout:
//
//	polecats/<name>/             ← state dir (mail, .runtime, etc.)
//	polecats/<name>/<rigName>/   ← the git worktree (where agents run)
//
// For each polecat state dir, this function returns the single non-hidden
// subdirectory that contains a `.git` entry (file for worktrees, dir for
// primary clones). Polecats without a discoverable worktree are skipped
// rather than returning the state dir, because writing hook files to the
// state dir is invisible to agent sessions (the original bug).
//
// Callers that need the state dir (e.g., for mail) should use DiscoverWorktrees.
func DiscoverPolecatWorktrees(polecatsDir string) []string {
	entries, err := os.ReadDir(polecatsDir)
	if err != nil {
		return nil
	}

	var worktrees []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		stateDir := filepath.Join(polecatsDir, entry.Name())
		if wt := findNestedWorktree(stateDir); wt != "" {
			worktrees = append(worktrees, wt)
		}
	}
	return worktrees
}

// findNestedWorktree looks inside a polecat state dir for its git worktree.
// Returns the path to the worktree subdirectory, or "" if none is found.
// A worktree is identified by the presence of a `.git` entry (either a file,
// as produced by `git worktree add`, or a directory for primary clones).
func findNestedWorktree(stateDir string) string {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return ""
	}

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		candidate := filepath.Join(stateDir, entry.Name())
		if _, err := os.Stat(filepath.Join(candidate, ".git")); err == nil {
			return candidate
		}
	}
	return ""
}

// nestedWorktreeRoots returns any immediate child directories of parent that
// are themselves git worktree roots (have a `.git` file or directory). Used
// by DiscoverWorktrees to transparently descend into nested worktrees for
// non-polecat roles.
func nestedWorktreeRoots(parent string) []string {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}

	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		path := filepath.Join(parent, entry.Name())
		if isGitWorktreeRoot(path) {
			dirs = append(dirs, path)
		}
	}

	return dirs
}

func isGitWorktreeRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// isRig checks if a directory looks like a rig (has crew/, witness/, or polecats/ subdirectory).
func isRig(path string) bool {
	for _, sub := range []string{"crew", "witness", "polecats", "refinery"} {
		info, err := os.Stat(filepath.Join(path, sub))
		if err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// EventTypes returns the known hook event type names in display order.
var EventTypes = []string{"PreToolUse", "PostToolUse", "SessionStart", "Stop", "PreCompact", "UserPromptSubmit", "WorktreeCreate", "WorktreeRemove"}

// GetEntries returns the hook entries for a given event type.
func (c *HooksConfig) GetEntries(eventType string) []HookEntry {
	switch eventType {
	case "PreToolUse":
		return c.PreToolUse
	case "PostToolUse":
		return c.PostToolUse
	case "SessionStart":
		return c.SessionStart
	case "Stop":
		return c.Stop
	case "PreCompact":
		return c.PreCompact
	case "UserPromptSubmit":
		return c.UserPromptSubmit
	case "WorktreeCreate":
		return c.WorktreeCreate
	case "WorktreeRemove":
		return c.WorktreeRemove
	default:
		return nil
	}
}

// SetEntries sets the hook entries for a given event type.
func (c *HooksConfig) SetEntries(eventType string, entries []HookEntry) {
	switch eventType {
	case "PreToolUse":
		c.PreToolUse = entries
	case "PostToolUse":
		c.PostToolUse = entries
	case "SessionStart":
		c.SessionStart = entries
	case "Stop":
		c.Stop = entries
	case "PreCompact":
		c.PreCompact = entries
	case "UserPromptSubmit":
		c.UserPromptSubmit = entries
	case "WorktreeCreate":
		c.WorktreeCreate = entries
	case "WorktreeRemove":
		c.WorktreeRemove = entries
	}
}

// ToMap converts HooksConfig to a map for iteration over non-empty event types.
func (c *HooksConfig) ToMap() map[string][]HookEntry {
	m := make(map[string][]HookEntry)
	for _, et := range EventTypes {
		entries := c.GetEntries(et)
		if len(entries) > 0 {
			m[et] = entries
		}
	}
	return m
}

// AddEntry appends a hook entry to the given event type if the matcher doesn't already exist.
// Returns true if the entry was added.
func (c *HooksConfig) AddEntry(eventType string, entry HookEntry) bool {
	entries := c.GetEntries(eventType)
	for _, e := range entries {
		if e.Matcher == entry.Matcher {
			return false
		}
	}
	c.SetEntries(eventType, append(entries, entry))
	return true
}

// gtPrimaryDir returns the highest-priority .gt config directory.
// If GT_HOME is set, returns $GT_HOME/.gt; otherwise returns ~/.gt.
// This is the target for all write operations and the first location checked
// during cascaded reads.
func gtPrimaryDir() string {
	if h := os.Getenv("GT_HOME"); h != "" {
		return filepath.Join(h, ".gt")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".gt")
	}
	return filepath.Join(home, ".gt")
}

// gtConfigDirs returns the ordered list of directories to search for hook
// configs, from highest to lowest priority:
//
//  1. $GT_HOME/.gt  (only when GT_HOME is set and differs from $HOME)
//  2. ~/.gt
//
// The binary's built-in defaults act as the implicit final fallback and are
// NOT represented here — callers handle them separately.
func gtConfigDirs() []string {
	primary := gtPrimaryDir()
	dirs := []string{primary}

	// Add ~/.gt as a lower-priority fallback only when GT_HOME redirects
	// the primary dir away from the user's home directory.
	if os.Getenv("GT_HOME") != "" {
		home, err := os.UserHomeDir()
		if err == nil {
			fallback := filepath.Join(home, ".gt")
			if fallback != primary {
				dirs = append(dirs, fallback)
			}
		}
	}
	return dirs
}

// BasePath returns the path to the base hooks config file in the primary dir.
func BasePath() string {
	return filepath.Join(gtPrimaryDir(), "hooks-base.json")
}

// OverridePath returns the path to the override config for a given target in
// the primary dir.
func OverridePath(target string) string {
	// Replace "/" with "__" for filesystem safety (e.g., "gastown/crew" -> "gastown__crew")
	safe := strings.ReplaceAll(target, "/", "__")
	return filepath.Join(gtPrimaryDir(), "hooks-overrides", safe+".json")
}

// OverridesDir returns the path to the overrides directory in the primary dir.
func OverridesDir() string {
	return filepath.Join(gtPrimaryDir(), "hooks-overrides")
}

// LoadBase loads the base hooks configuration using cascading directory search.
// Directories are tried in priority order (gtConfigDirs): the first file found
// wins. Returns os.ErrNotExist if no file exists in any location; callers
// should fall back to DefaultBase() in that case.
func LoadBase() (*HooksConfig, error) {
	for _, dir := range gtConfigDirs() {
		cfg, err := loadConfig(filepath.Join(dir, "hooks-base.json"))
		if err == nil {
			return cfg, nil
		}
		if !os.IsNotExist(err) {
			return nil, err // Parse error — surface it immediately.
		}
	}
	return nil, os.ErrNotExist
}

// LoadOverride loads an override configuration for the given target using
// cascading directory search. The first file found across gtConfigDirs wins.
// Returns os.ErrNotExist if no override exists in any location.
func LoadOverride(target string) (*HooksConfig, error) {
	safe := strings.ReplaceAll(target, "/", "__")
	for _, dir := range gtConfigDirs() {
		cfg, err := loadConfig(filepath.Join(dir, "hooks-overrides", safe+".json"))
		if err == nil {
			return cfg, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return nil, os.ErrNotExist
}

// LoadOverridePlugins loads the enabledPlugins map carried by an override file
// for the given target, using the same cascading directory search as
// LoadOverride. The map lives under a top-level "enabledPlugins" key in the
// same ~/.gt/hooks-overrides/<target>.json file as the hook overrides; the
// hooks loader (loadConfig) ignores it, and this reader ignores the hooks. The
// first file found across gtConfigDirs wins. Returns os.ErrNotExist if no
// override file exists in any location; a file that exists but carries no
// enabledPlugins key yields an empty (non-nil) map and no error.
func LoadOverridePlugins(target string) (map[string]bool, error) {
	safe := strings.ReplaceAll(target, "/", "__")
	for _, dir := range gtConfigDirs() {
		path := filepath.Join(dir, "hooks-overrides", safe+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		var wrapper struct {
			EnabledPlugins map[string]bool `json:"enabledPlugins"`
		}
		if err := json.Unmarshal(data, &wrapper); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		if wrapper.EnabledPlugins == nil {
			return map[string]bool{}, nil
		}
		return wrapper.EnabledPlugins, nil
	}
	return nil, os.ErrNotExist
}

// LoadOverrideMCPServers loads the mcpServers map carried by an override file
// for the given target, using the same cascading directory search as
// LoadOverride/LoadOverridePlugins. The map lives under a top-level
// "mcpServers" key in the same ~/.gt/hooks-overrides/<target>.json file as the
// hook overrides; the hooks loader (loadConfig) and the plugin reader both
// ignore it, and this reader ignores the hooks and plugins. The first file
// found across gtConfigDirs wins. Returns os.ErrNotExist if no override file
// exists in any location; a file that exists but carries no mcpServers key
// yields an empty (non-nil) map and no error.
//
// Each value is preserved as a raw JSON message so server definitions
// (command, args, env, type, …) roundtrip verbatim — the shared binary never
// needs to understand their schema.
func LoadOverrideMCPServers(target string) (map[string]json.RawMessage, error) {
	safe := strings.ReplaceAll(target, "/", "__")
	for _, dir := range gtConfigDirs() {
		path := filepath.Join(dir, "hooks-overrides", safe+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		var wrapper struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if err := json.Unmarshal(data, &wrapper); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		if wrapper.MCPServers == nil {
			return map[string]json.RawMessage{}, nil
		}
		return wrapper.MCPServers, nil
	}
	return nil, os.ErrNotExist
}

// SaveBase writes the base hooks configuration to the primary .gt directory
// ($GT_HOME/.gt if set, otherwise ~/.gt).
func SaveBase(cfg *HooksConfig) error {
	return saveConfig(BasePath(), cfg)
}

// SaveOverride writes an override configuration for the given target to the
// primary .gt directory.
func SaveOverride(target string, cfg *HooksConfig) error {
	return saveConfig(OverridePath(target), cfg)
}

// MarshalConfig serializes a HooksConfig to pretty-printed JSON.
func MarshalConfig(cfg *HooksConfig) ([]byte, error) {
	return json.MarshalIndent(cfg, "", "  ")
}

// NormalizeTarget normalizes a target string, mapping singular role aliases
// to their canonical forms (e.g., "polecat" → "polecats", "rig/polecat" → "rig/polecats").
// Returns the normalized target and true if valid, or ("", false) if invalid.
func NormalizeTarget(target string) (string, bool) {
	// Alias map: singular → canonical
	aliases := map[string]string{
		"polecat": "polecats",
	}

	validRoles := map[string]bool{
		"crew": true, "witness": true, "refinery": true,
		"polecats": true, "mayor": true, "deacon": true,
	}

	// Simple role target
	if validRoles[target] {
		return target, true
	}
	if canonical, ok := aliases[target]; ok {
		return canonical, true
	}

	// Rig/role target (e.g., "gastown/crew")
	parts := strings.SplitN(target, "/", 2)
	if len(parts) == 2 && parts[0] != "" {
		role := parts[1]
		if validRoles[role] {
			return target, true
		}
		if canonical, ok := aliases[role]; ok {
			return parts[0] + "/" + canonical, true
		}
	}

	return "", false
}

// ValidTarget returns true if the target string is a valid override target.
// Valid targets are roles (crew, witness, etc.) or rig/role combinations.
// Accepts singular aliases (e.g., "polecat") — use NormalizeTarget to get canonical form.
func ValidTarget(target string) bool {
	_, ok := NormalizeTarget(target)
	return ok
}

// DefaultBase returns a sensible default base configuration.
// This includes resolved gt hook commands that all agents need.
func DefaultBase() *HooksConfig {
	return &HooksConfig{
		PreToolUse: []HookEntry{
			{
				Matcher: "Bash(gh pr create*)",
				Hooks: []Hook{{
					Type:    "command",
					Command: gtCommand("gt tap guard pr-workflow"),
				}},
			},
			{
				Matcher: "Bash(git checkout -b*)",
				Hooks: []Hook{{
					Type:    "command",
					Command: gtCommand("gt tap guard pr-workflow"),
				}},
			},
			{
				Matcher: "Bash(git switch -c*)",
				Hooks: []Hook{{
					Type:    "command",
					Command: gtCommand("gt tap guard pr-workflow"),
				}},
			},
			{
				Matcher: "Bash(rm -rf /*)",
				Hooks: []Hook{{
					Type:    "command",
					Command: gtCommand("gt tap guard dangerous-command"),
				}},
			},
			{
				Matcher: "Bash(git push --force*)",
				Hooks: []Hook{{
					Type:    "command",
					Command: gtCommand("gt tap guard dangerous-command"),
				}},
			},
			{
				Matcher: "Bash(git push -f*)",
				Hooks: []Hook{{
					Type:    "command",
					Command: gtCommand("gt tap guard dangerous-command"),
				}},
			},
		},
		SessionStart: []HookEntry{
			{
				Matcher: "",
				Hooks: []Hook{
					{
						Type:    "command",
						Command: gtCommand("gt prime --hook"),
					},
				},
			},
		},
		PreCompact: []HookEntry{
			{
				Matcher: "",
				Hooks: []Hook{
					{
						Type:    "command",
						Command: gtCommand("gt prime --hook"),
					},
				},
			},
		},
		UserPromptSubmit: []HookEntry{
			{
				Matcher: "",
				Hooks: []Hook{
					{
						Type:    "command",
						Command: gtCommand("gt mail check --inject"),
					},
				},
			},
		},
		Stop: []HookEntry{
			{
				Matcher: "",
				Hooks: []Hook{
					{
						Type:    "command",
						Command: gtCommand("gt costs record &"),
					},
				},
			},
		},
	}
}

// OverrideKeyForRole maps a singular role constant to its role-level override
// key, matching the keys used by DiscoverTargets and the hooks override files.
// The polecat role keys off "polecats" (plural, the override-file convention);
// boot and dog key off their own DiscoverTargets keys so a town can ship
// boot.json / dog.json overrides. An unknown role yields the empty key, which
// ExpectedPlugins/ExpectedMCPServers treat as "neutral default only".
//
// This is the canonical role→override-key mapping shared by the provisioning
// path (InstallForRole) and the cmd-layer install path, so both resolve the
// same host-local override files.
func OverrideKeyForRole(role string) string {
	switch role {
	case constants.RolePolecat:
		return "polecats"
	case constants.RoleBoot:
		return "boot"
	case constants.RoleDog:
		return "dog"
	case constants.RoleWitness, constants.RoleRefinery, constants.RoleCrew,
		constants.RoleMayor, constants.RoleDeacon:
		return role
	}
	return ""
}

// GetApplicableOverrides returns the override keys in order of specificity
// for a given target. More specific overrides are applied later (and win).
//
// Examples:
//
//	"gastown/crew" -> ["crew", "gastown/crew"]
//	"mayor"        -> ["mayor"]
//	"beads/witness" -> ["witness", "beads/witness"]
func GetApplicableOverrides(target string) []string {
	parts := strings.SplitN(target, "/", 2)
	if len(parts) == 2 {
		// Rig/role target: apply role override first, then rig+role
		return []string{parts[1], target}
	}
	// Simple role target
	return []string{target}
}

// loadConfig loads a HooksConfig from a JSON file.
func loadConfig(path string) (*HooksConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg HooksConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := validateUniqueMatchers(&cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	return &cfg, nil
}

func validateUniqueMatchers(cfg *HooksConfig) error {
	for _, eventType := range EventTypes {
		seen := make(map[string]struct{})
		for _, entry := range cfg.GetEntries(eventType) {
			if _, exists := seen[entry.Matcher]; exists {
				return fmt.Errorf("duplicate matcher %q in %s", entry.Matcher, eventType)
			}
			seen[entry.Matcher] = struct{}{}
		}
	}
	return nil
}

// saveConfig writes a HooksConfig to a JSON file, creating directories as needed.
func saveConfig(path string, cfg *HooksConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	// Add trailing newline for human editing
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	return nil
}

func gtCommand(command string) string {
	if command == "gt" {
		return resolveGTBinary()
	}
	if strings.HasPrefix(command, "gt ") {
		return resolveGTBinary() + command[len("gt"):]
	}
	return command
}

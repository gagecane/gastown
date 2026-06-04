// Package hooks provides a generic hook/settings installer for all agent runtimes.
//
// Instead of per-agent packages (claude/, gemini/, cursor/, etc.) each containing
// near-identical boilerplate, this package embeds all agent templates and provides
// a single generic installer that reads template metadata from AgentPresetInfo.
package hooks

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/hookutil"
)

// askUserQuestionTool is the Claude tool that renders a blocking selection menu.
// Denying it prevents an unattended agent from parking on a prompt no human
// will answer (gs-wbj footgun).
const askUserQuestionTool = "AskUserQuestion"

// roleMustDenyAskUserQuestion reports whether a role's Claude settings must deny
// AskUserQuestion. This is the autonomous roles (headless by definition) PLUS
// the mayor: the mayor is classified interactive (it uses the interactive
// template and gets mail-injection), but it runs UNATTENDED for long stretches
// and has hung on stray AskUserQuestion prompts no operator was there to answer
// (gu-2qvnx). Denying the tool for mayor closes that footgun while leaving its
// interactive classification — and everything IsAutonomousRole drives — intact.
//
// Crew is deliberately excluded: it is human-attended and legitimately uses
// AskUserQuestion.
func roleMustDenyAskUserQuestion(role string) bool {
	return hookutil.IsAutonomousRole(role) || role == constants.RoleMayor
}

//go:embed templates/*
var templateFS embed.FS

// InstallForRole provisions hook/settings files for an agent based on its preset config.
// It creates the file if it does not exist, or overwrites if the existing file contains
// known stale patterns (e.g., legacy "export PATH=" format). Otherwise it does not
// overwrite — this is the safe path for session startup, where Claude's settings.json
// may have been customized by syncTarget (base + role overrides merge) and must not
// be clobbered.
//
// For explicit sync operations that should update stale files, use SyncForRole.
//
// Parameters:
//   - provider: the preset's HooksProvider (e.g., "claude", "gemini").
//   - settingsDir: the gastown-managed parent (used by agents with --settings flag).
//   - workDir: the agent's working directory.
//   - role: the Gas Town role (e.g., "polecat", "crew", "witness").
//   - hooksDir/hooksFile: from the preset's HooksDir and HooksSettingsFile.
//
// Template resolution:
//   - Role-aware agents (have both autonomous and interactive templates):
//     templates/<provider>/settings-autonomous.json + settings-interactive.json
//     or templates/<provider>/hooks-autonomous.json + hooks-interactive.json
//   - Role-agnostic agents (single template): templates/<provider>/<hooksFile>
//
// The install directory is settingsDir for agents that support --settings (useSettingsDir=true),
// or workDir for all others.
func InstallForRole(provider, settingsDir, workDir, role, hooksDir, hooksFile string, useSettingsDir bool) error {
	if provider == "" || hooksDir == "" || hooksFile == "" {
		return nil
	}

	targetPath := installTargetPath(settingsDir, workDir, hooksDir, hooksFile, useSettingsDir)

	if existing, err := os.ReadFile(targetPath); err == nil {
		if !needsUpgrade(existing) && !denyListDrifted(existing, hooksFile, role) {
			return nil // File exists and is current — don't overwrite
		}
		// Stale file or drifted deny-list detected — fall through to overwrite
		// with the current template.
	}

	return writeTemplate(provider, role, hooksFile, targetPath)
}

// denyListDrifted reports whether an existing settings.json for an AUTONOMOUS
// role is missing a deny-list entry that the canonical autonomous template
// requires. Historically InstallForRole only overwrote on the needsUpgrade
// stale-pattern signatures, so a settings file written before a deny entry was
// added to the template (e.g. "AskUserQuestion", gu-u4g21) never got the new
// entry — it silently drifted, leaving an autonomous, bypassPermissions agent
// able to hang on an interactive prompt no one is attending (gs-wbj footgun).
//
// This reconciles toward the canonical autonomous deny set: if the role is
// autonomous and the file's permissions.deny is missing ANY entry the template
// denies, return true so the caller overwrites with the current template. It is
// scoped narrowly:
//   - Only settings files (not hook files) and only roles that must deny
//     AskUserQuestion — autonomous roles, plus the unattended mayor (gu-2qvnx).
//     The interactive/crew template legitimately omits AskUserQuestion, so we
//     never force it there.
//   - It only ADDS toward the template's deny set; it does not invent new
//     denies (no scope creep beyond restoring the canonical set).
func denyListDrifted(existing []byte, hooksFile, role string) bool {
	if !isSettingsFile(hooksFile) || !roleMustDenyAskUserQuestion(role) {
		return false
	}
	want := autonomousDenySet()
	if len(want) == 0 {
		return false // can't read the template — don't trigger a rewrite
	}
	// The mayor uses the interactive template (which omits AskUserQuestion) but
	// must still deny it; the only canonical entry it can drift on is
	// AskUserQuestion itself. Autonomous roles drift against the full set.
	if !hookutil.IsAutonomousRole(role) {
		return !denyEntries(existing)[askUserQuestionTool]
	}
	have := denyEntries(existing)
	for entry := range want {
		if !have[entry] {
			return true
		}
	}
	return false
}

// ensureDeny returns content with entry present in permissions.deny, adding it
// (and the permissions/deny structure if absent) when missing. If the entry is
// already denied, content is returned unchanged so callers stay byte-identical.
// Returns an error only if content is not valid JSON.
func ensureDeny(content []byte, entry string) ([]byte, error) {
	if denyEntries(content)[entry] {
		return content, nil // already denied — no-op, preserves bytes
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(content, &root); err != nil {
		return nil, err
	}
	perms := map[string]json.RawMessage{}
	if raw, ok := root["permissions"]; ok {
		if err := json.Unmarshal(raw, &perms); err != nil {
			return nil, err
		}
	}
	var deny []string
	if raw, ok := perms["deny"]; ok {
		if err := json.Unmarshal(raw, &deny); err != nil {
			return nil, err
		}
	}
	deny = append([]string{entry}, deny...)
	denyRaw, err := json.Marshal(deny)
	if err != nil {
		return nil, err
	}
	perms["deny"] = denyRaw
	permsRaw, err := json.Marshal(perms)
	if err != nil {
		return nil, err
	}
	root["permissions"] = permsRaw
	return json.MarshalIndent(root, "", "  ")
}

// autonomousDenySet returns the deny entries the canonical claude autonomous
// settings template requires, as a set. Empty on parse failure.
func autonomousDenySet() map[string]bool {
	content, err := templateFS.ReadFile("templates/claude/settings-autonomous.json")
	if err != nil {
		return nil
	}
	return denyEntries(content)
}

// denyEntries parses a settings.json body and returns its permissions.deny
// entries as a set. Empty on parse failure or absent deny list.
func denyEntries(content []byte) map[string]bool {
	var parsed struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if json.Unmarshal(content, &parsed) != nil {
		return map[string]bool{}
	}
	set := make(map[string]bool, len(parsed.Permissions.Deny))
	for _, d := range parsed.Permissions.Deny {
		set[d] = true
	}
	return set
}

// needsUpgrade returns true if an existing hooks file contains stale patterns
// that should be replaced by the current template. This allows the installer
// to auto-upgrade hooks from earlier versions without requiring manual intervention.
func needsUpgrade(content []byte) bool {
	// Stale pattern: export PATH=... && gt — replaced by {{GT_BIN}} in current templates.
	// The PATH export breaks Gemini CLI's hook runner which expands $PATH into
	// an enormous string. Also catches files missing GT_HOOK_SOURCE env vars.
	if bytes.Contains(content, []byte(`export PATH=`)) {
		return true
	}
	if bytes.Contains(content, []byte(`Gas Town OpenCode plugin`)) {
		return bytes.Contains(content, []byte(`captureRun("gt prime")`)) ||
			bytes.Contains(content, []byte("$`gt prime`")) ||
			!bytes.Contains(content, []byte(`prime --hook`))
	}
	return false
}

// SyncResult describes what SyncForRole did.
type SyncResult int

const (
	SyncUnchanged SyncResult = iota // File already matches template
	SyncCreated                     // File did not exist, created
	SyncUpdated                     // File existed but content differed, updated
)

// SyncForRole compares the deployed hook/settings file against the current template
// and overwrites if content differs. Returns what action was taken.
//
// This is the explicit sync path used by "gt hooks sync" for template-based agents
// (OpenCode, Copilot, Pi, OMP, etc.). It should NOT be used for agents whose settings
// are managed by the JSON merge path (Claude), as that would clobber merged overrides.
func SyncForRole(provider, settingsDir, workDir, role, hooksDir, hooksFile string, useSettingsDir bool) (SyncResult, error) {
	if provider == "" || hooksDir == "" || hooksFile == "" {
		return SyncUnchanged, nil
	}

	targetPath := installTargetPath(settingsDir, workDir, hooksDir, hooksFile, useSettingsDir)

	content, err := resolveAndSubstitute(provider, hooksFile, role)
	if err != nil {
		return 0, err
	}

	fileExisted := false
	if existing, err := os.ReadFile(targetPath); err == nil {
		fileExisted = true
		if isSettingsFile(hooksFile) {
			// JSON files: use structural comparison to tolerate whitespace differences.
			if TemplateContentEqual(existing, content) {
				return SyncUnchanged, nil
			}
		} else {
			if bytes.Equal(existing, content) {
				return SyncUnchanged, nil
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return 0, fmt.Errorf("creating hooks directory: %w", err)
	}

	perm := os.FileMode(0644)
	if isSettingsFile(hooksFile) {
		perm = 0600
	}

	// Atomic write (temp + rename) prevents concurrent polecat spawns from
	// interleaving truncates+writes into a partial JSON file that Claude
	// rejects at startup. See gh#3500.
	if err := atomicfile.WriteFile(targetPath, content, perm); err != nil {
		return 0, fmt.Errorf("writing hooks file: %w", err)
	}

	if fileExisted {
		return SyncUpdated, nil
	}
	return SyncCreated, nil
}

// installTargetPath computes the full path for a hook/settings file.
func installTargetPath(settingsDir, workDir, hooksDir, hooksFile string, useSettingsDir bool) string {
	installDir := workDir
	if useSettingsDir {
		installDir = settingsDir
	}
	return filepath.Join(installDir, hooksDir, hooksFile)
}

// resolveAndSubstitute resolves the template and performs placeholder substitution.
func resolveAndSubstitute(provider, hooksFile, role string) ([]byte, error) {
	content, err := resolveTemplate(provider, hooksFile, role)
	if err != nil {
		return nil, fmt.Errorf("resolving template for %s: %w", provider, err)
	}

	// The mayor uses the interactive template (shared with crew) but must deny
	// AskUserQuestion because it runs unattended (gu-2qvnx). Inject the deny for
	// mayor specifically rather than editing the shared interactive template,
	// which would wrongly strip the tool from human-attended crew. Scoped to
	// Claude settings files; idempotent for roles whose template already denies
	// it (autonomous), so their output stays byte-identical.
	if provider == "claude" && isSettingsFile(hooksFile) && role == constants.RoleMayor {
		if injected, derr := ensureDeny(content, askUserQuestionTool); derr == nil {
			content = injected
		}
	}

	if bytes.Contains(content, []byte("{{GT_BIN}}")) {
		gtBin := resolveGTBinary()
		gtBinBytes := []byte(gtBin)
		if isSettingsFile(hooksFile) {
			// JSON-encode the path so Windows backslashes are properly escaped.
			// json.Marshal produces `"C:\\path\\gt.exe"` (with quotes); strip the quotes.
			if encoded, err := json.Marshal(gtBin); err == nil {
				gtBinBytes = encoded[1 : len(encoded)-1]
			}
		}
		content = bytes.ReplaceAll(content, []byte("{{GT_BIN}}"), gtBinBytes)
	}

	// AIM package path substitutions.
	if bytes.Contains(content, []byte("{{AIM_SKILL_PATHS}}")) {
		content = bytes.ReplaceAll(content, []byte("{{AIM_SKILL_PATHS}}"),
			[]byte(resolveAIMPaths("skills")))
	}
	if bytes.Contains(content, []byte("{{AIM_SOP_PATHS}}")) {
		content = bytes.ReplaceAll(content, []byte("{{AIM_SOP_PATHS}}"),
			[]byte(resolveAIMPaths("agent-sops")))
	}
	if bytes.Contains(content, []byte("{{AIM_GUARDRAIL}}")) {
		content = bytes.ReplaceAll(content, []byte("{{AIM_GUARDRAIL}}"),
			[]byte(resolveAIMGuardrail()))
	}

	return content, nil
}

// resolveAIMPaths scans ~/.aim/packages/*/eventId-*/subdir and returns a
// comma-separated list of the latest eventId path per package.
func resolveAIMPaths(subdir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	base := filepath.Join(home, ".aim", "packages")
	pkgs, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	var paths []string
	for _, pkg := range pkgs {
		if !pkg.IsDir() {
			continue
		}
		pkgDir := filepath.Join(base, pkg.Name())
		events, err := os.ReadDir(pkgDir)
		if err != nil {
			continue
		}
		// Sort descending to get latest eventId first.
		sort.Slice(events, func(i, j int) bool {
			return events[i].Name() > events[j].Name()
		})
		for _, ev := range events {
			if !ev.IsDir() || !strings.HasPrefix(ev.Name(), "eventId-") {
				continue
			}
			candidate := filepath.Join(pkgDir, ev.Name(), subdir)
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				paths = append(paths, candidate)
				break
			}
		}
	}
	return strings.Join(paths, ",")
}

// resolveAIMGuardrail returns the path to the AIM use_aws guardrail binary.
func resolveAIMGuardrail() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	base := filepath.Join(home, ".aim", "packages", "AIPowerUserCapabilities-1.0")
	events, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	sort.Slice(events, func(i, j int) bool {
		return events[i].Name() > events[j].Name()
	})
	for _, ev := range events {
		if !ev.IsDir() || !strings.HasPrefix(ev.Name(), "eventId-") {
			continue
		}
		candidate := filepath.Join(base, ev.Name(), "agents", "hooks", "use_aws_guardrail", "guardrail")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// writeTemplate resolves a template, substitutes placeholders, and writes it to targetPath.
func writeTemplate(provider, role, hooksFile, targetPath string) error {
	content, err := resolveAndSubstitute(provider, hooksFile, role)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("creating hooks directory: %w", err)
	}

	perm := os.FileMode(0644)
	if isSettingsFile(hooksFile) {
		perm = 0600
	}

	// Atomic write (temp + rename) — see gh#3500.
	if err := atomicfile.WriteFile(targetPath, content, perm); err != nil {
		return fmt.Errorf("writing hooks file: %w", err)
	}

	return nil
}

// resolveTemplate finds the right template for a provider+role combination.
func resolveTemplate(provider, hooksFile, role string) ([]byte, error) {
	// Determine role type
	autonomous := hookutil.IsAutonomousRole(role)

	// Try role-aware naming conventions
	if autonomous {
		for _, pattern := range roleAwarePatterns("autonomous", hooksFile) {
			path := fmt.Sprintf("templates/%s/%s", provider, pattern)
			if content, err := templateFS.ReadFile(path); err == nil {
				return content, nil
			}
		}
	} else {
		for _, pattern := range roleAwarePatterns("interactive", hooksFile) {
			path := fmt.Sprintf("templates/%s/%s", provider, pattern)
			if content, err := templateFS.ReadFile(path); err == nil {
				return content, nil
			}
		}
	}

	// Fall back to single template (role-agnostic agents)
	path := fmt.Sprintf("templates/%s/%s", provider, hooksFile)
	content, err := templateFS.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("no template found for provider %q file %q: %w", provider, hooksFile, err)
	}
	return content, nil
}

// roleAwarePatterns generates candidate template filenames for role-aware agents.
// Given roleType "autonomous" and hooksFile "settings.json", it tries:
//   - settings-autonomous.json
//   - hooks-autonomous.json
func roleAwarePatterns(roleType, hooksFile string) []string {
	ext := filepath.Ext(hooksFile)
	base := hooksFile[:len(hooksFile)-len(ext)]

	return []string{
		base + "-" + roleType + ext,  // settings-autonomous.json
		"hooks-" + roleType + ext,    // hooks-autonomous.json
		"settings-" + roleType + ext, // settings-autonomous.json (fallback)
	}
}

// isSettingsFile returns true for files that may contain sensitive role config.
func isSettingsFile(name string) bool {
	return filepath.Ext(name) == ".json"
}

// resolveGTBinary returns the absolute path to the gt binary.
// Tries os.Executable() first (most reliable when running as gt), then
// falls back to exec.LookPath for PATH-based discovery. If both fail,
// returns "gt" and hopes the runtime PATH has it.
func resolveGTBinary() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	if path, err := exec.LookPath("gt"); err == nil {
		return path
	}
	return "gt"
}

// ComputeExpectedTemplate returns the expected file content for a template-based
// provider (e.g., gemini) with {{GT_BIN}} resolved to the actual gt binary path.
// This is used by the doctor hooks-sync check to compare installed files against
// current templates.
func ComputeExpectedTemplate(provider, hooksFile, role string) ([]byte, error) {
	return resolveAndSubstitute(provider, hooksFile, role)
}

// TemplateContentEqual compares two JSON byte slices for structural equality
// by normalizing whitespace. Returns true if they represent the same JSON.
func TemplateContentEqual(expected, actual []byte) bool {
	var e, a interface{}
	if err := json.Unmarshal(expected, &e); err != nil {
		return false
	}
	if err := json.Unmarshal(actual, &a); err != nil {
		return false
	}
	ej, _ := json.Marshal(e)
	aj, _ := json.Marshal(a)
	return string(ej) == string(aj)
}

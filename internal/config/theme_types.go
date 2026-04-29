// Theme (tmux status bar + window tint) configuration types. Extracted from types.go.
package config

// ThemeConfig represents tmux theme settings for a rig.
type ThemeConfig struct {
	// Disabled skips tmux status/window theming for this rig.
	Disabled bool `json:"disabled,omitempty"`

	// Name picks from the default palette (e.g., "ocean", "forest").
	// If empty, a theme is auto-assigned based on rig name.
	Name string `json:"name,omitempty"`

	// Custom overrides the palette with specific colors.
	Custom *CustomTheme `json:"custom,omitempty"`

	// CrewThemes maps crew member names to theme names.
	// Checked before RoleThemes, so individual crew members can have distinct colors
	// while other crew members fall back to the role-level theme.
	// Example: {"krieger": "teal", "mallory": "ember"}
	CrewThemes map[string]string `json:"crew_themes,omitempty"`

	// RoleThemes overrides themes for specific roles in this rig.
	// Keys: "witness", "refinery", "crew", "polecat".
	// A value of "none" disables tmux theming for that role.
	RoleThemes map[string]string `json:"role_themes,omitempty"`

	// WindowTint controls window background (window-style) coloring for this rig.
	// If nil, falls back to town-level window tint config.
	WindowTint *WindowTint `json:"window_tint,omitempty"`
}

// CustomTheme allows specifying exact colors for the status bar.
type CustomTheme struct {
	BG string `json:"bg"` // Background color (hex or tmux color name)
	FG string `json:"fg"` // Foreground color (hex or tmux color name)
}

// TownThemeConfig represents global theme settings (mayor/config.json).
type TownThemeConfig struct {
	// Disabled skips tmux status/window theming for all sessions unless a rig
	// theme overrides it.
	Disabled bool `json:"disabled,omitempty"`

	// Name picks from the default palette when no role-specific override exists.
	Name string `json:"name,omitempty"`

	// Custom overrides the palette with specific colors when no role-specific
	// override exists.
	Custom *CustomTheme `json:"custom,omitempty"`

	// CrewThemes maps crew member names to theme names (town-wide defaults).
	// Checked before RoleDefaults. Per-rig CrewThemes take precedence.
	CrewThemes map[string]string `json:"crew_themes,omitempty"`

	// RoleDefaults sets default themes for roles across all rigs.
	// Keys: "mayor", "deacon", "witness", "refinery", "crew", "polecat".
	// A value of "none" disables tmux theming for that role.
	RoleDefaults map[string]string `json:"role_defaults,omitempty"`

	// WindowTint controls window background (window-style) coloring globally.
	// Per-rig WindowTint in ThemeConfig takes precedence over this.
	WindowTint *WindowTint `json:"window_tint,omitempty"`
}

// WindowTint controls window background (window-style) coloring.
// Mirrors status bar theme customization: palette name, custom colors, per-role overrides.
// When Enabled is nil or true, window backgrounds are tinted.
// When Enabled is false, window backgrounds use terminal defaults.
type WindowTint struct {
	// Enabled controls whether window tinting is active.
	// nil or true = enabled, false = disabled (window uses terminal default).
	Enabled *bool `json:"enabled,omitempty"`

	// Name picks a palette theme for the window background.
	// If empty, falls back to the session's status bar theme colors.
	Name string `json:"name,omitempty"`

	// Custom overrides the palette with specific window background colors.
	Custom *CustomTheme `json:"custom,omitempty"`

	// RoleTints overrides window tint themes for specific roles.
	// Keys: "witness", "refinery", "crew", "polecat"
	RoleTints map[string]string `json:"role_tints,omitempty"`

	// TintFactor controls how much the window background is darkened when
	// inheriting from the status bar theme (0.0–1.0). Lower = darker.
	// Default: 0.4 (40% of status bar brightness).
	// Only applies when window tint inherits from the status bar theme
	// (i.e., no explicit name, custom, or role_tints match).
	TintFactor *float64 `json:"tint_factor,omitempty"`
}

// BuiltinRoleThemes returns the default themes for each role.
// These are used when no explicit configuration is provided.
func BuiltinRoleThemes() map[string]string {
	return map[string]string{
		"witness":  "rust", // Red/rust - watchful, alert
		"refinery": "plum", // Purple - processing, refining
		// crew and polecat use rig theme by default (no override)
	}
}

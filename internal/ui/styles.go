// Package ui provides terminal styling for gastown CLI output.
// Uses the Ayu color theme with adaptive light/dark mode support.
// Design philosophy: semantic colors that communicate meaning at a glance,
// minimal visual noise, and consistent rendering across all commands.
package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func init() {
	if !ShouldUseColor() {
		// disable colors when not appropriate (non-TTY, NO_COLOR, etc.)
		lipgloss.SetColorProfile(termenv.Ascii)
	} else {
		// use TrueColor for distinct priority/status colors in modern terminals
		lipgloss.SetColorProfile(termenv.TrueColor)
	}
}

// ApplyThemeMode applies the theme mode settings to lipgloss.
// This should be called after InitTheme() has been called.
func ApplyThemeMode() {
	if !ShouldUseColor() {
		return
	}
	// Set lipgloss dark background flag based on theme mode
	lipgloss.SetHasDarkBackground(HasDarkBackground())
}

// Ayu theme color palette
// Dark: https://terminalcolors.com/themes/ayu/dark/
// Light: https://terminalcolors.com/themes/ayu/light/
// Source: https://github.com/ayu-theme/ayu-colors
var (
	// Core semantic colors (Ayu theme - adaptive light/dark)
	ColorPass = lipgloss.AdaptiveColor{
		Light: "#86b300", // ayu light bright green
		Dark:  "#c2d94c", // ayu dark bright green
	}
	ColorWarn = lipgloss.AdaptiveColor{
		Light: "#f2ae49", // ayu light bright yellow
		Dark:  "#ffb454", // ayu dark bright yellow
	}
	ColorFail = lipgloss.AdaptiveColor{
		Light: "#f07171", // ayu light bright red
		Dark:  "#f07178", // ayu dark bright red
	}
	ColorMuted = lipgloss.AdaptiveColor{
		Light: "#828c99", // ayu light muted
		Dark:  "#6c7680", // ayu dark muted
	}
	ColorAccent = lipgloss.AdaptiveColor{
		Light: "#399ee6", // ayu light bright blue
		Dark:  "#59c2ff", // ayu dark bright blue
	}

	// === Workflow Status Colors ===
	// Only actionable states get color - open/closed match standard text
	ColorStatusOpen = lipgloss.AdaptiveColor{
		Light: "", // standard text color
		Dark:  "",
	}
	ColorStatusInProgress = lipgloss.AdaptiveColor{
		Light: "#f2ae49", // yellow - active work, very visible
		Dark:  "#ffb454",
	}
	ColorStatusClosed = lipgloss.AdaptiveColor{
		Light: "#9099a1", // slightly dimmed - visually shows "done"
		Dark:  "#8090a0",
	}
	ColorStatusBlocked = lipgloss.AdaptiveColor{
		Light: "#f07171", // red - needs attention
		Dark:  "#f26d78",
	}
	ColorStatusPinned = lipgloss.AdaptiveColor{
		Light: "#d2a6ff", // purple - special/elevated
		Dark:  "#d2a6ff",
	}
	ColorStatusHooked = lipgloss.AdaptiveColor{
		Light: "#59c2ff", // cyan - actively worked by agent
		Dark:  "#59c2ff",
	}

	// === Priority Colors ===
	// P0/P1/P2 get color - they need attention
	// P3/P4 are neutral (low/backlog don't need visual urgency)
	ColorPriorityP0 = lipgloss.AdaptiveColor{
		Light: "#f07171", // bright red - critical, demands attention
		Dark:  "#f07178",
	}
	ColorPriorityP1 = lipgloss.AdaptiveColor{
		Light: "#ff8f40", // orange - high priority, needs attention soon
		Dark:  "#ff8f40",
	}
	ColorPriorityP2 = lipgloss.AdaptiveColor{
		Light: "#e6b450", // muted gold - medium priority, visible but calm
		Dark:  "#e6b450",
	}
	ColorPriorityP3 = lipgloss.AdaptiveColor{
		Light: "", // neutral - low priority
		Dark:  "",
	}
	ColorPriorityP4 = lipgloss.AdaptiveColor{
		Light: "", // neutral - backlog
		Dark:  "",
	}

	// === Issue Type Colors ===
	// Bugs and epics get color - they need attention
	// All other types use standard text
	ColorTypeBug = lipgloss.AdaptiveColor{
		Light: "#f07171", // bright red - bugs are problems
		Dark:  "#f26d78",
	}
	ColorTypeFeature = lipgloss.AdaptiveColor{
		Light: "", // standard text color
		Dark:  "",
	}
	ColorTypeTask = lipgloss.AdaptiveColor{
		Light: "", // standard text color
		Dark:  "",
	}
	ColorTypeEpic = lipgloss.AdaptiveColor{
		Light: "#d2a6ff", // purple - larger scope work
		Dark:  "#d2a6ff",
	}
	ColorTypeChore = lipgloss.AdaptiveColor{
		Light: "", // standard text color
		Dark:  "",
	}

	// === Issue ID Color ===
	// IDs use standard text color - subtle, not attention-grabbing
	ColorID = lipgloss.AdaptiveColor{
		Light: "", // standard text color
		Dark:  "",
	}
)

// Core styles - consistent across all commands
var (
	PassStyle   = lipgloss.NewStyle().Foreground(ColorPass)
	WarnStyle   = lipgloss.NewStyle().Foreground(ColorWarn)
	FailStyle   = lipgloss.NewStyle().Foreground(ColorFail)
	MutedStyle  = lipgloss.NewStyle().Foreground(ColorMuted)
	AccentStyle = lipgloss.NewStyle().Foreground(ColorAccent)
)

// Issue ID style
var IDStyle = lipgloss.NewStyle().Foreground(ColorID)

// Status styles for workflow states
var (
	StatusOpenStyle       = lipgloss.NewStyle().Foreground(ColorStatusOpen)
	StatusInProgressStyle = lipgloss.NewStyle().Foreground(ColorStatusInProgress)
	StatusClosedStyle     = lipgloss.NewStyle().Foreground(ColorStatusClosed)
	StatusBlockedStyle    = lipgloss.NewStyle().Foreground(ColorStatusBlocked)
	StatusPinnedStyle     = lipgloss.NewStyle().Foreground(ColorStatusPinned)
	StatusHookedStyle     = lipgloss.NewStyle().Foreground(ColorStatusHooked)
)

// Priority styles - P0 is bold for extra emphasis
var (
	PriorityP0Style = lipgloss.NewStyle().Foreground(ColorPriorityP0).Bold(true)
	PriorityP1Style = lipgloss.NewStyle().Foreground(ColorPriorityP1)
	PriorityP2Style = lipgloss.NewStyle().Foreground(ColorPriorityP2)
	PriorityP3Style = lipgloss.NewStyle().Foreground(ColorPriorityP3)
	PriorityP4Style = lipgloss.NewStyle().Foreground(ColorPriorityP4)
)

// Type styles for issue categories
var (
	TypeBugStyle     = lipgloss.NewStyle().Foreground(ColorTypeBug)
	TypeFeatureStyle = lipgloss.NewStyle().Foreground(ColorTypeFeature)
	TypeTaskStyle    = lipgloss.NewStyle().Foreground(ColorTypeTask)
	TypeEpicStyle    = lipgloss.NewStyle().Foreground(ColorTypeEpic)
	TypeChoreStyle   = lipgloss.NewStyle().Foreground(ColorTypeChore)
)

// CategoryStyle for section headers - bold with accent color
var CategoryStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)

// BoldStyle for emphasis
var BoldStyle = lipgloss.NewStyle().Bold(true)

// CommandStyle for command names - subtle contrast, not attention-grabbing
var CommandStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{
	Light: "#5c6166", // slightly darker than standard
	Dark:  "#bfbdb6", // slightly brighter than standard
})

// Status icons - consistent semantic indicators
// Design: small Unicode symbols, NOT emoji-style icons for visual consistency
const (
	IconPass = "✓"
	IconWarn = "⚠"
	IconFail = "✖"
	IconSkip = "-"
	IconInfo = "ℹ"
	IconFix  = "🔧"
)

// Issue status icons - used consistently across all commands
// Design principle: icons > text labels for scannability
const (
	StatusIconOpen       = "○" // available to work (hollow circle)
	StatusIconInProgress = "◐" // active work (half-filled)
	StatusIconBlocked    = "●" // needs attention (filled circle)
	StatusIconClosed     = "✓" // completed (checkmark)
	StatusIconDeferred   = "❄" // scheduled for later (snowflake)
	StatusIconPinned     = "📌" // elevated priority
)

// Priority icon - small filled circle, colored by priority level
const PriorityIcon = "●"

// Tree characters for hierarchical display
const (
	TreeChild  = "⎿ "  // child indicator
	TreeLast   = "└─ " // last child / detail line
	TreeIndent = "  "  // 2-space indent per level
)

// Separators - 42 characters wide
const (
	SeparatorLight = "──────────────────────────────────────────"
	SeparatorHeavy = "══════════════════════════════════════════"
)

// === Core Render Functions ===

// RenderPass renders text with pass (green) styling
func RenderPass(s string) string {
	return PassStyle.Render(s)
}

// RenderWarn renders text with warning (yellow) styling
func RenderWarn(s string) string {
	return WarnStyle.Render(s)
}

// RenderFail renders text with fail (red) styling
func RenderFail(s string) string {
	return FailStyle.Render(s)
}

// RenderMuted renders text with muted (gray) styling
func RenderMuted(s string) string {
	return MutedStyle.Render(s)
}

// RenderAccent renders text with accent (blue) styling
func RenderAccent(s string) string {
	return AccentStyle.Render(s)
}

// RenderCategory renders a category header in uppercase with accent color
func RenderCategory(s string) string {
	return CategoryStyle.Render(strings.ToUpper(s))
}

// RenderSeparator renders the light separator line in muted color
func RenderSeparator() string {
	return MutedStyle.Render(SeparatorLight)
}

// RenderCommand renders a command name with subtle styling
func RenderCommand(s string) string {
	return CommandStyle.Render(s)
}

// === Icon Render Functions ===

// RenderPassIcon renders the pass icon with styling
func RenderPassIcon() string {
	return PassStyle.Render(IconPass)
}

// RenderWarnIcon renders the warning icon with styling
func RenderWarnIcon() string {
	return WarnStyle.Render(IconWarn)
}

// RenderFailIcon renders the fail icon with styling
func RenderFailIcon() string {
	return FailStyle.Render(IconFail)
}

// RenderFixIcon renders the fix icon (wrench emoji, double-width)
func RenderFixIcon() string {
	return IconFix
}

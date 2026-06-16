package daemon

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/tmux"
)

// PressureResult holds the outcome of a pressure check.
type PressureResult struct {
	// OK is true if spawning should proceed.
	OK bool

	// Reason describes why spawning was blocked (empty if OK).
	Reason string

	// LoadAvg1 is the 1-minute load average at check time.
	LoadAvg1 float64

	// MemAvailableGB is approximate available memory in GB.
	MemAvailableGB float64

	// ActiveSessions is the count of active Claude agent sessions.
	ActiveSessions int
}

// checkPressure evaluates system load and session concurrency to decide
// whether spawning a new agent session is safe. It checks:
//
//  1. CPU pressure: 1-minute load average vs threshold (per-core).
//  2. Memory pressure: available memory vs minimum threshold.
//  3. Session concurrency: active tmux sessions vs maximum cap.
//
// Infrastructure agents (deacon, witness, mayor) should NOT be gated by
// pressure—they are the monitoring/recovery layer. Only gate:
//   - Polecats (dispatchQueuedWork, crash restarts)
//   - Refineries
//   - Dogs
func (d *Daemon) checkPressure(_ string) PressureResult {
	cfg := d.loadOperationalConfig().GetDaemonConfig()

	cpuThreshold := cfg.PressureCPUThresholdV()
	memThreshold := cfg.PressureMemThresholdGBV()
	maxSessions := cfg.PressureMaxSessionsV()
	memBudgetFraction := cfg.PressureMemBudgetFractionV()
	sessionCeilingFraction := cfg.PressureSessionCeilingFractionV()
	sessionMemGB := cfg.PressureSessionMemGBV()
	ramCeiling := ramDerivedSessionCeiling(sessionCeilingFraction, sessionMemGB)

	// All checks disabled — skip entirely, no subprocess calls. Note that
	// memBudgetFraction and the RAM-derived ceiling default ON (gu-xrkoq,
	// gu-tawx0), so this short-circuit only triggers when an operator has
	// explicitly zeroed every knob.
	if cpuThreshold <= 0 && memThreshold <= 0 && maxSessions <= 0 && memBudgetFraction <= 0 && ramCeiling <= 0 {
		return PressureResult{OK: true}
	}

	var result PressureResult
	result.OK = true

	// Tier 0: Memory budget (ON by default) — the boot-storm OOM safety net.
	// Defer spawns when available RAM drops below a fraction of total system
	// memory. Machine-independent: a 256GB box and a 16GB box keep the same
	// proportional headroom. Backstops systemd-oomd during the daemon's boot
	// fan-out of MCP-heavy sessions (gu-xrkoq).
	if memBudgetFraction > 0 {
		total := totalMemoryGB()
		avail := availableMemoryGB()
		result.MemAvailableGB = avail
		if total > 0 && avail > 0 {
			minAvail := total * memBudgetFraction
			if avail < minAvail {
				result.OK = false
				result.Reason = fmt.Sprintf("memory budget: %.1fGB available, need %.1fGB (%.0f%% of %.1fGB total)", avail, minAvail, memBudgetFraction*100, total)
				return result
			}
		}
	}

	// Tier 1: CPU pressure (load average per core)
	if cpuThreshold > 0 {
		result.LoadAvg1 = loadAverage1()
		numCPU := float64(runtime.NumCPU())
		loadPerCore := result.LoadAvg1 / numCPU
		if loadPerCore > cpuThreshold {
			result.OK = false
			result.Reason = fmt.Sprintf("cpu pressure: load/core %.2f exceeds threshold %.2f (load=%.1f, cores=%d)", loadPerCore, cpuThreshold, result.LoadAvg1, int(numCPU))
			return result
		}
	}

	// Tier 1: Memory pressure
	if memThreshold > 0 {
		result.MemAvailableGB = availableMemoryGB()
		if result.MemAvailableGB > 0 && result.MemAvailableGB < memThreshold {
			result.OK = false
			result.Reason = fmt.Sprintf("memory pressure: %.1fGB available, minimum %.1fGB", result.MemAvailableGB, memThreshold)
			return result
		}
	}

	// Tier 2: Session concurrency ceiling (counts ALL agent roles).
	//
	// Two count caps converge here:
	//   - maxSessions: a static operator-set ceiling (pressure_max_sessions),
	//     disabled by default.
	//   - ramCeiling: the RAM-derived hard ceiling = floor(MemTotal * fraction /
	//     per_session_GB), default ON (gu-tawx0). This is the durable fix for the
	//     memory thundering-herd: it caps the live session COUNT proactively so a
	//     spawn wave cannot push the box past RAM in the first place — whereas the
	//     Tier 0 memory budget only reacts once free RAM is already low.
	//
	// The binding cap is the smaller of whichever are enabled. We count sessions
	// once (a single tmux list-sessions) and compare against it.
	ceiling := effectiveSessionCeiling(maxSessions, ramCeiling)
	if ceiling > 0 {
		result.ActiveSessions = d.countAgentSessions()
		if result.ActiveSessions >= ceiling {
			result.OK = false
			result.Reason = sessionCeilingReason(result.ActiveSessions, ceiling, maxSessions, ramCeiling, sessionCeilingFraction)
			return result
		}
	}

	return result
}

// ramDerivedSessionCeiling computes the maximum number of live agent sessions
// the box can safely hold: floor(MemTotal * ceilingFraction / perSessionGB).
// Returns 0 (disabled) when either knob is non-positive or total memory is
// unreadable — failing open so an unreadable /proc/meminfo never blocks all
// spawns. See gu-tawx0.
func ramDerivedSessionCeiling(ceilingFraction, perSessionGB float64) int {
	if ceilingFraction <= 0 || perSessionGB <= 0 {
		return 0
	}
	total := totalMemoryGB()
	if total <= 0 {
		return 0
	}
	ceiling := int((total * ceilingFraction) / perSessionGB)
	// Always permit at least one session so a tiny box or an aggressive config
	// cannot wedge the town into admitting nothing.
	if ceiling < 1 {
		ceiling = 1
	}
	return ceiling
}

// effectiveSessionCeiling returns the binding session-count cap: the smaller of
// the static cap and the RAM-derived ceiling, ignoring whichever is disabled
// (<= 0). Returns 0 when both are disabled.
func effectiveSessionCeiling(maxSessions, ramCeiling int) int {
	switch {
	case maxSessions > 0 && ramCeiling > 0:
		if maxSessions < ramCeiling {
			return maxSessions
		}
		return ramCeiling
	case maxSessions > 0:
		return maxSessions
	case ramCeiling > 0:
		return ramCeiling
	default:
		return 0
	}
}

// sessionCeilingReason builds the deferral message, naming which cap bound the
// admission so operators can see whether to raise the static cap or the
// RAM-derived ceiling.
func sessionCeilingReason(active, ceiling, maxSessions, ramCeiling int, ceilingFraction float64) string {
	if ramCeiling > 0 && ceiling == ramCeiling && (maxSessions <= 0 || ramCeiling <= maxSessions) {
		return fmt.Sprintf("session ceiling: %d active sessions, RAM-derived max %d (%.0f%% of %.1fGB total); raise pressure_session_ceiling_fraction or add RAM",
			active, ceiling, ceilingFraction*100, totalMemoryGB())
	}
	return fmt.Sprintf("session cap: %d active sessions, max %d", active, ceiling)
}

// countAgentSessions counts active tmux sessions that belong to Gas Town agents.
// Uses the town's tmux socket so it only counts sessions for this town.
func (d *Daemon) countAgentSessions() int {
	if d.countAgentSessionsFn != nil {
		return d.countAgentSessionsFn()
	}
	t := tmux.NewTmux()
	sessions, err := t.ListSessions()
	if err != nil {
		return 0
	}

	count := 0
	for _, name := range sessions {
		if isAgentSession(name) {
			count++
		}
	}
	return count
}

// isAgentSession returns true if the tmux session name looks like a Gas Town agent.
// Agent sessions use prefixed names (e.g., "hq-mayor", "rig-witness", "rig-polecat-foo").
func isAgentSession(name string) bool {
	// Agent sessions contain role markers
	for _, marker := range []string{
		constants.RoleMayor,
		constants.RoleWitness,
		constants.RoleRefinery,
		constants.RolePolecat,
		constants.RoleDeacon,
		constants.RoleCrew,
		"boot",
		"dog",
	} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

// AvailableMemoryGB returns approximate available memory in GB.
//
// On Linux it reads MemAvailable from /proc/meminfo. On macOS it sums free
// and inactive pages from vm_stat. On unsupported platforms (Windows, etc.)
// it returns 0.
//
// Exported so other packages (e.g. the deacon patrol memory-check command,
// gu-ayam3) can reuse the platform-specific implementations without
// duplicating /proc/meminfo and vm_stat parsing.
func AvailableMemoryGB() float64 {
	return availableMemoryGB()
}

// LoadAverage1 returns the 1-minute load average.
//
// Exported so other packages (e.g. the dispatch refinery-backoff throttle,
// gu-5wn56) can reuse the platform-specific implementation without
// duplicating /proc/loadavg and sysctl parsing. Returns 0 when unavailable.
func LoadAverage1() float64 {
	return loadAverage1()
}

// loadAverage1 returns the 1-minute load average.
// Falls back to 0 if unavailable (effectively disabling the check).
func loadAverage1() float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		// macOS: use sysctl
		return loadAverage1Sysctl()
	}
	var load1 float64
	_, _ = fmt.Sscanf(string(data), "%f", &load1)
	return load1
}

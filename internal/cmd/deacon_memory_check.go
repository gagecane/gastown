package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/style"
)

// Memory-check thresholds (in GB) and CLI flags.
//
// Defaults are taken from the gu-ayam3 proposal (refiled from gc-l5bwv) so
// the deacon catches low-memory conditions BEFORE the kernel OOM-killer fires
// and takes Dolt down. The 2026-05-24 incident saw five Dolt kills in 90
// minutes on a host with 3.7GB of MemAvailable and no swap. Reactive cleanup
// after the kill is too late: bd commands fail, and the escalation paths
// themselves degrade.
//
// Tunable via flags so operators can adjust per-host without a code change.
var (
	memCheckWarnGB     float64
	memCheckHighGB     float64
	memCheckCriticalGB float64
	memCheckJSON       bool
	memCheckEscalate   bool
	memCheckDryRun     bool
)

var deaconMemoryCheckCmd = &cobra.Command{
	Use:     "memory-check",
	Aliases: []string{"mem-check", "memcheck"},
	Short:   "Check system memory pressure and escalate when low",
	Long: `Inspect /proc/meminfo (or vm_stat on macOS) and emit warnings/escalations
when MemAvailable drops below configured thresholds.

This is the proactive mirror of the daemon's pressure gate (which only
prevents new spawns). Run from the deacon patrol so we notice low memory
BEFORE the kernel OOM-killer fires and takes Dolt down (gu-ayam3 / gc-ppz9r).

Severity tiers (defaults — tunable via flags):
  WARN     MemAvailable < 10 GB    → log only, included in patrol digest
  HIGH     MemAvailable < 5  GB    → escalate (severity=high), notify mayor
  CRITICAL MemAvailable < 2  GB    → escalate (severity=critical) immediately

Exit codes:
  0  Memory healthy (above all thresholds) or warning only
  1  Error reading memory info
  2  HIGH or CRITICAL threshold crossed (escalation emitted unless --no-escalate)

Examples:
  gt deacon memory-check                   # Check with default thresholds
  gt deacon memory-check --json            # Machine-readable output
  gt deacon memory-check --warn 16 --high 8 --critical 4
  gt deacon memory-check --no-escalate     # Report only, do not call gt escalate
  gt deacon memory-check --dry-run         # Show what would happen, no escalation`,
	RunE: runDeaconMemoryCheck,
}

func init() {
	deaconCmd.AddCommand(deaconMemoryCheckCmd)

	deaconMemoryCheckCmd.Flags().Float64Var(&memCheckWarnGB, "warn", 10.0,
		"WARN threshold in GB (log only, included in patrol digest)")
	deaconMemoryCheckCmd.Flags().Float64Var(&memCheckHighGB, "high", 5.0,
		"HIGH threshold in GB (escalates with severity=high)")
	deaconMemoryCheckCmd.Flags().Float64Var(&memCheckCriticalGB, "critical", 2.0,
		"CRITICAL threshold in GB (escalates with severity=critical)")
	deaconMemoryCheckCmd.Flags().BoolVar(&memCheckJSON, "json", false,
		"Output as JSON")
	// Default true: this command is meant to drive escalation. --no-escalate
	// (=false) is the explicit opt-out path used by patrol-internal previews.
	deaconMemoryCheckCmd.Flags().BoolVar(&memCheckEscalate, "escalate", true,
		"Emit gt escalate calls when HIGH/CRITICAL thresholds are crossed")
	deaconMemoryCheckCmd.Flags().BoolVar(&memCheckDryRun, "dry-run", false,
		"Show what would happen without sending escalations")
}

// memoryCheckResult is the JSON payload for `gt deacon memory-check --json`.
type memoryCheckResult struct {
	MemAvailableGB float64 `json:"mem_available_gb"`
	Severity       string  `json:"severity"`     // "ok" | "warn" | "high" | "critical" | "unknown"
	Action         string  `json:"action"`       // "none" | "logged" | "escalated" | "would-escalate" | "skipped"
	WarnGB         float64 `json:"warn_gb"`
	HighGB         float64 `json:"high_gb"`
	CriticalGB     float64 `json:"critical_gb"`
	Reason         string  `json:"reason,omitempty"`
}

// runDeaconMemoryCheck classifies MemAvailable into tiers and escalates when
// the HIGH/CRITICAL bands are entered. Patrol-driven, so it intentionally
// does not maintain dedup state internally — `gt escalate --dedup` plus a
// stable fingerprint handles re-fire suppression across patrol cycles.
func runDeaconMemoryCheck(cmd *cobra.Command, args []string) error {
	avail := daemon.AvailableMemoryGB()
	result, exit2 := classifyMemory(avail, memCheckWarnGB, memCheckHighGB, memCheckCriticalGB,
		memCheckEscalate, memCheckDryRun)

	// Side effect (escalation) only fires when classification says so AND
	// we're not in dry-run / no-escalate mode. Doing this outside of
	// classifyMemory keeps the classifier pure and unit-testable.
	if result.Action == "escalated" {
		if err := emitMemoryEscalation(result); err != nil {
			// Don't return the error — we still want to surface the result
			// in JSON/human output so the patrol log captures it. Escalation
			// is best-effort; the alternative (silent failure) is worse.
			result.Reason += fmt.Sprintf(" [escalation failed: %v]", err)
		}
	}

	return outputMemoryCheck(result, exit2)
}

// classifyMemory is the pure classification core: given a MemAvailable
// reading and threshold flags, return the result struct and whether the
// caller should exit with code 2 (HIGH/CRITICAL).
//
// Keeping this side-effect-free lets us unit-test all bands without faking
// out /proc/meminfo or `gt escalate`.
func classifyMemory(avail, warnGB, highGB, criticalGB float64, doEscalate, dryRun bool) (memoryCheckResult, bool) {
	result := memoryCheckResult{
		MemAvailableGB: avail,
		WarnGB:         warnGB,
		HighGB:         highGB,
		CriticalGB:     criticalGB,
		Severity:       "ok",
		Action:         "none",
	}

	// availableMemoryGB returns 0 when reading /proc/meminfo or vm_stat
	// fails (or the platform is unsupported). Treat 0 as "unknown" rather
	// than as a critical alert — escalating from a missing data source
	// would be worse than staying quiet.
	if avail <= 0 {
		result.Severity = "unknown"
		result.Action = "skipped"
		result.Reason = "memory info unavailable on this platform"
		return result, false
	}

	// Classify into the most severe band that applies. Order matters:
	// critical < high < warn (lower threshold = more severe).
	switch {
	case criticalGB > 0 && avail < criticalGB:
		result.Severity = "critical"
		result.Reason = fmt.Sprintf("MemAvailable %.2fGB below CRITICAL threshold %.2fGB", avail, criticalGB)
	case highGB > 0 && avail < highGB:
		result.Severity = "high"
		result.Reason = fmt.Sprintf("MemAvailable %.2fGB below HIGH threshold %.2fGB", avail, highGB)
	case warnGB > 0 && avail < warnGB:
		result.Severity = "warn"
		result.Reason = fmt.Sprintf("MemAvailable %.2fGB below WARN threshold %.2fGB", avail, warnGB)
	}

	switch result.Severity {
	case "warn":
		// WARN tier: log only, no escalation. Captured in patrol digest.
		result.Action = "logged"
		return result, false
	case "high", "critical":
		if !doEscalate || dryRun {
			result.Action = "would-escalate"
		} else {
			result.Action = "escalated"
		}
		return result, true
	}

	// "ok" path
	return result, false
}

// emitMemoryEscalation invokes `gt escalate` with a stable signature so that
// repeated patrol cycles in the same low-memory window dedup into a single
// open escalation rather than spamming the mayor.
func emitMemoryEscalation(r memoryCheckResult) error {
	severity := r.Severity // "high" or "critical"
	desc := fmt.Sprintf("Memory pressure %s: %.2fGB available (threshold %.2fGB)",
		severity, r.MemAvailableGB, thresholdFor(r))
	reason := r.Reason +
		"\n\nSource: gt deacon memory-check (gu-ayam3)" +
		"\nThresholds: warn=" + fmt.Sprintf("%.1fGB high=%.1fGB critical=%.1fGB",
		r.WarnGB, r.HighGB, r.CriticalGB) +
		"\n\nIf this fires repeatedly, the kernel OOM-killer is likely to take" +
		" Dolt down soon (see gc-ppz9r). Consider pausing dispatch and" +
		" investigating top RSS consumers."

	args := []string{
		"escalate", desc,
		"--severity", severity,
		"--source", "deacon:memory-check",
		"--reason", reason,
		"--dedup",
		// Stable signature: same fingerprint across cycles, so the dedup
		// path bumps the existing escalation instead of creating a new one.
		// We deliberately do NOT include the GB reading — that would
		// fragment the dedup key on every cycle.
		"--signature", "deacon:memory-check:" + severity,
	}

	cmd := exec.Command("gt", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gt escalate failed: %w (output: %s)", err, string(out))
	}
	return nil
}

// thresholdFor returns the threshold corresponding to the result's severity.
// Used only for the escalation description; safe to assume severity is one
// of "high" or "critical" by the time this is called.
func thresholdFor(r memoryCheckResult) float64 {
	if r.Severity == "critical" {
		return r.CriticalGB
	}
	return r.HighGB
}

// outputMemoryCheck writes the result in the requested format and returns
// the right exit code via os.Exit when severity demands it.
//
// Exit codes are documented in the command Long. We use os.Exit for codes
// other than 0/1 because cobra+RunE only distinguishes "error" from "ok".
// We still return nil on the success/warn paths so cobra exits 0 cleanly.
func outputMemoryCheck(r memoryCheckResult, exit2 bool) error {
	if memCheckJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			return err
		}
	} else {
		printMemoryCheckHuman(r)
	}

	if exit2 {
		os.Exit(2)
	}
	return nil
}

func printMemoryCheckHuman(r memoryCheckResult) {
	switch r.Severity {
	case "ok":
		fmt.Printf("%s Memory healthy: %.2fGB available\n",
			style.Success.Render("✓"), r.MemAvailableGB)
	case "warn":
		fmt.Printf("%s Memory WARN: %.2fGB available (< %.1fGB threshold)\n",
			style.Bold.Render("⚠"), r.MemAvailableGB, r.WarnGB)
	case "high":
		fmt.Printf("%s Memory HIGH: %.2fGB available (< %.1fGB threshold)\n",
			style.Bold.Render("🚨"), r.MemAvailableGB, r.HighGB)
	case "critical":
		fmt.Printf("%s Memory CRITICAL: %.2fGB available (< %.1fGB threshold)\n",
			style.Bold.Render("🔥"), r.MemAvailableGB, r.CriticalGB)
	case "unknown":
		fmt.Printf("%s Memory unknown: %s\n",
			style.Dim.Render("○"), r.Reason)
	}
	if r.Action != "none" {
		fmt.Printf("  Action: %s\n", r.Action)
	}
	if r.Reason != "" && r.Severity != "unknown" {
		fmt.Printf("  %s\n", style.Dim.Render(r.Reason))
	}
}

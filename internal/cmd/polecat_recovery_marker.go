package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
)

var (
	markRecoveredClear  bool
	markRecoveredTTL    time.Duration
	markRecoveredBy     string
	markRecoveredReason string

	isRecoveredJSON bool
)

var polecatMarkRecoveredCmd = &cobra.Command{
	Use:   "mark-recovered <rig>/<polecat>",
	Short: "Mark a polecat as manually recovered (suppresses auto-restart)",
	Long: `Set a manual-recovery awareness marker on a polecat session.

When stuck-agent-dog or other automated recovery actors detect a stuck/dead
polecat that has an active marker, they SKIP RESTART_POLECAT and instead
request a NUKE_PENDING (or escalate). This prevents auto-restart from re-running
already-pushed work after a witness/mayor has performed an out-of-band recovery
(e.g. manual --no-verify push).

The marker is a small JSON file at:
  <town_root>/.runtime/recovery_markers/<session>.json

Markers expire after --ttl (default 30m) so a forgotten flag can't permanently
disable auto-restart for a slot.

Examples:
  gt polecat mark-recovered gastown/guzzle --by witness --reason "manual push"
  gt polecat mark-recovered gastown/guzzle --ttl 1h
  gt polecat mark-recovered gastown/guzzle --clear`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatMarkRecovered,
}

var polecatIsRecoveredCmd = &cobra.Command{
	Use:   "is-recovered <rig>/<polecat>",
	Short: "Check if a polecat has an active manual-recovery marker",
	Long: `Check whether a polecat has an active (non-expired) manual-recovery marker.

Exit code 0 means a fresh marker is active (auto-restart should be skipped).
Exit code 1 means no marker, or the marker has expired.

Designed for shell use by stuck-agent-dog and other plugins:
  if gt polecat is-recovered gastown/guzzle >/dev/null 2>&1; then
    # marker active — skip RESTART_POLECAT
  fi`,
	Args:          cobra.ExactArgs(1),
	RunE:          runPolecatIsRecovered,
	SilenceErrors: true,
	SilenceUsage:  true,
}

func init() {
	polecatMarkRecoveredCmd.Flags().BoolVar(&markRecoveredClear, "clear", false, "Remove the marker instead of setting it")
	polecatMarkRecoveredCmd.Flags().DurationVar(&markRecoveredTTL, "ttl", 0, "Marker lifetime (default 30m)")
	polecatMarkRecoveredCmd.Flags().StringVar(&markRecoveredBy, "by", "", "Who set the marker (e.g. witness, mayor, $USER)")
	polecatMarkRecoveredCmd.Flags().StringVar(&markRecoveredReason, "reason", "", "Why the marker was set")

	polecatIsRecoveredCmd.Flags().BoolVar(&isRecoveredJSON, "json", false, "Output marker details as JSON")

	polecatCmd.AddCommand(polecatMarkRecoveredCmd)
	polecatCmd.AddCommand(polecatIsRecoveredCmd)
}

func runPolecatMarkRecovered(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}
	_, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	townRoot := filepath.Dir(r.Path)
	sessionName := session.PolecatSessionName(session.PrefixFor(r.Name), polecatName)

	if markRecoveredClear {
		if err := polecat.ClearRecoveryMarker(townRoot, sessionName); err != nil {
			return fmt.Errorf("clearing marker: %w", err)
		}
		fmt.Printf("%s Cleared recovery marker for %s/%s (session=%s)\n",
			style.Success.Render("✓"), rigName, polecatName, sessionName)
		return nil
	}

	if err := polecat.WriteRecoveryMarker(townRoot, sessionName, markRecoveredBy, markRecoveredReason, markRecoveredTTL); err != nil {
		return fmt.Errorf("writing marker: %w", err)
	}

	m := polecat.ReadRecoveryMarker(townRoot, sessionName)
	if m == nil {
		return fmt.Errorf("marker write succeeded but read-back failed")
	}
	fmt.Printf("%s Set recovery marker for %s/%s (session=%s)\n",
		style.Success.Render("✓"), rigName, polecatName, sessionName)
	fmt.Printf("  expires_at: %s (in %s)\n",
		m.ExpiresAt.Format(time.RFC3339),
		time.Until(m.ExpiresAt).Round(time.Second))
	if m.SetBy != "" {
		fmt.Printf("  set_by:     %s\n", m.SetBy)
	}
	if m.Reason != "" {
		fmt.Printf("  reason:     %s\n", m.Reason)
	}
	fmt.Println("\nstuck-agent-dog will skip RESTART_POLECAT for this slot until the marker expires.")
	return nil
}

func runPolecatIsRecovered(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}
	_, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	townRoot := filepath.Dir(r.Path)
	sessionName := session.PolecatSessionName(session.PrefixFor(r.Name), polecatName)
	m := polecat.ReadRecoveryMarker(townRoot, sessionName)
	active := m != nil && time.Now().UTC().Before(m.ExpiresAt)

	if isRecoveredJSON {
		out := map[string]any{
			"rig":     rigName,
			"polecat": polecatName,
			"session": sessionName,
			"active":  active,
		}
		if m != nil {
			out["set_at"] = m.SetAt
			out["expires_at"] = m.ExpiresAt
			out["set_by"] = m.SetBy
			out["reason"] = m.Reason
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
	} else if active {
		fmt.Printf("active (set_by=%s, reason=%q, expires_at=%s)\n",
			m.SetBy, m.Reason, m.ExpiresAt.Format(time.RFC3339))
	} else if m != nil {
		fmt.Printf("expired (expired_at=%s)\n", m.ExpiresAt.Format(time.RFC3339))
	} else {
		fmt.Println("none")
	}

	if !active {
		// Non-zero exit communicates "no active marker" to shell callers.
		return NewSilentExit(1)
	}
	return nil
}

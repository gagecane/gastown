package cmd

// Watch mode for `gt status --watch`. Continuously renders status at a
// fixed interval, with caching so transient tmux/beads failures don't
// flicker the display to all-empty.

import (
	"bytes"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/style"
	"golang.org/x/term"
)

func runStatusWatch(_ *cobra.Command, _ []string) error {
	if statusJSON {
		return fmt.Errorf("--json and --watch cannot be used together")
	}
	if statusInterval <= 0 {
		return fmt.Errorf("interval must be positive, got %d", statusInterval)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	ticker := time.NewTicker(time.Duration(statusInterval) * time.Second)
	defer ticker.Stop()

	isTTY := term.IsTerminal(int(os.Stdout.Fd()))

	// Cache the last successful status to handle transient tmux/beads
	// failures. Watch mode spawns many tmux subprocesses per iteration;
	// under load the tmux server can intermittently fail, causing all
	// agents to appear as not running (empty bubbles).
	var cachedStatus *TownStatus
	var cachedAt time.Time
	maxStale := time.Duration(statusInterval) * time.Second * 5

	for {
		var buf bytes.Buffer

		if isTTY {
			buf.WriteString("\033[H\033[2J") // ANSI: cursor home + clear screen
		}

		timestamp := time.Now().Format("15:04:05")
		header := fmt.Sprintf("[%s] gt status --watch (every %ds, Ctrl+C to stop)", timestamp, statusInterval)
		if isTTY {
			fmt.Fprintf(&buf, "%s\n\n", style.Dim.Render(header))
		} else {
			fmt.Fprintf(&buf, "%s\n\n", header)
		}

		status, err := gatherStatus()
		usedCache := false

		// On error, retry once before giving up.
		if err != nil {
			status, err = gatherStatus()
		}

		if err == nil {
			// Detect degraded results: zero running agents when we
			// previously had some. This indicates a transient tmux
			// failure rather than all agents legitimately stopping.
			running := countRunningAgents(status)
			if running == 0 && cachedStatus != nil &&
				countRunningAgents(*cachedStatus) > 0 {
				// Retry once to confirm.
				retry, retryErr := gatherStatus()
				if retryErr == nil &&
					countRunningAgents(retry) > 0 {
					status = retry
				} else if time.Since(cachedAt) < maxStale {
					status = *cachedStatus
					usedCache = true
				}
			}
		} else if cachedStatus != nil &&
			time.Since(cachedAt) < maxStale {
			// Complete failure even after retry — use cache.
			status = *cachedStatus
			usedCache = true
			err = nil
		}

		if err != nil {
			fmt.Fprintf(&buf, "Error: %v\n", err)
		} else {
			if !usedCache {
				statusCopy := status
				cachedStatus = &statusCopy
				cachedAt = time.Now()
			}
			if usedCache {
				staleNote := fmt.Sprintf(
					"(using cached data from %s)",
					cachedAt.Format("15:04:05"),
				)
				if isTTY {
					fmt.Fprintf(&buf, "%s\n",
						style.Dim.Render(staleNote))
				} else {
					fmt.Fprintf(&buf, "%s\n", staleNote)
				}
			}
			if err := outputStatusText(&buf, status); err != nil {
				fmt.Fprintf(&buf, "Error: %v\n", err)
			}
		}

		// Write the entire frame atomically to prevent the terminal from
		// rendering a blank screen between the clear and the content.
		_, _ = os.Stdout.Write(buf.Bytes())

		select {
		case <-sigChan:
			if isTTY {
				fmt.Println("\nStopped.")
			}
			return nil
		case <-ticker.C:
		}
	}
}

// countRunningAgents returns the number of agents with Running=true
// across all global agents and rig agents in the status.
func countRunningAgents(s TownStatus) int {
	count := 0
	for _, a := range s.Agents {
		if a.Running {
			count++
		}
	}
	for _, r := range s.Rigs {
		for _, a := range r.Agents {
			if a.Running {
				count++
			}
		}
	}
	return count
}

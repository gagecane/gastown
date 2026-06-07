package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
)

// RunResult represents the outcome of a plugin execution.
type RunResult string

const (
	ResultSuccess RunResult = "success"
	ResultFailure RunResult = "failure"
	ResultSkipped RunResult = "skipped"

	// ResultInflight marks a record written when a plugin is dispatched to a
	// dog but BEFORE it has actually executed. Unlike the terminal results
	// above, an in-flight record satisfies the cooldown gate only within a
	// short grace window (see CooldownSatisfied) — not for the full cooldown.
	// This stops a dog that dies silently after dispatch from re-arming the
	// full cooldown on false pretenses, which let backup freshness drift
	// unbounded (gu-50nbo). It still suppresses immediate re-dispatch so the
	// daemon heartbeat does not storm a freshly-dispatched plugin (the original
	// reason dispatch was recorded at all — 055747cd).
	//
	// NOTE: the literal value is deliberately "inflight", NOT "dispatched" —
	// the auto-dispatch plugin's dog records result:dispatched to mean a
	// successful TERMINAL outcome ("I slung a bead"), so reusing that string
	// here would make a real success look non-terminal.
	ResultInflight RunResult = "inflight"
)

// PluginRunRecord represents data for creating a plugin run bead.
type PluginRunRecord struct {
	PluginName string
	RigName    string
	Result     RunResult
	Body       string
}

// PluginRunBead represents a recorded plugin run from the ledger.
type PluginRunBead struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	Labels    []string  `json:"labels"`
	Result    RunResult `json:"-"` // Parsed from labels
}

// Recorder handles plugin run recording and querying.
type Recorder struct {
	townRoot string
}

// NewRecorder creates a new plugin run recorder.
func NewRecorder(townRoot string) *Recorder {
	return &Recorder{townRoot: townRoot}
}

// RecordRun creates an ephemeral bead for a plugin run.
// This is pure data writing - the caller decides what result to record.
func (r *Recorder) RecordRun(record PluginRunRecord) (string, error) {
	title := fmt.Sprintf("Plugin run: %s", record.PluginName)

	// Build labels
	labels := []string{
		"type:plugin-run",
		fmt.Sprintf("plugin:%s", record.PluginName),
		fmt.Sprintf("result:%s", record.Result),
	}
	if record.RigName != "" {
		labels = append(labels, fmt.Sprintf("rig:%s", record.RigName))
	}

	// Build bd create command
	args := []string{
		"create",
		"--ephemeral",
		"--json",
		"--title=" + title,
	}
	for _, label := range labels {
		args = append(args, "-l", label)
	}
	if record.Body != "" {
		args = append(args, "--description="+record.Body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), constants.BdCommandTimeout)
	defer cancel()
	townBeads := beads.ResolveBeadsDir(r.townRoot)
	cmd := beads.CommandContext(ctx, r.townRoot, townBeads, beads.MutationPinned, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("creating plugin run bead: %s: %w", stderr.String(), err)
	}

	// Parse created bead ID from JSON output.
	// bd may emit warnings to stdout (e.g., "⚠ Creating test issue...")
	// before the JSON object, so strip any non-JSON prefix first.
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(extractJSONObject(stdout.Bytes()), &result); err != nil {
		return "", fmt.Errorf("parsing bd create output: %w", err)
	}

	// Close the receipt immediately — it exists for audit/cooldown-gate queries
	// (which use --all to include closed beads) but should not stay open.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), constants.BdCommandTimeout)
	defer closeCancel()
	closeCmd := beads.CommandContext(closeCtx, r.townRoot, townBeads, beads.MutationPinned, "close", result.ID, "--reason", "plugin run recorded")
	_ = closeCmd.Run() // Best-effort — reaper will catch it if this fails

	return result.ID, nil
}

// GetLastRun returns the most recent run for a plugin.
// Returns nil if no runs found.
func (r *Recorder) GetLastRun(pluginName string) (*PluginRunBead, error) {
	runs, err := r.queryRuns(pluginName, 1, "")
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	return runs[0], nil
}

// GetRunsSince returns all runs for a plugin since the given duration.
// Duration format: "1h", "24h", "7d", etc.
func (r *Recorder) GetRunsSince(pluginName string, since string) ([]*PluginRunBead, error) {
	return r.queryRuns(pluginName, 0, since)
}

// queryRuns queries plugin run beads from the ledger.
func (r *Recorder) queryRuns(pluginName string, limit int, since string) ([]*PluginRunBead, error) {
	args := []string{
		"list",
		"--json",
		"--all", // Include closed beads too
		"-l", "type:plugin-run",
		"-l", fmt.Sprintf("plugin:%s", pluginName),
	}
	if limit > 0 {
		args = append(args, fmt.Sprintf("--limit=%d", limit))
	}
	if since != "" {
		// Parse as Go duration and compute an absolute RFC3339 cutoff.
		// bd's compact duration uses "m" for months, but plugin gate
		// durations use Go's time.ParseDuration where "m" means minutes.
		// Passing an absolute timestamp avoids this unit mismatch.
		d, err := time.ParseDuration(since)
		if err != nil {
			return nil, fmt.Errorf("parsing duration %q: %w", since, err)
		}
		cutoff := time.Now().Add(-d).UTC().Format(time.RFC3339)
		args = append(args, "--created-after="+cutoff)
	}
	args = beads.InjectFlatForListJSON(args)

	ctx, cancel := context.WithTimeout(context.Background(), constants.BdCommandTimeout)
	defer cancel()
	cmd := beads.CommandContext(ctx, r.townRoot, beads.ResolveBeadsDir(r.townRoot), beads.ReadOnlyPinned, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Empty result is OK (no runs found)
		if stderr.Len() == 0 || stdout.String() == "[]\n" {
			return nil, nil
		}
		return nil, fmt.Errorf("querying plugin runs: %s: %w", stderr.String(), err)
	}

	// Parse JSON output (strip any non-JSON prefix like bd warnings).
	out := extractJSONArray(stdout.Bytes())
	var beads []struct {
		ID        string   `json:"id"`
		Title     string   `json:"title"`
		CreatedAt string   `json:"created_at"`
		Labels    []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &beads); err != nil {
		// Empty array is valid
		if string(out) == "[]\n" || len(out) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}

	// Convert to PluginRunBead with parsed result
	runs := make([]*PluginRunBead, 0, len(beads))
	for _, b := range beads {
		run := &PluginRunBead{
			ID:     b.ID,
			Title:  b.Title,
			Labels: b.Labels,
		}

		// Parse created_at
		if t, err := time.Parse(time.RFC3339, b.CreatedAt); err == nil {
			run.CreatedAt = t
		}

		// Extract result from labels
		for _, label := range b.Labels {
			if len(label) > 7 && label[:7] == "result:" {
				run.Result = RunResult(label[7:])
				break
			}
		}

		runs = append(runs, run)
	}

	return runs, nil
}

// CountRunsSince returns the count of runs for a plugin since the given duration.
// This is useful for cooldown gate evaluation.
func (r *Recorder) CountRunsSince(pluginName string, since string) (int, error) {
	runs, err := r.GetRunsSince(pluginName, since)
	if err != nil {
		return 0, err
	}
	return len(runs), nil
}

// CooldownSatisfied reports whether a plugin's cooldown gate should suppress a
// new run. It distinguishes a real completion from a bare dispatch record:
//
//   - A TERMINAL run (any result other than "inflight" — success, failure,
//     skipped) within the cooldown window satisfies the full cooldown. This
//     matches the historical CountRunsSince semantics for completed runs.
//   - An IN-FLIGHT record ("inflight", written when work is handed to a
//     dog before it executes) suppresses re-dispatch only within the short
//     `grace` window. After grace elapses with no terminal record, the gate
//     re-opens so the next heartbeat can re-dispatch / run in-process.
//
// This closes gu-50nbo: a dog that dies after dispatch but before running
// run.sh no longer re-arms the full cooldown, so backup freshness can no longer
// drift unbounded. It preserves the anti-storm guarantee (055747cd) because a
// freshly-dispatched plugin is still suppressed for the in-flight grace window.
//
// grace must be <= cooldown for dispatch records to fall within the query
// window; callers derive grace from the plugin's execution timeout.
func (r *Recorder) CooldownSatisfied(pluginName, cooldown, grace string) (bool, error) {
	runs, err := r.GetRunsSince(pluginName, cooldown)
	if err != nil {
		return false, err
	}
	graceDur, err := time.ParseDuration(grace)
	if err != nil {
		return false, fmt.Errorf("parsing grace duration %q: %w", grace, err)
	}
	return cooldownSatisfied(runs, time.Now(), graceDur), nil
}

// CronDue reports whether a plugin's cron gate is due to dispatch now. It is
// the cron analog of CooldownSatisfied (which returns "should suppress");
// CronDue returns "should dispatch", so callers `continue` when it is false.
//
// A cron gate is due when the most recent scheduled fire has not yet been
// serviced by ANY dispatch record — inflight or terminal. The daemon writes an
// inflight record the moment it hands the plugin to a dog (handler.go), so that
// record alone proves this fire was already dispatched and suppresses re-fire
// until the NEXT scheduled fire, when prevFire advances past the record.
//
// This differs deliberately from the cooldown gate's in-flight handling: a
// cooldown is a *minimum interval* where a dog that dies after dispatch should
// be retried within the window (gu-50nbo), so a stale inflight there re-opens
// after grace. A cron gate is a *scheduled fire* — once today's fire has been
// dispatched it must not re-fire until tomorrow's, even if the dog never wrote a
// terminal receipt. Reusing the cooldown's grace-reopen here re-fired daily cron
// plugins every grace window all day (gu-jifj5). The tradeoff: a dog that dies
// after dispatch skips that day's run, which self-corrects at the next fire — an
// acceptable cost for a daily idempotent patrol versus a re-dispatch storm.
//
// grace is accepted for signature parity with CooldownSatisfied and is used only
// to pad the lookback query window; the due decision no longer depends on it.
func (r *Recorder) CronDue(pluginName, schedule, grace string) (bool, error) {
	sched, err := parseCron(schedule)
	if err != nil {
		return false, fmt.Errorf("parsing cron schedule %q: %w", schedule, err)
	}
	graceDur, err := time.ParseDuration(grace)
	if err != nil {
		return false, fmt.Errorf("parsing grace duration %q: %w", grace, err)
	}
	now := time.Now()
	prevFire := sched.Prev(now)
	if prevFire.IsZero() {
		// Impossible schedule (e.g. Feb 30) — never fires.
		return false, nil
	}
	// Query a window that comfortably covers the most recent scheduled fire
	// (plus a small pad) so the serviced check below sees the dispatch record
	// for this fire even for infrequent schedules.
	window := now.Sub(prevFire) + graceDur + time.Minute
	runs, err := r.GetRunsSince(pluginName, window.String())
	if err != nil {
		return false, err
	}
	return cronDue(sched, runs, now), nil
}

// cronDue is the pure decision core of CronDue, split out for unit testing
// without a live beads store. `runs` are the plugin-run beads within the query
// window; `now` is the evaluation time.
func cronDue(sched *cronSchedule, runs []*PluginRunBead, now time.Time) bool {
	prevFire := sched.Prev(now)
	if prevFire.IsZero() {
		return false
	}
	for _, run := range runs {
		// Any dispatch record (inflight OR terminal) at or after the most recent
		// scheduled fire means this fire has already been serviced — the daemon
		// already dispatched for it. Suppress until the next scheduled fire, when
		// prevFire advances past this record and the gate re-opens.
		if !run.CreatedAt.Before(prevFire) {
			return false
		}
	}
	return true
}

// cooldownSatisfied is the pure decision core of CooldownSatisfied, split out
// for unit testing without a live beads store. `runs` are the plugin-run beads
// within the cooldown window; `now` and `grace` define the dispatch-record
// grace cutoff.
func cooldownSatisfied(runs []*PluginRunBead, now time.Time, grace time.Duration) bool {
	graceCutoff := now.Add(-grace)
	for _, run := range runs {
		if run.Result != ResultInflight {
			// A terminal run within the cooldown window satisfies the gate.
			return true
		}
		// In-flight record: satisfies only while still within the grace window.
		if run.CreatedAt.After(graceCutoff) {
			return true
		}
	}
	return false
}

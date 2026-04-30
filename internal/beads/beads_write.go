// Package beads provides create/update/close/dependency mutations against bd.
package beads

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/runtime"
)

// Create creates a new issue and returns it.
// If opts.Actor is empty, it defaults to the BD_ACTOR environment variable.
// This ensures created_by is populated for issue provenance tracking.
func (b *Beads) Create(opts CreateOptions) (*Issue, error) {
	// Guard against flag-like titles (gt-e0kx5: --help garbage beads)
	if IsFlagLikeTitle(opts.Title) {
		return nil, fmt.Errorf("refusing to create bead: %w (got %q)", ErrFlagTitle, opts.Title)
	}

	if b.store != nil && !opts.Ephemeral {
		return b.storeCreate(opts)
	}

	args := []string{"create", "--json"}

	if opts.Title != "" {
		args = append(args, "--title="+opts.Title)
	}
	// Labels takes precedence; fall back to deprecated single-label/Type fields.
	if len(opts.Labels) > 0 {
		args = append(args, "--labels="+strings.Join(opts.Labels, ","))
	} else if opts.Label != "" {
		args = append(args, "--labels="+opts.Label)
	} else if opts.Type != "" {
		args = append(args, "--labels=gt:"+opts.Type)
	}
	if opts.Priority >= 0 {
		args = append(args, fmt.Sprintf("--priority=%d", opts.Priority))
	}
	if opts.Description != "" {
		args = append(args, "--description="+opts.Description)
	}
	if opts.Parent != "" {
		args = append(args, "--parent="+opts.Parent)
	}
	if opts.Ephemeral {
		args = append(args, "--ephemeral")
	}
	if opts.Rig != "" {
		if townRoot := b.getTownRoot(); townRoot != "" {
			if rigDir := GetRigDirForName(townRoot, opts.Rig); rigDir != "" {
				args = append(args, "--repo="+rigDir)
			}
		}
	}
	// Default Actor from BD_ACTOR env var if not specified
	// Uses getActor() to respect isolated mode (tests)
	actor := opts.Actor
	if actor == "" {
		actor = b.getActor()
	}
	if actor != "" {
		args = append(args, "--actor="+actor)
	}

	out, err := b.run(args...)
	if err != nil {
		return nil, err
	}

	var issue Issue
	if err := json.Unmarshal(out, &issue); err != nil {
		return nil, fmt.Errorf("parsing bd create output: %w", err)
	}

	return &issue, nil
}

// CreateWithID creates an issue with a specific ID.
// This is useful for agent beads, role beads, and other beads that need
// deterministic IDs rather than auto-generated ones.
func (b *Beads) CreateWithID(id string, opts CreateOptions) (*Issue, error) {
	// Guard against flag-like titles (gt-e0kx5: --help garbage beads)
	if IsFlagLikeTitle(opts.Title) {
		return nil, fmt.Errorf("refusing to create bead: %w (got %q)", ErrFlagTitle, opts.Title)
	}

	args := []string{"create", "--json", "--id=" + id}
	if NeedsForceForID(id) {
		args = append(args, "--force")
	}

	if opts.Title != "" {
		args = append(args, "--title="+opts.Title)
	}
	// Labels takes precedence; fall back to deprecated single-label/Type fields.
	if len(opts.Labels) > 0 {
		args = append(args, "--labels="+strings.Join(opts.Labels, ","))
	} else if opts.Label != "" {
		args = append(args, "--labels="+opts.Label)
	} else if opts.Type != "" {
		args = append(args, "--labels=gt:"+opts.Type)
	}
	if opts.Priority >= 0 {
		args = append(args, fmt.Sprintf("--priority=%d", opts.Priority))
	}
	if opts.Description != "" {
		args = append(args, "--description="+opts.Description)
	}
	if opts.Parent != "" {
		args = append(args, "--parent="+opts.Parent)
	}
	// Default Actor from BD_ACTOR env var if not specified
	// Uses getActor() to respect isolated mode (tests)
	actor := opts.Actor
	if actor == "" {
		actor = b.getActor()
	}
	if actor != "" {
		args = append(args, "--actor="+actor)
	}

	out, err := b.run(args...)
	if err != nil {
		return nil, err
	}

	var issue Issue
	if err := json.Unmarshal(out, &issue); err != nil {
		return nil, fmt.Errorf("parsing bd create output: %w", err)
	}

	return &issue, nil
}

// Update updates an existing issue.
func (b *Beads) Update(id string, opts UpdateOptions) error {
	if b.store != nil {
		return b.storeUpdate(id, opts)
	}

	args := []string{"update", id}

	if opts.Title != nil {
		args = append(args, "--title="+*opts.Title)
	}
	if opts.Status != nil {
		args = append(args, "--status="+*opts.Status)
	}
	if opts.Priority != nil {
		args = append(args, fmt.Sprintf("--priority=%d", *opts.Priority))
	}
	if opts.Description != nil {
		args = append(args, "--description="+*opts.Description)
	}
	if opts.Assignee != nil {
		args = append(args, "--assignee="+*opts.Assignee)
	}
	// Label operations: set-labels replaces all, otherwise use add/remove
	if len(opts.SetLabels) > 0 {
		for _, label := range opts.SetLabels {
			args = append(args, "--set-labels="+label)
		}
	} else {
		for _, label := range opts.AddLabels {
			args = append(args, "--add-label="+label)
		}
		for _, label := range opts.RemoveLabels {
			args = append(args, "--remove-label="+label)
		}
	}

	_, err := b.run(args...)
	return err
}

// Close closes one or more issues.
// If a runtime session ID is set in the environment, it is passed to bd close
// for work attribution tracking (see decision 009-session-events-architecture.md).
func (b *Beads) Close(ids ...string) error {
	if len(ids) == 0 {
		return nil
	}

	if b.store != nil {
		return b.storeClose("", runtime.SessionIDFromEnv(), ids...)
	}

	args := append([]string{"close"}, ids...)

	// Pass session ID for work attribution if available
	if sessionID := runtime.SessionIDFromEnv(); sessionID != "" {
		args = append(args, "--session="+sessionID)
	}

	_, err := b.run(args...)
	return err
}

// CloseWithReason closes one or more issues with a reason.
// If a runtime session ID is set in the environment, it is passed to bd close
// for work attribution tracking (see decision 009-session-events-architecture.md).
func (b *Beads) CloseWithReason(reason string, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}

	if b.store != nil {
		return b.storeClose(reason, runtime.SessionIDFromEnv(), ids...)
	}

	args := append([]string{"close"}, ids...)
	args = append(args, "--reason="+reason)

	// Pass session ID for work attribution if available
	if sessionID := runtime.SessionIDFromEnv(); sessionID != "" {
		args = append(args, "--session="+sessionID)
	}

	_, err := b.run(args...)
	return err
}

// ForceCloseWithReason closes one or more issues with --force, bypassing
// dependency checks. Used by gt done where the polecat is about to be nuked
// and open molecule wisps should not block issue closure.
func (b *Beads) ForceCloseWithReason(reason string, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}

	// In-process store close doesn't enforce dependency checks (no --force
	// needed). Note: this means the store path bypasses the dependency
	// validation that the CLI's --force flag overrides. Callers relying on
	// ForceCloseWithReason (e.g., gt done nuking polecat wisps) are already
	// accepting that deps may remain dangling, so this is intentional.
	if b.store != nil {
		return b.storeClose(reason, runtime.SessionIDFromEnv(), ids...)
	}

	args := append([]string{"close"}, ids...)
	args = append(args, "--reason="+reason, "--force")

	// Pass session ID for work attribution if available
	if sessionID := runtime.SessionIDFromEnv(); sessionID != "" {
		args = append(args, "--session="+sessionID)
	}

	_, err := b.run(args...)
	return err
}

// Release moves an in_progress issue back to open status.
// This is used to recover stuck steps when a worker dies mid-task.
// It clears the assignee so the step can be claimed by another worker.
func (b *Beads) Release(id string) error {
	return b.ReleaseWithReason(id, "")
}

// ReleaseWithReason moves an in_progress issue back to open status with a reason.
// The reason is added as a note to the issue for tracking purposes.
func (b *Beads) ReleaseWithReason(id, reason string) error {
	if b.store != nil {
		updates := map[string]interface{}{
			"status":   "open",
			"assignee": "",
		}
		if reason != "" {
			updates["notes"] = "Released: " + reason
		}
		ctx, cancel := storeCtx()
		defer cancel()
		return b.store.UpdateIssue(ctx, id, updates, b.getActor())
	}

	args := []string{"update", id, "--status=open", "--assignee="}

	// Add reason as a note if provided
	if reason != "" {
		args = append(args, "--notes=Released: "+reason)
	}

	_, err := b.run(args...)
	return err
}

// AddDependency adds a dependency: issue depends on dependsOn.
func (b *Beads) AddDependency(issue, dependsOn string) error {
	if b.store != nil {
		return b.storeAddDependency(issue, dependsOn)
	}

	_, err := b.run("dep", "add", issue, dependsOn)
	return err
}

// RemoveDependency removes a dependency.
func (b *Beads) RemoveDependency(issue, dependsOn string) error {
	if b.store != nil {
		return b.storeRemoveDependency(issue, dependsOn)
	}

	_, err := b.run("dep", "remove", issue, dependsOn)
	return err
}

package refinery

import (
	"fmt"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/ciwatcher"
	"github.com/steveyegge/gastown/internal/events"
)

// CheckMQFreeze inspects the merge-queue freeze flag for the engineer's rig.
// When the freeze is present (a post-merge CI failure has not yet been cleared
// by ciwatcher), it returns a ProcessResult shaped like a no-merge so the
// caller can short-circuit MR processing without treating it as a hard failure.
//
// Side effects on freeze hit:
//   - Emits a `mq_frozen_blocked` audit event with rig + freeze metadata.
//   - Writes a one-line notice to e.output so a human watching the refinery's
//     stdout sees why the queue is stalled.
//
// Failure mode: if reading the freeze file fails for a reason OTHER than
// "not present", we fail closed (return frozen=true with an explanatory
// error). The merge queue blocking on a transient stat error is much safer
// than allowing merges while we cannot tell whether main is actually broken.
func (e *Engineer) CheckMQFreeze() (frozen bool, result ProcessResult) {
	townRoot := filepath.Dir(e.rig.Path)
	rigName := e.rig.Name

	isFrozen, err := ciwatcher.IsFrozen(townRoot, rigName)
	if err != nil {
		// Fail closed.
		_, _ = fmt.Fprintf(e.output, "[Engineer] WARNING: ciwatcher freeze check failed for %s: %v — failing closed (treating as frozen)\n", rigName, err)
		_ = events.LogAudit("mq_frozen_blocked", e.rig.Name+"/refinery", map[string]any{
			"rig":   rigName,
			"error": err.Error(),
			"mode":  "fail_closed",
		})
		return true, ProcessResult{NoMerge: true, Error: fmt.Sprintf("mq-frozen: freeze check error: %v", err)}
	}
	if !isFrozen {
		return false, ProcessResult{}
	}

	// Read freeze metadata for the audit event. A read error here is
	// non-fatal — IsFrozen already confirmed the file exists, so we still
	// know the queue is frozen even if the contents are corrupt.
	ff, readErr := ciwatcher.ReadFreeze(townRoot, rigName)
	payload := map[string]any{
		"rig": rigName,
	}
	reason := "broke-main-ci"
	if ff != nil {
		if ff.Reason != "" {
			reason = ff.Reason
			payload["reason"] = ff.Reason
		}
		if ff.BeadID != "" {
			payload["bead_id"] = ff.BeadID
		}
		if ff.RunID != "" {
			payload["run_id"] = ff.RunID
		}
		if ff.RunURL != "" {
			payload["run_url"] = ff.RunURL
		}
		if ff.CommitSHA != "" {
			payload["commit_sha"] = ff.CommitSHA
		}
		if !ff.FrozenAt.IsZero() {
			payload["frozen_at"] = ff.FrozenAt.Format("2006-01-02T15:04:05Z07:00")
		}
	} else if readErr != nil {
		payload["read_error"] = readErr.Error()
	}

	_, _ = fmt.Fprintf(e.output, "[Engineer] Merge queue is FROZEN for rig %s (%s) — refusing MR.\n", rigName, reason)
	_ = events.LogAudit("mq_frozen_blocked", e.rig.Name+"/refinery", payload)

	return true, ProcessResult{
		NoMerge: true,
		Error:   fmt.Sprintf("mq-frozen: %s", reason),
	}
}

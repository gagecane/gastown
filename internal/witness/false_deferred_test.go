package witness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// setupBareRepoDir creates a stub town root with a bare repo at the rig's
// .repo.git path. It writes an empty file so the os.Stat check in
// DiscoverDeferredButShipped passes; the test stubs runGitGrep so the bare
// repo doesn't need to be a real git directory.
func setupBareRepoDir(t *testing.T, rigName string) string {
	t.Helper()
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, ".gt-root"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	bareRepoPath := filepath.Join(tmpDir, rigName, ".repo.git")
	if err := os.MkdirAll(bareRepoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	return tmpDir
}

// stubFindCitingCommit replaces the package-level findCitingCommit lookup with
// a map-based stub: bead-ID → returned SHA. Empty SHA means "no citing commit
// on mainline." Restores the original on test cleanup.
func stubFindCitingCommit(t *testing.T, byBeadID map[string]string) {
	t.Helper()
	old := findCitingCommit
	findCitingCommit = func(_ *git.Git, _ /* defaultBranch */, beadID string) (string, error) {
		return byBeadID[beadID], nil
	}
	t.Cleanup(func() { findCitingCommit = old })
}

// stubFindCitingCommitWithError replaces findCitingCommit with a function that
// always returns an error. Used to test the per-bead skip-grep-error path.
func stubFindCitingCommitWithError(t *testing.T, err error) {
	t.Helper()
	old := findCitingCommit
	findCitingCommit = func(_ *git.Git, _ /* defaultBranch */, _ /* beadID */ string) (string, error) {
		return "", err
	}
	t.Cleanup(func() { findCitingCommit = old })
}

// falseDeferredTestBd constructs a mock bd that returns a canned `list` JSON
// payload for status=deferred queries. Closes and label updates are captured
// in mock.calls so tests can assert behavior.
func falseDeferredTestBd(deferredBeadsJSON string) (*BdCli, *mockBdCalls) {
	return mockBd(
		func(args []string) (string, error) {
			if len(args) >= 1 && args[0] == "list" {
				return deferredBeadsJSON, nil
			}
			return "[]", nil
		},
		func(args []string) error { return nil },
	)
}

// TestDiscoverDeferredButShipped_ClosesBeadWithCitedCommit verifies the primary
// recovery path: a deferred bead whose ID is cited by a commit on mainline is
// closed with the cited SHA in the reason and a dedup label is applied.
func TestDiscoverDeferredButShipped_ClosesBeadWithCitedCommit(t *testing.T) {
	const (
		rigName = "testrig"
		beadID  = "tr-deferred-1"
		sha     = "deadbeef0000111122223333444455556666"
	)
	tmpDir := setupBareRepoDir(t, rigName)

	// Stub: bead's ID maps to a citing SHA.
	stubFindCitingCommit(t, map[string]string{beadID: sha})

	listJSON := fmt.Sprintf(`[{"id":%q,"labels":[]}]`, beadID)
	bd, mock := falseDeferredTestBd(listJSON)

	result := DiscoverDeferredButShipped(bd, tmpDir, rigName)
	if result.Checked != 1 {
		t.Errorf("Checked = %d, want 1", result.Checked)
	}
	if len(result.Recovered) != 1 {
		t.Fatalf("Recovered = %d, want 1", len(result.Recovered))
	}
	r := result.Recovered[0]
	if r.Action != "closed" {
		t.Errorf("Action = %q, want %q", r.Action, "closed")
	}
	if r.CitedCommitSHA != sha {
		t.Errorf("CitedCommitSHA = %q, want %q", r.CitedCommitSHA, sha)
	}

	// bd close was called with -r and the cited short SHA in the reason.
	var foundClose bool
	for _, call := range mock.calls {
		if strings.Contains(call, "close "+beadID) && strings.Contains(call, "deadbeef") {
			foundClose = true
			break
		}
	}
	if !foundClose {
		t.Errorf("expected bd close %s with cited SHA in reason, got calls: %v", beadID, mock.calls)
	}

	// Dedup label was applied.
	var foundLabel bool
	wantLabel := FalseDeferredRecoveredLabelPrefix + ":deadbeef"
	for _, call := range mock.calls {
		if strings.Contains(call, "update "+beadID) && strings.Contains(call, wantLabel) {
			foundLabel = true
			break
		}
	}
	if !foundLabel {
		t.Errorf("expected --add-label=%s on %s, got calls: %v", wantLabel, beadID, mock.calls)
	}
}

// TestDiscoverDeferredButShipped_SkipsWhenNoCitedCommit verifies that a deferred
// bead with no citing commit on mainline is left untouched — that's the
// legitimate-deferred case (defer is doing its job).
func TestDiscoverDeferredButShipped_SkipsWhenNoCitedCommit(t *testing.T) {
	const (
		rigName = "testrig"
		beadID  = "tr-still-deferred"
	)
	tmpDir := setupBareRepoDir(t, rigName)

	// Stub: bead has no citing SHA on mainline.
	stubFindCitingCommit(t, map[string]string{})

	listJSON := fmt.Sprintf(`[{"id":%q,"labels":[]}]`, beadID)
	bd, mock := falseDeferredTestBd(listJSON)

	result := DiscoverDeferredButShipped(bd, tmpDir, rigName)
	if result.Checked != 1 {
		t.Errorf("Checked = %d, want 1", result.Checked)
	}
	if len(result.Recovered) != 1 {
		t.Fatalf("Recovered = %d, want 1", len(result.Recovered))
	}
	r := result.Recovered[0]
	if r.Action != "skip-no-commit" {
		t.Errorf("Action = %q, want %q", r.Action, "skip-no-commit")
	}

	// No bd close should have been issued.
	for _, call := range mock.calls {
		if strings.Contains(call, "close "+beadID) {
			t.Errorf("did not expect bd close on legitimately-deferred bead, got call: %q", call)
		}
	}
}

// TestDiscoverDeferredButShipped_DedupLabelSkipsRescan verifies that a bead
// already carrying `false-deferred-recovered:*` is skipped, even if a citing
// commit exists. This protects against repeated close attempts on a bead the
// patrol already handled (or that an operator manually marked).
func TestDiscoverDeferredButShipped_DedupLabelSkipsRescan(t *testing.T) {
	const (
		rigName = "testrig"
		beadID  = "tr-already-recovered"
		sha     = "facefacefacefacefacefacefaceface00000000"
	)
	tmpDir := setupBareRepoDir(t, rigName)

	// Stub: bead WOULD find a citing SHA — but the dedup label should prevent
	// the lookup from mattering.
	stubFindCitingCommit(t, map[string]string{beadID: sha})

	listJSON := fmt.Sprintf(`[{"id":%q,"labels":["false-deferred-recovered:abc123"]}]`, beadID)
	bd, mock := falseDeferredTestBd(listJSON)

	result := DiscoverDeferredButShipped(bd, tmpDir, rigName)
	if result.Checked != 1 {
		t.Errorf("Checked = %d, want 1", result.Checked)
	}
	if len(result.Recovered) != 1 {
		t.Fatalf("Recovered = %d, want 1", len(result.Recovered))
	}
	r := result.Recovered[0]
	if r.Action != "skip-already-labeled" {
		t.Errorf("Action = %q, want %q", r.Action, "skip-already-labeled")
	}

	// Sanity: no close issued.
	for _, call := range mock.calls {
		if strings.Contains(call, "close "+beadID) {
			t.Errorf("did not expect bd close on already-labeled bead, got call: %q", call)
		}
	}
}

// TestDiscoverDeferredButShipped_SkipsWhenBareRepoMissing verifies the patrol
// degrades gracefully when the rig's .repo.git is absent. This can happen on
// fresh rig boot or during rig migration; we don't want a hard error.
func TestDiscoverDeferredButShipped_SkipsWhenBareRepoMissing(t *testing.T) {
	const rigName = "testrig"
	// Don't create the bare repo dir.
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, ".gt-root"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	// bd should never be queried because we bail early.
	bd, _ := falseDeferredTestBd(`[{"id":"tr-x","labels":[]}]`)

	result := DiscoverDeferredButShipped(bd, tmpDir, rigName)
	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0", result.Checked)
	}
	if len(result.Errors) == 0 {
		t.Error("expected scan-wide error for missing bare repo")
	}
}

// TestDiscoverDeferredButShipped_GrepErrorIsPerBeadSkip verifies that a
// transient git error on one bead's --grep lookup doesn't abort the rest of
// the scan — the bead is recorded as skip-grep-error and the next bead is
// processed normally.
func TestDiscoverDeferredButShipped_GrepErrorIsPerBeadSkip(t *testing.T) {
	const rigName = "testrig"
	tmpDir := setupBareRepoDir(t, rigName)

	stubFindCitingCommitWithError(t, fmt.Errorf("git: unknown revision"))

	listJSON := `[{"id":"tr-a","labels":[]},{"id":"tr-b","labels":[]}]`
	bd, _ := falseDeferredTestBd(listJSON)

	result := DiscoverDeferredButShipped(bd, tmpDir, rigName)
	if result.Checked != 2 {
		t.Errorf("Checked = %d, want 2", result.Checked)
	}
	if len(result.Recovered) != 2 {
		t.Fatalf("Recovered = %d, want 2", len(result.Recovered))
	}
	for _, r := range result.Recovered {
		if r.Action != "skip-grep-error" {
			t.Errorf("bead %s: Action = %q, want %q", r.BeadID, r.Action, "skip-grep-error")
		}
		if r.Error == nil {
			t.Errorf("bead %s: expected per-bead error, got nil", r.BeadID)
		}
	}
}

// TestDiscoverDeferredButShipped_EmptyDeferredList covers the no-op case —
// no deferred beads in the rig — and verifies it returns cleanly with no
// per-bead recoveries.
func TestDiscoverDeferredButShipped_EmptyDeferredList(t *testing.T) {
	const rigName = "testrig"
	tmpDir := setupBareRepoDir(t, rigName)

	stubFindCitingCommit(t, map[string]string{})

	bd, _ := falseDeferredTestBd("[]")

	result := DiscoverDeferredButShipped(bd, tmpDir, rigName)
	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0", result.Checked)
	}
	if len(result.Recovered) != 0 {
		t.Errorf("Recovered = %d, want 0", len(result.Recovered))
	}
	if len(result.Errors) != 0 {
		t.Errorf("Errors = %v, want none", result.Errors)
	}
}

// TestHasFalseDeferredRecoveredLabel exercises the prefix-match guard so
// future label-format changes don't silently drift past the dedup check.
func TestHasFalseDeferredRecoveredLabel(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"empty", nil, false},
		{"unrelated", []string{"polecat", "stranded-assignee"}, false},
		{"prefix-only", []string{"false-deferred-recovered"}, false}, // missing colon
		{"happy", []string{"false-deferred-recovered:abc123"}, true},
		{"mixed", []string{"foo", "false-deferred-recovered:deadbeef", "bar"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasFalseDeferredRecoveredLabel(tt.labels); got != tt.want {
				t.Errorf("hasFalseDeferredRecoveredLabel(%v) = %v, want %v",
					tt.labels, got, tt.want)
			}
		})
	}
}

// TestShortShaTruncate verifies the SHA-truncation helper used in close
// reasons and label suffixes.
func TestShortShaTruncate(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"abc", "abc"},
		{"123456789012", "123456789012"},           // exactly 12
		{"1234567890123", "123456789012"},          // > 12 truncates
		{"  deadbeef  ", "deadbeef"},               // trim whitespace
		{"abcdef0123456789abcdef", "abcdef012345"}, // long → first 12
	}
	for _, tt := range tests {
		got := shortShaTruncate(tt.in)
		if got != tt.want {
			t.Errorf("shortShaTruncate(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestFindCitingCommit_FallsBackToBareRef verifies the origin/<default> →
// <default> fallback when the bare repo doesn't have an `origin` remote
// configured. The first call returns an error; the second returns a SHA.
func TestFindCitingCommit_FallsBackToBareRef(t *testing.T) {
	oldGrep := runGitGrep
	t.Cleanup(func() { runGitGrep = oldGrep })

	// Track which ref each call requested.
	var calls []string
	runGitGrep = func(_ *git.Git, ref, _ /* needle */ string) (string, error) {
		calls = append(calls, ref)
		if strings.HasPrefix(ref, "origin/") {
			return "", fmt.Errorf("git: unknown revision: %s", ref)
		}
		return "abc1234567890000abcdef\n", nil
	}

	sha, err := _findCitingCommit(nil, "main", "tr-bead")
	if err != nil {
		t.Fatalf("expected fallback success, got error: %v", err)
	}
	if !strings.HasPrefix(sha, "abc12345") {
		t.Errorf("sha = %q, want prefix abc12345", sha)
	}
	if len(calls) != 2 {
		t.Errorf("expected 2 grep attempts (primary + fallback), got %d: %v", len(calls), calls)
	}
}

// TestDiscoverOpenButShipped_ClosesUnassignedBeadWithCitedCommit is the
// primary regression gate for the re-dispatch zombie loop (gu-y55ux): an
// OPEN, unassigned bead whose ID is cited by a commit on mainline is
// auto-closed with the cited SHA — breaking the loop where a reaper re-opened
// a no-changes-closed bead (which wipes close_reason) and the scheduler
// re-dispatched it every cycle.
func TestDiscoverOpenButShipped_ClosesUnassignedBeadWithCitedCommit(t *testing.T) {
	const (
		rigName = "testrig"
		beadID  = "tr-open-shipped"
		sha     = "abcdef0000111122223333444455556666aaaa"
	)
	tmpDir := setupBareRepoDir(t, rigName)

	stubFindCitingCommit(t, map[string]string{beadID: sha})

	listJSON := fmt.Sprintf(`[{"id":%q,"labels":[],"assignee":""}]`, beadID)
	bd, mock := falseDeferredTestBd(listJSON)

	result := DiscoverOpenButShipped(bd, tmpDir, rigName)
	if result.Checked != 1 {
		t.Errorf("Checked = %d, want 1", result.Checked)
	}
	if len(result.Recovered) != 1 {
		t.Fatalf("Recovered = %d, want 1", len(result.Recovered))
	}
	if r := result.Recovered[0]; r.Action != "closed" || r.CitedCommitSHA != sha {
		t.Errorf("Recovered[0] = {Action:%q, SHA:%q}, want {closed, %q}", r.Action, r.CitedCommitSHA, sha)
	}

	var foundClose bool
	for _, call := range mock.calls {
		// Close reason must cite the loop-breaking bead so operators can trace it.
		if strings.Contains(call, "close "+beadID) && strings.Contains(call, "abcdef") && strings.Contains(call, "gu-y55ux") {
			foundClose = true
			break
		}
	}
	if !foundClose {
		t.Errorf("expected bd close %s with cited SHA + gu-y55ux reason, got calls: %v", beadID, mock.calls)
	}
}

// TestDiscoverOpenButShipped_SkipsAssignedBead verifies the conservatism gate:
// an open bead with an assignee (a polecat actively hooked, or an operator
// re-assignment) is NEVER auto-closed, even if a citing commit exists. We must
// never yank work from under a live worker.
func TestDiscoverOpenButShipped_SkipsAssignedBead(t *testing.T) {
	const (
		rigName = "testrig"
		beadID  = "tr-open-assigned"
		sha     = "feedface0000111122223333444455556666bb"
	)
	tmpDir := setupBareRepoDir(t, rigName)

	// A citing commit WOULD be found — the assignee guard must short-circuit
	// before the lookup matters.
	stubFindCitingCommit(t, map[string]string{beadID: sha})

	listJSON := fmt.Sprintf(`[{"id":%q,"labels":[],"assignee":"testrig/polecats/nux"}]`, beadID)
	bd, mock := falseDeferredTestBd(listJSON)

	result := DiscoverOpenButShipped(bd, tmpDir, rigName)
	if result.Checked != 1 {
		t.Errorf("Checked = %d, want 1", result.Checked)
	}
	if len(result.Recovered) != 1 || result.Recovered[0].Action != "skip-assigned" {
		t.Fatalf("Recovered = %+v, want one skip-assigned", result.Recovered)
	}

	for _, call := range mock.calls {
		if strings.Contains(call, "close "+beadID) {
			t.Errorf("did not expect bd close on assigned bead, got call: %q", call)
		}
	}
}

// TestDiscoverOpenButShipped_RecloseseLabeledReopenedBead verifies the open
// scan does NOT skip a bead already carrying the recovery label — unlike the
// deferred scan. A labeled bead that is open again is exactly the re-open loop
// being broken, so it must be re-closed. (skipIfLabeled=false for this scan.)
func TestDiscoverOpenButShipped_ReclosesLabeledReopenedBead(t *testing.T) {
	const (
		rigName = "testrig"
		beadID  = "tr-open-relabeled"
		sha     = "0badf00d0000111122223333444455556666cc"
	)
	tmpDir := setupBareRepoDir(t, rigName)

	stubFindCitingCommit(t, map[string]string{beadID: sha})

	// Bead already carries a prior recovery label AND is open + unassigned.
	listJSON := fmt.Sprintf(`[{"id":%q,"labels":["false-deferred-recovered:abc123"],"assignee":""}]`, beadID)
	bd, mock := falseDeferredTestBd(listJSON)

	result := DiscoverOpenButShipped(bd, tmpDir, rigName)
	if len(result.Recovered) != 1 || result.Recovered[0].Action != "closed" {
		t.Fatalf("Recovered = %+v, want one closed (re-close despite label)", result.Recovered)
	}

	var foundClose bool
	for _, call := range mock.calls {
		if strings.Contains(call, "close "+beadID) {
			foundClose = true
			break
		}
	}
	if !foundClose {
		t.Errorf("expected re-close of labeled-but-reopened bead %s, got calls: %v", beadID, mock.calls)
	}
}

// TestDiscoverOpenButShipped_SkipsWhenNoCitedCommit verifies that an ordinary
// open bead with real outstanding work (no citing commit on mainline) is left
// untouched — the common case the scan must not disturb.
func TestDiscoverOpenButShipped_SkipsWhenNoCitedCommit(t *testing.T) {
	const (
		rigName = "testrig"
		beadID  = "tr-open-real-work"
	)
	tmpDir := setupBareRepoDir(t, rigName)

	stubFindCitingCommit(t, map[string]string{}) // no citing commit

	listJSON := fmt.Sprintf(`[{"id":%q,"labels":[],"assignee":""}]`, beadID)
	bd, mock := falseDeferredTestBd(listJSON)

	result := DiscoverOpenButShipped(bd, tmpDir, rigName)
	if len(result.Recovered) != 1 || result.Recovered[0].Action != "skip-no-commit" {
		t.Fatalf("Recovered = %+v, want one skip-no-commit", result.Recovered)
	}
	for _, call := range mock.calls {
		if strings.Contains(call, "close "+beadID) {
			t.Errorf("did not expect bd close on a bead with real outstanding work, got call: %q", call)
		}
	}
}

// TestFindCitingCommit_BothRefsFail verifies that when both origin/<default>
// and the bare ref name fail, the original (primary) error is surfaced.
func TestFindCitingCommit_BothRefsFail(t *testing.T) {
	oldGrep := runGitGrep
	t.Cleanup(func() { runGitGrep = oldGrep })

	primary := fmt.Errorf("primary failure")
	runGitGrep = func(_ *git.Git, ref, _ string) (string, error) {
		if strings.HasPrefix(ref, "origin/") {
			return "", primary
		}
		return "", fmt.Errorf("fallback failure")
	}

	_, err := _findCitingCommit(nil, "main", "tr-bead")
	if err == nil {
		t.Fatal("expected error when both refs fail, got nil")
	}
	if !strings.Contains(err.Error(), "primary failure") {
		t.Errorf("error = %v, want primary failure surfaced", err)
	}
}

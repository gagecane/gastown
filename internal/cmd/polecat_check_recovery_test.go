package cmd

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
)

// fakeMRFinder is a test stub for the mrFinder interface used by applyMQCheck.
type fakeMRFinder struct {
	issue *beads.Issue
	err   error
}

func (f fakeMRFinder) FindMRForBranchAny(branch string) (*beads.Issue, error) {
	return f.issue, f.err
}

type fakeIssueShower struct {
	issue *beads.Issue
	err   error
}

func (f fakeIssueShower) Show(issueID string) (*beads.Issue, error) {
	return f.issue, f.err
}

type fakeCleanupUpdater struct {
	err    error
	id     string
	status string
	calls  int
}

func (f *fakeCleanupUpdater) UpdateAgentCleanupStatus(id string, cleanupStatus string) error {
	f.calls++
	f.id = id
	f.status = cleanupStatus
	return f.err
}

type fakeIssueMapShower struct {
	issues map[string]*beads.Issue
	errs   map[string]error
}

func (f fakeIssueMapShower) Show(issueID string) (*beads.Issue, error) {
	if err := f.errs[issueID]; err != nil {
		return nil, err
	}
	issue, ok := f.issues[issueID]
	if !ok {
		return nil, beads.ErrNotFound
	}
	return issue, nil
}

func TestApplyMQCheck(t *testing.T) {
	tests := []struct {
		name           string
		finder         mrFinder
		beadTerminal   bool
		hasWork        bool
		mqNotRequired  bool
		initialVerdict string
		wantVerdict    string
		wantMQStatus   string
		wantNeedsRecov bool
	}{
		{
			// The regression this change fixes: assigned bead is CLOSED
			// (e.g. aa-xtee no-op audit). Must NOT return NEEDS_MQ_SUBMIT
			// because there is nothing to submit — the work is terminal.
			name:           "closed bead skips MQ submit check",
			finder:         fakeMRFinder{issue: nil, err: nil},
			beadTerminal:   true,
			hasWork:        true,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "SAFE_TO_NUKE",
			wantMQStatus:   "submitted",
			wantNeedsRecov: false,
		},
		{
			name:           "no submittable work skips MQ submit check",
			finder:         fakeMRFinder{issue: nil, err: nil},
			beadTerminal:   false,
			hasWork:        false,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "SAFE_TO_NUKE",
			wantMQStatus:   "not_required",
			wantNeedsRecov: false,
		},
		{
			name:           "no merge source with pushed branch work skips MQ submit check",
			finder:         fakeMRFinder{issue: nil, err: nil},
			beadTerminal:   false,
			hasWork:        true,
			mqNotRequired:  true,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "SAFE_TO_NUKE",
			wantMQStatus:   "not_required",
			wantNeedsRecov: false,
		},
		{
			name:           "open bead with no MR escalates to NEEDS_MQ_SUBMIT",
			finder:         fakeMRFinder{issue: nil, err: nil},
			beadTerminal:   false,
			hasWork:        true,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "NEEDS_MQ_SUBMIT",
			wantMQStatus:   "not_submitted",
			wantNeedsRecov: true,
		},
		{
			name:           "open bead with MR stays SAFE_TO_NUKE",
			finder:         fakeMRFinder{issue: &beads.Issue{ID: "mr-1"}, err: nil},
			beadTerminal:   false,
			hasWork:        true,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "SAFE_TO_NUKE",
			wantMQStatus:   "submitted",
			wantNeedsRecov: false,
		},
		{
			name:           "MR lookup error fails closed",
			finder:         fakeMRFinder{issue: nil, err: errors.New("bd exploded")},
			beadTerminal:   false,
			hasWork:        true,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "NEEDS_RECOVERY",
			wantMQStatus:   "unknown",
			wantNeedsRecov: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := RecoveryStatus{
				Verdict: tt.initialVerdict,
				Branch:  "polecat/test",
			}
			applyMQCheck(&status, tt.finder, tt.beadTerminal, tt.hasWork, tt.mqNotRequired)

			if status.Verdict != tt.wantVerdict {
				t.Errorf("Verdict = %q, want %q", status.Verdict, tt.wantVerdict)
			}
			if status.MQStatus != tt.wantMQStatus {
				t.Errorf("MQStatus = %q, want %q", status.MQStatus, tt.wantMQStatus)
			}
			if status.NeedsRecovery != tt.wantNeedsRecov {
				t.Errorf("NeedsRecovery = %v, want %v", status.NeedsRecovery, tt.wantNeedsRecov)
			}
		})
	}
}

func TestIsMQNotRequiredSource(t *testing.T) {
	tests := []struct {
		name  string
		issue *beads.Issue
		err   error
		want  bool
	}{
		{
			name:  "no merge source",
			issue: &beads.Issue{Description: beads.FormatAttachmentFields(&beads.AttachmentFields{NoMerge: true})},
			want:  true,
		},
		{
			name:  "review only source",
			issue: &beads.Issue{Description: beads.FormatAttachmentFields(&beads.AttachmentFields{ReviewOnly: true})},
			want:  true,
		},
		{
			name:  "local merge strategy source",
			issue: &beads.Issue{Description: beads.FormatAttachmentFields(&beads.AttachmentFields{MergeStrategy: "local"})},
			want:  true,
		},
		{
			name:  "normal merge queue source",
			issue: &beads.Issue{Description: beads.FormatAttachmentFields(&beads.AttachmentFields{MergeStrategy: "mr"})},
			want:  false,
		},
		{
			name: "missing source is conservative",
			err:  beads.ErrNotFound,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isMQNotRequiredSource(fakeIssueShower{issue: tt.issue, err: tt.err}, "gt-test")
			if got != tt.want {
				t.Errorf("isMQNotRequiredSource() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCleanupStatusBlocker(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{status: "clean", want: ""},
		{status: "has_unpushed", want: "cleanup_status=has_unpushed"},
		{status: "unknown", want: "cleanup_status=unknown"},
		{status: "", want: "cleanup_status=<missing>"},
		{status: "weird", want: "cleanup_status=weird"},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := cleanupStatusBlocker(polecat.CleanupStatus(tt.status))
			if got != tt.want {
				t.Errorf("cleanupStatusBlocker(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestCleanupStatusBlockerForRecovery_PartialSpawnWithoutHook(t *testing.T) {
	tests := []struct {
		name         string
		status       polecat.CleanupStatus
		partialSpawn bool
		want         string
	}{
		{name: "missing cleanup is safe for partial spawn", partialSpawn: true, want: ""},
		{name: "unknown cleanup is safe for partial spawn", status: polecat.CleanupUnknown, partialSpawn: true, want: ""},
		{name: "dirty cleanup still blocks partial spawn", status: polecat.CleanupUnpushed, partialSpawn: true, want: "cleanup_status=has_unpushed"},
		{name: "missing cleanup still blocks ordinary polecat", partialSpawn: false, want: "cleanup_status=<missing>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanupStatusBlockerForRecovery(tt.status, tt.partialSpawn)
			if got != tt.want {
				t.Errorf("cleanupStatusBlockerForRecovery() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStaleCleanupStatusCanBeIgnoredForRecovery(t *testing.T) {
	tests := []struct {
		name         string
		status       polecat.CleanupStatus
		workTerminal bool
		hookSafe     bool
		activeMRSafe bool
		gitSafe      bool
		wantCanSkip  bool
	}{
		{
			name:         "closed source with clean git ignores stale unpushed cleanup",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      true,
			wantCanSkip:  true,
		},
		{
			name:         "open source still blocks",
			status:       polecat.CleanupUnpushed,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      true,
		},
		{
			name:         "hooked work still blocks",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			activeMRSafe: true,
			gitSafe:      true,
		},
		{
			name:         "active MR still blocks",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			hookSafe:     true,
			gitSafe:      true,
		},
		{
			name:         "dirty git still blocks",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
		},
		{
			name:         "git error still blocks",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
		},
		{
			name:         "non-unpushed cleanup still blocks",
			status:       polecat.CleanupStash,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      true,
		},
		{
			name:         "terminal hook can satisfy work terminal predicate",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      true,
			wantCanSkip:  true,
		},
		// gs-9wz: a stalled-debris polecat that died before gt done recorded a
		// cleanup_status carries an empty/unknown report. When the live predicates
		// prove no work is at risk, that missing report must be ignorable so
		// `gt polecat nuke` (no --force) can reclaim it.
		{
			name:         "empty cleanup with all predicates safe is ignorable (gs-9wz)",
			status:       "",
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      true,
			wantCanSkip:  true,
		},
		{
			name:         "unknown cleanup with all predicates safe is ignorable (gs-9wz)",
			status:       polecat.CleanupUnknown,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      true,
			wantCanSkip:  true,
		},
		{
			name:         "empty cleanup still blocks when git not safe",
			status:       "",
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      false,
		},
		{
			name:         "empty cleanup still blocks with open hook",
			status:       "",
			workTerminal: true,
			hookSafe:     false,
			activeMRSafe: true,
			gitSafe:      true,
		},
		{
			name:         "empty cleanup still blocks with non-terminal work",
			status:       "",
			workTerminal: false,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      true,
		},
		{
			name:         "empty cleanup still blocks with pending MR",
			status:       "",
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: false,
			gitSafe:      true,
		},
		// gs-048: a has_uncommitted self-report over benign runtime/.beads churn is
		// ignorable when the live gitSafe check (CleanExcludingRuntime) proves no
		// real source dirt, stash, or unpushed work remains. gitSafe==true is the
		// authoritative proof; the stale positive report is then moot.
		{
			name:         "has_uncommitted with gitSafe (benign runtime churn) is ignorable (gs-048)",
			status:       polecat.CleanupUncommitted,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      true,
			wantCanSkip:  true,
		},
		{
			name:         "has_uncommitted still blocks when git not safe (real source dirt)",
			status:       polecat.CleanupUncommitted,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      false,
		},
		// has_stash stays non-overridable even with every predicate safe — a stash
		// is durable work and too strong a signal to discard on a live check.
		{
			name:         "has_stash stays blocked even when gitSafe (gs-048 scope guard)",
			status:       polecat.CleanupStash,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := staleCleanupStatusCanBeIgnoredForRecovery(tt.status, tt.workTerminal, tt.hookSafe, tt.activeMRSafe, tt.gitSafe)
			if got != tt.wantCanSkip {
				t.Fatalf("staleCleanupStatusCanBeIgnoredForRecovery() = %v, want %v", got, tt.wantCanSkip)
			}
		})
	}
}

func TestReconcileCleanupStatusIfSafe(t *testing.T) {
	status := &RecoveryStatus{
		CleanupStatus: polecat.CleanupUnpushed,
		Verdict:       "SAFE_TO_NUKE",
		Branch:        "polecat/nitro",
		MQStatus:      "submitted",
	}
	updater := &fakeCleanupUpdater{}
	reconcileCleanupStatusIfSafe(status, updater, "gt-gastown-polecat-nitro", &polecat.Polecat{State: polecat.StateIdle}, &beads.AgentFields{
		AgentState:    string(beads.AgentStateIdle),
		CleanupStatus: string(polecat.CleanupUnpushed),
	})

	if updater.calls != 1 {
		t.Fatalf("UpdateAgentCleanupStatus calls = %d, want 1", updater.calls)
	}
	if updater.id != "gt-gastown-polecat-nitro" || updater.status != string(polecat.CleanupClean) {
		t.Fatalf("update = (%q, %q), want clean update for agent", updater.id, updater.status)
	}
	if status.CleanupStatus != polecat.CleanupClean || !status.Reconciled {
		t.Fatalf("status after reconcile = (%q, reconciled=%v), want clean true", status.CleanupStatus, status.Reconciled)
	}
}

func TestReconcileCleanupStatusIfSafe_FailsClosed(t *testing.T) {
	status := &RecoveryStatus{
		CleanupStatus: polecat.CleanupUnpushed,
		Verdict:       "SAFE_TO_NUKE",
		Branch:        "polecat/nitro",
		MQStatus:      "submitted",
	}
	reconcileCleanupStatusIfSafe(status, &fakeCleanupUpdater{err: errors.New("bd update failed")}, "gt-gastown-polecat-nitro", &polecat.Polecat{State: polecat.StateIdle}, &beads.AgentFields{
		AgentState:    string(beads.AgentStateIdle),
		CleanupStatus: string(polecat.CleanupUnpushed),
	})

	if status.Verdict != "NEEDS_RECOVERY" || !status.NeedsRecovery {
		t.Fatalf("failed update verdict = %q needs=%v, want NEEDS_RECOVERY true", status.Verdict, status.NeedsRecovery)
	}
	if len(status.Blockers) == 0 || !strings.Contains(status.Blockers[0], "cleanup_reconcile_failed") {
		t.Fatalf("blockers = %v, want cleanup_reconcile_failed", status.Blockers)
	}
}

func TestCleanupStatusReconcileCandidateRequiresStrictPredicates(t *testing.T) {
	baseStatus := &RecoveryStatus{Verdict: "SAFE_TO_NUKE", Branch: "polecat/nitro", MQStatus: "submitted"}
	basePolecat := &polecat.Polecat{State: polecat.StateIdle}
	baseFields := &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupUnpushed)}

	tests := []struct {
		name   string
		status *RecoveryStatus
		p      *polecat.Polecat
		fields *beads.AgentFields
	}{
		{name: "stale clean is not rewritten", status: baseStatus, p: basePolecat, fields: &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)}},
		{name: "working polecat blocks", status: baseStatus, p: &polecat.Polecat{State: polecat.StateWorking}, fields: baseFields},
		{name: "working agent bead blocks", status: baseStatus, p: basePolecat, fields: &beads.AgentFields{AgentState: string(beads.AgentStateWorking), CleanupStatus: string(polecat.CleanupUnpushed)}},
		{name: "needs recovery blocks", status: &RecoveryStatus{Verdict: "NEEDS_RECOVERY", NeedsRecovery: true, Branch: "polecat/nitro", MQStatus: "submitted"}, p: basePolecat, fields: baseFields},
		{name: "unknown mq blocks", status: &RecoveryStatus{Verdict: "SAFE_TO_NUKE", Branch: "polecat/nitro", MQStatus: "unknown"}, p: basePolecat, fields: baseFields},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := cleanupStatusReconcileCandidate(tt.status, tt.p, tt.fields); ok {
				t.Fatal("cleanupStatusReconcileCandidate() allowed unsafe reconciliation")
			}
		})
	}
}

func TestHookBeadSafeForCleanup(t *testing.T) {
	tests := []struct {
		name         string
		hookBead     string
		owner        string
		bd           issueShower
		wantSafe     bool
		wantTerminal bool
		wantBlocker  string
	}{
		{name: "empty hook", wantSafe: true},
		{name: "terminal hook", hookBead: "gt-work", bd: fakeIssueShower{issue: &beads.Issue{Status: "closed"}}, wantSafe: true, wantTerminal: true},
		{name: "open hook blocks", hookBead: "gt-work", owner: "gastown/polecats/nux", bd: fakeIssueShower{issue: &beads.Issue{Status: "open", Assignee: "gastown/polecats/nux"}}, wantBlocker: "hook_bead=gt-work status=open"},
		{name: "lookup error blocks", hookBead: "gt-work", bd: fakeIssueShower{err: errors.New("bd exploded")}, wantBlocker: "lookup_error"},
		// gu-l4gl5: non-terminal hook reassigned to a different owner is a stale
		// worktree pointer, not work-at-risk — safe (but not terminal).
		{name: "open hook reassigned is stale", hookBead: "gt-work", owner: "gastown/polecats/nux", bd: fakeIssueShower{issue: &beads.Issue{Status: "open", Assignee: "gastown/polecats/slit"}}, wantSafe: true},
		// No assignee cannot prove reassignment, so it still blocks.
		{name: "open hook no assignee blocks", hookBead: "gt-work", owner: "gastown/polecats/nux", bd: fakeIssueShower{issue: &beads.Issue{Status: "open"}}, wantBlocker: "hook_bead=gt-work status=open"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSafe, gotTerminal, blocker := hookBeadSafeForCleanup(tt.bd, tt.hookBead, tt.owner)
			if gotSafe != tt.wantSafe || gotTerminal != tt.wantTerminal {
				t.Fatalf("hookBeadSafeForCleanup() = (%v, %v), want (%v, %v)", gotSafe, gotTerminal, tt.wantSafe, tt.wantTerminal)
			}
			if tt.wantBlocker != "" && !strings.Contains(blocker, tt.wantBlocker) {
				t.Fatalf("blocker = %q, want contains %q", blocker, tt.wantBlocker)
			}
		})
	}
}

func TestPartialSpawnWithoutDurableHook(t *testing.T) {
	assignee := "gastown/polecats/nitro"
	tests := []struct {
		name         string
		fields       *beads.AgentFields
		currentIssue string
		issue        *beads.Issue
		wantPartial  bool
	}{
		{
			name:        "spawning legacy hook points to open unassigned bead",
			fields:      &beads.AgentFields{AgentState: "spawning", HookBead: "gt-work"},
			issue:       &beads.Issue{ID: "gt-work", Status: "open"},
			wantPartial: true,
		},
		{
			name:   "durably hooked bead is not partial",
			fields: &beads.AgentFields{AgentState: "spawning", HookBead: "gt-work"},
			issue:  &beads.Issue{ID: "gt-work", Status: beads.StatusHooked, Assignee: assignee},
		},
		{
			name:         "current issue already found is not partial",
			fields:       &beads.AgentFields{AgentState: "spawning", HookBead: "gt-work"},
			currentIssue: "gt-work",
			issue:        &beads.Issue{ID: "gt-work", Status: "open"},
		},
		{
			name:   "working state is not partial spawn",
			fields: &beads.AgentFields{AgentState: "working", HookBead: "gt-work"},
			issue:  &beads.Issue{ID: "gt-work", Status: "open"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, diagnostic := partialSpawnWithoutDurableHook(fakeIssueShower{issue: tt.issue}, tt.fields, assignee, tt.currentIssue)
			if got != tt.wantPartial {
				t.Fatalf("partialSpawnWithoutDurableHook() = %v, want %v", got, tt.wantPartial)
			}
			if got && !strings.Contains(diagnostic, "partial_spawn_without_durable_hook") {
				t.Fatalf("diagnostic missing partial spawn marker: %q", diagnostic)
			}
		})
	}
}

func TestRecoveryGitStateBlocker(t *testing.T) {
	tests := []struct {
		name  string
		state *GitState
		err   error
		want  string
	}{
		{
			name:  "clean has no blocker",
			state: &GitState{Clean: true},
		},
		{
			name:  "uncommitted work is classified",
			state: &GitState{UncommittedFiles: []string{"a.go", "b.go"}},
			want:  "git_state=has_uncommitted uncommitted_files=2",
		},
		{
			name:  "stash is classified",
			state: &GitState{StashCount: 1},
			want:  "git_state=has_stash stash_count=1",
		},
		{
			name:  "unpushed commits are classified",
			state: &GitState{UnpushedCommits: 3},
			want:  "git_state=has_unpushed unpushed_commits=3",
		},
		{
			name: "git error is classified",
			err:  errors.New("git failed"),
			want: "git_state=unknown path=/tmp/polecat: git failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := recoveryGitStateBlocker("/tmp/polecat", tt.state, tt.err)
			if got != tt.want {
				t.Errorf("recoveryGitStateBlocker() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStaleCleanWithRealUnpushedStillBlocks(t *testing.T) {
	status := RecoveryStatus{CleanupStatus: polecat.CleanupClean}
	if blocker := recoveryGitStateBlocker("/tmp/polecat", &GitState{UnpushedCommits: 1}, nil); blocker != "" {
		status.Blockers = append(status.Blockers, blocker)
	}
	if len(status.Blockers) != 1 || !strings.Contains(status.Blockers[0], "git_state=has_unpushed") {
		t.Fatalf("blockers = %v, want git_state=has_unpushed", status.Blockers)
	}
}

// TestUnpushedCommitsAreSquashMergeRedundant is the gu-5dq1d regression guard:
// the signature is nonzero UnpushedCommits (gu-7nrd unreachable-from-remotes
// trap fired) but UnpreservedPatchCount==0 with a real ComparisonBase, a clean
// tree, and no stash — i.e. redundant squash-merged commit objects whose work
// is fully preserved on the base. Any other shape (real unpreserved patch,
// missing base, dirty tree, stash) must NOT match.
func TestUnpushedCommitsAreSquashMergeRedundant(t *testing.T) {
	tests := []struct {
		name  string
		state *GitState
		want  bool
	}{
		{
			name:  "squash-merge redundant signature matches",
			state: &GitState{UnpushedCommits: 1, UnpreservedPatchCount: 0, ComparisonBase: "origin/main"},
			want:  true,
		},
		{
			name:  "nil state does not match",
			state: nil,
			want:  false,
		},
		{
			name:  "zero unpushed does not match",
			state: &GitState{UnpushedCommits: 0, ComparisonBase: "origin/main"},
			want:  false,
		},
		{
			name:  "unpreserved patch (real divergent work) does not match",
			state: &GitState{UnpushedCommits: 1, UnpreservedPatchCount: 1, ComparisonBase: "origin/main"},
			want:  false,
		},
		{
			name:  "no comparison base does not match",
			state: &GitState{UnpushedCommits: 1, UnpreservedPatchCount: 0, ComparisonBase: ""},
			want:  false,
		},
		{
			name:  "stash present does not match",
			state: &GitState{UnpushedCommits: 1, UnpreservedPatchCount: 0, ComparisonBase: "origin/main", StashCount: 1},
			want:  false,
		},
		{
			name:  "uncommitted files present does not match",
			state: &GitState{UnpushedCommits: 1, UnpreservedPatchCount: 0, ComparisonBase: "origin/main", UncommittedFiles: []string{"a.go"}},
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unpushedCommitsAreSquashMergeRedundant(tt.state); got != tt.want {
				t.Errorf("unpushedCommitsAreSquashMergeRedundant(%+v) = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}

// TestApplyGitStateToWorkstateInput_SquashMergeSuppression proves the
// gu-5dq1d end-to-end policy: with suppressUnpushed=true the redundant
// squash-merge "unpushed" delta is dropped so DecideWorkstate yields
// SAFE_TO_NUKE, while suppressUnpushed=false (or a real unpushed delta) still
// blocks with NEEDS_RECOVERY. This is the safety boundary — suppression must
// flip the verdict ONLY when the caller has already proven the work terminal.
func TestApplyGitStateToWorkstateInput_SquashMergeSuppression(t *testing.T) {
	redundant := &GitState{UnpushedCommits: 1, UnpreservedPatchCount: 0, ComparisonBase: "origin/main"}

	t.Run("suppressed redundant unpushed is safe to nuke", func(t *testing.T) {
		input := polecat.WorkstateInput{State: polecat.StateIdle, CleanupStatus: polecat.CleanupClean, Branch: "polecat/foo"}
		applyGitStateToWorkstateInput(&input, "/tmp/polecat", redundant, nil, true)
		if input.UnpushedCommits != 0 {
			t.Fatalf("UnpushedCommits = %d, want 0 (suppressed)", input.UnpushedCommits)
		}
		if d := polecat.DecideWorkstate(input); d.Verdict != polecat.WorkstateVerdictSafeToNuke {
			t.Fatalf("verdict = %q, want SAFE_TO_NUKE (blockers=%v)", d.Verdict, d.Blockers)
		}
	})

	t.Run("unsuppressed redundant unpushed still blocks", func(t *testing.T) {
		input := polecat.WorkstateInput{State: polecat.StateIdle, CleanupStatus: polecat.CleanupClean, Branch: "polecat/foo"}
		applyGitStateToWorkstateInput(&input, "/tmp/polecat", redundant, nil, false)
		if input.UnpushedCommits != 1 {
			t.Fatalf("UnpushedCommits = %d, want 1 (not suppressed)", input.UnpushedCommits)
		}
		if d := polecat.DecideWorkstate(input); d.Verdict != polecat.WorkstateVerdictNeedsRecovery {
			t.Fatalf("verdict = %q, want NEEDS_RECOVERY", d.Verdict)
		}
	})
}

func TestActiveMRBlocker(t *testing.T) {
	tests := []struct {
		name       string
		mrID       string
		sourceHint string
		bd         issueShower
		want       string
	}{
		{name: "empty", want: ""},
		{name: "closed terminal source", mrID: "mr-1", sourceHint: "gt-closed", bd: fakeIssueMapShower{issues: map[string]*beads.Issue{"mr-1": &beads.Issue{ID: "mr-1", Status: "closed"}, "gt-closed": &beads.Issue{ID: "gt-closed", Status: "closed"}}}, want: ""},
		{name: "closed unknown source", mrID: "mr-1", bd: fakeIssueMapShower{issues: map[string]*beads.Issue{"mr-1": &beads.Issue{ID: "mr-1", Status: "closed"}}}, want: "active_mr=mr-1 status=closed source_issue=<missing>"},
		{name: "open", mrID: "mr-1", bd: fakeIssueShower{issue: &beads.Issue{ID: "mr-1", Status: "open"}}, want: "active_mr=mr-1 status=open"},
		{name: "missing terminal source", mrID: "mr-1", sourceHint: "gt-closed", bd: fakeIssueMapShower{issues: map[string]*beads.Issue{"gt-closed": &beads.Issue{ID: "gt-closed", Status: "closed"}}}, want: ""},
		{name: "missing unknown source", mrID: "mr-1", bd: fakeIssueMapShower{}, want: "active_mr=mr-1 status=missing source_issue=<missing>"},
		{name: "nil issue unknown source", mrID: "mr-1", bd: fakeIssueShower{issue: nil}, want: "active_mr=mr-1 status=missing source_issue=<missing>"},
		{name: "nil reader", mrID: "mr-1", bd: nil, want: "active_mr=mr-1 status=unverified"},
		{name: "lookup error", mrID: "mr-1", bd: fakeIssueShower{err: errors.New("bd exploded")}, want: "active_mr=mr-1 status=lookup_error: bd exploded"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := activeMRBlocker(tt.bd, tt.mrID, tt.sourceHint, false, false)
			if got != tt.want {
				t.Errorf("activeMRBlocker() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatSafetyCheckBlockers(t *testing.T) {
	blocked := []*SafetyCheckResult{
		{Polecat: "gastown/fury", Reasons: []string{"cleanup_status=unknown", "active_mr=hq-wisp-1 status=open"}},
		{Polecat: "gastown/rust", Reasons: []string{"has work on hook (gt-abc)"}},
	}

	got := formatSafetyCheckBlockers(blocked)
	want := "gastown/fury: cleanup_status=unknown; active_mr=hq-wisp-1 status=open | gastown/rust: has work on hook (gt-abc)"
	if got != want {
		t.Errorf("formatSafetyCheckBlockers() = %q, want %q", got, want)
	}
}

func TestDisplaySafetyCheckBlockedToIncludesPredicates(t *testing.T) {
	var buf bytes.Buffer
	displaySafetyCheckBlockedTo(&buf, []*SafetyCheckResult{{
		Polecat: "gastown/fury",
		Reasons: []string{"cleanup_status=unknown", "active_mr=hq-wisp-1 status=open"},
	}})
	out := buf.String()
	for _, want := range []string{
		"Cannot nuke",
		"gastown/fury",
		"cleanup_status=unknown",
		"active_mr=hq-wisp-1 status=open",
		"Force nuke (LOSES WORK)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("displaySafetyCheckBlockedTo() missing %q in %q", want, out)
		}
	}
}

func TestDryRunNukeSummary(t *testing.T) {
	tests := []struct {
		name    string
		total   int
		blocked int
		want    string
	}{
		{name: "safe", total: 2, want: "Would nuke 2 polecat(s)."},
		{name: "blocked", total: 2, blocked: 1, want: "Would refuse to nuke 1 of 2 polecat(s) without --force."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dryRunNukeSummary(tt.total, tt.blocked); got != tt.want {
				t.Errorf("dryRunNukeSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHasSubmittableWorkForRecoveryUsesUpstream(t *testing.T) {
	repo := setupRecoveryGitRepo(t)

	if got := hasSubmittableWorkForRecovery(repo, nil, &GitState{UnpushedCommits: 99}, nil); got {
		t.Fatal("branch with no commits ahead of its upstream should not require MQ submission")
	}

	writeRecoveryFile(t, filepath.Join(repo, "change.txt"), "change")
	runGit(t, repo, "add", "change.txt")
	runGit(t, repo, "commit", "-m", "change")

	if got := hasSubmittableWorkForRecovery(repo, nil, &GitState{}, nil); !got {
		t.Fatal("branch with commits ahead of its upstream should require MQ submission")
	}
}

func TestHasSubmittableWorkForRecoveryIgnoresSelfUpstream(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	runGit(t, repo, "switch", "-c", "polecat/test")
	writeRecoveryFile(t, filepath.Join(repo, "feature.txt"), "feature")
	runGit(t, repo, "add", "feature.txt")
	runGit(t, repo, "commit", "-m", "feature")
	runGit(t, repo, "push", "-u", "origin", "polecat/test")

	if got := hasSubmittableWorkForRecovery(repo, nil, &GitState{UnpushedCommits: 1}, nil); !got {
		t.Fatal("self-upstream feature branch should fall back and preserve MQ requirement")
	}
}

func TestHasSubmittableWorkForRecoveryIgnoresPatchEquivalentBranch(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	runGit(t, repo, "switch", "-c", "polecat/equivalent")
	writeRecoveryFile(t, filepath.Join(repo, "equiv.txt"), "equiv")
	runGit(t, repo, "add", "equiv.txt")
	runGit(t, repo, "commit", "-m", "equiv")
	runGit(t, repo, "switch", "integration/test")
	writeRecoveryFile(t, filepath.Join(repo, "other.txt"), "other")
	runGit(t, repo, "add", "other.txt")
	runGit(t, repo, "commit", "-m", "other")
	runGit(t, repo, "cherry-pick", "polecat/equivalent")
	runGit(t, repo, "push", "origin", "integration/test")
	runGit(t, repo, "switch", "polecat/equivalent")
	runGit(t, repo, "branch", "--set-upstream-to=origin/integration/test")

	if got := hasSubmittableWorkForRecovery(repo, nil, &GitState{UnpushedCommits: 99}, nil); got {
		t.Fatal("patch-equivalent branch should not require MQ submission")
	}
}

func TestHasSubmittableWorkForRecoveryUsesExplicitTargetAncestor(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	runGit(t, repo, "switch", "-c", "polecat/contained")
	writeRecoveryFile(t, filepath.Join(repo, "contained.txt"), "contained")
	runGit(t, repo, "add", "contained.txt")
	runGit(t, repo, "commit", "-m", "contained")
	runGit(t, repo, "switch", "integration/test")
	runGit(t, repo, "merge", "--ff-only", "polecat/contained")
	runGit(t, repo, "push", "origin", "integration/test")
	runGit(t, repo, "switch", "polecat/contained")

	if got := hasSubmittableWorkForRecovery(repo, []string{"integration/test"}, &GitState{UnpushedCommits: 99}, nil); got {
		t.Fatal("branch whose HEAD is contained by explicit target should not require MQ submission")
	}
}

func TestHasSubmittableWorkForRecoveryUsesExplicitTargetCherry(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	runGit(t, repo, "switch", "-c", "polecat/cherry")
	writeRecoveryFile(t, filepath.Join(repo, "cherry.txt"), "cherry")
	runGit(t, repo, "add", "cherry.txt")
	runGit(t, repo, "commit", "-m", "cherry")
	runGit(t, repo, "switch", "integration/test")
	writeRecoveryFile(t, filepath.Join(repo, "target.txt"), "target")
	runGit(t, repo, "add", "target.txt")
	runGit(t, repo, "commit", "-m", "advance target")
	runGit(t, repo, "cherry-pick", "polecat/cherry")
	runGit(t, repo, "push", "origin", "integration/test")
	runGit(t, repo, "switch", "polecat/cherry")

	if got := hasSubmittableWorkForRecovery(repo, []string{"integration/test"}, &GitState{UnpushedCommits: 99}, nil); got {
		t.Fatal("patch-equivalent branch on advanced explicit target should not require MQ submission")
	}
}

func TestHasSubmittableWorkForRecoveryKeepsExplicitTargetUniquePatch(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	runGit(t, repo, "switch", "-c", "polecat/unique")
	writeRecoveryFile(t, filepath.Join(repo, "unique.txt"), "unique")
	runGit(t, repo, "add", "unique.txt")
	runGit(t, repo, "commit", "-m", "unique")

	if got := hasSubmittableWorkForRecovery(repo, []string{"integration/test"}, &GitState{}, nil); !got {
		t.Fatal("unique patch absent from explicit target should require MQ submission")
	}
}

func TestHasSubmittableWorkForRecoveryFallback(t *testing.T) {
	if got := hasSubmittableWorkForRecovery("/does/not/exist", nil, &GitState{UnpushedCommits: 0}, nil); got {
		t.Fatal("clean fallback git state should not require MQ submission")
	}
	if got := hasSubmittableWorkForRecovery("/does/not/exist", nil, &GitState{UnpushedCommits: 1}, nil); !got {
		t.Fatal("unpushed fallback git state should require MQ submission")
	}
	if got := hasSubmittableWorkForRecovery("/does/not/exist", nil, nil, errors.New("git failed")); !got {
		t.Fatal("git-state error fallback should remain conservative")
	}
}

func setupRecoveryGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	repo := filepath.Join(root, "repo")
	runCmd(t, root, "git", "init", "--bare", remote)
	runCmd(t, root, "git", "init", repo)
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	writeRecoveryFile(t, filepath.Join(repo, "README.md"), "base")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "base")
	runGit(t, repo, "branch", "-M", "main")
	runGit(t, repo, "remote", "add", "origin", remote)
	runGit(t, repo, "push", "-u", "origin", "main")
	runGit(t, repo, "switch", "-c", "integration/test")
	runGit(t, repo, "push", "-u", "origin", "integration/test")
	return repo
}

func writeRecoveryFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

// runCmd is the workhorse used by runGit (declared in polecat_test.go) for
// non-git invocations. The git wrapper itself lives in polecat_test.go so
// both files share a single definition with identical author/committer env.
func runCmd(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

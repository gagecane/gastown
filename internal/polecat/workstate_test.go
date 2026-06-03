package polecat

import "testing"

func TestDecideWorkstateCanonicalFields(t *testing.T) {
	tests := []struct {
		name string
		in   WorkstateInput
		want WorkstateDisposition
	}{
		{
			name: "clean idle is reusable and safe",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "main"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, ReuseStatus: "idle-clean"},
		},
		{
			name: "dirty idle needs recovery and capacity",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupUnpushed},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "cleanup-has_unpushed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
		{
			name: "unsubmitted branch needs mq submit",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, HasSubmittableWork: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsMQSubmit, Reason: "mq-not-submitted", NeedsRecovery: true, NeedsMQSubmit: true, MQStatus: "not_submitted", CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
		{
			name: "terminal source makes mq submitted",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, MQStatus: "submitted", ReuseStatus: "idle-preserved"},
		},
		{
			name: "terminal active mr does not block when gatherer omits blocker",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, ActiveMR: "gt-mr-closed"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, ReuseStatus: "idle-clean"},
		},
		{
			name: "open active mr is preserved pending mr",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, ActiveMR: "gt-mr-open", ActiveMRBlocker: "active_mr=gt-mr-open status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictPendingMR, Reason: "active-mr-open", ReuseStatus: "idle-pr-open"},
		},
		{
			name: "working counts as working capacity",
			in:   WorkstateInput{State: StateWorking, CleanupStatus: CleanupClean},
			want: WorkstateDisposition{Verdict: WorkstateVerdictWorking, Reason: "not-idle", NeedsRecovery: false, CountsTowardCapacity: true},
		},
		{
			name: "stalled completion with open mr and clean delta is pending mr not recovery",
			in:   WorkstateInput{State: StateStalled, CleanupStatus: CleanupClean, ActiveMR: "gt-mr-open", ActiveMRBlocker: "active_mr=gt-mr-open status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictPendingMR, Reason: "active-mr-open", ReuseStatus: "idle-pr-open"},
		},
		{
			// gu-54r7b: at-risk delta on a stalled polecat now falls through to the
			// shared predicate block, so blockers are NAMED instead of returning a
			// blocker-less "not-idle" NEEDS_RECOVERY ("unknown recovery predicate").
			name: "stalled with open mr but unpushed commits still needs recovery",
			in:   WorkstateInput{State: StateStalled, CleanupStatus: CleanupClean, ActiveMR: "gt-mr-open", ActiveMRBlocker: "active_mr=gt-mr-open status=open", UnpushedCommits: 2},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "git-unpushed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
		{
			name: "stalled with open mr but dirty tree still needs recovery",
			in:   WorkstateInput{State: StateStalled, CleanupStatus: CleanupClean, ActiveMR: "gt-mr-open", ActiveMRBlocker: "active_mr=gt-mr-open status=open", GitDirty: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "git-dirty", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
		{
			// gu-54r7b: the core regression. A stalled polecat with a verifiably
			// clean tree and no MR used to return NEEDS_RECOVERY with empty blockers
			// ("unknown recovery predicate") and escalate to the Mayor. It must now
			// be SAFE_TO_NUKE — no work is at risk.
			name: "stalled clean tree without an open mr is safe to nuke",
			in:   WorkstateInput{State: StateStalled, CleanupStatus: CleanupClean},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, ReuseStatus: "idle-clean"},
		},
		{
			// gu-54r7b: a stalled polecat with a dirty tree and no MR names its
			// blocker (git-dirty) rather than the blocker-less "unknown predicate".
			name: "stalled dirty tree without an open mr names blocker",
			in:   WorkstateInput{State: StateStalled, CleanupStatus: CleanupClean, GitDirty: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "git-dirty", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
		{
			// gu-54r7b: a stalled polecat still holding a hook bead names that
			// blocker rather than escalating with an unknown predicate.
			name: "stalled with hook still set names blocker",
			in:   WorkstateInput{State: StateStalled, CleanupStatus: CleanupClean, HookBead: "gt-work"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "hook-still-set", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecideWorkstate(tt.in)
			if got.Verdict != tt.want.Verdict || got.Reason != tt.want.Reason || got.Reusable != tt.want.Reusable || got.SafeToNuke != tt.want.SafeToNuke || got.NeedsRecovery != tt.want.NeedsRecovery || got.NeedsMQSubmit != tt.want.NeedsMQSubmit || got.MQStatus != tt.want.MQStatus || got.CountsTowardCapacity != tt.want.CountsTowardCapacity || got.ReuseStatus != tt.want.ReuseStatus {
				t.Fatalf("DecideWorkstate() = %+v, want fields %+v", got, tt.want)
			}
		})
	}
}

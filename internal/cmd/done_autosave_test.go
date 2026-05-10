package cmd

import (
	"errors"
	"testing"
)

// fakeAutoSaveGit is a stub implementation of autoSaveGit for testing.
type fakeAutoSaveGit struct {
	detached    bool
	detErr      error
	detachCalls int
}

func (f *fakeAutoSaveGit) IsDetachedHEAD() (bool, error) {
	f.detachCalls++
	return f.detached, f.detErr
}

// TestAutoSaveRefusalReason_DetachedHEAD verifies the guard added in gu-h5pr:
// when HEAD is detached, the auto-save safety net must refuse, because a commit
// on detached HEAD would orphan the work and break the subsequent branch push.
func TestAutoSaveRefusalReason_DetachedHEAD(t *testing.T) {
	g := &fakeAutoSaveGit{detached: true}

	reason := autoSaveRefusalReason(g, "polecat/chrome/gu-h5pr--xyz", "main")
	if reason != "detached HEAD" {
		t.Errorf("autoSaveRefusalReason on detached HEAD = %q, want %q", reason, "detached HEAD")
	}
}

// TestAutoSaveRefusalReason_DefaultBranch verifies the gu-cfb guard:
// refuse auto-commit when the branch is the default (main/master).
func TestAutoSaveRefusalReason_DefaultBranch(t *testing.T) {
	tests := []struct {
		name          string
		branch        string
		defaultBranch string
		want          string
	}{
		{name: "main is default", branch: "main", defaultBranch: "main", want: "default branch"},
		{name: "master is default alias", branch: "master", defaultBranch: "main", want: "default branch"},
		{name: "custom default", branch: "trunk", defaultBranch: "trunk", want: "default branch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &fakeAutoSaveGit{detached: false}
			reason := autoSaveRefusalReason(g, tt.branch, tt.defaultBranch)
			if reason != tt.want {
				t.Errorf("autoSaveRefusalReason(%q, %q) = %q, want %q",
					tt.branch, tt.defaultBranch, reason, tt.want)
			}
		})
	}
}

// TestAutoSaveRefusalReason_Safe verifies the auto-save proceeds on a normal
// polecat branch with attached HEAD.
func TestAutoSaveRefusalReason_Safe(t *testing.T) {
	g := &fakeAutoSaveGit{detached: false}

	reason := autoSaveRefusalReason(g, "polecat/chrome/gu-h5pr--xyz", "main")
	if reason != "" {
		t.Errorf("autoSaveRefusalReason on safe branch = %q, want empty", reason)
	}
}

// TestAutoSaveRefusalReason_DetachedTakesPrecedence verifies detached-HEAD
// is checked before the default-branch guard. The relative order doesn't
// affect correctness (both are refusals) but the precedence is asserted
// so future refactors don't accidentally drop the detached check when the
// branch happens to be the default-branch literal.
func TestAutoSaveRefusalReason_DetachedTakesPrecedence(t *testing.T) {
	g := &fakeAutoSaveGit{detached: true}

	reason := autoSaveRefusalReason(g, "main", "main")
	if reason != "detached HEAD" {
		t.Errorf("autoSaveRefusalReason with detached + default branch = %q, want %q",
			reason, "detached HEAD")
	}
}

// TestAutoSaveRefusalReason_DetachedErrorIgnored documents the failure mode:
// when IsDetachedHEAD errors (cannot determine state), we fall through to
// other guards rather than blocking all auto-saves. Matches the inline code
// path in done.go which uses `if detached, detErr := ...; detErr == nil && detached`.
func TestAutoSaveRefusalReason_DetachedErrorIgnored(t *testing.T) {
	g := &fakeAutoSaveGit{detErr: errors.New("git symbolic-ref failed")}

	reason := autoSaveRefusalReason(g, "polecat/chrome/gu-h5pr--xyz", "main")
	if reason != "" {
		t.Errorf("autoSaveRefusalReason on IsDetachedHEAD error = %q, want empty (fall through)", reason)
	}
	if g.detachCalls != 1 {
		t.Errorf("IsDetachedHEAD called %d times, want 1", g.detachCalls)
	}
}

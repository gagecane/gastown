package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
)

func stubUncommittedWorkCheckDeps(
	t *testing.T,
	listFn func(*rig.Rig) ([]*polecat.Polecat, error),
	checkFn func(string) (*git.UncommittedWorkStatus, error),
	isTTYFn func() bool,
	promptFn func(string) bool,
) {
	t.Helper()

	oldList := listPolecatsForWorkCheck
	oldCheck := checkPolecatWorkStatus
	oldIsTTY := isStdinTerminal
	oldPrompt := promptYesNoUnsafeProceed

	listPolecatsForWorkCheck = listFn
	checkPolecatWorkStatus = checkFn
	isStdinTerminal = isTTYFn
	promptYesNoUnsafeProceed = promptFn

	t.Cleanup(func() {
		listPolecatsForWorkCheck = oldList
		checkPolecatWorkStatus = oldCheck
		isStdinTerminal = oldIsTTY
		promptYesNoUnsafeProceed = oldPrompt
	})
}

func testRig() *rig.Rig {
	return &rig.Rig{
		Name: "testrig",
		Path: "/tmp/testrig",
	}
}

func TestCheckUncommittedWork_ListErrorBlocksWithoutForce(t *testing.T) {
	stubUncommittedWorkCheckDeps(
		t,
		func(*rig.Rig) ([]*polecat.Polecat, error) {
			return nil, errors.New("list failed")
		},
		func(string) (*git.UncommittedWorkStatus, error) {
			t.Fatalf("check should not be called when list fails")
			return nil, nil
		},
		func() bool { return false },
		func(string) bool {
			t.Fatalf("prompt should not be called without --force")
			return false
		},
	)

	var proceed bool
	output := captureStdout(t, func() {
		proceed = checkUncommittedWork(testRig(), "testrig", "stop", false)
	})

	if proceed {
		t.Fatal("expected proceed=false when polecat listing fails without --force")
	}
	if !strings.Contains(output, "Could not check polecats for uncommitted work") {
		t.Fatalf("expected list-error warning, got: %q", output)
	}
	if !strings.Contains(output, "--force") || !strings.Contains(output, "--nuclear") {
		t.Fatalf("expected override hint, got: %q", output)
	}
}

func TestCheckUncommittedWork_ListErrorForceTTYPrompts(t *testing.T) {
	stubUncommittedWorkCheckDeps(
		t,
		func(*rig.Rig) ([]*polecat.Polecat, error) {
			return nil, errors.New("list failed")
		},
		func(string) (*git.UncommittedWorkStatus, error) {
			t.Fatalf("check should not be called when list fails")
			return nil, nil
		},
		func() bool { return true },
		func(question string) bool {
			if question != "Proceed anyway?" {
				t.Fatalf("unexpected prompt question: %q", question)
			}
			return true
		},
	)

	proceed := checkUncommittedWork(testRig(), "testrig", "shutdown", true)
	if !proceed {
		t.Fatal("expected proceed=true after force+TTY confirmation")
	}
}

func TestCheckUncommittedWork_PolecatStatusErrorBlocks(t *testing.T) {
	stubUncommittedWorkCheckDeps(
		t,
		func(*rig.Rig) ([]*polecat.Polecat, error) {
			return []*polecat.Polecat{
				{Name: "alpha", ClonePath: "/tmp/alpha"},
			}, nil
		},
		func(string) (*git.UncommittedWorkStatus, error) {
			return nil, errors.New("git status failed")
		},
		func() bool { return false },
		func(string) bool {
			t.Fatalf("prompt should not be called without --force")
			return false
		},
	)

	var proceed bool
	output := captureStdout(t, func() {
		proceed = checkUncommittedWork(testRig(), "testrig", "restart", false)
	})

	if proceed {
		t.Fatal("expected proceed=false when polecat status check fails")
	}
	if !strings.Contains(output, "Could not verify uncommitted work for") {
		t.Fatalf("expected status-check error warning, got: %q", output)
	}
	if !strings.Contains(output, "alpha") {
		t.Fatalf("expected polecat name in warning, got: %q", output)
	}
}

func TestCheckUncommittedWork_DirtyForceNonTTYBlocks(t *testing.T) {
	stubUncommittedWorkCheckDeps(
		t,
		func(*rig.Rig) ([]*polecat.Polecat, error) {
			return []*polecat.Polecat{
				{Name: "alpha", ClonePath: "/tmp/alpha"},
			}, nil
		},
		func(string) (*git.UncommittedWorkStatus, error) {
			return &git.UncommittedWorkStatus{
				HasUncommittedChanges: true,
				ModifiedFiles:         []string{"README.md"},
			}, nil
		},
		func() bool { return false },
		func(string) bool {
			t.Fatalf("prompt should not be called in non-TTY mode")
			return false
		},
	)

	var proceed bool
	output := captureStdout(t, func() {
		proceed = checkUncommittedWork(testRig(), "testrig", "stop", true)
	})

	if proceed {
		t.Fatal("expected proceed=false for force in non-TTY mode")
	}
	if !strings.Contains(output, "--force") || !strings.Contains(output, "interactive terminal") {
		t.Fatalf("expected non-TTY force hint, got: %q", output)
	}
}

// gu-oi0al: agent runtime state should not block rig shutdown/reboot.
func TestCheckUncommittedWork_RuntimeOnlyDirtIsClean(t *testing.T) {
	stubUncommittedWorkCheckDeps(
		t,
		func(*rig.Rig) ([]*polecat.Polecat, error) {
			return []*polecat.Polecat{
				{Name: "alpha", ClonePath: "/tmp/alpha"},
			}, nil
		},
		func(string) (*git.UncommittedWorkStatus, error) {
			// Realistic runtime-only churn from the gu-oi0al rollover incident:
			// beads database files, claude transcripts, runtime state — all
			// regenerated on demand and never represent work-in-progress.
			return &git.UncommittedWorkStatus{
				HasUncommittedChanges: true,
				ModifiedFiles:         []string{".beads/beads.db"},
				UntrackedFiles: []string{
					".claude/transcripts/foo.jsonl",
					".runtime/state.json",
				},
			}, nil
		},
		func() bool { return false },
		func(string) bool {
			t.Fatalf("prompt should not be called when only runtime dirt is present")
			return false
		},
	)

	var proceed bool
	output := captureStdout(t, func() {
		proceed = checkUncommittedWork(testRig(), "testrig", "reboot", false)
	})

	if !proceed {
		t.Fatalf("expected proceed=true when only runtime artifacts are dirty; output=%q", output)
	}
	if strings.Contains(output, "uncommitted work") {
		t.Fatalf("expected no uncommitted-work warning for runtime-only dirt; output=%q", output)
	}
}

// gu-oi0al: real source changes alongside runtime dirt must still block.
func TestCheckUncommittedWork_RealChangesBlockEvenWithRuntimeDirt(t *testing.T) {
	stubUncommittedWorkCheckDeps(
		t,
		func(*rig.Rig) ([]*polecat.Polecat, error) {
			return []*polecat.Polecat{
				{Name: "alpha", ClonePath: "/tmp/alpha"},
			}, nil
		},
		func(string) (*git.UncommittedWorkStatus, error) {
			return &git.UncommittedWorkStatus{
				HasUncommittedChanges: true,
				ModifiedFiles:         []string{"internal/cmd/rig.go"},
				UntrackedFiles:        []string{".beads/beads.db"},
			}, nil
		},
		func() bool { return false },
		func(string) bool {
			t.Fatalf("prompt should not be called without --force")
			return false
		},
	)

	var proceed bool
	output := captureStdout(t, func() {
		proceed = checkUncommittedWork(testRig(), "testrig", "reboot", false)
	})

	if proceed {
		t.Fatal("expected proceed=false when real source changes are uncommitted")
	}
	if !strings.Contains(output, "1 uncommitted change(s)") {
		t.Fatalf("expected blocking summary to count only the non-runtime change, got: %q", output)
	}
}

// gu-oi0al: stashes still indicate real work that survives reboot — must block
// even when file dirt is purely runtime artifacts.
func TestCheckUncommittedWork_StashBlocksEvenWithRuntimeOnlyFiles(t *testing.T) {
	stubUncommittedWorkCheckDeps(
		t,
		func(*rig.Rig) ([]*polecat.Polecat, error) {
			return []*polecat.Polecat{
				{Name: "alpha", ClonePath: "/tmp/alpha"},
			}, nil
		},
		func(string) (*git.UncommittedWorkStatus, error) {
			return &git.UncommittedWorkStatus{
				StashCount:     1,
				UntrackedFiles: []string{".beads/beads.db"},
			}, nil
		},
		func() bool { return false },
		func(string) bool {
			t.Fatalf("prompt should not be called without --force")
			return false
		},
	)

	var proceed bool
	output := captureStdout(t, func() {
		proceed = checkUncommittedWork(testRig(), "testrig", "reboot", false)
	})

	if proceed {
		t.Fatal("expected proceed=false when a stash is present")
	}
	if !strings.Contains(output, "1 stash(es)") {
		t.Fatalf("expected stash count in blocking summary, got: %q", output)
	}
}

func TestCheckUncommittedWork_DirtyForceTTYPrompts(t *testing.T) {
	stubUncommittedWorkCheckDeps(
		t,
		func(*rig.Rig) ([]*polecat.Polecat, error) {
			return []*polecat.Polecat{
				{Name: "alpha", ClonePath: "/tmp/alpha"},
			}, nil
		},
		func(string) (*git.UncommittedWorkStatus, error) {
			return &git.UncommittedWorkStatus{
				HasUncommittedChanges: true,
				ModifiedFiles:         []string{"README.md"},
			}, nil
		},
		func() bool { return true },
		func(question string) bool {
			if question != "Proceed anyway?" {
				t.Fatalf("unexpected prompt question: %q", question)
			}
			return true
		},
	)

	proceed := checkUncommittedWork(testRig(), "testrig", "stop", true)
	if !proceed {
		t.Fatal("expected proceed=true after force+TTY confirmation")
	}
}

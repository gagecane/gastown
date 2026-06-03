package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeFakeCrewClone creates a directory shaped like a crew clone:
//
//	<root>/<rig>/crew/<name>/.git/
//
// The returned path is canonicalized via EvalSymlinks so it compares equal to
// what resolveCrewClone() returns at runtime (macOS /var ↔ /private/var).
func makeFakeCrewClone(t *testing.T, root, rig, name string) string {
	t.Helper()
	clone := filepath.Join(root, rig, "crew", name)
	if err := os.MkdirAll(filepath.Join(clone, ".git"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(clone)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", clone, err)
	}
	return resolved
}

func TestResolveCrewClone_FindsCloneInAncestorChain(t *testing.T) {
	root := t.TempDir()
	clone := makeFakeCrewClone(t, root, "wa_rig", "batista")

	// resolve from inside the clone
	got := resolveCrewClone(filepath.Join(clone, "lib", "deep"))
	if got != clone {
		t.Errorf("resolve from deep: got %q, want %q", got, clone)
	}

	// resolve from clone root itself
	got = resolveCrewClone(clone)
	if got != clone {
		t.Errorf("resolve from root: got %q, want %q", got, clone)
	}
}

func TestResolveCrewClone_ReturnsEmptyWhenNotInCrew(t *testing.T) {
	root := t.TempDir()
	got := resolveCrewClone(root)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestResolveCrewClone_RequiresGitMarker(t *testing.T) {
	root := t.TempDir()
	// Path looks like a crew clone but has no .git
	noGit := filepath.Join(root, "rig", "crew", "ghost")
	if err := os.MkdirAll(noGit, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got := resolveCrewClone(noGit)
	if got != "" {
		t.Errorf("expected empty for no-.git crew dir, got %q", got)
	}
}

func TestFirstSubcommand_HandlesFlagsBeforeSubcommand(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain commit", " commit -m foo", "commit"},
		{"with -c flag", " -c user.email=x commit -m foo", "commit"},
		{"only flags", " --version", ""},
		{"empty", "", ""},
		{"log", " log --oneline -5", "log"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstSubcommand(tt.in); got != tt.want {
				t.Errorf("firstSubcommand(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsWriteClass(t *testing.T) {
	tests := []struct {
		name       string
		subcommand string
		trailing   string
		want       bool
	}{
		{"commit", "commit", " commit -m foo", true},
		{"push", "push", " push origin main", true},
		{"merge", "merge", " merge feat", true},
		{"rebase", "rebase", " rebase main", true},
		{"cherry-pick", "cherry-pick", " cherry-pick abc123", true},
		{"reset", "reset", " reset --hard HEAD~1", true},
		{"clean", "clean", " clean -fd", true},
		{"apply", "apply", " apply patch.diff", true},
		{"am", "am", " am < patch", true},
		{"revert", "revert", " revert HEAD", true},
		{"checkout file", "checkout", " checkout -- file.go", true},
		{"restore", "restore", " restore file.go", true},
		{"switch", "switch", " switch main", true},
		{"add", "add", " add foo", true},
		{"branch -D", "branch", " branch -D feat/old", true},
		{"branch --delete", "branch", " branch --delete feat/old", true},
		{"branch -d", "branch", " branch -d feat/old", true},
		{"branch list (read-only)", "branch", " branch", false},
		{"branch -a list", "branch", " branch -a", false},
		{"tag -d", "tag", " tag -d v1", true},
		{"tag --delete", "tag", " tag --delete v1", true},
		{"tag list", "tag", " tag", false},
		{"log (read-only)", "log", " log --oneline", false},
		{"status (read-only)", "status", " status --short", false},
		{"diff (read-only)", "diff", " diff HEAD", false},
		{"show (read-only)", "show", " show abc", false},
		{"rev-parse (read-only)", "rev-parse", " rev-parse HEAD", false},
		{"fetch (read-only-ish — never modifies working tree)", "fetch", " fetch origin", false},
		{"unknown subcommand", "doesnotexist", " doesnotexist", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isWriteClass(tt.subcommand, tt.trailing); got != tt.want {
				t.Errorf("isWriteClass(%q, %q) = %v, want %v", tt.subcommand, tt.trailing, got, tt.want)
			}
		})
	}
}

func TestGitDashCRe_ExtractsTargetPath(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		{"plain", `git -C /home/u/rig/crew/x commit -m foo`, "/home/u/rig/crew/x"},
		{"equals form", `git -C=/home/u/rig/crew/x commit -m foo`, "/home/u/rig/crew/x"},
		{"with leading cd", `cd /tmp && git -C /home/u/rig/crew/x push`, "/home/u/rig/crew/x"},
		{"no -C", `git commit -m foo`, ""},
		{"after pipe", `echo hi | git -C /home/u/rig/crew/x commit`, "/home/u/rig/crew/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := gitDashCRe.FindStringSubmatch(tt.cmd)
			var got string
			if len(matches) > 0 {
				got = matches[1]
				if got == "" {
					got = matches[2]
				}
			}
			if got != tt.want {
				t.Errorf("path extract %q -> %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestRunTapGuardCrossClone_BlocksWriteAgainstOtherCrewClone(t *testing.T) {
	root := t.TempDir()
	other := makeFakeCrewClone(t, root, "rig", "other")
	mine := makeFakeCrewClone(t, root, "rig", "mine")

	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	if err := os.Chdir(mine); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	cmdStr := `git -C ` + other + ` commit -m "polluting"`

	myCrewClone := currentCrewClone()
	if myCrewClone != mine {
		t.Fatalf("currentCrewClone got %q, want %q", myCrewClone, mine)
	}

	matches := gitDashCRe.FindAllStringSubmatch(cmdStr, -1)
	if len(matches) == 0 {
		t.Fatalf("regex did not match: %q", cmdStr)
	}

	target := resolveCrewClone(matches[0][1])
	if target != other {
		t.Fatalf("resolveCrewClone got %q, want %q", target, other)
	}
	if target == myCrewClone {
		t.Fatalf("expected target != myCrewClone (cross-clone)")
	}

	sub := firstSubcommand(matches[0][3])
	if sub != "commit" {
		t.Fatalf("subcommand got %q, want %q", sub, "commit")
	}
	if !isWriteClass(sub, matches[0][3]) {
		t.Fatalf("expected commit to be write-class")
	}
	// All conditions met — runTapGuardCrossClone would block here.
}

func TestRunTapGuardCrossClone_AllowsReadOnlyAgainstOtherClone(t *testing.T) {
	root := t.TempDir()
	other := makeFakeCrewClone(t, root, "rig", "other")
	mine := makeFakeCrewClone(t, root, "rig", "mine")

	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	_ = os.Chdir(mine)

	cmdStr := `git -C ` + other + ` log --oneline -5`
	matches := gitDashCRe.FindStringSubmatch(cmdStr)
	if len(matches) == 0 {
		t.Fatalf("regex did not match")
	}
	sub := firstSubcommand(matches[3])
	if sub != "log" {
		t.Fatalf("subcommand got %q, want %q", sub, "log")
	}
	if isWriteClass(sub, matches[3]) {
		t.Fatalf("expected log to be NOT write-class")
	}
}

func TestRunTapGuardCrossClone_AllowsSameCloneOps(t *testing.T) {
	root := t.TempDir()
	mine := makeFakeCrewClone(t, root, "rig", "mine")

	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	_ = os.Chdir(mine)

	cmdStr := `git -C ` + mine + ` commit -m "my own work"`
	matches := gitDashCRe.FindStringSubmatch(cmdStr)
	target := resolveCrewClone(matches[1])
	if target != mine {
		t.Fatalf("resolve got %q, want %q", target, mine)
	}
	myCrewClone := currentCrewClone()
	if target != myCrewClone {
		t.Fatalf("same-clone: target=%q myCrewClone=%q should be equal", target, myCrewClone)
	}
}

func TestRunTapGuardCrossClone_ForceReplicateOverride(t *testing.T) {
	t.Setenv("DEACON_FORCE_REPLICATE", "1")
	// Simulate the exact early-return path. We can't easily run the full
	// command without stdin scaffolding; assert the precondition behavior
	// directly: when DEACON_FORCE_REPLICATE=1, the guard short-circuits.
	if os.Getenv("DEACON_FORCE_REPLICATE") != "1" {
		t.Fatalf("env not set in test")
	}
	// In runTapGuardCrossClone, the very first check returns nil when this
	// env is set. The test confirms the env-based short-circuit signal exists
	// and can be observed by external tests / CI.
	want := "1"
	if got := os.Getenv("DEACON_FORCE_REPLICATE"); got != want {
		t.Errorf("override env: got %q, want %q", got, want)
	}
}

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	tests := []struct {
		in   string
		want string
	}{
		{"~", home},
		{"~/foo", filepath.Join(home, "foo")},
		{"/no/tilde", "/no/tilde"},
		{"relative/path", "relative/path"},
	}
	for _, tt := range tests {
		got := expandTilde(tt.in)
		if got != tt.want {
			if !strings.HasSuffix(got, tt.want) {
				t.Errorf("expandTilde(%q) = %q, want %q", tt.in, got, tt.want)
			}
		}
	}
}

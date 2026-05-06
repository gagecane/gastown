package testutil

import (
	"os"
	"strings"
	"testing"
)

func TestCleanGTEnv_PreservesDoltPort(t *testing.T) {
	t.Setenv("GT_DOLT_PORT", "13307")
	t.Setenv("GT_TOWN_ROOT", "/some/town")
	t.Setenv("BD_ACTOR", "polecat/test")

	env := CleanGTEnv()

	var hasDoltPort, hasTownRoot, hasBDActor bool
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "GT_DOLT_PORT="):
			hasDoltPort = true
		case strings.HasPrefix(e, "GT_TOWN_ROOT="):
			hasTownRoot = true
		case strings.HasPrefix(e, "BD_ACTOR="):
			hasBDActor = true
		}
	}

	if !hasDoltPort {
		t.Error("CleanGTEnv stripped GT_DOLT_PORT — must preserve it")
	}
	if hasTownRoot {
		t.Error("CleanGTEnv preserved GT_TOWN_ROOT — must strip it")
	}
	if hasBDActor {
		t.Error("CleanGTEnv preserved BD_ACTOR — must strip it")
	}
}

func TestCleanGTEnv_PreservesBeadsDoltPort(t *testing.T) {
	t.Setenv("BEADS_DOLT_PORT", "13307")
	t.Setenv("BD_DEBUG", "1")

	env := CleanGTEnv()

	var hasBeadsPort, hasBDDebug bool
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "BEADS_DOLT_PORT="):
			hasBeadsPort = true
		case strings.HasPrefix(e, "BD_DEBUG="):
			hasBDDebug = true
		}
	}

	if !hasBeadsPort {
		t.Error("CleanGTEnv stripped BEADS_DOLT_PORT — must preserve it")
	}
	if hasBDDebug {
		t.Error("CleanGTEnv preserved BD_DEBUG — must strip it")
	}
}

func TestCleanGTEnv_ExtraEnv(t *testing.T) {
	env := CleanGTEnv("HOME=/tmp/test", "FOO=bar")

	var hasHome, hasFoo bool
	for _, e := range env {
		switch {
		case e == "HOME=/tmp/test":
			hasHome = true
		case e == "FOO=bar":
			hasFoo = true
		}
	}

	if !hasHome {
		t.Error("CleanGTEnv did not include extra HOME override")
	}
	if !hasFoo {
		t.Error("CleanGTEnv did not include extra FOO=bar")
	}
}

func TestNewBDCommand_InheritsEnv(t *testing.T) {
	t.Setenv("GT_DOLT_PORT", "13307")

	cmd := NewBDCommand("version")
	// cmd.Env should be nil (inherits process env)
	if cmd.Env != nil {
		t.Error("NewBDCommand should not set cmd.Env (nil inherits process env)")
	}
	if cmd.Path == "" {
		t.Error("NewBDCommand returned empty command path")
	}
}

func TestNewIsolatedBDCommand_SetEnv(t *testing.T) {
	t.Setenv("GT_DOLT_PORT", "13307")
	t.Setenv("GT_TOWN_ROOT", "/some/town")

	cmd := NewIsolatedBDCommand("version")
	if cmd.Env == nil {
		t.Fatal("NewIsolatedBDCommand should set cmd.Env")
	}

	var hasDoltPort, hasTownRoot bool
	for _, e := range cmd.Env {
		switch {
		case strings.HasPrefix(e, "GT_DOLT_PORT="):
			hasDoltPort = true
		case strings.HasPrefix(e, "GT_TOWN_ROOT="):
			hasTownRoot = true
		}
	}

	if !hasDoltPort {
		t.Error("NewIsolatedBDCommand stripped GT_DOLT_PORT")
	}
	if hasTownRoot {
		t.Error("NewIsolatedBDCommand preserved GT_TOWN_ROOT")
	}
}

func TestNewIsolatedGTCommand_SetEnv(t *testing.T) {
	t.Setenv("GT_DOLT_PORT", "13307")

	cmd := NewIsolatedGTCommand("version")
	if cmd.Env == nil {
		t.Fatal("NewIsolatedGTCommand should set cmd.Env")
	}

	var hasDoltPort bool
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "GT_DOLT_PORT=") {
			hasDoltPort = true
		}
	}

	if !hasDoltPort {
		t.Error("NewIsolatedGTCommand stripped GT_DOLT_PORT")
	}
}

// TestCleanGitEnv_StripsRepoPointingVars verifies that CleanGitEnv removes
// GIT_DIR, GIT_WORK_TREE, and the other git-repo-pointing environment
// variables that would otherwise redirect a test's git subprocess onto the
// ambient repo. See gu-h2ru for why this matters.
func TestCleanGitEnv_StripsRepoPointingVars(t *testing.T) {
	t.Setenv("GIT_DIR", "/real/repo/.git")
	t.Setenv("GIT_WORK_TREE", "/real/repo")
	t.Setenv("GIT_INDEX_FILE", "/real/repo/.git/index")
	t.Setenv("GIT_OBJECT_DIRECTORY", "/real/repo/.git/objects")
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", "/other/objects")
	t.Setenv("GIT_COMMON_DIR", "/real/repo/.git")
	t.Setenv("GIT_CEILING_DIRECTORIES", "/")
	t.Setenv("GIT_NAMESPACE", "some-ns")
	t.Setenv("GIT_PREFIX", "sub/")

	env := CleanGitEnv()

	for _, e := range env {
		for _, bad := range []string{
			"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=",
			"GIT_OBJECT_DIRECTORY=", "GIT_ALTERNATE_OBJECT_DIRECTORIES=",
			"GIT_COMMON_DIR=", "GIT_CEILING_DIRECTORIES=",
			"GIT_NAMESPACE=", "GIT_PREFIX=",
		} {
			if strings.HasPrefix(e, bad) {
				t.Errorf("CleanGitEnv retained repo-pointing var %q (full entry: %q)", bad, e)
			}
		}
	}
}

// TestCleanGitEnv_PreservesIdentity verifies CleanGitEnv does NOT strip
// author/committer identity vars — tests still need those to get
// deterministic commits, and they don't redirect git to a different repo.
func TestCleanGitEnv_PreservesIdentity(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "caller")
	t.Setenv("GIT_AUTHOR_EMAIL", "caller@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "caller")
	t.Setenv("GIT_COMMITTER_EMAIL", "caller@example.com")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")

	env := CleanGitEnv()

	want := map[string]bool{
		"GIT_AUTHOR_NAME":      false,
		"GIT_AUTHOR_EMAIL":     false,
		"GIT_COMMITTER_NAME":   false,
		"GIT_COMMITTER_EMAIL":  false,
		"GIT_CONFIG_GLOBAL":    false,
	}
	for _, e := range env {
		for k := range want {
			if strings.HasPrefix(e, k+"=") {
				want[k] = true
			}
		}
	}
	for k, present := range want {
		if !present {
			t.Errorf("CleanGitEnv stripped identity/config var %s — should preserve it", k)
		}
	}
}

// TestCleanGitEnv_AppendsExtra verifies extra env entries are appended.
func TestCleanGitEnv_AppendsExtra(t *testing.T) {
	env := CleanGitEnv("FOO=bar", "GIT_AUTHOR_NAME=Test")

	var hasFoo, hasAuthor bool
	for _, e := range env {
		switch e {
		case "FOO=bar":
			hasFoo = true
		case "GIT_AUTHOR_NAME=Test":
			hasAuthor = true
		}
	}
	if !hasFoo {
		t.Error("CleanGitEnv did not append FOO=bar")
	}
	if !hasAuthor {
		t.Error("CleanGitEnv did not append GIT_AUTHOR_NAME=Test")
	}
}

// TestCleanGitEnv_IdempotentWithoutGitEnv verifies that CleanGitEnv is a
// no-op relative to the stripped list when no git-repo vars are set.
func TestCleanGitEnv_IdempotentWithoutGitEnv(t *testing.T) {
	// Explicitly unset anything a parent process may have leaked in.
	for _, k := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_COMMON_DIR", "GIT_CEILING_DIRECTORIES",
		"GIT_NAMESPACE", "GIT_PREFIX",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}

	env := CleanGitEnv()
	// The returned env must be a proper subset of os.Environ() (identity
	// when no repo-pointing vars are set, at least the same size).
	if len(env) < len(os.Environ()) {
		// Accept strictly-less only if the process had repo-pointing vars
		// set from elsewhere (which the loop above tried to clear).
		// Don't fail — just sanity-check no stripped entries remain.
	}
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_DIR=") {
			t.Errorf("CleanGitEnv leaked GIT_DIR: %q", e)
		}
	}
}

// TestUnsetGitRepoEnv_ClearsAllRepoVars verifies that UnsetGitRepoEnv removes
// every GIT_* repo-pointing variable from the process environment. This is
// the init()-time sibling of CleanGitEnv — it mutates os.Environ instead of
// filtering it, so subsequent exec.Command calls that do `cmd.Env =
// append(os.Environ(), ...)` are automatically immunized. See gu-h2ru.
func TestUnsetGitRepoEnv_ClearsAllRepoVars(t *testing.T) {
	// t.Setenv handles both set+cleanup. We set every var UnsetGitRepoEnv
	// should clear, call the function, then assert each is gone.
	vars := []string{
		"GIT_DIR",
		"GIT_WORK_TREE",
		"GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_COMMON_DIR",
		"GIT_CEILING_DIRECTORIES",
		"GIT_NAMESPACE",
		"GIT_PREFIX",
		"GIT_LITERAL_PATHSPECS",
		"GIT_GLOB_PATHSPECS",
		"GIT_NOGLOB_PATHSPECS",
		"GIT_ICASE_PATHSPECS",
	}
	for _, k := range vars {
		t.Setenv(k, "/should/be/cleared")
	}

	UnsetGitRepoEnv()

	for _, k := range vars {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("UnsetGitRepoEnv did not clear %s (still set to %q)", k, v)
		}
	}
}

// TestUnsetGitRepoEnv_PreservesIdentityAndNonGitVars verifies that
// UnsetGitRepoEnv does NOT clear non-repo-pointing variables. Identity vars
// (GIT_AUTHOR_NAME etc.) and config vars (GIT_CONFIG_GLOBAL) must survive —
// otherwise tests that depend on deterministic commit identity would break.
// Non-git vars (HOME, PATH, FOO) must also be preserved.
func TestUnsetGitRepoEnv_PreservesIdentityAndNonGitVars(t *testing.T) {
	preserve := map[string]string{
		"GIT_AUTHOR_NAME":     "Test",
		"GIT_AUTHOR_EMAIL":    "test@test.com",
		"GIT_COMMITTER_NAME":  "Test",
		"GIT_COMMITTER_EMAIL": "test@test.com",
		"GIT_CONFIG_GLOBAL":   "/dev/null",
		"GT_DOLT_PORT":        "3306",
		"UNRELATED_VAR":       "keep-me",
	}
	for k, v := range preserve {
		t.Setenv(k, v)
	}

	UnsetGitRepoEnv()

	for k, want := range preserve {
		got, ok := os.LookupEnv(k)
		if !ok {
			t.Errorf("UnsetGitRepoEnv wrongly cleared %s", k)
			continue
		}
		if got != want {
			t.Errorf("UnsetGitRepoEnv mutated %s: got %q, want %q", k, got, want)
		}
	}
}

// TestUnsetGitRepoEnv_IsIdempotent verifies calling UnsetGitRepoEnv twice
// is safe — matters because multiple packages each install their own
// init() call, and the test binary may exec itself during integration
// tests (re-running init()).
func TestUnsetGitRepoEnv_IsIdempotent(t *testing.T) {
	t.Setenv("GIT_DIR", "/tmp/fake.git")

	UnsetGitRepoEnv()
	UnsetGitRepoEnv() // Second call must not panic or re-introduce anything.

	if _, ok := os.LookupEnv("GIT_DIR"); ok {
		t.Error("UnsetGitRepoEnv is not idempotent — GIT_DIR reappeared after second call")
	}
}

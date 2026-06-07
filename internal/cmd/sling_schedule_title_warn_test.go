package cmd

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/rig"
)

func TestRepoBasenameFromGitURL(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"https with .git", "https://github.com/owner/CodegenAgentSchedulerConstructs.git", "CodegenAgentSchedulerConstructs"},
		{"https without .git", "https://github.com/owner/foo", "foo"},
		{"scp style", "git@github.com:owner/foo.git", "foo"},
		{"trailing slash dropped by Base", "https://github.com/owner/foo/", "foo"},
		{"whitespace", "  https://github.com/owner/foo.git  ", "foo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := repoBasenameFromGitURL(tc.in)
			if got != tc.want {
				t.Errorf("repoBasenameFromGitURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDetectRigMismatchFromTitle(t *testing.T) {
	rigs := []*rig.Rig{
		{Name: "casc_cdk", GitURL: "https://github.com/o/CodegenAgentSchedulerCDK.git"},
		{Name: "casc_constructs", GitURL: "https://github.com/o/CodegenAgentSchedulerConstructs.git"},
		{Name: "casc_lambda", GitURL: "https://github.com/o/CodegenAgentSchedulerLambda.git"},
		{Name: "gastown", GitURL: "https://github.com/o/gastown.git"},
	}

	tests := []struct {
		name      string
		title     string
		targetRig string
		want      []string
	}{
		{
			name:      "concrete gu-an4y case: cadk bead titled for constructs",
			title:     "Bootstrap docs for CodegenAgentSchedulerConstructs",
			targetRig: "casc_cdk",
			want:      []string{"casc_constructs"},
		},
		{
			name:      "rig name token in title",
			title:     "Update casc_lambda integration",
			targetRig: "casc_cdk",
			want:      []string{"casc_lambda"},
		},
		{
			name:      "title mentions target rig only — no warning",
			title:     "Refactor CodegenAgentSchedulerCDK plumbing",
			targetRig: "casc_cdk",
			want:      nil,
		},
		{
			name:      "title mentions target rig name only — no warning",
			title:     "Ship something in casc_cdk",
			targetRig: "casc_cdk",
			want:      nil,
		},
		{
			name:      "no rig mentions",
			title:     "Fix flaky test in scheduler",
			targetRig: "casc_cdk",
			want:      nil,
		},
		{
			name:      "case-insensitive",
			title:     "fix CASC_LAMBDA build",
			targetRig: "casc_cdk",
			want:      []string{"casc_lambda"},
		},
		{
			name:      "multiple foreign rigs sorted",
			title:     "Touch casc_lambda and CodegenAgentSchedulerConstructs",
			targetRig: "casc_cdk",
			want:      []string{"casc_constructs", "casc_lambda"},
		},
		{
			name:      "substring should NOT match (whole-word boundary)",
			title:     "Update casc_lambdas test", // 'casc_lambdas' != 'casc_lambda'
			targetRig: "casc_cdk",
			want:      nil,
		},
		{
			name:      "empty title",
			title:     "",
			targetRig: "casc_cdk",
			want:      nil,
		},
		{
			name:      "empty target rig — still detects all foreign mentions",
			title:     "casc_lambda thing",
			targetRig: "",
			want:      []string{"casc_lambda"},
		},
		{
			name:      "punctuation around token still matches",
			title:     "[bootstrap] casc_constructs: docs",
			targetRig: "casc_cdk",
			want:      []string{"casc_constructs"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detectRigMismatchFromTitle(tc.title, tc.targetRig, rigs)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("detectRigMismatchFromTitle(%q, %q) = %v, want %v",
					tc.title, tc.targetRig, got, tc.want)
			}
		})
	}
}

func TestDetectRigMismatchFromTitle_EmptyRigs(t *testing.T) {
	got := detectRigMismatchFromTitle("anything goes here", "x", nil)
	if got != nil {
		t.Errorf("nil rigs should return nil, got %v", got)
	}
	got = detectRigMismatchFromTitle("anything", "x", []*rig.Rig{})
	if got != nil {
		t.Errorf("empty rigs should return nil, got %v", got)
	}
}

// TestWarnIfTitleMentionsForeignRig_GoesToStdoutNotStderr is the gu-ceocu
// regression. The convoy feeder classifies sling failures on the FIRST LINE of
// the child's STDERR (util.FirstLine). When this soft advisory was written to
// stderr it shadowed the bead's real failure line, forcing every
// title-mentioning bead down the feeder's default branch (escalate-once + flat
// re-feed forever) instead of its true failure class's backoff/untrack path.
// The advisory must therefore land on stdout, leaving stderr clean for the real
// error. This test fails if the advisory ever regresses back onto stderr.
func TestWarnIfTitleMentionsForeignRig_GoesToStdoutNotStderr(t *testing.T) {
	townRoot := t.TempDir()

	// Two rigs so a title mentioning the foreign one triggers the advisory.
	// loadRig only requires each rig's directory to exist under the town root.
	for _, name := range []string{"gastown_upstream", "casc_constructs"} {
		if err := os.MkdirAll(filepath.Join(townRoot, name), 0o755); err != nil {
			t.Fatalf("mkdir rig %q: %v", name, err)
		}
	}
	rigsConfig := &config.RigsConfig{
		Rigs: map[string]config.RigEntry{
			"gastown_upstream": {GitURL: "https://github.com/o/gastown.git", AddedAt: time.Unix(0, 0)},
			"casc_constructs":  {GitURL: "https://github.com/o/CodegenAgentSchedulerConstructs.git", AddedAt: time.Unix(0, 0)},
		},
	}
	if err := config.SaveRigsConfig(constants.MayorRigsPath(townRoot), rigsConfig); err != nil {
		t.Fatalf("save rigs config: %v", err)
	}

	// Capture both streams.
	oldStdout, oldStderr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr
	defer func() { os.Stdout, os.Stderr = oldStdout, oldStderr }()

	// Title mentions casc_constructs but is scheduled to gastown_upstream — the
	// exact gu-r8h3y shape (a gu- bead correctly on gastown_upstream whose title
	// merely references a sibling rig).
	warnIfTitleMentionsForeignRig(townRoot, "gastown_upstream", "gu-r8h3y",
		"pipeline-monitor: fingerprint override (casc_constructs authorizer 500)")

	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = oldStdout, oldStderr

	outBytes, _ := io.ReadAll(rOut)
	errBytes, _ := io.ReadAll(rErr)
	stdout, stderr := string(outBytes), string(errBytes)

	if !strings.Contains(stdout, "cross-rig title mismatch") {
		t.Errorf("advisory should be on stdout, got stdout=%q", stdout)
	}
	if strings.Contains(stderr, "cross-rig title mismatch") {
		t.Errorf("advisory must NOT be on stderr (it shadows the feeder's real-error classification), got stderr=%q", stderr)
	}
}

func TestDetectRigMismatchFromTitle_MultiTokenRepoBaseSkipped(t *testing.T) {
	// A repo basename containing characters outside [A-Za-z0-9_] tokenizes to
	// multiple tokens. We deliberately skip those rather than substring-match.
	rigs := []*rig.Rig{
		{Name: "weird", GitURL: "https://github.com/o/foo-bar-baz.git"},
	}
	// "foo" by itself is too generic; we don't substring-match the basename.
	got := detectRigMismatchFromTitle("the foo refactor", "other", rigs)
	if got != nil {
		t.Errorf("multi-token repo basename should not partial-match, got %v", got)
	}
	// But the rig name itself still matches.
	got = detectRigMismatchFromTitle("the weird thing", "other", rigs)
	if !reflect.DeepEqual(got, []string{"weird"}) {
		t.Errorf("rig name should match even when basename is skipped, got %v", got)
	}
}

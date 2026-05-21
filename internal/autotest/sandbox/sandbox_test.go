package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestNew_RejectsEmpty(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatalf("New(\"\") = nil, want error")
	}
}

func TestNew_RejectsRelative(t *testing.T) {
	if _, err := New("relative/path"); err == nil {
		t.Fatalf("New(\"relative/path\") = nil, want error")
	}
}

func TestNew_RejectsMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := New(dir); err == nil {
		t.Fatalf("New(missing) = nil, want error")
	}
}

func TestNew_RejectsFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(f); err == nil {
		t.Fatalf("New(file) = nil, want error")
	}
}

func TestNew_AcceptsExistingDir(t *testing.T) {
	sb, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if sb.Worktree() == "" {
		t.Fatalf("Worktree() empty")
	}
}

// TestFilterEnv_StripsCredentialFamilies covers each env-var family
// the Phase 0 task 5a synthesis enumerates. One sub-test per family
// keeps failure messages family-scoped.
func TestFilterEnv_StripsCredentialFamilies(t *testing.T) {
	cases := []struct {
		name string
		key  string
	}{
		// AWS_*
		{"AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID"},
		{"AWS_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY"},
		{"AWS_SESSION_TOKEN", "AWS_SESSION_TOKEN"},
		{"AWS_PROFILE", "AWS_PROFILE"},
		// GITHUB_TOKEN exact match
		{"GITHUB_TOKEN", "GITHUB_TOKEN"},
		// BD_*
		{"BD_BEADS_DIR", "BD_BEADS_DIR"},
		{"BD_DOLT_DSN", "BD_DOLT_DSN"},
		// DOLT_*
		{"DOLT_HOST", "DOLT_HOST"},
		{"DOLT_PORT", "DOLT_PORT"},
		// GIT_AUTHOR_*
		{"GIT_AUTHOR_NAME", "GIT_AUTHOR_NAME"},
		{"GIT_AUTHOR_EMAIL", "GIT_AUTHOR_EMAIL"},
		// GIT_COMMITTER_*
		{"GIT_COMMITTER_NAME", "GIT_COMMITTER_NAME"},
		{"GIT_COMMITTER_EMAIL", "GIT_COMMITTER_EMAIL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := []string{tc.key + "=secret-value", "PATH=/usr/bin"}
			out := FilterEnv(env)
			for _, kv := range out {
				if strings.HasPrefix(kv, tc.key+"=") {
					t.Fatalf("FilterEnv kept credential %q in %v", tc.key, out)
				}
			}
			if !containsKey(out, "PATH") {
				t.Fatalf("FilterEnv dropped non-credential PATH: %v", out)
			}
		})
	}
}

func TestFilterEnv_KeepsNonCredentials(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"GO111MODULE=on",
		"AWS_REGION=us-west-2", // sentinel: AWS_ prefix → must drop
	}
	out := FilterEnv(env)
	want := []string{"GO111MODULE", "HOME", "PATH"}
	got := keys(out)
	sort.Strings(got)
	if !equalStringSlices(got, want) {
		t.Fatalf("FilterEnv keys = %v, want %v", got, want)
	}
}

// TestFilterEnv_PreservesGitHubLookalikes guards against an over-broad
// strip that would also drop unrelated GITHUB_-prefixed variables a
// developer might legitimately set in their shell. The synthesis only
// names GITHUB_TOKEN as a credential; everything else must survive.
func TestFilterEnv_PreservesGitHubLookalikes(t *testing.T) {
	env := []string{
		"GITHUB_TOKEN=should-strip",
		"GITHUB_ACTOR=should-keep",
		"GITHUB_REPOSITORY=should-keep",
	}
	out := FilterEnv(env)
	if containsKey(out, "GITHUB_TOKEN") {
		t.Fatalf("GITHUB_TOKEN survived strip: %v", out)
	}
	if !containsKey(out, "GITHUB_ACTOR") || !containsKey(out, "GITHUB_REPOSITORY") {
		t.Fatalf("non-token GITHUB_* vars dropped: %v", out)
	}
}

func TestFilterEnv_LeavesMalformedEntriesAlone(t *testing.T) {
	// "FOO" without "=" is technically not an env var; the synthesis
	// does not require us to strip such entries, but we MUST NOT
	// crash on them.
	env := []string{"FOO", "PATH=/usr/bin", "AWS_X=y"}
	out := FilterEnv(env)
	if containsKey(out, "AWS_X") {
		t.Fatalf("AWS_X not stripped: %v", out)
	}
	found := false
	for _, kv := range out {
		if kv == "FOO" {
			found = true
		}
	}
	if !found {
		t.Fatalf("malformed entry FOO dropped: %v", out)
	}
}

func TestFilterEnv_DoesNotMutateInput(t *testing.T) {
	in := []string{"AWS_X=y", "PATH=/usr/bin"}
	snap := append([]string(nil), in...)
	_ = FilterEnv(in)
	if !equalStringSlices(in, snap) {
		t.Fatalf("FilterEnv mutated input: got %v want %v", in, snap)
	}
}

func TestApply_PinsCWDWhenEmpty(t *testing.T) {
	root := t.TempDir()
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/true")
	if err := sb.Apply(cmd); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if cmd.Dir != sb.Worktree() {
		t.Fatalf("cmd.Dir = %q, want %q", cmd.Dir, sb.Worktree())
	}
}

func TestApply_AcceptsDirInsideWorktree(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/true")
	cmd.Dir = sub
	if err := sb.Apply(cmd); err != nil {
		t.Fatalf("Apply with sub-dir: %v", err)
	}
	if cmd.Dir != sub {
		t.Fatalf("cmd.Dir = %q, want %q", cmd.Dir, sub)
	}
}

func TestApply_RejectsDirEscape(t *testing.T) {
	root := t.TempDir()
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir() // a sibling temp dir, distinct root
	cmd := exec.Command("/bin/true")
	cmd.Dir = outside
	if err := sb.Apply(cmd); err == nil {
		t.Fatalf("Apply with outside Dir = nil, want error")
	}
}

func TestApply_StripsCmdEnv(t *testing.T) {
	sb, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/true")
	cmd.Env = []string{"AWS_PROFILE=p", "PATH=/usr/bin"}
	if err := sb.Apply(cmd); err != nil {
		t.Fatal(err)
	}
	if containsKey(cmd.Env, "AWS_PROFILE") {
		t.Fatalf("AWS_PROFILE not stripped: %v", cmd.Env)
	}
	if !containsKey(cmd.Env, "PATH") {
		t.Fatalf("PATH dropped: %v", cmd.Env)
	}
}

func TestApply_RejectsNilCmd(t *testing.T) {
	sb, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sb.Apply(nil); err == nil {
		t.Fatalf("Apply(nil) = nil, want error")
	}
}

func TestApply_FallsBackToOSEnvironWhenEnvNil(t *testing.T) {
	root := t.TempDir()
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_PROFILE", "should-be-stripped")
	t.Setenv("AUTOTEST_SANDBOX_FIXTURE", "should-be-kept")
	cmd := exec.Command("/bin/true")
	if err := sb.Apply(cmd); err != nil {
		t.Fatal(err)
	}
	if containsKey(cmd.Env, "AWS_PROFILE") {
		t.Fatalf("AWS_PROFILE leaked from os.Environ: %v", cmd.Env)
	}
	if !containsKey(cmd.Env, "AUTOTEST_SANDBOX_FIXTURE") {
		t.Fatalf("non-credential env var dropped: %v", cmd.Env)
	}
}

func TestResolve_RelativeInsideWorktree(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sb.Resolve("a/b")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := filepath.Join(sb.Worktree(), "a", "b")
	if got != want {
		t.Fatalf("Resolve = %q, want %q", got, want)
	}
}

// TestResolve_RejectsDotDotEscape ensures a path that uses ".."
// traversal to escape the worktree is rejected, even when the
// escape target does not exist on disk.
func TestResolve_RejectsDotDotEscape(t *testing.T) {
	root := t.TempDir()
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Resolve("../escape"); err == nil {
		t.Fatalf("Resolve(\"../escape\") = nil, want error")
	}
}

func TestResolve_RejectsAbsoluteOutside(t *testing.T) {
	root := t.TempDir()
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir() // distinct
	if _, err := sb.Resolve(outside); err == nil {
		t.Fatalf("Resolve(outside) = nil, want error")
	}
}

func TestResolve_RejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "out")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Resolve("out"); err == nil {
		t.Fatalf("Resolve(symlink-out) = nil, want error")
	}
}

// TestResolve_RejectsEmpty asserts the documented empty-path
// rejection — callers must never accidentally pass "" and have
// Resolve return the worktree.
func TestResolve_RejectsEmpty(t *testing.T) {
	sb, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Resolve(""); err == nil {
		t.Fatalf("Resolve(\"\") = nil, want error")
	}
}

func TestResolve_AllowsWorktreeRoot(t *testing.T) {
	root := t.TempDir()
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sb.Resolve(sb.Worktree())
	if err != nil {
		t.Fatalf("Resolve(root): %v", err)
	}
	if got != sb.Worktree() {
		t.Fatalf("Resolve(root) = %q, want %q", got, sb.Worktree())
	}
}

// TestResolve_AllowsMissingPathInsideWorktree exercises the documented
// behaviour that a not-yet-created file under the worktree is still
// accepted (callers may use Resolve before writing the file).
func TestResolve_AllowsMissingPathInsideWorktree(t *testing.T) {
	sb, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := sb.Resolve("not/created/yet.txt")
	if err != nil {
		t.Fatalf("Resolve(missing): %v", err)
	}
	want := filepath.Join(sb.Worktree(), "not", "created", "yet.txt")
	if got != want {
		t.Fatalf("Resolve(missing) = %q, want %q", got, want)
	}
}

// helpers

func containsKey(env []string, key string) bool {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

func keys(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		out = append(out, kv[:eq])
	}
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

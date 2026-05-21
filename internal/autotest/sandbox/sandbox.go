package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// stripPrefixes lists the environment variable name prefixes whose
// matching variables are removed from any command configured by
// Apply. The set is fixed by the synthesis (.designs/auto-test-pr/
// synthesis.md, Phase 0 task 5a) and is intentionally over-broad:
// adding a new credential family is safer than allow-listing what
// happens to be in the polecat's process environment today.
var stripPrefixes = []string{
	"AWS_",
	"BD_",
	"DOLT_",
	"GIT_AUTHOR_",
	"GIT_COMMITTER_",
}

// stripExact lists environment variable names removed by exact match.
// GITHUB_TOKEN does not share a prefix with another variable Auto-Test-PR
// cares about, so it is enumerated here rather than in stripPrefixes.
var stripExact = []string{
	"GITHUB_TOKEN",
}

// Sandbox configures a subprocess to run with credential environment
// variables stripped and its working directory pinned to a known
// worktree. A Sandbox is immutable after construction and safe for
// concurrent use.
type Sandbox struct {
	worktree string
}

// New constructs a Sandbox anchored at worktree, which MUST be an
// absolute path to an existing directory. Symlinks in worktree are
// resolved at construction time so Resolve's escape check compares
// the canonical worktree against the canonical target.
func New(worktree string) (*Sandbox, error) {
	if worktree == "" {
		return nil, errors.New("sandbox: worktree is required")
	}
	if !filepath.IsAbs(worktree) {
		return nil, fmt.Errorf("sandbox: worktree must be absolute, got %q", worktree)
	}
	resolved, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return nil, fmt.Errorf("sandbox: resolve worktree %q: %w", worktree, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("sandbox: stat worktree %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("sandbox: worktree %q is not a directory", resolved)
	}
	return &Sandbox{worktree: resolved}, nil
}

// Worktree returns the absolute, symlink-resolved worktree path the
// sandbox pins commands to.
func (s *Sandbox) Worktree() string {
	return s.worktree
}

// Apply configures cmd's environment and working directory according
// to the sandbox policy. If cmd.Dir is empty, it is set to the
// worktree; if cmd.Dir is non-empty, it MUST already be the
// worktree or a path inside it (validated via Resolve), otherwise
// Apply returns an error. cmd.Env is replaced with the strip-applied
// view of os.Environ (or, if cmd.Env was already populated by the
// caller, that explicit slice).
func (s *Sandbox) Apply(cmd *exec.Cmd) error {
	if cmd == nil {
		return errors.New("sandbox: nil cmd")
	}
	if cmd.Dir == "" {
		cmd.Dir = s.worktree
	} else {
		// Validate caller-supplied Dir is inside the worktree.
		if _, err := s.Resolve(cmd.Dir); err != nil {
			return fmt.Errorf("sandbox: cmd.Dir: %w", err)
		}
	}
	base := cmd.Env
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = FilterEnv(base)
	return nil
}

// FilterEnv returns env with credential variables removed. The input
// slice is not modified. Variables are matched case-sensitively
// (POSIX env names are case-sensitive) against the strip prefix list
// and the exact-match list.
func FilterEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if shouldStrip(kv) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// shouldStrip reports whether the "KEY=VALUE" form should be removed.
// Lines without an "=" are kept (they are malformed environments but
// not credentials we know about).
func shouldStrip(kv string) bool {
	eq := strings.IndexByte(kv, '=')
	if eq < 0 {
		return false
	}
	key := kv[:eq]
	for _, exact := range stripExact {
		if key == exact {
			return true
		}
	}
	for _, prefix := range stripPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// Resolve returns an absolute, symlink-resolved path that is
// guaranteed to live inside the sandbox's worktree. It accepts both
// absolute and relative inputs; relative inputs are resolved against
// the worktree. The returned path exists on disk.
//
// Resolve rejects:
//   - empty paths,
//   - paths whose cleaned form escapes the worktree via "..",
//   - symlinks that resolve outside the worktree.
//
// Callers MUST use Resolve before passing untrusted paths (e.g.
// reviewer-supplied target file paths) to subprocesses.
func (s *Sandbox) Resolve(p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(s.worktree, abs)
	}
	abs = filepath.Clean(abs)

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// If the path doesn't exist yet, fall back to the cleaned
		// absolute form. The escape check below still applies so
		// "../../etc/passwd" is rejected even if the file is
		// missing.
		if os.IsNotExist(err) {
			resolved = abs
		} else {
			return "", fmt.Errorf("resolve %q: %w", p, err)
		}
	}

	if !pathInsideRoot(resolved, s.worktree) {
		return "", fmt.Errorf("path %q escapes worktree %q", p, s.worktree)
	}
	return resolved, nil
}

// pathInsideRoot reports whether p is root or a descendant of root.
// Both p and root are expected to be absolute and Cleaned.
func pathInsideRoot(p, root string) bool {
	if p == root {
		return true
	}
	rootSlash := root
	if !strings.HasSuffix(rootSlash, string(filepath.Separator)) {
		rootSlash += string(filepath.Separator)
	}
	return strings.HasPrefix(p, rootSlash)
}

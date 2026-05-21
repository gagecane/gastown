//go:build !linux

package sandbox

import "os/exec"

// applyNetNamespace is a no-op on non-Linux builds. ApplyOffline
// refuses to run unless netDropSupported reports true, so this is
// only reachable from a code path that has already been gated by
// the supported predicate.
func applyNetNamespace(cmd *exec.Cmd) {
	_ = cmd
}

// netDropSupported reports whether applyNetNamespace can produce a
// useful net-drop on this build. Non-Linux platforms have no
// equivalent of CLONE_NEWNET in our supported substrate, so the
// answer is always false — ApplyOffline returns an explicit
// `unsupported` error instead of silently letting traffic out.
func netDropSupported() bool {
	return false
}

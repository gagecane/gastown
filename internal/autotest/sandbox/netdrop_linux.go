//go:build linux

package sandbox

import (
	"os"
	"os/exec"
	"syscall"
)

// applyNetNamespace configures cmd to start in a fresh user+network
// namespace. The user namespace makes the network namespace creation
// possible without root: an unprivileged process is permitted to
// create a new netns iff it also creates a new userns. Identity
// mappings keep the in-namespace user identical to the outside
// user (uid → uid, gid → gid), so cmd.Dir, files, and Go's runtime
// behave the same way they would without the namespace — the only
// observable change is that the namespace's network stack contains
// only a (down) loopback interface, so any TCP/UDP dial fails with
// `network is unreachable`.
//
// GidMappingsEnableSetgroups must be false: writing the gid_map
// file is rejected by the kernel for unprivileged userns processes
// unless setgroups has first been disabled, which the kernel does
// for us when this flag is set.
func applyNetNamespace(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET
	cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{
		ContainerID: os.Getuid(),
		HostID:      os.Getuid(),
		Size:        1,
	}}
	cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{
		ContainerID: os.Getgid(),
		HostID:      os.Getgid(),
		Size:        1,
	}}
	cmd.SysProcAttr.GidMappingsEnableSetgroups = false
}

// netDropSupported reports whether applyNetNamespace can produce a
// useful net-drop on this build. The Linux build always returns
// true; runtime feature detection (kernel.unprivileged_userns_clone
// disabled, kernel.userns_max=0, etc.) surfaces as a Run-time error
// from cmd.Start, not from this predicate, because we cannot probe
// the kernel without an exec.
func netDropSupported() bool {
	return true
}

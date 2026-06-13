package util

import "os/exec"

// niceAdjustment is the niceness increment applied to gt's heavy gate/test
// subprocesses. +10 lowers CPU scheduling priority enough to yield to
// co-tenant bursts under host saturation without starving the gate outright.
const niceAdjustment = "10"

// ioniceIdleClass selects the "idle" I/O scheduling class (ionice -c 3), so a
// gate only gets disk I/O when no other process wants it — keeping gt's
// build/link/test I/O out of the way of co-tenant work during a load spiral.
const ioniceIdleClass = "3"

// NiceIonicePrefix returns a best-effort argv prefix that lowers the CPU (nice)
// and I/O (ionice) scheduling priority of heavy subprocesses, so gt yields to
// co-tenant bursts under host saturation instead of deepening the load spiral
// that SIGKILLs gates (hq-5em9k). Both niceness and the ionice class are
// inherited across fork/exec, so prefixing `sh -c <gate>` lowers the whole gate
// process tree (the go toolchain, linker, test binaries it spawns).
//
// Each tool is included only if it is found on PATH; on a host with neither the
// prefix is empty and callers run the command unmodified. A fresh slice is
// returned on every call.
func NiceIonicePrefix() []string {
	var prefix []string
	if p, err := exec.LookPath("nice"); err == nil {
		prefix = append(prefix, p, "-n", niceAdjustment)
	}
	if p, err := exec.LookPath("ionice"); err == nil {
		prefix = append(prefix, p, "-c", ioniceIdleClass)
	}
	return prefix
}

// WrapNiceIonice prepends NiceIonicePrefix to argv, returning a command that
// runs at lowered CPU/I/O priority when nice/ionice are available. When neither
// is on PATH it returns argv unchanged, so the command runs exactly as before.
func WrapNiceIonice(argv []string) []string {
	prefix := NiceIonicePrefix()
	if len(prefix) == 0 {
		return argv
	}
	return append(prefix, argv...)
}

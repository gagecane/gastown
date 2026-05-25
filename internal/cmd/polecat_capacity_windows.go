//go:build windows

package cmd

import (
	"os"
)

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Windows, FindProcess always succeeds; we rely on the fact that
	// the process handle was opened successfully as a heuristic.
	_ = p
	return true
}

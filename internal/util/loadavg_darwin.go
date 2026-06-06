//go:build darwin

package util

import (
	"os/exec"
	"strconv"
	"strings"
)

// loadAverage1Platform returns the 1-minute load average via sysctl on macOS,
// where /proc/loadavg does not exist.
func loadAverage1Platform() float64 {
	cmd := exec.Command("sysctl", "-n", "vm.loadavg")
	SetDetachedProcessGroup(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	// Output format: "{ 1.23 4.56 7.89 }"
	s := strings.TrimSpace(string(out))
	s = strings.Trim(s, "{ }")
	fields := strings.Fields(s)
	if len(fields) < 1 {
		return 0
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return v
}

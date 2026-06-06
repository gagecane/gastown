package util

import (
	"fmt"
	"os"
	"runtime"
)

// LoadAverage1 returns the system's 1-minute load average.
//
// On Linux (and other /proc-based systems) it reads /proc/loadavg. On macOS it
// uses `sysctl -n vm.loadavg`. On platforms where load average is unavailable
// (e.g. Windows) it returns 0, which callers should treat as "unknown" and not
// as "idle".
//
// This is a leaf helper so packages like internal/refinery can make
// load-aware scheduling decisions without importing internal/daemon (which
// would create an import cycle, since daemon imports refinery).
func LoadAverage1() float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return loadAverage1Platform()
	}
	var load1 float64
	if _, err := fmt.Sscanf(string(data), "%f", &load1); err != nil {
		return 0
	}
	return load1
}

// LoadPerCore returns the 1-minute load average divided by the number of
// logical CPUs. Returns 0 when the load average is unavailable. This is the
// normalized metric callers should compare against a threshold so the same
// configured value behaves consistently across hosts with different core
// counts.
func LoadPerCore() float64 {
	load := LoadAverage1()
	if load <= 0 {
		return 0
	}
	cores := runtime.NumCPU()
	if cores < 1 {
		cores = 1
	}
	return load / float64(cores)
}

//go:build !darwin

package util

// loadAverage1Platform is the fallback for non-darwin platforms. On Linux and
// other /proc-based systems LoadAverage1 reads /proc/loadavg directly and never
// calls this; on Windows (no /proc, no sysctl) load average is unavailable, so
// this returns 0 ("unknown").
func loadAverage1Platform() float64 {
	return 0
}

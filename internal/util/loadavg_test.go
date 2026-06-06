package util

import (
	"runtime"
	"testing"
)

func TestLoadAverage1(t *testing.T) {
	got := LoadAverage1()
	// Load average is non-negative everywhere; on Windows it's 0 (unavailable),
	// on Linux/macOS it's a real reading. We can't assert an exact value, only
	// the invariant.
	if got < 0 {
		t.Fatalf("load average must be non-negative, got %v", got)
	}
	if runtime.GOOS == "linux" && got == 0 {
		// On a live Linux host /proc/loadavg should yield a positive number,
		// but CI sandboxes can read 0.00 momentarily — only log, don't fail.
		t.Logf("LoadAverage1 returned 0 on linux (may be a quiet sandbox)")
	}
}

func TestLoadPerCore(t *testing.T) {
	got := LoadPerCore()
	if got < 0 {
		t.Fatalf("load per core must be non-negative, got %v", got)
	}
	// If load average is available, per-core must equal load/NumCPU.
	load := LoadAverage1()
	if load > 0 {
		want := load / float64(runtime.NumCPU())
		if got != want {
			t.Fatalf("LoadPerCore=%v, want load/cores=%v", got, want)
		}
	}
}

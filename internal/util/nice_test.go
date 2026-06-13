package util

import (
	"os/exec"
	"reflect"
	"testing"
)

// TestWrapNiceIonice proves the best-effort contract: the result always ends
// with the original argv, and when nice/ionice are absent the argv is returned
// unchanged.
func TestWrapNiceIonice(t *testing.T) {
	argv := []string{"sh", "-c", "go build ./..."}
	got := WrapNiceIonice(argv)

	// The original command must always be preserved as the tail, regardless of
	// which (if any) priority tools are present.
	if len(got) < len(argv) {
		t.Fatalf("WrapNiceIonice dropped args: got %v", got)
	}
	tail := got[len(got)-len(argv):]
	if !reflect.DeepEqual(tail, argv) {
		t.Fatalf("original argv must be the tail; got %v, want tail %v", got, argv)
	}

	prefix := NiceIonicePrefix()
	if len(prefix) == 0 {
		// Neither tool on PATH: must be returned unchanged.
		if !reflect.DeepEqual(got, argv) {
			t.Fatalf("with no nice/ionice on PATH, argv must be unchanged; got %v", got)
		}
	} else {
		// At least one tool present: it must be prepended.
		if len(got) != len(prefix)+len(argv) {
			t.Fatalf("expected prefix(%d)+argv(%d) length, got %d: %v", len(prefix), len(argv), len(got), got)
		}
	}
}

// TestNiceIonicePrefixMatchesPath proves the prefix only references tools that
// actually resolve on PATH, so we never produce a command that fails with
// "executable not found".
func TestNiceIonicePrefixMatchesPath(t *testing.T) {
	prefix := NiceIonicePrefix()

	_, niceErr := exec.LookPath("nice")
	_, ioniceErr := exec.LookPath("ionice")

	wantLen := 0
	if niceErr == nil {
		wantLen += 3 // <nice> -n 10
	}
	if ioniceErr == nil {
		wantLen += 3 // <ionice> -c 3
	}
	if len(prefix) != wantLen {
		t.Fatalf("prefix length %d (%v) does not match PATH availability (nice=%v ionice=%v)",
			len(prefix), prefix, niceErr == nil, ioniceErr == nil)
	}
}

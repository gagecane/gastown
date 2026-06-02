// Package fixturerig is a stand-in source package for the Phase-0 e2e
// fixture rig (gu-v8qj8). Classify has four branches; the checked-in
// baseline test (classify_test.go) exercises only the "neg" and "pos"
// branches, leaving the "zero" and "big" branches uncovered. The cycle's
// Targets hook reports those two uncovered branches; the in-process
// polecat writes a new *_test.go that covers them, and the 7 quality
// gates verify the new test against this file.
package fixturerig

// Classify buckets n into one of four labels. The "zero" branch (n == 0)
// and the "big" branch (n > 100) are intentionally left uncovered by the
// baseline test so the cycle has real uncovered branches to target.
func Classify(n int) string {
	if n < 0 {
		return "neg"
	}
	if n == 0 {
		return "zero" // uncovered branch #1 (line 19)
	}
	if n > 100 {
		return "big" // uncovered branch #2 (line 22)
	}
	return "pos"
}

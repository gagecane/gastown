package doltserver

import "testing"

// TestCopyVerified guards the close-originals gate: closing the live originals
// is only safe once every open original has a counterpart in wisps. A copy that
// dropped rows must NOT permit the close (that was the dual-write data-loss bug).
func TestCopyVerified(t *testing.T) {
	tests := []struct {
		name          string
		copiedAgents  int
		openOriginals int
		want          bool
	}{
		{"exact match", 5, 5, true},
		{"more copied than open (closed originals included)", 8, 5, true},
		{"nothing to migrate", 0, 0, true},
		{"copy dropped rows", 4, 5, false},
		{"copy produced nothing", 0, 3, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := copyVerified(tt.copiedAgents, tt.openOriginals); got != tt.want {
				t.Errorf("copyVerified(%d, %d) = %v, want %v",
					tt.copiedAgents, tt.openOriginals, got, tt.want)
			}
		})
	}
}

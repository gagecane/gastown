package crux

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractCRID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"full URL", "https://code.amazon.com/reviews/CR-12345678", 12345678},
		{"bare CR ID", "CR-42", 42},
		{"embedded in commit trailer", "Some commit\n\nReview: https://code.amazon.com/reviews/CR-9001\n", 9001},
		{"no match", "no cr id here", 0},
		{"empty string", "", 0},
		{"first match wins", "CR-1 and also CR-2", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ExtractCRID(tc.in)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFormatCRID(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "CR-12345678", FormatCRID(12345678))
	assert.Equal(t, "", FormatCRID(0))
	assert.Equal(t, "", FormatCRID(-1))
}

func TestBuildCreateArgs_NewReview(t *testing.T) {
	t.Parallel()
	args := BuildCreateArgs(0, "mainline", "My summary", "Detail body")
	joined := strings.Join(args, " ")

	// Expected flags for a brand-new review.
	assert.Contains(t, args, "--new-review")
	assert.NotContains(t, joined, "--update-review")
	assert.Contains(t, args, "--publish")
	assert.Contains(t, args, "--auto-merge")
	assert.Contains(t, args, "--no-open")
	assert.Contains(t, args, "--no-amend")

	// Metadata args: value appears directly after its flag.
	assertFlagValue(t, args, "--destination-branch", "mainline")
	assertFlagValue(t, args, "--summary", "My summary")
	assertFlagValue(t, args, "--description", "Detail body")
}

func TestBuildCreateArgs_UpdateReview(t *testing.T) {
	t.Parallel()
	args := BuildCreateArgs(555, "mainline", "Updated summary", "Updated body")

	assert.Contains(t, args, "--update-review")
	assertFlagValue(t, args, "--update-review", "CR-555")
	assert.NotContains(t, args, "--new-review")
	assert.Contains(t, args, "--publish")
	assert.Contains(t, args, "--auto-merge")
	assertFlagValue(t, args, "--summary", "Updated summary")
	assertFlagValue(t, args, "--description", "Updated body")
}

func TestBuildCreateArgs_OmitsEmptyMetadata(t *testing.T) {
	t.Parallel()
	args := BuildCreateArgs(0, "", "", "")
	joined := strings.Join(args, " ")
	assert.NotContains(t, joined, "--destination-branch")
	assert.NotContains(t, joined, "--summary")
	assert.NotContains(t, joined, "--description")
	// Publish/auto-merge are always present.
	assert.Contains(t, args, "--publish")
	assert.Contains(t, args, "--auto-merge")
}

func TestSummarizeCreateOutput(t *testing.T) {
	t.Parallel()
	out := `cr: uploading...
Review created: https://code.amazon.com/reviews/CR-987654
done.
`
	assert.Equal(t, 987654, SummarizeCreateOutput(out))
	assert.Equal(t, 0, SummarizeCreateOutput("no cr id\n"))
	assert.Equal(t, 0, SummarizeCreateOutput(""))
}

// assertFlagValue asserts that `flag` appears in args and is immediately
// followed by `value`.
func assertFlagValue(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			assert.Equal(t, value, args[i+1], "flag %q", flag)
			return
		}
	}
	t.Fatalf("flag %q not found in args %v", flag, args)
}

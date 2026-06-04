// Package stdlib_test contains test fixtures for standard-library-style
// assertions: the `if got != want { t.Errorf(...) }` idiom rather than
// testify. The linter must recognize these as real assertions so it neither
// floods correct stdlib tests with false "zero assertion" findings nor lets
// genuinely tautological stdlib tests slip through.
//
// As with the other fixtures, the function-under-test (sut.*) is deliberately
// NOT declared here: functions declared in a *_test.go file are treated as
// helpers, never as the FUT. AnalyzeFile only parses, so the import need not
// resolve.
package stdlib_test

import (
	"reflect"
	"testing"

	"example.com/sut"
)

// --- SHOULD NOT TRIGGER: valid stdlib tests ---

// TestStdlib_TableDriven is the canonical table-driven idiom that previously
// drowned the gate in false "zero assertion" findings.
func TestStdlib_TableDriven(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{{1, 2}, {3, 4}}
	for _, tt := range cases {
		got := sut.Transform(tt.in)
		if got != tt.want {
			t.Errorf("Transform(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// TestStdlib_FatalGuard mixes a t.Fatal error guard with a real comparison.
func TestStdlib_FatalGuard(t *testing.T) {
	got, err := sut.Do()
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" {
		t.Errorf("Do() = %q, want %q", got, "ok")
	}
}

// TestStdlib_DeepEqual uses the !reflect.DeepEqual guard idiom.
func TestStdlib_DeepEqual(t *testing.T) {
	got := sut.Build()
	want := []int{1, 2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Build() = %v, want %v", got, want)
	}
}

// TestStdlib_MethodOnRow calls the FUT as a method on a table row
// (tt.state.Classify()); taint must follow the method output.
func TestStdlib_MethodOnRow(t *testing.T) {
	cases := []struct {
		state sut.State
		want  string
	}{{sut.State("a"), "x"}, {sut.State("b"), "y"}}
	for _, tt := range cases {
		if got := tt.state.Classify(); got != tt.want {
			t.Errorf("Classify() = %q, want %q", got, tt.want)
		}
	}
}

// TestStdlib_FUTInCondition invokes the FUT directly inside the condition.
func TestStdlib_FUTInCondition(t *testing.T) {
	if !sut.Valid("input") {
		t.Errorf("Valid(input) = false, want true")
	}
}

// --- SHOULD TRIGGER sub-rule (iv): zero assertions ---

func TestStdlib_EmptyBody(t *testing.T) {
	// Nothing here — not even a failure call.
}

func TestStdlib_OnlyLogging(t *testing.T) {
	// t.Log is not a failure call, so this asserts nothing.
	t.Log("running")
}

// --- SHOULD TRIGGER sub-rule (iv): tautological self-comparison ---

// TestStdlib_SelfCompare guards a failure behind `x != x`, which is always
// false — the test can never fail.
func TestStdlib_SelfCompare(t *testing.T) {
	x := sut.Compute()
	if x != x {
		t.Errorf("unreachable")
	}
}

// --- SHOULD TRIGGER sub-rule (i): no FUT dependency ---

// TestStdlib_HardcodedComparison compares two literals; nothing flows from the
// function-under-test.
func TestStdlib_HardcodedComparison(t *testing.T) {
	got := 42
	want := 42
	if got != want {
		t.Errorf("got %d want %d", got, want)
	}
}

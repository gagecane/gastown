package env

import (
	"strings"
	"testing"
	"time"
)

func TestString(t *testing.T) {
	const v Var = "GT_TEST_STR"
	t.Setenv(v.Name(), "hello")
	if got := String(v); got != "hello" {
		t.Errorf("String=%q, want %q", got, "hello")
	}
}

func TestStringEmpty(t *testing.T) {
	const v Var = "GT_TEST_STR_EMPTY"
	if got := String(v); got != "" {
		t.Errorf("String=%q, want empty", got)
	}
}

func TestBool(t *testing.T) {
	const v Var = "GT_TEST_BOOL"
	cases := []struct {
		in   string
		want bool
	}{
		{"true", true},
		{"TRUE", true},
		{" True ", true},
		{"1", true},
		{"yes", true},
		{"YES", true},
		{"on", true},
		{"t", true},
		{"y", true},
		{"false", false},
		{"0", false},
		{"no", false},
		{"off", false},
		{"", false},
		{"garbage", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			t.Setenv(v.Name(), c.in)
			if got := Bool(v); got != c.want {
				t.Errorf("Bool(%q)=%v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestBoolUnset(t *testing.T) {
	const v Var = "GT_TEST_BOOL_UNSET"
	if Bool(v) {
		t.Error("Bool(unset)=true, want false")
	}
}

func TestInt(t *testing.T) {
	const v Var = "GT_TEST_INT"
	cases := []struct {
		in   string
		def  int
		want int
	}{
		{"42", 0, 42},
		{"-7", 0, -7},
		{" 99 ", 0, 99},
		{"", 5, 5},     // unset → default
		{"abc", 5, 5},  // garbage → default
		{"3.14", 5, 5}, // not an int → default
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			t.Setenv(v.Name(), c.in)
			if got := Int(v, c.def); got != c.want {
				t.Errorf("Int(%q, %d)=%d, want %d", c.in, c.def, got, c.want)
			}
		})
	}
}

func TestIntUnset(t *testing.T) {
	const v Var = "GT_TEST_INT_UNSET"
	if got := Int(v, 17); got != 17 {
		t.Errorf("Int(unset, 17)=%d, want 17", got)
	}
}

func TestDuration(t *testing.T) {
	const v Var = "GT_TEST_DUR"
	cases := []struct {
		in   string
		def  time.Duration
		want time.Duration
	}{
		{"5s", 0, 5 * time.Second},
		{"100ms", 0, 100 * time.Millisecond},
		{"2h30m", 0, 2*time.Hour + 30*time.Minute},
		{" 1m ", 0, time.Minute},
		{"", time.Second, time.Second},        // unset → default
		{"garbage", time.Second, time.Second}, // unparseable → default
		{"300", time.Second, time.Second},     // bare integer is NOT a duration
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			t.Setenv(v.Name(), c.in)
			if got := Duration(v, c.def); got != c.want {
				t.Errorf("Duration(%q, %v)=%v, want %v", c.in, c.def, got, c.want)
			}
		})
	}
}

func TestKindString(t *testing.T) {
	cases := []struct {
		k    Kind
		want string
	}{
		{KindString, "string"},
		{KindBool, "bool"},
		{KindInt, "int"},
		{KindDuration, "duration"},
		{Kind(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.k.String(); got != c.want {
			t.Errorf("Kind(%d).String()=%q, want %q", c.k, got, c.want)
		}
	}
}

func TestVarName(t *testing.T) {
	if (Var("GT_FOO")).Name() != "GT_FOO" {
		t.Error("Var.Name() did not return underlying string")
	}
}

func TestRegistryPopulated(t *testing.T) {
	specs := List()
	if len(specs) < 80 {
		t.Errorf("List() returned %d specs; expected at least 80 (audit shows 90)", len(specs))
	}
	// Check a few well-known names made it in.
	for _, want := range []Var{GTDoltPort, GTDebug, GTRole, GTPolecat, GTContextBudgetMaxTokens} {
		if _, ok := Lookup(want); !ok {
			t.Errorf("registry missing %s", want)
		}
	}
}

func TestRegistrySorted(t *testing.T) {
	specs := List()
	for i := 1; i < len(specs); i++ {
		if specs[i-1].Var >= specs[i].Var {
			t.Errorf("List() not sorted: %s >= %s at index %d", specs[i-1].Var, specs[i].Var, i)
		}
	}
}

func TestRegistryEveryVarStartsWithGT(t *testing.T) {
	for _, s := range List() {
		if !strings.HasPrefix(s.Var.Name(), "GT_") {
			t.Errorf("registered var %q does not start with GT_", s.Var)
		}
	}
}

func TestRegisterDuplicate(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Register(duplicate) did not panic")
		}
	}()
	Register(Spec{Var: GTDebug, Kind: KindString})
}

func TestRegisterEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Register(empty Var) did not panic")
		}
	}()
	Register(Spec{Var: "", Kind: KindString})
}

func TestLookupUnknown(t *testing.T) {
	if _, ok := Lookup("GT_DEFINITELY_NOT_REGISTERED_XYZ"); ok {
		t.Error("Lookup(unknown) returned ok=true")
	}
}

// TestRegistryCoversAllOSGetenvCallsites is a meta-check that catches drift:
// when someone adds a new os.Getenv("GT_FOO") in the codebase but forgets to
// register it here, this test would fail if it scanned the source. Doing
// that scan correctly requires running outside the package under test, so
// we leave it to a separate audit step (see scripts/check-env-coverage if
// added later) and assert only the registered count here.
func TestRegistryCount(t *testing.T) {
	if got := len(List()); got != 90 {
		t.Errorf("registry has %d entries; bootstrap audit found 90 — update the audit if new vars were intentionally added", got)
	}
}

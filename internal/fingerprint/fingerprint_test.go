package fingerprint

import "testing"

// TestLegacy2_ByteVector locks the legacy wire format. These exact bytes are
// what the in-prod failure_classifier dedups against; if this test ever needs
// updating, the classifier's existing fingerprint labels would be orphaned.
// Vectors captured from the pre-refactor classifierFingerprint implementation.
func TestLegacy2_ByteVector(t *testing.T) {
	cases := []struct {
		a, b string
		want string
	}{
		{"gastown", "ts-import-error", "694e97e41c2c"},
		{"a", "b", "69710d2aee75"},
	}
	for _, c := range cases {
		if got := Legacy2(c.a, c.b); got != c.want {
			t.Errorf("Legacy2(%q,%q) = %q, want %q (legacy wire format must not change)", c.a, c.b, got, c.want)
		}
	}
}

func TestLegacy2_Deterministic(t *testing.T) {
	if Legacy2("lia_web", "ts-import-error") != Legacy2("lia_web", "ts-import-error") {
		t.Fatal("Legacy2 not deterministic")
	}
	if len(Legacy2("x", "y")) != fingerprintLen {
		t.Fatalf("Legacy2 length = %d, want %d", len(Legacy2("x", "y")), fingerprintLen)
	}
}

// TestOf_ArityCollision is the CRITICAL arity-collision guard: a single
// dimension containing "::" must NOT collide with two dimensions, which the
// non-injective legacy "::" join would. Length-prefixing prevents this.
func TestOf_ArityCollision(t *testing.T) {
	if Of("a::b") == Of("a", "b") {
		t.Error("Of(\"a::b\") collided with Of(\"a\",\"b\") — encoding is not injective")
	}
	// Adjacent boundary shifts must also stay distinct.
	if Of("ab", "c") == Of("a", "bc") {
		t.Error("Of(\"ab\",\"c\") collided with Of(\"a\",\"bc\")")
	}
	if Of("", "ab") == Of("a", "b") {
		t.Error("Of(\"\",\"ab\") collided with Of(\"a\",\"b\")")
	}
}

func TestOf_Deterministic(t *testing.T) {
	if Of("rule", "target") != Of("rule", "target") {
		t.Fatal("Of not deterministic")
	}
	if len(Of("rule", "target")) != fingerprintLen {
		t.Fatalf("Of length = %d, want %d", len(Of("rule", "target")), fingerprintLen)
	}
}

// TestOf_DiffersFromLegacy documents that the two families are intentionally
// incompatible — Of must never be used where Legacy2 bytes are expected.
func TestOf_DiffersFromLegacy(t *testing.T) {
	if Of("a", "b") == Legacy2("a", "b") {
		t.Error("Of and Legacy2 produced the same bytes; they must differ")
	}
}

// Package fingerprint provides stable, short content fingerprints used for
// dedup across Gastown patrols (failure_classifier, curio, ...).
//
// Two entry points exist on purpose, and the distinction is load-bearing:
//
//   - Legacy2 reproduces the exact bytes the failure_classifier patrol has
//     emitted in production since gs-0wz: fnv-128a over "a::b", hex, first 12
//     chars. The classifier dedups open beads by a "fingerprint:<fp>" label, so
//     changing these bytes would orphan every existing label and cause the
//     classifier to re-file duplicates. Legacy2 MUST stay byte-identical — the
//     daemon's TestClassifierFingerprint_ByteVector test locks the wire format.
//
//   - Of is the general collision-free fingerprint for new code (curio). It is
//     NOT compatible with Legacy2 and must not be used where Legacy2's bytes are
//     expected.
//
// Why two functions instead of one "generalized" helper: Legacy2 joins its two
// dimensions with a literal "::" separator, which is non-injective —
// Legacy2("a::b","") and Legacy2("a","b") hash the same input. A single helper
// therefore cannot satisfy BOTH "byte-identical to the legacy classifier" AND
// "arity-collision-free" (Of("a::b") != Of("a","b")). The hashing/truncation
// machinery is shared; the encoding of dimensions into bytes is what differs.
package fingerprint

import (
	"fmt"
	"hash/fnv"
)

// fingerprintLen is the number of hex characters in a fingerprint.
const fingerprintLen = 12

// hash12 runs fnv-128a over s and returns the first 12 hex chars.
// Shared by Legacy2 and Of so the hash family and truncation never drift.
func hash12(s string) string {
	h := fnv.New128a()
	// fnv never returns a write error; ignoring it keeps the signature clean.
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%x", h.Sum(nil))[:fingerprintLen]
}

// Legacy2 reproduces the historical failure_classifier fingerprint:
// fnv-128a over "<a>::<b>", first 12 hex chars. Byte-identical to the
// pre-refactor classifierFingerprint(rig, signatureID). Do not change.
func Legacy2(a, b string) string {
	return hash12(a + "::" + b)
}

// Of computes a collision-free fingerprint over an arbitrary number of
// dimensions. Each dimension is length-prefixed ("<len>:<value>") before
// hashing, so no choice of separators in the inputs can make two distinct
// dimension lists encode to the same byte string. In particular
// Of("a::b") != Of("a", "b").
func Of(dims ...string) string {
	// Length-prefix every dimension. "<len>:<value>" is an injective encoding:
	// the decoder reads len, then exactly len bytes, leaving no ambiguity about
	// where one dimension ends and the next begins regardless of contents.
	enc := ""
	for _, d := range dims {
		enc += fmt.Sprintf("%d:%s", len(d), d)
	}
	return hash12(enc)
}

// Package corpus provides annotated test function samples for the
// tautology sub-rule (i) precision/recall spike.
//
// Each test function is annotated with a comment:
//
//	//tautology:yes — the assertion is tautological (expected depends on FUT)
//	//tautology:no  — the assertion is well-formed (independent expected)
//
// The corpus is sampled from real patterns found in gastown_upstream Go test files.
package corpus

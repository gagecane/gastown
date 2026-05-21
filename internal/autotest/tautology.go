// Package autotest implements gate runners for the auto-test-pr quality
// gates defined in .designs/auto-test-pr/synthesis.md. This file
// implements the tautology linter (gate 4d) which rejects tests whose
// assertions cannot meaningfully observe the function-under-test (SUT).
//
// Four sub-rules (gate 4d in synthesis.md):
//
//   (i)   ≥1 assertion must depend on the SUT's return value or
//         observable side effect.
//   (ii)  reject tests whose every assertion is literal-vs-literal
//         (assert.Equal("x", "x"), constant-vs-constant).
//   (iii) reject tests whose only assertions against the SUT are
//         NotNil/NotEmpty/truthy checks.
//   (iv)  reject assert(true) / expect(x).toBe(x) / zero-assertion
//         tests.
//
// Sub-rule (i) was spike-gated by Phase 0a-5 (gu-m57p6). The spike
// reported precision=1.000, recall=1.000 on a 50-test corpus, well
// above the 85%/75% thresholds, so sub-rule (i) ships unconditionally
// alongside the three syntactic sub-rules.
//
// The implementation uses go/parser + go/ast directly (per Risk R25 in
// synthesis.md) and operates on a single file at a time. Sub-rule (i)'s
// analysis is intra-function and flow-sensitive: it builds a taint set
// seeded by SUT call sites (return-value bindings, pointer arguments,
// method-receivers) and propagates through assignments to fixed point,
// then walks each assertion to check whether any argument references a
// tainted identifier or contains an inline SUT call.
package autotest

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// SubRule identifies which gate-4d sub-rule a Violation reports.
type SubRule int

const (
	// SubRuleNoSUTDependence corresponds to gate 4d sub-rule (i):
	// no assertion in the test depends on the SUT's return value or
	// observable side effect.
	SubRuleNoSUTDependence SubRule = iota + 1

	// SubRuleLiteralVsLiteral corresponds to gate 4d sub-rule (ii):
	// every assertion in the test compares a literal/constant to
	// another literal/constant.
	SubRuleLiteralVsLiteral

	// SubRuleOnlyNotNil corresponds to gate 4d sub-rule (iii): the
	// test's only assertions against SUT-derived values are
	// NotNil/NotEmpty/truthy/Nil checks (no equality / value
	// comparison).
	SubRuleOnlyNotNil

	// SubRuleZeroAssertion corresponds to gate 4d sub-rule (iv): the
	// test has zero assertions, or every assertion is trivially true
	// (assert.True(t, true), assert.Equal(t, x, x), etc.).
	SubRuleZeroAssertion
)

// String returns the spec-style label for a SubRule.
func (s SubRule) String() string {
	switch s {
	case SubRuleNoSUTDependence:
		return "i"
	case SubRuleLiteralVsLiteral:
		return "ii"
	case SubRuleOnlyNotNil:
		return "iii"
	case SubRuleZeroAssertion:
		return "iv"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// Violation reports a single sub-rule failure on a single test
// function.
type Violation struct {
	// SubRule is the gate-4d sub-rule that failed.
	SubRule SubRule
	// TestName is the name of the offending test function (e.g.
	// "TestQueue_LeaseExpired").
	TestName string
	// Pos is the position of the test function's declaration.
	Pos token.Position
	// Reason is a one-line human-readable explanation of why the
	// sub-rule fired (e.g. "all 3 assertions are literal-vs-literal").
	Reason string
}

// String formats a Violation as one diagnostic line, suitable for the
// gate runner's transitions log.
func (v Violation) String() string {
	return fmt.Sprintf("%s: tautology gate sub-rule (%s): %s — %s",
		v.Pos, v.SubRule, v.TestName, v.Reason)
}

// CheckSource runs the four tautology sub-rules over the Go source in
// src, identified by filename for diagnostic positions. sutNames lists
// the function-under-test names to track for sub-rule (i) and (iii);
// the gate runner derives this list from the dispatch envelope (the
// changed-source files' top-level funcs minus already-tested ones).
//
// If sutNames is empty, sub-rule (i) and (iii) are skipped — they
// require an SUT to reason about. Sub-rules (ii) and (iv) are purely
// syntactic and run unconditionally.
//
// Returns one Violation per (test function, failing sub-rule) pair. A
// single test that fails three sub-rules will contribute three
// Violation entries; the gate caller decides how to aggregate.
func CheckSource(filename string, src []byte, sutNames []string) ([]Violation, error) {
	if filename == "" {
		return nil, errors.New("tautology: filename is required")
	}
	if len(src) == 0 {
		return nil, errors.New("tautology: src is empty")
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("tautology: parse %s: %w", filename, err)
	}

	sutSet := map[string]bool{}
	for _, n := range sutNames {
		if n != "" {
			sutSet[n] = true
		}
	}

	var out []Violation
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || !isTestFunc(fn) {
			continue
		}
		out = append(out, checkTestFunc(fset, fn, sutSet)...)
	}
	return out, nil
}

// isTestFunc reports whether fn is a top-level `func TestXxx(t *testing.T)`.
func isTestFunc(fn *ast.FuncDecl) bool {
	if fn.Name == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
		return false
	}
	if fn.Recv != nil {
		return false
	}
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
		return false
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "testing" && sel.Sel.Name == "T"
}

// checkTestFunc runs all applicable sub-rules over a single test
// function and returns the resulting Violations.
func checkTestFunc(fset *token.FileSet, fn *ast.FuncDecl, sutSet map[string]bool) []Violation {
	pos := fset.Position(fn.Pos())
	var assertions []assertionSite
	collectAssertions(fn.Body, &assertions)

	var out []Violation

	// Sub-rule (iv): zero assertions OR every assertion is trivially
	// true. Run first because the syntactic rules below are vacuously
	// true on a function with no real assertions.
	if v, ok := checkZeroAssertion(fn.Name.Name, pos, assertions); ok {
		out = append(out, v)
		// Sub-rule (iv) subsumes (ii) and (iii) for this test —
		// emitting them too would be noisy without adding signal.
		return out
	}

	// Sub-rule (ii): every assertion is literal-vs-literal.
	if v, ok := checkLiteralVsLiteral(fn.Name.Name, pos, assertions); ok {
		out = append(out, v)
	}

	// Sub-rules (i) and (iii) require an SUT to reason about. If no
	// SUT names were provided, the gate caller has decided this file
	// has no SUT-of-record (e.g. a test for a top-level changed file
	// that exports no functions); skip silently.
	if len(sutSet) == 0 {
		return out
	}

	tainted, sutCallExists, sutWasStatementCall := computeTaint(fn.Body, sutSet)
	if sutWasStatementCall {
		applyGlobalSideEffectHeuristic(fn.Body.List, sutSet, tainted)
	}

	// Sub-rule (i): ≥1 assertion must reference an SUT-derived value
	// (taint hit) or contain an inline SUT call.
	if v, ok := checkSUTDependence(fn.Name.Name, pos, assertions, tainted, sutSet, sutCallExists); ok {
		out = append(out, v)
	}

	// Sub-rule (iii): only-NotNil / only-NotEmpty / only-truthy
	// assertions on SUT-tainted args.
	if v, ok := checkOnlyNotNil(fn.Name.Name, pos, assertions, tainted, sutSet); ok {
		out = append(out, v)
	}

	return out
}

// assertionSite records one assertion call (or if-cond-with-fail
// pattern) and the expressions the gate inspects for that assertion.
type assertionSite struct {
	pos      token.Pos
	exprs    []ast.Expr
	category string // "assert.Equal", "t.Errorf", "if-cond-with-fail", etc.
	method   string // selector method only (e.g. "Equal", "NotNil")
}

// collectAssertions walks root and appends every assertion site found.
//
// Recognized forms:
//   - testify-style:  assert.X(t, ...) / require.X(t, ...).
//   - t-failure:      t.Error / t.Errorf / t.Fatal / t.Fatalf / t.Fail / t.FailNow.
//   - if-cond-with-fail: `if cond { t.Error(...) }` — cond is the assertion.
func collectAssertions(root ast.Node, out *[]assertionSite) {
	ast.Inspect(root, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if cat, method, exprs, ok := classifyAssertionCall(x); ok {
				*out = append(*out, assertionSite{x.Pos(), exprs, cat, method})
			}
		case *ast.IfStmt:
			if ifBodyHasFailureCall(x.Body) {
				*out = append(*out, assertionSite{x.Pos(), []ast.Expr{x.Cond}, "if-cond-with-fail", ""})
			}
		}
		return true
	})
}

// classifyAssertionCall returns (category, method, args, true) if call
// is an assertion. The leading testing.T argument is dropped from args
// so callers don't have to special-case it.
func classifyAssertionCall(call *ast.CallExpr) (category, method string, args []ast.Expr, ok bool) {
	sel, sok := call.Fun.(*ast.SelectorExpr)
	if !sok {
		return "", "", nil, false
	}
	id, iok := sel.X.(*ast.Ident)
	if !iok {
		return "", "", nil, false
	}
	switch id.Name {
	case "assert", "require":
		args = call.Args
		if len(args) > 0 {
			args = args[1:] // drop t
		}
		return id.Name + "." + sel.Sel.Name, sel.Sel.Name, args, true
	case "t":
		switch sel.Sel.Name {
		case "Error", "Errorf", "Fatal", "Fatalf", "Fail", "FailNow":
			return "t." + sel.Sel.Name, sel.Sel.Name, call.Args, true
		}
	}
	return "", "", nil, false
}

// ifBodyHasFailureCall reports whether body contains a t.Error /
// t.Errorf / t.Fatal / t.Fatalf / t.Fail / t.FailNow call.
func ifBodyHasFailureCall(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if id.Name == "t" {
			switch sel.Sel.Name {
			case "Error", "Errorf", "Fatal", "Fatalf", "Fail", "FailNow":
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// notNilMethods is the set of testify (or t-failure-style) assertion
// methods that only check truthiness / non-nilness, not value equality.
// Sub-rule (iii) flags a test whose only SUT-tainted assertions all
// belong to this set.
var notNilMethods = map[string]bool{
	"NotNil":      true,
	"NotEmpty":    true,
	"NotZero":     true,
	"NotEqualf":   false, // value comparison — explicitly NOT in the set
	"True":        true,
	"NotFalse":    true,
	"Nil":         true,
	"Empty":       true,
	"Zero":        true,
	"Truef":       true,
	"NotNilf":     true,
	"NotEmptyf":   true,
	"NilRequire":  false, // not a real method; placeholder to make linter happy
	"Implements":  true,
	"IsType":      true,
	"NotImplements": true,
}

// ----------------------------------------------------------------------
// Sub-rule (iv): zero assertion / trivially true.
// ----------------------------------------------------------------------

// checkZeroAssertion fires sub-rule (iv) if there are no assertions OR
// if every assertion is trivially true (assert.True(t, true),
// assert.Equal(t, x, x), etc.).
func checkZeroAssertion(testName string, pos token.Position, assertions []assertionSite) (Violation, bool) {
	if len(assertions) == 0 {
		return Violation{
			SubRule:  SubRuleZeroAssertion,
			TestName: testName,
			Pos:      pos,
			Reason:   "test has zero assertions",
		}, true
	}
	for _, a := range assertions {
		if !isTriviallyTrue(a) {
			return Violation{}, false
		}
	}
	return Violation{
		SubRule:  SubRuleZeroAssertion,
		TestName: testName,
		Pos:      pos,
		Reason:   fmt.Sprintf("all %d assertions are trivially true (e.g. assert.True(t, true), assert.Equal(t, x, x))", len(assertions)),
	}, true
}

// isTriviallyTrue reports whether the assertion is one of the
// hard-coded trivially-true patterns.
func isTriviallyTrue(a assertionSite) bool {
	switch {
	// assert.True(t, true) / assert.False(t, false).
	case a.method == "True" && len(a.exprs) >= 1 && isLiteralBool(a.exprs[0], true):
		return true
	case a.method == "False" && len(a.exprs) >= 1 && isLiteralBool(a.exprs[0], false):
		return true
	// assert.Equal(t, x, x) — same expression on both sides.
	case (a.method == "Equal" || a.method == "Equalf" || a.method == "EqualValues") && len(a.exprs) >= 2:
		if astExprsEqual(a.exprs[0], a.exprs[1]) {
			return true
		}
	// t.Error / t.Errorf inside `if false { ... }` is trivially
	// dead, but we already model that via if-cond-with-fail with
	// cond=false.
	case a.category == "if-cond-with-fail" && len(a.exprs) == 1:
		if isLiteralBool(a.exprs[0], false) {
			// `if false { t.Error(...) }` is trivially-non-firing.
			return true
		}
	}
	return false
}

// isLiteralBool reports whether e is the boolean literal want.
func isLiteralBool(e ast.Expr, want bool) bool {
	id, ok := e.(*ast.Ident)
	if !ok {
		return false
	}
	if want {
		return id.Name == "true"
	}
	return id.Name == "false"
}

// astExprsEqual reports whether two expressions are structurally
// identical at the AST level (ignoring positions). Used by the
// trivially-true detector for `assert.Equal(t, x, x)`.
func astExprsEqual(a, b ast.Expr) bool {
	return exprText(a) == exprText(b) && exprText(a) != ""
}

// exprText returns a stable text form of an expression for structural
// comparison. We deliberately avoid go/printer here to keep this file
// dependency-light; the limited set of expressions we care about
// (idents, selectors, index expressions, literals) all formatize
// faithfully via this hand-rolled walker.
func exprText(e ast.Expr) string {
	if e == nil {
		return ""
	}
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.BasicLit:
		return x.Kind.String() + ":" + x.Value
	case *ast.SelectorExpr:
		return exprText(x.X) + "." + x.Sel.Name
	case *ast.IndexExpr:
		return exprText(x.X) + "[" + exprText(x.Index) + "]"
	case *ast.StarExpr:
		return "*" + exprText(x.X)
	case *ast.ParenExpr:
		return "(" + exprText(x.X) + ")"
	case *ast.CallExpr:
		var b strings.Builder
		b.WriteString(exprText(x.Fun))
		b.WriteByte('(')
		for i, a := range x.Args {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(exprText(a))
		}
		b.WriteByte(')')
		return b.String()
	case *ast.BinaryExpr:
		return exprText(x.X) + x.Op.String() + exprText(x.Y)
	case *ast.UnaryExpr:
		return x.Op.String() + exprText(x.X)
	}
	return ""
}

// ----------------------------------------------------------------------
// Sub-rule (ii): literal-vs-literal.
// ----------------------------------------------------------------------

// checkLiteralVsLiteral fires sub-rule (ii) if every assertion in the
// test compares a literal/constant to another literal/constant. An
// assertion with at least one non-literal argument absolves the test.
func checkLiteralVsLiteral(testName string, pos token.Position, assertions []assertionSite) (Violation, bool) {
	if len(assertions) == 0 {
		// Sub-rule (iv) handles this; (ii) doesn't fire on
		// zero-assertion tests.
		return Violation{}, false
	}
	for _, a := range assertions {
		if !allArgsLiteral(a) {
			return Violation{}, false
		}
	}
	return Violation{
		SubRule:  SubRuleLiteralVsLiteral,
		TestName: testName,
		Pos:      pos,
		Reason:   fmt.Sprintf("all %d assertions are literal-vs-literal (constants compared to constants)", len(assertions)),
	}, true
}

// allArgsLiteral reports whether every argument expression in the
// assertion is a literal or a parenthesized/unary literal. The leading
// t argument has already been stripped by classifyAssertionCall.
func allArgsLiteral(a assertionSite) bool {
	if len(a.exprs) == 0 {
		return false
	}
	for _, e := range a.exprs {
		if !isLiteralExpr(e) {
			return false
		}
	}
	return true
}

// isLiteralExpr reports whether e is a literal or trivial composition
// of literals: BasicLit, the bool/nil idents, ParenExpr around literal,
// UnaryExpr around literal (for `-1`), or a BinaryExpr whose operands
// are all literals.
func isLiteralExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.BasicLit:
		return true
	case *ast.Ident:
		return x.Name == "true" || x.Name == "false" || x.Name == "nil"
	case *ast.ParenExpr:
		return isLiteralExpr(x.X)
	case *ast.UnaryExpr:
		return isLiteralExpr(x.X)
	case *ast.BinaryExpr:
		return isLiteralExpr(x.X) && isLiteralExpr(x.Y)
	case *ast.CompositeLit:
		// Composite literal with literal-only elements (e.g.
		// []int{1, 2, 3}). Considered literal for sub-rule (ii)
		// because it contains no runtime values.
		for _, elt := range x.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if !isLiteralExpr(kv.Value) {
					return false
				}
				continue
			}
			if !isLiteralExpr(elt) {
				return false
			}
		}
		return true
	}
	return false
}

// ----------------------------------------------------------------------
// Sub-rule (i): SUT-dependence (taint analysis).
// ----------------------------------------------------------------------

// computeTaint builds a taint set for sub-rule (i). The returned bool
// pair is (sutCallExists, sutWasStatementCall).
//
// Taint sources:
//   - Assignment / decl whose RHS contains a call to any name in
//     sutSet → all LHS idents tainted.
//   - Pointer arg `&x` to a SUT call → x tainted (caller observes
//     mutation through the pointer).
//   - Receiver of a method call SUT → receiver tainted
//     (`x.SUT()` mutates x or returns x's state).
//
// Taint propagation: any assignment whose RHS reads a tainted ident
// taints the LHS, fixed-point.
func computeTaint(body *ast.BlockStmt, sutSet map[string]bool) (tainted map[string]bool, sutCallExists, sutWasStatementCall bool) {
	tainted = map[string]bool{}
	if body == nil {
		return tainted, false, false
	}

	var assigns []*ast.AssignStmt
	var decls []*ast.DeclStmt
	var bareCalls []*ast.ExprStmt
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			assigns = append(assigns, x)
		case *ast.DeclStmt:
			decls = append(decls, x)
		case *ast.ExprStmt:
			bareCalls = append(bareCalls, x)
		case *ast.CallExpr:
			if callTargetsAny(x, sutSet) {
				sutCallExists = true
			}
		}
		return true
	})

	// Step 1a: assignment-form taint sources.
	for _, as := range assigns {
		for _, rhs := range as.Rhs {
			if exprContainsAnySUTCall(rhs, sutSet) {
				for _, l := range as.Lhs {
					if id, ok := l.(*ast.Ident); ok && id.Name != "_" {
						tainted[id.Name] = true
					}
				}
			}
		}
		collectPointerArgTaints(as.Rhs, sutSet, tainted)
	}

	// Step 1b: var-decl form (`var x = SUT(...)`).
	for _, ds := range decls {
		gd, ok := ds.Decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, val := range vs.Values {
				if exprContainsAnySUTCall(val, sutSet) {
					for _, name := range vs.Names {
						if name.Name != "_" {
							tainted[name.Name] = true
						}
					}
				}
			}
		}
	}

	// Step 1c: bare-statement SUT call (no return captured).
	for _, es := range bareCalls {
		if !isAnySUTCallExpr(es.X, sutSet) {
			continue
		}
		sutWasStatementCall = true
		call := es.X.(*ast.CallExpr)
		collectPointerArgTaints([]ast.Expr{call}, sutSet, tainted)
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sutSet[sel.Sel.Name] {
			if id, ok := sel.X.(*ast.Ident); ok {
				tainted[id.Name] = true
			}
		}
	}

	// Step 2: propagate taint through assignments to fixed point.
	for changed := true; changed; {
		changed = false
		for _, as := range assigns {
			for _, rhs := range as.Rhs {
				if exprReferencesTaint(rhs, tainted, sutSet) {
					for _, l := range as.Lhs {
						if id, ok := l.(*ast.Ident); ok && id.Name != "_" {
							if !tainted[id.Name] {
								tainted[id.Name] = true
								changed = true
							}
						}
					}
				}
			}
		}
	}

	return tainted, sutCallExists, sutWasStatementCall
}

// callTargetsAny reports whether call invokes any name in sutSet,
// either as a direct identifier (`Foo()`) or as a method/selector
// (`x.Foo()`).
func callTargetsAny(call *ast.CallExpr, sutSet map[string]bool) bool {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return sutSet[fn.Name]
	case *ast.SelectorExpr:
		return sutSet[fn.Sel.Name]
	}
	return false
}

// exprContainsAnySUTCall reports whether any sub-expression of e is a
// call to a name in sutSet.
func exprContainsAnySUTCall(e ast.Expr, sutSet map[string]bool) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && callTargetsAny(call, sutSet) {
			found = true
			return false
		}
		return true
	})
	return found
}

// isAnySUTCallExpr reports whether e is itself a call to a name in
// sutSet.
func isAnySUTCallExpr(e ast.Expr, sutSet map[string]bool) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	return callTargetsAny(call, sutSet)
}

// collectPointerArgTaints scans exprs for SUT calls that take pointer
// arguments and taints the pointed-to identifiers. Handles direct
// SUT calls and SUT calls nested one level inside other expressions.
func collectPointerArgTaints(exprs []ast.Expr, sutSet map[string]bool, tainted map[string]bool) {
	for _, e := range exprs {
		ast.Inspect(e, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok || !callTargetsAny(c, sutSet) {
				return true
			}
			addPointerArgs(c, tainted)
			return true
		})
	}
}

// addPointerArgs taints any identifier passed as `&x` to call.
func addPointerArgs(call *ast.CallExpr, tainted map[string]bool) {
	for _, arg := range call.Args {
		u, ok := arg.(*ast.UnaryExpr)
		if !ok || u.Op != token.AND {
			continue
		}
		if id, ok := u.X.(*ast.Ident); ok {
			tainted[id.Name] = true
		}
		if sel, ok := u.X.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				tainted[id.Name] = true
			}
		}
	}
}

// exprReferencesTaint reports whether e reads any tainted identifier or
// contains an inline SUT call (which is an even stronger taint
// reference).
func exprReferencesTaint(e ast.Expr, tainted map[string]bool, sutSet map[string]bool) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if found {
			return false
		}
		switch x := n.(type) {
		case *ast.Ident:
			if tainted[x.Name] {
				found = true
				return false
			}
		case *ast.CallExpr:
			if callTargetsAny(x, sutSet) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// applyGlobalSideEffectHeuristic taints identifiers that appear both
// before AND after a bare-statement SUT call — the classic pattern
// `before := counter; Increment(); assert.Equal(t, before+1, counter)`.
func applyGlobalSideEffectHeuristic(stmts []ast.Stmt, sutSet map[string]bool, tainted map[string]bool) {
	sutIdx := -1
	for i, s := range stmts {
		if es, ok := s.(*ast.ExprStmt); ok && isAnySUTCallExpr(es.X, sutSet) {
			sutIdx = i
			break
		}
	}
	if sutIdx < 0 {
		return
	}
	before := map[string]bool{}
	for i := 0; i < sutIdx; i++ {
		for n := range identReadsIn(stmts[i]) {
			before[n] = true
		}
	}
	after := map[string]bool{}
	for i := sutIdx + 1; i < len(stmts); i++ {
		for n := range identReadsIn(stmts[i]) {
			after[n] = true
		}
	}
	for n := range before {
		if after[n] && !isBuiltinOrPunct(n) {
			tainted[n] = true
		}
	}
}

// identReadsIn returns the set of identifier names referenced anywhere
// in stmt. This is intentionally over-approximate: any text-position
// occurrence of an identifier counts as "read", which is fine for the
// heuristic (a false-positive taint here suppresses a sub-rule (i)
// false-positive elsewhere).
func identReadsIn(stmt ast.Stmt) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(stmt, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			out[id.Name] = true
		}
		return true
	})
	return out
}

// builtinIdents lists identifiers whose presence both before and after
// a SUT call should NOT count as evidence of observable side effect
// (they're language-builtin or test-harness names that appear in every
// test).
var builtinIdents = map[string]bool{
	"t": true, "true": true, "false": true, "nil": true,
	"len": true, "cap": true, "append": true, "make": true, "new": true,
	"int": true, "string": true, "bool": true, "byte": true, "error": true,
	"assert": true, "require": true,
	"_": true,
}

func isBuiltinOrPunct(name string) bool { return builtinIdents[name] }

// checkSUTDependence fires sub-rule (i) if no assertion in the test
// references an SUT-tainted value (no return-binding, pointer-arg,
// receiver, or global-side-effect taint hit; no inline SUT call).
//
// If the test body never calls the SUT at all, sub-rule (i) fires with
// the more specific reason "test never calls the SUT".
func checkSUTDependence(testName string, pos token.Position, assertions []assertionSite, tainted map[string]bool, sutSet map[string]bool, sutCallExists bool) (Violation, bool) {
	if len(assertions) == 0 {
		// Sub-rule (iv) handles zero-assertion already.
		return Violation{}, false
	}
	for _, a := range assertions {
		for _, e := range a.exprs {
			if exprReferencesTaint(e, tainted, sutSet) {
				return Violation{}, false
			}
		}
	}
	if !sutCallExists {
		return Violation{
			SubRule:  SubRuleNoSUTDependence,
			TestName: testName,
			Pos:      pos,
			Reason:   "test never calls the SUT",
		}, true
	}
	return Violation{
		SubRule:  SubRuleNoSUTDependence,
		TestName: testName,
		Pos:      pos,
		Reason:   "no assertion references the SUT's return value or observable side effect",
	}, true
}

// ----------------------------------------------------------------------
// Sub-rule (iii): only-NotNil / only-NotEmpty / only-truthy.
// ----------------------------------------------------------------------

// checkOnlyNotNil fires sub-rule (iii) if every SUT-tainted assertion
// belongs to notNilMethods (truthiness / non-nil checks, no equality).
//
// Tests with no SUT-tainted assertions are out of scope for (iii) —
// sub-rule (i) catches that case with a more specific diagnostic.
func checkOnlyNotNil(testName string, pos token.Position, assertions []assertionSite, tainted map[string]bool, sutSet map[string]bool) (Violation, bool) {
	var sutTainted []assertionSite
	for _, a := range assertions {
		hits := false
		for _, e := range a.exprs {
			if exprReferencesTaint(e, tainted, sutSet) {
				hits = true
				break
			}
		}
		if hits {
			sutTainted = append(sutTainted, a)
		}
	}
	if len(sutTainted) == 0 {
		// Out of scope — sub-rule (i) handles this case.
		return Violation{}, false
	}
	for _, a := range sutTainted {
		if !notNilMethods[a.method] {
			return Violation{}, false
		}
	}
	return Violation{
		SubRule:  SubRuleOnlyNotNil,
		TestName: testName,
		Pos:      pos,
		Reason:   fmt.Sprintf("all %d SUT-tainted assertions are NotNil/NotEmpty/truthy checks (no value comparison)", len(sutTainted)),
	}, true
}

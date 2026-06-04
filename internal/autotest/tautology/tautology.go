// Package tautology implements a static linter that detects tautological
// test assertions using go/ast. It enforces four sub-rules from gate 4d
// of the auto-test-pr formula:
//
//	(i)   ≥1 assertion must depend on the FUT's return value or observable
//	      side effect (flow-sensitive taint analysis).
//	(ii)  Reject tests where every assertion is literal-vs-literal
//	      (e.g. assert.Equal("x", "x")).
//	(iii) Reject tests whose only assertions against SUT are
//	      NotNil/NotEmpty/truthy checks.
//	(iv)  Reject assert(true) / expect(x).toBe(x) / zero-assertion tests.
//
// The package uses go/parser + go/ast directly (no shelling to external tools).
package tautology

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// Rule identifies which sub-rule fired.
type Rule int

const (
	RuleNoInputDerived Rule = iota + 1 // (i)
	RuleLiteral                        // (ii)
	RuleNotNilOnly                     // (iii)
	RuleZeroAssertion                  // (iv)
)

// String returns a human-readable label for the rule.
func (r Rule) String() string {
	switch r {
	case RuleNoInputDerived:
		return "no-input-derived"
	case RuleLiteral:
		return "literal"
	case RuleNotNilOnly:
		return "notnil"
	case RuleZeroAssertion:
		return "zero-assertion"
	}
	return "unknown"
}

// Finding represents a single linter violation.
type Finding struct {
	Pos      token.Position
	Rule     Rule
	Message  string
	FuncName string // Test function containing the violation
}

// AnalyzeFile parses a Go test file and returns findings for all sub-rules.
func AnalyzeFile(filename string, src []byte) ([]Finding, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	return AnalyzeAST(fset, f), nil
}

// AnalyzeAST runs all sub-rules on a parsed file.
func AnalyzeAST(fset *token.FileSet, f *ast.File) []Finding {
	// Collect bare names of all top-level functions declared in this file.
	// Functions defined here cannot be the function-under-test — they're
	// test fixtures, factories, or helpers. Excluding them from FUT
	// classification eliminates false negatives where helper-built values
	// flow into otherwise-tautological assertions.
	localFuncs := collectLocalFuncs(f)

	var findings []Finding

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || !isTestFunc(fn) {
			continue
		}
		findings = append(findings, analyzeTestFunc(fset, fn, localFuncs)...)
	}

	return findings
}

// collectLocalFuncs returns the set of bare function names declared at the
// top level of this file. Methods are also recorded by their bare name —
// since callName() returns "receiver.Method" (not "Type.Method"), bare-name
// matching is the closest syntactic approximation we can do without type info.
func collectLocalFuncs(f *ast.File) map[string]bool {
	locals := make(map[string]bool)
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil {
			continue
		}
		locals[fn.Name.Name] = true
	}
	return locals
}

// analyzeTestFunc runs all sub-rules on a single test function.
func analyzeTestFunc(fset *token.FileSet, fn *ast.FuncDecl, localFuncs map[string]bool) []Finding {
	if fn.Body == nil {
		return nil
	}

	var findings []Finding

	// Collect all assertion calls in the function.
	assertions := collectAssertions(fn.Body)

	// Sub-rule (iv): zero assertions.
	if len(assertions) == 0 {
		findings = append(findings, Finding{
			Pos:      fset.Position(fn.Pos()),
			Rule:     RuleZeroAssertion,
			Message:  "test function has zero assertions",
			FuncName: fn.Name.Name,
		})
		return findings
	}

	// Sub-rule (ii): all assertions are literal-vs-literal.
	if allLiteralVsLiteral(assertions) {
		findings = append(findings, Finding{
			Pos:      fset.Position(fn.Pos()),
			Rule:     RuleLiteral,
			Message:  "all assertions compare literals to literals",
			FuncName: fn.Name.Name,
		})
	}

	// Sub-rule (iii): only NotNil/NotEmpty/truthy assertions.
	if allTrivialChecks(assertions) {
		findings = append(findings, Finding{
			Pos:      fset.Position(fn.Pos()),
			Rule:     RuleNotNilOnly,
			Message:  "all assertions are trivial (NotNil/NotEmpty/True only)",
			FuncName: fn.Name.Name,
		})
	}

	// Sub-rule (i): no assertion depends on FUT output (flow-sensitive).
	if !anyInputDerived(fn.Body, assertions, localFuncs) {
		findings = append(findings, Finding{
			Pos:      fset.Position(fn.Pos()),
			Rule:     RuleNoInputDerived,
			Message:  "no assertion depends on function-under-test output",
			FuncName: fn.Name.Name,
		})
	}

	// Sub-rule (iv) — assert(true) / self-equal patterns.
	for _, a := range assertions {
		if f := checkAssertTrue(fset, fn, a); f != nil {
			findings = append(findings, *f)
		}
	}

	return findings
}

// assertion represents a single assertion call found in a test function.
type assertion struct {
	call *ast.CallExpr
	name string // e.g. "assert.Equal" or "t.Errorf"
	// args holds the meaningful expressions the assertion checks: for testify,
	// the comparison arguments (skip t, skip trailing msg); for stdlib, the
	// single enclosing `if` condition.
	args []ast.Expr
	// cmp is the comparison from the enclosing `if <cmp> { t.Errorf(...) }`
	// idiom, set only for standard-library assertions whose failure is guarded
	// by a single comparison condition. nil for testify-style assertions and
	// for stdlib failures not guarded by a plain comparison.
	cmp *ast.BinaryExpr
}

// collectAssertions walks the function body and returns all assertion calls.
//
// Two assertion shapes are recognized:
//
//	testify: assert.Equal(t, want, got) — the comparison operands are the
//	         call arguments, extracted by extractArgs.
//	stdlib:  if got != want { t.Errorf(...) } — the comparison lives in the
//	         enclosing `if` condition, not the failure call's arguments. The
//	         whole condition is lifted onto the assertion so the taint and
//	         tautology rules see it.
func collectAssertions(body *ast.BlockStmt) []assertion {
	var result []assertion

	// Pass 1: stdlib `if <cond> { t.Fail... }` idioms. The whole condition is
	// the assertion's subject — the taint analysis walks it to see whether a
	// function-under-test value flows into the check, whether directly
	// (if FUT(x) != want) or through a variable (got := FUT(); if got != want).
	// Record the failure calls so pass 2 does not double-count them.
	guarded := make(map[*ast.CallExpr]bool)
	ast.Inspect(body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		var cmp *ast.BinaryExpr
		if b, ok := ifStmt.Cond.(*ast.BinaryExpr); ok && isComparison(b.Op) {
			cmp = b // a single comparison carries tautology semantics
		}
		for _, call := range stdlibFailCalls(ifStmt.Body) {
			guarded[call] = true
			result = append(result, assertion{
				call: call,
				name: callName(call),
				args: []ast.Expr{ifStmt.Cond},
				cmp:  cmp,
			})
		}
		return true
	})

	// Pass 2: testify assertions and any stdlib failure calls not already
	// captured as a guarded idiom (e.g. inside a switch or unconditional).
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || guarded[call] {
			return true
		}
		name := callName(call)
		if !isAssertionCall(name) {
			return true
		}
		result = append(result, assertion{call: call, name: name, args: extractArgs(call, name)})
		return true
	})

	return result
}

// stdlibFailCalls returns the standard-library failure calls (t.Error,
// t.Fatalf, ...) that appear as direct statements in an `if` block body.
// Only direct children are considered so that nested `if`s are attributed to
// their own condition rather than an outer one.
func stdlibFailCalls(block *ast.BlockStmt) []*ast.CallExpr {
	if block == nil {
		return nil
	}
	var calls []*ast.CallExpr
	for _, stmt := range block.List {
		exprStmt, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := exprStmt.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		if isStdlibFailCall(callName(call)) {
			calls = append(calls, call)
		}
	}
	return calls
}

// isComparison reports whether op is an ordering/equality comparison operator.
func isComparison(op token.Token) bool {
	switch op {
	case token.EQL, token.NEQ, token.LSS, token.GTR, token.LEQ, token.GEQ:
		return true
	}
	return false
}

// --- Sub-rule (ii): literal-vs-literal ---

// allLiteralVsLiteral returns true if EVERY assertion in the function
// compares only literal values.
func allLiteralVsLiteral(assertions []assertion) bool {
	if len(assertions) == 0 {
		return false
	}
	for _, a := range assertions {
		if !isLiteralAssertion(a) {
			return false
		}
	}
	return true
}

// isLiteralAssertion returns true if this assertion only involves literals.
func isLiteralAssertion(a assertion) bool {
	if isEqualStyle(a.name) && len(a.args) >= 2 {
		return isLiteral(a.args[0]) && isLiteral(a.args[1])
	}
	if isSingleArgStyle(a.name) && len(a.args) >= 1 {
		return isLiteral(a.args[0])
	}
	return false
}

// isLiteral returns true if the expression is a compile-time constant.
func isLiteral(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return true
	case *ast.Ident:
		return e.Name == "true" || e.Name == "false" || e.Name == "nil"
	case *ast.UnaryExpr:
		// -1, +3, etc.
		return isLiteral(e.X)
	case *ast.CompositeLit:
		// []int{1, 2, 3} — all elements must be literal
		for _, elt := range e.Elts {
			if !isLiteral(elt) {
				return false
			}
		}
		return true
	case *ast.ParenExpr:
		return isLiteral(e.X)
	}
	return false
}

// --- Sub-rule (iii): NotNil/NotEmpty/truthy only ---

// allTrivialChecks returns true if every assertion in the function is
// a trivial existence check (NotNil, NotEmpty, True).
func allTrivialChecks(assertions []assertion) bool {
	if len(assertions) == 0 {
		return false
	}
	for _, a := range assertions {
		if !isTrivialCheck(a.name) {
			return false
		}
	}
	return true
}

// isTrivialCheck returns true for assertion functions that are "trivial"
// existence/truthiness checks.
func isTrivialCheck(name string) bool {
	trivial := map[string]bool{
		"assert.NotNil":    true,
		"assert.NotEmpty":  true,
		"assert.True":      true,
		"require.NotNil":   true,
		"require.NotEmpty": true,
		"require.True":     true,
		// NoError is trivial — it just checks err == nil
		"assert.NoError":  true,
		"require.NoError": true,
	}
	return trivial[name]
}

// --- Sub-rule (iv): assert(true) / self-equal ---

// checkAssertTrue detects patterns like assert.True(t, true) or
// assert.Equal(t, x, x) that are trivially true.
func checkAssertTrue(fset *token.FileSet, fn *ast.FuncDecl, a assertion) *Finding {
	// assert.True(t, true) / assert.False(t, false)
	if (a.name == "assert.True" || a.name == "require.True") && len(a.args) >= 1 {
		if ident, ok := a.args[0].(*ast.Ident); ok && ident.Name == "true" {
			return &Finding{
				Pos:      fset.Position(a.call.Pos()),
				Rule:     RuleZeroAssertion,
				Message:  "assert(true) is tautologically true",
				FuncName: fn.Name.Name,
			}
		}
	}
	if (a.name == "assert.False" || a.name == "require.False") && len(a.args) >= 1 {
		if ident, ok := a.args[0].(*ast.Ident); ok && ident.Name == "false" {
			return &Finding{
				Pos:      fset.Position(a.call.Pos()),
				Rule:     RuleZeroAssertion,
				Message:  "assert.False(false) is tautologically true",
				FuncName: fn.Name.Name,
			}
		}
	}

	// assert.Equal(t, x, x) — same identifier both sides.
	if isEqualStyle(a.name) && len(a.args) >= 2 {
		if exprEqual(a.args[0], a.args[1]) {
			return &Finding{
				Pos:      fset.Position(a.call.Pos()),
				Rule:     RuleZeroAssertion,
				Message:  "assertion compares identical expressions",
				FuncName: fn.Name.Name,
			}
		}
	}

	// stdlib idiom: if x != x { t.Errorf(...) } — the failure condition
	// compares identical expressions, so it is always false and the test can
	// never fail. (== / <= / >= are excluded: those always-fail rather than
	// always-pass, which is a different defect.)
	if a.cmp != nil && exprEqual(a.cmp.X, a.cmp.Y) {
		switch a.cmp.Op {
		case token.NEQ, token.LSS, token.GTR:
			return &Finding{
				Pos:      fset.Position(a.call.Pos()),
				Rule:     RuleZeroAssertion,
				Message:  "failure condition compares identical expressions (test can never fail)",
				FuncName: fn.Name.Name,
			}
		}
	}

	return nil
}

// exprEqual returns true if two expressions are syntactically identical.
func exprEqual(a, b ast.Expr) bool {
	switch x := a.(type) {
	case *ast.Ident:
		if y, ok := b.(*ast.Ident); ok {
			return x.Name == y.Name
		}
	case *ast.BasicLit:
		if y, ok := b.(*ast.BasicLit); ok {
			return x.Kind == y.Kind && x.Value == y.Value
		}
	case *ast.SelectorExpr:
		if y, ok := b.(*ast.SelectorExpr); ok {
			return exprEqual(x.X, y.X) && x.Sel.Name == y.Sel.Name
		}
	case *ast.IndexExpr:
		if y, ok := b.(*ast.IndexExpr); ok {
			return exprEqual(x.X, y.X) && exprEqual(x.Index, y.Index)
		}
	case *ast.CallExpr:
		if y, ok := b.(*ast.CallExpr); ok {
			if !exprEqual(x.Fun, y.Fun) || len(x.Args) != len(y.Args) {
				return false
			}
			for i := range x.Args {
				if !exprEqual(x.Args[i], y.Args[i]) {
					return false
				}
			}
			return true
		}
	}
	return false
}

// --- Sub-rule (i): flow-sensitive taint analysis ---

// anyInputDerived returns true if at least one assertion depends on a value
// returned from a function-under-test (not a test helper or stdlib setup).
//
// localFuncs lists names of functions declared in the same _test.go file —
// those are fixtures/helpers, never FUT, so calls to them do not introduce
// taint.
func anyInputDerived(body *ast.BlockStmt, assertions []assertion, localFuncs map[string]bool) bool {
	// Build taint map: variable -> source FUT name.
	tainted := make(map[string]string)
	propagateTaint(body.List, tainted, localFuncs)

	for _, a := range assertions {
		for _, arg := range a.args {
			if exprIsTainted(arg, tainted, localFuncs) {
				return true
			}
		}
	}
	return false
}

// propagateTaint walks statements and builds a taint map of variables
// that are derived from function-under-test calls.
func propagateTaint(stmts []ast.Stmt, tainted map[string]string, localFuncs map[string]bool) {
	for _, stmt := range stmts {
		propagateStmt(stmt, tainted, localFuncs)
	}
}

// propagateStmt processes a single statement for taint propagation.
func propagateStmt(stmt ast.Stmt, tainted map[string]string, localFuncs map[string]bool) {
	switch s := stmt.(type) {
	case *ast.AssignStmt:
		for i, lhs := range s.Lhs {
			ident, ok := lhs.(*ast.Ident)
			if !ok || ident.Name == "_" {
				continue
			}
			var rhs ast.Expr
			if len(s.Rhs) == 1 {
				rhs = s.Rhs[0]
			} else if i < len(s.Rhs) {
				rhs = s.Rhs[i]
			} else {
				continue
			}
			if src := classifyExpr(rhs, tainted, localFuncs); src != "" {
				tainted[ident.Name] = src
			}
		}
	case *ast.DeclStmt:
		if gd, ok := s.Decl.(*ast.GenDecl); ok {
			for _, spec := range gd.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					for i, name := range vs.Names {
						if i < len(vs.Values) {
							if src := classifyExpr(vs.Values[i], tainted, localFuncs); src != "" {
								tainted[name.Name] = src
							}
						}
					}
				}
			}
		}
	case *ast.IfStmt:
		if s.Init != nil {
			propagateStmt(s.Init, tainted, localFuncs)
		}
		if s.Body != nil {
			propagateTaint(s.Body.List, tainted, localFuncs)
		}
		if s.Else != nil {
			propagateStmt(s.Else, tainted, localFuncs)
		}
	case *ast.BlockStmt:
		if s != nil {
			propagateTaint(s.List, tainted, localFuncs)
		}
	case *ast.ForStmt:
		if s.Init != nil {
			propagateStmt(s.Init, tainted, localFuncs)
		}
		if s.Body != nil {
			propagateTaint(s.Body.List, tainted, localFuncs)
		}
	case *ast.RangeStmt:
		if src := exprTaintSource(s.X, tainted, localFuncs); src != "" {
			if key, ok := s.Key.(*ast.Ident); ok && key.Name != "_" {
				tainted[key.Name] = src
			}
			if s.Value != nil {
				if val, ok := s.Value.(*ast.Ident); ok && val.Name != "_" {
					tainted[val.Name] = src
				}
			}
		}
		if s.Body != nil {
			propagateTaint(s.Body.List, tainted, localFuncs)
		}
	case *ast.ExprStmt:
		// Subtest closures — t.Run(name, func(t *testing.T){ ... }) — carry the
		// FUT call and the assertions on its output. Assertions are collected
		// from inside closures (ast.Inspect recurses), so taint must follow.
		propagateFuncLits(s.X, tainted, localFuncs)
	}
}

// propagateFuncLits descends into any function literals nested in an
// expression (e.g. the closure passed to t.Run) and propagates taint through
// their bodies into the shared taint map.
func propagateFuncLits(expr ast.Expr, tainted map[string]string, localFuncs map[string]bool) {
	ast.Inspect(expr, func(n ast.Node) bool {
		fl, ok := n.(*ast.FuncLit)
		if !ok || fl.Body == nil {
			return true
		}
		propagateTaint(fl.Body.List, tainted, localFuncs)
		return false // body (incl. nested literals) handled by the recursion
	})
}

// classifyExpr determines if an expression introduces or propagates taint.
func classifyExpr(expr ast.Expr, tainted map[string]string, localFuncs map[string]bool) string {
	switch e := expr.(type) {
	case *ast.CallExpr:
		name := callName(e)
		if name == "" {
			return ""
		}
		if isTestHelper(name) {
			return ""
		}
		// Functions defined in the same _test.go file are fixtures or
		// helpers, never the FUT. Treat them as transparent: propagate
		// taint from their args (so a helper that wraps a real FUT call
		// still propagates) but do not introduce taint themselves.
		if isLocalFunc(name, localFuncs) {
			for _, arg := range e.Args {
				if src := exprTaintSource(arg, tainted, localFuncs); src != "" {
					return src
				}
			}
			return ""
		}
		if isStdlibCall(name) {
			// Transparent: propagate taint from args.
			for _, arg := range e.Args {
				if src := exprTaintSource(arg, tainted, localFuncs); src != "" {
					return src
				}
			}
			return ""
		}
		// Unknown function call — treat as potential FUT.
		// Check if args propagate existing taint first.
		for _, arg := range e.Args {
			if src := exprTaintSource(arg, tainted, localFuncs); src != "" {
				return name
			}
		}
		return name
	case *ast.Ident:
		if t, ok := tainted[e.Name]; ok {
			return t
		}
	case *ast.SelectorExpr:
		if src := exprTaintSource(e.X, tainted, localFuncs); src != "" {
			return src
		}
	case *ast.IndexExpr:
		if src := exprTaintSource(e.X, tainted, localFuncs); src != "" {
			return src
		}
	case *ast.SliceExpr:
		if src := exprTaintSource(e.X, tainted, localFuncs); src != "" {
			return src
		}
	case *ast.TypeAssertExpr:
		if src := exprTaintSource(e.X, tainted, localFuncs); src != "" {
			return src
		}
	case *ast.UnaryExpr:
		if src := exprTaintSource(e.X, tainted, localFuncs); src != "" {
			return src
		}
	case *ast.CompositeLit:
		for _, elt := range e.Elts {
			if src := exprTaintSource(elt, tainted, localFuncs); src != "" {
				return src
			}
		}
	case *ast.ParenExpr:
		return classifyExpr(e.X, tainted, localFuncs)
	case *ast.BinaryExpr:
		if src := exprTaintSource(e.X, tainted, localFuncs); src != "" {
			return src
		}
		if src := exprTaintSource(e.Y, tainted, localFuncs); src != "" {
			return src
		}
	}
	return ""
}

// exprTaintSource returns the taint source if the expression is tainted.
func exprTaintSource(expr ast.Expr, tainted map[string]string, localFuncs map[string]bool) string {
	if expr == nil {
		return ""
	}
	switch e := expr.(type) {
	case *ast.Ident:
		if t, ok := tainted[e.Name]; ok {
			return t
		}
	case *ast.SelectorExpr:
		return exprTaintSource(e.X, tainted, localFuncs)
	case *ast.IndexExpr:
		return exprTaintSource(e.X, tainted, localFuncs)
	case *ast.SliceExpr:
		return exprTaintSource(e.X, tainted, localFuncs)
	case *ast.CallExpr:
		name := callName(e)
		// Functions defined in this file are fixtures/helpers, not FUT —
		// fall through to argument inspection so wrapped real-FUT taint
		// still propagates.
		if name != "" && isLocalFunc(name, localFuncs) {
			for _, arg := range e.Args {
				if src := exprTaintSource(arg, tainted, localFuncs); src != "" {
					return src
				}
			}
		} else if name != "" && isStdlibCall(name) {
			for _, arg := range e.Args {
				if src := exprTaintSource(arg, tainted, localFuncs); src != "" {
					return src
				}
			}
		} else if name != "" && !isTestHelper(name) {
			return name
		}
	case *ast.TypeAssertExpr:
		return exprTaintSource(e.X, tainted, localFuncs)
	case *ast.UnaryExpr:
		return exprTaintSource(e.X, tainted, localFuncs)
	case *ast.ParenExpr:
		return exprTaintSource(e.X, tainted, localFuncs)
	case *ast.KeyValueExpr:
		return exprTaintSource(e.Value, tainted, localFuncs)
	case *ast.BinaryExpr:
		if src := exprTaintSource(e.X, tainted, localFuncs); src != "" {
			return src
		}
		return exprTaintSource(e.Y, tainted, localFuncs)
	}
	return ""
}

// exprIsTainted returns true if the expression depends on a tainted value.
func exprIsTainted(expr ast.Expr, tainted map[string]string, localFuncs map[string]bool) bool {
	return exprTaintSource(expr, tainted, localFuncs) != ""
}

// isLocalFunc returns true if name matches a top-level function declared
// in the current _test.go file. Only bare-name matches count: a selector
// call like "svc.Process" is intentionally NOT treated as local even if a
// "Process" method is declared in the file, because the receiver "svc"
// could be a real production type whose methods coincidentally share names
// with local helpers. Without type information we err on the side of
// keeping real-FUT detection intact.
func isLocalFunc(name string, localFuncs map[string]bool) bool {
	if localFuncs == nil {
		return false
	}
	return localFuncs[name]
}

// --- Helpers ---

// isTestFunc returns true if the function is a Test* function.
func isTestFunc(fn *ast.FuncDecl) bool {
	if fn.Name == nil {
		return false
	}
	return strings.HasPrefix(fn.Name.Name, "Test")
}

// callName extracts a function call name as a string.
func callName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			return x.Name + "." + fn.Sel.Name
		}
		// Method call on a non-trivial receiver, e.g. tt.state.IsStalled().
		// Return a dotted name so the call is classified as a FUT call — the
		// dot keeps it out of the bare-name local/stdlib/helper sets, matching
		// how single-receiver method calls (svc.Process) are already handled.
		return receiverName(fn.X) + "." + fn.Sel.Name
	}
	return ""
}

// receiverName renders a selector/ident receiver chain as a dotted string,
// e.g. tt.state -> "tt.state". Non-nameable receivers (index, call, ...)
// yield "", leaving a leading-dot name that is still treated as FUT.
func receiverName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		if r := receiverName(x.X); r != "" {
			return r + "." + x.Sel.Name
		}
	case *ast.ParenExpr:
		return receiverName(x.X)
	}
	return ""
}

// extractArgs returns the meaningful arguments to an assertion call,
// skipping t and trailing message args.
func extractArgs(call *ast.CallExpr, name string) []ast.Expr {
	args := call.Args
	if len(args) == 0 {
		return nil
	}

	// Testify-style: first arg is t.
	if isTestifyStyle(name) {
		if len(args) < 2 {
			return nil
		}
		args = args[1:] // skip t

		switch {
		case isEqualStyle(name) || isLenStyle(name):
			if len(args) >= 2 {
				return args[:2]
			}
		case isSingleArgStyle(name):
			return args[:1]
		default:
			if len(args) >= 1 {
				return args[:1]
			}
		}
		return nil
	}

	return nil
}

// isAssertionCall returns true for known test assertion calls — both
// testify-style helpers and standard-library failure calls.
func isAssertionCall(name string) bool {
	return isTestifyStyle(name) || isStdlibFailCall(name)
}

func isTestifyStyle(name string) bool {
	return strings.HasPrefix(name, "assert.") || strings.HasPrefix(name, "require.")
}

// isStdlibFailCall returns true for standard-library *testing.T failure calls.
// These signal an assertion in stdlib-style tests (e.g. the `if got != want {
// t.Errorf(...) }` idiom). Non-failing helpers like t.Log/t.Skip are excluded:
// they do not assert anything.
func isStdlibFailCall(name string) bool {
	switch name {
	case "t.Error", "t.Errorf", "t.Fatal", "t.Fatalf", "t.Fail", "t.FailNow":
		return true
	}
	return false
}

func isEqualStyle(name string) bool {
	eqs := map[string]bool{
		"assert.Equal": true, "assert.NotEqual": true,
		"assert.Equalf": true, "assert.NotEqualf": true,
		"require.Equal": true, "require.NotEqual": true,
		"require.Equalf": true, "require.NotEqualf": true,
		"assert.Greater": true, "assert.Less": true,
		"assert.GreaterOrEqual": true, "assert.LessOrEqual": true,
		"require.Greater": true, "require.Less": true,
		"require.GreaterOrEqual": true, "require.LessOrEqual": true,
		"assert.Same": true, "assert.NotSame": true,
		"require.Same": true, "require.NotSame": true,
		"assert.JSONEq": true, "require.JSONEq": true,
		"assert.Contains": true, "assert.NotContains": true,
		"require.Contains": true, "require.NotContains": true,
		"assert.Containsf": true, "require.Containsf": true,
		"assert.ElementsMatch": true, "require.ElementsMatch": true,
	}
	return eqs[name]
}

func isSingleArgStyle(name string) bool {
	singles := map[string]bool{
		"assert.True": true, "assert.False": true,
		"assert.Nil": true, "assert.NotNil": true,
		"assert.Empty": true, "assert.NotEmpty": true,
		"assert.NoError": true, "assert.Error": true,
		"require.True": true, "require.False": true,
		"require.Nil": true, "require.NotNil": true,
		"require.Empty": true, "require.NotEmpty": true,
		"require.NoError": true, "require.Error": true,
	}
	return singles[name]
}

func isLenStyle(name string) bool {
	return name == "assert.Len" || name == "require.Len"
}

// isTestHelper returns true for known test helpers that don't produce FUT output.
func isTestHelper(name string) bool {
	helpers := map[string]bool{
		"t.Helper": true, "t.Run": true, "t.Parallel": true,
		"t.Cleanup": true, "t.TempDir": true, "t.Setenv": true,
		"t.Fatal": true, "t.Fatalf": true, "t.Error": true,
		"t.Errorf": true, "t.Log": true, "t.Logf": true,
		"t.Skip": true, "t.Skipf": true, "testing.Short": true,
		// Assertions themselves are not FUT.
		"require.NoError": true, "require.Error": true,
		"assert.NoError": true, "assert.Error": true,
	}
	return helpers[name]
}

// isStdlibCall returns true for stdlib/builtin calls that are "transparent"
// to taint — they propagate taint from their arguments.
func isStdlibCall(name string) bool {
	prefixes := []string{
		"os.", "filepath.", "path.", "io.", "ioutil.",
		"strings.", "bytes.", "fmt.", "strconv.",
		"json.", "http.", "httptest.", "net.",
		"context.", "time.", "sync.", "sort.",
		"errors.", "regexp.", "math.",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	builtins := map[string]bool{
		"make": true, "append": true, "len": true, "cap": true,
		"new": true, "copy": true, "delete": true, "close": true,
		"string": true, "int": true, "int64": true, "float64": true,
		"byte": true, "rune": true, "bool": true,
	}
	return builtins[name]
}

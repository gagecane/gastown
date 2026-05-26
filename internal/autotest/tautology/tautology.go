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
	var findings []Finding

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || !isTestFunc(fn) {
			continue
		}
		findings = append(findings, analyzeTestFunc(fset, fn)...)
	}

	return findings
}

// analyzeTestFunc runs all sub-rules on a single test function.
func analyzeTestFunc(fset *token.FileSet, fn *ast.FuncDecl) []Finding {
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
	if !anyInputDerived(fn.Body, assertions) {
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
	call      *ast.CallExpr
	name      string   // e.g. "assert.Equal"
	args      []ast.Expr // meaningful args (skip t, skip trailing msg)
}

// collectAssertions walks the function body and returns all assertion calls.
func collectAssertions(body *ast.BlockStmt) []assertion {
	var result []assertion
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := callName(call)
		if !isAssertionCall(name) {
			return true
		}
		args := extractArgs(call, name)
		result = append(result, assertion{call: call, name: name, args: args})
		return true
	})
	return result
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
		"assert.NotNil":   true,
		"assert.NotEmpty": true,
		"assert.True":     true,
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
func anyInputDerived(body *ast.BlockStmt, assertions []assertion) bool {
	// Build taint map: variable -> source FUT name.
	tainted := make(map[string]string)
	propagateTaint(body.List, tainted)

	for _, a := range assertions {
		for _, arg := range a.args {
			if exprIsTainted(arg, tainted) {
				return true
			}
		}
	}
	return false
}

// propagateTaint walks statements and builds a taint map of variables
// that are derived from function-under-test calls.
func propagateTaint(stmts []ast.Stmt, tainted map[string]string) {
	for _, stmt := range stmts {
		propagateStmt(stmt, tainted)
	}
}

// propagateStmt processes a single statement for taint propagation.
func propagateStmt(stmt ast.Stmt, tainted map[string]string) {
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
			if src := classifyExpr(rhs, tainted); src != "" {
				tainted[ident.Name] = src
			}
		}
	case *ast.DeclStmt:
		if gd, ok := s.Decl.(*ast.GenDecl); ok {
			for _, spec := range gd.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					for i, name := range vs.Names {
						if i < len(vs.Values) {
							if src := classifyExpr(vs.Values[i], tainted); src != "" {
								tainted[name.Name] = src
							}
						}
					}
				}
			}
		}
	case *ast.IfStmt:
		if s.Init != nil {
			propagateStmt(s.Init, tainted)
		}
		if s.Body != nil {
			propagateTaint(s.Body.List, tainted)
		}
		if s.Else != nil {
			propagateStmt(s.Else, tainted)
		}
	case *ast.BlockStmt:
		if s != nil {
			propagateTaint(s.List, tainted)
		}
	case *ast.ForStmt:
		if s.Init != nil {
			propagateStmt(s.Init, tainted)
		}
		if s.Body != nil {
			propagateTaint(s.Body.List, tainted)
		}
	case *ast.RangeStmt:
		if src := exprTaintSource(s.X, tainted); src != "" {
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
			propagateTaint(s.Body.List, tainted)
		}
	}
}

// classifyExpr determines if an expression introduces or propagates taint.
func classifyExpr(expr ast.Expr, tainted map[string]string) string {
	switch e := expr.(type) {
	case *ast.CallExpr:
		name := callName(e)
		if name == "" {
			return ""
		}
		if isTestHelper(name) {
			return ""
		}
		if isStdlibCall(name) {
			// Transparent: propagate taint from args.
			for _, arg := range e.Args {
				if src := exprTaintSource(arg, tainted); src != "" {
					return src
				}
			}
			return ""
		}
		// Unknown function call — treat as potential FUT.
		// Check if args propagate existing taint first.
		for _, arg := range e.Args {
			if src := exprTaintSource(arg, tainted); src != "" {
				return name
			}
		}
		return name
	case *ast.Ident:
		if t, ok := tainted[e.Name]; ok {
			return t
		}
	case *ast.SelectorExpr:
		if src := exprTaintSource(e.X, tainted); src != "" {
			return src
		}
	case *ast.IndexExpr:
		if src := exprTaintSource(e.X, tainted); src != "" {
			return src
		}
	case *ast.SliceExpr:
		if src := exprTaintSource(e.X, tainted); src != "" {
			return src
		}
	case *ast.TypeAssertExpr:
		if src := exprTaintSource(e.X, tainted); src != "" {
			return src
		}
	case *ast.UnaryExpr:
		if src := exprTaintSource(e.X, tainted); src != "" {
			return src
		}
	case *ast.CompositeLit:
		for _, elt := range e.Elts {
			if src := exprTaintSource(elt, tainted); src != "" {
				return src
			}
		}
	case *ast.ParenExpr:
		return classifyExpr(e.X, tainted)
	case *ast.BinaryExpr:
		if src := exprTaintSource(e.X, tainted); src != "" {
			return src
		}
		if src := exprTaintSource(e.Y, tainted); src != "" {
			return src
		}
	}
	return ""
}

// exprTaintSource returns the taint source if the expression is tainted.
func exprTaintSource(expr ast.Expr, tainted map[string]string) string {
	if expr == nil {
		return ""
	}
	switch e := expr.(type) {
	case *ast.Ident:
		if t, ok := tainted[e.Name]; ok {
			return t
		}
	case *ast.SelectorExpr:
		return exprTaintSource(e.X, tainted)
	case *ast.IndexExpr:
		return exprTaintSource(e.X, tainted)
	case *ast.SliceExpr:
		return exprTaintSource(e.X, tainted)
	case *ast.CallExpr:
		name := callName(e)
		if name != "" && isStdlibCall(name) {
			for _, arg := range e.Args {
				if src := exprTaintSource(arg, tainted); src != "" {
					return src
				}
			}
		} else if name != "" && !isTestHelper(name) {
			return name
		}
	case *ast.TypeAssertExpr:
		return exprTaintSource(e.X, tainted)
	case *ast.UnaryExpr:
		return exprTaintSource(e.X, tainted)
	case *ast.ParenExpr:
		return exprTaintSource(e.X, tainted)
	case *ast.KeyValueExpr:
		return exprTaintSource(e.Value, tainted)
	case *ast.BinaryExpr:
		if src := exprTaintSource(e.X, tainted); src != "" {
			return src
		}
		return exprTaintSource(e.Y, tainted)
	}
	return ""
}

// exprIsTainted returns true if the expression depends on a tainted value.
func exprIsTainted(expr ast.Expr, tainted map[string]string) bool {
	return exprTaintSource(expr, tainted) != ""
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

// isAssertionCall returns true for known test assertion calls.
func isAssertionCall(name string) bool {
	return isTestifyStyle(name)
}

func isTestifyStyle(name string) bool {
	return strings.HasPrefix(name, "assert.") || strings.HasPrefix(name, "require.")
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

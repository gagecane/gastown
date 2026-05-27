// Package tautology implements a flow-sensitive analyzer that detects
// tautological test assertions — assertions where both the actual and
// expected values depend on the same function-under-test return value.
//
// Sub-rule (i): "Does any assertion's argument depend on a value returned
// from the function-under-test?"
//
// A tautological assertion is one where the "expected" value is derived
// from the same computation as the "actual" value, making the assertion
// trivially true regardless of correctness.
package tautology

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// Finding represents a single detected tautological assertion.
type Finding struct {
	Pos         token.Position
	Message     string
	FuncName    string // test function containing the assertion
	AssertCall  string // e.g. "assert.Equal"
	TaintSource string // the FUT call that taints both sides
}

// AnalyzeFile parses a Go test file and returns tautological findings.
func AnalyzeFile(filename string, src []byte) ([]Finding, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	return AnalyzeAST(fset, f), nil
}

// AnalyzeAST performs flow-sensitive taint analysis on a parsed file.
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

// isTestFunc returns true if fn is a Test* function.
func isTestFunc(fn *ast.FuncDecl) bool {
	if fn.Name == nil {
		return false
	}
	name := fn.Name.Name
	return strings.HasPrefix(name, "Test")
}

// analyzeTestFunc performs flow-sensitive taint analysis on a single test function.
func analyzeTestFunc(fset *token.FileSet, fn *ast.FuncDecl) []Finding {
	if fn.Body == nil {
		return nil
	}

	// tainted maps variable names to the FUT call expression string that produced them.
	tainted := make(map[string]string)
	var findings []Finding

	for _, stmt := range fn.Body.List {
		findings = append(findings, analyzeStmt(fset, fn, stmt, tainted)...)
	}

	return findings
}

// analyzeStmt processes a single statement for taint propagation and assertion checking.
func analyzeStmt(fset *token.FileSet, fn *ast.FuncDecl, stmt ast.Stmt, tainted map[string]string) []Finding {
	var findings []Finding

	switch s := stmt.(type) {
	case *ast.AssignStmt:
		analyzeAssign(s, tainted)
	case *ast.ExprStmt:
		if f := analyzeExprStmt(fset, fn, s, tainted); f != nil {
			findings = append(findings, *f)
		}
	case *ast.IfStmt:
		if s.Init != nil {
			findings = append(findings, analyzeStmt(fset, fn, s.Init, tainted)...)
		}
		if s.Body != nil {
			for _, bs := range s.Body.List {
				findings = append(findings, analyzeStmt(fset, fn, bs, tainted)...)
			}
		}
		if s.Else != nil {
			findings = append(findings, analyzeStmt(fset, fn, s.Else, tainted)...)
		}
	case *ast.BlockStmt:
		if s != nil {
			for _, bs := range s.List {
				findings = append(findings, analyzeStmt(fset, fn, bs, tainted)...)
			}
		}
	case *ast.ForStmt:
		if s.Init != nil {
			findings = append(findings, analyzeStmt(fset, fn, s.Init, tainted)...)
		}
		if s.Body != nil {
			for _, bs := range s.Body.List {
				findings = append(findings, analyzeStmt(fset, fn, bs, tainted)...)
			}
		}
	case *ast.RangeStmt:
		// Taint loop variables from tainted range source.
		if src := exprToTaint(s.X, tainted); src != "" {
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
			for _, bs := range s.Body.List {
				findings = append(findings, analyzeStmt(fset, fn, bs, tainted)...)
			}
		}
	case *ast.DeclStmt:
		// Handle var declarations
		if gd, ok := s.Decl.(*ast.GenDecl); ok {
			for _, spec := range gd.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					for i, name := range vs.Names {
						if i < len(vs.Values) {
							if src := classifyRHS(vs.Values[i], tainted); src != "" {
								tainted[name.Name] = src
							}
						}
					}
				}
			}
		}
	}
	return findings
}

// analyzeAssign processes assignment statements to track taint flow.
func analyzeAssign(s *ast.AssignStmt, tainted map[string]string) {
	for i, lhs := range s.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok || ident.Name == "_" {
			continue
		}

		var rhs ast.Expr
		if len(s.Rhs) == 1 {
			// Multi-value return: all LHS vars are tainted by the single call.
			rhs = s.Rhs[0]
		} else if i < len(s.Rhs) {
			rhs = s.Rhs[i]
		} else {
			continue
		}

		if src := classifyRHS(rhs, tainted); src != "" {
			tainted[ident.Name] = src
		}
	}
}

// classifyRHS determines if an RHS expression introduces or propagates taint.
// Returns the FUT source name if tainted, empty string otherwise.
func classifyRHS(expr ast.Expr, tainted map[string]string) string {
	switch e := expr.(type) {
	case *ast.CallExpr:
		name := callName(e)
		if name == "" {
			return ""
		}
		// Skip test helpers and well-known non-FUT calls.
		if isTestHelper(name) || isStdlibSetup(name) {
			return ""
		}
		// If the call wraps a tainted argument, propagate taint.
		for _, arg := range e.Args {
			if src := exprToTaint(arg, tainted); src != "" {
				return name // new taint source from wrapping call
			}
		}
		return name

	case *ast.SelectorExpr:
		if src := exprToTaint(e.X, tainted); src != "" {
			return src
		}

	case *ast.IndexExpr:
		if src := exprToTaint(e.X, tainted); src != "" {
			return src
		}

	case *ast.SliceExpr:
		if src := exprToTaint(e.X, tainted); src != "" {
			return src
		}

	case *ast.TypeAssertExpr:
		if src := exprToTaint(e.X, tainted); src != "" {
			return src
		}

	case *ast.UnaryExpr:
		if src := exprToTaint(e.X, tainted); src != "" {
			return src
		}

	case *ast.Ident:
		if t, ok := tainted[e.Name]; ok {
			return t
		}

	case *ast.CompositeLit:
		// Struct/slice/map literals: tainted if any element is tainted
		for _, elt := range e.Elts {
			if src := exprToTaint(elt, tainted); src != "" {
				return src
			}
		}

	case *ast.ParenExpr:
		return classifyRHS(e.X, tainted)
	}
	return ""
}

// exprToTaint returns the taint source for an expression, if any.
func exprToTaint(expr ast.Expr, tainted map[string]string) string {
	if expr == nil {
		return ""
	}
	switch e := expr.(type) {
	case *ast.Ident:
		if t, ok := tainted[e.Name]; ok {
			return t
		}
	case *ast.SelectorExpr:
		return exprToTaint(e.X, tainted)
	case *ast.IndexExpr:
		return exprToTaint(e.X, tainted)
	case *ast.SliceExpr:
		return exprToTaint(e.X, tainted)
	case *ast.CallExpr:
		name := callName(e)
		if name != "" && !isTestHelper(name) && !isStdlibSetup(name) {
			return name
		}
		// Check arguments for taint propagation through stdlib calls
		if isStdlibSetup(name) {
			for _, arg := range e.Args {
				if src := exprToTaint(arg, tainted); src != "" {
					return src
				}
			}
		}
	case *ast.TypeAssertExpr:
		return exprToTaint(e.X, tainted)
	case *ast.UnaryExpr:
		return exprToTaint(e.X, tainted)
	case *ast.ParenExpr:
		return exprToTaint(e.X, tainted)
	case *ast.KeyValueExpr:
		return exprToTaint(e.Value, tainted)
	}
	return ""
}

// analyzeExprStmt checks expression statements for assertion calls with tainted args.
func analyzeExprStmt(fset *token.FileSet, fn *ast.FuncDecl, s *ast.ExprStmt, tainted map[string]string) *Finding {
	call, ok := s.X.(*ast.CallExpr)
	if !ok {
		return nil
	}

	assertName := callName(call)
	if !isAssertionCall(assertName) {
		return nil
	}

	return checkAssertionTaint(fset, fn, call, assertName, tainted)
}

// checkAssertionTaint examines an assertion call for tautological patterns.
func checkAssertionTaint(fset *token.FileSet, fn *ast.FuncDecl, call *ast.CallExpr, assertName string, tainted map[string]string) *Finding {
	args := assertionArgs(call, assertName)
	if args == nil {
		return nil
	}

	// For Equal-style assertions: args[0] is expected, args[1] is actual.
	if len(args) >= 2 && isEqualAssertion(assertName) {
		src0 := exprToTaint(args[0], tainted)
		src1 := exprToTaint(args[1], tainted)

		// For Contains-style assertions, the semantics are (haystack, needle).
		// The haystack (arg[0]) is naturally tainted (it IS the FUT output).
		// Only flag if BOTH sides are tainted (needle also from FUT).
		if isContainsAssertion(assertName) {
			if src0 != "" && src1 != "" {
				return &Finding{
					Pos:         fset.Position(call.Pos()),
					Message:     "both arguments to " + assertName + " depend on FUT output",
					FuncName:    fn.Name.Name,
					AssertCall:  assertName,
					TaintSource: src0,
				}
			}
			return nil // haystack-only taint is expected for Contains
		}

		// Tautology: both sides depend on the same FUT.
		if src0 != "" && src1 != "" {
			return &Finding{
				Pos:         fset.Position(call.Pos()),
				Message:     "both arguments to " + assertName + " depend on FUT output",
				FuncName:    fn.Name.Name,
				AssertCall:  assertName,
				TaintSource: src0,
			}
		}

		// Sub-rule (i): if the "expected" position (first non-t arg) is tainted
		// by a FUT, that's tautological — expected should be independent.
		if src0 != "" {
			return &Finding{
				Pos:         fset.Position(call.Pos()),
				Message:     "expected argument to " + assertName + " depends on FUT " + src0,
				FuncName:    fn.Name.Name,
				AssertCall:  assertName,
				TaintSource: src0,
			}
		}
	}

	// For single-argument assertions (True/False), check if the argument is
	// a comparison where both sides are tainted by the same FUT.
	if len(args) >= 1 && isSingleArgAssertion(assertName) {
		if binExpr, ok := args[0].(*ast.BinaryExpr); ok {
			srcX := exprToTaint(binExpr.X, tainted)
			srcY := exprToTaint(binExpr.Y, tainted)
			if srcX != "" && srcY != "" {
				return &Finding{
					Pos:         fset.Position(call.Pos()),
					Message:     "comparison in " + assertName + " has both sides depending on FUT",
					FuncName:    fn.Name.Name,
					AssertCall:  assertName,
					TaintSource: srcX,
				}
			}
		}
	}

	return nil
}

// assertionArgs returns the meaningful arguments to an assertion call,
// skipping the testing.T parameter and trailing message arguments.
func assertionArgs(call *ast.CallExpr, name string) []ast.Expr {
	args := call.Args
	if len(args) == 0 {
		return nil
	}

	// testify-style: first arg is t, then assertion args.
	if isTestifyAssertion(name) {
		if len(args) < 2 {
			return nil
		}
		args = args[1:] // skip t

		switch {
		case isEqualAssertion(name):
			if len(args) >= 2 {
				return args[:2]
			}
		case isSingleArgAssertion(name):
			return args[:1]
		case isLenAssertion(name):
			if len(args) >= 2 {
				return args[:2]
			}
		default:
			if len(args) >= 1 {
				return args[:1]
			}
		}
		return nil
	}

	return nil
}

// callName extracts the name of a function call as a string.
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

// isTestHelper returns true for known test helpers that don't produce
// the function-under-test's output.
func isTestHelper(name string) bool {
	helpers := map[string]bool{
		"t.Helper":        true,
		"t.Run":           true,
		"t.Parallel":      true,
		"t.Cleanup":       true,
		"t.TempDir":       true,
		"t.Setenv":        true,
		"t.Fatal":         true,
		"t.Fatalf":        true,
		"t.Error":         true,
		"t.Errorf":        true,
		"t.Log":           true,
		"t.Logf":          true,
		"t.Skip":          true,
		"t.Skipf":         true,
		"testing.Short":   true,
		"require.NoError": true,
		"require.Error":   true,
		"assert.NoError":  true,
		"assert.Error":    true,
	}
	return helpers[name]
}

// isStdlibSetup returns true for stdlib calls that set up test fixtures
// (not the FUT). These are "transparent" to taint — taint passes through them.
func isStdlibSetup(name string) bool {
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
	// Builtins — these propagate taint from their arguments.
	builtins := map[string]bool{
		"make": true, "append": true, "len": true, "cap": true,
		"new": true, "copy": true, "delete": true, "close": true,
		"panic": true, "recover": true, "print": true, "println": true,
		"string": true, "int": true, "int64": true, "float64": true,
		"byte": true, "rune": true, "bool": true,
	}
	return builtins[name]
}

// isAssertionCall returns true if the call is a test assertion.
func isAssertionCall(name string) bool {
	return isTestifyAssertion(name)
}

func isTestifyAssertion(name string) bool {
	assertFuncs := map[string]bool{
		"assert.Equal": true, "assert.NotEqual": true,
		"assert.True": true, "assert.False": true,
		"assert.Nil": true, "assert.NotNil": true,
		"assert.Contains": true, "assert.NotContains": true,
		"assert.Len": true, "assert.Empty": true, "assert.NotEmpty": true,
		"assert.Greater": true, "assert.Less": true,
		"assert.GreaterOrEqual": true, "assert.LessOrEqual": true,
		"assert.Same": true, "assert.NotSame": true,
		"assert.ElementsMatch": true,
		"assert.JSONEq":        true,
		"assert.Equalf":        true, "assert.NotEqualf": true,
		"assert.Containsf": true,
		"require.Equal":    true, "require.NotEqual": true,
		"require.True": true, "require.False": true,
		"require.Nil": true, "require.NotNil": true,
		"require.Contains": true, "require.NotContains": true,
		"require.Len": true, "require.Empty": true, "require.NotEmpty": true,
		"require.Greater": true, "require.Less": true,
		"require.GreaterOrEqual": true, "require.LessOrEqual": true,
		"require.Same": true, "require.NotSame": true,
		"require.ElementsMatch": true,
		"require.JSONEq":        true,
		"require.Equalf":        true, "require.NotEqualf": true,
	}
	return assertFuncs[name]
}

func isEqualAssertion(name string) bool {
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

func isSingleArgAssertion(name string) bool {
	singles := map[string]bool{
		"assert.True": true, "assert.False": true,
		"assert.Nil": true, "assert.NotNil": true,
		"assert.Empty": true, "assert.NotEmpty": true,
		"require.True": true, "require.False": true,
		"require.Nil": true, "require.NotNil": true,
		"require.Empty": true, "require.NotEmpty": true,
	}
	return singles[name]
}

func isLenAssertion(name string) bool {
	return name == "assert.Len" || name == "require.Len"
}

func isContainsAssertion(name string) bool {
	contains := map[string]bool{
		"assert.Contains":     true,
		"assert.NotContains":  true,
		"assert.Containsf":    true,
		"require.Contains":    true,
		"require.NotContains": true,
		"require.Containsf":   true,
	}
	return contains[name]
}

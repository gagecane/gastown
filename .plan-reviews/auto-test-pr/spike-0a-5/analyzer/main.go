// Spike-0a-5 prototype analyzer for tautology sub-rule (i).
//
// Sub-rule (i): "≥1 assertion must depend on the function-under-test's
// return value or observable side effect."
//
// Approach (flow-sensitive, intra-function, AST-only):
//
//  1. Each fixture file contains a `// SUT: <Name>` annotation on line 1.
//     The analyzer reads that annotation to learn the SUT name. (In
//     production the gate would derive SUT from package + naming
//     conventions; the spike fixes the SUT name to keep the
//     precision/recall measurement free of SUT-detection noise.)
//
//  2. For each `func TestXxx(t *testing.T)`:
//
//     a. Build a taint set seeded by SUT call sites:
//        - Return values bound via assignment/short-decl → tainted.
//        - Pointer arguments to the SUT (`SUT(&x, ...)`) → x tainted.
//        - Receiver of a method call where SUT is the method → receiver
//          tainted.
//
//     b. Propagate taint to fixed point: any assignment whose RHS reads
//        a tainted ident taints the LHS; field/index/star/method
//        expressions of tainted bases stay tainted.
//
//     c. Side-effect-on-global heuristic: if the SUT is called as a
//        bare statement (no return captured, no pointer arg) and an
//        identifier read AFTER the call is also read BEFORE the call,
//        treat that identifier as tainted. This catches the
//        before/after global-mutation pattern (`before := counter;
//        Increment(); assert.Equal(t, before+1, counter)`).
//
//  3. Identify assertion call sites (testify `assert.X` / `require.X`,
//     `t.Error*` / `t.Fatal*` / `t.Fail*`, and the condition of any
//     `if`-statement whose body contains a `t.Error*` / `t.Fatal*`
//     call). For each assertion, walk every argument (or condition)
//     subtree and check whether it references:
//        - any identifier in the taint set, OR
//        - a call expression to the SUT (inline call form
//          `assert.Equal(t, want, SUT(in))`).
//     If found, the test PASSES sub-rule (i). Otherwise, flag.
//
// Output: per-fixture verdict, confusion matrix, precision, recall.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const sutAnnotationPrefix = "// SUT:"

// classification result for a single test function.
type verdict struct {
	test     string
	flagged  bool
	rationale string
}
type assertion struct {
	pos      token.Pos
	exprs    []ast.Expr
	category string
}


func main() {
	flag.Parse()
	args := flag.Args()
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: analyzer <corpus-dir>")
		os.Exit(2)
	}
	corpusDir := args[0]

	tautDir := filepath.Join(corpusDir, "tautological")
	goodDir := filepath.Join(corpusDir, "good")

	tautFiles, err := listFixtures(tautDir)
	if err != nil {
		die(err)
	}
	goodFiles, err := listFixtures(goodDir)
	if err != nil {
		die(err)
	}

	var tp, fp, tn, fn int
	var details []string

	process := func(files []string, isTautological bool) {
		for _, f := range files {
			vs, err := analyze(f)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR %s: %v\n", f, err)
				os.Exit(1)
			}
			if len(vs) == 0 {
				fmt.Fprintf(os.Stderr, "ERROR %s: no test function found\n", f)
				os.Exit(1)
			}
			// Each fixture has exactly one test function.
			v := vs[0]
			label := "good"
			if isTautological {
				label = "tautological"
			}
			outcome := "PASS"
			if v.flagged {
				outcome = "FLAG"
			}
			detail := fmt.Sprintf("  [%s] %-50s ground-truth=%-12s analyzer=%s  %s",
				humanResult(v.flagged, isTautological), filepath.Base(f), label, outcome, v.rationale)
			details = append(details, detail)

			switch {
			case isTautological && v.flagged:
				tp++
			case isTautological && !v.flagged:
				fn++
			case !isTautological && v.flagged:
				fp++
			case !isTautological && !v.flagged:
				tn++
			}
		}
	}

	process(tautFiles, true)
	process(goodFiles, false)

	sort.Strings(details)
	for _, d := range details {
		fmt.Println(d)
	}

	fmt.Println()
	fmt.Println("Confusion matrix")
	fmt.Println("                 actual=tautological  actual=good")
	fmt.Printf("predicted=flag         %4d                 %4d\n", tp, fp)
	fmt.Printf("predicted=pass         %4d                 %4d\n", fn, tn)

	precision := safeDiv(tp, tp+fp)
	recall := safeDiv(tp, tp+fn)
	fmt.Println()
	fmt.Printf("Precision = TP/(TP+FP) = %d/%d = %.3f\n", tp, tp+fp, precision)
	fmt.Printf("Recall    = TP/(TP+FN) = %d/%d = %.3f\n", tp, tp+fn, recall)
	fmt.Printf("FP rate (on known-good) = FP/(FP+TN) = %d/%d = %.3f\n", fp, fp+tn, safeDiv(fp, fp+tn))
	fmt.Printf("FN rate (on known-taut) = FN/(FN+TP) = %d/%d = %.3f\n", fn, fn+tp, safeDiv(fn, fn+tp))

	fmt.Println()
	const precThreshold, recallThreshold = 0.85, 0.75
	if precision >= precThreshold && recall >= recallThreshold {
		fmt.Printf("RESULT: PASS (precision %.3f ≥ %.2f AND recall %.3f ≥ %.2f)\n",
			precision, precThreshold, recall, recallThreshold)
	} else {
		fmt.Printf("RESULT: FAIL (need precision ≥ %.2f AND recall ≥ %.2f)\n",
			precThreshold, recallThreshold)
	}
}

func listFixtures(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".txt") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

func analyze(path string) ([]verdict, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sut := readSUTAnnotation(src)
	if sut == "" {
		return nil, fmt.Errorf("missing %q annotation", sutAnnotationPrefix)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	var results []verdict
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || !isTestFunc(fn) {
			continue
		}
		flagged, rationale := classifyTestFunc(fn, sut)
		results = append(results, verdict{
			test:      fn.Name.Name,
			flagged:   flagged,
			rationale: rationale,
		})
	}
	return results, nil
}

func readSUTAnnotation(src []byte) string {
	// First line is the SUT annotation.
	lines := strings.SplitN(string(src), "\n", 2)
	if len(lines) == 0 {
		return ""
	}
	first := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(first, sutAnnotationPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(first, sutAnnotationPrefix))
}

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

// classifyTestFunc returns (flagged, rationale) for a single Test* function.
//
// flagged=true means: sub-rule (i) considers this test tautological (no
// assertion observably depends on the SUT's return value or side effect).
func classifyTestFunc(fn *ast.FuncDecl, sut string) (bool, string) {
	if fn.Body == nil {
		return true, "empty body"
	}

	// Step 1: collect SUT call sites and pointer-arg / receiver taint
	// sources. While walking, we'll also remember each statement's
	// position so the side-effect-on-global heuristic can sequence
	// reads.
	tainted := map[string]bool{}

	// Collect every assignment / decl / bare-call statement in the
	// function body (any depth — for/range body, if/else, switch case,
	// FuncLit body via t.Run, etc.) and feed them into one taint pass.
	var allAssigns []*ast.AssignStmt
	var allDecls []*ast.DeclStmt
	var allBareCalls []*ast.ExprStmt
	var sutWasStatementCall bool
	var sutCallExists bool
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			allAssigns = append(allAssigns, x)
		case *ast.DeclStmt:
			allDecls = append(allDecls, x)
		case *ast.ExprStmt:
			allBareCalls = append(allBareCalls, x)
		case *ast.CallExpr:
			if callTargetsSUT(x, sut) {
				sutCallExists = true
			}
		}
		return true
	})

	// Step 1a: assignment-form taint sources.
	for _, as := range allAssigns {
		for _, rhs := range as.Rhs {
			if exprContainsSUTCall(rhs, sut) {
				for _, l := range as.Lhs {
					if id, ok := l.(*ast.Ident); ok && id.Name != "_" {
						tainted[id.Name] = true
					}
				}
			}
		}
		collectPointerArgTaints(as.Rhs, sut, tainted)
	}

	// Step 1b: decl-form (`var x = SUT(...)`).
	for _, ds := range allDecls {
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
				if exprContainsSUTCall(val, sut) {
					for _, name := range vs.Names {
						if name.Name != "_" {
							tainted[name.Name] = true
						}
					}
				}
			}
		}
	}

	// Step 1c: bare-statement SUT call.
	for _, es := range allBareCalls {
		if !isSUTCallExpr(es.X, sut) {
			continue
		}
		sutWasStatementCall = true
		call := es.X.(*ast.CallExpr)
		collectPointerArgTaints([]ast.Expr{call}, sut, tainted)
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == sut {
			if id, ok := sel.X.(*ast.Ident); ok {
				tainted[id.Name] = true
			}
		}
	}

	// Step 2: propagate taint to fixed point through assignments.
	for changed := true; changed; {
		changed = false
		for _, as := range allAssigns {
			for _, rhs := range as.Rhs {
				if exprReferencesTaint(rhs, tainted, sut) {
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

	// `stmts` retained for the global side-effect heuristic.
	stmts := fn.Body.List


	// Step 3: side-effect-on-global heuristic.
	// If SUT was called as a statement (no return captured), AND an
	// identifier is referenced both BEFORE and AFTER the SUT call,
	// treat that identifier as tainted (likely observing global mutation).
	if sutWasStatementCall {
		applyGlobalSideEffectHeuristic(stmts, sut, tainted)
	}

	// Step 4: walk assertions and decide.
	var assertions []assertion
	collectAssertions(fn.Body, &assertions)

	if len(assertions) == 0 {
		return true, "zero assertions"
	}

	for _, a := range assertions {
		for _, e := range a.exprs {
			if exprReferencesTaint(e, tainted, sut) {
				return false, fmt.Sprintf("assertion at %s references SUT-derived value (%s)",
					formatPos(a.pos), a.category)
			}
		}
	}

	if !sutCallExists {
		return true, "no SUT call in test body"
	}
	return true, "no assertion references SUT return / observable side effect"
}


func exprContainsSUTCall(e ast.Expr, sut string) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && callTargetsSUT(call, sut) {
			found = true
			return false
		}
		return true
	})
	return found
}

func isSUTCallExpr(e ast.Expr, sut string) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	return callTargetsSUT(call, sut)
}

func callTargetsSUT(call *ast.CallExpr, sut string) bool {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name == sut
	case *ast.SelectorExpr:
		return fn.Sel.Name == sut
	}
	return false
}

func collectPointerArgTaints(exprs []ast.Expr, sut string, tainted map[string]bool) {
	for _, e := range exprs {
		call, ok := e.(*ast.CallExpr)
		if !ok || !callTargetsSUT(call, sut) {
			// Look one level deeper — RHS may be `foo(SUT(&x))`.
			ast.Inspect(e, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok && callTargetsSUT(c, sut) {
					addPointerArgs(c, tainted)
				}
				return true
			})
			continue
		}
		addPointerArgs(call, tainted)
	}
}

func addPointerArgs(call *ast.CallExpr, tainted map[string]bool) {
	for _, arg := range call.Args {
		if u, ok := arg.(*ast.UnaryExpr); ok && u.Op == token.AND {
			if id, ok := u.X.(*ast.Ident); ok {
				tainted[id.Name] = true
			}
			if sel, ok := u.X.(*ast.SelectorExpr); ok {
				// &x.f → taint x.
				if id, ok := sel.X.(*ast.Ident); ok {
					tainted[id.Name] = true
				}
			}
		}
	}
}

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

// applyGlobalSideEffectHeuristic taints any identifier that appears both
// before and after a bare-statement SUT call.
func applyGlobalSideEffectHeuristic(stmts []ast.Stmt, sut string, tainted map[string]bool) {
	sutIdx := -1
	for i, s := range stmts {
		if es, ok := s.(*ast.ExprStmt); ok && isSUTCallExpr(es.X, sut) {
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

var builtinIdents = map[string]bool{
	"t": true, "true": true, "false": true, "nil": true,
	"len": true, "cap": true, "append": true, "make": true, "new": true,
	"int": true, "string": true, "bool": true, "byte": true, "error": true,
	"assert": true, "require": true,
	"_": true,
}

func isBuiltinOrPunct(name string) bool {
	return builtinIdents[name]
}

// exprReferencesTaint walks e and returns true if it reads any tainted
// identifier or contains an inline SUT call.
func exprReferencesTaint(e ast.Expr, tainted map[string]bool, sut string) bool {
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
			if callTargetsSUT(x, sut) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// collectAssertions scans body for assertion sites and appends them.
func collectAssertions(root ast.Node, out *[]assertion) {
	ast.Inspect(root, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if cat, exprs, ok := classifyAssertionCall(x); ok {
				*out = append(*out, assertion{x.Pos(), exprs, cat})
			}
		case *ast.IfStmt:
			// `if cond { ... t.Errorf/Fatal/... }` — treat cond as the
			// assertion. (Many idiomatic Go tests use this form.)
			if ifBodyHasFailureCall(x.Body) {
				*out = append(*out, assertion{x.Pos(), []ast.Expr{x.Cond}, "if-cond-with-fail"})
			}
		}
		return true
	})
}

// classifyAssertionCall returns the (category, exprs, true) if call is an
// assertion. Args inspected exclude the `t` testing argument.
func classifyAssertionCall(call *ast.CallExpr) (string, []ast.Expr, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", nil, false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", nil, false
	}
	switch id.Name {
	case "assert", "require":
		// First arg is t — drop it.
		args := call.Args
		if len(args) > 0 {
			args = args[1:]
		}
		return id.Name + "." + sel.Sel.Name, args, true
	case "t":
		switch sel.Sel.Name {
		case "Error", "Errorf", "Fatal", "Fatalf", "Fail", "FailNow":
			return "t." + sel.Sel.Name, call.Args, true
		}
	}
	return "", nil, false
}

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

func formatPos(p token.Pos) string { return fmt.Sprintf("%v", p) }

func safeDiv(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

func humanResult(flagged, isTaut bool) string {
	switch {
	case flagged && isTaut:
		return "TP"
	case !flagged && !isTaut:
		return "TN"
	case flagged && !isTaut:
		return "FP"
	default:
		return "FN"
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

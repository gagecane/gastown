package beads

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestNoAdHocBdSubprocessesInHardenedPackages(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	packages := []string{
		"internal/deacon",
		"internal/plugin",
		"internal/refinery",
		"internal/witness",
	}

	var violations []string
	for _, pkg := range packages {
		dir := filepath.Join(repoRoot, pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			violations = append(violations, adHocBDSubprocesses(t, repoRoot, path)...)
		}
	}

	if len(violations) > 0 {
		t.Fatalf("do not spawn bd directly in hardened packages; use internal/beads.Command so env targeting, read-only mode, and side-effect suppression stay centralized:\n%s", strings.Join(violations, "\n"))
	}
}

// TestBdSQLSubprocessesPinEnv guards against the shared-Dolt pollution class from
// gs-7v3 / gs-qbd: a raw `bd sql` subprocess that does not set cmd.Env inherits the
// caller's BEADS_DOLT_* selectors with no BEADS_DIR, so bd connects to the shared
// server's default "beads" database and pollutes it. `bd sql` connects straight to
// the server (unlike other bd subcommands it does not resolve the database from
// cmd.Dir/.beads), so every raw `exec.Command("bd", "sql", ...)` must pin cmd.Env
// (e.g. via beads.BuildPinnedBDEnv). Builder-based call sites (cmd.BdCmd) centralize
// env and are not raw exec.Command calls, so they are correctly exempt.
func TestBdSQLSubprocessesPinEnv(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	var violations []string
	err := filepath.WalkDir(filepath.Join(repoRoot, "internal"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		violations = append(violations, bdSQLWithoutEnv(t, repoRoot, path)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}

	if len(violations) > 0 {
		t.Fatalf("raw `bd sql` subprocesses must pin cmd.Env (e.g. beads.BuildPinnedBDEnv + DatabaseEnv) so they cannot leak the default \"beads\" database onto the shared Dolt server (gs-7v3 / gs-qbd):\n%s", strings.Join(violations, "\n"))
	}
}

// bdSQLWithoutEnv returns "file:line" for every top-level function in path that
// runs a raw `exec.Command("bd", "sql", ...)` without assigning a *.Env field.
func bdSQLWithoutEnv(t *testing.T, repoRoot, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var out []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		var sqlCalls []token.Pos
		envAssigned := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && isBDSQLCall(call) {
				sqlCalls = append(sqlCalls, call.Pos())
			}
			if assign, ok := n.(*ast.AssignStmt); ok && assignsEnvField(assign) {
				envAssigned = true
			}
			return true
		})
		if envAssigned {
			continue
		}
		for _, pos := range sqlCalls {
			p := fset.Position(pos)
			rel, err := filepath.Rel(repoRoot, p.Filename)
			if err != nil {
				rel = p.Filename
			}
			out = append(out, rel+":"+strconv.Itoa(p.Line))
		}
	}
	return out
}

// isBDSQLCall reports whether call is exec.Command("bd", "sql", ...) (or the
// CommandContext form).
func isBDSQLCall(call *ast.CallExpr) bool {
	if !isExecCommandCall(call) {
		return false
	}
	argIndex := 0
	if selectorName(call) == "CommandContext" {
		argIndex = 1
	}
	if len(call.Args) <= argIndex+1 {
		return false
	}
	if !isBDCommandArg(call.Args[argIndex]) {
		return false
	}
	lit, ok := call.Args[argIndex+1].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	val, err := strconv.Unquote(lit.Value)
	return err == nil && val == "sql"
}

// assignsEnvField reports whether assign sets a field named "Env" (e.g. cmd.Env = ...).
func assignsEnvField(assign *ast.AssignStmt) bool {
	for _, lhs := range assign.Lhs {
		if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Env" {
			return true
		}
	}
	return false
}

func adHocBDSubprocesses(t *testing.T, repoRoot, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isExecCommandCall(call) {
			return true
		}
		argIndex := 0
		if selectorName(call) == "CommandContext" {
			argIndex = 1
		}
		if len(call.Args) <= argIndex || !isBDCommandArg(call.Args[argIndex]) {
			return true
		}
		pos := fset.Position(call.Pos())
		rel, err := filepath.Rel(repoRoot, pos.Filename)
		if err != nil {
			rel = pos.Filename
		}
		out = append(out, rel+":"+strconv.Itoa(pos.Line))
		return true
	})
	return out
}

func isExecCommandCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext") {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "exec"
}

func selectorName(call *ast.CallExpr) string {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return ""
}

func isBDCommandArg(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return false
		}
		value, err := strconv.Unquote(v.Value)
		return err == nil && value == "bd"
	case *ast.Ident:
		return strings.EqualFold(v.Name, "bdPath")
	case *ast.SelectorExpr:
		return strings.EqualFold(v.Sel.Name, "bdPath")
	default:
		return false
	}
}

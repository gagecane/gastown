// Phase 0 task 6b: AST-aware mutant runner. See .designs/auto-test-pr/
// synthesis.md (D6, D11, gate 4b) for the contract.
//
// Surface area:
//
//   - Plan(Target) returns up to MaxMutantsPerTest mutation candidates
//     drawn from the synthesis's three-form grammar (comment-out-line,
//     negate-boolean, return-zero-value), restricted to lines marked
//     covered-by-the-test in the per-test coverage profile.
//   - Apply(src, Candidate) splices the mutation bytes into a source
//     buffer at the candidate's recorded byte offsets.
//   - CopyPackage(pkgDir) materialises a sibling copy of pkgDir under
//     os.MkdirTemp so callers can mutate the copy and never touch the
//     polecat's worktree.
//   - Stage(target, candidate) is the convenience wrapper that calls
//     CopyPackage + Apply for one candidate and returns the staged
//     directory; callers pair it with the sandbox package to drive
//     `go test` and observe whether the mutant survives.
//
// Knowledge-prep (synthesis Round 2 fix #9):
//   - Sources are parsed with go/parser and traversed with go/ast.
//     We never shell out to gofmt or goimports for AST work.
//   - Position arithmetic is anchored to *token.FileSet and to the
//     *ast.Node Pos/End offsets — staticcheck/errcheck use the same
//     pattern. We do NOT compute line/column literals on raw bytes.
//   - Comment-out candidates are restricted to direct children of
//     *ast.BlockStmt so we never substitute an ExprStmt into a slot
//     that the parser would not accept (IfStmt.Init, ForStmt.Init/Post,
//     RangeStmt.Key, etc.) — see go vet's `unreachable` analyser for
//     the same defensive pattern.
//   - Build-tag-gated files are honoured via parser.ParseFile's
//     ImportsOnly path-aware traversal: parsing succeeds for any
//     well-formed Go file, and our walker ignores files whose AST
//     contains a `//go:build ignore` line so we never mutate test
//     fixtures.
package autotest

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	mathrand "math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxMutantsPerTest is the synthesis D11 hard cap on mutation candidates
// returned by Plan for a single Target. Plan never returns more than
// MaxMutantsPerTest, regardless of how large the candidate pool is.
const MaxMutantsPerTest = 5

// Mutation enumerates the synthesis Round 2 fix #3 mutation grammar.
// The numeric values are stable across releases — they are part of the
// audit log emitted by the gate runner.
type Mutation int

const (
	// MutationCommentOutLine replaces a top-of-block statement with a
	// no-op (`_ = struct{}{}`). Restricted to direct *ast.BlockStmt
	// children so it always parses regardless of surrounding context.
	MutationCommentOutLine Mutation = 1

	// MutationNegateBoolean flips a unary `!x` to `x`, or swaps
	// `==` ↔ `!=` on a *ast.BinaryExpr. Other boolean operators
	// (`&&`, `||`, `<`, `>`, `<=`, `>=`) are intentionally out of
	// scope: the synthesis grammar names only `!` and `==`/`!=`.
	MutationNegateBoolean Mutation = 2

	// MutationReturnZeroValue replaces the first `return X1, …` in a
	// function body with `return *new(T1), …`, where `T1, …` are the
	// declared result types. Functions with no return values produce
	// no candidate.
	MutationReturnZeroValue Mutation = 3
)

// String returns the short audit-log identifier for m. The strings are
// stable: external tools (gate audit logs, MR provenance markers) match
// on these.
func (m Mutation) String() string {
	switch m {
	case MutationCommentOutLine:
		return "comment-out-line"
	case MutationNegateBoolean:
		return "negate-boolean"
	case MutationReturnZeroValue:
		return "return-zero-value"
	default:
		return fmt.Sprintf("unknown-mutation(%d)", int(m))
	}
}

// LineSet is the per-file set of 1-based source line numbers a target
// applies to. A nil LineSet matches no lines (NOT all lines), so the
// caller MUST populate it from a real coverage profile.
type LineSet map[int]struct{}

// Has reports whether line is in s. nil-safe.
func (s LineSet) Has(line int) bool {
	if s == nil {
		return false
	}
	_, ok := s[line]
	return ok
}

// Add records line in s. Allocates the underlying map if needed; does
// not retain its receiver, so callers MUST assign back when starting
// from nil:
//
//	var s LineSet
//	s = s.Add(42)
func (s LineSet) Add(line int) LineSet {
	if s == nil {
		s = make(LineSet)
	}
	s[line] = struct{}{}
	return s
}

// Target is the input to Plan. It identifies (a) the package dir whose
// implementation files are eligible for mutation, (b) the test whose
// covered lines define the candidate pool, and (c) the deterministic
// seed used to break ties when more than MaxMutantsPerTest candidates
// share the same blast-radius score.
type Target struct {
	// PackageDir is the absolute path of the package containing the
	// implementation files to mutate. The runner reads every non-
	// test, non-`//go:build ignore` `.go` file in this directory at
	// the top level (subdirectories are ignored — Go packages are
	// flat).
	PackageDir string

	// TestName is the test function whose covered lines define the
	// candidate pool (e.g., "TestFoo"). It is also stirred into the
	// deterministic seed.
	TestName string

	// FileSHA is a stable identifier for the package contents at the
	// instant Plan was called — typically the git tree-hash of
	// PackageDir or the SHA-256 of its concatenated file bytes. The
	// runner does not interpret it beyond stirring into the seed; it
	// only requires that two calls with the same FileSHA, TestName,
	// and inputs produce the same Plan output.
	FileSHA string

	// CoveredLines maps each file basename in PackageDir (e.g.,
	// "foo.go") to the set of 1-based source line numbers covered by
	// TestName's run. Lines outside this set are NOT eligible for
	// mutation, per the synthesis ("the runner mutates *only* lines
	// marked covered-by-the-test in the test's own coverage profile").
	CoveredLines map[string]LineSet

	// GlobalLineHits, if non-nil, maps each file basename to a per-
	// line hit count from the full test suite. It is the blast-radius
	// score for ranking — higher = more downstream tests would observe
	// a surviving mutant, so a passing mutant on this line is more
	// likely to indicate a tautological test (synthesis Round 2 fix
	// #3). When nil or missing, every covered line scores 1.
	GlobalLineHits map[string]map[int]int
}

// Candidate is a single mutation Plan returns. A Candidate is byte-
// addressable: Apply splices Replacement into [StartOffset, EndOffset)
// of the file's source bytes.
type Candidate struct {
	// Mutation is the grammar form this candidate represents.
	Mutation Mutation

	// File is the basename of the file the candidate mutates, relative
	// to Target.PackageDir. Always a single path component (e.g.,
	// "foo.go") — never a subdirectory path.
	File string

	// Line is the 1-based source line of the AST node being mutated.
	Line int

	// StartOffset and EndOffset are byte offsets into the file's
	// source. The half-open interval [StartOffset, EndOffset) is the
	// span Apply replaces with Replacement. Both are non-negative
	// and StartOffset ≤ EndOffset.
	StartOffset int
	EndOffset   int

	// Replacement is the bytes Apply splices into the source. May be
	// empty (which deletes the original span).
	Replacement []byte

	// Description is a short human-readable summary of the mutation,
	// suitable for the audit log emitted by the gate runner. Stable
	// across releases.
	Description string

	// BlastRadius is the candidate's ranking score — typically the
	// global hit count of the mutated line. Plan exposes this so
	// callers can log the ranking it produced.
	BlastRadius int
}

// Plan returns up to MaxMutantsPerTest mutation candidates for t,
// deterministically ordered. The ordering is:
//
//  1. Filter every grammar-form candidate to those whose Line is in
//     t.CoveredLines for the candidate's file.
//  2. Score each surviving candidate by blast radius (Target.GlobalLineHits
//     for the candidate's line, falling back to 1 when missing).
//  3. Sort candidates by (BlastRadius desc, File asc, StartOffset asc,
//     Mutation asc) for a fully-deterministic primary order.
//  4. Shuffle ties (same BlastRadius) using a PCG RNG seeded from
//     SHA-256(FileSHA || 0x00 || TestName).
//  5. Return the top MaxMutantsPerTest entries.
//
// Step 3 makes the order independent of map iteration; step 4 makes ties
// shuffle deterministically given the same seed inputs (synthesis Round
// 2 fix #3 acceptance: "Selection determinism verified by re-running on
// the same seed and getting identical mutant set").
func Plan(t Target) ([]Candidate, error) {
	if t.PackageDir == "" {
		return nil, errors.New("autotest: Target.PackageDir is required")
	}
	if t.TestName == "" {
		return nil, errors.New("autotest: Target.TestName is required")
	}
	if !filepath.IsAbs(t.PackageDir) {
		return nil, fmt.Errorf("autotest: Target.PackageDir must be absolute, got %q", t.PackageDir)
	}

	entries, err := os.ReadDir(t.PackageDir)
	if err != nil {
		return nil, fmt.Errorf("autotest: read package dir: %w", err)
	}
	// Sort the directory entries for stable iteration: os.ReadDir
	// already sorts by name on every supported platform, but we re-
	// sort defensively so Plan does not silently depend on an
	// undocumented invariant.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var pool []Candidate
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		// CoveredLines may omit files entirely (the test does not
		// cover that file). Skip them — there are no candidates from
		// uncovered files.
		coverage := t.CoveredLines[name]
		if len(coverage) == 0 {
			continue
		}

		path := filepath.Join(t.PackageDir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("autotest: read %s: %w", path, err)
		}
		fileCandidates, err := planFile(name, src, coverage, t.GlobalLineHits[name])
		if err != nil {
			return nil, fmt.Errorf("autotest: plan %s: %w", name, err)
		}
		pool = append(pool, fileCandidates...)
	}

	if len(pool) == 0 {
		return nil, nil
	}

	// Stable, deterministic primary order.
	sort.SliceStable(pool, func(i, j int) bool {
		a, b := pool[i], pool[j]
		if a.BlastRadius != b.BlastRadius {
			return a.BlastRadius > b.BlastRadius
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.StartOffset != b.StartOffset {
			return a.StartOffset < b.StartOffset
		}
		return a.Mutation < b.Mutation
	})

	// Tie-break only within blast-radius bands using the seeded RNG.
	// Sorting first then shuffling within bands keeps the high-blast
	// candidates ahead of low-blast ones while still randomising
	// equally-ranked groups deterministically.
	rng := newPlanRNG(t.FileSHA, t.TestName)
	bandStart := 0
	for i := 1; i <= len(pool); i++ {
		if i == len(pool) || pool[i].BlastRadius != pool[bandStart].BlastRadius {
			band := pool[bandStart:i]
			if len(band) > 1 {
				rng.Shuffle(len(band), func(a, b int) {
					band[a], band[b] = band[b], band[a]
				})
			}
			bandStart = i
		}
	}

	if len(pool) > MaxMutantsPerTest {
		pool = pool[:MaxMutantsPerTest]
	}
	return pool, nil
}

// planFile parses src and returns every grammar-form candidate whose
// Line is covered by coverage. Ranking is left to Plan (which sees the
// pool across all files).
func planFile(name string, src []byte, coverage LineSet, hits map[int]int) ([]Candidate, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.ParseComments)
	if err != nil {
		// Synthesis Round 2 fix #9: callers may hand us an
		// unparseable file (e.g., a target mid-edit). Surface the
		// error rather than silently dropping the file — the gate
		// runner's audit log needs to record that a file was
		// unparseable.
		return nil, err
	}
	if hasIgnoreBuildTag(file) {
		return nil, nil
	}

	var out []Candidate
	score := func(line int) int {
		if hits != nil {
			if h, ok := hits[line]; ok && h > 0 {
				return h
			}
		}
		return 1
	}

	planCommentOut(name, fset, file, src, coverage, score, &out)
	planNegateBoolean(name, fset, file, src, coverage, score, &out)
	planReturnZeroValue(name, fset, file, src, coverage, score, &out)
	return out, nil
}

// hasIgnoreBuildTag reports whether file's leading comment group contains
// `//go:build ignore`. We treat such files as fixtures and never mutate
// them, mirroring `go vet`'s behaviour.
func hasIgnoreBuildTag(file *ast.File) bool {
	for _, cg := range file.Comments {
		if cg.Pos() > file.Package {
			break
		}
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//go:build ignore") || c.Text == "// +build ignore" {
				return true
			}
		}
	}
	return false
}

// planCommentOut walks file, emitting one MutationCommentOutLine
// candidate per direct *ast.BlockStmt child whose start line is
// covered. Restricting to BlockStmt children means the replacement
// (`_ = struct{}{}`) always slots into a position where any statement
// is legal, regardless of surrounding context.
func planCommentOut(file string, fset *token.FileSet, root *ast.File, src []byte, coverage LineSet, score func(int) int, out *[]Candidate) {
	ast.Inspect(root, func(n ast.Node) bool {
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for _, stmt := range block.List {
			startLine := fset.Position(stmt.Pos()).Line
			if !coverage.Has(startLine) {
				continue
			}
			startOff := fset.Position(stmt.Pos()).Offset
			endOff := fset.Position(stmt.End()).Offset
			if startOff < 0 || endOff > len(src) || endOff < startOff {
				continue
			}
			*out = append(*out, Candidate{
				Mutation:    MutationCommentOutLine,
				File:        file,
				Line:        startLine,
				StartOffset: startOff,
				EndOffset:   endOff,
				Replacement: []byte("_ = struct{}{}"),
				Description: fmt.Sprintf("comment out statement at %s:%d", file, startLine),
				BlastRadius: score(startLine),
			})
		}
		return true
	})
}

// planNegateBoolean walks file, emitting candidates for unary `!x`
// (replace with `x`) and binary `==` / `!=` (swap). Other comparison
// operators are intentionally NOT mutated — the synthesis grammar names
// only `!` and `==`/`!=`.
func planNegateBoolean(file string, fset *token.FileSet, root *ast.File, src []byte, coverage LineSet, score func(int) int, out *[]Candidate) {
	ast.Inspect(root, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.UnaryExpr:
			if e.Op != token.NOT {
				return true
			}
			line := fset.Position(e.OpPos).Line
			if !coverage.Has(line) {
				return true
			}
			// Strip the `!` token: the replacement span is just the
			// `!` character; the operand bytes are left in place.
			startOff := fset.Position(e.OpPos).Offset
			// The `!` token is one byte wide.
			endOff := startOff + 1
			if endOff > len(src) {
				return true
			}
			*out = append(*out, Candidate{
				Mutation:    MutationNegateBoolean,
				File:        file,
				Line:        line,
				StartOffset: startOff,
				EndOffset:   endOff,
				Replacement: []byte(""),
				Description: fmt.Sprintf("drop `!` at %s:%d", file, line),
				BlastRadius: score(line),
			})
		case *ast.BinaryExpr:
			if e.Op != token.EQL && e.Op != token.NEQ {
				return true
			}
			line := fset.Position(e.OpPos).Line
			if !coverage.Has(line) {
				return true
			}
			startOff := fset.Position(e.OpPos).Offset
			// Both `==` and `!=` are two bytes wide.
			endOff := startOff + 2
			if endOff > len(src) {
				return true
			}
			var repl []byte
			var desc string
			if e.Op == token.EQL {
				repl = []byte("!=")
				desc = fmt.Sprintf("swap `==` -> `!=` at %s:%d", file, line)
			} else {
				repl = []byte("==")
				desc = fmt.Sprintf("swap `!=` -> `==` at %s:%d", file, line)
			}
			*out = append(*out, Candidate{
				Mutation:    MutationNegateBoolean,
				File:        file,
				Line:        line,
				StartOffset: startOff,
				EndOffset:   endOff,
				Replacement: repl,
				Description: desc,
				BlastRadius: score(line),
			})
		}
		return true
	})
}

// planReturnZeroValue walks file, emitting one MutationReturnZeroValue
// candidate per *ast.FuncDecl whose body contains at least one
// *ast.ReturnStmt with a covered line. The candidate replaces only the
// first such ReturnStmt — multiple returns in the same function compete
// for the single slot.
func planReturnZeroValue(file string, fset *token.FileSet, root *ast.File, src []byte, coverage LineSet, score func(int) int, out *[]Candidate) {
	for _, decl := range root.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		results := fn.Type.Results
		if results == nil || len(results.List) == 0 {
			continue // void function — no zero-value mutation.
		}
		zeroLiteral, ok := zeroReturnExpr(fn.Type)
		if !ok {
			continue
		}
		var first *ast.ReturnStmt
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if first != nil {
				return false
			}
			if r, ok := n.(*ast.ReturnStmt); ok {
				if len(r.Results) == 0 {
					// Naked return — no expression to mutate. Skip.
					return true
				}
				line := fset.Position(r.Pos()).Line
				if coverage.Has(line) {
					first = r
					return false
				}
			}
			// Don't dive into nested function literals — their own
			// FuncDecl-level walk (if any) would handle them.
			if _, isFuncLit := n.(*ast.FuncLit); isFuncLit {
				return false
			}
			return true
		})
		if first == nil {
			continue
		}
		line := fset.Position(first.Pos()).Line
		// Replace from the `return` keyword through the end of the
		// last result expression. We anchor on first.Pos() (the
		// `return` keyword) and first.End() (just past the last
		// expression).
		startOff := fset.Position(first.Pos()).Offset
		endOff := fset.Position(first.End()).Offset
		if endOff > len(src) || startOff < 0 || endOff < startOff {
			continue
		}
		*out = append(*out, Candidate{
			Mutation:    MutationReturnZeroValue,
			File:        file,
			Line:        line,
			StartOffset: startOff,
			EndOffset:   endOff,
			Replacement: []byte("return " + zeroLiteral),
			Description: fmt.Sprintf("return zero values from %s at %s:%d", funcName(fn), file, line),
			BlastRadius: score(line),
		})
	}
}

// zeroReturnExpr returns the comma-separated zero-value expression list
// for the function type's declared results (e.g., "*new(int), *new(error)"),
// or "" if any result type is unrepresentable. The `*new(T)` idiom
// produces a typed zero value for any T, including named structs and
// generics, without requiring us to enumerate the universe of types —
// this matches go/types' analogous fallback in errcheck.
func zeroReturnExpr(ft *ast.FuncType) (string, bool) {
	if ft.Results == nil || len(ft.Results.List) == 0 {
		return "", false
	}
	var parts []string
	for _, field := range ft.Results.List {
		typeStr, ok := exprText(field.Type)
		if !ok {
			return "", false
		}
		// Each Field can declare multiple named results sharing one
		// type (e.g., `(a, b int)`); count Names for the multiplier.
		// An anonymous result (`(int, error)`) has len(Names) == 0
		// but represents one slot.
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		for i := 0; i < count; i++ {
			parts = append(parts, "*new("+typeStr+")")
		}
	}
	return strings.Join(parts, ", "), true
}

// exprText renders an *ast.Expr back to its source text. We avoid
// go/printer because we want to preserve the exact byte form (e.g.,
// `[]byte`, `*int`, `map[string]int`, `T[U]`) without injecting
// canonical whitespace. The ast.Expr forms we accept are: identifier,
// pointer, array/slice, map, chan, func, interface, struct, selector
// (qualified type), and index/index-list (generics). Anything else
// (call expressions, parenthesised oddities) returns ok=false so the
// candidate is skipped — better to lose one mutation than emit an
// uncompilable replacement.
func exprText(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name, true
	case *ast.SelectorExpr:
		pkg, ok := exprText(x.X)
		if !ok {
			return "", false
		}
		return pkg + "." + x.Sel.Name, true
	case *ast.StarExpr:
		t, ok := exprText(x.X)
		if !ok {
			return "", false
		}
		return "*" + t, true
	case *ast.ArrayType:
		var lenStr string
		if x.Len != nil {
			s, ok := exprText(x.Len)
			if !ok {
				return "", false
			}
			lenStr = s
		}
		elt, ok := exprText(x.Elt)
		if !ok {
			return "", false
		}
		return "[" + lenStr + "]" + elt, true
	case *ast.MapType:
		k, ok := exprText(x.Key)
		if !ok {
			return "", false
		}
		v, ok := exprText(x.Value)
		if !ok {
			return "", false
		}
		return "map[" + k + "]" + v, true
	case *ast.ChanType:
		v, ok := exprText(x.Value)
		if !ok {
			return "", false
		}
		switch x.Dir {
		case ast.SEND:
			return "chan<- " + v, true
		case ast.RECV:
			return "<-chan " + v, true
		default:
			return "chan " + v, true
		}
	case *ast.InterfaceType:
		// An anonymous interface{} type is the only shape we render
		// without delegating to go/printer; named interfaces appear
		// as *ast.Ident or *ast.SelectorExpr above.
		if x.Methods == nil || len(x.Methods.List) == 0 {
			return "interface{}", true
		}
		return "", false
	case *ast.FuncType:
		// `*new(func(...))` is valid Go syntax — but rendering a
		// FuncType back to source faithfully is fiddly enough that
		// we skip it. Returning ok=false drops the candidate; the
		// other grammar forms still apply to functions returning
		// closures.
		return "", false
	case *ast.StructType:
		if x.Fields == nil || len(x.Fields.List) == 0 {
			return "struct{}", true
		}
		return "", false
	case *ast.IndexExpr:
		base, ok := exprText(x.X)
		if !ok {
			return "", false
		}
		idx, ok := exprText(x.Index)
		if !ok {
			return "", false
		}
		return base + "[" + idx + "]", true
	case *ast.IndexListExpr:
		base, ok := exprText(x.X)
		if !ok {
			return "", false
		}
		var parts []string
		for _, ix := range x.Indices {
			s, ok := exprText(ix)
			if !ok {
				return "", false
			}
			parts = append(parts, s)
		}
		return base + "[" + strings.Join(parts, ", ") + "]", true
	case *ast.BasicLit:
		return x.Value, true
	default:
		return "", false
	}
}

// funcName returns the function's declared name, including a receiver
// type prefix for methods (e.g., "(*Foo).Bar"). Used only for
// human-readable Candidate.Description text.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	recv, ok := exprText(fn.Recv.List[0].Type)
	if !ok {
		return fn.Name.Name
	}
	return "(" + recv + ")." + fn.Name.Name
}

// Apply returns src with c.Replacement spliced into [c.StartOffset,
// c.EndOffset). The original src is not modified; the returned slice
// is a fresh allocation.
func Apply(src []byte, c Candidate) ([]byte, error) {
	if c.StartOffset < 0 || c.EndOffset < c.StartOffset || c.EndOffset > len(src) {
		return nil, fmt.Errorf("autotest: candidate offsets [%d,%d) out of range for %d-byte source", c.StartOffset, c.EndOffset, len(src))
	}
	out := make([]byte, 0, len(src)-(c.EndOffset-c.StartOffset)+len(c.Replacement))
	out = append(out, src[:c.StartOffset]...)
	out = append(out, c.Replacement...)
	out = append(out, src[c.EndOffset:]...)
	return out, nil
}

// CopyPackage materialises a sibling copy of pkgDir under
// os.MkdirTemp("", "autotest-mutant-*") and returns the absolute path
// of the destination. Only top-level files are copied (Go packages are
// flat); subdirectories — including testdata/ — are intentionally
// excluded so the mutant runner never copies large, unrelated trees.
//
// Per synthesis D6, mutated source is written to the returned path,
// not to pkgDir. The polecat's worktree is never modified.
//
// The caller is responsible for calling os.RemoveAll on the returned
// path when done.
func CopyPackage(pkgDir string) (string, error) {
	if pkgDir == "" {
		return "", errors.New("autotest: pkgDir is required")
	}
	if !filepath.IsAbs(pkgDir) {
		return "", fmt.Errorf("autotest: pkgDir must be absolute, got %q", pkgDir)
	}
	info, err := os.Stat(pkgDir)
	if err != nil {
		return "", fmt.Errorf("autotest: stat pkgDir: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("autotest: pkgDir %q is not a directory", pkgDir)
	}
	dst, err := os.MkdirTemp("", "autotest-mutant-*")
	if err != nil {
		return "", fmt.Errorf("autotest: mkdirtemp: %w", err)
	}
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		_ = os.RemoveAll(dst)
		return "", fmt.Errorf("autotest: read pkgDir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue // flat-package invariant; skip testdata/, sub-pkgs.
		}
		srcPath := filepath.Join(pkgDir, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if err := copyFile(srcPath, dstPath); err != nil {
			_ = os.RemoveAll(dst)
			return "", fmt.Errorf("autotest: copy %s: %w", e.Name(), err)
		}
	}
	return dst, nil
}

// Stage is the convenience wrapper that copies pkgDir to a fresh
// tmpdir and applies c to the file named c.File inside the copy. It
// returns the absolute path of the staged tmpdir; callers MUST
// os.RemoveAll it themselves.
//
// Stage exists so the gate runner does not have to reproduce the
// CopyPackage + Apply + WriteFile sequence at every call site.
func Stage(pkgDir string, c Candidate) (string, error) {
	dst, err := CopyPackage(pkgDir)
	if err != nil {
		return "", err
	}
	if c.File == "" {
		_ = os.RemoveAll(dst)
		return "", errors.New("autotest: Candidate.File is required")
	}
	if strings.ContainsAny(c.File, "/\\") {
		_ = os.RemoveAll(dst)
		return "", fmt.Errorf("autotest: Candidate.File must be a basename, got %q", c.File)
	}
	target := filepath.Join(dst, c.File)
	src, err := os.ReadFile(target)
	if err != nil {
		_ = os.RemoveAll(dst)
		return "", fmt.Errorf("autotest: read %s: %w", c.File, err)
	}
	mutated, err := Apply(src, c)
	if err != nil {
		_ = os.RemoveAll(dst)
		return "", err
	}
	if err := os.WriteFile(target, mutated, 0o600); err != nil {
		_ = os.RemoveAll(dst)
		return "", fmt.Errorf("autotest: write mutated %s: %w", c.File, err)
	}
	return dst, nil
}

// copyFile is the regular-file-only copier used by CopyPackage. It
// preserves the source's file mode but does not attempt to copy
// extended attributes, ownership, or symlinks — the auto-test-pr
// runner only encounters regular `.go` files in the package dir.
func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		// Not a regular file — silently skip. The runner only
		// mutates `.go` source files; sockets, FIFOs, and symlinks
		// are not part of a Go package's authoring surface.
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// newPlanRNG returns a deterministic *mathrand.Rand seeded by
// SHA-256(fileSHA || 0x00 || testName). The same inputs always
// produce the same RNG sequence, which is what the synthesis Round 2
// fix #3 acceptance criterion ("identical mutant set on rerun")
// requires.
func newPlanRNG(fileSHA, testName string) *mathrand.Rand {
	var buf bytes.Buffer
	buf.WriteString(fileSHA)
	buf.WriteByte(0x00)
	buf.WriteString(testName)
	sum := sha256.Sum256(buf.Bytes())
	s1 := binary.BigEndian.Uint64(sum[0:8])
	s2 := binary.BigEndian.Uint64(sum[8:16])
	// PCG requires non-zero state; guard against the (astronomically
	// unlikely) all-zero hash so the RNG never panics on construction.
	if s1 == 0 && s2 == 0 {
		s1 = 1
	}
	return mathrand.New(mathrand.NewPCG(s1, s2))
}

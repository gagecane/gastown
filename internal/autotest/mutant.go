// Package autotest gate 4b: AST-aware mutant runner.
//
// The mutant runner applies controlled mutations to Go source lines
// that are covered by a given test, then runs the test in a sandboxed
// temp directory to detect whether the test catches each mutation.
//
// Mutation grammar (fixed by synthesis, round 2 fix #3):
//
//	(i)   comment-out-line   — replace statement with blank
//	(ii)  negate-boolean     — flip ! / swap == ↔ !=
//	(iii) return-zero-value  — replace first return with type's zero
//
// Selection is deterministic given (file SHA + test name) and bounded
// to ≤5 mutants (D11).
package autotest

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/autotest/sandbox"
)

// MaxMutantsPerTest is the hard ceiling on mutants per test run (D11).
const MaxMutantsPerTest = 5

// MutationKind identifies the grammar form of a mutation.
type MutationKind int

const (
	// MutCommentOutLine replaces a statement with a blank line.
	MutCommentOutLine MutationKind = iota
	// MutNegateBoolean flips ! or swaps == ↔ !=.
	MutNegateBoolean
	// MutReturnZeroValue replaces a return statement with zero values.
	MutReturnZeroValue
)

// String returns a human-readable name for the mutation kind.
func (k MutationKind) String() string {
	switch k {
	case MutCommentOutLine:
		return "comment-out-line"
	case MutNegateBoolean:
		return "negate-boolean"
	case MutReturnZeroValue:
		return "return-zero-value"
	default:
		return "unknown"
	}
}

// Mutant describes a single mutation candidate.
type Mutant struct {
	// Kind is the mutation grammar form.
	Kind MutationKind
	// File is the relative path within the package directory.
	File string
	// Line is the 1-based line number of the mutated statement.
	Line int
	// Original is the original source text of the line/statement.
	Original string
	// Mutated is the replacement source text.
	Mutated string
	// BlastRadius estimates mutation impact (higher = more likely to
	// be caught by a good test). Used for selection ranking.
	BlastRadius int
}

// MutantResult holds the outcome of running one mutant.
type MutantResult struct {
	Mutant Mutant
	// Killed is true if the test failed (detected the mutation).
	Killed bool
	// Output is the combined stdout+stderr from the test run.
	Output string
	// Err is any error from the sandbox/subprocess infrastructure
	// (distinct from a test failure, which sets Killed=true).
	Err error
}

// RunConfig configures a mutant run.
type RunConfig struct {
	// PkgDir is the absolute path to the package under test.
	PkgDir string
	// TestName is the Go test function name (e.g. "TestFoo").
	TestName string
	// CoveredLines maps relative file paths to sets of 1-based line
	// numbers that the test covers (from a coverage profile).
	CoveredLines map[string]map[int]bool
	// Sandbox is the sandbox instance for running test subprocesses.
	Sandbox *sandbox.Sandbox
	// FileSHA is the SHA-256 of the primary file under test, used
	// together with TestName for deterministic seed generation.
	FileSHA [32]byte
}

// FindMutants discovers all possible mutation candidates in the
// package source, restricted to lines covered by the test. It uses
// go/parser + go/ast directly (no shelling out). The returned slice
// is deterministically ordered.
func FindMutants(cfg *RunConfig) ([]Mutant, error) {
	if cfg == nil {
		return nil, fmt.Errorf("mutant: nil config")
	}
	if cfg.PkgDir == "" {
		return nil, fmt.Errorf("mutant: empty PkgDir")
	}

	var allMutants []Mutant

	for relFile, coveredLines := range cfg.CoveredLines {
		if len(coveredLines) == 0 {
			continue
		}
		absPath := filepath.Join(cfg.PkgDir, relFile)
		src, err := os.ReadFile(absPath)
		if err != nil {
			return nil, fmt.Errorf("mutant: read %s: %w", relFile, err)
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, relFile, src, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("mutant: parse %s: %w", relFile, err)
		}

		lines := strings.Split(string(src), "\n")
		mutants := findMutantsInFile(fset, f, relFile, lines, coveredLines)
		allMutants = append(allMutants, mutants...)
	}

	// Sort for determinism before selection.
	sort.Slice(allMutants, func(i, j int) bool {
		if allMutants[i].File != allMutants[j].File {
			return allMutants[i].File < allMutants[j].File
		}
		if allMutants[i].Line != allMutants[j].Line {
			return allMutants[i].Line < allMutants[j].Line
		}
		return allMutants[i].Kind < allMutants[j].Kind
	})

	return allMutants, nil
}

// SelectMutants picks at most MaxMutantsPerTest mutants from
// candidates, preferring those with the greatest blast radius.
// Selection is deterministic given the same FileSHA + TestName.
func SelectMutants(candidates []Mutant, fileSHA [32]byte, testName string) []Mutant {
	if len(candidates) <= MaxMutantsPerTest {
		return candidates
	}

	// Deterministic seed from file SHA + test name.
	seed := deterministicSeed(fileSHA, testName)
	rng := rand.New(rand.NewSource(seed))

	// Sort by blast radius descending, break ties with index stability.
	type indexed struct {
		idx int
		m   Mutant
	}
	ranked := make([]indexed, len(candidates))
	for i, m := range candidates {
		ranked[i] = indexed{idx: i, m: m}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].m.BlastRadius > ranked[j].m.BlastRadius
	})

	// Take top candidates by blast radius. If there are ties at the
	// boundary, shuffle among tied candidates deterministically.
	threshold := ranked[MaxMutantsPerTest-1].m.BlastRadius
	var above, atThreshold []indexed
	for _, r := range ranked {
		if r.m.BlastRadius > threshold {
			above = append(above, r)
		} else if r.m.BlastRadius == threshold {
			atThreshold = append(atThreshold, r)
		}
	}

	needed := MaxMutantsPerTest - len(above)
	// Shuffle tied candidates deterministically.
	rng.Shuffle(len(atThreshold), func(i, j int) {
		atThreshold[i], atThreshold[j] = atThreshold[j], atThreshold[i]
	})

	selected := make([]Mutant, 0, MaxMutantsPerTest)
	for _, r := range above {
		selected = append(selected, r.m)
	}
	for i := 0; i < needed && i < len(atThreshold); i++ {
		selected = append(selected, atThreshold[i].m)
	}

	return selected
}

// RunMutants executes the test against each mutant in a sandboxed
// temp directory. It copies the package to os.MkdirTemp, applies
// each mutation, and runs through the sandbox wrapper.
func RunMutants(cfg *RunConfig, mutants []Mutant) ([]MutantResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("mutant: nil config")
	}
	if len(mutants) > MaxMutantsPerTest {
		return nil, fmt.Errorf("mutant: %d mutants exceeds cap %d", len(mutants), MaxMutantsPerTest)
	}
	if cfg.Sandbox == nil {
		return nil, fmt.Errorf("mutant: nil sandbox")
	}

	results := make([]MutantResult, 0, len(mutants))
	for _, m := range mutants {
		result := runSingleMutant(cfg, m)
		results = append(results, result)
	}
	return results, nil
}

// runSingleMutant copies the package dir to a temp dir, applies the
// mutation, and runs the test via sandbox.
func runSingleMutant(cfg *RunConfig, m Mutant) MutantResult {
	// Create temp dir for this mutant.
	tmpDir, err := os.MkdirTemp("", "mutant-*")
	if err != nil {
		return MutantResult{Mutant: m, Err: fmt.Errorf("mktmp: %w", err)}
	}
	defer os.RemoveAll(tmpDir)

	// Copy package directory to temp.
	if err := copyDir(cfg.PkgDir, tmpDir); err != nil {
		return MutantResult{Mutant: m, Err: fmt.Errorf("copy: %w", err)}
	}

	// Apply mutation in the temp dir.
	targetFile := filepath.Join(tmpDir, m.File)
	if err := applyMutation(targetFile, m); err != nil {
		return MutantResult{Mutant: m, Err: fmt.Errorf("apply: %w", err)}
	}

	// Create a sandbox rooted in the temp dir.
	sb, err := sandbox.New(tmpDir)
	if err != nil {
		return MutantResult{Mutant: m, Err: fmt.Errorf("sandbox: %w", err)}
	}

	// Run the specific test.
	cmd := exec.Command("go", "test", "-run", "^"+cfg.TestName+"$", "-count=1", "./...")
	if err := sb.Apply(cmd); err != nil {
		return MutantResult{Mutant: m, Err: fmt.Errorf("sandbox apply: %w", err)}
	}

	out, runErr := cmd.CombinedOutput()
	outStr := string(out)

	// A test failure (exit code != 0) means the mutant was killed.
	if runErr != nil {
		if _, ok := runErr.(*exec.ExitError); ok {
			return MutantResult{Mutant: m, Killed: true, Output: outStr}
		}
		return MutantResult{Mutant: m, Err: runErr, Output: outStr}
	}

	// Test passed — mutant survived (not killed).
	return MutantResult{Mutant: m, Killed: false, Output: outStr}
}

// applyMutation rewrites a source file with the specified mutation.
func applyMutation(filePath string, m Mutant) error {
	src, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	lines := strings.Split(string(src), "\n")
	if m.Line < 1 || m.Line > len(lines) {
		return fmt.Errorf("line %d out of range [1, %d]", m.Line, len(lines))
	}

	// Replace the target line with the mutated version.
	lines[m.Line-1] = m.Mutated

	return os.WriteFile(filePath, []byte(strings.Join(lines, "\n")), 0644)
}

// findMutantsInFile discovers mutation candidates in a single parsed
// Go file, restricted to covered lines.
func findMutantsInFile(fset *token.FileSet, f *ast.File, relFile string, lines []string, coveredLines map[int]bool) []Mutant {
	var mutants []Mutant

	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return true
		}

		pos := fset.Position(n.Pos())
		line := pos.Line

		// Only mutate covered lines.
		if !coveredLines[line] {
			return true
		}

		switch node := n.(type) {
		case *ast.ExprStmt:
			// (i) comment-out-line: remove expression statement.
			if line >= 1 && line <= len(lines) {
				mutants = append(mutants, Mutant{
					Kind:        MutCommentOutLine,
					File:        relFile,
					Line:        line,
					Original:    lines[line-1],
					Mutated:     commentOutLine(lines[line-1]),
					BlastRadius: 2,
				})
			}

		case *ast.AssignStmt:
			// (i) comment-out-line: remove assignment.
			if line >= 1 && line <= len(lines) {
				mutants = append(mutants, Mutant{
					Kind:        MutCommentOutLine,
					File:        relFile,
					Line:        line,
					Original:    lines[line-1],
					Mutated:     commentOutLine(lines[line-1]),
					BlastRadius: 3,
				})
			}

		case *ast.BinaryExpr:
			// (ii) negate-boolean: swap == ↔ != and other comparisons.
			if neg := negateBinaryOp(node, fset, lines); neg != nil {
				neg.File = relFile
				mutants = append(mutants, *neg)
			}

		case *ast.UnaryExpr:
			// (ii) negate-boolean: flip ! operator.
			if node.Op == token.NOT {
				if line >= 1 && line <= len(lines) {
					mutants = append(mutants, Mutant{
						Kind:        MutNegateBoolean,
						File:        relFile,
						Line:        line,
						Original:    lines[line-1],
						Mutated:     flipNot(lines[line-1], fset.Position(node.Pos()).Column),
						BlastRadius: 4,
					})
				}
			}

		case *ast.ReturnStmt:
			// (iii) return-zero-value: replace return with zero values.
			if len(node.Results) > 0 {
				if line >= 1 && line <= len(lines) {
					if zeroRet := zeroValueReturn(node, lines[line-1]); zeroRet != "" {
						mutants = append(mutants, Mutant{
							Kind:        MutReturnZeroValue,
							File:        relFile,
							Line:        line,
							Original:    lines[line-1],
							Mutated:     zeroRet,
							BlastRadius: 5,
						})
					}
				}
			}
		}
		return true
	})

	return mutants
}

// commentOutLine replaces content with a blank (preserving indentation
// as a comment marker for readability of diffs).
func commentOutLine(line string) string {
	indent := ""
	for _, ch := range line {
		if ch == ' ' || ch == '\t' {
			indent += string(ch)
		} else {
			break
		}
	}
	return indent + "// mutant: removed"
}

// negateBinaryOp swaps comparison operators for boolean negation.
func negateBinaryOp(expr *ast.BinaryExpr, fset *token.FileSet, lines []string) *Mutant {
	var replacement token.Token
	switch expr.Op {
	case token.EQL:
		replacement = token.NEQ
	case token.NEQ:
		replacement = token.EQL
	case token.LSS:
		replacement = token.GEQ
	case token.GTR:
		replacement = token.LEQ
	case token.LEQ:
		replacement = token.GTR
	case token.GEQ:
		replacement = token.LSS
	default:
		return nil
	}

	pos := fset.Position(expr.OpPos)
	line := pos.Line
	if line < 1 || line > len(lines) {
		return nil
	}

	original := lines[line-1]
	col := pos.Column - 1 // 0-based
	opLen := len(expr.Op.String())

	if col < 0 || col+opLen > len(original) {
		return nil
	}

	mutated := original[:col] + replacement.String() + original[col+opLen:]

	return &Mutant{
		Kind:        MutNegateBoolean,
		Line:        line,
		Original:    original,
		Mutated:     mutated,
		BlastRadius: 4,
	}
}

// flipNot removes the ! operator at the specified column position.
func flipNot(line string, col int) string {
	// col is 1-based from token.Position.
	idx := col - 1
	if idx < 0 || idx >= len(line) {
		return line
	}
	if line[idx] != '!' {
		return line
	}
	// Remove the '!' character.
	return line[:idx] + line[idx+1:]
}

// zeroValueReturn attempts to build a zero-value return statement
// from a return statement's results. It uses AST type inference
// heuristics since we don't have full type information.
func zeroValueReturn(ret *ast.ReturnStmt, originalLine string) string {
	if len(ret.Results) == 0 {
		return ""
	}

	// Determine indentation from the original line.
	indent := ""
	for _, ch := range originalLine {
		if ch == ' ' || ch == '\t' {
			indent += string(ch)
		} else {
			break
		}
	}

	// Build zero-value return arguments by inspecting expression types.
	zeros := make([]string, len(ret.Results))
	for i, expr := range ret.Results {
		zeros[i] = inferZeroValue(expr)
	}

	return indent + "return " + strings.Join(zeros, ", ")
}

// inferZeroValue heuristically determines the zero value for an
// expression based on its AST structure.
func inferZeroValue(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		switch e.Kind {
		case token.INT:
			return "0"
		case token.FLOAT:
			return "0.0"
		case token.STRING:
			return `""`
		case token.CHAR:
			return "0"
		}
	case *ast.Ident:
		switch e.Name {
		case "true", "false":
			return "false"
		case "nil":
			return "nil"
		}
		// Named identifier — likely a variable; return nil as zero
		// for pointer/interface types, "" for strings, 0 for numbers.
		// Without type info, nil is the safest guess for non-primitives.
		return "nil"
	case *ast.BinaryExpr:
		// Arithmetic or comparison expressions: if the operator is
		// arithmetic (+, -, *, /, %), the result is numeric → 0.
		// For comparisons (==, !=, <, etc.), the result is bool → false.
		switch e.Op {
		case token.ADD, token.SUB, token.MUL, token.QUO, token.REM,
			token.AND, token.OR, token.XOR, token.SHL, token.SHR, token.AND_NOT:
			return "0"
		case token.EQL, token.NEQ, token.LSS, token.GTR, token.LEQ, token.GEQ,
			token.LAND, token.LOR:
			return "false"
		}
		return "0"
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			// &something → nil (pointer)
			return "nil"
		}
		return "0"
	case *ast.CallExpr:
		// Function call — zero depends on what it returns.
		// For error-returning calls, nil is common.
		name := callExprName(e)
		if strings.Contains(name, "Error") || strings.Contains(name, "err") {
			return "nil"
		}
		return "nil"
	case *ast.CompositeLit:
		// Struct/slice/map literal → nil.
		return "nil"
	case *ast.SelectorExpr:
		// pkg.Something — likely an error or interface.
		return "nil"
	}
	return "nil"
}

// callExprName extracts a readable name from a call expression.
func callExprName(call *ast.CallExpr) string {
	var buf strings.Builder
	// printer.Fprint to a strings.Builder cannot fail; ignore the error
	// to satisfy errcheck without losing readability.
	_ = printer.Fprint(&buf, token.NewFileSet(), call.Fun)
	return buf.String()
}

// deterministicSeed generates a repeatable int64 seed from the file
// SHA and test name. This ensures the same file+test always selects
// the same mutants.
func deterministicSeed(fileSHA [32]byte, testName string) int64 {
	h := sha256.New()
	h.Write(fileSHA[:])
	h.Write([]byte(testName))
	sum := h.Sum(nil)
	return int64(binary.LittleEndian.Uint64(sum[:8]))
}

// copyDir recursively copies src directory contents to dst.
// dst must already exist.
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := os.MkdirAll(dstPath, 0755); err != nil {
				return err
			}
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(srcPath)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dstPath, data, 0644); err != nil {
				return err
			}
		}
	}
	return nil
}

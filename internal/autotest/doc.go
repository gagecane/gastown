// Package autotest hosts the auto-test-pr quality-gate runners that
// the polecat formula `mol-polecat-work-test-improver` calls between
// implementation and submit (.designs/auto-test-pr/synthesis.md, gates
// 4a–4g).
//
// This package is leaf-level: it depends only on the Go standard
// library and on internal/autotest/sandbox so the gate runners are
// reachable from the polecat molecule, the Mayor cycle, and the
// Phase-0 e2e fixture without import cycles.
//
// # Knowledge-prep (synthesis Round 2 fix #9)
//
// The mutant and tautology runners parse Go sources with go/parser
// and traverse them with go/ast directly — they MUST NOT shell out
// to gofmt or goimports for AST traversal. The conventions absorbed
// from real-world AST tools (go vet, staticcheck, errcheck) drove
// three behaviors here:
//
//   - Token positions are anchored to *token.FileSet rather than
//     line/column literals so the same Candidate value survives
//     reformatted source.
//   - Mutations are applied as byte-level splices keyed off the
//     parsed AST node's Pos/End offsets. We do NOT run go/printer
//     on the whole file; the original formatting (whitespace,
//     comments, build-tag headers) is preserved everywhere except
//     inside the mutation's exact byte range.
//   - Comment-out candidates are restricted to direct children of
//     *ast.BlockStmt so we never substitute an ExprStmt into a
//     SimpleStmt slot (IfStmt.Init, ForStmt.Init/Post, etc.).
//
// # Files
//
//   - mutant.go — Phase 0 task 6b: AST-aware mutant runner.
//
// Future tasks (6c tautology linter, 8 coverage-delta gate, etc.) add
// peer files to this package per the synthesis task list.
package autotest

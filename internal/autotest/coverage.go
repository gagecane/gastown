// Package autotest provides quality-gate primitives for the
// auto-test-pr formula (.designs/auto-test-pr/synthesis.md). The
// gates run in a hardened sandbox (internal/autotest/sandbox) and
// each lives in its own file under this package: coverage delta
// (this file, gate 4a), AST-aware mutant runner (gate 4b), and
// tautology linter (gate 4d).
//
// # Gate 4a — coverage delta
//
// Gate 4a hard-fails the cycle when the polecat's new test does not
// exercise at least one previously-uncovered basic block. The
// synthesis specifies that the parser consumes profiles in the
// format produced by `go test -coverprofile` (the same format
// `golang.org/x/tools/cover` parses), and that the marker-comment
// alone — `// gt:auto-test-pr origin=<bead-id> covers=<file:line>` —
// MUST NOT satisfy the gate. Comments live in the source, never in
// the cover profile, so the parser is structurally incapable of
// being fooled by them: BranchDelta only counts basic blocks whose
// Count went from 0 to >0 between the before and after profiles.
//
// Profile format (one entry per basic block):
//
//	mode: set|count|atomic
//	<file>:<startLine>.<startCol>,<endLine>.<endCol> <numStatements> <count>
//
// Implementation note: this parser is stdlib-only by design. It
// cannot depend on golang.org/x/tools/cover because internal/autotest
// is a leaf package consumed by gate runners and the (future) Mayor
// cycle code; pulling in x/tools would inflate the dependency graph
// of every consumer for a 100-line file format. Round-trip parity
// with x/tools/cover is verified on hand-rolled fixtures by the
// tests in coverage_test.go.
package autotest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ErrMalformedProfile is returned by ParseProfile when the input
// cannot be parsed as a Go cover profile. Callers MUST use
// errors.Is to test for this sentinel; the wrapped error carries
// the offending line number and a human-readable diagnostic.
var ErrMalformedProfile = errors.New("autotest: malformed cover profile")

// Block describes a single basic block emitted by the cover tool.
// Each block is a "branch" for gate-4a accounting purposes: the
// auto-test-pr cycle hard-fails when no block transitions from
// Count == 0 (in the before profile) to Count > 0 (in the after
// profile).
type Block struct {
	// File is the import-path-qualified file name as emitted by
	// the cover tool, e.g. "github.com/example/pkg/foo.go". The
	// parser preserves the toolchain's exact spelling so block
	// matching across before/after profiles is byte-identical.
	File string

	// StartLine, StartCol, EndLine, EndCol identify the block's
	// span in the source file. All four are 1-based and positive.
	StartLine int
	StartCol  int
	EndLine   int
	EndCol    int

	// NumStatements is the count of Go statements inside the
	// block. Always non-negative; the cover tool emits ≥ 1 in
	// practice but the parser tolerates 0 for robustness.
	NumStatements int

	// Count is the execution count for this block in the test run
	// the profile describes. In set mode Count ∈ {0, 1}; in count
	// and atomic modes Count is the unbounded execution count.
	Count int
}

// Covered reports whether the block was executed at least once in
// the run that produced its containing profile. Gate-4a counts a
// block as a "newly covered branch" only when it is Covered() in
// the after profile but not in the before profile.
func (b Block) Covered() bool { return b.Count > 0 }

// Key returns the location-only identity of a block, suitable for
// matching the same block across two profiles. The execution
// counts are deliberately excluded so two runs of the same code
// produce equal keys for the same source span.
func (b Block) Key() BlockKey {
	return BlockKey{
		File:      b.File,
		StartLine: b.StartLine,
		StartCol:  b.StartCol,
		EndLine:   b.EndLine,
		EndCol:    b.EndCol,
	}
}

// BlockKey is the location-only identity of a basic block. Two
// blocks with equal BlockKeys describe the same source span,
// regardless of how many times each was executed.
type BlockKey struct {
	File      string
	StartLine int
	StartCol  int
	EndLine   int
	EndCol    int
}

// Profile is the parsed form of a Go cover profile. Blocks are
// stored in input order so callers that want to render the profile
// back out (or correlate by index with another tool's output) can
// do so without re-sorting.
type Profile struct {
	// Mode is the cover mode: "set", "count", or "atomic".
	Mode string

	// Blocks holds every basic-block entry from the profile, in
	// input order. May be empty for a header-only profile (which
	// is itself well-formed: a package with no executable code
	// produces zero block entries).
	Blocks []Block
}

// CoveredBlocks returns the number of basic blocks in p whose
// Count > 0. A nil receiver is treated as zero blocks.
func (p *Profile) CoveredBlocks() int {
	if p == nil {
		return 0
	}
	n := 0
	for _, b := range p.Blocks {
		if b.Covered() {
			n++
		}
	}
	return n
}

// ParseProfile reads a Go cover profile from r and returns the
// parsed structure. The first non-empty, non-blank line MUST be
// "mode: <mode>" where <mode> is one of "set", "count", or
// "atomic". Subsequent lines are basic-block entries.
//
// Any parse failure yields an error wrapping ErrMalformedProfile;
// the partial result is discarded so callers cannot accidentally
// consume a half-parsed profile. Use errors.Is(err, ErrMalformedProfile)
// to detect this case.
func ParseProfile(r io.Reader) (*Profile, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: nil reader", ErrMalformedProfile)
	}
	sc := bufio.NewScanner(r)
	// Cover profile lines comfortably fit within the default
	// Scanner buffer (≤ 1 KiB even for long import paths). Real-
	// world profiles emitted by the Go toolchain never exceed
	// this; if a future toolchain change does, bufio.Scanner
	// returns a typed error that we wrap below.

	p := &Profile{}
	seenMode := false
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !seenMode {
			rest, ok := strings.CutPrefix(line, "mode:")
			if !ok {
				return nil, fmt.Errorf(
					"%w: line %d: expected \"mode:\" header, got %q",
					ErrMalformedProfile, lineNo, line,
				)
			}
			mode := strings.TrimSpace(rest)
			switch mode {
			case "set", "count", "atomic":
				p.Mode = mode
			default:
				return nil, fmt.Errorf(
					"%w: line %d: unknown mode %q (want set, count, or atomic)",
					ErrMalformedProfile, lineNo, mode,
				)
			}
			seenMode = true
			continue
		}
		b, err := parseBlock(line)
		if err != nil {
			return nil, fmt.Errorf("%w: line %d: %v", ErrMalformedProfile, lineNo, err)
		}
		p.Blocks = append(p.Blocks, b)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: read: %v", ErrMalformedProfile, err)
	}
	if !seenMode {
		return nil, fmt.Errorf("%w: missing \"mode:\" header", ErrMalformedProfile)
	}
	return p, nil
}

// parseBlock parses a single block entry of the form
//
//	<file>:<startLine>.<startCol>,<endLine>.<endCol> <numStatements> <count>
//
// The file portion may itself contain colons (Windows drive paths
// emit "C:/..." even though the Go toolchain normalizes most
// profiles to forward-slash paths). The split is done on the LAST
// ':' in the first whitespace-separated field, which correctly
// recovers the file portion in all cases the toolchain emits.
func parseBlock(line string) (Block, error) {
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return Block{}, fmt.Errorf(
			"expected 3 whitespace-separated fields, got %d in %q",
			len(fields), line,
		)
	}
	fileSpan, numStmtsS, countS := fields[0], fields[1], fields[2]

	colon := strings.LastIndexByte(fileSpan, ':')
	if colon < 0 {
		return Block{}, fmt.Errorf("missing %q separator in %q", ":", fileSpan)
	}
	file := fileSpan[:colon]
	span := fileSpan[colon+1:]
	if file == "" {
		return Block{}, fmt.Errorf("empty file in %q", fileSpan)
	}

	comma := strings.IndexByte(span, ',')
	if comma < 0 {
		return Block{}, fmt.Errorf("missing %q in span %q", ",", span)
	}
	startS, endS := span[:comma], span[comma+1:]
	sl, sc, err := parseLineCol(startS)
	if err != nil {
		return Block{}, fmt.Errorf("start %q: %v", startS, err)
	}
	el, ec, err := parseLineCol(endS)
	if err != nil {
		return Block{}, fmt.Errorf("end %q: %v", endS, err)
	}
	ns, err := strconv.Atoi(numStmtsS)
	if err != nil {
		return Block{}, fmt.Errorf("numStatements %q: %v", numStmtsS, err)
	}
	if ns < 0 {
		return Block{}, fmt.Errorf("numStatements %d < 0", ns)
	}
	cn, err := strconv.Atoi(countS)
	if err != nil {
		return Block{}, fmt.Errorf("count %q: %v", countS, err)
	}
	if cn < 0 {
		return Block{}, fmt.Errorf("count %d < 0", cn)
	}
	return Block{
		File:          file,
		StartLine:     sl,
		StartCol:      sc,
		EndLine:       el,
		EndCol:        ec,
		NumStatements: ns,
		Count:         cn,
	}, nil
}

// parseLineCol parses a "<line>.<col>" pair where both numbers are
// positive 1-based ints. It rejects 0 and negatives so the parser
// never accepts a span the cover tool itself would not emit.
func parseLineCol(s string) (int, int, error) {
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return 0, 0, fmt.Errorf("missing %q", ".")
	}
	line, err := strconv.Atoi(s[:dot])
	if err != nil {
		return 0, 0, fmt.Errorf("line: %v", err)
	}
	col, err := strconv.Atoi(s[dot+1:])
	if err != nil {
		return 0, 0, fmt.Errorf("col: %v", err)
	}
	if line <= 0 {
		return 0, 0, fmt.Errorf("line %d not positive", line)
	}
	if col <= 0 {
		return 0, 0, fmt.Errorf("col %d not positive", col)
	}
	return line, col, nil
}

// BranchDelta returns the number of basic blocks that are covered
// in after but were not covered in before. Blocks are matched by
// location key (BlockKey), so blocks added by the change under
// test (present only in after) and covered there each contribute
// +1; blocks present only in before contribute 0; and blocks whose
// covered/uncovered status is unchanged contribute 0.
//
// Either argument may be nil. A nil before is equivalent to "no
// blocks were covered before"; a nil after is equivalent to "no
// blocks were covered after" (which always yields 0).
//
// BranchDelta is the gate-4a delta. The auto-test-pr formula
// hard-fails when this value is ≤ 0: a comment-only marker (e.g.
// the bare gt:auto-test-pr marker line in a no-op test) cannot
// transition any block from uncovered to covered, so it correctly
// yields 0 here per the synthesis (.designs/auto-test-pr/
// synthesis.md, gate 4a).
func BranchDelta(before, after *Profile) int {
	beforeCovered := map[BlockKey]bool{}
	if before != nil {
		for _, b := range before.Blocks {
			if b.Covered() {
				beforeCovered[b.Key()] = true
			}
		}
	}
	delta := 0
	if after != nil {
		for _, b := range after.Blocks {
			if !b.Covered() {
				continue
			}
			if !beforeCovered[b.Key()] {
				delta++
			}
		}
	}
	return delta
}

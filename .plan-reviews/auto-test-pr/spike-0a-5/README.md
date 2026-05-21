# Phase 0a-5 Spike: Tautology sub-rule (i) precision/recall

Self-contained corpus + analyzer prototype for evaluating whether sub-rule (i)
of gate 4d ("≥1 assertion must depend on the function-under-test's return
value or observable side effect") is feasible at ≥85% precision / ≥75%
recall on real-Go-test patterns.

## Layout

```
spike-0a-5/
├── analyzer/        Go module — flow-sensitive analyzer prototype
├── corpus/
│   ├── tautological/   25 .txt fixtures, each is a Go test snippet that
│   │                   sub-rule (i) MUST flag (positive class)
│   └── good/           25 .txt fixtures, each is a Go test snippet that
│                       sub-rule (i) MUST NOT flag (negative class)
└── README.md
```

## Corpus annotation convention

First line of each fixture: `// SUT: <FunctionName>` — names the
function-under-test the analyzer treats as the SUT for that fixture. The
analyzer is told the SUT name out-of-band (test driver passes it in); in
production the gate would derive SUT from package + naming convention
(`TestFoo` → `Foo`), but for the spike we cut that variable out so
mis-detection of the SUT does not pollute the precision/recall measurement.

Files are `.txt` (not `.go`) so `go build ./...` and `go vet ./...` ignore
them. They parse cleanly with `go/parser.ParseFile`.

## Running

```
cd analyzer
go run . ../corpus
```

Outputs the confusion matrix and precision/recall.

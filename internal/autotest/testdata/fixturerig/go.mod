// Phase-0 e2e fixture rig (gu-v8qj8). A self-contained Go module that
// stands in for an opted-in auto-test-pr rig. The cycle's stub Targets
// hook points at classify.go (1 churned file with 2 uncovered branches);
// the in-process polecat writes a new *_test.go covering them and the
// 7 quality gates run against a temp copy of this module.
//
// This lives under testdata/ so the parent module's `go build/vet/test
// ./...` ignores it entirely (the go tool skips testdata directories and
// nested modules). The transitive testify pins below match the parent
// repo's go.sum so the fixture resolves entirely from the module cache
// without network access (no GOPROXY round-trip needed).
module fixturerig

go 1.23

require github.com/stretchr/testify v1.11.1

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

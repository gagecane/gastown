package gates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrePushHookMatchesYAML asserts that scripts/pre-push-check.sh runs the
// same gate commands declared in gates.yaml. This is the CI-side enforcement
// of the bead's acceptance criterion #1 — "New gate added to gates.yaml
// automatically appears in pre-push, formula, refinery, and CI without code
// changes in those four places."
//
// Until pre-push is rewritten to invoke `gt gates print --shell` directly, the
// hook keeps a hand-maintained list of gate commands. This test fails if any
// gate command from gates.yaml doesn't appear verbatim in the hook script —
// which is the drift the parent bead (gu-1wm3) aims to eliminate.
//
// When the hook migrates to consuming `gt gates print`, this test gets simpler
// (just assert the bash shells out to the right command). For now it's the
// canary.
func TestPrePushHookMatchesYAML(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := findRepoRoot(cwd)
	if err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(root, "scripts", "pre-push-check.sh")
	hookBody, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read %s: %v", hookPath, err)
	}
	hook := string(hookBody)

	f, err := Load(filepath.Join(root, "gates.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	for _, g := range f.All() {
		// Gates with non-trivial bash semantics (gofmt's "non-empty stdout = fail",
		// integration-tests being CI-only) deserve targeted checks rather than a
		// raw substring match — the bash form will not match the YAML form
		// verbatim.
		switch g.Name {
		case "gofmt":
			if !strings.Contains(hook, "gofmt -l") {
				t.Errorf("hook missing gofmt -l invocation declared in gates.yaml")
			}
			continue
		case "test":
			// pre-push-exempt by design (gs-4s06): b462f806 dropped the slow
			// `go test ./...` tier from the hook because the full suite always
			// blew past the hook's 360s wall, and the Refinery re-runs full
			// gates on every merge — so a pre-push test tier was redundant.
			// This gate is therefore refinery/CI-only at pre-push time, same
			// as integration-tests. Assert it does NOT appear as a live
			// command (the comment in the hook explaining the omission is
			// fine; we only inspect non-comment lines).
			for _, line := range strings.Split(hook, "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
				if strings.Contains(trimmed, "go test ./...") {
					t.Errorf("hook line %q runs go test ./... but gates.yaml's test gate is pre-push-exempt by design (gs-4s06); do not re-add the slow test tier to the hook", trimmed)
					break
				}
			}
			continue
		case "integration-tests":
			// ci-only — must NOT appear as a live command in pre-push (comments
			// are fine; the existing hook explains *why* integration tests are
			// skipped). We approximate "live command" by checking only
			// non-comment lines.
			for _, line := range strings.Split(hook, "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
				if strings.Contains(trimmed, "-tags=integration") {
					t.Errorf("hook line %q contains -tags=integration but gates.yaml declares integration-tests as ci-only", trimmed)
					break
				}
			}
			continue
		}

		// Other gates: the YAML command should appear as a substring of the
		// hook. We trim the trailing "./..." flexibility so e.g. `go build`
		// matches whether the hook spells it `go build ./...` or `go build`.
		needle := strings.TrimSpace(g.Command)
		if !strings.Contains(hook, needle) {
			t.Errorf("hook drift: gates.yaml declares %q as the %q gate command but pre-push-check.sh does not contain it",
				needle, g.Name)
		}
	}
}

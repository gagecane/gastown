package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestBdCLIVersionPinsMatchGoMod fails if any CI bd-CLI install pin drifts from
// the github.com/steveyegge/beads version in go.mod.
//
// Background (gu-6amrs): the bd CLI binary CI installs is version-pinned in
// several places that are decoupled from go.mod's beads library version. When
// go.mod bumped beads to v1.0.5 (which migrated the dependencies schema, e.g.
// splitting depends_on_id into depends_on_issue_id) but Dockerfile.e2e still
// pinned bd@v0.57.0, the stale bd binary's hardcoded ready_issues view referenced
// the dropped depends_on_id column. `bd init` then failed with "table \"d\" does
// not have column \"depends_on_id\"", breaking every TestInstall* e2e test and
// freezing the merge queue for days before anyone correlated the bump to the
// stale pin. This guard makes such drift fail fast and locally instead.
func TestBdCLIVersionPinsMatchGoMod(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	want := beadsVersionFromGoMod(t, repoRoot)

	// Files whose bd-CLI install pin must track go.mod. Each entry is scanned
	// for `go install .../beads/cmd/bd@<version>` and/or a BD_VERSION=<version>
	// arg; every concrete version found must equal `want`. The "@latest" pin in
	// scripts/test-gce-install.sh is intentionally excluded — it is a manual GCE
	// smoke test, not a CI gate, and is meant to float to the newest release.
	files := []string{
		filepath.Join("Dockerfile.e2e"),
		filepath.Join(".github", "workflows", "ci.yml"),
		filepath.Join(".github", "workflows", "nightly-integration.yml"),
	}

	// Matches `go install github.com/steveyegge/beads/cmd/bd@v1.2.3` (captures
	// the version) but not `@latest` or `@${BD_VERSION}` indirections, which are
	// validated via the BD_VERSION arg instead.
	bdInstallRE := regexp.MustCompile(`beads/cmd/bd@(v[0-9][^\s"'` + "`" + `]*)`)
	// Matches `ARG BD_VERSION=v1.2.3` / `BD_VERSION: v1.2.3` style declarations.
	bdVersionArgRE := regexp.MustCompile(`BD_VERSION[=:]\s*"?(v[0-9][^\s"'` + "`" + `]*)`)

	var violations []string
	var sawAnyPin bool

	for _, rel := range files {
		path := filepath.Join(repoRoot, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := string(data)

		for _, m := range bdInstallRE.FindAllStringSubmatch(text, -1) {
			sawAnyPin = true
			if got := m[1]; got != want {
				violations = append(violations, fmt.Sprintf(
					"%s: bd CLI install pin %q != go.mod beads %q", rel, got, want))
			}
		}
		for _, m := range bdVersionArgRE.FindAllStringSubmatch(text, -1) {
			sawAnyPin = true
			if got := m[1]; got != want {
				violations = append(violations, fmt.Sprintf(
					"%s: BD_VERSION %q != go.mod beads %q", rel, got, want))
			}
		}
	}

	if !sawAnyPin {
		t.Fatalf("found no bd CLI version pins in %v — the guard's matchers or file "+
			"list are stale; update them so drift is still caught", files)
	}
	if len(violations) > 0 {
		t.Fatalf("bd CLI version pins drifted from go.mod (github.com/steveyegge/beads %s). "+
			"Bump every pin in lockstep with go.mod to keep the e2e install fixture working:\n%s",
			want, strings.Join(violations, "\n"))
	}
}

// beadsVersionFromGoMod returns the github.com/steveyegge/beads version pinned
// in the repo's go.mod (e.g. "v1.0.5").
func beadsVersionFromGoMod(t *testing.T, repoRoot string) string {
	t.Helper()
	path := filepath.Join(repoRoot, "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\s*github\.com/steveyegge/beads\s+(v[0-9][^\s]*)`)
	m := re.FindStringSubmatch(string(data))
	if m == nil {
		t.Fatal("could not find github.com/steveyegge/beads version in go.mod")
	}
	return m[1]
}

package gates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoad_RepoFile reads the real gates.yaml at repo root via LoadFromRepo
// and asserts the gates the bead's acceptance criteria require: build, vet,
// gofmt, lint in the fast tier; test, integration-tests in the slow tier.
//
// This is the regression-test the bead's acceptance criterion #2 calls for —
// "Existing gates remain identical in behavior." If someone removes a gate
// from gates.yaml, this test fails and they have to look at the four
// consumers and make sure none of them relied on it.
func TestLoad_RepoFile(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	f, err := LoadFromRepo(cwd)
	if err != nil {
		t.Fatalf("LoadFromRepo: %v", err)
	}

	wantFast := map[string]Tier{
		"build": TierRequired,
		"vet":   TierRequired,
		"gofmt": TierRequired,
		"lint":  TierRequiredIfInstalled,
	}
	gotFast := map[string]Tier{}
	for _, g := range f.Gates.Fast {
		gotFast[g.Name] = g.Tier
	}
	for name, tier := range wantFast {
		if gotFast[name] != tier {
			t.Errorf("fast gate %q: tier=%q, want %q", name, gotFast[name], tier)
		}
	}

	wantSlow := map[string]Tier{
		"test":              TierRequired,
		"integration-tests": TierCIOnly,
	}
	gotSlow := map[string]Tier{}
	for _, g := range f.Gates.Slow {
		gotSlow[g.Name] = g.Tier
	}
	for name, tier := range wantSlow {
		if gotSlow[name] != tier {
			t.Errorf("slow gate %q: tier=%q, want %q", name, gotSlow[name], tier)
		}
	}

	// The "test" gate is the only one with skip_if_skip_prepush=true today.
	// If someone flips this without thinking it through, the GT_SKIP_PREPUSH
	// audit trail (gu-zy57) silently changes shape — fail loud.
	for _, g := range f.Gates.Slow {
		if g.Name == "test" && !g.SkipIfSkipPrepush {
			t.Errorf(`slow gate "test": skip_if_skip_prepush=false, want true (gu-zy57 contract)`)
		}
	}
}

func TestLoad_Validation(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "duplicate name across phases",
			yaml: `
gates:
  fast:
    - name: build
      command: go build ./...
      tier: required
  slow:
    - name: build
      command: go test ./...
      tier: required
`,
			wantErr: "duplicate gate name",
		},
		{
			name: "unknown tier",
			yaml: `
gates:
  fast:
    - name: build
      command: go build ./...
      tier: bogus
`,
			wantErr: "unknown tier",
		},
		{
			name: "ci-only in fast phase is incoherent",
			yaml: `
gates:
  fast:
    - name: build
      command: go build ./...
      tier: ci-only
`,
			wantErr: "ci-only tier is incompatible with fast phase",
		},
		{
			name: "empty command",
			yaml: `
gates:
  fast:
    - name: build
      command: ""
      tier: required
`,
			wantErr: "empty command",
		},
		{
			name: "missing name",
			yaml: `
gates:
  fast:
    - command: go build ./...
      tier: required
`,
			wantErr: "gate with no name",
		},
		{
			name: "unknown field rejected",
			yaml: `
gates:
  fast:
    - name: build
      comand: go build ./...
      tier: required
`,
			wantErr: "field comand not found",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempYAML(t, tc.yaml)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestAll_PreservesOrder(t *testing.T) {
	yaml := `
gates:
  fast:
    - name: alpha
      command: echo a
      tier: required
    - name: bravo
      command: echo b
      tier: required
  slow:
    - name: charlie
      command: echo c
      tier: required
`
	path := writeTempYAML(t, yaml)
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	all := f.All()
	wantOrder := []string{"alpha", "bravo", "charlie"}
	if len(all) != len(wantOrder) {
		t.Fatalf("got %d gates, want %d", len(all), len(wantOrder))
	}
	for i, name := range wantOrder {
		if all[i].Name != name {
			t.Errorf("All()[%d]=%q, want %q", i, all[i].Name, name)
		}
	}
	if all[0].Phase != PhaseFast || all[2].Phase != PhaseSlow {
		t.Errorf("phase tags lost ordering: got %v, %v, %v", all[0].Phase, all[1].Phase, all[2].Phase)
	}
}

func writeTempYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gates.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

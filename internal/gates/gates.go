// Package gates is the canonical reader for gates.yaml — the single source of
// truth describing which gate commands run in pre-push, CI, the polecat
// formula's gates_commands variable, and the refinery's merge gates.
//
// Today only pre-push reads from this package directly. The other three
// consumers (CI workflow, refinery, mol-polecat-work formula) still own their
// own copies; they will migrate one by one. The parent bead (gu-1wm3) tracks
// the full migration. Until those migrations land, gates.yaml MUST stay in
// sync with the other definitions — CI Lint + pre-push tests enforce this
// for the fast tier.
//
// Why a package and not just inline parsing in pre-push? Three of the four
// consumers are written in Go (refinery, formula resolver, future CI manifest
// generator). Putting the schema + reader here means each consumer migration
// is a thin call site — call Load, filter by tier/phase, render. The schema
// drift problem the bead describes is then literally impossible: there is one
// type, one parser, one set of validations.
package gates

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Tier classifies how strictly a gate must run.
//
//   - TierRequired: gate must run and pass everywhere it's invoked (fast tier
//     under --pre-verified, slow tier in CI, etc.).
//   - TierRequiredIfInstalled: gate must run if the underlying tool is on
//     PATH; absence is a soft warning rather than a failure. golangci-lint
//     uses this so contributors without the linter installed locally can
//     still push (CI is the authoritative gate).
//   - TierCIOnly: gate is too expensive or environment-specific to run
//     locally (integration tests need Docker + Dolt). Pre-push must NOT run
//     these; CI must.
type Tier string

const (
	TierRequired            Tier = "required"
	TierRequiredIfInstalled Tier = "required-if-installed"
	TierCIOnly              Tier = "ci-only"
)

// Phase classifies when a gate runs in the local pre-push hook.
//
//   - PhaseFast: run unconditionally, including under GT_SKIP_PREPUSH=1.
//     The fast tier exists because --pre-verified callers can lie or run
//     against a stale base; build/vet/gofmt/lint are seconds and close that
//     audit gap (see gu-7f0v, gu-mu11).
//   - PhaseSlow: skipped when GT_SKIP_PREPUSH=1 with a recorded reason.
//     Tests are the canonical slow gate.
type Phase string

const (
	PhaseFast Phase = "fast"
	PhaseSlow Phase = "slow"
)

// Gate is one declared gate. Field documentation lives on the YAML schema in
// gates.yaml; this struct mirrors that schema 1:1.
type Gate struct {
	Name              string `yaml:"name"`
	Command           string `yaml:"command"`
	Tier              Tier   `yaml:"tier"`
	SkipIfSkipPrepush bool   `yaml:"skip_if_skip_prepush"`
	Note              string `yaml:"note,omitempty"`
}

// File is the parsed root of gates.yaml. Field names match the YAML keys.
type File struct {
	Gates struct {
		Fast []Gate `yaml:"fast"`
		Slow []Gate `yaml:"slow"`
	} `yaml:"gates"`
}

// Load reads and validates gates.yaml at the given path. Validation errors
// (unknown tier, missing command, duplicate names) are surfaced as errors so
// a malformed gate definition fails closed at parse time rather than silently
// dropping a gate.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	// KnownFields rejects typos like `comand:` so a gate that meant to land
	// doesn't silently lose its command field.
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if err := validate(&f); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return &f, nil
}

// LoadFromRepo finds gates.yaml at the repo root, walking up from startDir.
// Convenience helper for callers that only know the current working directory
// (the pre-push hook, mostly).
func LoadFromRepo(startDir string) (*File, error) {
	root, err := findRepoRoot(startDir)
	if err != nil {
		return nil, err
	}
	return Load(filepath.Join(root, "gates.yaml"))
}

// All returns every gate flattened with its phase tag, in declaration order:
// fast tier first, then slow tier. Stable iteration order matters because the
// pre-push hook prints "[fast] go build ./..." in the order the YAML lists
// the gates; reordering the file should reorder the hook output and nothing
// else.
func (f *File) All() []PhasedGate {
	out := make([]PhasedGate, 0, len(f.Gates.Fast)+len(f.Gates.Slow))
	for _, g := range f.Gates.Fast {
		out = append(out, PhasedGate{Gate: g, Phase: PhaseFast})
	}
	for _, g := range f.Gates.Slow {
		out = append(out, PhasedGate{Gate: g, Phase: PhaseSlow})
	}
	return out
}

// PhasedGate is a Gate enriched with its declared phase. The phase isn't on
// the Gate struct because it's implied by the YAML location (under fast: or
// slow:), and duplicating it on the gate would let the two get out of sync.
type PhasedGate struct {
	Gate
	Phase Phase
}

// validate enforces invariants that aren't expressible in the YAML schema:
// every gate has a name + command, names are unique within a phase, tiers
// are in the known set.
func validate(f *File) error {
	seen := map[string]Phase{}
	check := func(g Gate, phase Phase) error {
		if g.Name == "" {
			return fmt.Errorf("%s tier: gate with no name", phase)
		}
		if g.Command == "" {
			return fmt.Errorf("%s tier: gate %q: empty command", phase, g.Name)
		}
		switch g.Tier {
		case TierRequired, TierRequiredIfInstalled, TierCIOnly:
		default:
			return fmt.Errorf("%s tier: gate %q: unknown tier %q", phase, g.Name, g.Tier)
		}
		if existing, ok := seen[g.Name]; ok {
			return fmt.Errorf("duplicate gate name %q (already declared in %s tier)", g.Name, existing)
		}
		seen[g.Name] = phase
		// ci-only gates in the fast phase make no sense — the fast phase
		// is "always run, including locally"; ci-only is "never run
		// locally". Catch this combination early.
		if phase == PhaseFast && g.Tier == TierCIOnly {
			return fmt.Errorf("fast tier: gate %q: ci-only tier is incompatible with fast phase", g.Name)
		}
		return nil
	}
	for _, g := range f.Gates.Fast {
		if err := check(g, PhaseFast); err != nil {
			return err
		}
	}
	for _, g := range f.Gates.Slow {
		if err := check(g, PhaseSlow); err != nil {
			return err
		}
	}
	return nil
}

// findRepoRoot walks up from startDir looking for a .git entry. Mirrors what
// `git rev-parse --show-toplevel` does without shelling out.
func findRepoRoot(startDir string) (string, error) {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("no .git found from %s", startDir)
		}
		abs = parent
	}
}

package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/formula"
)

const (
	// bdMolTimeout is the timeout for bd molecule operations.
	bdMolTimeout = 15 * time.Second
)

// dogMol tracks a molecule (wisp) lifecycle for a daemon dog patrol.
// Graceful degradation: if bd fails, the dog still does its work — molecule
// tracking is observability, not control flow.
//
// Wisp model: `bd mol wisp <formula>` creates a ROOT-ONLY wisp — the formula's
// [[steps]] are NOT materialized as child step-wisps. Steps are read from the
// formula file (the same model prime/polecat-work uses, see prime_molecule.go).
// closeStep/failStep are therefore observability-only: they validate the step
// against the formula and log progress; there are no child beads to close.
type dogMol struct {
	rootID   string          // Root wisp ID (e.g., "gt-wisp-abc123"), empty if pour failed.
	formula  string          // Formula name, for log context.
	steps    map[string]bool // Valid step IDs declared in the formula.
	bdPath   string
	townRoot string
	logger   interface{ Printf(string, ...interface{}) }
}

// pourDogMolecule creates an ephemeral wisp molecule from a formula.
// Returns a dogMol handle for closing steps. If bd fails, returns a no-op
// handle so the caller can proceed without error checking.
func (d *Daemon) pourDogMolecule(formulaName string, vars map[string]string) *dogMol {
	dm := &dogMol{
		formula:  formulaName,
		steps:    make(map[string]bool),
		bdPath:   d.bdPath,
		townRoot: d.config.TownRoot,
		logger:   d.logger,
	}

	// Build args: bd mol wisp <formula> --var k=v ...
	args := []string{"mol", "wisp", formulaName}
	for k, v := range vars {
		args = append(args, "--var", fmt.Sprintf("%s=%s", k, v))
	}

	out, err := dm.runBd(args...)
	if err != nil {
		d.logger.Printf("dog_molecule: pour %s failed (non-fatal): %v", formulaName, err)
		return dm
	}

	// Parse root ID from output. bd mol wisp prints the root ID on the first line.
	// Example output: "✓ Spawned wisp: gt-wisp-abc123 — Reap stale wisps..."
	dm.rootID = parseWispID(out)
	if dm.rootID == "" {
		d.logger.Printf("dog_molecule: pour %s: could not parse root ID from output: %s", formulaName, out)
		return dm
	}

	// Load the formula's step IDs from the formula file. Steps are not
	// materialized as child wisps (root-only wisp model), so we read them
	// directly to give closeStep/failStep a valid step set for observability.
	dm.loadFormulaSteps(formulaName)

	d.logger.Printf("dog_molecule: poured %s → %s (%d steps)", formulaName, dm.rootID, len(dm.steps))
	return dm
}

// loadFormulaSteps reads the formula file and records its declared step IDs.
// Non-fatal: on any failure the step set stays empty and closeStep will warn.
func (dm *dogMol) loadFormulaSteps(formulaName string) {
	content, err := formula.ResolveFormulaContent(formulaName, dm.townRoot, "")
	if err != nil {
		dm.logger.Printf("dog_molecule: load formula %s failed (non-fatal): %v", formulaName, err)
		return
	}

	f, err := formula.Parse(content)
	if err != nil {
		dm.logger.Printf("dog_molecule: parse formula %s failed (non-fatal): %v", formulaName, err)
		return
	}

	// Resolve extends/compose so steps inherited from parent formulas are
	// included. Fall back to the unresolved formula if resolution fails.
	if resolved, rerr := formula.Resolve(f, dm.formulaSearchPaths()); rerr == nil {
		f = resolved
	}

	for _, step := range f.Steps {
		if step.ID != "" {
			dm.steps[step.ID] = true
		}
	}
}

// formulaSearchPaths returns the town-level formula override dir used to
// resolve extends/compose chains. Dog formulas are town/embedded, not
// rig-specific, so only the town tier is needed.
func (dm *dogMol) formulaSearchPaths() []string {
	if dm.townRoot == "" {
		return nil
	}
	return []string{filepath.Join(dm.townRoot, ".beads", "formulas")}
}

// closeStep records a molecule step as complete. Steps are read from the
// formula file rather than materialized as child wisps, so this is
// observability only — it validates the step against the formula and logs an
// unknown-step warning if the dog's Go code and the formula have drifted.
func (dm *dogMol) closeStep(stepID string) {
	if dm.rootID == "" {
		return // No molecule — graceful degradation.
	}
	if !dm.steps[stepID] {
		dm.logger.Printf("dog_molecule: closeStep %q on %s: unknown step (known: %v)", stepID, dm.formula, dm.knownSteps())
	}
}

// failStep records a molecule step as failed with a reason. Like closeStep,
// this is observability only under the root-only wisp model.
func (dm *dogMol) failStep(stepID, reason string) {
	if dm.rootID == "" {
		return
	}
	if !dm.steps[stepID] {
		dm.logger.Printf("dog_molecule: failStep %q on %s: unknown step (known: %v)", stepID, dm.formula, dm.knownSteps())
		return
	}
	dm.logger.Printf("dog_molecule: step %s on %s failed: %s", stepID, dm.formula, reason)
}

// close closes the root molecule wisp. There are no child step wisps to close
// under the root-only wisp model.
func (dm *dogMol) close() {
	if dm.rootID == "" {
		return
	}

	_, err := dm.runBd("close", dm.rootID)
	if err != nil {
		dm.logger.Printf("dog_molecule: close root %s failed (non-fatal): %v", dm.rootID, err)
	}
}

// knownSteps returns the list of known step IDs for debugging.
func (dm *dogMol) knownSteps() []string {
	var steps []string
	for k := range dm.steps {
		steps = append(steps, k)
	}
	return steps
}

// runBd executes a bd command and returns stdout.
func (dm *dogMol) runBd(args ...string) (string, error) {
	bdPath := dm.bdPath
	if bdPath == "" {
		bdPath = "bd"
	}

	ctx, cancel := context.WithTimeout(context.Background(), bdMolTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bdPath, args...)
	beads.ConfigureCommand(cmd, dm.townRoot, filepath.Join(dm.townRoot, ".beads"), beads.SubprocessModeForArgs(args))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("%s: %s", err, errMsg)
		}
		return "", err
	}

	return strings.TrimSpace(stdout.String()), nil
}

// parseWispID extracts a wisp ID from bd mol wisp output.
// Looks for patterns like "gt-wisp-abc123" or any ID containing "-wisp-".
func parseWispID(output string) string {
	for _, word := range strings.Fields(output) {
		// Strip ANSI codes and punctuation.
		cleaned := stripANSI(word)
		cleaned = strings.TrimRight(cleaned, ".,;:!?")
		if strings.Contains(cleaned, "-wisp-") {
			return cleaned
		}
	}
	// Fallback: look for any bead-like ID (prefix-xxxx pattern).
	for _, word := range strings.Fields(output) {
		cleaned := stripANSI(word)
		cleaned = strings.TrimRight(cleaned, ".,;:!?")
		if len(cleaned) > 3 && strings.Contains(cleaned, "-") && !strings.HasPrefix(cleaned, "--") {
			// Could be a bead ID like "gt-abc123".
			return cleaned
		}
	}
	return ""
}

// stripANSI removes ANSI escape codes from a string.
func stripANSI(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\033' {
			// Skip escape sequence.
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) {
					i++
				}
				if i < len(s) {
					i++ // Skip the terminating letter.
				}
			}
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}

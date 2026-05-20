package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/steveyegge/gastown/internal/workspace"
)

// wrongRigPatterns is the set of regexes that indicate a bead was closed
// because it didn't belong in the closing rig. These power Layer 2 of the
// auto-dispatch wrong-rig feedback loop (companion to gu-mhfs Layer 1's
// wrong-rig:<rig> label / sling guard): when a polecat closes a bead with
// any of these phrases in the reason, we auto-attach the corresponding
// wrong-rig:<closing-rig> label so the next dispatcher refuses to route
// the bead back to the same wrong rig.
//
// Patterns are intentionally narrow to keep false-positive risk low. False
// positives are still benign — the label only matters if the bead is
// later reopened and re-dispatched, and the worst case is the bead skips
// one rig that would have been wrong anyway.
var wrongRigPatterns = []*regexp.Regexp{
	// "wrong rig", "wrong-rig", "Wrong Rig", "no-changes — wrong rig"
	regexp.MustCompile(`(?i)\bwrong[\s-]?rig\b`),
	// "belongs in casc_crud", "belongs in rig casc_crud", "belong in foo"
	regexp.MustCompile(`(?i)\bbelongs?\s+in\s+(?:rig\s+)?[\w-]+`),
	// "should be in casc_crud", "should be filed in casc_crud",
	// "should be filed under casc_crud"
	regexp.MustCompile(`(?i)\bshould\s+be\s+(?:in|filed\s+(?:in|under))\s+(?:rig\s+)?[\w-]+`),
	// "not this rig", "not the right rig", "not in this rig", "not right rig"
	regexp.MustCompile(`(?i)\bnot\s+(?:in\s+)?(?:this|the\s+right|right)\s+rig\b`),
}

// matchesWrongRigReason reports whether the reason text indicates the bead
// was closed because it didn't belong in the closing rig.
func matchesWrongRigReason(reason string) bool {
	if reason == "" {
		return false
	}
	for _, p := range wrongRigPatterns {
		if p.MatchString(reason) {
			return true
		}
	}
	return false
}

// extractCloseReason returns the value of --reason / -r / --reason=<value>
// from a close args slice. Returns "" if no reason flag is present.
//
// Caller is expected to pass args with --comment already aliased to
// --reason (see runClose), so this only inspects the canonical flags.
func extractCloseReason(args []string) string {
	skipNext := false
	for i, arg := range args {
		if skipNext {
			return arg
		}
		if arg == "--reason" || arg == "-r" {
			if i+1 < len(args) {
				skipNext = true
			}
			continue
		}
		if strings.HasPrefix(arg, "--reason=") {
			return strings.TrimPrefix(arg, "--reason=")
		}
	}
	return ""
}

// detectClosingRig returns the rig name that performed the close, or ""
// if it cannot be determined. Prefers GT_RIG (set by gt prime in polecat
// sessions); falls back to inferring from the working directory.
func detectClosingRig() string {
	if rig := strings.TrimSpace(os.Getenv("GT_RIG")); rig != "" {
		return rig
	}
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return ""
	}
	rig, err := inferRigFromCwd(townRoot)
	if err != nil {
		return ""
	}
	return rig
}

// applyWrongRigLabels adds a wrong-rig:<closing-rig> label to each closed
// bead when the close reason matches one of the wrong-rig phrasings.
// Best-effort: errors are logged to stderr and do not fail the close.
//
// This is the auto-attach side of the gu-mhfs Layer 1 label. The matching
// guard that respects the label lives in sling and the auto-dispatch
// run.sh filter.
func applyWrongRigLabels(beadIDs []string, reason string) {
	if !matchesWrongRigReason(reason) {
		return
	}
	rig := detectClosingRig()
	if rig == "" {
		return
	}
	label := "wrong-rig:" + rig
	for _, id := range beadIDs {
		updateCmd := exec.Command("bd", "update", id, "--add-label", label)
		if dir := resolveBeadDir(id); dir != "" && dir != "." {
			updateCmd.Dir = dir
			updateCmd.Env = filterEnvKey(os.Environ(), "BEADS_DIR")
		}
		if out, err := updateCmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not add %s label to %s: %v\n%s",
				label, id, err, string(out))
		}
	}
}

package witness

import (
	"encoding/json"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// minGateNeedleLen guards against trivially-short gate commands matching
// unrelated processes. A gate command shorter than this is ignored for
// liveness detection (it would over-match the descendant arg scan).
const minGateNeedleLen = 3

// gateProcessRunning reports whether any of the supplied process command lines
// (typically the descendant processes of a polecat's pane) corresponds to one
// of the polecat's configured pre-merge gates actively executing.
//
// Gates run as `sh -c "<cmd>"`, so the gate command string appears verbatim in
// the argv of the wrapping shell process; a substring match against the full
// gate command is precise enough to avoid false positives while catching the
// long-running gate (e.g. `go test ./...`, `scripts/refinery-gate.sh`,
// `brazil-build release`). (gu-0x2be)
func gateProcessRunning(gates []PolecatGate, procCmds []string) bool {
	if len(gates) == 0 || len(procCmds) == 0 {
		return false
	}
	for _, gate := range gates {
		needle := strings.TrimSpace(gate.Cmd)
		if len(needle) < minGateNeedleLen {
			continue
		}
		for _, proc := range procCmds {
			if strings.Contains(proc, needle) {
				return true
			}
		}
	}
	return false
}

// loadHookGates parses the pre-merge gate commands configured on a polecat's
// hook bead (via its formula_vars gates_commands block). Returns nil when the
// bead can't be read or has no gates — callers treat that as "no gates to
// check" and fall back to their existing behavior. (gu-0x2be)
func loadHookGates(bd *BdCli, workDir, hookBead string) []PolecatGate {
	if hookBead == "" {
		return nil
	}
	out, err := bd.Exec(workDir, "show", hookBead, "--json")
	if err != nil || out == "" {
		return nil
	}
	var issues []struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(out), &issues); err != nil || len(issues) == 0 {
		return nil
	}
	af := beads.ParseAttachmentFields(&beads.Issue{Description: issues[0].Description})
	if af == nil {
		return nil
	}
	return extractGatesCommandsVar(af.FormulaVars)
}

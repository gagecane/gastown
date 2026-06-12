// Package cmd implements `gt agents resolve`, a helper that locates an
// agent bead ID across both the issues and wisps tables, in both the rig
// and town DBs.
//
// This works around two issues that strand legacy refinery/witness agent
// beads from rig worktrees (aa-b2tm):
//
//  1. `bd list --label=gt:agent` only queries the issues table. After the
//     agent-bead-to-wisps migration, agent beads live in the wisps table
//     and are invisible to the patrol formula's resolver query.
//  2. Some legacy refinery/witness agent beads (e.g. au-wisp-0ti,
//     aa-wisp-a4v) live in hq.wisps (town DB) even though their prefix
//     routes to the rig DB. From a rig worktree, bd lookups against the
//     rig DB miss them entirely.
//
// `gt agents resolve --role refinery --rig <rig>` searches both tables in
// both DBs and prints the matching bead ID (with provenance metadata in
// JSON mode). The patrol formula uses this to discover its agent bead at
// runtime instead of the legacy `bd list --label=gt:agent` query.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	agentsResolveRole     string
	agentsResolveRig      string
	agentsResolveJSON     bool
	agentsResolveQuiet    bool
	agentsResolveTownRoot string // override town root (for tests)
)

var agentsResolveCmd = &cobra.Command{
	Use:   "resolve",
	Short: "Resolve an agent bead ID for a given role+rig across tables and DBs",
	Long: `Resolve an agent bead ID by role and rig.

Searches BOTH the issues table and the wisps table (` + "`bd mol wisp list`" + `)
in BOTH the rig database and the town database, returning the best match.

This is the patrol-formula-safe replacement for
` + "`bd list --label=gt:agent --desc-contains=\"role_type: refinery\"`" + `:

  - It sees agent beads that have migrated to the wisps table.
  - It sees legacy agent beads stranded in hq.wisps (aa-b2tm).
  - It filters by role and rig so the patrol gets exactly one match.

PREFERENCE ORDER:
  1. Rig DB wisps  (modern, canonical)
  2. Rig DB issues (pre-migration)
  3. Town DB wisps (legacy hq.wisps strandings)
  4. Town DB issues (very old)

EXAMPLES:
  # Print the bead ID (one per line, suitable for shell)
  gt agents resolve --role refinery --rig alleago_ui

  # JSON output with full provenance
  gt agents resolve --role refinery --rig alleago_ui --json
`,
	RunE: runAgentsResolve,
}

func init() {
	agentsResolveCmd.Flags().StringVar(&agentsResolveRole, "role", "",
		"Role to resolve (refinery, witness, crew, polecat, etc.) — required")
	agentsResolveCmd.Flags().StringVar(&agentsResolveRig, "rig", "",
		"Rig name to filter by — required for rig-level roles")
	agentsResolveCmd.Flags().BoolVar(&agentsResolveJSON, "json", false,
		"Output as JSON with source/table provenance")
	agentsResolveCmd.Flags().BoolVar(&agentsResolveQuiet, "quiet", false,
		"Suppress 'no match' diagnostic on stderr (exit code still reflects result)")
	agentsResolveCmd.Flags().StringVar(&agentsResolveTownRoot, "town-root", "",
		"Override town root (defaults to workspace detection or $GT_TOWN_ROOT)")
	_ = agentsResolveCmd.MarkFlagRequired("role")

	agentsCmd.AddCommand(agentsResolveCmd)
}

// agentBeadCandidate is a single match from one of the four searched sources.
type agentBeadCandidate struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Source string `json:"source"` // "rig-wisps", "rig-issues", "town-wisps", "town-issues"
	Role   string `json:"role"`
	Rig    string `json:"rig"`
}

// agentsResolveResult is the JSON envelope returned by --json mode.
type agentsResolveResult struct {
	Match      *agentBeadCandidate  `json:"match,omitempty"`
	Candidates []agentBeadCandidate `json:"candidates,omitempty"`
}

func runAgentsResolve(cmd *cobra.Command, args []string) error {
	if agentsResolveRole == "" {
		return fmt.Errorf("--role is required")
	}

	townRoot, err := resolveResolveTownRoot()
	if err != nil {
		return err
	}

	candidates := findAgentBeadCandidates(townRoot, agentsResolveRig, agentsResolveRole)

	// Pick the best match using the preference order documented above.
	match := pickBestAgentBead(candidates)

	if agentsResolveJSON {
		out := agentsResolveResult{
			Match:      match,
			Candidates: candidates,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	if match == nil {
		if !agentsResolveQuiet {
			fmt.Fprintf(os.Stderr, "%s no agent bead found for role=%s rig=%s\n",
				style.Warning.Render("!"), agentsResolveRole, agentsResolveRig)
		}
		return fmt.Errorf("no agent bead found")
	}

	fmt.Println(match.ID)
	return nil
}

// resolveResolveTownRoot determines the town root for resolution, honoring
// --town-root, then GT_TOWN_ROOT, then workspace detection.
func resolveResolveTownRoot() (string, error) {
	if agentsResolveTownRoot != "" {
		return agentsResolveTownRoot, nil
	}
	if env := os.Getenv("GT_TOWN_ROOT"); env != "" {
		return env, nil
	}
	root, err := workspace.FindFromCwd()
	if err != nil || root == "" {
		return "", fmt.Errorf("could not determine town root (set GT_TOWN_ROOT or pass --town-root)")
	}
	return root, nil
}

// findAgentBeadCandidates searches the four (table, db) sources for agent
// beads matching the given role and rig.
func findAgentBeadCandidates(townRoot, rig, role string) []agentBeadCandidate {
	townBeadsDir := filepath.Join(townRoot, ".beads")

	// Build the list of beads dirs to search: town + rig (if specified and different).
	type searchTarget struct {
		dir     string
		dbLabel string // "town" or "rig"
	}
	targets := []searchTarget{
		{dir: townBeadsDir, dbLabel: "town"},
	}
	if rig != "" {
		rigDir := beads.GetRigDirForName(townRoot, rig)
		if rigDir != "" {
			rigBeadsDir := beads.ResolveBeadsDir(rigDir)
			if rigBeadsDir != "" && rigBeadsDir != townBeadsDir {
				targets = append(targets, searchTarget{dir: rigBeadsDir, dbLabel: "rig"})
			}
		}
	}

	var candidates []agentBeadCandidate
	for _, t := range targets {
		// Search wisps table
		wispCands := searchAgentBeadsInDir(t.dir, true, role, rig)
		for _, c := range wispCands {
			c.Source = t.dbLabel + "-wisps"
			candidates = append(candidates, c)
		}
		// Search issues table
		issueCands := searchAgentBeadsInDir(t.dir, false, role, rig)
		for _, c := range issueCands {
			c.Source = t.dbLabel + "-issues"
			candidates = append(candidates, c)
		}
	}

	return candidates
}

// searchAgentBeadsInDir runs bd against a single beads dir and returns
// candidates that match the given role+rig.
func searchAgentBeadsInDir(beadsDir string, wisps bool, role, rig string) []agentBeadCandidate {
	ctx, cancel := context.WithTimeout(context.Background(), bdCallTimeout)
	defer cancel()

	var args []string
	if wisps {
		args = []string{"mol", "wisp", "list", "--json"}
	} else {
		// --all keeps closed agent beads discoverable: the reaper or a manual
		// close should not make `gt agents resolve` blind to an agent's identity
		// bead (gu-016x1). pickBestAgentBead already prefers open over closed.
		args = []string{"list", "--label=gt:agent", "--include-infra", "--all", "--json", "--flat", "--no-pager", "--limit=0"}
	}

	cmd := exec.CommandContext(ctx, "bd", args...) //nolint:gosec // G204: bd is a trusted internal tool
	cmd.Env = append(os.Environ(), "BEADS_DIR="+beadsDir)

	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	// Wisps come back as {"wisps": [...]} while issues come back as a bare array.
	type entry struct {
		ID          string   `json:"id"`
		Status      string   `json:"status"`
		Type        string   `json:"issue_type"`
		Labels      []string `json:"labels"`
		Description string   `json:"description"`
	}
	var entries []entry
	if wisps {
		var wrapper struct {
			Wisps []entry `json:"wisps"`
		}
		if err := json.Unmarshal(out, &wrapper); err != nil {
			return nil
		}
		entries = wrapper.Wisps
	} else {
		if err := json.Unmarshal(out, &entries); err != nil {
			return nil
		}
	}

	var matches []agentBeadCandidate
	for _, e := range entries {
		if !entryMatchesRole(e.Description, role) {
			continue
		}
		if rig != "" && !entryMatchesRig(e.Description, e.ID, rig) {
			continue
		}
		matches = append(matches, agentBeadCandidate{
			ID:     e.ID,
			Status: e.Status,
			Role:   role,
			Rig:    rig,
		})
	}
	return matches
}

// entryMatchesRole returns true if the bead description matches the
// requested role (via a "role_type: <role>" line).
func entryMatchesRole(description, role string) bool {
	if role == "" {
		return true
	}
	// Check description for "role_type: <role>"
	needle := "role_type: " + role
	for _, line := range strings.Split(description, "\n") {
		if strings.TrimSpace(line) == needle {
			return true
		}
	}
	return false
}

// entryMatchesRig returns true if the bead description has "rig: <rig>",
// or as a fallback if the ID contains the rig name as a path component.
func entryMatchesRig(description, id, rig string) bool {
	needle := "rig: " + rig
	for _, line := range strings.Split(description, "\n") {
		if strings.TrimSpace(line) == needle {
			return true
		}
	}
	// Fallback: legacy beads with sparse descriptions sometimes embed rig in
	// the ID. Match "-<rig>-" or trailing "-<rig>".
	if strings.Contains(id, "-"+rig+"-") || strings.HasSuffix(id, "-"+rig) {
		return true
	}
	return false
}

// pickBestAgentBead returns the highest-preference open candidate from the
// list. Preference order: rig-wisps > rig-issues > town-wisps > town-issues.
// Among same-source candidates, an open bead beats a closed one.
func pickBestAgentBead(candidates []agentBeadCandidate) *agentBeadCandidate {
	sourceRank := map[string]int{
		"rig-wisps":   0,
		"rig-issues":  1,
		"town-wisps":  2,
		"town-issues": 3,
	}

	var best *agentBeadCandidate
	bestRank := 1 << 30
	bestOpen := false
	for i := range candidates {
		c := candidates[i]
		open := c.Status != "closed"
		rank := sourceRank[c.Source]
		if best == nil {
			best = &candidates[i]
			bestRank = rank
			bestOpen = open
			continue
		}
		// Prefer open beads first, then higher-ranked sources.
		if open && !bestOpen {
			best = &candidates[i]
			bestRank = rank
			bestOpen = open
			continue
		}
		if open == bestOpen && rank < bestRank {
			best = &candidates[i]
			bestRank = rank
			bestOpen = open
		}
	}
	return best
}

package cmd

import (
	"path/filepath"

	"github.com/steveyegge/gastown/internal/beads"
	gitpkg "github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/reaper"
)

// orphanGitReconcileAdapter wires the reaper's git-evidence orphan reconcile
// (gu-hrweu) to the production beads + git layers. It satisfies
// reaper.OrphanGitReconcileBeads (cross-rig label listing + routed
// close/update) and reaper.GitMergeProof (commit-citation lookup against the
// target branch).
//
// Source issues carrying awaiting_refinery_merge live in per-rig databases, so
// listing aggregates over routes.jsonl. Closes and label-clears go through a
// town-rooted ROUTING client (noRoute=false) so rig-prefixed IDs resolve to the
// owning rig DB via beads.ResolveRoutingTarget.
type orphanGitReconcileAdapter struct {
	townRoot string
	router   *beads.Beads // routing client: prefix → rig DB
}

func newOrphanGitReconcileAdapter(townRoot string) *orphanGitReconcileAdapter {
	return &orphanGitReconcileAdapter{
		townRoot: townRoot,
		router:   beads.New(townRoot),
	}
}

// Show routes by prefix to the owning rig DB.
func (a *orphanGitReconcileAdapter) Show(id string) (*beads.Issue, error) {
	return a.router.Show(id)
}

// ShowMultiple fetches fresh state for many bead IDs with one batched query per
// owning rig DB instead of one Show per bead (the old reaper 1+N round-trips —
// gu-nid89.21). Source issues live in per-rig databases, so the IDs are first
// grouped by the rig DB they route to (via the same ResolveRoutingTarget the
// per-ID Show uses), then each group is fetched in a single beads.ShowMultiple.
// IDs that no longer exist (purged/reaped) are simply omitted from the result.
func (a *orphanGitReconcileAdapter) ShowMultiple(ids []string) (map[string]*beads.Issue, error) {
	result := make(map[string]*beads.Issue, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

	townRoot := a.townRoot
	fallbackDir := filepath.Join(a.townRoot, ".beads")

	// Group IDs by the beads dir they route to so each rig DB is queried once.
	byDir := map[string][]string{}
	for _, id := range ids {
		if id == "" {
			continue
		}
		dir := beads.ResolveRoutingTarget(townRoot, id, fallbackDir)
		byDir[dir] = append(byDir[dir], id)
	}

	for dir, dirIDs := range byDir {
		client := beads.NewWithBeadsDir(filepath.Dir(dir), dir)
		issues, err := client.ShowMultiple(dirIDs)
		if err != nil {
			// Best-effort: an unreachable rig DB must not abort the whole batch.
			// Skip its IDs (they fall through as terminal) and continue.
			continue
		}
		for id, issue := range issues {
			result[id] = issue
		}
	}
	return result, nil
}

// ForceCloseWithReason routes by prefix to the owning rig DB.
func (a *orphanGitReconcileAdapter) ForceCloseWithReason(reason string, ids ...string) error {
	return a.router.ForceCloseWithReason(reason, ids...)
}

// Update routes by prefix to the owning rig DB.
func (a *orphanGitReconcileAdapter) Update(id string, opts beads.UpdateOptions) error {
	return a.router.Update(id, opts)
}

// ListIssuesWithLabel aggregates issues carrying the given label across every
// rig database registered in routes.jsonl (plus the town/hq DB). Source issues
// live in per-rig DBs, so a single town-rooted list would miss them. Results
// are de-duplicated by ID (multiple route prefixes can map to the same path,
// e.g. several town-level prefixes all map to ".").
func (a *orphanGitReconcileAdapter) ListIssuesWithLabel(label string) ([]*beads.Issue, error) {
	townBeadsDir := filepath.Join(a.townRoot, ".beads")
	routes, err := beads.LoadRoutes(townBeadsDir)
	if err != nil {
		return nil, err
	}

	// Always include the town beads dir itself (covers town-level beads even
	// when routes.jsonl is sparse).
	seenDirs := map[string]bool{}
	var dirs []string
	addDir := func(d string) {
		if d == "" || seenDirs[d] {
			return
		}
		seenDirs[d] = true
		dirs = append(dirs, d)
	}
	addDir(townBeadsDir)
	for _, r := range routes {
		var rigBeadsDir string
		if filepath.IsAbs(r.Path) {
			rigBeadsDir = r.Path
		} else {
			rigBeadsDir = filepath.Join(a.townRoot, r.Path)
		}
		addDir(filepath.Join(rigBeadsDir, ".beads"))
	}

	seenIDs := map[string]bool{}
	var aggregated []*beads.Issue
	for _, dir := range dirs {
		if !isDir(dir) {
			continue
		}
		b := beads.NewWithBeadsDir(filepath.Dir(dir), dir)
		issues, err := b.List(beads.ListOptions{
			Status:   "all",
			Label:    label,
			Priority: -1,
		})
		if err != nil {
			// Best-effort: a single unreachable rig DB must not abort the whole
			// reconcile. Skip it and continue aggregating the rest.
			continue
		}
		for _, issue := range issues {
			if issue == nil || issue.ID == "" || seenIDs[issue.ID] {
				continue
			}
			seenIDs[issue.ID] = true
			aggregated = append(aggregated, issue)
		}
	}
	return aggregated, nil
}

// ProveMerged reports whether a commit citing the source issue's bead ID has
// landed on its target branch — durable proof the work merged, independent of
// the MR wisp bead and the agent bead's active_mr (both purgeable).
//
// verified is false when no usable git worktree could be resolved or the git
// command failed; the caller fails closed in that case (never closes the bead).
func (a *orphanGitReconcileAdapter) ProveMerged(issue *beads.Issue) (proven bool, verified bool) {
	if issue == nil || issue.ID == "" {
		return false, false
	}
	rigPath := resolveRigWorktreePath(a.townRoot, issue.ID)
	if rigPath == "" {
		return false, false
	}
	g := gitpkg.NewGit(rigPath)
	if !g.IsRepo() {
		return false, false
	}

	// Target branch: prefer the issue's recorded base_branch (relay/integration
	// legs target a non-default branch); fall back to the remote default branch.
	targetBranch := beads.GetBaseBranchField(issue.Description)
	if targetBranch == "" {
		targetBranch = g.RemoteDefaultBranch()
	}
	if targetBranch == "" {
		targetBranch = "main"
	}

	cited, err := g.HasCommitCitingRef("origin/"+targetBranch, issue.ID)
	if err != nil {
		return false, false
	}
	return cited, true
}

// Ensure the adapter satisfies the reaper interfaces at compile time.
var (
	_ reaper.OrphanGitReconcileBeads = (*orphanGitReconcileAdapter)(nil)
	_ reaper.GitMergeProof           = (*orphanGitReconcileAdapter)(nil)
)

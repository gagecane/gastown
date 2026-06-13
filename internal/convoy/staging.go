// Package convoy — staging DAG state machine.
//
// This file holds the convoy *staging* state machine extracted from
// internal/cmd/convoy_stage.go (gu-nid89.12.5). It is pure domain logic with
// no CLI, shell, or filesystem coupling: build a dependency DAG from raw bead
// data, detect cycles, schedule slingable tasks into execution waves via
// Kahn's algorithm, and decide the resulting convoy status. The CLI layer in
// internal/cmd keeps thin type-aliases and delegators so command wiring and
// the existing test suite call the same names.
package convoy

import (
	"fmt"
	"sort"
	"strings"
)

// ConvoyDAG represents an in-memory dependency graph for convoy staging.
type ConvoyDAG struct {
	Nodes map[string]*ConvoyDAGNode
}

// ConvoyDAGNode represents a single bead in the DAG.
type ConvoyDAGNode struct {
	ID        string
	Title     string
	Type      string // "epic", "task", "bug", etc.
	Status    string
	Rig       string
	BlockedBy []string // IDs of beads that block this one (execution edges)
	Blocks    []string // IDs of beads this one blocks
	Children  []string // parent-child children (hierarchy only, not execution)
	Parent    string   // parent-child parent
}

// DetectCycles checks the DAG for cycles in execution edges (blocks/conditional-blocks/waits-for).
// Returns the cycle path as []string if a cycle is found, or nil if acyclic.
// Only considers BlockedBy/Blocks edges (execution edges), NOT parent-child.
//
// Uses DFS with 3-color marking:
//   - white (0): unvisited
//   - gray  (1): on the current recursion stack
//   - black (2): fully explored
func DetectCycles(dag *ConvoyDAG) []string {
	const (
		white = 0
		gray  = 1
		black = 2
	)

	color := make(map[string]int)     // default zero = white
	parent := make(map[string]string) // tracks DFS parent for cycle extraction

	// Sort node IDs for deterministic traversal order.
	ids := make([]string, 0, len(dag.Nodes))
	for id := range dag.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	// extractCycle walks back from the back-edge target through the DFS
	// parent chain to reconstruct the cycle path.
	extractCycle := func(from, to string) []string {
		// from -> to is the back-edge. The cycle is: to -> ... -> from -> to.
		path := []string{to}
		cur := from
		for cur != to {
			path = append(path, cur)
			cur = parent[cur]
		}
		// Reverse so the cycle reads in traversal order.
		for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
			path[i], path[j] = path[j], path[i]
		}
		return path
	}

	var dfs func(id string) []string
	dfs = func(id string) []string {
		color[id] = gray
		node := dag.Nodes[id]
		if node == nil {
			color[id] = black
			return nil
		}

		// Sort neighbors for deterministic traversal.
		neighbors := make([]string, len(node.Blocks))
		copy(neighbors, node.Blocks)
		sort.Strings(neighbors)

		for _, next := range neighbors {
			switch color[next] {
			case white:
				parent[next] = id
				if cycle := dfs(next); cycle != nil {
					return cycle
				}
			case gray:
				// Back-edge found → cycle.
				return extractCycle(id, next)
			}
			// black → already fully explored, skip.
		}

		color[id] = black
		return nil
	}

	for _, id := range ids {
		if color[id] == white {
			if cycle := dfs(id); cycle != nil {
				return cycle
			}
		}
	}

	return nil
}

// Wave represents a group of tasks that can execute in parallel.
type Wave struct {
	Number int
	Tasks  []string // bead IDs, sorted for determinism
}

// GatedTask represents a slingable task that cannot be placed in any wave
// because it is blocked (directly or transitively) by an open non-slingable
// node such as a decision or epic.
type GatedTask struct {
	TaskID  string
	GatedBy []string // IDs of non-slingable open blockers (direct gates only)
}

// isInactiveStatus reports whether a bead status removes the bead from active
// wave scheduling. Closed and tombstone beads are already done; deferred beads
// are intentionally held (the dispatch path refuses them and the scheduler
// auto-releases them when the defer window expires). A slingable bead in any
// of these statuses occupies no wave slot, so deferring (or closing) a tracked
// bead reduces the recomputed wave count (gu-bvl8u).
func isInactiveStatus(status string) bool {
	switch status {
	case "closed", "tombstone", "deferred":
		return true
	default:
		return false
	}
}

// ComputeWaves assigns each slingable task to an execution wave using Kahn's
// algorithm. Wave 1 = tasks with no unsatisfied blocking deps within the
// staged set. Wave N+1 = tasks whose blockers are ALL in wave N or earlier.
// Epics and non-slingable types are excluded from wave task lists but their
// blocking edges ARE respected — a task blocked by a decision bead will not
// appear until that decision is resolved (fixes #2141). Parent-child deps do
// NOT create execution edges. Returns (waves, gatedTasks, error); gatedTasks
// lists tasks blocked by open non-slingable nodes that cannot be placed in any
// wave.
func ComputeWaves(dag *ConvoyDAG) ([]Wave, []GatedTask, error) {
	// Step 1: Filter to slingable types in an active status.
	// Inactive beads (closed, tombstone, deferred) are excluded from wave
	// slots: closed/tombstone are already done, and deferred work is
	// intentionally held. Excluding them here means deferring a tracked bead
	// is reflected in the recomputed waves — a deferred bead that was alone
	// in its wave drops that wave entirely (gu-bvl8u).
	slingable := make(map[string]*ConvoyDAGNode)
	for id, node := range dag.Nodes {
		if IsSlingableType(node.Type) && !isInactiveStatus(node.Status) {
			slingable[id] = node
		}
	}
	if len(slingable) == 0 {
		return nil, nil, fmt.Errorf("no slingable tasks in DAG (need task, bug, feature, or chore)")
	}

	// Step 2: Calculate in-degree for each slingable node.
	// Count slingable blockers (decremented by Kahn's) and open
	// non-slingable blockers (never decremented — act as gates).
	inDegree := make(map[string]int, len(slingable))
	for id, node := range slingable {
		deg := 0
		for _, blocker := range node.BlockedBy {
			if _, ok := slingable[blocker]; ok {
				deg++ // slingable blocker — handled by Kahn's
			} else if bNode, ok := dag.Nodes[blocker]; ok {
				if bNode.Status != "closed" && bNode.Status != "tombstone" {
					deg++ // non-slingable open blocker — gate
				}
			}
		}
		inDegree[id] = deg
	}

	// Step 3-6: Kahn's algorithm — peel off waves of in-degree-0 nodes.
	var waves []Wave
	processed := 0
	waveNum := 0

	for processed < len(slingable) {
		// Collect nodes with in-degree 0.
		var ready []string
		for id, deg := range inDegree {
			if deg == 0 {
				ready = append(ready, id)
			}
		}

		if len(ready) == 0 {
			// No cycles exist (DetectCycles ran before ComputeWaves),
			// so remaining tasks are gated by open non-slingable nodes
			// (decisions, epics, etc.) either directly or transitively.
			var gated []GatedTask
			for id := range inDegree {
				node := slingable[id]
				var gatedBy []string
				for _, blocker := range node.BlockedBy {
					if _, ok := slingable[blocker]; ok {
						continue
					}
					if bNode, ok := dag.Nodes[blocker]; ok {
						if bNode.Status != "closed" && bNode.Status != "tombstone" {
							gatedBy = append(gatedBy, blocker)
						}
					}
				}
				sort.Strings(gatedBy)
				gated = append(gated, GatedTask{TaskID: id, GatedBy: gatedBy})
			}
			sort.Slice(gated, func(i, j int) bool { return gated[i].TaskID < gated[j].TaskID })
			return waves, gated, nil
		}

		// Step 7: Sort within each wave for determinism.
		sort.Strings(ready)
		waveNum++

		waves = append(waves, Wave{
			Number: waveNum,
			Tasks:  ready,
		})

		// Remove processed nodes and decrement in-degrees of their dependents.
		for _, id := range ready {
			delete(inDegree, id)
			processed++

			// Decrement in-degree of nodes this one blocks (that are slingable).
			for _, blocked := range slingable[id].Blocks {
				if _, ok := inDegree[blocked]; ok {
					inDegree[blocked]--
				}
			}
		}
	}

	return waves, nil, nil
}

// BeadInfo represents raw bead data from bd show output.
type BeadInfo struct {
	ID     string
	Title  string
	Type   string // "epic", "task", "bug", etc.
	Status string
	Rig    string // resolved rig name
}

// DepInfo represents a raw dependency from bd dep list output.
type DepInfo struct {
	IssueID     string // the dependent bead
	DependsOnID string // the bead it depends on
	Type        string // "blocks", "parent-child", "waits-for", "conditional-blocks", "tracks", "related", etc.
}

// BuildConvoyDAG constructs a ConvoyDAG from raw bead and dependency data.
// Edge classification:
//   - blocks, conditional-blocks, waits-for → execution edges (BlockedBy/Blocks)
//   - parent-child → hierarchy metadata (Children/Parent), NOT execution edges
//   - related, tracks, discovered-from, etc. → ignored
func BuildConvoyDAG(beads []BeadInfo, deps []DepInfo) *ConvoyDAG {
	dag := &ConvoyDAG{Nodes: make(map[string]*ConvoyDAGNode)}

	// Create nodes from beads.
	for _, b := range beads {
		dag.Nodes[b.ID] = &ConvoyDAGNode{
			ID:     b.ID,
			Title:  b.Title,
			Type:   b.Type,
			Status: b.Status,
			Rig:    b.Rig,
		}
	}

	// Process deps.
	for _, d := range deps {
		from := dag.Nodes[d.DependsOnID] // the blocker
		to := dag.Nodes[d.IssueID]       // the blocked
		if from == nil || to == nil {
			continue // skip deps referencing beads not in our set
		}

		switch d.Type {
		case "blocks", "conditional-blocks", "waits-for", "merge-blocks":
			// Execution edges.
			from.Blocks = append(from.Blocks, to.ID)
			to.BlockedBy = append(to.BlockedBy, from.ID)
		case "parent-child":
			// Hierarchy only.
			from.Children = append(from.Children, to.ID)
			to.Parent = from.ID
		default:
			// related, tracks, discovered-from, etc. — ignored.
		}
	}

	return dag
}

// StagingFinding represents an error or warning found during convoy staging analysis.
type StagingFinding struct {
	Severity     string   // "error" or "warning"
	Category     string   // "cycle", "no-rig", "orphan", "blocked-rig", "cross-rig", "capacity", "missing-branch"
	BeadIDs      []string // affected bead IDs
	Message      string   // human-readable description
	SuggestedFix string   // actionable fix suggestion
}

// CategorizeFindings splits findings into errors and warnings by severity.
func CategorizeFindings(findings []StagingFinding) (errors, warnings []StagingFinding) {
	for _, f := range findings {
		switch f.Severity {
		case "error":
			errors = append(errors, f)
		default:
			warnings = append(warnings, f)
		}
	}
	return
}

// DetectErrors runs all error detection checks on the DAG.
// Returns findings with severity="error" for fatal issues.
func DetectErrors(dag *ConvoyDAG) []StagingFinding {
	var findings []StagingFinding

	// Check for cycles
	cyclePath := DetectCycles(dag)
	if cyclePath != nil {
		findings = append(findings, StagingFinding{
			Severity:     "error",
			Category:     "cycle",
			BeadIDs:      cyclePath,
			Message:      fmt.Sprintf("dependency cycle detected: %s", strings.Join(cyclePath, " → ")),
			SuggestedFix: fmt.Sprintf("remove one blocking dependency in the chain: %s", strings.Join(cyclePath, " → ")),
		})
	}

	// Check for beads with no valid rig
	for _, node := range dag.Nodes {
		if !IsSlingableType(node.Type) {
			continue // epics don't need rigs
		}
		if node.Rig == "" {
			findings = append(findings, StagingFinding{
				Severity:     "error",
				Category:     "no-rig",
				BeadIDs:      []string{node.ID},
				Message:      fmt.Sprintf("bead %s has no valid rig (prefix not mapped in routes.jsonl or resolves to empty)", node.ID),
				SuggestedFix: fmt.Sprintf("add a routes.jsonl entry mapping the prefix of %s to a rig, or check that the bead ID has a valid prefix", node.ID),
			})
		}
	}

	// Sort findings by bead ID for determinism
	sort.Slice(findings, func(i, j int) bool {
		if len(findings[i].BeadIDs) == 0 || len(findings[j].BeadIDs) == 0 {
			return findings[i].Category < findings[j].Category
		}
		return findings[i].BeadIDs[0] < findings[j].BeadIDs[0]
	})

	return findings
}

// ChooseStatus determines the convoy status based on analysis results.
// Returns "" if errors found (no convoy should be created).
func ChooseStatus(errors, warnings []StagingFinding) string {
	if len(errors) > 0 {
		return "" // no convoy
	}
	if len(warnings) > 0 {
		return "staged_warnings"
	}
	return "staged_ready"
}

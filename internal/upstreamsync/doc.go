// Package upstreamsync provides the state machine, bead model, and
// utilities for the upstream-sync feature.
//
// Upstream sync automatically merges upstream/main into the fork's
// origin/main, using polecat agents for conflict resolution and a full
// CI gate before push. This package owns the persistent state (pinned
// bead per rig) and the state machine transitions.
//
// The pinned state bead pattern follows internal/autotestpr/town_state.go:
// one bead per rig with structured JSON metadata, CAS-safe transitions,
// and bounded attempt history.
//
// Design context: .designs/cv-2s6tq/ (data.md, api.md, integration.md)
package upstreamsync

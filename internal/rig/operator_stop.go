// Package rig provides rig management functionality.
// This file tracks explicit operator intent to keep an agent (currently
// refinery) stopped so the daemon's auto-restart loop does not undo a
// manual `gt refinery stop`.
//
// Motivation: When mayor (or a human operator) runs `gt refinery stop` —
// for example, because git auth credentials are expired and the refinery
// would only fail and escalate — the daemon's next heartbeat would
// blindly restart it, which promptly re-fails and re-escalates. See
// gu-8ug1: SSH cert expiry produced loops of duplicate gt:escalation
// beads every few minutes per rig until a human ran `mwinit -o`.
//
// Solution: Persist the operator intent in the rig's wisp so
// ensureRefineryRunning can see it across daemon restarts and heartbeat
// cycles. wisp (not bead) is the right layer here because the state is
// local-operational and must not propagate via git to other machines.
package rig

import (
	"github.com/steveyegge/gastown/internal/wisp"
)

// WispRefineryStoppedKey is the wisp config key that records an explicit
// operator intent to keep the refinery stopped. When true, the daemon's
// auto-restart path skips this rig. `gt refinery start` and
// `gt refinery restart` clear it.
const WispRefineryStoppedKey = "refinery_stopped"

// SetRefineryStoppedByOperator records that the operator explicitly
// stopped the refinery for rigName and the daemon should not auto-restart
// it until the operator runs `gt refinery start` (or equivalent).
//
// Best-effort: errors are returned but callers typically log-and-continue
// since the underlying Stop() already succeeded — a failed flag write
// only means the daemon will eventually resurrect the refinery, which is
// no worse than the pre-fix behavior.
func SetRefineryStoppedByOperator(townRoot, rigName string) error {
	cfg := wisp.NewConfig(townRoot, rigName)
	return cfg.Set(WispRefineryStoppedKey, true)
}

// ClearRefineryStoppedByOperator removes the operator-stop flag. Called
// from `gt refinery start` and `gt refinery restart` so a subsequent
// `gt refinery stop` does not inherit stale intent from a prior session.
//
// Unsetting (rather than setting to false) keeps the wisp config tidy —
// the daemon's check already treats missing/false identically.
func ClearRefineryStoppedByOperator(townRoot, rigName string) error {
	cfg := wisp.NewConfig(townRoot, rigName)
	return cfg.Unset(WispRefineryStoppedKey)
}

// IsRefineryStoppedByOperator reports whether the operator has explicitly
// stopped the refinery for rigName. Returns false when the key is unset,
// not a bool, blocked, or the wisp config cannot be read — all of which
// are equivalent to "no explicit stop intent recorded" for the daemon's
// auto-restart decision.
func IsRefineryStoppedByOperator(townRoot, rigName string) bool {
	cfg := wisp.NewConfig(townRoot, rigName)
	return cfg.GetBool(WispRefineryStoppedKey)
}

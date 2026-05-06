// Package rig provides rig management functionality.
// This file centralizes parked/docked state checking used by both the cmd and
// daemon packages (resolves gu-03im / upstream #2120 — previously duplicated in
// cmd.IsRigParkedOrDocked, cmd.IsRigParked, cmd.hasRigBeadLabel, and
// daemon.isRigOperational).
package rig

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/wisp"
)

// Wisp and bead label constants for rig operational state.
// These are the canonical values used across the codebase.
const (
	// WispStatusKey is the wisp config key for rig operational status.
	WispStatusKey = "status"
	// WispStatusParked is the wisp status value indicating a rig is parked.
	WispStatusParked = "parked"
	// WispStatusDocked is the wisp status value indicating a rig is docked
	// (ephemeral docked signal; persistent docked state lives on bead labels).
	WispStatusDocked = "docked"

	// LabelStatusParked is the bead label indicating a rig is parked.
	LabelStatusParked = "status:parked"
	// LabelStatusDocked is the bead label indicating a rig is docked.
	LabelStatusDocked = "status:docked"
)

// IsRigParkedOrDocked reports whether a rig is parked or docked by any
// mechanism (wisp ephemeral state for parked, persistent bead labels for
// parked or docked). Returns (blocked, reason).
//
// This is the error-swallowing variant used by dispatch paths (sling,
// convoy launch, convoy stage, up, prime). When the rig bead cannot be
// read (e.g., missing config, Dolt unavailable), this returns (false, "")
// — the caller has chosen to proceed under the assumption the rig is
// operational. Callers that need fail-safe semantics should use
// IsRigParkedOrDockedE instead.
//
// Parked vs docked asymmetry: parked state is checked in both the wisp
// layer (ephemeral, set by "gt rig park") and bead labels (persistent
// fallback for when wisp state is lost during cleanup). Docked state is
// bead-label only because "gt rig dock" never writes to wisp — it persists
// exclusively via the rig identity bead's status:docked label.
func IsRigParkedOrDocked(townRoot, rigName string) (bool, string) {
	blocked, reason, _ := checkParkedOrDocked(townRoot, rigName)
	return blocked, reason
}

// IsRigParkedOrDockedE is the error-returning variant of IsRigParkedOrDocked.
// It surfaces the underlying error when the rig bead cannot be read, allowing
// callers to implement fail-safe semantics (e.g., the daemon refuses to
// auto-start agents when it cannot verify docked status to avoid wasting
// credits on a potentially-docked rig).
//
// Returns:
//   - (true, reason, nil)    — rig is parked or docked
//   - (false, "",   nil)     — rig is operational
//   - (false, "",   err)     — could not determine (caller decides)
func IsRigParkedOrDockedE(townRoot, rigName string) (bool, string, error) {
	return checkParkedOrDocked(townRoot, rigName)
}

// checkParkedOrDocked is the shared implementation behind both public
// variants. The returned error is non-nil only when the rig bead lookup
// failed in a way callers might want to treat as fail-safe (prefix
// missing or bead read error). Parked detection via wisp never produces
// an error — wisp is local filesystem state.
func checkParkedOrDocked(townRoot, rigName string) (bool, string, error) {
	// Layer 1: Wisp (fast, local, ephemeral). Only parked is checked here —
	// "gt rig dock" never writes to wisp. We still recognize wisp
	// status=docked because legacy callers (daemon.isRigOperational)
	// treated it as authoritative.
	wispCfg := wisp.NewConfig(townRoot, rigName)
	switch wispCfg.GetString(WispStatusKey) {
	case WispStatusParked:
		return true, "parked", nil
	case WispStatusDocked:
		return true, "docked", nil
	}

	// Layer 2: Persistent bead labels. Single bead lookup for both
	// parked and docked.
	rigPath := filepath.Join(townRoot, rigName)
	prefix := RigBeadsPrefix(townRoot, rigPath, rigName)
	if prefix == "" {
		// No prefix means no way to look up the bead. The error-swallowing
		// variant (used by dispatch) treats this as "not blocked" so
		// legitimate tests and isolated rigs still work. The daemon's
		// fail-safe variant surfaces this so it can refuse to start
		// agents when rig identity is unknown.
		return false, "", errRigPrefixMissing
	}

	beadsPath := filepath.Join(rigPath, "mayor", "rig")
	if _, err := os.Stat(beadsPath); err != nil {
		beadsPath = rigPath
	}

	bd := beads.New(beadsPath)
	rigBeadID := beads.RigBeadIDWithPrefix(prefix, rigName)
	rigBead, err := bd.Show(rigBeadID)
	if err != nil {
		// Distinguish "rig identity bead simply does not exist" from other
		// failures (Dolt unavailable, network timeout, malformed data).
		// A missing bead is a persistent state — the rig was never set up
		// with an identity bead, or the bead was deleted. No amount of
		// retrying will make it appear, so callers should not treat this
		// like a transient Dolt outage.
		if errors.Is(err, beads.ErrNotFound) {
			return false, "", ErrRigBeadNotFound
		}
		return false, "", err
	}

	for _, label := range rigBead.Labels {
		switch label {
		case LabelStatusParked:
			return true, "parked", nil
		case LabelStatusDocked:
			return true, "docked", nil
		}
	}

	return false, "", nil
}

// IsRigParked reports whether a rig is parked (wisp or bead layer).
// Docked rigs are NOT considered parked — callers that want both should
// use IsRigParkedOrDocked.
func IsRigParked(townRoot, rigName string) bool {
	// Wisp layer: only parked counts (not docked).
	wispCfg := wisp.NewConfig(townRoot, rigName)
	if wispCfg.GetString(WispStatusKey) == WispStatusParked {
		return true
	}
	return hasRigBeadLabel(townRoot, rigName, LabelStatusParked)
}

// IsRigDocked reports whether a rig is docked by checking the
// status:docked label on the rig identity bead.
func IsRigDocked(townRoot, rigName, prefix string) bool {
	rigPath := filepath.Join(townRoot, rigName)
	beadsPath := filepath.Join(rigPath, "mayor", "rig")
	if _, err := os.Stat(beadsPath); err != nil {
		beadsPath = rigPath
	}

	bd := beads.New(beadsPath)
	rigBeadID := beads.RigBeadIDWithPrefix(prefix, rigName)

	rigBead, err := bd.Show(rigBeadID)
	if err != nil {
		return false
	}
	for _, label := range rigBead.Labels {
		if label == LabelStatusDocked {
			return true
		}
	}
	return false
}

// hasRigBeadLabel reports whether the rig identity bead carries a specific
// label. Returns false if the rig config or bead can't be loaded
// (safe default for the error-swallowing callers).
func hasRigBeadLabel(townRoot, rigName, label string) bool {
	rigPath := filepath.Join(townRoot, rigName)
	prefix := RigBeadsPrefix(townRoot, rigPath, rigName)
	if prefix == "" {
		return false
	}

	beadsPath := filepath.Join(rigPath, "mayor", "rig")
	if _, err := os.Stat(beadsPath); err != nil {
		beadsPath = rigPath
	}

	bd := beads.New(beadsPath)
	rigBeadID := beads.RigBeadIDWithPrefix(prefix, rigName)

	rigBead, err := bd.Show(rigBeadID)
	if err != nil {
		return false
	}
	for _, l := range rigBead.Labels {
		if l == label {
			return true
		}
	}
	return false
}

// RigBeadsPrefix resolves a rig's beads prefix, preferring the town-level
// registry (mayor/rigs.json) and falling back to the rig's own config.json
// for isolated/test scenarios. Returns "" if neither source yields a prefix.
func RigBeadsPrefix(townRoot, rigPath, rigName string) string {
	rigsConfigPath := constants.MayorRigsPath(townRoot)
	if rigsConfig, err := config.LoadRigsConfig(rigsConfigPath); err == nil {
		if entry, ok := rigsConfig.Rigs[rigName]; ok && entry.BeadsConfig != nil && entry.BeadsConfig.Prefix != "" {
			return entry.BeadsConfig.Prefix
		}
	}

	rigConfigPath := filepath.Join(rigPath, "config.json")
	if rigCfg, err := config.LoadRigConfig(rigConfigPath); err == nil && rigCfg.Beads != nil && rigCfg.Beads.Prefix != "" {
		return rigCfg.Beads.Prefix
	}
	return ""
}

// errRigPrefixMissing is returned by the error variant when the rig's
// beads prefix cannot be resolved. Callers needing fail-safe semantics
// (daemon) treat this as "cannot verify — assume not operational".
var errRigPrefixMissing = &rigPrefixError{}

type rigPrefixError struct{}

func (*rigPrefixError) Error() string {
	return "rig beads prefix not found (cannot locate rig identity bead)"
}

// ErrRigBeadNotFound is returned by IsRigParkedOrDockedE when the rig's
// identity bead does not exist in the beads database. This is a persistent
// state (the rig was never set up with an identity bead, or it was deleted),
// not a transient failure. Callers should handle it separately from
// "cannot verify" errors: since no identity bead can carry status labels,
// no rig without a bead can be parked or docked via the bead layer.
//
// The daemon uses this to distinguish persistent "no bead" state (log once,
// treat as not-blocking) from transient Dolt failures (log and fail-safe to
// not-operational). Without this distinction, missing rig beads produce
// per-heartbeat warning spam (see gu-resv).
var ErrRigBeadNotFound = errors.New("rig identity bead not found")

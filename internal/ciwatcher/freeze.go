package ciwatcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FreezeFile holds the metadata persisted alongside an active freeze. The
// refinery checks for the file's existence; the contents are advisory and
// human-readable so an operator can `cat` the file to see why the queue is
// stopped without having to crawl mail or beads.
type FreezeFile struct {
	// Rig is the rig name whose merge queue is frozen.
	Rig string `json:"rig"`

	// FrozenAt is the wall-clock time at which the freeze was first written.
	FrozenAt time.Time `json:"frozen_at"`

	// Reason is a short human-readable explanation. For ciwatcher-triggered
	// freezes this is "broke-main-ci: <bead>".
	Reason string `json:"reason"`

	// BeadID is the bead the watcher reopened; empty if the failed commit
	// could not be attributed to a bead.
	BeadID string `json:"bead_id,omitempty"`

	// CommitSHA is the SHA on main whose CI failed.
	CommitSHA string `json:"commit_sha,omitempty"`

	// RunID is the CI run identifier (host-specific; for GitHub Actions it
	// is the numeric run ID rendered as a string).
	RunID string `json:"run_id,omitempty"`

	// RunURL is a clickable URL to the failed run, when available.
	RunURL string `json:"run_url,omitempty"`
}

// freezeRelPath returns the file name (relative to the runtime directory) for
// the rig's freeze flag. The rig name is used verbatim — Gas Town rig names
// are filesystem-safe by construction (no slashes, no spaces).
func freezeRelPath(rig string) string {
	return "mq-frozen-" + rig
}

// FreezePath returns the absolute path to the freeze file for `rig` under the
// town's runtime directory. The townRoot/.runtime/ directory itself is
// expected to exist (it is provisioned by `gt boot`); callers MAY pre-create
// it but Write does so defensively.
func FreezePath(townRoot, rig string) string {
	return filepath.Join(townRoot, ".runtime", freezeRelPath(rig))
}

// IsFrozen reports whether a freeze flag is present for `rig`. Any error
// other than os.IsNotExist is treated as "not frozen" with the error
// returned, so callers can decide whether to fail open or fail closed. The
// refinery fails closed (treats non-IsNotExist errors as freeze) — see
// internal/refinery/freeze_guard.go.
func IsFrozen(townRoot, rig string) (bool, error) {
	_, err := os.Stat(FreezePath(townRoot, rig))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// ReadFreeze decodes the freeze metadata for `rig`. Returns (nil, nil) when
// the file does not exist, mirroring IsFrozen's friendly default. A malformed
// file returns the decode error so an operator can investigate rather than
// silently treating a corrupt freeze as "no freeze".
func ReadFreeze(townRoot, rig string) (*FreezeFile, error) {
	path := FreezePath(townRoot, rig)
	data, err := os.ReadFile(path) //nolint:gosec // path is composed from operator-controlled rig name
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var ff FreezeFile
	if err := json.Unmarshal(data, &ff); err != nil {
		return nil, fmt.Errorf("ciwatcher: parse freeze file %s: %w", path, err)
	}
	return &ff, nil
}

// WriteFreeze atomically persists the freeze flag. Existing flags are
// overwritten so a later failure replaces the earlier metadata — the freeze
// is tracking "the queue is stopped right now", not a history; the bead and
// the structured event log carry the per-incident audit trail.
//
// Atomicity: write to a temp file in the same directory, then os.Rename. On
// platforms where Rename across files is atomic (Linux/macOS local FS) this
// guarantees a reader never sees a half-written file.
func WriteFreeze(townRoot string, ff FreezeFile) error {
	if ff.Rig == "" {
		return errors.New("ciwatcher: WriteFreeze: Rig is required")
	}
	if ff.FrozenAt.IsZero() {
		ff.FrozenAt = time.Now().UTC()
	}
	dir := filepath.Join(townRoot, ".runtime")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ciwatcher: mkdir %s: %w", dir, err)
	}
	final := FreezePath(townRoot, ff.Rig)
	tmp, err := os.CreateTemp(dir, freezeRelPath(ff.Rig)+".*.tmp")
	if err != nil {
		return fmt.Errorf("ciwatcher: temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	encoded, err := json.MarshalIndent(ff, "", "  ")
	if err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("ciwatcher: encode freeze: %w", err)
	}
	if _, err := tmp.Write(append(encoded, '\n')); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("ciwatcher: write freeze: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("ciwatcher: close temp: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		cleanup()
		return fmt.Errorf("ciwatcher: rename %s -> %s: %w", tmpName, final, err)
	}
	return nil
}

// ClearFreeze removes the freeze flag for `rig`. Idempotent: missing file is
// treated as success, since the goal ("no freeze on disk") is already met.
func ClearFreeze(townRoot, rig string) error {
	err := os.Remove(FreezePath(townRoot, rig))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("ciwatcher: clear freeze for %s: %w", rig, err)
}

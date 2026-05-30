package ciwatcher

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SeenRunsCap bounds the number of run IDs retained in the ledger. The watcher
// only ever needs to know "have I seen this recent run?" — we never look more
// than a few hours back. The cap keeps the file small (a few KB) under a
// chatty CI configuration.
const SeenRunsCap = 500

// seenRunsRelPath returns the file name for a rig's seen-runs ledger.
func seenRunsRelPath(rig string) string {
	return "ci-watcher-seen-" + rig
}

// SeenRunsPath returns the absolute path to the seen-runs ledger.
func SeenRunsPath(townRoot, rig string) string {
	return filepath.Join(townRoot, ".runtime", seenRunsRelPath(rig))
}

// SeenRuns is the on-disk ledger of run IDs the watcher has already
// processed. Format: one "<runID>\t<RFC3339-stamp>\n" per line. The stamp
// is informational; ordering and dedup is by runID.
type SeenRuns struct {
	townRoot string
	rig      string
	cache    map[string]time.Time // runID -> recorded-at
	fresh    bool                 // true when no ledger existed at load (cold start)
}

// LoadSeenRuns opens (or initializes) the ledger. A missing file is
// equivalent to an empty ledger.
func LoadSeenRuns(townRoot, rig string) (*SeenRuns, error) {
	if rig == "" {
		return nil, errors.New("ciwatcher: LoadSeenRuns: rig is required")
	}
	s := &SeenRuns{
		townRoot: townRoot,
		rig:      rig,
		cache:    map[string]time.Time{},
	}
	f, err := os.Open(SeenRunsPath(townRoot, rig)) //nolint:gosec // operator-controlled rig name
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.fresh = true
			return s, nil
		}
		return nil, fmt.Errorf("ciwatcher: open seen-runs: %w", err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		var ts time.Time
		if len(parts) == 2 {
			if t, err := time.Parse(time.RFC3339, parts[1]); err == nil {
				ts = t
			}
		}
		s.cache[parts[0]] = ts
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ciwatcher: read seen-runs: %w", err)
	}
	return s, nil
}

// Fresh reports whether the ledger had no backing file at load time — i.e.
// this is the watcher's first-ever poll for the rig (a cold start). Callers
// use this to bound which historical runs are eligible for escalation, so a
// fresh daemon doesn't re-escalate every past CI failure.
func (s *SeenRuns) Fresh() bool {
	if s == nil {
		return false
	}
	return s.fresh
}

// Has reports whether `runID` has already been processed.
func (s *SeenRuns) Has(runID string) bool {
	if s == nil {
		return false
	}
	_, ok := s.cache[runID]
	return ok
}

// Mark records `runID` as processed at the given time. Caller is expected to
// invoke Save() afterwards (we keep the in-memory and on-disk operations
// separate so a single Save can flush a batch).
func (s *SeenRuns) Mark(runID string, at time.Time) {
	if s == nil || runID == "" {
		return
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	s.cache[runID] = at
}

// Save flushes the ledger to disk, trimming to SeenRunsCap entries by
// retaining the most-recently-recorded ones. Atomic via temp-file + rename.
func (s *SeenRuns) Save() error {
	if s == nil {
		return nil
	}
	dir := filepath.Join(s.townRoot, ".runtime")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ciwatcher: mkdir seen-runs: %w", err)
	}

	type entry struct {
		id string
		at time.Time
	}
	entries := make([]entry, 0, len(s.cache))
	for id, t := range s.cache {
		entries = append(entries, entry{id: id, at: t})
	}
	// Sort newest-first so trim drops the oldest.
	sort.Slice(entries, func(i, j int) bool { return entries[i].at.After(entries[j].at) })
	if len(entries) > SeenRunsCap {
		entries = entries[:SeenRunsCap]
	}

	tmp, err := os.CreateTemp(dir, seenRunsRelPath(s.rig)+".*.tmp")
	if err != nil {
		return fmt.Errorf("ciwatcher: temp seen-runs: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	w := bufio.NewWriter(tmp)
	for _, e := range entries {
		stamp := e.at
		if stamp.IsZero() {
			stamp = time.Now().UTC()
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\n", e.id, stamp.UTC().Format(time.RFC3339)); err != nil {
			_ = tmp.Close()
			cleanup()
			return fmt.Errorf("ciwatcher: write seen-runs: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("ciwatcher: flush seen-runs: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("ciwatcher: close seen-runs: %w", err)
	}
	final := SeenRunsPath(s.townRoot, s.rig)
	if err := os.Rename(tmpName, final); err != nil {
		cleanup()
		return fmt.Errorf("ciwatcher: rename seen-runs: %w", err)
	}
	return nil
}

// Len reports how many run IDs are currently tracked. Test-only convenience.
func (s *SeenRuns) Len() int { return len(s.cache) }

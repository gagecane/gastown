package prwatcher

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

// ActionedCap bounds the number of comment IDs retained in the ledger. The
// watcher only needs to answer "have I actioned this comment?" for comments on
// currently-open PRs; once a PR merges its comments stop appearing in the
// fetch. The cap keeps the file small under a chatty review configuration.
const ActionedCap = 2000

// actionedRelPath returns the file name for a rig's actioned-comments ledger.
func actionedRelPath(rig string) string {
	return "pr-watcher-actioned-" + rig
}

// ActionedPath returns the absolute path to the actioned-comments ledger.
func ActionedPath(townRoot, rig string) string {
	return filepath.Join(townRoot, ".runtime", actionedRelPath(rig))
}

// Actioned is the on-disk ledger of comment IDs the watcher has already
// actioned. Format: one "<commentID>\t<RFC3339-stamp>\n" per line. The stamp is
// informational; dedup is by commentID. Comment IDs may themselves contain no
// tab (GitHub node IDs are base64) so the split is safe.
type Actioned struct {
	townRoot string
	rig      string
	cache    map[string]time.Time // commentID -> recorded-at
	fresh    bool                 // true when no ledger existed at load (cold start)
}

// LoadActioned opens (or initializes) the ledger. A missing file is equivalent
// to an empty ledger and flags a cold start.
func LoadActioned(townRoot, rig string) (*Actioned, error) {
	if rig == "" {
		return nil, errors.New("prwatcher: LoadActioned: rig is required")
	}
	a := &Actioned{
		townRoot: townRoot,
		rig:      rig,
		cache:    map[string]time.Time{},
	}
	f, err := os.Open(ActionedPath(townRoot, rig)) //nolint:gosec // operator-controlled rig name
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			a.fresh = true
			return a, nil
		}
		return nil, fmt.Errorf("prwatcher: open actioned: %w", err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	// Comment bodies aren't stored here, but node IDs are short; the default
	// scanner buffer is plenty.
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
		a.cache[parts[0]] = ts
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("prwatcher: read actioned: %w", err)
	}
	return a, nil
}

// Fresh reports whether the ledger had no backing file at load time — i.e. this
// is the watcher's first-ever poll for the rig. Callers use this to bound which
// comments are eligible for dispatch, so a fresh poller doesn't dispatch a
// backlog of pre-existing review comments.
func (a *Actioned) Fresh() bool {
	if a == nil {
		return false
	}
	return a.fresh
}

// Has reports whether `commentID` has already been actioned.
func (a *Actioned) Has(commentID string) bool {
	if a == nil {
		return false
	}
	_, ok := a.cache[commentID]
	return ok
}

// Mark records `commentID` as actioned at the given time. Caller is expected to
// invoke Save() afterwards.
func (a *Actioned) Mark(commentID string, at time.Time) {
	if a == nil || commentID == "" {
		return
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	a.cache[commentID] = at
}

// Save flushes the ledger to disk, trimming to ActionedCap entries by retaining
// the most-recently-recorded ones. Atomic via temp-file + rename.
func (a *Actioned) Save() error {
	if a == nil {
		return nil
	}
	dir := filepath.Join(a.townRoot, ".runtime")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("prwatcher: mkdir actioned: %w", err)
	}

	type entry struct {
		id string
		at time.Time
	}
	entries := make([]entry, 0, len(a.cache))
	for id, t := range a.cache {
		entries = append(entries, entry{id: id, at: t})
	}
	// Sort newest-first so trim drops the oldest.
	sort.Slice(entries, func(i, j int) bool { return entries[i].at.After(entries[j].at) })
	if len(entries) > ActionedCap {
		entries = entries[:ActionedCap]
	}

	tmp, err := os.CreateTemp(dir, actionedRelPath(a.rig)+".*.tmp")
	if err != nil {
		return fmt.Errorf("prwatcher: temp actioned: %w", err)
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
			return fmt.Errorf("prwatcher: write actioned: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("prwatcher: flush actioned: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("prwatcher: close actioned: %w", err)
	}
	final := ActionedPath(a.townRoot, a.rig)
	if err := os.Rename(tmpName, final); err != nil {
		cleanup()
		return fmt.Errorf("prwatcher: rename actioned: %w", err)
	}
	return nil
}

// Len reports how many comment IDs are currently tracked. Test-only convenience.
func (a *Actioned) Len() int { return len(a.cache) }

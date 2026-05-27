// Package testpathmap implements Layer 2 part B of the auto-dispatch
// wrong-rig feedback loop (gu-yblg follow-up to gu-ai0j / gu-mhfs).
//
// Layer 1 (gu-mhfs) introduced the wrong-rig:<rig> label so that sling /
// auto-dispatch refuses to re-route a bead to a rig that has already been
// flagged as wrong. Layer 2 part A (gu-ai0j) auto-attaches that label
// when a polecat closes a bead with a "wrong-rig" reason.
//
// Layer 2 part B — this package — closes the loop in the opposite
// direction. Instead of (only) blocking the wrong rig, we *learn the
// right rig* for a given test path and consult that learned mapping
// before any rig-guessing heuristic.
//
// Concretely:
//
//   - When a polecat closes a bead with a "wrong-rig" reason and the
//     bead description identifies one or more test paths, callers
//     invoke Record(testPath, correctRig) to remember the mapping.
//
//   - When the failure classifier (or any future routing site) sees a
//     failure on a known test path, it calls Lookup(testPath) and routes
//     the resulting bead directly to the corrected rig — without
//     re-running the path-based heuristic that mis-routed it the first
//     time.
//
// Storage: a single JSON document at $TownRoot/.gt/test-path-rig-map.json.
// File-locked for cross-process safety. Lazy decay: entries older than
// the configured TTL (30 days by default) are filtered at lookup time
// and compacted on the next Save.
//
// Schema is versioned (currently version 1) so future changes can
// migrate gracefully — if the on-disk version is unrecognized the
// store is treated as empty rather than corrupting the user's data.
package testpathmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/lock"
)

// SchemaVersion is the on-disk schema version. Bump when the JSON
// shape changes in a way old readers cannot handle.
const SchemaVersion = 1

// DefaultTTL is the default time-to-live for entries. Entries older
// than this are filtered at Lookup() and compacted on Save(). Tuned
// to roughly one month so genuine code re-organizations eventually
// re-converge on the new ownership.
const DefaultTTL = 30 * 24 * time.Hour

// relativeMapPath is the path of the map file relative to the town root.
const relativeMapPath = ".gt/test-path-rig-map.json"

// Entry records a single test-path → owning-rig correction.
type Entry struct {
	// TargetRig is the rig that the corrected close reason identified
	// as the correct owner for this test path.
	TargetRig string `json:"target_rig"`

	// LastCorrectedAt is the most recent time Record() was called for
	// this test path. Used for decay.
	LastCorrectedAt time.Time `json:"last_corrected_at"`

	// CorrectionCount is how many times this mapping has been recorded.
	// Useful as a confidence signal: count >= 2 means independent
	// polecats agreed on the same target rig.
	CorrectionCount int `json:"correction_count"`
}

// document is the JSON document persisted to disk.
type document struct {
	Version int              `json:"version"`
	Entries map[string]Entry `json:"entries"`
}

// Store is a file-backed test-path → owning-rig map. Methods are
// safe for use across processes via flock around the underlying file.
type Store struct {
	path string
	ttl  time.Duration
	now  func() time.Time // injectable for tests
}

// New returns a Store rooted at townRoot. The map file is at
// $townRoot/.gt/test-path-rig-map.json. If townRoot is empty an
// error is returned — callers must resolve the workspace first.
func New(townRoot string) (*Store, error) {
	if strings.TrimSpace(townRoot) == "" {
		return nil, errors.New("testpathmap.New: townRoot must not be empty")
	}
	return &Store{
		path: filepath.Join(townRoot, relativeMapPath),
		ttl:  DefaultTTL,
		now:  time.Now,
	}, nil
}

// WithTTL returns a copy of the Store with the given decay TTL.
// Useful for tests and for callers that want a non-default policy.
func (s *Store) WithTTL(ttl time.Duration) *Store {
	cp := *s
	cp.ttl = ttl
	return &cp
}

// withClock returns a copy of the Store with a custom clock. Test-only.
func (s *Store) withClock(now func() time.Time) *Store {
	cp := *s
	cp.now = now
	return &cp
}

// Path returns the absolute path of the on-disk JSON file. Exported for
// observability (operators sometimes want to peek at the learned map).
func (s *Store) Path() string {
	return s.path
}

// Lookup returns the corrected owning rig for testPath, or "" if no
// mapping exists or the entry has expired. Expired entries are not
// removed by Lookup itself — the next Save() will compact them.
//
// Lookup acquires a shared flock to allow concurrent readers without
// races against an in-flight Record().
func (s *Store) Lookup(testPath string) (string, bool, error) {
	doc, err := s.read()
	if err != nil {
		return "", false, err
	}
	entry, ok := doc.Entries[testPath]
	if !ok {
		return "", false, nil
	}
	if s.expired(entry) {
		return "", false, nil
	}
	return entry.TargetRig, true, nil
}

// Record adds or refreshes a (testPath → correctRig) mapping.
//
// If an entry already exists with the same TargetRig, CorrectionCount
// is incremented (high-confidence signal). If the existing entry points
// to a *different* rig, this Record overwrites it — the most recent
// correction wins, since stale mappings should yield to fresh ones.
//
// Empty inputs are silently dropped (Record returns nil) — callers may
// not always have both fields, and dropping a malformed update is
// preferable to corrupting the store.
//
// Record acquires an exclusive flock for the duration of the
// read-modify-write to keep concurrent recorders from clobbering
// each other.
func (s *Store) Record(testPath, correctRig string) error {
	testPath = strings.TrimSpace(testPath)
	correctRig = strings.TrimSpace(correctRig)
	if testPath == "" || correctRig == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return fmt.Errorf("testpathmap.Record: ensuring dir: %w", err)
	}

	unlock, err := lock.FlockAcquire(s.path + ".flock")
	if err != nil {
		return fmt.Errorf("testpathmap.Record: flock: %w", err)
	}
	defer unlock()

	doc, err := s.readLocked()
	if err != nil {
		return err
	}

	now := s.now()
	prev, ok := doc.Entries[testPath]
	switch {
	case !ok:
		doc.Entries[testPath] = Entry{
			TargetRig:       correctRig,
			LastCorrectedAt: now,
			CorrectionCount: 1,
		}
	case prev.TargetRig == correctRig:
		prev.LastCorrectedAt = now
		prev.CorrectionCount++
		doc.Entries[testPath] = prev
	default:
		// Different rig: most recent correction wins, reset count.
		doc.Entries[testPath] = Entry{
			TargetRig:       correctRig,
			LastCorrectedAt: now,
			CorrectionCount: 1,
		}
	}

	// Compact expired entries on every write to keep the file small
	// and avoid unbounded growth.
	s.compact(doc)

	return s.writeLocked(doc)
}

// Compact drops expired entries and rewrites the file. Intended for
// occasional housekeeping (e.g., from a daemon patrol). Lookup and
// Record do not require an explicit Compact since Record always
// compacts before writing.
func (s *Store) Compact() (removed int, err error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return 0, fmt.Errorf("testpathmap.Compact: ensuring dir: %w", err)
	}

	unlock, err := lock.FlockAcquire(s.path + ".flock")
	if err != nil {
		return 0, fmt.Errorf("testpathmap.Compact: flock: %w", err)
	}
	defer unlock()

	doc, err := s.readLocked()
	if err != nil {
		return 0, err
	}
	before := len(doc.Entries)
	s.compact(doc)
	after := len(doc.Entries)
	removed = before - after
	if removed == 0 {
		return 0, nil
	}
	return removed, s.writeLocked(doc)
}

// Snapshot returns a copy of all live (non-expired) entries. Useful
// for diagnostics, inspection, and tests. Order of iteration is not
// guaranteed.
func (s *Store) Snapshot() (map[string]Entry, error) {
	doc, err := s.read()
	if err != nil {
		return nil, err
	}
	out := make(map[string]Entry, len(doc.Entries))
	for k, v := range doc.Entries {
		if s.expired(v) {
			continue
		}
		out[k] = v
	}
	return out, nil
}

// expired reports whether entry should be filtered out at read time
// or compacted at write time.
func (s *Store) expired(entry Entry) bool {
	if s.ttl <= 0 {
		return false
	}
	return s.now().Sub(entry.LastCorrectedAt) > s.ttl
}

// compact mutates doc in place, dropping expired entries.
func (s *Store) compact(doc *document) {
	if s.ttl <= 0 {
		return
	}
	for k, v := range doc.Entries {
		if s.expired(v) {
			delete(doc.Entries, k)
		}
	}
}

// read returns the on-disk document, applying its own short-lived
// flock for safety. An absent or unrecognized-version file yields an
// empty document — never an error — so first-use is seamless.
func (s *Store) read() (*document, error) {
	// FlockAcquire creates the lock file at $path.flock; the parent
	// directory must exist or that open() fails. On first-use the
	// .gt dir may not yet exist, so ensure it before locking. This
	// keeps Lookup/Snapshot tolerant of a never-written store.
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return nil, fmt.Errorf("testpathmap.read: ensuring dir: %w", err)
	}
	unlock, err := lock.FlockAcquire(s.path + ".flock")
	if err != nil {
		// Falling back to an empty doc on flock failure would silently
		// hide bugs; surface the error to the caller instead.
		return nil, fmt.Errorf("testpathmap.read: flock: %w", err)
	}
	defer unlock()
	return s.readLocked()
}

// readLocked is the read body that assumes the caller already holds
// the flock. Returns an empty (non-nil) doc on missing file, malformed
// JSON, or unknown schema version — all of which are recoverable
// situations on first use or after a corruption incident.
func (s *Store) readLocked() (*document, error) {
	doc := &document{Version: SchemaVersion, Entries: map[string]Entry{}}

	data, err := os.ReadFile(s.path) //nolint:gosec // G304: path derived from town root
	if err != nil {
		if os.IsNotExist(err) {
			return doc, nil
		}
		return nil, fmt.Errorf("testpathmap.read: %w", err)
	}
	if len(data) == 0 {
		return doc, nil
	}

	var onDisk document
	if err := json.Unmarshal(data, &onDisk); err != nil {
		// Malformed file: treat as empty rather than failing every
		// call. The next Record() will overwrite it cleanly.
		return doc, nil
	}
	if onDisk.Version != SchemaVersion {
		// Unknown future version (or 0 from never-saved): treat as
		// empty rather than risk misinterpreting fields.
		return doc, nil
	}
	if onDisk.Entries == nil {
		onDisk.Entries = map[string]Entry{}
	}
	return &onDisk, nil
}

// writeLocked persists doc atomically. Caller must hold the flock.
func (s *Store) writeLocked(doc *document) error {
	doc.Version = SchemaVersion
	if doc.Entries == nil {
		doc.Entries = map[string]Entry{}
	}
	if err := atomicfile.EnsureDirAndWriteJSON(s.path, doc); err != nil {
		return fmt.Errorf("testpathmap.write: %w", err)
	}
	return nil
}

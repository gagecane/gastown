package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// CircuitBreakLogFile is the town-relative path of the circuit-break log.
// It records every sling-context that the scheduler closes as "circuit-broken"
// (maxDispatchFailures consecutive dispatch failures). Circuit-break state is
// otherwise per-sling-context (SlingContextFields.DispatchFailures) and is
// destroyed when the context bead closes — there is no town-wide aggregate.
// This append-only log gives a daemon patrol (circuit_break_dog) a data source
// to detect the "repeated circuit-breaks on the same bead/context" signature
// that single-context state cannot surface (gu-ixo67, motivated by gu-r8b0q —
// epics re-dispatched every cycle, circuit-breaking each run).
//
// Lives under .runtime so it is wiped with other ephemeral state and never
// committed to git.
const CircuitBreakLogFile = ".runtime/scheduler-circuit-breaks.jsonl"

// CircuitBreakRecord is one circuit-break event. A work bead can appear
// multiple times — once per distinct sling-context that broke for it — which
// is exactly the repeated-failure signature the monitor watches for.
type CircuitBreakRecord struct {
	Timestamp   string `json:"ts"`                     // RFC3339 UTC
	WorkBeadID  string `json:"work_bead_id"`           // the bead that kept failing to dispatch
	ContextID   string `json:"context_id"`             // the sling-context that broke
	TargetRig   string `json:"target_rig,omitempty"`   // destination rig, if known
	LastFailure string `json:"last_failure,omitempty"` // the final dispatch error
}

// circuitBreakLogPath returns the absolute path of the circuit-break log.
func circuitBreakLogPath(townRoot string) string {
	return filepath.Join(townRoot, CircuitBreakLogFile)
}

// LogCircuitBreak appends a circuit-break record to the town-wide log.
// Best-effort and concurrency-safe (multiple gt dispatch processes may write):
// uses a cross-process flock, mirroring events.write. A nil/empty townRoot or
// any I/O error is returned to the caller, which should treat logging as
// non-fatal — a missed record only delays detection by one episode.
func LogCircuitBreak(townRoot string, rec CircuitBreakRecord) error {
	if townRoot == "" {
		return nil
	}
	if rec.Timestamp == "" {
		rec.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	path := circuitBreakLogPath(townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating circuit-break log dir: %w", err)
	}

	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshaling circuit-break record: %w", err)
	}
	data = append(data, '\n')

	fl := flock.New(path + ".lock")
	if err := fl.Lock(); err != nil {
		return fmt.Errorf("acquiring circuit-break log lock: %w", err)
	}
	defer fl.Unlock() //nolint:errcheck // best-effort unlock

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644) //nolint:gosec // G302: operational log, non-sensitive
	if err != nil {
		return fmt.Errorf("opening circuit-break log: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing circuit-break record: %w", err)
	}
	return f.Close()
}

// ReadCircuitBreaks returns all circuit-break records within retention of now,
// and rewrites the log with only those records — pruning anything older in the
// same locked pass so the append-only file stays bounded. A retention <= 0
// disables pruning (returns all records, rewrites nothing).
//
// Records with an unparseable timestamp are kept (fail-open): dropping them
// could silently discard a real signal, and they age out once the file is
// rewritten with a valid timestamp boundary anyway.
func ReadCircuitBreaks(townRoot string, retention time.Duration) ([]CircuitBreakRecord, error) {
	if townRoot == "" {
		return nil, nil
	}
	path := circuitBreakLogPath(townRoot)

	// Ensure the directory exists so the lock file can be created even before
	// the first write (e.g. the dog polls before any break has been logged).
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("creating circuit-break log dir: %w", err)
	}

	fl := flock.New(path + ".lock")
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("acquiring circuit-break log lock: %w", err)
	}
	defer fl.Unlock() //nolint:errcheck // best-effort unlock

	f, err := os.Open(path) //nolint:gosec // G304: path derived from trusted townRoot
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no breaks logged yet
		}
		return nil, fmt.Errorf("opening circuit-break log: %w", err)
	}

	cutoff := time.Now().UTC().Add(-retention)
	var kept []CircuitBreakRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec CircuitBreakRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // skip corrupt line
		}
		if retention > 0 {
			t, perr := time.Parse(time.RFC3339, rec.Timestamp)
			if perr == nil && t.Before(cutoff) {
				continue // aged out — prune
			}
		}
		kept = append(kept, rec)
	}
	scanErr := scanner.Err()
	_ = f.Close()
	if scanErr != nil {
		return nil, fmt.Errorf("scanning circuit-break log: %w", scanErr)
	}

	// Rewrite the file with only the retained records (prune). Only when
	// retention is enabled — a read-only caller passes retention<=0.
	if retention > 0 {
		if err := rewriteCircuitBreaks(path, kept); err != nil {
			// Pruning is best-effort: return the records we read regardless.
			return kept, nil //nolint:nilerr // prune failure must not lose the read
		}
	}
	return kept, nil
}

// rewriteCircuitBreaks atomically replaces the log with the given records.
// Caller must hold the flock.
func rewriteCircuitBreaks(path string, recs []CircuitBreakRecord) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644) //nolint:gosec // G302: operational log
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, rec := range recs {
		data, err := json.Marshal(rec)
		if err != nil {
			_ = f.Close()
			return err
		}
		if _, err := w.Write(append(data, '\n')); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

package pushlog

// This file adds a durable, append-only record of FAILED pushes, the
// symmetric counterpart of the successful-push Receipt log in pushlog.go.
//
// Motivation (gu-7m9h9): genuine push-infra flakiness still strands work at
// low/stable connection load even with gu-1or22's retry-backoff deployed. The
// blocking diagnostic gap is that the actual `gt done` push error is NOT
// centrally recorded — it happens in the polecat's `gt done` session, which
// then dies. daemon.log only shows convoy re-sling attempts on the
// already-hooked bead, never the root push error. The existing failure path
// records the error in two places, both lossy in a terminating session:
//
//   1. stderr — gone the moment the session exits.
//   2. fileStrandedPushWisp -> a Dolt-backed bead Create. A Dolt write from a
//      session that is about to self-terminate can itself fail silently (the
//      exact silent-strand class gs-onu/gs-9sr fight), so the very record meant
//      to make a strand loud can be lost with the session.
//
// A local, fsync'd, Dolt-independent JSONL file under the rig's .runtime dir
// survives session death without depending on the network or Dolt. The next
// strand investigation can then read the real error instead of inferring it.
//
// Storage: <townRoot>/<rigName>/.runtime/push-failures.jsonl
//
// Writes are best-effort and MUST never block or fail the surrounding push
// path: a logging failure is itself logged to stderr and otherwise swallowed.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FailureFilename is the name of the push-failure log within a rig's
// .runtime dir.
const FailureFilename = "push-failures.jsonl"

// Stage identifies which step of the push path produced a failure. Stable
// strings — persisted in the log and inspected by forensic tooling.
const (
	// StagePush is a failure of the git push itself (the refspec push to
	// origin reported a non-nil error after all retries/fallbacks).
	StagePush = "push"

	// StageVerify is a push that reported success but whose post-push
	// remote-tip verification could not confirm the commit landed.
	StageVerify = "verify"
)

// Failure is a single durable record of a failed (or unverified) push. The
// shape mirrors Receipt so forensic tooling can correlate the two logs by
// branch/sha/issue.
type Failure struct {
	// Timestamp is when the failure was recorded, RFC3339 UTC. Sortable as
	// a string thanks to RFC3339's lexical ordering.
	Timestamp string `json:"at"`

	// Branch is the ref name that failed to push (e.g.,
	// "polecat/guzzle/gu-ftja--xxx", or "main" for direct merges).
	Branch string `json:"branch"`

	// CommitSHA is the local commit that was being pushed, when known.
	CommitSHA string `json:"sha,omitempty"`

	// Remote is the git remote targeted (e.g., "origin").
	Remote string `json:"remote"`

	// Source indicates which `gt done` code path recorded the failure; one
	// of the Source* constants (SourceDone, SourceDoneRelay, ...).
	Source string `json:"source"`

	// Stage is which step failed: StagePush or StageVerify.
	Stage string `json:"stage"`

	// Error is the failure detail — the whole reason this log exists. It is
	// the stringified push/verify error, lightly bounded so a pathological
	// multi-megabyte error can't bloat the rig's runtime dir.
	Error string `json:"error"`

	// Worker, when known, is the polecat / crew name responsible.
	Worker string `json:"worker,omitempty"`

	// IssueID, when known, is the bead the push corresponds to.
	IssueID string `json:"issue,omitempty"`
}

// maxFailureErrorLen bounds the persisted error string. Push/verify errors
// are normally short; this guards against a pathological git error (e.g. one
// echoing a huge remote response) bloating the runtime log.
const maxFailureErrorLen = 4096

// FailurePath returns the push-failure log's path for a rig (regardless of
// whether it exists yet). townRoot and rigName must both be non-empty;
// otherwise the returned path is empty.
func FailurePath(townRoot, rigName string) string {
	if townRoot == "" || rigName == "" {
		return ""
	}
	return filepath.Join(townRoot, rigName, ".runtime", FailureFilename)
}

// AppendFailure writes a failure record to the rig's push-failure log. It is
// best-effort: missing/invalid arguments return an error, and the caller MUST
// NOT escalate or fail the surrounding push path on a logging failure.
//
// The log file and its parent .runtime directory are created on demand,
// matching Append's behavior for the success log.
func AppendFailure(townRoot, rigName string, f Failure) error {
	path := FailurePath(townRoot, rigName)
	if path == "" {
		return errors.New("pushlog: empty townRoot or rigName")
	}
	if f.Branch == "" {
		return errors.New("pushlog: failure missing required branch")
	}
	if f.Timestamp == "" {
		f.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if f.Remote == "" {
		f.Remote = "origin"
	}
	if len(f.Error) > maxFailureErrorLen {
		f.Error = f.Error[:maxFailureErrorLen] + "...(truncated)"
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating .runtime dir: %w", err)
	}

	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshaling failure: %w", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644) //nolint:gosec // G304: path is rig-internal
	if err != nil {
		return fmt.Errorf("opening failure log: %w", err)
	}
	defer file.Close() //nolint:errcheck // best-effort close; sync below catches data-loss

	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing failure: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("syncing failure log: %w", err)
	}
	return nil
}

// LogFailureOrWarn calls AppendFailure and prints a warning to stderr on
// failure. Used by the gt done call sites where logging must never block.
func LogFailureOrWarn(townRoot, rigName string, f Failure) {
	if err := AppendFailure(townRoot, rigName, f); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: push failure log append failed (rig=%s, branch=%s): %v\n", rigName, f.Branch, err)
	}
}

// ReadFailures returns all failure records in the log, oldest first. Returns
// (nil, nil) if the log doesn't exist yet. Malformed lines are skipped (with
// no error) so a single corrupt write doesn't deny forensic access to the
// rest of the log — matching Read's behavior for the success log.
func ReadFailures(townRoot, rigName string) ([]Failure, error) {
	path := FailurePath(townRoot, rigName)
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path) //nolint:gosec // G304: path is rig-internal
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening failure log: %w", err)
	}
	defer file.Close() //nolint:errcheck

	var out []Failure
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var f Failure
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			// Skip malformed lines; future records must remain readable.
			continue
		}
		out = append(out, f)
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("scanning failure log: %w", err)
	}
	return out, nil
}

// Package pushlog provides a durable, append-only record of successful
// pushes performed by gt done and witness recovery. It exists so the
// witness/deacon teardown decision path can prove "this branch was pushed
// at SHA X at time T" independent of current origin state — even after a
// fork branch has been deleted (post-merge or fork-sync).
//
// Background (gu-ftja, follow-up to gu-ftlw): witness/deacon push checks
// historically rely on a live `git ls-remote` at decision time. When a
// fork branch is later reaped, forensics cannot distinguish "push happened
// then was reaped" from "push never happened". The receipt log fills that
// gap with a small, JSONL-formatted append-only log under the rig's
// runtime directory.
//
// Storage: <townRoot>/<rigName>/.runtime/push-receipts.jsonl
//
// Each line is a JSON-encoded Receipt. The file is created lazily on the
// first successful push. Writes are best-effort: a failure to log MUST
// never block the surrounding push/merge path. Errors are logged to
// stderr and otherwise swallowed.
package pushlog

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

// Filename is the name of the receipt log within a rig's .runtime dir.
const Filename = "push-receipts.jsonl"

// Source identifies which code path recorded a push receipt. Stable strings
// — they are persisted in the log and inspected by forensic tooling.
const (
	// SourceDone is `gt done` writing a receipt after its own push to a
	// polecat feature branch on origin succeeded.
	SourceDone = "done"

	// SourceDoneNoMR is `gt done` recording a direct push to the rig's
	// default/main branch on the no-MR (review_only/no_merge) path.
	SourceDoneNoMR = "done-no-mr"

	// SourceDoneDirect is `gt done` recording a direct merge to the
	// default branch (the "late direct merge" path, or `--target main`
	// without an MR queue).
	SourceDoneDirect = "done-direct"

	// SourceDoneRelay is `gt done` recording a fast-forward push of a
	// merge=local relay leg to its named base branch (gs-d26), so the next
	// leg in the relay builds on top of it.
	SourceDoneRelay = "done-relay"

	// SourceWitnessRecovery is the witness recoverUnfiledMR path pushing
	// a polecat's stranded branch on its behalf.
	SourceWitnessRecovery = "witness-recovery"
)

// Receipt is a single durable record of a successful push. The shape is
// stable enough that other tools (forensics, dashboards, deacon) can
// consume it without coupling to gt internals.
type Receipt struct {
	// Timestamp is when the push completed, in RFC3339 UTC. Sortable as
	// a string thanks to RFC3339's lexical ordering.
	Timestamp string `json:"at"`

	// Branch is the ref name pushed (e.g., "polecat/guzzle/gu-ftja--xxx"
	// or "main" for direct-to-main merges).
	Branch string `json:"branch"`

	// CommitSHA is the full 40-char SHA at the branch tip after the push.
	CommitSHA string `json:"sha"`

	// Remote is the git remote that received the push (e.g., "origin").
	Remote string `json:"remote"`

	// PushURL is the resolved push URL, when known. Recorded so a future
	// forensic tool can answer "where did the push actually go" even
	// after the remote URL has been reconfigured.
	PushURL string `json:"push_url,omitempty"`

	// Source indicates which code path recorded the receipt; one of the
	// Source* constants above.
	Source string `json:"source"`

	// Worker, when known, is the polecat / crew name responsible for the
	// push (e.g., "gastown_upstream/polecats/guzzle"). Helps cross-reference
	// the agent capability ledger.
	Worker string `json:"worker,omitempty"`

	// IssueID, when known, is the bead the push corresponds to.
	IssueID string `json:"issue,omitempty"`
}

// Path returns the receipt log's path for a rig (regardless of whether it
// exists yet). townRoot and rigName must both be non-empty; otherwise the
// returned path is empty.
func Path(townRoot, rigName string) string {
	if townRoot == "" || rigName == "" {
		return ""
	}
	return filepath.Join(townRoot, rigName, ".runtime", Filename)
}

// Append writes a receipt to the rig's push-receipt log. It is best-effort:
// missing/invalid arguments are silently ignored, and write errors are
// returned but the caller should NOT escalate or fail the surrounding
// push path on a logging failure.
//
// The log file and its parent .runtime directory are created on demand
// with mode 0644 / 0755 respectively (matching the rest of the codebase's
// .runtime usage).
func Append(townRoot, rigName string, r Receipt) error {
	path := Path(townRoot, rigName)
	if path == "" {
		return errors.New("pushlog: empty townRoot or rigName")
	}
	if r.Branch == "" || r.CommitSHA == "" {
		return errors.New("pushlog: receipt missing required branch/sha")
	}
	if r.Timestamp == "" {
		r.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if r.Remote == "" {
		r.Remote = "origin"
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating .runtime dir: %w", err)
	}

	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshaling receipt: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644) //nolint:gosec // G304: path is rig-internal
	if err != nil {
		return fmt.Errorf("opening receipt log: %w", err)
	}
	defer f.Close() //nolint:errcheck // best-effort close; sync below catches data-loss

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing receipt: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("syncing receipt log: %w", err)
	}
	return nil
}

// LogOrWarn calls Append and prints a warning to stderr on failure. Used
// by the gt done / witness call sites where logging must never block.
func LogOrWarn(townRoot, rigName string, r Receipt) {
	if err := Append(townRoot, rigName, r); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: push receipt log append failed (rig=%s, branch=%s): %v\n", rigName, r.Branch, err)
	}
}

// Read returns all receipts in the log, oldest first. Returns (nil, nil)
// if the log doesn't exist yet — that's the common case on a freshly
// provisioned rig and is not an error. Malformed lines are skipped (with
// no error) so a single corrupt write doesn't deny forensic access to
// the rest of the log.
func Read(townRoot, rigName string) ([]Receipt, error) {
	path := Path(townRoot, rigName)
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path) //nolint:gosec // G304: path is rig-internal
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening receipt log: %w", err)
	}
	defer f.Close() //nolint:errcheck

	var out []Receipt
	scanner := bufio.NewScanner(f)
	// Allow large lines without surprising callers (push URLs can be long).
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r Receipt
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			// Skip malformed lines; future receipts must remain readable.
			continue
		}
		out = append(out, r)
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("scanning receipt log: %w", err)
	}
	return out, nil
}

// FindByBranch returns the most recent receipt for a given branch, or nil
// if no receipt has been recorded. Useful for deacon ScanStaleHooks: if a
// fork branch has vanished from origin but a receipt exists, the deacon
// can prove the push happened and infer the branch was reaped — instead
// of mis-attributing the absence as "push never happened".
func FindByBranch(townRoot, rigName, branch string) (*Receipt, error) {
	if branch == "" {
		return nil, nil
	}
	all, err := Read(townRoot, rigName)
	if err != nil {
		return nil, err
	}
	var latest *Receipt
	for i := range all {
		if all[i].Branch != branch {
			continue
		}
		// Lexical compare on RFC3339 strings yields chronological order.
		if latest == nil || all[i].Timestamp > latest.Timestamp {
			rec := all[i]
			latest = &rec
		}
	}
	return latest, nil
}

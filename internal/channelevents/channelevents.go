// Package channelevents provides file-based event emission for named channels.
//
// Channel events are JSON files written to ~/gt/events/<channel>/*.event
// and consumed by await-event subscribers (e.g., the refinery watching for
// MERGE_READY events). This is distinct from the activity feed events in
// the events package (~/gt/.events.jsonl).
package channelevents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/steveyegge/gastown/internal/workspace"
)

// ValidChannelName restricts channel names to safe characters (no path traversal).
var ValidChannelName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// emitSeq is an atomic counter to ensure unique event filenames even when
// time.Now().UnixNano() has low resolution.
var emitSeq atomic.Uint64

// Emit creates an event file in the channel directory, resolving the town
// root from the current working directory.
func Emit(channel, eventType string, payloadPairs []string) (string, error) {
	if !ValidChannelName.MatchString(channel) {
		return "", fmt.Errorf("invalid channel name %q: must match [a-zA-Z0-9_-]", channel)
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		home, _ := os.UserHomeDir()
		townRoot = filepath.Join(home, "gt")
	}
	eventDir := filepath.Join(townRoot, "events", channel)
	if err := os.MkdirAll(eventDir, 0755); err != nil {
		return "", fmt.Errorf("creating event directory: %w", err)
	}

	return emitToDir(eventDir, channel, eventType, payloadPairs)
}

// EmitToTown creates an event file using an explicit town root.
// Used by internal callers that already know the town root.
func EmitToTown(townRoot, channel, eventType string, payloadPairs []string) (string, error) {
	if !ValidChannelName.MatchString(channel) {
		return "", fmt.Errorf("invalid channel name %q: must match [a-zA-Z0-9_-]", channel)
	}

	eventDir := filepath.Join(townRoot, "events", channel)
	if err := os.MkdirAll(eventDir, 0755); err != nil {
		return "", fmt.Errorf("creating event directory: %w", err)
	}
	return emitToDir(eventDir, channel, eventType, payloadPairs)
}

// GCResult summarizes what GCOlderThan removed in a single sweep.
type GCResult struct {
	// Channels is the number of channel subdirectories scanned.
	Channels int
	// Removed is the number of stale .event files deleted.
	Removed int
	// Errors is the number of non-fatal errors encountered (e.g.,
	// unreadable directories or files).
	Errors int
}

// GCOlderThan walks every channel directory under <townRoot>/events and deletes
// .event files older than maxAge (based on file modification time).
//
// Channel events are a pure fire-and-forget fan-out: await-event reads ALL
// pending .event files on each wake (sorted oldest-first) and has no
// offset/cursor/watermark — there is no replay-from-start consumer. Consumers
// that pass --cleanup delete files as they read them, but the witness/ and
// mayor/ channels have consumers that don't, so their directories grow
// unbounded (gu-5bf4f: witness/ at 3549 files back to May 8). Age-based
// pruning is therefore safe: any file older than maxAge has long since been
// delivered, and a fresh-enough window is retained so a slow consumer never
// races the sweep.
//
// Best-effort and non-fatal: a single failed read/remove is counted in Errors;
// the sweep continues. A missing events root is treated as "nothing to GC"
// (nil error, zero counts) so the daemon can call this on a fresh town.
func GCOlderThan(townRoot string, maxAge time.Duration) (GCResult, error) {
	root := filepath.Join(townRoot, "events")

	channelDirs, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return GCResult{}, nil
		}
		return GCResult{}, fmt.Errorf("reading events root: %w", err)
	}

	cutoff := time.Now().Add(-maxAge)
	var result GCResult

	for _, cd := range channelDirs {
		if !cd.IsDir() {
			continue
		}
		result.Channels++

		dir := filepath.Join(root, cd.Name())
		removed, errs := pruneOlderInDir(dir, cutoff)
		result.Removed += removed
		result.Errors += errs
	}

	return result, nil
}

// pruneOlderInDir removes every .event file in dir whose modification time is
// before cutoff, returning the number removed and the number of non-fatal
// errors. A missing directory counts as zero of both.
func pruneOlderInDir(dir string, cutoff time.Time) (removed, errs int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			errs++
		}
		return removed, errs
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".event") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				continue // raced with a consumer's --cleanup — fine
			}
			errs++
			continue
		}
		if info.ModTime().Before(cutoff) {
			if rmErr := os.Remove(filepath.Join(dir, entry.Name())); rmErr != nil {
				if os.IsNotExist(rmErr) {
					continue // raced with a consumer — fine
				}
				errs++
				continue
			}
			removed++
		}
	}

	return removed, errs
}

// emitToDir writes an event file to the given directory.
func emitToDir(eventDir, channel, eventType string, payloadPairs []string) (string, error) {
	if !ValidChannelName.MatchString(channel) {
		return "", fmt.Errorf("invalid channel name %q: must match [a-zA-Z0-9_-]", channel)
	}

	payload := make(map[string]string)
	for _, pair := range payloadPairs {
		key, val, found := strings.Cut(pair, "=")
		if found {
			payload[key] = val
		}
	}

	now := time.Now()
	event := map[string]interface{}{
		"type":      eventType,
		"channel":   channel,
		"timestamp": now.Format(time.RFC3339),
		"payload":   payload,
	}

	data, err := json.MarshalIndent(event, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling event: %w", err)
	}

	seq := emitSeq.Add(1)
	eventFile := filepath.Join(eventDir, fmt.Sprintf("%d-%d-%d.event", now.UnixNano(), seq, os.Getpid()))
	if err := os.WriteFile(eventFile, data, 0644); err != nil {
		return "", fmt.Errorf("writing event file: %w", err)
	}

	return eventFile, nil
}

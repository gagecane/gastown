package agentlog

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// kiroSessionsDir is the path under $HOME where Kiro CLI stores session logs.
	// Unlike Claude Code (which hashes the project dir into subdirectories),
	// Kiro writes all sessions into a single flat directory and records the
	// working directory inside a sibling metadata JSON file.
	kiroSessionsDir = ".kiro/sessions/cli"
)

// KiroAdapter watches Kiro CLI JSONL conversation files.
//
// Kiro writes conversation files at:
//
//	~/.kiro/sessions/cli/<session-uuid>.jsonl
//
// Each JSONL file is paired with a sibling metadata file at:
//
//	~/.kiro/sessions/cli/<session-uuid>.json
//
// The metadata file records the working directory (cwd) that Kiro was
// launched in. Since the directory is flat (not project-hashed like
// Claude Code), we use the metadata file's cwd to find the JSONL file
// matching our workDir.
//
// The adapter finds the most recently modified JSONL whose metadata cwd
// matches workDir and whose mod time is >= since, then tails it.
type KiroAdapter struct{}

func (a *KiroAdapter) AgentType() string { return "kiro" }

// Watch starts tailing the most recent Kiro JSONL file matching workDir.
// See KiroAdapter for layout details.
//
// When Kiro exits and a new session starts (new JSONL file with a matching
// metadata cwd), Watch automatically switches to the new file within one
// poll interval (500ms), mirroring the ClaudeCodeAdapter.
func (a *KiroAdapter) Watch(ctx context.Context, sessionID, workDir string, since time.Time) (<-chan AgentEvent, error) {
	sessionsDir, err := kiroSessionsDirPath()
	if err != nil {
		return nil, fmt.Errorf("resolving kiro sessions dir: %w", err)
	}

	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("resolving absolute workDir: %w", err)
	}

	ch := make(chan AgentEvent, 64)
	go func() {
		defer close(ch)

		var currentPath string
		for {
			if ctx.Err() != nil {
				return
			}
			jsonlPath, err := waitForNewestKiroJSONL(ctx, sessionsDir, absWorkDir, since)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				// Timeout: no matching JSONL appeared in 30s. Reset `since`
				// so we pick up any new file, same recovery strategy as
				// the Claude Code adapter.
				since = time.Now().Add(-watchPollInterval)
				continue
			}

			currentPath = jsonlPath
			tailKiroJSONL(ctx, currentPath, sessionsDir, absWorkDir, since, sessionID, a.AgentType(), ch)

			if ctx.Err() != nil {
				return
			}
		}
	}()
	return ch, nil
}

// kiroSessionsDirPath returns $HOME/.kiro/sessions/cli.
func kiroSessionsDirPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home dir: %w", err)
	}
	return filepath.Join(home, kiroSessionsDir), nil
}

// waitForNewestKiroJSONL polls sessionsDir until a qualifying .jsonl appears.
// A file qualifies when:
//   - its sibling .json metadata file has cwd == workDir, AND
//   - its mod time is >= since (or since is zero).
//
// Returns the path of the most recently modified qualifying file.
func waitForNewestKiroJSONL(ctx context.Context, sessionsDir, workDir string, since time.Time) (string, error) {
	deadline := time.Now().Add(watchFileTimeout)
	for {
		if path, ok := newestKiroJSONLIn(sessionsDir, workDir, since); ok {
			return path, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timeout: no Kiro JSONL file matching workDir %q appeared in %s within %s", workDir, sessionsDir, watchFileTimeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(watchPollInterval):
		}
	}
}

// newestKiroJSONLIn returns the most recently modified .jsonl file in dir
// whose sibling .json metadata has cwd == workDir and whose mod time is >= since.
func newestKiroJSONLIn(dir, workDir string, since time.Time) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var bestPath string
	var bestTime time.Time
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !since.IsZero() && info.ModTime().Before(since) {
			continue
		}
		jsonlPath := filepath.Join(dir, name)
		metaPath := strings.TrimSuffix(jsonlPath, ".jsonl") + ".json"
		if !kiroMetaMatches(metaPath, workDir) {
			continue
		}
		if bestPath == "" || info.ModTime().After(bestTime) {
			bestPath = jsonlPath
			bestTime = info.ModTime()
		}
	}
	return bestPath, bestPath != ""
}

// kiroMetadata is the subset of fields we read from the sibling .json file.
type kiroMetadata struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
}

// kiroMetaMatches returns true iff the metadata file at metaPath exists,
// parses successfully, and records a cwd equal to workDir (after resolving
// both to absolute paths). Missing or unparseable metadata returns false
// so we skip orphaned or malformed sessions.
func kiroMetaMatches(metaPath, workDir string) bool {
	b, err := os.ReadFile(metaPath)
	if err != nil {
		return false
	}
	var m kiroMetadata
	if err := json.Unmarshal(b, &m); err != nil {
		return false
	}
	if m.Cwd == "" {
		return false
	}
	absCwd, err := filepath.Abs(m.Cwd)
	if err != nil {
		return false
	}
	return absCwd == workDir
}

// kiroNativeSessionIDFromPath extracts the Kiro session UUID from a JSONL path.
func kiroNativeSessionIDFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, ".jsonl")
}

// tailKiroJSONL reads all existing lines in path then polls for new ones,
// emitting AgentEvents on ch. It returns (without closing ch) when:
//   - a newer matching JSONL file appears (new Kiro session), or
//   - ctx is canceled.
func tailKiroJSONL(ctx context.Context, path, sessionsDir, workDir string, since time.Time, sessionID, agentType string, ch chan<- AgentEvent) {
	nativeID := kiroNativeSessionIDFromPath(path)

	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 256*1024)
	var partial strings.Builder

	// lastTS carries the most recently observed timestamp forward so that
	// AssistantMessage / ToolResults entries (which have no meta.timestamp
	// in Kiro's JSONL) still get a sensible ordering. We seed with zero so
	// the parser falls back to time.Now() until the first Prompt arrives.
	var lastTS time.Time

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			partial.WriteString(line)
		}
		if err == nil || (err == io.EOF && strings.HasSuffix(partial.String(), "\n")) {
			fullLine := strings.TrimRight(partial.String(), "\r\n")
			partial.Reset()
			if fullLine != "" {
				events, newLastTS := parseKiroLine(fullLine, sessionID, agentType, nativeID, lastTS)
				lastTS = newLastTS
				for _, ev := range events {
					select {
					case ch <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
		}
		if err == io.EOF {
			if newer, ok := newestKiroJSONLIn(sessionsDir, workDir, since); ok && newer != path {
				return // newer Kiro session detected
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(watchPollInterval):
			}
		} else if err != nil {
			return
		}
	}
}

// ── Kiro JSONL structures ─────────────────────────────────────────────────────

// kiroEntry is a top-level line in a Kiro CLI JSONL file.
//
// Shape (observed):
//
//	{"version":"v1","kind":"Prompt","data":{...}}
//	{"version":"v1","kind":"AssistantMessage","data":{...}}
//	{"version":"v1","kind":"ToolResults","data":{...}}
type kiroEntry struct {
	Version string        `json:"version"`
	Kind    string        `json:"kind"`
	Data    *kiroDataBody `json:"data,omitempty"`
}

// kiroDataBody is the "data" object for Prompt / AssistantMessage / ToolResults.
type kiroDataBody struct {
	MessageID string            `json:"message_id"`
	Content   []kiroContent     `json:"content"`
	Meta      *kiroMeta         `json:"meta,omitempty"`
}

// kiroMeta carries the unix-seconds timestamp attached to Prompt entries.
type kiroMeta struct {
	Timestamp int64 `json:"timestamp"`
}

// kiroContent is one content block inside a kiroDataBody.Content array.
//
// Observed kinds:
//   - "text"       → data is a JSON string
//   - "toolUse"    → data is a kiroToolUseData object
//   - "toolResult" → data is a kiroToolResultData object
//   - "image"      → skipped (not emitted as an event)
type kiroContent struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// kiroToolUseData is the "data" object for a content block of kind "toolUse".
type kiroToolUseData struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

// kiroToolResultData is the "data" object for a content block of kind "toolResult".
type kiroToolResultData struct {
	ToolUseID string          `json:"toolUseId"`
	Content   []kiroContent   `json:"content"`
	Status    string          `json:"status,omitempty"`
}

// parseKiroLine parses one Kiro JSONL line and returns 0 or more AgentEvents
// plus the latest timestamp seen (so callers can carry it forward to entries
// that lack their own meta.timestamp).
func parseKiroLine(line, sessionID, agentType, nativeSessionID string, lastTS time.Time) ([]AgentEvent, time.Time) {
	var entry kiroEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return nil, lastTS
	}
	if entry.Data == nil {
		return nil, lastTS
	}

	// Resolve a timestamp for this entry.
	//   Prompt           → meta.timestamp (unix seconds); updates lastTS
	//   AssistantMessage → inherit lastTS, else time.Now()
	//   ToolResults      → inherit lastTS, else time.Now()
	ts := lastTS
	if entry.Data.Meta != nil && entry.Data.Meta.Timestamp > 0 {
		ts = time.Unix(entry.Data.Meta.Timestamp, 0).UTC()
		lastTS = ts
	} else if ts.IsZero() {
		ts = time.Now()
	}

	// Derive role from top-level kind.
	role := ""
	switch entry.Kind {
	case "Prompt":
		role = "user"
	case "AssistantMessage":
		role = "assistant"
	case "ToolResults":
		role = "user" // tool results are fed back to the model as a user-role message
	default:
		// Ignore unknown entry kinds (e.g., future Kiro additions).
		return nil, lastTS
	}

	var events []AgentEvent
	for _, c := range entry.Data.Content {
		eventType, content, ok := kiroContentToEvent(c)
		if !ok {
			continue
		}
		events = append(events, AgentEvent{
			AgentType:       agentType,
			SessionID:       sessionID,
			NativeSessionID: nativeSessionID,
			EventType:       eventType,
			Role:            role,
			Content:         content,
			Timestamp:       ts,
		})
	}
	return events, lastTS
}

// kiroContentToEvent converts one Kiro content block into normalized
// (eventType, content) values. Returns ok=false when the block should be
// skipped (empty content, unknown kind, unparseable data).
func kiroContentToEvent(c kiroContent) (eventType, content string, ok bool) {
	switch c.Kind {
	case "text":
		var s string
		if err := json.Unmarshal(c.Data, &s); err != nil {
			return "", "", false
		}
		if s == "" {
			return "", "", false
		}
		return "text", s, true

	case "toolUse":
		var d kiroToolUseData
		if err := json.Unmarshal(c.Data, &d); err != nil {
			return "", "", false
		}
		if d.Name == "" {
			return "", "", false
		}
		// Match the Claude adapter's format: "<name>: <json input>".
		input := string(d.Input)
		if input == "" {
			input = "{}"
		}
		return "tool_use", d.Name + ": " + input, true

	case "toolResult":
		var d kiroToolResultData
		if err := json.Unmarshal(c.Data, &d); err != nil {
			return "", "", false
		}
		// Flatten the nested content array into a single string. Kiro wraps
		// stdout/stderr/json payloads here; downstream telemetry just needs
		// something searchable.
		text := flattenKiroToolResult(d.Content)
		if text == "" {
			return "", "", false
		}
		return "tool_result", text, true

	default:
		// Skip "image" and any other unmodeled kinds.
		return "", "", false
	}
}

// flattenKiroToolResult joins the inner content blocks of a toolResult into a
// single string. "text" blocks contribute their string; "json" blocks are
// re-serialized compactly. Order is preserved.
func flattenKiroToolResult(blocks []kiroContent) string {
	if len(blocks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		switch b.Kind {
		case "text":
			var s string
			if err := json.Unmarshal(b.Data, &s); err == nil && s != "" {
				parts = append(parts, s)
			}
		case "json":
			// b.Data is already compact JSON; keep as-is.
			if len(b.Data) > 0 {
				parts = append(parts, string(b.Data))
			}
		default:
			// Skip unknown inner kinds.
		}
	}
	return strings.Join(parts, "\n")
}

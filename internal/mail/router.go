package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Router handles mail delivery via beads. Address parsing, group resolution,
// agent-bead queries, recipient validation, per-scheme dispatch, and
// notification delivery live in the router_*.go sibling files in this
// package. This file owns the Router type, its constructors, the top-level
// Send dispatcher, and a few low-level helpers shared across the rest.

// ErrUnknownList indicates a mailing list name was not found in configuration.
var ErrUnknownList = errors.New("unknown mailing list")

// ErrUnknownQueue indicates a queue name was not found in configuration.
var ErrUnknownQueue = errors.New("unknown queue")

// ErrUnknownAnnounce indicates an announce channel name was not found in configuration.
var ErrUnknownAnnounce = errors.New("unknown announce channel")

// DefaultIdleNotifyTimeout is how long the router waits for a recipient's
// session to become idle before falling back to a queued nudge.
const DefaultIdleNotifyTimeout = 3 * time.Second

// Router handles message delivery via beads.
// It routes messages to the correct beads database based on address:
// - Town-level (mayor/, deacon/) -> {townRoot}/.beads
// - Rig-level (rig/polecat) -> {townRoot}/{rig}/.beads
type Router struct {
	workDir  string // fallback directory to run bd commands in
	townRoot string // town root directory (e.g., ~/gt)
	tmux     *tmux.Tmux

	// IdleNotifyTimeout controls how long to wait for a session to become
	// idle before falling back to a queued nudge. Zero uses the default.
	IdleNotifyTimeout time.Duration

	notifyWg sync.WaitGroup // tracks in-flight async notifications
}

// NewRouter creates a new mail router.
// workDir should be a directory containing a .beads database.
// The town root is auto-detected from workDir if possible.
func NewRouter(workDir string) *Router {
	// Try to detect town root from workDir
	townRoot := detectTownRoot(workDir)

	return &Router{
		workDir:  workDir,
		townRoot: townRoot,
		tmux:     tmux.NewTmux(),
	}
}

// NewRouterWithTownRoot creates a router with an explicit town root.
func NewRouterWithTownRoot(workDir, townRoot string) *Router {
	return &Router{
		workDir:  workDir,
		townRoot: townRoot,
		tmux:     tmux.NewTmux(),
	}
}

// WaitPendingNotifications blocks until all in-flight async notifications
// have completed. CLI commands should call this before exiting to avoid
// losing notifications that are still being delivered.
func (r *Router) WaitPendingNotifications() {
	r.notifyWg.Wait()
}

// detectTownRoot finds the town root directory.
//
// Uses workspace.Find which correctly handles nested workspaces by always
// searching to the filesystem root and returning the outermost workspace.
// Falls back to GT_TOWN_ROOT/GT_ROOT env vars when workspace.Find cannot
// locate a workspace (e.g., running from outside any workspace).
func detectTownRoot(startDir string) string {
	// workspace.Find handles nested workspaces correctly: it always searches
	// to the filesystem root and returns the outermost mayor/town.json match.
	townRoot, err := workspace.Find(startDir)
	if err == nil && townRoot != "" {
		return townRoot
	}

	// Fallback: try GT_TOWN_ROOT or GT_ROOT env vars when workspace detection
	// fails (e.g., running from outside any workspace directory).
	for _, envName := range []string{"GT_TOWN_ROOT", "GT_ROOT"} {
		if envRoot := os.Getenv(envName); envRoot != "" {
			if ok, _ := workspace.IsWorkspace(envRoot); ok {
				return envRoot
			}
		}
	}
	return ""
}

// resolveBeadsDir returns the correct .beads directory for mail delivery.
//
// All mail uses town beads ({townRoot}/.beads). Rig-level beads ({rig}/.beads)
// are for project issues only, not mail.
func (r *Router) resolveBeadsDir() string {
	// If no town root, fall back to workDir's .beads
	if r.townRoot == "" {
		return filepath.Join(r.workDir, ".beads")
	}

	// All mail uses town-level beads
	return filepath.Join(r.townRoot, ".beads")
}

func (r *Router) ensureCustomTypes(beadsDir string) error {
	if err := beads.EnsureCustomTypes(beadsDir); err != nil {
		return fmt.Errorf("ensuring custom types: %w", err)
	}
	return nil
}

func (r *Router) buildLabels(msg *Message) []string {
	var labels []string
	labels = append(labels, "gt:message")
	if msg.Type == TypeEscalation {
		labels = append(labels, "gt:escalation")
	}
	labels = append(labels, "from:"+msg.From)
	labels = append(labels, "msg-type:"+string(msg.Type))
	labels = append(labels, DeliverySendLabels()...)
	if msg.ThreadID != "" {
		labels = append(labels, "thread:"+msg.ThreadID)
	}
	if msg.ReplyTo != "" {
		labels = append(labels, "reply-to:"+msg.ReplyTo)
	}
	for _, cc := range msg.CC {
		ccIdentity := AddressToIdentity(cc)
		labels = append(labels, "cc:"+ccIdentity)
	}
	return labels
}

// appendMetadataArgs appends --metadata JSON to bd create args if msg.Metadata is set.
func appendMetadataArgs(args []string, msg *Message) []string {
	if len(msg.Metadata) == 0 {
		return args
	}
	b, err := json.Marshal(msg.Metadata)
	if err != nil {
		return args
	}
	return append(args, "--metadata", string(b))
}

// shouldBeWisp determines if a message should be stored as a wisp.
// Returns true if:
// - Message.Wisp is explicitly set
// - Subject matches lifecycle message patterns (POLECAT_*, NUDGE, etc.)
func (r *Router) shouldBeWisp(msg *Message) bool {
	if msg.Wisp {
		return true
	}
	// Auto-detect protocol/lifecycle messages by subject prefix
	subjectLower := strings.ToLower(msg.Subject)
	wispPrefixes := []string{
		"polecat_started",
		"polecat_done",
		"work_done",
		"start_work",
		"nudge",
		"lifecycle:",
		"merged",
		"merge_ready",
		"merge_failed",
	}
	for _, prefix := range wispPrefixes {
		if strings.HasPrefix(subjectLower, prefix) {
			return true
		}
	}
	return false
}

// Send delivers a message via beads message.
// Routes the message to the correct beads database based on recipient address.
// Supports fan-out for:
// - Mailing lists (list:name) - fans out to all list members
// - @group addresses - resolves and fans out to matching agents
// Supports single-copy delivery for:
// - Queues (queue:name) - stores single message for worker claiming
// - Announces (announce:name) - bulletin board, no claiming, retention-limited
func (r *Router) Send(msg *Message) error {
	// Check for mailing list address
	if isListAddress(msg.To) {
		return r.sendToList(msg)
	}

	// Check for queue address - single message for claiming
	if isQueueAddress(msg.To) {
		return r.sendToQueue(msg)
	}

	// Check for announce address - bulletin board (single copy, no claiming)
	if isAnnounceAddress(msg.To) {
		return r.sendToAnnounce(msg)
	}

	// Check for beads-native channel address - broadcast with retention
	if isChannelAddress(msg.To) {
		return r.sendToChannel(msg)
	}

	// Check for @group address - resolve and fan-out
	if isGroupAddress(msg.To) {
		return r.sendToGroup(msg)
	}

	// Single recipient - send directly
	return r.sendToSingle(msg)
}

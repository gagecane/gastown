// Package beads provides a wrapper for the bd (beads) CLI.
package beads

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	beadsdk "github.com/steveyegge/beads"
)

// Common errors
// ZFC: Only define errors that don't require stderr parsing for decisions.
// ErrNotARepo and ErrSyncConflict were removed - agents should handle these directly.
var (
	ErrNotInstalled = errors.New("bd not installed: run 'pip install beads-cli' or see https://github.com/anthropics/beads")
	ErrNotFound     = errors.New("issue not found")
	ErrFlagTitle    = errors.New("title looks like a CLI flag (starts with '-'); use --title=\"...\" to set flag-like titles intentionally")
)

// ExtractIssueID strips the external:prefix:id wrapper from bead IDs.
// bd dep add wraps cross-rig IDs as "external:prefix:id" for routing,
// but consumers need the raw bead ID for display and lookups.
func ExtractIssueID(id string) string {
	if strings.HasPrefix(id, "external:") {
		parts := strings.SplitN(id, ":", 3)
		if len(parts) == 3 {
			return parts[2]
		}
	}
	return id
}

// IsFlagLikeTitle returns true if the title looks like it was accidentally set
// from a CLI flag (e.g., "--help", "--json", "-v"). This catches a common
// mistake where `bd create --title --help` consumes --help as the title value
// instead of showing help. Titles with spaces (e.g., "Fix --help handling")
// are allowed since they're clearly intentional multi-word titles.
func IsFlagLikeTitle(title string) bool {
	if !strings.HasPrefix(title, "-") {
		return false
	}
	// Single-word flag-like strings: "--help", "-h", "--json", "--verbose"
	// Multi-word titles with flags embedded are fine: "Fix --help handling"
	return !strings.Contains(title, " ")
}

// ListOptions specifies filters for listing issues.
type ListOptions struct {
	Status     string // "open", "closed", "all"
	Type       string // Deprecated: use Label instead. Was "task", "bug", "feature", "epic"; converted to "gt:" prefix.
	Label      string // Label filter (e.g., "gt:agent", "gt:merge-request")
	Priority   int    // 0-4, -1 for no filter
	Parent     string // filter by parent ID
	Assignee   string // filter by assignee (e.g., "gastown/Toast")
	NoAssignee bool   // filter for issues with no assignee
	Limit      int    // Max results (0 = unlimited, overrides bd default of 50)
	Ephemeral  bool   // Search wisps table (ephemeral issues) instead of issues table
}

// CreateOptions specifies options for creating an issue.
type CreateOptions struct {
	Title       string
	Type        string   // Deprecated: use Labels instead. Was "task", "bug", "feature", "epic".
	Label       string   // Deprecated: use Labels instead. Backward-compatible single-label form.
	Labels      []string // Labels to set (e.g., "gt:task", "gt:merge-request")
	Priority    int      // 0-4
	Description string
	Parent      string
	Actor       string // Who is creating this issue (populates created_by)
	Ephemeral   bool   // Create as ephemeral (wisp) - not synced to git
	Rig         string // Target rig database (e.g., "gantry"). When set, routes bd create to the rig's directory via --repo.
}

// UpdateOptions specifies options for updating an issue.
type UpdateOptions struct {
	Title        *string
	Status       *string
	Priority     *int
	Description  *string
	Assignee     *string
	AddLabels    []string // Labels to add
	RemoveLabels []string // Labels to remove
	SetLabels    []string // Labels to set (replaces all existing)
}

// Beads wraps bd CLI operations for a working directory.
// When store is non-nil, methods with in-process implementations use the
// beadsdk.Storage directly instead of shelling out to the bd CLI. This
// eliminates ~600ms of subprocess overhead per operation.
type Beads struct {
	workDir    string
	beadsDir   string // Optional BEADS_DIR override for cross-database access
	isolated   bool   // If true, suppress inherited beads env vars (for test isolation)
	serverPort int    // If set, pass --server-port to bd init and GT_DOLT_PORT to env

	// store is an optional in-process beadsdk.Storage. When set, methods
	// bypass the bd subprocess and use the store directly. Follows the
	// pattern in internal/daemon/convoy_manager.go. Callers are responsible
	// for closing the store.
	store beadsdk.Storage

	// Lazy-cached town root for routing resolution.
	// Populated on first call to getTownRoot() to avoid filesystem walk on every operation.
	townRoot     string
	townRootOnce sync.Once
}

// New creates a new Beads wrapper for the given directory.
func New(workDir string) *Beads {
	return &Beads{workDir: workDir}
}

// NewIsolated creates a Beads wrapper for test isolation.
// This suppresses inherited beads env vars (BD_ACTOR, BEADS_DB) to prevent
// tests from accidentally routing to production databases.
func NewIsolated(workDir string) *Beads {
	return &Beads{workDir: workDir, isolated: true}
}

// NewIsolatedWithPort creates a Beads wrapper for test isolation that targets
// a specific Dolt server port. Init() passes --server-port to bd init, and all
// commands get GT_DOLT_PORT in their environment. This prevents tests from
// creating databases on the production Dolt server (port 3307).
func NewIsolatedWithPort(workDir string, serverPort int) *Beads {
	return &Beads{workDir: workDir, isolated: true, serverPort: serverPort}
}

// NewWithBeadsDir creates a Beads wrapper with an explicit BEADS_DIR.
// This is needed when running from a polecat worktree but accessing town-level beads.
func NewWithBeadsDir(workDir, beadsDir string) *Beads {
	return &Beads{workDir: workDir, beadsDir: beadsDir}
}

// getActor returns the BD_ACTOR value for this context.
// Returns empty string when in isolated mode (tests) to prevent
// inherited actors from routing to production databases.
func (b *Beads) getActor() string {
	if b.isolated {
		return ""
	}
	return os.Getenv("BD_ACTOR")
}

// getTownRoot returns the Gas Town root directory, using lazy caching.
// The town root is found by walking up from workDir looking for mayor/town.json.
// Returns empty string if not in a Gas Town project.
// Thread-safe: uses sync.Once to prevent races on concurrent access.
func (b *Beads) getTownRoot() string {
	b.townRootOnce.Do(func() {
		b.townRoot = FindTownRoot(b.workDir)
	})
	return b.townRoot
}

// getResolvedBeadsDir returns the beads directory this wrapper is operating on.
// This follows any redirects and returns the actual beads directory path.
func (b *Beads) getResolvedBeadsDir() string {
	if b.beadsDir != "" {
		return b.beadsDir
	}
	return ResolveBeadsDir(b.workDir)
}

// Init initializes a new beads database in the working directory.
// This uses the same environment isolation as other commands.
// If ServerPort is set (via NewIsolatedWithPort), passes --server-port to bd init
// so the database is created on the test Dolt server.
func (b *Beads) Init(prefix string) error {
	args := []string{"init"}
	if prefix != "" {
		args = append(args, "--prefix", prefix)
	}
	args = append(args, "--quiet")
	if b.serverPort > 0 {
		args = append(args, "--server-port", fmt.Sprintf("%d", b.serverPort))
	}
	_, err := b.run(args...)
	return err
}

// Stats returns repository statistics.
func (b *Beads) Stats() (string, error) {
	out, err := b.run("stats")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// IsBeadsRepo checks if the working directory is a beads repository.
// ZFC: Check file existence directly instead of parsing bd errors.
func (b *Beads) IsBeadsRepo() bool {
	beadsDir := ResolveBeadsDir(b.workDir)
	info, err := os.Stat(beadsDir)
	return err == nil && info.IsDir()
}

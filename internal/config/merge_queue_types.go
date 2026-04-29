// Merge-queue configuration types and accessors. Extracted from types.go.
package config

// MergeQueueConfig represents merge queue settings for a rig.
type MergeQueueConfig struct {
	// Enabled controls whether the merge queue is active.
	Enabled bool `json:"enabled"`

	// IntegrationBranchPolecatEnabled controls whether polecats auto-source
	// their worktrees from integration branches when the parent epic has one.
	// Nil defaults to true.
	IntegrationBranchPolecatEnabled *bool `json:"integration_branch_polecat_enabled,omitempty"`

	// IntegrationBranchRefineryEnabled controls whether mq submit and gt done
	// auto-detect integration branches as MR targets.
	// Nil defaults to true.
	IntegrationBranchRefineryEnabled *bool `json:"integration_branch_refinery_enabled,omitempty"`

	// IntegrationBranchTemplate is the pattern for integration branch names.
	// Supports variables: {epic}, {prefix}, {user}
	// - {epic}: Full epic ID (e.g., "RA-123")
	// - {prefix}: Epic prefix before first hyphen (e.g., "RA")
	// - {user}: Git user.name (e.g., "klauern")
	// Default: "integration/{epic}"
	IntegrationBranchTemplate string `json:"integration_branch_template,omitempty"`

	// IntegrationBranchAutoLand controls whether the refinery should automatically
	// land integration branches when all children of the epic are closed.
	// Nil defaults to false (manual landing required).
	IntegrationBranchAutoLand *bool `json:"integration_branch_auto_land,omitempty"`

	// MergeStrategy controls how the refinery lands approved work: "direct" (default)
	// merges directly to the base branch, "pr" uses the VCS provider's merge API
	// which respects branch protection/restriction rules.
	MergeStrategy string `json:"merge_strategy,omitempty"`

	// VCSProvider selects the VCS platform for PR operations when
	// MergeStrategy="pr". Valid values: "github" (default), "bitbucket".
	VCSProvider string `json:"vcs_provider,omitempty"`

	// RequireReview controls whether the refinery requires at least one approving
	// review before merging a PR. Only meaningful when merge_strategy="pr".
	// Nil defaults to false (no review required).
	RequireReview *bool `json:"require_review,omitempty"`

	// OnConflict specifies conflict resolution strategy: "assign_back" or "auto_rebase".
	OnConflict string `json:"on_conflict"`

	// RunTests controls whether to run tests before merging.
	// Nil defaults to true (tests are run).
	RunTests *bool `json:"run_tests,omitempty"`

	// TestCommand is the command to run for tests.
	TestCommand string `json:"test_command,omitempty"`

	// LintCommand is the command to run for linting (used by formulas).
	LintCommand string `json:"lint_command,omitempty"`

	// BuildCommand is the command to run for building (used by formulas).
	BuildCommand string `json:"build_command,omitempty"`

	// SetupCommand is the command to run for project setup (e.g., pnpm install).
	SetupCommand string `json:"setup_command,omitempty"`

	// TypecheckCommand is the command to run for type checking (e.g., tsc --noEmit).
	TypecheckCommand string `json:"typecheck_command,omitempty"`

	// DeleteMergedBranches controls whether to delete branches after merging.
	// Nil defaults to true (merged branches are deleted).
	DeleteMergedBranches *bool `json:"delete_merged_branches,omitempty"`

	// RetryFlakyTests is the number of times to retry flaky tests.
	RetryFlakyTests int `json:"retry_flaky_tests"`

	// PollInterval is how often to poll for new merge requests (e.g., "30s").
	PollInterval string `json:"poll_interval"`

	// MaxConcurrent is the maximum number of concurrent merges.
	MaxConcurrent int `json:"max_concurrent"`

	// StaleClaimTimeout is how long a claimed MR can go without updates before
	// being considered abandoned and eligible for re-claim (e.g., "30m").
	StaleClaimTimeout string `json:"stale_claim_timeout,omitempty"`

	// JudgmentEnabled controls whether the refinery performs quality review
	// before merging. When true, the refinery patrol's quality-review step
	// evaluates the diff for correctness, security, and code quality.
	// Nil defaults to false (no quality review).
	JudgmentEnabled *bool `json:"judgment_enabled,omitempty"`

	// ReviewDepth controls the thoroughness of quality review when judgment
	// is enabled. Valid values: "quick", "standard", "deep".
	// Nil defaults to "standard".
	ReviewDepth string `json:"review_depth,omitempty"`

	// Gates defines named quality gate commands to run before (and optionally
	// after) the squash merge. When non-empty, gates replace the legacy
	// RunTests/TestCommand path. Each gate runs as a shell command with an
	// optional per-gate timeout and phase.
	//
	// The canonical runtime parsing (durations, phase enums) lives in
	// internal/refinery/engineer.go. This struct mirrors the on-disk JSON
	// shape so that `gt rig settings set` / `show` round-trip through a
	// single source of truth.
	Gates map[string]*GateConfig `json:"gates,omitempty"`

	// GatesParallel controls whether gates run concurrently.
	// When true, all gates start simultaneously; any failure = overall failure.
	// Nil means "not configured" (refinery default applies).
	GatesParallel *bool `json:"gates_parallel,omitempty"`
}

// GateConfig defines a single quality gate command as stored in
// settings/config.json. The runtime form (with parsed time.Duration / phase
// enum) lives in internal/refinery/engineer.go. This type exists so the
// canonical RigSettings struct is aware of gates, which lets the `gt rig
// settings` CLI accept and preserve gate configuration.
type GateConfig struct {
	// Cmd is the shell command to execute.
	Cmd string `json:"cmd"`

	// Timeout is a Go duration string (e.g., "30s", "5m") capping the gate's
	// run time. Empty means no per-gate timeout; the refinery inherits the
	// context deadline.
	Timeout string `json:"timeout,omitempty"`

	// Phase controls when this gate runs: "pre-merge" (default) or
	// "post-squash". Pre-merge gates run before the squash merge on the
	// source branch. Post-squash gates run after the squash merge on the
	// combined result, before pushing.
	Phase string `json:"phase,omitempty"`
}

// OnConflict strategy constants.
const (
	OnConflictAssignBack = "assign_back"
	OnConflictAutoRebase = "auto_rebase"
)

// IsPolecatIntegrationEnabled returns whether polecat integration branch
// sourcing is enabled. Nil-safe, defaults to true.
func (c *MergeQueueConfig) IsPolecatIntegrationEnabled() bool {
	if c.IntegrationBranchPolecatEnabled == nil {
		return true
	}
	return *c.IntegrationBranchPolecatEnabled
}

// IsRefineryIntegrationEnabled returns whether refinery/submit integration
// branch auto-detection is enabled. Nil-safe, defaults to true.
func (c *MergeQueueConfig) IsRefineryIntegrationEnabled() bool {
	if c.IntegrationBranchRefineryEnabled == nil {
		return true
	}
	return *c.IntegrationBranchRefineryEnabled
}

// IsIntegrationBranchAutoLandEnabled returns whether the refinery should
// auto-land integration branches when all epic children are closed.
// Nil-safe, defaults to false (manual landing required).
func (c *MergeQueueConfig) IsIntegrationBranchAutoLandEnabled() bool {
	if c.IntegrationBranchAutoLand == nil {
		return false
	}
	return *c.IntegrationBranchAutoLand
}

// IsRunTestsEnabled returns whether tests should run before merging.
// Nil-safe, defaults to true.
func (c *MergeQueueConfig) IsRunTestsEnabled() bool {
	if c.RunTests == nil {
		return true
	}
	return *c.RunTests
}

// IsDeleteMergedBranchesEnabled returns whether merged branches should be deleted.
// Nil-safe, defaults to true.
func (c *MergeQueueConfig) IsDeleteMergedBranchesEnabled() bool {
	if c.DeleteMergedBranches == nil {
		return true
	}
	return *c.DeleteMergedBranches
}

// IsJudgmentEnabled returns whether quality review is enabled for merges.
// Nil-safe, defaults to false.
func (c *MergeQueueConfig) IsJudgmentEnabled() bool {
	if c.JudgmentEnabled == nil {
		return false
	}
	return *c.JudgmentEnabled
}

// IsRequireReviewEnabled returns whether PR reviews are required before merging.
// Nil-safe, defaults to false.
func (c *MergeQueueConfig) IsRequireReviewEnabled() bool {
	if c.RequireReview == nil {
		return false
	}
	return *c.RequireReview
}

// GetReviewDepth returns the configured review depth.
// Nil-safe, defaults to "standard".
func (c *MergeQueueConfig) GetReviewDepth() string {
	if c.ReviewDepth == "" {
		return "standard"
	}
	return c.ReviewDepth
}

// DefaultMergeQueueConfig returns a MergeQueueConfig with sensible defaults.
func DefaultMergeQueueConfig() *MergeQueueConfig {
	return &MergeQueueConfig{
		Enabled:                          true,
		IntegrationBranchPolecatEnabled:  boolPtr(true),
		IntegrationBranchRefineryEnabled: boolPtr(true),
		OnConflict:                       OnConflictAssignBack,
		RunTests:                         boolPtr(true),
		TestCommand:                      "",
		DeleteMergedBranches:             boolPtr(true),
		RetryFlakyTests:                  1,
		PollInterval:                     "30s",
		MaxConcurrent:                    1,
		StaleClaimTimeout:                "30m",
	}
}

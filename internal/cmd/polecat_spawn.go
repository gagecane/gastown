// Package cmd provides polecat spawning utilities for gt sling.
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

// SpawnedPolecatInfo contains info about a spawned polecat session.
type SpawnedPolecatInfo struct {
	RigName     string // Rig name (e.g., "gastown")
	PolecatName string // Polecat name (e.g., "Toast")
	ClonePath   string // Path to polecat's git worktree
	SessionName string // Tmux session name (e.g., "gt-gastown-p-Toast")
	Pane        string // Tmux pane ID (empty until StartSession is called)
	BaseBranch  string // Effective base branch (e.g., "main", "integration/epic-id")
	Branch      string // Git branch name (for cleanup on rollback)

	// Internal fields for deferred session start
	account string
	agent   string
}

// AgentID returns the agent identifier (e.g., "gastown/polecats/Toast")
func (s *SpawnedPolecatInfo) AgentID() string {
	return fmt.Sprintf("%s/polecats/%s", s.RigName, s.PolecatName)
}

// SessionStarted returns true if the tmux session has been started.
func (s *SpawnedPolecatInfo) SessionStarted() bool {
	return s.Pane != ""
}

// SlingSpawnOptions contains options for spawning a polecat via sling.
type SlingSpawnOptions struct {
	TownRoot      string // Gas Town workspace root; falls back to cwd when empty
	Force         bool   // Force spawn even if polecat has uncommitted work
	Account       string // Claude Code account handle to use
	Create        bool   // Create polecat if it doesn't exist (currently always true for sling)
	HookBead      string // Bead ID to set as hook_bead at spawn time (atomic assignment)
	Agent         string // Agent override for this spawn (e.g., "gemini", "codex", "claude-haiku")
	BaseBranch    string // Override base branch for polecat worktree (e.g., "develop", "release/v2")
	ResumeBranch  string // Resume an existing branch (e.g. PR head) instead of creating polecat/<name>/<bead>@<ts>
	SkipAdmission bool   // Caller already holds a polecat admission reservation
}

// SpawnPolecatForSling creates a fresh polecat and optionally starts its session.
// This is used by gt sling when the target is a rig name.
// The caller (sling) handles hook attachment and nudging.
func SpawnPolecatForSling(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
	// Find workspace
	townRoot := opts.TownRoot
	if townRoot == "" {
		var err error
		townRoot, err = workspace.FindFromCwdOrError()
		if err != nil {
			return nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
		}
	}

	// Load rig config
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return nil, fmt.Errorf("rig '%s' not found", rigName)
	}

	// Get polecat manager (with tmux for session-aware allocation)
	polecatGit := git.NewGit(r.Path)
	t := tmux.NewTmux()
	polecatMgr := polecat.NewManager(r, polecatGit, t)

	// Pre-spawn Dolt health check (gt-94llt7): verify Dolt is reachable before
	// allocating a polecat. Prevents orphaned polecats when Dolt is down.
	if err := polecatMgr.CheckDoltHealth(); err != nil {
		return nil, fmt.Errorf("pre-spawn health check failed: %w", err)
	}

	// Pre-spawn admission control (gt-1obzke): verify Dolt server has connection
	// capacity before spawning. Prevents connection storms during mass sling.
	if err := polecatMgr.CheckDoltServerCapacity(); err != nil {
		return nil, fmt.Errorf("admission control: %w", err)
	}

	if blocked, reason := IsRigParkedOrDocked(townRoot, rigName); blocked {
		undoCmd := "gt rig unpark"
		if reason == "docked" {
			undoCmd = "gt rig undock"
		}
		return nil, fmt.Errorf("cannot sling to %s rig %q\n%s %s", reason, rigName, undoCmd, rigName)
	}

	var admission *polecatAdmissionHandle
	if !opts.SkipAdmission {
		admission, _, err = acquirePolecatAdmissionFn(townRoot, rigName, opts.HookBead, "spawn-or-reuse")
		if err != nil {
			return nil, err
		}
		defer admission.Release()
	}

	// Per-rig concurrency cap (gu-1lvs): configurable via
	// `gt rig settings set <rig> polecat.max_concurrent N`. Nil/0 means
	// no per-rig cap — only the town-wide cap applies. This is distinct from
	// maxPolecatDirsPerRig below, which caps worktree directories (working +
	// idle), not just working polecats.
	if rigPolecatCap := loadRigPolecatMaxConcurrent(r.Path); rigPolecatCap > 0 {
		rigWorkingCount := countWorkingPolecatsInRig(rigName)
		if rigWorkingCount >= rigPolecatCap {
			return nil, fmt.Errorf("rig %s has %d/%d working polecats (per-rig cap). "+
				"Wait for one to finish, or raise: "+
				"gt rig settings set %s polecat.max_concurrent %d",
				rigName, rigWorkingCount, rigPolecatCap, rigName, rigPolecatCap+1)
		}
	}

	// Refinery-backoff dispatch throttle (gu-5wn56): when opted in, refuse the
	// spawn while this rig's refinery is draining a non-empty merge queue under
	// host build pressure. Breaks the dispatch/refinery deadlock where uncapped
	// dispatch keeps build pressure high so a load-sensitive refinery never
	// retries. Opt-in per rig (default off); fails open on any query error.
	if rigSettings, err := config.LoadRigSettings(filepath.Join(r.Path, "settings", "config.json")); err == nil {
		if throttleErr := checkRefineryBackoffThrottle(r, rigSettings); throttleErr != nil {
			return nil, throttleErr
		}
	}

	// Per-bead respawn circuit breaker (clown show #22 + gu-iqji):
	// Track how many times this bead has been slung. There are TWO tiers:
	//
	//   1. Soft block — bead has hit MaxBeadRespawns within the live decay
	//      window. Operators can override with --force when they have reason
	//      to believe the prior failures were transient (e.g. host load
	//      storm, infra outage).
	//
	//   2. Permanent block — bead's lifetime cumulative attempts have crossed
	//      PermanentBlockMultiplier × MaxBeadRespawns. The bead has
	//      demonstrated chronic failure across multiple decay windows; --force
	//      MUST NOT bypass this. Only `gt sling respawn-reset` clears it.
	//
	// We always increment the lifetime counter (RecordBeadRespawn) on a real
	// dispatch — including --force paths from the deacon's RECOVERED_BEAD
	// loop — so the chronic-failure detector observes every attempt, not
	// just the polite ones. (Before gu-iqji, --force skipped the increment
	// entirely, so the deacon's auto-redispatch loop never tripped the
	// circuit breaker no matter how many times it ran.)
	if opts.HookBead != "" {
		// Permanent block: hard fail regardless of --force.
		if witness.ShouldPermanentlyBlockRespawn(townRoot, opts.HookBead) {
			maxRespawns := config.LoadOperationalConfig(townRoot).GetWitnessConfig().MaxBeadRespawnsV()
			limit := witness.PermanentBlockMultiplier * maxRespawns
			return nil, fmt.Errorf("PERMANENT respawn block for %s (cumulative attempts ≥ %d).\n"+
				"This bead has failed across multiple decay windows and is no longer auto-dispatchable.\n"+
				"--force does NOT override a permanent block — investigate the root cause first.\n"+
				"Reset: gt sling respawn-reset %s",
				opts.HookBead, limit, opts.HookBead)
		}
		// Soft block: --force lets operators retry after transient failures
		// (load storms, infra blips). The witness/deacon redispatch path
		// always carries --force, so they bypass this gate by design — the
		// permanent block above is what stops their feedback loop.
		if !opts.Force {
			if witness.ShouldBlockRespawn(townRoot, opts.HookBead) {
				maxRespawns := config.LoadOperationalConfig(townRoot).GetWitnessConfig().MaxBeadRespawnsV()
				return nil, fmt.Errorf("respawn limit reached for %s (%d attempts). "+
					"This bead keeps failing — investigate before re-dispatching.\n"+
					"Override: gt sling %s %s --force\n"+
					"Reset:    gt sling respawn-reset %s",
					opts.HookBead, maxRespawns,
					opts.HookBead, rigName, opts.HookBead)
			}
		}
		witness.RecordBeadRespawn(townRoot, opts.HookBead)
	}

	// Persistent polecat model (gt-4ac): try to reuse an idle polecat first.
	// Idle polecats have completed their work but kept their sandbox (worktree).
	// Reusing avoids the overhead of creating a new worktree.
	//
	// gu-ylom: iterate over ALL idle polecats, not just the first. A single
	// corrupted polecat worktree (missing .git, stale gitdir reference, etc.)
	// would otherwise block dispatch to every other idle polecat in the rig.
	// On reuse/verification failure we log the problem and try the next idle
	// polecat; if none work we fall through to allocating a fresh one.
	idlePolecats, findErr := polecatMgr.FindIdlePolecats()
	if findErr == nil {
		// Builder-independence guard (gs-aoz): never REUSE a polecat that built
		// the work this bead depends on (the work under review) or that built
		// this bead on a prior attempt. Reusing such a polecat for a review gate
		// lets a builder review its own work, defeating adversarial review. We
		// only skip the specific builder(s); other idle polecats are still reused
		// (pool optimization preserved), and if every idle candidate is excluded
		// we fall through to a fresh allocation — which is independent by
		// construction (a brand-new polecat name).
		var exclude map[string]bool
		if len(idlePolecats) > 0 && opts.HookBead != "" {
			exclude = builderIndependenceExclusions(beads.New(r.Path), opts.HookBead)
		}
		for _, idlePolecat := range idlePolecats {
			if exclude[strings.ToLower(idlePolecat.Name)] {
				fmt.Printf("  Skipping idle polecat %s for %s: builder-independence — it built the work under review (gs-aoz)\n",
					idlePolecat.Name, opts.HookBead)
				continue
			}
			info, reused := tryReuseIdlePolecat(polecatMgr, r, t, idlePolecat.Name, rigName, opts)
			if reused {
				return info, nil
			}
			// tryReuseIdlePolecat already logged the specific failure.
			// Continue to the next idle polecat.
		}
	}

	// Per-rig directory cap: prevent unbounded worktree accumulation, but only
	// after trying safe reuse. A reusable preserved polecat should not be blocked
	// just because the rig is already at the directory cap.
	const maxPolecatDirsPerRig = 30
	rigPolecatDir := filepath.Join(townRoot, rigName, "polecats")
	if entries, err := os.ReadDir(rigPolecatDir); err == nil {
		dirCount := 0
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				dirCount++
			}
		}
		if dirCount >= maxPolecatDirsPerRig {
			return nil, fmt.Errorf("rig %s has %d polecat directories (max %d). "+
				"Resolve recovery-needed polecats before allocating more slots: gt polecat list %s",
				rigName, dirCount, maxPolecatDirsPerRig, rigName)
		}
	}

	// Determine base branch for polecat worktree.
	// ResumeBranch (gh#3602) takes precedence: when resuming an existing branch
	// we must not start from main or auto-detect an integration branch.
	baseBranch := opts.BaseBranch
	if opts.ResumeBranch == "" {
		if baseBranch == "" && opts.HookBead != "" {
			// Auto-detect: check if the hooked bead's parent epic has an integration branch
			settingsPath := filepath.Join(r.Path, "settings", "config.json")
			polecatIntegrationEnabled := true
			if settings, err := config.LoadRigSettings(settingsPath); err == nil && settings.MergeQueue != nil {
				polecatIntegrationEnabled = settings.MergeQueue.IsPolecatIntegrationEnabled()
			}
			if polecatIntegrationEnabled {
				repoGit, repoErr := getRigGit(r.Path)
				if repoErr == nil {
					bd := beads.New(r.Path)
					detected, detectErr := beads.DetectIntegrationBranch(bd, repoGit, opts.HookBead)
					if detectErr == nil && detected != "" {
						baseBranch = "origin/" + detected
						fmt.Printf("  Auto-detected integration branch: %s\n", detected)
					}
				}
			}
		}
		if baseBranch != "" && !strings.HasPrefix(baseBranch, "origin/") {
			baseBranch = "origin/" + baseBranch
		}
	}

	// Build add options with hook_bead set atomically at spawn time
	addOpts := polecat.AddOptions{
		HookBead:     opts.HookBead,
		BaseBranch:   baseBranch,
		ResumeBranch: opts.ResumeBranch,
	}

	// No idle polecat available — allocate and create atomically (GH#2215).
	// AllocateAndAdd holds the pool lock through directory creation, preventing
	// concurrent processes from allocating the same name.
	polecatName, _, err := polecatMgr.AllocateAndAdd(addOpts)
	if err != nil {
		return nil, fmt.Errorf("allocating and creating polecat: %w", err)
	}
	fmt.Printf("Created polecat: %s\n", polecatName)

	// Get polecat object for path info
	polecatObj, err := polecatMgr.Get(polecatName)
	if err != nil {
		return nil, fmt.Errorf("getting polecat after creation: %w", err)
	}

	// Verify worktree was actually created (fixes #1070)
	// The identity bead may exist but worktree creation can fail silently
	if err := verifyWorktreeExists(polecatObj.ClonePath); err != nil {
		// Clean up the partial state before returning error
		_ = polecatMgr.Remove(polecatName, true) // force=true to clean up partial state
		return nil, fmt.Errorf("worktree verification failed for %s: %w\nHint: try 'gt polecat nuke %s/%s --force' to clean up",
			polecatName, err, rigName, polecatName)
	}

	// Get session manager for session name (session start is deferred)
	polecatSessMgr := polecat.NewSessionManager(t, r)
	sessionName := polecatSessMgr.SessionName(polecatName)

	fmt.Printf("%s Polecat %s spawned (session start deferred)\n", style.Bold.Render("✓"), polecatName)

	// Log spawn event to activity feed
	_ = events.LogFeed(events.TypeSpawn, "gt", events.SpawnPayload(rigName, polecatName))

	// Compute effective base branch (strip origin/ prefix since formula prepends it)
	effectiveBranch := strings.TrimPrefix(baseBranch, "origin/")
	if effectiveBranch == "" {
		effectiveBranch = r.DefaultBranch()
	}
	if opts.ResumeBranch != "" {
		effectiveBranch = opts.ResumeBranch
	}

	return &SpawnedPolecatInfo{
		RigName:     rigName,
		PolecatName: polecatName,
		ClonePath:   polecatObj.ClonePath,
		SessionName: sessionName,
		Pane:        "", // Empty until StartSession is called
		BaseBranch:  effectiveBranch,
		Branch:      polecatObj.Branch,
		account:     opts.Account,
		agent:       opts.Agent,
	}, nil
}

// tryReuseIdlePolecat attempts to reuse a single idle polecat by name.
// On success it returns (info, true). On failure (broken worktree,
// branch-only + full repair both fail, verification fails) it prints a
// diagnostic and returns (nil, false) so the caller can try the next
// idle polecat or fall through to allocating a new one. See gu-ylom.
func tryReuseIdlePolecat(
	polecatMgr *polecat.Manager,
	r *rig.Rig,
	t *tmux.Tmux,
	polecatName, rigName string,
	opts SlingSpawnOptions,
) (*SpawnedPolecatInfo, bool) {
	fmt.Printf("Reusing idle polecat: %s\n", polecatName)

	// Determine base branch (same logic as the allocate path below).
	// ResumeBranch (gh#3602) takes precedence: when resuming an existing branch
	// we must not start from main or auto-detect an integration branch.
	baseBranch := opts.BaseBranch
	if opts.ResumeBranch == "" {
		if baseBranch == "" && opts.HookBead != "" {
			settingsPath := filepath.Join(r.Path, "settings", "config.json")
			polecatIntegrationEnabled := true
			if settings, err := config.LoadRigSettings(settingsPath); err == nil && settings.MergeQueue != nil {
				polecatIntegrationEnabled = settings.MergeQueue.IsPolecatIntegrationEnabled()
			}
			if polecatIntegrationEnabled {
				repoGit, repoErr := getRigGit(r.Path)
				if repoErr == nil {
					bd := beads.New(r.Path)
					detected, detectErr := beads.DetectIntegrationBranch(bd, repoGit, opts.HookBead)
					if detectErr == nil && detected != "" {
						baseBranch = "origin/" + detected
						fmt.Printf("  Auto-detected integration branch: %s\n", detected)
					}
				}
			}
		}
		if baseBranch != "" && !strings.HasPrefix(baseBranch, "origin/") {
			baseBranch = "origin/" + baseBranch
		}
	}

	// Reuse the idle polecat with branch-only operations (no worktree add/remove).
	// Phase 3 of persistent-polecat-pool: eliminates ~5s worktree creation overhead.
	// Falls back to full worktree repair if branch-only reuse fails.
	addOpts := polecat.AddOptions{
		HookBead:     opts.HookBead,
		BaseBranch:   baseBranch,
		ResumeBranch: opts.ResumeBranch,
	}
	reuseOK := false
	if _, err := polecatMgr.ReuseIdlePolecat(polecatName, addOpts); err != nil {
		// Branch-only reuse failed — try full worktree repair as fallback
		fmt.Printf("  Branch-only reuse failed for idle polecat %s: %v, trying full repair...\n", polecatName, err)
		if _, err := polecatMgr.RepairWorktreeWithOptions(polecatName, true, addOpts); err != nil {
			fmt.Printf("  Full repair also failed for %s: %v, trying next idle polecat...\n", polecatName, err)
			return nil, false
		}
		reuseOK = true
	} else {
		reuseOK = true
	}

	if !reuseOK {
		return nil, false
	}

	polecatObj, err := polecatMgr.Get(polecatName)
	if err != nil {
		fmt.Printf("  Getting idle polecat %s after reuse failed: %v, trying next...\n", polecatName, err)
		return nil, false
	}
	if err := verifyWorktreeExists(polecatObj.ClonePath); err != nil {
		// gu-ylom: don't return a hard error here. The worktree for this
		// idle polecat is broken (e.g. missing .git). Skip it so the
		// caller can try the next idle polecat or allocate a new one.
		fmt.Printf("  Worktree verification failed for reused %s: %v, skipping (candidate for nuke)\n",
			polecatName, err)
		return nil, false
	}

	polecatSessMgr := polecat.NewSessionManager(t, r)
	sessionName := polecatSessMgr.SessionName(polecatName)

	fmt.Printf("%s Polecat %s reused (idle → working, session start deferred)\n", style.Bold.Render("✓"), polecatName)
	_ = events.LogFeed(events.TypeSpawn, "gt", events.SpawnPayload(rigName, polecatName))

	effectiveBranch := strings.TrimPrefix(baseBranch, "origin/")
	if effectiveBranch == "" {
		effectiveBranch = r.DefaultBranch()
	}
	if opts.ResumeBranch != "" {
		effectiveBranch = opts.ResumeBranch
	}

	return &SpawnedPolecatInfo{
		RigName:     rigName,
		PolecatName: polecatName,
		ClonePath:   polecatObj.ClonePath,
		SessionName: sessionName,
		Pane:        "",
		BaseBranch:  effectiveBranch,
		Branch:      polecatObj.Branch,
		account:     opts.Account,
		agent:       opts.Agent,
	}, true
}

// beadShower is the minimal beads accessor needed by the builder-independence
// guard. *beads.Beads satisfies it; tests use a stub.
type beadShower interface {
	Show(id string) (*beads.Issue, error)
}

// builderIndependenceExclusions returns the set of polecat names (lowercased)
// that must NOT be reused to work beadID, because they built the work it
// depends on — or built beadID itself on a prior attempt. For a review gate
// (which dep-blocks on the build leg it reviews), the build leg's assignee is
// exactly the polecat that produced the work under review; reusing it would let
// a builder review its own work, defeating adversarial review (gs-aoz).
//
// Builder identity is read from the durable `assignee` field, which a closed
// bead retains. The guard is read-only and conservative: it only narrows the
// reuse candidate set and never blocks dispatch (callers fall through to a
// fresh, independent allocation).
func builderIndependenceExclusions(shower beadShower, beadID string) map[string]bool {
	excl := map[string]bool{}
	if shower == nil || beadID == "" {
		return excl
	}
	bead, err := shower.Show(beadID)
	if err != nil || bead == nil {
		return excl
	}
	add := func(assignee string) {
		if name := polecatNameFromAgent(assignee); name != "" {
			excl[strings.ToLower(name)] = true
		}
	}
	// A prior builder of this same bead (e.g. a reopened review gate that the
	// builder previously held): give it fresh eyes on re-dispatch.
	add(bead.Assignee)
	// Builders of the work under review = assignees of this bead's dependencies.
	for _, depID := range bead.DependsOn {
		dep, err := shower.Show(depID)
		if err != nil || dep == nil {
			continue
		}
		add(dep.Assignee)
	}
	return excl
}

// polecatNameFromAgent extracts the polecat name from an agent address of the
// form "<rig>/polecats/<name>". It returns "" for non-polecat agents (crew,
// witness, refinery, etc.), which are never idle-reuse candidates anyway.
func polecatNameFromAgent(agent string) string {
	const marker = "/polecats/"
	i := strings.Index(agent, marker)
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(agent[i+len(marker):])
}

// verifyHookedWorkForAgent queries the beads database for any work bead that is
// hooked to the given agent. It returns nil if at least one hooked/in-progress
// bead with matching assignee exists, or a descriptive error otherwise.
//
// This is a pre-condition guard used by StartSession to catch gu-56ik: scheduler
// dispatch paths must spawn a polecat AND attach work to it atomically. If the
// session is about to start without a hooked work bead, something upstream
// skipped or silently failed the sling step. Starting the tmux session would
// produce a polecat that primes, finds an empty hook, mails witness, and exits —
// wasting a session slot and triggering noisy escalations.
//
// beadsDir may be empty; when empty, the default beads lookup path is used.
//
// Declared as var so tests can stub it out without a real beads database.
var verifyHookedWorkForAgent = func(agentID, beadsDir string) error {
	dir := beadsDir
	if dir == "" {
		if townRoot, err := workspace.FindFromCwd(); err == nil && townRoot != "" {
			dir = townRoot
		}
	}
	b := beads.New(dir)

	// Primary: any bead with status=hooked + assignee=<agent>. This is the
	// canonical post-sling state set by hookBeadWithRetry.
	hooked, err := b.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: agentID,
		Priority: -1,
	})
	if err == nil && len(hooked) > 0 {
		return nil
	}

	// Secondary: an in_progress bead with this assignee — can happen when the
	// session is restarted for a polecat that was mid-work before a crash.
	inProgress, err := b.List(beads.ListOptions{
		Status:   "in_progress",
		Assignee: agentID,
		Priority: -1,
	})
	if err == nil && len(inProgress) > 0 {
		return nil
	}

	return fmt.Errorf("no hooked or in-progress work bead found for agent %s — "+
		"refusing to start session with empty hook (gu-56ik: dispatch must attach work before starting session)",
		agentID)
}

// StartSession starts the tmux session for a spawned polecat.
// This is called after the molecule/bead is attached, so the polecat
// sees its work when gt prime runs on session start.
// Returns the pane ID after session start.
//
// Pre-condition (gu-56ik): a work bead must be hooked to this polecat (status
// =hooked and assignee=<this polecat>) before the tmux session is launched.
// This guards against the failure mode where a session is started without a
// preceding sling step, producing a polecat that primes to an empty hook,
// mails the witness, and self-terminates. Callers (executeSling, runSling)
// always hook work before calling StartSession, so this check should pass on
// the happy path; a failure indicates a bug in an upstream dispatch path.
func (s *SpawnedPolecatInfo) StartSession() (string, error) {
	if s.SessionStarted() {
		return s.Pane, nil
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Pre-condition guard: refuse to start a session with an empty hook.
	// Without this, a polecat can come up, find no assigned work, escalate
	// to the witness, and exit — wasting a session slot and generating
	// noisy dispatch_failed alerts. See gu-56ik.
	//
	// Pass the rig's beads dir so the guard queries the correct database.
	// Without this, the guard defaults to the town-root beads DB (HQ), which
	// does not contain rig-specific beads (e.g. gu-* live in gastown_upstream/.beads/).
	// Use the rig root as the working dir so bd list finds the rig's .beads/ DB.
	rigRoot := filepath.Join(townRoot, s.RigName)
	if err := verifyHookedWorkForAgent(s.AgentID(), rigRoot); err != nil {
		return "", fmt.Errorf("refusing to start session for %s: %w", s.AgentID(), err)
	}

	// Load rig config
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(s.RigName)
	if err != nil {
		return "", fmt.Errorf("rig '%s' not found", s.RigName)
	}

	// Resolve account
	accountsPath := constants.MayorAccountsPath(townRoot)
	claudeConfigDir, _, err := config.ResolveAccountConfigDir(accountsPath, s.account)
	if err != nil {
		return "", fmt.Errorf("resolving account: %w", err)
	}

	// Start session
	t := tmux.NewTmux()
	polecatSessMgr := polecat.NewSessionManager(t, r)

	fmt.Printf("Starting session for %s/%s...\n", s.RigName, s.PolecatName)
	startOpts := polecat.SessionStartOptions{
		RuntimeConfigDir: claudeConfigDir,
		Agent:            s.agent,
	}
	if err := polecatSessMgr.Start(s.PolecatName, startOpts); err != nil {
		return "", fmt.Errorf("starting session: %w", err)
	}

	// Wait for runtime to be fully ready before returning.
	// When an agent override is specified (e.g., --agent codex), resolve the runtime
	// config from the override so WaitForRuntimeReady uses the correct readiness
	// strategy (delay-based for Codex vs prompt-polling for Claude). Without this,
	// ResolveRoleAgentConfig returns the default agent (Claude) and polls for "❯ "
	// in a Codex session, always timing out after 30 seconds (gt-1j3m).
	spawnTownRoot := filepath.Dir(r.Path)
	var runtimeConfig *config.RuntimeConfig
	if s.agent != "" {
		rc, _, err := config.ResolveAgentConfigWithOverride(spawnTownRoot, r.Path, s.agent)
		if err != nil {
			style.PrintWarning("resolving agent config for %s: %v (using default)", s.agent, err)
			runtimeConfig = config.ResolveRoleAgentConfig("polecat", spawnTownRoot, r.Path)
		} else {
			runtimeConfig = rc
		}
	} else {
		runtimeConfig = config.ResolveRoleAgentConfig("polecat", spawnTownRoot, r.Path)
	}
	if err := t.WaitForRuntimeReady(s.SessionName, runtimeConfig, 30*time.Second); err != nil {
		style.PrintWarning("runtime may not be fully ready: %v", err)
	}

	// Update agent state with retry logic (gt-94llt7: fail-safe Dolt writes).
	// Note: warn-only, not fail-hard. The tmux session is already started above,
	// so returning an error here would leave an orphaned session with no cleanup path.
	// The polecat can still function without the agent state update — it only affects
	// monitoring visibility, not correctness. Compare with createAgentBeadWithRetry
	// which fails hard because a polecat without an agent bead is untrackable.
	polecatGit := git.NewGit(r.Path)
	polecatMgr := polecat.NewManager(r, polecatGit, t)
	if err := polecatMgr.SetAgentStateWithRetry(s.PolecatName, "working"); err != nil {
		style.PrintWarning("could not update agent state after retries: %v", err)
	}

	// Update issue status from hooked to in_progress.
	// Also warn-only for the same reason: session is already running.
	if err := polecatMgr.SetState(s.PolecatName, polecat.StateWorking); err != nil {
		style.PrintWarning("could not update issue status to in_progress: %v", err)
	}

	// Get pane — if this fails, the session may have died during startup.
	// Kill the dead session to prevent "session already running" on next attempt (gt-jn40ft).
	pane, err := getSessionPane(s.SessionName)
	if err != nil {
		// Session likely died — clean up the tmux session so it doesn't block re-sling
		_ = t.KillSession(s.SessionName)
		return "", fmt.Errorf("getting pane for %s (session likely died during startup): %w", s.SessionName, err)
	}

	s.Pane = pane
	return pane, nil
}

// IsRigName checks if a target string is a rig name (not a role or path).
// Returns the rig name and true if it's a valid rig.
func IsRigName(target string) (string, bool) {
	// If it contains a slash, it's a path format (rig/role or rig/crew/name)
	if strings.Contains(target, "/") {
		return "", false
	}

	// Check known non-rig role names
	switch strings.ToLower(target) {
	case constants.RoleMayor, "may", constants.RoleDeacon, "dea", constants.RoleCrew, constants.RoleWitness, "wit", constants.RoleRefinery, "ref":
		return "", false
	}

	// Try to load as a rig
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", false
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return "", false
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	_, err = rigMgr.GetRig(target)
	if err != nil {
		return "", false
	}

	return target, true
}

// verifyWorktreeExists checks that a git worktree was actually created at the given path
// and that it is a functional git repository. Returns an error if the worktree is missing,
// has a broken .git reference, or fails basic git validation. (GH#2056)
func verifyWorktreeExists(clonePath string) error {
	// Check if directory exists
	info, err := os.Stat(clonePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("worktree directory does not exist: %s", clonePath)
		}
		return fmt.Errorf("checking worktree directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("worktree path is not a directory: %s", clonePath)
	}

	// Check for .git file (worktrees have a .git file, not a .git directory)
	gitPath := filepath.Join(clonePath, ".git")
	if _, err := os.Stat(gitPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("worktree missing .git file (not a valid git worktree): %s", clonePath)
		}
		return fmt.Errorf("checking .git: %w", err)
	}

	// For worktree .git files, verify the gitdir reference points to a valid path.
	// A broken reference (e.g., from os.Rename instead of git worktree move) causes
	// "fatal: not a git repository" for every git operation.
	gitContent, err := os.ReadFile(gitPath)
	if err == nil {
		content := strings.TrimSpace(string(gitContent))
		if strings.HasPrefix(content, "gitdir: ") {
			gitdirPath := strings.TrimPrefix(content, "gitdir: ")
			if !filepath.IsAbs(gitdirPath) {
				gitdirPath = filepath.Join(clonePath, gitdirPath)
			}
			if _, err := os.Stat(gitdirPath); err != nil {
				return fmt.Errorf("worktree .git references nonexistent gitdir %s: %w", gitdirPath, err)
			}
		}
	}

	// Final validation: run git rev-parse to confirm the worktree is functional
	cmd := exec.Command("git", "-C", clonePath, "rev-parse", "--git-dir")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("worktree at %s is not a valid git repository: %s", clonePath, strings.TrimSpace(string(output)))
	}

	return nil
}

// loadRigPolecatMaxConcurrent reads settings/config.json from the given rig
// path and returns the configured per-rig working polecat cap. Returns 0 when
// unset, non-positive, or unreadable (meaning no per-rig cap applies).
// See PolecatPoolConfig in internal/config/types.go.
func loadRigPolecatMaxConcurrent(rigPath string) int {
	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil {
		return 0
	}
	return settings.GetPolecatMaxConcurrent()
}

# Requirements Completeness

## Summary

The upstream-sync feature has no PRD draft (the referenced file `.prd-reviews/upstream-sync/prd-draft.md` does not exist). The entire specification consists of a single problem statement: "Automatically keep fixes from gastownhall/gastown (upstream) merged into gagecane/gastown (fork) via this local gastown rig. Currently upstream fixes don't flow to the fork." Additional context comes from the architecture observation that `gastownhall/gastown` is the upstream open-source repo, `gagecane/gastown` is the fork (origin remote), and the local rig `gastown_upstream` has crew workers.

This is insufficient to build from. The problem statement describes a *desire* (upstream fixes should flow) but specifies no mechanism, no triggers, no conflict resolution policy, no scope boundaries, and no success criteria. A developer given this specification would need to make dozens of architectural decisions independently, with high risk of building the wrong thing.

## Findings

### Critical Gaps / Questions

1. **No mechanism defined: how does sync happen?**
   - Is this a scheduled cron job? A webhook triggered by upstream pushes? A manual `gt` command? A daemon watcher? A GitHub Action?
   - Why this matters: The mechanism determines the entire architecture. A polling approach vs. event-driven approach vs. manual trigger have radically different complexity, latency, and failure modes.
   - Suggested question: "Should upstream sync be automatic (daemon/cron), event-driven (webhook/GitHub Action), or manual (`gt sync` command), or some combination?"

2. **No conflict resolution policy**
   - The fork (`gagecane/gastown`) presumably has divergent commits from upstream. What happens when an upstream fix conflicts with fork-specific changes?
   - Why this matters: Conflict resolution is the hardest part of any sync system. Without a policy, implementation will either silently break (fast-forward only, skipping conflicting fixes) or require human intervention (defeating "automatic" goal).
   - Suggested question: "When an upstream fix conflicts with fork-local changes, should the system: (a) skip and alert, (b) attempt auto-resolve with merge, (c) create a bead for human resolution, (d) always prefer upstream?"

3. **No definition of "fix" — what gets synced?**
   - "Fixes" implies selective sync (not all commits). But there's no definition of what qualifies as a fix vs. a feature vs. a breaking change.
   - Why this matters: Full mirror sync is trivial (`git merge upstream/main`). Selective sync requires classification criteria (commit message parsing? label-based? path-based?).
   - Suggested question: "Should ALL upstream commits be synced (full merge), or only specific categories? If selective, what's the filter criteria?"

4. **No success criteria — how do we know this is working?**
   - No metrics, no latency SLAs, no coverage targets.
   - Why this matters: Without success criteria, there's no way to verify the implementation works or detect when it breaks.
   - Suggested question: "What does 'done' look like? Example: 'Within N hours of an upstream commit landing, the fork has it merged — or a bead is filed for manual resolution.'"

5. **No scope boundary — which branches? which paths?**
   - Does sync apply to `main` only? All branches? Does it include tags/releases?
   - Are there paths in upstream that should NOT be synced (e.g., CI config, docs that are fork-specific)?
   - Suggested question: "Which upstream branches sync to which fork branches? Are there path exclusions?"

6. **No failure modes or error states defined**
   - What happens if sync fails mid-merge? What if the fork is ahead of upstream? What if there are force-pushes upstream?
   - Why this matters: Failure handling determines reliability. An unattended sync system that fails silently is worse than no sync at all.
   - Suggested question: "What should happen when sync fails? Alert only? Auto-retry? File a bead? Block further syncs until resolved?"

### Important Considerations

1. **No rollback or undo mechanism specified**
   - If a synced upstream commit breaks the fork, is there a way to revert just that sync?
   - This is especially important if sync is automatic — a bad upstream commit could break the fork before anyone notices.

2. **No observability or monitoring requirements**
   - No mention of how to know sync is healthy (dashboard? alerts? `gt` command?).
   - For an automated system, observability is essential.

3. **No interaction with existing rig infrastructure**
   - The rig has crew workers. Does sync happen via crew polecats (one sync = one bead)? Or is it a daemon-level operation? How does it interact with the existing refinery/witness/daemon?

4. **No performance or scale requirements**
   - How many upstream commits per day/week? How large are typical diffs? Is there a latency requirement?

5. **Patches directory exists — relationship unclear**
   - The repo has a `patches/` directory (with `patches/README.md`). Is this the current manual sync mechanism? Should the automated system replace it, or work alongside it?

### Observations

1. **Architecture is observable** — the remote configuration (`origin` = fork, `upstream` = upstream repo) and rig structure (crew workers in `gastown_upstream`) provide architectural context. This is helpful but doesn't substitute for explicit requirements.

2. **Precedent exists** — the `auto-test-pr` feature has a fully specified PRD (at `.prd-reviews/auto-test-pr/prd-draft.md`) with user stories, acceptance criteria, functional requirements, and open questions. The upstream-sync feature would benefit from the same treatment.

3. **The problem is real** — having to manually merge upstream fixes is a genuine pain point for fork maintenance. The desire is clear; the specification is not.

4. **"Crew workers" hint at an approach** — the mention that "the local rig gastown_upstream has crew workers" suggests the intended mechanism might be bead-driven (file a sync bead, polecat executes it). But this is inference, not specification.

## Confidence Assessment

**Low** — The requirements are at "napkin sketch" level. There is no PRD draft, no user stories, no acceptance criteria, no functional requirements, and no definition of done. The problem statement is clear and real, but a developer cannot write tests, make architectural decisions, or verify completeness from this specification alone. At minimum, the following must be defined before implementation:

1. Sync mechanism (trigger type)
2. Sync scope (all commits vs. selective, which branches)
3. Conflict resolution policy
4. Success/done criteria (measurable)
5. Failure handling and alerting

Without these, any implementation is speculative.

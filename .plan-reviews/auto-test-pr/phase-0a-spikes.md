# Phase 0a Spike Outcomes — Auto-Test-PR

One-page summaries of Phase 0a verification/spike tasks. Required by
synthesis.md "Phase 0a exit criteria".

---

## 0a-4. OQ1: Does the rig's settings JSON exist as a distinct artifact?

**Outcome: PASS — proceed with Phase 0 task 1 as-is.**

### Question

D2 in `.designs/auto-test-pr/synthesis.md` assumes `auto_test_pr.*` keys
live in the per-rig **settings JSON** (operator/Mayor authority,
gitignored, outside the worktree) — *not* the in-repo `config.json`
(committed rig identity). OQ1 asks whether such a settings JSON exists
today as a distinct artifact, or whether "rig settings" is just an alias
for the in-repo `config.json`.

### Evidence

**1. Distinct loader exists:** `internal/config/loader.go`

```go
// LoadRigSettings loads and validates a rig settings file.
func LoadRigSettings(path string) (*RigSettings, error) { ... }

// RigSettingsPath returns the path to rig settings file.
func RigSettingsPath(rigPath string) string {
    return filepath.Join(rigPath, "settings", "config.json")
}
```

`RigSettings` is its own type (with `merge_queue`, `agents`,
`role_agents`, `theme`, `namepool`, `crew`, `workflow`, etc.), distinct
from the rig manifest type used by the in-repo `config.json`.

**2. Distinct on-disk artifact exists today:**

```
/home/canewiw/gt/gastown_upstream/settings/config.json
```

```json
{
  "type": "rig-settings",
  "version": 1,
  "merge_queue": { "enabled": true, "gates": { ... }, ... },
  "namepool": { "style": "wasteland" }
}
```

This file is at `<rigPath>/settings/config.json` — *outside* any git
worktree (`git rev-parse --show-toplevel` from that directory fails:
"not a git repository"). It is operator-authored, edited via `gt rig`
commands, and not version-controlled with the rig's source.

Sampling other rigs in the same town confirms the convention is
ubiquitous: every rig under `~/gt/` (`agentforge`, `casc_cdk`,
`casc_constructs`, `casc_crud`, …) has its own
`settings/config.json` with `"type": "rig-settings"`, several with
historical `.bak-*` snapshots showing operator edits over time.

**3. Distinct from the in-repo `config.json`:**

The in-repo file
(`/home/canewiw/gt/gastown_upstream/polecats/fury/gastown_upstream/config.json`)
has:

```json
{ "type": "rig",
  "name": "gastown_upstream",
  "git_url": "https://github.com/gastownhall/gastown",
  "default_branch": "main",
  "merge_queue": { ... } }
```

`"type": "rig"` (rig identity manifest), committed to the repo, edited
via PRs. The `merge_queue` block here is the *defaults* the manifest
ships with; the operator-tuned values live in `settings/config.json`
above and override the manifest values via the loader merge logic
(`internal/config/loader.go` ~L1080-2840 references show
`LoadRigSettings(RigSettingsPath(rigPath))` is used as an override
layer on top of the manifest in many call sites).

**4. Operator authority is real, not aspirational:**

`internal/cmd/rig.go` and `internal/cmd/sling_helpers.go` both load
settings via `LoadRigSettings` and write via `SaveRigSettings`. The
file is mutated by `gt` CLI subcommands (mayor/operator path), never
by polecats, and changes do not flow through PR review — exactly the
authority surface D2 assumes.

### Implication for Phase 0 task 1

Phase 0 task 1 ("settings-JSON loader for `auto_test_pr.*` keys") can
proceed as-described in synthesis.md §"Phase 0 task list":

- Add `auto_test_pr` block to the existing `RigSettings` struct in
  `internal/config/loader.go`.
- Existing `LoadRigSettings` is reused — no new loader needed, no
  ~3-day "create the file" detour, no fallback to in-repo
  `config.json` (which would re-litigate D2's security trade-off and
  require a CODEOWNERS rule on `auto_test_pr.*`).
- Default-absent → disabled semantics (synthesis.md task 1 (a/b/c))
  fit naturally because `LoadRigSettings` already returns a `*RigSettings`
  whose unset fields are zero-valued; an absent `auto_test_pr` block
  unmarshals to a zero-valued struct interpreted as "disabled".

### No prerequisite bead required

Settings JSON exists, is distinct, is operator-authority, and the
loader is already wired. Phase 0 task 1 is unblocked for the path D2
prescribes.

---

## 0a-2 — Mayor main-CI-break event subscription (D16 prerequisite)

**Spike bead:** `gu-g3hm3`
**Prerequisite bead filed:** `gu-grkl`
**Affected Phase 0 task re-shaped:** task 11 (`gu-36voy`)
**Outcome:** **FAIL on key acceptance dimension**

### Acceptance criterion (synthesis §0a-2)

> A fixture main-CI-break event triggers a Mayor callback that can
> read the attributing commit's MR-bead.

### Result

The substrate that detects main-CI-breaks exists, but neither half of
the criterion ("Mayor callback" + "can read the attributing commit's
MR-bead") holds against today's code:

1. **There is no "Mayor callback" surface.** `internal/mayor/` is a
   tmux/ACP session manager (start/stop/attach/status). It has no
   programmatic event-subscription, patrol-callback, or escalation-
   consumer surface. `gt mayor` exposes only session-lifecycle
   subcommands.

2. **Detection lives in `daemon`, not Mayor.**
   `internal/daemon/main_branch_test_runner.go::runMainBranchTests`
   runs configured pre-merge gates against `origin/<default_branch>`
   in a temp worktree on a 30-minute ticker. Per-rig opt-in is via
   `MainBranchTestConfig.Rigs []string` (a daemon patrol config rig
   list), not a rig-bead-level `auto_test_pr.enabled` flag. On gate
   failure it emits `gt escalate -s HIGH "main_branch_test: ..."`.

3. **The callback that exists is `failure_classifier_dog`**, also in
   `internal/daemon/`. It reads open `gt:escalation` beads whose
   title prefixes `main_branch_test:`, pattern-matches the
   description against signatures, and files dedup-fingerprinted
   rig beads. This is a callback in the operational sense (daemon
   dog → escalation bead → daemon dog → rig bead).

4. **Attribution to the breaking commit / MR-bead is structurally
   absent from the escalation.** `runMainBranchTests` builds the
   escalation message from `rig name + gate name + failure output
   tail + go-test FAIL-signal lines`. It does NOT capture HEAD SHA
   on `origin/<default_branch>`, the previous-known-good SHA, or
   any MR-bead ID. The classifier therefore cannot read the
   attributing commit's MR-bead — there is nothing in the
   escalation body to read.

### Re-shape applied to Phase 0 task 11 (`gu-36voy`)

Original task 11 was framed as "wire-only on top of Mayor's existing
main-CI-break subscription." That framing is no longer accurate:

- "Mayor's main-CI-break subscription" should be re-stated as
  "a Town-level daemon dog handling main_branch_test escalations."
  The realistic owner is an extension of `failure_classifier_dog`
  or a new sibling daemon dog, NOT the literal Mayor session.
- "Whose attributing commit's MR-bead carries `gt:auto-test-pr`"
  requires substrate work first: `runMainBranchTests` must add
  structured `commit:` and `previous_commit:` lines to the
  escalation body, and a commit-SHA → MR-bead lookup path must
  be available to the consumer.

The substrate work (escalation attribution + a Town-level daemon-dog
consumer for auto-test-pr-labeled MR-beads) is tracked as a
BLOCKING prerequisite in `gu-grkl`. Phase 0 task 11 (`gu-36voy`) now
depends on `gu-grkl` and is re-scoped to land the SEV-1 chain on top
of that substrate.

### Documentation follow-up (non-blocking)

Synthesis §D16 should be updated in a future revision to say "a
Town-level daemon dog subscribes to main_branch_test escalations"
rather than "Mayor subscribes to main-CI-break events." This is a
docs-only fix; the prerequisite bead carries the operational
substance.

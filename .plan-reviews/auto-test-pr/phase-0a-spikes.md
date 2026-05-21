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

## 0a-1. Refinery per-MR-bead label query + `approved-by:<user>` semantics

**Outcome: PASS — Phase 0 task 10 (gu-mahth) is unblocked as a wire-only change.**

### Question

D15 (PRD-align round 1) requires that auto-test MRs be held by the
Refinery merge handler until a maintainer applies an
`approved-by:<user>` label. The spike must confirm:

(a) Refinery's merge handler can be conditioned on the presence of a
    label on the MR bead.

(b) `bd update <mr-bead> --add-label approved-by:<user>` is canonical
    (or there is an existing equivalent).

### Method

Static inspection of `internal/refinery/engineer.go` (merge-handler
entry points) and `internal/beads/beads.go` (label API and `bd update`
shellout). No runtime fixture was needed because both mechanisms
surface unambiguously from the source.

### Findings

**(a) Label-conditioned merge handling — PRESENT.**

`Engineer.ListReadyMRs` (`internal/refinery/engineer.go:1705-1769`) is
the gating point that decides which MRs proceed through `doMerge`
(`engineer.go:558`). It already conditions on bead labels: the
`gt:owned-direct` belt-and-suspenders skip at `engineer.go:1733` reads
`beads.HasLabel(issue, "gt:owned-direct")` and excludes the MR from
the ready set when present.

`beads.HasLabel` (`internal/beads/beads.go:309-315`) is a stable helper
that takes an `*Issue` (with `Labels []string`) and returns a bool. It
is the same primitive used throughout the codebase
(`beads_escalation.go:213`, `beads_channel.go:194`,
`beads_group.go:199`, `beads_rig.go:201`, `beads_queue.go:216`).

The D15 wire-step (Phase 0 task 10) can add a check at the same filter
site of the form:

```go
if beads.HasLabel(issue, "gt:auto-test-pr") &&
   !beads.HasLabel(issue, "approved-by:"+user) {
    // hold: skip this iteration; do not advance to doMerge
    continue
}
```

There is no architectural change required — only the addition of a
label predicate beside the existing `gt:owned-direct` predicate.

**(b) `bd update --add-label approved-by:<user>` — CANONICAL.**

`UpdateOptions.AddLabels []string` is the documented Go-side field
(`internal/beads/beads.go:521-522`) and is shelled out as
`--add-label=<label>` per item in `Beads.Update`
(`internal/beads/beads.go:1850-1852`). Existing callers use this idiom
(e.g. `beads_escalation.go:298` `AddLabels: []string{"acked"}`,
`beads_escalation.go:328` `AddLabels: []string{"resolved"}`,
`beads_escalation.go:492-493` add+remove pair). The `bd update
--add-label` CLI surface is the same path operators use today, so the
D15 approval write — `bd update <mr-bead> --add-label
approved-by:<user>` — uses established primitives end-to-end.

### Acceptance

> A fixture MR-bead labeled `gt:auto-test-pr` *without*
> `approved-by:<user>` is held by Refinery's merge handler.

The "wire" step in Phase 0 task 10 is what makes this fixture pass —
but the prerequisite this spike verifies is that the **mechanism**
exists to implement that wire-step without new infra. Both required
primitives — label-conditioned filtering at the merge-handler entry
point, and the canonical `--add-label` write path — exist today and
are exercised by production code.

### Notes / risks deferred to wire-step

- **Username derivation for `approved-by:<user>`.** The wire-step must
  decide what `<user>` resolves to (operator's bd-recorded identity
  vs. rig-config admin list). Out of scope for this spike; tracked
  under task 10.
- **Hold vs. skip semantics.** `ListReadyMRs` currently *skips* a
  labeled MR (it remains in the open-MR set and will be re-evaluated
  next loop). This is the desired hold-until-approved behavior — no
  separate hold-state machine is needed. Confirmed by re-reading
  `engineer.go:1730-1737`.
- **Ordering with `gt:owned-direct`.** The new check should come
  *after* the `gt:owned-direct` skip so that owned-direct MRs are
  still unconditionally excluded (they should not exist for
  auto-test-pr but belt-and-suspenders ordering matches the existing
  comment at `engineer.go:1730`).

### No prerequisite bead required

Both required primitives exist. Phase 0 task 10 (gu-mahth) can proceed
as a wire-only change adding two `beads.HasLabel` calls at the
`engineer.go:1733`-area filter site, gated behind the
`auto_test_pr.require_review_approval` config flag (default-true per
D15).

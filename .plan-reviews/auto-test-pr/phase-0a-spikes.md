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

---

## 0a-2. (TBD)

## 0a-3. (TBD)

## 0a-5. Tautology sub-rule (i) precision/recall spike

**Outcome: PASS — sub-rule (i) ships in gate 4d.**

### Question

Can a flow-sensitive taint analysis detect tautological assertions (where
the expected value depends on the function-under-test's return value) with
sufficient precision and recall to be useful as an automated gate?

Thresholds:
- Precision ≥ 85% (≤15% false-positive on known-good tests)
- Recall ≥ 75% (≤25% false-negative on known-tautological tests)

### Method

1. Built a 50-test corpus (25 known-tautological, 25 known-good) sampled
   from real Go test patterns found in gastown_upstream. Corpus lives at
   `internal/autotestpr/tautology/testdata/corpus/`.

2. Implemented a flow-sensitive taint analyzer
   (`internal/autotestpr/tautology/analyzer.go`) that:
   - Tracks taint flow from function-under-test (FUT) return values through
     assignments, selectors, index expressions, slice operations, type
     assertions, range variables, and intermediate variable chains
   - Identifies testify `assert.*` / `require.*` assertions
   - Flags assertions where the "expected" argument (first non-t arg) is
     tainted by a FUT call
   - Handles multi-value returns, struct field access, method calls on FUT
     objects, and stdlib pass-through functions (len, append, string
     conversions, fmt, etc.)

3. Ran the analyzer against:
   - The curated 50-test corpus (precision/recall measurement)
   - 20 real test files from gastown_upstream (248 test functions) to
     validate false-positive rate in the wild

### Results

| Metric | Value | Threshold | Status |
|--------|-------|-----------|--------|
| Precision | 100.0% | ≥ 85% | ✓ PASS |
| Recall | 100.0% | ≥ 75% | ✓ PASS |
| Real-world FP rate | 0.0% (0/248) | Low | ✓ PASS |

Detailed breakdown:
- True Positives: 25 (all tautological tests correctly flagged)
- False Negatives: 0 (no tautological tests missed)
- False Positives: 0 (no good tests incorrectly flagged)
- True Negatives: 25 (all good tests correctly passed)

### Tautological patterns detected (25 corpus samples)

1. Same field compared to itself (`result.Name == result.Name`)
2. FUT return stored in variable then compared back to same field
3. Method on FUT return compared to same method call
4. Multi-return variable compared to itself
5. Index into FUT slice stored then compared to same index
6. Two calls to same FUT compared ("idempotency" non-test)
7. Type assertion on FUT return compared to same assertion
8. Taint through intermediate variable chain
9. Same method called twice on FUT object
10. `assert.True(a == b)` where both from same FUT
11. Range variable from FUT collection compared to itself
12. Deep selector chain both sides from same root
13. Stored vs. fresh call to same FUT with same args
14. Error message from FUT compared to itself
15. Sub-struct field accessed via different paths from same root
16. Same FUT called twice as "consistency" check
17. Map lookup stored then compared to same key
18. FUT return stored conditionally then asserted
19. Both sides from wrapper function over FUT output
20. Different vars storing same FUT call compared
21. Struct field compared to itself (direct)
22. Interface method on FUT called twice
23. Slice operation on FUT compared to same slice
24. Same field via different access path variables
25. Encode/Decode/Encode round-trip (taint propagation through calls)

### Good patterns NOT flagged (25 corpus samples)

String/numeric/boolean literals, table-driven test structs, independently
constructed fixtures, package-level constants, `assert.Empty`/`NotNil`
(single-arg), `assert.Len` with literal, env vars, file fixtures,
`assert.Contains` with literal substring, input (not output) as expected,
`assert.False` with result, map/slice literals, `len(input)` (not
`len(output)`), and `ElementsMatch` with independent slice.

### Implication for Phase 0 task 6c

Sub-rule (i) — "Does any assertion's argument depend on a value returned
from the function-under-test?" — **ships in gate 4d** alongside sub-rules
(ii), (iii), and (iv).

The analyzer implementation at `internal/autotestpr/tautology/analyzer.go`
is production-ready for integration into the tautology linter gate.

### Artifacts

- Analyzer: `internal/autotestpr/tautology/analyzer.go`
- Spike test: `internal/autotestpr/tautology/spike_test.go`
- Real-world validation: `internal/autotestpr/tautology/realworld_test.go`
- Corpus (tautological): `internal/autotestpr/tautology/testdata/corpus/tautological_test.go`
- Corpus (good): `internal/autotestpr/tautology/testdata/corpus/good_test.go`

## 0a-6. Does a substrate exist for the Mayor cycle-close handler (task 3c) to subscribe to MR-bead state-change events?

**Outcome: FAIL — file prerequisite bead `gu-h1fn`; re-shape Phase 0
task 3c (gu-xrxm6) from 'wire-only' to 'consume daemon-dog poller +
implement four cycle-close paths'.**

### Question

Phase 0 task 3c (gu-xrxm6) in `.designs/auto-test-pr/synthesis.md`
specifies the handler "Subscribes to MR-bead state-change events for
beads labeled `gt:auto-test-pr`." This assumes a subscription / event /
change-feed primitive exists on bead state today. Phase 0a-2 (gu-g3hm3)
already verified the analogous assumption for main-CI-break events
(task 11) FAILED — Mayor is a tmux/ACP session manager, not a
programmatic event consumer; that failure spawned `gu-grkl` as a
prerequisite. OQ-3c asks whether the same architectural problem
applies to MR-bead state-change events.

### Evidence

**1. No native subscribe / watch / change-feed APIs in `internal/beads/`:**
The `Beads` type exposes `List`, `Show`, `ListMergeRequests`,
`Create`, `Update`, `Close`, `CloseWithReason`, `ForceCloseWithReason`,
etc. There is no `Watch`, `Subscribe`, `Listen`, `Stream`, `Tail`, or
`Since` method. The convoy `AddWatcher` / `AddNudgeWatcher`
(`internal/beads/fields.go`) are subscriber-list **fields stored on a
convoy bead** used by the convoy completion-nudge code — they do **not**
emit on bead state changes; they are address lists, not a primitive.

**2. No Dolt change-feed primitive in `internal/doltserver/`:**
the package exposes server lifecycle, port/config, identity, and SQL
plumbing. No commit hook, no label-change trigger, no notification
channel.

**3. The activity-events log declares the types but has no producer:**
`internal/events/events.go` declares `TypeMergeStarted`, `TypeMerged`,
`TypeMergeFailed`, `TypeMergeSkipped` and a `MergePayload(...)` helper.
Consumers exist (`internal/feed/curator.go:507` →
`events.TypeMerged`/`TypeMergeFailed`; `internal/cmd/audit.go:458`;
`internal/cmd/activity.go:136`). But across all of `internal/refinery/`,
the only `events.Log*` call is **one** `events.LogFeed(events.TypeMail,
…, MailPayload("deacon/", "CONVOY_NEEDS_FEEDING …"))` at
`internal/refinery/engineer.go:2031`. The merge-event consumers are
listening to a producer that doesn't fire.

**4. The actual MR close path emits no event and no callable hook:**
`internal/refinery/engineer.go:1263` `HandleMRInfoSuccess` is the
post-merge handler. On successful merge it (a) updates `mrFields`
`MergeCommit` + `CloseReason='merged'`, (b) calls
`beads.CloseWithReason("merged", mr.ID)`, (c) fires a transient
`gt nudge mayor/ "MERGED: <id> issue=<x> branch=<y>"`. The nudge is
ephemeral (no Dolt commit, by design — GH#2434), targets only `mayor/`,
and is **not** label-filterable. The symmetric failure path
(`HandleMRInfoFailure`, line 1388) sends `MERGE_FAILED` over the same
nudge channel — the MR stays open in queue, so the
"closed-unmerged" final state for an auto-test-pr cycle would
arrive via a separate path (admin reject at `manager.go:585` or a
GitHub PR close), neither of which emits anything locally beyond a
`bd close` write.

**5. `internal/mayor/` is not an event consumer:** the package contains
ACP/session lifecycle helpers (`cleanup.go`, `manager.go`,
`process_unix.go`). There is no event loop; nothing in `mayor/`
listens for bead changes. Same finding as 0a-2 / `gu-grkl`: the
realistic Town-level consumer is a daemon-dog, not "Mayor".

**6. The only working substrate is poll-list-by-label:**
`Beads.ListMergeRequests(ListOptions{Label, Status})`
(`internal/beads/beads.go:1261`) joins the issues + wisps tables and
filters by label + status. Refinery uses it in 4 places.
`internal/daemon/failure_classifier_dog.go` (line 316) is the canonical
**poll-and-ack-via-label** pattern in this codebase: tick → list-unacked
escalations → classify → file rig bead → ack via fingerprint label
(`classifierFingerprint`, `fpLabel`) written back on the source bead.
The same pattern transposes cleanly to MR-bead cycle close.

### What the design assumed vs. what exists

| Synthesis assumption (3c)                        | Reality                                                                   |
| ------------------------------------------------ | ------------------------------------------------------------------------- |
| MR-bead state-change emits an event              | No — the close path writes the bead and fires a transient nudge to mayor/ |
| Mayor subscribes to those events                 | No — `internal/mayor/` is ACP/session helpers, no event loop              |
| `rig:<target_rig>` label drives O(1) state lookup | Sound — but the *consumer* still has to exist; label alone is inert       |

### Prerequisite bead filed

`gu-h1fn` — "Prerequisite for Phase 0 task 3c: build MR-bead
cycle-close daemon-dog substrate". Acceptance:

1. Daemon-dog poller (extension of `failure_classifier_dog` or a
   sibling file in `internal/daemon/`) lists MR beads by
   label=`gt:auto-test-pr` + status=`closed` on a 30–60s tick and
   ack-dedups via a fingerprint-style label written back on the MR
   bead.
2. Test fixture (closed MR bead with labels `gt:auto-test-pr` +
   `rig:gastown_upstream` + `close_reason=merged`) is dispatched
   exactly once across N ticks (idempotency).
3. The poller's classified output exposes a `(mr_id, target_rig,
   close_reason, body)` struct that the 3c handler consumes directly.
4. Phase 0 task 3c (gu-xrxm6) updated to `depends_on=gu-h1fn` and
   re-shaped from "subscribe to MR-bead state-change events" to
   "consume the dog's classified output and implement the four
   cycle-close paths".
5. Optional follow-up: emit
   `events.LogAudit(events.TypeMerged, …)` from
   `HandleMRInfoSuccess` so the existing dead consumers in
   `feed/curator` and `activity` start seeing real merge events.
   Independent of the 3c critical path; file separately if not done
   here.

### Implication for Phase 0 task 3c

`gu-xrxm6` is no longer "wire-only". It now runs after `gu-h1fn`
and consumes the dog's output. The four cycle-close paths
(merged → cooled-down, closed-unmerged → cooled-down + rejection,
3-closes-in-7d → paused-by-circuit-breaker + Overseer nudge,
BUG-DISCOVERED parsing → P2 bug bead) themselves stay the same
complexity as the synthesis describes; only the substrate changes
from "subscription primitive" to "daemon-dog poll + ack-via-label".
Estimated effort delta: +1 task-time (the substrate); the four
handler paths land on top of it.

### Why this matches Phase 0a-2 / `gu-grkl`

The two failures are structurally identical:

| Phase 0a-2 (task 11 / main-CI-break) | Phase 0a-6 (task 3c / MR cycle-close) |
| ------------------------------------ | ------------------------------------- |
| "Mayor subscribes to main-CI-break events" assumed an event substrate that didn't exist | "Mayor subscribes to MR-bead state-change events" assumed an event substrate that didn't exist |
| Resolution: `gu-grkl` adds attribution + daemon-dog routing on top of `runMainBranchTests` escalations | Resolution: `gu-h1fn` adds daemon-dog poll-and-ack on top of `ListMergeRequests({Label, Status})` |
| Realistic owner: a daemon-dog (extend `failure_classifier_dog`) | Realistic owner: a daemon-dog (extend `failure_classifier_dog` or sibling) |

Both confirm: gastown today has no programmatic subscribe-on-bead-state
primitive; the realistic substrate is daemon-dog polling of
`ListMergeRequests` (or analog) with idempotency via labels written
back on the bead.

### Recorded artifacts

- Work bead: `gu-id33` (this spike) — full audit in `bd show gu-id33` notes
- Prerequisite bead: `gu-h1fn` — substrate work
- Re-shaped task: `gu-xrxm6` — depends-on `gu-h1fn` updated; notes
  appended explaining the new shape

---

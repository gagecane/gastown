# Scope Discipline

> Dimension review of `.designs/curio-p3-retrospect-agent/{design-doc.md,child-beads.md}`
> Leg: scope-creep — "Is there unnecessary work? What can be cut?"

## Verdict

PASS WITH NOTES — the design's *macro* thesis is exemplary scope discipline (it
DELETES an embedded LLM client, patch generator, and `gt curio apply` write path
in favor of the existing polecat substrate), but the 9-bead epic it spawns
re-accretes scope internally: roughly **3 of the 9 beads (B3, B7, B8) plus one
sub-deliverable (B6's CI guard) are not needed for a working MVP** and should be
deferred as follow-ups. The lane delivers its core value with ~5 beads.

## The macro call is right — say so first

Before cutting, credit what the plan already cut. The entire P3 pivot is a
scope-reduction: P2 builds 4-6/6 (in-proc Anthropic SDK, prompt assembly,
structured-output validator, patch generator, bespoke `gt curio apply` gate) are
all DELETED (design-doc Go/No-Go table, line 414). That is an order-of-magnitude
reduction in net-new trusted code and the single best scope decision in the doc.
Q1's post-incident augmentation and Q2's scoped-Dolt-read-role are both correctly
deferred. This review is *not* arguing the design over-builds at the architecture
level — it argues the **child-bead epic bundles future-phase work into the MVP**.

## Must Fix (blocks implementation)

### 1. B7 (precision-gate auto-merge) is future work shipped disabled — pull it out of this epic

- **Issue.** B7 builds a Refinery merge policy that auto-merges threshold CRs,
  then ships it **OFF** ("Default OFF... every proposal requires human approval
  initially. Enable only after observing several cycles," child-beads B7 /
  design-doc Q4 line 314). The design itself admits the Refinery "has no
  path-scoped, body-asserting conditional auto-merge engine today" (B7
  "Implementation mechanism") — so B7 is net-new enforcement-engine code in
  `internal/refinery`, a wider blast radius than every other bead (the doc flags
  this itself: "B7 touches Refinery-internal policy code... its review scope and
  blast radius are wider," child-beads Notes line 376).
- **Why it matters.** This is the textbook anti-pattern the scope lens exists to
  catch: *building configurable machinery now for a capability that is OFF at
  launch and gated behind "after observation."* The MVP's defined posture is "all
  CRs require human approval" (Q4 line 314) — which is the **absence** of B7, not
  a use of it. Zero MVP behavior depends on B7 existing. Carrying it into this
  epic adds the highest-blast-radius, cross-package bead to the critical epic for
  no launch value, and couples the lane's ship date to Refinery-internal work.
- **Suggested resolution.** Delete B7 from this epic. File it as a standalone
  follow-up bead, explicitly blocked on "N cycles of human-reviewed tunes
  observed." The MVP lane ships with human-approval-only, exactly as the
  conservative default already specifies. (This also removes one of B3's two
  justifications — see #2.)

## Should Fix (important but not blocking)

### 2. The threshold-tune-as-config-CR path (and its B3 dependency) is the most expensive proposal kind and need not be in the first epic

- **Issue.** Q3 defines three proposal kinds. Two are cheap (new-rule **sketch
  bead** for a human; root-cause **hypothesis bead** — informational, no gate).
  The third — **threshold tune as a `daemon.json` config CR** — is the one that
  drags in real machinery: B3 (replay harness must learn to grade a config
  overlay, an otherwise-unneeded change to the replay harness), the replay-CI
  gating wiring, and the entire reason B7 exists.
- **Why it matters.** The cheap two kinds deliver the lane's headline value on
  their own: *"a nightly agent that reads precision data and surfaces ranked
  hypotheses + rule sketches for humans to act on."* That is shippable with no
  config-CR path, no B3, no replay-overlay grading, no auto-merge. The
  threshold-CR path is a second, higher-complexity product capability bundled
  into the MVP — it is "doing it properly" (mechanical config CR + replay gate)
  when "good enough" (agent files a threshold-suggestion as a `curio-proposal`
  bead a human edits, same as new-rule sketches) ships the signal immediately.
- **Suggested resolution.** Consider an MVP that lands proposal kinds as
  **beads only** (hypothesis + sketch, where a threshold suggestion is just
  another sketch bead a human turns into a config edit). Defer the config-CR
  landing path, B3 (replay overlay), and B7 to a "Retrospect phase 2" epic. If
  the team wants the config-CR path in v1, then B3 is correctly an MVP bead (the
  sequencing review is right that it gates the first live threshold CR, not just
  B7) — but then say so and accept the larger epic. The scope-honest framing is:
  **B3 is MVP-necessary iff config-CRs are MVP — and config-CRs are the most
  deferrable of the three kinds.**

### 3. B8 (proposal expiry + breaker-reset) solves a problem that cannot occur until the lane has run unattended for weeks — defer it

- **Issue.** B8 prevents the volume circuit breaker (Q7, ~10 open proposals) from
  permanently wedging the lane, and ages out stale proposals. The child-beads doc
  itself says "it can land any time after B5+B6" (Notes line 382) — i.e. it is
  not on the critical path.
- **Why it matters.** A nightly lane emitting ≤3 proposals/run, with dedup (B6)
  preventing re-proposal, reaches a 10-proposal backlog only after many days of a
  human ignoring the queue. The wedge is a real but *slow* failure mode with a
  trivial manual recovery (close some beads). Building auto-expiry + a
  breaker-open alert now is solving a problem the MVP will not hit in its first
  operational weeks.
- **Suggested resolution.** File B8 as a fast-follow, not part of the launch
  epic. Acceptable interim: the B5 breaker's `result:skipped` receipt is already
  observable in the daemon digest (design-doc line 453), so a wedged lane is not
  *silent* even without B8 — it is visible, just not auto-recovered. That is
  good-enough for MVP.

### 4. B6's proposal-target CI guard is a backstop the design itself ranks third — split it out

- **Issue.** B6 bundles three things: (a) the label scheme, (b) the cluster-key
  dedup convention, and (c) a **built** CI check that fails any CR proposing a
  rule targeting a `curio.*` series (air-gap layer 2). The design explicitly
  ranks this CI guard as a *backstop*: "Layer 1 means the self-referential data
  never reaches the agent, so layers 2-3 are backstops, not the primary defense"
  (Q5 line 348).
- **Why it matters.** (a) and (b) are MVP-load-bearing (without dedup the lane
  re-proposes nightly). The CI guard (c) is net-new enforcement code (a gate
  script inspecting CR diff + body) defending against a case that layer-1's
  substrate filter (B2) already prevents at source *and* the prompt (Q5 layer 3)
  re-states — for a lane where every CR is human-reviewed at MVP anyway. Building
  all three air-gap layers before go-live is defense-in-depth that is
  disproportionate to a low-frequency, human-gated lane.
- **Suggested resolution.** Split the CI guard into its own follow-up bead. Ship
  MVP with air-gap layers 1 (B2 substrate filter, mechanical, primary) + 3
  (prompt). Add the layer-2 CI guard when/if the auto-merge path (B7) lands —
  that is the moment a human is no longer in the loop and the mechanical CR-level
  guard actually earns its cost. (Note: this directly contradicts child-beads
  line 211, which makes B6's guard a hard precondition of B5. That coupling is
  only justified once CRs can merge without human review — i.e. with B7.)

## Observations (non-blocking)

- **Minimum viable epic.** The smallest lane that delivers value is **B0 → B1 →
  B2 → B4 → B5 + B6-lite (labels + dedup key only)** = ~5.5 beads, producing
  hypothesis and new-rule-sketch beads for humans. Deferred to a phase-2 epic:
  B3, B7, B8, and B6's CI guard. This roughly halves the critical-path bead count
  while preserving the core capability (precision-aware nightly proposal
  surfacing).

- **Even B0 is debatable as MVP-blocking — flag the bundled-capability seam.**
  The epic bundles two products: (i) *precision-driven threshold tuning* (needs
  B0's ledger, B3, the config-CR path) and (ii) *unresolved-cluster hypothesis
  surfacing* (needs only `curio_candidate`, which already exists). The digest's
  "Unresolved candidate clusters" section (Q2) renders without the ledger at all.
  A truly minimal first cut could ship (ii) alone — agent surfaces clusters as
  hypotheses — deferring even B0's daemon-side post-close reconciler. I am **not**
  recommending cutting B0 (the completeness review correctly makes it the
  prerequisite for the *tuning* capability, and B0 is now in the doc), but the
  team should consciously decide whether v1 is "tuning + hypotheses" or just
  "hypotheses." If timeline were halved, (ii)-only is the cut that ships.

- **Q1 run.sh kill-switch pre-check is mild redundancy, not creep.** Reading
  `patrols.curio.llm.enabled` even though "not slinging = lane off" already
  disables the lane is explicit defense-in-depth (design-doc line 164). It is
  cheap (one config read) so not worth cutting, but it is a second off-switch for
  a lane that an opt-in plugin install already gates — note it, keep it.

- **Sequencing/completeness overlap, deliberately not re-litigated here.** The L1
  ledger gap (B0) is the other reviews' Must-Fix; from a pure scope lens B0 is
  *necessary* work (not creep), so it is out of this dimension's verdict except
  as the bundled-capability seam noted above.

## Sources

- `.designs/curio-p3-retrospect-agent/design-doc.md` — reviewed 2026-06-12
- `.designs/curio-p3-retrospect-agent/child-beads.md` — reviewed 2026-06-12
- `.plan-reviews/ofo2g/{completeness.md,sequencing.md}` — sibling reviews, read to avoid duplication — accessed 2026-06-12

# Independent Adversarial Review B + Final Synthesis — Whole-Repo Gastown Audit

- **Bead:** gu-nid89.18 (epic gu-nid89: Whole-Repo Gastown Audit)
- **Date:** 2026-06-12
- **Reviewer:** polecat mutant (gastown_upstream) — Reviewer B + Synthesizer
- **Inputs:** 16 dimension reports in `reports/audit-2026-06/` + Reviewer A's
  assessment (`adversarial-review-A.md`, recovered from branch `land-capstone-17`).
- **Mandate:** Independently verify disputed findings, catch what Reviewer A
  missed or misjudged, then produce the final synthesis: confirmed findings with
  confidence ratings, a prioritized action list, convoy groupings, and an
  executive health assessment.

---

## Executive Summary

Gas Town is a **healthy, well-engineered codebase** with disciplined auditors.
Across 16 dimensions covering ~400K LOC, the audit surfaced **no critical
unaddressed vulnerability**, strong documentation hygiene (88–100% godoc in large
packages), clean `go vet`, zero `panic()` in non-test code, and a security
posture that is correct for its stated trust model. The real risk concentrates in
a **small number of HIGH-severity concurrency / data-loss bugs** in the
daemon/reaper/refinery hot paths — most of which already have filed beads, and
**several of which have already been fixed since the audit ran.**

**The single most important thing this synthesis adds over Reviewer A:** the audit
(and Reviewer A) verified against commit `a9afe585`, but the working tree has
since advanced to `089f7c04`, and **a wave of fixes has landed.** Reviewer A —
reading a frozen snapshot — rated several findings as live HIGH bugs that are now
**closed in source.** I re-verified every HIGH finding against *current* HEAD. The
net effect: the actionable backlog is **smaller than either the dimension reports
or Reviewer A imply**, and there is **one stale-open bead** (a real fix landed but
the bead was never closed) that creates a re-dispatch hazard.

**Overall audit confidence: 8.8 / 10.** I concur with Reviewer A's headline
(unusually trustworthy, verifiable discipline) and raise it a notch because my
independent spot-checks — deliberately sampling findings A did *not* check —
also held up, and because the auditors' own False-Positive sections proved sound.
The deductions are the same two A identified (no `govulncheck`, no `-race`/dynamic
verification) plus a **process gap A missed: findings were never reconciled
against current HEAD**, so the backlog carried already-fixed items.

---

## 1. Method — how Reviewer B differs from Reviewer A

Reviewer A spot-checked 19 findings against source at the frozen audit commit and
found 19/19 exact. To add *independent* signal rather than re-confirming the same
sample, I:

1. **Re-verified against current HEAD (`089f7c04`), not the audit commit
   (`a9afe585`).** This is the dimension A could not see from a static snapshot —
   and it changed the disposition of 6 HIGH findings.
2. **Sampled findings A did NOT spot-check** — D3 (circuit breaker HalfOpen), D5
   (restart-pending escalation), D2 (operatorStopped map), H3/H5 (doctor checks),
   the sling tripwire guard, the mail CC fan-out copy.
3. **Cross-checked bead status against source state** for every HIGH finding, to
   catch stale-open / stale-closed mismatches.
4. **Audited Reviewer A's own conclusions skeptically** — including its dedup
   table, its severity calibrations, and its gap list.

Source verified by direct read at branch `polecat/mutant/gu-nid89.18--mqa8r25k`
(HEAD `089f7c04`): `internal/daemon/{daemon,dolt_circuit_breaker,restart_pending_dog,convoy_manager}.go`,
`internal/reaper/reaper.go`, `internal/cmd/sling.go`, `internal/mail/router.go`,
`internal/doctor/{idle_timeout_check,stale_dolt_port_check}.go`,
`internal/wasteland/trust.go`, `docs/WASTELAND.md`, plus `git log`/`git show` on
the fix commits.

---

## 2. The finding Reviewer A missed: HEAD has moved, fixes have landed

The audit reports and Reviewer A both describe the world at `a9afe585`. Between
the audit and this synthesis, the following fixes **landed on the working branch**
(verified by `git log` + reading the post-fix source). A triager acting on the raw
reports or A's assessment would re-do work that is already done.

| Finding (report) | Bead | Fix commit | Status in source NOW |
|---|---|---|---|
| Refinery fabricated-SHA async merge (bugs-cfg H1) | gu-nid89.34 | `7485da44` | **FIXED + closed** |
| Bitbucket merge treats HTTP errors as success (bugs-cfg H2) | gu-nid89.35 | `743a98cf` | **FIXED + closed** |
| idle-timeout `--fix` blanks beads prefix (bugs-cfg H4) | gu-nid89.37 | `0e667573` | **FIXED + closed** |
| stale-dolt-port divergent resolver (bugs-cfg H5) | gu-nid89.38 | `c90c054f` | **FIXED + closed** |
| `polecatHasAutoSaveCommits` first-remote-only (bugs-pwr #4) | gu-1ufs3 | `005cd871` | **FIXED + closed** |
| reaper `liveTrackedContextExcludeJoin` omits `hooked` (bugs-pwr #1) | gu-6reia | `16ce65f7` | **FIXED + closed** |
| reaper `parentExcludeJoin` omits `hooked` (bugs-pwr #2) | **gu-gvwqx** | `16ce65f7` | **FIXED — but bead STILL OPEN** ⚠️ |
| docs: broken `gt config` commands (docs-drift) | gu-nid89.27 | `4fad451c` | **FIXED + closed** |

### 2.1 ⚠️ STALE-OPEN BEAD — gu-gvwqx (action required)

Commit `16ce65f7` ("include 'hooked' in issue-side exclude joins") fixed **both**
reaper exclude joins in one diff and its message explicitly references **both**
`gu-6reia` AND `gu-gvwqx`. But only `gu-6reia` was closed; **gu-gvwqx remains
OPEN.** I verified `internal/reaper/reaper.go:254` now reads
`pi.status IN ('open', 'hooked', 'in_progress')` — the fix is present, with a
regression assertion in `reaper_test.go`.

**Impact of leaving it open:** the witness zombie/reaper patrol can re-dispatch
gu-gvwqx to a new polecat, who will find nothing to fix (a spawn-storm seed —
exactly the failure mode the polecat completion protocol warns about). **This is
the single highest-priority, lowest-effort action in the whole synthesis:**
`bd close gu-gvwqx --reason "fixed by 16ce65f7 alongside gu-6reia; both
issue-side joins now include 'hooked', regression test added"`.

> I did not close it myself — closing another lineage's bead is outside this
> review's mandate and the close-reason should be confirmed by the epic owner —
> but it is flagged as P0-process here.

---

## 3. Independent verification results (Reviewer B sample)

All checks below are against current HEAD. **CONFIRMED-LIVE** = bug present in
source now. **CONFIRMED-FIXED** = landed since audit.

| # | Finding | Bead | My verdict | Evidence (HEAD) |
|---|---------|------|-----------|-----------------|
| 1 | Circuit breaker stuck HalfOpen | gu-nid89.41 | **CONFIRMED-LIVE** | `dolt_circuit_breaker.go` `Allow()` returns `true` unconditionally in HalfOpen; only `Record()` transitions state. A's claim holds; A did not source-check this one — I did. |
| 2 | restart-pending marked handled on failed escalate | gu-nid89.43 | **CONFIRMED-LIVE** | `restart_pending_dog.go:129` `d.escalate(...)` (signature `func (d *Daemon) escalate(source, message string)` — **returns nothing**, swallows errors) then `:130` unconditionally marks escalated. Exact. |
| 3 | Concurrent map write `isRigOperational` | gu-nid89.39 (PINNED) | **CONFIRMED-LIVE** | `daemon.go:~2856` still lazy-`make`+writes `missingRigBeadLogged`/`collisionRigBeadLogged` with no lock; maps still documented "no sync needed." |
| 4 | Concurrent map write `operatorStoppedRefineryLogged` | gu-nid89.40 | **CONFIRMED-LIVE** | `daemon.go:182` map present, `:2323` read/write path unsynchronized; same class as #3. |
| 5 | ConvoyManager `wg.Add(2)` + 3 goroutines | gu-nid89.19 | **CONFIRMED-LIVE** | `convoy_manager.go:347` `wg.Add(2)`; `:348/:349/:352` launch three; `runStartupSweep` self-`Add`s nowhere. |
| 6 | `gt sling <id>` missing tripwire guard | gu-nid89.32 | **CONFIRMED-LIVE** | grep: `isReferenceTripwireBeadInfo` count = `sling.go:0`, `sling_dispatch.go:1`, `sling_schedule.go:1`. Guard still absent from interactive path. |
| 7 | Mail CC N-duplicate fan-out | gu-nid89.33 | **CONFIRMED-LIVE** | `router.go:968/1274/1299/1580` all `msgCopy := *msg` (shallow); CC slice header shared. |
| 8 | WASTELAND trust table wrong | gu-nid89.26 | **CONFIRMED-LIVE** | `docs/WASTELAND.md:156-159` = Registered/Participant/…/Maintainer; `trust.go:32-46` = Drifter/Registered/Contributor/War Chief. Doc still wrong. |
| 9 | reaper `parentExcludeJoin` hooked | gu-gvwqx | **CONFIRMED-FIXED (stale-open)** | `reaper.go:254` includes `'hooked'`; see §2.1. |
| 10 | Refinery fabricated-SHA | gu-nid89.34 | **CONFIRMED-FIXED** | commit `7485da44`; bead closed. |

**Reviewer B hit rate on the live findings: 8/8 confirmed present, 2/2
confirmed fixed.** Zero hallucinations in my sample, matching A's experience on
the disjoint sample. Combined A+B independent coverage is now ~27 distinct
findings source-verified across both reviewers with **zero false positives** —
this is strong corroboration of the audit's reliability.

### Reviewer A's conclusions I independently re-examined and AGREE with

- **A's "DISPUTED = 0":** I found no hallucinated finding either. Confirmed.
- **A's severity calibration on refinery H1** ("fires only under
  `merge_strategy=pr`"): correct — and now moot, since the fix landed.
- **A's dedup table (§3):** spot-checked the ConvoyManager (perf C1 ==
  bugs-daemon == upstream PR1 == gu-nid89.19) and `RemoveDatabase` (sec #2/#6 ==
  beads M5 == gu-zl25s) collapses — both correct.
- **A's NEEDS-MORE-EVIDENCE set (perf Q2/Q4 multi-status flag, Q3 profile, index
  recs, govulncheck):** I concur these should not be filed as settled fixes.

### Reviewer A conclusions I'd REFINE

- A treated the bug-hunt findings as a live backlog. **Half of the bugs-cfg HIGHs
  and two bugs-pwr HIGHs are now fixed.** A's 8.7 is fair *for the snapshot*; the
  actionable backlog today is materially smaller.
- A's gap list is excellent but omits the **HEAD-reconciliation gap** — the most
  consequential process issue, because it directly caused the stale-open bead.

---

## 4. Final confirmed-findings register (by confidence, current state)

### 4a. HIGH confidence — LIVE, actionable now

| Finding | Bead | Pri | Class | Notes |
|---|---|---|---|---|
| Concurrent map write `isRigOperational` (daemon crash) | gu-nid89.39 | **P1** | concurrency/crash | PINNED. Highest crash-risk; `-race` would prove it. Fix: one `dedupMu` over all three dedup maps. |
| Concurrent map write `operatorStoppedRefineryLogged` | gu-nid89.40 | **P1** | concurrency/crash | Same root cause + same fix as .39. **Fix together.** |
| Circuit breaker stuck HalfOpen | gu-nid89.41 | **P1** | resilience | Re-amplifies bd load on a recovering Dolt — the exact thing it guards. |
| restart-pending marked handled on failed escalate | gu-nid89.43 | **P1** | error-handling/silent-loss | Reintroduces gu-muj66 (stale-code daemon). Fix: make `escalate` return error; gate the label on success. |
| ConvoyManager `wg.Add(2)`→`Add(3)` | gu-nid89.19 | **P1** | lifecycle race | One-liner; strongest upstream candidate (PR1). |
| `reapLeakedActContainers` docker calls no timeout | gu-nid89.42 | **P1** | availability | Hangs daemon main loop if Docker wedges. Use `CommandContext`. |
| SLOT_OPEN coalescer timer abandoned by short patrol | gu-uukrs | **P1** | notification loss | Mayor nudge/mail dropped every cycle; `Flush()` has no prod caller. |
| Merge-slot non-atomic RMW | gu-sz1xl | **P1** | data corruption | Latent under single-writer; CAS/flock fix. (A's calibration: fires only if single-writer breaks.) |
| nudge poller drops drained nudges on injection error | gu-nid89.31 | **P2** | loss | Mirror `propulsion.go` `Requeue`. Non-Claude agents only. |
| `gt sling <id>` missing tripwire guard | gu-nid89.32 | **P2** | eligibility/gate-loss | Add guard to `sling.go` matching the other two paths. |
| mail CC N-duplicate fan-out | gu-nid89.33 | **P2** | double-delivery | Strip CC on fan-out copies. |
| doctor hooks-path check skips new-layout polecats | gu-nid89.36 | **P2** | false-negative | Pre-push hook silently unchecked on nested clones. |

### 4b. HIGH/MED — LIVE docs & security (defense-in-depth)

| Finding | Bead | Pri | Notes |
|---|---|---|---|
| Stale OTEL design docs ("not on main") | gu-nid89.28 | **P1** | telemetry D4 + docs-drift agree; `Record*`/`run.id`/`agentlog` all shipped. |
| WASTELAND trust table wrong | gu-nid89.26 | **P1** | Doc names ≠ `trust.go`. |
| Broken relative links (`watchdog-chain.md`, `swarm-architecture.md` ×missing) | (file) | P2 | 11 links; 2 target files never existed. |
| `RemoveDatabase` dbName SQL injection | gu-zl25s | P2 | MEDIUM per threat model; `validSQLName` at entry covers #2+#6. |
| git ref flag-injection | gu-n5dvk | P2 | `validateGitRef` at choke point; external-data boundary. |
| dashboard SSRF `handlePRShow` | gu-4zl6k | P2 | Host-allowlist before `gh pr view`; matters because dashboard can bind `0.0.0.0`. |
| GROQ_API_KEY in `0644` settings + `.env` not gitignored | (sec-secrets #1/#2) | P2 | Local disclosure + commit-secret risk. Both unfiled — **file them.** |
| Lost-update RMW races (escalation/channel/queue) | gu-tucci | P2 | Shared CAS helper closes with gu-sz1xl. |
| wisps migration no transaction | gu-ivcpy | P2 | Partial-migration dual-write. |
| formula path traversal | gu-hpnjo | P3 | Read-only, CLI-trust. |

### 4c. MED — quality / perf (opportunistic)

- Perf, all LIVE, all filed or foldable: F1 `ps` fork storm (gu-nid89.20),
  Q1 N+1 `bd.Show()` (gu-nid89.21, fork-only — not upstreamable), Q2 `GetAssignedIssue`
  3× List (**gated on confirming `bd list` multi-status flag** — do not file the
  fix shape until verified).
- Quality extractions `runDone` (CC 228) / `runSling` (CC 203) — gu-nid89.12.1/.2.
  **Real but high-risk refactors on the most incident-prone files; sequence
  carefully, do not batch with bug fixes.**
- Dead code ~74 symbols (gu-nid89.12.3), reaper-mail dedup (gu-nid89.12.4),
  `internal/convoy` extraction (gu-nid89.12.5).

### 4d. NEEDS-MORE-EVIDENCE (do not file as settled) — concurring with Reviewer A

- `bd list` multi-status flag capability (perf Q2/Q4) — confirm before filing fix.
- perf Q3 per-rig MR-count hotness — needs a profile.
- Dolt index recommendations — schema never inspected.
- **govulncheck never ran** (DNS sinkhole) — the single biggest coverage hole.
- staticcheck never ran town-wide (go1.25 binary vs go1.26.2 tree).
- No `-race` / dynamic verification anywhere.

### 4e. Telemetry KPI catalog & feature proposals — NOT defects

Per A: KPI-1..KPI-10 and the quality extractions are feature/refactor work, **not
the defect backlog.** Filed telemetry beads (gu-nniyx, gu-y7p6j, gu-dnkz4,
gu-pkhxh, gu-nate5, gu-ojvc7) are instrumentation enhancements — track
separately.

---

## 5. Prioritized action list (what to fix first)

**P0 — process, do immediately (minutes):**
1. **Close gu-gvwqx** — fixed by `16ce65f7`, stale-open, re-dispatch hazard (§2.1).
2. **Reconcile all finding-beads against HEAD** before any fix dispatch — confirm
   .34/.35/.37/.38/gu-1ufs3/gu-6reia/.27 are closed (they are) and that no other
   fix landed without closing its bead. (gu-gvwqx is the one that slipped.)

**P1 — daemon stability convoy (highest crash/availability risk):**
3. gu-nid89.39 + gu-nid89.40 — **one shared mutex fix** for both map races. Add a
   `-race` reproduction (cheap, proves the crash). A's recommendation #5.
4. gu-nid89.41 — circuit breaker HalfOpen gating.
5. gu-nid89.42 — docker timeout on `reapLeakedActContainers`.
6. gu-nid89.43 — `escalate` returns error; gate the handled-label.
7. gu-nid89.19 — ConvoyManager `Add(3)` (also ship upstream).
8. gu-uukrs — SLOT_OPEN flush on patrol exit.

**P1 — docs (user/agent-facing, cheap):**
9. gu-nid89.28 (OTEL drift), gu-nid89.26 (WASTELAND), broken links. Agents copy
   `dolt-health-guide.md` during real incidents — the `gt escalate -m`→`-r` fix
   already landed via the docs sweep; verify reference.md/escalation.md siblings.

**P2 — mail/nudge/dispatch correctness convoy:**
10. gu-nid89.31 (nudge requeue), gu-nid89.32 (sling tripwire), gu-nid89.33 (CC dup),
    gu-nid89.36 (hooks-path layout).

**P2 — beads atomicity convoy:**
11. gu-sz1xl + gu-tucci + gu-ivcpy — one shared CAS/locked-update helper closes
    the merge-slot and counter RMW races; migration transaction is independent.

**P2 — security convoy:**
12. gu-zl25s (dbName), gu-n5dvk (git refs), gu-4zl6k (SSRF), + **file the two
    unfiled secrets beads** (GROQ key 0644, `.env` gitignore).

**P2 — audit-tooling (prerequisites for next audit, per A #3):**
13. Run `govulncheck` from a proxy-capable host; rebuild staticcheck for go1.26
    and run town-wide. File both as tooling beads.

**P3 — quality (opportunistic, high-risk-sequenced):**
14. gu-nid89.12.1/.2 (runDone/runSling extraction) — one per sprint, never
    bundled with behavior changes; .12.3 dead-code (keep 4 test hooks),
    .12.4 dedup, .12.5 convoy extraction.

**Re-run now (A's #1, highest follow-up value):**
15. **Re-run upstream-pr-candidates** — it synthesized only 3 of 16 dimensions.
    Strong newly-eligible upstream PRs: ConvoyManager Add(3), nudge requeue,
    mail CC dedup, force-push `git -c` guard (M15), `gt status` writer (H3),
    compact-weekly idempotency (H2), checkpoint title (H1), mq submit-step (H4),
    `ps` fork storm (F1). **Exclude fork-only** (reaper N+1 gu-nid89.21) and
    **already-landed-locally** fixes that may differ from upstream state.

---

## 6. Recommended convoy groupings for follow-up dispatch

Group by shared-fix / shared-subsystem so one polecat lands a coherent diff and
the gate suite runs once:

| Convoy | Beads | Rationale |
|---|---|---|
| **daemon-concurrency** | gu-nid89.39, .40, .41 | Two map races share one mutex fix; breaker is same file-family. `-race` gate. |
| **daemon-resilience** | gu-nid89.42, .43, gu-uukrs | Availability/notification-loss in daemon dogs; independent diffs but one reviewer context. |
| **lifecycle-oneliner** | gu-nid89.19 | Standalone one-liner; also the lead upstream PR. |
| **mail-dispatch-correctness** | gu-nid89.31, .32, .33, .36 | All in mail/nudge/sling/doctor dispatch surface. |
| **beads-atomicity** | gu-sz1xl, gu-tucci, gu-ivcpy | Shared CAS helper closes the first two; migration txn rides along. |
| **security-hardening** | gu-zl25s, gu-n5dvk, gu-4zl6k, gu-hpnjo, +2 new secrets beads | All defense-in-depth validation; one threat-model framing. |
| **docs-accuracy** | gu-nid89.26, .28, broken-links | Doc-only; no gate risk; fast. |
| **audit-tooling** | govulncheck, staticcheck-go1.26 | Prereqs for next audit; not code fixes. |
| **quality-extraction** (sequential, NOT parallel) | gu-nid89.12.1 → .12.2 → .12.5; .12.3/.12.4 parallel-safe | High-churn incident-prone files; one-per-sprint, never with behavior changes. |

**Convoy hygiene note:** before dispatching any convoy, run the HEAD-reconciliation
(P0 #2) so no polecat is slung a bead whose fix already landed.

---

## 7. Overall codebase health assessment

**Grade: A− / 8.8 confidence.**

**Strengths (verified, not asserted):**
- Documentation is a genuine strength — 88–100% godoc in large packages, accurate
  agent-instruction docs, near-zero stale-annotation debt.
- Security is correctly calibrated to a documented trust model; no CRITICAL, no
  hardcoded secrets, hardened proxy (mTLS + env allowlist).
- `go vet` clean, zero `panic()`, defensive nil-guards and flock discipline
  pervasive; auditors recorded their own rejected claims (False-Positive sections).
- The team **fixes fast** — between audit and synthesis, ~8 HIGH findings landed
  fixes. This is a high-velocity, self-correcting codebase.

**The real risk surface (small, concentrated):**
- A handful of **daemon concurrency / data-loss HIGHs** on the hot path — the
  map races (.39/.40) are the most likely to actually crash production and are
  the top priority. Most are one-token or one-line fixes.
- **Dolt is the systemic fragility** — corroborated by both log-archaeology
  reports: imposter-server spawning (gu-3phku P0 in_progress, gc-6lzjy2 open) and
  the convoy-auto-close connection leak (re-filed ~10×). These are *operational*
  failure surfaces partly outside this audit's code scope but dominate real
  incidents. Highest real-world leverage: make the imposter impossible and add a
  connection-leak regression guard.

**What would move it to A:**
1. Run the two automated deep-static legs that never ran (`govulncheck`,
   `staticcheck` on go1.26) and a targeted `-race` pass on the daemon maps.
2. Close the Dolt imposter + connection-leak systemic items.
3. Adopt a **HEAD-reconciliation step** in the audit→fix pipeline so the backlog
   never carries already-fixed findings (the gu-gvwqx slip is the cost of its
   absence).

---

## Sources

- All 16 dimension reports under `reports/audit-2026-06/` — read 2026-06-12.
- `reports/audit-2026-06/adversarial-review-A.md` (gu-nid89.17), recovered from
  branch `land-capstone-17` — read 2026-06-12.
- Source independently verified at HEAD `089f7c04` (branch
  `polecat/mutant/gu-nid89.18--mqa8r25k`): `internal/daemon/{daemon,dolt_circuit_breaker,restart_pending_dog,convoy_manager}.go`,
  `internal/reaper/reaper.go`, `internal/cmd/sling.go`, `internal/mail/router.go`,
  `internal/doctor/{idle_timeout_check,stale_dolt_port_check}.go`,
  `internal/wasteland/trust.go`, `docs/WASTELAND.md` — accessed 2026-06-12.
- Fix commits verified via `git log`/`git show`: `7485da44`, `743a98cf`,
  `0e667573`, `c90c054f`, `005cd871`, `16ce65f7`, `4fad451c` — accessed 2026-06-12.
- Bead statuses via `bd show <id>` across gu-nid89.* and gu-{gvwqx,6reia,1ufs3,
  uukrs,sz1xl,tucci,ivcpy,zl25s,n5dvk,4zl6k,hpnjo} — accessed 2026-06-12.

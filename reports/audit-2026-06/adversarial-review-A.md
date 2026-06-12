# Independent Adversarial Review A — Whole-Repo Gastown Audit

- **Bead:** gu-nid89.17 (epic gu-nid89: Whole-Repo Gastown Audit)
- **Date:** 2026-06-11
- **Reviewer:** polecat chrome (gastown_upstream) — Reviewer A
- **Mandate:** Skeptically re-assess every dimension report in `reports/audit-2026-06/`.
  Assume auditors hallucinated unless verifiable in source. Rate findings
  CONFIRMED / DISPUTED / NEEDS-MORE-EVIDENCE. Produce an overall confidence score.

## Method

I read all 14 dimension reports end-to-end (4,323 lines), then **spot-checked 19
findings by reading the actual source** (the acceptance bar was ≥5). Spot-checks
were deliberately spread across every report and weighted toward the
highest-severity / highest-blast-radius claims, since those are the most
expensive to get wrong. For each I traced the cited file:line, confirmed the
control flow, and — where the finding asserted a *consequence* (data loss,
concurrency crash, guard bypass) — verified the consequence path, not just the
surface code pattern.

Reports reviewed: `bugs-cmd`, `bugs-daemon`,
`bugs-config-doctor-hooks-formula-refinery`, `bugs-polecat-witness-reaper`,
`bugs-mail-nudge-sling`, `bugs-beads-dolt`, `docs-drift`, `docs-inline-godoc`,
`security-secrets`, `security-injection`, `perf-runtime`, `quality-structural`,
`telemetry-signals`, `logs-town-events`, `logs-dolt-archive`,
`upstream-pr-candidates`.

---

## Headline verdict

**The audit is unusually trustworthy.** Of 19 independently source-verified
findings spanning all six bug-hunt subsystems plus perf, security, docs,
quality, telemetry, and logs, **19/19 held up exactly as described** — zero
hallucinations, zero misread line numbers, zero invented APIs. The reports are
disciplined: each one carries an explicit "verified against source" claim per
HIGH finding, several include a *False Positives* section recording claims that
did **not** survive their own re-tracing (e.g. `bugs-beads-dolt` correctly
downgraded two "SQL injection" HIGHs to LOW and killed a connection-leak claim),
and the perf report visibly downgraded three of its own sweep findings on
verification. This is the signature of auditors who actually re-read the code
rather than pattern-matching.

The issues I *did* find are not false positives — they are **severity /
blast-radius calibration** points where a finding is real but its real-world
impact is narrower (or wider) than the headline implies, and a handful of
**cross-report duplications** and **scope gaps**. None of these undermine the
audit's core conclusions.

**Overall audit confidence: 8.7 / 10** (see §6 for the breakdown).

---

## 1. Findings rated by verification

### 1a. CONFIRMED — verified in source, exactly as described (19)

| # | Report | Finding | What I verified |
|---|--------|---------|-----------------|
| 1 | bugs-cmd H3 | `gt status` agent-runtime line uses `fmt.Printf` not `Fprintf(w,…)` | `status.go:1378` is `fmt.Printf`; the immediately-preceding line 1374 uses `fmt.Fprintf(w,…)`. Exact. |
| 2 | bugs-cmd H1 | Checkpoint step title clobbered by `WithMolecule(…, "")` | `checkpoint_cmd.go:127-134` guards the title set then unconditionally re-calls with `""`; `checkpoint.go:164-168` assigns `cp.StepTitle = stepTitle` unconditionally. Exact. |
| 3 | bugs-cmd H4 | `validateMoleculePrereqs` dead `break` kills min-search | `mq_submit.go:570-578`: `break` is unconditional inside `if Contains("submit")`, so `if seq < submitSeq` only ever sees the first match. Exact. |
| 4 | bugs-cmd M15 | Force-push guard fails open with `git -c`/`git -C` prefix | `tap_guard_dangerous.go:225` requires `fields[i-1]=="git"`; a `-c k=v` token between `git` and `push` defeats it. Exact. |
| 5 | bugs-cmd M3 | `gt assign --force` declared but never read | `assign.go:51,62` declare+register `assignForce`; no other reference. Exact. |
| 6 | bugs-daemon D1 | Concurrent map writes in `isRigOperational` | `daemon.go:2856-2887` lazy-`make`+write `missingRigBeadLogged`/`collisionRigBeadLogged`, documented "no sync needed" (`:160-172`); reached from `ensureWitnessRunning`→`isRigOperational` inside `rigPool.runPerRig`'s per-rig goroutine fan-out (`worker.go:51`, one goroutine per rig under a semaphore). The "heartbeat-only" invariant is genuinely false. **HIGH severity is correct.** |
| 7 | bugs-daemon D4 | `reapLeakedActContainers` docker calls have no timeout, run inline on main loop | `main_branch_test_runner.go:1191-1222` uses bare `exec.Command("docker",…)` (vs `CommandContext` everywhere else in the file); `daemon.go:911-916` calls `runMainBranchTests` inline in the main `select`. Exact. |
| 8 | bugs-mns F1/H1 | Nudge poller drops drained nudges on injection error | `nudge_poller.go:135-147` logs-and-continues on `NudgeSessionWithOpts` error; `propulsion.go:222` (the sibling) calls `nudge.Requeue`. Asymmetry exact. |
| 9 | bugs-mns H2 | `gt sling <id>` missing reference-tripwire guard | `isReferenceTripwireBeadInfo` present only in `sling_dispatch.go:307` + `sling_schedule.go:273`, absent from `sling.go`. Exact. |
| 10 | bugs-mns H3 | CC recipient gets N duplicate fan-out copies | `router.go:967` `msgCopy := *msg` is a shallow copy; the `CC []string` slice header is shared so every fan-out copy carries the full CC list. Exact. |
| 11 | bugs-pwr #1/#2 | Reaper exclude-joins omit `'hooked'` on issues side | `reaper.go:254` and `:287`: wisp side has `('open','hooked','in_progress')`, issues side has only `('open','in_progress')`. Both helpers, exact. |
| 12 | bugs-pwr #4 | `polecatHasAutoSaveCommits` returns after first remote | `handlers.go:1466` `return false, nil` sits inside the `for _, remote` loop, after the inner scan — only `remotes[0]` is ever inspected. Exact. |
| 13 | bugs-cfg H1 | Refinery `doMergePR` fabricates SHA for async (CRUX) merge | `engineer.go:1069-1075`: when `mergeCommit==""` it sets `mergeCommit = Rev("HEAD")` (post-pull tip) then `VerifyPushedCommit` passes trivially; `pr_provider_crux.go:79-99` `MergePR` literally `return "", nil` with comment "CRUX does not surface a merge commit SHA synchronously." Exact. **(severity calibration in §2.1)** |
| 14 | bugs-beads H1 | Merge-slot acquire/release is non-atomic RMW | Confirmed pattern in `beads_merge_slot.go`; mitigated by single-Engineer-per-rig assumption, which the report correctly states. |
| 15 | perf C1 / bugs-daemon | ConvoyManager `wg.Add(2)` + 3 goroutines | `convoy_manager.go:347` `wg.Add(2)`, lines 348/349/352 launch three goroutines; `runStartupSweep` (`:1513`) doesn't self-`Add`. Exact. |
| 16 | perf F1 | Per-candidate `ps` fork storm | `orphan.go:342` and `:452` each `exec.Command("ps","-p",pid,"-o","args=")`, called per candidate. Exact. |
| 17 | security-secrets #1 | Live `GROQ_API_KEY` resolved into persisted settings | `cost_tier.go:267-269` replaces the `$GROQ_API_KEY` sentinel with `os.Getenv("GROQ_API_KEY")` at preset-creation; `loader.go:1136` writes `0644`. Exact. |
| 18 | security-secrets #2 / security-injection #2 | `.env` not git-ignored; `RemoveDatabase` interpolates unescaped `dbName` | `git check-ignore .env` → rc=1 (not ignored); `doltserver.go:3580` `DELETE … WHERE \`database\` = '%s'` and `:3572` `DROP DATABASE IF EXISTS \`%s\`` both unescaped. Exact. |
| 19 | docs-drift / quality / docs-inline | `watchdog-chain.md`+`swarm-architecture.md` missing; go.mod is `1.26.2` (README says "Go 1.25+"); WASTELAND trust labels wrong | All confirmed: both docs absent; `go.mod:3 go 1.26.2`; `trust.go:32-55` = Drifter/Registered/Contributor/War Chief (doc says Registered/Participant/Contributor/Maintainer). |

**Hit rate: 19/19 (100%).** Extrapolating, the audits' HIGH/MED findings are
highly reliable. I found no fabricated file, no invented function, and no
line-number that was off.

### 1b. CONFIRMED-but-severity-calibrated (3) — real bugs, narrower or wider blast radius than headline

See §2.

### 1c. NEEDS-MORE-EVIDENCE (4) — plausible, but the report itself flags an unverified assumption

These are *honestly* flagged by the auditors as unconfirmed; I'm formalizing them
as NEEDS-MORE-EVIDENCE so the synthesis (gu-nid89.18) and any fix-bead author
don't treat them as settled:

- **perf Q2 / Q4 (multi-status `bd list` flag).** The proposed fix (collapse 3
  sequential `List` calls into one multi-status query) depends on `bd list`
  accepting a comma-separated status. The report explicitly says this "needs a
  quick check before implementing." **Do not file the fix as written until the
  flag capability is confirmed.** The underlying inefficiency (3 round-trips) is
  CONFIRMED; the *fix shape* is unverified.
- **perf Q3 (per-rig MR-count query is actually hot).** Report says "Confirm with
  a profile before investing — tick cadence and rig count determine whether this
  is actually hot." No profile was run (the report's own caveat: "Impact
  estimates are reasoned from call cadence … not from profiling"). Reasonable
  lead, unproven cost.
- **perf index recommendations.** Report explicitly: "I did **not** inspect the
  Dolt schema, so I cannot confirm which indexes already exist." Correctly
  self-flagged; treat as a lead, not a finding.
- **security-injection #8 (govulncheck).** Could not run (corp DNS sinkhole
  blocks `proxy.golang.org`). The manual dep review is sound as far as it goes,
  but **no authoritative CVE scan exists for this tree.** This is the single
  biggest *coverage hole* in the security dimension and must be re-run from a
  proxy-capable host. NEEDS-MORE-EVIDENCE by the auditor's own admission.

### 1d. DISPUTED (0)

**I found no finding that is wrong, hallucinated, or already-fixed-but-reported-as-open.**
The closest candidates (refinery H1 blast radius, log-archaeology noise
classification) are calibration/heuristic notes, not disputes — the underlying
code claims are all correct. The auditors' own *False Positives* sections
(notably `bugs-beads-dolt`) already caught the claims that would otherwise have
been disputable, which is exactly the discipline this review was meant to
enforce.

---

## 2. Severity / blast-radius calibration

These findings are **real and correctly traced**, but a fix-prioritizer should
understand the true exposure.

### 2.1 Refinery `doMergePR` fabricated-SHA data loss (bugs-cfg H1 / bugs-pwr context) — severity correct, blast radius **conditional on config**

The bug is real and the data-loss path (close beads + delete polecat branch on a
merge that never happened) is correctly traced. **Calibration:** `doMergePR` only
runs when `MergeStrategy == "pr"` (`engineer.go:791`). The **default is
`"direct"`** (`engineer.go:184`), and *this* town (gastown/beads repos) lands via
the Refinery merge-queue with direct push — so `doMergePR` is **not on the hot
path here**. It IS the active path for any rig configured `merge_strategy=pr`
with a GitHub/Bitbucket/Amazon-CRUX remote (the PR-workflow repos — longeye and
similar). So:

- For **PR-workflow rigs: P1, genuine silent work-loss** — the headline severity
  is fully justified.
- For **this town's direct-merge rigs: latent** — won't fire until someone flips
  a rig to `merge_strategy=pr`.

The bugs-cfg report's framing ("strands unmerged work") is accurate; I'd just
annotate the filed bead (gu-nid89.34) with "fires only under `merge_strategy=pr`"
so a triager doesn't over- or under-prioritize it. The sibling Bitbucket finding
(H2/gu-nid89.35) is the same class and same conditionality.

### 2.2 bugs-beads H1/H2 non-atomic RMW (merge slot, counters) — severity rests on a stated assumption

Correctly traced and the report is admirably explicit that correctness "rests on
an *unstated single-writer assumption*" (one Engineer per rig). I confirmed
`OpenDB` (`reaper.go:219`) sets no `MaxOpenConns(1)`, so the sibling
autocommit-over-pool concern (bugs-pwr #10) is structurally real too. **These are
correctly MED/HIGH-with-caveat, not unconditional HIGH** — they bite only if the
single-writer invariant ever breaks (two refineries, stale session, manual
`gt mq`). The report says exactly this. No change needed; flagging so the
synthesis doesn't escalate them to "actively corrupting today."

### 2.3 logs-town-events noise classification — heuristic, correctly self-flagged

The "23% synthetic/test noise" and `myr/mycat` phantom-loop findings are
heuristic (rig absent from `~/gt/` + placeholder IDs). I confirmed `myr` is **not**
a configured rig dir on this host, which supports the phantom hypothesis. The
report's own caveat ("if any is a real but mis-tagged rig, its events would
reclassify as signal") is the right hedge. CONFIRMED as a heuristic finding;
counts are exact, classification is sound-but-heuristic.

---

## 3. Duplicates & overlaps (cross-report and vs. existing beads)

The audit dimensions overlap by design (e.g. perf C1 == bugs-daemon's reference
to gu-nid89.19; perf F1 == bugs-cmd's orphan-fork class). The auditors mostly
cross-referenced cleanly, but the synthesis (gu-nid89.18) should **dedupe before
filing** to avoid double-counting:

| Finding | Appears in | Single bead |
|---|---|---|
| ConvoyManager `wg.Add(2)`→`Add(3)` | perf C1 **and** bugs-daemon (refs gu-nid89.19) **and** upstream PR1 | gu-nid89.19 (already exists) |
| Per-candidate `ps` fork storm | perf F1 **and** upstream PR6 | gu-nid89.20 (filed) |
| N+1 `bd.Show()` reaper reconcile | perf Q1 **and** upstream "not upstreamable" | gu-nid89.21 (filed, fork-only — correct) |
| `RemoveDatabase` unescaped `dbName` | security-injection #2/#6 **and** bugs-beads M5 (notes same `dbName` not escaped) **and** logs-dolt "remote-URL validation" gu-zl25s | gu-zl25s (filed) — one fix covers all |
| Reaper autocommit-over-pool | bugs-pwr #10 **and** bugs-cmd M9 (reaper throttle) — adjacent, not identical | keep separate |
| OTEL doc drift ("not on main") | docs-drift HIGH **and** telemetry D4 **and** existing gu-nid89.28 | gu-nid89.28 (already exists) — both reports point at it |
| `gt escalate -m` nonexistent flag | docs-drift (reference.md + dolt-health-guide.md, 2 sites) | one docs bead, fix both sites |

**No finding is a duplicate of a *fix already landed*** that I could detect — the
reports that touch already-tracked beads (gu-nid89.19, .28, gu-zl25s, gu-msz5t,
gu-5ja0e) correctly mark them as existing rather than re-filing. The
logs-dolt-archive report is exemplary here: it cross-references existing trackers
and files only the two genuinely-untracked systemic gaps (gu-s2l9t escalation
storm, gu-d1r8g leak-regression guard).

---

## 4. Actionability gaps (findings too thin to act on as-is)

Most findings include a `Fix sketch` + file:line and are directly actionable.
The exceptions:

1. **bugs-cmd M2 / upstream PR10 (`--max-concurrent` throttle loop).** The fix
   requires knowing the *intended* pause interval (6s? 60s? configurable?), which
   "isn't obvious from the code." Actionable only after a maintainer decides the
   semantics. The bug (loop always breaks at 3rd iter) is CONFIRMED-shaped from
   the report; the fix is a judgment call. Correctly flagged P3/optional.
2. **perf Q2/Q4** — see §1c; fix blocked on the `bd list` multi-status flag check.
3. **telemetry KPI catalog (§4 of that report)** — these are *proposals*, not
   defects (except D1–D6, which are concrete and actionable). The proposals are
   well-specified (emit site, format, aggregation) but are net-new feature work,
   not audit findings; the synthesis should not count KPI-1..KPI-10 as "bugs."
4. **quality-structural extraction beads (gu-nid89.12.1-.5)** — `runDone` CC 228 /
   `runSling` CC 203 are real and measured, but "extract into phase functions" is
   a large refactor on the two most incident-prone files. Actionable but high-risk;
   sequence carefully (the report's own recommendation).

---

## 5. Gaps — what the auditors missed or under-examined

The audit is broad, but coverage is uneven:

1. **No authoritative vulnerability scan.** `govulncheck` never ran (DNS
   sinkhole). The large `dolthub/*` transitive tree (vitess, go-mysql-server) is
   exactly where a real CVE would hide, and it was only eyeballed. **This is the
   most important gap.** → re-run from a proxy-capable host (security-injection
   already recommends this; elevate it).
2. **`staticcheck` ran nowhere.** Three separate reports (bugs-cmd, quality,
   docs-inline) note staticcheck is unrunnable (go1.25 binary vs go1.26.2 tree).
   So the *static-analysis* leg of the bug hunt rests entirely on `go vet` (clean)
   + golangci-lint (`unused`/`gocyclo`/`dupl` only) + manual read. The deeper
   staticcheck checks (SA-class: nil derefs, impossible conditions, lock
   mistakes) were **never executed town-wide**. Manual review is good but not a
   substitute. → rebuild staticcheck against go1.26 and run it; treat as a
   coverage gap, not a finding.
3. **No runtime/dynamic verification anywhere.** Every report is static read. The
   concurrency findings (D1 concurrent-map-write, C1 WaitGroup race) are textbook
   `go test -race` candidates but no race detector was run. The findings are
   sound by inspection; a `-race` run would *prove* D1 and is cheap. → recommend
   a targeted `-race` reproduction for D1/D2 before/with the fix.
4. **`internal/web/` lightly examined for logic bugs.** Security-injection covered
   its SSRF/XSS surface well, but no *bug-hunt* dimension owns `internal/web/`
   (6.6K LOC) — it falls between the cmd/daemon/subsystem splits. Possible
   correctness bugs there are uncovered.
5. **`internal/acp/` + `agent/provider/`** flagged by docs-inline as 0–11% doc
   coverage and by quality as having dead code, but **no bug-hunt dimension read
   them for logic defects.** The ACP layer is the non-Claude-agent integration
   path — under-examined relative to its risk.
6. **Test quality not assessed.** The audit hunts product bugs but never asks
   whether the *tests* that gate merges are themselves correct/adequate (e.g. are
   the concurrency paths in D1/C1 covered at all?). Given `runDone`'s incident
   history, test-coverage-of-hot-paths is a worthwhile dimension that's missing.
7. **upstream-pr-candidates is explicitly partial.** It synthesized only 3 of ~10
   dimensions (the ones filed at the time) and says so loudly. It should be
   **re-run now** that all dimensions have landed — several CONFIRMED findings
   here (refinery H1, reaper `hooked` omission, nudge requeue, CC duplication) are
   strong upstream candidates that the partial run never saw. **This is the
   highest-value follow-up for gu-nid89.18.**

---

## 6. Overall confidence score

**8.7 / 10.**

| Dimension | Confidence | Rationale |
|---|---|---|
| Bug-hunt (cmd/daemon/cfg/pwr/mns/beads) | **9.2** | 13/13 spot-checks exact; explicit per-finding verification + False-Positive sections; severity calibrations are minor. |
| Performance | 8.5 | Findings exact, but several rest on un-profiled cadence assumptions (Q2/Q3/Q4, index leads) the report honestly flags. |
| Security | 8.0 | Secrets + injection review is sharp and threat-model-calibrated, but **no govulncheck** and no staticcheck = a real coverage hole on supply chain / deep static. |
| Docs (drift + inline) | 9.0 | Drift findings verified against the actual binary (`gt --help`); broken links + missing docs confirmed; inline-doc numbers reproducible via AST. |
| Code quality | 8.5 | golangci-lint numbers reproducible; CC/dead-code spot-checks held; staticcheck gap noted by the auditors themselves. |
| Telemetry | 8.0 | Current-state map is accurate; dead-signal claims (D1-D6) verified-shaped; KPI catalog is proposal-grade, not defect-grade. |
| Log archaeology | 8.5 | Counts exact (full-file parse); root causes are co-occurrence *hypotheses* (correctly hedged); phantom-rig heuristic supported by `myr` absence. |
| Upstream PRs | 8.5 | upstream/main re-verification is genuine (I confirmed PR1 line-for-line); docked only for being an explicitly-partial synthesis. |

**What earns the high score:** verifiable discipline. Every report I checked told
the truth about the code; the ones that couldn't verify something said so; and at
least two reports actively recorded claims they *rejected* during their own
review. That is the opposite of hallucination.

**What keeps it below 9:** the audit is almost entirely *static and manual*. The
two automated deep-static legs that would catch a different class of bug
(staticcheck SA-checks, govulncheck CVEs) never ran, and no dynamic/`-race`
verification was done. The findings that exist are trustworthy; the question is
what a green-field `staticcheck`/`govulncheck`/`-race` pass would *add*.

---

## 7. Recommendations for the synthesizer (gu-nid89.18)

1. **Re-run upstream-pr-candidates** against all 14 dimensions — it only saw 3.
   Strong new candidates: reaper `hooked` omission (data loss, tiny diff), nudge
   poller requeue, mail CC duplication, refinery fabricated-SHA (gate on
   `merge_strategy=pr`).
2. **Dedupe before filing** per the §3 table — don't double-count C1/F1/Q1 across
   perf + bugs-daemon + upstream.
3. **Close the two automated-coverage gaps**: rebuild staticcheck for go1.26 and
   run it; run `govulncheck` from a proxy-capable host. File both as audit-tooling
   beads — they're prerequisites for *next* time, not optional.
4. **Annotate conditional-severity findings**: refinery H1/H2 fire only under
   `merge_strategy=pr`; beads RMW races fire only if single-writer breaks. Don't
   let triagers over-prioritize latent-here / under-prioritize live-elsewhere.
5. **Add a `-race` reproduction** for daemon D1/D2 (concurrent map write) — it's
   the most likely to actually crash the daemon in production and is cheap to
   prove.
6. **Do not file telemetry KPI-1..10 or the quality extractions as "bugs"** —
   they're feature/refactor work; separate them from the defect backlog.

---

## Sources

- All 14 dimension reports under `reports/audit-2026-06/` — read 2026-06-11.
- Source verified by direct read at branch `polecat/chrome/gu-nid89.17--mqa5jdc0`
  (base `main` @ 97915a00): `internal/cmd/{status,checkpoint_cmd,mq_submit,tap_guard_dangerous,assign,nudge_poller,sling,sling_dispatch,sling_schedule}.go`,
  `internal/checkpoint/checkpoint.go`,
  `internal/daemon/{daemon,worker,convoy_manager,main_branch_test_runner}.go`,
  `internal/refinery/{engineer,pr_provider_crux}.go`,
  `internal/reaper/reaper.go`, `internal/witness/handlers.go`,
  `internal/mail/router.go`, `internal/util/orphan.go`,
  `internal/config/cost_tier.go`, `internal/doltserver/doltserver.go`,
  `internal/wasteland/trust.go`, `go.mod`, `.gitignore` — accessed 2026-06-11.
- `upstream/main` `convoy_manager.go` (PR1 cross-check) — accessed 2026-06-11.

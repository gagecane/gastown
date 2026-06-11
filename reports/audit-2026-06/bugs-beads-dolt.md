# Bug Audit — `internal/beads/` + `internal/doltserver/`

**Bead:** gu-nid89.3 (Audit: Bug hunt — beads & doltserver)
**Date:** 2026-06-11
**Auditor:** polecat gastown_upstream/chrome
**Scope:** `internal/beads/` (12.7K LOC non-test) + `internal/doltserver/` (8.0K LOC non-test) = ~20.7K LOC
**Bug classes hunted:** data corruption, transaction handling, query injection, connection leaks, migration failures, orphan data, error handling.

## Method

Parallel review across 7 file-slices, every finding then re-verified against the
actual source before inclusion. Findings are rated **HIGH** (definite bug with a
defensible failure path), **MED** (likely bug, severity depends on runtime
assumptions), **LOW** (smell / latent risk). Claims that did not survive
verification are recorded in the **False Positives** section so reviewers don't
re-litigate them.

---

## HIGH-confidence findings

### H1 — Merge slot acquire/release is a non-atomic read-modify-write (lost claims)
- **File:** `internal/beads/beads_merge_slot.go:103-163` (`MergeSlotAcquire`), `:167-202` (`MergeSlotRelease`)
- **Class:** data corruption / concurrency (TOCTOU)
- **What:** The merge slot is the *serialization primitive* for the Refinery merge
  queue, yet acquiring it is a plain read-modify-write with no lock, transaction, or
  compare-and-set:
  ```go
  issue, _ := b.getMergeSlotBead()      // read
  data := parseMergeSlotData(issue)     // decode
  if data.Holder != "" && data.Holder != holder { ... return held ... }
  data.Holder = holder                  // modify
  b.Update(issue.ID, ...)               // write — no check that Holder is still ""
  ```
  Two processes that both observe `Holder == ""` will both write their own holder;
  the second write wins and **both believe they hold the slot**. `MergeSlotRelease`
  has the symmetric problem: it promotes `Waiters[0]` after a non-atomic read, so two
  concurrent releases can promote the same waiter or drop a waiter.
- **Failure path:** refinery A and refinery B (or a retry racing a release) both read
  the empty slot → both acquire → conflict resolution runs twice on the same merged
  stack → corrupt/duplicated merge.
- **Mitigation in place:** today there is one `Engineer` per rig (`NewEngineer` in
  `internal/refinery/engineer.go`) and acquire uses 10 retries w/ 500ms backoff, so
  same-rig concurrency is normally low. The correctness of the merge queue therefore
  rests on an *unstated single-writer assumption*. If that ever breaks (two refinery
  sessions, a stale session, manual `gt mq` invocation), the slot guarantees nothing.
- **Fix:** make claim atomic — a conditional `UPDATE ... WHERE holder = ''` (CAS) via
  the store, or wrap acquire+release in a Dolt transaction, or reuse the `flock`
  pattern already used by `lockAgentBead` (`beads_agent.go:22`).

### H2 — Read-modify-write races repeated across escalation / channel / queue counters
- **Files:**
  - `internal/beads/beads_escalation.go:337-354` (`BumpOccurrenceCount`)
  - `internal/beads/beads_escalation.go:676-734` (`ReescalateEscalation`)
  - `internal/beads/beads_channel.go:238-258` (`SubscribeToChannel`)
  - `internal/beads/beads_queue.go:237-252` (`UpdateQueueCounts`)
- **Class:** data corruption / concurrency (lost updates)
- **What:** Same root cause as H1 — each does `Show` → mutate field in memory →
  `Update`, with no locking or CAS. Concurrent callers lose updates:
  - occurrence counts under-count (two bumps land as one),
  - channel subscriber appended by A is clobbered by B's append (subscriber dropped),
  - queue available/processing counters drift permanently out of sync.
- **Failure path:** the channel-subscriber and queue-counter cases are the most
  damaging — a dropped subscriber silently stops receiving mail, and a corrupted
  queue counter mis-reports backlog forever (no self-heal).
- **Fix:** introduce a small read-modify-write helper that takes the per-bead `flock`
  (already exists for agent beads) or does a CAS update; route all counter/list
  mutations through it. This is one shared fix, not five.

### H3 — Multi-step wisps migration has no transaction / rollback (partial-migration inconsistency)
- **File:** `internal/doltserver/wisps_migrate.go:104-119` (`MigrateAgentBeadsToWisps`),
  copy helpers `:260-303`
- **Class:** migration failure / data corruption
- **What:** The migration runs as independent steps — create tables, copy agent beads,
  copy auxiliary data (labels/comments/events), then close the originals. There is no
  surrounding transaction. If `copyAuxiliaryData` fails midway (e.g. after labels but
  before events), the function returns an error and **step 6 (close originals) never
  runs**, leaving the same agents live in *both* `issues` and `wisps` with only partial
  auxiliary data copied.
- **Idempotency caveat:** the function is documented as idempotent and uses
  `INSERT IGNORE`, so a clean re-run mostly converges — but rows that failed for a
  *non-"nothing"* reason (schema mismatch, missing source table) abort the whole copy,
  and the error-string matching at `:265/:276/:286/:296` (`strings.Contains(err, "nothing")`)
  is brittle: a real failure whose message happens to contain "nothing" is silently
  swallowed, dropping data without surfacing an error.
- **Fix:** wrap the copy+close sequence in `DOLT_BEGIN` / `DOLT_COMMIT` / `DOLT_ROLLBACK`,
  or gate "close originals" on a verified row-count match. Replace substring error
  matching with a typed/sentinel check for the benign "no rows" case.

---

## MED-confidence findings

### M1 — `replaceDir` destroys the destination before verifying the copy (rollback data loss)
- **File:** `internal/doltserver/rollback.go:181-206`
- **Class:** data corruption
- **What:** `replaceDir` `os.RemoveAll(dst)` first, then `copyDir(dst, src)`. If the
  copy fails (disk full, src vanished, permission), `dst` is already gone and there is
  no backup → the database directory is lost. On a rollback path this is exactly when
  you can least afford to lose the current state.
- **Fix:** copy to a temp sibling dir first, then atomically `os.Rename` over `dst`
  (rename-into-place), so a failed copy leaves the original intact.

### M2 — Molecule instantiation cleanup ignores `Close` errors → orphan step beads
- **File:** `internal/beads/molecule.go:333-335` (`instantiateFromChildren`), `:426-429` (`instantiateFromMarkdown`)
- **Class:** orphan data
- **What:** On a step-creation failure the rollback loop does `_ = b.Close(created.ID)`.
  If `Close` fails, the error is discarded and the partially-created step beads remain
  in the DB with no parent linkage — orphaned molecule steps that later confuse
  progress/DAG queries.
- **Fix:** collect Close errors and surface them (return a wrapped "partial cleanup
  failed" error) so the caller knows manual cleanup is needed.

### M3 — Delegation reports success after the blocking-dependency write fails → orphan delegation
- **File:** `internal/beads/beads_delegation.go:78-82`
- **Class:** error handling / orphan data
- **What:** After recording a delegation, the code adds the blocking dependency; if
  that `AddDependency` fails it logs a warning and **returns nil**. The delegation is
  recorded but the block that prevents premature parent closure is missing — the parent
  can close while the delegated child is still open.
- **Fix:** return the dependency error (or roll back the delegation) instead of
  swallowing it.

### M4 — `storeCreate` persists an issue then fails if the parent link fails (orphan in wrong hierarchy)
- **File:** `internal/beads/store.go:283-296`
- **Class:** transaction handling / orphan data
- **What:** `CreateIssue` then `AddDependency(parent)`; if the parent link fails the
  issue is already persisted with no parent. The error message even admits it
  ("issue created but parent link failed"). Caller has no clean recovery.
- **Fix:** validate parent existence before create, or delete the created issue on
  link failure (compensating action), or do both writes in one transaction.

### M5 — `RemoveDatabase` discards the branch-control cleanup error
- **File:** `internal/doltserver/doltserver.go:3580`
- **Class:** orphan data / error handling
- **What:** `_ = serverExecSQL(..., "DELETE FROM dolt_branch_control WHERE \`database\` = '...'")`.
  Per the adjacent comment, stale `dolt_branch_control` rows cause the DB directory to
  be re-materialized on the next connection. If this DELETE fails silently, removal can
  silently un-stick. There is a re-materialization recovery check immediately after
  (sleep + restart), which is why this is MED not HIGH — but the swallowed error blinds
  operators to the underlying failure.
- **Note:** `dbName` here derives from a filesystem directory name and is single-quoted
  but not escaped; injection risk is LOW (not operator/attacker controlled) but the
  pattern is fragile.
- **Fix:** log/return the DELETE error.

### M6 — Scorekeeper/charsheet parse helpers swallow `Sscanf`/`Unmarshal` errors → silent data skew
- **Files:** `internal/doltserver/wl_charsheet.go:379,381` (`fmt.Sscanf` unchecked),
  `:338` (`json.Unmarshal` unchecked → nil map later indexed), `wl_commons.go:715`
- **Class:** data corruption / error handling
- **What:** Non-numeric CSV cells leave `confidence`/`stamp_index` at zero-value instead
  of a sentinel, skewing tier/geometry computation; an unmarshal failure leaves a nil
  valence map that is indexed downstream (potential nil-map read → zero values, or panic
  if assigned). Quiet corruption rather than a loud failure.
- **Fix:** check the errors; set explicit sentinels or skip-with-log.

### M7 — `RunScorekeeper` drops per-subject failures with bare `continue`
- **File:** `internal/doltserver/wl_charsheet.go:499-500, 542-543`
- **Class:** error handling
- **What:** A failing upsert is skipped silently; the function returns "success" with a
  partial leaderboard and no signal that N entries were lost.
- **Fix:** accumulate and report failure count / return partial-error.

---

## LOW-confidence findings (latent / defense-in-depth)

- **L1 — SQL built via `fmt.Sprintf` guarded only by `validSQLName`** — `sync.go:356-359`
  (`DOLT_PUSH`), `wisps_migrate.go:365` (`DOLT_COMMIT`), `wl_commons.go:79`
  (`SHOW DATABASES LIKE`). Inputs are validated (`[a-zA-Z0-9_.-]+`) or hardcoded today,
  so not currently exploitable, but the string-interpolation pattern is one weakened
  validator away from injection. The `LIKE '%s'` case (`wl_commons.go:79`) does not
  escape `%`/`_` wildcards. Prefer parameterized queries / identifier quoting helpers.
- **L2 — `time_zone` set via `fmt.Sprintf("SET GLOBAL time_zone = '%s'", tz)`** —
  `doltserver.go:4792`, `tz` from `GT_DOLT_TIME_ZONE`. Operator-controlled env, not
  untrusted input, so practical risk is LOW; still worth a whitelist/escape.
- **L3 — custom CSV parser** (`wl_commons.go:580-606`) reimplements quote handling
  instead of using `encoding/csv`; latent edge-case mis-parsing.
- **L4 — temp SQL file `defer os.Remove` before/while subprocess may still hold it**
  (`doltserver.go:4977-4981`) on timeout/cancel; mostly benign on Linux.
- **L5 — `catalog.go` `tmp.Close()` errors unchecked on the write/error paths**
  (`:173-174, :179-180`) can leave `.catalog-*.tmp` orphans on some filesystems.

---

## False positives (verified NOT bugs)

- **`json.Marshal(data)` "ignored error" in `beads_merge_slot.go:128,151,194`** — `data`
  is a struct of strings/`[]string`; `encoding/json` cannot fail marshaling these. The
  discarded error is unreachable. (The *concurrency* bug in the same functions is real —
  see H1 — but the marshal-error claim is not.)
- **`UpdateAgentCompletion` maps `meta.HookBead` → `LastSourceIssue`
  (`beads_agent.go:656`)** — this is correct: `LastSourceIssue` is documented as
  "Last source/work bead ID, preserved after hook_bead is cleared", and `HookBead` *is*
  the work bead ID. Not a mis-assignment.
- **`DOLT_PUSH` / `DOLT_COMMIT` "SQL injection" (HIGH claims)** — both are guarded by
  `validSQLName` (`sync.go:139`) which restricts to `[a-zA-Z0-9_.-]`; downgraded to L1.
- **`copyAuxiliaryData` connection-leak claim** — `defer db.Close()` is correctly placed
  before the early returns; no leak. (The migration-atomicity issue is real — H3.)

---

## Recommendation

The dominant systemic issue is **non-atomic read-modify-write on bead state** (H1, H2,
M3, M4) — the codebase uses `bd`/store `Show`+`Update` as if it were transactional. A
single shared CAS/locked-update helper would close H1, H2, and reduce M3/M4. H3 and M1
are independent data-safety fixes worth filing on their own.

Beads filed for confirmed HIGH findings: see "Filed beads" below.

## Filed beads

- **gu-sz1xl** (P1, bug) — H1: Merge slot acquire/release non-atomic RMW
- **gu-tucci** (P2, bug) — H2: Lost-update RMW races (escalation/channel/queue)
- **gu-ivcpy** (P2, bug) — H3: wisps migration lacks transaction/rollback

All three linked `discovered-from` gu-nid89.3.

## Sources

- Code reviewed in-repo: `internal/beads/`, `internal/doltserver/` (no external sources).

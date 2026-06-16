# Technical Debt Trend Audit

## Summary

The gastown Go codebase (1,784 `.go` files / 920 `_test.go` files, 9,048 commits
since 2025-12-15) continues to carry **almost no inline technical-debt cruft**.
A full-tree scan for `TODO` / `FIXME` / `HACK` / `XXX` in non-vendor Go source
returns the **same 6 matches as the 2026-06-04 baseline, still all false
positives** — test fixtures, format-string templates, and prose describing ID
shapes (`CR-XXXX`, `testdb-XXXXXXXX`, `mr-XXXXXXXXXX`). Unmanaged TODO inventory
in production code remains effectively **zero**, and deprecation hygiene stays
strong: all 13 `// Deprecated:` markers follow godoc convention and point callers
to a replacement; the deprecated rig-bead helpers (`GetRigBead`, `UpdateRigBead`,
`DeleteRigBead`, `RigBeadID`) **still have zero non-test callers**.

The trajectory is **holding steady, with one regression in the test suite that
the prior run flagged and which remains unaddressed**. Both P1 vacuous-skip
findings from 2026-06-04 in `internal/doctor/integration_test.go` are still
present (~47 days old, untouched since the last audit). One has quietly gotten
*worse*: line 188's table now populates `wantActor` for every row, so the
`t.Skip` guard no longer fires — but the `t.Run` body still contains **no
assertion of any kind**, so the test now reports green by running an empty
closure rather than by skipping. The false-confidence is the same; only its
disguise changed. The headline metric (591→817 `t.Skip` sites, line-based) grew,
but growth is proportional to test-file count and dominated by legitimate
platform/env guards — debt-smell skips number only **3–4**, essentially flat.

## Score

score: 0.88

## Critical Findings (P0 — file as beads, fix urgently)

None. No deprecated API with both an absent removal plan *and* active production
callers was found. The only deprecated symbols with live callers (`HasRemote`,
`GetMR`) are trivial one-line shims that delegate to their replacement
(`FindRemote`, `FindMR`) — see Minor below.

## Major Findings (P1 — track but do not auto-bead)

- **Doctor env-var integration test asserts nothing — now passes vacuously instead of skipping (regression)**
  - **Location**: `internal/doctor/integration_test.go:185-191` (the
    `wantActor` table loop), skip site `:188`
  - **Impact**: At the 2026-06-04 baseline this test skipped because table rows
    left `wantActor` empty. The rows now carry expected values
    (`mayor`→`mayor`, `witness`/`gastown`→`gastown/witness`, …), so the
    `if tt.wantActor == "" { t.Skip(...) }` guard never triggers — but the
    `t.Run` body contains only comments and still **never compares the computed
    actor against `tt.wantActor`**. The subtest runs an empty closure and reports
    green. This is *worse* than the prior state: the skip at least signaled
    "unfinished"; now coverage tooling counts it as a passing assertion of
    health-check actor computation when nothing is asserted. Untouched since the
    last audit (introduced `65483624`, 2026-04-30, ~47 days old).
  - **Suggested fix**: Add the actual assertion inside the `t.Run` body
    (compute the actor and `if got != tt.wantActor { t.Errorf(...) }`), or delete
    the test if `env_check_test.go` already covers it (the comment claims it
    does) so it stops inflating coverage.

- **Doctor fix-path test skips its own assertion when the detector fails**
  - **Location**: `internal/doctor/integration_test.go:395`
  - **Impact**: Unchanged from baseline. When the `runtime-gitignore` checker
    fails to detect the deliberately-broken state, the test `t.Skip`s instead of
    failing, so a regression in the detector silently turns the fix-path test
    into a no-op. Same commit/age as above; not addressed since 2026-06-04.
  - **Suggested fix**: Convert the skip to `t.Fatalf`/`t.Errorf`; if the broken
    state genuinely cannot be staged in-test, file a bead and reference its ID in
    the skip message so the gap is tracked rather than silently tolerated.

## Minor Findings (P2 — informational)

- **Dead deprecated rig-bead helpers (still zero callers)**: `GetRigBead`,
  `UpdateRigBead`, `DeleteRigBead`, `RigBeadID` in
  `internal/beads/beads_rig.go` (lines 184, 230, 256, 299) remain deprecated and
  have **0** non-test callers. Their referencing bead `gu-r83v` is now **CLOSED**
  — the work the comment described is done, so these helpers are pure dead
  surface area safe to delete in a follow-up. (The comment has outlived its
  bead.)

- **Two new deprecated shims with live callers but no removal date**:
  `HasRemote` (`internal/doltserver/sync.go:81`, called from `dolthub.go:102`)
  and `GetMR` (`internal/refinery/manager.go:571`, called from `cmd/mq.go:434`).
  Both are one-line delegations to their replacement, so risk is low, but neither
  carries a removal version/date. Migrate the two callers and drop the shims, or
  add a removal plan to the doc comment to match the `gt polecat add` pattern.

- **`CreatePolecatCLAUDEmd` deprecated no-op retained for backward compat**:
  `internal/templates/templates.go:234`. References bead `gu-k9oj`, which is now
  **CLOSED**. The function explicitly says it will be removed "once no external
  integrations depend on them" — with the bead closed, schedule the removal or
  confirm the external-integration concern is still real.

- **Debt-smell `t.Skip` count is flat (~3–4), but total skip sites grew
  591→817**: The growth is proportional to test-file growth and dominated by
  legitimate guards — categorizing skip reasons yields ≈451 platform
  (Windows/unix/goos), ≈136 env-dependency-missing (git/bd/dolt not installed),
  ≈20 short-mode, ≈17 unix-mock, ≈10 flaky/CI, and only **3** unconditional
  debt-smell ("not implemented"/"not detecting"/"removed"). Not alarming, but the
  raw count will keep climbing; worth a per-directory skip budget if it doubles
  again.

- **New disabled artifacts checked in**: `plugins/.disabled/verify-build/plugin.md`
  joins the previously-noted `plugins/code-scout/plugin.md.disabled` and
  `plugins/task-discovery/plugin.md.disabled`. Confirm these are intentionally
  parked vs. abandoned; if abandoned, remove them so the tree doesn't accumulate
  inert manifests.

- **180-day aging threshold is now reachable but no marker hits it**: the repo's
  first commit (2025-12-15) is ~183 days old, so the 180-day criterion is finally
  live. The oldest debt markers found (doctor skips, deprecated rig helpers) are
  ~47 days old — well under threshold. No aged-TODO findings this run; keep this
  check active for subsequent audits.

## Trend Delta vs. 2026-06-04

| Metric | 2026-06-04 | 2026-06-16 | Direction |
|---|---|---|---|
| Score | 0.90 | 0.88 | ↓ slight (carried-over P1s unfixed) |
| TODO/FIXME/HACK/XXX (all false-pos) | 6 | 6 | → flat |
| `// Deprecated:` markers | — | 13 | tracked |
| Deprecated rig-helper callers | 0 | 0 | → flat |
| Debt-smell skips | ~6 | ~3–4 | → roughly flat |
| Total `t.Skip` sites | 591 | 817 | ↑ (proportional to test growth) |
| Doctor vacuous-skip P1s | 2 | 2 | → unfixed (one regressed to vacuous-pass) |

**Answer to the audit questions:** The codebase is **holding steady** on debt —
neither paying down nor accumulating inline cruft — with the one exception that
the two test-suite findings raised last run went unaddressed, and one degraded
from "skips visibly" to "passes silently." The oldest active debt markers (~47
days) are the doctor placeholders and the dead deprecated rig helpers; they
persist because no one owns the doctor test cleanup and the rig helpers' tracking
bead (`gu-r83v`) closed without deleting the now-dead code it described.

## Counts

  counts: critical=0 major=2 minor=6

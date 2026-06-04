# Technical Debt Trend Audit

## Summary

The gastown Go codebase (1,642 `.go` files, ~8,200 commits since 2025-12-15)
carries remarkably little inline technical-debt cruft. A full-tree scan for
`TODO` / `FIXME` / `HACK` / `XXX` in Go source returns **6 matches, all of
which are false positives** — they are test fixtures, format-string templates,
and prose describing ID shapes (`CR-XXXX`, `testdb-XXXXXXXX`, `gt-XXXXXX`), not
debt markers. There is effectively **zero unmanaged TODO inventory** in
production code. Deprecation hygiene is similarly strong: every `// Deprecated:`
follows Go's godoc convention and points the caller to its replacement, several
carry explicit removal versions, and the deprecated rig-bead helpers have **zero
remaining production callers**.

The one soft spot is the test suite. Of 591 `t.Skip` sites, the overwhelming
majority are legitimate *environment guards* (Windows-specific behavior,
`bd`/Dolt not installed, env-gated e2e). Only six carry a debt-smell reason, and
two of those are tests that **pass vacuously** — they skip past their own
assertions, manufacturing false green. This is the highest-value finding in this
dimension. **No prior `.quality/` summary exists, so this is the baseline run —
no trend delta can be computed yet.**

## Score

score: 0.90

## Critical Findings (P0 — file as beads, fix urgently)

None. No deprecated API with both an absent removal plan *and* active production
callers was found. The deprecated rig-bead helpers (`GetRigBead`,
`UpdateRigBead`, `DeleteRigBead`, `RigBeadID`) each have **0** non-test callers
outside their own file, so no caller is at risk.

## Major Findings (P1 — track but do not auto-bead)

- **Doctor integration test passes vacuously — "actor validation not implemented"**
  - **Location**: `internal/doctor/integration_test.go:188`
  - **Impact**: The test's only assertion is gated behind `if tt.wantActor == ""
    { t.Skip(...) }`, and the table rows leave `wantActor` empty, so the test
    `t.Run` body runs, skips, and reports green without ever validating actor
    computation. This is false confidence in a health-check subsystem. Introduced
    2026-05-01 (`086dbc96`), ~34 days old.
  - **Suggested fix**: Either populate `wantActor` for the table rows and assert,
    or delete the placeholder test so coverage reports do not count it.

- **Doctor fix-path test skips its assertion — "runtime-gitignore check not detecting broken state"**
  - **Location**: `internal/doctor/integration_test.go:395`
  - **Impact**: When the `runtime-gitignore` checker fails to detect the
    deliberately-broken state, the test `t.Skip`s instead of failing — so a
    regression in the detector silently turns the fix-path test into a no-op.
    Same commit/age as above.
  - **Suggested fix**: Convert the skip to a `t.Fatal`/`t.Errorf`; if the broken
    state genuinely cannot be staged in-test, file a bead and reference it in the
    skip message so the gap is tracked.

## Minor Findings (P2 — informational)

- **Dead deprecated rig-bead helpers**: `GetRigBead`, `UpdateRigBead`,
  `DeleteRigBead`, and `RigBeadID` in `internal/beads/beads_rig.go` (lines 184,
  230, 256, 299) are all deprecated for assuming the hardcoded `"gt"` prefix and
  have zero production callers. Safe to delete in a follow-up; they only add
  surface area. (References gu-r83v / ta-0pk #5 in their doc comments.)

- **Debt-smell skips outside doctor** (4): platform/CI-conditional but worth a
  glance — `internal/beads/beads_agent_test.go:361` ("not implemented on
  Windows"), `internal/cmd/agents_test.go:557` ("tmux discovery unreliable on
  Windows"), `internal/polecat/session_manager_test.go:590` ("idle detection
  unreliable in test environment"), `internal/tmux/respawn_hook_test.go:97`
  ("tmux run-shell -b hooks unreliable in CI"). All are environment-qualified,
  not unconditional disables.

- **Two disabled plugin manifests**: `plugins/code-scout/plugin.md.disabled` and
  `plugins/task-discovery/plugin.md.disabled` are checked in but inert. Confirm
  they are intentionally parked vs. abandoned; if abandoned, remove them.

- **User-facing deprecation with a clean removal plan** (no action, positive
  signal): `gt polecat add` (`internal/cmd/polecat.go:101`) is deprecated with an
  explicit "will be removed in v1.0" plan and a runtime warning. This is the
  pattern to emulate.

- **180-day aging is not yet reachable**: the repository's first commit is
  2025-12-15 (~172 days ago), so no marker can yet be older than the 180-day
  threshold. Re-evaluate this criterion on the next periodic run.

## Counts

  counts: critical=0 major=2 minor=5

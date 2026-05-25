# Ambiguity Analysis

## Summary

The PRD is well-structured with a clear problem statement and comprehensive clarification section. However, significant ambiguities remain — particularly around metric definitions, boundary conditions in the quality gates, contradictions between the original scenario descriptions and the finalized decisions, and several "vague qualifier" patterns that would cause PR review debates during implementation. The state machine is well-specified but its relationship to human-visible concepts ("open PR") creates semantic confusion that will surface during coding.

The most impactful ambiguities cluster around (1) the quality-gate implementation details that determine whether tests pass or fail, and (2) the exact scope boundary of what the polecat may and may not touch during a cycle. These are the areas where two engineers would most diverge in implementation.

## Findings

### Critical Gaps / Questions

**1. "≤200 added test LOC" — what counts as a line of code?**
- The size cap is the primary scope limiter, but "LOC" is undefined. Does this include blank lines in test files? Comments (which are required by the diff-marker gate)? Test helper functions? Import statements? Type definitions used only by tests?
- Two engineers implementing this would produce different counting tools producing different results.
- **Suggested question:** Define precisely: is it `wc -l` on added lines in `_test.go` files, or is it a semantic LOC count (e.g., `gocloc` non-blank non-comment)?

**2. "comment out one line of the function under test" — selection algorithm undefined**
- The synthetic-mutant sanity check is a MUST gate, but the line-selection algorithm is unspecified. "AST-aware (skip lines whose comment-out produces syntax errors; pick a different line)" still leaves open: which line is tried first? What if ALL lines produce syntax errors when commented? What if the function under test is a one-liner? What counts as "the function under test" when a test exercises multiple functions?
- This gate will be the most contentious to implement and the hardest to debug when it false-positives.
- **Suggested question:** Specify: (a) how to identify which function(s) a test exercises, (b) the line selection priority (random? first non-trivial? highest complexity?), (c) failure mode when no valid line can be commented.

**3. Contradiction: S1 says "twice a week" but final decision says "≤1 PR per rig per 7-day window"**
- Scenario S1 states "Twice a week the mechanism wakes up, picks 2-3 under-tested branches in a recently-changed file, drafts tests, opens a PR." The finalized rate limit (OQ7 resolution) says ≤1 PR per 7 days. These are mutually exclusive. S1 is now stale.
- An implementer reading scenarios for behavioral guidance will get a different answer than one reading the Clarifications section.
- **Suggested question:** Update S1 to reflect the finalized cadence, or explicitly mark original scenarios as superseded.

**4. "No non-test source changes unless absolutely required" — undefined threshold**
- Goal 5 allows source changes "unless absolutely required." What qualifies? Adding an `Export` to make a private function testable? Adding a test interface/mock point? Fixing a trivial bug discovered during testing? The non-goal says "we do NOT bundle a fix into the test PR" but doesn't address testability refactors.
- This will cause PR review debates: "you touched production code in a test PR."
- **Suggested question:** Enumerate the allowed categories of non-test changes (e.g., exporting unexported symbols: yes/no; adding interfaces for mock injection: yes/no; fixing bugs: always no).

**5. "line/branch coverage below rig threshold" — threshold undefined**
- The target-selection algorithm references a "rig threshold" for coverage, but this is never defined. What is the default threshold? Where is it configured? Is it per-file or per-package? Is it line coverage, branch coverage, or both (the formula uses "coverage" as a scalar)?
- **Suggested question:** Define the default coverage threshold for the pilot rig, and specify whether the formula uses line coverage, branch coverage, or a composite.

**6. Full test suite vs. new-test-only validation before PR open**
- S5 says "A test the polecat wrote fails locally. The mechanism does NOT push it." This implies the full suite is run. But Q2's MUST gate 4 says "N=10 flakiness re-run on the new tests + their direct package only (NOT the full rig suite)." What if the new test passes but breaks an existing test (e.g., test pollution via shared state)? The polecat would push a PR that breaks CI.
- The non-goal "No PR ever appears with a red build" (S5) may not hold with package-scoped validation only.
- **Suggested question:** Is "no red build" a hard guarantee (requiring full-suite run before push) or best-effort (package-scoped validation + Refinery catches the rest)?

### Important Considerations

**7. "direct package" — scope of flakiness validation**
- "N=10 flakiness re-run on the new tests + their direct package only" — what is a "direct package"? The Go package containing the `_test.go` file? All packages in the same directory subtree? This matters for Go where packages can have `_test` suffix packages in the same directory.
- **Impact:** Over-scoping wastes time; under-scoping misses cross-package flakiness.

**8. State machine "open" vs. human concept of "open PR"**
- Goal 3 says "At most one open auto-test PR per rig at any time." The state machine defines "open" as any of `{dispatched, mr-pending, mr-revising}`. But in `dispatched` state, no PR exists yet on GitHub. Calling this "open" creates cognitive dissonance for rig owners checking via `gt auto-test-pr status` who expect "open PR" to mean a visible PR on GitHub.
- **Impact:** UX/messaging confusion. Status command needs careful wording.

**9. "churn" definition**
- Target selection ranks by `churn × (1 − coverage)`. "Files churned in last 30 days" — what counts as churn? Any commit touching the file? Only commits on main (not reverted)? Does a formatting-only commit count? Does a file moved/renamed count as churned?
- **Impact:** Different churn definitions will produce very different target rankings.

**10. Pilot success criteria measurement window**
- "≥60% merge rate over the first 5 PRs" — is this the first 5 PRs opened, or the first 5 PRs that reach a terminal state (merged or closed)? What if PR #3 is still open when PR #5 is closed? What's the time window — if it takes 6 months to produce 5 PRs (at 1/week but with pauses), is the pilot still running?
- **Impact:** Unclear when the pilot "graduates" or is deemed failed.

**11. "polecat commits as polecat via the existing convention"**
- For Refinery-mode v1, identity is hand-waved as "existing convention." What IS this convention? Git author email format? GPG signing? The Refinery needs to attribute the work. If the convention isn't documented, implementers will guess.
- **Impact:** Inconsistent attribution metadata across polecats.

**12. "Mayor must be able to deprioritize it" vs. patrol-based architecture**
- The Constraints section says "The PR-creation cycle must not flood `bd ready`. Mayor must be able to deprioritize it." But the final architecture is a standing patrol (cron-shaped) that doesn't use `bd ready` for its trigger. How does Mayor "deprioritize" something that fires on a timer? Is the kill-switch sufficient, or is there a priority-queue mechanism?
- **Impact:** The constraint may be satisfied by the kill-switch alone, but this isn't stated. Implementer might build unnecessary priority plumbing.

**13. Branch GC "no PR after 7 days" — which clock?**
- "Branch GC for stale `auto-test/<rig>/...` branches with no PR after 7 days" — 7 days from branch creation? From last commit? From state-machine entering `dispatched`? What if the polecat crashes mid-work and the state bead is stuck in `dispatched` with a pushed branch but no PR?
- **Impact:** Incorrect GC timing could delete a branch that's still in-use by a slow polecat.

### Observations

**14. "Should" vs "must" inconsistency in Non-Goals**
- Non-Goals use "Not" declarations which are clear. But Goals mix "target:" (soft) with implied "must" (Goal 6: "New tests must pass, not be flaky"). The distinction between goals that are aspirational vs. goals that are hard gates is only clarified retroactively in Q2. Consider promoting all MUST-gate items into a "Hard Requirements" section.

**15. Bead naming collision**
- "Mayor files a single bead `<rig>-auto-test-NNN`" — NNN format isn't specified. If multiple cycles fire across time, how is NNN generated to avoid collision? Sequential counter stored where? Hash? Timestamp?
- Non-blocking but will require a design decision during implementation.

**16. "Rejection history" persistence**
- S4 says "The mechanism records that rejection, backs off (rate limit), and avoids retargeting that file for some cooldown period." But the state bead per Q7 tracks cycle-level state, not per-file rejection history. Where does per-file rejection history live? In the same pinned bead? A separate data structure?
- Non-blocking for v1 pilot (can use bead notes) but architecturally relevant for v2 multi-rig.

**17. "Greenfield only" non-goal vs. target selection on existing files**
- Non-Goals says "Greenfield only — pick targets forward, don't try to backfill the whole rig." But the target-selection algorithm explicitly picks "files churned in last 30 days" which are existing files. The word "greenfield" here seems to mean "new tests for recently-touched code" not "only test new files." This overloading of "greenfield" could confuse.

**18. Feedback patrol identity**
- When `mol-pr-feedback-patrol` dispatches a polecat to revise the auto-test PR, is that polecat running the same `mol-polecat-work-test-improver` formula or the standard feedback-patrol formula? The PRD says "We may need to teach it to honor the `gt:auto-test-pr` label by dispatching to the same polecat-work-test-improver formula" — the "may need" is unresolved.

## Confidence Assessment

**Medium-High.** The PRD's Clarifications section resolves most of the high-level architectural ambiguities well (Q1–Q7 are decisive and well-reasoned). The remaining ambiguities are concentrated at the implementation-detail level — metric definitions, boundary conditions, and stale scenario text that contradicts finalized decisions. These are exactly the things that will cause PR review debates and inconsistent implementations across the codebase, but they're addressable with a focused "implementation spec" pass before coding begins.

The single highest-risk ambiguity is the synthetic-mutant gate (Finding #2) — it's a MUST gate with undefined behavior for common edge cases, meaning the first implementer will make design decisions that become de facto spec.

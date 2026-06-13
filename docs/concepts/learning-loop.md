# The Learning Loop — Recurring PR Feedback → CLAUDE.md Conventions

> **A reviewer comment should teach more than one PR.**

Without a learning loop, every human review comment on a customer PR teaches
exactly one PR. `mol-pr-feedback-patrol` routes those comments back to a polecat
for fixing, but the lesson dies when the PR merges — the next PR relearns it from
scratch. The learning loop captures the generalizable lessons so the codegen
engine actually improves from reviewer feedback.

## How It Works

The loop rides on the existing PR-feedback patrol — no new patrol, no new
formula:

1. **Patrol** — `mol-pr-feedback-patrol` detects a PR with `CHANGES_REQUESTED`
   and dispatches a review-feedback bead to a polecat.
2. **Address** — The polecat reads the actual review comments (the patrol never
   sees comment text — only `reviewDecision`) and pushes a fix.
3. **Distill** — As a final step, the polecat asks: *is any of this feedback a
   generalizable rule?* A rule is generalizable when the same correction would
   apply to future PRs — a team style the reviewer wants, a pattern to avoid, or
   a landmine just discovered. One-off, PR-specific notes are not.
4. **Propose** — If (and only if) the feedback is recurring, the polecat files a
   one-line distill bead labeled `claude-md-convention` proposing a single,
   concrete CLAUDE.md "Conventions" line, naming the target rig CLAUDE.md.
5. **Apply** — A human or crew member reviews the proposal and appends the
   one-line entry to the rig-level CLAUDE.md "Conventions" section.

## The Guard

Rig CLAUDE.md files (e.g. `lia_bac` / `lia_iac` / `lia_web`) are **gastown-side**
files — they are NOT the customer repo. Polecats only **propose** via the distill
bead; a **human or crew member applies** the edit. This keeps generated-code
conventions under human control and avoids a polecat silently rewriting the rules
it is graded against.

## Conventions Hygiene

- Keep each entry **one line and concrete** — an imperative rule, not a paragraph.
- **Prune stale entries quarterly** — conventions that no longer hold add noise.
- The `claude-md-convention` label makes pending proposals easy to sweep:
  `bd list --label claude-md-convention --status open`.

## Why Not Auto-Edit?

A weekly `gh api` sweep over merged-PR threads was considered, but the patrol
already routes feedback to a polecat that has the comments in front of it — the
cheapest place to make the recurrence judgment. Auto-editing CLAUDE.md was
rejected per the guard above: the rules the engine is measured against stay
human-applied.

Implements hq-fsyy7 / gs-agwi.

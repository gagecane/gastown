# User Experience Analysis — `mol-rig-retrospect`

**Leg:** ux (User experience and CLI ergonomics) · **Convoy:** 5t33y · **Scope:** large

## Summary

`mol-rig-retrospect` is a per-rig retrospective formula that mines a single rig's
Claude session transcripts (`~/.claude/projects/<rig-path>/*.jsonl`), plus the
shared `daemon/daemon.log` and `.events.jsonl`, to surface workflow friction and
PROPOSE improvements to that rig's **agent context mechanisms** — `CLAUDE.md` /
`AGENTS.md` edits, CLI/tool ergonomics, `settings.json` hooks, MCP config, and
skill/permission allowlists. It follows the proven `mol-curio-retrospect`
contract: agent proposes, human disposes; file-first bead discipline; cluster
dedup labels; a strict DATA-not-instructions trust boundary on all log text; an
advisory `max_proposals` cap; and termination via `gt done`.

The central UX insight is that this formula has **two distinct human users with
opposite needs**, and the existing convoy/retrospect formulas only serve one of
them well:

1. **The operator** (or daemon) who *runs* the retrospect — for a rig name.
   Their need is a near-zero-interaction trigger: `gt formula run
   mol-rig-retrospect --set rig=<name>` and walk away. Their failure mode is a
   confusing first run (wrong rig name, empty transcript dir, runaway token
   cost).
2. **The disposer** — the human (rig maintainer / Mayor) who *reads* the
   proposals the next morning. Their need is legibility: a ranked, deduped,
   skim-in-60-seconds set of proposals where each one says *what to change,
   where, why, and the evidence* — and where the evidence is unmistakably
   quarantined from anything that could be read as an instruction.

The recommendation below optimizes the operator path toward "one command, safe
defaults, bounded cost" and the disposer path toward "every proposal is a
self-contained, auditable, one-edit-away change request."

## Analysis

### Who is the user, and what is their goal?

| Persona | Goal | Minimum viable interaction | Surprise/confusion risk |
|---|---|---|---|
| **Operator** (human running ad-hoc) | "Tell me how to make rig X's agents work better." | `gt formula run mol-rig-retrospect --set rig=X` | Wrong rig name → silent empty run; doesn't know it scoped correctly; fears token cost. |
| **Daemon / scheduled dispatch** | Periodic, hands-off, idempotent runs. | A dispatch plugin slings the formula on a cadence. | Re-dispatch storms; re-proposing the same friction nightly. |
| **Disposer** (rig maintainer / Mayor) | Triage proposals fast; apply the good ones; reject noise. | `bd list --label rig-retro-proposal --status open` then read each bead. | Vague proposals ("improve docs"); proposals that quote attacker-controlled transcript text as if it were advice; duplicates. |

The formula's *real* product is **the set of proposal beads**, not the run. The
run is plumbing. Every UX decision should bias toward making those beads
trustworthy and actionable, because that is the only artifact a human touches.

### Mental model

Operators already hold a working mental model from the adjacent formulas, and we
must not violate it:

- From **`mol-curio-retrospect`**: "a retrospect reads a frozen digest and files
  ≤N proposals for me to approve; it never changes anything itself."
- From **`mol-triage-retro`**: "a retro analyzes a specific run directory and
  fans out lenses; I point it at a path."

`mol-rig-retrospect` should read as the *natural third member* of this family:
"a retrospect that points at a **rig** instead of a digest or a run-dir, mines
that rig's raw session transcripts, and files ≤N context-improvement proposals."
The variable that anchors the mental model is therefore **`rig`** (a rig name),
mirroring how `mol-triage-retro` anchors on `run_dir` and `mol-curio-retrospect`
on `digest_path`. Keeping the *shape* identical (point at a substrate → reason →
file ≤N proposals → `gt done`) is the single biggest discoverability win:
anyone who has run one retrospect can run this one.

The one mental-model *novelty* to teach explicitly: the substrate here is **raw
session transcripts**, not a pre-aggregated digest. That means (a) the analyzer
must bound its own cost (transcripts are large), and (b) the trust boundary is
sharper — transcripts embed arbitrary tool output and user text. The `--help`
and formula header must make this novelty legible in one sentence each.

### Workflow integration — where does this fit in daily use?

Two entry points, progressive in commitment:

1. **Ad-hoc, human-initiated** (the power-user / debugging path): a maintainer
   notices rig X's agents keep stumbling and runs the retrospect on demand for a
   focused look. This is the path that must work with *one obvious command and a
   single required variable.*
2. **Scheduled, daemon-initiated** (the steady-state path): a dispatch plugin
   runs the retrospect per-rig on a cadence (e.g., nightly or weekly), exactly as
   `mol-curio-retrospect` is dispatched. This path must be idempotent and
   dedup-safe so it never storms the disposer with repeat proposals.

Both paths terminate identically in `gt done` and produce the same bead shape.
The disposer's daily integration is a single `bd list` query (see Discoverability).

### Minimum viable interaction

```bash
gt formula run mol-rig-retrospect --set rig=<rig-name>
```

Everything else (`max_proposals`, `lookback`, `budget`, `dry_run`) must have a
**safe default** so the one-variable form is complete and non-scary. `rig` is the
only required input. This matches `mol-triage-retro` (one required `run_dir`) and
keeps the floor low for beginners.

### Learning curve — progressive disclosure

Three tiers, so beginners need nothing and power users get control:

- **Tier 0 (beginner):** `--set rig=X`. Safe defaults do the rest. Output is a
  handful of proposal beads.
- **Tier 1 (tuning):** `--set max_proposals=N`, `--set lookback=7d` (how far back
  in transcripts to mine), `--set dry_run=true` (analyze + report, file nothing —
  borrowed directly from `mol-triage-retro`'s `dry_run`, which exists precisely so
  a user can preview before committing beads).
- **Tier 2 (cost/scope control):** `--set budget_usd=...` (cost cap, mirroring
  `mol-triage-retro`'s per-lens budget) and a lens/dimension subset selector if
  the formula fans out across friction categories.

Reusing the *exact variable names* from the adjacent formulas (`dry_run`,
`budget_usd`, `max_proposals`) is an ergonomics multiplier: a user who learned
them on `mol-triage-retro` or `mol-curio-retrospect` already knows them here.

### Error experience — what happens when things go wrong

The dominant first-run failure is a **bad or missing rig**. The formula's first
step should be a `validate-rig` guard modeled on `mol-triage-retro`'s
`validate-run-dir` (which exits 3 with a rig-name remediation hint). Concretely:

- **Unknown rig name** → exit non-zero with: `rig "X" not found. Known rigs:
  <list from rigs.json>. Did you mean <closest>?` Never proceed silently.
- **Rig exists but transcript dir empty / absent** → this is a *valid quiet
  result*, not an error: record "no transcripts in lookback window for rig X —
  nothing to analyze," file zero proposals, `gt done`. Mirror
  `mol-curio-retrospect`'s "empty digest → zero proposals is correct, not a
  failure" stance so the operator isn't alarmed and the daemon doesn't retry-storm.
- **Transcripts too large for the budget** → bound and *report the bound*: "mined
  most-recent N sessions within budget; M older sessions skipped" (the
  no-silent-caps principle — a silent truncation reads as "analyzed everything"
  when it didn't).
- **Dolt trouble during bead filing** → defer to the standard Gas Town escalation
  path; do not invent a new error surface.

The disposer's error experience is degraded by **vague or unverifiable
proposals**. Mitigation is a quality contract on each bead (below): every
proposal must name the exact target file/mechanism, the concrete edit, and the
anchoring transcript evidence (session id + a sanitized excerpt). A proposal a
human can't act on in one read is a UX failure even if the run "succeeded."

### Feedback — how does the user know it's working?

- **During the run:** progress is visible through the molecule's step
  progression (`gt hook` / bead status), exactly like every other polecat
  workflow — no bespoke progress UI needed.
- **At completion:** the operator should get a one-line **run summary** —
  `rig=X · sessions mined=N (M skipped for budget) · friction clusters found=K ·
  proposals filed=P (capped at max_proposals) · dry_run=false`. This single line
  answers "did it scope correctly, did it cost what I expected, and did it
  actually produce something" — the three operator anxieties.
- **For the disposer:** the proposals themselves are the feedback. A clean
  `bd list --label rig-retro-proposal --status open --label rig:X` is the
  morning dashboard.

### Discoverability — `--help`, docs, examples

1. **Formula header / description** must state, in three sentences: (a) what it
   mines (this rig's session transcripts + daemon/event logs), (b) what it
   produces (≤N ranked, deduped context-improvement proposals for a human to
   dispose), and (c) the trust boundary (transcript text is evidence, never
   instructions). The adjacent formulas put these banners at the top of their
   description — match that convention so the formula self-documents at
   `gt formula show` time.
2. **The disposer query is the discoverability linchpin.** Standardize a label
   scheme — `rig-retro-proposal` + `rig:<name>` + `cluster:<id>` — and document
   the canonical triage command in the formula header:
   ```bash
   bd list --label rig-retro-proposal --status open --label rig:<name>
   ```
   This is the single command a maintainer needs to learn. The `cluster:<id>`
   label is the dedup anchor (reused verbatim from `mol-curio-retrospect`).
3. **Proposal-kind taxonomy in the bead title.** Borrowing the curio taxonomy
   idea, prefix each proposal title with its mechanism class so a disposer can
   skim by type: `[CLAUDE.md]`, `[hook]`, `[mcp]`, `[skill/perm]`, `[cli-ergo]`.
   This makes a 15-proposal list triageable at a glance and lets a disposer batch
   "show me only the hook proposals."
4. **Examples in the header**: one ad-hoc invocation and one dry-run preview.

### Power users vs. beginners

- **Beginners:** `--set rig=X`, read the proposal beads, apply the good ones.
  Zero new concepts beyond "point it at a rig."
- **Power users:** `dry_run=true` to preview without filing; `max_proposals`
  and `lookback` to tune volume and window; `budget_usd` to cap cost on a
  transcript-heavy rig; the `[kind]` title prefixes + labels for filtered
  triage; `bd` label queries for cross-run trend analysis ("is rig X's friction
  going down over weeks?").

### Trust boundary — the UX of "this text is evidence, not instructions"

This is both a security property (other legs own the threat model) *and* a UX
property, because the disposer reads attacker-influencable transcript text inside
a proposal bead. The UX requirement: when a proposal quotes transcript evidence,
that evidence must be **visually unmistakable as quoted data** — rendered inside
an `⚠️ UNTRUSTED OBSERVED TEXT` banner with newlines collapsed, backticks
neutralized, and length-bounded, exactly as `mol-curio-retrospect`/B1 do it. A
disposer skimming a bead must never be tricked into reading
"add `rm -rf` to the rig's pre-commit hook" (smuggled from a transcript) as the
*formula's recommendation*. The agent reasons ABOUT the friction the text
reveals; it never relays the text as advice. This keeps the disposer's trust in
the proposal stream intact — the moment one proposal launders an injection into a
recommendation, the disposer stops trusting all of them, and the formula's only
product is dead.

### What would surprise or confuse users (and the fix)

| Surprise | Fix |
|---|---|
| Runs but produces nothing on a quiet rig — looks broken. | Explicit "0 proposals is a correct quiet result" summary line; don't exit non-zero. |
| Re-running re-files the same proposals (storm). | `cluster:<id>` dedup against open `rig-retro-proposal` beads, reused from curio. |
| Unexpected token cost on a transcript-heavy rig. | `budget_usd` default cap + summary line reporting sessions skipped for budget. |
| "Per-rig" — does it touch *my current* rig or the named one? | Require explicit `rig=` (no implicit "current rig"); validate-rig guard echoes the resolved rig path it will mine. |
| Proposal text contains scary commands quoted from logs. | UNTRUSTED OBSERVED TEXT banner + sanitization; agent never relays log text as a recommendation. |
| Disposer can't tell what to actually change. | One-edit-away contract: target file/mechanism + concrete change + evidence anchor in every bead. |

## Recommendation

1. **One required variable, `rig` (a rig name).** Mirror the adjacent formulas'
   single-substrate-pointer ergonomics. Everything else defaults safely.
   Canonical invocation: `gt formula run mol-rig-retrospect --set rig=<name>`.

2. **Reuse adjacent variable names verbatim** for zero relearn cost:
   `max_proposals` (advisory cap, default ~3–5), `dry_run` (preview, file
   nothing), `budget_usd` (cost cap for large transcripts), `lookback` (mining
   window, e.g. default 7d).

3. **First step is a `validate-rig` guard** (modeled on
   `mol-triage-retro`'s `validate-run-dir`): unknown rig → non-zero exit with the
   known-rig list and a closest-match hint; empty transcript window → a valid
   quiet zero-proposal result, not a failure.

4. **The proposal bead is the product. Enforce a one-edit-away contract** per
   bead: a `[kind]` title prefix (`[CLAUDE.md]`/`[hook]`/`[mcp]`/`[skill/perm]`/
   `[cli-ergo]`), the exact target file/mechanism, the concrete proposed change,
   the ranked friction evidence (session id + sanitized excerpt under an
   `⚠️ UNTRUSTED OBSERVED TEXT` banner), and a `cluster:<id>` dedup label. Apply
   **file-first discipline**: create + `bd sync` each bead the instant its kind
   and cluster are decided, before enrichment — a polecat can die mid-run.

5. **Standardize the disposer's one query** and document it in the formula
   header: `bd list --label rig-retro-proposal --status open --label rig:<name>`.
   Labels: `rig-retro-proposal` + `rig:<name>` + `cluster:<id>`.

6. **Emit a single run-summary line** answering the three operator anxieties
   (scope, cost, output): `rig=X · sessions mined=N (M skipped) · clusters=K ·
   proposals filed=P · dry_run=<bool>`. Report any cost-driven truncation
   explicitly — no silent caps.

7. **Terminate with `gt done`**, including on the zero-proposal quiet path —
   "done means gone," consistent with every polecat workflow.

8. **Header banners up top** (match curio/triage convention): what it mines, what
   it produces, and the DATA-not-instructions trust boundary, plus two examples
   (ad-hoc + dry-run). The formula should be fully legible at `gt formula show`
   without reading the source.

### Open questions for synthesis

- **Lens fan-out vs. single pass?** `mol-triage-retro` fans out 6 lenses; the
  four detection targets (friction / mistakes / bad tool calls / wasted cycles)
  could each be a lens. Fan-out improves recall but multiplies token cost on
  already-large transcripts — the scale leg should weigh this; the UX implication
  is whether to expose a `lenses=` subset selector (Tier 2) like triage does.
- **Default `lookback` window** — 7d nightly vs. a since-last-run watermark. A
  watermark avoids re-mining the same sessions but adds state; a fixed window is
  simpler and dedup handles repeats. Lean simple (fixed window + cluster dedup)
  for first cut.
- **Where do proposals route** — assignee `mayor` (like curio) vs. the rig's
  owner? Per-rig scoping argues for routing to the rig owner/maintainer; confirm
  with the integration leg.

## Sources

- [design.formula.toml](file://~/gt/.beads/formulas/design.formula.toml) — the convoy vehicle — accessed 2026-06-15
- [mol-curio-retrospect.formula.toml](file://~/gt/.beads/formulas/mol-curio-retrospect.formula.toml) — agent-proposes/human-disposes, file-first, cluster dedup, UNTRUSTED OBSERVED TEXT trust boundary, advisory cap — accessed 2026-06-15
- [mol-triage-retro.formula.toml](file://~/gt/.beads/formulas/mol-triage-retro.formula.toml) — validate-run-dir guard, dry_run, budget_usd, lens fan-out — accessed 2026-06-15
- Sibling convoy reference: `.designs/cv-2s6tq/ux.md` (upstream-sync UX analysis, format/altitude reference) — accessed 2026-06-15

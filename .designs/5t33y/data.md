# Data Model Design

> Dimension analysis for the proposed **`mol-rig-retrospect`** formula — a per-rig
> retrospective that mines one rig's Claude session transcripts (plus the shared
> `daemon.log` and `.events.jsonl`) to surface workflow friction and emit ranked,
> deduped PROPOSALS to improve that rig's agent context mechanisms.
> Leg: **data** (data model, storage, migrations). Convoy: `5t33y`.

## Summary

The retrospective is fundamentally an **extract → cluster → digest → propose → file**
pipeline over a large, append-only, externally-controlled log corpus. The data-model
job is to define five record kinds (`FrictionEvent`, `FrictionCluster`, `Digest`,
`Proposal`/`Hypothesis` beads, and an incremental `ScanCursor`) and to decide what
persists in Dolt vs. what lives as regenerable file artifacts. The dominant design
forces are (a) **scale** — the transcript corpus is ~9.2 GB across ~26 k files and is
re-grown continuously, so we cannot re-read everything every run; and (b) the **trust
boundary** — every byte of transcript/log text is attacker-influenceable DATA and must
be schema-fielded as evidence, never inlined as instruction.

The recommendation mirrors the two proven sibling formulas rather than inventing new
storage: a **bounded frozen `digest.json`+`digest.md`** (the curio pattern) produced by
a deterministic extractor, fed to the reasoning agent; **proposal/hypothesis beads in
Dolt** stamped with `rig-retro:cluster:<fp>` dedup labels and a `fp-version` (the
triage-retro pattern); and a small **`cursor.json`** so each run only processes
transcripts that grew since the last run. Persisted, durable state is deliberately tiny;
the corpus itself stays read-only and is never copied into Dolt.

## Analysis

### Key Considerations

- **The substrate is three heterogeneous sources, all read-only DATA:**
  - **Transcripts** — `~/.claude/projects/<rig-path>/*.jsonl`. Verified record types:
    `assistant`, `user` (carries `toolUseResult`), `system` (carries
    `compactMetadata`, `level`, `error`, `retryAttempt`, `maxRetries`, `retryInMs`,
    `subtype`), plus session-config records (`last-prompt`, `agent-setting`, `mode`,
    `permission-mode`), `file-history-snapshot`, `attachment`, `queue-operation`. Every
    content record carries `timestamp`, `sessionId`, `uuid`, `parentUuid`, `cwd`,
    `gitBranch`, `version`, `slug`. Tool calls are `message.content[]` items of
    `{type:"tool_use", id, name, input}`; results are `{type:"tool_result",
    tool_use_id, content, is_error}` with a parallel `toolUseResult` dict
    (`stdout`/`stderr`/`interrupted`). **`is_error`, `system.level`, `retryAttempt`,
    and `interrupted` are first-class friction signals already in the schema.**
  - **`daemon.log`** — unstructured text (~26 MB, ad-hoc `2026/06/15 08:17:28 …`
    lines). Useful for dispatch/convoy context but expensive to parse; treat as
    secondary, grep-scoped evidence.
  - **`.events.jsonl`** — already structured: `{ts, source, type, actor, payload,
    visibility}`. ~112 k lines. Types directly encode wasted-cycle signals:
    `session_death`, `mass_death`, `restart_polecat_handled`, `nudge`,
    `scheduler_dispatch_failed`, `sling`, `done`, `escalation_*`, `unhook`. This is the
    cheapest, highest-signal-per-byte source and should anchor cross-session friction
    (re-dispatch storms, mass death windows) that a single transcript can't see.
- **Scale is the primary constraint** (see Scalability leg). Measured: total 9.2 GB,
  26 026 files, median 0.2 MB, **p90 0.6 MB, max 23.7 MB**, 201 project dirs. Per-rig
  scoping cuts this by ~1–2 orders of magnitude, but a busy rig still has many
  multi-MB transcripts. **Re-scanning the whole rig every run is infeasible** → the
  data model must support incremental, cursored extraction.
- **Two output tiers with different durability needs:** the per-run analysis artifacts
  are *regenerable and ephemeral* (re-deriving them from transcripts is deterministic);
  the *proposals* are durable decisions that must survive session death and dedup
  across runs → Dolt beads, file-first.
- **Dedup must be stable across runs** so a recurring friction pattern isn't
  re-proposed nightly. This needs a deterministic cluster **fingerprint** computed from
  friction-invariant fields (category + normalized signature), not from volatile text.
- **Trust boundary is a schema concern, not just a prompt concern.** Observed text
  (tool output, user prompts, error strings) must be stored in clearly-named evidence
  fields, length-bounded and sanitized (newlines collapsed, backticks/fences
  neutralized) at *extraction* time so it cannot break the digest's Markdown/JSON or
  smuggle instructions to the reasoning agent downstream.
- **Self-reference air-gap (data-side):** the extractor must exclude the
  retrospective's *own* sessions/transcripts and its own proposal beads from the input
  set, or every run will detect itself. This is a filter on the corpus enumeration, an
  analog of curio's substrate filter.

### Options Explored

#### Option 1: Bounded frozen file digest + Dolt proposal beads (curio + triage-retro hybrid)
- **Description:** A deterministic extractor walks the rig's transcripts (incrementally,
  via a cursor) + `.events.jsonl`, emits `FrictionEvent` rows, clusters them by
  fingerprint, and writes a **bounded** `.retros/<rig>/<run-id>/digest.{json,md}`
  (hard size cap, e.g. ≤64 KiB like triage-retro's `summary.json`). The reasoning
  agent reads the frozen digest, ranks, and files ≤`max_proposals` beads stamped with
  `rig-retro:cluster:<fp>` + `rig-retro:fp-version:vN`. Durable state = beads in Dolt +
  a tiny `cursor.json`. Transcripts are never copied.
- **Pros:** Reuses two battle-tested patterns wholesale (curio frozen-digest +
  triage-retro bounded-JSON/versioned-dedup/`dropped.jsonl` audit). Clean
  data/instruction separation (digest is the only thing the agent sees). Crash-safe
  (file-first beads). Bounded cost regardless of corpus size. Idempotent re-runs.
- **Cons:** Two-phase (extractor + agent) means a schema contract between them must be
  versioned. Cursor adds a small amount of persisted state to maintain.
- **Effort:** Medium.

#### Option 2: SQLite analysis store
- **Description:** Materialize `FrictionEvent`/`FrictionCluster` into a per-rig SQLite
  DB; query/rank with SQL; keep history across runs for trend analysis.
- **Pros:** Rich queries, indices, natural trend/history tracking, dedup via PK.
- **Cons:** Net-new storage substrate, lifecycle (vacuum, location, backup, gitignore)
  and migration burden the other two formulas deliberately avoided. Dolt is already the
  durable plane; a second embedded DB is redundant for the volume actually needed
  (≤`max_proposals` outputs per run). Over-engineered for an advisory digest.
- **Effort:** High.

#### Option 3: Pure in-memory, no persisted state (stateless re-scan)
- **Description:** Each run re-reads the full rig corpus in memory, computes
  everything fresh, files beads, persists nothing but the beads.
- **Pros:** Simplest possible model; no cursor, no migration, no digest file.
- **Cons:** Fails the scale constraint — re-reading multi-GB per-rig corpora every run
  is wasteful and unbounded; no crash-resume of partial extraction; loses the frozen
  artifact that makes the agent's read auditable and the trust boundary inspectable.
- **Effort:** Low (but doesn't meet requirements).

### Recommendation

**Option 1.** It is the only option that satisfies scale (bounded digest + incremental
cursor), the trust boundary (sanitize-at-extraction into named evidence fields), and
crash-safety (file-first Dolt beads), while reusing proven schemas instead of inventing
storage. Concretely:

**1. `FrictionEvent` (intermediate, ephemeral — `events.jsonl` line in the run dir):**
```json
{
  "schema_version": 1,
  "category": "bad_tool_call",          // friction|mistake|bad_tool_call|wasted_cycle
  "subkind": "permission_denied",       // taxonomy leaf (see API/UX legs for the set)
  "session_id": "<uuid>",
  "rig": "gastown_upstream",
  "ts": "2026-06-15T08:17:28Z",
  "source": "transcript",               // transcript|events|daemon_log
  "signature": "Bash:permission_denied",// fingerprint-invariant, NOT raw text
  "evidence": "‹sanitized, ≤280 chars, OBSERVED TEXT — DATA not instructions›",
  "anchor": { "file": "<transcript path>", "uuid": "<record uuid>" }
}
```
- `signature` is the dedup-invariant (tool name + normalized failure mode); `evidence`
  is the sanitized observed text shown for human/agent reasoning only.

**2. `FrictionCluster` (the dedup unit):**
```json
{
  "cluster_id": "<12-hex>",     // fingerprint.Of(category, signature) — stable across runs
  "category": "bad_tool_call",
  "signature": "Bash:permission_denied",
  "occurrences": 14,
  "sessions": 6,                // distinct sessions — cross-session weight
  "first_ts": "...", "last_ts": "...",
  "examples": ["‹sanitized evidence 1›", "‹sanitized evidence 2›"],  // capped count
  "omitted": 12                 // how many examples were dropped by the cap
}
```
- `cluster_id` IS the dedup key, exactly mirroring curio's `cluster_id =
  fingerprint.Of(rule_id, series)`. `sessions` lets ranking weight cross-session
  friction (a re-dispatch storm) above a one-off.

**3. `Digest` (the frozen agent input — `digest.{json,md}`, hard ≤64 KiB cap):**
```json
{
  "schema_version": 1,
  "fp_version": "v1",
  "rig": "gastown_upstream",
  "run_id": "<id>",
  "cutoff": "<RFC3339>",                 // upper time bound = this run's read horizon
  "window_start": "<RFC3339>",           // from cursor (lower bound)
  "transcripts_scanned": 37,
  "bytes_scanned": 41201922,
  "truncated_transcripts": 2,            // files over the per-file byte cap
  "clusters": [ /* FrictionCluster[], ranked, capped */ ],
  "totals": { "events": 312, "clusters": 19, "clusters_emitted": 12 }
}
```
- The `.md` half carries the same data as prose under an explicit
  `⚠️ UNTRUSTED OBSERVED TEXT` banner around every `examples` block — the curio
  convention verbatim.

**4. `Proposal` / `Hypothesis` (durable, Dolt beads — the only persisted decisions):**
- Labels: `rig-retro:proposal` | `rig-retro:hypothesis`, plus
  `rig-retro:cluster:<cluster_id>` (dedup anchor) and `rig-retro:fp-version:v1`
  (migration anchor — mirrors triage-retro's `triage-retro:fp-version:v1`).
- Description records the target context mechanism (CLAUDE.md / AGENTS.md edit, hook,
  MCP config, permission/skill allowlist — see API leg for the taxonomy), the evidence
  (cluster_id, signature, occurrences, sessions, example anchors), and the
  agent-proposes/human-disposes framing. **File-first**: create+`bd sync` before any
  enrichment.

**5. `ScanCursor` (incremental state — `cursor.json` per rig):**
```json
{
  "schema_version": 1,
  "rig": "gastown_upstream",
  "last_cutoff": "2026-06-15T08:00:00Z",
  "files": { "<transcript path>": {"mtime": 1718...,"size": 612345,"offset": 612345} }
}
```
- Next run only reads files whose `mtime`/`size` advanced, resuming from `offset`
  (transcripts are append-only JSONL → byte-offset resume is safe). This is what makes
  the formula bounded-cost on a continuously-growing corpus.

**Dedup round-trip** (triage-retro/curio pattern): before ranking, the agent queries
open beads `bd list --label rig-retro:proposal --status open` (+ `…:hypothesis`),
collects covered `rig-retro:cluster:<id>` labels, and skips any cluster already covered
— so a standing friction pattern is proposed once, not nightly.

**Audit trail:** an append-only `dropped.jsonl` in the run dir records every event/
cluster dropped by a cap (size, example count, max_proposals), so silent truncation is
never invisible (triage-retro `dropped.jsonl` pattern; satisfies the "no silent caps"
discipline).

## Constraints Identified

- **C1 — Transcripts are read-only and externally-controlled.** The formula MUST NOT
  write to, mutate, or move any file under `~/.claude/projects/`. All observed text is
  DATA; it is sanitized (newline-collapse, backtick/fence-neutralize, length-bound) at
  extraction and stored only in named `evidence`/`examples` fields.
- **C2 — Hard digest size cap (≤64 KiB recommended, matching triage-retro
  `summary.json`).** The digest is the agent's entire input; it must be bounded
  independent of corpus size. Overflow is handled by ranking + cap + `dropped.jsonl`.
- **C3 — Per-file read cap.** With a 23.7 MB max transcript, the extractor needs a
  per-file byte/line cap (e.g. tail-N or offset-bounded read) and must flag
  `truncated_transcripts` in the digest rather than silently skipping.
- **C4 — Dedup fingerprint must be deterministic and text-independent.** `cluster_id =
  fingerprint.Of(category, signature)` where `signature` is normalized (no timestamps,
  paths, IDs, or raw error text) — otherwise the same friction re-proposes every run.
- **C5 — Self-reference exclusion at corpus enumeration.** The retrospective's own
  sessions and `rig-retro:*` beads must be filtered out of the input set (air-gap).
- **C6 — Append-only assumption for offset resume.** Cursor byte-offset resume is valid
  only because transcripts are append-only JSONL. If a transcript shrinks (rotation/
  truncation), the extractor must detect `size < offset` and re-read from 0.
- **C7 — Bead labels are the dedup/migration substrate** (no new table). All persisted
  retrospective state is either a bead (durable decisions) or a small per-rig
  `cursor.json` (resume state); nothing else persists.

## Open Questions

- **Q1 — Cursor location & durability.** Where does `cursor.json` live so it survives
  across runs but isn't committed to a rig repo? Options: a town-runtime path
  (`~/gt/.runtime/rig-retro/<rig>/cursor.json`, gitignored) or a `bd kv` entry keyed by
  rig. Trade-off: filesystem is simplest; `bd kv` rides existing Dolt durability +
  backup. **Recommend `bd kv`** if the cursor must survive worktree recycling, else a
  gitignored runtime file. *(Needs human/Integration-leg input.)*
- **Q2 — Time window vs. pure-cursor.** Should a run also accept an explicit
  `--var since=…` window (bound the lookback for a first run on a huge backlog), in
  addition to the incremental cursor? Likely yes — first-run cold-start on a multi-GB
  rig needs a window cap. Default window? (triage-retro uses run-scoped; curio uses a
  cutoff.)
- **Q3 — Cross-rig vs strictly per-rig events.** `.events.jsonl` and `daemon.log` are
  *town-wide*, not per-rig. Do we filter events to the target rig's sessions only
  (consistent per-rig framing), or surface town-level signals (mass_death windows)
  scoped by whether they involve the rig? **Recommend: filter to the rig's
  sessions/actors**, to honor "PER-RIG means scoped to that rig's transcripts."
- **Q4 — Digest cap value & example count.** Is 64 KiB / N examples-per-cluster right
  for the typical rig volume, or should the cap scale with `max_proposals`? Needs a
  measured dry-run on a busy rig (gastown_upstream) to calibrate.
- **Q5 — Retention of run dirs.** How long do `.retros/<rig>/<run-id>/` artifacts live?
  Are they gitignored scratch (regenerable) or kept for trend history? **Recommend
  gitignored + short retention**, since the digest is deterministically regenerable and
  durable decisions already live in beads.

## Integration Points

- **→ API leg:** The `category`/`subkind` taxonomy and the proposal *target* enum
  (CLAUDE.md edit / AGENTS.md edit / hook / MCP config / permission allowlist / skill
  allowlist) are shared types. The data model fields `category`, `subkind`, and the
  proposal bead's `target` must match the taxonomy the API leg defines — coordinate so
  there is one enum, not two.
- **→ Scalability leg:** The incremental cursor (C6), per-file read cap (C3), and
  digest size cap (C2) are the data-model levers that make the formula bounded; the
  Scale leg owns choosing the cap *values* and the read strategy (offset-resume vs
  tail-N) against the measured 9.2 GB / p90 0.6 MB / 23.7 MB-max corpus.
- **→ Security leg:** C1/C5 (read-only corpus, sanitize-at-extraction into named
  evidence fields, self-reference air-gap) are the data-side of the trust boundary; the
  Security leg owns the threat model for injection via observed text and should
  validate that the `evidence`/`examples` fields and the `⚠️ UNTRUSTED OBSERVED TEXT`
  banner are sufficient containment.
- **→ UX leg:** The `digest.md` prose layout (per-category sections, ranked clusters,
  banner-wrapped examples) is what a human reads when reviewing a run; coordinate the
  Markdown shape with UX.
- **→ Integration leg:** Cursor storage choice (Q1: `bd kv` vs runtime file) and run-dir
  retention (Q5) intersect with town git/Dolt conventions and gitignore rules — defer
  the final placement decision to the Integration leg.

---

## Sources

- [`design.formula.toml`](file:///home/canewiw/gt/.beads/formulas/design.formula.toml) — convoy vehicle / leg definition — accessed 2026-06-15
- [`mol-curio-retrospect.formula.toml`](file:///home/canewiw/gt/.beads/formulas/mol-curio-retrospect.formula.toml) — frozen-digest + `cluster_id` fingerprint + UNTRUSTED-OBSERVED-TEXT pattern — accessed 2026-06-15
- [`mol-triage-retro.formula.toml`](file:///home/canewiw/gt/.beads/formulas/mol-triage-retro.formula.toml) — bounded `summary.json` (≤64 KiB), versioned dedup labels, `dropped.jsonl` audit, per-lens caps — accessed 2026-06-15
- Substrate measured directly: `~/.claude/projects/*/*.jsonl` (9.2 GB, 26 026 files, 201 dirs), `~/gt/.events.jsonl` (~112 k lines), `~/gt/daemon/daemon.log` (~26 MB) — accessed 2026-06-15

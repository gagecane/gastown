# Scalability Analysis

## Summary

The proposed `mol-rig-retrospect` formula mines a single rig's raw Claude
session transcripts to surface workflow friction and propose context-mechanism
improvements. Its defining scaling property — and the thing that separates it
from the adjacent `mol-curio-retrospect` (reads a frozen metric digest) and
`mol-triage-retro` (reads a bounded ≤64 KiB `RetroSummary`) — is that **its
input substrate is raw, unbounded, agent-generated text**. Measured on this
live town: the transcript corpus is **9.2 GB across 26,035 `.jsonl` files in
202 rig project-dirs**. A single transcript runs as large as **22.6 MB ≈ 5.3M
tokens**; a single busy rig-dir (`deacon-dogs-alpha`) is **1.1 GB ≈ ~275M
tokens across 4,384 files**. No LLM context — and no affordable analysis
budget — holds even one large rig's raw transcripts. The auxiliary substrates
(`daemon/daemon.log` 25 MB, `.events.jsonl` 24 MB) are smaller but still
exceed a single context window.

The architectural conclusion is therefore forced and unambiguous: **the formula
must NOT feed raw transcripts to an LLM.** It must follow the proven
two-phase shape of both adjacent formulas — a cheap, deterministic
**extraction/digest phase** that reduces the rig's raw transcripts to a small,
bounded, fixed-schema friction digest (target ≤256 KiB, similar in spirit to
triage-retro's `RetroSummary` ≤64 KiB cap), followed by an **LLM proposer
phase** that reasons only over that digest. Everything that scales badly
(transcript volume) is confined to the deterministic phase, where it is a
bounded grep/parse cost; everything expensive per unit (LLM tokens) sees only
the bounded digest, which is invariant to transcript size. Get that boundary
right and the formula scales to 1000× the current corpus on commodity
hardware; get it wrong (any path where the LLM sees raw `.jsonl`) and the
formula is unaffordable on day one for any rig past the median.

## Analysis

### Key Considerations

**Scale dimensions (measured on this town, 2026-06-15):**

| Dimension | Current measured value | Notes |
|-----------|------------------------|-------|
| Total corpus | 9.2 GB / 26,035 files / 202 rig-dirs | `~/.claude/projects/<rig-path>/*.jsonl` |
| Per-transcript size | median 0.18 MB, p90 0.57 MB, **max 22.6 MB** | Long-lived agents (deacon, witness) dominate the tail |
| Per-transcript tokens | median ~45K, **max ~5.3M** | chars/4 estimate |
| Busiest rig-dir | **1.1 GB / 4,384 files / ~275M tokens** | `deacon-dogs-alpha` |
| Rigs > 100 MB | 17 of 202 | Heavily skewed distribution |
| Rigs > 10 MB | 116 of 202 | Most rigs are modest; a handful are huge |
| `daemon.log` | 25 MB | Shared, town-wide |
| `.events.jsonl` | 24 MB | Shared, town-wide |

**Transcript line structure** (sampled from a 21,405-line witness transcript):
records are typed JSONL — `assistant` (933), `user` (526), `system` (105),
`attachment` (97), plus `agent-setting`, `mode`, `permission-mode`,
`last-prompt`, `file-history-snapshot`, `queue-operation`. The signal this
formula wants (tool calls, errors, retries, permission prompts, idle gaps)
lives in a **structured subset** of these lines — which is exactly why a
deterministic extractor can shrink the corpus by 2–4 orders of magnitude before
any LLM sees it.

**The growth law that matters.** Per-rig transcript volume grows with
`(session count) × (avg session length) × (verbosity per turn)`. All three
trend *up* over a rig's lifetime, and the corpus is **append-only and
never pruned** by default. So "current scale" for a long-lived rig is already
the 10×–100× case for a young rig. The formula must be designed for the
deacon-dogs-alpha regime (1 GB+), not the median (0.18 MB) regime.

**Cost asymmetry — the core insight.** Deterministic scan of 1 GB of JSONL is
seconds of CPU and ~0 marginal dollars. LLM reasoning over the same 275M tokens
is, at any current model price, hundreds of dollars *per run per rig* and
physically impossible in one context. The entire scalability strategy is:
**push all volume-proportional work into the deterministic phase; keep the
LLM phase proportional to the bounded digest, not the substrate.**

### Resource consumption per run (recommended two-phase design)

| Resource | Extraction phase (deterministic) | Proposer phase (LLM) |
|----------|----------------------------------|----------------------|
| CPU | O(bytes scanned); ~seconds–low-minutes for 1 GB streamed | negligible |
| Memory | O(1) streaming, or O(window) — never load full corpus | digest only (≤256 KiB) |
| Disk read | full rig-dir once (bounded by `--since` window) | digest only |
| Disk write | one digest artifact (≤256 KiB) | proposal beads/CRs |
| LLM tokens | **0** | ~digest size + reasoning; **invariant to transcript volume** |
| $ cost | ~0 | bounded; same for a 1 MB rig and a 1 GB rig |
| Wall time | scan time (I/O bound, bounded by window) | one polecat reasoning pass |

### Bottlenecks: what limits growth?

1. **(Primary) LLM context window** — the binding constraint *if and only if*
   the design is naive. A single max transcript (5.3M tokens) already exceeds
   every context window. This bottleneck is *eliminated by construction* in the
   two-phase design and *fatal* in any single-phase design.
2. **(Secondary) Deterministic scan I/O** — at 1 GB/rig the extractor is
   disk-read bound. Mitigated by an incremental `--since` window (only scan
   transcripts modified since last run) and streaming parse. This is the real
   bottleneck once context is solved, and it is gentle (linear, cheap, parallel-
   izable).
3. **(Tertiary) Digest size drift** — if the extractor's friction-signal
   density is high (a pathological rig where every turn is a retry), the digest
   itself could approach the LLM context limit. Solved by a hard digest cap +
   ranked truncation (keep the top-N clusters by frequency/severity, record
   `omitted=N`), exactly as triage-retro caps `RetroSummary` at 64 KiB and
   curio-retrospect caps clusters with an `omitted` count.
4. **(Operational) Polecat fleet slot** — each retrospect run consumes one
   polecat slot for the duration. Per-rig + scheduled cadence keeps this to one
   slot per rig per period; fanning out across all 202 rigs simultaneously
   would saturate the fleet (see Constraints).

### Complexity

- **Time**: extraction is `O(B)` in bytes scanned (linear, streaming),
  reducible to `O(ΔB)` with an incremental window. Proposer is `O(D)` in
  digest size, which is `O(1)` (capped). Overall per-run: **`O(ΔB)`**.
- **Space**: `O(1)` streaming extraction (or `O(window)` if buffering a session
  for gap-detection); `O(D)` capped digest. Never `O(corpus)`.
- **Token (the expensive axis)**: `O(D)` = `O(1)` capped. **Decoupled from
  substrate size** — the single most important property of the design.

### Caching opportunities

1. **Incremental extraction (highest value).** Persist a per-rig watermark
   (last-processed transcript mtime / line offset). Each run scans only new or
   grown transcripts. Converts per-run cost from `O(corpus)` to `O(new
   activity since last run)` — turns the 1 GB rig into a ~tens-of-MB delta per
   daily run. This is the difference between a feasible daily cadence and an
   unaffordable one.
2. **Cached digest with content-addressed key.** If no transcripts changed
   since last run (dormant rig), skip both phases and emit "nothing new" — same
   short-circuit `mol-curio-retrospect` uses when its digest is empty
   ("the lane has nothing to do … do NOT fabricate clusters").
3. **Cluster fingerprint dedup across runs.** Reuse curio's proven
   `fingerprint.Of(...)` → stable `cluster_id` pattern so a friction cluster
   that recurs across runs is recognized as a duplicate and not re-proposed.
   This caches *proposal identity*, preventing re-proposal storms.

### Degradation modes: what happens at limits?

- **Digest cap exceeded** → ranked truncation, `omitted=N` recorded, proposer
  proceeds on the top clusters with a `partial=true`/low-confidence marker
  (triage-retro's `partial` status precedent). Graceful, not fatal.
- **Single pathologically huge transcript (>>5M tokens)** → the extractor
  streams it (never loads whole); only extracted friction signals enter the
  digest. No degradation as long as extraction is streaming, not buffered.
- **Extraction wall-time budget exceeded** (very large delta) → emit a partial
  digest covering the most-recent window, mark truncated, finish via `gt done`.
  Never block the polecat slot indefinitely.
- **Corrupt/partial JSONL line** → skip-and-count (transcripts can be
  mid-write); never abort the whole run on one bad line.

### Options Explored

#### Option 1: Single-phase — LLM reads raw transcripts directly

- **Description**: Polecat globs the rig's `*.jsonl` and reads them into context
  to reason about friction.
- **Pros**: Simplest formula to write; no extractor to build; maximal signal
  fidelity (LLM sees everything).
- **Cons**: **Fails immediately at observed scale.** The max single transcript
  (5.3M tokens) exceeds every context window; a busy rig (275M tokens) exceeds
  it by ~3 orders of magnitude. Unbounded, unaffordable per-run cost. Also
  maximally exposes the LLM to the untrusted-text injection surface (every byte
  of tool output reaches the model). Non-starter.
- **Effort**: Low to write, infinite to operate.
- **Scaling**: Broken at 1×; catastrophically broken at 10×+.

#### Option 2: Two-phase — deterministic extractor → bounded digest → LLM proposer (RECOMMENDED)

- **Description**: A deterministic Phase A extractor streams the rig's
  transcripts (within a `--since` window), pattern-matches friction signals
  (tool errors, `tool-not-found`, permission prompts, retries/backtracking,
  long idle gaps, reverted commits), clusters and ranks them, and emits a
  capped fixed-schema digest. A Phase B LLM polecat reasons *only* over the
  digest and emits ranked proposals (agent-proposes/human-disposes).
- **Pros**: Volume-proportional work is deterministic and cheap; LLM cost is
  bounded and invariant to substrate size. Matches both proven precedents.
  Confines the untrusted-text trust boundary to a small, sanitizable digest.
  Naturally supports incremental scanning and dedup.
- **Cons**: Requires building/maintaining the extractor (the friction-pattern
  matchers are the real engineering work and will need tuning). Some signal is
  lost in reduction — a friction pattern the extractor doesn't recognize is
  invisible to the proposer (mitigated by including a small sample of
  highest-entropy un-clustered turns in the digest).
- **Effort**: Medium (extractor + schema + caps). The LLM side is light.
- **Scaling**: Flat LLM cost from 1× to 1000×; extraction scales linearly and
  is tamed to `O(delta)` by incremental windowing.

#### Option 3: Sampled / windowed extraction

- **Description**: Like Option 2, but the extractor only inspects the most
  recent K sessions or a time window (e.g. last 7 days), explicitly sampling
  rather than aiming for completeness.
- **Pros**: Hard upper bound on extraction cost regardless of corpus size;
  trivially affordable; recency-weighted (recent friction is most actionable).
- **Cons**: Misses older recurring friction; sampling bias. But note: with
  incremental watermarking (Option 2's cache), *completeness over time is free*
  — you don't need to sample, you just process the delta each run. Sampling is
  the fallback when no watermark exists (first run on a 1 GB rig).
- **Effort**: Low (it's Option 2 with a window bound).
- **Scaling**: Constant cost by construction; the natural **first-run / cold-
  start** mode and the **budget-exceeded degradation** mode for Option 2.

#### Option 4: Map-reduce extraction across transcripts (parallel sub-agents)

- **Description**: Fan out N deterministic extractors (or even LLM mini-
  summarizers) across transcript shards, then reduce their partial digests.
- **Pros**: Parallelizes extraction wall-time; could handle a 1 GB cold-start
  first run faster.
- **Cons**: Over-engineered for a deterministic extractor (grep over 1 GB is
  already seconds, not minutes — parallelism buys little and costs complexity).
  If the map step uses LLMs, it reintroduces per-volume token cost — the exact
  thing we're avoiding. Fleet-slot cost multiplies.
- **Effort**: High.
- **Scaling**: Good wall-time, poor cost/complexity tradeoff. Reserve for a
  future 10,000-rig federation, not v1.

### Recommendation

**Option 2 (two-phase: deterministic extractor → bounded digest → LLM
proposer)** for v1, with **Option 3 (windowed/incremental)** baked in as both
the cold-start mode and the steady-state cache. Concretely:

1. **Deterministic Phase A extractor.** Stream `*.jsonl` for the target rig.
   Match a fixed catalogue of friction signals against the *structured* fields
   (tool-result error flags, permission-prompt records, retry/backtrack
   heuristics, inter-turn timestamp gaps for idle detection, reverted-commit
   markers). Emit a fixed-schema digest with a **hard size cap (recommend ≤256
   KiB)**, ranked clusters, per-cluster stable `cluster_id` fingerprint, and an
   `omitted` count when truncated. Mirror triage-retro's `RetroSummary`
   contract and curio's digest JSON contract.
2. **Incremental watermark.** Persist last-processed mtime/offset per rig so
   steady-state runs cost `O(delta)`, not `O(corpus)`. First run on a large rig
   falls back to a recency window (Option 3) and marks `partial=true`.
3. **LLM Phase B proposer reads only the digest.** Never the raw transcripts.
   Token cost is bounded and identical for a 1 MB and a 1 GB rig.
4. **Reuse curio's enforced backstops:** advisory `max_proposals` cap +
   `cluster:<id>` dedup so re-proposals across runs are recognized duplicates.

**The single most important design choice for scalability:** *the LLM must
never touch raw `.jsonl`.* This is not a tuning knob — it is the load-bearing
wall. Every scaling property above depends on it.

## Constraints Identified

1. **HARD: No LLM phase may read raw transcripts.** The max single transcript
   (5.3M tokens) already exceeds context; a busy rig (275M tokens) exceeds it
   by ~1000×. The extractor↔proposer boundary is a correctness constraint, not
   an optimization. (Measured, not estimated.)

2. **HARD: Digest must be size-capped with graceful truncation.** Without a cap
   (recommend ≤256 KiB; triage-retro uses 64 KiB), a high-friction rig's digest
   can itself blow the context budget. Cap + rank + `omitted=N`.

3. **Extraction must stream, never buffer the whole corpus.** `O(1)`/`O(window)`
   memory. A 1 GB rig-dir must not be loaded into memory.

4. **Append-only, never-pruned substrate.** Per-rig volume only grows. Design
   for the tail (1 GB+), not the median (0.18 MB). Incremental watermarking is
   effectively mandatory for a daily cadence on long-lived rigs.

5. **Per-rig scoping is a scaling feature, not just an API choice.** Scoping to
   one rig's transcripts bounds each run's substrate to one rig-dir (≤1.1 GB
   today) rather than the full 9.2 GB corpus. A town-wide variant would need
   sharding/federation (out of scope for v1).

6. **Fleet-slot budget across rigs.** One polecat slot per rig per run. Running
   all 202 rigs at once would saturate the fleet — the scheduler (Deacon
   patrol) must stagger per-rig dispatch, not thunder-herd.

## Open Questions

1. **What `--since` window for cold-start first runs on huge rigs?** A first
   run on `deacon-dogs-alpha` (1.1 GB) can't process everything within a sane
   wall-time budget. Recommend a default recency window (e.g. last 7 days or
   last K sessions) with `partial=true`; needs a human-set default. *(Cross-
   dimension: data — digest schema's `partial`/`omitted` fields.)*

2. **Digest size cap — 64 KiB (triage parity) or larger (256 KiB)?** This
   formula's substrate is richer/noisier than triage's single-run logs, which
   argues for a larger cap; but larger caps cost LLM tokens. Needs a token-
   budget decision. *(Cross-dimension: data, and the proposer's cost.)*

3. **Should the extractor be Go (like curio/triage substrate) or a scripted
   pass?** Go gives a typed schema + a shared injection-inert acceptance test
   (curio's `TestRenderDigest_InjectionInert`); a script is faster to prototype
   but weaker on the trust boundary. *(Cross-dimension: security, integration.)*

4. **Run cadence and trigger.** Scheduled (Deacon patrol, staggered) vs
   on-demand vs activity-threshold ("only when rig added >X MB since last
   run")? Activity-threshold composes best with the watermark cache and avoids
   wasted runs on dormant rigs. *(Cross-dimension: integration, ux.)*

5. **How much un-clustered raw sampling to include in the digest?** To avoid
   the extractor's fixed pattern-catalogue blinding the proposer to *novel*
   friction, include a small sample of highest-entropy un-matched turns. How
   many, and sanitized how? *(Cross-dimension: security trust boundary.)*

## Integration Points

- **Data dimension** owns the digest schema. My constraints (size cap, ranked
  clusters, stable `cluster_id` fingerprint, `partial`/`omitted` fields,
  watermark/offset persistence) are scaling requirements *on* that schema. The
  schema is the contract that makes the extractor↔proposer cost decoupling real.

- **Security dimension** and I share the same boundary from opposite sides: the
  extractor↔digest cut is *both* the scaling lever (volume confined to cheap
  deterministic code) *and* the trust boundary (raw untrusted transcript text —
  which embeds arbitrary tool output and user prompts — is reduced and
  sanitized before any LLM sees it). Curio's `⚠️ UNTRUSTED OBSERVED TEXT`
  banner + sanitization + `TestRenderDigest_InjectionInert` is the shared
  precedent. Smaller digest = smaller attack surface = the scaling and security
  goals are aligned, not in tension.

- **Integration dimension** owns dispatch/cadence. Scaling needs: per-rig
  scoping (bounds substrate per run), staggered scheduling across 202 rigs (no
  thundering herd on disk I/O), and the incremental watermark living in a
  durable per-rig location. The Deacon patrol is the natural dispatcher; it
  already manages staggered per-target dispatch.

- **API/UX dimensions** own the `rig-name` variable and the `max_proposals`
  cap. My input: the cap bounds the proposer's *output* cost; the `--since`
  window bounds the extractor's *input* cost. Both should be operator-tunable
  variables (curio/triage both expose `max_proposals`/`budget_usd`).

- **Synthesis note:** the recommendation here (two-phase, LLM-never-reads-raw)
  is the same architecture both adjacent formulas already validated in
  production. This dimension's contribution is the *measured proof* that for
  this substrate it is mandatory rather than merely preferred — 9.2 GB corpus,
  5.3M-token max transcript, 275M-token max rig — and the specific knobs
  (≤256 KiB digest cap, incremental watermark, recency-window cold start) that
  keep it flat from 1× to 1000×.

## Sources

- Live transcript corpus measurement: `~/.claude/projects/` (9.2 GB / 26,035 `.jsonl` / 202 rig-dirs) — measured 2026-06-15
- [mol-curio-retrospect.formula.toml](file:///home/canewiw/gt/.beads/formulas/mol-curio-retrospect.formula.toml) — frozen-digest + trust-boundary + advisory-cap patterns — accessed 2026-06-15
- [mol-triage-retro.formula.toml](file:///home/canewiw/gt/.beads/formulas/mol-triage-retro.formula.toml) — bounded `RetroSummary` ≤64 KiB, `partial`/truncation degradation, per-lens budget cap — accessed 2026-06-15
- [design.formula.toml](file:///home/canewiw/gt/.beads/formulas/design.formula.toml) — convoy vehicle / leg+synthesis structure — accessed 2026-06-15
- Sibling convoy reference for depth/format: `.designs/cv-2s6tq/scale.md` — accessed 2026-06-15

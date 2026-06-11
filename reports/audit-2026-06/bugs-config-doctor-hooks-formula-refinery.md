# Bug Hunt Audit — `internal/config/`, `internal/doctor/`, `internal/hooks/`, `internal/formula/`, `internal/refinery/`

- **Bead:** gu-nid89.6 (epic gu-nid89: Whole-Repo Gastown Audit)
- **Date:** 2026-06-11
- **Auditor:** polecat pipboy (gastown_upstream)
- **Scope:** `internal/config/` (~25K LOC), `internal/doctor/` (~47K LOC, 89 files),
  `internal/hooks/` (~7K LOC), `internal/formula/` (~6K LOC), `internal/refinery/` (~12K LOC).
  ~96K LOC combined.
- **Method:** `go vet` (clean across all five packages) + 6 parallel deep-read sub-audits
  over functional clusters (config; formula+hooks; refinery; doctor split into 3 chunks),
  followed by orchestrator re-verification of **every** HIGH/MED candidate against the actual
  source — each HIGH below was re-read at file:line and traced by the orchestrator before
  filing.

## Summary

| Confidence | Count |
|------------|-------|
| HIGH (verified, clearly wrong) | 5 |
| MED (likely bug, context-dependent) | 8 |
| LOW (suspicious / latent) | 9 |

`go vet ./internal/{config,doctor,hooks,formula,refinery}/...` is clean. These packages are
generally careful (defensive nil-guards in config, pointer-typed optional fields, crew-session
guards in most doctor checks, defensive slice-copies + final re-gate in refinery bisection).
The findings below survived skeptical re-tracing.

**The two most serious findings cause silent data destruction:**
- **H1 (refinery):** declares an async PR merge "successful" when nothing has merged, then
  closes beads and deletes the polecat branch — losing work CRUX hasn't landed yet.
- **H4 (doctor `idle-timeout` `--fix`):** blanks every rig's beads `prefix`/`issue-prefix` line
  while "fixing" an unrelated cosmetic setting.

New beads were filed for the **5 confirmed HIGH bugs** (per acceptance criteria).

---

## HIGH — confirmed, beads filed

### H1. Refinery declares async (CRUX) PR merge "successful" with a fabricated SHA → closes beads, deletes branch, strands unmerged work
- **File:** `internal/refinery/engineer.go:1055-1086` (+ `pr_provider_crux.go:MergePR`, `internal/git/git.go:2378-2394`)
- **Bug:** In `doMergePR`, after `e.prProvider.MergePR(...)` returns `(mergeCommit, err)`:
  - The CRUX provider's `MergePR` **always returns `("", nil)`** — its own doc comment says
    "CRUX does not surface a merge commit SHA synchronously" and "CRUX merges it once required
    approvals land." So at call time **nothing has merged yet**.
  - Lines 1064-1068 then `Checkout(target)` + `Pull("origin", target)`, making local `HEAD`
    equal to the current `origin/<target>` tip.
  - Lines 1070-1074: because `mergeCommit == ""`, it synthesizes `mergeCommit = Rev("HEAD")`
    — i.e. the freshly-pulled target tip, which has nothing to do with this MR.
  - Line 1075 `VerifyPushedCommit("origin", target, mergeCommit)` compares the push-remote
    `target` tip to that same just-pulled SHA, so the check **passes trivially, verifying
    nothing**. The function returns `Success: true`.
- **Impact:** The caller (`processSingleMR`/`ProcessMRInfo` → `HandleMRInfoSuccess`,
  engineer.go:1504+) treats this as a real merge: closes the source bead, closes the MR bead,
  and **deletes the polecat branch locally and remotely** (engineer.go:1584-1605). For a
  `polecat/*` branch the remote delete is unconditional unless `HasOpenPR` returns true — and
  `HasOpenPR` is `gh`-only (git.go), so on an Amazon/CRUX remote it fails-open to `false` and
  the remote delete proceeds. The work CRUX has not yet merged is destroyed/stranded off
  mainline, and the bead is closed as shipped. Note the deliberate `gs-4uz`/`gu-ilf86`
  already-merged path *above* (lines 1004-1024) correctly uses `IsAncestor(mergeCommit, target)`
  to fail-closed against phantom merges — the main synchronous path lacks that guard.
- **Verified:** Read `doMergePR` end-to-end; confirmed CRUX `MergePR` returns `""`; confirmed
  `VerifyPushedCommit` requires exact tip-equality which post-pull HEAD always satisfies;
  traced `Success:true` → `HandleMRInfoSuccess` → `DeleteBranch`/`DeleteRemoteBranch`; confirmed
  `MergeStrategy == "pr"` dispatches here (engineer.go:791-792).
- **Fix sketch:** When the provider returns no synchronous SHA (async providers like CRUX),
  do **not** fabricate the SHA from post-pull HEAD. Treat it as pending (leave MR in queue /
  return `NeedsApproval`), or verify the PR/CR is actually in a *merged* state before declaring
  success — mirror the `IsAncestor` fail-closed guard already used in the already-merged path.
- **Bead:** **gu-nid89.34**

### H2. Bitbucket PR merge treats HTTP error responses as success → same fabricated-SHA data loss
- **File:** `internal/git/git.go` `BitbucketPRMerge` (consumed by `pr_provider_bitbucket.go:MergePR` → `doMergePR`)
- **Bug:** `BitbucketPRMerge` runs `curl -s -X POST .../merge` and only inspects curl's **process**
  exit (`err` from `CombinedOutput`). With `-s` and no `--fail`/`--fail-with-body`, curl exits 0
  on HTTP 403/409/401 (branch restriction, merge conflict, auth failure). The error JSON body
  has no `merge_commit.hash`, so either (a) it unmarshals with `Hash == ""` → falls through to
  `sha, _ := g.Rev("HEAD")` and returns the local tip, or (b) on unmarshal failure it explicitly
  `pull`s and returns `Rev("HEAD")`. Both paths return a **non-empty fabricated SHA** for a merge
  that was rejected.
- **Impact:** A Bitbucket-rejected merge is reported as merged → feeds H1's success path → source/
  MR beads closed, polecat branch deleted, nothing landed. Wrong-merge / lost-work class.
- **Verified:** Read `BitbucketPRMerge`; confirmed no `--fail` flag and no HTTP-status inspection;
  both the unmarshal-success-empty-hash and unmarshal-failure branches return `Rev("HEAD")`.
- **Fix sketch:** Add `--fail-with-body` (or `-w '%{http_code}'`) and reject non-2xx; never
  fabricate a SHA from local HEAD when the API did not return a merge commit.
- **Bead:** **gu-nid89.35**

### H3. `hooks-path-configured` check skips every new-layout polecat clone (false negative → missing pre-push hook undetected)
- **File:** `internal/doctor/rig_check.go:340-352` (`HooksPathConfiguredCheck.Run`)
- **Bug:** Polecat clones are appended to `clonePaths` as `polecats/<name>` (old flat layout)
  only. The current layout nests the clone one level deeper: `polecats/<name>/<rigName>/.git`.
  The guard at line 352 then `os.Stat`s `polecats/<name>/.git`, which does not exist for new-
  layout polecats, so every new-layout polecat is silently `continue`d and never checked. Every
  other polecat-walking check in the package (`PolecatClonesValidCheck`, `BareRepoExistsCheck`,
  `CloneDivergenceCheck`, `SparseCheckoutCheck`) handles both layouts; this one does not.
- **Impact:** Polecat clones with `core.hooksPath` unset are never detected or fixed. The pre-push
  hook (which blocks pushes to invalid branches / enforces gate checks) is silently absent on
  those clones — exactly the failure this check exists to catch. (Confirmed live: this auditor's
  own worktree is `polecats/pipboy/gastown_upstream/.git`; the check stats `polecats/pipboy/.git`,
  which is absent.)
- **Verified:** Read the loop; confirmed the `.git` stat at 352 short-circuits the new layout;
  compared against the dual-layout handling in the sibling checks.
- **Fix sketch:** For each polecat entry, prefer `filepath.Join(polecatDir, name, rigName)` when
  it has a `.git`, else fall back to `filepath.Join(polecatDir, name)`.
- **Bead:** **gu-nid89.36**

### H4. `idle-timeout` `--fix` blanks every rig's beads `prefix`/`issue-prefix` line
- **File:** `internal/doctor/idle_timeout_check.go:139`
- **Bug:** `Fix` calls `beads.EnsureConfigYAML(rigBeadsPath, "")` with an **empty** prefix.
  `ensureConfigYAML` (internal/beads/config_yaml.go) unconditionally rewrites any existing
  `prefix:`/`issue-prefix:` line to `"prefix: " + prefix` / `"issue-prefix: " + prefix` — i.e. to
  a blank value — whenever it finds one (`strings.HasPrefix(trimmed, "prefix:")` → `lines[i] =
  wantPrefix`). So "fixing" the cosmetic `dolt.idle-timeout` setting silently wipes the rig's
  beads prefix. The sibling `AutoStartCheck.Fix` author hit this exact trap and guarded against
  it (auto_start_check.go:136-141: derives the prefix via `beads.ConfigDefaultsFromMetadata`
  precisely "so EnsureConfigYAML does not blank the existing prefix line"). The idle-timeout Fix
  lacks that guard.
- **Impact:** Corrupts beads routing / ID generation for every rig that already had a `config.yaml`
  with a prefix, on any `gt doctor --fix` run that touches idle-timeout. Persistent state damage.
- **Verified:** Read both Fix paths and `ensureConfigYAML` end-to-end; confirmed prefix lines are
  rewritten (not preserved) when the passed prefix is empty; confirmed the auto_start sibling's
  explicit guard against the same call.
- **Fix sketch:** Mirror `auto_start_check.go`: `prefix := beads.ConfigDefaultsFromMetadata(
  rigBeadsPath, ""); beads.EnsureConfigYAML(rigBeadsPath, prefix)`.
- **Bead:** **gu-nid89.37**

### H5. `stale-dolt-port` uses a private divergent port resolver → false positives + destructive `--fix` on towns with a non-3307 port
- **File:** `internal/doctor/stale_dolt_port_check.go:53-56`, `144-173` (resolver); Fix `126-142`
- **Bug:** `getCorrectPort()` reads the port **only** from `.dolt-data/config.yaml`
  (`listener.port`); if that file is absent or has no matching `port:` line it returns 0 and
  `Run()` hardcodes `correctPort = 3307`. But the canonical resolution used everywhere else
  (`doltserver.DefaultConfig` → `readPortFromConfigYAML` → `GT_DOLT_PORT` env → `mayor/daemon.json`
  `Env.GT_DOLT_PORT` → 3307) supports custom ports set **without** config.yaml. The codebase's own
  docs/comments call out the daemon.json/`GT_DOLT_PORT=3308` custom-port case, and the sibling
  `migration_check.go` correctly uses `DefaultConfig`.
- **Impact:** On any town whose real Dolt port comes from daemon.json/env (e.g. 3308) with no
  config.yaml port line, `correctPort` becomes 3307. Every `dolt-server.port` file and every
  `metadata.json` holding the true port is flagged stale (false positive), and under `--fix`,
  `Fix()` **deletes the port files** (`os.Remove`) and **rewrites every metadata.json's port to
  3307** — actively corrupting correct config and pointing bd at a dead/wrong server.
- **Verified:** Read both resolvers; confirmed `DefaultConfig` precedence and the existence of the
  canonical `readPortFromConfigYAML` the check ignores; confirmed the Fix deletes files + rewrites
  metadata. (The first `getCorrectPort` branch, `TrimSpace(line) == "port:"`, is also effectively
  dead — see L8.)
- **Fix sketch:** Replace `getCorrectPort()` with `doltserver.DefaultConfig(ctx.TownRoot).Port` so
  the check and the server agree on the authoritative port.
- **Bead:** **gu-nid89.38**

---

## MED — likely bugs (not filed; recommend follow-up)

### M1. `idle-timeout` is not the only check at risk — but the rest are guarded
The prefix-blank class (H4) is specific to idle-timeout. Audited siblings (`auto_start_check`)
are guarded. No other unguarded `EnsureConfigYAML(_, "")` Fix call found.

### M2. `MergeSettingsCommand` can never disable a repo-enabled merge queue
- **File:** `internal/config/loader.go:385-387`
- **Bug:** `if local.Enabled { result.Enabled = local.Enabled }` — guarded by the value itself, so
  a local override of `Enabled:false` is a no-op (can only flip false→true). `Enabled` is a plain
  `bool` (not `*bool`), unlike sibling toggles `RunTests`/`DeleteMergedBranches`/`RequireReview`
  which correctly use `!= nil` and honor an explicit false.
- **Impact:** Latent today — the only live caller (`sling_helpers.go:1500`) doesn't read
  `.Enabled`. But the function is general-purpose and the override semantics are wrong; the first
  consumer that reads merged `.Enabled` to gate the queue will silently keep it enabled.
- **Fix sketch:** Make `Enabled` a `*bool` and merge with `!= nil`, or drop the `Enabled` overlay.

### M3. `CrewWorktreeCheck` auto-removes any hyphenated crew worktree; source-rig guard is dead code
- **File:** `internal/doctor/crew_check.go:147-158` (Fix `:86`)
- **Bug:** A crew dir is flagged "stale cross-rig worktree" if its `.git` is a file (worktree) and
  its name contains a hyphen. The comment says "Verify the source rig exists" but line 150 is
  `_ = filepath.Join(townRoot, sourceRig)` — the result is discarded, so collection is
  unconditional. Fix runs `git worktree remove --force`, discarding uncommitted work.
- **Impact:** A legitimately hyphen-named crew worktree is force-removed under `--fix`, losing
  uncommitted changes. Narrow (requires hyphen + worktree-not-clone) but unsafe.
- **Fix sketch:** Gate collection on `dirExists(filepath.Join(townRoot, sourceRig))` against the
  registered rig set, not "has a hyphen."

### M4. `SocketSplitBrainCheck.Fix` kills crew sessions (no crew guard)
- **File:** `internal/doctor/tmux_socket_check.go:98-109, 149-152`
- **Bug:** Run collects every `IsKnownSession` Gas Town session on the default socket into
  `staleSessions`; Fix `KillSessionWithProcesses` on all. No `isCrewSession` exclusion, unlike the
  orphan and zombie checks which explicitly protect human-managed crew sessions.
- **Impact:** A crew session on the default socket is auto-killed (with descendants) by
  `gt doctor --fix`.
- **Fix sketch:** `if isCrewSession(s) { continue }` in Fix (and exclude from `staleSessions`).

### M5. `stale-agent-beads` closes live agent beads when the workers dir is transiently unreadable
- **File:** `internal/doctor/stale_agent_beads_check.go:96-108` (via `listCrewWorkers`/`listPolecats`)
- **Bug:** Those listers return `nil` on **any** `os.ReadDir` error, not just not-exist. A transient
  permission/NFS/IO error makes the on-disk set look empty → all open crew/polecat agent beads
  classified stale → `Fix()` closes them.
- **Impact:** A filesystem hiccup during `--fix` can mass-close live agent identity beads, breaking
  dispatch until recreated.
- **Fix sketch:** Distinguish `os.IsNotExist` (→ empty) from other errors (→ skip that rig).

### M6. `daemon_check.go` prints heartbeat count as a single Unicode rune
- **File:** `internal/doctor/daemon_check.go:49`
- **Bug:** `"Heartbeats: "+string(rune(state.HeartbeatCount))` where `HeartbeatCount` is `int64`.
  `string(rune(42))` → `"*"`, not `"42"`. The file already defines a correct `itoa` helper used
  two lines above for the PID.
- **Impact:** Garbage `gt doctor` output (display-only, no control-flow effect).
- **Fix sketch:** `"Heartbeats: " + itoa(int(state.HeartbeatCount))`.

### M7. `Resolve` drops `Inputs`/`Prompts`/`Output`/`ReviewOnly` from composed formulas
- **File:** `internal/formula/parser.go:567-576`
- **Bug:** The `merged` formula built during `extends`/`compose` copies only Name, Description,
  Type, Version, Pour, Agent, Compose, Vars, Steps — omitting `Inputs`, `Prompts`, `Output`,
  `ReviewOnly` (and others). Latent today (current embedded `extends` formulas use only
  vars+steps) but bites the first composed formula declaring those sections.
- **Fix sketch:** Copy/inherit the remaining child fields into `merged`.

### M8. `ResolveFormulaContent` swallows non-not-exist read errors and falls through tiers
- **File:** `internal/formula/embed.go:62-78`
- **Bug:** Both rig-tier and town-tier reads use `if content, err := os.ReadFile(path); err == nil`
  — any error (perms, IO, dir-in-the-way) is indistinguishable from absent and silently falls
  through to the embedded default, violating the `rig > town > embedded` precedence under error.
- **Fix sketch:** Return the error when `err != nil && !os.IsNotExist(err)`; only fall through on
  not-exist.

---

## LOW — suspicious / latent (not filed)

- **L1.** `internal/config/env.go:545-553` — `lookupConfigEnv` treats an explicitly-empty
  daemon.json value as "unset" (`ok && v != ""`), so daemon.json cannot *clear* a stale inherited
  process-env value; it re-reads `os.Getenv`. Same pattern in `resolveDoltPort` (env.go:582).
- **L2.** `internal/config/env.go:604-627` — `parsePortFromConfigYAML` is brittle against any
  hand-edit that nests under `listener:` (safe for machine-generated files only).
- **L3.** `internal/config/types.go:480, 274, 277` — `Default*` doc comments disagree with the
  actual constants in `operational.go` (doc-only; accessors return the constant).
- **L4.** `internal/formula/overlay.go:126-146` — `ApplyOverlays` ModeSkip splices a skipped step's
  needs into dependents **without dedup**, producing duplicate `needs` edges (e.g. `[a, a]`).
  `TopologicalSort` survives because in-degree and reverse-adjacency are built from the same slice
  (double-count cancels), but the corrupted list is emitted downstream.
- **L5.** `internal/formula/variable_validation.go:12,137-234` — `ValidateTemplateVariables`
  regex only matches `{{name}}`, never block helpers (`{{#if x}}`/`{{#each x}}`), so block-arg
  variables are never validated against `[vars]` despite the docstring's claim.
- **L6.** `internal/formula/parser.go:686-708` / `696-704` — expansion template IDs without
  `{target}` can collide silently at authoring time (caught only later at compose); and the
  "first step inherits target's needs" comment doesn't match the `len(Needs)==0`-applies-to-all
  code.
- **L7.** `internal/doctor/agent_beads_check.go:494-507` — `verifyLabelAdded` substring-matches
  `"1"` against full (non-CSV) command output; any "1" in headers/IDs reads as success.
- **L8.** `internal/doctor/stale_dolt_port_check.go:156-162` — the `TrimSpace(line)=="port:"`
  branch is effectively dead/accidental (works only via a no-op TrimPrefix); real configs hit the
  `  port:` branch. Subsumed by the H5 resolver replacement.
- **L9.** `internal/doctor/jsonl_bloat_check.go:106-120` — default `bufio.Scanner` 64KB token
  limit + ignored `scanner.Err()` → silently undercounts bloat on rigs with oversized JSONL lines
  (warn-only). Also `precheckout_hook_check.go:96 vs 180-184` removes the obsolete hook from
  `EffectiveHooksDir` while Run detected it at `.git/hooks` (non-converging warning); and
  `idle_timeout_check.go:85-86` raw-substring detection mis-handles commented config lines.

---

## Areas checked and cleared (no bug)

- **Refinery concurrency:** parallel gate goroutines write distinct `results[idx]` with matching
  `WaitGroup.Add`; bisection slices are defensively copied and the good subset is re-gated before
  merge (batch.go); `acquireMainPushSlot` backoff honors ctx; `verifyMergeCommitLanded` /
  `CheckMQFreeze` fail-closed; single-refinery-per-rig enforced by `ErrAlreadyRunning`. The
  already-merged `gs-4uz` path correctly fail-closes with `IsAncestor` (the contrast that makes H1
  a clear regression in the synchronous path).
- **Config:** registry presets are cloned-not-mutated; pointer-typed optional numerics correctly
  distinguish absent from zero; `extractWrappedBinary` handles the kiro wrapper cases.
- **Doctor:** zombie/orphan/stalled discriminators (`IsAgentAlive`, TOCTOU re-check before kill)
  are sound and double-guard crew sessions; `orphaned_admission_records` PID liveness is
  conservative; `BareRepoExistsCheck` corrupt-repo recovery re-verifies before nuking.
- **Hooks:** `mergeEntries` explicit-disable semantics (empty-hooks disable) are intentional and
  tested; `installer.go` atomic-write + deny-list drift handling are correct; cycle detection and
  toposort in formula are self-consistent.

All five packages' existing test suites and `go vet` pass.

## Sources

- Gas Town source tree at `internal/{config,doctor,hooks,formula,refinery}/` — read 2026-06-11
- Sibling audit report `reports/audit-2026-06/bugs-cmd.md` (format reference) — accessed 2026-06-11

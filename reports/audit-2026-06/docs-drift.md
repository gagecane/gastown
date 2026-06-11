# Documentation Drift Audit — `docs/` vs Implementation

**Bead:** gu-nid89.7 (epic gu-nid89 — Whole-Repo Gastown Audit)
**Date:** 2026-06-11
**Scope:** 68 markdown files under `docs/` + 7 top-level docs (README, CONTRIBUTING,
RELEASING, AGENTS, SECURITY, CODE_OF_CONDUCT, CHANGELOG)
**Ground truth:** `gt` binary built from this branch (`Version = "1.2.1"`,
`go 1.26.2`), source under `internal/`, `Makefile`, `scripts/`, `gates.yaml`.

## Method

Built the `gt` binary from source and cross-checked every documented command,
flag, env var, file path, and config key against the real binary
(`gt <cmd> --help`) and source (`grep`/read). Relative cross-references were
checked with a link-resolution pass. Design/research docs explicitly labelled
*proposed / planned / future / vision* were excluded from drift unless they
present something as **currently true** that the code contradicts, or contain a
concrete broken reference. Every finding below was independently re-verified
against the binary before inclusion.

## Summary

| Impact | Count | Meaning |
|--------|------:|---------|
| **HIGH** | 17 | User/dev copies it and it fails or is materially misled |
| **MEDIUM** | 17 | Confusing, partially wrong, or stale-but-recoverable |
| **LOW** | 23 | Cosmetic, stale reference, off-by-one, naming nit |
| **Broken links** | 11 | Relative links resolving to non-existent files |

Most user-facing damage is concentrated in **README troubleshooting**,
**docs/reference.md** (the command reference), and the **two OTEL design docs**
(whose "not yet on main" audit tables are now badly stale). New beads are
recommended for the HIGH-impact, user-facing fixes (see end).

---

## HIGH-impact drift

### README.md (user entry point)
- **README.md:732 — [HIGH]** Troubleshooting "Convoy stuck" → `gt convoy refresh <id>`. No `refresh` subcommand exists (`Error: unknown command "refresh" for "gt convoy"`). Real subcommands: add, check, close, create, land, launch, list, stage, status, stranded, unwatch, watch.
- **README.md:724 — [HIGH]** "Agents lose connection" → `gt hooks repair`. No `repair` subcommand (`Error: unknown command "repair" for "gt hooks"`). `gt hooks` has: base, diff, init, install, list, override, registry, scan, sync.
- **README.md:360 — [HIGH]** Manual Convoy Workflow ends with `gt convoy show`. No `show` subcommand (`Error: unknown command "show" for "gt convoy"`). Correct command is `gt convoy status`. (Reference tables at :423 and :683 repeat `gt convoy show [id]` — same drift, MEDIUM there.)

### docs/reference.md (command reference — highest user reliance)
- **reference.md:659-662 — [HIGH]** Emergency section: `gt stop --all` and `gt stop --rig <name>`. `gt stop` does not exist (`Error: unknown command "stop" for "gt"`). Real equivalents: `gt shutdown --all`, `gt rig stop <rig>`, `gt down`, `gt estop`.
- **reference.md:635 — [HIGH]** `gt handoff --shutdown`. No such flag (`Error: unknown flag: --shutdown`). Flags are `--auto, -c/--collect, --cycle, -n/--dry-run, -m/--message, --no-git-check, --reason, --stdin, -s/--subject, -w/--watch, -y/--yes`.
- **reference.md:626 — [HIGH]** `gt escalate -s MEDIUM "msg" -m "Details..."`. No `-m` shorthand (`Error: unknown shorthand flag: 'm' in -m`). Detail flag is `-r/--reason`.
- **reference.md:331 — [HIGH]** Env var `CLAUDE_RUNTIME_CONFIG_DIR` = "Custom Claude settings directory". No such variable in the codebase. The real one is `CLAUDE_CONFIG_DIR` (env.go:43,171). Setting the documented name has no effect.

### docs/dolt-health-guide.md
- **dolt-health-guide.md:31 — [HIGH]** `gt escalate -s HIGH "Dolt: ..." -m "Evidence: ..."` — same nonexistent `-m` flag; should be `-r/--reason`. (Project CLAUDE.md correctly omits `-m`.)

### docs/concepts/molecules.md
- **molecules.md:100 — [HIGH]** Polecat workflow summary: `gt done # Submit, nuke sandbox, exit`. `gt done` does **not** nuke the sandbox — it syncs the worktree to main and preserves it for reuse (IDLE), per done.go:1967 ("warm worktree preserved for reuse") and done.go:653. Contradicts the persistent-polecat model in polecat-lifecycle.md.

### docs/concepts/convoy.md
- **convoy.md:166 — [HIGH]** `gt convoy create "Feature X" gt-abc --notify mayor/ --notify --human`. `--human` is not a flag on `convoy create` (`Error: unknown flag: --human`) and `--notify` is a single-value string flag, not repeatable. Command fails outright.

### docs/WASTELAND.md
- **WASTELAND.md:152-162 — [HIGH]** Trust-level table is wrong. Doc: 0=Registered, 1=Participant, 2=Contributor, 3=Maintainer. Code (`internal/wasteland/trust.go:32-55`): 0=Drifter, 1=Registered, 2=Contributor, 3=War Chief. `gt wl join` sets `trust_level=1` (wasteland.go:172) which the code labels **"Registered"**, not "Participant". Users checking their tier see different names than documented.

### docs/agent-provider-integration.md
- **agent-provider-integration.md:706 — [HIGH]** `gt config set agent your-agent --rig <rigname>`. `gt config set` has no `--rig` flag (`Error: unknown flag: --rig`) and `agent` is not a valid key. Correct: `gt config default-agent <name>`, `gt config agent set <name> <cmd>`, or edit `<rig>/settings/config.json`. Breaks step 2 of the integration walkthrough.

### docs/runtimes/NOS_TOWN.md
- **NOS_TOWN.md:64-65 — [HIGH]** `gt config set runtime.provider groq` and `gt config set runtime.base_url ...`. `gt config set` supports no `runtime.*` keys; both commands fail. Runtime/provider is set via the rig `settings/config.json` `runtime` block, not `gt config set`.

### docs/design/model-aware-molecules.md
- **model-aware-molecules.md:80-96, 540-546 — [HIGH]** Phase 1 marked DONE `[x]`, asserts `internal/models/{database,router,usage}.go` with `SelectModel`/`LoadDatabase`/`RecordUsage` exist. The entire `internal/models/` package does not exist.
- **model-aware-molecules.md:104-111, 545-546 — [HIGH]** Claims (all `[x]`) formula steps support `model`/`provider`/`min_mmlu`/`min_swe`/`requires`/`access_type`/`max_cost` validated in `internal/formula/types.go`+`parser.go`. The `Step` struct (types.go:130) has none of these fields.

### docs/design/otel/otel-architecture.md
- **otel-architecture.md:54,80,81 — [HIGH]** Marks `agent.instantiate`, `mol.cook/wisp/squash/burn`, `bead.create` as "❌ Roadmap — no Record* function exists". All exist on this branch: `RecordAgentInstantiate` (recorder.go:442), `RecordMolCook` (720), `RecordMolSquash` (759), `RecordMolBurn` (777), `RecordBeadCreate` (794).
- **otel-architecture.md:652-695 — [HIGH]** "Absent functions and features (confirmed by grep on origin/main)" table is stale (pinned to `origin/main @ 2d8d71ee`, now an ancestor of HEAD). Claims `WithRunID`/`RunIDFromCtx`, `GT_RUN`, `telemetry.IsActive()`, `internal/agentlog/`, `agent_log.go`, `agent_logging_unix.go` "do not exist" — all present on this branch.

### docs/design/otel/otel-data-model.md
- **otel-data-model.md:8-10, 270-296, 440-471 — [HIGH]** Same stale "PR #2199 / not yet on main" framing: marks `agent.instantiate`, `mol.*`, `bead.create` as "❌ Roadmap — no Record* function exists yet". All exist on this branch.

---

## MEDIUM-impact drift

### README.md
- **README.md:740-741 — [MEDIUM]** "Mayor not responding" → `gt mayor detach` then `gt mayor attach`. No `detach` subcommand (`gt mayor`: attach, restart, start, stop, status). Use `gt mayor restart`.
- **README.md:125 — [MEDIUM]** Prerequisites: "Go 1.25+". go.mod declares `go 1.26.2`; Go 1.25 fails the module directive.

### docs/reference.md
- **reference.md:677-679 — [MEDIUM]** `gt mq list [rig]` / `next [rig]` / `retry <id>` / `reject <id>`. Real signatures: `list`/`next` require the rig arg (`accepts 1 arg(s)`); `retry <rig> <mr-id>` and `reject <rig> <mr-id-or-branch>` require **two** args.
- **reference.md:488 — [MEDIUM]** "Built-in agents": lists 8 (`claude, gemini, codex, cursor, auggie, amp, opencode, copilot`). Binary lists 13 — missing `groq-compound, kiro, omp, pi, vibe`.
- **reference.md:314 — [MEDIUM]** Lists `BEADS_DIR` as a core session env var "via config.AgentEnv()". `AgentEnv()` (env.go) never sets it — only set per-`bd`-subprocess inside the beads package.
- **reference.md:329 — [MEDIUM]** Lists `GIT_AUTHOR_EMAIL` as set by Gas Town. `AgentEnv()` sets only `GIT_AUTHOR_NAME`.

### docs/concepts/polecat-lifecycle.md
- **polecat-lifecycle.md:11-20 — [MEDIUM]** "Polecats have four operating states" (Working/Idle/Stalled/Zombie). Code (`internal/polecat/types.go:30-60`) also defines `StateReviewNeeded`, `StateStuck`, `StateDone` — real states surfaced in `gt polecat status`.

### docs/concepts/propulsion-principle.md
- **propulsion-principle.md:50-67 — [MEDIUM]** Internally contradictory: line 50 says "no `bd mol current`, no step closures", lines 52-67 build the workflow on `bd close ... --continue` + `bd mol current`. Both commands still exist; the page tells the reader to both use and not-use them.

### docs/CLEANUP.md
- **CLEANUP.md:151-152 — [MEDIUM]** "Internal" table claims `selfNukePolecat()` and `selfKillSession()` live in done.go. Neither identifier exists anywhere. Actual kill is `tmux.DetachedKillSessionWithProcesses` (done.go:1997); no sandbox self-nuke exists.

### docs/design/escalation.md
- **escalation.md:142-146 — [MEDIUM]** Documents `gt escalate ... -m "..."` body flag and a `--to` route flag. Neither exists; body flag is `-r/--reason`, and there is no `--to`.

### docs/design/dog-infrastructure.md
- **dog-infrastructure.md:460-467 — [MEDIUM]** Debugging section lists `gt dog pool status`, `gt dog dances`, `gt dog warrants` — none exist (`gt dog`: add, call, clear, dispatch, done, health-check, list, remove, status).
- **dog-infrastructure.md:482 — [MEDIUM]** "Fix: `gt session kill hq-deacon`". No `gt session kill` (`unknown command "kill" for "gt session"`). Use `gt session stop` / `gt deacon force-kill`.

### docs/design/property-layers.md
- **property-layers.md:168 — [MEDIUM]** `gt rig config show gastown --layer`. Flag is `--layers` (plural); `--layer` is rejected.

### docs/otel-data-model.md vs docs/design/otel/otel-data-model.md
- **docs/otel-data-model.md:4-6,24-36 — [MEDIUM]** The two `otel-data-model.md` files conflict on `run.id`. Top-level says it is current reality; design-dir version says "not yet on main". `run.id`/`GT_RUN` DO exist → top-level is correct, design-dir is stale. Reader gets opposite answers depending on which file they open. (Top-level uniquely carries the `kiro_wrapper` event schema.)

### docs/design/otel/kiro-wrapper-dashboard.md
- **kiro-wrapper-dashboard.md:21,152 — [MEDIUM]** Links to `../otel-data-model.md`, which from `docs/design/otel/` resolves to the non-existent `docs/design/otel-data-model.md`. Intended target is `../../otel-data-model.md` (top-level) or the sibling (no `../`). (Also see Broken Links.)

### docs/agent-provider-integration.md
- **agent-provider-integration.md:289 — [MEDIUM]** Reference hooks template cited as `internal/claude/config/settings-autonomous.json`. No `internal/claude/` directory exists; actual path is `internal/hooks/templates/claude/settings-autonomous.json` (installer.go:170).

### docs/examples/hanoi-demo.md
- **hanoi-demo.md:27 — [MEDIUM]** Pre-generated formulas "Located in `.beads/formulas/`". That dir does not exist; the formulas live in `internal/formula/formulas/`.
- **hanoi-demo.md:126 — [MEDIUM]** Generator redirects to `.beads/formulas/towers-of-hanoi-15.formula.toml` — same non-existent dir.

---

## LOW-impact drift

### README.md
- **README.md:289-291, 468 — [LOW]** References `internal/formula/formulas/release.formula.toml` as an example formula path; no such file exists in that dir.

### RELEASING.md
- **RELEASING.md:18 — [LOW]** Option A says `cd gastown/mayor/rig` before bump-version; no such path in the repo (stale town-layout dir). Option B uses the correct `./scripts/bump-version.sh`.

### SECURITY.md
- **SECURITY.md:32-34 — [LOW]** Supported Versions table lists only `0.1.x`. Current shipped version is `1.2.1`; the 1.x line has no row.

### docs/reference.md / docs/overview.md / docs/glossary.md / docs/HOOKS.md
- **overview.md:51 — [LOW]** `gt convoy create ... --notify overseer`. Well-known address is `@overseer` / `--human`; bare `overseer` is not a registered notify address.
- **glossary.md:84 — [LOW]** "Handoff — ... via `/handoff`". Actual command is `gt handoff`; `/handoff` is not a `gt` command.
- **HOOKS.md:181,186 — [LOW]** `gt hooks list`/`scan` descriptions say `settings.local.json`; binary manages `settings.json`. Stale `.local.` wording.

### docs/design/* (stale-but-not-misleading)
- **dolt-storage.md:557 — [LOW]** Layout diagram shows `daemon/dolt.log`; actual is `daemon/dolt-server.log` (dolt.go:90). architecture.md:109 has it right.
- **formula-drift-reconciliation.md:30 — [LOW]** "51 formulas"; `internal/formula/formulas/` currently has 52.
- **architecture.md:43-46 — [LOW]** Agent-bead table gives `<rig>/.beads/` while the same doc's canonical path is `<rig>/mayor/rig/.beads/`. Internal inconsistency.
- **escalation.md:13-18 — [LOW]** Severity table omits `low` (accepted by the binary; default is `medium`).
- **property-layers.md:33 — [LOW]** Town defaults cited at `~/gt/config.json`; actual is `~/gt/settings/config.json` (types.go:40).
- **persistent-polecat-pool.md:211,227,228 — [LOW]** Marks `ReconcilePool()` and `gt polecat pool init` as DEFERRED; both now exist (manager.go:2417; working subcommand). Understates shipped status.
- **persistent-polecat-pool.md:93 — [LOW]** Pool size "Configured in `rig.config.json`"; actual file is `config.json` in the rig dir.
- **polecat-lifecycle-patrol.md:654,655 — [LOW]** Lists `gt polecat pool init` / `ReconcilePool()` as "Deferred (design only)"; both implemented.
- **kiro-wrapper-dashboard.md:21 — [LOW]** `#polecatkiro_wrapperterminal` anchor + kiro_wrapper section exist only in top-level `docs/otel-data-model.md`, not the sibling; even a fixed `../` link would miss the section.

### docs/research/w-gc-004-agent-framework-survey.md
- **w-gc-004-agent-framework-survey.md:99 — [LOW]** Example cites `internal/formula/formulas/release.formula.toml` ("Standard release process"); no such file exists.

---

## Broken relative links (11)

Detected by link-resolution pass over all 74 markdown files. Targets that do not
resolve to an existing file:

| Source | Target | Missing file |
|--------|--------|--------------|
| docs/overview.md:31 | `design/watchdog-chain.md` | docs/design/watchdog-chain.md |
| docs/concepts/identity.md:131 | `reference.md#environment-variables` | docs/concepts/reference.md (should be `../reference.md`) |
| docs/design/model-aware-molecules.md:7 | `agent-provider-interface.md` | docs/design/agent-provider-interface.md |
| docs/design/property-layers.md:355 | `watchdog-chain.md` | docs/design/watchdog-chain.md |
| docs/design/scheduler.md:457 | `watchdog-chain.md` | docs/design/watchdog-chain.md |
| docs/design/convoy/convoy-lifecycle.md:381 | `../watchdog-chain.md` | docs/design/watchdog-chain.md |
| docs/design/convoy/mountain-eater.md:7 | `../../../docs/swarm-architecture.md` | docs/swarm-architecture.md |
| docs/design/convoy/mountain-eater.md:474 | `../../../docs/swarm-architecture.md` | docs/swarm-architecture.md |
| docs/design/convoy/spec.md:7 | `../daemon/convoy-manager.md` | docs/design/daemon/convoy-manager.md |
| docs/design/otel/kiro-wrapper-dashboard.md:21 | `../otel-data-model.md#...` | docs/design/otel-data-model.md |
| docs/design/otel/kiro-wrapper-dashboard.md:152 | `../otel-data-model.md` | docs/design/otel-data-model.md |

Note `watchdog-chain.md` is referenced from 4 docs but does not exist anywhere —
either the file was removed/renamed or never written. `swarm-architecture.md`
referenced twice, also missing.

---

## Coverage notes (verified accurate — no drift)

These docs were audited and found accurate against the binary/code:
CONTRIBUTING.md, CODE_OF_CONDUCT.md, AGENTS.md, docs/INSTALLING.md,
docs/concepts/identity.md (content), docs/concepts/integration-branches.md,
docs/design/mail-protocol.md, docs/design/agent-api-inventory.md,
docs/design/scheduler.md, docs/design/convoy/{convoy-lifecycle,spec}.md (content),
docs/design/tmux-keybindings.md, docs/design/plugin-dispatch-transport.md,
docs/guides/{local-rig-bootstrap,mvgt-integration}.md, docs/skills/convoy/SKILL.md,
docs/contrib-harnesses/*, docs/proxy-server.md, docs/cursor-runtime-beads-tasks.md,
docs/release/*, docs/SECURITY-DEPENDENCY-EXCEPTIONS.md,
docs/phase4-minimum-fix-acceptance.md, docs/research/macos-sandbox-exec.md.

Design/proposal docs correctly self-labelled as aspirational (excluded from
drift): factory-worker-api, federation, formula-resolution,
ledger-export-triggers, mol-mall-design, dog-execution-model,
directives-and-overlays, witness-at-team-lead, sandboxed-polecat-execution,
plugin-system, mountain-eater, crew-specialization-design, convoy/roadmap,
convoy/stage-launch/{prd,testing}, polecat-self-managed-completion.

### Missing docs for major subsystems (observed)
- No `docs/design/watchdog-chain.md` despite 4 inbound links (Boot/Deacon/Witness watchdog chain is a real, central subsystem).
- No `docs/swarm-architecture.md` despite 2 inbound links.
- `docs/reference.md` omits several shipped top-level commands from its catalog (e.g. `vitals`, `seance`, `krc`, `warrant`, `tap`) — reference is not a complete command index.

---

## Recommended follow-up beads (HIGH-impact, user-facing)

1. **fix(docs): correct invalid commands in README troubleshooting** — `gt convoy refresh`→`status`, `gt hooks repair`→(remove), `gt convoy show`→`status`, `gt mayor detach`→`restart` (README.md:724,732,360,740).
2. **fix(docs): correct docs/reference.md command/flag drift** — `gt stop`→`shutdown`/`rig stop`, `gt handoff --shutdown` (remove), `gt escalate -m`→`-r`, `CLAUDE_RUNTIME_CONFIG_DIR`→`CLAUDE_CONFIG_DIR`, mq arg signatures, built-in agent list (reference.md:626,635,659-662,331,677-679,488).
3. **fix(docs): correct `gt escalate -m` in dolt-health-guide** (dolt-health-guide.md:31) — agents copy this during real Dolt incidents.
4. **fix(docs): WASTELAND trust-level table wrong** (WASTELAND.md:152-162) — names don't match `trust.go`.
5. **fix(docs): broken agent-provider/NOS_TOWN config commands** — `gt config set agent --rig` and `gt config set runtime.*` don't exist (agent-provider-integration.md:706, NOS_TOWN.md:64-65).
6. **fix(docs): refresh stale OTEL design docs** — both otel-data-model.md files + otel-architecture.md mark now-shipped telemetry (`Record*`, `run.id`, `agentlog`) as "not on main"; reconcile the two otel-data-model.md copies.
7. **fix(docs): molecules.md `gt done` no longer nukes sandbox** (molecules.md:100).
8. **fix(docs): repair 11 broken relative links** + decide fate of missing `watchdog-chain.md` / `swarm-architecture.md`.

## Sources

- Gas Town `gt` binary built from this branch (`internal/cmd/`, `internal/`), verified via `gt <cmd> --help` — accessed 2026-06-11
- Repository source: `Makefile`, `scripts/`, `go.mod`, `gates.yaml`, `internal/` — accessed 2026-06-11

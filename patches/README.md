# Local patches against upstream gastown

Re-apply these after any `git pull` / clean re-checkout of
https://github.com/gastownhall/gastown.

Apply all:
```
cd /workplace/canewiw/gastown-upstream
for p in patches/*.patch; do git apply "$p"; done
```

## 0001-fix-dashboard-tmux-socket.patch
**Bug:** `internal/web/fetcher.go runCmd()` shells out to raw `tmux`
without `-L <socket>`, so `gt dashboard` hits the default tmux server
and shows 0 polecats / 0 sessions / 0 workers when the town uses a
custom socket (e.g. `gt-a0b688`).

**Fix:** when `name == "tmux"`, route through
`tmux.BuildCommandContext(ctx, args...)` which applies the
town socket already set by `PersistentPreRun -> InitRegistry
-> tmux.SetDefaultSocket`.

Discovered and applied 2026-04-27.

## 0002-fix-rig-add-prefix-mismatch.patch
**Bug:** `internal/rig/manager.go InitBeads()` runs `bd config set
issue_prefix` which bd rejects. Rig DB gets no prefix, bd falls back
to HQ prefix (`hq-`), causing prefix mismatch on bead creation.

**Fix:** Replace `bd config set issue_prefix` with
`bd init --server --prefix <prefix> --force`.

Discovered and applied 2026-04-27.

## 0003-fix-done-state-update-escape-path.patch
**Bug:** `internal/cmd/done.go:1592` returns early when molecule close
fails, skipping the polecat state update. Polecat stays "stalled" with
HOOKED bead even after `gt done` prints success.

**Fix:** Replace `return` with `goto doneStateUpdate` so agent state
is updated even when molecule close fails.

Discovered and applied 2026-04-27.

## 0004-fix-mainline-branch-whitelist.patch
**Bug:** `internal/cmd/root.go:240` hardcodes branch whitelist
`["main","master","gt_managed"]` and warns on every command when the
town branch is `mainline` (Amazon convention).

**Fix:** Add `"mainline"` to the whitelist. Warning is cosmetic only.

Discovered and applied 2026-04-27.

## 0005-add-kiro-agent-preset.patch
**Feature:** Adds `AgentKiro` preset to `internal/config/agents.go`
with kiro-cli command, `--trust-all-tools --agent gastown` args, and
`.kiro/agents` hooks directory. Required for `gt config agent set kiro`.

Note: companion hook templates in `internal/hooks/templates/kiro/` are
untracked and not captured in this patch — copy them separately.

Applied 2026-04-27.

## 0006-fix-gt-root-symlink-resolution.patch
**Bug:** `internal/config/env.go:148-161` assigns `env["GT_ROOT"]`
directly from `cfg.TownRoot` without resolving symlinks. On hosts where
`~/gt` → `/local/home/.../gt`, this causes `gt doctor tmux-global-env`
warnings and path mismatches in refineries.

**Fix:** Run `filepath.EvalSymlinks` before assigning `GT_ROOT` and
`GIT_CEILING_DIRECTORIES`.

Discovered and applied 2026-04-27.

## 0009-fix-beads-circuit-breaker-unbounded-growth.patch
**Target:** steveyegge/beads `internal/storage/dolt/circuit.go` (NOT gastown)
**Status:** Prepared for upstream PR, blocked by GitHub token permissions.

**Bug:** Two issues in the beads circuit breaker:
1. Per-database keying (GH#3140) creates a file per DB name. Ephemeral/test DBs
   (testdb_*, testcontainers) mint permanent closed files never removed.
   Observed: 35,653 files / 140MB after 8 days.
2. `CleanStaleCircuitBreakerFiles()` globs+reads+parses the whole directory on
   every `bd init`. With 35k files this adds ~650ms per call.

**Fix:**
- Expire CLOSED breaker files by mtime (>1h retention TTL)
- Rate-limit the cleanup scan via a sentinel file (at most once per 5m)
- Hot-path cost drops from O(n) to O(1) for nearly all inits

**Stopgap:** The daemon-side `circuit_breaker_gc` dog in this repo already
reaps stale closed files every 5m with 15m retention, keeping the directory
small (~20-30 files). This upstream fix would eliminate the need for that dog.

**To submit upstream:** Fork steveyegge/beads, apply this patch, submit PR.
The gagecane PAT lacks `public_repo` fork scope — needs a broader token or
manual fork creation.

Prepared 2026-06-05.

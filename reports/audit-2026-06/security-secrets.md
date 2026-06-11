# Security Audit — Secrets & Credential Handling

- **Scope:** Gas Town repository (`steveyegge/gastown`) — secret storage, credential
  handling, env var exposure, container/config secret leakage, `.gitignore` adequacy.
- **Date:** 2026-06-11
- **Auditor:** polecat/scavenger (gu-nid89.9)
- **Method:** Static source review (`internal/`, `cmd/`, `scripts/`, `plugins/`,
  `templates/`), config/Docker/gitignore inspection, git-history file scan, and
  high-entropy pattern scan.

## Executive Summary

Gas Town's credential handling is **broadly sound**. No hardcoded secrets, API keys,
or private keys were found in production source — only documentation placeholders.
The proxy server (the only network-exposed component) uses mutual TLS
(`tls.RequireAndVerifyClientCert`), a strict env allowlist (`minimalEnv` → `HOME`,
`PATH` only), and is careful **not** to log argv ("may contain tokens or secrets").
Agent env propagation uses an explicit allowlist rather than a blanket passthrough.
There is no `InsecureSkipVerify` anywhere in the codebase. CA/private-key files are
written `0600`.

The findings below are mostly **defense-in-depth hardening** issues rather than
exploitable vulnerabilities. The highest-impact items are: (1) a live `GROQ_API_KEY`
written into a world-readable (`0644`) town settings file, and (2) `.env` not being
git-ignored despite Docker/Groq workflows instructing users to create one.

| # | Severity | Finding |
|---|----------|---------|
| 1 | **MEDIUM** | Live `GROQ_API_KEY` persisted into `settings/config.json` at `0644` (plaintext, world-readable) |
| 2 | **MEDIUM** | `.env` is **not** in `.gitignore` despite docker-compose/Groq docs telling users to create one |
| 3 | LOW | Town/rig settings files written `0644` while they can carry provider API keys (`agents.*.env`) |
| 4 | LOW | `gt handoff --dry-run` prints the full restart command, which inlines exported secrets (e.g. `ANTHROPIC_API_KEY`) to stdout |
| 5 | LOW | `SECURITY.md` "email the maintainers directly" has no contact address — no actionable disclosure path |
| 6 | INFO | `docker-entrypoint.sh` sets `credential.helper store` (git credentials in plaintext `~/.git-credentials`) — expected for a sandbox but worth documenting |
| 7 | INFO | Several diagnostic/exec paths set `DOLT_CLI_PASSWORD` via `cmd.Env` — correct (env, not argv); noted for completeness |

No CRITICAL or HIGH findings. No beads filed for critical vulns (none found).

---

## Findings

### 1. [MEDIUM] Live `GROQ_API_KEY` written into world-readable settings file

**Location:** `internal/config/cost_tier.go:261-273` (`groqCompoundPreset`),
`internal/config/cost_tier.go:308-311` (`ApplyCostTier`),
`internal/config/loader.go:1119-1140` (`SaveTownSettings`, writes `0644`).

When a user selects a Groq-backed cost tier (`TierCustomGroqOpus` or
`TierCustomGroqSonnet` — `internal/cmd/config.go:163-167`), `groqCompoundPreset()`
**resolves the live `$GROQ_API_KEY` value at preset-creation time** and stores it in
the agent's `Env` map:

```go
// cost_tier.go
if v, ok := rc.Env["ANTHROPIC_API_KEY"]; ok && v == "$GROQ_API_KEY" {
    rc.Env["ANTHROPIC_API_KEY"] = os.Getenv("GROQ_API_KEY") // live key value
}
```

`ApplyCostTier` then copies that preset into `settings.Agents`, and `SaveTownSettings`
serializes the whole struct (including `Env map[string]string \`json:"env,omitempty"\``,
`types.go:1317`) to `settings/config.json` with mode **`0644`**:

```go
// loader.go:1136
if err := os.WriteFile(path, data, 0644); err != nil { //nolint:gosec // G306: settings files don't contain secrets
```

The `//nolint` comment asserting "settings files don't contain secrets" is **false in
this code path** — the file now contains a real Groq API key in plaintext, readable by
any local user (`0644`).

**Impact:** Local secret disclosure. Any user/process on a shared host can read the
Groq API key from `settings/config.json`. The comment also masks the issue from the
gosec linter (G306), so it won't be flagged automatically.

**Mitigating factors:** `settings/` is git-ignored (`.gitignore:73`), so the key is
not committed to the repo. Exposure is limited to local-filesystem read access.

**Recommendation (in priority order):**
1. Do **not** resolve `$GROQ_API_KEY` into the persisted settings — keep the
   `$GROQ_API_KEY` sentinel and expand it at spawn time (the env-allowlist path in
   `internal/config/env.go` already forwards provider keys from the live environment).
2. If a resolved value must be persisted, write settings files containing
   `agents.*.env` secrets with mode `0600` and remove/qualify the misleading
   `//nolint:gosec` comment.

---

### 2. [MEDIUM] `.env` not git-ignored despite Docker/Groq workflows requiring it

**Location:** `.gitignore` (no `.env` entry), `docker-compose.yml:28-32`, Groq tier
docs (`internal/config/cost_tier.go:260`, `internal/config/agents.go:606-614`).

`docker-compose.yml` instructs:

```yaml
# FOLDER must be defined in .env file, e.g. FOLDER=/home/user
```

and the Groq tier requires `export GROQ_API_KEY=gsk_...`, which users commonly persist
in a project-local `.env`. However, `.gitignore` has **no `.env` entry**:

```
$ git check-ignore .env ; echo rc=$?
rc=1            # NOT ignored
```

`.gitignore` ignores `config.toml`, `state.json`, `.beads-credential-key`, etc., but
not `.env`. A user who creates `.env` with `GROQ_API_KEY`/`DOLTHUB_TOKEN`/`GITHUB_TOKEN`
can accidentally `git add .` and commit it.

**Impact:** High-likelihood path to committing real secrets to a public upstream repo.

**Recommendation:** Add to `.gitignore`:
```
.env
.env.*
!.env.example
```
(`gt-model-eval/.env.example` is already tracked and should stay tracked — the negation
preserves it.)

---

### 3. [LOW] Settings files written `0644` can carry provider API keys

**Location:** `internal/config/loader.go:484` (`SaveRigSettings`),
`internal/config/loader.go:1136` (`SaveTownSettings`).

Both writers use `0644` with a `//nolint:gosec // G306: settings files don't contain
secrets` annotation. Beyond Finding #1, the general `agents.<name>.env` block is a
documented place for `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_CUSTOM_HEADERS`, and other
provider credentials (`internal/cmd/handoff.go:952-957`). Any such configured value is
serialized to a world-readable file.

**Recommendation:** Default these writes to `0600`. Settings files are per-user
operational state, not shared artifacts — `0600` costs nothing and closes the
local-disclosure gap for all current and future secret-bearing fields.

---

### 4. [LOW] `gt handoff --dry-run` prints exported secrets to stdout

**Location:** `internal/cmd/handoff.go:283`, building on
`internal/config/loader.go:2518-2537` (`PrependEnv`).

The restart command is built by `PrependEnv`, which emits
`export ANTHROPIC_API_KEY=<value> ... && exec <cmd>`. In dry-run mode:

```go
fmt.Printf("Would execute: tmux respawn-pane -k -t %s %s\n", pane, restartCmd)
```

This prints the fully-expanded export line — including `ANTHROPIC_API_KEY` and any AWS
credentials in `claudeEnvVars` (`handoff.go:742-762`) — to stdout, where it can land in
terminal scrollback, CI logs, or captured command output.

**Impact:** Low — requires the user to run `--dry-run` and the output to be captured;
the secret is the user's own. Still a needless exposure surface.

**Recommendation:** Redact known secret-bearing env keys in the dry-run print (e.g.
replace values of `*_API_KEY`, `*_TOKEN`, `AWS_SECRET_*`, `*_OAUTH_TOKEN` with
`<redacted>`), or print only the command sans env prefix in dry-run.

**Note (not a finding):** The non-dry-run path is fine — `export VAR=val` is a shell
builtin, so the value does **not** appear in `ps`/`/proc/*/cmdline` of the child.

---

### 5. [LOW] `SECURITY.md` has no disclosure contact

**Location:** `SECURITY.md` ("Email the maintainers directly with details").

There is no email address, alias, or link. A reporter following the policy has no
actionable channel, increasing the chance of public disclosure of a real vuln.

**Recommendation:** Add a concrete security contact (alias/email or GitHub private
advisory link).

---

### 6. [INFO] Container uses plaintext git credential store

**Location:** `docker-entrypoint.sh:9` — `git config --global credential.helper store`.

`credential.helper store` writes credentials in plaintext to `~/.git-credentials`.
This is a deliberate choice for an unattended sandbox container (no interactive
credential prompt possible), and `SECURITY.md` already states workers can push to
configured remotes. Acceptable for the sandbox threat model, but worth documenting
explicitly so operators don't mount a host home directory with real long-lived
credentials into the container.

**Recommendation (optional):** Note in `SECURITY.md`/docs that the container stores git
credentials in plaintext and that short-lived/scoped tokens should be used.

---

### 7. [INFO] `DOLT_CLI_PASSWORD` passed via `cmd.Env` (correct)

**Location:** `internal/daemon/dolt.go:285-293`, `internal/doltserver/doltserver.go:587,4280`,
`internal/cmd/dolt.go:1000,1033`, `internal/cmd/vitals.go:168`.

The Dolt CLI password is consistently passed through `cmd.Env` (environment), never as
a command-line argument, so it does not appear in process listings. The local Dolt
server runs as `root` with no password for localhost access by default
(`DefaultUser = "root"`, `doltserver.go:141`); `GT_DOLT_PASSWORD` is an opt-in
override. No issue — recorded to show this path was reviewed.

---

## Positive Observations (what's done well)

- **No hardcoded secrets.** High-entropy pattern scan (`gsk_`, `sk-`, `ghp_`, `AKIA`,
  `xox[bapr]`, PEM private-key headers, `AIza`) over non-test source returned only a
  documentation placeholder in `docs/proxy-server.md`. Git-history filename scan for
  `*.pem`/`*.key`/`*credential*`/`.env` found nothing committed.
- **Proxy server is well-hardened:** mutual TLS (`tls.RequireAndVerifyClientCert`,
  `internal/proxy/server.go:210-211`), admin endpoint bound to `127.0.0.1`, request
  body capped at 1 MiB, identity derived from client-cert CN, and `runCommand` runs
  with `minimalEnv()` (only `HOME`/`PATH`) to prevent server credentials leaking into
  subprocesses (`internal/proxy/server.go:532-538`, `internal/proxy/exec.go:213-229`).
- **Audit logging is secret-aware:** `exec.go:112` explicitly does **not** log full
  argv "it may contain tokens or secrets."
- **Env propagation is allowlist-based:** `internal/config/env.go:423+` forwards only
  an explicit set of provider credentials and deliberately excludes `ANTHROPIC_BASE_URL`
  to prevent cross-provider contamination.
- **CA/key file perms:** private keys written `0600` (`internal/proxy/ca.go:77`),
  certs `0644`; covered by a regression test (`ca_test.go:52`).
- **No `InsecureSkipVerify`** anywhere in production source.
- **Telemetry is privacy-conscious:** prompt-key content logging is opt-in via
  `GT_LOG_PROMPT_KEYS=true`, "default off because prompts may contain secrets or PII"
  (`internal/telemetry/recorder.go:389-405`).
- **macOS keychain integration** (`internal/quota/keychain.go`) uses the `security` CLI
  and writes `.claude.json` at `0600`; OAuth tokens are read/written via the OS keychain
  rather than flat files.
- **`.gitignore` is otherwise thorough:** ignores `config.toml`, `state.json`,
  `.beads-credential-key`, `settings/`, `.dolt/`, `*.db`, agent worktrees, etc.

---

## Methodology / Commands

- Secret pattern scan: `rg` for `gsk_`/`sk-`/`ghp_`/`AKIA`/`xox[baprs]-`/PEM headers/`AIza`
  over non-test source.
- Credential-flow review: `internal/doltserver/`, `internal/quota/keychain.go`,
  `internal/config/{env,cost_tier,loader}.go`, `internal/cmd/handoff.go`,
  `internal/proxy/{server,exec,ca,git}.go`, `internal/doctor/groq_compound_check.go`.
- Container/config review: `Dockerfile`, `docker-compose.yml`, `docker-entrypoint.sh`,
  `.dockerignore`, `.gitignore`, `SECURITY.md`.
- `.gitignore` adequacy: `git check-ignore` for `.env`, `settings.json`, etc.
- Git-history file scan: `git log --all --diff-filter=A --name-only` filtered for
  secret-like filenames (clean).

## Sources

- Gas Town repository source — `internal/`, `cmd/`, `scripts/`, `plugins/`,
  `Dockerfile`, `docker-compose.yml`, `.gitignore`, `SECURITY.md` — accessed 2026-06-11

# Security Audit: Injection & Input Validation

**Bead:** gu-nid89.10 (parent epic gu-nid89 — Whole-Repo Gastown Audit)
**Repo:** `github.com/steveyegge/gastown` (fork)
**Commit audited:** `a9afe5854832b898346c76769e3735adeb4e44f7`
**Date:** 2026-06-11
**Auditor:** polecat guzzle (gastown_upstream)

---

## Threat model (read this first — it drives every severity rating)

`SECURITY.md` states Gas Town's design assumptions explicitly:

> - Agent isolation: Workers run in separate tmux sessions but **share filesystem access**
> - Shell execution: **Agents execute shell commands as the running user**
> - Run in isolated environments for untrusted code

So **agents and the operator are inside the trust boundary by design.** An agent that
can already run `sh` as the user gains nothing by "injecting" into a gate command — it
can run the command directly. Findings that require agent/operator write access to rig
config, beads, or the local filesystem are therefore rated **LOW/INFO** (defense-in-depth),
*not* critical, even where the raw code pattern looks alarming.

The **genuine** elevated-risk surfaces are inputs that cross a boundary from *outside*
that trust zone:

1. **The web dashboard** (`internal/web/`) — can bind to `0.0.0.0` (setup.go:258, 925), so
   request input may originate off-host on a shared/internal network.
2. **Upstream / PR-derived data** — branch names, refs, PR URLs pulled from remote
   repositories (`internal/upstreamsync/`, `internal/github/`, `internal/bitbucket/`).
3. **Filesystem-derived names** that an *unprivileged* process or a malicious archive
   could plant (e.g. a directory name in `.dolt-data/` that later flows into SQL).
4. **Third-party dependencies** (supply chain).

Severity ratings below are calibrated to this model.

---

## Summary of findings

| # | Title | Area | Severity | Status |
|---|-------|------|----------|--------|
| 1 | `git` flag-injection: branch/ref names lack a `--` guard or leading-`-` reject | Git | **MEDIUM** | Open |
| 2 | SQL injection in `RemoveDatabase` via filesystem-derived DB name | SQL | **MEDIUM** | Open |
| 3 | Dashboard PR-view SSRF: only `https://` enforced, no host allowlist | Web | **MEDIUM** | Open |
| 4 | Formula name path traversal (read-only) | Path | **LOW** | Open |
| 5 | Gate/test commands run via `sh -c` from rig config / bead formula_vars | Cmd | **LOW** (info) | By design |
| 6 | `RemoveDatabase` DROP/SELECT identifier backtick-injection | SQL | **LOW** | Open |
| 7 | No `io.LimitReader` on external HTTP/JSON bodies | Parsing | **LOW** | Open |
| 8 | govulncheck could not run (network sinkhole) — deps checked manually | Deps | **INFO** | N/A |

No **CRITICAL** issues found. The codebase shows generally strong hygiene: `exec.Command`
uses argv arrays (no shell), tmux send-keys sanitizes control chars, the formula template
engine registers **no** FuncMap (no code-exec via templates), YAML uses v3 with
`KnownFields(true)`, and many name inputs (rig, polecat, dog) are regex-validated.

---

## Findings

### 1. Git flag-injection — branch/ref args lack `--` separator or leading-`-` rejection — MEDIUM

**Files:** `internal/git/git.go`
- `Push(remote, branch, force)` — line 1085
- `Checkout(ref)` / `CheckoutNewBranch` / `CheckoutResetBranch` — lines 989, 996, 1005
- `Merge(branch)` / `MergeNoFF` / `MergeSquash` — lines 1648, 1654, 1671
- `Fetch*` family — lines 1011–1032

**Code:**
```go
func (g *Git) Push(remote, branch string, force bool) error {
    args := []string{"push", remote, branch}     // branch placed positionally, no "--"
    if force { args = append(args, "--force") }
    _, err := g.runWithTimeout(pushTimeout, args...)
    return err
}
func (g *Git) Checkout(ref string) error { _, err := g.run("checkout", ref); return err }
func (g *Git) Merge(branch string) error { _, err := g.run("merge", branch); return err }
```

The `run`/`runRaw` wrapper (git.go:153–190) correctly uses `exec.Command("git", args...)`
— so there is **no shell** and **no classic shell injection**. The residual risk is
**git argument/flag injection**: git parses any positional arg beginning with `-` as an
option. A ref named `--upload-pack=<cmd>`, `--output=<path>`, `-o`, etc., changes the
command's behavior. There is no centralized `validateRef()` and no `--` end-of-options
separator before the ref argument in these wrappers.

**Data path / exploitability.** Most branch names are internally generated
(`polecat/<name>/<bead>--mqaNNNN`) and safe. The elevated concern is
`internal/upstreamsync/` and the GitHub/Bitbucket integrations, where **branch/ref names
originate from a remote repository** the operator does not fully control. A hostile
upstream branch named `--upload-pack=...` flowing into a `Checkout`/`Fetch`/`Merge`
wrapper could alter git behavior. I did **not** find a confirmed end-to-end path where an
attacker-chosen upstream ref reaches these wrappers unvalidated — but I also found **no
guard that would stop one**, and `git.go` does not validate at the choke point. This is a
real defense-in-depth gap at a boundary that handles external data.

Note: there *is* partial validation elsewhere — `branchArgsMutate` (git.go:461) guards
`guardUnsafeTownRootMutation`, and `internal/web/validate.go` validates IDs at the API
layer — but neither enforces leading-`-` rejection on refs at the git wrapper itself.

**Recommendation.** Add a single `validateGitRef(s string) error` (reject empty, leading
`-`, control chars, newlines) and call it in every wrapper that accepts a branch/ref/remote
from a non-constant source; **and/or** insert `"--"` before positional ref args where the
git subcommand supports it (`checkout`, `merge`, `fetch <remote> -- <ref>` is not
universally supported, so prefer validation). Add a unit test feeding `"--upload-pack=x"`.

---

### 2. SQL injection in `RemoveDatabase` via filesystem-derived DB name — MEDIUM

**File:** `internal/doltserver/doltserver.go:3580` (also 3572)

```go
// dbName flows in from os.ReadDir(.dolt-data) entry names (ListDatabases / FindOrphanedDatabases)
_ = serverExecSQL(townRoot,
    fmt.Sprintf("DELETE FROM dolt_branch_control WHERE `database` = '%s'", dbName))
```

`dbName` is interpolated into a **string literal** with no escaping. The package already
has `EscapeSQL()` (wl_commons.go:151) and `validSQLName()` (sync.go:139), but neither is
applied here.

**Data path.** `dbName` comes from `os.ReadDir(config.DataDir)` entry names
(doltserver.go:2459/776) → `ListDatabases` / `FindOrphanedDatabases` →
`RemoveDatabase(townRoot, o.Name, force)` (callers: cmd/dolt.go:1224,
doctor/migration_check.go:557, rig/manager.go:1202). So exploitation requires an attacker
to create a **directory** under `.dolt-data/` whose name contains a single quote (e.g.
`x'; DROP DATABASE foo;--`). Single quotes are legal in Unix filenames.

**Why MEDIUM not CRITICAL.** Under the threat model, anyone who can create directories in
`.dolt-data/` already has filesystem write as the user (and `CLAUDE.md` forbids touching
that dir). But the cleanup path runs **with the Dolt server's privileges** and processes
*orphan/test* DB names that accumulate from many sources — a name planted by a buggy test
harness or a partially-trusted tool would execute as SQL on the shared data plane. It also
violates the package's own established `EscapeSQL`/`validSQLName` discipline.

**Recommendation.** Validate `dbName` with `validSQLName()` at `RemoveDatabase` entry
(reject anything outside `^[A-Za-z0-9_-]+$`) and/or `EscapeSQL()` the literal. Validation
is preferable since identifiers can't be parameterized.

---

### 3. Dashboard PR-view SSRF — only `https://` enforced, no host allowlist — MEDIUM

**File:** `internal/web/api.go` — `handlePRShow` (~lines 1608–1667)

```go
prURL := r.URL.Query().Get("url")
// validates: length<=2000, no null/newline, must start with "https://"
args = []string{"pr", "view", prURL, "--json", ...}   // passed to `gh pr view`
```

The only URL check is the `https://` prefix — there is **no host allowlist**. A request
`?url=https://169.254.169.254/...` or `https://<internal-host>/...` is forwarded to the
`gh` CLI. The `gh` tool largely constrains this to GitHub-shaped APIs, which mitigates pure
SSRF, but the app performs no validation of its own.

**Why this matters here:** the dashboard **can bind `0.0.0.0`** (setup.go:258 default path
and the client JS at setup.go:925 sends `bind: '0.0.0.0'` for non-localhost hostnames), so
on a shared host/VPC the endpoint may be reachable by other parties — moving this from
"localhost-only, ignore" to a real boundary.

Also in this file: `handleSessionPreview`/`handleSessionSend` validate session names with a
strict `[A-Za-z0-9_-]` whitelist + known-prefix check before passing to `tmux` (well
mitigated); `handleMailRead` validates IDs via `isValidID` (`^[A-Za-z0-9][A-Za-z0-9._-]*$`,
no leading `-`). Template rendering uses `html/template` (auto-escaped) — reflected-XSS via
`?expand=` is mitigated.

**Recommendation.** Add an explicit host allowlist (`github.com` + configured GH-Enterprise
host) before invoking `gh pr view`. Document the dashboard's intended bind model and warn
when binding non-loopback.

---

### 4. Formula name path traversal (read-only) — LOW

**Files:** `internal/formula/embed.go:62,71`; `internal/formula/overlay.go:44`;
`internal/formula/parser.go:646`

```go
filename := name; if !hasFormulaSuffix(filename) { filename += ".formula.toml" }
path := filepath.Join(townRoot, rigName, ".beads", "formulas", filename)
content, _ := os.ReadFile(path)
```

`name` is not checked for `..`. `gt sling ../../../../etc/passwd` would resolve outside the
formulas dir. Impact is bounded to **arbitrary file read** of files the user already can
read (CLI runs as the user), and the suffix `.formula.toml` is appended unless the name
already ends with it — so reading non-`.formula.toml` files requires the name to itself end
in `.formula.toml`. The `parser.go:646` `extends`/`compose` path resolves names from inside
formula files (operator-authored = trusted).

**Why LOW.** No write/exec, CLI-arg sourced (same trust level as the shell the user is in),
suffix constraint limits target files.

**Recommendation.** Add a shared `validateFormulaName()` rejecting `..`, `/`, leading `-`;
or after `filepath.Join`, assert `filepath.Clean(path)` stays under the base dir. Reuse the
pattern already present in `polecat.ValidatePoolName` / `dog.validateDogName`.

---

### 5. Gate/test commands executed via `sh -c` from rig config & bead `formula_vars` — LOW (by design)

**Files:** `internal/refinery/engineer.go:1181,1239`; `internal/cmd/mq_integration.go:842`;
`internal/daemon/main_branch_test_runner.go:890`; `internal/polecat/completion/pre_verify.go:135`;
`internal/polecat/manager.go:3387`; `internal/witness/handlers.go:1985`;
`internal/upstreamsync/gate_runner.go:333`

```go
cmd := exec.Command("sh", "-c", testCmd) //nolint:gosec // G204: TestCommand is from trusted rig config
```

These run operator-authored gate/test command strings, and (handlers.go:1985) gate commands
extracted from a bead's `formula_vars` via `beads.ParseAttachmentFields`. The `nolint`
comments assert "trusted rig config." **Under the stated threat model this is correct and
working as designed** — the operator/agent that writes rig config or beads already has
shell-as-user. There is no privilege boundary crossed.

The only way this becomes a real finding is if a future change lets a *less-privileged*
actor (e.g. an external PR, or a remote API request) write rig config or bead descriptions.
That is not the case today. Logged as INFO/defense-in-depth so it isn't silently relied on.

**Recommendation (optional).** Keep the trust assumption documented next to each call site
(already done). If beads ever become writable across a trust boundary, sanitize
`formula_vars` before shell use.

---

### 6. `RemoveDatabase` DROP/SELECT identifier backtick-injection — LOW

**File:** `internal/doltserver/doltserver.go:3572`, `:4633`

```go
serverExecSQL(townRoot, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
fmt.Sprintf("SELECT MAX(date) FROM `%s`.dolt_log LIMIT 1", dbName)
```

Same filesystem-derived `dbName` as Finding #2, here interpolated into a **backtick-quoted
identifier**. A backtick in the directory name breaks out of the identifier quoting (MySQL
escapes embedded backticks by doubling; a lone backtick terminates the identifier → at
worst a syntax error, at best altered identifier targeting). Lower impact than the
string-literal `DELETE` (#2) because breaking out of an identifier into executable SQL is
harder, but it's the same root cause and the same fix.

**Recommendation.** Same as #2 — `validSQLName(dbName)` at entry covers both findings.

---

### 7. No size limit on external HTTP/JSON response bodies — LOW

Multiple `io.ReadAll` / `json.NewDecoder` calls over GitHub/Bitbucket/Dolt HTTP responses
lack `io.LimitReader`. Sources are trusted APIs, so this is a memory-DoS defense-in-depth
item, not an active vuln.

**Recommendation.** Wrap external bodies in `io.LimitReader(r, maxBytes)` (e.g. 10–50 MB)
before decoding. Optionally add `dec.DisallowUnknownFields()` on agent-protocol decoders to
catch malformed input early.

---

### 8. Dependency / supply-chain check — INFO

`govulncheck` **could not run** in this environment: the corporate DNS sinkhole blocks
`proxy.golang.org` (TLS cert is `chalupa-dns-sinkhole.corp.amazon.com`), so the vuln DB
fetch fails. This audit could not produce an authoritative CVE list.

Manual review of `go.mod` security-relevant pins (all current as of audit date, no known
*open* advisories at these versions):

- Go `1.26.2`
- `golang.org/x/net v0.55.0`, `golang.org/x/crypto v0.52.0`, `golang.org/x/sys v0.45.0`,
  `golang.org/x/text v0.37.0` — recent; the older HTTP/2 and crypto CVEs are fixed well
  below these versions.
- `gopkg.in/yaml.v3 v3.0.1`, `gopkg.in/yaml.v2 v2.4.0` (indirect), `BurntSushi/toml v1.6.0`.
- Large transitive `github.com/dolthub/*` tree (vitess, go-mysql-server, dolt/go) — these
  are the embedded MySQL-compatible engine; pinned to recent 2026-05 snapshots.

**Recommendation.** Run `govulncheck ./...` from a host with module-proxy access (or set
`GOFLAGS=-mod=mod GOPROXY=off` against a local vuln DB) in CI. Add it as a scheduled gate.
This is the one audit item I could not complete here and it should be re-run.

---

## Areas reviewed and found clean (no action)

- **`exec.Command` argv usage** — no `sh -c` with externally-derived strings outside the
  config/gate cases in #5; the proxy exec endpoint (`internal/proxy/exec.go`) uses a strict
  command allowlist + argv array.
- **tmux send-keys** (`internal/tmux/sendkeys.go`) — `sanitizeNudgeMessage` strips control
  chars (ESC, CR, BS, TAB, DEL, `<0x20`); literal `-l` mode prevents keystroke injection.
- **Formula template engine** (`internal/cmd/formula.go:renderTemplate`) — Go `text/template`
  with **no FuncMap**; values are data-substituted only, no code/env/file access. Not
  exploitable for RCE even with attacker-controlled `--set` vars.
- **YAML/TOML/JSON parsing** — yaml.v3 with `KnownFields(true)`; BurntSushi/toml and
  encoding/json into typed structs; no `gob`, no XXE, no zip-slip (no archive extraction of
  untrusted input found).
- **Name validation** — `polecat.ValidatePoolName`, `dog.validateDogName`,
  `web.isValidRigName`, `beads.IsFlagLikeTitle` reject `..`/`/`/leading-`-` for their inputs.

---

## Recommended follow-up beads (for critical/notable items)

Per acceptance criteria, beads were filed for the actionable findings:

- **gu-n5dvk** (MEDIUM) — git: validate refs / add `--` guard in `internal/git` wrappers (Finding #1).
- **gu-zl25s** (MEDIUM) — doltserver: validate `dbName` with `validSQLName` in `RemoveDatabase`
  (Findings #2 + #6, single fix).
- **gu-4zl6k** (MEDIUM) — web: host-allowlist `handlePRShow` URL before `gh pr view` (Finding #3).
- **gu-hpnjo** (LOW) — formula: add `validateFormulaName` (reject `..`) (Finding #4).

Finding #8 (wire `govulncheck` into CI from a proxy-capable host) is left as a recommendation
in the deps section rather than a bead, since it is environmental tooling, not a code fix.

No CRITICAL-severity findings; no CRITICAL beads required.

---

## Sources

- [SECURITY.md](../../SECURITY.md) — accessed 2026-06-11 (threat model / trust boundary)
- Codebase at commit `a9afe585`: `internal/git/git.go`, `internal/doltserver/doltserver.go`,
  `internal/web/api.go`, `internal/web/setup.go`, `internal/formula/{embed,overlay,parser}.go`,
  `internal/refinery/engineer.go`, `internal/witness/handlers.go`, `internal/tmux/sendkeys.go`,
  `internal/cmd/formula.go`, `go.mod` — accessed 2026-06-11

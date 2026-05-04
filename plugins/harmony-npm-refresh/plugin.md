+++
name = "harmony-npm-refresh"
description = "Refresh Harmony CodeArtifact npm token (22h lifetime)"
version = 1

[gate]
type = "cooldown"
duration = "18h"

[tracking]
labels = ["plugin:harmony-npm-refresh", "category:maintenance"]
digest = true

[execution]
timeout = "2m"
notify_on_failure = true
severity = "medium"
+++

# Harmony NPM Refresh

Harmony CodeArtifact (Amazon's internal npm registry) issues authorization
tokens that expire after **22 hours**. Once expired, `npm install` against
CodeArtifact returns `E401 Unauthorized` and blocks every Node-based rig's
`main_branch_test` (casc_e2e, casc_lambda, casc_cdk, codegen_ws, etc.).

This plugin runs `harmony npm` on an 18-hour cadence — 4 hours of safety
buffer ahead of the 22-hour token TTL — so the token is always fresh when
dogs pull dependencies.

## What it does

1. Verify `harmony` CLI is on PATH (escalate if missing).
2. Verify Midway auth is valid (`mwinit -l` — `harmony npm` needs a live
   Kerberos/Midway session). If Midway is expired, escalate **HIGH** since
   refreshing Midway requires human YubiKey/FIDO2 interaction.
3. Run `harmony npm` to mint a fresh CodeArtifact token (writes to
   `~/.npmrc`).
4. Verify the token works by running `npm ping` — expects a response from
   `*.codeartifact.*.amazonaws.com`.
5. Record a plugin-run receipt bead.

## Failure modes

| Symptom                                | Action                                    |
|----------------------------------------|-------------------------------------------|
| `harmony` not on PATH                  | Escalate HIGH (operator must install)     |
| Midway expired (`mwinit -l` empty)     | Escalate HIGH (operator must run mwinit)  |
| `harmony npm` non-zero exit            | Escalate HIGH (unknown; needs human)      |
| `npm ping` doesn't hit CodeArtifact    | Escalate MEDIUM (registry misconfig)      |
| Everything works                       | Silent success receipt                    |

## Cooldown: 18h

- Token TTL: 22h
- Refresh every 18h → 4h safety buffer before expiry

## Out of scope

- **Midway auth refresh.** `mwinit` requires YubiKey/FIDO2 interaction, so
  it cannot be automated from a daemon. If Midway expires, this plugin
  escalates and a human must run `mwinit` manually.
- **Non-Harmony npm registries.** `npmjs.org` tokens don't have this
  short TTL; this plugin only touches CodeArtifact via `harmony npm`.

## Run

```bash
cd "$(git rev-parse --show-toplevel)/plugins/harmony-npm-refresh" && bash run.sh
```

# Auto-Test-PR Branch-Protection Template

This directory holds the per-rig opt-in template for the
`refs/heads/auto-test/*/*` branch-protection rule, as specified in Phase 0
task 13 of the Auto-Test-PR design (`.designs/auto-test-pr/synthesis.md`).

## Why

`mol-auto-test-pr-cycle` pushes feature branches named
`auto-test/<rig>/<bead-id>` to the rig's GitHub origin. Without a
branch-protection rule on that namespace, an attacker with rig-write access
could push commits *into* an in-flight auto-test branch and have them
reviewed and merged as if they were polecat-authored. This implements
risk **R11** and security control **C-SEC-6** from the design.

The protection MUST be:

- **Scoped to `refs/heads/auto-test/*/*`** — not the whole repo. Polecat
  branches and other rig traffic continue unchanged.
- **Restricted to the cycle-agent / Refinery service identity** — only
  the bot that runs `mol-auto-test-pr-cycle` (or the Refinery agent
  reviewing its output) may push to these branches.
- **Enforced (`enforcement: active`)** — not "evaluate" / dry-run.

## Files

| File | Purpose |
|------|---------|
| `branch-protection-ruleset.json` | GitHub Rulesets API payload. Source of truth for the rule shape. |
| `apply-branch-protection.sh` | Idempotent wrapper that creates-or-updates the ruleset on a target repo. |

## Apply (manual, single-rig — v1)

```sh
# As an admin on the target repo:
templates/auto-test-pr/apply-branch-protection.sh gagecane/gastown
```

The default bypass actor is the `admin` RepositoryRole (`actor_id=5`), which
matches v1's pilot-rig model where the rig owner IS the cycle-agent (no
separate service identity yet exists). The rule still blocks any
non-admin push to the `auto-test/*` namespace, including push attempts
from misconfigured polecats running under other identities.

## Apply (multi-rig — v2)

For multi-rig federation, each rig provisions a dedicated GitHub App or
Deploy Key to act as its cycle-agent. Pass that actor's numeric id:

```sh
templates/auto-test-pr/apply-branch-protection.sh <owner/repo> <bypass-actor-id>
```

`gt auto-test-pr enable --emit-template` (Phase 0 task 2d) emits this
script invocation as part of the per-rig opt-in template, so new rigs
inherit the protection on enable.

## Verify

```sh
# List all rulesets on the repo:
gh api repos/<owner>/<repo>/rulesets

# Confirm the auto-test-pr ruleset is present and active:
gh api repos/<owner>/<repo>/rulesets \
  --jq '.[] | select(.name == "auto-test-pr-branch-namespace")'
```

To verify the protection works in practice (Phase 0 task 13 acceptance
criterion), attempt a push from a non-bypass identity to a matching
branch:

```sh
git checkout -b auto-test/test-rig/test-bead
git commit --allow-empty -m "test: should be rejected"
git push origin auto-test/test-rig/test-bead
# Expected: "remote: error: Bypass not permitted"
```

A push from the bypass identity (admin in v1, the configured service
identity in v2) MUST succeed.

## Rollback

```sh
RULESET_ID="$(gh api repos/<owner>/<repo>/rulesets --jq \
  '.[] | select(.name == "auto-test-pr-branch-namespace") | .id')"
gh api repos/<owner>/<repo>/rulesets/${RULESET_ID} -X DELETE
```

Delete only the named ruleset — do NOT touch other rulesets on the repo.

## Caveats — v1

- v1 uses a **single git identity** for the rig owner and the cycle-agent
  (the same person). The "bypass to the cycle-agent only" semantics are
  approximated by "bypass to admins only" — anyone with admin push to
  the repo is implicitly trusted as the cycle-agent. This is a known
  design compromise documented in `.designs/auto-test-pr/synthesis.md`
  task 13.
- Splitting the cycle-agent into its own service identity is a v2 task
  tracked under "Second-rig opt-in" in the synthesis (Q3 / Open
  Questions). When v2 lands, re-run `apply-branch-protection.sh` with
  the new actor_id; the script's idempotency makes this a safe drop-in.

## References

- `.designs/auto-test-pr/synthesis.md` — Phase 0 task 13, R11, C-SEC-6
- `.designs/auto-test-pr/security.md` — branch-namespace threat model
- `.plan-reviews/auto-test-pr/review-round-3.md` — test 13 coverage check
- [GitHub Rulesets API](https://docs.github.com/en/rest/repos/rules)

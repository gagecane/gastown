+++
name = "lia-codegen-digest"
description = "Weekly codegen quality digest per lia rig: first-pass approval rate, review comments per PR, gate failure rate, and PR time-to-merge from gh + beads"
version = 1

[gate]
type = "cron"
schedule = "0 9 * * 1"

[tracking]
labels = ["plugin:lia-codegen-digest", "category:quality"]
digest = true

[execution]
type = "script"
timeout = "10m"
notify_on_failure = false
severity = "low"
+++

# lia Codegen Quality Digest

A read-only weekly digest of codegen quality for each **lia** rig (`lia_*`).
It instruments nothing new — every metric is derived from data Gas Town
already has: GitHub (via `gh`) and beads (via `gt rig bd`).

The digest runs once a week (cron `0 9 * * 1` — Monday 09:00) and, for each
lia rig, emits a Markdown summary to stdout and records a non-ephemeral
digest bead in that rig's beads database (labels
`type:digest,plugin:lia-codegen-digest,rig:<rig>`) so the four metrics are
queryable as a weekly trend.

Requires: `gh` CLI installed and authenticated (`gh auth status`). When `gh`
is unauthenticated, the plugin records a single skip receipt and exits 0 — it
never fails the daemon.

## The four metrics (7-day window)

For each merged PR in the window:

1. **First-pass approval rate** — merged PRs that went through **no**
   `CHANGES_REQUESTED` review cycle, divided by total merged PRs. A PR merged
   straight through (incl. via the merge queue with no GitHub review) counts
   as first-pass; a PR that ever received a `CHANGES_REQUESTED` review does
   not. Source: `gh pr list --json reviews` review states.

2. **Review comments per PR (median + max)** — inline review-comment volume
   per PR. Source: `gh api repos/<repo>/pulls/<n>/comments | length`.

3. **Gate failure rate** — share of work beads in the window that recorded a
   build-check / gate failure pre-merge. Source: beads in the rig whose
   labels or description carry a gate/build-check failure signal (e.g.
   `MERGE REJECTION`, `gate fail`, `build-check`), divided by total
   non-ephemeral work beads in the window. Best-effort: reported as `n/a`
   when the rig has no work beads in the window.

4. **PR time-to-merge — median + max (hours)** — wall-clock from PR open to
   merge. Source: `mergedAt − createdAt`.

> **Note on metric 4.** The originating spec bead `hq-bvt9u` defined three
> metrics explicitly and left the fourth to implementation; that bead was not
> present in the town db at build time. PR time-to-merge was chosen because it
> is the most distinct from metrics 1–3 (which all measure review/gate
> quality) and captures codegen *velocity* from data already in hand. If the
> real fourth metric surfaces, swap the `metric4_*` block in `run.sh`.

## Detection

```bash
gh auth status 2>/dev/null || { echo "SKIP: gh not authenticated"; exit 0; }
```

lia rigs are discovered from `gt rig list --json` (names matching `lia*`).
Each rig's GitHub repo and default branch come from
`$GT_TOWN_ROOT/<rig>/config.json` (`git_url`, `default_branch`).

## Tunables (env)

- `DIGEST_WINDOW_DAYS` — metric window in days (default `7`).
- `DIGEST_MAX_PRS` — cap on PRs inspected per rig for per-PR API calls
  (default `100`); truncation is reported in the digest.
- `LIA_RIG_GLOB` — shell glob selecting which rigs to include (default `lia*`).
- `DIGEST_DRY_RUN` — when set (non-empty), compute and print every digest but
  write no beads. Useful for previewing the output.

## Record Result

Per rig: one non-ephemeral digest bead holding the Markdown summary
(`type:digest,plugin:lia-codegen-digest,rig:<rig>`). Overall: one ephemeral
plugin-run receipt (`type:plugin-run,plugin:lia-codegen-digest,result:...`).

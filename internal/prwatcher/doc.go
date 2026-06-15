// Package prwatcher implements the GitHub PR reviewer-comment → autonomous-action
// poller for Gas Town (gc-nfoah7).
//
// Some Gas Town instances push to GitHub via pull requests rather than directly
// to main. On those instances a human reviewer leaves review comments on a PR,
// and someone has to read each comment, decide what to do, and dispatch the
// fix. Gas Town has no native support for this: gt config exposes no
// review/pr/comment keys, the Refinery only runs mechanical gates (build/test →
// merge or spawn-fresh-polecat) and never reads human review comments, and
// gt ci-watcher only watches post-merge CI on main. prwatcher is the external
// glue layer that closes that gap using existing primitives (gh CLI, bd create,
// gt sling, gt mail).
//
// It is modeled directly on internal/ciwatcher: a stateless, one-shot,
// idempotent poller invoked from a plugin on a cooldown cadence. On every poll
// cycle:
//
//  1. Fetch UNRESOLVED review comments on open PRs in the rig's GitHub repo via
//     the gh CLI (GraphQL review threads where isResolved=false).
//  2. Dedup against an on-disk ledger of already-actioned comment IDs
//     (<townRoot>/.runtime/pr-watcher-actioned-<rig>), so re-polls never
//     double-action a comment.
//  3. Triage each fresh comment (see triage.go). The classifier is heuristic
//     and gate-by-default: a comment is auto-dispatched only when it clearly
//     matches a mechanical signal (typo, gofmt, rename, formatting, …) AND
//     carries no judgment signal (why/consider/refactor/security/race/…).
//     Everything else is gated for human confirmation.
//  4. Act:
//     - Mechanical → bd create a work bead, gt sling it to the rig (a fresh
//     polecat fixes + re-pushes), and post an ack reply on the PR thread.
//     - Judgment → bd create a work bead labeled needs-human-triage, mail the
//     mayor at HIGH severity, and post an ack reply on the PR thread.
//     Either way the reviewer sees the comment was picked up.
//
// Poll-vs-webhook: we poll, mirroring the ci-watcher-poll plugin precedent. The
// gt callbacks seam (External triggers: webhooks, timers → mayor inbox) remains
// a future alternative; polling is chosen here because it needs no inbound
// network surface and reuses the existing plugin cooldown machinery.
//
// Cold start: on the first-ever poll for a rig (no actioned ledger) every open
// review comment looks new. To avoid flooding the rig with beads for a backlog
// of pre-existing comments, comments created before now-ColdStartLookback are
// recorded as actioned but not dispatched. Once the ledger exists every unseen
// comment is actioned as normal.
//
// Design context: bead gc-nfoah7. Sibling: internal/ciwatcher (gu-xuzc).
package prwatcher

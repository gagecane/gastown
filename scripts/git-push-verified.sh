#!/usr/bin/env bash
# git-push-verified.sh — wrap `git push` with an unambiguous success/failure marker.
#
# Purpose
# -------
# The Refinery agent's reasoning loop has been observed reporting `git push`
# as "rejected" even when the push succeeded (exit 0 + remote-side
# "old..new branch -> ref" confirmation). Once the agent latches onto a
# hallucinated rejection, it enters the manual-hold state described in
# gu-0099 and refuses to retry until a human gives explicit direction.
#
# Bead gu-vph7 documents a live incident in gastown_upstream where the agent
# reported a successful push (1772a631..aa88ed10  temp -> main) as rejected,
# wedging the merge queue. The mayor verified via `git ls-remote` that the
# push had landed.
#
# The fix is to remove the LLM from the success-classification path. This
# script wraps `git push`, captures both the exit code and the remote-side
# success line, and emits exactly one of two structured markers:
#
#   PUSH_OK:     <oldsha>..<newsha> <local> -> <remote>
#   PUSH_FAILED: <reason> [: <details>]
#
# The Refinery formula treats these as the ONLY signals — never the agent's
# free-form reading of the underlying git output. See bead gu-vph7 acceptance
# criteria for the complete contract.
#
# Usage
# -----
#   scripts/git-push-verified.sh <git-push-args...>
#
# Examples
#   scripts/git-push-verified.sh origin HEAD:main
#   GT_ALLOW_DIRECT_MAIN=1 scripts/git-push-verified.sh origin temp:main
#
# Exit codes
#   0   — push succeeded; stdout has a single line beginning with `PUSH_OK:`
#   1   — push failed (any reason); stdout has a single line beginning with
#         `PUSH_FAILED:`. The full git output is preserved on stderr for
#         human debugging.
#
# Detection rules (applied in order)
#   1. If `git push` exited non-zero  → PUSH_FAILED: exit-<rc>
#   2. If output contains `! [rejected]` or `! [remote rejected]`
#                                    → PUSH_FAILED: rejected
#   3. If output contains a `<sha>..<sha> <ref> -> <ref>` success line
#                                    → PUSH_OK: <that line>
#   4. Otherwise (zero exit, no marker line — e.g. up-to-date push)
#                                    → PUSH_OK: up-to-date
#
# Why these rules
#   - Rule 1 trusts git's exit code, which is the canonical signal.
#   - Rule 2 catches the case where git inexplicably exits 0 but printed
#     a rejection line (defense in depth; not observed in practice but
#     cheap to guard against).
#   - Rule 3 extracts the remote-side confirmation that the agent kept
#     ignoring in gu-vph7. Reproducing this line in the marker means the
#     refinery can also persist it as audit evidence.
#   - Rule 4 covers `Everything up-to-date` (genuine no-op pushes), which
#     are correctly successful even though they emit no `->` line.

set -uo pipefail

if [ "$#" -eq 0 ]; then
  echo "PUSH_FAILED: usage — git-push-verified.sh <git-push-args...>" >&2
  echo "PUSH_FAILED: usage — git-push-verified.sh <git-push-args...>"
  exit 1
fi

# Run git push, capture combined output AND exit code.
# We write the raw output to stderr (so humans / log scrapers still see the
# real git messages) and emit ONLY the structured marker on stdout.
out=$(git push "$@" 2>&1)
rc=$?

# Echo full git output to stderr verbatim so the existing log trail is preserved.
printf '%s\n' "$out" >&2

# Rule 1: non-zero exit is failure, period.
if [ "$rc" -ne 0 ]; then
  # Surface the most useful one-liner from the output if we can find one.
  reason="exit-$rc"
  rejected_line=$(printf '%s\n' "$out" | grep -E '! \[(remote )?rejected\]' | head -1 || true)
  if [ -n "$rejected_line" ]; then
    reason="rejected"
    echo "PUSH_FAILED: $reason: $rejected_line"
  else
    err_line=$(printf '%s\n' "$out" | grep -E '^(error|fatal):' | head -1 || true)
    if [ -n "$err_line" ]; then
      echo "PUSH_FAILED: $reason: $err_line"
    else
      echo "PUSH_FAILED: $reason"
    fi
  fi
  exit 1
fi

# Rule 2: zero exit but rejection text present — still a failure.
rejected_line=$(printf '%s\n' "$out" | grep -E '! \[(remote )?rejected\]' | head -1 || true)
if [ -n "$rejected_line" ]; then
  echo "PUSH_FAILED: rejected: $rejected_line"
  exit 1
fi

# Rule 3: extract the canonical "<old>..<new> <local> -> <remote>" success
# line. Also accept new-branch pushes which use "* [new branch]      <local> -> <remote>".
success_line=$(printf '%s\n' "$out" \
  | grep -oE '[0-9a-f]+\.\.[0-9a-f]+ +[^ ]+ +-> +[^ ]+' \
  | head -1 || true)
if [ -z "$success_line" ]; then
  success_line=$(printf '%s\n' "$out" \
    | grep -oE '\* \[new branch\] +[^ ]+ +-> +[^ ]+' \
    | head -1 || true)
fi
if [ -z "$success_line" ]; then
  success_line=$(printf '%s\n' "$out" \
    | grep -oE '\* \[new tag\] +[^ ]+ +-> +[^ ]+' \
    | head -1 || true)
fi
if [ -z "$success_line" ]; then
  success_line=$(printf '%s\n' "$out" \
    | grep -oE '\+ +[0-9a-f]+\.\.\.[0-9a-f]+ +[^ ]+ +-> +[^ ]+' \
    | head -1 || true)
fi

if [ -n "$success_line" ]; then
  echo "PUSH_OK: $success_line"
  exit 0
fi

# Rule 4: zero exit, no rejection, no `->` line — this is the
# "Everything up-to-date" path. Treat as success.
echo "PUSH_OK: up-to-date"
exit 0

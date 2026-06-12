Advance the work queue: note context, hook the next bead, hand off to a fresh session.

Optional argument (bead id and/or a context note): $ARGUMENTS

Execute these steps in order:

1. Capture context. For any bead you touched this session, append a short note so
   the next agent inherits your context:
   `bd update <bead-id> --append-notes "<what you learned / current state>"`
   If $ARGUMENTS contains a free-text note, fold it into these notes.

2. Select the next bead.
   - If $ARGUMENTS contains a bead id (gt-xxx, hq-xxx, gu-xxx, gc-xxx), use it.
   - Otherwise run `bd ready` and pick the top-priority ready bead. Confirm it
     exists with `bd show <bead-id>` before proceeding.

3. Reflect on friction. Before handing off, think back over this session and note
   any genuine friction you hit: tooling failures, confusing error messages,
   missing or wrong docs, flaky tests, permission/config snags, or steps that
   wasted time. For each real friction item — and only when there is genuine
   friction — file one concise bead so the papercut is captured instead of lost
   at the session boundary:
   `bd create -t "<short friction summary>" -d "<what happened, where, impact>" -l friction`
   Keep it lightweight: one bead per distinct item, skip entirely if the session
   was smooth. Do not invent friction to fill the step.

4. Hook the next bead and hand off in one step:
   `gt handoff <bead-id> -y -s "NEXTBEAD: <bead-id> <short title>"`
   `gt handoff <bead>` hooks that work first, then restarts — so this both
   advances the queue and cycles the session.

Note: The new session auto-primes via the SessionStart hook and continues the
hooked bead. Unlike /handoff (which cycles whatever is already on the hook),
/nextbead deliberately selects and hooks the next bead before handing off.

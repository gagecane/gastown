---
name: gastown-formula-author
description: |
  Author, validate, and iterate on Gas Town workflow formula TOML files.
  Use when asked to "create a formula", "write a formula", "new workflow formula",
  "formula for X", "validate this formula", "fix this formula TOML",
  "add a step to formula Y", or "edit the steps/vars/prompts in formula Y".
  Also use when the user describes a multi-step
  agent workflow they want encoded as a repeatable formula.
  Do NOT use for running existing formulas (use gt formula run),
  or for editing a formula's per-molecule overlay (use gt formula overlay edit) —
  editing the formula file's own contents IS in scope.
---

# gastown-formula-author

## Critical

- **`gt formula show` does NOT validate.** It pretty-prints whatever parsed and
  silently ignores cycles, dangling `needs`, and undeclared variables. The real
  structural validator is **`gt formula run <name> --dry-run --rig <rig>`** — it
  runs the parser (`internal/formula/parser.go`), which checks required fields,
  cycles, and `needs` references, and exits non-zero with the error. Always
  validate with `--dry-run`, never with `show`.
- **A formula's filename must be `<name>.formula.toml`**, and the inner
  `formula = "<name>"` field must match the filename stem. `gt formula show foo`
  looks for `foo.formula.toml`.
- **Two distinct template syntaxes.** Workflow/expansion `[[steps]]` and
  `[[template]]` use **Handlebars `{{var}}`** (resolved against `[vars]`). Convoy
  `[prompts]` and `[output]` use **Go `text/template` `{{.var}}`** (note the
  leading dot). Do not mix them — `{{.feature}}` in a workflow step or `{{feature}}`
  in a convoy prompt will not resolve.

## Instructions

This is a **workflow-automation** skill using **Pattern 1 (sequential steps with
validation gates)**. Author or fix a formula by walking these steps in order; the
gate at the end (`--dry-run`) is non-negotiable.

### Step 1: Pick the formula type

The `type` field is one of four values:

| Type        | Execution model                                  | Key fields                          |
|-------------|--------------------------------------------------|-------------------------------------|
| `workflow`  | Sequential steps with a dependency DAG           | `[[steps]]` with `needs`            |
| `convoy`    | Parallel legs + a synthesis step                 | `[inputs]`, `[prompts]`, `[[legs]]`, `[synthesis]` |
| `expansion` | Template steps that replace one target step      | `[[template]]` with `[compose.expand]` |
| `aspect`    | Cross-cutting parallel analysis (convoy-like)    | `[[aspects]]`                       |

If unsure, ask the user how the work executes: "one agent doing ordered steps"
→ `workflow`; "several agents in parallel then a merge" → `convoy`.

For the complete per-type field list, see [`references/schema.md`](references/schema.md).

### Step 2: Scaffold a starter file

```bash
gt formula create <name> --type=workflow   # types: task | workflow | patrol
```

This writes `<name>.formula.toml` into the project `.beads/formulas/`. The
`--type` values here are CLI scaffolds (`task` = single step) — the file's own
`type` field is what the parser reads, so for `convoy`/`expansion`/`aspect` you
edit the `type` field after scaffolding (or write the file directly). For a
small formula it is often faster to write the file by hand from the schema below.

### Step 3: Write the required top-level fields

Every formula needs these (the parser errors without `formula` and a valid `type`):

```toml
description = "One paragraph: what this workflow does."
formula = "<name>"      # MUST equal the filename stem
type = "workflow"
version = 1
```

### Step 4: Write the steps / legs

**Workflow** — ordered steps; `needs` lists the step IDs that must finish first.
IDs must be unique; every `needs` entry must reference a real step ID; the graph
must be acyclic.

```toml
[[steps]]
id = "design"
title = "Design {{feature}}"
description = "Think through the approach for {{feature}} before coding."
acceptance = "Design doc committed."

[[steps]]
id = "implement"
title = "Implement {{feature}}"
needs = ["design"]
description = "Write the code. Follow the design."
```

**Convoy** — parallel `[[legs]]` plus a `[synthesis]` whose `depends_on` lists
the leg IDs to merge. Convoy prompts use Go `{{.var}}` syntax:

```toml
[[legs]]
id = "security"
title = "Security Review"
focus = "Vulnerabilities and attack surface"
description = "Review for injection, auth bypass, secret exposure."

[synthesis]
title = "Synthesis"
description = "Merge findings from {{.output.directory}}."
depends_on = ["security"]
```

### Step 5: Declare variables

Any `{{var}}` used in a workflow step must be declared in `[vars]`, or
`bd mol wisp` fails with "missing required variables". Two equivalent forms:

```toml
[vars]
assignee = "default-owner"          # shorthand string = the default value

[vars.feature]                       # full table form
description = "The feature being implemented"
required = true
```

Handlebars control words (`else`, `this`, `range`, `with`, `end`, …) are NOT
treated as variables and need no declaration.

### Step 6: Validate (the gate — do not skip)

Two complementary validators. Run the offline one in the edit loop, the
authoritative parser as the final gate.

```bash
# Offline structural check (no rig needed). Mirrors parser.go and also catches
# undeclared {{var}} usage and filename/stem mismatch.
python3 scripts/validate-formula.py <path/to/name.formula.toml>

# Authoritative parser + dispatch preview (the gate).
gt formula run <name> --dry-run --rig <rig>
```

Exit 0 with a dispatch preview = the formula parses, has no cycles, and all
`needs` references resolve. A non-zero exit prints the exact problem (see
[`references/troubleshooting.md`](references/troubleshooting.md)). Re-run until
clean. Then confirm the rendered shape:

```bash
gt formula show <name>      # human-readable: type, vars, step tree
gt formula list             # confirm it appears in a search path
```

### Step 7: Place it in the right search path

Resolution order (first match wins), from `gt formula --help`:

1. `.beads/formulas/` (project)
2. `~/.beads/formulas/` (user)
3. `$GT_ROOT/.beads/formulas/` (orchestrator)

Put a project-specific formula in the project path; a personal one in the user
path. `gt formula create` already writes to the project path.

## Examples

**Example 1 — Author a new workflow from scratch.**
User says: *"Create a workflow formula that designs, implements, then tests a feature."*
→ Actions: (1) `type = "workflow"`; (2) `gt formula create feature-flow --type=workflow`;
(3) write three `[[steps]]` (`design` → `implement` needs `design` → `test` needs
`implement`) with a `{{feature}}` var in `[vars]`; (4) `gt formula run feature-flow
--dry-run --rig myrig` → exit 0; (5) `gt formula show feature-flow` to confirm the tree.
→ Result: a valid `feature-flow.formula.toml` in `.beads/formulas/`.

**Example 2 — Fix a formula that won't run.**
User says: *"This formula errors when I dry-run it."*
→ Actions: (1) `gt formula run <name> --dry-run --rig myrig` and read the error;
(2) e.g. `step "implement" needs unknown step: desgin` → fix the typo in `needs`;
(3) re-run `--dry-run` until exit 0.
→ Result: formula parses and dispatches cleanly.

**Example 3 — Add a step to an existing formula.**
User says: *"Add a lint step before the deploy step."*
→ Actions: (1) read the file; (2) insert a `[[steps]]` block with `id = "lint"`;
(3) change the `deploy` step's `needs` to include `"lint"`; (4) `--dry-run` to
confirm no cycle and the new ID resolves.
→ Result: `deploy` now waits on `lint`, validated.

## Troubleshooting

When a validator rejects the formula, look up the exact error string, its
cause, and the fix in [`references/troubleshooting.md`](references/troubleshooting.md).
The common ones: `formula field is required`, `duplicate step id`,
`step "X" needs unknown step: Y`, `cycle detected involving: X`,
`synthesis depends_on references unknown leg: X`, and `missing required
variables`. Remember: `gt formula show` does NOT validate — trust
`scripts/validate-formula.py` and `--dry-run`.

- **My formula's convoy is barely progressing / shows up in `gt convoy
  stranded`.** If you launched it with `gt formula run` from a CREW session,
  the step beads are crew-owned and the auto-dispatch plugin skips
  `*/crew/*`-owned beads (the `is_agent_owner` filter), so the convoy advances
  only on the Deacon's periodic stranded-feed cycle — slow and bursty. The
  steps are dependency-eligible; they're just invisible to the fast dispatcher.
  To accelerate, sling the ready steps directly: `gt sling <step-bead-id>
  <rig>` (repeat each wave), or launch the formula from a non-crew context.
  See engine bug gu-3y6ro.

## Reference files

Loaded on demand — keep SKILL.md focused (progressive disclosure):

- [`references/schema.md`](references/schema.md) — complete per-type field
  reference (workflow, convoy, expansion, aspect), composition, variables, and
  type inference. Consult for any field not in the quick examples above.
- [`references/troubleshooting.md`](references/troubleshooting.md) — every
  validation error string → cause → fix.
- [`references/design-notes.md`](references/design-notes.md) — skill-design
  metadata (use cases, framing, success criteria, capabilities) for maintainers.
- [`scripts/validate-formula.py`](scripts/validate-formula.py) — offline
  structural validator. Run `python3 scripts/validate-formula.py <file>` in the
  edit loop before the `--dry-run` gate.

For the source of truth, read `internal/formula/types.go` (schema) and
`internal/formula/parser.go` (`Validate`, `checkCycles`).

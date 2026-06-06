---
name: gastown-formula-author
description: |
  Author, validate, and iterate on Gas Town workflow formula TOML files.
  Use when asked to "create a formula", "write a formula", "new workflow formula",
  "formula for X", "validate this formula", "fix this formula TOML", or
  "add a step to formula Y". Also use when the user describes a multi-step
  agent workflow they want encoded as a repeatable formula.
  Do NOT use for running existing formulas (use gt formula run),
  or for editing formula overlays (use gt formula overlay edit).
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

The `type` field is one of four values (see `internal/formula/types.go`):

| Type        | Execution model                                  | Key fields                          |
|-------------|--------------------------------------------------|-------------------------------------|
| `workflow`  | Sequential steps with a dependency DAG           | `[[steps]]` with `needs`            |
| `convoy`    | Parallel legs + a synthesis step                 | `[inputs]`, `[prompts]`, `[[legs]]`, `[synthesis]` |
| `expansion` | Template steps that replace one target step      | `[[template]]` with `{target}` macro |
| `aspect`    | Cross-cutting advice woven around matched steps  | `[[advice]]`, `[[pointcuts]]`       |

If unsure, ask the user how the work executes: "one agent doing ordered steps"
→ `workflow`; "several agents in parallel then a merge" → `convoy`.

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

```bash
gt formula run <name> --dry-run --rig <rig>
```

Exit 0 with a dispatch preview = the formula parses, has no cycles, and all
`needs` references resolve. A non-zero exit prints the exact problem (see
Troubleshooting). Re-run until clean. Then confirm the rendered shape:

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

| Error (from `--dry-run`) | Cause | Fix |
|---|---|---|
| `formula field is required` | Missing top-level `formula = "..."` | Add it; match the filename stem |
| `invalid formula type "X"` | `type` not one of convoy/workflow/expansion/aspect | Use a valid type |
| `workflow formula requires at least one step` | No `[[steps]]` blocks | Add at least one step |
| `duplicate step id: X` | Two steps share an `id` | Make IDs unique |
| `step "X" needs unknown step: Y` | `needs` points at a non-existent ID (often a typo) | Correct the referenced ID |
| `cycle detected involving: X` | Steps depend on each other in a loop | Break the cycle; `needs` must form a DAG |
| `convoy formula requires at least one leg` | Convoy with no `[[legs]]` | Add legs |
| `synthesis depends_on references unknown leg: X` | `[synthesis].depends_on` names a missing leg | Fix the leg ID |
| `missing required variables` (at `gt formula run`) | A `{{var}}` is used but not in `[vars]` | Declare it in `[vars]` (use `default=""` for computed vars) |
| `formula "X" not found in search paths` | File misnamed or in wrong dir | Name it `X.formula.toml` in a search-path dir (Step 7) |
| `gt formula show` looks fine but `--dry-run` fails | `show` does not validate | Trust `--dry-run`, not `show` |

For deep schema questions (every field per type), read
`internal/formula/types.go`; for the exact validation rules, read
`internal/formula/parser.go` (`Validate`, `checkCycles`).

## Use Cases and Success Criteria

### Use Case 1: Author a new workflow formula from scratch

**Trigger:** User says "create a formula for X", "write a workflow formula",
"I need a formula that does X then Y then Z", or "encode this workflow as a formula"

**Steps:**
1. Clarify formula type (workflow, convoy, patrol, expansion, aspect) based on
   the user's described execution model
2. Determine variables needed (from user description or by asking)
3. Generate the `.formula.toml` file with correct TOML structure:
   - Top-level fields: `description`, `formula`, `type`, `version`
   - Steps/legs/aspects with proper dependency DAGs (`needs` field)
   - Variable declarations with descriptions, defaults, and required flags
4. Write to the appropriate formulas directory
5. Validate with `gt formula run <name> --dry-run --rig <rig>` (parse check, DAG
   cycle detection, `needs` resolution), then `gt formula show <name>` to confirm
   the rendered tree

**Result:** A syntactically valid, well-structured formula TOML file placed in
the correct search path, passing `gt formula run --dry-run` and viewable via
`gt formula show <name>`.

---

### Use Case 2: Validate and fix an existing formula

**Trigger:** User says "validate this formula", "check my formula TOML",
"why won't this formula parse", "fix this formula", or shows a formula
that `gt formula run --dry-run` rejects

**Steps:**
1. Read the formula file (or accept pasted TOML content)
2. Run structural validation:
   - TOML syntax (valid TOML?)
   - Required fields present (`formula`, `type`, `description`)
   - Type-specific fields match (workflow needs `[[steps]]`, convoy needs `[[legs]]`)
   - Step dependency DAG is acyclic
   - All `needs` references point to existing step IDs
   - Variables referenced in `{{var}}` placeholders are declared in `[vars]`
   - No duplicate step/leg IDs
   (run `gt formula run <name> --dry-run --rig <rig>` to surface parse, cycle,
   and dangling-`needs` errors — `gt formula show` does NOT validate)
3. Report issues with line-level context and suggested fixes
4. Apply fixes if user approves
5. Re-validate until clean

**Result:** Formula passes `gt formula run <name> --dry-run` without errors; all
structural issues resolved with explanations.

---

### Use Case 3: Add/modify steps in an existing formula

**Trigger:** User says "add a step to formula X", "insert a gate step before
the build", "change the deploy step to also run lint", or "reorder these steps"

**Steps:**
1. Load the existing formula via file read
2. Parse the step dependency graph
3. Determine where the new/modified step fits in the DAG
4. Update `needs` fields to maintain correct ordering
5. Write the modified TOML preserving existing formatting/comments
6. Validate the result (no cycles, no dangling refs)

**Result:** Modified formula with correct step ordering, no broken dependencies,
validated via `gt formula run <name> --dry-run`.

---

## Framing Choice: Tool-first

This skill is **tool-first**. The user has access to the `gt formula` CLI tooling
and the formula TOML format. The skill supplies the expert workflow knowledge:
correct TOML structure, valid field combinations per formula type, dependency
DAG rules, variable declaration patterns, and the conventions that make formulas
work well in the Gas Town execution model.

The user already knows WHAT they want the formula to do; the skill knows HOW
to encode that intent correctly in the formula TOML format.

---

## Success Criteria

1. **Trigger accuracy (~90%):** Triggers on formula-authoring/validation requests.
   Test prompts:
   - "Create a patrol formula for nightly DB backup" -> triggers
   - "Write a convoy formula with 3 legs" -> triggers
   - "This formula won't parse, can you fix it?" -> triggers
   - "Add a lint step before the deploy step" -> triggers
   - "Encode my workflow as a formula" -> triggers
   - "Run the code-review formula" -> does NOT trigger
   - "Show me what formulas are available" -> does NOT trigger
   - "Edit the overlay for mol-polecat-work" -> does NOT trigger

2. **Fewer tool calls than baseline:** Without the skill, an agent must:
   discover the TOML schema by reading source code, examine multiple example
   formulas, trial-and-error the structure. With the skill: direct generation
   from known schema in 2-3 tool calls (write file + validate).

3. **Zero failed tool/MCP calls per run:** All generated TOML parses correctly
   on first `gt formula run --dry-run` invocation. No user correction needed for
   structural issues.

4. **Correct by construction:** Generated formulas pass the internal parser
   (internal/formula/parser.go) without modification.

---

## Required Capabilities

### Built-in (no MCP needed):
- **File creation/editing:** Write `.formula.toml` files to disk
- **Code execution (Bash):** Run `gt formula run <name> --dry-run --rig <rig>` to
  validate, `gt formula show <name>` to view the rendered tree, `gt formula list`
  to check placement, `gt formula create` for scaffolding
- **File reading:** Read existing formulas for reference patterns, read
  `internal/formula/types.go` for schema reference

### Key knowledge embedded in skill (no external tool needed):
- Formula TOML schema (all four types: workflow, convoy, expansion, aspect)
- Step dependency DAG rules (needs field, cycle detection)
- Variable declaration patterns (`[vars.X]` tables vs shorthand strings)
- Search path resolution (project > user > orchestrator)
- Naming conventions (kebab-case with `.formula.toml` suffix)
- Type-specific required/optional fields
- Template variable syntax (`{{variable_name}}` for workflows,
  `{{.variable_name}}` for convoys/Go templates)

### No MCP tools required
This is a pure workflow-automation skill using built-in file and shell capabilities.

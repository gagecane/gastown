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
4. Validate the generated TOML: parse check, DAG cycle detection, variable
   reference resolution
5. Write to the appropriate formulas directory and confirm via `gt formula show`

**Result:** A syntactically valid, well-structured formula TOML file placed in
the correct search path, loadable by `gt formula show <name>`.

---

### Use Case 2: Validate and fix an existing formula

**Trigger:** User says "validate this formula", "check my formula TOML",
"why won't this formula parse", "fix this formula", or shows a formula
that `gt formula show` rejects

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
3. Report issues with line-level context and suggested fixes
4. Apply fixes if user approves
5. Re-validate until clean

**Result:** Formula passes `gt formula show <name>` without errors; all
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
validated via `gt formula show`.

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
   on first `gt formula show` invocation. No user correction needed for
   structural issues.

4. **Correct by construction:** Generated formulas pass the internal parser
   (internal/formula/parser.go) without modification.

---

## Required Capabilities

### Built-in (no MCP needed):
- **File creation/editing:** Write `.formula.toml` files to disk
- **Code execution (Bash):** Run `gt formula show <name>` to validate,
  run `gt formula list` to check placement, run `gt formula create` for
  scaffolding
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

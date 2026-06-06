# gastown-formula-author — design notes

Skill-design metadata: use cases, framing choice, success criteria, and
capability list. This is not needed at runtime — it documents why the skill is
shaped the way it is, for maintainers extending it.

## Use cases

### Use Case 1: Author a new workflow formula from scratch

**Trigger:** "create a formula for X", "write a workflow formula",
"I need a formula that does X then Y then Z", "encode this workflow as a formula".

**Steps:**
1. Clarify formula type (workflow, convoy, expansion, aspect) from the described
   execution model.
2. Determine variables needed (from the description or by asking).
3. Generate the `.formula.toml`: top-level fields (`description`, `formula`,
   `type`, `version`), steps/legs/aspects with proper dependency DAGs, variable
   declarations with descriptions/defaults/required flags.
4. Write to the appropriate formulas directory.
5. Validate with `scripts/validate-formula.py` then
   `gt formula run <name> --dry-run --rig <rig>`; confirm the tree with
   `gt formula show <name>`.

**Result:** A syntactically valid, well-structured formula passing the parser.

### Use Case 2: Validate and fix an existing formula

**Trigger:** "validate this formula", "check my formula TOML", "why won't this
formula parse", "fix this formula", or a formula `--dry-run` rejects.

**Steps:** read the file → run `validate-formula.py` and `--dry-run` →
report issues with context and fixes → apply → re-validate until clean.

**Result:** Formula passes `--dry-run` with all structural issues resolved.

### Use Case 3: Add/modify steps in an existing formula

**Trigger:** "add a step to formula X", "insert a gate step before the build",
"change the deploy step to also run lint", "reorder these steps".

**Steps:** load the formula → parse the step DAG → place the new/modified step →
update `needs` to maintain ordering → write preserving formatting → validate.

**Result:** Correct step ordering, no broken deps, validated via `--dry-run`.

## Framing choice: tool-first

The user has the `gt formula` CLI and the TOML format. The skill supplies the
expert workflow knowledge: correct structure, valid field combinations per
type, DAG rules, variable declaration patterns, and the conventions that make
formulas work in the Gas Town execution model. The user knows WHAT they want;
the skill knows HOW to encode it correctly.

## Success criteria

1. **Trigger accuracy (~90%):**
   - "Create a patrol formula for nightly DB backup" → triggers
   - "Write a convoy formula with 3 legs" → triggers
   - "This formula won't parse, can you fix it?" → triggers
   - "Add a lint step before the deploy step" → triggers
   - "Encode my workflow as a formula" → triggers
   - "Run the code-review formula" → does NOT trigger
   - "Show me what formulas are available" → does NOT trigger
   - "Edit the overlay for mol-polecat-work" → does NOT trigger
2. **Fewer tool calls than baseline:** direct generation from known schema in
   2–3 tool calls (write + validate) instead of source-reading and trial-error.
3. **Zero failed tool/MCP calls per run:** generated TOML parses on first
   `--dry-run`.
4. **Correct by construction:** generated formulas pass `parser.go` unmodified.

## Required capabilities

All built-in — no MCP tools required:

- **File creation/editing:** write `.formula.toml` files.
- **Code execution (Bash):** run `scripts/validate-formula.py`,
  `gt formula run --dry-run`, `gt formula show`, `gt formula list`,
  `gt formula create`.
- **File reading:** read existing formulas and `internal/formula/types.go` /
  `parser.go` for schema/validation reference.

Embedded knowledge (no external tool needed): the four-type TOML schema, DAG
rules, variable patterns, search-path resolution, naming conventions, and the
two template syntaxes (Handlebars `{{var}}` for workflows, Go `{{.var}}` for
convoys).

## Trigger test (gu-wfs-btzue, 2026-06-06)

The description was tested empirically: fresh-context classifier agents were
given only the competing skill descriptions (this skill plus the adjacent
`crew-sling-work` / `crew-merge-queue` / `crew-commit`) and a suite of candidate
prompts, then asked which single skill each prompt routes to.

**Suite — should trigger:** create/write/new formula; "formula for X";
validate/fix formula TOML; add a step; encode a multi-step workflow;
multi-leg convoy; reorder/edit steps or vars in an existing formula file.
**Should NOT trigger:** run an existing formula (`gt formula run`); list
formulas; edit a per-molecule overlay (`gt formula overlay edit`); sling/dispatch
or "kick off" a formula (→ `crew-sling-work`); generic non-Gas-Town TOML;
unrelated coding; "what's the weather".

**Results:** Round 1 (obvious phrasings) 14/14. Round 2 (hard paraphrases with
no exact keywords) surfaced one under-trigger: *"tweak the variables block in my
convoy formula so the leg prompts resolve"* was misrouted to the overlay
exclusion. **Fix:** added the trigger *"edit the steps/vars/prompts in formula Y"*
and clarified that editing a formula file's own contents is in scope while only
**per-molecule overlay** editing is excluded. Round 3 (re-test) 6/6, with the
overlay-vs-file-contents boundary now disambiguated correctly. Meets the ~90%
trigger-accuracy target.

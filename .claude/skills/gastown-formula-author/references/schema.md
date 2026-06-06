# Formula TOML schema reference

Complete field reference for all four formula types, derived from
`internal/formula/types.go`. SKILL.md covers the common case; consult this when
you need a field that isn't in the quick examples, or to confirm exact spelling.

## Top-level fields (all types)

| Field | TOML key | Notes |
|---|---|---|
| Name | `formula` | **Required.** Must equal the filename stem (`<stem>.formula.toml`). |
| Description | `description` | One paragraph describing the workflow. |
| Type | `type` | One of `convoy`, `workflow`, `expansion`, `aspect`. Inferred from content if omitted (see below). |
| Version | `version` | Integer, e.g. `1`. |
| Pour | `pour` | bool. If true, steps materialize as sub-wisps with checkpoint recovery. Default false. |
| Agent | `agent` | Default agent/runtime for all legs/steps (e.g. `gemini`, `codex`). |
| ReviewOnly | `review_only` | bool. If true, all legs are analysis-only — no code commits expected. |

### Type inference (parser.go `inferType`)

When `type` is omitted, the parser infers it from which arrays are present,
in this priority order:

1. `extends` present → `workflow`
2. `steps` present → `workflow`
3. `legs` present → `convoy`
4. `template` present → `expansion`
5. `aspects` present → `aspect`

Always set `type` explicitly — inference is a fallback, not a contract.

## Workflow type

```toml
[[steps]]
id = "implement"            # required, unique
title = "Implement {{feature}}"
description = "..."
needs = ["design"]          # step IDs that must finish first; must form a DAG
target = "myrig"            # optional gt sling target; defaults to formula target rig
parallel = false           # if true, runs concurrently with other parallel steps sharing the same needs
interactive = false         # if true, runs in the current session (user dialog) instead of dispatching to a polecat
acceptance = "Tests pass."  # exit criteria (used by Ralph loop mode)
wisp_ttl = "15m"            # TTL for ephemeral beads this step creates: "", "inherit", or a Go duration
consumer_bead_id = "gu-x"   # declared consumer for ephemeral beads (alternative to wisp_ttl)
```

**Lifecycle metadata (GUPP, gu-hhqk):** steps that create ephemeral beads
(wisps, HANDOFF messages, patrol reports) should declare either
`consumer_bead_id` or a bounded `wisp_ttl`. These are descriptive — the
reaper uses its own TTL config — but they make the policy auditable.

### Composition (workflow only)

```toml
extends = ["parent-formula"]   # inherit steps from parent formulas after Resolve()

[compose]
[[compose.expand]]
target = "build"               # step ID in this formula to replace
with = "build-expansion"       # expansion formula whose template steps replace it
```

A workflow with `extends` may legally have zero `[[steps]]` — steps come from
the parents.

## Convoy type

```toml
[[legs]]
id = "security"               # required, unique
title = "Security Review"
focus = "Vulnerabilities and attack surface"
description = "..."
agent = "codex"               # per-leg agent override
review_only = true            # analysis-only leg

[synthesis]
title = "Synthesis"
description = "Merge findings from {{.output.directory}}."   # Go text/template syntax
depends_on = ["security"]     # must reference real leg IDs

[inputs.pr]
description = "PR number"
type = "int"
required = true
required_unless = ["branch"]  # must reference other input keys

[output]
directory = "reviews/{{.pr}}"
leg_pattern = "leg-{{.id}}.md"
synthesis = "summary.md"

[prompts]
intro = "Reviewing PR {{.pr}}"  # Go text/template syntax ({{.var}})
```

**Convoy template syntax is Go `text/template` (`{{.var}}`)**, distinct from
workflow Handlebars (`{{var}}`). See SKILL.md "Critical".

## Expansion type

Template steps that replace a single target step in a workflow (via
`[compose.expand]`).

```toml
[[template]]
id = "lint"                   # required, unique
title = "Lint"
description = "..."
needs = ["fetch"]             # references other template IDs; must form a DAG
acceptance = "No lint errors."
wisp_ttl = "inherit"          # propagated to the generated Step
consumer_bead_id = ""         # propagated to the generated Step
```

## Aspect type

Cross-cutting parallel analysis (like convoy but analysis-only).

```toml
[[aspects]]
id = "perf"                   # required, unique
title = "Performance"
focus = "Hot paths and allocations"
description = "..."
```

## Variables (`[vars]`, workflow)

Two equivalent forms. Any `{{var}}` in a workflow step must be declared here or
`bd mol wisp` / `gt formula run` fails with "missing required variables".

```toml
[vars]
assignee = "default-owner"     # shorthand string = the default value

[vars.feature]                 # full table form
description = "The feature being implemented"
required = true
default = ""                   # use default="" for computed/optional vars
```

`Var` decodes from either a plain string (treated as `default`) or a full
table with `description` / `required` / `default`.

Handlebars control words (`else`, `this`, `range`, `with`, `end`, `if`, `each`,
`unless`, block helpers `{{#...}}`/`{{/...}}`) are NOT variables and need no
declaration.

## Sources

- `internal/formula/types.go` — struct definitions and TOML tags — accessed 2026-06-06
- `internal/formula/parser.go` — `Validate`, `inferType`, `checkCycles` — accessed 2026-06-06

# Formula validation troubleshooting

Errors surfaced by `scripts/validate-formula.py` (offline) and
`gt formula run <name> --dry-run --rig <rig>` (authoritative parser), with
cause and fix. The error strings come from `internal/formula/parser.go`.

| Error | Cause | Fix |
|---|---|---|
| `formula field is required` | Missing top-level `formula = "..."` | Add it; match the filename stem |
| `invalid formula type "X"` | `type` not one of convoy/workflow/expansion/aspect | Use a valid type (or rely on inference — see schema.md) |
| `workflow formula requires at least one step` | No `[[steps]]` blocks and no `extends` | Add at least one step (or set `extends`) |
| `step missing required id field` | A `[[steps]]` block has no `id` | Add a unique `id` |
| `duplicate step id: X` | Two steps share an `id` | Make IDs unique |
| `step "X" needs unknown step: Y` | `needs` points at a non-existent ID (often a typo) | Correct the referenced ID |
| `cycle detected involving: X` | Steps depend on each other in a loop | Break the cycle; `needs` must form a DAG |
| `step "X": invalid wisp_ttl "Y"` | `wisp_ttl` isn't `""`, `"inherit"`, or a Go duration | Use a Go duration like `"15m"`, `"2h30m"` |
| `convoy formula requires at least one leg` | Convoy with no `[[legs]]` | Add legs |
| `leg missing required id field` | A `[[legs]]` block has no `id` | Add a unique `id` |
| `duplicate leg id: X` | Two legs share an `id` | Make IDs unique |
| `synthesis depends_on references unknown leg: X` | `[synthesis].depends_on` names a missing leg | Fix the leg ID |
| `input "X" has required_unless referencing unknown input "Y"` | `required_unless` names a non-existent input | Point it at a real `[inputs.*]` key |
| `expansion formula requires at least one template` | Expansion with no `[[template]]` | Add a template block |
| `template "X" needs unknown template: Y` | Template `needs` points at a missing template ID | Correct the referenced ID |
| `aspect formula requires at least one aspect` | Aspect formula with no `[[aspects]]` | Add an aspect |
| `duplicate aspect id: X` | Two aspects share an `id` | Make IDs unique |
| `missing required variables` (at `gt formula run`) | A `{{var}}` is used but not in `[vars]` | Declare it in `[vars]` (use `default=""` for computed vars) |
| `formula "X" not found in search paths` | File misnamed or in wrong dir | Name it `X.formula.toml` in a search-path dir (SKILL.md Step 7) |
| `gt formula show` looks fine but `--dry-run` fails | `show` does not validate | Trust `--dry-run` and `validate-formula.py`, not `show` |

## Two validators, two purposes

- **`scripts/validate-formula.py`** — offline, no rig needed. Mirrors the parser
  checks and additionally catches undeclared `{{var}}` usage and
  filename/stem mismatch before you ever dispatch. Use it in the edit loop.
- **`gt formula run <name> --dry-run --rig <rig>`** — the authoritative parser
  (`parser.go`) plus a real dispatch preview. Use it as the final gate.

When they disagree, the Go parser wins — file a bug against the script.

## Sources

- `internal/formula/parser.go` — error strings and validation order — accessed 2026-06-06

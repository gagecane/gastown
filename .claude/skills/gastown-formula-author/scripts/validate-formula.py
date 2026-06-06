#!/usr/bin/env python3
"""Offline structural validator for Gas Town formula TOML files.

Mirrors the checks in internal/formula/parser.go so a formula can be validated
without a live rig. This is the deterministic gate the skill leans on; prose
about "make sure the needs resolve" is not trustworthy, code is.

It checks, per formula type (inferred when `type` is absent, exactly like
parser.go inferType):
  - required `formula` field and a valid `type`
  - workflow:   >=1 step, unique step ids, every `needs` resolves, no cycles,
                valid `wisp_ttl`, and every {{var}} is declared in [vars]
  - convoy:     >=1 leg, unique leg ids, synthesis.depends_on resolves,
                input.required_unless references real inputs
  - expansion:  >=1 template, unique ids, every `needs` resolves, no cycles,
                valid `wisp_ttl`
  - aspect:     >=1 aspect, unique aspect ids
  - filename:   <stem>.formula.toml and inner `formula = "<stem>"` match

Usage:
    python3 scripts/validate-formula.py <path/to/name.formula.toml> [more...]

Exit codes:
    0  all files valid
    1  one or more validation errors
    2  bad invocation / file unreadable / TOML parse error

NOTE: This validates STRUCTURE only. It does not pour or dispatch. For the
authoritative parser check plus a dispatch preview, also run:
    gt formula run <name> --dry-run --rig <rig>
"""

import os
import re
import sys
import tomllib

VALID_TYPES = {"convoy", "workflow", "expansion", "aspect"}

# Handlebars control words that are not variables. Mirrors isHandlebarsKeyword
# in internal/formula/variable_validation.go (source of truth). Anything in a
# {{...}} that starts with one of these, or with `/` (close) or `#` (open
# block), or `.` (Go-template convoy syntax), is not a [vars] reference.
HANDLEBARS_CONTROL = {"else", "this", "root", "index", "key", "first", "last",
                      "end", "range", "with", "block", "define", "template", "nil"}

# Go time.ParseDuration accepts a sequence of unit groups, e.g. "2h30m" or
# "1.5h" — not just a single unit. Mirror that so the offline check matches the
# real parser (internal/formula/parser.go validateWispTTL).
DURATION_RE = re.compile(r"^[-+]?((\d+(\.\d+)?|\.\d+|\d+\.)(ns|us|µs|μs|ms|s|m|h))+$")


def infer_type(f):
    """Replicate parser.go inferType()."""
    t = f.get("type", "")
    if t:
        return t
    if f.get("extends"):
        return "workflow"
    if f.get("steps"):
        return "workflow"
    if f.get("legs"):
        return "convoy"
    if f.get("template"):
        return "expansion"
    if f.get("aspects"):
        return "aspect"
    return ""


def valid_wisp_ttl(ttl):
    if ttl in ("", "inherit"):
        return True
    return bool(DURATION_RE.match(ttl))


def find_handlebars_vars(text):
    """Return the set of plain {{var}} names used (excludes {{.x}} Go-template
    and {{#block}}/{{/block}} control + control words)."""
    out = set()
    for m in re.finditer(r"\{\{\s*([^}]+?)\s*\}\}", text):
        token = m.group(1).strip()
        if not token:
            continue
        if token[0] in "#/.":  # block open/close, or Go-template dot syntax
            continue
        head = token.split()[0].split(".")[0]
        if head in HANDLEBARS_CONTROL:
            continue
        if not re.match(r"^[A-Za-z_][A-Za-z0-9_]*$", head):
            continue
        out.add(head)
    return out


def collect_text(*values):
    """Flatten strings out of nested formula values for var scanning."""
    parts = []
    for v in values:
        if isinstance(v, str):
            parts.append(v)
        elif isinstance(v, list):
            for item in v:
                parts.append(collect_text(item))
        elif isinstance(v, dict):
            for item in v.values():
                parts.append(collect_text(item))
    return "\n".join(parts)


def check_cycles(deps, kind):
    """deps: {id: [needs...]}. Returns error string or None."""
    visited, in_stack = set(), set()

    def visit(node, path):
        if node in in_stack:
            return f"cycle detected involving: {node}"
        if node in visited:
            return None
        visited.add(node)
        in_stack.add(node)
        for dep in deps.get(node, []):
            err = visit(dep, path + [node])
            if err:
                return err
        in_stack.discard(node)
        return None

    for node in sorted(deps):
        err = visit(node, [])
        if err:
            return err
    return None


def validate_ided_steps(items, label, errors):
    """Validate a list of step/template dicts: unique ids, resolvable needs."""
    seen = set()
    for it in items:
        sid = it.get("id", "")
        if not sid:
            errors.append(f"{label} missing required id field")
            continue
        if sid in seen:
            errors.append(f"duplicate {label} id: {sid}")
        seen.add(sid)
    for it in items:
        for need in it.get("needs", []):
            if need not in seen:
                errors.append(f'{label} "{it.get("id")}" needs unknown {label}: {need}')
    for it in items:
        if not valid_wisp_ttl(it.get("wisp_ttl", "")):
            errors.append(f'{label} "{it.get("id")}": invalid wisp_ttl '
                          f'"{it.get("wisp_ttl")}" (use "", "inherit", or a Go duration like "15m")')
    deps = {it.get("id"): it.get("needs", []) for it in items if it.get("id")}
    cyc = check_cycles(deps, label)
    if cyc:
        errors.append(cyc)
    return seen


def validate_formula(path):
    errors = []
    base = os.path.basename(path)
    if not base.endswith(".formula.toml"):
        errors.append(f'filename must be "<name>.formula.toml", got "{base}"')
        stem = None
    else:
        stem = base[: -len(".formula.toml")]

    try:
        with open(path, "rb") as fh:
            f = tomllib.load(fh)
    except FileNotFoundError:
        return [f"file not found: {path}"], 2
    except tomllib.TOMLDecodeError as e:
        return [f"TOML parse error: {e}"], 2

    name = f.get("formula", "")
    if not name:
        errors.append("formula field is required")
    elif stem is not None and name != stem:
        errors.append(f'inner formula = "{name}" must match filename stem "{stem}"')

    ftype = infer_type(f)
    if ftype not in VALID_TYPES:
        errors.append(f'invalid formula type "{ftype}" '
                      f"(must be convoy, workflow, expansion, or aspect)")
        return errors, 1

    if ftype == "workflow":
        steps = f.get("steps", [])
        if not steps and not f.get("extends"):
            errors.append("workflow formula requires at least one step")
        validate_ided_steps(steps, "step", errors)
        # Undeclared variable check (not enforced by parser.go, but fails at
        # `gt formula run` with "missing required variables" — catch it early).
        declared = set(f.get("vars", {}).keys())
        used = find_handlebars_vars(collect_text(steps))
        for var in sorted(used - declared):
            errors.append(f'variable "{{{{{var}}}}}" used in a step but not declared in [vars]')

    elif ftype == "convoy":
        legs = f.get("legs", [])
        if not legs:
            errors.append("convoy formula requires at least one leg")
        leg_ids = set()
        for leg in legs:
            lid = leg.get("id", "")
            if not lid:
                errors.append("leg missing required id field")
                continue
            if lid in leg_ids:
                errors.append(f"duplicate leg id: {lid}")
            leg_ids.add(lid)
        synth = f.get("synthesis")
        if isinstance(synth, dict):
            for dep in synth.get("depends_on", []):
                if dep not in leg_ids:
                    errors.append(f"synthesis depends_on references unknown leg: {dep}")
        for iname, inp in f.get("inputs", {}).items():
            for ref in inp.get("required_unless", []):
                if ref not in f.get("inputs", {}):
                    errors.append(f'input "{iname}" has required_unless '
                                  f'referencing unknown input "{ref}"')

    elif ftype == "expansion":
        tmpls = f.get("template", [])
        if not tmpls:
            errors.append("expansion formula requires at least one template")
        validate_ided_steps(tmpls, "template", errors)

    elif ftype == "aspect":
        aspects = f.get("aspects", [])
        if not aspects:
            errors.append("aspect formula requires at least one aspect")
        aids = set()
        for a in aspects:
            aid = a.get("id", "")
            if not aid:
                errors.append("aspect missing required id field")
                continue
            if aid in aids:
                errors.append(f"duplicate aspect id: {aid}")
            aids.add(aid)

    return errors, (1 if errors else 0)


def main(argv):
    if len(argv) < 2:
        print(__doc__.strip().splitlines()[0])
        print("usage: validate-formula.py <name.formula.toml> [more...]", file=sys.stderr)
        return 2
    worst = 0
    for path in argv[1:]:
        errors, code = validate_formula(path)
        worst = max(worst, code)
        if errors:
            print(f"✗ {path}")
            for e in errors:
                print(f"    {e}")
        else:
            print(f"✓ {path}")
    return worst


if __name__ == "__main__":
    sys.exit(main(sys.argv))

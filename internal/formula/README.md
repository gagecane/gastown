# Formula Package

TOML-based workflow definitions with validation, cycle detection, and execution planning.

## Overview

The formula package parses and validates structured workflow definitions, enabling:

- **Type inference** - Automatically detect formula type from content
- **Validation** - Check required fields, unique IDs, valid references
- **Cycle detection** - Prevent circular dependencies
- **Topological sorting** - Compute dependency-ordered execution
- **Ready computation** - Find steps with satisfied dependencies

## Installation

```go
import "github.com/steveyegge/gastown/internal/formula"
```

## Quick Start

```go
// Parse a formula file
f, err := formula.ParseFile("workflow.formula.toml")
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Formula: %s (type: %s)\n", f.Name, f.Type)

// Get execution order
order, _ := f.TopologicalSort()
fmt.Printf("Execution order: %v\n", order)

// Track and execute
completed := make(map[string]bool)
for len(completed) < len(order) {
    ready := f.ReadySteps(completed)
    // Execute ready steps (can be parallel)
    for _, id := range ready {
        step := f.GetStep(id)
        fmt.Printf("Executing: %s\n", step.Title)
        completed[id] = true
    }
}
```

## Formula Types

### Workflow

Sequential steps with explicit dependencies. Steps execute when all `needs` are satisfied.

```toml
formula = "release"
description = "Standard release process"
type = "workflow"

[vars.version]
description = "Version to release"
required = true

[[steps]]
id = "test"
title = "Run Tests"
description = "Execute test suite"

[[steps]]
id = "build"
title = "Build Artifacts"
needs = ["test"]

[[steps]]
id = "publish"
title = "Publish Release"
needs = ["build"]
```

### Convoy

Parallel legs that execute independently, with optional synthesis.

```toml
formula = "security-scan"
type = "convoy"

[[legs]]
id = "sast"
title = "Static Analysis"
focus = "Code vulnerabilities"

[[legs]]
id = "deps"
title = "Dependency Audit"
focus = "Vulnerable packages"

[[legs]]
id = "secrets"
title = "Secret Detection"
focus = "Leaked credentials"

[synthesis]
title = "Security Report"
description = "Combine all findings"
depends_on = ["sast", "deps", "secrets"]
```

### Expansion

Template-based formulas for parameterized workflows.

```toml
formula = "component-review"
type = "expansion"

[[template]]
id = "analyze"
title = "Analyze {{component}}"

[[template]]
id = "test"
title = "Test {{component}}"
needs = ["analyze"]
```

### Aspect

Multi-aspect parallel analysis (similar to convoy).

```toml
formula = "code-review"
type = "aspect"

[[aspects]]
id = "security"
title = "Security Review"
focus = "OWASP Top 10"

[[aspects]]
id = "performance"
title = "Performance Review"
focus = "Complexity and bottlenecks"

[[aspects]]
id = "maintainability"
title = "Maintainability Review"
focus = "Code clarity and documentation"
```

## Step Lifecycle Annotations

Workflow steps that create ephemeral beads (wisps, HANDOFF mail, patrol
reports) can declare their cleanup policy using two optional TOML fields on
`[[steps]]` (and `[[template]]`) blocks:

| Field | Purpose |
|-------|---------|
| `wisp_ttl` | Maximum lifetime for ephemeral beads this step creates. Go duration (`"15m"`, `"2h"`), `"inherit"` (use reaper default), or empty (step creates no ephemeral beads). |
| `consumer_bead_id` | Identifier or description of the bead/step that consumes (closes) what this step creates. May include formula `{{var}}` substitutions. Empty means TTL-bounded cleanup only. |

These fields are **descriptive metadata today** (GUPP / gu-hhqk AC6). The
reaper uses its own TTL configuration for the actual sweep. Declaring the
policy on the formula side makes it discoverable, audit-friendly, and
positions the data for future enforcement.

Parse-time validation rejects malformed `wisp_ttl` values (e.g. `"15 minutes"`,
`"soonish"`) so typos fail fast rather than silently.

```toml
[[steps]]
id = "loop-or-exit"
title = "Loop or exit for respawn"
needs = ["context-check"]
# Wisp created here lives 30m if respawn never consumes it.
wisp_ttl = "30m"
consumer_bead_id = "next patrol cycle (self-respawn)"
```

## API Reference

### Parsing

```go
// Parse from file
f, err := formula.ParseFile("path/to/formula.toml")

// Parse from bytes
f, err := formula.Parse([]byte(tomlContent))
```

### Validation

Validation is automatic during parsing. Errors are descriptive:

```go
f, err := formula.Parse(data)
// Possible errors:
// - "formula field is required"
// - "invalid formula type \"foo\""
// - "duplicate step id: build"
// - "step \"deploy\" needs unknown step: missing"
// - "cycle detected involving step: a"
```

### Execution Planning

```go
// Get dependency-sorted order
order, err := f.TopologicalSort()

// Find ready steps given completed set
completed := map[string]bool{"test": true, "lint": true}
ready := f.ReadySteps(completed)

// Lookup individual items
step := f.GetStep("build")
leg := f.GetLeg("sast")
tmpl := f.GetTemplate("analyze")
aspect := f.GetAspect("security")
```

### Dependency Queries

```go
// Get all item IDs
ids := f.GetAllIDs()

// Get dependencies for a specific item
deps := f.GetDependencies("build")  // Returns ["test"]
```

## Embedded Formulas

The package embeds common formulas for Gas Town workflows:

```go
// Provision embedded formulas to a beads workspace
count, err := formula.ProvisionFormulas("/path/to/workspace")

// Check formula health (outdated, modified, etc.)
report, err := formula.CheckFormulaHealth("/path/to/workspace")

// Update formulas safely (preserves user modifications)
updated, skipped, reinstalled, err := formula.UpdateFormulas("/path/to/workspace")
```

## Testing

```bash
go test ./internal/formula/... -v
```

The package has 130% test coverage (1,200 lines of tests for 925 lines of code).

## Dependencies

- `github.com/BurntSushi/toml` - TOML parsing (stable, widely-used)

## License

MIT License - see repository LICENSE file.

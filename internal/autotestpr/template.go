// Package autotestpr ships the conventions-sheet template that the
// auto-test-pr polecat reads at cycle-start, plus the loader exposed via
// the `gt auto-test-pr show-template` and `gt auto-test-pr enable
// --emit-template` CLI verbs.
//
// The template is the source of truth for the constraints the polecat is
// expected to follow when writing tests in an opted-in rig. It is
// embedded into the gt binary so every gt install has a known-good
// baseline; rigs check a per-rig copy in at .gt/auto-test-pr/
// conventions.md and free-form-edit the rig-specific sections.
//
// See .designs/auto-test-pr/synthesis.md (Phase 0 task 2d) for the
// surrounding design context.
package autotestpr

import _ "embed"

//go:embed conventions_template.md
var conventionsTemplate string

// ConventionsTemplate returns the embedded conventions-sheet template
// exactly as it ships with the gt binary. The returned string is the
// canonical bytes of internal/autotestpr/conventions_template.md.
//
// Callers MUST NOT modify the returned string. The CLI verbs
// `gt auto-test-pr show-template` and `gt auto-test-pr enable
// --emit-template` write this content to stdout / a file unchanged.
func ConventionsTemplate() string {
	return conventionsTemplate
}

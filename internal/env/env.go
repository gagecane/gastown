// Package env provides typed accessors for GT_* environment variables.
//
// Background: gastown reads its configuration from 90+ GT_* env vars across
// 297 callsites (audit at HEAD 18da030a). Each callsite re-implements its
// own parsing — the variable name is a string literal, defaults are
// open-coded, and a typo silently no-ops. This package centralizes the
// protocol:
//
//   - Every recognized GT_* var is declared once, as a typed [Var] constant.
//   - Each var is registered with its kind, default, and description.
//   - Callers read it through a typed accessor: [String], [Bool], [Int], [Duration].
//
// The accessors all key off [Var], so a typo is a compile error rather than
// a silent zero value. The registry doubles as a single source of truth for
// the doc generator (see [List]).
//
// Migration policy: this package is the destination. Callsites are migrated
// in their own per-package beads (see Phase 3 of the parent epic) — this
// package adds the accessors without touching existing call sites.
package env

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Var is the name of a GT_* environment variable.
//
// Use a typed name (rather than a bare string) so accessors can distinguish
// "registered var" from "arbitrary string", and so typos in callers are
// caught at compile time.
type Var string

// Name returns the underlying environment variable name (e.g. "GT_DOLT_PORT").
func (v Var) Name() string { return string(v) }

// Kind classifies how a Var is interpreted.
type Kind int

const (
	// KindString is a free-form string value.
	KindString Kind = iota
	// KindBool is a boolean. See [Bool] for the accepted forms.
	KindBool
	// KindInt is a base-10 integer.
	KindInt
	// KindDuration is a Go duration string (e.g. "5s", "100ms", "2h30m").
	KindDuration
)

func (k Kind) String() string {
	switch k {
	case KindString:
		return "string"
	case KindBool:
		return "bool"
	case KindInt:
		return "int"
	case KindDuration:
		return "duration"
	default:
		return "unknown"
	}
}

// Spec describes a registered environment variable.
type Spec struct {
	Var     Var    // the variable name, e.g. "GT_DOLT_PORT"
	Kind    Kind   // expected value kind
	Default string // human-readable default; empty means "no default / unset means absent"
	Desc    string // one-line description for doc output
}

// registry is the authoritative inventory of recognized GT_* vars. Keyed by
// Var so duplicate registrations are caught (see [Register]).
var registry = map[Var]Spec{}

// Register adds spec to the registry. It panics on duplicate registration —
// the registry is a static manifest, not a runtime configuration store, so
// a duplicate signals a programming error worth surfacing immediately.
func Register(spec Spec) {
	if spec.Var == "" {
		panic("env: Register called with empty Var")
	}
	if _, dup := registry[spec.Var]; dup {
		panic("env: duplicate registration for " + spec.Var.Name())
	}
	registry[spec.Var] = spec
}

// Lookup returns the spec for v and whether it was registered.
func Lookup(v Var) (Spec, bool) {
	s, ok := registry[v]
	return s, ok
}

// List returns every registered Spec, sorted by Var name. Used by the doc
// generator (see internal/env/cmd/envdoc).
func List() []Spec {
	out := make([]Spec, 0, len(registry))
	for _, s := range registry {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Var < out[j].Var })
	return out
}

// Reset clears the registry. Test helper — production code never calls this.
func Reset() { registry = map[Var]Spec{} }

// String returns the raw value of v. Empty string means unset (or
// explicitly empty — POSIX environments do not distinguish the two).
//
// String is the lowest-level accessor; the typed accessors below use it
// internally. If your callsite needs presence detection rather than
// truthiness, prefer `String(v) != ""` over [Bool].
func String(v Var) string { return os.Getenv(v.Name()) }

// Bool returns true iff v is set to a truthy value: "true", "1", "yes",
// "on", "t", "y" (case-insensitive, leading/trailing whitespace trimmed).
// Any other value — including empty/unset — returns false.
//
// This deliberately picks ONE truthiness convention. Existing callsites
// use a mix of `== "true"`, `== "1"`, and `!= ""` — Phase 3 migration
// polecats decide per-site whether the new convention preserves behavior;
// presence-only checks should migrate to `String(v) != ""` instead.
func Bool(v Var) bool {
	s := strings.ToLower(strings.TrimSpace(os.Getenv(v.Name())))
	switch s {
	case "true", "1", "yes", "on", "t", "y":
		return true
	}
	return false
}

// Int returns v parsed as a base-10 integer, or def if v is unset or
// unparseable. Use this for numeric configuration where you want a fallback.
func Int(v Var, def int) int {
	s := os.Getenv(v.Name())
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

// Duration returns v parsed as a Go duration (see [time.ParseDuration]),
// or def if v is unset or unparseable.
//
// Note on migration: several existing callsites parse integer seconds via
// strconv.Atoi (e.g. "GT_DOLT_WAIT_TIMEOUT=300"). Those callers should
// migrate to `time.Duration(env.Int(v, defSec)) * time.Second` to preserve
// integer-seconds semantics, or operators should switch to Go duration
// strings ("300s"). Duration here is strict ParseDuration — it will not
// silently accept bare integers.
func Duration(v Var, def time.Duration) time.Duration {
	s := strings.TrimSpace(os.Getenv(v.Name()))
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

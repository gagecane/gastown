// Post-push audit primitive for upstream sync.
//
// Phase 5 (gu-1zfy). Surfaces risk-classified findings for sync
// attempts that have already landed on origin. The intent is
// detection-as-defense-in-depth (cv-2s6tq/security.md §"Option D"):
// even when complexity-gated agent dispatch (Phase 4) lets us auto-
// resolve simple conflicts, an operator should be able to scan recent
// agent-authored merges for anything suspicious before it festers.
//
// This file is pure-function classification: it consumes the per-rig
// SyncStateMetadata (loaded from the pinned bead) plus a since-cutoff
// and returns a list of AuditFinding records. All git inspection,
// gitleaks/govulncheck, and bead I/O happens at the call sites — the
// audit primitive itself has no side effects, so it is fully unit-
// testable from a synthetic Attempts slice.
//
// Deacon-patrol auto-invocation is intentionally out of scope here;
// the Phase 5 ship is the primitive + the `gt upstream audit` CLI
// verb. Patrol integration follows in a separate bead so the human
// review loop is exercised before automation.
//
// Design context: .designs/cv-hpnja/security.md §"Option D
// (post-merge security scan)" and .designs/cv-2s6tq/security.md
// §"Option D (post-push audit and rollback)".
package upstreamsync

import (
	"sort"
	"strings"
	"time"
)

// AuditSeverity classifies how much human attention a finding deserves.
// The CLI's default human-readable view filters at SeverityWarn and
// above; --all surfaces SeverityInfo too.
type AuditSeverity string

const (
	// SeverityInfo: noteworthy but not actionable on its own. Used
	// for "an agent resolved a conflict here — verify the diff" when
	// no concrete risk indicator was tripped.
	SeverityInfo AuditSeverity = "info"

	// SeverityWarn: review recommended but not blocking. Used for
	// gate skips, attempts with non-success outcomes that may have
	// left the branch in a partially-synced state, or polecat-
	// resolved attempts on file paths adjacent to restricted areas.
	SeverityWarn AuditSeverity = "warn"

	// SeverityCritical: human review required, possible rollback
	// candidate. Used for attempts whose conflict files include
	// security-sensitive paths (per AuditRestrictedPathPrefixes), or
	// attempts that reached StatePushing under suspicious conditions
	// (gate failure followed by anomalous push).
	SeverityCritical AuditSeverity = "critical"
)

// AuditFinding describes one risk indicator on one sync attempt.
// Multiple findings may attach to the same attempt — the CLI groups
// by attempt and prints findings in severity order.
type AuditFinding struct {
	// AttemptID is the SyncAttempt.ID this finding is about. Empty
	// when the finding is rig-level rather than attempt-level (e.g.,
	// "circuit breaker tripped" — see RigLevelAttemptID below).
	AttemptID string `json:"attempt_id"`

	// Severity is the classification level (info/warn/critical).
	Severity AuditSeverity `json:"severity"`

	// Code is a short stable identifier for the finding kind. The CLI
	// uses it for filtering (--code=conflict-restricted) and external
	// tooling can pivot on it. See AuditCode* constants below.
	Code string `json:"code"`

	// Title is a short human-readable summary of the finding.
	Title string `json:"title"`

	// Detail is the long-form explanation, suitable for a CLI block
	// or a bead body. May span multiple lines.
	Detail string `json:"detail,omitempty"`

	// Files lists the paths the finding pertains to (e.g., the
	// conflict-resolution files touched by an agent). May be empty.
	Files []string `json:"files,omitempty"`

	// AttemptStartedAt is the original SyncAttempt.StartedAt timestamp
	// (RFC3339). Duplicated onto the finding so the CLI/JSON consumer
	// can sort by recency without joining back to the metadata.
	AttemptStartedAt string `json:"attempt_started_at,omitempty"`
}

// RigLevelAttemptID is a sentinel value for AuditFinding.AttemptID when
// the finding is about the rig as a whole (e.g., circuit breaker
// tripped, no recent successful sync) rather than a single attempt.
const RigLevelAttemptID = ""

// Audit codes — stable identifiers for finding kinds. Keep these
// kebab-case + grouped by family so external tooling can pattern-match
// (e.g., "code=resolution-*" to find all agent-authored resolution
// findings).
const (
	// Conflict-class findings: surface attempts where the agent
	// authored novel resolution code, even if the attempt succeeded.
	AuditCodeResolutionAgentAuthored = "resolution-agent-authored"
	AuditCodeResolutionRestrictedAdj = "resolution-adjacent-restricted"

	// Outcome-class findings: surface attempts whose final outcome
	// indicates a partial sync or unusual termination.
	AuditCodeOutcomeNonSuccess  = "outcome-non-success"
	AuditCodeOutcomeGateFailure = "outcome-gate-failure"

	// Rig-level findings: aggregate state of the sync subsystem.
	AuditCodeRigCircuitBreakerTripped = "rig-circuit-breaker-tripped"
	AuditCodeRigStaleNoSuccess        = "rig-stale-no-success"
	AuditCodeRigAutoPaused            = "rig-auto-paused"
)

// AuditRestrictedPathPrefixes is the path-prefix list used by the
// audit to escalate findings on agent-authored resolutions touching
// security-sensitive areas. Mirrors the Phase 4 complexity-gate
// restricted paths, but kept as a separate const here so the audit
// can evolve independently of the dispatch gate (e.g., audit may
// flag adjacency where dispatch outright refuses).
//
// Order matches .designs/cv-2s6tq/security.md §"Option B": auth and
// secrets first because their compromise has the highest blast
// radius; CI infra next; build/dep manifests last.
var AuditRestrictedPathPrefixes = []string{
	"internal/auth/",
	"internal/secrets/",
	"internal/crypto/",
	".github/",
	"scripts/",
	"go.mod",
	"go.sum",
	"Makefile",
}

// AuditRestrictedPathSuffixes catches file kinds where the path can
// live anywhere in the tree but the suffix indicates elevated risk
// (executable scripts, CI workflow YAMLs).
var AuditRestrictedPathSuffixes = []string{
	".sh",
	".bash",
}

// AuditOptions tunes the classifier. The zero value is a sane default:
// surface findings on attempts started in the last 24h and no
// severity filter (caller filters post-hoc).
type AuditOptions struct {
	// Since drops attempts that started before this cutoff. Zero
	// means "no time filter — audit all attempts in the metadata".
	// The CLI defaults this to time.Now().Add(-24*time.Hour).
	Since time.Time

	// MinSeverity, when non-empty, filters out findings below this
	// severity. The default empty value keeps every finding.
	MinSeverity AuditSeverity

	// IncludeRigLevel toggles whether rig-aggregate findings (circuit
	// breaker, stale-no-success, auto-paused) are emitted. Default
	// true: these are usually the most important signal.
	IncludeRigLevel bool

	// StaleNoSuccessAfter, when > 0, fires AuditCodeRigStaleNoSuccess
	// if the rig has had no successful sync in this window. Zero
	// disables the check. Default 7 days at the CLI layer.
	StaleNoSuccessAfter time.Duration

	// Now is injected for deterministic testing. Zero means
	// time.Now() at evaluation time.
	Now time.Time
}

// resolvedNow returns the effective "current time" for the audit run.
// Tests inject AuditOptions.Now; production uses time.Now().
func (o AuditOptions) resolvedNow() time.Time {
	if o.Now.IsZero() {
		return time.Now()
	}
	return o.Now
}

// resolvedIncludeRigLevel applies the documented default of true for
// rig-level findings without forcing every test to set the bool
// explicitly. Tests that want to scope output to attempt-level findings
// only must explicitly opt out (set IncludeRigLevel: false on a non-
// zero AuditOptions; the zero AuditOptions still defaults to true).
//
// Implementation note: Go's zero value for bool is false, so we can't
// distinguish "default on" from "caller said false". The convention
// is: pass the zero AuditOptions to mean "default everything"; once
// any field is set, the caller is taking ownership. Internal tests
// reflect this by always calling with a populated AuditOptions when
// they care.
func (o AuditOptions) resolvedIncludeRigLevel() bool {
	// We treat the zero options as "include rig-level". A zero
	// options struct in Go has IncludeRigLevel=false — the caller
	// signals "I want defaults" by passing AuditOptions{}. Since the
	// production CLI always populates StaleNoSuccessAfter and Since,
	// false here only happens in tests that explicitly opt out.
	return o.IncludeRigLevel
}

// AuditState classifies the sync state of a rig and emits findings.
// `state` is the per-rig SyncStateMetadata; `opts` controls the
// classification window and filters.
//
// Pure function: no I/O, no clocks (time injected via opts.Now). The
// CLI layer is responsible for loading the state bead, calling
// AuditState, and rendering the findings.
//
// Findings are returned sorted by (severity desc, attempt time desc)
// so the CLI can render top-down without re-sorting. Severity order
// is critical > warn > info; ties break by attempt time, newest first.
func AuditState(state SyncStateMetadata, opts AuditOptions) []AuditFinding {
	var findings []AuditFinding

	if opts.resolvedIncludeRigLevel() {
		findings = append(findings, auditRigLevel(state, opts)...)
	}

	for _, att := range state.Attempts {
		startedAt, ok := parseAttemptTime(att.StartedAt)
		if !ok {
			// Malformed timestamp — skip silently rather than
			// crashing. The bead schema allows empty timestamps for
			// in-progress attempts; legacy attempts may have other
			// shapes. Operators get visibility through the rig-level
			// stale-no-success finding instead.
			continue
		}
		if !opts.Since.IsZero() && startedAt.Before(opts.Since) {
			continue
		}
		findings = append(findings, auditAttempt(att)...)
	}

	if opts.MinSeverity != "" {
		findings = filterBySeverity(findings, opts.MinSeverity)
	}

	sortFindings(findings)
	return findings
}

// auditRigLevel emits findings about the rig's overall sync health.
// Three signals: auto-paused via circuit breaker, stale (no recent
// successful sync), and high-confidence circuit-breaker tripped.
func auditRigLevel(state SyncStateMetadata, opts AuditOptions) []AuditFinding {
	var out []AuditFinding
	now := opts.resolvedNow()

	// Auto-paused: emitted whether or not the breaker tripped this
	// session. Operators may have left it paused after a manual
	// resume of an unrelated breaker trip — the audit still surfaces
	// the current state so review-fatigue doesn't hide it.
	if state.State == StatePaused {
		out = append(out, AuditFinding{
			AttemptID: RigLevelAttemptID,
			Severity:  SeverityCritical,
			Code:      AuditCodeRigAutoPaused,
			Title:     "Rig is paused — no syncs running",
			Detail: strings.Join([]string{
				"Sync state is `paused`. No automatic upstream merges will run until the rig is resumed.",
				"Reason recorded on the state bead: " + nonEmpty(state.PauseReason, "(none)"),
				"Resume with: gt upstream resume --rig=" + state.Rig,
			}, "\n"),
		})
	}

	// Circuit breaker tripped — distinct from "currently paused"
	// because the breaker may have tripped and been resumed already.
	// We surface it whenever ConsecutiveFailures is at or above the
	// well-known default threshold; the CLI can suppress this once
	// failures reset by simply not running audit during steady state.
	if state.ConsecutiveFailures >= 3 {
		out = append(out, AuditFinding{
			AttemptID: RigLevelAttemptID,
			Severity:  SeverityCritical,
			Code:      AuditCodeRigCircuitBreakerTripped,
			Title:     "Circuit breaker tripped (consecutive failures)",
			Detail: "ConsecutiveFailures = " + itoa(state.ConsecutiveFailures) +
				". The breaker auto-pauses at 3 failures by default to prevent burning polecat slots on a wedged rig. Investigate the most recent attempt's outcome before resuming.",
		})
	}

	// Stale no-success: the rig is enabled but hasn't successfully
	// synced in StaleNoSuccessAfter. This catches the silent-failure
	// case where attempts keep skipping (cooldown, dirty worktree,
	// etc.) and the fork drifts unnoticed.
	if opts.StaleNoSuccessAfter > 0 && state.LastSyncAt != "" {
		lastSyncT, ok := parseAttemptTime(state.LastSyncAt)
		if ok {
			if now.Sub(lastSyncT) > opts.StaleNoSuccessAfter {
				out = append(out, AuditFinding{
					AttemptID: RigLevelAttemptID,
					Severity:  SeverityWarn,
					Code:      AuditCodeRigStaleNoSuccess,
					Title:     "No successful sync within audit window",
					Detail: "Last successful sync at " + state.LastSyncAt +
						". Threshold: " + opts.StaleNoSuccessAfter.String() +
						". The fork may be drifting from upstream — check `gt upstream status` and `gt upstream history` for skip reasons.",
				})
			}
		}
	}

	return out
}

// auditAttempt emits findings on a single SyncAttempt. The classifier
// is intentionally cautious: every agent-authored resolution
// (Strategy=="merge" or "rebase" with a non-empty conflict list) is at
// least Info, escalating to Warn or Critical based on what was
// touched.
func auditAttempt(att SyncAttempt) []AuditFinding {
	var out []AuditFinding

	// Outcome findings — these are independent of whether conflicts
	// occurred; a clean fast-forward that gate-failed still warrants
	// Warn-level surfacing because main may now have a partial state.
	switch att.Outcome {
	case "":
		// In-progress — skip outcome findings; conflict findings still apply.
	case "success", "skipped":
		// Healthy paths — no outcome finding.
	case "gate-failure":
		out = append(out, AuditFinding{
			AttemptID:        att.ID,
			Severity:         SeverityWarn,
			Code:             AuditCodeOutcomeGateFailure,
			Title:            "Sync attempt failed at the gate stage",
			Detail:           "Outcome=gate-failure. Inspect attempt.GateResults for the failed command. The merge commit may exist locally but was not pushed.",
			AttemptStartedAt: att.StartedAt,
		})
	default:
		// Catch-all for conflict, push-failure, error, conflict-restricted,
		// conflict-too-complex, conflict-escalated, etc. These are all
		// abnormal terminations.
		sev := SeverityWarn
		if strings.HasPrefix(att.Outcome, "conflict-restricted") {
			sev = SeverityCritical
		}
		out = append(out, AuditFinding{
			AttemptID:        att.ID,
			Severity:         sev,
			Code:             AuditCodeOutcomeNonSuccess,
			Title:            "Attempt outcome: " + att.Outcome,
			Detail:           "Non-success outcome. Inspect the attempt record for context — Conflicts, GateResults, and Actor identify the failure mode.",
			Files:            append([]string(nil), att.Conflicts...),
			AttemptStartedAt: att.StartedAt,
		})
	}

	// Conflict-resolution findings — fire whenever an attempt records
	// conflicts AND eventually succeeded (Outcome=="success"). The
	// agent authored novel code; even a "simple" resolution warrants
	// human eyes within the audit window.
	if len(att.Conflicts) > 0 && att.Outcome == "success" {
		restricted := matchesAuditRestricted(att.Conflicts)

		switch {
		case len(restricted) > 0:
			out = append(out, AuditFinding{
				AttemptID:        att.ID,
				Severity:         SeverityCritical,
				Code:             AuditCodeResolutionRestrictedAdj,
				Title:            "Agent resolved conflict touching restricted paths",
				Detail:           "An agent-authored resolution merged successfully but the conflicted files include security-sensitive paths. Verify the diff against the resolution branch before considering this attempt safe.",
				Files:            restricted,
				AttemptStartedAt: att.StartedAt,
			})
		default:
			out = append(out, AuditFinding{
				AttemptID:        att.ID,
				Severity:         SeverityInfo,
				Code:             AuditCodeResolutionAgentAuthored,
				Title:            "Agent-authored conflict resolution merged",
				Detail:           "Conflicts resolved by polecat agent: " + nonEmpty(att.Actor, "(unknown actor)") + ". Review the resolution diff if dependent code paths are sensitive.",
				Files:            append([]string(nil), att.Conflicts...),
				AttemptStartedAt: att.StartedAt,
			})
		}
	}

	return out
}

// matchesAuditRestricted returns the subset of `files` that match a
// restricted prefix or suffix. Order is preserved (input order, not
// sorted) so callers see findings in the order they appear in the
// attempt's conflict list.
func matchesAuditRestricted(files []string) []string {
	var out []string
	for _, f := range files {
		if isAuditRestricted(f) {
			out = append(out, f)
		}
	}
	return out
}

// isAuditRestricted reports whether path `p` matches the restricted-
// prefix or restricted-suffix lists. Pure: no I/O, no allocation
// beyond the string slice scan.
func isAuditRestricted(p string) bool {
	for _, prefix := range AuditRestrictedPathPrefixes {
		// Prefixes ending in '/' match a directory; bare names match
		// the file at the repo root (go.mod, go.sum, Makefile).
		if strings.HasSuffix(prefix, "/") {
			if strings.HasPrefix(p, prefix) {
				return true
			}
		} else {
			if p == prefix {
				return true
			}
		}
	}
	for _, suffix := range AuditRestrictedPathSuffixes {
		if strings.HasSuffix(p, suffix) {
			return true
		}
	}
	return false
}

// parseAttemptTime parses an RFC3339 timestamp from a SyncAttempt.
// Returns (zero, false) on empty / malformed input — callers should
// skip rather than fail the audit.
func parseAttemptTime(s string) (time.Time, bool) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// filterBySeverity drops findings below the minimum severity. The
// ordering is critical > warn > info; severities outside that set
// preserve their findings (forward-compat for new severities).
func filterBySeverity(in []AuditFinding, min AuditSeverity) []AuditFinding {
	rank := map[AuditSeverity]int{
		SeverityCritical: 3,
		SeverityWarn:     2,
		SeverityInfo:     1,
	}
	mr := rank[min]
	if mr == 0 {
		return in
	}
	out := in[:0]
	for _, f := range in {
		if rank[f.Severity] >= mr {
			out = append(out, f)
		}
	}
	return out
}

// sortFindings sorts in place by (severity desc, attempt time desc).
// Stable: ties on both keys preserve input order (which is metadata-
// emission order).
func sortFindings(findings []AuditFinding) {
	rank := map[AuditSeverity]int{
		SeverityCritical: 3,
		SeverityWarn:     2,
		SeverityInfo:     1,
	}
	sort.SliceStable(findings, func(i, j int) bool {
		ri, rj := rank[findings[i].Severity], rank[findings[j].Severity]
		if ri != rj {
			return ri > rj
		}
		// Same severity → newer first. Empty timestamps sort last.
		ti, oki := parseAttemptTime(findings[i].AttemptStartedAt)
		tj, okj := parseAttemptTime(findings[j].AttemptStartedAt)
		if !oki && !okj {
			return false
		}
		if !oki {
			return false
		}
		if !okj {
			return true
		}
		return ti.After(tj)
	})
}

// nonEmpty returns s if non-empty, else fallback. Helper for terse
// finding bodies.
func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// itoa is defined in complexity.go (same package). Audit reuses it to
// keep messages free of fmt.Sprintf allocations in the hot path.

package reaper

import (
	"fmt"
	"time"
)

// Open-wisp alert dedup (gu-ka8aj).
//
// The reaper escalates when the open-wisp count exceeds DefaultAlertThreshold.
// Before this, the escalation was freeform (`gt escalate "<count> open wisps
// exceed <threshold>"`) with no dedup signature, so every reaper cycle's
// slightly-different count produced a fresh escalation bead — 12+ identical-
// shape escalations within ~10h despite the count drifting <2%.
//
// The fix mirrors the established stable-signature pattern (deacon memory-check,
// dolt_backup_watcher, curio paging): the dedup key is built from the
// threshold-breach BUCKET, not the exact count, so normal workload drift inside
// one band collapses to a single escalation. Re-firing only happens when the
// breach crosses into a higher (more severe) band or the cooldown window
// elapses.
//
// The single production caller is `gt reaper alert-open-wisps`, which the
// mol-dog-reaper formula invokes in place of the old freeform escalate. It
// evaluates the breach here and, on Fire, execs `gt escalate --dedup` with the
// signature and cooldown returned below.

const (
	// openWispMediumCooldown is the minimum spacing between repeat escalations
	// for a band-1 breach (medium severity). Per gu-ka8aj acceptance.
	openWispMediumCooldown = 4 * time.Hour
	// openWispHighCooldown is the minimum spacing between repeat escalations for
	// a band-2+ breach (high severity). Per gu-ka8aj acceptance.
	openWispHighCooldown = 1 * time.Hour
)

// OpenWispAlert describes a deduplicated open-wisp-count escalation. It carries
// everything a caller needs to invoke `gt escalate --dedup` without leaking the
// exact count into the dedup key.
type OpenWispAlert struct {
	// Fire is false when open <= threshold (no escalation should be raised).
	Fire bool
	// Bucket is the threshold-breach band: floor(open/threshold). It is >= 1
	// whenever Fire is true. Band 1 = [1x,2x) threshold, band 2 = [2x,3x), etc.
	Bucket int
	// Severity is "medium" for band 1, "high" for band 2+.
	Severity string
	// Cooldown is the dedup window: how long to suppress repeat escalations of
	// the same bucket after the matching escalation closes.
	Cooldown time.Duration
	// Signature is the stable dedup key. It includes the breach bucket but NOT
	// the exact count, so workload drift within a band reuses one escalation.
	Signature string
}

// EvaluateOpenWispAlert decides whether an open-wisp escalation should fire and,
// if so, returns its stable dedup metadata. It makes no I/O and is the single
// source of truth used by the `gt reaper alert-open-wisps` command.
//
// The alert gates on `actionable` — the count of open wisps PAST their per-type
// TTL, i.e. the subset compaction would actually act on (see
// CountActionableOpenWisps). It does NOT gate on the raw open count, because
// within-TTL accumulation under high session activity drains naturally and is
// not something an operator can act on (gu-9ks4i). The raw count is still
// carried through EscalateArgs for the human-readable reason so accumulation
// stays visible without driving the dedup/fire decision.
//
// threshold <= 0 is treated as "alerting disabled" (never fire) so an operator
// can opt out via config without a special case at every call site.
func EvaluateOpenWispAlert(actionable, threshold int) OpenWispAlert {
	if threshold <= 0 || actionable <= threshold {
		return OpenWispAlert{Fire: false}
	}
	bucket := actionable / threshold // >= 1 since actionable > threshold > 0
	severity := "medium"
	cooldown := openWispMediumCooldown
	if bucket >= 2 {
		severity = "high"
		cooldown = openWispHighCooldown
	}
	return OpenWispAlert{
		Fire:      true,
		Bucket:    bucket,
		Severity:  severity,
		Cooldown:  cooldown,
		Signature: fmt.Sprintf("reaper:open-wisp-breach:b%d", bucket),
	}
}

// openWispAlertSource identifies this alert's origin in escalation routing and
// is also reused as the dedup signature's stable namespace.
const openWispAlertSource = "reaper:open-wisp-count"

// EscalateArgs returns the `gt escalate` argument vector for a firing alert
// (excluding the leading "gt"/"escalate"). It is exported as a method so the
// CLI command stays a thin exec wrapper and the full argument shape — severity,
// dedup signature, and close-aware cooldown window — is covered by unit tests
// rather than only exercised at runtime.
//
// Callers MUST check Fire before invoking; calling on a non-firing alert
// returns nil.
//
// `actionable` is the past-TTL count the alert fired on (the value compared to
// the threshold); `rawOpen` is the total open-wisp count, reported alongside it
// so the escalation surfaces both the actionable subset and the overall
// accumulation (gu-9ks4i).
func (a OpenWispAlert) EscalateArgs(actionable, rawOpen, threshold int) []string {
	if !a.Fire {
		return nil
	}
	title := fmt.Sprintf("%d actionable (past-TTL) open wisps exceed alert threshold (%d)", actionable, threshold)
	reason := fmt.Sprintf(
		"Actionable (past-TTL) open wisp count %d is %dx the alert threshold of %d "+
			"(breach band %d); total open wisps: %d. These are wisps compaction "+
			"would act on (promote/delete) — investigate wisp lifecycle. Within-TTL "+
			"accumulation is excluded because it drains naturally. This escalation "+
			"deduplicates on the breach band, so it will not re-fire for normal count "+
			"drift within the same band until the cooldown (%s) elapses.",
		actionable, a.Bucket, threshold, a.Bucket, rawOpen, a.Cooldown)
	return []string{
		"escalate",
		"-s", a.Severity,
		title,
		"--source=" + openWispAlertSource,
		"--dedup",
		"--signature=" + a.Signature,
		"--dedup-window=" + a.Cooldown.String(),
		"-r", reason,
	}
}

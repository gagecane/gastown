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
// source of truth shared by the daemon inline path and the `gt reaper
// alert-open-wisps` CLI helper.
//
// threshold <= 0 is treated as "alerting disabled" (never fire) so an operator
// can opt out via config without a special case at every call site.
func EvaluateOpenWispAlert(open, threshold int) OpenWispAlert {
	if threshold <= 0 || open <= threshold {
		return OpenWispAlert{Fire: false}
	}
	bucket := open / threshold // >= 1 since open > threshold > 0
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

package util

import (
	"fmt"
	"time"
)

// HumanizeSince formats the elapsed time since t as a relative string like
// "just now", "1 minute ago", "3 hours ago", or "5 days ago".
//
// When t is the zero value, zeroLabel is returned instead. Callers that want a
// placeholder (e.g. "(unknown)") pass it explicitly; callers that prefer an
// empty string for missing timestamps pass "".
func HumanizeSince(t time.Time, zeroLabel string) string {
	if t.IsZero() {
		return zeroLabel
	}

	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}

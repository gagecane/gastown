package witness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/events"
)

// writeRefineryPausedEventsFile is a small helper that writes a slice of
// events.Event values as JSONL to the canonical events file under townRoot.
func writeRefineryPausedEventsFile(t *testing.T, townRoot string, evs []events.Event) {
	t.Helper()
	path := filepath.Join(townRoot, events.EventsFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open events file: %v", err)
	}
	defer f.Close()
	for _, ev := range evs {
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		data = append(data, '\n')
		if _, err := f.Write(data); err != nil {
			t.Fatalf("write event: %v", err)
		}
	}
}

func TestDetectRefineryPaused_DisabledByZeroLookback(t *testing.T) {
	tmp := t.TempDir()
	res := DetectRefineryPaused(tmp, "", 0)
	if res == nil {
		t.Fatalf("nil result")
	}
	if res.Scanned != 0 || len(res.Paused) > 0 {
		t.Errorf("expected empty result for lookback=0, got %+v", res)
	}
}

func TestDetectRefineryPaused_NoEventsFile(t *testing.T) {
	tmp := t.TempDir()
	res := DetectRefineryPaused(tmp, "", time.Hour)
	if res == nil {
		t.Fatalf("nil result")
	}
	if res.Scanned != 0 || len(res.Paused) > 0 || len(res.Errors) > 0 {
		t.Errorf("expected silent empty result when events file missing, got %+v", res)
	}
}

func TestDetectRefineryPaused_PicksUpAndDedupes(t *testing.T) {
	tmp := t.TempDir()
	now := time.Now().UTC()

	// Three events on the same (rig, mrID) within window — should collapse
	// to one entry with Count=3.
	mr := "gu-mr-aaa"
	mk := func(ts time.Time) events.Event {
		return events.Event{
			Timestamp:  ts.Format(time.RFC3339),
			Source:     "gt",
			Type:       events.TypeRefineryPaused,
			Actor:      "gastown_upstream/refinery",
			Visibility: events.VisibilityFeed,
			Payload: events.RefineryPausedPayload(
				"gastown_upstream",
				mr,
				"polecat/nitro/gu-t3why",
				"gu-t3why",
				"pr_needs_approval",
				"PR #123 requires approving review before merge",
				"github_pr_review",
			),
		}
	}
	writeRefineryPausedEventsFile(t, tmp, []events.Event{
		mk(now.Add(-30 * time.Minute)),
		mk(now.Add(-15 * time.Minute)),
		mk(now.Add(-1 * time.Minute)),
	})

	res := DetectRefineryPaused(tmp, "gastown_upstream", time.Hour)
	if len(res.Paused) != 1 {
		t.Fatalf("expected 1 deduplicated entry, got %d (%+v)", len(res.Paused), res.Paused)
	}
	got := res.Paused[0]
	if got.MRID != mr {
		t.Errorf("MRID = %q, want %q", got.MRID, mr)
	}
	if got.Count != 3 {
		t.Errorf("Count = %d, want 3", got.Count)
	}
	if got.Reason != "pr_needs_approval" {
		t.Errorf("Reason = %q, want pr_needs_approval", got.Reason)
	}
	if got.SuspectedConvention != "github_pr_review" {
		t.Errorf("SuspectedConvention = %q, want github_pr_review", got.SuspectedConvention)
	}
	if got.LastSeen.Before(got.FirstSeen) {
		t.Errorf("LastSeen %v before FirstSeen %v", got.LastSeen, got.FirstSeen)
	}
	if got.LastSeen.Sub(got.FirstSeen) < 25*time.Minute {
		t.Errorf("expected LastSeen-FirstSeen >= ~29m, got %v", got.LastSeen.Sub(got.FirstSeen))
	}
}

func TestDetectRefineryPaused_DropsOldEvents(t *testing.T) {
	tmp := t.TempDir()
	now := time.Now().UTC()

	old := events.Event{
		Timestamp:  now.Add(-2 * time.Hour).Format(time.RFC3339),
		Source:     "gt",
		Type:       events.TypeRefineryPaused,
		Actor:      "gastown_upstream/refinery",
		Visibility: events.VisibilityFeed,
		Payload: events.RefineryPausedPayload(
			"gastown_upstream", "gu-mr-old", "br", "gu-old",
			"pr_needs_approval", "old", "github_pr_review"),
	}
	fresh := events.Event{
		Timestamp:  now.Add(-5 * time.Minute).Format(time.RFC3339),
		Source:     "gt",
		Type:       events.TypeRefineryPaused,
		Actor:      "gastown_upstream/refinery",
		Visibility: events.VisibilityFeed,
		Payload: events.RefineryPausedPayload(
			"gastown_upstream", "gu-mr-new", "br", "gu-new",
			"pr_needs_approval", "fresh", "github_pr_review"),
	}
	writeRefineryPausedEventsFile(t, tmp, []events.Event{old, fresh})

	res := DetectRefineryPaused(tmp, "", 30*time.Minute)
	if len(res.Paused) != 1 {
		t.Fatalf("expected only the fresh event in window, got %d entries: %+v", len(res.Paused), res.Paused)
	}
	if res.Paused[0].MRID != "gu-mr-new" {
		t.Errorf("MRID = %q, want gu-mr-new", res.Paused[0].MRID)
	}
}

func TestDetectRefineryPaused_RigFilterMatchesCaseInsensitive(t *testing.T) {
	tmp := t.TempDir()
	now := time.Now().UTC()

	mk := func(rig, mr string) events.Event {
		return events.Event{
			Timestamp:  now.Add(-1 * time.Minute).Format(time.RFC3339),
			Source:     "gt",
			Type:       events.TypeRefineryPaused,
			Actor:      rig + "/refinery",
			Visibility: events.VisibilityFeed,
			Payload: events.RefineryPausedPayload(
				rig, mr, "br", "src",
				"pr_needs_approval", "details", "github_pr_review"),
		}
	}
	writeRefineryPausedEventsFile(t, tmp, []events.Event{
		mk("gastown_upstream", "gu-mr-a"),
		mk("gastown", "gt-mr-b"),
		mk("talon_cdk", "tk-mr-c"),
	})

	// Case-insensitive match — operators sometimes type rig names with mixed case.
	res := DetectRefineryPaused(tmp, "Gastown_Upstream", time.Hour)
	if len(res.Paused) != 1 {
		t.Fatalf("expected 1 filtered entry, got %d: %+v", len(res.Paused), res.Paused)
	}
	if res.Paused[0].Rig != "gastown_upstream" {
		t.Errorf("Rig = %q, want gastown_upstream", res.Paused[0].Rig)
	}

	// Empty filter returns all rigs.
	all := DetectRefineryPaused(tmp, "", time.Hour)
	if len(all.Paused) != 3 {
		t.Fatalf("expected all 3 entries with empty filter, got %d", len(all.Paused))
	}
}

func TestDetectRefineryPaused_IgnoresOtherEventTypes(t *testing.T) {
	tmp := t.TempDir()
	now := time.Now().UTC()

	other := events.Event{
		Timestamp:  now.Add(-1 * time.Minute).Format(time.RFC3339),
		Source:     "gt",
		Type:       events.TypeMerged,
		Actor:      "gastown_upstream/refinery",
		Visibility: events.VisibilityFeed,
		Payload:    events.MergePayload("gu-mr-x", "polecat", "br", ""),
	}
	paused := events.Event{
		Timestamp:  now.Add(-1 * time.Minute).Format(time.RFC3339),
		Source:     "gt",
		Type:       events.TypeRefineryPaused,
		Actor:      "gastown_upstream/refinery",
		Visibility: events.VisibilityFeed,
		Payload: events.RefineryPausedPayload(
			"gastown_upstream", "gu-mr-y", "br", "src",
			"pr_needs_approval", "details", "github_pr_review"),
	}
	writeRefineryPausedEventsFile(t, tmp, []events.Event{other, paused})

	res := DetectRefineryPaused(tmp, "", time.Hour)
	if len(res.Paused) != 1 {
		t.Fatalf("expected 1 paused entry (Merged event must be ignored), got %d", len(res.Paused))
	}
	if res.Paused[0].MRID != "gu-mr-y" {
		t.Errorf("MRID = %q, want gu-mr-y", res.Paused[0].MRID)
	}
}

func TestDetectRefineryPaused_MalformedLinesAreSkipped(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, events.EventsFile)
	good := events.Event{
		Timestamp:  time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339),
		Source:     "gt",
		Type:       events.TypeRefineryPaused,
		Actor:      "gastown_upstream/refinery",
		Visibility: events.VisibilityFeed,
		Payload: events.RefineryPausedPayload(
			"gastown_upstream", "gu-mr-z", "br", "src",
			"pr_needs_approval", "details", "github_pr_review"),
	}
	data, _ := json.Marshal(good)

	// Mix in two malformed lines: corrupt JSON and a non-JSON line.
	body := []byte("{not-json\n")
	body = append(body, []byte("garbage line without braces\n")...)
	body = append(body, data...)
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write events file: %v", err)
	}

	res := DetectRefineryPaused(tmp, "", time.Hour)
	if len(res.Paused) != 1 {
		t.Fatalf("expected 1 paused entry past malformed lines, got %d (%+v)", len(res.Paused), res.Paused)
	}
}

func TestDetectRefineryPaused_DeterministicOrder(t *testing.T) {
	tmp := t.TempDir()
	now := time.Now().UTC()

	mk := func(rig, mr string) events.Event {
		return events.Event{
			Timestamp:  now.Add(-1 * time.Minute).Format(time.RFC3339),
			Source:     "gt",
			Type:       events.TypeRefineryPaused,
			Actor:      rig + "/refinery",
			Visibility: events.VisibilityFeed,
			Payload: events.RefineryPausedPayload(
				rig, mr, "br", "src",
				"pr_needs_approval", "details", "github_pr_review"),
		}
	}
	// Inserted out of order.
	writeRefineryPausedEventsFile(t, tmp, []events.Event{
		mk("gastown_upstream", "gu-mr-z"),
		mk("alpha_rig", "al-mr-a"),
		mk("gastown_upstream", "gu-mr-a"),
	})

	res := DetectRefineryPaused(tmp, "", time.Hour)
	if len(res.Paused) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(res.Paused))
	}
	want := []struct{ rig, mr string }{
		{"alpha_rig", "al-mr-a"},
		{"gastown_upstream", "gu-mr-a"},
		{"gastown_upstream", "gu-mr-z"},
	}
	for i, w := range want {
		if res.Paused[i].Rig != w.rig || res.Paused[i].MRID != w.mr {
			t.Errorf("entry %d = (%s, %s), want (%s, %s)",
				i, res.Paused[i].Rig, res.Paused[i].MRID, w.rig, w.mr)
		}
	}
}

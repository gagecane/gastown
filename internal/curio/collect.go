package curio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/liveness"
)

// Collectors turn live Gastown state into normalized records, resolving all
// probes (PID liveness, git ancestry, Dolt proximity) so the rules stay pure.
//
//   - Phase 1 (gu-6s8ao) shipped the dead-owner-admission collector below.
//   - Phase 1.1 (gu-9bnaw) adds the three remaining live collectors in
//     collect_live.go: events.jsonl rate counts (c), dog-log kill-signal scan
//     (b), and dual-source merged-but-not-landed beads (a). Each handles the
//     eng-review failure mode that justified deferring it.
//
// CollectInput wires the filesystem-backed collectors (admissions, rate, logs)
// directly from townRoot; the merged-bead source (a) requires bead Dolt access,
// so the caller (the daemon) gathers the raw observations and injects them —
// keeping this package free of a beads import and the failure-mode logic
// (ResolveMergedBeads) unit-testable.

// admissionReservationFile mirrors the on-disk reservation written by
// internal/cmd/polecat_capacity.go. Only the fields the rule needs are read.
type admissionReservationFile struct {
	ID  string `json:"id"`
	PID int    `json:"pid"`
	Rig string `json:"rig,omitempty"`
}

// CollectAdmissions reads the polecat-admission reservation dir under townRoot
// and returns a normalized AdmissionRecord per reservation, with OwnerAlive
// resolved via a PID liveness probe. A missing dir yields no records (not an
// error — the dir only exists once admissions have run).
func CollectAdmissions(townRoot string) ([]AdmissionRecord, error) {
	dir := filepath.Join(townRoot, ".runtime", "polecat-admission")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var out []AdmissionRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // G304: path constructed internally
		if err != nil {
			continue // skip unreadable records rather than failing the cycle
		}
		var r admissionReservationFile
		if err := json.Unmarshal(data, &r); err != nil {
			continue // skip corrupt records
		}
		if r.ID == "" || r.PID <= 0 {
			continue
		}
		out = append(out, AdmissionRecord{
			ID:         r.ID,
			PID:        r.PID,
			Rig:        r.Rig,
			OwnerAlive: liveness.PIDAlive(r.PID),
			// Reservations are written by the scheduler, never by curio; the
			// loop-breaker check is a no-op here but kept explicit for safety.
			FiledBy: "scheduler",
		})
	}
	return out, nil
}

// CollectOptions injects the data sources a collector cannot derive from
// townRoot alone. All fields are optional — a zero CollectOptions yields the
// filesystem-only collectors (admissions, rate, logs).
type CollectOptions struct {
	// Window bounds the rate collector's events.jsonl scan. Zero values mean
	// "last 24h ending now" (the rate thresholds are daily — see rules.go).
	Start time.Time
	End   time.Time

	// MergedBeadSources are closed-"merged" bead observations gathered by the
	// caller from each source (bead Dolt first, then OTel), passed straight to
	// ResolveMergedBeads. Empty means the merged-not-landed rule sees no beads.
	MergedBeadSources [][]MergedBeadObservation
	// Ancestry resolves whether a merged bead's commit is in its rig's main.
	// Nil means every commit is treated as not-landed (conservative).
	Ancestry AncestryResolver

	// KnownDoltPIDs, when set, makes the kill-signal collector require a line to
	// reference one of these PIDs. Empty means a textual Dolt reference suffices.
	KnownDoltPIDs []int
}

// CollectInput assembles the live Input for one patrol cycle from townRoot
// using default options (filesystem collectors, last-24h rate window).
func CollectInput(townRoot, windowID string) (Input, error) {
	return CollectInputWith(townRoot, windowID, CollectOptions{})
}

// CollectInputWith assembles the live Input with caller-injected sources for
// the merged-bead rule and an explicit rate window. It runs all four Phase 1.1
// collectors; a failure in the admission collector (the only one that can
// surface a hard error) aborts the cycle, while the rate/log collectors degrade
// to empty on missing data.
func CollectInputWith(townRoot, windowID string, opts CollectOptions) (Input, error) {
	admissions, err := CollectAdmissions(townRoot)
	if err != nil {
		return Input{}, err
	}

	start, end := opts.Start, opts.End
	if end.IsZero() {
		end = time.Now().UTC()
	}
	if start.IsZero() {
		start = end.Add(-24 * time.Hour)
	}

	eventCounts, err := collectEventCountsFromFile(filepath.Join(townRoot, ".events.jsonl"), start, end)
	if err != nil {
		return Input{}, err
	}

	logLines, err := CollectKillSignals(filepath.Join(townRoot, "daemon"), opts.KnownDoltPIDs)
	if err != nil {
		return Input{}, err
	}

	beads := ResolveMergedBeads(opts.MergedBeadSources, opts.Ancestry)

	return Input{
		Window:      Window{ID: windowID, Start: start, End: end},
		Beads:       beads,
		LogLines:    logLines,
		EventCounts: eventCounts,
		Admissions:  admissions,
	}, nil
}

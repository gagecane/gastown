package curio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/liveness"
)

// Collectors turn live Gastown state into normalized records, resolving all
// probes (PID liveness, etc.) so the rules stay pure. Phase 1 ships the
// dead-owner-admission collector (pure local file reads, addresses the live
// gu-t6jqq incident). The bead/log/rate collectors are staged for a follow-up
// (they carry the eng-review's dual-source / log-rotation failure modes that
// need focused handling) — until then those Input slices are empty and their
// rules simply produce nothing, which is correct.

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

// CollectInput assembles the live Input for one patrol cycle. windowID labels
// the cycle. Only the admission collector is live in Phase 1.
func CollectInput(townRoot, windowID string) (Input, error) {
	admissions, err := CollectAdmissions(townRoot)
	if err != nil {
		return Input{}, err
	}
	return Input{
		Window:     Window{ID: windowID},
		Admissions: admissions,
	}, nil
}

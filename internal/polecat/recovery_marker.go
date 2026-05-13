package polecat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// DefaultRecoveryMarkerTTL is how long a manual-recovery marker remains active
// after it is set. Picked to comfortably outlast the witness's recovery window
// and a few stuck-agent-dog cycles (5m cooldown), so the plugin can't outrace a
// human/witness who's mid-cleanup.
const DefaultRecoveryMarkerTTL = 30 * time.Minute

// RecoveryMarker represents a "manual recovery in progress" awareness flag set
// on a polecat session. When present and unexpired, automated recovery actors
// (stuck-agent-dog plugin, witness retries) should skip RESTART_POLECAT and
// instead let the polecat be nuked or cleaned up out-of-band.
//
// Background: gu-v5mk. After a witness/mayor performs an out-of-band push
// recovery (e.g. manual --no-verify push), the polecat session is still on a
// dead-but-already-pushed branch. Without this marker, stuck-agent-dog sees the
// dead session and issues RESTART_POLECAT, which re-runs already-pushed work
// and risks re-hitting the same hang that caused the manual recovery.
type RecoveryMarker struct {
	SetAt     time.Time `json:"set_at"`
	SetBy     string    `json:"set_by,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

func recoveryMarkersDir(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "recovery_markers")
}

func recoveryMarkerFile(townRoot, sessionName string) string {
	return filepath.Join(recoveryMarkersDir(townRoot), sessionName+".json")
}

// WriteRecoveryMarker creates or refreshes a manual-recovery marker for the
// given session. ttl<=0 falls back to DefaultRecoveryMarkerTTL.
func WriteRecoveryMarker(townRoot, sessionName, setBy, reason string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = DefaultRecoveryMarkerTTL
	}
	dir := recoveryMarkersDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	now := time.Now().UTC()
	m := RecoveryMarker{
		SetAt:     now,
		SetBy:     setBy,
		Reason:    reason,
		ExpiresAt: now.Add(ttl),
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(recoveryMarkerFile(townRoot, sessionName), data, 0644)
}

// ReadRecoveryMarker returns the marker for a session, or nil if none exists or
// the file cannot be parsed. A malformed marker is treated as absent so a
// corrupt file cannot wedge the plugin into a permanent skip-restart loop.
func ReadRecoveryMarker(townRoot, sessionName string) *RecoveryMarker {
	data, err := os.ReadFile(recoveryMarkerFile(townRoot, sessionName))
	if err != nil {
		return nil
	}
	var m RecoveryMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return &m
}

// HasActiveRecoveryMarker reports whether a non-expired marker exists for the
// session. Expired markers are considered absent. Callers should treat an
// active marker as "skip RESTART_POLECAT for this slot".
func HasActiveRecoveryMarker(townRoot, sessionName string) bool {
	m := ReadRecoveryMarker(townRoot, sessionName)
	if m == nil {
		return false
	}
	return time.Now().UTC().Before(m.ExpiresAt)
}

// ClearRecoveryMarker removes the marker for a session. Missing markers are
// not an error (idempotent).
func ClearRecoveryMarker(townRoot, sessionName string) error {
	err := os.Remove(recoveryMarkerFile(townRoot, sessionName))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

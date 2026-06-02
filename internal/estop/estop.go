// Package estop provides emergency stop functionality for Gas Town.
//
// The E-stop is a town-wide mechanism to pause all agent work. It uses a
// sentinel file (ESTOP) at the town root. When present, all agents should
// be frozen (SIGTSTP) and the daemon should not restart them.
//
// The Mayor is exempt from E-stop so it can coordinate recovery.
//
// Original implementation by outdoorsea (PR #3237).
package estop

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

// FileName is the sentinel file name at the town root.
const FileName = "ESTOP"

// TriggerManual is the prefix for a human-triggered E-stop.
const TriggerManual = "manual"

// TriggerAuto is the prefix for an auto-triggered E-stop.
const TriggerAuto = "auto"

// Info represents the parsed contents of an ESTOP file.
type Info struct {
	Trigger     string    // "manual" or "auto"
	TriggeredBy string    // actor who triggered the E-stop (os user + GT_ROLE/session)
	Reason      string    // human-readable reason
	Timestamp   time.Time // when the E-stop was triggered
}

// CurrentActor returns a best-effort identity string for whoever is triggering
// an E-stop: the OS user combined with the Gas Town role/session when present
// (e.g. "canewiw (gastown_upstream/polecats/raider)"). It never fails — an
// empty or partial result is acceptable, since attribution is best-effort.
func CurrentActor() string {
	osUser := os.Getenv("USER")
	if osUser == "" {
		if u, err := user.Current(); err == nil {
			osUser = u.Username
		}
	}

	role := os.Getenv("GT_ROLE")
	if role == "" {
		role = os.Getenv("GT_SESSION")
	}

	switch {
	case osUser != "" && role != "":
		return fmt.Sprintf("%s (%s)", osUser, role)
	case role != "":
		return role
	default:
		return osUser
	}
}

// FilePath returns the full path to the ESTOP sentinel file.
func FilePath(townRoot string) string {
	return filepath.Join(townRoot, FileName)
}

// IsActive checks whether an E-stop is currently active.
func IsActive(townRoot string) bool {
	_, err := os.Stat(FilePath(townRoot))
	return err == nil
}

// Read reads and parses the ESTOP file. Returns nil if not active.
func Read(townRoot string) *Info {
	data, err := os.ReadFile(FilePath(townRoot))
	if err != nil {
		return nil
	}
	return parse(string(data))
}

// Activate creates the ESTOP sentinel file with the given trigger and reason.
// The triggering actor is captured automatically via CurrentActor.
func Activate(townRoot, trigger, reason string) error {
	return os.WriteFile(FilePath(townRoot), []byte(format(trigger, CurrentActor(), reason)), 0644)
}

// Deactivate removes the ESTOP sentinel file.
// If onlyAuto is true, only removes auto-triggered E-stops.
func Deactivate(townRoot string, onlyAuto bool) error {
	if onlyAuto {
		info := Read(townRoot)
		if info != nil && info.Trigger == TriggerManual {
			return fmt.Errorf("E-stop was manually triggered — use 'gt thaw' to clear")
		}
	}
	err := os.Remove(FilePath(townRoot))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// RigFileName returns the sentinel file name for a per-rig E-stop.
func RigFileName(rigName string) string {
	return fmt.Sprintf("ESTOP.%s", rigName)
}

// RigFilePath returns the full path to a per-rig ESTOP sentinel file.
func RigFilePath(townRoot, rigName string) string {
	return filepath.Join(townRoot, RigFileName(rigName))
}

// IsRigActive checks whether a per-rig E-stop is active.
func IsRigActive(townRoot, rigName string) bool {
	_, err := os.Stat(RigFilePath(townRoot, rigName))
	return err == nil
}

// ReadRig reads and parses a per-rig ESTOP file. Returns nil if not active.
func ReadRig(townRoot, rigName string) *Info {
	data, err := os.ReadFile(RigFilePath(townRoot, rigName))
	if err != nil {
		return nil
	}
	return parse(string(data))
}

// ActivateRig creates a per-rig ESTOP sentinel file.
// The triggering actor is captured automatically via CurrentActor.
func ActivateRig(townRoot, rigName, trigger, reason string) error {
	return os.WriteFile(RigFilePath(townRoot, rigName), []byte(format(trigger, CurrentActor(), reason)), 0644)
}

// DeactivateRig removes a per-rig ESTOP sentinel file.
func DeactivateRig(townRoot, rigName string) error {
	err := os.Remove(RigFilePath(townRoot, rigName))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// IsAnyActive checks if a town-wide or rig-specific E-stop affects this rig.
func IsAnyActive(townRoot, rigName string) bool {
	return IsActive(townRoot) || IsRigActive(townRoot, rigName)
}

// format renders the on-disk ESTOP file content.
//
// Current format (4 fields): trigger\ttimestamp\ttriggered_by\treason
// Legacy format (3 fields):  trigger\ttimestamp\treason
//
// parse reads both. triggered_by may be empty (best-effort attribution).
func format(trigger, triggeredBy, reason string) string {
	ts := time.Now().Format(time.RFC3339)
	return fmt.Sprintf("%s\t%s\t%s\t%s\n", trigger, ts, triggeredBy, reason)
}

func parse(content string) *Info {
	// Trim only the trailing newline, not all trailing whitespace: an empty
	// reason leaves a trailing tab ("trigger\tts\tactor\t") that TrimSpace
	// would eat, collapsing the 4-field form back into the 3-field legacy
	// form and dropping the actor.
	content = strings.TrimRight(content, "\r\n")
	if strings.TrimSpace(content) == "" {
		return &Info{Trigger: TriggerManual, Timestamp: time.Now()}
	}

	parts := strings.SplitN(content, "\t", 4)
	info := &Info{Trigger: TriggerManual}

	if len(parts) >= 1 {
		info.Trigger = parts[0]
	}
	if len(parts) >= 2 {
		if t, err := time.Parse(time.RFC3339, parts[1]); err == nil {
			info.Timestamp = t
		}
	}
	switch {
	case len(parts) >= 4:
		// Current format: trigger\ttimestamp\ttriggered_by\treason
		info.TriggeredBy = parts[2]
		info.Reason = parts[3]
	case len(parts) == 3:
		// Legacy format: trigger\ttimestamp\treason (no actor captured)
		info.Reason = parts[2]
	}

	return info
}

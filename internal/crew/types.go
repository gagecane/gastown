// Package crew provides crew workspace management for overseer workspaces.
package crew

// CrewWorker represents a user-managed workspace in a rig.
//
// CrewWorker is a derived, read-only view built from the filesystem layout
// (directory name), the crew manager's rig context, and the live git state
// of the clone. There is no on-disk state.json; metadata lives in the
// crew agent bead (gt-<rig>-crew-<name>) created by `gt crew add`.
// See gu-kplt for the migration rationale.
type CrewWorker struct {
	// Name is the crew worker identifier (always derived from the crew directory name).
	Name string `json:"name"`

	// Rig is the rig this crew worker belongs to.
	Rig string `json:"rig"`

	// ClonePath is the path to the crew worker's clone of the rig.
	ClonePath string `json:"clone_path"`

	// Branch is the current git branch in the clone at the time of read.
	Branch string `json:"branch"`
}

// Summary provides a concise view of crew worker status.
type Summary struct {
	Name   string `json:"name"`
	Branch string `json:"branch"`
}

// Summary returns a Summary for this crew worker.
func (c *CrewWorker) Summary() Summary {
	return Summary{
		Name:   c.Name,
		Branch: c.Branch,
	}
}

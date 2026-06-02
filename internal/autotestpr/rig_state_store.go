// Beads-backed RigStateStore implementation.
//
// Phase 0 task 4 (gu-2n7xi). The CAS transition layer (dispatch.go) and
// the D18 cooldown-release (cooldown_release.go) operate against the
// RigStateStore interface so their logic is unit-testable without Dolt.
// This file provides the production adapter that wires that interface to
// the real per-rig `<rig>-auto-test-state` pinned bead and the OQ4
// transition-attachment beads.
package autotestpr

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/beads"
)

// BeadsRigStateStore is the production RigStateStore backed by a
// *beads.Beads client. State reads/writes target the per-rig pinned
// bead (`<rig>-auto-test-state`); transition records are appended as
// OQ4-fallback attachment beads.
type BeadsRigStateStore struct {
	Beads *beads.Beads
}

// NewBeadsRigStateStore constructs a store over the given beads client.
func NewBeadsRigStateStore(b *beads.Beads) *BeadsRigStateStore {
	return &BeadsRigStateStore{Beads: b}
}

// compile-time interface check.
var _ RigStateStore = (*BeadsRigStateStore)(nil)

// LoadRigState reads the per-rig state pinned bead.
func (s *BeadsRigStateStore) LoadRigState(rig string) (RigState, error) {
	return LoadRigState(s.Beads, rig)
}

// SaveRigState writes the per-rig state back via a whole-blob metadata
// replace. Single-writer is safe here per the OQ4 fallback: the Mayor
// cycle is the only writer of the state field, and the CAS loop in
// CASTransition guards on the expected `from` state. Transient Dolt
// write conflicts surface verbatim so the CAS loop's
// isTransientDoltWriteError check can retry.
func (s *BeadsRigStateStore) SaveRigState(rig string, state RigState) error {
	if s.Beads == nil {
		return fmt.Errorf("BeadsRigStateStore: nil beads wrapper")
	}
	raw, err := state.MarshalMetadata()
	if err != nil {
		return fmt.Errorf("marshaling rig state for %s: %w", rig, err)
	}
	return s.Beads.Update(RigStateBeadID(rig), beads.UpdateOptions{Metadata: raw})
}

// AppendTransition files a transition attachment bead for the audit log.
func (s *BeadsRigStateStore) AppendTransition(rec TransitionRecord) error {
	_, err := CreateTransitionAttachment(s.Beads, rec)
	return err
}

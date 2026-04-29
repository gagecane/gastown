// Package doctor: rig_check.go is the aggregator for rig-level checks.
//
// Individual rig checks live in their own files (see *_check.go in this package).
// This file only provides the RigChecks() registry that returns all rig-level
// health checks in the order they should be executed.
package doctor

// RigChecks returns all rig-level health checks.
func RigChecks() []Check {
	return []Check{
		NewRigIsGitRepoCheck(),
		NewGitExcludeConfiguredCheck(),
		NewHooksPathConfiguredCheck(),
		NewBareRepoExistsCheck(),
		NewBareRepoRefspecCheck(),
		NewDefaultBranchExistsCheck(),
		NewWitnessExistsCheck(),
		NewRefineryExistsCheck(),
		NewMayorCloneExistsCheck(),
		NewPolecatClonesValidCheck(),
		NewBeadsConfigValidCheck(),
		NewBeadsRedirectCheck(),
		NewTestutilSymlinkCheck(),
	}
}

package daemon

import (
	"os"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/beads"
)

func bdReadOnlyRoutingEnv(townRoot string) []string {
	fallback := ""
	if townRoot != "" {
		fallback = filepath.Join(townRoot, ".beads")
	}
	return beads.BuildReadOnlyRoutingBDEnv(os.Environ(), fallback)
}

func bdMutationRoutingEnv(townRoot string) []string {
	fallback := ""
	if townRoot != "" {
		fallback = filepath.Join(townRoot, ".beads")
	}
	return beads.BuildMutationRoutingBDEnv(os.Environ(), fallback)
}

func bdReadOnlyPinnedEnv(beadsDir string) []string {
	return beads.BuildReadOnlyPinnedBDEnv(os.Environ(), beadsDir)
}

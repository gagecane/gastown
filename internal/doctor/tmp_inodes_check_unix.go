//go:build !windows

package doctor

import (
	"fmt"
	"syscall"
)

// readTmpInodeUsage returns inode usage stats for the filesystem
// backing path, using syscall.Statfs. On filesystems that don't
// track inodes (Total == 0), the caller treats the result as "OK".
func readTmpInodeUsage(path string) (tmpInodeUsage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return tmpInodeUsage{}, fmt.Errorf("statfs %s: %w", path, err)
	}

	// Normalize to uint64: on FreeBSD, Statfs_t.Ffree is declared as int64
	// while Files is uint64. Other Unix variants (Linux, darwin) expose both
	// as uint64. An explicit cast keeps one _unix build tag covering all of
	// them. In the exceedingly rare case that Ffree is reported as negative
	// (treated as "unknown" by some filesystems), the cast wraps to a very
	// large uint64 which then falls through the total >= free guard and
	// leaves used at zero.
	total := uint64(st.Files) //nolint:unconvert // Required for FreeBSD where Files/Ffree types differ
	free := uint64(st.Ffree) //nolint:unconvert // Required for FreeBSD where Ffree is int64
	var used uint64
	if total >= free {
		used = total - free
	}

	return tmpInodeUsage{Total: total, Free: free, Used: used}, nil
}

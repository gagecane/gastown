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

	total := st.Files
	free := st.Ffree
	var used uint64
	if total >= free {
		used = total - free
	}

	return tmpInodeUsage{Total: total, Free: free, Used: used}, nil
}

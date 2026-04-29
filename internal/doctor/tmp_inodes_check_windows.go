//go:build windows

package doctor

import "errors"

// readTmpInodeUsage is a no-op on Windows: NTFS/ReFS don't expose a
// fixed per-filesystem inode count the way POSIX filesystems do.
// The check treats the returned error as "inode usage not available"
// and reports StatusOK, so this effectively disables the check.
func readTmpInodeUsage(_ string) (tmpInodeUsage, error) {
	return tmpInodeUsage{}, errors.New("inode usage not tracked on Windows filesystems")
}

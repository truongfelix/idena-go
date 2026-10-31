//go:build !idena_memory_ipfs

package ipfs

import (
	"os"

	"golang.org/x/sys/unix"
)

// diskFreeBytes: the bytes free for this user on the disk holding dir (f_bavail counts f_frsize units).
func diskFreeBytes(dir string) (int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Frsize), nil
}

// syncTree puts every file written under dir on disk: syncfs flushes the whole filesystem holding it.
func syncTree(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Syncfs(int(f.Fd()))
}

//go:build !idena_memory_ipfs

package ipfs

import (
	"io/fs"
	"os"
	"path/filepath"

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

// canDropFileCache: dropFileCache drops pages here.
const canDropFileCache = true

// syncTree puts every file written under dir on disk: syncfs flushes the whole filesystem holding it.
func syncTree(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Syncfs(int(f.Fd()))
}

// dropFileCache asks the kernel to drop the cached pages of every file under dir (best effort). The conversion reads
// the Badger store once: without it, its gigabytes stay cached and are charged to the node's cgroup, which memory
// pressure monitors such as systemd-oomd watch. Dirty pages are not dropped, only clean ones.
func dropFileCache(dir string) {
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
		f.Close()
		return nil
	})
}

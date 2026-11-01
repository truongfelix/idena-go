//go:build !idena_memory_ipfs

package ipfs

import (
	"os"

	"golang.org/x/sys/unix"
)

// diskFreeBytes: the bytes free for this user on the disk holding dir.
func diskFreeBytes(dir string) (int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

// syncsFilesystem: syncCopied needs the list of the block files written.
const syncsFilesystem = false

// syncCopied puts the block files written since the last call on disk, one by one.
func syncCopied(_ string, blocks []string) error {
	return syncFiles(blocks)
}

// fsyncFile puts one file on the drive with a plain fsync: File.Sync's F_FULLFSYNC also flushes the drive's cache,
// tens of milliseconds a file. The marker written after them (writeFileAtomic, F_FULLFSYNC) flushes that cache once
// for all the files before it.
func fsyncFile(f *os.File) error {
	return unix.Fsync(int(f.Fd()))
}

// syncTree puts every file under dir on disk, one by one (File.Sync is F_FULLFSYNC on macOS).
func syncTree(dir string) error {
	return syncEachFile(dir)
}

// dropFileCache: no page cache advice here (Linux only).
func dropFileCache(string) {}

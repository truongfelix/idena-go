//go:build !idena_memory_ipfs

package ipfs

import (
	"time"

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

// syncCopied puts the copy written so far on disk: the block files written since the last call, one by one, and
// LevelDB's files changed since.
func syncCopied(_ string, blocks []string, leveldbDir string, since time.Time) error {
	if err := syncFiles(blocks); err != nil {
		return err
	}
	return syncChangedSince(leveldbDir, since)
}

// syncTree puts every file under dir on disk, one by one (File.Sync is F_FULLFSYNC on macOS).
func syncTree(dir string) error {
	return syncEachFile(dir)
}

// dropFileCache: no page cache advice here (Linux only).
func dropFileCache(string) {}

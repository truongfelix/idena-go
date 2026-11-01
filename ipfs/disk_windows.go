//go:build !idena_memory_ipfs

package ipfs

import (
	"os"

	"golang.org/x/sys/windows"
)

// diskFreeBytes: the bytes free for this user on the disk holding dir.
func diskFreeBytes(dir string) (int64, error) {
	path, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(path, &free, nil, nil); err != nil {
		return 0, err
	}
	return int64(free), nil
}

// syncsFilesystem: syncCopied needs the list of the block files written.
const syncsFilesystem = false

// syncCopied puts the block files written since the last call on disk, one by one.
func syncCopied(_ string, blocks []string) error {
	return syncFiles(blocks)
}

// fsyncFile puts one file on disk (FlushFileBuffers).
func fsyncFile(f *os.File) error {
	return f.Sync()
}

// syncTree puts every file under dir on disk, one by one (File.Sync is FlushFileBuffers on Windows).
func syncTree(dir string) error {
	return syncEachFile(dir)
}

// dropFileCache: no page cache advice here (Linux only).
func dropFileCache(string) {}

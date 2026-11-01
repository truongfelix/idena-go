//go:build !idena_memory_ipfs

package ipfs

import "golang.org/x/sys/windows"

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

// canDropFileCache: dropFileCache drops pages here.
const canDropFileCache = false

// syncTree puts every file under dir on disk, one by one (File.Sync is FlushFileBuffers on Windows).
func syncTree(dir string) error {
	return syncEachFile(dir)
}

// dropFileCache: no page cache advice here (Linux only).
func dropFileCache(string) {}

//go:build !idena_memory_ipfs

package ipfs

import (
	"io/fs"
	"os"
	"path/filepath"
)

// syncEachFile syncs every regular file under dir.
func syncEachFile(dir string) error {
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		// A read-only file (flatfs's _README, 0444) cannot be opened for the sync that Windows needs a writable
		// handle for; nothing the copy writes is read-only.
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0200 == 0 {
			return nil
		}
		return syncFile(path)
	})
}

// syncFile puts a file on disk (Windows needs a writable handle for it).
func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	syncErr := fsyncFile(f)
	if err := f.Close(); syncErr == nil {
		syncErr = err
	}
	return syncErr
}

// syncFiles syncs the files named.
func syncFiles(paths []string) error {
	for _, path := range paths {
		if err := syncFile(path); err != nil {
			return err
		}
	}
	return nil
}

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
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		syncErr := f.Sync()
		if err := f.Close(); syncErr == nil {
			syncErr = err
		}
		return syncErr
	})
}

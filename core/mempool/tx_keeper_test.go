package mempool

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTxKeeperLoadIgnoresOpenFileError(t *testing.T) {
	datadir := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(datadir, []byte("file"), 0600))

	keeper := NewTxKeeper(datadir)

	require.NotPanics(t, keeper.Load)
	require.Equal(t, 0, keeper.Len())
}

func TestTxKeeperOpenFileCreatesPrivateStorage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX file mode bits")
	}

	datadir := t.TempDir()
	keeper := NewTxKeeper(datadir)

	file, err := keeper.openFile()
	require.NoError(t, err)
	require.NoError(t, file.Close())

	assertMode(t, filepath.Join(datadir, Folder), 0700)
	assertMode(t, filepath.Join(datadir, Folder, "txs.json"), 0600)
}

func assertMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, mode, info.Mode().Perm())
}

func TestTxKeeper_StopEndsItsSaves(t *testing.T) {
	datadir := t.TempDir()
	k := NewTxKeeper(datadir)
	k.Load()
	k.Stop()

	// After Stop, a pending change is never saved: the folder stays removed.
	require.NoError(t, os.RemoveAll(filepath.Join(datadir, Folder)))
	k.markHasChanges()
	time.Sleep(300 * time.Millisecond)
	_, err := os.Stat(filepath.Join(datadir, Folder))
	require.True(t, os.IsNotExist(err), "the stopped keeper wrote its folder again: %v", err)
}

func TestTxKeeper_StopTwiceAndBeforeLoad(t *testing.T) {
	k := NewTxKeeper(t.TempDir())
	k.Stop() // never loaded: nothing to wait for
	k.Stop()
}

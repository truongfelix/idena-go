package mempool

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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

package state

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/state/snapshot"
	"github.com/idena-network/idena-go/ipfs"
	"github.com/idena-network/idena-go/log"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

func TestSnapshotManager_IsInvalidManifest(t *testing.T) {
	m := SnapshotManager{
		db: db.NewMemDB(),
	}
	m.AddInvalidManifest([]byte{0x1})

	m.AddTimeoutManifest([]byte{0x3})
	m.AddTimeoutManifest([]byte{0x3})
	m.AddTimeoutManifest([]byte{0x3})
	m.AddTimeoutManifest([]byte{0x3})
	m.AddTimeoutManifest([]byte{0x3})

	m.AddTimeoutManifest([]byte{0x4})
	m.AddTimeoutManifest([]byte{0x4})
	m.AddTimeoutManifest([]byte{0x4})
	m.AddTimeoutManifest([]byte{0x4})

	require.True(t, m.IsInvalidManifest([]byte{0x1}))
	require.False(t, m.IsInvalidManifest([]byte{0x2}))
	require.True(t, m.IsInvalidManifest([]byte{0x3}))
	require.False(t, m.IsInvalidManifest([]byte{0x4}))
}

func TestCreateSnapshotFileCreatesPrivateStorage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX file mode bits")
	}

	datadir := t.TempDir()

	fileName, file, err := createSnapshotFile(datadir, 123, SnapshotVersionV2)
	require.NoError(t, err)
	require.NoError(t, file.Close())

	require.True(t, strings.HasPrefix(fileName, filepath.Join(datadir, SnapshotsFolder)))
	assertMode(t, filepath.Join(datadir, SnapshotsFolder), 0700)
	assertMode(t, fileName, 0600)
}

func TestClearSnapshotFolderHandlesMissingDirectory(t *testing.T) {
	m := SnapshotManager{
		cfg: &config.Config{DataDir: t.TempDir()},
		log: log.New(),
	}

	require.NotPanics(t, func() {
		m.clearSnapshotFolder(nil)
	})
}

func assertMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, mode, info.Mode().Perm())
}

// slowIpfs serves a snapshot a few bytes at a time until the download is cancelled.
type slowIpfs struct {
	ipfs.Proxy
}

func (slowIpfs) LoadTo(_ []byte, _ io.Writer, ctx context.Context, onLoading func(size, loaded int64)) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	giveUp := time.After(30 * time.Second)
	var loaded int64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-giveUp:
			return errors.New("the download was not cancelled")
		case <-ticker.C:
			loaded += 512
			onLoading(1<<30, loaded)
		}
	}
}

// A download that keeps receiving data stops at MaxSnapshotDownloadTime: the idle timeout never ends it.
func TestDownloadSnapshotStopsAtTheTimeLimit(t *testing.T) {
	defer func(saved time.Duration) { MaxSnapshotDownloadTime = saved }(MaxSnapshotDownloadTime)
	MaxSnapshotDownloadTime = 2 * time.Second
	m := &SnapshotManager{cfg: &config.Config{DataDir: t.TempDir()}, ipfs: slowIpfs{}, log: log.New()}

	started := time.Now()
	_, _, err := m.DownloadSnapshot(&snapshot.Manifest{Height: 100, CidV2: []byte{0x1}})

	require.ErrorIs(t, err, context.Canceled)
	require.GreaterOrEqual(t, time.Since(started), MaxSnapshotDownloadTime)
	require.Less(t, time.Since(started), 10*time.Second)
}

package state

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/state/snapshot"
	"github.com/idena-network/idena-go/database"
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

// chunkIpfs serves data in 512-byte writes and announces size as the snapshot's size, until the download is cancelled.
type chunkIpfs struct {
	ipfs.Proxy
	data     []byte
	size     int64
	unpinned [][]byte
}

func (c *chunkIpfs) LoadTo(_ []byte, to io.Writer, ctx context.Context, onLoading func(size, loaded int64)) error {
	for loaded := 0; loaded < len(c.data); loaded += 512 {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := loaded + 512
		if end > len(c.data) {
			end = len(c.data)
		}
		if _, err := to.Write(c.data[loaded:end]); err != nil {
			return err
		}
		onLoading(c.size, int64(end))
	}
	return nil
}

func (c *chunkIpfs) Unpin(key []byte) error {
	c.unpinned = append(c.unpinned, key)
	return nil
}

func newDownloadTestManager(t *testing.T, node ipfs.Proxy) (*SnapshotManager, string) {
	t.Helper()
	chainDb := db.NewMemDB()
	datadir := t.TempDir()
	previousFile := filepath.Join(datadir, SnapshotsFolder, "100.2.tar")
	require.NoError(t, os.MkdirAll(filepath.Dir(previousFile), 0700))
	require.NoError(t, os.WriteFile(previousFile, []byte("previous snapshot"), 0600))
	require.NoError(t, database.NewRepo(chainDb).WriteLastSnapshotManifest([]byte("previous"), common.Hash{0x1}, 100,
		previousFile))
	return &SnapshotManager{db: chainDb, repo: database.NewRepo(chainDb), cfg: &config.Config{DataDir: datadir}, ipfs: node,
		log: log.New()}, previousFile
}

func requirePreviousSnapshotKept(t *testing.T, m *SnapshotManager, previousFile string) {
	t.Helper()
	cid, _, height, fileName := m.repo.LastSnapshotManifest()
	require.Equal(t, []byte("previous"), cid)
	require.Equal(t, uint64(100), height)
	require.Equal(t, previousFile, fileName)
	require.FileExists(t, previousFile)
	entries, err := os.ReadDir(filepath.Dir(previousFile))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

// A snapshot larger than MaxSnapshotSize stops its download, whether it announces its size or not, and the node
// keeps the snapshot it has.
func TestDownloadSnapshotStopsAboveTheSizeLimit(t *testing.T) {
	defer func(saved int64) { MaxSnapshotSize = saved }(MaxSnapshotSize)
	MaxSnapshotSize = 4096
	for _, announced := range []int64{0, 4096, 1 << 40} {
		t.Run(fmt.Sprint(announced), func(t *testing.T) {
			node := &chunkIpfs{data: make([]byte, 8192), size: announced}
			m, previousFile := newDownloadTestManager(t, node)

			_, _, err := m.DownloadSnapshot(&snapshot.Manifest{Height: 200, CidV2: []byte("new")})

			require.ErrorIs(t, err, ErrSnapshotTooLarge)
			requirePreviousSnapshotKept(t, m, previousFile)
			require.Empty(t, node.unpinned)
		})
	}

	t.Run("at the limit", func(t *testing.T) {
		node := &chunkIpfs{data: make([]byte, 4096), size: 4096}
		m, _ := newDownloadTestManager(t, node)
		_, _, err := m.DownloadSnapshot(&snapshot.Manifest{Height: 200, CidV2: []byte("new")})
		require.NoError(t, err)
	})
}

// A downloaded snapshot replaces the node's own only once it is promoted, after its root was checked.
func TestDownloadedSnapshotIsKeptOnlyOncePromoted(t *testing.T) {
	node := &chunkIpfs{data: []byte("new snapshot"), size: 12}
	m, previousFile := newDownloadTestManager(t, node)
	manifest := &snapshot.Manifest{Height: 200, CidV2: []byte("new"), Root: common.Hash{0x2}}

	filePath, _, err := m.DownloadSnapshot(manifest)
	require.NoError(t, err)
	data, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, []byte("new snapshot"), data)
	cid, _, _, _ := m.repo.LastSnapshotManifest()
	require.Equal(t, []byte("previous"), cid, "not before the promotion")
	require.FileExists(t, previousFile)
	require.Empty(t, node.unpinned)

	require.NoError(t, m.PromoteSnapshot(manifest, filePath))

	cid, root, height, fileName := m.repo.LastSnapshotManifest()
	require.Equal(t, []byte("new"), cid)
	require.Equal(t, common.Hash{0x2}, root)
	require.Equal(t, uint64(200), height)
	require.Equal(t, [][]byte{[]byte("previous")}, node.unpinned)
	require.NoFileExists(t, previousFile)
	data, err = os.ReadFile(fileName)
	require.NoError(t, err)
	require.Equal(t, []byte("new snapshot"), data)
	entries, err := os.ReadDir(filepath.Dir(fileName))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

// A download of the height the node made its own snapshot at does not overwrite that snapshot's file.
func TestDownloadSnapshotKeepsTheOwnSnapshotOfTheSameHeight(t *testing.T) {
	node := &chunkIpfs{data: []byte("other bytes"), size: 11}
	m, previousFile := newDownloadTestManager(t, node)

	filePath, _, err := m.DownloadSnapshot(&snapshot.Manifest{Height: 100, CidV2: []byte("other")})
	require.NoError(t, err)
	require.NotEqual(t, previousFile, filePath)
	data, err := os.ReadFile(previousFile)
	require.NoError(t, err)
	require.Equal(t, []byte("previous snapshot"), data)

	m.DiscardSnapshot(filePath)
	requirePreviousSnapshotKept(t, m, previousFile)
}

//go:build !idena_memory_ipfs

package ipfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/tink/go/subtle/random"
	ds "github.com/ipfs/go-datastore"
	levelds "github.com/ipfs/go-ds-leveldb"
	ipfsConf "github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/plugin"
	kubolevelds "github.com/ipfs/kubo/plugin/plugins/levelds"
	"github.com/stretchr/testify/require"
)

func TestLeveldsDiskSpecMatchesKubo(t *testing.T) {
	kubo := kubolevelds.Plugins[0].(plugin.PluginDatastore)
	require.Equal(t, kuboLevelDsPlugin, kubo.Name())
	require.Equal(t, kubo.DatastoreTypeName(), (&leveldsPlugin{}).DatastoreTypeName())

	for _, params := range []map[string]any{
		{"type": "levelds", "path": "datastore", "compression": "none"},
		{"type": "levelds", "path": "datastore"},
	} {
		want, err := kubo.DatastoreConfigParser()(params)
		require.NoError(t, err)
		got, err := (&leveldsPlugin{writeBufferMiB: 16}).DatastoreConfigParser()(params)
		require.NoError(t, err)
		require.Equal(t, want.DiskSpec(), got.DiskSpec())
	}
}

func TestLeveldsWriteBuffer(t *testing.T) {
	// 10 MiB of new data: two memtable rotations or more with the default 4 MiB buffer, none with 16 MiB. goleveldb
	// asks for the flush of a rotated memtable without waiting, and the request is lost when its compaction goroutine
	// is not ready for it (seen on a busy CI runner); the next rotation flushes it before going on. So two
	// rotations leave a level-0 table when the writes return, whatever the scheduling.
	for _, tc := range []struct {
		writeBufferMiB int
		flushed        bool
	}{{0, true}, {16, false}} {
		cfg, err := (&leveldsPlugin{writeBufferMiB: tc.writeBufferMiB}).DatastoreConfigParser()(map[string]any{
			"path": "datastore", "compression": "none",
		})
		require.NoError(t, err)
		store, err := cfg.Create(t.TempDir())
		require.NoError(t, err)
		db := store.(*levelds.Datastore).DB

		ctx := context.Background()
		for i := 0; i < 10; i++ {
			batch, err := store.(ds.Batching).Batch(ctx)
			require.NoError(t, err)
			for j := 0; j < 1024; j++ {
				require.NoError(t, batch.Put(ctx, ds.RandomKey(), random.GetRandomBytes(1024)))
			}
			require.NoError(t, batch.Commit(ctx))
		}

		tables, err := db.GetProperty("leveldb.num-files-at-level0")
		require.NoError(t, err)
		if tc.flushed {
			require.NotEqual(t, "0", tables)
		} else {
			require.Equal(t, "0", tables)
		}
		require.NoError(t, store.Close())
	}
}

func TestNewPluginLoaderLeavesNoConfig(t *testing.T) {
	// An older idena-go reads <ipfs>/plugins/config too: the file disabling kubo's LevelDB plugin must not stay.
	dir := filepath.Join(t.TempDir(), "plugins")
	_, err := newPluginLoader(dir)
	require.NoError(t, err)
	_, err = os.Stat(dir)
	require.True(t, os.IsNotExist(err))

	// A directory holding something else stays, without the file.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "plugins"), 0o700))
	_, err = newPluginLoader(dir)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, ipfsConf.DefaultConfigFile))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(dir, "plugins"))
	require.NoError(t, err)
}

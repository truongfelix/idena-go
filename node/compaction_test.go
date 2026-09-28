package node

import (
	"encoding/binary"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/tendermint/tm-db"
	"math/rand"
	"testing"
)

func TestShareAboveDeepestLevel(t *testing.T) {
	for _, c := range []struct {
		sizes    leveldb.Sizes
		expected float64
	}{
		{nil, 0},
		{leveldb.Sizes{0, 0, 0}, 0},
		{leveldb.Sizes{50, 0, 0}, 0},
		{leveldb.Sizes{0, 0, 100, 0}, 0},
		{leveldb.Sizes{10, 0, 90}, 0.1},
		{leveldb.Sizes{5, 20, 25, 50, 0, 0}, 0.5},
		{leveldb.Sizes{30, 70, 0}, 0.3},
	} {
		require.InDelta(t, c.expected, shareAboveDeepestLevel(c.sizes), 1e-9, "%v", c.sizes)
	}
}

func TestOpenDatabaseCompactsOnlyWithEnoughDataAboveDeepestLevel(t *testing.T) {
	dir := t.TempDir()
	rnd := rand.New(rand.NewSource(1))
	const keys = 4000
	write := func(d db.DB, count int) {
		batch := d.NewBatch()
		for i := 0; i < count; i++ {
			key := make([]byte, 8)
			binary.BigEndian.PutUint64(key, uint64(rnd.Intn(keys)))
			value := make([]byte, 256)
			rnd.Read(value)
			require.NoError(t, batch.Set(key, value))
		}
		require.NoError(t, batch.Write())
		require.NoError(t, batch.Close())
	}
	// open reopens the database; writes kept in the journal are flushed to level 0 on open.
	open := func(compact bool) (db.DB, float64) {
		d, err := OpenDatabase(dir, "test", 16, 16, compact)
		require.NoError(t, err)
		var stats leveldb.DBStats
		require.NoError(t, d.(*db.GoLevelDB).DB().Stats(&stats))
		return d, shareAboveDeepestLevel(stats.LevelSizes)
	}

	d, _ := open(false)
	write(d, keys)
	require.NoError(t, d.(*db.GoLevelDB).ForceCompact(nil, nil))
	write(d, keys/2)
	require.NoError(t, d.Close())

	// Enough data above the deepest level: the database is compacted on open.
	d, share := open(false)
	require.Greater(t, share, minShareAboveDeepestLevel)
	require.NoError(t, d.Close())
	d, share = open(true)
	require.Zero(t, share)
	write(d, keys/100)
	require.NoError(t, d.Close())

	// Little data above the deepest level: the full compaction is skipped.
	d, share = open(false)
	require.Greater(t, share, 0.0)
	require.Less(t, share, minShareAboveDeepestLevel)
	require.NoError(t, d.Close())
	d, shareAfterOpen := open(true)
	require.Equal(t, share, shareAfterOpen)
	require.NoError(t, d.Close())
}

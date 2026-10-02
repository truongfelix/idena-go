package node

import (
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/tendermint/tm-db"
)

// The write buffer is chosen at each open: data written with one size and left in the journal (a stop without a
// clean close) is replayed into level-0 tables of the size used at the next open, in both directions, and no write
// is lost.
func TestOpenDatabaseWithWriteBufferChangesBetweenOpens(t *testing.T) {
	dir := t.TempDir()
	rnd := rand.New(rand.NewSource(1))
	written := map[string][]byte{}
	// write adds mib MiB of incompressible 1 KiB values, in 1 MiB batches.
	write := func(d db.DB, mib int) {
		for b := 0; b < mib; b++ {
			batch := d.NewBatch()
			for i := 0; i < 1024; i++ {
				key := make([]byte, 8)
				binary.BigEndian.PutUint64(key, rnd.Uint64())
				value := make([]byte, 1024)
				rnd.Read(value)
				require.NoError(t, batch.Set(key, value))
				written[string(key)] = value
			}
			require.NoError(t, batch.Write())
			require.NoError(t, batch.Close())
		}
	}
	stats := func(d db.DB) leveldb.DBStats {
		var s leveldb.DBStats
		require.NoError(t, d.(*db.GoLevelDB).DB().Stats(&s))
		return s
	}
	open := func(writeBufferMiB int) db.DB {
		d, err := OpenDatabaseWithWriteBuffer(dir, "test", 16, 16, writeBufferMiB, false)
		require.NoError(t, err)
		return d
	}

	// 16 MiB: 10 MiB of writes stay in the memtable, no flush.
	d := open(16)
	write(d, 10)
	require.Zero(t, stats(d).MemComp)
	require.NoError(t, d.Close())

	// Down to 4 MiB: the 10 MiB journal is replayed into several level-0 tables, then writes flush every 4 MiB.
	d = open(4)
	require.GreaterOrEqual(t, stats(d).LevelTablesCounts[0], 2)
	write(d, 10)
	require.GreaterOrEqual(t, stats(d).MemComp, uint32(2))
	require.NoError(t, d.Close())

	// Up to 64 MiB: no flush for the next 10 MiB.
	d = open(64)
	before := stats(d).MemComp
	write(d, 10)
	require.Equal(t, before, stats(d).MemComp)
	require.NoError(t, d.Close())

	// Default size: everything written under the three sizes is there.
	d = open(0)
	for key, value := range written {
		got, err := d.Get([]byte(key))
		require.NoError(t, err)
		require.Equal(t, value, got)
	}
	require.NoError(t, d.Close())
}

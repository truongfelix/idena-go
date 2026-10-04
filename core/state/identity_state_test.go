package state

import (
	"crypto/rand"
	"github.com/idena-network/idena-go/common"
	"github.com/stretchr/testify/require"
	"github.com/tendermint/tm-db"
	"testing"
)

func getRandAddr() common.Address {
	bytes := make([]byte, 20)
	rand.Read(bytes)
	return common.BytesToAddress(bytes)
}

func TestIdentityStateDB_AddDiff(t *testing.T) {

	database := db.NewMemDB()
	database2 := db.NewMemDB()

	stateDb, _ := NewLazyIdentityState(database)
	stateDb2, _ := NewLazyIdentityState(database2)

	diffs := make([]*IdentityStateDiff, 0)

	forDeleteAddrs := make([]common.Address, 0, 10)

	for i := 0; i < 100; i++ {

		for _, delAddr := range forDeleteAddrs {
			stateDb.Remove(delAddr)
		}

		forDeleteAddrs = make([]common.Address, 0, 10)

		for j := 0; j < 50; j++ {
			addr := getRandAddr()
			if j < 10 {
				forDeleteAddrs = append(forDeleteAddrs, addr)
			}
			stateDb.SetValidated(addr, true)
		}

		diffs = append(diffs, stateDb.Precommit(true))
		stateDb.Commit(true)
	}

	i := int64(1)
	for _, d := range diffs {
		stateDb2.AddDiff(uint64(i), d)
		stateDb2.CommitTree(i)
		i++
	}

	require.Equal(t, stateDb.Root(), stateDb2.Root())
}

func TestIdentityStateDB_CreatePreliminaryCopy(t *testing.T) {
	stateDb := createStateDb()

	preliminary, err := stateDb.CreatePreliminaryCopy(100)
	require.NoError(t, err)
	require.Equal(t, preliminary.original, stateDb.original)

	require.Equal(t, stateDb.Root(), preliminary.Root())

	preliminary.SetValidated(getRandAddr(), true)
	_, _, _, err = preliminary.Commit(true)
	require.NoError(t, err)

	require.Error(t, stateDb.Load(101))

	preliminaryPrefix, _ := IdentityStateDbKeys.LoadDbPrefix(preliminary.original, true)
	prefix, _ := IdentityStateDbKeys.LoadDbPrefix(stateDb.original, false)

	require.NotNil(t, preliminaryPrefix)
	require.NotNil(t, prefix)

	require.NotEqual(t, preliminaryPrefix, prefix)

	preliminary.DropPreliminary()

	it, _ := preliminary.db.Iterator(nil, nil)
	defer it.Close()
	require.False(t, it.Valid())

	require.True(t, stateDb.HasVersion(100))

	preliminaryPrefix, _ = IdentityStateDbKeys.LoadDbPrefix(preliminary.original, true)
	prefix, _ = IdentityStateDbKeys.LoadDbPrefix(stateDb.original, false)

	require.Len(t, preliminaryPrefix, 0)
	require.NotNil(t, prefix)
}

func createStateDb() *IdentityStateDB {

	database := db.NewMemDB()
	stateDb, _ := NewLazyIdentityState(database)

	for i := 0; i < 100; i++ {
		for j := 0; j < 50; j++ {
			addr := getRandAddr()
			stateDb.SetValidated(addr, true)
		}
		stateDb.Commit(true)
	}
	return stateDb
}

func TestIdentityStateDB_SwitchToPreliminary(t *testing.T) {
	stateDb := createStateDb()
	database := stateDb.db
	preliminary, _ := stateDb.CreatePreliminaryCopy(100)
	for i := 0; i < 50; i++ {
		preliminary.SetValidated(getRandAddr(), true)
		preliminary.Commit(true)
	}

	root := preliminary.Root()

	prevVreliminaryPrefix, _ := IdentityStateDbKeys.LoadDbPrefix(preliminary.original, true)

	batch, dropDb, err := stateDb.SwitchToPreliminary(150)
	require.NoError(t, err)
	batch.WriteSync()
	common.ClearDb(dropDb)
	preliminaryPrefix, _ := IdentityStateDbKeys.LoadDbPrefix(preliminary.original, true)
	prefix, _ := IdentityStateDbKeys.LoadDbPrefix(stateDb.original, false)

	require.Len(t, preliminaryPrefix, 0)
	require.NotNil(t, prefix)
	require.Equal(t, prevVreliminaryPrefix, prefix)

	it, _ := database.Iterator(nil, nil)
	defer it.Close()
	require.False(t, it.Valid())

	require.Equal(t, root, stateDb.Root())
}

func TestStateDB_Precommit(t *testing.T) {
	stateDb := createStateDb()
	addr := common.Address{0x1}
	addr2 := common.Address{0x2}
	stateDb.SetValidated(addr, true)
	stateDb.SetValidated(addr2, true)
	diff := stateDb.Precommit(true)
	require.Len(t, diff.Values, 2)
	require.Equal(t, addr, diff.Values[1].Address)
	require.Equal(t, addr2, diff.Values[0].Address)
	require.False(t, diff.Values[1].Deleted)
	require.False(t, diff.Values[0].Deleted)

	stateDb.Commit(true)

	addr3 := common.Address{0x3}
	stateDb.SetValidated(addr3, true)
	stateDb.Remove(addr2)

	diff = stateDb.Precommit(true)
	require.Len(t, diff.Values, 2)
	require.Equal(t, addr2, diff.Values[1].Address)
	require.Equal(t, addr3, diff.Values[0].Address)
	require.True(t, diff.Values[1].Deleted)
	require.False(t, diff.Values[0].Deleted)
}

func countKeys(t *testing.T, database db.DB) int {
	it, err := database.Iterator(nil, nil)
	require.NoError(t, err)
	defer it.Close()
	n := 0
	for ; it.Valid(); it.Next() {
		n++
	}
	return n
}

// With no copy for a fast sync, dropping it must change nothing. The stored prefix is unset on a node that
// never fast synced: LoadDbPrefix reads it as prefix 0, the identity state itself. It is empty after a copy was
// dropped or became the identity state: the prefix of the whole database.
func TestIdentityStateDB_DropPreliminaryWithoutCopy(t *testing.T) {
	stateDb := createStateDb()
	database := stateDb.original
	require.NoError(t, database.Set([]byte("chain data"), []byte{0x1}))
	current, err := IdentityStateDbKeys.LoadDbPrefix(database, false)
	require.NoError(t, err)
	root := stateDb.Root()

	for _, c := range []struct {
		name   string
		stored []byte
	}{
		{"never set", nil},
		{"empty", []byte{}},
		{"the identity state's", current},
		{"not a copy's", identityStateDbPrefixBytes},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.stored == nil {
				require.NoError(t, database.Delete(preliminaryIdentityStateDbPrefixKey))
			} else {
				require.NoError(t, database.Set(preliminaryIdentityStateDbPrefixKey, c.stored))
			}
			keys := countKeys(t, database)

			require.False(t, stateDb.HasPreliminary())
			stateDb.DropPreliminary()

			require.Equal(t, keys, countKeys(t, database))
			require.NoError(t, stateDb.Load(100))
			require.Equal(t, root, stateDb.Root())
		})
	}
}

// A new copy for a fast sync replaces a copy left by an earlier one (the node restarted before that fast sync
// stored a header): under another prefix it would be left behind for good, under the same prefix the new copy
// would get its versions.
func TestIdentityStateDB_CreatePreliminaryCopyDropsStaleCopy(t *testing.T) {
	t.Run("other prefix", func(t *testing.T) {
		stateDb := createStateDb()
		stale, err := stateDb.CreatePreliminaryCopy(100)
		require.NoError(t, err)
		stateDb.SetValidated(getRandAddr(), true)
		stateDb.Commit(true)

		fresh, err := stateDb.CreatePreliminaryCopy(101)
		require.NoError(t, err)

		require.Zero(t, countKeys(t, stale.db))
		require.True(t, stateDb.HasPreliminary())
		require.Equal(t, stateDb.Root(), fresh.Root())
	})
	t.Run("same prefix", func(t *testing.T) {
		stateDb := createStateDb()
		stale, err := stateDb.CreatePreliminaryCopy(100)
		require.NoError(t, err)
		stale.SetValidated(getRandAddr(), true)
		stale.Commit(true)
		require.True(t, stale.HasVersion(101))

		fresh, err := stateDb.CreatePreliminaryCopy(100)
		require.NoError(t, err)

		require.False(t, fresh.HasVersion(101))
		require.Equal(t, stateDb.Root(), fresh.Root())
	})
}

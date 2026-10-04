package state

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tm-db"
)

func setKeys(t *testing.T, database db.DB, prefix []byte, count int) {
	for i := 0; i < count; i++ {
		require.NoError(t, database.Set(append(append([]byte{}, prefix...), byte(i)), []byte{0x1}))
	}
}

// The data in use stays: the current state, the current identity state and the identity state copy of a fast
// sync whose headers are kept. The rest of the state and identity state data goes: a snapshot whose loading was
// interrupted, and the identity state copy of a fast sync abandoned by an older version.
func TestDropOrphanedPrefixes(t *testing.T) {
	identityState := createStateDb()
	database := identityState.original
	identityRoot := identityState.Root()
	preliminary, err := identityState.CreatePreliminaryCopy(100)
	require.NoError(t, err)
	currentState, err := StateDbKeys.LoadDbPrefix(database)
	require.NoError(t, err)
	setKeys(t, database, currentState, 10)
	require.NoError(t, database.Set([]byte("h-chain-data"), []byte{0x1}))

	interruptedSnapshot := StateDbKeys.BuildDbPrefix(11370095)
	setKeys(t, database, interruptedSnapshot, 20)
	abandonedCopy := IdentityStateDbKeys.buildDbPrefix(51)
	setKeys(t, database, abandonedCopy, 30)
	keys := countKeys(t, database)

	dropped, err := DropOrphanedPrefixes(database)

	require.NoError(t, err)
	require.Equal(t, [][]byte{interruptedSnapshot, abandonedCopy}, dropped)
	require.Equal(t, keys-50, countKeys(t, database))
	require.Zero(t, countKeys(t, db.NewPrefixDB(database, interruptedSnapshot)))
	require.Zero(t, countKeys(t, db.NewPrefixDB(database, abandonedCopy)))
	require.Equal(t, 10, countKeys(t, db.NewPrefixDB(database, currentState)))
	require.NoError(t, identityState.Load(100))
	require.Equal(t, identityRoot, identityState.Root())
	require.True(t, identityState.HasPreliminary())
	loaded, err := identityState.LoadPreliminary(100)
	require.NoError(t, err)
	require.Equal(t, preliminary.Root(), loaded.Root())
	has, err := database.Has([]byte("h-chain-data"))
	require.NoError(t, err)
	require.True(t, has)

	// Nothing is left to drop.
	dropped, err = DropOrphanedPrefixes(database)
	require.NoError(t, err)
	require.Empty(t, dropped)
}

// Without its current prefix stored, nothing tells which data of a space is in use: the space is left as it is.
func TestDropOrphanedPrefixesKeepsSpaceWithoutCurrentPrefix(t *testing.T) {
	for _, stored := range [][]byte{nil, {}, {0x2}} {
		database := db.NewMemDB()
		if stored != nil {
			require.NoError(t, database.Set(currentIdentityStateDbPrefixKey, stored))
		}
		setKeys(t, database, IdentityStateDbKeys.buildDbPrefix(0), 10)
		setKeys(t, database, IdentityStateDbKeys.buildDbPrefix(51), 10)
		setKeys(t, database, StateDbKeys.BuildDbPrefix(0), 10)
		keys := countKeys(t, database)

		dropped, err := DropOrphanedPrefixes(database)

		require.NoError(t, err)
		require.Empty(t, dropped)
		require.Equal(t, keys, countKeys(t, database))
	}
}

func TestNextKey(t *testing.T) {
	require.Equal(t, []byte{0x3}, nextKey([]byte{0x2}))
	require.Equal(t, []byte{0x2, 0x1, 0x3}, nextKey([]byte{0x2, 0x1, 0x2}))
	require.Equal(t, []byte{0x2, 0x2}, nextKey([]byte{0x2, 0x1, 0xff}))
	require.Equal(t, []byte{0x3}, nextKey([]byte{0x2, 0xff, 0xff}))
	require.Nil(t, nextKey([]byte{0xff, 0xff}))
}

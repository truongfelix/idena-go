package state

import (
	"math/big"
	"testing"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/crypto"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

func newStateWithIdentities(t *testing.T, count int) (*StateDB, []common.Address) {
	stateDb, err := NewLazy(db.NewMemDB())
	require.NoError(t, err)
	addrs := make([]common.Address, 0, count)
	for i := 0; i < count; i++ {
		key, _ := crypto.GenerateKey()
		addr := crypto.PubkeyToAddress(key.PublicKey)
		stateDb.SetState(addr, Verified)
		stateDb.SetBirthday(addr, uint16(i))
		addrs = append(addrs, addr)
	}
	stateDb.Commit(false)
	stateDb.Clear()
	return stateDb, addrs
}

func collectIdentities(s *StateDB) (keys, values [][]byte) {
	s.IterateIdentities(func(key []byte, value []byte) bool {
		keys = append(keys, append([]byte{}, key...))
		values = append(values, append([]byte{}, value...))
		return false
	})
	return keys, values
}

func TestIdentitiesIterationCacheReplaysTreeWalk(t *testing.T) {
	s, _ := newStateWithIdentities(t, 500)
	expectedKeys, expectedValues := collectIdentities(s)
	require.Len(t, expectedKeys, 500)

	s.EnableIdentitiesIterationCache()
	defer s.DisableIdentitiesIterationCache()
	for i := 0; i < 3; i++ {
		keys, values := collectIdentities(s)
		require.Equal(t, expectedKeys, keys)
		require.Equal(t, expectedValues, values)
	}
	require.NotNil(t, s.identitiesIteration)
}

func TestIdentitiesIterationCacheFollowsTreeChanges(t *testing.T) {
	s, addrs := newStateWithIdentities(t, 50)
	s.EnableIdentitiesIterationCache()
	collectIdentities(s)
	require.NotNil(t, s.identitiesIteration)

	s.SetBirthday(addrs[0], 1000)
	key, _ := crypto.GenerateKey()
	s.SetState(crypto.PubkeyToAddress(key.PublicKey), Newbie)
	s.Commit(false)
	s.Clear()

	keys, values := collectIdentities(s)
	s.DisableIdentitiesIterationCache()
	expectedKeys, expectedValues := collectIdentities(s)
	require.Len(t, keys, 51)
	require.Equal(t, expectedKeys, keys)
	require.Equal(t, expectedValues, values)
}

func TestIdentitiesIterationCacheEarlyStop(t *testing.T) {
	s, _ := newStateWithIdentities(t, 50)
	s.EnableIdentitiesIterationCache()
	defer s.DisableIdentitiesIterationCache()

	stopAfter := func(n int) (int, bool) {
		count := 0
		stopped := s.IterateIdentities(func(key []byte, value []byte) bool {
			count++
			return count == n
		})
		return count, stopped
	}
	count, stopped := stopAfter(10)
	require.True(t, stopped)
	require.Equal(t, 10, count)
	require.Nil(t, s.identitiesIteration, "a walk that stopped early must not be kept")

	keys, _ := collectIdentities(s)
	require.Len(t, keys, 50)
	require.NotNil(t, s.identitiesIteration)

	count, stopped = stopAfter(10)
	require.True(t, stopped)
	require.Equal(t, 10, count)
}

func TestIdentitiesIterationCacheIsOffByDefault(t *testing.T) {
	s, _ := newStateWithIdentities(t, 10)
	collectIdentities(s)
	require.Nil(t, s.identitiesIteration)

	s.EnableIdentitiesIterationCache()
	collectIdentities(s)
	require.NotNil(t, s.identitiesIteration)

	s.DisableIdentitiesIterationCache()
	require.Nil(t, s.identitiesIteration)
	collectIdentities(s)
	require.Nil(t, s.identitiesIteration)
}

func TestIterateOverIdentitiesPrefersLiveObjectsWithCache(t *testing.T) {
	s, addrs := newStateWithIdentities(t, 10)
	s.EnableIdentitiesIterationCache()
	defer s.DisableIdentitiesIterationCache()
	collectIdentities(s)

	s.SetBirthday(addrs[3], 777)
	birthdays := make(map[common.Address]uint16)
	s.IterateOverIdentities(func(addr common.Address, identity Identity) {
		birthdays[addr] = identity.Birthday
	})
	require.Len(t, birthdays, 10)
	for i, addr := range addrs {
		expected := uint16(i)
		if i == 3 {
			expected = 777
		}
		require.Equal(t, expected, birthdays[addr])
	}
}

func TestIdentitiesIterationCacheFollowsUnsavedTreeWrites(t *testing.T) {
	s, addrs := newStateWithIdentities(t, 20)
	s.EnableIdentitiesIterationCache()
	defer s.DisableIdentitiesIterationCache()
	collectIdentities(s)

	// Precommit writes the change into the tree without saving a version.
	s.SetBirthday(addrs[5], 555)
	s.Precommit(false)
	s.Clear()

	keys, values := collectIdentities(s)
	s.DisableIdentitiesIterationCache()
	expectedKeys, expectedValues := collectIdentities(s)
	require.Equal(t, expectedKeys, keys)
	require.Equal(t, expectedValues, values)
	require.Equal(t, uint16(555), s.GetIdentity(addrs[5]).Birthday)
}

// Without identities the kept walk is empty: replaying it must report no identity, and the first
// identity written to the tree must show up.
func TestIdentitiesIterationCacheWithoutIdentities(t *testing.T) {
	s, _ := newStateWithIdentities(t, 0)
	s.EnableIdentitiesIterationCache()
	defer s.DisableIdentitiesIterationCache()

	requireNoIdentities := func() {
		for i := 0; i < 2; i++ {
			stopped := s.IterateIdentities(func(key []byte, value []byte) bool {
				t.Fatalf("unexpected identity key %x", key)
				return true
			})
			require.False(t, stopped)
			require.NotNil(t, s.identitiesIteration, "an empty walk is kept too")
			s.IterateOverIdentities(func(addr common.Address, identity Identity) {
				t.Fatalf("unexpected identity %v", addr.Hex())
			})
		}
	}
	// An empty tree, then a tree with an account but no identity.
	requireNoIdentities()
	s.SetBalance(common.Address{0x1}, big.NewInt(1))
	s.Commit(false)
	s.Clear()
	requireNoIdentities()

	key, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey)
	s.SetState(addr, Newbie)
	s.Commit(false)
	s.Clear()
	keys, _ := collectIdentities(s)
	require.Equal(t, [][]byte{StateDbKeys.IdentityKey(addr)}, keys)
}

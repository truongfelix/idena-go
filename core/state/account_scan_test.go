package state

import (
	"math/big"
	"math/rand"
	"runtime"
	"sort"
	"testing"

	"github.com/idena-network/idena-go/common"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

func isSmallBalance(account Account) bool {
	return account.Balance == nil || account.Balance.Cmp(big.NewInt(1000)) == -1
}

type visitedAccount struct {
	addr    common.Address
	balance string
}

// splitVisits separates the accounts loaded in the state (visited first, in map order) from the
// stored ones (visited next, in key order).
func splitVisits(visits []visitedAccount, used map[common.Address]bool) (usedPart []string, storedPart []visitedAccount) {
	for _, v := range visits {
		if used[v.addr] {
			usedPart = append(usedPart, v.addr.Hex()+":"+v.balance)
		} else {
			storedPart = append(storedPart, v)
		}
	}
	sort.Strings(usedPart)
	return usedPart, storedPart
}

func referenceVisits(s *StateDB) []visitedAccount {
	var visits []visitedAccount
	s.IterateOverAccounts(func(addr common.Address, account Account) {
		if isSmallBalance(account) {
			visits = append(visits, visitedAccount{addr, account.Balance.String()})
		}
	})
	return visits
}

func matchingVisits(s *StateDB) []visitedAccount {
	var visits []visitedAccount
	s.IterateOverMatchingAccounts(isSmallBalance, func(addr common.Address, account Account) {
		visits = append(visits, visitedAccount{addr, account.Balance.String()})
	})
	return visits
}

func newStateWithAccounts(t *testing.T, addrs []common.Address, rnd *rand.Rand) *StateDB {
	s, err := NewLazy(db.NewMemDB())
	require.NoError(t, err)
	for _, addr := range addrs {
		s.SetBalance(addr, big.NewInt(rnd.Int63n(3000)))
	}
	s.Commit(false)
	s.Clear()
	return s
}

func randomAddresses(rnd *rand.Rand, count int) []common.Address {
	addrs := make([]common.Address, count)
	for i := range addrs {
		rnd.Read(addrs[i][:])
	}
	return addrs
}

func requireSameVisits(t *testing.T, s *StateDB) {
	used := make(map[common.Address]bool)
	for addr := range s.stateAccounts {
		used[addr] = true
	}
	expectedUsed, expectedStored := splitVisits(referenceVisits(s), used)
	actualUsed, actualStored := splitVisits(matchingVisits(s), used)
	require.Equal(t, expectedUsed, actualUsed)
	require.Equal(t, expectedStored, actualStored)
}

func TestIterateOverMatchingAccountsMatchesSequential(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	addrs := randomAddresses(rnd, 3000)
	s := newStateWithAccounts(t, addrs, rnd)

	// accounts loaded and modified in this state are visited first, from memory
	for _, addr := range addrs[:50] {
		s.SetBalance(addr, big.NewInt(rnd.Int63n(3000)))
	}
	requireSameVisits(t, s)

	usedAccounts := make(map[common.Address]Account)
	for addr, obj := range s.stateAccounts {
		usedAccounts[addr] = obj.data
	}
	matches, ok := s.scanStoredAccountsInParallel(usedAccounts, isSmallBalance)
	require.True(t, ok, "the tree has no unsaved changes, so the parallel scan must be used")
	require.NotEmpty(t, matches)
}

func TestScanStoredAccountsInParallelNeedsSavedTree(t *testing.T) {
	rnd := rand.New(rand.NewSource(2))
	addrs := randomAddresses(rnd, 500)
	s := newStateWithAccounts(t, addrs, rnd)

	// write the tree without saving a version
	s.SetBalance(addrs[0], big.NewInt(5))
	s.Precommit(false)
	s.Clear()

	_, ok := s.scanStoredAccountsInParallel(map[common.Address]Account{}, isSmallBalance)
	require.False(t, ok)
	requireSameVisits(t, s)
}

func TestScanStoredAccountsInParallelCoversRangeBoundaries(t *testing.T) {
	rnd := rand.New(rand.NewSource(3))
	var addrs []common.Address
	for first := 0; first < 256; first++ {
		below, at := common.Address{}, common.Address{byte(first)}
		below[0] = byte(first - 1)
		for i := 1; i < len(below); i++ {
			below[i] = 0xff
		}
		addrs = append(addrs, at, below)
	}
	addrs = append(addrs, common.MinAddr, common.MaxAddr)
	s, err := NewLazy(db.NewMemDB())
	require.NoError(t, err)
	for _, addr := range addrs {
		s.SetBalance(addr, big.NewInt(rnd.Int63n(2000)))
	}
	s.Commit(false)
	s.Clear()

	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(0))
	for _, workers := range []int{2, 3, 7, 16} {
		runtime.GOMAXPROCS(workers)
		_, ok := s.scanStoredAccountsInParallel(map[common.Address]Account{}, isSmallBalance)
		require.True(t, ok)
		requireSameVisits(t, s)
	}
}

package validators

import (
	"bytes"
	"fmt"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/core/state"
	"github.com/stretchr/testify/require"
	"github.com/tendermint/tm-db"
	"math/rand"
	"sort"
	"testing"
)

// sortedInsert is the per-address insert that newSortedAddressesFrom replaced, kept as the
// reference: binary search in the descending list, skip an address already present.
func sortedInsert(list []common.Address, addr common.Address) []common.Address {
	i := sort.Search(len(list), func(i int) bool {
		return bytes.Compare(list[i].Bytes(), addr.Bytes()) <= 0
	})
	if i < len(list) && bytes.Compare(list[i].Bytes(), addr.Bytes()) == 0 {
		return list
	}
	list = append(list, common.Address{})
	copy(list[i+1:], list[i:])
	list[i] = addr
	return list
}

// referenceSortedValidators builds the validators list of v with the reference insert.
func referenceSortedValidators(v *ValidatorsCache) []common.Address {
	list := []common.Address{}
	for _, n := range v.onlineAddresses.ToSlice() {
		addr := n.(common.Address)
		if v.validatedAddresses.Contains(addr) {
			list = sortedInsert(list, addr)
		}
		if pool, ok := v.pools[addr]; ok {
			for _, delegator := range pool.delegators {
				list = sortedInsert(list, delegator)
			}
		}
	}
	return list
}

func TestNewSortedAddressesFromMatchesSortedInsert(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	for _, n := range []int{0, 1, 2, 3, 10, 100, 1000} {
		// A small value range forces duplicates; shared prefixes make comparisons reach the last byte.
		for _, values := range []int{1, 3, 256} {
			for _, order := range []string{"random", "ascending", "descending"} {
				addrs := make([]common.Address, n)
				for i := range addrs {
					addrs[i][0] = byte(rnd.Intn(2))
					addrs[i][common.AddressLength-1] = byte(rnd.Intn(values))
					if values == 256 {
						rnd.Read(addrs[i][1:])
					}
				}
				switch order {
				case "ascending":
					sort.Slice(addrs, func(i, j int) bool { return bytes.Compare(addrs[i][:], addrs[j][:]) < 0 })
				case "descending":
					sort.Slice(addrs, func(i, j int) bool { return bytes.Compare(addrs[i][:], addrs[j][:]) > 0 })
				}

				expected := []common.Address{}
				for _, addr := range addrs {
					expected = sortedInsert(expected, addr)
				}
				input := append([]common.Address(nil), addrs...)
				require.Equal(t, expected, newSortedAddressesFrom(input).list, "n=%v values=%v order=%v", n, values, order)
			}
		}
	}
	require.Equal(t, []common.Address{}, newSortedAddressesFrom(nil).list)
}

func TestValidatorsCache_SortedValidatorsMatchSortedInsert(t *testing.T) {
	rnd := rand.New(rand.NewSource(2))
	randomAddress := func() common.Address {
		var addr common.Address
		rnd.Read(addr[:])
		return addr
	}
	pools := make([]common.Address, 20)
	for i := range pools {
		pools[i] = randomAddress()
	}
	identities := make([]common.Address, 400)
	for i := range identities {
		identities[i] = randomAddress()
	}
	setRandomIdentity := func(identityState *state.IdentityStateDB, addr common.Address, canDelegate bool) {
		identityState.SetValidated(addr, rnd.Intn(4) != 0)
		identityState.SetOnline(addr, rnd.Intn(2) == 0)
		if canDelegate && rnd.Intn(3) == 0 {
			identityState.SetDelegatee(addr, pools[rnd.Intn(len(pools))])
			identityState.SetOnline(addr, false)
		} else {
			identityState.RemoveDelegatee(addr)
		}
	}

	database := db.NewMemDB()
	identityState, _ := state.NewLazyIdentityState(database)
	updatedCache := NewValidatorsCache(identityState, common.Address{0x1})
	updatedCache.Load()
	for _, addr := range pools {
		setRandomIdentity(identityState, addr, false)
	}
	for _, addr := range identities {
		setRandomIdentity(identityState, addr, true)
	}

	for round := 0; round < 5; round++ {
		_, _, diff, err := identityState.Commit(true)
		require.NoError(t, err)

		loadedCache := NewValidatorsCache(identityState, common.Address{0x1})
		loadedCache.Load()
		updatedCache.UpdateFromIdentityStateDiff(diff)

		expected := referenceSortedValidators(loadedCache)
		require.NotEmpty(t, expected)
		require.Equal(t, expected, loadedCache.sortedValidators.list, "round %v", round)
		require.Equal(t, expected, updatedCache.sortedValidators.list, "round %v", round)

		for i := 0; i < 60; i++ {
			if rnd.Intn(4) == 0 {
				setRandomIdentity(identityState, pools[rnd.Intn(len(pools))], false)
			} else {
				setRandomIdentity(identityState, identities[rnd.Intn(len(identities))], true)
			}
		}
		identityState.Remove(identities[rnd.Intn(len(identities))])
	}
}

func BenchmarkSortedValidators(b *testing.B) {
	for _, n := range []int{1000, 10000, 100000} {
		// The identity walk yields addresses in ascending order, the worst case for the insert.
		addrs := make([]common.Address, n)
		for i := range addrs {
			rand.Read(addrs[i][:])
		}
		sort.Slice(addrs, func(i, j int) bool { return bytes.Compare(addrs[i][:], addrs[j][:]) < 0 })

		if n <= 10000 {
			b.Run(fmt.Sprintf("insert/n=%v", n), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					list := []common.Address{}
					for _, addr := range addrs {
						list = sortedInsert(list, addr)
					}
				}
			})
		}
		b.Run(fmt.Sprintf("sort/n=%v", n), func(b *testing.B) {
			input := make([]common.Address, n)
			for i := 0; i < b.N; i++ {
				copy(input, addrs)
				newSortedAddressesFrom(input)
			}
		})
	}
}

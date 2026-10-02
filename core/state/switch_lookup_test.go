package state

import (
	"fmt"
	"github.com/idena-network/idena-go/common"
	models "github.com/idena-network/idena-go/protobuf"
	"github.com/stretchr/testify/require"
	"github.com/tendermint/tm-db"
	"math/rand"
	"testing"
)

// The reference functions below are the list scans that the lookup maps replaced.

func scanHasAddress(list []common.Address, addr common.Address) bool {
	for _, item := range list {
		if item == addr {
			return true
		}
	}
	return false
}

func scanRemoveAddress(list []common.Address, addr common.Address) ([]common.Address, bool) {
	for i := 0; i < len(list); i++ {
		if list[i] == addr {
			return append(list[:i], list[i+1:]...), true
		}
	}
	return list, false
}

func scanDelegationSwitch(list []*Delegation, sender common.Address) *Delegation {
	for _, d := range list {
		if d.Delegator == sender {
			return d
		}
	}
	return nil
}

// switchTestAddresses returns a few addresses, so that random operations hit the same ones often.
func switchTestAddresses() []common.Address {
	addrs := make([]common.Address, 6)
	for i := range addrs {
		addrs[i] = common.Address{byte(i + 1)}
	}
	return addrs
}

// randomAddressList may repeat addresses, like stored data that did not come from the toggles.
func randomAddressList(rnd *rand.Rand, addrs []common.Address) []common.Address {
	list := make([]common.Address, rnd.Intn(8))
	for i := range list {
		list[i] = addrs[rnd.Intn(len(addrs))]
	}
	return list
}

func TestStatusSwitchLookupMatchesScan(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	addrs := switchTestAddresses()
	for trial := 0; trial < 200; trial++ {
		initial := randomAddressList(rnd, addrs)
		touches := 0
		obj := newStatusSwitchObject(IdentityStatusSwitch{Addresses: append([]common.Address(nil), initial...)}, func() { touches++ })
		expected, expectedTouches := append([]common.Address(nil), initial...), 0

		for step := 0; step < 100; step++ {
			addr := addrs[rnd.Intn(len(addrs))]
			switch rnd.Intn(10) {
			case 0:
				obj.Clear()
				expected = []common.Address{}
				expectedTouches++
			case 1:
				obj.add(addr)
				expected = append(expected, addr)
			default:
				obj.ToggleAddress(addr)
				var removed bool
				if expected, removed = scanRemoveAddress(expected, addr); !removed {
					expected = append(expected, addr)
				}
				expectedTouches++
			}
			require.Equal(t, expected, obj.Addresses(), "trial %v step %v", trial, step)
			require.Equal(t, expectedTouches, touches)
			for _, addr := range addrs {
				require.Equal(t, scanHasAddress(expected, addr), obj.HasAddress(addr), "trial %v step %v", trial, step)
			}
		}
	}
}

func TestDelayedOfflinePenaltiesLookupMatchesScan(t *testing.T) {
	rnd := rand.New(rand.NewSource(2))
	addrs := switchTestAddresses()
	for trial := 0; trial < 200; trial++ {
		initial := randomAddressList(rnd, addrs)
		touches := 0
		obj := newDelayedOfflinePenaltiesObject(DelayedPenalties{Identities: append([]common.Address(nil), initial...)}, func() { touches++ })
		expected, expectedTouches := append([]common.Address(nil), initial...), 0

		for step := 0; step < 100; step++ {
			addr := addrs[rnd.Intn(len(addrs))]
			switch rnd.Intn(10) {
			case 0:
				obj.Clear()
				expected = []common.Address{}
			case 1, 2, 3, 4:
				obj.Add(addr)
				expected = append(expected, addr)
			default:
				obj.Remove(addr)
				expected, _ = scanRemoveAddress(expected, addr)
			}
			expectedTouches++
			require.Equal(t, expected, obj.data.Identities, "trial %v step %v", trial, step)
			require.Equal(t, expectedTouches, touches)
			for _, addr := range addrs {
				require.Equal(t, scanHasAddress(expected, addr), obj.Has(addr), "trial %v step %v", trial, step)
			}
		}
	}
}

func TestDelegationSwitchLookupMatchesScan(t *testing.T) {
	rnd := rand.New(rand.NewSource(3))
	addrs := switchTestAddresses()
	delegatees := append(switchTestAddresses(), common.EmptyAddress)
	randomDelegatee := func() common.Address {
		return delegatees[rnd.Intn(len(delegatees))]
	}
	copyDelegations := func(list []*Delegation) []*Delegation {
		result := make([]*Delegation, len(list))
		for i, d := range list {
			result[i] = &Delegation{Delegator: d.Delegator, Delegatee: d.Delegatee}
		}
		return result
	}
	for trial := 0; trial < 200; trial++ {
		// Stored data may repeat a delegator; lookups must keep returning its first entry.
		var initial []*Delegation
		for _, delegator := range randomAddressList(rnd, addrs) {
			initial = append(initial, &Delegation{Delegator: delegator, Delegatee: randomDelegatee()})
		}
		touches := 0
		obj := newDelegationSwitchObject(DelegationSwitch{Delegations: copyDelegations(initial)}, func() { touches++ })
		expected, expectedTouches := copyDelegations(initial), 0

		for step := 0; step < 100; step++ {
			sender := addrs[rnd.Intn(len(addrs))]
			if rnd.Intn(10) == 0 {
				obj.Clear()
				expected = []*Delegation{}
			} else {
				delegatee := randomDelegatee()
				obj.ToggleDelegation(sender, delegatee)
				if d := scanDelegationSwitch(expected, sender); d != nil {
					d.Delegatee = delegatee
				} else {
					expected = append(expected, &Delegation{Delegator: sender, Delegatee: delegatee})
				}
			}
			expectedTouches++
			require.Equal(t, expected, obj.data.Delegations, "trial %v step %v", trial, step)
			require.Equal(t, expectedTouches, touches)
			require.Equal(t, expected, obj.Delegations(), "trial %v step %v", trial, step)
			for _, addr := range addrs {
				require.Equal(t, scanDelegationSwitch(expected, addr), obj.DelegationSwitch(addr), "trial %v step %v", trial, step)
			}
		}
	}
}

func TestSwitchLookupsAfterReload(t *testing.T) {
	stateDb, _ := NewLazy(db.NewMemDB())
	a, b, c, d, e := common.Address{0x1}, common.Address{0x2}, common.Address{0x3}, common.Address{0x4}, common.Address{0x5}

	stateDb.SetPredefinedStatusSwitch(&models.ProtoPredefinedState{
		StatusSwitch: &models.ProtoPredefinedState_StatusSwitch{Addresses: [][]byte{a[:], b[:]}},
	})
	require.True(t, stateDb.HasStatusSwitchAddresses(a))
	stateDb.ToggleStatusSwitchAddress(b)
	stateDb.ToggleStatusSwitchAddress(c)
	stateDb.AddDelayedPenalty(d)
	stateDb.AddDelayedPenalty(d)
	stateDb.ToggleDelegationAddress(e, a)
	_, _, _, err := stateDb.Commit(true)
	require.NoError(t, err)

	// A fresh view decodes the stored lists and builds the lookups from them.
	reloaded, err := stateDb.Readonly(stateDb.Version())
	require.NoError(t, err)
	require.Equal(t, []common.Address{a, c}, reloaded.StatusSwitchAddresses())
	require.True(t, reloaded.HasStatusSwitchAddresses(a))
	require.False(t, reloaded.HasStatusSwitchAddresses(b))
	require.True(t, reloaded.HasStatusSwitchAddresses(c))
	require.True(t, reloaded.HasDelayedOfflinePenalty(d))
	reloaded.RemoveDelayedOfflinePenalty(d)
	require.True(t, reloaded.HasDelayedOfflinePenalty(d))
	reloaded.RemoveDelayedOfflinePenalty(d)
	require.False(t, reloaded.HasDelayedOfflinePenalty(d))
	require.Equal(t, &Delegation{Delegator: e, Delegatee: a}, reloaded.DelegationSwitch(e))
	require.Nil(t, reloaded.DelegationSwitch(a))
}

// The getters return copies: changing what they return must leave the stored lists and the
// lookups built from them in step.
func TestSwitchGettersReturnCopies(t *testing.T) {
	stateDb, _ := NewLazy(db.NewMemDB())
	a, b, c, d := common.Address{0x1}, common.Address{0x2}, common.Address{0x3}, common.Address{0x4}

	require.Empty(t, stateDb.StatusSwitchAddresses())
	require.Zero(t, stateDb.StatusSwitchAddressesCount())
	require.Empty(t, stateDb.DelayedOfflinePenalties())
	require.Zero(t, stateDb.DelayedOfflinePenaltiesCount())
	require.Empty(t, stateDb.Delegations())
	require.Zero(t, stateDb.DelegationsCount())
	require.Nil(t, stateDb.DelegationSwitch(a))

	stateDb.ToggleStatusSwitchAddress(a)
	stateDb.StatusSwitchAddresses()[0] = b
	require.Equal(t, []common.Address{a}, stateDb.StatusSwitchAddresses())
	require.Equal(t, 1, stateDb.StatusSwitchAddressesCount())
	require.True(t, stateDb.HasStatusSwitchAddresses(a))
	require.False(t, stateDb.HasStatusSwitchAddresses(b))
	stateDb.ToggleStatusSwitchAddress(a)
	require.Empty(t, stateDb.StatusSwitchAddresses())
	require.Zero(t, stateDb.StatusSwitchAddressesCount())
	require.False(t, stateDb.HasStatusSwitchAddresses(a))

	stateDb.AddDelayedPenalty(a)
	stateDb.DelayedOfflinePenalties()[0] = b
	require.Equal(t, []common.Address{a}, stateDb.DelayedOfflinePenalties())
	require.Equal(t, 1, stateDb.DelayedOfflinePenaltiesCount())
	require.True(t, stateDb.HasDelayedOfflinePenalty(a))
	require.False(t, stateDb.HasDelayedOfflinePenalty(b))
	stateDb.RemoveDelayedOfflinePenalty(a)
	require.Empty(t, stateDb.DelayedOfflinePenalties())
	require.Zero(t, stateDb.DelayedOfflinePenaltiesCount())
	require.False(t, stateDb.HasDelayedOfflinePenalty(a))

	stateDb.ToggleDelegationAddress(a, c)
	stateDb.Delegations()[0].Delegator = b
	stateDb.Delegations()[0].Delegatee = d
	stateDb.Delegations()[0] = &Delegation{Delegator: b, Delegatee: d}
	stateDb.DelegationSwitch(a).Delegator = b
	stateDb.DelegationSwitch(a).Delegatee = d
	require.Equal(t, []*Delegation{{Delegator: a, Delegatee: c}}, stateDb.Delegations())
	require.Equal(t, 1, stateDb.DelegationsCount())
	require.Equal(t, &Delegation{Delegator: a, Delegatee: c}, stateDb.DelegationSwitch(a))
	require.Nil(t, stateDb.DelegationSwitch(b))
	// A copy taken earlier does not follow later toggles, and the toggle still finds the stored entry.
	before := stateDb.DelegationSwitch(a)
	stateDb.ToggleDelegationAddress(a, common.EmptyAddress)
	require.Equal(t, &Delegation{Delegator: a, Delegatee: c}, before)
	require.Equal(t, []*Delegation{{Delegator: a, Delegatee: common.EmptyAddress}}, stateDb.Delegations())
	require.Equal(t, 1, stateDb.DelegationsCount())
	stateDb.ClearDelegations()
	require.Empty(t, stateDb.Delegations())
	require.Zero(t, stateDb.DelegationsCount())
	require.Nil(t, stateDb.DelegationSwitch(a))
}

// BenchmarkStatusSwitchWindow checks and toggles n distinct addresses, as a status switch range
// with n online status transactions does (validation, then application).
func BenchmarkStatusSwitchWindow(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		addrs := make([]common.Address, n)
		for i := range addrs {
			rand.Read(addrs[i][:])
		}
		b.Run(fmt.Sprintf("scan/n=%v", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				var list []common.Address
				for _, addr := range addrs {
					if !scanHasAddress(list, addr) {
						list = append(list, addr)
					}
				}
			}
		})
		b.Run(fmt.Sprintf("lookup/n=%v", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				obj := newStatusSwitchObject(IdentityStatusSwitch{}, nil)
				for _, addr := range addrs {
					if !obj.HasAddress(addr) {
						obj.ToggleAddress(addr)
					}
				}
			}
		})
	}
}

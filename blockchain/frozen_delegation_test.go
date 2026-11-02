package blockchain

import (
	"crypto/ecdsa"
	"math/big"
	"testing"
	"time"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/blockchain/validation"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/crypto"
	"github.com/idena-network/idena-go/stats/collector"
	"github.com/stretchr/testify/require"
)

// Consensus v13 on a chain where identities delegated to the bridge wallet before the upgrade: after it, the
// delegations are undelegated at the next delegation switch block (a switch to it queued before the upgrade is
// dropped), the wallet is never credited, a delegation to another pool is untouched, and a block proposed by a frozen
// address is refused.
func Test_FrozenPoolIsUndelegatedAfterV13(t *testing.T) {
	chain, appState, txpool, coinbaseKey := NewTestBlockchainWithConfig(true, GetDefaultConsensusConfig(), &config.ValidationConfig{}, nil, -1, -1, 0, 0)
	defer chain.SecStore().Destroy()
	defer validation.SetAppConfig(nil)
	defer state.SetFrozenAddressCheck(nil)

	bridge := common.HexToAddress("0x98D16d7021930b788135dD834983394fF2De9869")
	otherPool := common.Address{0x2}
	coinbase := crypto.PubkeyToAddress(coinbaseKey.PublicKey)
	appState.State.SetState(coinbase, state.Verified)
	appState.State.SetNextValidationTime(time.Date(2099, 01, 01, 00, 00, 00, 0, time.UTC))
	appState.IdentityState.SetValidated(coinbase, true)
	appState.IdentityState.SetOnline(coinbase, true)

	var keys []*ecdsa.PrivateKey
	var addrs []common.Address
	for i := 0; i < 4; i++ {
		key, _ := crypto.GenerateKey()
		addr := crypto.PubkeyToAddress(key.PublicKey)
		keys, addrs = append(keys, key), append(addrs, addr)
		appState.State.SetState(addr, state.Newbie)
		appState.IdentityState.SetValidated(addr, true)
		appState.IdentityState.SetOnline(addr, true)
		appState.State.SetBalance(addr, new(big.Int).Mul(big.NewInt(1000), common.DnaBase))
	}
	validation.SetAppConfig(chain.config)
	state.SetFrozenAddressCheck(func(addr common.Address) bool { return chain.config.Consensus.IsFrozenAddress(addr) })
	appState.Commit(nil)
	appState.ValidatorsCache.Load()
	chain.CommitState()

	delegate := func(key *ecdsa.PrivateKey, to common.Address) error {
		tx, _ := types.SignTx(&types.Transaction{Type: types.DelegateTx, AccountNonce: appState.State.GetNonce(crypto.PubkeyToAddress(key.PublicKey)) + 1,
			To: &to, MaxFee: new(big.Int).Mul(big.NewInt(50), common.DnaBase)}, key)
		return txpool.AddInternalTx(tx)
	}

	// v12: identities 0 and 1 delegate to the bridge wallet, 2 to another pool; the switches apply.
	require.NoError(t, delegate(keys[0], bridge))
	require.NoError(t, delegate(keys[1], bridge))
	require.NoError(t, delegate(keys[2], otherPool))
	chain.GenerateBlocks(1, 0)
	chain.GenerateEmptyBlocks(50)
	require.Equal(t, 2, appState.ValidatorsCache.PoolSize(bridge))
	require.Equal(t, bridge, *appState.State.Delegatee(addrs[0]))

	// Still on v12, identity 3 queues a switch to the wallet, not applied yet.
	require.NoError(t, delegate(keys[3], bridge))
	chain.GenerateBlocks(1, 0)
	require.NotNil(t, appState.State.DelegationSwitch(addrs[3]))

	// The upgrade.
	config.ApplyConsensusVersion(config.ConsensusV13, chain.config.Consensus)
	require.True(t, chain.config.Consensus.IsFrozenAddress(bridge))
	bridgeBalance := appState.State.GetBalance(bridge)
	require.Zero(t, bridgeBalance.Sign())

	require.ErrorIs(t, delegate(keys[2], bridge), validation.FrozenAddress)

	chain.GenerateBlocks(1, 0)
	require.Equal(t, common.EmptyAddress, appState.State.DelegationSwitch(addrs[0]).Delegatee, "undelegation not queued")
	require.Equal(t, common.EmptyAddress, appState.State.DelegationSwitch(addrs[1]).Delegatee, "undelegation not queued")
	require.Nil(t, appState.State.DelegationSwitch(addrs[2]), "another pool's delegation must stay")

	chain.GenerateEmptyBlocks(50)
	require.False(t, appState.ValidatorsCache.IsPool(bridge), "the wallet is still a pool")
	for _, addr := range addrs[:2] {
		require.Nil(t, appState.State.Delegatee(addr), "still delegated to the wallet")
	}
	require.Nil(t, appState.State.Delegatee(addrs[3]), "a switch to the wallet queued before the upgrade was applied")
	require.Equal(t, otherPool, *appState.State.Delegatee(addrs[2]))
	require.Equal(t, 1, appState.ValidatorsCache.PoolSize(otherPool))
	require.Empty(t, appState.State.Delegations())

	// Nothing more is queued once the wallet is no pool.
	chain.GenerateEmptyBlocks(51)
	require.Empty(t, appState.State.Delegations())
	require.Zero(t, appState.State.GetBalance(bridge).Sign(), "the wallet was credited")
	require.True(t, appState.State.GetStakeBalance(bridge) == nil || appState.State.GetStakeBalance(bridge).Sign() == 0)

	// A credit outside any transaction (a reward, a stake transfer) never reaches a frozen address.
	appState.State.AddBalance(bridge, big.NewInt(5))
	appState.State.AddStake(bridge, big.NewInt(5))
	appState.State.AddReplenishedStake(bridge, big.NewInt(5))
	require.Zero(t, appState.State.GetBalance(bridge).Sign())
	require.True(t, appState.State.GetStakeBalance(bridge) == nil || appState.State.GetStakeBalance(bridge).Sign() == 0)

	// A block whose proposer is frozen is refused.
	chain.config.Consensus.FrozenAddresses[coinbase] = struct{}{}
	block := chain.ProposeBlock([]byte{})
	block.Block.Header.ProposedHeader.Time = chain.Head.Time() + 20
	require.EqualError(t, chain.AddBlock(block.Block, nil, collector.NewStatsCollector()), "proposer is frozen")
}

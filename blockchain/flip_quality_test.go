package blockchain

import (
	"math/big"
	"testing"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/common/math"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestScaleByFlipQuality(t *testing.T) {
	chain, appState, _, _ := NewTestBlockchain(false, nil)
	defer chain.SecStore().Destroy()
	a, b, c := common.Address{1}, common.Address{2}, common.Address{3}
	appState.State.AddFlipQuality(a, 4, 3) // 3/8
	appState.State.AddFlipQuality(b, 5, 0)
	appState.State.AddFlipQuality(c, 2, 4)
	reward := big.NewInt(1001)

	v12 := *chain.config.Consensus
	v12.EnableUpgrade13 = false
	require.Equal(t, reward, scaleByFlipQuality(appState, &v12, a, reward), "v12 scaled a reward")

	v13 := v12
	v13.EnableUpgrade13 = true
	require.Equal(t, big.NewInt(375), scaleByFlipQuality(appState, &v13, a, reward), "1001 * 3 / 8, rounded down")
	require.Zero(t, scaleByFlipQuality(appState, &v13, b, reward).Sign())
	require.Equal(t, reward, scaleByFlipQuality(appState, &v13, c, reward))
	require.Equal(t, reward, scaleByFlipQuality(appState, &v13, common.Address{9}, reward), "an empty window keeps the reward")
	require.Equal(t, big.NewInt(1001), reward, "the argument was changed")
}

func blockForRewards(chain *TestBlockchain) *types.Block {
	return &types.Block{
		Header: &types.Header{ProposedHeader: &types.ProposedHeader{
			Height:         3,
			ParentHash:     chain.Head.Hash(),
			Time:           1500,
			ProposerPubKey: chain.pubKey,
			TxHash:         types.DeriveSha(types.Transactions([]*types.Transaction{})),
		}},
		Body: &types.Body{Transactions: []*types.Transaction{}},
	}
}

// applyRewardsWith applies one block's rewards on a copy of the committed state, with or without the v13 rules.
func applyRewardsWith(t *testing.T, chain *TestBlockchain, v13 bool, fee, tips *big.Int) (*appstate.AppState, *blockRewardCtx) {
	saved := chain.config.Consensus
	defer func() { chain.config.Consensus = saved }()
	cfg := *saved
	cfg.EnableUpgrade13 = v13
	chain.config.Consensus = &cfg
	appState, err := chain.appState.ForCheck(2)
	require.NoError(t, err)
	block := blockForRewards(chain)
	ctx := chain.prepareBlockRewardCtx(block.Header.Coinbase(), appState, block.Height(), chain.Head)
	chain.applyBlockRewards(fee, tips, appState, block, ctx, nil)
	return appState, ctx
}

func credited(before, after *appstate.AppState, addr common.Address) *big.Int {
	sum := func(s *appstate.AppState) *big.Int {
		res := new(big.Int).Set(s.State.GetBalance(addr))
		if stake := s.State.GetStakeBalance(addr); stake != nil {
			res.Add(res, stake)
		}
		return res
	}
	return new(big.Int).Sub(sum(after), sum(before))
}

// Consensus v13: the proposer's minted reward and each committee member's reward are multiplied by that identity's
// own flip quality ratio; the part removed goes to nobody (the proposer does not get the committee's), fees and tips
// are paid whole, and an identity without a window is paid whole.
func Test_ApplyBlockRewardsScaledByFlipQuality(t *testing.T) {
	members := []common.Address{{0x1}, {0x2}, {0x3}}
	alloc := make(map[common.Address]config.GenesisAllocation)
	for i, addr := range members {
		alloc[addr] = config.GenesisAllocation{State: uint8(state.Verified), Stake: ConvertToInt(decimal.NewFromInt(int64(10 * (i + 1))))}
	}
	chain, _, _, _ := NewTestBlockchain(true, alloc)
	defer chain.SecStore().Destroy()
	coinbase := chain.coinBaseAddress
	for _, addr := range members {
		chain.appState.IdentityState.SetOnline(addr, true)
	}
	chain.appState.State.AddStake(coinbase, ConvertToInt(decimal.NewFromInt(20)))
	// ratios: proposer 1/2, members 1/4, none (whole), 0
	windows := map[common.Address][2]uint32{coinbase: {2, 2}, members[0]: {4, 2}, members[2]: {3, 0}}
	for addr, w := range windows {
		chain.appState.State.AddFlipQuality(addr, w[0], w[1])
	}
	chain.appState.Precommit()
	require.NoError(t, chain.appState.CommitAt(2))
	chain.appState.ValidatorsCache.Load()

	fee := ConvertToInt(decimal.NewFromInt(1000))
	tips := ConvertToInt(decimal.NewFromInt(10))
	before := chain.appState
	after12, ctx := applyRewardsWith(t, chain, false, fee, tips)
	after13, _ := applyRewardsWith(t, chain, true, fee, tips)

	require.Len(t, ctx.committee, len(members))
	for _, item := range ctx.committee {
		require.NotEqual(t, coinbase, item.address, "the proposer is in the committee: the test cannot tell its rewards apart")
	}

	scale := func(v *big.Int, w [2]uint32) *big.Int {
		if w[0] == 0 {
			return new(big.Int).Set(v)
		}
		res := new(big.Int).Mul(v, big.NewInt(int64(w[1])))
		return res.Quo(res, big.NewInt(int64(2*w[0])))
	}
	committeePaid := new(big.Int)
	for _, addr := range members {
		r12 := credited(before, after12, addr)
		require.Positive(t, r12.Sign(), "member %v got no reward under v12", addr)
		committeePaid.Add(committeePaid, r12)
		require.Equal(t, scale(r12, windows[addr]).String(), credited(before, after13, addr).String(), "member %v", addr)
	}
	minted := new(big.Int).Add(chain.config.Consensus.BlockReward, chain.config.Consensus.FinalCommitteeReward)
	proposerMinted := new(big.Int).Sub(minted, committeePaid)
	require.Positive(t, proposerMinted.Sign())
	want := credited(before, after12, coinbase)
	want.Sub(want, proposerMinted)
	want.Add(want, scale(proposerMinted, windows[coinbase]))
	require.Equal(t, want.String(), credited(before, after13, coinbase).String(), "proposer")
}

// Consensus v13 in a pool: a proposal's stake part goes to the delegator the pool's nonce picks and its balance part
// to the pool, each scaled by that delegator's ratio; with every ratio 0 nothing is minted and only the fees and tips
// are paid.
func Test_ApplyBlockRewardsFlipQualityZeroInAPool(t *testing.T) {
	ids := []common.Address{{0x1}, {0x2}, {0x3}, {0x4}}
	alloc := make(map[common.Address]config.GenesisAllocation)
	for i, addr := range ids {
		alloc[addr] = config.GenesisAllocation{State: uint8(state.Verified), Stake: ConvertToInt(decimal.NewFromInt(int64(10 * (i + 1))))}
	}
	chain, _, _, _ := NewTestBlockchain(true, alloc)
	defer chain.SecStore().Destroy()
	coinbase := chain.coinBaseAddress
	chain.appState.IdentityState.SetOnline(coinbase, true)
	chain.appState.IdentityState.SetDelegatee(ids[0], coinbase)
	chain.appState.IdentityState.SetDelegatee(ids[1], coinbase)
	chain.appState.IdentityState.SetOnline(ids[2], true)
	chain.appState.IdentityState.SetOnline(ids[3], true)
	chain.appState.State.SetDelegationNonce(coinbase, 1)
	for _, addr := range append([]common.Address{coinbase}, ids...) {
		chain.appState.State.AddFlipQuality(addr, 3, 0)
	}
	chain.appState.Precommit()
	require.NoError(t, chain.appState.CommitAt(2))
	chain.appState.ValidatorsCache.Load()
	require.True(t, chain.appState.ValidatorsCache.IsPool(coinbase))

	fee := ConvertToInt(decimal.NewFromInt(1000))
	tips := ConvertToInt(decimal.NewFromInt(10))
	before := chain.appState
	after12, _ := applyRewardsWith(t, chain, false, fee, tips)
	after13, _ := applyRewardsWith(t, chain, true, fee, tips)

	total := func(after *appstate.AppState) *big.Int {
		res := new(big.Int)
		for _, addr := range append([]common.Address{coinbase}, ids...) {
			res.Add(res, credited(before, after, addr))
		}
		return res
	}
	minted := new(big.Int).Add(chain.config.Consensus.BlockReward, chain.config.Consensus.FinalCommitteeReward)
	require.Equal(t, minted.String(), new(big.Int).Sub(total(after12), total(after13)).String(), "v13 minted something with every ratio 0")
	feePart := new(big.Int).Sub(fee, math.ToInt(decimal.NewFromBigInt(fee, 0).Mul(decimal.NewFromFloat32(chain.config.Consensus.FeeBurnRate))))
	require.Equal(t, new(big.Int).Add(feePart, tips).String(), total(after13).String())
}

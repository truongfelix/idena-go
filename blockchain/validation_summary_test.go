package blockchain

import (
	"math/big"
	"testing"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/events"
	statsTypes "github.com/idena-network/idena-go/stats/types"
	"github.com/idena-network/idena-go/stats/validationsummary"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

// The validation summaries recorded from the real reward distribution: what each identity earned is what it
// received (stake, and balance or its pool's balance).
func Test_rewardValidIdentities_validationSummaries(t *testing.T) {
	god := common.Address{0x1}
	pool := common.Address{0x10}
	delegator := common.Address{0x2}
	candidate := common.Address{0x3}
	human := common.Address{0x4}
	badAuthor := common.Address{0x5}
	missedAuthor := common.Address{0x6}
	reporter := common.Address{0x8}

	conf := GetDefaultConsensusConfig()
	conf.BlockReward = new(big.Int).Mul(big.NewInt(5), common.DnaBase)
	conf.FinalCommitteeReward = new(big.Int).Mul(big.NewInt(5), common.DnaBase)

	memdb := db.NewMemDB()
	bus := eventbus.New()
	appState, _ := appstate.NewAppState(memdb, bus)
	require.NoError(t, appState.Initialize(0))
	appState.State.SetGlobalEpoch(5)
	appState.State.SetGodAddress(god)
	appState.State.SetShardsNum(1)
	dna := func(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), common.DnaBase) }
	appState.State.SetState(delegator, state.Newbie)
	appState.State.SetDelegatee(delegator, pool)
	appState.State.AddStake(delegator, dna(10))
	appState.State.SetState(candidate, state.Candidate)
	appState.State.SetState(human, state.Human)
	appState.State.AddStake(human, dna(95))
	appState.State.SetState(badAuthor, state.Newbie)
	appState.State.AddStake(badAuthor, dna(20))
	appState.State.SetState(missedAuthor, state.Verified)
	appState.State.AddStake(missedAuthor, dna(30))
	appState.State.SetState(reporter, state.Newbie)
	appState.Commit(nil)

	store := validationsummary.NewStore(memdb)
	recorder := validationsummary.NewRecorder(nil, store, appState, bus)
	recorder.EnableCollecting()

	// The ceremony applies the identities' new states.
	newStates := map[common.Address]state.IdentityState{
		delegator: state.Verified, candidate: state.Newbie, human: state.Human, badAuthor: state.Newbie,
		missedAuthor: state.Suspended, reporter: state.Newbie,
	}
	for addr, newState := range newStates {
		recorder.BeginFailedValidationBalanceUpdate(addr, appState)
		appState.State.SetState(addr, newState)
		recorder.CompleteBalanceUpdate(appState)
	}
	appState.State.SetBirthday(candidate, 5)
	recorder.AddNonValidatedStake(missedAuthor, dna(30))
	recorder.SetValidation(&statsTypes.ValidationStats{Shards: map[common.ShardId]*statsTypes.ValidationShardStats{1: {}}})

	validationResults := map[common.ShardId]*types.ValidationResults{
		1: {
			BadAuthors: map[common.Address]types.BadAuthorReason{badAuthor: types.WrongWordsBadAuthor},
			GoodAuthors: map[common.Address]*types.ValidationResult{
				delegator: {FlipsToReward: []*types.FlipToReward{flipToReward([]byte{0x1}, types.GradeNone, decimal.NewFromFloat32(4.5))}, NewIdentityState: uint8(state.Verified)},
				human: {FlipsToReward: []*types.FlipToReward{
					flipToReward([]byte{0x2}, types.GradeNone, decimal.NewFromFloat32(5.5)), flipToReward([]byte{0x3}, types.GradeNone, decimal.NewFromFloat32(2.5)),
					flipToReward([]byte{0x4}, types.GradeNone, decimal.NewFromFloat32(1.5)), flipToReward([]byte{0x5}, types.GradeNone, decimal.NewFromFloat32(3.5)),
				}, NewIdentityState: uint8(state.Human)},
				missedAuthor: {FlipsToReward: []*types.FlipToReward{flipToReward([]byte{0x6}, types.GradeNone, decimal.NewFromFloat32(6.5))}, Missed: true},
			},
			GoodInviters: map[common.Address]*types.InviterValidationResult{
				delegator: {SuccessfulInvites: []*types.SuccessfulInvite{successfulInvite(2, 100, false, candidate)}, PayInvitationReward: true, NewIdentityState: uint8(state.Verified)},
			},
			ReportersToRewardByFlip: map[int]map[common.Address]*types.Candidate{
				100: {reporter: {Address: reporter, NewIdentityState: uint8(state.Newbie)}, human: {Address: human, NewIdentityState: uint8(state.Human)}},
			},
		},
	}

	addresses := []common.Address{delegator, candidate, human, badAuthor, missedAuthor, reporter, pool}
	balances, stakes := make(map[common.Address]*big.Int), make(map[common.Address]*big.Int)
	for _, addr := range addresses {
		balances[addr], stakes[addr] = appState.State.GetBalance(addr), appState.State.GetStakeBalance(addr)
	}

	rewardValidIdentities(appState, conf, validationResults, []uint32{400, 200, 100}, nil, recorder)

	appState.State.IncEpoch()
	bus.Publish(&events.NewBlockEvent{Block: &types.Block{Header: &types.Header{EmptyBlockHeader: &types.EmptyBlockHeader{Flags: types.ValidationFinished}}}})
	recorder.CompleteCollecting()

	delta := func(now, before *big.Int) *big.Int { return new(big.Int).Sub(now, before) }
	toWei := func(d decimal.Decimal) *big.Int { return d.Mul(decimal.NewFromBigInt(common.DnaBase, 0)).BigInt() }
	earned := func(s *validationsummary.Summary) *big.Int {
		r := s.Rewards
		sum := decimal.Sum(r.Validation.Earned, r.Flips.Earned, r.ExtraFlips.Earned, r.Invitations.Earned, r.Invitee.Earned,
			r.Reports.Earned, r.Candidate.Earned, r.Staking.Earned)
		return toWei(sum)
	}
	get := func(addr common.Address) *validationsummary.Summary {
		s, err := store.Get(5, addr)
		require.NoError(t, err)
		require.NotNil(t, s)
		return s
	}

	for _, addr := range []common.Address{candidate, human, reporter} {
		s := get(addr)
		received := new(big.Int).Add(delta(appState.State.GetBalance(addr), balances[addr]), delta(appState.State.GetStakeBalance(addr), stakes[addr]))
		require.Positive(t, received.Sign(), addr.Hex())
		require.Equal(t, received.String(), earned(s).String(), addr.Hex())
		require.Nil(t, s.DelegateeReward)
	}
	require.Positive(t, get(human).Rewards.ExtraFlips.Earned.Sign())
	require.Positive(t, get(human).Rewards.Reports.Earned.Sign())
	require.Positive(t, get(candidate).Rewards.Candidate.Earned.Sign())
	require.Equal(t, "Candidate", get(candidate).PrevState)
	require.Equal(t, "Newbie", get(candidate).State)

	// The delegator's balance part went to its pool.
	s := get(delegator)
	poolReceived := delta(appState.State.GetBalance(pool), balances[pool])
	require.Positive(t, poolReceived.Sign())
	require.Equal(t, pool, s.DelegateeReward.Address)
	require.Equal(t, poolReceived.String(), toWei(s.DelegateeReward.Amount).String())
	require.Equal(t, new(big.Int).Add(poolReceived, delta(appState.State.GetStakeBalance(delegator), stakes[delegator])).String(), earned(s).String())
	require.Positive(t, s.Rewards.Invitations.Earned.Sign())
	require.True(t, delta(appState.State.GetBalance(delegator), balances[delegator]).Sign() == 0)

	// Penalized: nothing earned, the staking reward of its 20 iDNA missed.
	s = get(badAuthor)
	require.True(t, s.Penalized)
	require.Equal(t, "WrongWords", s.PenaltyReason)
	require.Equal(t, "0", earned(s).String())
	require.Equal(t, "penalty", s.Rewards.Staking.Reason)
	require.Positive(t, s.Rewards.Staking.Missed.Sign())

	// Missed the validation: nothing earned, flips reward lost.
	s = get(missedAuthor)
	require.Equal(t, "Suspended", s.State)
	require.Equal(t, "0", earned(s).String())
	require.Equal(t, "missed", s.Rewards.Flips.Reason)
	require.Equal(t, "not_validated", s.Rewards.Staking.Reason)
	// Its stake would have weighed like the others: same share per weight unit.
	humanStaking := get(human).Rewards.Staking.Earned.InexactFloat64()
	require.InEpsilon(t, humanStaking*stakeWeightRatio(30, 95), s.Rewards.Staking.Missed.InexactFloat64(), 1e-4)
}

func stakeWeightRatio(a, b int64) float64 {
	return float64(stakeWeight(big.NewInt(a))) / float64(stakeWeight(big.NewInt(b)))
}

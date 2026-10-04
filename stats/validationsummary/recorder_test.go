package validationsummary

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/events"
	"github.com/idena-network/idena-go/stats/collector"
	statsTypes "github.com/idena-network/idena-go/stats/types"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

func dna(n int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(n), common.DnaBase)
}

func epochBlock(flags types.BlockFlag) *types.Block {
	return &types.Block{Header: &types.Header{EmptyBlockHeader: &types.EmptyBlockHeader{Flags: flags}}}
}

// countingCollector checks that the recorder forwards to the collector it wraps.
type countingCollector struct {
	collector.StatsCollector
	stakingRewards, completes int
}

func (c *countingCollector) AddStakingReward(_, _ common.Address, _ *big.Int, _, _ *big.Int) {
	c.stakingRewards++
}

func (c *countingCollector) CompleteCollecting() {
	c.completes++
}

type fixture struct {
	appState *appstate.AppState
	bus      eventbus.Bus
	store    *Store
	recorder *Recorder
	inner    *countingCollector
}

func newFixture(t *testing.T) *fixture {
	memdb := db.NewMemDB()
	bus := eventbus.New()
	appState, err := appstate.NewAppState(memdb, bus)
	require.NoError(t, err)
	require.NoError(t, appState.Initialize(0))
	appState.State.SetGlobalEpoch(5)
	store := NewStore(memdb)
	inner := &countingCollector{StatsCollector: collector.NewStatsCollector()}
	return &fixture{appState, bus, store, NewRecorder(inner, store, appState, bus), inner}
}

// applyIdentity does what the ceremony does for each identity: the new state between the two calls.
func (f *fixture) applyIdentity(addr common.Address, newState state.IdentityState) {
	f.recorder.BeginFailedValidationBalanceUpdate(addr, f.appState)
	f.appState.State.SetState(addr, newState)
	f.recorder.CompleteBalanceUpdate(f.appState)
}

// clear does what the epoch block does to a killed identity after the ceremony.
func (f *fixture) clear(addr common.Address) {
	f.appState.State.SetState(addr, state.Undefined)
}

// endBlock: the block is added (state in the next epoch, NewBlockEvent), then collecting completes.
func (f *fixture) endBlock(flags types.BlockFlag) {
	f.appState.State.IncEpoch()
	f.bus.Publish(&events.NewBlockEvent{Block: epochBlock(flags)})
	f.recorder.CompleteCollecting()
}

func Test_Recorder_ceremony(t *testing.T) {
	f := newFixture(t)
	r := f.recorder
	verified, candidate, missed, penalized, pool, stranger, killed :=
		common.Address{0x1}, common.Address{0x2}, common.Address{0x3}, common.Address{0x4}, common.Address{0x5}, common.Address{0x9}, common.Address{0x6}

	f.appState.State.SetState(verified, state.Verified)
	f.appState.State.AddStake(verified, dna(100))
	f.appState.State.AddFlip(verified, []byte{0x1}, 0)
	f.appState.State.AddFlip(verified, []byte{0x2}, 1)
	f.appState.State.SetState(candidate, state.Candidate)
	f.appState.State.SetState(missed, state.Verified)
	f.appState.State.AddStake(missed, dna(50))
	f.appState.State.SetState(penalized, state.Newbie)
	f.appState.State.AddStake(penalized, dna(10))
	f.appState.State.SetState(killed, state.Suspended)

	r.EnableCollecting()
	f.applyIdentity(verified, state.Verified)
	f.applyIdentity(candidate, state.Newbie)
	f.applyIdentity(missed, state.Suspended)
	f.applyIdentity(penalized, state.Newbie)
	f.applyIdentity(killed, state.Killed)
	f.clear(killed)
	r.SetValidation(&statsTypes.ValidationStats{Shards: map[common.ShardId]*statsTypes.ValidationShardStats{
		1: {
			IdentitiesPerAddr: map[common.Address]*statsTypes.IdentityStats{
				verified:  {ShortPoint: 5.5, ShortFlips: 6, LongPoint: 20, LongFlips: 22, Approved: true},
				candidate: {ShortPoint: 5, ShortFlips: 5, LongPoint: 18, LongFlips: 20, Approved: true},
				missed:    {Approved: true, Missed: true},
				penalized: {ShortPoint: 4, ShortFlips: 6, LongPoint: 15, LongFlips: 20, Approved: true},
			},
			FlipsPerIdx: map[int]*statsTypes.FlipStats{
				0: {ShortAnswers: []statsTypes.FlipAnswerStats{{Respondent: verified}, {Respondent: candidate}}, LongAnswers: []statsTypes.FlipAnswerStats{{Respondent: verified}}},
				1: {ShortAnswers: []statsTypes.FlipAnswerStats{{Respondent: verified}}},
			},
			WrongGradeReasons: map[common.Address]statsTypes.WrongGradeReason{candidate: statsTypes.TooManyReports},
		},
	}})
	r.SetValidationResults(map[common.ShardId]*types.ValidationResults{1: {
		BadAuthors:  map[common.Address]types.BadAuthorReason{penalized: types.WrongWordsBadAuthor},
		GoodAuthors: map[common.Address]*types.ValidationResult{missed: {Missed: true}},
	}})
	r.SetTotalStakingReward(dna(1000), dna(1))
	r.SetTotalCandidateReward(dna(100), dna(5))
	r.AddPenalizedStake(penalized, dna(10))
	r.AddNonValidatedStake(missed, dna(50))
	r.AddNonValidatedStake(killed, big.NewInt(0))
	r.AddStakingReward(pool, verified, dna(100), dna(8), dna(2))
	r.AddFlipsBasicReward(pool, verified, dna(3), big.NewInt(0), nil)
	r.AddReportedFlipsReward(pool, verified, 1, 0, dna(1), big.NewInt(0))
	r.AddReportedFlipsReward(pool, verified, 1, 1, dna(1), big.NewInt(0))
	r.AddCandidateReward(candidate, candidate, dna(4), dna(1))
	r.AddInviteeReward(candidate, dna(2), 1, common.Hash{}, 10)
	f.endBlock(types.ValidationFinished)

	require.Equal(t, 1, f.inner.stakingRewards)
	require.Equal(t, 1, f.inner.completes)

	s, err := f.store.Get(5, verified)
	require.NoError(t, err)
	require.NotNil(t, s)
	require.Equal(t, uint16(5), s.Epoch)
	require.True(t, s.Participated)
	require.Equal(t, "Verified", s.PrevState)
	require.Equal(t, "Verified", s.State)
	require.True(t, s.Approved)
	require.False(t, s.Missed)
	require.False(t, s.Penalized)
	require.Equal(t, uint8(2), s.MadeFlips)
	require.Equal(t, AnswersSummary{Point: 5.5, FlipsCount: 6}, s.ShortAnswers)
	require.Equal(t, AnswersSummary{Point: 20, FlipsCount: 22}, s.LongAnswers)
	require.Equal(t, uint32(2), s.ShortAnswersCount)
	require.Equal(t, uint32(1), s.LongAnswersCount)
	require.Equal(t, "10", s.Rewards.Staking.Earned.String())
	require.Equal(t, "0", s.Rewards.Staking.Missed.String())
	require.Equal(t, "3", s.Rewards.Flips.Earned.String())
	require.Nil(t, s.Rewards.Flips.Missed)
	require.Equal(t, "2", s.Rewards.Reports.Earned.String())
	require.Equal(t, "0", s.Rewards.Candidate.Earned.String())
	require.Equal(t, &DelegateeReward{Address: pool, Amount: s.DelegateeReward.Amount}, s.DelegateeReward)
	require.Equal(t, "13", s.DelegateeReward.Amount.String())

	s, err = f.store.Get(5, candidate)
	require.NoError(t, err)
	require.Equal(t, "Candidate", s.PrevState)
	require.Equal(t, "Newbie", s.State)
	require.Equal(t, "5", s.Rewards.Candidate.Earned.String())
	require.Equal(t, "0", s.Rewards.Candidate.Missed.String())
	require.Equal(t, "2", s.Rewards.Invitee.Earned.String())
	require.True(t, s.WrongGrades)
	require.Equal(t, "grades_ignored", s.Rewards.Reports.Reason)
	require.Equal(t, uint32(1), s.ShortAnswersCount)
	require.Nil(t, s.DelegateeReward)

	// Missed the validation: no stake reward on 50 iDNA (1 iDNA per weight unit, weight 50^0.9).
	s, err = f.store.Get(5, missed)
	require.NoError(t, err)
	require.Equal(t, "Verified", s.PrevState)
	require.Equal(t, "Suspended", s.State)
	require.True(t, s.Missed)
	require.Equal(t, "not_validated", s.Rewards.Staking.Reason)
	require.InDelta(t, 33.81, s.Rewards.Staking.Missed.InexactFloat64(), 0.01)
	require.Equal(t, "missed", s.Rewards.Flips.Reason)

	s, err = f.store.Get(5, penalized)
	require.NoError(t, err)
	require.Equal(t, "Newbie", s.State)
	require.True(t, s.Penalized)
	require.Equal(t, "WrongWords", s.PenaltyReason)
	require.Equal(t, "penalty", s.Rewards.Staking.Reason)
	require.InDelta(t, 7.943, s.Rewards.Staking.Missed.InexactFloat64(), 0.001)
	require.Equal(t, "penalty", s.Rewards.Flips.Reason)
	require.Equal(t, "penalty", s.Rewards.ExtraFlips.Reason)

	s, err = f.store.Get(5, killed)
	require.NoError(t, err)
	require.Equal(t, "Suspended", s.PrevState)
	require.Equal(t, "Undefined", s.State)
	require.False(t, s.Participated)
	require.True(t, s.Missed)
	require.Empty(t, s.Rewards.Staking.Reason)

	// No identity at the ceremony.
	s, err = f.store.Get(5, stranger)
	require.NoError(t, err)
	require.False(t, s.Participated)
	require.Equal(t, "Undefined", s.PrevState)
	require.Equal(t, "Undefined", s.State)

	// Another epoch: not recorded.
	s, err = f.store.Get(4, verified)
	require.NoError(t, err)
	require.Nil(t, s)
}

func Test_Recorder_failedBlockIsNotRecorded(t *testing.T) {
	f := newFixture(t)
	addr := common.Address{0x1}
	f.appState.State.SetState(addr, state.Verified)

	apply := func() {
		f.recorder.EnableCollecting()
		f.applyIdentity(addr, state.Verified)
		f.recorder.SetValidation(&statsTypes.ValidationStats{})
		f.recorder.AddStakingReward(addr, addr, dna(10), dna(1), big.NewInt(0))
	}
	// AddBlock returns an error: no NewBlockEvent, nothing recorded.
	apply()
	f.recorder.CompleteCollecting()
	s, err := f.store.Get(5, addr)
	require.NoError(t, err)
	require.Nil(t, s)

	// An ordinary block follows, then the epoch block again: its rewards are counted once.
	f.recorder.EnableCollecting()
	f.bus.Publish(&events.NewBlockEvent{Block: epochBlock(0)})
	f.recorder.CompleteCollecting()
	apply()
	f.endBlock(types.ValidationFinished)
	s, err = f.store.Get(5, addr)
	require.NoError(t, err)
	require.Equal(t, "1", s.Rewards.Staking.Earned.String())
}

func Test_Recorder_validationFailed(t *testing.T) {
	f := newFixture(t)
	f.recorder.EnableCollecting()
	f.recorder.SetValidation(&statsTypes.ValidationStats{Failed: true})
	f.endBlock(types.ValidationFinished)

	s, err := f.store.Get(5, common.Address{0x1})
	require.NoError(t, err)
	require.True(t, s.ValidationFailed)
	require.False(t, s.Participated)
	require.Empty(t, s.State)
}

func Test_Store_keepsLastEpochsAndReplacesAnEpoch(t *testing.T) {
	store := NewStore(db.NewMemDB())
	a, b := common.Address{0xa}, common.Address{0xb}
	for epoch := uint16(1); epoch <= KeptEpochs+2; epoch++ {
		require.NoError(t, store.Write(epoch, false, []*Summary{{Epoch: epoch, Address: a, Participated: true}}))
	}
	for epoch := uint16(1); epoch <= KeptEpochs+2; epoch++ {
		s, err := store.Get(epoch, a)
		require.NoError(t, err)
		if epoch <= 2 {
			require.Nil(t, s, "epoch %d", epoch)
		} else {
			require.True(t, s.Participated, "epoch %d", epoch)
		}
	}

	// A reset applies the ceremony block again: nothing of the first write is left.
	require.NoError(t, store.Write(12, false, []*Summary{{Epoch: 12, Address: b, Participated: true}}))
	s, err := store.Get(12, a)
	require.NoError(t, err)
	require.False(t, s.Participated)
	s, err = store.Get(12, b)
	require.NoError(t, err)
	require.True(t, s.Participated)
	s, err = store.Get(11, a)
	require.NoError(t, err)
	require.True(t, s.Participated)
}

func Test_Store_lastEpochNumber(t *testing.T) {
	store := NewStore(db.NewMemDB())
	a := common.Address{0xa}
	require.NoError(t, store.Write(^uint16(0), false, []*Summary{{Address: a, Participated: true}}))
	require.NoError(t, store.Write(^uint16(0), false, nil))
	s, err := store.Get(^uint16(0), a)
	require.NoError(t, err)
	require.False(t, s.Participated)
}

func Test_Summary_json(t *testing.T) {
	zero := Summary{}.Rewards.Staking.Earned
	data, err := json.Marshal(&Summary{
		Address: common.Address{0x1},
		Rewards: Rewards{Staking: Reward{Earned: zero, Missed: &zero, Reason: "penalty"}},
	})
	require.NoError(t, err)
	var fields map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &fields))
	require.Equal(t, "0x0100000000000000000000000000000000000000", fields["address"])
	require.Contains(t, fields, "shortAnswers")
	require.NotContains(t, fields, "penaltyReason")
	require.NotContains(t, fields, "delegateeReward")
	rewards := fields["rewards"].(map[string]interface{})
	require.Equal(t, map[string]interface{}{"earned": "0", "missed": "0", "reason": "penalty"}, rewards["staking"])
	require.Equal(t, map[string]interface{}{"earned": "0", "missed": nil}, rewards["flips"])
}

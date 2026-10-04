package validationsummary

import (
	"math"
	"math/big"
	"sync"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	math2 "github.com/idena-network/idena-go/common/math"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/events"
	"github.com/idena-network/idena-go/log"
	"github.com/idena-network/idena-go/stats/collector"
	statsTypes "github.com/idena-network/idena-go/stats/types"
	"github.com/shopspring/decimal"
)

// Recorder is the node's stats collector with the validation summaries recorded on the side. It forwards every
// call to the collector it wraps (the no-op one, or an indexer's), keeps what the ceremony block reports while the
// block is applied, and writes the summaries once the block is added (NewBlockEvent); a block that fails is
// dropped at CompleteCollecting. It only reads the state it is given.
type Recorder struct {
	collector.StatsCollector
	store    *Store
	appState *appstate.AppState
	log      log.Logger

	mu      sync.Mutex
	current *ceremonyData
}

const (
	kindValidation = iota
	kindStaking
	kindCandidate
	kindFlips
	kindExtraFlips
	kindReports
	kindInvitations
	kindInvitee
	kindCount
)

type ceremonyData struct {
	identities         map[common.Address]*identityData
	validation         *statsTypes.ValidationStats
	results            map[common.ShardId]*types.ValidationResults
	stakingShare       *big.Int
	candidateShare     *big.Int
	penalizedStakes    map[common.Address]*big.Int
	nonValidatedStakes map[common.Address]*big.Int
	rewards            map[common.Address]*identityRewards
}

type identityData struct {
	prevState state.IdentityState
	madeFlips int
}

type identityRewards struct {
	earned    [kindCount]*big.Int
	delegatee *common.Address
	delegated *big.Int
}

// NewRecorder wraps inner (nil: the no-op collector) and listens to the bus for added blocks. appState is the
// node's state: its epoch tells which ceremony an epoch block ended.
func NewRecorder(inner collector.StatsCollector, store *Store, appState *appstate.AppState, bus eventbus.Bus) *Recorder {
	if inner == nil {
		inner = collector.NewStatsCollector()
	}
	r := &Recorder{
		StatsCollector: inner,
		store:          store,
		appState:       appState,
		log:            log.New("component", "validationsummary"),
	}
	bus.Subscribe(events.AddBlockEventID, func(e eventbus.Event) {
		r.onBlockAdded(e.(*events.NewBlockEvent).Block)
	})
	return r
}

func (r *Recorder) data() *ceremonyData {
	if r.current == nil {
		r.current = &ceremonyData{
			identities:         make(map[common.Address]*identityData),
			penalizedStakes:    make(map[common.Address]*big.Int),
			nonValidatedStakes: make(map[common.Address]*big.Int),
			rewards:            make(map[common.Address]*identityRewards),
		}
	}
	return r.current
}

func (r *Recorder) EnableCollecting() {
	r.mu.Lock()
	r.current = nil
	r.mu.Unlock()
	r.StatsCollector.EnableCollecting()
}

func (r *Recorder) CompleteCollecting() {
	r.StatsCollector.CompleteCollecting()
	r.mu.Lock()
	r.current = nil
	r.mu.Unlock()
}

// BeginFailedValidationBalanceUpdate is called for every identity the ceremony applies, before its new state is
// set: the identity is still the one of the ending epoch.
func (r *Recorder) BeginFailedValidationBalanceUpdate(addr common.Address, appState *appstate.AppState) {
	r.StatsCollector.BeginFailedValidationBalanceUpdate(addr, appState)
	identity := appState.State.GetIdentity(addr)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data().identities[addr] = &identityData{
		prevState: identity.State,
		madeFlips: len(identity.Flips),
	}
}

func (r *Recorder) SetValidation(validation *statsTypes.ValidationStats) {
	r.StatsCollector.SetValidation(validation)
	r.mu.Lock()
	r.data().validation = validation
	r.mu.Unlock()
}

func (r *Recorder) SetValidationResults(validationResults map[common.ShardId]*types.ValidationResults) {
	r.StatsCollector.SetValidationResults(validationResults)
	r.mu.Lock()
	r.data().results = validationResults
	r.mu.Unlock()
}

func (r *Recorder) SetTotalStakingReward(amount *big.Int, share *big.Int) {
	r.StatsCollector.SetTotalStakingReward(amount, share)
	r.mu.Lock()
	r.data().stakingShare = copyInt(share)
	r.mu.Unlock()
}

func (r *Recorder) SetTotalCandidateReward(amount *big.Int, share *big.Int) {
	r.StatsCollector.SetTotalCandidateReward(amount, share)
	r.mu.Lock()
	r.data().candidateShare = copyInt(share)
	r.mu.Unlock()
}

func (r *Recorder) AddPenalizedStake(addr common.Address, amount *big.Int) {
	r.StatsCollector.AddPenalizedStake(addr, amount)
	r.mu.Lock()
	r.data().penalizedStakes[addr] = copyInt(amount)
	r.mu.Unlock()
}

func (r *Recorder) AddNonValidatedStake(addr common.Address, amount *big.Int) {
	r.StatsCollector.AddNonValidatedStake(addr, amount)
	r.mu.Lock()
	r.data().nonValidatedStakes[addr] = copyInt(amount)
	r.mu.Unlock()
}

func (r *Recorder) AddValidationReward(balanceDest, stakeDest common.Address, age uint16, balance, stake *big.Int) {
	r.StatsCollector.AddValidationReward(balanceDest, stakeDest, age, balance, stake)
	r.addReward(kindValidation, balanceDest, stakeDest, balance, stake)
}

func (r *Recorder) AddCandidateReward(balanceDest, stakeDest common.Address, balance, stake *big.Int) {
	r.StatsCollector.AddCandidateReward(balanceDest, stakeDest, balance, stake)
	r.addReward(kindCandidate, balanceDest, stakeDest, balance, stake)
}

func (r *Recorder) AddStakingReward(balanceDest, stakeDest common.Address, stakedAmount *big.Int, balance, stake *big.Int) {
	r.StatsCollector.AddStakingReward(balanceDest, stakeDest, stakedAmount, balance, stake)
	r.addReward(kindStaking, balanceDest, stakeDest, balance, stake)
}

func (r *Recorder) AddFlipsBasicReward(balanceDest, stakeDest common.Address, balance, stake *big.Int, flipsToReward []*types.FlipToReward) {
	r.StatsCollector.AddFlipsBasicReward(balanceDest, stakeDest, balance, stake, flipsToReward)
	r.addReward(kindFlips, balanceDest, stakeDest, balance, stake)
}

func (r *Recorder) AddFlipsExtraReward(balanceDest, stakeDest common.Address, balance, stake *big.Int, flipsToReward []*types.FlipToReward) {
	r.StatsCollector.AddFlipsExtraReward(balanceDest, stakeDest, balance, stake, flipsToReward)
	r.addReward(kindExtraFlips, balanceDest, stakeDest, balance, stake)
}

func (r *Recorder) AddReportedFlipsReward(balanceDest, stakeDest common.Address, shardId common.ShardId, flipIdx int, balance, stake *big.Int) {
	r.StatsCollector.AddReportedFlipsReward(balanceDest, stakeDest, shardId, flipIdx, balance, stake)
	r.addReward(kindReports, balanceDest, stakeDest, balance, stake)
}

func (r *Recorder) AddInvitationsReward(balanceDest, stakeDest common.Address, balance, stake *big.Int, age uint16,
	txHash *common.Hash, epochHeight uint32, isSavedInviteWinner bool) {
	r.StatsCollector.AddInvitationsReward(balanceDest, stakeDest, balance, stake, age, txHash, epochHeight, isSavedInviteWinner)
	r.addReward(kindInvitations, balanceDest, stakeDest, balance, stake)
}

func (r *Recorder) AddInviteeReward(addr common.Address, stake *big.Int, age uint16, txHash common.Hash, epochHeight uint32) {
	r.StatsCollector.AddInviteeReward(addr, stake, age, txHash, epochHeight)
	r.addReward(kindInvitee, addr, addr, nil, stake)
}

func (r *Recorder) addReward(kind int, balanceDest, stakeDest common.Address, balance, stake *big.Int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.data()
	rewards, ok := d.rewards[stakeDest]
	if !ok {
		rewards = &identityRewards{}
		d.rewards[stakeDest] = rewards
	}
	if rewards.earned[kind] == nil {
		rewards.earned[kind] = new(big.Int)
	}
	addInt(rewards.earned[kind], balance)
	addInt(rewards.earned[kind], stake)
	if balanceDest != stakeDest && !common.ZeroOrNil(balance) {
		dest := balanceDest
		rewards.delegatee = &dest
		if rewards.delegated == nil {
			rewards.delegated = new(big.Int)
		}
		rewards.delegated.Add(rewards.delegated, balance)
	}
}

func (r *Recorder) onBlockAdded(block *types.Block) {
	if !block.Header.Flags().HasFlag(types.ValidationFinished) {
		return
	}
	r.mu.Lock()
	d := r.current
	r.current = nil
	r.mu.Unlock()
	if d == nil || d.validation == nil {
		return
	}
	// The epoch block has moved the state to the next epoch.
	epoch := r.appState.State.Epoch()
	if epoch == 0 {
		return
	}
	epoch--
	// The state after the block, as dna_identity shows it (a killed identity is cleared: Undefined).
	summaries := d.summaries(epoch, r.appState.State.GetIdentityState)
	if err := r.store.Write(epoch, d.validation.Failed, summaries); err != nil {
		r.log.Error("Cannot record validation summaries", "epoch", epoch, "err", err)
		return
	}
	r.log.Info("Validation summaries recorded", "epoch", epoch, "identities", len(summaries))
}

func (d *ceremonyData) summaries(epoch uint16, stateAfter func(common.Address) state.IdentityState) []*Summary {
	identityStats := make(map[common.Address]*statsTypes.IdentityStats)
	wrongGrades := make(map[common.Address]bool)
	shortAnswers := make(map[common.Address]uint32)
	longAnswers := make(map[common.Address]uint32)
	for _, shard := range d.validation.Shards {
		if shard == nil {
			continue
		}
		for addr, stats := range shard.IdentitiesPerAddr {
			identityStats[addr] = stats
		}
		for addr, reason := range shard.WrongGradeReasons {
			if reason != 0 {
				wrongGrades[addr] = true
			}
		}
		for _, flip := range shard.FlipsPerIdx {
			if flip == nil {
				continue
			}
			for _, answer := range flip.ShortAnswers {
				shortAnswers[answer.Respondent]++
			}
			for _, answer := range flip.LongAnswers {
				longAnswers[answer.Respondent]++
			}
		}
	}
	badAuthors := make(map[common.Address]types.BadAuthorReason)
	goodAuthors := make(map[common.Address]*types.ValidationResult)
	for _, shard := range d.results {
		if shard == nil {
			continue
		}
		for addr, reason := range shard.BadAuthors {
			badAuthors[addr] = reason
		}
		for addr, result := range shard.GoodAuthors {
			goodAuthors[addr] = result
		}
	}

	summaries := make([]*Summary, 0, len(d.identities))
	for addr, identity := range d.identities {
		newState := stateAfter(addr)
		s := &Summary{
			Epoch:             epoch,
			Address:           addr,
			PrevState:         stateName(identity.prevState),
			State:             stateName(newState),
			Missed:            true,
			MadeFlips:         uint8(min(identity.madeFlips, math.MaxUint8)),
			WrongGrades:       wrongGrades[addr],
			ShortAnswersCount: shortAnswers[addr],
			LongAnswersCount:  longAnswers[addr],
		}
		if stats, ok := identityStats[addr]; ok {
			s.Participated = true
			s.Approved = stats.Approved
			s.Missed = stats.Missed
			s.ShortAnswers = AnswersSummary{Point: stats.ShortPoint, FlipsCount: stats.ShortFlips}
			s.LongAnswers = AnswersSummary{Point: stats.LongPoint, FlipsCount: stats.LongFlips}
		}
		if reason, ok := badAuthors[addr]; ok {
			s.Penalized = true
			s.PenaltyReason = penaltyReasonName(reason)
		}
		s.Rewards = d.rewardsOf(addr, identity.prevState, newState, s, goodAuthors[addr])
		if rewards := d.rewards[addr]; rewards != nil && rewards.delegatee != nil {
			s.DelegateeReward = &DelegateeReward{Address: *rewards.delegatee, Amount: toDna(rewards.delegated)}
		}
		summaries = append(summaries, s)
	}
	return summaries
}

func (d *ceremonyData) rewardsOf(addr common.Address, prevState, newState state.IdentityState, s *Summary, goodAuthor *types.ValidationResult) Rewards {
	var earned [kindCount]*big.Int
	if rewards := d.rewards[addr]; rewards != nil {
		earned = rewards.earned
	}
	reward := func(kind int) Reward {
		return Reward{Earned: toDna(earned[kind])}
	}
	zero := decimal.Zero

	rewards := Rewards{
		Validation:  reward(kindValidation),
		Flips:       reward(kindFlips),
		ExtraFlips:  reward(kindExtraFlips),
		Invitations: reward(kindInvitations),
		Invitee:     reward(kindInvitee),
		Reports:     reward(kindReports),
		Candidate:   reward(kindCandidate),
		Staking:     reward(kindStaking),
	}
	rewards.Validation.Missed = &zero

	// Staking: a penalized or not validated identity's stake earns nothing; it would have earned share x weight of
	// the stake the reward is computed on (for a Newbie becoming Verified: after 75% of it moved to the balance).
	rewards.Staking.Missed = &zero
	stake, ok := d.penalizedStakes[addr]
	if !ok {
		stake, ok = d.nonValidatedStakes[addr]
	}
	if ok && stake.Sign() > 0 {
		rewards.Staking.Missed, rewards.Staking.Reason = weightedShare(d.stakingShare, stake), "not_validated"
		if s.Penalized {
			rewards.Staking.Reason = "penalty"
		}
	}

	// Candidate: paid once, to a candidate that becomes Newbie unpenalized.
	rewards.Candidate.Missed = &zero
	if prevState == state.Candidate {
		if s.Penalized {
			rewards.Candidate.Missed, rewards.Candidate.Reason = share(d.candidateShare), "penalty"
		} else if !newState.NewbieOrBetter() {
			rewards.Candidate.Missed, rewards.Candidate.Reason = share(d.candidateShare), "not_validated"
		}
	}

	if s.Penalized {
		rewards.Flips.Reason, rewards.ExtraFlips.Reason = "penalty", "penalty"
	} else if goodAuthor != nil && goodAuthor.Missed {
		rewards.Flips.Reason, rewards.ExtraFlips.Reason = "missed", "missed"
	}
	if s.WrongGrades {
		rewards.Reports.Reason = "grades_ignored"
	}
	return rewards
}

// stakeWeight as the staking reward computes it (blockchain/rewards.go).
func stakeWeight(stake *big.Int) float32 {
	stakeF, _ := toDna(stake).Float64()
	return float32(math.Pow(stakeF, 0.9))
}

func weightedShare(rewardShare, stake *big.Int) *decimal.Decimal {
	if rewardShare == nil {
		return nil
	}
	amount := toDna(math2.ToInt(decimal.NewFromBigInt(rewardShare, 0).Mul(decimal.NewFromFloat(float64(stakeWeight(stake))))))
	return &amount
}

func share(rewardShare *big.Int) *decimal.Decimal {
	if rewardShare == nil {
		return nil
	}
	amount := toDna(rewardShare)
	return &amount
}

func penaltyReasonName(reason types.BadAuthorReason) string {
	switch reason {
	case types.NoQualifiedFlipsBadAuthor:
		return "NoQualifiedFlips"
	case types.QualifiedByNoneBadAuthor:
		return "QualifiedByNone"
	case types.WrongWordsBadAuthor:
		return "WrongWords"
	default:
		return "Unknown"
	}
}

// toDna: iDNA from the smallest unit, as blockchain.ConvertToFloat.
func toDna(amount *big.Int) decimal.Decimal {
	if amount == nil {
		return decimal.Zero
	}
	return decimal.NewFromBigInt(amount, 0).DivRound(decimal.NewFromBigInt(common.DnaBase, 0), 18)
}

func copyInt(value *big.Int) *big.Int {
	if value == nil {
		return nil
	}
	return new(big.Int).Set(value)
}

func addInt(sum, value *big.Int) {
	if value != nil {
		sum.Add(sum, value)
	}
}

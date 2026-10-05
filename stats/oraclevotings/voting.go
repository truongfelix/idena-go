package oraclevotings

import (
	"bytes"
	"math"
	"math/big"
	"time"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/hexutil"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/crypto"
	"github.com/idena-network/idena-go/vm/helpers"
	"github.com/shopspring/decimal"
)

// Voting is an oracle voting as the indexer's API (api.idena.io OracleVotingContract) shows it, with the same
// names, so its clients can switch to the node as they are.
type Voting struct {
	ContractAddress                 common.Address   `json:"contractAddress"`
	Author                          common.Address   `json:"author"`
	Balance                         decimal.Decimal  `json:"balance"`
	Stake                           decimal.Decimal  `json:"stake"`
	Fact                            hexutil.Bytes    `json:"fact"`
	VoteProofsCount                 uint64           `json:"ballotsCount"`
	SecretVotesCount                uint64           `json:"secretVotesCount"`
	VotesCount                      uint64           `json:"votesCount"`
	Votes                           []*OptionVotes   `json:"votes,omitempty"`
	State                           string           `json:"state"`
	CreateTime                      time.Time        `json:"createTime"`
	StartTime                       time.Time        `json:"startTime"`
	EstimatedVotingFinishTime       *time.Time       `json:"estimatedVotingFinishTime,omitempty"`
	EstimatedPublicVotingFinishTime *time.Time       `json:"estimatedPublicVotingFinishTime,omitempty"`
	EstimatedTerminationTime        *time.Time       `json:"estimatedTerminationTime,omitempty"`
	EstimatedOracleReward           *decimal.Decimal `json:"estimatedOracleReward,omitempty"`
	EstimatedMaxOracleReward        *decimal.Decimal `json:"estimatedMaxOracleReward,omitempty"`
	EstimatedTotalReward            *decimal.Decimal `json:"estimatedTotalReward,omitempty"`
	VotingFinishTime                *time.Time       `json:"votingFinishTime,omitempty"`
	PublicVotingFinishTime          *time.Time       `json:"publicVotingFinishTime,omitempty"`
	TerminationTime                 *time.Time       `json:"terminationTime,omitempty"`
	MinPayment                      *decimal.Decimal `json:"minPayment,omitempty"`
	Quorum                          byte             `json:"quorum"`
	CommitteeSize                   uint64           `json:"committeeSize"`
	VotingDuration                  uint64           `json:"votingDuration"`
	PublicVotingDuration            uint64           `json:"publicVotingDuration"`
	WinnerThreshold                 byte             `json:"winnerThreshold"`
	OwnerFee                        byte             `json:"ownerFee"`
	IsOracle                        bool             `json:"isOracle"`
	CommitteeEpoch                  *uint64          `json:"committeeEpoch,omitempty"`
	TotalReward                     *decimal.Decimal `json:"totalReward,omitempty"`
	EpochWithoutGrowth              byte             `json:"epochWithoutGrowth"`
	OwnerDeposit                    *decimal.Decimal `json:"ownerDeposit,omitempty"`
	OracleRewardFund                *decimal.Decimal `json:"oracleRewardFund,omitempty"`
	RefundRecipient                 *common.Address  `json:"refundRecipient,omitempty"`
	Hash                            hexutil.Bytes    `json:"hash"`
	Result                          *byte            `json:"result,omitempty"`
}

type OptionVotes struct {
	Option   byte   `json:"option"`
	Count    uint64 `json:"count"`
	AllCount uint64 `json:"allCount"`
}

// States as the API names them.
const (
	StatePending        = "Pending"
	StateOpen           = "Open"
	StateVoted          = "Voted"
	StateCounting       = "Counting"
	StateArchive        = "Archive"
	StateTerminated     = "Terminated"
	StateCanBeProlonged = "CanBeProlonged"
)

// Contract states as the contract stores them (vm/embedded/oraclevoting.go).
const (
	contractPending  = 0
	contractStarted  = 1
	contractFinished = 2
)

const (
	blockSeconds        = 20 // the block time the estimates assume (the indexer's)
	blocksInDay         = 4320
	pendingTermination  = 30 * 24 * time.Hour
	maxContractKeyBytes = common.MaxContractStoreKeyLength
)

var maxHash *big.Float

func init() {
	var max [32]byte
	for i := range max {
		max[i] = 0xff
	}
	maxHash = new(big.Float).SetInt(new(big.Int).SetBytes(max[:]))
}

// storage reads a voting's contract storage: from the state, or what its termination kept.
type storage interface {
	value(key string) []byte
	// iterate calls f with the key after the prefix, for every key of the map with that prefix.
	iterate(prefix string, f func(key, value []byte))
}

type liveStorage struct {
	state  *state.StateDB
	voting common.Address
}

func (l liveStorage) value(key string) []byte {
	return l.state.GetContractValue(l.voting, []byte(key))
}

func (l liveStorage) iterate(prefix string, f func(key, value []byte)) {
	min := []byte(prefix)
	max := append([]byte{}, min...)
	for len(max) < maxContractKeyBytes {
		max = append(max, 0xff)
	}
	l.state.IterateContractStore(l.voting, min, max, func(key []byte, value []byte) bool {
		f(key[len(prefix):], value)
		return false
	})
}

type keptStorage []*StoredValue

func (k keptStorage) value(key string) []byte {
	return storageValue(k, []byte(key))
}

func (k keptStorage) iterate(prefix string, f func(key, value []byte)) {
	for _, v := range k {
		if bytes.HasPrefix(v.Key, []byte(prefix)) {
			f(v.Key[len(prefix):], v.Value)
		}
	}
}

func readUint64(s storage, key string) uint64 {
	v, _ := helpers.ExtractUInt64(0, s.value(key))
	return v
}

func readUint16(s storage, key string) uint16 {
	v, _ := helpers.ExtractUInt16(0, s.value(key))
	return v
}

func readByte(s storage, key string) byte {
	v := s.value(key)
	if len(v) == 0 {
		return 0
	}
	return v[0]
}

func readInt(s storage, key string) *big.Int {
	v := s.value(key)
	if v == nil {
		return nil
	}
	return new(big.Int).SetBytes(v)
}

func toDna(v *big.Int) decimal.Decimal {
	if v == nil {
		return decimal.Zero
	}
	return decimal.NewFromBigInt(v, -18)
}

func toDnaPtr(v *big.Int) *decimal.Decimal {
	if v == nil {
		return nil
	}
	d := toDna(v)
	return &d
}

func unixTime(seconds int64) time.Time {
	return time.Unix(seconds, 0).UTC()
}

func timePtr(t time.Time) *time.Time {
	return &t
}

// chainView is what the views need besides the voting: the head, the network, the identities, past block times.
type chainView struct {
	height      uint64
	time        int64
	epoch       uint16
	networkSize int
	headerTime  func(height uint64) (int64, bool)
	identity    func(addr common.Address) state.Identity
	validatedIn func(addr common.Address, epoch uint16) (validated, known bool)
}

// votingData is what a voting's storage says, read once.
type votingData struct {
	address              common.Address
	record               *Record
	s                    storage
	balance, stake       *big.Int
	state                byte
	terminated           bool
	startBlock           uint64
	votingDuration       uint64
	publicVotingDuration uint64
	deployCommittee      uint64
	quorum, threshold    byte
	ownerFee             byte
	minPayment           *big.Int
	ownerDeposit         *big.Int
	oracleRewardFund     *big.Int
	votedCount           uint64
	secretVotesCount     uint64
}

func readVoting(address common.Address, record *Record, s storage, balance, stake *big.Int) *votingData {
	d := &votingData{
		address:              address,
		record:               record,
		s:                    s,
		balance:              balance,
		stake:                stake,
		state:                readByte(s, "state"),
		terminated:           record != nil && record.Termination != nil,
		startBlock:           readUint64(s, "startBlock"),
		votingDuration:       readUint64(s, "votingDuration"),
		publicVotingDuration: readUint64(s, "publicVotingDuration"),
		quorum:               readByte(s, "quorum"),
		threshold:            readByte(s, "winnerThreshold"),
		ownerFee:             readByte(s, "ownerFee"),
		minPayment:           readInt(s, "votingMinPayment"),
		ownerDeposit:         readInt(s, "ownerDeposit"),
		oracleRewardFund:     readInt(s, "oracleRewardFund"),
		votedCount:           readUint64(s, "votedCount"),
	}
	// The committee size chosen at the deploy (the stored one changes with the network at each prolongation).
	if d.deployCommittee = readUint64(s, "cs"); d.deployCommittee == 0 {
		d.deployCommittee = readUint64(s, "committeeSize")
	}
	if s.value("secretVotesCount") != nil {
		d.secretVotesCount = readUint64(s, "secretVotesCount")
	} else {
		s.iterate("voteHashes", func([]byte, []byte) { d.secretVotesCount++ })
	}
	return d
}

func (d *votingData) voted(addr common.Address) bool {
	return d.s.value("votes"+string(addr.Bytes())) != nil || d.s.value("voteHashes"+string(addr.Bytes())) != nil
}

// selected tells whether the address passes the committee draw of the voting, as sendVoteProof checks it: the
// stored seed, committee size and network size.
func (d *votingData) selected(pubKey []byte) bool {
	seed := d.s.value("vrfSeed")
	if len(seed) == 0 {
		return false
	}
	h := crypto.Hash(append(append([]byte{}, pubKey...), seed...))
	q := new(big.Float).Quo(new(big.Float).SetInt(new(big.Int).SetBytes(h[:])), maxHash)
	networkSize := float64(readUint64(d.s, "network"))
	if networkSize == 0 {
		networkSize = 1
	}
	return q.Cmp(big.NewFloat(1-float64(readUint64(d.s, "committeeSize"))/networkSize)) >= 0
}

// acceptsVotes: sendVoteProof would take a vote now (started, drawn in this epoch, secret voting not over).
func (d *votingData) acceptsVotes(c *chainView) bool {
	return !d.terminated && d.state == contractStarted && readUint16(d.s, "epoch") == c.epoch &&
		c.height-d.startBlock < d.votingDuration
}

// isOracle: the address voted, or it is in the committee of the voting's last draw. While the voting takes votes,
// the draw counts the identities validated now (as sendVoteProof checks); an earlier draw counted those validated
// in the draw's epoch, which the node knows from its validation summaries (the recent epochs it applied): before
// those, only the voters count. A started voting left in an older epoch has no committee until it is prolonged; a
// terminated one keeps only its voters.
func (d *votingData) isOracle(addr common.Address, c *chainView) bool {
	if d.voted(addr) {
		return true
	}
	if d.terminated || d.state == contractPending {
		return false
	}
	epoch := readUint16(d.s, "epoch")
	if d.state == contractStarted && epoch != c.epoch {
		return false
	}
	identity := c.identity(addr)
	if !d.selected(identity.PubKey) {
		return false
	}
	if d.acceptsVotes(c) {
		return identity.State.NewbieOrBetter()
	}
	validated, known := c.validatedIn(addr, epoch)
	return known && validated
}

func percentOf(value uint64, percent byte) float64 {
	return float64(value) * float64(percent) / 100.0
}

// canBeProlonged: a prolongVoting call would pass now (the contract's conditions).
func (d *votingData) canBeProlonged(c *chainView) bool {
	if d.state != contractStarted || readByte(d.s, "no-growth") >= 3 {
		return false
	}
	duration := c.height - d.startBlock
	committeeSize := readUint64(d.s, "committeeSize")
	winnerVotes := uint64(0)
	d.s.iterate("voteOptions", func(_, value []byte) {
		if cnt, _ := helpers.ExtractUInt64(0, value); cnt > winnerVotes {
			winnerVotes = cnt
		}
	})
	votes := float64(d.votedCount + d.secretVotesCount)
	noWinner := float64(winnerVotes) < percentOf(committeeSize, d.threshold)
	noQuorum := votes < percentOf(committeeSize, d.quorum)
	discrimination := readByte(d.s, "dis") == 1
	afterSecret := duration >= d.votingDuration
	afterPublic := duration >= d.votingDuration+d.publicVotingDuration
	return readUint16(d.s, "epoch") != c.epoch && !afterSecret ||
		afterPublic && noWinner && noQuorum ||
		afterSecret && noQuorum ||
		afterSecret && discrimination && readByte(d.s, "notDisP") == 0 ||
		afterPublic && discrimination && readByte(d.s, "notDisV") == 0
}

// stateName: the voting's state as the API shows it to the oracle (nil: to nobody in particular).
func (d *votingData) stateName(c *chainView, oracle *common.Address) string {
	switch {
	case d.terminated:
		return StateTerminated
	case d.state == contractPending:
		return StatePending
	case d.state == contractFinished:
		return StateArchive
	case d.canBeProlonged(c):
		return StateCanBeProlonged
	case c.height >= d.startBlock+d.votingDuration:
		return StateCounting
	case oracle != nil && d.voted(*oracle):
		return StateVoted
	default:
		return StateOpen
	}
}

// ownerReward is the contract's calculateOwnerReward for a fund and a number of voters.
func (d *votingData) ownerReward(fund *big.Int, voters uint64) *big.Int {
	locks := new(big.Int)
	if d.minPayment != nil {
		locks.Mul(d.minPayment, new(big.Int).SetUint64(voters))
	}
	fee := decimal.NewFromFloat(float64(d.ownerFee) / 100.0)
	if d.ownerDeposit != nil {
		result := new(big.Int).Set(d.ownerDeposit)
		if d.ownerFee > 0 {
			replenished := new(big.Int).Sub(fund, d.ownerDeposit)
			if d.oracleRewardFund != nil {
				replenished.Sub(replenished, d.oracleRewardFund)
			}
			replenished.Sub(replenished, locks)
			if replenished.Sign() > 0 {
				result.Add(result, decimal.NewFromBigInt(replenished, 0).Mul(fee).BigInt())
			}
		}
		return result
	}
	if d.ownerFee > 0 {
		return decimal.NewFromBigInt(new(big.Int).Sub(fund, locks), 0).Mul(fee).BigInt()
	}
	return new(big.Int)
}

// rewardPerOracle: what each of winners gets from the fund once voters paid the minimal payment.
func (d *votingData) rewardPerOracle(voters, winners uint64) decimal.Decimal {
	pot := new(big.Int).Set(d.balance)
	if ballots := d.votedCount + d.secretVotesCount; voters > ballots && d.minPayment != nil {
		pot.Add(pot, new(big.Int).Mul(d.minPayment, new(big.Int).SetUint64(voters-ballots)))
	}
	reward := toDna(new(big.Int).Sub(pot, d.ownerReward(pot, voters)))
	if winners == 0 {
		winners = 1
	}
	reward = reward.Div(decimal.NewFromInt(int64(winners)))
	if reward.Sign() < 0 {
		return decimal.Zero
	}
	return reward
}

func ceilPercent(value uint64, percent byte) uint64 {
	return uint64(math.Ceil(percentOf(value, percent)))
}

func maxUint64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

// view builds the API's voting, relative to the oracle (nil: nobody in particular).
func (d *votingData) view(c *chainView, oracle *common.Address) *Voting {
	s := d.s
	v := &Voting{
		ContractAddress:      d.address,
		Balance:              toDna(d.balance),
		Stake:                toDna(d.stake),
		Fact:                 s.value("fact"),
		VoteProofsCount:      d.votedCount + d.secretVotesCount,
		SecretVotesCount:     d.secretVotesCount,
		VotesCount:           d.votedCount,
		State:                d.stateName(c, oracle),
		StartTime:            unixTime(int64(readUint64(s, "startTime"))),
		MinPayment:           toDnaPtr(d.minPayment),
		Quorum:               d.quorum,
		CommitteeSize:        d.deployCommittee,
		VotingDuration:       d.votingDuration,
		PublicVotingDuration: d.publicVotingDuration,
		WinnerThreshold:      d.threshold,
		OwnerFee:             d.ownerFee,
		EpochWithoutGrowth:   readByte(s, "no-growth"),
		OwnerDeposit:         toDnaPtr(d.ownerDeposit),
		OracleRewardFund:     toDnaPtr(d.oracleRewardFund),
		Hash:                 s.value("hash"),
	}
	v.Author.SetBytes(s.value("owner"))
	if recipient := s.value("refundRecipient"); len(recipient) > 0 {
		addr := common.BytesToAddress(recipient)
		v.RefundRecipient = &addr
	}
	if result := s.value("result"); len(result) > 0 {
		r := result[0]
		v.Result = &r
	}
	if oracle != nil {
		v.IsOracle = d.isOracle(*oracle, c)
	}
	options := map[byte]*OptionVotes{}
	option := func(key []byte) *OptionVotes {
		o := options[key[0]]
		if o == nil {
			o = &OptionVotes{Option: key[0]}
			options[key[0]] = o
			v.Votes = append(v.Votes, o)
		}
		return o
	}
	s.iterate("voteOptions", func(key, value []byte) {
		if len(key) > 0 {
			option(key).Count, _ = helpers.ExtractUInt64(0, value)
		}
	})
	s.iterate("allVotes", func(key, value []byte) {
		if len(key) > 0 {
			option(key).AllCount, _ = helpers.ExtractUInt64(0, value)
		}
	})

	// Times.
	v.CreateTime = v.StartTime
	if d.record != nil && d.record.CreateTime > 0 {
		v.CreateTime = unixTime(d.record.CreateTime)
	} else if d.startBlock > 0 {
		if t, ok := c.headerTime(d.startBlock); ok {
			v.CreateTime = unixTime(t)
		}
	}
	if d.state != contractPending && d.startBlock > 0 {
		epoch := uint64(readUint16(s, "epoch"))
		v.CommitteeEpoch = &epoch
		countingBlock := d.startBlock + d.votingDuration
		if countingBlock <= c.height {
			if t, ok := c.headerTime(countingBlock); ok {
				v.VotingFinishTime = timePtr(unixTime(t))
			}
		}
		if countingBlock+d.publicVotingDuration <= c.height {
			if t, ok := c.headerTime(countingBlock + d.publicVotingDuration); ok {
				v.PublicVotingFinishTime = timePtr(unixTime(t))
			}
		}
		if !d.terminated {
			blocksLeft := int64(countingBlock) - int64(c.height)
			at := func(blocks int64) *time.Time {
				return timePtr(unixTime(c.time + blocks*blockSeconds))
			}
			stake, _ := toDna(d.stake).Float64()
			terminationDays := int64(math.Round(math.Cbrt(stake * float64(c.networkSize) / 100)))
			v.EstimatedTerminationTime = at(blocksLeft + int64(d.publicVotingDuration) + terminationDays*blocksInDay)
			switch v.State {
			case StateOpen, StateVoted:
				v.EstimatedVotingFinishTime = at(blocksLeft)
				v.EstimatedPublicVotingFinishTime = at(blocksLeft + int64(d.publicVotingDuration))
			case StateCounting, StateCanBeProlonged:
				v.EstimatedPublicVotingFinishTime = at(blocksLeft + int64(d.publicVotingDuration))
			}
		}
	}
	if d.state == contractPending && !d.terminated {
		v.EstimatedTerminationTime = timePtr(v.StartTime.Add(pendingTermination))
	}
	if d.terminated {
		v.TerminationTime = timePtr(unixTime(d.record.Termination.Time))
	}

	// Rewards.
	switch v.State {
	case StatePending, StateOpen, StateVoted, StateCounting, StateCanBeProlonged:
		ballots := d.votedCount + d.secretVotesCount
		committee := maxUint64(maxUint64(d.deployCommittee, 1), ballots)
		oracleReward := d.rewardPerOracle(committee, committee)
		v.EstimatedOracleReward = &oracleReward
		quorum := maxUint64(maxUint64(ceilPercent(d.deployCommittee, d.quorum), 1), ballots)
		maxReward := d.rewardPerOracle(quorum, ceilPercent(quorum, d.threshold))
		v.EstimatedMaxOracleReward = &maxReward
		total := toDna(new(big.Int).Sub(d.balance, d.ownerReward(d.balance, ballots)))
		if total.Sign() < 0 {
			total = decimal.Zero
		}
		v.EstimatedTotalReward = &total
	case StateArchive, StateTerminated:
		result := (*Result)(nil)
		if d.record != nil {
			result = d.record.Finish
			if d.terminated && d.record.Termination.Result != nil && result == nil {
				result = d.record.Termination.Result
			}
		}
		if result != nil && result.Fund != nil {
			total := new(big.Int).Set(result.Fund)
			if result.OwnerReward != nil {
				total.Sub(total, result.OwnerReward)
			}
			v.TotalReward = toDnaPtr(total)
		}
	}
	return v
}

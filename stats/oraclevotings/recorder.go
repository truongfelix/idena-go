package oraclevotings

import (
	"math/big"
	"sync"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/events"
	"github.com/idena-network/idena-go/log"
	"github.com/idena-network/idena-go/stats/collector"
	"github.com/idena-network/idena-go/vm/embedded"
)

// Recorder is a stats collector that keeps the oracle voting index on the side. It forwards every call to the
// collector it wraps, gathers what the votings do while a block is applied, and writes it once the block is added
// (NewBlockEvent); a block that fails is dropped at CompleteCollecting. It only reads the state it is given.
type Recorder struct {
	collector.StatsCollector
	store *Store
	// own tells the node's addresses (coinbase and keystore accounts): only their balance changes are recorded.
	own func(common.Address) bool
	log log.Logger

	mu    sync.Mutex
	block *blockData
	tx    *txData
}

type blockData struct {
	votings map[common.Address]*votingChange
	rows    []*BalanceRow
}

// votingChange is what the block's transactions did to a voting's state.
type votingChange struct {
	deployed    bool
	finish      *Result
	termination *Termination
}

type txData struct {
	tx       *types.Transaction
	appState *appstate.AppState

	deployed    *common.Address
	stateChange bool
	finish      *Result
	termination *Termination
	// balances: the own addresses' balance before the contract changed it, and after.
	balances map[common.Address][2]*big.Int
}

// NewRecorder wraps inner (nil: the no-op collector) and listens to the bus for added blocks and chain resets.
func NewRecorder(inner collector.StatsCollector, store *Store, own func(common.Address) bool, bus eventbus.Bus) *Recorder {
	if inner == nil {
		inner = collector.NewStatsCollector()
	}
	r := &Recorder{
		StatsCollector: inner,
		store:          store,
		own:            own,
		log:            log.New("component", "oraclevotings"),
	}
	bus.Subscribe(events.AddBlockEventID, func(e eventbus.Event) {
		r.onBlockAdded(e.(*events.NewBlockEvent).Block)
	})
	bus.Subscribe(events.BlockchainResetEventID, func(e eventbus.Event) {
		// Blocks were reverted: what they recorded may be gone from the state.
		if err := store.markForScan(); err != nil {
			r.log.Error("Cannot mark the oracle voting index for a scan", "err", err)
		}
	})
	return r
}

func (r *Recorder) EnableCollecting() {
	r.mu.Lock()
	r.block, r.tx = nil, nil
	r.mu.Unlock()
	r.StatsCollector.EnableCollecting()
}

func (r *Recorder) CompleteCollecting() {
	r.StatsCollector.CompleteCollecting()
	r.mu.Lock()
	r.block, r.tx = nil, nil
	r.mu.Unlock()
}

func (r *Recorder) BeginApplyingTx(tx *types.Transaction, appState *appstate.AppState) {
	r.StatsCollector.BeginApplyingTx(tx, appState)
	r.mu.Lock()
	r.tx = &txData{tx: tx, appState: appState}
	r.mu.Unlock()
}

func (r *Recorder) CompleteApplyingTx(appState *appstate.AppState) {
	r.StatsCollector.CompleteApplyingTx(appState)
	r.mu.Lock()
	r.tx = nil
	r.mu.Unlock()
}

func (r *Recorder) withTx(f func(tx *txData)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tx != nil {
		f(r.tx)
	}
}

func (r *Recorder) AddOracleVotingDeploy(contractAddress common.Address, startTime uint64, votingMinPayment *big.Int,
	fact []byte, state byte, votingDuration, publicVotingDuration uint64, winnerThreshold, quorum byte,
	committeeSize, networkSize uint64, ownerFee byte, ownerDeposit, oracleRewardFund *big.Int, refundRecipient *common.Address, hash []byte) {
	r.StatsCollector.AddOracleVotingDeploy(contractAddress, startTime, votingMinPayment, fact, state, votingDuration,
		publicVotingDuration, winnerThreshold, quorum, committeeSize, networkSize, ownerFee, ownerDeposit,
		oracleRewardFund, refundRecipient, hash)
	r.withTx(func(tx *txData) {
		addr := contractAddress
		tx.deployed = &addr
		tx.stateChange = true
	})
}

func (r *Recorder) AddOracleVotingCallStart(state byte, startBlock uint64, epoch uint16, votingMinPayment *big.Int,
	vrfSeed []byte, committeeSize uint64, networkSize int) {
	r.StatsCollector.AddOracleVotingCallStart(state, startBlock, epoch, votingMinPayment, vrfSeed, committeeSize, networkSize)
	r.withTx(func(tx *txData) { tx.stateChange = true })
}

func (r *Recorder) AddOracleVotingCallProlongation(startBlock *uint64, epoch uint16, vrfSeed []byte, committeeSize,
	networkSize, newCommitteeSize uint64, newEpochWithoutGrowth *byte, newProlongVoteCount *uint64) {
	r.StatsCollector.AddOracleVotingCallProlongation(startBlock, epoch, vrfSeed, committeeSize, networkSize,
		newCommitteeSize, newEpochWithoutGrowth, newProlongVoteCount)
	r.withTx(func(tx *txData) { tx.stateChange = true })
}

func copyInt(v *big.Int) *big.Int {
	if v == nil {
		return nil
	}
	return new(big.Int).Set(v)
}

func newResult(fund, oracleReward, ownerReward *big.Int) *Result {
	if fund == nil && oracleReward == nil && ownerReward == nil {
		return nil
	}
	return &Result{Fund: copyInt(fund), OracleReward: copyInt(oracleReward), OwnerReward: copyInt(ownerReward)}
}

func (r *Recorder) AddOracleVotingCallFinish(state byte, result *byte, fund, oracleReward, ownerReward *big.Int) {
	r.StatsCollector.AddOracleVotingCallFinish(state, result, fund, oracleReward, ownerReward)
	r.withTx(func(tx *txData) {
		tx.stateChange = true
		tx.finish = newResult(fund, oracleReward, ownerReward)
		if tx.finish == nil {
			tx.finish = &Result{}
		}
	})
}

// AddOracleVotingTermination is called by the contract before the termination is committed: the state still holds
// the voting, which is kept as it is (the state drops it).
func (r *Recorder) AddOracleVotingTermination(fund, oracleReward, ownerReward *big.Int) {
	r.StatsCollector.AddOracleVotingTermination(fund, oracleReward, ownerReward)
	r.withTx(func(tx *txData) {
		if tx.tx.To == nil {
			return
		}
		voting := *tx.tx.To
		s := tx.appState.State
		t := &Termination{
			Result:  newResult(fund, oracleReward, ownerReward),
			Balance: copyInt(s.GetBalance(voting)),
			Stake:   copyInt(s.GetContractStake(voting)),
		}
		s.IterateContractStore(voting, nil, nil, func(key []byte, value []byte) bool {
			t.Storage = append(t.Storage, &StoredValue{Key: append([]byte{}, key...), Value: append([]byte{}, value...)})
			return false
		})
		tx.stateChange = true
		tx.termination = t
	})
}

// AddContractBalanceUpdate is called for each balance the contract changes; getCurrentBalance gives the balance
// before this change.
func (r *Recorder) AddContractBalanceUpdate(contractAddress *common.Address, address common.Address,
	getCurrentBalance collector.GetBalanceFunc, newBalance *big.Int, appState *appstate.AppState,
	balancesCache *map[common.Address]*big.Int) {
	r.StatsCollector.AddContractBalanceUpdate(contractAddress, address, getCurrentBalance, newBalance, appState, balancesCache)
	if r.own == nil || !r.own(address) {
		return
	}
	r.withTx(func(tx *txData) {
		if tx.balances == nil {
			tx.balances = make(map[common.Address][2]*big.Int)
		}
		b, ok := tx.balances[address]
		if !ok {
			b[0] = copyInt(getCurrentBalance(address))
		}
		b[1] = copyInt(newBalance)
		tx.balances[address] = b
	})
}

// AddTxReceipt ends a contract transaction: a failed one changed nothing (the contract's changes are dropped).
func (r *Recorder) AddTxReceipt(txReceipt *types.TxReceipt, appState *appstate.AppState) {
	r.StatsCollector.AddTxReceipt(txReceipt, appState)
	r.mu.Lock()
	defer r.mu.Unlock()
	tx := r.tx
	if tx == nil || txReceipt == nil || !txReceipt.Success {
		return
	}
	voting := txReceipt.ContractAddress
	isVoting := tx.deployed != nil || tx.termination != nil
	if !isVoting {
		if hash := appState.State.GetCodeHash(voting); hash != nil && *hash == embedded.OracleVotingContract {
			isVoting = true
		}
	}
	if !isVoting {
		return
	}
	if r.block == nil {
		r.block = &blockData{votings: make(map[common.Address]*votingChange)}
	}
	if tx.stateChange {
		c := r.block.votings[voting]
		if c == nil {
			c = &votingChange{}
			r.block.votings[voting] = c
		}
		c.deployed = c.deployed || tx.deployed != nil
		if tx.finish != nil {
			c.finish = tx.finish
		}
		if tx.termination != nil {
			c.termination = tx.termination
		}
	}
	for addr, b := range tx.balances {
		change := new(big.Int).Sub(b[1], b[0])
		if change.Sign() == 0 {
			continue
		}
		row := NewBalanceRow(addr, voting, tx.tx, txReceipt, appState.State.FeePerGas(), appState.ValidatorsCache.NetworkSize())
		row.BalanceChange = change
		r.block.rows = append(r.block.rows, row)
	}
}

func (r *Recorder) onBlockAdded(block *types.Block) {
	r.mu.Lock()
	d := r.block
	r.block = nil
	r.mu.Unlock()

	height, timestamp := block.Height(), block.Header.Time()
	changes := &blockChanges{height: height, time: timestamp, records: make(map[common.Address]func(*Record))}
	if d != nil {
		for voting, c := range d.votings {
			c := c
			changes.records[voting] = func(rec *Record) {
				rec.StateHeight = height
				if c.deployed {
					rec.CreateHeight, rec.CreateTime = height, timestamp
				}
				if c.finish != nil {
					rec.Finish = c.finish
				}
				if c.termination != nil {
					c.termination.Height, c.termination.Time = height, timestamp
					rec.Termination = c.termination
				}
			}
		}
		changes.rows = d.rows
	}
	if err := r.store.writeBlock(changes); err != nil {
		r.log.Error("Cannot record the oracle votings of a block", "height", height, "err", err)
	}
}

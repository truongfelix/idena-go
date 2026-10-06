package identityhistory

import (
	"math/big"
	"sync"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/events"
	"github.com/idena-network/idena-go/log"
	"github.com/idena-network/idena-go/stats/collector"
)

// Recorder is a stats collector that records, on the side, the block that ends each epoch and the mining rewards of
// the node's own addresses. It forwards every call to the collector it wraps, gathers the block's rewards while the
// block is applied and writes them once the block is added (NewBlockEvent); a block that fails is dropped at
// CompleteCollecting. It only reads the state it is given.
type Recorder struct {
	collector.StatsCollector
	store    *Store
	appState *appstate.AppState
	// own tells the node's addresses (coinbase and keystore accounts): only their mining is recorded.
	own   func(common.Address) bool
	gaps  chan struct{}
	log   log.Logger
	mu    sync.Mutex
	block map[common.Address]*Mining
	// lastHeight: the last block added since the node started (0: none yet).
	lastHeight uint64
}

// NewRecorder wraps inner (nil: the no-op collector) and listens to the bus for added blocks and chain resets.
// appState is the node's state: its epoch tells which epoch a block belongs to.
func NewRecorder(inner collector.StatsCollector, store *Store, appState *appstate.AppState, own func(common.Address) bool,
	bus eventbus.Bus) *Recorder {
	if inner == nil {
		inner = collector.NewStatsCollector()
	}
	r := &Recorder{
		StatsCollector: inner,
		store:          store,
		appState:       appState,
		own:            own,
		gaps:           make(chan struct{}, 1),
		log:            log.New("component", "identityhistory"),
	}
	bus.Subscribe(events.AddBlockEventID, func(e eventbus.Event) {
		r.onBlockAdded(e.(*events.NewBlockEvent).Block)
	})
	bus.Subscribe(events.BlockchainResetEventID, func(e eventbus.Event) {
		r.onReset(e.(*events.BlockchainResetEvent).Header)
	})
	return r
}

// Gaps receives a signal when the recorded ceremonies may have a gap: blocks were added after a range the node did
// not apply (a fast sync).
func (r *Recorder) Gaps() <-chan struct{} {
	return r.gaps
}

func (r *Recorder) signalGap() {
	select {
	case r.gaps <- struct{}{}:
	default:
	}
}

func (r *Recorder) EnableCollecting() {
	r.mu.Lock()
	r.block = nil
	r.mu.Unlock()
	r.StatsCollector.EnableCollecting()
}

func (r *Recorder) CompleteCollecting() {
	r.StatsCollector.CompleteCollecting()
	r.mu.Lock()
	r.block = nil
	r.mu.Unlock()
}

func (r *Recorder) AddProposerReward(balanceDest, stakeDest common.Address, balance, stake *big.Int, stakeWeight *big.Float) {
	r.StatsCollector.AddProposerReward(balanceDest, stakeDest, balance, stake, stakeWeight)
	r.addReward(balanceDest, stakeDest, balance, stake, func(m *Mining, amount *big.Int) {
		m.ProposedBlocks = 1
		m.ProposerReward = addInt(m.ProposerReward, amount, 1)
	})
}

func (r *Recorder) AddFinalCommitteeReward(balanceDest, stakeDest common.Address, balance, stake *big.Int, stakeWeight *big.Float) {
	r.StatsCollector.AddFinalCommitteeReward(balanceDest, stakeDest, balance, stake, stakeWeight)
	r.addReward(balanceDest, stakeDest, balance, stake, func(m *Mining, amount *big.Int) {
		m.CommitteeBlocks = 1
		m.CommitteeReward = addInt(m.CommitteeReward, amount, 1)
	})
}

// AddPenaltyBurntCoins: the block rewards are the only payments a penalty takes its share of.
func (r *Recorder) AddPenaltyBurntCoins(addr common.Address, amount *big.Int) {
	r.StatsCollector.AddPenaltyBurntCoins(addr, amount)
	if common.ZeroOrNil(amount) || !r.own(addr) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.mining(addr)
	m.PenaltyBurnt = addInt(m.PenaltyBurnt, amount, 1)
}

// addReward credits each own address with the part it received: the balance part to balanceDest, the stake part
// to stakeDest (the same address unless the identity mines for a pool).
func (r *Recorder) addReward(balanceDest, stakeDest common.Address, balance, stake *big.Int, add func(*Mining, *big.Int)) {
	parts := make(map[common.Address]*big.Int, 2)
	if r.own(balanceDest) {
		parts[balanceDest] = addInt(parts[balanceDest], balance, 1)
	}
	if r.own(stakeDest) {
		parts[stakeDest] = addInt(parts[stakeDest], stake, 1)
	}
	if len(parts) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for addr, amount := range parts {
		if amount == nil {
			amount = new(big.Int)
		}
		add(r.mining(addr), amount)
	}
}

func (r *Recorder) mining(addr common.Address) *Mining {
	if r.block == nil {
		r.block = make(map[common.Address]*Mining)
	}
	m, ok := r.block[addr]
	if !ok {
		m = &Mining{}
		r.block[addr] = m
	}
	return m
}

func (r *Recorder) onBlockAdded(block *types.Block) {
	r.mu.Lock()
	mining := r.block
	r.block = nil
	gap := r.lastHeight != 0 && block.Height() != r.lastHeight+1
	r.lastHeight = block.Height()
	r.mu.Unlock()

	// The block's epoch: a ceremony block has moved the state to the next epoch already.
	epoch := r.appState.State.Epoch()
	if block.Header.Flags().HasFlag(types.ValidationFinished) && epoch > 0 {
		epoch--
		if err := r.store.writeCeremony(epoch, &Ceremony{Height: block.Height(), Time: block.Header.Time()}); err != nil {
			r.log.Error("Cannot record the ceremony block", "epoch", epoch, "err", err)
		}
		if epoch > 0 {
			if prev, err := r.store.Ceremony(epoch - 1); err == nil && prev == nil {
				gap = true
			}
		}
	}
	if len(mining) > 0 {
		// A reset goes back at most as far as the saved states; the journal keeps twice that.
		keepFrom := uint64(0)
		if block.Height() > 2*state.MaxSavedStatesCount {
			keepFrom = block.Height() - 2*state.MaxSavedStatesCount
		}
		if err := r.store.writeBlockMining(block.Height(), epoch, mining, keepFrom); err != nil {
			r.log.Error("Cannot record the block's mining", "height", block.Height(), "err", err)
		}
	}
	if gap {
		r.signalGap()
	}
}

func (r *Recorder) onReset(head *types.Header) {
	r.mu.Lock()
	r.lastHeight = head.Height()
	r.mu.Unlock()
	if err := r.store.revertTo(head.Height()); err != nil {
		r.log.Error("Cannot revert the identity history", "height", head.Height(), "err", err)
	}
}

package protocol

import (
	"errors"
	"fmt"
	"github.com/deckarep/golang-set"
	"github.com/idena-network/idena-go/blockchain"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/common/math"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/core/state/snapshot"
	"github.com/idena-network/idena-go/core/upgrade"
	"github.com/idena-network/idena-go/ipfs"
	"github.com/idena-network/idena-go/keystore"
	"github.com/idena-network/idena-go/log"
	"github.com/idena-network/idena-go/secstore"
	"github.com/idena-network/idena-go/stats/collector"
	"github.com/idena-network/idena-go/subscriptions"
	"github.com/libp2p/go-libp2p/core/peer"
	"time"
)

const (
	MaxAttemptsCountPerBatch = 10
)

var (
	BanReasonTimeout = errors.New("timeout")
)

type Syncer interface {
	IsSyncing() bool
}

type blockApplier interface {
	batchSize() uint64
	processBatch(batch *batch, attemptNum int) error
	postConsuming() (err error)
	preConsuming(head *types.Header) (uint64, error)
}

type ForkResolver interface {
	HasLoadedFork() bool
}

type BlockSeeker interface {
	SeekBlocks(fromBlock, toBlock uint64, peers []peer.ID) chan *types.BlockBundle
}

type Downloader struct {
	pm                   *IdenaGossipHandler
	cfg                  *config.Config
	log                  log.Logger
	chain                *blockchain.Blockchain
	batches              chan *batch
	ipfs                 ipfs.Proxy
	isSyncing            bool
	appState             *appstate.AppState
	top                  uint64
	potentialForkedPeers mapset.Set
	sm                   *state.SnapshotManager
	bus                  eventbus.Bus
	secStore             *secstore.SecStore
	statsCollector       collector.StatsCollector
	keyStore             *keystore.KeyStore
	subManager           *subscriptions.Manager
	upgrader             *upgrade.Upgrader
	// fullSyncNext makes the next pass a full sync slice: the last fast sync pass failed (afterFailedPass).
	fullSyncNext bool
	// manifestsAwaited is set once a pass of this sync has looked for snapshot manifests.
	manifestsAwaited bool
}

func (d *Downloader) IsSyncing() bool {
	return d.isSyncing
}

func (d *Downloader) SyncProgress() (head uint64, top uint64) {
	height := d.chain.Head.Height()
	if d.chain.PreliminaryHead != nil {
		height = math.Max(height, d.chain.PreliminaryHead.Height())
	}
	return height, d.top
}

func NewDownloader(
	pm *IdenaGossipHandler,
	cfg *config.Config,
	chain *blockchain.Blockchain,
	ipfs ipfs.Proxy,
	appState *appstate.AppState,
	sm *state.SnapshotManager,
	bus eventbus.Bus,
	secStore *secstore.SecStore,
	statsCollector collector.StatsCollector,
	subManager *subscriptions.Manager,
	keyStore *keystore.KeyStore,
	upgrader *upgrade.Upgrader,
) *Downloader {
	return &Downloader{
		pm:                   pm,
		cfg:                  cfg,
		chain:                chain,
		log:                  log.New("component", "downloader"),
		ipfs:                 ipfs,
		appState:             appState,
		isSyncing:            false,
		potentialForkedPeers: mapset.NewSet(),
		sm:                   sm,
		bus:                  bus,
		secStore:             secStore,
		statsCollector:       statsCollector,
		subManager:           subManager,
		keyStore:             keyStore,
		upgrader:             upgrader,
	}
}

func getTopHeight(heights map[peer.ID]uint64) uint64 {
	max := uint64(0)
	for _, value := range heights {
		if value > max {
			max = value
		}
	}
	return max
}

func (d *Downloader) filterForkedPeers(peers map[peer.ID]uint64) {
	for _, p := range d.potentialForkedPeers.ToSlice() {
		delete(peers, p.(peer.ID))
	}
}

func (d *Downloader) SyncBlockchain(forkResolver ForkResolver) error {

	for {
		if forkResolver.HasLoadedFork() {
			return errors.New("loaded fork is detected")
		}
		knownHeights := d.pm.GetKnownHeights()
		if knownHeights == nil {
			d.log.Info(fmt.Sprintf("Peers are not found. Assume node is synchronized"))
			return nil
		}

		d.filterForkedPeers(knownHeights)

		if len(knownHeights) == 0 {
			return errors.New("all connected peers are in fork")
		}

		head := d.chain.Head
		d.top = getTopHeight(knownHeights)
		if head.Height() >= d.top {
			d.log.Info(fmt.Sprintf("Node is synchronized"))
			return nil
		}
		if !d.isSyncing {
			d.startSync()
			defer d.stopSync()
		}
		d.Load()
	}
}

func (d *Downloader) Load() {

	head := d.chain.Head

	applier, toHeight := d.createBlockApplier()

	var from uint64
	var err error
	if from, err = applier.preConsuming(head); err != nil {
		d.log.Error("pre consuming error", "err", err)
		time.Sleep(5 * time.Second)
		return
	}

	d.batches = make(chan *batch, 10)
	term := make(chan interface{})
	completed := make(chan interface{})
	go d.consumeBlocks(applier, term, completed)

	requestBatches(d.pm, d.pm.GetKnownHeights(), from, toHeight, applier.batchSize(), d.batches, term)
	d.log.Info("All blocks were requested. Wait for applying of blocks")
	close(completed)
	<-term
	if err := applier.postConsuming(); err != nil {
		d.log.Error("Post consuming error", "err", err)
		d.afterFailedPass(applier)
		time.Sleep(5 * time.Second)
	}
}

func (d *Downloader) consumeBlocks(applier blockApplier, term chan interface{}, completed chan interface{}) {
	defer close(term)

	consume := func(batch *batch) (stop bool) {
		if len(batch.headers) == 0 && !d.pm.IsConnected(batch.p.id) {
			batch = requestBatch(d.pm, batch.from, batch.to, batch.p.id)
			if batch == nil {
				d.log.Warn("failed to process batch", "err", "no peers")
				return true
			}
		}

		if err := applier.processBatch(batch, 1); err != nil {
			d.log.Warn("failed to process batch", "err", err)
			return true
		}
		return false
	}

	for {
		timeout := time.After(time.Second * 15)

		select {
		case batch := <-d.batches:
			if consume(batch) {
				return
			}
			continue
		default:
		}

		select {
		case batch := <-d.batches:
			if consume(batch) {
				return
			}
			continue
		case <-completed:
			return
		case <-timeout:
			return
		}
	}
}

func (d *Downloader) SeekBlocks(fromBlock, toBlock uint64, peers []peer.ID) chan *types.BlockBundle {
	return NewFullSync(d.pm, d.log, d.chain, d.ipfs, d.appState, d.potentialForkedPeers, 0, d.statsCollector).SeekBlocks(fromBlock, toBlock, peers)
}

func (d *Downloader) SeekForkedBlocks(ownBlocks []common.Hash, peerId peer.ID) chan types.BlockBundle {
	return NewFullSync(d.pm, d.log, d.chain, d.ipfs, d.appState, d.potentialForkedPeers, 0, d.statsCollector).SeekForkedBlocks(ownBlocks, peerId)
}

func (d *Downloader) HasPotentialFork() bool {
	return d.potentialForkedPeers.Cardinality() > 0
}

func (d *Downloader) GetForkedPeers() mapset.Set {
	return d.potentialForkedPeers
}

func (d *Downloader) ClearPotentialForks() {
	d.potentialForkedPeers.Clear()
}

// syncPlan is how one pass of the downloader brings the chain towards the top.
type syncPlan int

const (
	planFullSync syncPlan = iota
	planFastSync
)

// fullSyncSlice is how many blocks one full sync pass applies while the headers of a fast sync are kept above
// the chain, and after a failed fast sync pass: between passes the downloader looks for a snapshot that lets
// the fast sync go on.
const fullSyncSlice = 1000

func (d *Downloader) createBlockApplier() (loader blockApplier, toHeight uint64) {
	head := d.chain.Head.Height()
	headersKept := d.chain.PreliminaryHead != nil && d.chain.PreliminaryHead.Height() > head
	afterFailedFastSync := d.fullSyncNext
	d.fullSyncNext = false
	var manifest *snapshot.Manifest
	if !afterFailedFastSync && d.cfg.Sync.FastSync && d.top-head >= d.cfg.Sync.ForceFullSync {
		// Peers announce their manifests when they connect. While headers are kept the sync is under way, and a
		// pass waits for manifests only once per sync: the first pass after a restart can come before the
		// announcements, and with no manifest announced a wait before every slice would hold each one.
		manifest = d.getBestManifest(!headersKept || !d.manifestsAwaited)
		d.manifestsAwaited = true
	}

	plan, toHeight := chooseSyncPlan(d.cfg.Sync, head, d.top, d.chain.PreliminaryHead, manifest, afterFailedFastSync)
	if plan == planFastSync {
		d.log.Info("Fast sync will be used")
		return NewFastSync(d.pm, d.log, d.chain, d.ipfs, d.appState, d.potentialForkedPeers, manifest, d.sm, d.bus, d.secStore.GetAddress(), d.keyStore, d.subManager, d.upgrader), toHeight
	}
	if headersKept {
		d.log.Info("Full sync will be used, keeping the fast sync headers", "to", toHeight, "headers", d.chain.PreliminaryHead.Height())
	} else {
		d.log.Info("Full sync will be used")
	}
	return NewFullSync(d.pm, d.log, d.chain, d.ipfs, d.appState, d.potentialForkedPeers, toHeight, d.statsCollector), toHeight
}

// chooseSyncPlan picks fast sync when a snapshot lies far enough above the chain, and full sync otherwise.
// A snapshot below the headers of an unfinished fast sync is not usable: the fast sync goes on from its
// headers, above the snapshot.
//
// A full sync goes at most fullSyncSlice blocks per pass after a failed fast sync pass (afterFailedPass), and
// while those headers are kept above the chain. It keeps the headers while it applies the blocks they hold
// (AddBlock), so that the fast sync can go on from them when a snapshot above them appears, for example after
// its snapshot could not be downloaded. The node never waits for a snapshot: a node a few hundred blocks behind
// catches up by full sync, and a node millions of blocks behind full syncs until the next snapshot takes over.
func chooseSyncPlan(cfg *config.SyncConfig, head, top uint64, preliminaryHead *types.Header, manifest *snapshot.Manifest, afterFailedFastSync bool) (syncPlan, uint64) {
	if !cfg.FastSync || top-head < cfg.ForceFullSync {
		return planFullSync, top
	}
	if afterFailedFastSync {
		return planFullSync, math.Min(top, head+fullSyncSlice)
	}
	if manifest != nil && manifest.Height >= head && manifest.Height-head >= cfg.ForceFullSync &&
		(preliminaryHead == nil || manifest.Height >= preliminaryHead.Height()) {
		return planFastSync, manifest.Height
	}
	if preliminaryHead != nil && preliminaryHead.Height() > head {
		return planFullSync, math.Min(top, head+fullSyncSlice)
	}
	return planFullSync, top
}

// afterFailedPass makes the next pass a full sync slice after any failed fast sync pass: its snapshot could not
// be downloaded or loaded, or its headers stopped below the manifest. Whether the headers are complete cannot be
// told from what peers announce (the manifest, their heights). Trying the fast sync again at once would leave the
// chain where it is for as long as no snapshot loads.
func (d *Downloader) afterFailedPass(applier blockApplier) {
	if _, ok := applier.(*fastSync); ok {
		d.fullSyncNext = true
	}
}

func (d *Downloader) getBestManifest(wait bool) *snapshot.Manifest {

	manifests := d.pm.GetKnownManifests()

	timeout := time.Second * 30
	if len(manifests) == 0 && wait {
		d.log.Info("Wait for snapshot manifests")
		for start := time.Now(); time.Since(start) < timeout && len(manifests) == 0; {
			time.Sleep(2 * time.Second)
			manifests = d.pm.GetKnownManifests()
		}
	}

	var best *snapshot.Manifest
	for _, m := range manifests {
		if (best == nil || best.Height < m.Height) && !d.sm.IsInvalidManifest(m.CidV2) {
			best = m
		}
	}
	if best == nil {
		d.log.Info("Snapshot manifest is not found")
	} else {
		d.log.Info("Found manifest", "height", best.Height)
	}
	return best
}

func (d *Downloader) startSync() {
	d.isSyncing = true
	// A failed pass or a wait for manifests of an earlier sync does not carry over.
	d.fullSyncNext = false
	d.manifestsAwaited = false
	d.chain.StartSync()
	d.sm.StartSync()
}

func (d *Downloader) stopSync() {
	d.chain.StopSync()
	d.sm.StopSync()
	d.isSyncing = false
	d.top = 0
}

func (d *Downloader) BanPeer(peerId peer.ID, reason error) {
	if d.pm != nil {
		d.pm.BanPeer(peerId, reason)
	}
}

// blocksRangeRequester requests a range of blocks from a peer (IdenaGossipHandler).
type blocksRangeRequester interface {
	GetBlocksRange(peerId peer.ID, from uint64, to uint64) (*batch, error)
}

// requestBatches requests the blocks from..toHeight in batches from the peers that know them, in turn, and sends
// the batches on until term is closed.
func requestBatches(pm blocksRangeRequester, knownHeights map[peer.ID]uint64, from, toHeight, batchSize uint64, batches chan<- *batch, term <-chan interface{}) {
	for from <= toHeight && len(knownHeights) > 0 {
		for peer, height := range knownHeights {
			// The previous batch reached toHeight. The peers know blocks above it when toHeight is below the top
			// (a full sync slice, the snapshot of a fast sync): a request from them would be for an empty range.
			if from > toHeight {
				return
			}
			if height < from {
				delete(knownHeights, peer)
				continue
			}
			to := math.Min(from+batchSize, math.Min(toHeight, height))
			if batch, err := pm.GetBlocksRange(peer, from, to); err != nil {
				delete(knownHeights, peer)
				continue
			} else {
				select {
				case batches <- batch:
				case <-term:
					return
				}
			}
			from = to + 1
		}
	}
}

func requestBatch(pm *IdenaGossipHandler, from, to uint64, ignoredPeer peer.ID) *batch {
	knownHeights := pm.GetKnownHeights()
	if knownHeights == nil {
		return nil
	}
	for peerId, height := range knownHeights {
		if (peerId != ignoredPeer || len(knownHeights) == 1) && height >= to {
			if batch, err := pm.GetBlocksRange(peerId, from, to); err != nil {
				continue
			} else {
				return batch
			}
		}
	}
	return nil
}

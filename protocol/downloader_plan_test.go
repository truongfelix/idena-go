package protocol

import (
	"testing"
	"time"

	"github.com/idena-network/idena-go/blockchain"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/core/state/snapshot"
	"github.com/idena-network/idena-go/crypto"
	"github.com/idena-network/idena-go/log"
	"github.com/idena-network/idena-go/secstore"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

func headersAt(height uint64) *types.Header {
	return &types.Header{ProposedHeader: &types.ProposedHeader{Height: height}}
}

func TestChooseSyncPlan(t *testing.T) {
	on := &config.SyncConfig{FastSync: true, ForceFullSync: 100}
	manifest := func(height uint64) *snapshot.Manifest { return &snapshot.Manifest{Height: height} }

	for _, c := range []struct {
		name            string
		cfg             *config.SyncConfig
		head, top       uint64
		preliminaryHead *types.Header
		manifest        *snapshot.Manifest
		want            syncPlan
		wantTo          uint64
	}{
		{"close to the top", on, 1000, 1050, nil, manifest(2000), planFullSync, 1050},
		{"snapshot far above the chain", on, 1000, 5000, nil, manifest(4000), planFastSync, 4000},
		{"snapshot too close to the chain", on, 1000, 5000, nil, manifest(1050), planFullSync, 5000},
		{"snapshot below the chain", on, 1000, 5000, nil, manifest(900), planFullSync, 5000},
		{"no snapshot and no fast sync under way", on, 1000, 5000, nil, nil, planFullSync, 5000},
		{"fast sync switched off", &config.SyncConfig{FastSync: false, ForceFullSync: 100}, 4871137, 11369200, headersAt(11369095), nil, planFullSync, 11369200},
		// Mainnet, 2026-09-28: a new node had its headers up to the snapshot height, and the snapshot
		// could not be downloaded. It full syncs a slice and keeps the headers, instead of waiting.
		{"no snapshot, headers far above the chain", on, 4871137, 11369200, headersAt(11369095), nil, planFullSync, 4872137},
		{"newer snapshot while the headers are kept", on, 4871137, 11370200, headersAt(11369095), manifest(11370095), planFastSync, 11370095},
		// Another manifest of the headers' snapshot (another CID for the same height).
		{"other manifest of the headers' snapshot", on, 4871137, 11369200, headersAt(11369095), manifest(11369095), planFastSync, 11369095},
		// The fast sync would go on from its headers, above this snapshot: it cannot complete the sync.
		{"snapshot below the headers", on, 4871137, 11369200, headersAt(11369095), manifest(11368095), planFullSync, 4872137},
		{"headers barely above the chain", on, 1000, 5000, headersAt(1050), nil, planFullSync, 2000},
		{"slice beyond the top", on, 5000, 5400, headersAt(5300), nil, planFullSync, 5400},
		// The headers are not above the chain: the next block drops them, the full sync goes to the top.
		{"headers at the chain", on, 1000, 5000, headersAt(1000), nil, planFullSync, 5000},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan, to := chooseSyncPlan(c.cfg, c.head, c.top, c.preliminaryHead, c.manifest, false)
			require.Equal(t, c.want, plan)
			require.Equal(t, c.wantTo, to)
		})
	}
}

// After a failed fast sync pass the next pass is one full sync slice, whatever the snapshots and the headers.
func TestChooseSyncPlanAfterFailedFastSync(t *testing.T) {
	on := &config.SyncConfig{FastSync: true, ForceFullSync: 100}

	for _, c := range []struct {
		name            string
		head, top       uint64
		preliminaryHead *types.Header
		manifest        *snapshot.Manifest
		wantTo          uint64
	}{
		{"no headers", 4871137, 11369200, nil, nil, 4872137},
		{"headers kept, a newer snapshot announced", 4871137, 11370200, headersAt(11369095), &snapshot.Manifest{Height: 11370095}, 4872137},
		{"slice beyond the top", 5000, 5400, nil, nil, 5400},
		{"close to the top", 1000, 1050, nil, nil, 1050},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan, to := chooseSyncPlan(on, c.head, c.top, c.preliminaryHead, c.manifest, true)
			require.Equal(t, planFullSync, plan)
			require.Equal(t, c.wantTo, to)
		})
	}
}

// Review of #42: the chain stops at H, the latest snapshot, honest blocks go up to H+200, and a peer
// announces a snapshot at H+200 that cannot be downloaded. The fast sync gets the genuine headers up to
// H+200, then no snapshot. #42 kept the headers and waited for the next snapshot (up to SnapshotRange
// blocks, three times); the node must catch up by full sync instead.
func TestDownloaderFullSyncsAfterFastSyncGetsNoSnapshot(t *testing.T) {
	chain, _, _, _ := blockchain.NewTestBlockchain(false, nil)
	head := chain.Head.Height()
	d := &Downloader{
		cfg:   &config.Config{Sync: &config.SyncConfig{FastSync: true, ForceFullSync: 100}},
		log:   log.New(),
		chain: chain.Blockchain,
		top:   head + 200,
	}
	fake := &fastSync{manifest: &snapshot.Manifest{Height: head + 200}}

	chain.PreliminaryHead = headersAt(head + 200)
	d.afterFailedPass(fake)
	require.True(t, d.fullSyncNext)

	// d.pm is nil: looking for a manifest would panic. The full sync goes to the top in one pass and keeps
	// the headers while it applies the blocks they hold.
	applier, to := d.createBlockApplier()
	require.IsType(t, &fullSync{}, applier)
	require.Equal(t, head+200, to)
	require.False(t, d.fullSyncNext)
}

// Any failed fast sync pass makes the next pass a full sync slice, whatever its headers: whether they are
// complete cannot be told from what peers announce.
func TestDownloaderAfterFailedPass(t *testing.T) {
	for _, c := range []struct {
		name    string
		applier blockApplier
		want    bool
	}{
		{"fast sync", &fastSync{manifest: &snapshot.Manifest{Height: 11369095}}, true},
		{"full sync", &fullSync{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := &Downloader{}
			d.afterFailedPass(c.applier)
			require.Equal(t, c.want, d.fullSyncNext)
		})
	}
}

type recordingRequester struct {
	ranges [][2]uint64
}

func (r *recordingRequester) GetBlocksRange(_ peer.ID, from uint64, to uint64) (*batch, error) {
	r.ranges = append(r.ranges, [2]uint64{from, to})
	return &batch{from: from, to: to}, nil
}

// A pass whose target is below the peers' heights (a full sync slice) ends with the batch that reaches the
// target: the peers left in the round get no request for an empty range.
func TestRequestBatchesStopsAtTarget(t *testing.T) {
	for _, c := range []struct {
		name         string
		knownHeights map[peer.ID]uint64
		want         [][2]uint64
	}{
		{"peers above the target", map[peer.ID]uint64{"a": 5000, "b": 5000, "c": 5000, "d": 5000, "e": 5000},
			[][2]uint64{{1000, 1200}, {1201, 1401}, {1402, 1500}}},
		{"peers below the target", map[peer.ID]uint64{"a": 1100, "b": 1100}, [][2]uint64{{1000, 1100}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &recordingRequester{}
			batches := make(chan *batch, 10)

			requestBatches(r, c.knownHeights, 1000, 1500, 200, batches, make(chan interface{}))

			require.Equal(t, c.want, r.ranges)
			require.Len(t, batches, len(c.want))
		})
	}
}

// A fast sync pass that failed before it stored a header leaves no headers above the chain: the next pass is
// still one full sync slice, so a node far behind goes back to the fast sync after it instead of full syncing
// to the top.
func TestDownloaderSlicesAfterFastSyncWithoutHeaders(t *testing.T) {
	chain, _, _, _ := blockchain.NewTestBlockchain(false, nil)
	head := chain.Head.Height()
	d := &Downloader{
		cfg:   &config.Config{Sync: &config.SyncConfig{FastSync: true, ForceFullSync: 100}},
		log:   log.New(),
		chain: chain.Blockchain,
		top:   head + 5000000,
	}
	chain.PreliminaryHead = headersAt(head)
	d.afterFailedPass(&fastSync{manifest: &snapshot.Manifest{Height: head + 4000000}})
	require.True(t, d.fullSyncNext)

	applier, to := d.createBlockApplier()
	require.IsType(t, &fullSync{}, applier)
	require.Equal(t, head+fullSyncSlice, to)
}

// A restart keeps the fast sync headers, and the first pass can come before the peers announce their manifests:
// it waits for them, and the fast sync goes on. The next passes of the sync do not wait, with headers kept or
// not: with no manifest announced, a wait would hold every pass.
func TestDownloaderWaitsForManifestsOncePerSync(t *testing.T) {
	chain, _, _, _ := blockchain.NewTestBlockchain(false, nil)
	head := chain.Head.Height()
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	secStore := secstore.NewSecStore()
	secStore.AddKey(crypto.FromECDSA(key))
	pm := &IdenaGossipHandler{peers: newPeerSet()}
	p := &protoPeer{id: "peer", log: log.New(), queuedRequests: make(chan *request, 10), finished: make(chan struct{})}
	require.NoError(t, pm.peers.Register(p))
	setManifest := func(m *snapshot.Manifest) {
		p.manifestLock.Lock()
		p.manifest = m
		p.manifestLock.Unlock()
	}
	d := &Downloader{
		cfg:      &config.Config{Sync: &config.SyncConfig{FastSync: true, ForceFullSync: 100}},
		log:      log.New(),
		chain:    chain.Blockchain,
		pm:       pm,
		sm:       state.NewSnapshotManager(db.NewMemDB(), nil, eventbus.New(), nil, nil),
		secStore: secStore,
		top:      head + 5000,
	}
	chain.PreliminaryHead = headersAt(head + 3000)
	go func() {
		time.Sleep(time.Second)
		setManifest(&snapshot.Manifest{Height: head + 4000, CidV2: []byte("cid")})
	}()

	applier, to := d.createBlockApplier()
	require.IsType(t, &fastSync{}, applier)
	require.Equal(t, head+4000, to)
	require.Equal(t, []peer.ID{"peer"}, applier.(*fastSync).announcers)

	setManifest(nil)
	started := time.Now()
	applier, to = d.createBlockApplier()
	require.Less(t, time.Since(started), 5*time.Second)
	require.IsType(t, &fullSync{}, applier)
	require.Equal(t, head+fullSyncSlice, to)

	// The full sync has applied the headers (or a snapshot has loaded), and the chain is still behind: no wait.
	chain.PreliminaryHead = nil
	started = time.Now()
	applier, to = d.createBlockApplier()
	require.Less(t, time.Since(started), 5*time.Second)
	require.IsType(t, &fullSync{}, applier)
	require.Equal(t, head+5000, to)
}

// A failed fast sync pass, a wait for manifests or failed snapshots of an earlier sync do not carry over to the next sync.
func TestDownloaderStartSyncForgetsTheLastSync(t *testing.T) {
	chain, _, _, _ := blockchain.NewTestBlockchain(false, nil)
	d := &Downloader{chain: chain.Blockchain, sm: &state.SnapshotManager{}, fullSyncNext: true, manifestsAwaited: true,
		snapshotFailures: map[uint64]int{11369095: MaxSnapshotHeightFailures}, failedAnnouncers: map[peer.ID]struct{}{"peer": {}}}

	d.startSync()

	require.False(t, d.fullSyncNext)
	require.False(t, d.manifestsAwaited)
	require.Empty(t, d.snapshotFailures)
	require.Empty(t, d.failedAnnouncers)
}

package protocol

import (
	"testing"

	"github.com/idena-network/idena-go/blockchain"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/state/snapshot"
	"github.com/idena-network/idena-go/log"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
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
			plan, to := chooseSyncPlan(c.cfg, c.head, c.top, c.preliminaryHead, c.manifest)
			require.Equal(t, c.want, plan)
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

func TestDownloaderAfterFailedPass(t *testing.T) {
	chain, _, _, _ := blockchain.NewTestBlockchain(false, nil)
	d := &Downloader{chain: chain.Blockchain, top: 11369200}
	fastSyncTo := func(height uint64) *fastSync { return &fastSync{manifest: &snapshot.Manifest{Height: height}} }

	for _, c := range []struct {
		name            string
		preliminaryHead *types.Header
		applier         blockApplier
		want            bool
	}{
		{"snapshot not downloaded or not loaded", headersAt(11369095), fastSyncTo(11369095), true},
		// The headers can go no further than the top.
		{"snapshot above the top", headersAt(11369200), fastSyncTo(11400000), true},
		// The headers are not complete: the fast sync is tried again.
		{"headers below the snapshot", headersAt(11000000), fastSyncTo(11369095), false},
		{"no headers", nil, fastSyncTo(11369095), false},
		{"full sync", headersAt(11369095), &fullSync{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			chain.PreliminaryHead = c.preliminaryHead
			d.fullSyncNext = false
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

package protocol

import (
	"testing"
	"time"

	mapset "github.com/deckarep/golang-set"
	"github.com/idena-network/idena-go/core/state/snapshot"
	"github.com/idena-network/idena-go/log"
	"github.com/idena-network/idena-go/stats/collector"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

func newHeightTestPeer(id peer.ID, height uint64) *protoPeer {
	p := &protoPeer{id: id, log: log.New(), knownHeight: &syncHeight{}, potentialHeight: &syncHeight{}}
	p.setHeight(height)
	return p
}

func TestProtoPeerCapHeight(t *testing.T) {
	p := newHeightTestPeer("peer", 1000)

	p.capHeight(1500) // not below the announced height: no cap
	require.Equal(t, uint64(1000), p.syncTargetHeight())

	p.capHeight(400)
	require.Equal(t, uint64(400), p.syncTargetHeight())
	require.Equal(t, uint64(1000), p.knownHeight.Read(), "the announced height stays")

	p.setHeight(450) // a block served above the cap
	require.Equal(t, uint64(450), p.syncTargetHeight())
	p.setHeight(300) // below the cap: no change
	require.Equal(t, uint64(450), p.syncTargetHeight())

	p.setHeight(1200) // a height above the announced one
	require.Equal(t, uint64(1200), p.syncTargetHeight())
}

// After a link drop every peer is capped at the chain; a cap that nothing lifts lapses, so the node does not
// take a stale head for the top for longer than heightCapTTL.
func TestProtoPeerCapHeightLapses(t *testing.T) {
	defer func(saved time.Duration) { heightCapTTL = saved }(heightCapTTL)
	heightCapTTL = 50 * time.Millisecond

	p := newHeightTestPeer("peer", 1000)
	p.capHeight(400)
	require.Equal(t, uint64(400), p.syncTargetHeight())
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, uint64(1000), p.syncTargetHeight())
}

// The sync target and the batches use the capped height.
func TestGetKnownHeightsCountsCappedPeersUpToTheCap(t *testing.T) {
	pm := &IdenaGossipHandler{peers: newPeerSet()}
	capped := newHeightTestPeer("capped", 5000)
	other := newHeightTestPeer("other", 1200)
	for _, p := range []*protoPeer{capped, other} {
		p.queuedRequests = make(chan *request, 1)
		p.finished = make(chan struct{})
		require.NoError(t, pm.peers.Register(p))
	}
	capped.capHeight(1000)

	require.Equal(t, map[peer.ID]uint64{"capped": 1000, "other": 1200}, pm.GetKnownHeights())
	require.Equal(t, uint64(1200), getTopHeight(pm.GetKnownHeights()))
}

// A peer that announces a height but serves no block of the requested range counts only up to the last height it
// served (here: the chain head), so getTopHeight no longer counts the height it does not serve and the sync loop
// can finish.
func TestFastSyncCapsHeightOfANonServingPeer(t *testing.T) {
	defer func(saved time.Duration) { batchBlockTimeout = saved }(batchBlockTimeout)
	batchBlockTimeout = 20 * time.Millisecond

	st := newFastSyncTest(t)
	head := st.fs.chain.Head.Height()
	st.fs.manifest = &snapshot.Manifest{Height: head + 1000000}

	p := newHeightTestPeer("nonServing", head+1000000)
	// A batch whose blocks never arrive: the peer is not in the handler, so the reload finds no peer and the
	// pass ends at once.
	b := &batch{from: head + 1, to: head + 10, p: p, headers: make(chan *block, 10)}

	require.Error(t, st.fs.processBatch(b, 1))
	require.Equal(t, head, p.syncTargetHeight(), "the peer should count only up to the head it served")
}

func TestFullSyncCapsHeightOfANonServingPeer(t *testing.T) {
	defer func(saved time.Duration) { batchBlockTimeout = saved }(batchBlockTimeout)
	batchBlockTimeout = 20 * time.Millisecond

	st := newFastSyncTest(t)
	head := st.fs.chain.Head.Height()
	fs := NewFullSync(st.fs.pm, log.New(), st.fs.chain, st.fs.ipfs, st.fs.appState, mapset.NewSet(), head+1000000, collector.NewStatsCollector())

	p := newHeightTestPeer("nonServing", head+1000000)
	b := &batch{from: head + 1, to: head + 10, p: p, headers: make(chan *block, 10)}

	require.Error(t, fs.processBatch(b, 1))
	require.Equal(t, head, p.syncTargetHeight(), "the peer should count only up to the head it served")
}

// A peer that serves the first blocks of a batch and then stops counts up to the last one it served.
func TestSyncCapsHeightOfAPeerThatStopsServing(t *testing.T) {
	defer func(saved time.Duration) { batchBlockTimeout = saved }(batchBlockTimeout)
	batchBlockTimeout = 20 * time.Millisecond

	for _, full := range []bool{false, true} {
		st := newFastSyncTest(t)
		head := st.fs.chain.Head.Height()
		st.generateBlock(0)
		st.generateBlock(0)
		st.fs.manifest = &snapshot.Manifest{Height: head + 1000000}
		p := newHeightTestPeer("stops", head+1000000)
		b := &batch{from: head + 1, to: head + 10, p: p, headers: make(chan *block, 10)}
		// Without a certificate: the full sync defers the blocks, the fast sync their headers.
		for _, header := range st.headers {
			b.headers <- &block{Header: header}
		}

		if full {
			fs := NewFullSync(st.fs.pm, log.New(), st.fs.chain, st.fs.ipfs, st.fs.appState, mapset.NewSet(), head+1000000, collector.NewStatsCollector())
			require.Error(t, fs.processBatch(b, 1))
		} else {
			require.Error(t, st.fs.processBatch(b, 1))
		}
		require.Equal(t, head+2, p.syncTargetHeight(), "full sync %v", full)
	}
}

// The full sync wants a certificate on the block at the height a peer announced (its top block has one). A cap
// does not move that height: an old block below the top often has no certificate, and asking one of a capped
// peer would get the peer banned.
func TestFullSyncCertificateRuleKeepsTheAnnouncedHeight(t *testing.T) {
	st := newFastSyncTest(t)
	head := st.fs.chain.Head.Height()
	next := st.generateBlock(0)
	fs := NewFullSync(st.fs.pm, log.New(), st.fs.chain, st.fs.ipfs, st.fs.appState, mapset.NewSet(), head+1000, collector.NewStatsCollector())

	capped := newHeightTestPeer("capped", head+1000)
	capped.capHeight(next.Height())
	require.NoError(t, fs.validateHeader(&block{Header: next}, capped))

	top := newHeightTestPeer("top", next.Height())
	require.Equal(t, BlockCertIsMissing, fs.validateHeader(&block{Header: next}, top))
}

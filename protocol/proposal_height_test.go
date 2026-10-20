package protocol

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/pengings"
	"github.com/idena-network/idena-go/stats/collector"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-msgio"
	"github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
)

type proposalStream struct{ *bytes.Buffer }

func (proposalStream) Close() error { return nil }

var _ io.ReadWriteCloser = proposalStream{}

func newProposalTestPeer(id peer.ID, height uint64) *protoPeer {
	p := newHeightTestPeer(id, height)
	p.msgCache = cache.New(msgCacheAliveTime, msgCacheGcTime)
	p.transportErr = make(chan error, 1)
	p.metrics = &metricCollector{
		incomeMessage: func(uint64, int, time.Duration, string) {},
		compress:      func(uint64, int) {},
	}
	return p
}

// handleProofProposal passes one proof proposal for round from peer p through the gossip handler.
func handleProofProposal(t *testing.T, h *IdenaGossipHandler, p *protoPeer, round uint64) {
	proof := make([]byte, 129)
	proof[0] = byte(round)
	proof[1] = byte(round >> 8)
	buf := new(bytes.Buffer)
	w := msgio.NewWriter(buf)
	require.NoError(t, w.WriteMsg(makeMsg(ProposeProof, &types.ProofProposal{Proof: proof, Round: round}, common.MultiShard)))
	p.rw = msgio.NewReadWriter(proposalStream{buf})
	require.NoError(t, h.handle(p))
}

// A proof proposal for a coming round, kept or past the pending window, only gives the peer a hint: its known height
// stays, the sync asks it for the blocks up to the hint. Round 0 changes nothing.
func TestProofProposalForAComingRoundIsAHint(t *testing.T) {
	st := newFastSyncTest(t)
	proposals, _ := pengings.NewProposals(st.fs.chain, st.fs.appState, nil, nil, collector.NewStatsCollector())
	h := &IdenaGossipHandler{peers: newPeerSet(), proposals: proposals}
	head := st.fs.chain.Head.Height()

	for _, round := range []uint64{head + 2, head + 1 + pengings.DeferFutureProposalsPeriod - 1, head + 1000000} {
		p := newProposalTestPeer("proposer", head)
		handleProofProposal(t, h, p, round)
		require.Equal(t, head, p.knownHeight.Read(), "round %d", round)
		require.Equal(t, round-1, p.hintHeight.Read(), "round %d", round)
		require.Equal(t, round-1, p.syncTargetHeight(), "round %d", round)

		p.capHeight(head) // the peer did not serve the blocks
		require.Equal(t, head, p.syncTargetHeight(), "round %d", round)
	}

	p := newProposalTestPeer("zero", head)
	handleProofProposal(t, h, p, 0)
	require.Equal(t, head, p.syncTargetHeight())
	require.Equal(t, head, p.potentialHeight.Read())
}

// The blocks of a range a peer answers raise its height only once their headers check out (processBatch), not
// when they arrive.
func TestBlocksRangeAnswerDoesNotRaiseThePeerHeight(t *testing.T) {
	h := &IdenaGossipHandler{peers: newPeerSet(), incomeBatches: &sync.Map{}}
	p := newProposalTestPeer("server", 1000)
	b := &batch{from: 1001, to: 1002, p: p, headers: make(chan *block, 2), maxBlocks: 2}
	peerBatches := &sync.Map{}
	peerBatches.Store(uint32(7), b)
	h.incomeBatches.Store(p.id, peerBatches)

	answer := &blockRange{BatchId: 7, Blocks: []*block{
		{Header: &types.Header{ProposedHeader: &types.ProposedHeader{Height: 1001}}},
		{Header: &types.Header{ProposedHeader: &types.ProposedHeader{Height: 1000000}}},
	}}
	buf := new(bytes.Buffer)
	require.NoError(t, msgio.NewWriter(buf).WriteMsg(makeMsg(BlocksRange, answer, common.MultiShard)))
	p.rw = msgio.NewReadWriter(proposalStream{buf})
	require.NoError(t, h.handle(p))

	require.Len(t, b.headers, 2, "the blocks are passed on")
	require.Equal(t, uint64(1000), p.syncTargetHeight())
	require.Equal(t, uint64(1000), p.knownHeight.Read())
}

package protocol

import (
	"sync"
	"testing"
	"time"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/log"
	models "github.com/idena-network/idena-go/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func blockAt(height uint64) *block {
	return &block{Header: headersAt(height)}
}

func TestBatchFillPassesAnAnswerThatFits(t *testing.T) {
	b := newBatch(nil, 1, 3, 3)
	var heights []uint64

	b.fill([]*block{blockAt(1), blockAt(2)}, func(blk *block) { heights = append(heights, blk.Header.Height()) })

	require.Equal(t, []uint64{1, 2}, heights)
	require.Equal(t, uint64(1), (<-b.headers).Header.Height())
	require.Equal(t, uint64(2), (<-b.headers).Header.Height())
	_, open := <-b.headers
	require.False(t, open)
}

// An answer with more blocks than the batch was requested for is not passed on, and fill returns although
// nothing reads the batch.
func TestBatchFillDropsALongerAnswer(t *testing.T) {
	b := newBatch(nil, 1, 2, 2)
	done := make(chan struct{})

	go func() {
		defer close(done)
		b.fill([]*block{blockAt(1), blockAt(2), blockAt(3), blockAt(4), blockAt(5)}, func(*block) { t.Error("block passed on") })
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fill did not return")
	}
	_, open := <-b.headers
	require.False(t, open)
}

// A fork answer goes on after the blocks for the hashes sent until a block with a certificate
// (blockchain.ReadBlockForForkedPeer): the whole answer is passed on.
func TestForkBatchTakesTheBlocksUpToACertificate(t *testing.T) {
	h := &IdenaGossipHandler{peers: newPeerSet(), incomeBatches: &sync.Map{}}
	p := &protoPeer{id: "peer", log: log.New(), queuedRequests: make(chan *request, 10), finished: make(chan struct{})}
	require.NoError(t, h.peers.Register(p))
	b, err := h.GetForkBlockRange(p.id, make([]common.Hash, 100))
	require.NoError(t, err)

	answer := make([]*block, 0, 100+forkAnswerExtraBlocks)
	for i := uint64(1); i <= 100+forkAnswerExtraBlocks; i++ {
		answer = append(answer, blockAt(i))
	}
	passed := 0
	b.fill(answer, func(*block) { passed++ })

	require.Equal(t, len(answer), passed)
	require.Len(t, b.headers, len(answer))
}

// Requests made at the same time each carry the id their batch is stored under, so every answer reaches the
// batch it was sent for.
func TestBlockRequestsCarryTheirBatchIds(t *testing.T) {
	h := &IdenaGossipHandler{peers: newPeerSet(), incomeBatches: &sync.Map{}}
	p := &protoPeer{id: "peer", log: log.New(), queuedRequests: make(chan *request, 10000), finished: make(chan struct{})}
	require.NoError(t, h.peers.Register(p))

	const n = 2000
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = h.GetBlocksRange(p.id, uint64(i), uint64(i))
			} else {
				// The number of hashes sizes the batch: a different number for each request tells them apart.
				_, err = h.GetForkBlockRange(p.id, make([]common.Hash, 1+i/2))
			}
			assert.NoError(t, err)
		}(i)
	}
	wg.Wait()

	require.Len(t, p.queuedRequests, n)
	ib, ok := h.incomeBatches.Load(p.id)
	require.True(t, ok)
	batches := ib.(*sync.Map)
	for i := 0; i < n; i++ {
		switch r := (<-p.queuedRequests).data.(type) {
		case *models.ProtoGetBlocksRangeRequest:
			stored, ok := batches.Load(r.BatchId)
			require.True(t, ok)
			require.Equal(t, r.From, stored.(*batch).from, "a request carries the id of another batch")
		case *models.ProtoGetForkBlockRangeRequest:
			stored, ok := batches.Load(r.BatchId)
			require.True(t, ok)
			require.Equal(t, len(r.Blocks)+forkAnswerExtraBlocks, stored.(*batch).maxBlocks, "a request carries the id of another batch")
		default:
			t.Fatalf("unexpected request %T", r)
		}
	}
}

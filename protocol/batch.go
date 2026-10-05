package protocol

import (
	"github.com/golang/protobuf/proto"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/core/state"
	models "github.com/idena-network/idena-go/protobuf"
	"github.com/libp2p/go-libp2p/core/peer"
)

type batch struct {
	p    *protoPeer
	from uint64
	to   uint64
	// maxBlocks is the longest answer the request can get; headers holds that many blocks.
	maxBlocks int
	headers   chan *block
}

// newBatch makes the batch for a request whose answer has at most maxBlocks blocks.
func newBatch(p *protoPeer, from, to uint64, maxBlocks int) *batch {
	return &batch{p: p, from: from, to: to, maxBlocks: maxBlocks, headers: make(chan *block, maxBlocks)}
}

// fill passes the blocks of a peer's answer to the batch, then closes it. An answer longer than the request can
// get is not passed on: the batch is closed without blocks, and its reader gives up on the peer. Every block of
// an answer that fits goes into the channel's buffer, so fill returns whether or not the batch is still read.
func (b *batch) fill(blocks []*block, onBlock func(*block)) {
	if len(blocks) <= b.maxBlocks {
		for _, blk := range blocks {
			b.headers <- blk
			onBlock(blk)
		}
	}
	close(b.headers)
}

type block struct {
	Header       *types.Header
	Cert         *types.BlockCert         `rlp:"nil"`
	IdentityDiff *state.IdentityStateDiff `rlp:"nil"`
}

type blockPeer struct {
	block
	peerId peer.ID
}

type blockRange struct {
	BatchId uint32
	Blocks  []*block
}

func (r *blockRange) ToBytes() ([]byte, error) {
	protoObj := &models.ProtoGossipBlockRange{
		BatchId: r.BatchId,
	}
	for _, item := range r.Blocks {
		b := new(models.ProtoGossipBlockRange_Block)
		if item.Header != nil {
			b.Header = item.Header.ToProto()
		}
		if item.Cert != nil {
			b.Cert = item.Cert.ToProto()
		}
		if item.IdentityDiff != nil {
			b.Diff = item.IdentityDiff.ToProto()
		}
		protoObj.Blocks = append(protoObj.Blocks, b)
	}
	return proto.Marshal(protoObj)
}

func (r *blockRange) FromBytes(data []byte) error {
	protoObj := new(models.ProtoGossipBlockRange)
	if err := proto.Unmarshal(data, protoObj); err != nil {
		return err
	}

	r.BatchId = protoObj.BatchId
	for _, item := range protoObj.Blocks {
		b := new(block)
		if item.Header != nil {
			b.Header = new(types.Header).FromProto(item.Header)
		}
		if item.Cert != nil {
			b.Cert = new(types.BlockCert).FromProto(item.Cert)
		}
		if item.Diff != nil {
			b.IdentityDiff = new(state.IdentityStateDiff).FromProto(item.Diff)
		}
		r.Blocks = append(r.Blocks, b)
	}
	return nil
}

func (r *blockRange) IsValid() bool {
	for _, item := range r.Blocks {
		if !item.Header.IsValid() {
			return false
		}
	}
	return true
}

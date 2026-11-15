package events

import (
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
)

const (
	NewTxEventID                 = eventbus.EventID("transaction-new")
	AddBlockEventID              = eventbus.EventID("block-add")
	NewFlipKeyID                 = eventbus.EventID("flip-key-new")
	FastSyncCompleted            = eventbus.EventID("fast-sync-completed")
	NewFlipEventID               = eventbus.EventID("flip-new")
	NewFlipKeysPackageID         = eventbus.EventID("flip-keys-package-new")
	IpfsPortChangedEventId       = eventbus.EventID("ipfs-port-changed")
	DeleteFlipEventID            = eventbus.EventID("flip-delete")
	PeersEventID                 = eventbus.EventID("peers")
	BlockchainResetEventID       = eventbus.EventID("chain-reset")
	DatabaseInitEventId          = eventbus.EventID("db-init")
	DatabaseInitCompletedEventId = eventbus.EventID("db-init-completed")
	DatabaseCompactionEventId    = eventbus.EventID("db-compaction")
	IpfsGcEventId                = eventbus.EventID("ipfc-gc")
)

type NewTxEvent struct {
	Tx       *types.Transaction
	Own      bool
	ShardId  common.ShardId
	Deferred bool
}

func (e *NewTxEvent) EventID() eventbus.EventID {
	return NewTxEventID
}

type NewBlockEvent struct {
	Block *types.Block
}

func (e *NewBlockEvent) EventID() eventbus.EventID {
	return AddBlockEventID
}

type NewFlipKeyEvent struct {
	Key     *types.PublicFlipKey
	ShardId common.ShardId
	Own     bool
}

func (e *NewFlipKeyEvent) EventID() eventbus.EventID {
	return NewFlipKeyID
}

type FastSyncCompletedEvent struct {
}

func (FastSyncCompletedEvent) EventID() eventbus.EventID {
	return FastSyncCompleted
}

type NewFlipEvent struct {
	Flip *types.Flip
}

func (NewFlipEvent) EventID() eventbus.EventID {
	return NewFlipEventID
}

type NewFlipKeysPackageEvent struct {
	Key     *types.PrivateFlipKeysPackage
	ShardId common.ShardId
	Own     bool
}

func (e *NewFlipKeysPackageEvent) EventID() eventbus.EventID {
	return NewFlipKeysPackageID
}

type DeleteFlipEvent struct {
	FlipCid []byte
}

func (DeleteFlipEvent) EventID() eventbus.EventID {
	return DeleteFlipEventID
}

type BlockchainResetEvent struct {
	Header      *types.Header
	RevertedTxs []*types.Transaction
}

func (e *BlockchainResetEvent) EventID() eventbus.EventID {
	return BlockchainResetEventID
}

type DatabaseInitEvent struct {
	Message string
}

func (e *DatabaseInitEvent) EventID() eventbus.EventID {
	return DatabaseInitEventId
}

type DatabaseInitCompletedEvent struct {
}

func (e *DatabaseInitCompletedEvent) EventID() eventbus.EventID {
	return DatabaseInitCompletedEventId
}

// DatabaseCompactionEvent marks the start (Completed false) and the end (Completed true) of the chain database's
// full compaction at start-up.
type DatabaseCompactionEvent struct {
	Completed bool
}

func (e *DatabaseCompactionEvent) EventID() eventbus.EventID {
	return DatabaseCompactionEventId
}

type IpfsGcEvent struct {
	Message   string
	Completed bool
}

func (e *IpfsGcEvent) EventID() eventbus.EventID {
	return IpfsGcEventId
}

package state

import (
	"sync/atomic"

	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/events"
)

type NodeState struct {
	info    string
	syncing bool
	// compacting is set while the chain database is fully compacted at start-up (minutes on a hard disk).
	compacting atomic.Bool
}

// CompactingMessage is the node's message while the chain database is compacted.
const CompactingMessage = "Compacting database..."

func NewNodeState(eventBus eventbus.Bus) *NodeState {
	nodeState := &NodeState{}
	eventBus.Subscribe(events.DatabaseInitEventId, func(event eventbus.Event) {
		nodeState.info = "Initializing database..."
	})
	eventBus.Subscribe(events.DatabaseInitCompletedEventId, func(event eventbus.Event) {
		nodeState.compacting.Store(false)
		nodeState.info = ""
	})
	eventBus.Subscribe(events.DatabaseCompactionEventId, func(event eventbus.Event) {
		if event.(*events.DatabaseCompactionEvent).Completed {
			nodeState.compacting.Store(false)
			nodeState.info = "Initializing database..."
		} else {
			nodeState.compacting.Store(true)
			nodeState.info = CompactingMessage
		}
	})
	eventBus.Subscribe(events.IpfsGcEventId, func(event eventbus.Event) {
		e := event.(*events.IpfsGcEvent)
		if e.Completed {
			nodeState.info = ""
			nodeState.syncing = false
		} else {
			nodeState.info = e.Message
			nodeState.syncing = true
		}
	})
	return nodeState
}

func (nodeState *NodeState) Info() string {
	return nodeState.info
}

// Compacting reports whether the chain database is being compacted.
func (nodeState *NodeState) Compacting() bool {
	return nodeState.compacting.Load()
}

func (nodeState *NodeState) Syncing() bool {
	return nodeState.syncing
}

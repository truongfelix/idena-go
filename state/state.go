package state

import (
	"sync"

	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/events"
)

// NodeState is what bcn_syncing says while the node starts: written by the start-up and IPFS events, read by the
// RPC server meanwhile, so every field is behind mu.
type NodeState struct {
	mu      sync.RWMutex
	info    string
	syncing bool
	// compacting is set while the chain database is fully compacted at start-up (minutes on a hard disk).
	compacting bool
}

// CompactingMessage is the node's message while the chain database is compacted.
const CompactingMessage = "Compacting database..."

// InitializingMessage is the node's message while it opens its chain database.
const InitializingMessage = "Initializing database..."

func NewNodeState(eventBus eventbus.Bus) *NodeState {
	nodeState := &NodeState{}
	eventBus.Subscribe(events.DatabaseInitEventId, func(event eventbus.Event) {
		nodeState.set(func() { nodeState.info = InitializingMessage })
	})
	eventBus.Subscribe(events.DatabaseInitCompletedEventId, func(event eventbus.Event) {
		nodeState.set(func() {
			nodeState.compacting = false
			nodeState.info = ""
		})
	})
	eventBus.Subscribe(events.DatabaseCompactionEventId, func(event eventbus.Event) {
		completed := event.(*events.DatabaseCompactionEvent).Completed
		nodeState.set(func() {
			nodeState.compacting = !completed
			if completed {
				nodeState.info = InitializingMessage
			} else {
				nodeState.info = CompactingMessage
			}
		})
	})
	eventBus.Subscribe(events.IpfsGcEventId, func(event eventbus.Event) {
		e := event.(*events.IpfsGcEvent)
		nodeState.set(func() {
			if e.Completed {
				nodeState.info = ""
				nodeState.syncing = false
			} else {
				nodeState.info = e.Message
				nodeState.syncing = true
			}
		})
	})
	return nodeState
}

func (nodeState *NodeState) set(change func()) {
	nodeState.mu.Lock()
	defer nodeState.mu.Unlock()
	change()
}

func (nodeState *NodeState) Info() string {
	nodeState.mu.RLock()
	defer nodeState.mu.RUnlock()
	return nodeState.info
}

// Starting returns the message and whether the chain database is being compacted, read together.
func (nodeState *NodeState) Starting() (info string, compacting bool) {
	nodeState.mu.RLock()
	defer nodeState.mu.RUnlock()
	return nodeState.info, nodeState.compacting
}

func (nodeState *NodeState) Syncing() bool {
	nodeState.mu.RLock()
	defer nodeState.mu.RUnlock()
	return nodeState.syncing
}

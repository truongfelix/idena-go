package api

import (
	"testing"

	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/events"
	"github.com/idena-network/idena-go/state"
	"github.com/stretchr/testify/require"
)

// While the chain database is opened, bcn_syncing (the only call served then) says whether it is being compacted.
func TestInitialSyncingReportsCompaction(t *testing.T) {
	bus := eventbus.New()
	api := NewBlockchainInitialApi(state.NewNodeState(bus))

	bus.Publish(&events.DatabaseInitEvent{})
	require.Equal(t, Syncing{Syncing: true, Message: "Initializing database..."}, api.Syncing())

	bus.Publish(&events.DatabaseCompactionEvent{})
	require.Equal(t, Syncing{Syncing: true, Message: state.CompactingMessage, Compacting: true}, api.Syncing())

	bus.Publish(&events.DatabaseCompactionEvent{Completed: true})
	require.Equal(t, Syncing{Syncing: true, Message: "Initializing database..."}, api.Syncing())

	bus.Publish(&events.DatabaseCompactionEvent{})
	bus.Publish(&events.DatabaseInitCompletedEvent{})
	require.Equal(t, Syncing{Syncing: true}, api.Syncing(), "the end of the opening clears the compaction")
}

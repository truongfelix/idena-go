package api

import (
	"sync"
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
	require.Equal(t, Syncing{Syncing: true, Message: state.InitializingMessage}, api.Syncing())

	bus.Publish(&events.DatabaseCompactionEvent{})
	require.Equal(t, Syncing{Syncing: true, Message: state.CompactingMessage, Compacting: true}, api.Syncing())

	bus.Publish(&events.DatabaseCompactionEvent{Completed: true})
	require.Equal(t, Syncing{Syncing: true, Message: state.InitializingMessage}, api.Syncing())

	bus.Publish(&events.DatabaseCompactionEvent{})
	bus.Publish(&events.DatabaseInitCompletedEvent{})
	require.Equal(t, Syncing{Syncing: true}, api.Syncing(), "the end of the opening clears the compaction")
}

// bcn_syncing is served while the start-up goroutine opens the database: a read beside the events sees one state,
// whole (run with -race).
func TestInitialSyncingWhileTheDatabaseOpens(t *testing.T) {
	bus := eventbus.New()
	api := NewBlockchainInitialApi(state.NewNodeState(bus))
	bus.Publish(&events.DatabaseInitEvent{})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	mixed := 0
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			s := api.Syncing()
			if s.Compacting != (s.Message == state.CompactingMessage) {
				mixed++
			}
		}
	}()
	for i := 0; i < 2000; i++ {
		bus.Publish(&events.DatabaseCompactionEvent{})
		bus.Publish(&events.DatabaseCompactionEvent{Completed: true})
	}
	close(stop)
	wg.Wait()
	require.Zero(t, mixed, "an answer mixed the compacting flag of one state with the message of another")
}

//go:build !idena_memory_ipfs

package protocol

import (
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/events"
)

// subscribeIpfsPortChanged moves the handler to the host and pubsub of the
// IPFS node started on a new port.
func (h *IdenaGossipHandler) subscribeIpfsPortChanged(setHandler func()) {
	h.bus.Subscribe(events.IpfsPortChangedEventId, func(e eventbus.Event) {
		portChangedEvent := e.(*events.IpfsPortChangedEvent)
		h.host = portChangedEvent.Host
		h.pubsub = portChangedEvent.PubSub
		setHandler()
	})
}

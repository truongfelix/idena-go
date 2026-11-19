package ipfs

import (
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// A protocol advertised from the start is registered on the host as soon as the host is made, before its first
// connection, with a handler that resets each stream until the protocol's own handler replaces it. A peer learns the
// node's protocols when it connects (identify): one registered later is missing from what the peers that connected
// in between saw, and the node used to close all its connections to have them learn it again, its relay
// reservations included.
type earlyProtocol struct {
	id    protocol.ID
	match func(protocol.ID) bool
}

var (
	earlyProtocolsMutex sync.Mutex
	earlyProtocols      []earlyProtocol
)

// AdvertiseFromStart registers a protocol on every host the IPFS node makes from now on, from the host's creation.
func AdvertiseFromStart(id protocol.ID, match func(protocol.ID) bool) {
	earlyProtocolsMutex.Lock()
	defer earlyProtocolsMutex.Unlock()
	for _, p := range earlyProtocols {
		if p.id == id {
			return
		}
	}
	earlyProtocols = append(earlyProtocols, earlyProtocol{id: id, match: match})
}

func registerEarlyProtocols(h host.Host) {
	earlyProtocolsMutex.Lock()
	protocols := append([]earlyProtocol(nil), earlyProtocols...)
	earlyProtocolsMutex.Unlock()
	for _, p := range protocols {
		h.SetStreamHandlerMatch(p.id, p.match, resetStream)
	}
}

// resetStream answers a stream that arrives before the protocol's handler is set: the peer tries again later.
func resetStream(s network.Stream) {
	_ = s.Reset()
}

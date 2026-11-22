package protocol

import (
	"context"
	"errors"
	"testing"

	"github.com/idena-network/idena-go/config"
	core "github.com/libp2p/go-libp2p/core"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
)

type recordingNetwork struct {
	network.Network
	peers      []peer.ID
	closed     []peer.ID
	closeError map[peer.ID]error
}

func (n *recordingNetwork) Peers() []peer.ID {
	return append([]peer.ID(nil), n.peers...)
}

func (n *recordingNetwork) ClosePeer(id peer.ID) error {
	n.closed = append(n.closed, id)
	return n.closeError[id]
}

type recordingHost struct {
	core.Host
	network    *recordingNetwork
	peerstore  *recordingPeerstore
	protocol   libp2pprotocol.ID
	matcher    func(libp2pprotocol.ID) bool
	streamFunc network.StreamHandler
	connected  []peer.AddrInfo
	advertised []libp2pprotocol.ID
}

// recordingMux lists the protocols the host advertised before the Idena handler is set.
type recordingMux struct {
	libp2pprotocol.Switch
	protocols []libp2pprotocol.ID
}

func (m *recordingMux) Protocols() []libp2pprotocol.ID {
	return m.protocols
}

func (h *recordingHost) Mux() libp2pprotocol.Switch {
	return &recordingMux{protocols: h.advertised}
}

type recordingPeerstore struct {
	peerstore.Peerstore
	peerInfo map[peer.ID]peer.AddrInfo
}

func (p *recordingPeerstore) PeerInfo(id peer.ID) peer.AddrInfo {
	return p.peerInfo[id]
}

func (h *recordingHost) Network() network.Network {
	return h.network
}

func (h *recordingHost) Peerstore() peerstore.Peerstore {
	return h.peerstore
}

func (h *recordingHost) Connect(_ context.Context, peerInfo peer.AddrInfo) error {
	h.connected = append(h.connected, peerInfo)
	return nil
}

func (h *recordingHost) SetStreamHandlerMatch(id libp2pprotocol.ID, matcher func(libp2pprotocol.ID) bool, handler network.StreamHandler) {
	h.protocol = id
	h.matcher = matcher
	h.streamFunc = handler
}

func TestRegisterIdenaStreamHandlerRefreshesPreexistingPeers(t *testing.T) {
	peerA := peer.ID("peer-a")
	peerB := peer.ID("peer-b")
	wantError := errors.New("close failed")
	networkRecorder := &recordingNetwork{
		peers:      []peer.ID{peerA, peerB},
		closeError: map[peer.ID]error{peerB: wantError},
	}
	host := &recordingHost{
		network: networkRecorder,
		peerstore: &recordingPeerstore{peerInfo: map[peer.ID]peer.AddrInfo{
			peerA: {ID: peerA},
			peerB: {ID: peerB},
		}},
	}
	handler := func(network.Stream) {}

	reconnectTargets, closed, failed := registerIdenaStreamHandler(host, handler)

	if closed != 1 || failed != 1 {
		t.Fatalf("registerIdenaStreamHandler() = (%d, %d), want (1, 1)", closed, failed)
	}
	if host.protocol != IdenaProtocol || host.streamFunc == nil {
		t.Fatal("Idena stream handler was not registered")
	}
	if !host.matcher(IdenaProtocol) || host.matcher("/idena/gossip/2.0.0") {
		t.Fatal("registered matcher does not preserve Idena protocol compatibility")
	}
	if len(networkRecorder.closed) != 2 || networkRecorder.closed[0] != peerA || networkRecorder.closed[1] != peerB {
		t.Fatalf("closed peers = %v, want [%s %s]", networkRecorder.closed, peerA, peerB)
	}
	if len(reconnectTargets) != 1 || reconnectTargets[0].ID != peerA {
		t.Fatalf("reconnect targets = %v, want peer %s", reconnectTargets, peerA)
	}

	reconnected, reconnectFailed := reconnectIdenaPeers(host, reconnectTargets)
	if reconnected != 1 || reconnectFailed != 0 {
		t.Fatalf("reconnectIdenaPeers() = (%d, %d), want (1, 0)", reconnected, reconnectFailed)
	}
	if len(host.connected) != 1 || host.connected[0].ID != peerA {
		t.Fatalf("connected peers = %v, want peer %s", host.connected, peerA)
	}
}

// A host that advertised the Idena protocol from its creation keeps its connections: every peer saw the protocol
// when it connected, and the relay reservations stay.
func TestRegisterIdenaStreamHandlerKeepsConnectionsWhenAdvertisedFromStart(t *testing.T) {
	peerA := peer.ID("peer-a")
	networkRecorder := &recordingNetwork{peers: []peer.ID{peerA}}
	host := &recordingHost{
		network:    networkRecorder,
		peerstore:  &recordingPeerstore{peerInfo: map[peer.ID]peer.AddrInfo{peerA: {ID: peerA}}},
		advertised: []libp2pprotocol.ID{"/ipfs/id/1.0.0", IdenaProtocol},
	}

	reconnectTargets, closed, failed := registerIdenaStreamHandler(host, func(network.Stream) {})

	if closed != 0 || failed != 0 || len(reconnectTargets) != 0 || len(networkRecorder.closed) != 0 {
		t.Fatalf("closed %v (%d, %d), reconnect %v: want no connection closed", networkRecorder.closed, closed, failed, reconnectTargets)
	}
	if host.protocol != IdenaProtocol || host.streamFunc == nil {
		t.Fatal("Idena stream handler was not registered")
	}
}

type watchedNetwork struct {
	network.Network
	conns    []network.Conn
	notifiee network.Notifiee
}

func (n *watchedNetwork) Conns() []network.Conn {
	return n.conns
}

func (n *watchedNetwork) Notify(f network.Notifiee) {
	n.notifiee = f
}

type watchedHost struct {
	core.Host
	net *watchedNetwork
}

func (h *watchedHost) Network() network.Network {
	return h.net
}

// The connections open before the handler (kept since the protocol is advertised from the start) are dial
// candidates at once, as the ones opened later.
func TestWatchConnectionsTakesTheConnectionsAlreadyOpen(t *testing.T) {
	open := &closableConn{remote: peer.ID("seed")}
	net := &watchedNetwork{conns: []network.Conn{open}}
	host := &watchedHost{net: net}
	m := NewConnManager(host, config.P2P{})

	watchConnections(host, m)

	if net.notifiee == nil {
		t.Fatal("the connections opened later are not followed")
	}
	if got := m.activeConnections[open.remote]; got != open {
		t.Fatalf("stored = %v, want the connection open before the handler", got)
	}
}

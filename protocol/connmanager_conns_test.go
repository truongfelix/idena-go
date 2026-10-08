package protocol

import (
	"testing"

	"github.com/idena-network/idena-go/config"
	core "github.com/libp2p/go-libp2p/core"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// peerConnsHost answers ConnsToPeer with the connections a test leaves open, as the swarm does when it sends a
// Disconnected notification: the closed connection is already gone from the list.
type peerConnsHost struct {
	core.Host
	net *peerConnsNetwork
}

func (h *peerConnsHost) Network() network.Network {
	return h.net
}

type peerConnsNetwork struct {
	network.Network
	open map[peer.ID][]network.Conn
}

func (n *peerConnsNetwork) ConnsToPeer(id peer.ID) []network.Conn {
	return n.open[id]
}

type closableConn struct {
	network.Conn
	remote peer.ID
	closed bool
}

func (c *closableConn) RemotePeer() peer.ID {
	return c.remote
}

func (c *closableConn) IsClosed() bool {
	return c.closed
}

func newConnsTestManager() (*ConnManager, *peerConnsNetwork) {
	net := &peerConnsNetwork{open: make(map[peer.ID][]network.Conn)}
	return NewConnManager(&peerConnsHost{net: net}, config.P2P{}), net
}

// closeConn closes c the way the swarm does: out of the open list first, then the notification.
func closeConn(m *ConnManager, net *peerConnsNetwork, c *closableConn) {
	c.closed = true
	var left []network.Conn
	for _, o := range net.open[c.remote] {
		if o != c {
			left = append(left, o)
		}
	}
	net.open[c.remote] = left
	m.RemoveConnection(c)
}

func TestRemoveConnectionKeepsPeerWhileAnotherConnectionIsOpen(t *testing.T) {
	m, net := newConnsTestManager()
	id := peer.ID("nat-peer")
	relayed := &closableConn{remote: id}
	direct := &closableConn{remote: id}
	net.open[id] = []network.Conn{relayed, direct}
	m.storeConnection(direct)
	m.storeConnection(relayed)

	// The relay ends the relayed connection; the direct one hole punching opened stays.
	closeConn(m, net, relayed)
	if got := m.activeConnections[id]; got != direct {
		t.Fatalf("after the relayed connection closed, stored = %v, want the direct connection", got)
	}

	closeConn(m, net, direct)
	if got, ok := m.activeConnections[id]; ok {
		t.Fatalf("after the last connection closed, the peer is still stored: %v", got)
	}
}

func TestRemoveConnectionOfAnotherConnectionKeepsTheStoredOne(t *testing.T) {
	m, net := newConnsTestManager()
	id := peer.ID("nat-peer")
	relayed := &closableConn{remote: id}
	direct := &closableConn{remote: id}
	net.open[id] = []network.Conn{relayed, direct}
	m.storeConnection(relayed)
	m.storeConnection(direct)

	closeConn(m, net, relayed)
	if got := m.activeConnections[id]; got != direct {
		t.Fatalf("stored = %v, want the direct connection", got)
	}
}

func TestRemoveConnectionForgetsThePeerWithNoConnectionLeft(t *testing.T) {
	m, net := newConnsTestManager()
	id := peer.ID("peer")
	only := &closableConn{remote: id}
	net.open[id] = []network.Conn{only}
	m.storeConnection(only)

	closeConn(m, net, only)
	if len(m.activeConnections) != 0 {
		t.Fatalf("active connections = %d, want 0", len(m.activeConnections))
	}
}

func TestStoreConnectionSkipsAConnectionClosedDuringTheWait(t *testing.T) {
	m, net := newConnsTestManager()
	id := peer.ID("short-lived")
	c := &closableConn{remote: id}
	net.open[id] = []network.Conn{c}

	// Closed (and its Disconnected notification handled) before AddConnection's wait ends.
	closeConn(m, net, c)
	m.storeConnection(c)
	if got, ok := m.activeConnections[id]; ok {
		t.Fatalf("a closed connection was stored: %v", got)
	}
}

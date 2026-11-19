package protocol

import (
	"context"
	"testing"
	"time"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/log"
	"github.com/libp2p/go-libp2p"
	core "github.com/libp2p/go-libp2p/core"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

const relayPeerId = "12D3KooWELG2LZXSJYvToBH4DKZmaW2dzwCy9W4orpzvhqsuh2mW"

func newExchangeHost(t *testing.T) core.Host {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// newExchangeNode is a gossip handler with only what peer exchange reads: its host, peers and connection manager.
func newExchangeNode(t *testing.T, host core.Host, p2p config.P2P) *IdenaGossipHandler {
	t.Helper()
	h := &IdenaGossipHandler{host: host, cfg: p2p, peers: newPeerSet(), log: log.New(), connManager: NewConnManager(host, p2p)}
	h.exchange = newPeerExchange(h)
	host.SetStreamHandler(PeerExchangeProtocol, h.exchange.serve)
	return h
}

func addIdenaPeer(t *testing.T, h *IdenaGossipHandler, id peer.ID, inbound bool) {
	t.Helper()
	require.NoError(t, h.peers.Register(&protoPeer{id: id, inbound: inbound, log: log.New()}))
	h.connManager.Connected(id, inbound, 0)
}

func connectHosts(t *testing.T, from, to core.Host) {
	t.Helper()
	require.NoError(t, from.Connect(context.Background(), peer.AddrInfo{ID: to.ID(), Addrs: to.Addrs()}))
}

func mustAddr(t *testing.T, s string) multiaddr.Multiaddr {
	t.Helper()
	a, err := multiaddr.NewMultiaddr(s)
	require.NoError(t, err)
	return a
}

func TestPeerExchangeSharesListedIncomingPeersByRelayOnly(t *testing.T) {
	sharerHost, askerHost, listedHost := newExchangeHost(t), newExchangeHost(t), newExchangeHost(t)
	sharer := newExchangeNode(t, sharerHost, config.P2P{})
	asker := newExchangeNode(t, askerHost, config.P2P{})
	listed := newExchangeNode(t, listedHost, config.P2P{})
	connectHosts(t, askerHost, sharerHost)
	connectHosts(t, listedHost, sharerHost)
	// Both connected out to the sharer: they are its incoming peers, it is their outgoing one.
	addIdenaPeer(t, sharer, askerHost.ID(), true)
	addIdenaPeer(t, sharer, listedHost.ID(), true)
	addIdenaPeer(t, asker, sharerHost.ID(), false)
	addIdenaPeer(t, listed, sharerHost.ID(), false)

	relayAddr := "/ip4/1.2.3.4/tcp/4001/p2p/" + relayPeerId + "/p2p-circuit"
	// The sharer knows the node's own address too, as identify gives it: it must stay out of the answer.
	sharerHost.Peerstore().AddAddrs(listedHost.ID(), []multiaddr.Multiaddr{mustAddr(t, "/ip4/5.6.7.8/tcp/40405"), mustAddr(t, relayAddr)}, peerstore.PermanentAddrTTL)

	// The listed node asks to be listed; nobody else is listed yet.
	got, err := listed.exchange.ask(sharerHost.ID(), true)
	require.NoError(t, err)
	require.Empty(t, got)

	got, err = asker.exchange.ask(sharerHost.ID(), false)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, listedHost.ID(), got[0].ID)
	require.Equal(t, []multiaddr.Multiaddr{mustAddr(t, relayAddr)}, got[0].Addrs, "only the relay address, never the node's own")
}

func TestPeerExchangeListsOnlyThePeersThatAsked(t *testing.T) {
	sharerHost, askerHost, otherHost := newExchangeHost(t), newExchangeHost(t), newExchangeHost(t)
	sharer := newExchangeNode(t, sharerHost, config.P2P{})
	asker := newExchangeNode(t, askerHost, config.P2P{})
	other := newExchangeNode(t, otherHost, config.P2P{})
	connectHosts(t, askerHost, sharerHost)
	connectHosts(t, otherHost, sharerHost)
	addIdenaPeer(t, sharer, askerHost.ID(), true)
	addIdenaPeer(t, sharer, otherHost.ID(), true)
	sharerHost.Peerstore().AddAddrs(otherHost.ID(), []multiaddr.Multiaddr{mustAddr(t, "/ip4/1.2.3.4/tcp/4001/p2p/"+relayPeerId+"/p2p-circuit")}, peerstore.PermanentAddrTTL)

	// The other node asks without listMe: it stays out of the answers.
	_, err := other.exchange.ask(sharerHost.ID(), false)
	require.NoError(t, err)
	got, err := asker.exchange.ask(sharerHost.ID(), false)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestPeerExchangeAnswersOnlyIncomingAndDirectPeers(t *testing.T) {
	sharerHost, outHost, strangerHost := newExchangeHost(t), newExchangeHost(t), newExchangeHost(t)
	sharer := newExchangeNode(t, sharerHost, config.P2P{})
	out := newExchangeNode(t, outHost, config.P2P{})
	stranger := newExchangeNode(t, strangerHost, config.P2P{})
	connectHosts(t, outHost, sharerHost)
	connectHosts(t, strangerHost, sharerHost)
	// The sharer connected out to this one: it is not an incoming peer there.
	addIdenaPeer(t, sharer, outHost.ID(), false)

	_, err := out.exchange.ask(sharerHost.ID(), true)
	require.Error(t, err, "an outgoing peer of the sharer was answered")
	_, err = stranger.exchange.ask(sharerHost.ID(), true)
	require.Error(t, err, "a node that is no Idena peer was answered")

	sharer.exchange.mutex.Lock()
	require.Empty(t, sharer.exchange.listed, "a refused request listed its sender")
	sharer.exchange.mutex.Unlock()
}

func TestPeerExchangeAnswersADirectPeer(t *testing.T) {
	sharerHost, directHost := newExchangeHost(t), newExchangeHost(t)
	sharer := newExchangeNode(t, sharerHost, config.P2P{DirectPeers: []string{"/ip4/127.0.0.1/tcp/1/p2p/" + directHost.ID().String()}})
	direct := newExchangeNode(t, directHost, config.P2P{})
	connectHosts(t, directHost, sharerHost)
	// The sharer dialed its direct peer: outgoing, but direct peers are answered.
	addIdenaPeer(t, sharer, directHost.ID(), false)

	_, err := direct.exchange.ask(sharerHost.ID(), false)
	require.NoError(t, err)
}

func TestPeerExchangeRefusesASecondRequestWithinAMinute(t *testing.T) {
	sharerHost, askerHost := newExchangeHost(t), newExchangeHost(t)
	sharer := newExchangeNode(t, sharerHost, config.P2P{})
	asker := newExchangeNode(t, askerHost, config.P2P{})
	connectHosts(t, askerHost, sharerHost)
	addIdenaPeer(t, sharer, askerHost.ID(), true)

	_, err := asker.exchange.ask(sharerHost.ID(), false)
	require.NoError(t, err)
	_, err = asker.exchange.ask(sharerHost.ID(), false)
	require.Error(t, err)

	// A minute later it is answered again.
	sharer.exchange.mutex.Lock()
	sharer.exchange.lastRequests[askerHost.ID()] = time.Now().Add(-peerExchangeMinInterval - time.Second)
	sharer.exchange.mutex.Unlock()
	_, err = asker.exchange.ask(sharerHost.ID(), false)
	require.NoError(t, err)
}

func TestPeerExchangeForgetsAPeerThatLeft(t *testing.T) {
	sharerHost, askerHost, listedHost := newExchangeHost(t), newExchangeHost(t), newExchangeHost(t)
	sharer := newExchangeNode(t, sharerHost, config.P2P{})
	asker := newExchangeNode(t, askerHost, config.P2P{})
	listed := newExchangeNode(t, listedHost, config.P2P{})
	connectHosts(t, askerHost, sharerHost)
	connectHosts(t, listedHost, sharerHost)
	addIdenaPeer(t, sharer, askerHost.ID(), true)
	addIdenaPeer(t, sharer, listedHost.ID(), true)
	sharerHost.Peerstore().AddAddrs(listedHost.ID(), []multiaddr.Multiaddr{mustAddr(t, "/ip4/1.2.3.4/tcp/4001/p2p/"+relayPeerId+"/p2p-circuit")}, peerstore.PermanentAddrTTL)
	_, err := listed.exchange.ask(sharerHost.ID(), true)
	require.NoError(t, err)

	// It is no Idena peer of the sharer any more.
	require.NoError(t, sharer.peers.Unregister(listedHost.ID()))
	got, err := asker.exchange.ask(sharerHost.ID(), false)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestParsePeerExchangeEntriesKeepsRelayAddressesOnly(t *testing.T) {
	listed := newExchangeHost(t).ID()
	relay := "/ip4/1.2.3.4/tcp/4001/p2p/" + relayPeerId + "/p2p-circuit"
	got := parsePeerExchangeEntries([]peerExchangeEntry{
		{ID: "not a peer id", Addrs: []string{relay}},
		{ID: listed.String(), Addrs: []string{
			"/ip4/5.6.7.8/tcp/40405",          // the node's own address: never dialed
			"not an address",                  // dropped
			relay + "/p2p/" + listed.String(), // its own id at the end: kept without it
			relay + "/p2p/" + relayPeerId,     // another id at the end: dropped
		}},
	})
	require.Len(t, got, 1)
	require.Equal(t, listed, got[0].ID)
	require.Equal(t, []multiaddr.Multiaddr{mustAddr(t, relay)}, got[0].Addrs)

	require.Empty(t, parsePeerExchangeEntries([]peerExchangeEntry{{ID: listed.String(), Addrs: []string{"/ip4/5.6.7.8/tcp/40405"}}}),
		"an entry without a relay address was kept")
}

func TestParsePeerExchangeEntriesCapsTheAnswer(t *testing.T) {
	relay := "/ip4/1.2.3.4/tcp/4001/p2p/" + relayPeerId + "/p2p-circuit"
	var entries []peerExchangeEntry
	for i := 0; i < peerExchangeMaxEntries+3; i++ {
		addrs := make([]string, 0, peerExchangeMaxAddrs+2)
		for j := 0; j < peerExchangeMaxAddrs+2; j++ {
			addrs = append(addrs, relay)
		}
		entries = append(entries, peerExchangeEntry{ID: newExchangeHost(t).ID().String(), Addrs: addrs})
	}
	got := parsePeerExchangeEntries(entries)
	require.Len(t, got, peerExchangeMaxEntries)
	for _, info := range got {
		require.Len(t, info.Addrs, peerExchangeMaxAddrs)
	}
}

func TestSharedPeersGoFirstForHalfTheOutgoingSlots(t *testing.T) {
	m := NewConnManager(nil, config.P2P{MaxOutboundOwnShardPeers: 4, MaxOutboundPeers: 2})
	require.Equal(t, 3, m.exchangeOutCap())

	until := time.Now().Add(time.Minute)
	shared := []peer.ID{"a1", "a2", "a3", "a4"}
	for _, id := range shared {
		m.AddCandidate(id, until)
	}
	require.True(t, m.preferCandidate("a1"))
	require.False(t, m.preferCandidate("usual"), "a node nobody shared went first")

	for _, id := range shared[:3] {
		m.Connected(id, false, 0)
	}
	require.False(t, m.IsCandidate("a1"), "a candidate stays one once connected")
	require.False(t, m.preferCandidate("a4"), "shared peers took more than half of the outgoing slots")

	// One of them leaves: the next shared node goes first again.
	m.Disconnected("a2", nil)
	require.True(t, m.preferCandidate("a4"))
}

func TestSharedPeerConnectingInDoesNotCount(t *testing.T) {
	m := NewConnManager(nil, config.P2P{MaxOutboundOwnShardPeers: 4, MaxOutboundPeers: 2})
	m.AddCandidate("a1", time.Now().Add(time.Minute))
	m.Connected("a1", true, 0)
	m.peerMutex.RLock()
	defer m.peerMutex.RUnlock()
	require.Empty(t, m.exchangeOut)
	require.Empty(t, m.candidates)
}

func TestPruneCandidatesDropsTheExpiredOnes(t *testing.T) {
	m := NewConnManager(nil, config.P2P{})
	now := time.Now()
	m.AddCandidate("old", now.Add(-time.Second))
	m.AddCandidate("new", now.Add(time.Minute))
	var dropped []peer.ID
	m.PruneCandidates(now, func(id peer.ID) { dropped = append(dropped, id) })
	require.Equal(t, []peer.ID{"old"}, dropped)
	require.True(t, m.IsCandidate("new"))
	require.False(t, m.IsCandidate("old"))
}

func TestRelayAddrsNeverShareTheNodesOwnAddress(t *testing.T) {
	relay := "/ip4/1.2.3.4/tcp/4001/p2p/" + relayPeerId + "/p2p-circuit"
	got := relayAddrs([]multiaddr.Multiaddr{
		mustAddr(t, "/ip4/5.6.7.8/tcp/40405"),
		mustAddr(t, relay),
		mustAddr(t, "/ip6/2a01::1/tcp/40405"),
	})
	require.Equal(t, []string{relay}, got)
}

func TestIsRelayAddrAcceptsOnlyACircuitThroughAPublicRelay(t *testing.T) {
	relay := "/p2p/" + relayPeerId + "/p2p-circuit"
	for addr, want := range map[string]bool{
		"/ip4/1.2.3.4/tcp/4001" + relay:              true,
		"/ip6/2a01:4f9:c015:ebc::1/tcp/4001" + relay: true,
		"/ip4/192.168.1.1/tcp/80" + relay:            false, // the asking node's own network
		"/ip4/127.0.0.1/tcp/4001" + relay:            false,
		"/ip4/1.2.3.4/tcp/4001/p2p-circuit":          false, // no relay id
		"/p2p-circuit":                               false,
		"/ip4/1.2.3.4/tcp/4001" + relay + relay:      false, // nested circuit
		"/ip4/1.2.3.4/tcp/4001":                      false,
	} {
		require.Equal(t, want, isRelayAddr(mustAddr(t, addr)), addr)
	}
}

// newRoundNode is an exchange node that connected out to sharer, with outgoing slots as given.
func newRoundNode(t *testing.T, sharer *IdenaGossipHandler, maxOut int) *IdenaGossipHandler {
	t.Helper()
	h := newExchangeNode(t, newExchangeHost(t), config.P2P{MaxOutboundOwnShardPeers: maxOut})
	h.connManager.SetShardId(common.MultiShard)
	connectHosts(t, h.host, sharer.host)
	addIdenaPeer(t, sharer, h.host.ID(), true)
	addIdenaPeer(t, h, sharer.host.ID(), false)
	return h
}

func listTwoPeers(t *testing.T, sharer *IdenaGossipHandler) {
	t.Helper()
	for i := 0; i < 2; i++ {
		listed := newRoundNode(t, sharer, 4)
		sharer.host.Peerstore().AddAddrs(listed.host.ID(), []multiaddr.Multiaddr{mustAddr(t, "/ip4/1.2.3.4/tcp/4001/p2p/"+relayPeerId+"/p2p-circuit")}, peerstore.PermanentAddrTTL)
		_, err := listed.exchange.ask(sharer.host.ID(), true)
		require.NoError(t, err)
	}
}

func candidateCount(h *IdenaGossipHandler) int {
	h.connManager.peerMutex.RLock()
	defer h.connManager.peerMutex.RUnlock()
	return len(h.connManager.candidates)
}

func TestPeerExchangeRoundTakesOneNodePerAnsweringPeer(t *testing.T) {
	sharer := newExchangeNode(t, newExchangeHost(t), config.P2P{})
	listTwoPeers(t, sharer)
	asker := newRoundNode(t, sharer, 4)

	asker.exchange.round()
	require.Equal(t, 1, candidateCount(asker), "one answering peer chose more than one outgoing peer")
}

func TestPeerExchangeRoundDialsNothingWithoutAFreeOutgoingSlot(t *testing.T) {
	sharer := newExchangeNode(t, newExchangeHost(t), config.P2P{})
	listTwoPeers(t, sharer)
	// Its one outgoing slot holds the sharer.
	asker := newRoundNode(t, sharer, 1)

	asker.exchange.round()
	require.Zero(t, candidateCount(asker))
}

package protocol

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	core "github.com/libp2p/go-libp2p/core"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// Peer exchange: a node tells its incoming Idena peers and its direct peers which of its other incoming peers can be
// reached through a relay ("these nodes connect to me, here they are if you want to connect to them"). A node with no
// open port only ever connects out, so no other node behind a router ever meets it; once two such nodes have a relayed
// connection, hole punching turns it into a direct one and either can then take the other as a peer.
//
// Only the peers that asked to be listed are shared, and an entry carries a peer id and its relay addresses, never
// the peer's own IP address. A node opens this protocol only to the peers it connected out to (it is an incoming peer
// there) and to its direct peers; a node answers only its incoming and direct peers.
const PeerExchangeProtocol = core.ProtocolID("/idena/peer-exchange/1.0.0")

const (
	// peerExchangeMaxEntries caps an answer, peerExchangeMaxAddrs the relay addresses of one entry.
	peerExchangeMaxEntries = 5
	peerExchangeMaxAddrs   = 4
	// peerExchangeMaxMessage caps what either side reads.
	peerExchangeMaxMessage = 8 * 1024
	// peerExchangeMinInterval is the shortest time between two requests of one peer that are answered.
	peerExchangeMinInterval = time.Minute
	// peerExchangeAskedPeers is how many peers a node asks each round, peerExchangeMaxDials how many new nodes it dials.
	peerExchangeAskedPeers = 3
	peerExchangeMaxDials   = 3
	// peerExchangeCandidateTTL is how long a shared node is preferred when this node picks its next outgoing peer.
	peerExchangeCandidateTTL = 15 * time.Minute
	// peerExchangeListingTTL is how long a peer stays listed after it last asked: it asks again on its rounds while it
	// still has free incoming slots and no open port.
	peerExchangeListingTTL = 15 * time.Minute
	peerExchangeTimeout    = 30 * time.Second
	peerExchangeTag        = "idena-peer-exchange"
	peerExchangeTagWeight  = 20
)

type peerExchangeRequest struct {
	// ListMe asks the answering node to share this node with its other peers: it has free incoming slots and no
	// open port.
	ListMe bool `json:"listMe"`
}

type peerExchangeEntry struct {
	ID    string   `json:"id"`
	Addrs []string `json:"addrs"`
}

type peerExchangeResponse struct {
	Peers []peerExchangeEntry `json:"peers"`
}

type peerExchange struct {
	handler *IdenaGossipHandler

	mutex sync.Mutex
	// listed holds when each peer last asked to be listed: a listing lasts peerExchangeListingTTL.
	listed       map[peer.ID]time.Time
	lastRequests map[peer.ID]time.Time

	reachability sync.Mutex
	private      bool
	sub          event.Subscription
}

func newPeerExchange(handler *IdenaGossipHandler) *peerExchange {
	return &peerExchange{
		handler:      handler,
		listed:       make(map[peer.ID]time.Time),
		lastRequests: make(map[peer.ID]time.Time),
	}
}

// register serves the protocol on host and follows the host's reachability (AutoNAT): a node asks to be listed only
// while it is reachable through relays alone. It runs again on the host of an IPFS node started on a new port.
func (e *peerExchange) register(host core.Host) {
	host.SetStreamHandler(PeerExchangeProtocol, e.serve)
	e.reachability.Lock()
	if e.sub != nil {
		_ = e.sub.Close()
		e.sub = nil
	}
	e.private = false
	e.reachability.Unlock()
	sub, err := host.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged))
	if err != nil {
		e.handler.log.Warn("peer exchange cannot follow the reachability", "err", err)
		return
	}
	e.reachability.Lock()
	e.sub = sub
	e.reachability.Unlock()
	go func() {
		for ev := range sub.Out() {
			r := ev.(event.EvtLocalReachabilityChanged).Reachability
			e.reachability.Lock()
			current := e.sub == sub
			if current {
				e.private = r == network.ReachabilityPrivate
			}
			e.reachability.Unlock()
			if !current {
				return
			}
			e.handler.log.Info("Reachability changed", "reachability", r.String())
		}
	}()
}

func (e *peerExchange) isPrivate() bool {
	e.reachability.Lock()
	defer e.reachability.Unlock()
	return e.private
}

// forget drops what the exchange keeps about a peer that is no longer an Idena peer.
func (e *peerExchange) forget(id peer.ID) {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	delete(e.listed, id)
	// lastRequests stays: a peer that reconnects waits out the interval too. serve ages it out.
}

// mayAsk tells whether a peer may open the protocol here: an incoming Idena peer or a direct peer.
func (e *peerExchange) mayAsk(id peer.ID) bool {
	if e.handler.connManager.IsDirectPeer(id) {
		return e.handler.peers.Peer(id) != nil
	}
	p := e.handler.peers.Peer(id)
	return p != nil && p.Inbound()
}

func (e *peerExchange) serve(stream network.Stream) {
	defer stream.Close()
	from := stream.Conn().RemotePeer()
	if !e.mayAsk(from) {
		_ = stream.Reset()
		return
	}
	_ = stream.SetDeadline(time.Now().Add(peerExchangeTimeout))

	var req peerExchangeRequest
	if err := json.NewDecoder(io.LimitReader(stream, peerExchangeMaxMessage)).Decode(&req); err != nil {
		_ = stream.Reset()
		return
	}

	now := time.Now()
	e.mutex.Lock()
	for id, at := range e.lastRequests {
		if now.Sub(at) >= peerExchangeMinInterval {
			delete(e.lastRequests, id)
		}
	}
	if last, ok := e.lastRequests[from]; ok && now.Sub(last) < peerExchangeMinInterval {
		e.mutex.Unlock()
		_ = stream.Reset()
		return
	}
	if e.handler.peers.Peer(from) == nil {
		// It left while the request was read: forget ran already.
		e.mutex.Unlock()
		_ = stream.Reset()
		return
	}
	e.lastRequests[from] = now
	if req.ListMe {
		e.listed[from] = now
	} else {
		delete(e.listed, from)
	}
	e.mutex.Unlock()

	resp := peerExchangeResponse{Peers: e.entriesFor(from)}
	if err := json.NewEncoder(stream).Encode(resp); err != nil {
		_ = stream.Reset()
	}
}

// entriesFor picks at most peerExchangeMaxEntries listed peers, other than the asking one, that are still incoming or
// direct Idena peers of this node and have relay addresses.
func (e *peerExchange) entriesFor(asking peer.ID) []peerExchangeEntry {
	e.mutex.Lock()
	ids := make([]peer.ID, 0, len(e.listed))
	now := time.Now()
	for id, at := range e.listed {
		if now.Sub(at) > peerExchangeListingTTL {
			delete(e.listed, id)
			continue
		}
		if id != asking {
			ids = append(ids, id)
		}
	}
	e.mutex.Unlock()

	entries := make([]peerExchangeEntry, 0, peerExchangeMaxEntries)
	for len(ids) > 0 && len(entries) < peerExchangeMaxEntries {
		i := secureIntn(len(ids))
		id := ids[i]
		ids[i] = ids[len(ids)-1]
		ids = ids[:len(ids)-1]
		if !e.mayAsk(id) {
			continue
		}
		addrs := relayAddrs(e.handler.host.Peerstore().Addrs(id))
		if len(addrs) == 0 {
			continue
		}
		entries = append(entries, peerExchangeEntry{ID: id.String(), Addrs: addrs})
	}
	return entries
}

// relayAddrs keeps the relay (circuit) addresses, at most peerExchangeMaxAddrs: a node's own IP address is never
// shared.
func relayAddrs(addrs []multiaddr.Multiaddr) []string {
	var res []string
	for _, a := range addrs {
		if !isRelayAddr(a) {
			continue
		}
		res = append(res, a.String())
		if len(res) == peerExchangeMaxAddrs {
			break
		}
	}
	return res
}

// isRelayAddr accepts /ip4|ip6/<public IP>/tcp/<port>/p2p/<relay>/p2p-circuit only (Idena's IPFS runs TCP alone): no
// DNS name, one IP, one circuit, so an answer cannot have this node dial its own network.
func isRelayAddr(a multiaddr.Multiaddr) bool {
	if len(a) != 4 || a[1].Code() != multiaddr.P_TCP || a[2].Code() != multiaddr.P_P2P || a[3].Code() != multiaddr.P_CIRCUIT {
		return false
	}
	if code := a[0].Code(); code != multiaddr.P_IP4 && code != multiaddr.P_IP6 {
		return false
	}
	return manet.IsPublicAddr(a[:1])
}

// wantsListing tells whether this node asks to be shared: it has no open port and free incoming slots.
func (e *peerExchange) wantsListing() bool {
	return e.isPrivate() && e.handler.connManager.CanAcceptStream()
}

// askTargets picks the peers to ask this round among the peers this node connected out to (it is an incoming peer
// there) and its direct peers, whichever side connected: two direct peers both dial, and the one that connected in
// would otherwise never ask (it is answered as a direct peer where it is named).
func (e *peerExchange) askTargets() []peer.ID {
	var all []peer.ID
	for _, p := range e.handler.peers.Peers() {
		if !p.Inbound() || e.handler.connManager.IsDirectPeer(p.id) {
			all = append(all, p.id)
		}
	}
	var picked []peer.ID
	for len(all) > 0 && len(picked) < peerExchangeAskedPeers {
		i := secureIntn(len(all))
		picked = append(picked, all[i])
		all[i] = all[len(all)-1]
		all = all[:len(all)-1]
	}
	return picked
}

// round asks a few peers for their listed peers and dials the new ones through their relays.
func (e *peerExchange) round() {
	listMe := e.wantsListing()
	// A shared node is dialed only when an outgoing slot can take it.
	cm := e.handler.connManager
	canDial := cm.PrefersCandidates() && cm.CanDial() && cm.CanTakeCandidate()
	dials, answers, shared := 0, 0, 0
	targets := e.askTargets()
	for _, target := range targets {
		infos, err := e.ask(target, listMe)
		if err != nil {
			e.handler.log.Debug("peer exchange request failed", "peer", target.String(), "err", err)
			continue
		}
		answers++
		shared += len(infos)
		// At most one node per answering peer: no single peer chooses several of this node's outgoing peers.
		for _, info := range infos {
			if !canDial || dials >= peerExchangeMaxDials {
				break
			}
			if !e.worthDialing(info.ID) {
				continue
			}
			dials++
			e.handler.connManager.AddCandidate(info.ID, time.Now().Add(peerExchangeCandidateTTL))
			go e.dial(info)
			break
		}
	}
	e.handler.connManager.PruneCandidates(time.Now(), func(id peer.ID) {
		e.handler.host.ConnManager().UntagPeer(id, peerExchangeTag)
	})
	e.handler.log.Info("Peer exchange round", "listMe", listMe, "relayAddrs", len(relayAddrs(e.handler.host.Addrs())),
		"asked", len(targets), "answers", answers, "shared", shared, "dials", dials)
}

func (e *peerExchange) worthDialing(id peer.ID) bool {
	h := e.handler
	if id == h.host.ID() || h.peers.Peer(id) != nil || h.connManager.IsCandidate(id) || h.connManager.IsBanned(id) ||
		h.connManager.waitsAfterDisconnect(id) {
		return false
	}
	return h.host.Network().Connectedness(id) != network.Connected
}

func (e *peerExchange) dial(info peer.AddrInfo) {
	host := e.handler.host
	host.ConnManager().TagPeer(info.ID, peerExchangeTag, peerExchangeTagWeight)
	host.Peerstore().AddAddrs(info.ID, info.Addrs, peerstore.TempAddrTTL)
	ctx, cancel := context.WithTimeout(context.Background(), peerExchangeTimeout)
	defer cancel()
	// The relayed connection is a limited one: hole punching (DCUtR) then opens the direct connection the Idena
	// stream needs.
	if err := host.Connect(network.WithAllowLimitedConn(ctx, "peer exchange"), info); err != nil {
		e.handler.log.Debug("cannot reach a shared peer", "peer", info.ID.String(), "err", err)
	}
}

// ask sends one request to target and reads the shared peers it answers with.
func (e *peerExchange) ask(target peer.ID, listMe bool) ([]peer.AddrInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), peerExchangeTimeout)
	defer cancel()
	stream, err := e.handler.host.NewStream(ctx, target, PeerExchangeProtocol)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(peerExchangeTimeout))
	if err := json.NewEncoder(stream).Encode(peerExchangeRequest{ListMe: listMe}); err != nil {
		_ = stream.Reset()
		return nil, err
	}
	if err := stream.CloseWrite(); err != nil {
		_ = stream.Reset()
		return nil, err
	}
	var resp peerExchangeResponse
	if err := json.NewDecoder(io.LimitReader(stream, peerExchangeMaxMessage)).Decode(&resp); err != nil {
		_ = stream.Reset()
		return nil, err
	}
	return parsePeerExchangeEntries(resp.Peers), nil
}

// parsePeerExchangeEntries keeps the well-formed entries of an answer, at most peerExchangeMaxEntries, each with at
// most peerExchangeMaxAddrs relay addresses; an entry without one is dropped.
func parsePeerExchangeEntries(entries []peerExchangeEntry) []peer.AddrInfo {
	var res []peer.AddrInfo
	for _, entry := range entries {
		if len(res) == peerExchangeMaxEntries {
			break
		}
		id, err := peer.Decode(entry.ID)
		if err != nil {
			continue
		}
		info := peer.AddrInfo{ID: id}
		for _, s := range entry.Addrs {
			if len(info.Addrs) == peerExchangeMaxAddrs {
				break
			}
			a, err := multiaddr.NewMultiaddr(s)
			if err != nil {
				continue
			}
			// A relay address may end with the peer's own id; any other id there names another peer.
			transport, last := peer.SplitAddr(a)
			if last != "" && last != id || transport == nil || !isRelayAddr(transport) {
				continue
			}
			info.Addrs = append(info.Addrs, transport)
		}
		if len(info.Addrs) > 0 {
			res = append(res, info)
		}
	}
	return res
}

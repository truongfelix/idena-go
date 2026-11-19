package protocol

import (
	"context"
	"github.com/deckarep/golang-set"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/config"
	core "github.com/libp2p/go-libp2p/core"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-yamux"
	"github.com/pkg/errors"
	"strings"
	"sync"
	"time"
)

const (
	dialPeerAttempts = 10
)

var FailedToDialPeer = errors.New("failed to select peer")
var NoPeersToDial = errors.New("no peers to dial")

type ConnManager struct {
	bannedPeers       mapset.Set
	activeConnections map[peer.ID]network.Conn
	discTimes         map[peer.ID]time.Time
	resetTimes        map[peer.ID]time.Time

	inboundPeers  map[peer.ID]common.ShardId
	outboundPeers map[peer.ID]common.ShardId

	// directPeers are the nodes named in P2P.DirectPeers. They stay out of inboundPeers and outboundPeers, which
	// every slot count and every disconnect choice reads: never refused for want of a slot, never picked to make room.
	directPeers map[peer.ID]struct{}

	// candidates are the nodes peer exchange shared (until the time given): the next outgoing peer is taken among them
	// first. exchangeOut are the outgoing peers taken that way, at most exchangeOutCap so that the others still come
	// from the usual pick.
	candidates  map[peer.ID]time.Time
	exchangeOut map[peer.ID]struct{}

	peerMutex sync.RWMutex
	connMutex sync.Mutex
	host      core.Host
	cfg       config.P2P

	ownShardId common.ShardId
}

func NewConnManager(host core.Host, cfg config.P2P) *ConnManager {
	directPeers := make(map[peer.ID]struct{})
	// The config was validated when it was made: an invalid list cannot reach this point.
	infos, _ := cfg.DirectPeerInfos()
	for _, info := range infos {
		directPeers[info.ID] = struct{}{}
	}
	return &ConnManager{
		host:              host,
		cfg:               cfg,
		bannedPeers:       mapset.NewSet(),
		activeConnections: make(map[peer.ID]network.Conn),
		inboundPeers:      make(map[peer.ID]common.ShardId),
		outboundPeers:     make(map[peer.ID]common.ShardId),
		directPeers:       directPeers,
		candidates:        make(map[peer.ID]time.Time),
		exchangeOut:       make(map[peer.ID]struct{}),
		discTimes:         make(map[peer.ID]time.Time),
		resetTimes:        make(map[peer.ID]time.Time),
	}
}

// IsDirectPeer tells whether id is named in P2P.DirectPeers.
func (m *ConnManager) IsDirectPeer(id peer.ID) bool {
	_, ok := m.directPeers[id]
	return ok
}

// DirectPeers lists the nodes named in P2P.DirectPeers.
func (m *ConnManager) DirectPeers() []peer.ID {
	ids := make([]peer.ID, 0, len(m.directPeers))
	for id := range m.directPeers {
		ids = append(ids, id)
	}
	return ids
}

func (m *ConnManager) CanConnect(id peer.ID) bool {

	if m.bannedPeers.Contains(id) {
		return false
	}
	if m.waitsAfterDisconnect(id) {
		return false
	}
	if m.host.Network().Connectedness(id) != network.Connected {
		return false
	}
	protos, err := m.host.Peerstore().GetProtocols(id)
	if err != nil {
		return false
	}
	for _, p := range protos {
		if strings.Contains(string(p), IdenaProtocolPath) {
			return true
		}
	}
	return false
}

// waitsAfterDisconnect tells whether id left or reset its stream too recently to be connected again.
func (m *ConnManager) waitsAfterDisconnect(id peer.ID) bool {
	m.peerMutex.RLock()
	defer m.peerMutex.RUnlock()
	if discTime, ok := m.discTimes[id]; ok && time.Now().UTC().Sub(discTime) < ReconnectAfterDiscTimeout {
		return true
	}
	if resetTime, ok := m.resetTimes[id]; ok && time.Now().UTC().Sub(resetTime) < ReconnectAfterResetTimeout {
		return true
	}
	return false
}

func (m *ConnManager) Connected(id peer.ID, inbound bool, shardId common.ShardId) {
	if m.IsDirectPeer(id) {
		return
	}
	m.peerMutex.Lock()
	defer m.peerMutex.Unlock()
	if inbound {
		m.inboundPeers[id] = shardId
	} else {
		m.outboundPeers[id] = shardId
		if _, ok := m.candidates[id]; ok && len(m.exchangeOut) < m.exchangeOutCap() {
			m.exchangeOut[id] = struct{}{}
		}
	}
	delete(m.candidates, id)
}

func (m *ConnManager) UpdatePeerShardId(id peer.ID, shardId common.ShardId) {
	m.peerMutex.Lock()
	defer m.peerMutex.Unlock()
	if _, ok := m.inboundPeers[id]; ok {
		m.inboundPeers[id] = shardId
		return
	}
	if _, ok := m.outboundPeers[id]; ok {
		m.outboundPeers[id] = shardId
		return
	}
}

func (m *ConnManager) SetShardId(shard common.ShardId) (updated bool) {
	prevShardId := m.ownShardId
	m.ownShardId = shard
	return prevShardId != shard
}

func (m *ConnManager) Disconnected(id peer.ID, reason error) {
	m.peerMutex.Lock()
	defer m.peerMutex.Unlock()

	m.discTimes[id] = time.Now().UTC()
	if reason == yamux.ErrStreamReset {
		m.resetTimes[id] = time.Now().UTC()
	}
	delete(m.inboundPeers, id)
	delete(m.outboundPeers, id)
	delete(m.exchangeOut, id)
}

// exchangeOutCap is how many outgoing peers may come from peer exchange: half of them, at least one.
func (m *ConnManager) exchangeOutCap() int {
	if c := (m.MaxOutboundPeers() + m.MaxOutboundOwnPeers()) / 2; c > 0 {
		return c
	}
	return 1
}

// AddCandidate keeps a node peer exchange shared until the given time.
func (m *ConnManager) AddCandidate(id peer.ID, until time.Time) {
	m.peerMutex.Lock()
	defer m.peerMutex.Unlock()
	m.candidates[id] = until
}

// DropCandidate forgets a node peer exchange shared.
func (m *ConnManager) DropCandidate(id peer.ID) {
	m.peerMutex.Lock()
	defer m.peerMutex.Unlock()
	delete(m.candidates, id)
}

// PrefersCandidates tells whether shared nodes go first: only without shards, where any peer fills the slot.
func (m *ConnManager) PrefersCandidates() bool {
	return m.ownShardId == common.MultiShard
}

func (m *ConnManager) IsCandidate(id peer.ID) bool {
	m.peerMutex.RLock()
	defer m.peerMutex.RUnlock()
	_, ok := m.candidates[id]
	return ok
}

// PruneCandidates forgets the candidates kept until before now, calling dropped for each.
func (m *ConnManager) PruneCandidates(now time.Time, dropped func(peer.ID)) {
	m.peerMutex.Lock()
	var gone []peer.ID
	for id, until := range m.candidates {
		if now.After(until) {
			delete(m.candidates, id)
			gone = append(gone, id)
		}
	}
	m.peerMutex.Unlock()
	for _, id := range gone {
		dropped(id)
	}
}

// preferCandidate tells whether the next outgoing peer is taken among the candidates first.
func (m *ConnManager) preferCandidate(id peer.ID) bool {
	m.peerMutex.RLock()
	defer m.peerMutex.RUnlock()
	_, ok := m.candidates[id]
	return ok && len(m.exchangeOut) < m.exchangeOutCap()
}

func (m *ConnManager) IsBanned(id peer.ID) bool {
	return m.bannedPeers.Contains(id)
}

func (m *ConnManager) BanPeer(id peer.ID) {
	m.bannedPeers.Add(id)
	if m.bannedPeers.Cardinality() > MaxBannedPeers {
		m.bannedPeers.Pop()
	}
}

func (m *ConnManager) DialRandomPeer() (network.Stream, error) {
	m.connMutex.Lock()
	conns := make([]network.Conn, 0, len(m.activeConnections))
	for _, c := range m.activeConnections {
		conns = append(conns, c)
	}
	m.connMutex.Unlock()

	filteredConns := make([]network.Conn, 0, len(conns))

	for _, c := range conns {
		id := c.RemotePeer()
		m.peerMutex.RLock()
		_, inbound := m.inboundPeers[id]
		_, outbound := m.outboundPeers[id]
		m.peerMutex.RUnlock()
		if !inbound && !outbound && m.CanConnect(id) {
			filteredConns = append(filteredConns, c)
		}
	}

	if len(filteredConns) == 0 {
		return nil, NoPeersToDial
	}

	// The nodes peer exchange shared go first: they have free incoming slots that no other pick fills. Only without
	// shards, where any peer fills the slot: a shard need takes the usual pick. Both share the attempts.
	attempt := 0
	if m.PrefersCandidates() {
		var preferred []network.Conn
		for _, c := range filteredConns {
			if m.preferCandidate(c.RemotePeer()) {
				preferred = append(preferred, c)
			}
		}
		failed := make(map[peer.ID]struct{})
		for len(preferred) > 0 && attempt < dialPeerAttempts {
			attempt++
			idx := secureIntn(len(preferred))
			if stream, err := m.findOrOpenStream(preferred[idx]); err == nil {
				return stream, nil
			}
			failed[preferred[idx].RemotePeer()] = struct{}{}
			preferred[idx] = preferred[len(preferred)-1]
			preferred = preferred[:len(preferred)-1]
		}
		if len(failed) > 0 {
			left := filteredConns[:0]
			for _, c := range filteredConns {
				if _, ok := failed[c.RemotePeer()]; !ok {
					left = append(left, c)
				}
			}
			filteredConns = left
			if len(filteredConns) == 0 {
				return nil, FailedToDialPeer
			}
		}
	}

	for ; attempt < dialPeerAttempts; attempt++ {
		idx := secureIntn(len(filteredConns))
		conn := filteredConns[idx]

		if stream, err := m.findOrOpenStream(conn); err == nil {
			return stream, nil
		}
		if len(filteredConns) == 1 {
			break
		}
		filteredConns[idx] = filteredConns[len(filteredConns)-1]
		filteredConns[len(filteredConns)-1] = nil
		filteredConns = filteredConns[:len(filteredConns)-1]
	}
	return nil, FailedToDialPeer
}

func (m *ConnManager) findOrOpenStream(conn network.Conn) (network.Stream, error) {
	// Newer libp2p hosts can wrap a stream to complete protocol negotiation
	// lazily. Conn.GetStreams exposes the underlying stream, so reusing it can
	// bypass that wrapper and feed multistream bytes to the Idena frame reader.
	return m.newStream(conn.RemotePeer())
}

func (m *ConnManager) newStream(peerID peer.ID) (network.Stream, error) {

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()

	protos, err := m.host.Peerstore().GetProtocols(peerID)
	if err != nil {
		return nil, err
	}

	var idenaProtocol protocol.ID
	for _, p := range protos {
		if strings.Contains(string(p), IdenaProtocolPath) {
			idenaProtocol = protocol.ID(p)
			break
		}
	}
	if idenaProtocol == "" {
		return nil, errors.New("peer doesn't support idena protocol")
	}
	stream, err := m.host.NewStream(ctx, peerID, idenaProtocol)

	select {
	case <-ctx.Done():
		err = errors.New("timeout while opening idena stream")
	default:
		break
	}

	return stream, err
}

func (m *ConnManager) AddConnection(conn network.Conn) {

	go func() {
		id := conn.RemotePeer()
		if m.bannedPeers.Contains(id) {
			return
		}
		time.Sleep(time.Second * 5)

		m.storeConnection(conn)
	}()
}

// storeConnection makes conn its peer's dial candidate, unless it closed while AddConnection waited: its
// Disconnected notification has then come already and nothing would remove it.
func (m *ConnManager) storeConnection(conn network.Conn) {
	m.connMutex.Lock()
	defer m.connMutex.Unlock()
	if conn.IsClosed() {
		return
	}
	m.activeConnections[conn.RemotePeer()] = conn
}

// RemoveConnection forgets a closed connection. A peer can have several at once (a relayed one and the direct one
// hole punching opened, or two simultaneous dials): it stays a dial candidate while another one is open.
func (m *ConnManager) RemoveConnection(conn network.Conn) {
	id := conn.RemotePeer()
	m.connMutex.Lock()
	defer m.connMutex.Unlock()
	if stored, ok := m.activeConnections[id]; !ok || stored != conn {
		return
	}
	for _, c := range m.host.Network().ConnsToPeer(id) {
		if c != conn && !c.IsClosed() {
			m.activeConnections[id] = c
			return
		}
	}
	delete(m.activeConnections, id)
}

func (m *ConnManager) CanAcceptStream() bool {
	m.peerMutex.RLock()
	defer m.peerMutex.RUnlock()
	return len(m.inboundPeers) < m.cfg.MaxInboundPeers+m.cfg.MaxInboundOwnShardPeers
}

func (m *ConnManager) NeedPeerFromSomeShard(shardsNum int) bool {
	if m.ownShardId != common.MultiShard || shardsNum <= 1 {
		return false
	}
	for i := common.ShardId(0); i <= common.ShardId(shardsNum); i++ {
		if m.PeersFromShard(i) < m.minimalNumberOfPeersFromShard() {
			return true
		}
	}
	return false
}

func (m *ConnManager) NeedInboundOwnShardPeers() bool {
	if m.ownShardId == common.MultiShard {
		return false
	}
	m.peerMutex.Lock()
	defer m.peerMutex.Unlock()
	var cnt int
	for _, s := range m.inboundPeers {
		if s == m.ownShardId {
			cnt++
		}
	}
	return cnt < m.cfg.MaxInboundOwnShardPeers
}

func (m *ConnManager) NeedOutboundOwnShardPeers() bool {
	if m.ownShardId == common.MultiShard {
		return false
	}
	m.peerMutex.Lock()
	defer m.peerMutex.Unlock()
	var cnt int
	for _, s := range m.outboundPeers {
		if s == m.ownShardId {
			cnt++
		}
	}
	return cnt < m.MaxOutboundOwnPeers()
}

func (m *ConnManager) MaxOutboundPeers() int {
	return m.cfg.MaxOutboundPeers
}

func (m *ConnManager) MaxOutboundOwnPeers() int {
	return m.cfg.MaxOutboundOwnShardPeers
}

func (m *ConnManager) CanDial() bool {
	m.peerMutex.RLock()
	defer m.peerMutex.RUnlock()
	return len(m.outboundPeers) < m.MaxOutboundPeers()+m.MaxOutboundOwnPeers()
}

func (m *ConnManager) GetRandomPeer(inbound bool) peer.ID {
	m.peerMutex.RLock()
	defer m.peerMutex.RUnlock()

	var peersMap map[peer.ID]common.ShardId
	if inbound {
		peersMap = m.inboundPeers
	} else {
		peersMap = m.outboundPeers
	}

	candidates := make([]peer.ID, 0)
	for k, s := range peersMap {
		if s == common.MultiShard {
			candidates = append(candidates, k)
			continue
		}
		if s != m.ownShardId && m.ownShardId != common.MultiShard {
			candidates = append(candidates, k)
			continue
		}
		if m.peersCntFromShard(s) > m.minimalNumberOfPeersFromShard() {
			candidates = append(candidates, k)
		}
	}
	if len(candidates) > 0 {
		return candidates[secureIntn(len(candidates))]
	}
	return ""
}

func (m *ConnManager) PeerForDisconnect(inbound bool, newPeerShardId common.ShardId) peer.ID {
	m.peerMutex.RLock()
	defer m.peerMutex.RUnlock()

	canDisconnect := func(oldPeerShardId common.ShardId) bool {
		if oldPeerShardId == newPeerShardId {
			return false
		}
		if m.ownShardId == common.MultiShard {
			return m.peersCntFromShard(oldPeerShardId) > m.minimalNumberOfPeersFromShard()
		}
		return oldPeerShardId != newPeerShardId
	}

	if inbound {
		for k, s := range m.inboundPeers {
			if canDisconnect(s) {
				return k
			}
		}
	} else {
		for k, s := range m.outboundPeers {
			if canDisconnect(s) {
				return k
			}
		}
	}
	return ""
}

func (m *ConnManager) IsFromOwnShards(id common.ShardId) bool {
	if m.ownShardId == common.MultiShard {
		return false
	}
	return m.ownShardId == id
}

func (m *ConnManager) NeedPeerFromShard(inbound bool, shardId common.ShardId) (canConnect bool, shouldDisconnectAnotherPeer bool) {

	if m.ownShardId == common.MultiShard {
		if m.PeersFromShard(shardId) < m.minimalNumberOfPeersFromShard() {
			if inbound {
				shouldDisconnectAnotherPeer = !m.CanAcceptStream()
			} else {
				shouldDisconnectAnotherPeer = !m.CanDial()
			}
			return true, shouldDisconnectAnotherPeer
		}
	}

	if inbound {
		if m.IsFromOwnShards(shardId) {
			canConnect = m.NeedInboundOwnShardPeers()
			shouldDisconnectAnotherPeer = canConnect && !m.CanAcceptStream()
		} else {
			canConnect = m.CanAcceptStream()
		}
	} else {
		if m.IsFromOwnShards(shardId) {
			canConnect = m.NeedOutboundOwnShardPeers()
			shouldDisconnectAnotherPeer = canConnect && !m.CanDial()
		} else {
			canConnect = m.CanDial()
		}
	}
	return canConnect, shouldDisconnectAnotherPeer
}

func (m *ConnManager) minimalNumberOfPeersFromShard() int {
	if m.cfg.Shared {
		return 2
	}
	return 1
}

func (m *ConnManager) PeersFromShard(shardId common.ShardId) int {
	m.peerMutex.RLock()
	defer m.peerMutex.RUnlock()
	return m.peersCntFromShard(shardId)
}

func (m *ConnManager) peersCntFromShard(shardId common.ShardId) int {
	var cnt int
	for _, s := range m.outboundPeers {
		if s == shardId {
			cnt++
		}
	}
	for _, s := range m.inboundPeers {
		if s == shardId {
			cnt++
		}
	}
	return cnt
}

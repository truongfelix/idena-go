package protocol

import (
	"testing"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/config"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

const directPeerId = "QmNqkSwad5HTShxVzFcYLQkRCRjrs9ZhQykqrRTQcdR7xp"

func newDirectPeerTestManager(t *testing.T, p2p config.P2P) (*ConnManager, peer.ID) {
	t.Helper()
	p2p.DirectPeers = []string{"/ip4/1.2.3.4/tcp/40405/p2p/" + directPeerId}
	id, err := peer.Decode(directPeerId)
	require.NoError(t, err)
	return NewConnManager(nil, p2p), id
}

func TestDirectPeersComeFromTheConfig(t *testing.T) {
	m, direct := newDirectPeerTestManager(t, config.P2P{})

	require.True(t, m.IsDirectPeer(direct))
	require.False(t, m.IsDirectPeer(peer.ID("other")))
	require.Equal(t, []peer.ID{direct}, m.DirectPeers())
}

func TestDirectPeerTakesNoSlot(t *testing.T) {
	m, direct := newDirectPeerTestManager(t, config.P2P{MaxInboundPeers: 1, MaxOutboundPeers: 1})
	m.SetShardId(common.MultiShard)

	m.Connected(direct, true, 1)
	m.Connected(peer.ID("out-direct"), false, 1)
	require.True(t, m.CanAcceptStream(), "the direct peer took the only incoming slot")

	m.Connected(peer.ID("in"), true, 1)
	require.False(t, m.CanAcceptStream(), "a regular peer takes the incoming slot")
}

func TestDirectPeerIsNeverPickedForADisconnect(t *testing.T) {
	m, direct := newDirectPeerTestManager(t, config.P2P{MaxInboundPeers: 1})
	m.SetShardId(common.ShardId(1))

	m.Connected(direct, true, common.ShardId(2))
	require.Equal(t, peer.ID(""), m.GetRandomPeer(true))
	require.Equal(t, peer.ID(""), m.PeerForDisconnect(true, common.ShardId(1)))

	// Control: a regular peer in the same place is picked.
	regular := peer.ID("regular")
	m.Connected(regular, true, common.ShardId(2))
	require.Equal(t, regular, m.GetRandomPeer(true))
	require.Equal(t, regular, m.PeerForDisconnect(true, common.ShardId(1)))
}

func TestDirectPeerDisconnectStartsTheRedialWait(t *testing.T) {
	m, direct := newDirectPeerTestManager(t, config.P2P{})

	m.Connected(direct, false, 1)
	m.Disconnected(direct, nil)

	_, waiting := m.discTimes[direct]
	require.True(t, waiting)
}

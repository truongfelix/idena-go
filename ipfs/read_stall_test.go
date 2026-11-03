package ipfs

import (
	"context"
	"testing"
	"time"

	"github.com/idena-network/idena-go/log"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

func TestReadStall_RedialsAfterTimeoutsWithPeersAndNoMessages(t *testing.T) {
	var s readStall
	start := time.Unix(1_700_000_000, 0)
	require.False(t, s.timeout(start, 5, 10), "one timeout is no stall")
	require.True(t, s.timeout(start.Add(readStallWindow), 5, 10), "a second timeout 30 s later, nothing received")
}

func TestReadStall_NoRedialWhenBitswapHeardFromAPeer(t *testing.T) {
	var s readStall
	start := time.Unix(1_700_000_000, 0)
	require.False(t, s.timeout(start, 5, 10))
	// A message arrived between the two timeouts: the data is missing, bitswap is not stuck.
	require.False(t, s.timeout(start.Add(readStallWindow), 5, 11))
	// The streak starts again from that second timeout.
	require.False(t, s.timeout(start.Add(readStallWindow+time.Second), 5, 11))
	require.True(t, s.timeout(start.Add(2*readStallWindow), 5, 11))
}

func TestReadStall_NoRedialWithoutPeers(t *testing.T) {
	var s readStall
	start := time.Unix(1_700_000_000, 0)
	require.False(t, s.timeout(start, 0, 10))
	require.False(t, s.timeout(start.Add(time.Minute), 0, 10))
}

func TestReadStall_TimeoutsTogetherAreNoStall(t *testing.T) {
	var s readStall
	start := time.Unix(1_700_000_000, 0)
	// Reads started together time out together.
	for i := 0; i < 10; i++ {
		require.False(t, s.timeout(start.Add(time.Duration(i)*time.Second), 5, 10))
	}
}

func TestReadStall_SuccessEndsTheStreak(t *testing.T) {
	var s readStall
	start := time.Unix(1_700_000_000, 0)
	require.False(t, s.timeout(start, 5, 10))
	s.success()
	require.False(t, s.timeout(start.Add(readStallWindow), 5, 10))
}

func TestReadStall_RedialsAtMostEveryInterval(t *testing.T) {
	var s readStall
	start := time.Unix(1_700_000_000, 0)
	require.False(t, s.timeout(start, 5, 10))
	require.True(t, s.timeout(start.Add(readStallWindow), 5, 10))
	at := start.Add(readStallWindow)
	require.False(t, s.timeout(at.Add(time.Minute), 5, 10))
	require.False(t, s.timeout(at.Add(2*time.Minute), 5, 10), "still within the interval")
	require.True(t, s.timeout(at.Add(readStallRedialInterval), 5, 10))
}

func newTestHost(t *testing.T) host.Host {
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func TestRedialPeers_ReplacesEveryConnection(t *testing.T) {
	h1, h2, h3 := newTestHost(t), newTestHost(t), newTestHost(t)
	ctx := context.Background()
	oldConns := map[peer.ID]string{}
	for _, other := range []host.Host{h2, h3} {
		require.NoError(t, h1.Connect(ctx, peer.AddrInfo{ID: other.ID(), Addrs: other.Addrs()}))
		conns := h1.Network().ConnsToPeer(other.ID())
		require.Len(t, conns, 1)
		oldConns[other.ID()] = conns[0].ID()
	}

	redialPeers(h1, log.New())

	require.Eventually(t, func() bool {
		for id, old := range oldConns {
			conns := h1.Network().ConnsToPeer(id)
			if len(conns) != 1 || conns[0].ID() == old {
				return false
			}
		}
		return true
	}, 10*time.Second, 50*time.Millisecond, "each peer is connected again over a new connection")
}

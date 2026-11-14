//go:build !idena_memory_ipfs

package ipfs

import (
	"context"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/log"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestParsePublicAddrs(t *testing.T) {
	for _, ok := range [][]string{
		nil,
		{"/ip4/82.123.115.203/tcp/50506"},
		{"/ip6/2a01:cb04:273:1c00:ae5b:43de:6e94:3139/tcp/50506", "/ip4/1.2.3.4/tcp/40405"},
	} {
		got, err := parsePublicAddrs(ok)
		require.NoError(t, err, ok)
		require.Len(t, got, len(ok))
	}
	for name, bad := range map[string][]string{
		"not a multiaddr":       {"82.123.115.203:50506"},
		"home network":          {"/ip4/192.168.1.102/tcp/50506"},
		"loopback":              {"/ip4/127.0.0.1/tcp/50506"},
		"carrier-grade NAT":     {"/ip4/100.64.1.2/tcp/50506"},
		"udp":                   {"/ip4/1.2.3.4/udp/50506"},
		"no port":               {"/ip4/1.2.3.4"},
		"port 0":                {"/ip4/1.2.3.4/tcp/0"},
		"peer id after port":    {"/ip4/1.2.3.4/tcp/50506/p2p/QmYQEa2YYEcFRAnxuaVS5jBYKPQQFricdTHjtDMr3Ma2M3"},
		"dns":                   {"/dns4/example.com/tcp/50506"},
		"one bad refuses all":   {"/ip4/1.2.3.4/tcp/1", "/ip4/10.0.0.1/tcp/2"},
		"more than the maximum": {"/ip4/1.1.1.1/tcp/1", "/ip4/1.1.1.2/tcp/1", "/ip4/1.1.1.3/tcp/1", "/ip4/1.1.1.4/tcp/1", "/ip4/1.1.1.5/tcp/1"},
	} {
		_, err := parsePublicAddrs(bad)
		require.Error(t, err, name)
	}
}

func TestPublicAddrsRefusedListKeepsThePrevious(t *testing.T) {
	p := &publicAddrs{}
	require.NoError(t, p.set([]string{"/ip4/1.2.3.4/tcp/50506"}))
	require.Error(t, p.set([]string{"/ip4/192.168.1.2/tcp/50506"}))
	require.Equal(t, []string{"/ip4/1.2.3.4/tcp/50506"}, p.get())
	require.NoError(t, p.set(nil))
	require.Empty(t, p.get())
}

func hasAddr(addrs []ma.Multiaddr, want string) bool {
	return slices.ContainsFunc(addrs, func(a ma.Multiaddr) bool { return a.String() == want })
}

// newAnnouncingHost builds a host through the host option, with an address factory of its own as Kubo passes one
// (Addresses.Announce/AppendAnnounce): both must apply.
func newAnnouncingHost(t *testing.T, public *publicAddrs, kuboAddr string) host.Host {
	priv, _, err := crypto.GenerateEd25519Key(nil)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)
	ps, err := pstoremem.NewPeerstore()
	require.NoError(t, err)
	require.NoError(t, ps.AddPrivKey(id, priv))
	require.NoError(t, ps.AddPubKey(id, priv.GetPublic()))
	h, err := public.hostOption()(id, ps,
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.AddrsFactory(func(addrs []ma.Multiaddr) []ma.Multiaddr {
			return append(addrs, ma.StringCast(kuboAddr))
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// A peer learns an address set while the node runs, without a restart, and its removal; Kubo's own factory still
// applies.
func TestPublicAddrsAnnouncedToPeersAtRuntime(t *testing.T) {
	const kuboAddr = "/ip4/9.9.9.9/tcp/1"
	const routerAddr = "/ip4/82.123.115.203/tcp/50506"
	public := &publicAddrs{}
	node := newAnnouncingHost(t, public, kuboAddr)
	other, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close() })

	require.True(t, hasAddr(node.Addrs(), kuboAddr), "Kubo's factory applies: %v", node.Addrs())
	require.False(t, hasAddr(node.Addrs(), routerAddr))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, other.Connect(ctx, peer.AddrInfo{ID: node.ID(), Addrs: node.Addrs()}))
	require.Eventually(t, func() bool { return hasAddr(other.Peerstore().Addrs(node.ID()), kuboAddr) },
		10*time.Second, 100*time.Millisecond, "identify ran")
	require.False(t, hasAddr(other.Peerstore().Addrs(node.ID()), routerAddr))

	require.NoError(t, public.set([]string{routerAddr}))
	require.Eventually(t, func() bool { return hasAddr(node.Addrs(), routerAddr) }, 15*time.Second, 100*time.Millisecond,
		"the node announces the address: %v", node.Addrs())
	require.True(t, hasAddr(node.Addrs(), kuboAddr), "Kubo's factory still applies")
	require.Eventually(t, func() bool { return hasAddr(other.Peerstore().Addrs(node.ID()), routerAddr) },
		15*time.Second, 100*time.Millisecond, "the connected peer learns it (identify push)")

	require.NoError(t, public.set(nil))
	require.Eventually(t, func() bool { return !hasAddr(node.Addrs(), routerAddr) }, 15*time.Second,
		100*time.Millisecond, "cleared: %v", node.Addrs())
}

// The address from the settings is announced from the start by a node built as the proxy builds it; a bad list
// does not stop the node and announces nothing.
func TestNodeAnnouncesPublicAddrsFromConfig(t *testing.T) {
	const routerAddr = "/ip4/82.123.115.203/tcp/4013"
	// Free ports from the system: the shared test node moves to 4013, 4014... after 2 minutes without peers.
	newNode := func(addrs []string) (*publicAddrs, host.Host) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		port := ln.Addr().(*net.TCPAddr).Port
		require.NoError(t, ln.Close())
		cfg := &config.IpfsConfig{
			SwarmKey:    "9ad6f96bb2b02a7308ad87938d6139a974b550cc029ce416641a60c46db2f530",
			BootNodes:   []string{},
			IpfsPort:    port,
			DataDir:     t.TempDir(),
			GracePeriod: "20s",
			PublicAddrs: addrs,
		}
		public := publicAddrsFromConfig(cfg, log.New())
		node, _, cancel, err := createNode(cfg, eventbus.New(), public)
		require.NoError(t, err)
		t.Cleanup(func() { cancel(); _ = node.Close() })
		return public, node.PeerHost
	}

	public, h := newNode([]string{routerAddr})
	require.Equal(t, []string{routerAddr}, public.get())
	require.Eventually(t, func() bool { return hasAddr(h.Addrs(), routerAddr) }, 15*time.Second,
		100*time.Millisecond, "announced from the start: %v", h.Addrs())

	bad, h2 := newNode([]string{"/ip4/192.168.1.102/tcp/4014"})
	require.Empty(t, bad.get())
	require.NoError(t, bad.set([]string{"/ip4/82.123.115.203/tcp/4014"}))
	require.Eventually(t, func() bool { return hasAddr(h2.Addrs(), "/ip4/82.123.115.203/tcp/4014") },
		15*time.Second, 100*time.Millisecond, "set at runtime")
}

// The proxy offers the addresses to the RPC (net_setPublicAddrs) and refuses a bad list.
func TestIpfsProxyPublicAddrs(t *testing.T) {
	public, ok := proxy.(PublicAddrs)
	require.True(t, ok)
	t.Cleanup(func() { _ = public.SetPublicAddrs(nil) })
	require.Error(t, public.SetPublicAddrs([]string{"/ip4/10.0.0.1/tcp/4012"}))
	require.NoError(t, public.SetPublicAddrs([]string{"/ip4/82.123.115.203/tcp/4012"}))
	require.Equal(t, []string{"/ip4/82.123.115.203/tcp/4012"}, public.PublicAddrs())
	require.Eventually(t, func() bool { return hasAddr(proxy.Host().Addrs(), "/ip4/82.123.115.203/tcp/4012") },
		15*time.Second, 100*time.Millisecond, "the shared test node announces it")
}

func TestPublicAddrsAnnouncedOnce(t *testing.T) {
	p := &publicAddrs{}
	require.NoError(t, p.set([]string{"/ip4/1.2.3.4/tcp/1", "/ip4/1.2.3.4/tcp/1", "/ip4/5.6.7.8/tcp/2"}))
	got := p.appendTo([]ma.Multiaddr{ma.StringCast("/ip4/5.6.7.8/tcp/2")})
	require.Equal(t, []string{"/ip4/5.6.7.8/tcp/2", "/ip4/1.2.3.4/tcp/1"}, func() (out []string) {
		for _, a := range got {
			out = append(out, a.String())
		}
		return
	}())
}

func TestPublicAddrsWithoutHolder(t *testing.T) {
	var p *publicAddrs
	require.Error(t, p.set([]string{"/ip4/1.2.3.4/tcp/1"}))
	require.Empty(t, p.get())
	require.Len(t, p.appendTo([]ma.Multiaddr{ma.StringCast("/ip4/1.2.3.4/tcp/1")}), 1)
	require.NotNil(t, p.hostOption())
	proxy := &ipfsProxy{}
	require.Error(t, proxy.SetPublicAddrs(nil))
	require.Empty(t, proxy.PublicAddrs())
}

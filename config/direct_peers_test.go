package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	directPeerA = "QmNqkSwad5HTShxVzFcYLQkRCRjrs9ZhQykqrRTQcdR7xp"
	directPeerB = "QmTseSBwV9xPN2iEn6ViZbdPbk5MBk1HAD9SKy8B2EgSrY"
	directPeerC = "QmVMxHMU7pFf475gQRA158unEQCbmhJz6u3k81nuRueAdp"
	directPeerD = "QmNYWtiwM1UfeCmHfWSdefrMuQdg6nycY5yS64HYqWCUhD"
)

func TestDirectPeerInfosReadsIdsAndAddresses(t *testing.T) {
	p2p := P2P{DirectPeers: []string{
		directPeerA,
		"/ip4/51.178.138.211/tcp/40405/p2p/" + directPeerB,
		" /p2p/" + directPeerC + " ",
	}}

	infos, err := p2p.DirectPeerInfos()
	require.NoError(t, err)
	require.Len(t, infos, 3)
	require.Equal(t, directPeerA, infos[0].ID.String())
	require.Empty(t, infos[0].Addrs)
	require.Equal(t, directPeerB, infos[1].ID.String())
	require.Len(t, infos[1].Addrs, 1)
	require.Equal(t, "/ip4/51.178.138.211/tcp/40405", infos[1].Addrs[0].String())
	require.Equal(t, directPeerC, infos[2].ID.String())
	require.Empty(t, infos[2].Addrs)
}

func TestDirectPeerInfosRefusesBadLists(t *testing.T) {
	for name, entries := range map[string][]string{
		"too many":     {directPeerA, directPeerB, directPeerC, directPeerD},
		"listed twice": {directPeerA, "/ip4/1.2.3.4/tcp/40405/p2p/" + directPeerA},
		"not an id":    {"not-a-peer-id"},
		"no peer id":   {"/ip4/1.2.3.4/tcp/40405"},
		"empty":        {""},
	} {
		_, err := P2P{DirectPeers: entries}.DirectPeerInfos()
		require.Error(t, err, name)
	}
}

func TestApplyP2PFlagsSetsDirectPeers(t *testing.T) {
	cfg := getDefaultConfig(DefaultDataDir)
	ctx := newTestContext(t)

	require.NoError(t, ctx.Set(DirectPeersFlag.Name, directPeerA+", "+directPeerB+","))
	applyP2PFlags(ctx, cfg)

	require.Equal(t, []string{directPeerA, directPeerB}, cfg.P2P.DirectPeers)
}

func TestApplyP2PFlagsEmptyDirectPeersClearsTheList(t *testing.T) {
	cfg := getDefaultConfig(DefaultDataDir)
	cfg.P2P.DirectPeers = []string{directPeerA}
	ctx := newTestContext(t)

	require.NoError(t, ctx.Set(DirectPeersFlag.Name, ""))
	applyP2PFlags(ctx, cfg)

	require.Empty(t, cfg.P2P.DirectPeers)
}

func TestMakeConfigRefusesInvalidDirectPeers(t *testing.T) {
	ctx := newTestContext(t)
	require.NoError(t, ctx.Set(DirectPeersFlag.Name, "not-a-peer-id"))

	_, err := MakeConfig(ctx, func(cfg *Config) {})
	require.Error(t, err)
}

func TestMakeMobileConfigReadsDirectPeers(t *testing.T) {
	cfg, err := MakeMobileConfig(t.TempDir(), `{"P2P":{"DirectPeers":["`+directPeerA+`"]}}`)
	require.NoError(t, err)

	require.Equal(t, []string{directPeerA}, cfg.P2P.DirectPeers)
}

func TestMakeMobileConfigRefusesTooManyDirectPeers(t *testing.T) {
	_, err := MakeMobileConfig(t.TempDir(),
		`{"P2P":{"DirectPeers":["`+directPeerA+`","`+directPeerB+`","`+directPeerC+`","`+directPeerD+`"]}}`)
	require.Error(t, err)
}

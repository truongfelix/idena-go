package config

type P2P struct {
	MaxInboundPeers  int
	MaxOutboundPeers int

	MaxInboundOwnShardPeers  int
	MaxOutboundOwnShardPeers int

	MaxDelay       int
	DisableMetrics bool
	Multishard     bool
	Shared         bool

	// DirectPeers names up to MaxDirectPeers nodes the node stays connected to beyond the peer slots: a peer id, or
	// a multiaddr ending in /p2p/<id> whose address is tried before a DHT lookup.
	DirectPeers []string
}

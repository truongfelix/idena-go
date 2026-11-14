package ipfs

// MaxPublicAddrs bounds the addresses a node announces beside the ones libp2p finds itself.
const MaxPublicAddrs = 4

// PublicAddrs is the part of an IPFS proxy that announces addresses the node cannot learn by itself: the router port
// the app opened (UPnP), on the router's public address. libp2p takes a node behind a router as unreachable until
// its AutoNAT probes say otherwise (every 15 minutes, after 4 peers saw the same address) and announces only relay
// addresses until then, and a phone cannot list its own interfaces (Android 11+): without these, the opened port
// stays unknown to the network.
type PublicAddrs interface {
	// SetPublicAddrs replaces the announced addresses: public TCP multiaddrs (/ip4/<a>/tcp/<p> or /ip6/...), at most
	// MaxPublicAddrs, on a node with a static IPFS port; none clears them. Peers learn the change within seconds (identify push), no restart.
	SetPublicAddrs(addrs []string) error
	// PublicAddrs is the announced addresses, as given.
	PublicAddrs() []string
}

//go:build !idena_memory_ipfs

package ipfs

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/log"

	kubolibp2p "github.com/ipfs/kubo/core/node/libp2p"
	"github.com/libp2p/go-libp2p"
	libp2pconfig "github.com/libp2p/go-libp2p/config"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// errNoStaticPort refuses public addresses on a node whose IPFS port may move (it moves after 2 minutes without
// peers): a router forwards one fixed port, so an address would soon lead nowhere.
var errNoStaticPort = errors.New("public addresses need a static IPFS port (--ipfsportstatic)")

// publicAddrs holds the announced addresses; the host's address factory reads them every time libp2p computes the
// node's addresses (every few seconds), so a change applies at once.
type publicAddrs struct {
	mu    sync.RWMutex
	addrs []ma.Multiaddr
}

// parsePublicAddrs checks each address: a multiaddr, a public IP (not private, loopback or reserved), TCP with a port,
// no peer id or other part after the port. Any bad one refuses the whole list; one given twice counts once.
func parsePublicAddrs(addrs []string) ([]ma.Multiaddr, error) {
	parsed := make([]ma.Multiaddr, 0, len(addrs))
	for _, s := range addrs {
		a, err := ma.NewMultiaddr(s)
		if err != nil {
			return nil, fmt.Errorf("public address %q: %v", s, err)
		}
		if !manet.IsPublicAddr(a) {
			return nil, fmt.Errorf("public address %q: not a public IP address", s)
		}
		parts := ma.Split(a)
		if len(parts) != 2 || (parts[0].Protocols()[0].Code != ma.P_IP4 && parts[0].Protocols()[0].Code != ma.P_IP6) ||
			parts[1].Protocols()[0].Code != ma.P_TCP {
			return nil, fmt.Errorf("public address %q: expected /ip4/<address>/tcp/<port> or /ip6/<address>/tcp/<port>", s)
		}
		if port, err := a.ValueForProtocol(ma.P_TCP); err != nil || port == "0" {
			return nil, fmt.Errorf("public address %q: no port to dial", s)
		}
		if !slices.ContainsFunc(parsed, a.Equal) {
			parsed = append(parsed, a)
		}
	}
	if len(parsed) > MaxPublicAddrs {
		return nil, fmt.Errorf("at most %d public addresses, got %d", MaxPublicAddrs, len(parsed))
	}
	return parsed, nil
}

// publicAddrsFromConfig takes the addresses of the settings. A bad one, or a port that may move, must not stop the
// node: it then starts announcing none, as before, and says why.
func publicAddrsFromConfig(cfg *config.IpfsConfig, logger log.Logger) *publicAddrs {
	public := &publicAddrs{}
	if len(cfg.PublicAddrs) > 0 && !cfg.StaticPort {
		logger.Warn("Public IPFS addresses ignored", "err", errNoStaticPort)
	} else if err := public.set(cfg.PublicAddrs); err != nil {
		logger.Warn("Public IPFS addresses ignored", "err", err)
	} else if len(cfg.PublicAddrs) > 0 {
		logger.Info("Public IPFS addresses announced", "addrs", strings.Join(cfg.PublicAddrs, ","))
	}
	return public
}

func (p *publicAddrs) set(addrs []string) error {
	parsed, err := parsePublicAddrs(addrs)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.addrs = parsed
	p.mu.Unlock()
	return nil
}

func (p *publicAddrs) get() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, len(p.addrs))
	for i, a := range p.addrs {
		out[i] = a.String()
	}
	return out
}

// appendTo returns addrs with the announced addresses it does not already hold, each once.
func (p *publicAddrs) appendTo(addrs []ma.Multiaddr) []ma.Multiaddr {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.addrs) == 0 {
		return addrs
	}
	out := make([]ma.Multiaddr, 0, len(addrs)+len(p.addrs))
	out = append(out, addrs...)
	for _, a := range p.addrs {
		known := false
		for _, b := range out {
			if a.Equal(b) {
				known = true
				break
			}
		}
		if !known {
			out = append(out, a)
		}
	}
	return out
}

// hostOption builds the libp2p host as Kubo's default does (kubo core/node/libp2p constructPeerHost, then libp2p.New),
// with Kubo's address factory (Addresses.Announce, AppendAnnounce, NoAnnounce) followed by the announced addresses.
// libp2p accepts one address factory only, hence the wrap after the options are applied rather than another option.
// libp2p hides public addresses while it takes the node as unreachable, before the factory runs: the factory's
// addresses are announced in any case.
// It mirrors Kubo v0.42.0 (constructPeerHost) and go-libp2p v0.48.0 (New): compare both on the next upgrade.
func (p *publicAddrs) hostOption() kubolibp2p.HostOption {
	return func(id peer.ID, ps peerstore.Peerstore, options ...libp2p.Option) (host.Host, error) {
		pkey := ps.PrivKey(id)
		if pkey == nil {
			return nil, fmt.Errorf("missing private key for node ID: %s", id)
		}
		options = append([]libp2p.Option{libp2p.Identity(pkey), libp2p.Peerstore(ps)}, options...)
		var cfg libp2pconfig.Config
		if err := cfg.Apply(append(options, libp2p.FallbackDefaults)...); err != nil {
			return nil, err
		}
		inner := cfg.AddrsFactory
		cfg.AddrsFactory = func(addrs []ma.Multiaddr) []ma.Multiaddr {
			if inner != nil {
				addrs = inner(addrs)
			}
			return p.appendTo(addrs)
		}
		return cfg.NewNode()
	}
}

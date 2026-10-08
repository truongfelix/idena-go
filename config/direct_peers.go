package config

import (
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/pkg/errors"
)

// MaxDirectPeers is how many nodes P2P.DirectPeers can name.
const MaxDirectPeers = 3

// DirectPeerInfos parses DirectPeers. An entry without an address leaves the address to a DHT lookup.
func (p P2P) DirectPeerInfos() ([]peer.AddrInfo, error) {
	if len(p.DirectPeers) > MaxDirectPeers {
		return nil, errors.Errorf("invalid P2P.DirectPeers: %d entries, at most %d", len(p.DirectPeers), MaxDirectPeers)
	}
	infos := make([]peer.AddrInfo, 0, len(p.DirectPeers))
	seen := make(map[peer.ID]bool, len(p.DirectPeers))
	for _, entry := range p.DirectPeers {
		info, err := parseDirectPeer(strings.TrimSpace(entry))
		if err != nil {
			return nil, errors.Errorf("invalid P2P.DirectPeers entry %q: %v", entry, err)
		}
		if seen[info.ID] {
			return nil, errors.Errorf("invalid P2P.DirectPeers: %s is listed twice", info.ID)
		}
		seen[info.ID] = true
		infos = append(infos, info)
	}
	return infos, nil
}

func parseDirectPeer(entry string) (peer.AddrInfo, error) {
	if strings.HasPrefix(entry, "/") {
		addr, err := multiaddr.NewMultiaddr(entry)
		if err != nil {
			return peer.AddrInfo{}, err
		}
		info, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
			return peer.AddrInfo{}, err
		}
		return *info, nil
	}
	id, err := peer.Decode(entry)
	if err != nil {
		return peer.AddrInfo{}, err
	}
	return peer.AddrInfo{ID: id}, nil
}

// splitDirectPeers reads the --directpeers flag: comma-separated entries, empty ones dropped, so "" clears the list.
func splitDirectPeers(value string) []string {
	var entries []string
	for _, entry := range strings.Split(value, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

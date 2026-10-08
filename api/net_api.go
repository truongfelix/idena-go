package api

import (
	"github.com/idena-network/idena-go/ipfs"
	"github.com/idena-network/idena-go/protocol"
)

// NetApi offers helper utils
type NetApi struct {
	pm        *protocol.IdenaGossipHandler
	ipfsProxy ipfs.Proxy
}

// NewNetApi creates a new NetApi instance
func NewNetApi(pm *protocol.IdenaGossipHandler, ipfsProxy ipfs.Proxy) *NetApi {
	return &NetApi{pm, ipfsProxy}
}

func (api *NetApi) PeersCount() int {
	return api.pm.PeersCount()
}

type Peer struct {
	ID         string `json:"id"`
	RemoteAddr string `json:"addr"`
	// Direct is set for a node named in P2P.DirectPeers.
	Direct bool `json:"direct,omitempty"`
}

func (api *NetApi) Peers() []Peer {
	peers := make([]Peer, 0)
	for _, p := range api.pm.Peers() {
		peers = append(peers, Peer{
			ID:         p.ID(),
			RemoteAddr: p.RemoteAddr(),
			Direct:     api.pm.IsDirectPeer(p),
		})
	}
	return peers
}

func (api *NetApi) IpfsAddress() string {
	return api.pm.Endpoint()
}

func (api *NetApi) AddPeer(url string) error {
	return api.pm.AddPeer(url)
}

package api

import (
	"errors"

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
	// Inbound is set for a peer that opened the Idena stream to this node (counted in its inbound peers), false for
	// one this node opened it to: always present, so a client tells a node that reports it from an older one.
	Inbound bool `json:"inbound"`
}

func (api *NetApi) Peers() []Peer {
	peers := make([]Peer, 0)
	for _, p := range api.pm.Peers() {
		peers = append(peers, Peer{
			ID:         p.ID(),
			RemoteAddr: p.RemoteAddr(),
			Direct:     api.pm.IsDirectPeer(p),
			Inbound:    p.Inbound(),
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

// SetPublicAddrs replaces the addresses the node announces beside the ones it finds itself (net_setPublicAddrs): the
// router port the app opened, as /ip4/<public address>/tcp/<port>; an empty list clears them. Public TCP addresses
// only, at most 4. Peers learn the change within seconds, without a restart.
func (api *NetApi) SetPublicAddrs(addrs []string) error {
	public, ok := api.ipfsProxy.(ipfs.PublicAddrs)
	if !ok {
		return errors.New("public addresses are not supported by this node")
	}
	return public.SetPublicAddrs(addrs)
}

// PublicAddrs is the addresses set by SetPublicAddrs or at start (net_publicAddrs).
func (api *NetApi) PublicAddrs() []string {
	public, ok := api.ipfsProxy.(ipfs.PublicAddrs)
	if !ok {
		return []string{}
	}
	return public.PublicAddrs()
}

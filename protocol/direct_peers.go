package protocol

import (
	"github.com/libp2p/go-libp2p/core/peer"
)

// dialDirectPeers opens the Idena stream to each direct peer (P2P.DirectPeers) the IPFS layer has a direct
// connection to but that is not an Idena peer yet. Kubo's peering service keeps that connection
// (ipfs.Proxy.KeepConnected); CanConnect keeps the usual wait after a disconnect and refuses a banned peer.
func (h *IdenaGossipHandler) dialDirectPeers() {
	for _, id := range h.connManager.DirectPeers() {
		if h.peers.Peer(id) != nil || !h.connManager.CanConnect(id) {
			continue
		}
		go h.dialDirectPeer(id)
	}
}

func (h *IdenaGossipHandler) dialDirectPeer(id peer.ID) {
	stream, err := h.connManager.newStream(id)
	if err != nil {
		h.log.Debug("cannot open a stream to a direct peer", "id", id.String(), "err", err)
		return
	}
	if _, err := h.runPeer(stream, false); err != nil {
		h.log.Debug("failed to run a direct peer", "id", id.String(), "err", err)
		// runPeer leaves the stream open when the peer is already connected or connecting through another one.
		_ = stream.Reset()
	}
}

// IsDirectPeer tells whether p is named in P2P.DirectPeers.
func (h *IdenaGossipHandler) IsDirectPeer(p *protoPeer) bool {
	return h.connManager.IsDirectPeer(p.id)
}

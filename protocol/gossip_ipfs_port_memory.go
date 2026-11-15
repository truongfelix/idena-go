//go:build idena_memory_ipfs

package protocol

// subscribeIpfsPortChanged does nothing in the memory IPFS build: its IPFS
// has no network node, so no port change is ever published.
func (h *IdenaGossipHandler) subscribeIpfsPortChanged(setHandler func()) {}

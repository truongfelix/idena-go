package depcheck

// This guard protects the Idena peer-record fix in a replaced dependency.
// Upstream kad-dht counts nonpublic advertised addresses when filtering DHT
// responses, which can prevent Idena nodes from finding peers and IPFS blocks.
// The pinned fork ignores those addresses while retaining public IP diversity.

import "testing"

const (
	kadDHTModule         = "github.com/libp2p/go-libp2p-kad-dht"
	patchedKadDHTMod     = "github.com/truongfelix/go-libp2p-kad-dht"
	patchedKadDHTVersion = "v0.41.1-0.20260924080750-83f1403bcb17"
)

func TestKadDHTReplaceDirectivePresent(t *testing.T) {
	requireReplacement(t, kadDHTModule, patchedKadDHTMod, patchedKadDHTVersion,
		"containing the Idena DHT fix")
}

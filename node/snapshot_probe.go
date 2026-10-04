package node

// Diagnostic only (not for release). With IDENA_PROBE_CID set, the node waits for its IPFS peers, then
// downloads that snapshot block by block (ipfs.ProbeSnapshot). Log lines start with "PROBE".

import (
	"os"
	"time"

	"github.com/idena-network/idena-go/ipfs"
	"github.com/ipfs/go-cid"
)

func (node *Node) startSnapshotProbe() {
	value := os.Getenv("IDENA_PROBE_CID")
	if value == "" {
		return
	}
	logf := func(msg string, ctx ...interface{}) { node.log.Info("PROBE "+msg, ctx...) }
	go func() {
		key, err := cid.Decode(value)
		if err != nil {
			logf("bad cid", "err", err)
			return
		}
		// The same start for every variant: 90 seconds to connect to peers.
		time.Sleep(90 * time.Second)
		logf("probing", "idenaPeers", node.pm.PeersCount())
		logf("finished", "err", ipfs.ProbeSnapshot(node.ipfsProxy, key, 3*time.Minute, logf))
	}()
}

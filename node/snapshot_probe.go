package node

// Diagnostic only (branch diag/snapshot-probe, not for release). With IDENA_SNAPSHOT_PROBE set, the node
// waits for the snapshot manifests its peers announce, then downloads the newest snapshot block by block
// (ipfs.ProbeSnapshot), logging how every block arrives. Log lines start with "PROBE".

import (
	"os"
	"strconv"
	"time"

	"github.com/idena-network/idena-go/core/state/snapshot"
	"github.com/idena-network/idena-go/ipfs"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
)

func (node *Node) startSnapshotProbe() {
	if os.Getenv("IDENA_FILESTORE_CHECK") != "" {
		logf := func(msg string, ctx ...interface{}) { node.log.Info("FSCHECK "+msg, ctx...) }
		if m := node.blockchain.ReadSnapshotManifest(); m != nil {
			c, _ := cid.Cast(m.CidV2)
			logf("own snapshot", "height", m.Height, "cidV2", c)
			logf("finished", "err", ipfs.CheckSnapshotFilestore(node.ipfsProxy, m.CidV2, logf))
		} else {
			logf("no own snapshot manifest")
		}
	}
	if os.Getenv("IDENA_SNAPSHOT_PROBE") == "" {
		return
	}
	logf := func(msg string, ctx ...interface{}) { node.log.Info("PROBE "+msg, ctx...) }
	// With IDENA_SNAPSHOT_PROBE_MIN_HEIGHT, waits (up to 6 hours) for a snapshot at least that high.
	minHeight, _ := strconv.ParseUint(os.Getenv("IDENA_SNAPSHOT_PROBE_MIN_HEIGHT"), 10, 64)
	// With IDENA_SNAPSHOT_PROBE_PEER, only the manifest that peer announces counts.
	onlyPeer := os.Getenv("IDENA_SNAPSHOT_PROBE_PEER")
	go func() {
		started := time.Now()
		maxWait := 10 * time.Minute
		if minHeight > 0 {
			maxWait = 6 * time.Hour
		}
		seen := make(map[string]bool)
		var best *snapshot.Manifest
		var manifests map[peer.ID]*snapshot.Manifest
		versions := make(map[string]string)
		for {
			manifests = node.pm.GetKnownManifests()
			for _, p := range node.pm.Peers() {
				versions[p.ID()] = p.AppVersion()
			}
			best = nil
			for id, m := range manifests {
				c, _ := cid.Cast(m.CidV2)
				if key := id.String() + c.String(); !seen[key] {
					seen[key] = true
					logf("manifest", "peer", id, "version", versions[id.String()], "height", m.Height, "cidV2", c)
				}
				if onlyPeer != "" && id.String() != onlyPeer {
					continue
				}
				if best == nil || m.Height > best.Height {
					best = m
				}
			}
			ready := best != nil && best.Height >= minHeight &&
				(minHeight > 0 || len(manifests) >= 3 || time.Since(started) > 2*time.Minute)
			if ready || time.Since(started) > maxWait {
				break
			}
			time.Sleep(10 * time.Second)
		}
		if best == nil || best.Height < minHeight {
			logf("no suitable manifest", "idenaPeers", node.pm.PeersCount())
			return
		}
		holders := 0
		for id, m := range manifests {
			if string(m.CidV2) == string(best.CidV2) {
				holders++
				logf("holder", "peer", id, "version", versions[id.String()])
			}
		}
		counts := make(map[string]int)
		for _, p := range node.pm.Peers() {
			counts[p.AppVersion()]++
		}
		for v, n := range counts {
			logf("peer versions", "version", v, "peers", n)
		}
		logf("probing", "height", best.Height, "peersAnnouncingIt", holders, "idenaPeers", node.pm.PeersCount(),
			"waitedForManifests", time.Since(started).Round(time.Second))
		err := ipfs.ProbeSnapshot(node.ipfsProxy, best.CidV2, 3*time.Minute, logf)
		logf("finished", "err", err)
	}()
}

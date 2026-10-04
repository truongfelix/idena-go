package ipfs

// Diagnostic only (branch diag/snapshot-probe, not for release): downloads a snapshot block by block
// and logs how every block arrives, to find why snapshot downloads stall on some nodes.

import (
	"context"
	"errors"
	"time"

	"github.com/ipfs/boxo/filestore"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/network"
)

// ProbeSnapshot fetches the DAG of the snapshot key depth-first, one block at a time with blockTimeout
// each. A block that does not arrive is logged with the providers the routing finds for it, and the walk
// goes on: it shows whether one block or all the following ones are missing.
func ProbeSnapshot(proxy Proxy, key []byte, blockTimeout time.Duration, logf func(msg string, ctx ...interface{})) error {
	p, ok := proxy.(*ipfsProxy)
	if !ok {
		return errors.New("the probe needs the real IPFS node")
	}
	root, err := cid.Cast(key)
	if err != nil {
		return err
	}
	node := p.node
	ctx := context.Background()
	logf("start", "cid", root, "ipfsPeers", len(node.PeerHost.Network().Peers()))

	var blocks, local, failed, bytes int
	started := time.Now()
	var walk func(c cid.Cid, depth int)
	walk = func(c cid.Cid, depth int) {
		blocks++
		n := blocks
		isLocal, _ := node.Blockstore.Has(ctx, c)
		if isLocal {
			local++
		}
		start := time.Now()
		blockCtx, cancel := context.WithTimeout(ctx, blockTimeout)
		nd, err := node.DAG.Get(blockCtx, c)
		cancel()
		if err != nil {
			failed++
			logf("block FAILED", "n", n, "depth", depth, "cid", c, "local", isLocal, "waited", time.Since(start).Round(time.Millisecond),
				"err", err, "ipfsPeers", len(node.PeerHost.Network().Peers()))
			p.probeProviders(c, logf)
			return
		}
		bytes += len(nd.RawData())
		logf("block", "n", n, "depth", depth, "local", isLocal, "size", len(nd.RawData()), "links", len(nd.Links()),
			"took", time.Since(start).Round(time.Millisecond))
		for _, link := range nd.Links() {
			walk(link.Cid, depth+1)
		}
	}
	walk(root, 0)

	logf("done", "blocks", blocks, "local", local, "failed", failed, "bytes", bytes, "took", time.Since(started).Round(time.Second))
	if node.Bitswap != nil {
		if stat, err := node.Bitswap.Stat(); err == nil {
			logf("bitswap", "blocksReceived", stat.BlocksReceived, "dataReceived", stat.DataReceived,
				"duplicateBlocks", stat.DupBlksReceived, "peers", len(stat.Peers), "wantlist", len(stat.Wantlist))
		}
		for _, id := range node.PeerHost.Network().Peers() {
			if receipt := node.Bitswap.LedgerForPeer(id); receipt != nil && receipt.Recv > 0 {
				logf("received from", "peer", id, "bytes", receipt.Recv)
			}
		}
	}
	if failed > 0 {
		return errors.New("some blocks did not arrive")
	}
	return nil
}

// probeProviders logs the peers the routing knows for c, and whether this node is connected to them.
func (p *ipfsProxy) probeProviders(c cid.Cid, logf func(msg string, ctx ...interface{})) {
	if p.node.Routing == nil {
		logf("no routing")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	count := 0
	for info := range p.node.Routing.FindProvidersAsync(ctx, c, 10) {
		count++
		connected := p.node.PeerHost.Network().Connectedness(info.ID) == network.Connected
		logf("provider", "cid", c, "peer", info.ID, "connected", connected, "addrs", len(info.Addrs))
	}
	logf("providers found", "cid", c, "count", count)
}

// CheckSnapshotFilestore lists, for every leaf of the snapshot key, the file its filestore entry
// (Nocopy) points to and whether that file still holds the block: blocks shared with an older snapshot
// keep the entry of the older file, which the node deletes after each new snapshot.
func CheckSnapshotFilestore(proxy Proxy, key []byte, logf func(msg string, ctx ...interface{})) error {
	p, ok := proxy.(*ipfsProxy)
	if !ok || p.node.Filestore == nil {
		return errors.New("no filestore")
	}
	root, err := cid.Cast(key)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nd, err := p.node.DAG.Get(ctx, root)
	if err != nil {
		return err
	}
	type group struct {
		file   string
		status filestore.Status
	}
	counts := make(map[group]int)
	for i, link := range nd.Links() {
		res := filestore.Verify(context.Background(), p.node.Filestore, link.Cid)
		counts[group{res.FilePath, res.Status}]++
		if res.Status != filestore.StatusOk && counts[group{res.FilePath, res.Status}] <= 3 {
			logf("filestore leaf", "n", i+1, "cid", link.Cid, "file", res.FilePath, "offset", res.Offset, "status", res.Status.String())
		}
	}
	for g, n := range counts {
		logf("filestore", "leaves", n, "file", g.file, "status", g.status.String())
	}
	logf("filestore checked", "cid", root, "leaves", len(nd.Links()))
	return nil
}

package ipfs

// Diagnostic only (not for release): downloads a snapshot block by block and logs how every block
// arrives, to compare the official IPFS stack with the fork's (ablation of the snapshot stall).

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ipfs/go-cid"
)

// ProbeSnapshot fetches the DAG of key depth-first, one block at a time with blockTimeout each, and
// logs every block; a block that does not arrive is logged with the providers the routing finds.
func ProbeSnapshot(proxy Proxy, key cid.Cid, blockTimeout time.Duration, logf func(msg string, ctx ...interface{})) error {
	p, ok := proxy.(*ipfsProxy)
	if !ok {
		return errors.New("the probe needs the real IPFS node")
	}
	node := p.node
	ctx := context.Background()
	logf("start", "cid", key, "ipfsPeers", len(node.PeerHost.Network().Peers()))
	var blocks, failed int
	started := time.Now()
	var walk func(c cid.Cid, depth int)
	maxBlocks, _ := strconv.Atoi(os.Getenv("IDENA_PROBE_MAX_BLOCKS"))
	walk = func(c cid.Cid, depth int) {
		if maxBlocks > 0 && blocks >= maxBlocks {
			return
		}
		blocks++
		n := blocks
		isLocal, _ := node.Blockstore.Has(ctx, c)
		start := time.Now()
		blockCtx, cancel := context.WithTimeout(ctx, blockTimeout)
		nd, err := node.DAG.Get(blockCtx, c)
		cancel()
		if err != nil {
			failed++
			logf("block FAILED", "n", n, "depth", depth, "cid", c, "local", isLocal, "waited", time.Since(start).Round(time.Millisecond),
				"err", err, "ipfsPeers", len(node.PeerHost.Network().Peers()))
			pctx, pcancel := context.WithTimeout(ctx, time.Minute)
			count := 0
			for info := range node.Routing.FindProvidersAsync(pctx, c, 10) {
				count++
				logf("provider", "cid", c, "peer", info.ID, "connected", node.PeerHost.Network().Connectedness(info.ID).String())
			}
			pcancel()
			logf("providers found", "cid", c, "count", count)
			return
		}
		logf("block", "n", n, "depth", depth, "local", isLocal, "size", len(nd.RawData()), "links", len(nd.Links()),
			"took", time.Since(start).Round(time.Millisecond))
		if dir := os.Getenv("IDENA_PROBE_SAVE_DIR"); dir != "" {
			_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d.bin", n-1)), nd.RawData(), 0600)
		}
		if depth == 0 {
			// The root: its encoding and the fingerprints of all its pieces, to compare two roots.
			logf("root raw", "hex", hex.EncodeToString(nd.RawData()))
			for i, link := range nd.Links() {
				logf("root link", "i", i+1, "cid", link.Cid, "size", link.Size, "name", link.Name)
			}
			if os.Getenv("IDENA_PROBE_ROOT_ONLY") != "" {
				return
			}
		}
		for _, link := range nd.Links() {
			walk(link.Cid, depth+1)
		}
	}
	walk(key, 0)
	logf("done", "blocks", blocks, "failed", failed, "took", time.Since(started).Round(time.Second))
	if failed > 0 {
		return errors.New("some blocks did not arrive")
	}
	return nil
}

// Diagnostic only (branch diag/snapshot-probe, not for release). Checks offline, in a copy of a node's
// IPFS repo, which file every leaf of its pinned snapshots points to (filestore, Nocopy) and whether that
// file still holds the leaf. Usage: fscheck <ipfs repo dir>
package main

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/ipfs/boxo/blockservice"
	"github.com/ipfs/boxo/blockstore"
	offline "github.com/ipfs/boxo/exchange/offline"
	"github.com/ipfs/boxo/filestore"
	"github.com/ipfs/boxo/ipld/merkledag"
	"github.com/ipfs/boxo/pinning/pinner/dspinner"
	"github.com/ipfs/kubo/plugin/loader"
	"github.com/ipfs/kubo/repo/fsrepo"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: fscheck <ipfs repo dir>")
		os.Exit(2)
	}
	ctx := context.Background()
	plugins, err := loader.NewPluginLoader("")
	must(err)
	must(plugins.Initialize())
	must(plugins.Inject())
	r, err := fsrepo.Open(os.Args[1])
	must(err)
	defer r.Close()
	if r.FileManager() == nil {
		must(fmt.Errorf("the repo has no filestore"))
	}
	fs := filestore.NewFilestore(blockstore.NewBlockstore(r.Datastore()), r.FileManager(), nil)
	dag := merkledag.NewDAGService(blockservice.New(fs, offline.Exchange(fs)))
	pinner, err := dspinner.New(ctx, r.Datastore(), dag)
	must(err)
	pins, snapshots := 0, 0
	for pin := range pinner.RecursiveKeys(ctx, false) {
		must(pin.Err)
		pins++
		root := pin.Pin.Key
		nd, err := dag.Get(ctx, root)
		if err != nil || len(nd.Links()) == 0 {
			continue
		}
		counts := make(map[string]int)
		inFilestore := 0
		var bad []string
		for i, link := range nd.Links() {
			res := filestore.Verify(ctx, fs, link.Cid)
			if res.Status == filestore.StatusKeyNotFound {
				continue
			}
			inFilestore++
			counts[fmt.Sprintf("%-8s %s", res.Status.String(), res.FilePath)]++
			if res.Status != filestore.StatusOk && len(bad) < 5 {
				bad = append(bad, fmt.Sprintf("leaf %d %s -> offset %d of %s: %s", i+1, link.Cid, res.Offset, res.FilePath, res.Status))
			}
		}
		if inFilestore == 0 {
			continue
		}
		snapshots++
		fmt.Printf("pinned %s: %d leaves, %d in the filestore\n", root, len(nd.Links()), inFilestore)
		keys := make([]string, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  %4d leaves  %s\n", counts[k], k)
		}
		for _, b := range bad {
			fmt.Println("    ", b)
		}
	}
	fmt.Printf("%d recursive pins, %d with filestore leaves\n", pins, snapshots)
}

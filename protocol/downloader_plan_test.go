package protocol

import (
	"testing"

	"github.com/idena-network/idena-go/blockchain"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/core/state/snapshot"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

func headersAt(height uint64) *types.Header {
	return &types.Header{ProposedHeader: &types.ProposedHeader{Height: height}}
}

func TestChooseSyncPlan(t *testing.T) {
	on := &config.SyncConfig{FastSync: true, ForceFullSync: 100}
	manifest := func(height uint64) *snapshot.Manifest { return &snapshot.Manifest{Height: height} }

	for _, c := range []struct {
		name            string
		cfg             *config.SyncConfig
		head, top       uint64
		preliminaryHead *types.Header
		manifest        *snapshot.Manifest
		failedSnapshots int
		want            syncPlan
	}{
		{"close to the top", on, 1000, 1050, nil, manifest(2000), 0, planFullSync},
		{"snapshot far above the chain", on, 1000, 5000, nil, manifest(4000), 0, planFastSync},
		{"snapshot too close to the chain", on, 1000, 5000, nil, manifest(1050), 0, planFullSync},
		{"snapshot below the chain", on, 1000, 5000, nil, manifest(900), 0, planFullSync},
		{"no snapshot and no fast sync under way", on, 1000, 5000, nil, nil, 0, planFullSync},
		// Mainnet, 2026-09-28: a new node had its headers up to the snapshot height, and the snapshot
		// could not be downloaded.
		{"no snapshot, headers far above the chain", on, 4871137, 11369200, headersAt(11369095), nil, 1, planWaitForSnapshot},
		{"no snapshot, headers barely above the chain", on, 1000, 5000, headersAt(1050), nil, 0, planFullSync},
		{"newer snapshot while the headers are kept", on, 4871137, 11370200, headersAt(11369095), manifest(11370095), 1, planFastSync},
		{"fast sync switched off", &config.SyncConfig{FastSync: false, ForceFullSync: 100}, 4871137, 11369200, headersAt(11369095), nil, 1, planFullSync},
		// Another manifest of the headers' snapshot (another CID for the same height).
		{"other manifest of the headers' snapshot", on, 4871137, 11369200, headersAt(11369095), manifest(11369095), 1, planFastSync},
		// The fast sync goes on from its headers, above this snapshot: it cannot complete the sync.
		{"snapshot below the headers", on, 4871137, 11369200, headersAt(11369095), manifest(11368095), 1, planWaitForSnapshot},
		{"two failed snapshots", on, 4871137, 11371200, headersAt(11370095), nil, maxFailedSnapshots - 1, planWaitForSnapshot},
		{"too many failed snapshots", on, 4871137, 11371200, headersAt(11371095), nil, maxFailedSnapshots, planFullSync},
		{"too many failed snapshots, snapshot below the headers", on, 4871137, 11371200, headersAt(11371095), manifest(11370095), maxFailedSnapshots, planFullSync},
		// A usable snapshot is always tried.
		{"newer snapshot after too many failed snapshots", on, 4871137, 11372200, headersAt(11371095), manifest(11372095), maxFailedSnapshots, planFastSync},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, chooseSyncPlan(c.cfg, c.head, c.top, c.preliminaryHead, c.manifest, c.failedSnapshots))
		})
	}
}

func TestDownloaderCountsFailedSnapshots(t *testing.T) {
	chain, _, _, _ := blockchain.NewTestBlockchain(false, nil)
	sm := state.NewSnapshotManager(db.NewMemDB(), nil, eventbus.New(), nil, nil)
	d := &Downloader{chain: chain.Blockchain, sm: sm}
	// Each attempt of a fast sync whose download times out, as Load runs it.
	timeOut := func(cid string, height uint64, attempts int) {
		m := &snapshot.Manifest{CidV2: []byte(cid), Height: height}
		for i := 0; i < attempts; i++ {
			sm.AddTimeoutManifest(m.CidV2)
			d.recordFailedSnapshot(&fastSync{manifest: m})
		}
	}
	chain.PreliminaryHead = headersAt(11369095)

	// The downloader gives a snapshot up after MaxManifestTimeouts timeouts, not before.
	timeOut("a", 11369095, int(state.MaxManifestTimeouts)-1)
	require.Equal(t, 0, d.failedSnapshotCount())
	timeOut("a", 11369095, 1)
	require.Equal(t, 1, d.failedSnapshotCount())

	// Another manifest of the same snapshot: still one failed snapshot.
	timeOut("b", 11369095, int(state.MaxManifestTimeouts))
	require.Equal(t, 1, d.failedSnapshotCount())

	// A snapshot that cannot be loaded fails at once.
	sm.AddInvalidManifest([]byte("c"))
	d.recordFailedSnapshot(&fastSync{manifest: &snapshot.Manifest{CidV2: []byte("c"), Height: 11370095}})
	require.Equal(t, 2, d.failedSnapshotCount())

	// A full sync that fails is not a failed snapshot.
	d.recordFailedSnapshot(&fullSync{})
	require.Equal(t, 2, d.failedSnapshotCount())

	// Headers no longer kept (the fast sync completed, or a full sync dropped them): the count starts again.
	chain.PreliminaryHead = nil
	require.Equal(t, 0, d.failedSnapshotCount())
}

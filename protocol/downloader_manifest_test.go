package protocol

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/idena-network/idena-go/blockchain"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/core/state/snapshot"
	"github.com/idena-network/idena-go/ipfs"
	"github.com/idena-network/idena-go/log"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

// A mainnet snapshot height: on 2026-09-29 six official nodes announced one CID for it, a node of another build
// a second one.
const testSnapshotHeight = 11370095

func newManifestTestDownloader() *Downloader {
	return &Downloader{
		log: log.New(),
		sm:  state.NewSnapshotManager(db.NewMemDB(), nil, eventbus.New(), nil, nil),
		top: testSnapshotHeight + 10000,
	}
}

func cidManifest(height uint64, cid string) *snapshot.Manifest {
	return &snapshot.Manifest{Height: height, CidV2: []byte(cid)}
}

// announce gives each peer the manifest.
func announce(manifests map[peer.ID]*snapshot.Manifest, m *snapshot.Manifest, peers ...peer.ID) {
	for _, p := range peers {
		manifests[p] = m
	}
}

// failSnapshot does what a fast sync pass does when the snapshot it tried cannot be downloaded (postConsuming,
// then afterFailedPass).
func failSnapshot(d *Downloader, m *snapshot.Manifest, announcers []peer.ID) {
	d.sm.AddTimeoutManifest(m.CidV2)
	d.afterFailedPass(&fastSync{manifest: m, announcers: announcers, snapshotFailure: snapshotFailed})
}

func chosenCid(t *testing.T, d *Downloader, manifests map[peer.ID]*snapshot.Manifest) (string, []peer.ID) {
	t.Helper()
	m, announcers := d.chooseManifest(manifests)
	if m == nil {
		require.Empty(t, announcers)
		return "", nil
	}
	return string(m.CidV2), announcers
}

func TestChooseManifestTakesTheMajorityOfTheNewestHeight(t *testing.T) {
	d := newManifestTestDownloader()
	// A manifest with no CID cannot be downloaded, however new.
	manifests := map[peer.ID]*snapshot.Manifest{"empty": cidManifest(testSnapshotHeight+5000, ""), "none": nil}
	announce(manifests, cidManifest(testSnapshotHeight, "official"), "o1", "o2", "o3", "o4", "o5", "o6")
	announce(manifests, cidManifest(testSnapshotHeight, "other build"), "b1")
	announce(manifests, cidManifest(testSnapshotHeight-1000, "older"), "p1", "p2", "p3", "p4", "p5", "p6", "p7")

	cid, announcers := chosenCid(t, d, manifests)
	require.Equal(t, "official", cid)
	require.ElementsMatch(t, []peer.ID{"o1", "o2", "o3", "o4", "o5", "o6"}, announcers)

	// The newest snapshot goes first, however few peers announce it yet: peers announce their manifest when
	// they connect.
	announce(manifests, cidManifest(testSnapshotHeight+1000, "newer"), "n1")
	cid, _ = chosenCid(t, d, manifests)
	require.Equal(t, "newer", cid)
}

// The most announced CID fails; each failure moves the next attempt on, and a height that keeps failing is given up
// until a newer snapshot height is announced (the sync full syncs meanwhile).
func TestChooseManifestAfterAFailedSnapshot(t *testing.T) {
	served := cidManifest(testSnapshotHeight, "served")

	t.Run("the most announced snapshot is not served", func(t *testing.T) {
		d := newManifestTestDownloader()
		manifests := map[peer.ID]*snapshot.Manifest{}
		announce(manifests, served, "k1", "k2")
		announce(manifests, cidManifest(testSnapshotHeight, "unserved"), "m1", "m2", "m3")

		cid, announcers := chosenCid(t, d, manifests)
		require.Equal(t, "unserved", cid)
		failSnapshot(d, manifests["m1"], announcers)

		cid, _ = chosenCid(t, d, manifests)
		require.Equal(t, "served", cid)
	})

	t.Run("the peers of a failed snapshot announce another CID", func(t *testing.T) {
		d := newManifestTestDownloader()
		manifests := map[peer.ID]*snapshot.Manifest{}
		announce(manifests, served, "k1", "k2")
		announce(manifests, cidManifest(testSnapshotHeight, "cid-0"), "m1", "m2", "m3")

		_, announcers := chosenCid(t, d, manifests)
		failSnapshot(d, manifests["m1"], announcers)
		announce(manifests, cidManifest(testSnapshotHeight, "cid-1"), "m1", "m2", "m3")

		cid, _ := chosenCid(t, d, manifests)
		require.Equal(t, "served", cid)
	})

	t.Run("new peers announce another CID after each failure", func(t *testing.T) {
		d := newManifestTestDownloader()
		manifests := map[peer.ID]*snapshot.Manifest{}
		announce(manifests, served, "k1", "k2")
		for round, ids := range [][]peer.ID{{"a1", "a2", "a3"}, {"b1", "b2", "b3"}, {"c1", "c2", "c3"}} {
			next := cidManifest(testSnapshotHeight, string(rune('x'+round)))
			announce(manifests, next, ids...)
			cid, announcers := chosenCid(t, d, manifests)
			require.Equal(t, string(next.CidV2), cid)
			failSnapshot(d, next, announcers)
		}

		// The height is given up: no fast sync to it in this sync.
		cid, _ := chosenCid(t, d, manifests)
		require.Empty(t, cid)

		// A newer snapshot height starts afresh.
		announce(manifests, cidManifest(testSnapshotHeight+1000, "newer"), "k3")
		cid, _ = chosenCid(t, d, manifests)
		require.Equal(t, "newer", cid)
	})

	t.Run("one snapshot, which fails for a while", func(t *testing.T) {
		d := newManifestTestDownloader()
		manifests := map[peer.ID]*snapshot.Manifest{}
		announce(manifests, served, "k1", "k2")
		for i := 0; i < MaxSnapshotHeightFailures; i++ {
			cid, announcers := chosenCid(t, d, manifests)
			require.Equal(t, "served", cid, "attempt %d", i+1)
			failSnapshot(d, served, announcers)
		}
		cid, _ := chosenCid(t, d, manifests)
		require.Empty(t, cid)

		// The next sync tries the height again; the download goes on from the blocks already in IPFS.
		chain, _, _, _ := blockchain.NewTestBlockchain(false, nil)
		d.chain = chain.Blockchain
		d.startSync()
		cid, _ = chosenCid(t, d, manifests)
		require.Equal(t, "served", cid)
	})
}

func TestChooseManifestOrder(t *testing.T) {
	t.Run("equal votes: fewer failed downloads first", func(t *testing.T) {
		d := newManifestTestDownloader()
		d.sm.AddTimeoutManifest([]byte("served")) // in an earlier sync
		manifests := map[peer.ID]*snapshot.Manifest{}
		announce(manifests, cidManifest(testSnapshotHeight, "served"), "k1")
		announce(manifests, cidManifest(testSnapshotHeight, "other"), "m1")

		cid, _ := chosenCid(t, d, manifests)
		require.Equal(t, "other", cid)

		// Votes go before failed downloads.
		announce(manifests, manifests["k1"], "k2")
		cid, _ = chosenCid(t, d, manifests)
		require.Equal(t, "served", cid)
	})

	t.Run("equal votes and failures: most announcers first", func(t *testing.T) {
		d := newManifestTestDownloader()
		manifests := map[peer.ID]*snapshot.Manifest{}
		announce(manifests, cidManifest(testSnapshotHeight, "first"), "a1", "a2")
		announce(manifests, cidManifest(testSnapshotHeight, "second"), "b1", "b2", "b3")
		d.failedAnnouncers = map[peer.ID]struct{}{"a1": {}, "a2": {}, "b1": {}, "b2": {}, "b3": {}}

		cid, _ := chosenCid(t, d, manifests)
		require.Equal(t, "second", cid)
	})

	t.Run("a tie is broken at random", func(t *testing.T) {
		d := newManifestTestDownloader()
		manifests := map[peer.ID]*snapshot.Manifest{}
		announce(manifests, cidManifest(testSnapshotHeight, "first"), "a1")
		announce(manifests, cidManifest(testSnapshotHeight, "second"), "b1")

		seen := map[string]bool{}
		for i := 0; i < 100; i++ {
			cid, _ := chosenCid(t, d, manifests)
			seen[cid] = true
		}
		require.Equal(t, map[string]bool{"first": true, "second": true}, seen)
	})

	t.Run("a manifest above the top is skipped", func(t *testing.T) {
		d := newManifestTestDownloader()
		manifests := map[peer.ID]*snapshot.Manifest{}
		announce(manifests, cidManifest(testSnapshotHeight, "served"), "k1")
		announce(manifests, cidManifest(d.top+1, "above"), "m1")

		cid, _ := chosenCid(t, d, manifests)
		require.Equal(t, "served", cid)

		d.top++
		cid, _ = chosenCid(t, d, manifests)
		require.Equal(t, "above", cid)
	})

	t.Run("an invalid CID is skipped", func(t *testing.T) {
		d := newManifestTestDownloader()
		d.sm.AddInvalidManifest([]byte("invalid"))
		manifests := map[peer.ID]*snapshot.Manifest{}
		announce(manifests, cidManifest(testSnapshotHeight, "invalid"), "m1", "m2", "m3")
		announce(manifests, cidManifest(testSnapshotHeight, "served"), "k1")

		cid, _ := chosenCid(t, d, manifests)
		require.Equal(t, "served", cid)
	})
}

func TestDownloaderAfterFailedSnapshot(t *testing.T) {
	manifest := cidManifest(testSnapshotHeight, "x")
	announcers := []peer.ID{"m1", "m2"}

	t.Run("no Snapshot flag at the height: given up at once", func(t *testing.T) {
		d := newManifestTestDownloader()
		d.afterFailedPass(&fastSync{manifest: manifest, announcers: announcers, snapshotFailure: noSnapshotAtHeight})
		require.True(t, d.fullSyncNext)
		require.Equal(t, MaxSnapshotHeightFailures, d.snapshotFailures[testSnapshotHeight])
		require.Equal(t, map[peer.ID]struct{}{"m1": {}, "m2": {}}, d.failedAnnouncers)
	})

	// The headers stopped below the manifest: the snapshot was not tried, peers serve the headers.
	t.Run("snapshot not tried: nothing counted", func(t *testing.T) {
		d := newManifestTestDownloader()
		d.afterFailedPass(&fastSync{manifest: manifest, announcers: announcers})
		require.True(t, d.fullSyncNext)
		require.Empty(t, d.snapshotFailures)
		require.Empty(t, d.failedAnnouncers)
	})
}

// servingIpfs serves data for any CID; nil data is a snapshot nobody serves.
type servingIpfs struct {
	ipfs.Proxy
	data []byte
}

func (s *servingIpfs) LoadTo(_ []byte, to io.Writer, _ context.Context, onLoading func(size, loaded int64)) error {
	if s.data == nil {
		return errors.New("not served")
	}
	onLoading(int64(len(s.data)), int64(len(s.data)))
	_, err := to.Write(s.data)
	return err
}

func snapshotHeader(height uint64, flags types.BlockFlag) *types.Header {
	return &types.Header{ProposedHeader: &types.ProposedHeader{Height: height, Flags: flags}}
}

func TestFastSyncSnapshotFailures(t *testing.T) {
	t.Run("no Snapshot flag at the manifest's height", func(t *testing.T) {
		// No IPFS and no data dir: a download would panic.
		sm := state.NewSnapshotManager(db.NewMemDB(), nil, eventbus.New(), nil, nil)
		fs := &fastSync{
			log:      log.New(),
			chain:    &blockchain.Blockchain{PreliminaryHead: snapshotHeader(testSnapshotHeight, types.IdentityUpdate)},
			manifest: cidManifest(testSnapshotHeight, "x"),
			sm:       sm,
		}
		require.Error(t, fs.postConsuming())
		require.Equal(t, noSnapshotAtHeight, fs.snapshotFailure)
		require.True(t, sm.IsInvalidManifest([]byte("x")))
	})

	t.Run("snapshot not served", func(t *testing.T) {
		sm := state.NewSnapshotManager(db.NewMemDB(), nil, eventbus.New(), &servingIpfs{}, &config.Config{DataDir: t.TempDir()})
		fs := &fastSync{
			log:      log.New(),
			chain:    &blockchain.Blockchain{PreliminaryHead: snapshotHeader(testSnapshotHeight, types.Snapshot)},
			manifest: cidManifest(testSnapshotHeight, "x"),
			sm:       sm,
		}
		require.Error(t, fs.postConsuming())
		require.Equal(t, snapshotFailed, fs.snapshotFailure)
		require.Equal(t, byte(1), sm.ManifestTimeouts([]byte("x")))
		require.False(t, sm.IsInvalidManifest([]byte("x")))
	})

	t.Run("snapshot does not match the header", func(t *testing.T) {
		chain, appState, _, _ := blockchain.NewTestBlockchain(false, nil)
		sm := state.NewSnapshotManager(db.NewMemDB(), nil, eventbus.New(), &servingIpfs{data: []byte("not a snapshot")}, &config.Config{DataDir: t.TempDir()})
		chain.PreliminaryHead = snapshotHeader(testSnapshotHeight, types.Snapshot)
		fs := &fastSync{
			log:      log.New(),
			chain:    chain.Blockchain,
			appState: appState,
			manifest: cidManifest(testSnapshotHeight, "x"),
			sm:       sm,
		}
		require.Error(t, fs.postConsuming())
		require.Equal(t, snapshotFailed, fs.snapshotFailure)
		require.True(t, sm.IsInvalidManifest([]byte("x")))
	})
}

package protocol

import (
	"testing"
	"time"

	mapset "github.com/deckarep/golang-set"
	"github.com/idena-network/idena-go/blockchain"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/log"
	"github.com/stretchr/testify/require"
)

type noLoadedFork struct{}

func (noLoadedFork) HasLoadedFork() bool { return false }

func staleTestConfig() *config.Config {
	return &config.Config{
		Sync: &config.SyncConfig{
			FastSync:      true,
			ForceFullSync: 100,
			StaleHeadAge:  config.DefaultStaleHeadAge,
			StaleHeadWait: config.DefaultStaleHeadWait,
		},
		Consensus: &config.ConsensusConf{},
	}
}

func headAt(height uint64, at time.Time) *types.Header {
	return &types.Header{ProposedHeader: &types.ProposedHeader{Height: height, Time: at.Unix()}}
}

func TestHeadLooksStale(t *testing.T) {
	now := time.Date(2026, 10, 12, 13, 22, 40, 0, time.UTC)
	on := staleTestConfig()
	automine := staleTestConfig()
	automine.Consensus.Automine = true
	waitOff := staleTestConfig()
	waitOff.Sync.StaleHeadWait = 0
	ageOff := staleTestConfig()
	ageOff.Sync.StaleHeadAge = 0
	// The intermediate genesis of mainnet, a new node's head.
	genesis := headAt(4871138, time.Date(2022, 2, 11, 0, 0, 0, 0, time.UTC))
	recent := headAt(11428700, now.Add(-time.Minute))

	for _, c := range []struct {
		name          string
		cfg           *config.Config
		head, headers *types.Header
		want          bool
	}{
		{"recent head", on, recent, nil, false},
		{"head of 2022", on, genesis, nil, true},
		{"head just under the age", on, headAt(100, now.Add(-23*time.Hour)), nil, false},
		{"head just over the age", on, headAt(100, now.Add(-25*time.Hour)), nil, true},
		{"fast sync headers above a recent head", on, recent, headersAt(11428800), true},
		{"fast sync headers at the head", on, recent, headersAt(11428700), false},
		{"automine", automine, genesis, headersAt(5021200), false},
		{"wait off", waitOff, genesis, headersAt(5021200), false},
		{"age off, no headers", ageOff, genesis, nil, false},
		{"age off, headers above", ageOff, genesis, headersAt(5021200), true},
		{"no sync config", &config.Config{}, genesis, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, headLooksStale(c.cfg, c.head, c.headers, now))
		})
	}
}

func TestWaitForPeerAheadGivesUpAfterTheWait(t *testing.T) {
	d := &Downloader{cfg: staleTestConfig(), log: log.New()}
	start := time.Date(2026, 10, 12, 13, 22, 40, 0, time.UTC)
	stale := headAt(4871138, time.Date(2022, 2, 11, 0, 0, 0, 0, time.UTC))

	require.True(t, d.waitForPeerAhead(stale, nil, start))
	require.True(t, d.waitForPeerAhead(stale, nil, start.Add(29*time.Minute)))
	// After the wait the head is taken as the top (a network that stopped starts again), and stays so.
	require.False(t, d.waitForPeerAhead(stale, nil, start.Add(30*time.Minute)))
	require.False(t, d.waitForPeerAhead(stale, nil, start.Add(31*time.Minute)))

	// A head that no longer looks stale ends the wait: a later stale head waits again.
	fresh := headAt(11428700, start.Add(32*time.Minute))
	require.False(t, d.waitForPeerAhead(fresh, nil, start.Add(32*time.Minute)))
	require.Equal(t, staleHeadWait{}, d.staleWait)
	require.True(t, d.waitForPeerAhead(stale, nil, start.Add(33*time.Minute)))
}

// The Galaxy, 2026-10-12: a new node restarted in fast sync (its headers up to 5,021,200, its chain at the
// intermediate genesis) and its one peer was a node in fast sync too, which announces its chain head: the same
// height. The node took its head as the top and spent its rounds on a block of 2022; three peers above it that
// connected later were never synced from.
func TestSyncBlockchainWaitsForAPeerAboveAStaleHead(t *testing.T) {
	for _, c := range []struct {
		name string
		wait time.Duration
		want error
	}{
		{"waits for a peer above it", config.DefaultStaleHeadWait, ErrNoPeerAhead},
		{"wait off: synchronized, as before", 0, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			chain, _, _, _ := blockchain.NewTestBlockchain(false, nil)
			head := chain.Head.Height()
			chain.PreliminaryHead = headersAt(head + 150000)

			pm := &IdenaGossipHandler{peers: newPeerSet()}
			syncing := newHeightTestPeer("a node in fast sync", head)
			syncing.queuedRequests = make(chan *request, 1)
			syncing.finished = make(chan struct{})
			require.NoError(t, pm.peers.Register(syncing))

			cfg := staleTestConfig()
			cfg.Sync.StaleHeadWait = c.wait
			d := &Downloader{
				cfg:                  cfg,
				log:                  log.New(),
				chain:                chain.Blockchain,
				pm:                   pm,
				potentialForkedPeers: mapset.NewSet(),
			}
			require.Equal(t, c.want, d.SyncBlockchain(noLoadedFork{}))
		})
	}
}

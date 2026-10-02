package protocol

import (
	"testing"

	"github.com/idena-network/idena-go/blockchain"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/state/snapshot"
	"github.com/idena-network/idena-go/core/upgrade"
	"github.com/idena-network/idena-go/log"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

// A fast sync that gives up must leave the node on the consensus version of its own chain: the next
// attempt or a full sync applies that chain's blocks, and under the version of the downloaded headers
// they get other state roots than on the network. Seen on mainnet: after a snapshot could not be
// downloaded, a new node rejected block 4,871,248 (2022) because it applied it under consensus v12.
func TestFastSyncRestoresConsensusConfigWhenItGivesUp(t *testing.T) {
	chain, appState, _, _ := blockchain.NewTestBlockchain(false, nil)
	cfg := chain.Config()
	v10 := *config.ConsensusVersions[config.ConsensusV10]
	cfg.Consensus = &v10
	fs := &fastSync{
		log:      log.New(),
		chain:    chain.Blockchain,
		appState: appState,
		upgrader: upgrade.NewUpgrader(cfg, appState, db.NewMemDB()),
		manifest: &snapshot.Manifest{Height: chain.Head.Height() + 1000},
	}

	// The downloaded headers pass two upgrades: the config follows them while they are applied.
	for _, ver := range []config.ConsensusVerson{config.ConsensusV11, config.ConsensusV12} {
		fs.tryUpgradeConsensus(&types.Header{ProposedHeader: &types.ProposedHeader{Upgrade: uint32(ver)}})
	}
	require.Equal(t, config.ConsensusV12, chain.Config().Consensus.Version)

	// The fast sync gives up before switching to the downloaded chain (here its headers stop below the
	// manifest; a snapshot that cannot be downloaded fails the same way).
	chain.PreliminaryHead = chain.Head
	require.Error(t, fs.postConsuming())

	require.Equal(t, config.ConsensusV10, chain.Config().Consensus.Version)
}

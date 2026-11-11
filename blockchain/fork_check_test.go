package blockchain

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/idena-network/idena-go/blockchain/attachments"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/blockchain/validation"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/core/mempool"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/core/upgrade"
	"github.com/idena-network/idena-go/crypto"
	"github.com/idena-network/idena-go/ipfs"
	"github.com/idena-network/idena-go/keystore"
	"github.com/idena-network/idena-go/secstore"
	"github.com/idena-network/idena-go/stats/collector"
	"github.com/idena-network/idena-go/subscriptions"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

func forkCheckConfig(god, other common.Address) *config.Config {
	conf := GetDefaultConsensusConfig()
	conf.Automine = true
	conf.StatusSwitchRange = 5
	return &config.Config{
		Network:   0x99,
		Consensus: conf,
		GenesisConf: &config.GenesisConf{
			Alloc: map[common.Address]config.GenesisAllocation{
				god:   {Balance: new(big.Int).Mul(big.NewInt(100), common.DnaBase)},
				other: {State: uint8(state.Human), Stake: new(big.Int).Mul(big.NewInt(10), common.DnaBase), Balance: new(big.Int).Mul(big.NewInt(100), common.DnaBase)},
			},
			GodAddress:        god,
			FirstCeremonyTime: 4070908800, // 2099-01-01
		},
		Validation:       &config.ValidationConfig{},
		Blockchain:       &config.BlockchainConfig{},
		OfflineDetection: config.GetDefaultOfflineDetectionConfig(),
		Mempool:          config.GetDefaultMempoolConfig(),
	}
}

// forkCopyWithKey returns a node on a copy of chain's database that proposes and votes with key.
func forkCopyWithKey(chain *TestBlockchain, cfg *config.Config, key *ecdsa.PrivateKey) (*TestBlockchain, *appstate.AppState) {
	memDb := db.NewMemDB()
	bus := eventbus.New()
	it, _ := chain.db.Iterator(nil, nil)
	defer it.Close()
	for ; it.Valid(); it.Next() {
		memDb.Set(it.Key(), it.Value())
	}
	secStore := secstore.NewSecStore()
	secStore.AddKey(crypto.FromECDSA(key))
	appState, _ := appstate.NewAppState(memDb, bus)
	txPool := mempool.NewTxPool(appState, bus, cfg, collector.NewStatsCollector())
	offline := NewOfflineDetector(cfg, memDb, appState, secStore, bus)
	keyStore := keystore.NewKeyStore("./testdata", keystore.StandardScryptN, keystore.StandardScryptP)
	subManager, _ := subscriptions.NewManager("./testdata2")
	upgrader := upgrade.NewUpgrader(cfg, appState, memDb)
	copied := NewBlockchain(cfg, memDb, txPool, appState, ipfs.NewMemoryIpfsProxy(), secStore, bus, offline, keyStore, subManager, upgrader)
	copied.InitializeChain()
	appState.Initialize(copied.Head.Height())
	txPool.Initialize(copied.Head, secStore.GetAddress(), false)
	return &TestBlockchain{memDb, copied, appState.State.GetNonce(secStore.GetAddress())}, appState
}

// A fork whose blocks cross an online switch (an IdentityUpdate block) is checked with the validators that switch
// brings: the first block proposed by the identity it made online is accepted. The fork check used to commit each
// block a second time, with nothing left to diff, so its validators never changed and it refused that block
// ("proposer is not identity").
func TestForkAcrossOnlineSwitchAccepted(t *testing.T) {
	godKey, _ := crypto.GenerateKey()
	otherKey, _ := crypto.GenerateKey()
	god, other := crypto.PubkeyToAddress(godKey.PublicKey), crypto.PubkeyToAddress(otherKey.PublicKey)
	node, _ := NewCustomTestBlockchainWithConfig(1, 0, godKey, forkCheckConfig(god, other))
	defer node.SecStore().Destroy()
	commonHeight := node.Head.Height()

	// The fork: the god address proposes until the switch block that makes the other identity online, then that
	// identity proposes.
	fork, forkState := forkCopyWithKey(node, forkCheckConfig(god, other), godKey)
	payload, _ := (&attachments.OnlineStatusAttachment{Online: true}).ToBytes()
	tx := BuildTx(forkState, other, nil, types.OnlineStatusTx, decimal.Zero, decimal.New(20, 0), decimal.Zero,
		forkState.State.GetNonce(other)+1, 0, payload)
	tx, err := types.SignTx(tx, otherKey)
	require.NoError(t, err)
	require.NoError(t, fork.txpool.AddExternalTxs(validation.InboundTx, tx))
	for !fork.Head.Flags().HasFlag(types.IdentityUpdate) {
		require.Less(t, fork.Head.Height(), uint64(20), "no online switch")
		fork.GenerateBlocks(1, 0)
	}
	switchHeight := fork.Head.Height()
	require.True(t, forkState.ValidatorsCache.IsOnlineIdentity(other))

	proposer, _ := forkCopyWithKey(fork, forkCheckConfig(god, other), otherKey)
	proposer.GenerateBlocks(2, 0)
	require.Equal(t, other, proposer.Head.Coinbase())

	var bundles []types.BlockBundle
	for h := commonHeight + 1; h <= proposer.Head.Height(); h++ {
		// Each test node keeps the bodies of its own blocks (an in-memory IPFS each).
		maker := fork
		if h > switchHeight {
			maker = proposer
		}
		block := maker.GetBlockByHeight(h)
		require.NotNil(t, block, "height %v", h)
		cert := maker.GetCertificate(block.Hash())
		if cert == nil || !block.Header.Flags().HasFlag(types.IdentityUpdate) {
			cert = &types.BlockCert{}
		}
		bundles = append(bundles, types.BlockBundle{Block: block, Cert: cert})
	}
	require.Greater(t, proposer.Head.Height(), switchHeight)
	require.NoError(t, node.ValidateSubChain(commonHeight, bundles))
}

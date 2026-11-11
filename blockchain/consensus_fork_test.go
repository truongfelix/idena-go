package blockchain

import (
	"math/big"
	"testing"
	"time"

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
	"github.com/idena-network/idena-go/stats/collector"
	"github.com/idena-network/idena-go/subscriptions"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

var forkTestBridge = common.HexToAddress("0x98D16d7021930b788135dD834983394fF2De9869")

// forkTestConfig is a v12 config whose node does not vote for v13 (no upgrade until a test allows it).
func forkTestConfig(coinbase common.Address) *config.Config {
	conf := GetDefaultConsensusConfig()
	conf.Automine = true
	conf.SnapshotRange = 5
	conf.MigrationTimeout = 0
	conf.UpgradeIntervalBeforeValidation = 200 * 365 * 24 * time.Hour
	return &config.Config{
		Network:   0x99,
		Consensus: conf,
		GenesisConf: &config.GenesisConf{
			Alloc: map[common.Address]config.GenesisAllocation{
				coinbase: {Balance: new(big.Int).Mul(big.NewInt(100), common.DnaBase)},
			},
			GodAddress:        coinbase,
			FirstCeremonyTime: 4070908800, // 2099-01-01
		},
		Validation:       &config.ValidationConfig{},
		Blockchain:       &config.BlockchainConfig{},
		OfflineDetection: config.GetDefaultOfflineDetectionConfig(),
		Mempool:          config.GetDefaultMempoolConfig(),
	}
}

// forkCopy returns a second node on a copy of chain's database, with its own config: the two share the blocks so
// far and can go separate ways.
func forkCopy(chain *TestBlockchain, cfg *config.Config) (*TestBlockchain, *appstate.AppState) {
	memDb := db.NewMemDB()
	bus := eventbus.New()
	it, _ := chain.db.Iterator(nil, nil)
	defer it.Close()
	for ; it.Valid(); it.Next() {
		memDb.Set(it.Key(), it.Value())
	}
	appState, _ := appstate.NewAppState(memDb, bus)
	txPool := mempool.NewTxPool(appState, bus, cfg, collector.NewStatsCollector())
	offline := NewOfflineDetector(cfg, memDb, appState, chain.secStore, bus)
	keyStore := keystore.NewKeyStore("./testdata", keystore.StandardScryptN, keystore.StandardScryptP)
	subManager, _ := subscriptions.NewManager("./testdata2")
	upgrader := upgrade.NewUpgrader(cfg, appState, memDb)
	copied := NewBlockchain(cfg, memDb, txPool, appState, ipfs.NewMemoryIpfsProxy(), chain.secStore, bus, offline, keyStore, subManager, upgrader)
	copied.InitializeChain()
	appState.Initialize(copied.Head.Height())
	txPool.Initialize(copied.Head, chain.secStore.GetAddress(), false)
	return &TestBlockchain{memDb, copied, appState.State.GetNonce(chain.secStore.GetAddress())}, appState
}

func forkBundles(t *testing.T, chain *TestBlockchain, from, to uint64) []types.BlockBundle {
	var bundles []types.BlockBundle
	for h := from; h <= to; h++ {
		block := chain.GetBlockByHeight(h)
		require.NotNil(t, block, "height %v", h)
		bundles = append(bundles, types.BlockBundle{Block: block, Cert: &types.BlockCert{}})
	}
	return bundles
}

// Two nodes share their first blocks, then one applies the v13 upgrade block while the other stays on v12, where a
// transfer to the bridge wallet is valid. Each node accepts the other's branch: its blocks are checked under the rules
// of their own height on that branch. A reset of the upgraded node below its upgrade block brings back the v12 rules,
// the stored version and upgrade height, and the v12 branch then applies on it.
func TestForkAcrossConsensusUpgrade(t *testing.T) {
	key, _ := crypto.GenerateKey()
	coinbase := crypto.PubkeyToAddress(key.PublicKey)
	upgraded, _ := NewCustomTestBlockchainWithConfig(3, 0, key, forkTestConfig(coinbase))
	defer upgraded.SecStore().Destroy()
	staying, stayingState := forkCopy(upgraded, forkTestConfig(coinbase))
	commonHeight := upgraded.Head.Height()
	require.Equal(t, commonHeight, staying.Head.Height())

	// The node's process-wide checks, as the node sets them, following whichever node acts.
	current := staying.config
	validation.SetAppConfig(current)
	state.SetFrozenAddressCheck(func(addr common.Address) bool { return current.Consensus.IsFrozenAddress(addr) })
	defer validation.SetAppConfig(nil)
	defer state.SetFrozenAddressCheck(nil)

	// The v12 branch: a transfer to the bridge wallet, then more blocks than the other branch.
	tx := BuildTx(stayingState, coinbase, &forkTestBridge, types.SendTx, decimal.New(1, 0), decimal.New(20, 0), decimal.Zero,
		stayingState.State.GetNonce(coinbase)+1, 0, nil)
	tx, err := staying.secStore.SignTx(tx)
	require.NoError(t, err)
	require.NoError(t, staying.AddTx(tx))
	staying.GenerateBlocks(1, 0)
	require.Positive(t, stayingState.State.GetBalance(forkTestBridge).Sign(), "the v12 branch holds no transfer to the wallet")
	staying.GenerateBlocks(9, 0)

	// The other branch upgrades at its first block.
	current = upgraded.config
	validation.SetAppConfig(current)
	upgraded.config.Consensus.UpgradeIntervalBeforeValidation = 0
	upgraded.GenerateBlocks(1, 0)
	upgradeHeight := upgraded.Head.Height()
	require.Equal(t, uint32(config.ConsensusV13), upgraded.Head.ProposedHeader.Upgrade)
	require.Equal(t, config.ConsensusV13, upgraded.config.Consensus.Version)
	require.Equal(t, commonHeight+1, upgradeHeight)
	require.Equal(t, uint32(config.ConsensusV13), upgraded.repo.ReadConsensusVersion())
	require.Equal(t, upgradeHeight, upgraded.repo.ReadConsensusUpgradeHeight())
	require.Equal(t, config.ConsensusV12, upgraded.config.ConsensusBeforeUpgrade.Version)
	upgraded.GenerateBlocks(7, 0)
	listBlocks := 0
	for h := upgradeHeight + 1; h <= upgraded.Head.Height(); h++ {
		if len(upgraded.GetBlockHeaderByHeight(h).IdentityListCid()) > 0 {
			listBlocks++
		}
	}
	require.Positive(t, listBlocks, "no v13 block on the upgraded branch carries the identity list")

	// The upgraded node checks the v12 branch under v12 rules.
	require.NoError(t, upgraded.ValidateSubChain(commonHeight, forkBundles(t, staying, commonHeight+1, staying.Head.Height())))
	// Its own running rules are untouched.
	require.Equal(t, config.ConsensusV13, upgraded.config.Consensus.Version)

	// The v12 node checks the upgraded branch under v13 rules after its upgrade block.
	current = staying.config
	validation.SetAppConfig(current)
	require.NoError(t, staying.ValidateSubChain(commonHeight, forkBundles(t, upgraded, commonHeight+1, upgraded.Head.Height())))
	require.Equal(t, config.ConsensusV12, staying.config.Consensus.Version)

	// A reset of the upgraded node below its upgrade block, then the v12 branch applied on it.
	current = upgraded.config
	validation.SetAppConfig(current)
	stayingBlocks := forkBundles(t, staying, commonHeight+1, staying.Head.Height())
	_, err = upgraded.ResetTo(commonHeight)
	require.NoError(t, err)
	require.Equal(t, config.ConsensusV12, upgraded.config.Consensus.Version)
	require.False(t, upgraded.config.Consensus.EnableUpgrade13)
	require.False(t, upgraded.config.Consensus.IsFrozenAddress(forkTestBridge))
	require.Nil(t, upgraded.config.ConsensusBeforeUpgrade)
	require.Equal(t, uint32(config.ConsensusV12), upgraded.repo.ReadConsensusVersion())
	require.Zero(t, upgraded.repo.ReadConsensusUpgradeHeight())
	require.Equal(t, commonHeight, upgraded.Head.Height())
	upgraded.config.Consensus.UpgradeIntervalBeforeValidation = 200 * 365 * 24 * time.Hour
	for _, b := range stayingBlocks {
		require.NoError(t, upgraded.AddBlock(b.Block, nil, collector.NewStatsCollector()), "height %v", b.Block.Height())
	}
	require.Equal(t, staying.Head.Hash(), upgraded.Head.Hash())
	require.Positive(t, upgraded.appState.State.GetBalance(forkTestBridge).Sign())
}

// A reset that stays at or above the upgrade block keeps the new rules.
func TestResetAboveConsensusUpgradeKeepsRules(t *testing.T) {
	key, _ := crypto.GenerateKey()
	coinbase := crypto.PubkeyToAddress(key.PublicKey)
	chain, _ := NewCustomTestBlockchainWithConfig(2, 0, key, forkTestConfig(coinbase))
	defer chain.SecStore().Destroy()
	chain.config.Consensus.UpgradeIntervalBeforeValidation = 0
	chain.GenerateBlocks(1, 0)
	upgradeHeight := chain.Head.Height()
	require.Equal(t, config.ConsensusV13, chain.config.Consensus.Version)
	chain.GenerateBlocks(3, 0)

	_, err := chain.ResetTo(upgradeHeight)
	require.NoError(t, err)
	require.Equal(t, config.ConsensusV13, chain.config.Consensus.Version)
	require.Equal(t, uint32(config.ConsensusV13), chain.repo.ReadConsensusVersion())
	require.Equal(t, upgradeHeight, chain.repo.ReadConsensusUpgradeHeight())
}

// A reset below an upgrade whose previous rules the node did not keep (a config built without the stored version)
// goes back to the built-in rules of the version below, with the node's own setting kept.
func TestResetBelowConsensusUpgradeWithoutKeptRules(t *testing.T) {
	key, _ := crypto.GenerateKey()
	coinbase := crypto.PubkeyToAddress(key.PublicKey)
	chain, _ := NewCustomTestBlockchainWithConfig(2, 0, key, forkTestConfig(coinbase))
	defer chain.SecStore().Destroy()
	chain.config.Consensus.UpgradeIntervalBeforeValidation = 0
	chain.GenerateBlocks(3, 0)
	require.Equal(t, config.ConsensusV13, chain.config.Consensus.Version)
	chain.config.ConsensusBeforeUpgrade = nil

	_, err := chain.ResetTo(1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), chain.Head.Height())
	require.Equal(t, config.ConsensusV12, chain.config.Consensus.Version)
	require.False(t, chain.config.Consensus.EnableUpgrade13)
	require.True(t, chain.config.Consensus.Automine, "the node's own setting was lost")
	require.Equal(t, uint32(config.ConsensusV12), chain.repo.ReadConsensusVersion())
}

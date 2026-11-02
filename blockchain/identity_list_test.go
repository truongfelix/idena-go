package blockchain

import (
	"math/big"
	"testing"
	"time"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/core/identitylist"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/crypto"
	"github.com/idena-network/idena-go/stats/collector"
	"github.com/stretchr/testify/require"
)

func identityListTestChain(t *testing.T, v13 bool) (*TestBlockchain, *appstate.AppState) {
	conf := GetDefaultConsensusConfig()
	conf.Automine = true
	conf.EnableUpgrade13 = v13
	conf.SnapshotRange = 5
	// With nobody online the node would vote itself to the next version, v13, at once: no vote on these chains.
	conf.UpgradeIntervalBeforeValidation = 200 * 365 * 24 * time.Hour
	key, _ := crypto.GenerateKey()
	coinbase := crypto.PubkeyToAddress(key.PublicKey)
	alloc := map[common.Address]config.GenesisAllocation{
		coinbase: {Balance: new(big.Int).Mul(big.NewInt(100), common.DnaBase)},
	}
	for i := 1; i <= 3; i++ {
		alloc[common.Address{byte(i)}] = config.GenesisAllocation{
			State: uint8(state.Human), Stake: big.NewInt(int64(i) * 1000), Balance: big.NewInt(1),
		}
	}
	cfg := &config.Config{
		Network:   0x99,
		Consensus: conf,
		GenesisConf: &config.GenesisConf{
			Alloc:             alloc,
			GodAddress:        coinbase,
			FirstCeremonyTime: 4070908800, // 2099-01-01
		},
		Validation: &config.ValidationConfig{},
		Blockchain: &config.BlockchainConfig{},
	}
	return NewCustomTestBlockchainWithConfig(0, 0, key, cfg)
}

func proposeNext(t *testing.T, chain *TestBlockchain) *types.Block {
	block := chain.ProposeBlock([]byte{}).Block
	block.Header.ProposedHeader.Time = chain.Head.Time() + 20
	return block
}

// checkListBlocks adds count blocks (empty ones when empty is set) and checks that each snapshot block, and only
// such a block, carries the CID of the list built from the state after it.
func checkListBlocks(t *testing.T, chain *TestBlockchain, appState *appstate.AppState, count int, empty bool) int {
	listBlocks := 0
	for i := 0; i < count; i++ {
		if empty {
			chain.GenerateEmptyBlocks(1)
		} else {
			chain.GenerateBlocks(1, 0)
		}
		head := chain.Head
		require.Equal(t, empty, head.EmptyBlockHeader != nil)
		if !head.Flags().HasFlag(types.Snapshot) {
			require.Empty(t, head.IdentityListCid(), "height %v", head.Height())
			continue
		}
		listBlocks++
		expected, err := identitylist.FromState(head.Height(), appState.State)
		require.NoError(t, err)
		require.GreaterOrEqual(t, expected.Count, 3)
		require.Equal(t, expected.IndexCid.Bytes(), head.IdentityListCid(), "height %v", head.Height())
	}
	return listBlocks
}

func TestIdentityListInProposedBlocks(t *testing.T) {
	chain, appState := identityListTestChain(t, true)
	require.GreaterOrEqual(t, checkListBlocks(t, chain, appState, 12, false), 2)
}

func TestIdentityListInEmptyBlocks(t *testing.T) {
	chain, appState := identityListTestChain(t, true)
	require.GreaterOrEqual(t, checkListBlocks(t, chain, appState, 12, true), 2)
}

func TestNoIdentityListBeforeV13(t *testing.T) {
	chain, appState := identityListTestChain(t, false)
	snapshots := 0
	for i := 0; i < 12; i++ {
		chain.GenerateBlocks(1, 0)
		if chain.Head.Flags().HasFlag(types.Snapshot) {
			snapshots++
		}
		require.Empty(t, chain.Head.IdentityListCid())
	}
	require.False(t, chain.config.Consensus.EnableUpgrade13)
	require.GreaterOrEqual(t, snapshots, 2)

	block := proposeNext(t, chain)
	block.Header.ProposedHeader.IdentityListCid = []byte{1, 2, 3}
	_, err := chain.ValidateBlock(block, nil, collector.NewStatsCollector(), false)
	require.ErrorContains(t, err, "invalid identity list cid")
	_ = appState
}

func TestWrongIdentityListCidRefused(t *testing.T) {
	chain, _ := identityListTestChain(t, true)
	// Up to the block before a snapshot block.
	for {
		next := proposeNext(t, chain)
		if next.Header.Flags().HasFlag(types.Snapshot) {
			break
		}
		require.NoError(t, chain.AddBlock(next, nil, collector.NewStatsCollector()))
		chain.addCert(next)
	}

	block := proposeNext(t, chain)
	require.NotEmpty(t, block.IdentityListCid())
	_, err := chain.ValidateBlock(block, nil, collector.NewStatsCollector(), false)
	require.NoError(t, err)

	wrong := append([]byte{}, block.IdentityListCid()...)
	wrong[len(wrong)-1] ^= 1
	block.Header.ProposedHeader.IdentityListCid = wrong
	_, err = chain.ValidateBlock(block, nil, collector.NewStatsCollector(), false)
	require.ErrorContains(t, err, "invalid identity list cid")

	block.Header.ProposedHeader.IdentityListCid = nil
	_, err = chain.ValidateBlock(block, nil, collector.NewStatsCollector(), false)
	require.ErrorContains(t, err, "invalid identity list cid")

	empty := chain.GenerateEmptyBlock()
	require.NotEmpty(t, empty.IdentityListCid())
	empty.Header.EmptyBlockHeader.IdentityListCid = wrong
	_, err = chain.ValidateBlock(empty, nil, collector.NewStatsCollector(), false)
	require.ErrorContains(t, err, "empty blocks' hashes mismatch")
}

func TestIdentityListCidOutsideListBlocksRefused(t *testing.T) {
	chain, _ := identityListTestChain(t, true)
	block := proposeNext(t, chain)
	require.False(t, block.Header.Flags().HasFlag(types.Snapshot))
	block.Header.ProposedHeader.IdentityListCid = []byte{1}
	_, err := chain.ValidateBlock(block, nil, collector.NewStatsCollector(), false)
	require.ErrorContains(t, err, "invalid identity list cid")
}

// A header without the field hashes as it did before the field existed: protobuf leaves an empty field out.
func TestEmptyIdentityListCidKeepsHeaderBytes(t *testing.T) {
	chain, _ := identityListTestChain(t, true)
	block := proposeNext(t, chain)
	require.Empty(t, block.IdentityListCid())
	b, err := block.Header.ToBytes()
	require.NoError(t, err)
	block.Header.ProposedHeader.IdentityListCid = []byte{}
	b2, err := block.Header.ToBytes()
	require.NoError(t, err)
	require.Equal(t, b, b2)
	block.Header.ProposedHeader.IdentityListCid = []byte{7}
	b3, err := block.Header.ToBytes()
	require.NoError(t, err)
	require.NotEqual(t, b, b3)
	var decoded types.Header
	require.NoError(t, decoded.FromBytes(b3))
	require.Equal(t, []byte{7}, decoded.IdentityListCid())
}

func TestReadIdentityListBack(t *testing.T) {
	chain, appState := identityListTestChain(t, true)
	var listHeights []uint64
	var lastRows []identitylist.Row
	for len(listHeights) < 3 {
		chain.GenerateBlocks(1, 0)
		if len(chain.Head.IdentityListCid()) > 0 {
			listHeights = append(listHeights, chain.Head.Height())
			expected, err := identitylist.FromState(chain.Head.Height(), appState.State)
			require.NoError(t, err)
			_, lastRows = decodeList(t, expected)
		}
	}
	c, index, rows, err := chain.IdentityList(0)
	require.NoError(t, err)
	require.Equal(t, listHeights[2], index.Height)
	require.Equal(t, chain.GetBlockHeaderByHeight(listHeights[2]).IdentityListCid(), c.Bytes())
	require.Equal(t, lastRows, rows)

	_, index, _, err = chain.IdentityList(listHeights[0])
	require.NoError(t, err)
	require.Equal(t, listHeights[0], index.Height)

	_, _, _, err = chain.IdentityList(listHeights[0] + 1)
	require.ErrorContains(t, err, "carries no identity list")
	_, _, _, err = chain.IdentityList(chain.Head.Height() + 10)
	require.ErrorContains(t, err, "no block at height")
}

func decodeList(t *testing.T, l *identitylist.List) (*identitylist.Index, []identitylist.Row) {
	index, err := identitylist.DecodeIndex(l.Index)
	require.NoError(t, err)
	var rows []identitylist.Row
	for _, p := range l.Pages {
		r, err := identitylist.DecodePage(p)
		require.NoError(t, err)
		rows = append(rows, r...)
	}
	return index, rows
}

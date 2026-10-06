package api

import (
	"testing"

	"github.com/idena-network/idena-go/blockchain"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/core/mempool"
	"github.com/idena-network/idena-go/crypto"
	"github.com/idena-network/idena-go/stats/collector"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// Only contract transactions have a receipt, once they are in a block: bcn_txReceipt answers null for the others.
func TestTxReceiptWithoutReceipt(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	chain, appState := blockchain.NewCustomTestBlockchain(3, 0, key)
	chain.GenerateBlocks(1, 1)
	inBlock := chain.GetBlock(chain.Head.Hash()).Body.Transactions[0]
	sender := crypto.PubkeyToAddress(key.PublicKey)

	pool := mempool.NewTxPool(appState, eventbus.New(), chain.Config(), collector.NewStatsCollector())
	pool.Initialize(chain.Head, sender, false)
	pending, err := types.SignTx(blockchain.BuildTx(appState, sender, &sender, types.SendTx, decimal.Zero,
		decimal.New(20, 0), decimal.Zero, 0, 0, nil), key)
	require.NoError(t, err)
	require.NoError(t, pool.AddInternalTx(pending))
	require.NotNil(t, pool.GetTx(pending.Hash()))

	api := &BlockchainApi{bc: chain.Blockchain, pool: pool}
	require.Nil(t, api.TxReceipt(pending.Hash()), "a transaction in the mempool")
	require.Nil(t, api.TxReceipt(inBlock.Hash()), "a transfer in a block")
	require.Nil(t, api.TxReceipt(common.Hash{1}), "an unknown hash")
}

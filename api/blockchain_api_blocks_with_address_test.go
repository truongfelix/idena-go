package api

import (
	"testing"

	"github.com/idena-network/idena-go/blockchain"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/crypto"
	"github.com/stretchr/testify/require"
)

// A fixed key: the blocks' transaction filters, and so the results, are the same on every run.
const blocksWithAddressTestKey = "b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291"

func TestBlocksWithAddress(t *testing.T) {
	key, err := crypto.HexToECDSA(blocksWithAddressTestKey)
	require.NoError(t, err)
	chain, _ := blockchain.NewCustomTestBlockchain(3, 2, key)
	withoutTxs := chain.Head.Height()
	chain.GenerateBlocks(2, 1)
	chain.GenerateEmptyBlocks(1)
	head := chain.Head.Height()
	api := &BlockchainApi{bc: chain.Blockchain}
	sender := crypto.PubkeyToAddress(key.PublicKey)

	heights, err := api.BlocksWithAddress(BlocksWithAddressArgs{Address: sender, From: 1, To: head})
	require.NoError(t, err)
	require.Equal(t, []uint64{withoutTxs + 1, withoutTxs + 2}, heights, "the blocks with a transaction of the address")

	heights, err = api.BlocksWithAddress(BlocksWithAddressArgs{Address: sender, From: withoutTxs + 2, To: withoutTxs + 2})
	require.NoError(t, err)
	require.Equal(t, []uint64{withoutTxs + 2}, heights, "a range of one block")

	other := common.HexToAddress("0x840e092e31e9656fF15E541505039ed77585338E")
	heights, err = api.BlocksWithAddress(BlocksWithAddressArgs{Address: other, From: 1, To: head})
	require.NoError(t, err)
	require.Empty(t, heights, "an address without transactions")
}

func TestBlocksWithAddressRejectsInvalidRanges(t *testing.T) {
	key, err := crypto.HexToECDSA(blocksWithAddressTestKey)
	require.NoError(t, err)
	chain, _ := blockchain.NewCustomTestBlockchain(3, 0, key)
	head := chain.Head.Height()
	api := &BlockchainApi{bc: chain.Blockchain}
	sender := crypto.PubkeyToAddress(key.PublicKey)

	for name, args := range map[string]BlocksWithAddressArgs{
		"from zero":       {Address: sender, From: 0, To: head},
		"from after to":   {Address: sender, From: head, To: head - 1},
		"after the head":  {Address: sender, From: 1, To: head + 1},
		"range too large": {Address: sender, From: 1, To: MaxBlocksWithAddressRange + 1},
		"far after head":  {Address: sender, From: head + 1, To: head + 10},
	} {
		_, err := api.BlocksWithAddress(args)
		require.Error(t, err, name)
	}
}

package api

import (
	"math/big"
	"testing"

	"github.com/idena-network/idena-go/blockchain"
	"github.com/idena-network/idena-go/blockchain/attachments"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/hexutil"
	"github.com/idena-network/idena-go/crypto"
	"github.com/stretchr/testify/require"
)

func contractCallsTestTx(t *testing.T, nonce uint32, txType types.TxType, to *common.Address, amount *big.Int, payload []byte) *types.Transaction {
	key, err := crypto.HexToECDSA(blocksWithAddressTestKey)
	require.NoError(t, err)
	tx, err := types.SignTx(&types.Transaction{AccountNonce: nonce, Type: txType, To: to, Amount: amount, Payload: payload}, key)
	require.NoError(t, err)
	return tx
}

func contractCallPayload(t *testing.T, method string, args ...[]byte) []byte {
	payload, err := attachments.CreateCallContractAttachment(method, args...).ToBytes()
	require.NoError(t, err)
	return payload
}

func TestContractCallsOf(t *testing.T) {
	key, err := crypto.HexToECDSA(blocksWithAddressTestKey)
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	contract := common.HexToAddress("0x840e092e31e9656fF15E541505039ed77585338E")
	other := common.HexToAddress("0x18b0a55eb99AcA113f50eEBbdeAf6f96E789277f")

	post := contractCallsTestTx(t, 1, types.CallContractTx, &contract, big.NewInt(10_000_000_000_000),
		contractCallPayload(t, "makePost", []byte(`{"message":"hello"}`)))
	tip := contractCallsTestTx(t, 4, types.CallContractTx, &contract, nil,
		contractCallPayload(t, "sendTip", []byte(`{"postId":"7"}`), []byte{0x01, 0x02}))
	block := &types.Block{
		Header: &types.Header{ProposedHeader: &types.ProposedHeader{Height: 42, Time: 1_781_956_251}},
		Body: &types.Body{Transactions: []*types.Transaction{
			contractCallsTestTx(t, 0, types.SendTx, &contract, big.NewInt(1), nil),
			post,
			contractCallsTestTx(t, 2, types.CallContractTx, &other, nil, contractCallPayload(t, "makePost", []byte("{}"))),
			contractCallsTestTx(t, 3, types.CallContractTx, &contract, nil, []byte{0xff, 0xff}),
			tip,
		}},
	}

	calls := contractCallsOf(block, contract)
	require.Len(t, calls, 2, "only the calls of the contract: no send, no other contract, no payload that is not a call")

	require.Equal(t, uint64(42), calls[0].Height)
	require.Equal(t, int64(1_781_956_251), calls[0].Timestamp)
	require.Equal(t, 1, calls[0].Index, "the position of the transaction in the block")
	require.Equal(t, post.Hash(), calls[0].Hash)
	require.Equal(t, sender, calls[0].From, "the sender, recovered from the signature")
	require.Equal(t, "0.00001", calls[0].Amount.String())
	require.Equal(t, "makePost", calls[0].Method)
	require.Equal(t, []hexutil.Bytes{[]byte(`{"message":"hello"}`)}, calls[0].Args)

	require.Equal(t, 4, calls[1].Index)
	require.Equal(t, tip.Hash(), calls[1].Hash)
	require.Equal(t, "0", calls[1].Amount.String(), "a call without coins")
	require.Equal(t, "sendTip", calls[1].Method)
	require.Equal(t, []hexutil.Bytes{[]byte(`{"postId":"7"}`), {0x01, 0x02}}, calls[1].Args)

	require.Empty(t, contractCallsOf(block, sender), "an address that no transaction calls as a contract")
}

func TestContractCallsReadsTheBlocks(t *testing.T) {
	key, err := crypto.HexToECDSA(blocksWithAddressTestKey)
	require.NoError(t, err)
	chain, _ := blockchain.NewCustomTestBlockchain(3, 2, key)
	chain.GenerateBlocks(2, 1)
	head := chain.Head.Height()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	api := &BlockchainApi{bc: chain.Blockchain}

	// The sender's blocks, as BlocksWithAddress finds them, are read; their sends are no contract calls.
	heights, err := api.BlocksWithAddress(BlocksWithAddressArgs{Address: sender, From: 1, To: head})
	require.NoError(t, err)
	require.NotEmpty(t, heights)
	calls, err := api.ContractCalls(ContractCallsArgs{Contract: sender, Heights: heights})
	require.NoError(t, err)
	require.NotNil(t, calls, "an empty list, not null")
	require.Empty(t, calls)

	calls, err = api.ContractCalls(ContractCallsArgs{Contract: sender})
	require.NoError(t, err)
	require.NotNil(t, calls, "no blocks, no calls")

	for name, heights := range map[string][]uint64{
		"block zero":      {0},
		"after the head":  {head + 1},
		"too many blocks": make([]uint64, MaxContractCallsBlocks+1),
	} {
		_, err := api.ContractCalls(ContractCallsArgs{Contract: sender, Heights: heights})
		require.Error(t, err, name)
	}

	// A copy holds the headers but not the bodies (its IPFS is empty): a block it cannot read is an error,
	// never a block without calls.
	copied, _ := chain.Copy()
	_, err = (&BlockchainApi{bc: copied.Blockchain}).ContractCalls(ContractCallsArgs{Contract: sender, Heights: heights})
	require.ErrorContains(t, err, "cannot be read")
}

func TestContractCallsSuccess(t *testing.T) {
	key, err := crypto.HexToECDSA(blocksWithAddressTestKey)
	require.NoError(t, err)
	chain, _ := blockchain.NewCustomTestBlockchain(5, 0, key)
	api := &BlockchainApi{bc: chain.Blockchain}
	contract := common.HexToAddress("0x840e092e31e9656fF15E541505039ed77585338E")
	other := common.HexToAddress("0x18b0a55eb99AcA113f50eEBbdeAf6f96E789277f")

	post := contractCallsTestTx(t, 1, types.CallContractTx, &contract, nil,
		contractCallPayload(t, "makePost", []byte(`{"message":"hello"}`)))
	refused := contractCallsTestTx(t, 2, types.CallContractTx, &contract, nil,
		contractCallPayload(t, "makePost", []byte(`{"message":"hello again"}`)))
	elsewhere := contractCallsTestTx(t, 3, types.CallContractTx, &other, nil, contractCallPayload(t, "makePost", []byte("{}")))
	receipt := func(tx *types.Transaction, success bool) *types.TxReceipt {
		return &types.TxReceipt{ContractAddress: *tx.To, TxHash: tx.Hash(), Success: success, Method: "makePost"}
	}
	block := func(height uint64, txs ...*types.Transaction) *types.Block {
		return &types.Block{
			Header: &types.Header{ProposedHeader: &types.ProposedHeader{Height: height, Time: 1_781_956_251}},
			Body:   &types.Body{Transactions: txs},
		}
	}

	chain.StoreBlock(block(1, refused, elsewhere, post),
		types.TxReceipts{receipt(refused, false), receipt(elsewhere, true), receipt(post, true)})
	calls, err := api.ContractCalls(ContractCallsArgs{Contract: contract, Heights: []uint64{1}})
	require.NoError(t, err)
	require.Len(t, calls, 2)
	require.Equal(t, refused.Hash(), calls[0].Hash)
	require.False(t, calls[0].Success, "a refused call is listed, as refused")
	require.Equal(t, post.Hash(), calls[1].Hash)
	require.True(t, calls[1].Success)

	// Each call needs its own receipt: a block whose receipts lack one, or that has none, is an error.
	chain.StoreBlock(block(2, post), types.TxReceipts{receipt(elsewhere, true)})
	_, err = api.ContractCalls(ContractCallsArgs{Contract: contract, Heights: []uint64{1, 2}})
	require.ErrorContains(t, err, "block 2: no receipt for transaction "+post.Hash().Hex())
	chain.StoreBlock(block(3, post), nil)
	_, err = api.ContractCalls(ContractCallsArgs{Contract: contract, Heights: []uint64{3}})
	require.ErrorContains(t, err, "block 3: no receipt")

	// Receipts the node cannot get (not in its IPFS) are an error, as a body would be.
	missing, err := types.TxReceipts{receipt(post, true)}.ToBytes()
	require.NoError(t, err)
	missingCid, err := chain.Ipfs().Cid(missing)
	require.NoError(t, err)
	unreadable := block(4, post)
	unreadable.Header.ProposedHeader.TxReceiptsCid = missingCid.Bytes()
	chain.StoreBlock(unreadable, nil)
	_, err = api.ContractCalls(ContractCallsArgs{Contract: contract, Heights: []uint64{4}})
	require.ErrorContains(t, err, "the receipts of block 4 cannot be read")

	// A block without a call of the contract needs no receipts.
	chain.StoreBlock(block(5, elsewhere), nil)
	calls, err = api.ContractCalls(ContractCallsArgs{Contract: contract, Heights: []uint64{5}})
	require.NoError(t, err)
	require.Empty(t, calls)
}

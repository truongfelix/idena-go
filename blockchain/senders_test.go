package blockchain

import (
	"math/big"
	"sync"
	"testing"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/blockchain/validation"
	"github.com/idena-network/idena-go/crypto"
	"github.com/stretchr/testify/require"
)

func decodedTx(t *testing.T, tx *types.Transaction) *types.Transaction {
	data, err := tx.ToBytes()
	require.NoError(t, err)
	cp := new(types.Transaction)
	require.NoError(t, cp.FromBytes(data))
	return cp
}

// sendersTestTxs returns decoded transactions with valid signatures, signatures that recover
// another key and signatures that cannot be recovered at all.
func sendersTestTxs(t *testing.T, n int) []*types.Transaction {
	var txs []*types.Transaction
	for i := 0; i < n; i++ {
		key, _ := crypto.GenerateKey()
		txType := types.SendTx
		if i%3 == 1 {
			txType = types.SubmitLongAnswersTx
		}
		tx, err := types.SignTx(&types.Transaction{AccountNonce: uint32(i), Type: txType, Amount: big.NewInt(int64(i)), Payload: []byte{byte(i)}}, key)
		require.NoError(t, err)
		switch i % 5 {
		case 3:
			tx.Signature[5] ^= 0xff // recovers another key, or nothing
		case 4:
			tx.Signature = tx.Signature[:10] // malformed
		}
		txs = append(txs, decodedTx(t, tx))
	}
	return txs
}

// recoverSenders must leave every transaction with exactly the sender that sequential recovery
// from a fresh copy produces, and must not cache failures.
func TestRecoverSendersMatchesSequentialRecovery(t *testing.T) {
	for _, n := range []int{recoverSendersMinTxs, 64} {
		txs := sendersTestTxs(t, n)
		blockTxs := make([]*types.Transaction, len(txs))
		for i, tx := range txs {
			blockTxs[i] = decodedTx(t, tx)
		}

		recoverSenders(blockTxs)

		for i, tx := range blockTxs {
			expectedSender, expectedErr := types.Sender(decodedTx(t, txs[i]))
			if expectedErr != nil {
				_, err := types.Sender(tx)
				require.Equal(t, expectedErr, err, "tx %d", i)
				continue
			}
			// Replace the signature: only a sender cached by recoverSenders can still match.
			tx.Signature = []byte{1, 2, 3}
			sender, err := types.Sender(tx)
			require.NoError(t, err, "tx %d", i)
			require.Equal(t, expectedSender, sender, "tx %d", i)
		}
	}
}

// Other goroutines, such as the mempool, may recover the sender of the same transaction objects
// while a block is validated.
func TestRecoverSendersConcurrentWithSender(t *testing.T) {
	txs := sendersTestTxs(t, 64)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, tx := range txs {
			types.Sender(tx)
		}
	}()
	recoverSenders(txs)
	wg.Wait()
}

// requireSendersNotCached checks that no transaction has a cached sender: once its signature is
// replaced, its sender cannot be recovered.
func requireSendersNotCached(t *testing.T, txs []*types.Transaction) {
	for i, tx := range txs {
		tx.Signature = []byte{1, 2, 3}
		_, err := types.Sender(tx)
		require.Error(t, err, "tx %d", i)
	}
}

// Below recoverSendersMinTxs, down to no transactions at all, recoverSenders recovers nothing and
// leaves it to sequential validation.
func TestRecoverSendersSkipsSmallBatches(t *testing.T) {
	recoverSenders(nil)
	recoverSenders([]*types.Transaction{})
	for _, n := range []int{1, recoverSendersMinTxs - 1} {
		txs := sendersTestTxs(t, n)
		recoverSenders(txs)
		requireSendersNotCached(t, txs)
	}
	// The last batch of a block can be small too.
	txs := sendersTestTxs(t, recoverSendersBatchSize+recoverSendersMinTxs-1)
	recoverSenders(txs[recoverSendersBatchSize:])
	requireSendersNotCached(t, txs)
}

func TestProcessTxsWithoutTransactions(t *testing.T) {
	chain, appState, _, _ := NewTestBlockchain(true, nil)
	defer chain.SecStore().Destroy()

	for _, txs := range [][]*types.Transaction{nil, {}} {
		totalFee, totalTips, receipts, tasks, usedGas, err := chain.processTxs(txs, &txsExecutionContext{appState: appState, header: chain.Head}, false)
		require.NoError(t, err)
		require.Zero(t, totalFee.Sign())
		require.Zero(t, totalTips.Sign())
		require.Empty(t, receipts)
		require.Empty(t, tasks)
		require.Zero(t, usedGas)
	}
}

func TestRecoverSendersDoesNotWarmBeyondBatch(t *testing.T) {
	txs := sendersTestTxs(t, recoverSendersBatchSize+1)
	txs[recoverSendersBatchSize] = decodedTx(t, txs[0])
	recoverSenders(txs)

	tail := txs[recoverSendersBatchSize]
	_, err := types.Sender(decodedTx(t, tail))
	require.NoError(t, err)
	tail.Signature = []byte{1, 2, 3}
	_, err = types.Sender(tail)
	require.Error(t, err, "sender outside the batch must not be cached")
}

func TestProcessTxsStopsSenderPrefetchAfterInvalidTransaction(t *testing.T) {
	chain, appState, _, _ := NewTestBlockchain(true, nil)
	defer chain.SecStore().Destroy()

	txs := sendersTestTxs(t, recoverSendersBatchSize+1)
	txs[recoverSendersBatchSize] = decodedTx(t, txs[0])
	txs[0].Signature = []byte{1, 2, 3}
	_, _, _, _, _, err := chain.processTxs(txs, &txsExecutionContext{appState: appState, header: chain.Head}, false)
	require.ErrorIs(t, err, validation.InvalidSignature)

	tail := txs[recoverSendersBatchSize]
	_, err = types.Sender(decodedTx(t, tail))
	require.NoError(t, err)
	tail.Signature = []byte{1, 2, 3}
	_, err = types.Sender(tail)
	require.Error(t, err, "validation must not prefetch a later batch after rejection")
}

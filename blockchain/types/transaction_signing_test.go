package types

import (
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/crypto"
	"math/big"
	"testing"
)

func TestSignTx(t *testing.T) {
	key, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey)

	tx := Transaction{
		AccountNonce: 0,
		Type:         ActivationTx,
		To:           &addr,
		Amount:       new(big.Int),
	}

	signedTx, err := SignTx(&tx, key)
	if err != nil {
		t.Fatal(err)
	}

	from, err := Sender(signedTx)
	if err != nil {
		t.Fatal(err)
	}
	if from != addr {
		t.Errorf("exected from and address to be equal. Got %x want %x", from, addr)
	}
}

func decodedCopy(t *testing.T, tx *Transaction) *Transaction {
	data, err := tx.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	cp := new(Transaction)
	if err := cp.FromBytes(data); err != nil {
		t.Fatal(err)
	}
	return cp
}

func TestCopyVerification(t *testing.T) {
	key, _ := crypto.GenerateKey()
	otherKey, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey)
	seed, otherSeed := Seed{1}, Seed{2}

	poolTx, _ := SignTx(&Transaction{AccountNonce: 1, Type: SubmitLongAnswersTx, Amount: new(big.Int)}, key)
	if from, _ := Sender(poolTx); from != addr {
		t.Fatal("unexpected sender")
	}
	MarkAsValidLongSessionAnswers(poolTx, seed)

	// the block's copy of the same signed transaction reuses both results
	blockTx := decodedCopy(t, poolTx)
	if blockTx.from.Load() != nil || IsValidLongSessionAnswers(blockTx, seed) {
		t.Fatal("a decoded copy must start without cached verification")
	}
	CopyVerification(blockTx, poolTx)
	if from := blockTx.from.Load(); from == nil || from.(common.Address) != addr {
		t.Fatal("sender was not reused")
	}
	if !IsValidLongSessionAnswers(blockTx, seed) {
		t.Fatal("proof verification was not reused")
	}
	// a proof verified against another seed does not count
	if IsValidLongSessionAnswers(blockTx, otherSeed) {
		t.Fatal("proof verification must be bound to its seed")
	}

	// a transaction that differs in any signed field or in the signature gets nothing
	changedPayload, _ := SignTx(&Transaction{AccountNonce: 2, Type: SubmitLongAnswersTx, Amount: new(big.Int)}, key)
	changedSigner, _ := SignTx(&Transaction{AccountNonce: 1, Type: SubmitLongAnswersTx, Amount: new(big.Int)}, otherKey)
	changedSignature := decodedCopy(t, poolTx)
	changedSignature.Signature = append([]byte{}, poolTx.Signature...)
	changedSignature.Signature[0] ^= 1
	for _, tx := range []*Transaction{decodedCopy(t, changedPayload), decodedCopy(t, changedSigner), changedSignature} {
		CopyVerification(tx, poolTx)
		if tx.from.Load() != nil || IsValidLongSessionAnswers(tx, seed) {
			t.Fatal("verification must not be copied to a different transaction")
		}
	}
}

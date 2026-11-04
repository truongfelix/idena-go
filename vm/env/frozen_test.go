package env

import (
	"math/big"
	"math/rand"
	"testing"

	"github.com/idena-network/idena-go/blockchain/attachments"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/crypto"
	"github.com/stretchr/testify/require"
	db2 "github.com/tendermint/tm-db"
)

// A contract's Send to a frozen address (consensus v13) succeeds and destroys the amount: the contract pays, the
// address receives nothing, and the gas is that of any send; Send to any other address works as before, and before
// the freeze (nil set) the same transfer credits it.
func TestEnvImpSendToFrozenRecipientDestroysTheAmount(t *testing.T) {
	header := &types.Header{ProposedHeader: &types.ProposedHeader{Height: 3, Time: 21, BlockSeed: types.Seed{}}}
	rnd := rand.New(rand.NewSource(1))
	key, _ := crypto.GenerateKeyFromSeed(rnd)
	attachment := attachments.CreateDeployContractAttachment(common.Hash{0x1}, nil, nil)
	payload, _ := attachment.ToBytes()
	tx := &types.Transaction{Epoch: 0, AccountNonce: 1, Type: types.DeployContractTx, Amount: common.DnaBase, Payload: payload}
	tx, _ = types.SignTx(tx, key)
	ctx := NewDeployContextImpl(tx, nil, attachment.CodeHash)

	frozen := common.HexToAddress("0x98D16d7021930b788135dD834983394fF2De9869")
	other := common.Address{0x1}

	newEnv := func() *EnvImp {
		appState, _ := appstate.NewAppState(db2.NewMemDB(), eventbus.New())
		e := NewEnvImp(appState, header, &GasCounter{gasLimit: -1}, nil)
		appState.State.AddBalance(ctx.ContractAddr(), big.NewInt(100))
		return e
	}

	// Before the freeze: the transfer to the (later) frozen address goes through.
	e := newEnv()
	require.NoError(t, e.Send(ctx, frozen, big.NewInt(40)))
	require.Zero(t, big.NewInt(40).Cmp(e.getBalance(frozen)))

	gasBefore := e.gasCounter.UsedGas

	// After the freeze: the contract pays, the frozen address receives nothing, the gas is the same.
	e = newEnv()
	e.SetFrozenAddresses(map[common.Address]struct{}{frozen: {}})
	require.NoError(t, e.Send(ctx, frozen, big.NewInt(40)))
	require.Zero(t, big.NewInt(60).Cmp(e.getBalance(ctx.ContractAddr())), "the contract must pay the amount")
	require.Zero(t, e.getBalance(frozen).Sign(), "a frozen address was credited")
	require.Equal(t, gasBefore, e.gasCounter.UsedGas)

	// A different recipient is unaffected.
	require.NoError(t, e.Send(ctx, other, big.NewInt(40)))
	require.Zero(t, big.NewInt(40).Cmp(e.getBalance(other)))
}

package validation

import (
	"math/big"
	"testing"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/crypto"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

func TestValidateTxRefusesFrozenAddresses(t *testing.T) {
	defer SetAppConfig(nil)

	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	frozenRecipient := common.Address{0x7}
	otherRecipient := common.Address{0x8}

	appState, _ := appstate.NewAppState(db.NewMemDB(), eventbus.New())
	require.NoError(t, appState.Initialize(0))
	appState.State.SetBalance(sender, new(big.Int).Mul(big.NewInt(1000), common.DnaBase))
	appState.State.SetBalance(frozenRecipient, new(big.Int).Mul(big.NewInt(1000), common.DnaBase))
	appState.Precommit()
	require.NoError(t, appState.CommitAt(1))
	require.NoError(t, appState.Initialize(1))

	send := func(to common.Address) *types.Transaction {
		tx := &types.Transaction{
			AccountNonce: 1,
			Type:         types.SendTx,
			To:           &to,
			Amount:       big.NewInt(1),
			MaxFee:       new(big.Int).Mul(big.NewInt(1), common.DnaBase),
		}
		signed, err := types.SignTx(tx, key)
		require.NoError(t, err)
		return signed
	}
	minFee := new(big.Int).Div(common.DnaBase, big.NewInt(100000)) // a max fee of 1 iDNA buys 100,000 gas

	rules := func(enable bool, frozen ...common.Address) {
		cfg := &config.ConsensusConf{EnableUpgrade10: true, EnableUpgrade11: true, EnableUpgrade12: true, EnableUpgrade13: enable,
			FrozenAddresses: map[common.Address]struct{}{}}
		for _, a := range frozen {
			cfg.FrozenAddresses[a] = struct{}{}
		}
		SetAppConfig(&config.Config{Consensus: cfg})
	}

	// Before the upgrade, the listed addresses send and receive as any other.
	rules(false, sender, frozenRecipient)
	for _, txType := range []TxType{MempoolTx, InBlockTx} {
		require.NoError(t, ValidateTx(appState, send(otherRecipient), minFee, txType))
		require.NoError(t, ValidateTx(appState, send(frozenRecipient), minFee, txType))
	}

	// After it, a transaction from a frozen address is refused, in the mempool and in a block.
	rules(true, sender)
	for _, txType := range []TxType{MempoolTx, InBlockTx} {
		require.ErrorIs(t, ValidateTx(appState, send(otherRecipient), minFee, txType), FrozenAddress)
	}

	// A transaction to a frozen address is refused; one to any other address still passes.
	rules(true, frozenRecipient)
	for _, txType := range []TxType{MempoolTx, InBlockTx} {
		require.ErrorIs(t, ValidateTx(appState, send(frozenRecipient), minFee, txType), FrozenAddress)
		require.NoError(t, ValidateTx(appState, send(otherRecipient), minFee, txType))
	}
}

func TestConsensusV13FreezesTheBridgeWalletOnly(t *testing.T) {
	bridge := common.HexToAddress("0x98D16d7021930b788135dD834983394fF2De9869")

	v12 := config.ConsensusVersions[config.ConsensusV12]
	require.False(t, v12.IsFrozenAddress(bridge), "v12 must freeze nothing")

	v13 := config.ConsensusVersions[config.ConsensusV13]
	require.True(t, v13.EnableUpgrade13)
	require.True(t, v13.IsFrozenAddress(bridge))
	require.Len(t, v13.FrozenAddresses, 1)
	require.False(t, v13.IsFrozenAddress(common.Address{}))

	// A v12 node upgraded in place gets the same rules.
	upgraded := *v12
	config.ApplyConsensusVersion(config.ConsensusV13, &upgraded)
	require.True(t, upgraded.IsFrozenAddress(bridge))
}

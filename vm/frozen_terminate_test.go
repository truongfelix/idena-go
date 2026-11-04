package vm

import (
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/idena-network/idena-go/blockchain/attachments"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/crypto"
	"github.com/idena-network/idena-go/vm/embedded"
	"github.com/stretchr/testify/require"
	dbm "github.com/tendermint/tm-db"
)

// Terminating an embedded contract refunds half its stake to an address the terminate call names: under v13, when
// that address is frozen, the contract still terminates and the refund is destroyed with the other half; before v13
// and for any other address the refund is paid.
func TestTerminateRefundToFrozenAddressIsDestroyed(t *testing.T) {
	bridge := common.HexToAddress("0x98D16d7021930b788135dD834983394fF2De9869")
	other := common.Address{0x7}
	stake := new(big.Int).Mul(big.NewInt(1000), common.DnaBase)
	half := new(big.Int).Quo(stake, big.NewInt(2))

	run := func(consensus *config.ConsensusConf, dest common.Address) (*types.TxReceipt, *appstate.AppState, common.Address) {
		appState, err := appstate.NewAppState(dbm.NewMemDB(), eventbus.New())
		require.NoError(t, err)
		require.NoError(t, appState.Initialize(0))
		key, err := crypto.GenerateKey()
		require.NoError(t, err)
		from := crypto.PubkeyToAddress(key.PublicKey)
		appState.State.SetBalance(from, new(big.Int).Mul(stake, big.NewInt(2)))
		cfg := &config.Config{
			Consensus:        consensus,
			Blockchain:       &config.BlockchainConfig{},
			OfflineDetection: config.GetDefaultOfflineDetectionConfig(),
			Mempool:          config.GetDefaultMempoolConfig(),
		}
		header := &types.Header{ProposedHeader: &types.ProposedHeader{Height: 2, Time: 100}}

		unlock := make([]byte, 8)
		binary.LittleEndian.PutUint64(unlock, 0)
		deployPayload, err := attachments.CreateDeployContractAttachment(embedded.TimeLockContract, nil, nil, unlock).ToBytes()
		require.NoError(t, err)
		deploy, err := types.SignTx(&types.Transaction{AccountNonce: 1, Type: types.DeployContractTx, Amount: stake,
			MaxFee: big.NewInt(0), Payload: deployPayload}, key)
		require.NoError(t, err)
		receipt := NewVmImpl(appState, nil, header, nil, cfg).Run(deploy, &from, -1, true)
		require.True(t, receipt.Success, "deploy: %v", receipt.Error)
		contract := receipt.ContractAddress
		require.Zero(t, stake.Cmp(appState.State.GetContractStake(contract)))

		terminatePayload, err := attachments.CreateTerminateContractAttachment(dest.Bytes()).ToBytes()
		require.NoError(t, err)
		terminate, err := types.SignTx(&types.Transaction{AccountNonce: 2, Type: types.TerminateContractTx, To: &contract,
			Amount: big.NewInt(0), MaxFee: big.NewInt(0), Payload: terminatePayload}, key)
		require.NoError(t, err)
		return NewVmImpl(appState, nil, header, nil, cfg).Run(terminate, &from, -1, true), appState, contract
	}
	v12 := config.ConsensusVersions[config.ConsensusV12]
	v13 := config.ConsensusVersions[config.ConsensusV13]

	receipt, appState, contract := run(v13, bridge)
	require.True(t, receipt.Success, "terminate to a frozen address: %v", receipt.Error)
	require.Zero(t, appState.State.GetBalance(bridge).Sign(), "the frozen address received the refund")
	require.True(t, appState.State.GetContractStake(contract) == nil || appState.State.GetContractStake(contract).Sign() == 0,
		"the contract kept its stake")

	receipt, appState, _ = run(v13, other)
	require.True(t, receipt.Success, "terminate to another address: %v", receipt.Error)
	require.Zero(t, half.Cmp(appState.State.GetBalance(other)))

	receipt, appState, _ = run(v12, bridge)
	require.True(t, receipt.Success, "terminate before v13: %v", receipt.Error)
	require.Zero(t, half.Cmp(appState.State.GetBalance(bridge)))
}

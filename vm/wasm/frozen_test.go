package wasm

import (
	"math/big"
	"math/rand"
	"testing"

	"github.com/idena-network/idena-go/blockchain/attachments"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/crypto"
	"github.com/idena-network/idena-go/vm/wasm/testdata"
	lib "github.com/idena-network/idena-wasm-binding/lib"
	"github.com/stretchr/testify/require"
	dbm "github.com/tendermint/tm-db"
)

// A contract crediting a frozen address (the BSC bridge wallet, consensus v13) credits nothing, sub envs included;
// crediting any other address, or the same address before the freeze, works.
func TestWasmEnvAddBalanceBlocksFrozenAddress(t *testing.T) {
	bridge := common.HexToAddress("0x98D16d7021930b788135dD834983394fF2De9869")
	other := common.Address{0x1}

	newEnv := func() *WasmEnv {
		appState, _ := appstate.NewAppState(dbm.NewMemDB(), eventbus.New())
		require.NoError(t, appState.Initialize(0))
		key, _ := crypto.GenerateKey()
		to := common.Address{0x9}
		tx := &types.Transaction{Type: types.CallContractTx, To: &to, Amount: big.NewInt(0)}
		tx, _ = types.SignTx(tx, key)
		return NewWasmEnv(appState, nil, NewContractContext(tx), &types.Header{ProposedHeader: &types.ProposedHeader{}}, "m", false, false, true, nil)
	}
	credit := func(e *WasmEnv, addr common.Address) {
		e.AddBalance(&lib.GasMeter{}, addr, big.NewInt(5))
	}

	// Before the freeze: crediting the bridge is allowed.
	e := newEnv()
	credit(e, bridge)
	require.Zero(t, big.NewInt(5).Cmp(e.getBalance(bridge)))

	// After the freeze: another address is credited; the bridge is not.
	e = newEnv()
	e.SetFrozenAddresses(map[common.Address]struct{}{bridge: {}})
	credit(e, other)
	require.Zero(t, big.NewInt(5).Cmp(e.getBalance(other)))
	credit(e, bridge)
	require.Zero(t, e.getBalance(bridge).Sign(), "a frozen address was credited")

	// A sub env inherits the frozen set.
	sub, err := e.CreateSubEnv(common.Address{0x2}, "m", big.NewInt(0), false)
	require.NoError(t, err)
	credit(sub.(*WasmEnv), bridge)
	require.Zero(t, sub.(*WasmEnv).getBalance(bridge).Sign())
}

// A real contract sending iDNA to a frozen address: under v13 the call succeeds, the contract pays and the amount is
// destroyed (the address receives nothing); another recipient, or the same one before v13, is paid.
func TestWasmTransferToFrozenAddressDestroysTheAmount(t *testing.T) {
	code, err := testdata.FrozenTransfer()
	require.NoError(t, err)
	bridge := common.HexToAddress("0x98D16d7021930b788135dD834983394fF2De9869")
	other := common.Address{0x3}

	cases := []struct {
		name      string
		version   config.ConsensusVerson
		dest      common.Address
		destGains bool
	}{
		{"v13 to the frozen address", config.ConsensusV13, bridge, false},
		{"v13 to another address", config.ConsensusV13, other, true},
		{"v12 to the bridge", config.ConsensusV12, bridge, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			appState, _ := appstate.NewAppState(dbm.NewMemDB(), eventbus.New())
			require.NoError(t, appState.Initialize(0))
			key, _ := crypto.GenerateKeyFromSeed(rand.New(rand.NewSource(7)))
			receipt := deployContract(key, appState, code)
			require.True(t, receipt.Success, receipt.Error)
			contract := receipt.ContractAddress
			appState.State.SetBalance(contract, big.NewInt(1000))
			require.NoError(t, appState.Commit(nil))

			cfg := getLatestConfig()
			cfg.Consensus = config.ConsensusVersions[tc.version]
			payload, _ := attachments.CreateCallContractAttachment("send", tc.dest.Bytes(), big.NewInt(100).Bytes()).ToBytes()
			tx := &types.Transaction{AccountNonce: nonce, To: &contract, Type: types.CallContractTx, Payload: payload, Amount: big.NewInt(0)}
			tx, _ = types.SignTx(tx, key)
			nonce++
			receipt = NewWasmVM(appState, nil, createHeader(1, 1), cfg, true, nil).Run(tx, 612000000)

			require.True(t, receipt.Success, "receipt error: %v", receipt.Error)
			wantDest := int64(0)
			if tc.destGains {
				wantDest = 100
			}
			require.Equal(t, int64(900), appState.State.GetBalance(contract).Int64(), "the contract pays in every case")
			require.Equal(t, wantDest, appState.State.GetBalance(tc.dest).Int64())
		})
	}
}

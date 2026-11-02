package wasm

import (
	"math/big"
	"testing"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/crypto"
	lib "github.com/idena-network/idena-wasm-binding/lib"
	"github.com/stretchr/testify/require"
	dbm "github.com/tendermint/tm-db"
)

// A contract crediting a frozen address (the BSC bridge wallet, consensus v13) aborts; crediting any other address,
// or the same address before the freeze, does not.
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
	credit := func(e *WasmEnv, addr common.Address) (panicked bool) {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		e.AddBalance(&lib.GasMeter{}, addr, big.NewInt(5))
		return false
	}

	// Before the freeze: crediting the bridge is allowed.
	require.False(t, credit(newEnv(), bridge))

	// After the freeze: crediting the bridge aborts; another address is unaffected.
	e := newEnv()
	e.SetFrozenAddresses(map[common.Address]struct{}{bridge: {}})
	require.True(t, credit(e, bridge), "crediting a frozen address must abort")
	require.False(t, credit(e, other), "crediting another address must still work")
	require.Zero(t, big.NewInt(5).Cmp(e.getBalance(other)))

	// A sub-env inherits the frozen set.
	sub, err := e.CreateSubEnv(common.Address{0x2}, "m", big.NewInt(0), false)
	require.NoError(t, err)
	require.True(t, credit(sub.(*WasmEnv), bridge))
}

package consensus

import (
	"testing"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/core/state"
	"github.com/stretchr/testify/require"
	dbm "github.com/tendermint/tm-db"
)

func Test_needsOnlineStatusTx(t *testing.T) {
	appState, err := appstate.NewAppState(dbm.NewMemDB(), eventbus.New())
	require.NoError(t, err)
	require.NoError(t, appState.Initialize(0))

	validated := common.Address{0x1}
	delegated := common.Address{0x2}
	pool := common.Address{0x3}
	online := common.Address{0x4}
	candidate := common.Address{0x5}

	for _, addr := range []common.Address{validated, delegated, online} {
		appState.State.SetState(addr, state.Human)
		appState.IdentityState.SetValidated(addr, true)
	}
	appState.State.SetState(candidate, state.Candidate)
	appState.State.SetDelegatee(delegated, pool)
	appState.IdentityState.SetDelegatee(delegated, pool)
	appState.IdentityState.SetOnline(online, true)

	appState.Precommit()
	require.NoError(t, appState.CommitAt(1))
	appState.ValidatorsCache.Load()

	require.True(t, appState.ValidatorsCache.IsValidated(delegated))
	require.True(t, appState.ValidatorsCache.IsPool(pool))

	require.True(t, needsOnlineStatusTx(appState, validated))
	require.False(t, needsOnlineStatusTx(appState, delegated), "a delegated identity is refused online-status txs")
	require.True(t, needsOnlineStatusTx(appState, pool))
	require.False(t, needsOnlineStatusTx(appState, online))
	require.False(t, needsOnlineStatusTx(appState, candidate))

	appState.State.SetValidationPeriod(state.FlipLotteryPeriod)
	require.False(t, needsOnlineStatusTx(appState, validated))
}

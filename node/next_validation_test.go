package node

import (
	"testing"
	"time"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/database"
	"github.com/stretchr/testify/require"
	dbm "github.com/tendermint/tm-db"
)

// The IPFS conversion waits near a validation (ipfs/badger_convert.go conversionPostponed): its time comes from the
// chain database before IPFS opens.
func TestNextValidationTime(t *testing.T) {
	// No chain yet: unknown (zero), which converts.
	require.True(t, nextValidationTime(dbm.NewMemDB()).IsZero())

	chain := dbm.NewMemDB()
	stateDb, err := state.NewLazy(chain)
	require.NoError(t, err)
	next := time.Date(2026, 11, 3, 15, 0, 0, 0, time.UTC)
	stateDb.SetNextValidationTime(next)
	_, _, version, err := stateDb.Commit(true)
	require.NoError(t, err)
	repo := database.NewRepo(chain)

	// A head with no state at its height: unknown.
	repo.WriteHead(nil, &types.Header{ProposedHeader: &types.ProposedHeader{Height: uint64(version) + 5}})
	require.True(t, nextValidationTime(chain).IsZero())

	repo.WriteHead(nil, &types.Header{ProposedHeader: &types.ProposedHeader{Height: uint64(version)}})
	require.True(t, next.Equal(nextValidationTime(chain)), nextValidationTime(chain))
}

package depcheck

// The state and identity trees, the block roots and the transaction roots are
// computed with this exact iavl commit (Idena's additions to cosmos/iavl
// v0.12). Any other version can hash differently, so the module must stay
// pinned to it, fetched from the truongfelix fork where a branch holds it.

import "testing"

const (
	iavlModule        = "github.com/cosmos/iavl"
	pinnedIavlMod     = "github.com/truongfelix/iavl"
	pinnedIavlVersion = "v0.12.3-0.20211223100228-a33b117aa31e"
)

func TestIavlReplaceDirectivePresent(t *testing.T) {
	requireReplacement(t, iavlModule, pinnedIavlMod, pinnedIavlVersion,
		"the commit the state roots are computed with")
}

package blockchain

import (
	"bytes"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/core/identitylist"
	"github.com/idena-network/idena-go/log"
	"github.com/pkg/errors"
)

// isIdentityListBlock reports whether a block carries the identity list (consensus v13): each snapshot block
// (every SnapshotRange blocks outside the validation) and the block that applies the ceremony's results.
func isIdentityListBlock(cfg *config.ConsensusConf, flags types.BlockFlag) bool {
	return cfg.EnableUpgrade13 && (flags.HasFlag(types.Snapshot) || flags.HasFlag(types.ValidationFinished))
}

// buildIdentityList builds the identity list of the state after the block, once applyBlockOnState or
// applyEmptyBlockOnState ran on it; nil for a block that carries none. A list that cannot be built (no state holds
// one identity twice, nor over a thousand million identities) leaves the field empty, the same on every node.
func (chain *Blockchain) buildIdentityList(appState *appstate.AppState, block *types.Block) *identitylist.List {
	if !isIdentityListBlock(chain.config.Consensus, block.Header.Flags()) {
		return nil
	}
	list, err := identitylist.FromState(block.Height(), appState.State)
	if err != nil {
		log.Error("Identity list not built", "height", block.Height(), "err", err)
		return nil
	}
	return list
}

func identityListCidBytes(list *identitylist.List) []byte {
	if list == nil {
		return nil
	}
	return list.IndexCid.Bytes()
}

func validateIdentityListCid(list *identitylist.List, block *types.Block) error {
	if expected := identityListCidBytes(list); !bytes.Equal(expected, block.IdentityListCid()) {
		return errors.Errorf("invalid identity list cid. Expected=%x, actual=%x", expected, block.IdentityListCid())
	}
	return nil
}

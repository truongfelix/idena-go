package api

import (
	"github.com/idena-network/idena-go/state"
)

type BlockchainInitialApi struct {
	nodeState *state.NodeState
}

func NewBlockchainInitialApi(nodeState *state.NodeState) *BlockchainInitialApi {
	return &BlockchainInitialApi{nodeState}
}

func (api *BlockchainInitialApi) Syncing() Syncing {
	message, compacting := api.nodeState.Starting()
	return Syncing{
		Syncing:    true,
		Message:    message,
		Compacting: compacting,
	}
}

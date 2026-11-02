package blockchain

import (
	"math/big"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/appstate"
)

// scaleByFlipQuality returns the part of a minted reward that identity keeps under v13: the reward times its flip
// quality ratio, points / flips made over its window (core/state/flip_quality.go). An empty window (a Candidate, or
// no ceremony with flips since the fork) keeps the whole reward. The rest is never minted. Before v13 it returns
// reward unchanged.
//
// It scales the block proposer's minted reward and the final committee's (blockchain.go), and the validation
// rewards (rewards.go): staking, flips, reports, and each side of an invitation by its own recipient. At the
// ceremony block the window already holds that ceremony's flips. Not scaled: fees and tips, the candidate reward of a
// new identity, the foundation payouts and the zero wallet fund.
func scaleByFlipQuality(appState *appstate.AppState, cfg *config.ConsensusConf, identity common.Address, reward *big.Int) *big.Int {
	if !cfg.EnableUpgrade13 || reward == nil || reward.Sign() <= 0 {
		return reward
	}
	flips, points2 := appState.State.GetFlipQuality(identity)
	if flips == 0 {
		return reward
	}
	if points2 >= 2*flips {
		return reward
	}
	res := new(big.Int).Mul(reward, new(big.Int).SetUint64(uint64(points2)))
	return res.Quo(res, new(big.Int).SetUint64(2*uint64(flips)))
}

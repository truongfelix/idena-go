package blockchain

import (
	"math/big"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/stats/collector"
)

// mintedReward is the one place a reward minted for an identity meets the v13 flip quality rule: it returns the part
// recipient is paid and reports the rest, never minted, to the stats collector. Every minted reward paid to an
// identity goes through it: the block proposer's minted part (fees and tips are added after it) and the final
// committee's (blockchain.go), the staking, flip and report rewards and both sides of an invitation (rewards.go). At
// the ceremony block the window already holds that ceremony's flips. Deliberately not scaled: fees and tips, the
// candidate reward of a new identity, the foundation payouts and the zero wallet fund. A new kind of minted reward
// goes through it too, or joins that list. Before v13 it returns reward.
func mintedReward(appState *appstate.AppState, cfg *config.ConsensusConf, statsCollector collector.StatsCollector,
	recipient common.Address, reward *big.Int) *big.Int {
	kept := scaleByFlipQuality(appState, cfg, recipient, reward)
	if kept != reward {
		if burnt := new(big.Int).Sub(reward, kept); burnt.Sign() > 0 {
			collector.AddFlipQualityBurntCoins(statsCollector, recipient, burnt)
		}
	}
	return kept
}

// scaleByFlipQuality returns reward times identity's flip quality ratio, points / flips made over its window
// (core/state/flip_quality.go), rounded down; an empty window (a Candidate, or no ceremony with flips since the fork)
// keeps the whole reward. Only mintedReward calls it. Before v13 it returns reward unchanged.
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

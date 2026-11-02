package api

import (
	"github.com/idena-network/idena-go/blockchain"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/hexutil"
	"github.com/idena-network/idena-go/core/identitylist"
	"github.com/shopspring/decimal"
)

// IdentityList is the identity list a block carries (consensus v13).
type IdentityList struct {
	Height     uint64             `json:"height"`
	Cid        string             `json:"cid"`
	PageCids   []string           `json:"pageCids"`
	Count      uint32             `json:"count"`
	Identities []IdentityListItem `json:"identities"`
}

type IdentityListItem struct {
	Address          common.Address  `json:"address"`
	State            string          `json:"state"`
	Birthday         uint16          `json:"birthday"`
	Stake            decimal.Decimal `json:"stake"`
	LockedStake      decimal.Decimal `json:"lockedStake"`
	ReplenishedStake decimal.Decimal `json:"replenishedStake"`
	Delegatee        *common.Address `json:"delegatee"`
	Penalty          decimal.Decimal `json:"penalty"`
	PenaltySeconds   uint16          `json:"penaltySeconds"`
	PenaltyTimestamp int64           `json:"penaltyTimestamp"`
	QualifiedFlips   uint32          `json:"qualifiedFlips"`
	ShortFlipPoints  float32         `json:"shortFlipPoints"`
	Scores           hexutil.Bytes   `json:"scores"`
}

// IdentityList returns the identity list of the block at height, or the last one this node pinned when height is
// left out or 0. Each block read from IPFS is checked against the header's CID.
func (api *DnaApi) IdentityList(height *uint64) (*IdentityList, error) {
	var h uint64
	if height != nil {
		h = *height
	}
	indexCid, index, rows, err := api.bc.IdentityList(h)
	if err != nil {
		return nil, err
	}
	res := &IdentityList{Height: index.Height, Cid: indexCid.String(), Count: index.Count}
	for _, c := range index.PageCids {
		res.PageCids = append(res.PageCids, c.String())
	}
	res.Identities = make([]IdentityListItem, 0, len(rows))
	for _, r := range rows {
		res.Identities = append(res.Identities, convertIdentityListRow(r))
	}
	return res, nil
}

func convertIdentityListRow(r identitylist.Row) IdentityListItem {
	return IdentityListItem{
		Address:          r.Address,
		State:            identityStateName(r.State),
		Birthday:         r.Birthday,
		Stake:            blockchain.ConvertToFloat(r.Stake),
		LockedStake:      blockchain.ConvertToFloat(r.LockedStake),
		ReplenishedStake: blockchain.ConvertToFloat(r.ReplenishedStake),
		Delegatee:        r.Delegatee,
		Penalty:          blockchain.ConvertToFloat(r.Penalty),
		PenaltySeconds:   r.PenaltySeconds,
		PenaltyTimestamp: r.PenaltyTimestamp,
		QualifiedFlips:   r.QualifiedFlips,
		ShortFlipPoints:  float32(r.ShortFlipPoints) / 2,
		Scores:           r.Scores,
	}
}

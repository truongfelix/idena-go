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
// left out or 0. With page, only that page's identities (pages of identitylist.PageSize, from 0): a large list in
// one answer can outlast the RPC's write timeout. Each block read from IPFS is checked against the header's CID.
func (api *DnaApi) IdentityList(height *uint64, page *int) (*IdentityList, error) {
	var h uint64
	if height != nil {
		h = *height
	}
	indexCid, index, err := api.bc.IdentityListIndex(h)
	if err != nil {
		return nil, err
	}
	res := &IdentityList{Height: index.Height, Cid: indexCid.String(), Count: index.Count}
	for _, c := range index.PageCids {
		res.PageCids = append(res.PageCids, c.String())
	}
	pages := make([]int, 0, len(index.PageCids))
	if page != nil {
		pages = append(pages, *page)
	} else {
		for i := range index.PageCids {
			pages = append(pages, i)
		}
	}
	res.Identities = []IdentityListItem{}
	for _, i := range pages {
		rows, err := api.bc.IdentityListPage(index, i)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			res.Identities = append(res.Identities, convertIdentityListRow(r))
		}
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

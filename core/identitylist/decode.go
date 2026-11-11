package identitylist

import (
	"encoding/binary"
	"math/big"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/core/state"
	"github.com/ipfs/go-cid"
	"github.com/pkg/errors"
)

// Index is a decoded index block.
type Index struct {
	Height   uint64
	Count    uint32
	PageCids []cid.Cid
}

// DecodeIndex reads an index block.
func DecodeIndex(data []byte) (*Index, error) {
	if len(data) < 17 {
		return nil, errors.New("index too short")
	}
	if data[0] != Version {
		return nil, errors.Errorf("unknown index version %v", data[0])
	}
	idx := &Index{
		Height: binary.BigEndian.Uint64(data[1:9]),
		Count:  binary.BigEndian.Uint32(data[9:13]),
	}
	pages := binary.BigEndian.Uint32(data[13:17])
	rest := data[17:]
	for i := uint32(0); i < pages; i++ {
		if len(rest) < 1 || len(rest) < 1+int(rest[0]) {
			return nil, errors.New("index cut in a page CID")
		}
		c, err := cid.Cast(rest[1 : 1+int(rest[0])])
		if err != nil {
			return nil, errors.Wrap(err, "page CID")
		}
		idx.PageCids = append(idx.PageCids, c)
		rest = rest[1+int(rest[0]):]
	}
	if len(rest) != 0 {
		return nil, errors.New("bytes after the last page CID")
	}
	if want := (uint64(idx.Count) + PageSize - 1) / PageSize; uint64(pages) != want {
		return nil, errors.Errorf("%v pages for %v identities", pages, idx.Count)
	}
	return idx, nil
}

// DecodePage reads the rows of a page block.
func DecodePage(data []byte) ([]Row, error) {
	if len(data) < 1 {
		return nil, errors.New("empty page")
	}
	if data[0] != Version {
		return nil, errors.Errorf("unknown page version %v", data[0])
	}
	body := data[1:]
	if len(body)%RowSize != 0 {
		return nil, errors.Errorf("page of %v bytes is not whole rows", len(data))
	}
	rows := make([]Row, 0, len(body)/RowSize)
	for len(body) > 0 {
		rows = append(rows, decodeRow(body[:RowSize]))
		body = body[RowSize:]
	}
	return rows, nil
}

func decodeRow(b []byte) Row {
	var r Row
	take := func(n int) []byte {
		v := b[:n]
		b = b[n:]
		return v
	}
	amount := func() *big.Int {
		v := new(big.Int).SetBytes(take(amountSize))
		if v.Sign() == 0 {
			return new(big.Int)
		}
		return v
	}
	copy(r.Address[:], take(common.AddressLength))
	r.State = state.IdentityState(take(1)[0])
	r.Birthday = binary.BigEndian.Uint16(take(2))
	r.Stake = amount()
	r.LockedStake = amount()
	r.ReplenishedStake = amount()
	hasDelegatee := take(1)[0] == 1
	delegatee := take(common.AddressLength)
	if hasDelegatee {
		var d common.Address
		copy(d[:], delegatee)
		r.Delegatee = &d
	}
	r.Penalty = amount()
	r.PenaltySeconds = binary.BigEndian.Uint16(take(2))
	r.PenaltyTimestamp = int64(binary.BigEndian.Uint64(take(8)))
	r.QualifiedFlips = binary.BigEndian.Uint32(take(4))
	r.ShortFlipPoints = binary.BigEndian.Uint32(take(4))
	n := int(take(1)[0])
	scores := take(common.LastScoresCount)
	if n > 0 {
		if n > common.LastScoresCount {
			n = common.LastScoresCount
		}
		r.Scores = append([]byte(nil), scores[:n]...)
	}
	r.FlipQualityFlips = binary.BigEndian.Uint16(take(2))
	r.FlipQualityPoints = binary.BigEndian.Uint16(take(2))
	return r
}

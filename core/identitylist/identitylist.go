// Package identitylist builds the list of every identity in the state at a block, in pages stored as raw IPFS
// blocks, and the index block whose CID consensus v13 writes in the header of each list block.
//
// Format, version 2 (all integers big-endian, amounts as 32-byte unsigned integers):
//
//	page:  version (1 byte) | rows, sorted by address, RowSize bytes each
//	index: version (1 byte) | height (8) | identity count (4) | page count (4) | the CID of each page, as bytes,
//	       each preceded by its length (1)
//	row:   address (20) | state (1) | birthday (2) | stake (32) | locked stake (32) | replenished stake (32) |
//	       has delegatee (1) | delegatee (20, zero without one) | penalty (32) | penalty seconds (2) |
//	       penalty timestamp (8) | qualified flips (4) | short flip points x2 (4) | scores count (1) |
//	       scores (common.LastScoresCount bytes, zero-padded) | flip quality: flips made (2) |
//	       flip quality: points x2 (2)
//
// The flip quality counts are the identity's v13 window summed (state.Identity.FlipQuality): its mining rewards are
// scaled by points / flips made, or kept whole while it has none. Version 1 (no flip quality) was never released.
//
// A page holds at most PageSize rows, so it stays under the 256 KiB a single raw IPFS block holds: its CID is the
// CIDv1 raw SHA2-256 of its bytes, which is also what the node's IPFS Add returns for it.
package identitylist

import (
	"encoding/binary"
	"math/big"
	"sort"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/core/state"
	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
	"github.com/pkg/errors"
)

const (
	Version = 2

	amountSize = 32
	// RowSize is the size of one identity's row.
	RowSize = common.AddressLength + 1 + 2 + 3*amountSize + 1 + common.AddressLength + amountSize + 2 + 8 + 4 + 4 + 1 +
		common.LastScoresCount + 2 + 2
	// PageSize is the number of rows in every page but the last.
	PageSize = 1000

	maxRawBlockSize = 256 * 1024
)

var cidPrefix = cid.Prefix{
	Version:  1,
	Codec:    cid.Raw,
	MhType:   multihash.SHA2_256,
	MhLength: -1,
}

// Row is one identity of the list.
type Row struct {
	Address          common.Address
	State            state.IdentityState
	Birthday         uint16
	Stake            *big.Int
	LockedStake      *big.Int
	ReplenishedStake *big.Int
	Delegatee        *common.Address
	Penalty          *big.Int
	PenaltySeconds   uint16
	PenaltyTimestamp int64
	QualifiedFlips   uint32
	ShortFlipPoints  uint32 // twice the points, as the state stores them
	Scores           []byte
	FlipQualityFlips uint16
	// FlipQualityPoints is twice the points, as the state stores them.
	FlipQualityPoints uint16
}

// RowFromIdentity reads the row of one identity record.
func RowFromIdentity(addr common.Address, identity state.Identity) Row {
	r := Row{
		Address:          addr,
		State:            identity.State,
		Birthday:         identity.Birthday,
		Stake:            identity.Stake,
		LockedStake:      identity.LockedStake(),
		ReplenishedStake: identity.ReplenishedStake(),
		Delegatee:        identity.Delegatee(),
		Penalty:          identity.Penalty,
		PenaltySeconds:   identity.PenaltySeconds(),
		PenaltyTimestamp: identity.PenaltyTimestamp(),
		QualifiedFlips:   identity.QualifiedFlips,
		ShortFlipPoints:  identity.ShortFlipPoints,
		Scores:           identity.Scores,
	}
	flips, points2 := identity.FlipQuality()
	r.FlipQualityFlips, r.FlipQualityPoints = clampUint16(flips), clampUint16(points2)
	return r
}

// clampUint16 bounds a window sum, which the state keeps far below 2^16 (10 ceremonies of at most 127 flips).
func clampUint16(v uint32) uint16 {
	if v > 0xffff {
		return 0xffff
	}
	return uint16(v)
}

// List is a built list: its pages and its index, with their CIDs.
type List struct {
	Height   uint64
	Count    int
	Pages    [][]byte
	PageCids []cid.Cid
	Index    []byte
	IndexCid cid.Cid
}

// FromState builds the list of every identity in the state's working tree.
func FromState(height uint64, s *state.StateDB) (*List, error) {
	var rows []Row
	s.IterateOverIdentities(func(addr common.Address, identity state.Identity) {
		rows = append(rows, RowFromIdentity(addr, identity))
	})
	return Build(height, rows)
}

// Build sorts the rows by address and builds the pages and the index.
func Build(height uint64, rows []Row) (*List, error) {
	sorted := make([]Row, len(rows))
	copy(sorted, rows)
	sort.Slice(sorted, func(i, j int) bool {
		return compareAddresses(sorted[i].Address, sorted[j].Address) < 0
	})
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Address == sorted[i-1].Address {
			return nil, errors.Errorf("identity %v listed twice", sorted[i].Address.Hex())
		}
	}
	list := &List{Height: height, Count: len(sorted)}
	for start := 0; start < len(sorted); start += PageSize {
		end := start + PageSize
		if end > len(sorted) {
			end = len(sorted)
		}
		page := make([]byte, 1, 1+(end-start)*RowSize)
		page[0] = Version
		for _, row := range sorted[start:end] {
			page = appendRow(page, row)
		}
		c, err := Cid(page)
		if err != nil {
			return nil, err
		}
		list.Pages = append(list.Pages, page)
		list.PageCids = append(list.PageCids, c)
	}
	index := make([]byte, 0, 17+len(list.PageCids)*37)
	index = append(index, Version)
	index = binary.BigEndian.AppendUint64(index, height)
	index = binary.BigEndian.AppendUint32(index, uint32(list.Count))
	index = binary.BigEndian.AppendUint32(index, uint32(len(list.PageCids)))
	for _, c := range list.PageCids {
		b := c.Bytes()
		index = append(index, byte(len(b)))
		index = append(index, b...)
	}
	if len(index) > maxRawBlockSize {
		return nil, errors.Errorf("index of %v pages does not fit in one block", len(list.PageCids))
	}
	c, err := Cid(index)
	if err != nil {
		return nil, err
	}
	list.Index, list.IndexCid = index, c
	return list, nil
}

// Cid is the CID of a page or an index: CIDv1, raw, SHA2-256 of its bytes.
func Cid(data []byte) (cid.Cid, error) {
	if len(data) > maxRawBlockSize {
		return cid.Cid{}, errors.Errorf("block of %v bytes does not fit in one raw block", len(data))
	}
	return cidPrefix.Sum(data)
}

func compareAddresses(a, b common.Address) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func appendAmount(b []byte, v *big.Int) []byte {
	var buf [amountSize]byte
	// The state never holds a negative amount nor one above 2^256; such a value is written as zero, the same on
	// every node.
	if v != nil && v.Sign() > 0 && v.BitLen() <= amountSize*8 {
		v.FillBytes(buf[:])
	}
	return append(b, buf[:]...)
}

func appendRow(b []byte, r Row) []byte {
	b = append(b, r.Address[:]...)
	b = append(b, byte(r.State))
	b = binary.BigEndian.AppendUint16(b, r.Birthday)
	b = appendAmount(b, r.Stake)
	b = appendAmount(b, r.LockedStake)
	b = appendAmount(b, r.ReplenishedStake)
	if r.Delegatee != nil {
		b = append(b, 1)
		b = append(b, r.Delegatee[:]...)
	} else {
		b = append(b, 0)
		b = append(b, make([]byte, common.AddressLength)...)
	}
	b = appendAmount(b, r.Penalty)
	b = binary.BigEndian.AppendUint16(b, r.PenaltySeconds)
	b = binary.BigEndian.AppendUint64(b, uint64(r.PenaltyTimestamp))
	b = binary.BigEndian.AppendUint32(b, r.QualifiedFlips)
	b = binary.BigEndian.AppendUint32(b, r.ShortFlipPoints)
	scores := r.Scores
	if len(scores) > common.LastScoresCount {
		scores = scores[len(scores)-common.LastScoresCount:]
	}
	b = append(b, byte(len(scores)))
	b = append(b, scores...)
	b = append(b, make([]byte, common.LastScoresCount-len(scores))...)
	b = binary.BigEndian.AppendUint16(b, r.FlipQualityFlips)
	return binary.BigEndian.AppendUint16(b, r.FlipQualityPoints)
}

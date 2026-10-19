package appstate

import (
	"bytes"
	"fmt"
	"github.com/RoaringBitmap/roaring"
	"github.com/google/tink/go/subtle/random"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/crypto"
	"github.com/idena-network/idena-go/tests"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestEvidenceMap_CalculateBitmap(t *testing.T) {
	require := require.New(t)

	bus := eventbus.New()
	em := NewEvidenceMap(bus)
	now := time.Now().UTC().Add(-24 * time.Second)
	em.SetShortSessionTime(now, time.Minute*1)

	const candidatesCount = 10000
	const txCandidate = 1
	const delayedKeyAuthor = 10
	const delayedKeyPackageAuthor = 100
	var addrs []common.Address

	for i := 0; i < candidatesCount; i++ {
		addr := tests.GetRandAddr()

		if i == txCandidate {
			key, _ := crypto.GenerateKey()
			a := crypto.PubkeyToAddress(key.PublicKey)
			tx := &types.Transaction{
				Payload: random.GetRandomBytes(common.HashLength),
				Type:    types.SubmitAnswersHashTx,
			}
			signed, _ := types.SignTx(tx, key)
			em.newTx(signed)
			addrs = append(addrs, a)
		} else {
			addrs = append(addrs, addr)
		}
	}

	additional := []common.Address{}

	for i := 0; i < candidatesCount; i++ {
		if i%2 == 0 {
			additional = append(additional, addrs[i])
		}
	}

	em.NewFlipsKey(addrs[txCandidate])
	em.NewFlipKeyPackage(addrs[txCandidate])
	time.Sleep(2 * time.Second)
	em.NewFlipsKey(addrs[delayedKeyAuthor])
	em.NewFlipKeyPackage(addrs[delayedKeyPackageAuthor])

	m := em.CalculateBitmap(addrs, additional, func(a common.Address) uint8 {
		if a == addrs[delayedKeyAuthor] || a == addrs[txCandidate] || a == addrs[delayedKeyPackageAuthor] {
			return 1
		}
		return 0
	})

	buf := new(bytes.Buffer)

	m.WriteTo(buf)
	bytesArray := buf.Bytes()
	fmt.Printf("size of bitmap for %v candidates is %v bytes\n", candidatesCount, len(bytesArray))

	rmap := common.NewBitmap(candidatesCount)
	rmap.Read(bytesArray)

	require.True(rmap.Contains(txCandidate))
	require.False(rmap.Contains(delayedKeyAuthor))
	require.False(rmap.Contains(delayedKeyPackageAuthor))

	for i := 0; i < candidatesCount; i++ {
		if i%2 == 0 && i != delayedKeyAuthor && i != delayedKeyPackageAuthor {
			require.True(rmap.Contains(uint32(i)))
		} else if i != txCandidate {
			require.False(rmap.Contains(uint32(i)))
		}
	}
}

func TestEvidenceMap_CalculateApprovedCandidates(t *testing.T) {
	require := require.New(t)

	bus := eventbus.New()
	em := NewEvidenceMap(bus)

	const candidatesCount = 3
	var candidates []common.Address

	for i := 0; i < candidatesCount; i++ {
		addr := tests.GetRandAddr()
		candidates = append(candidates, addr)
	}
	// first map
	var maps [][]byte
	m := common.NewBitmap(candidatesCount)
	m.Add(0)
	m.Add(1)
	m.Add(2)

	buf := new(bytes.Buffer)
	m.WriteTo(buf)
	maps = append(maps, buf.Bytes())

	// second map
	m = common.NewBitmap(candidatesCount)
	m.Add(0)
	m.Add(2)

	buf = new(bytes.Buffer)
	m.WriteTo(buf)
	maps = append(maps, buf.Bytes())

	// third map
	m = common.NewBitmap(candidatesCount)
	m.Add(0)

	buf = new(bytes.Buffer)
	m.WriteTo(buf)
	maps = append(maps, buf.Bytes())

	// act
	appproved := em.CalculateApprovedCandidates(candidates, maps)

	// assert
	require.Len(appproved, 2)

	require.Contains(appproved, candidates[0])
	require.Contains(appproved, candidates[2])

}

func encodeBitmap(size uint32, values ...uint32) []byte {
	m := common.NewBitmap(size)
	for _, v := range values {
		m.Add(v)
	}
	buf := new(bytes.Buffer)
	m.WriteTo(buf)
	return buf.Bytes()
}

// Maps that read as no votes still count toward the majority, as in v1.1.2.
func TestEvidenceMap_CalculateApprovedCandidatesCountsMapsWithoutVotes(t *testing.T) {
	require := require.New(t)
	em := NewEvidenceMap(eventbus.New())

	const candidatesCount = 3
	var candidates []common.Address
	for i := 0; i < candidatesCount; i++ {
		candidates = append(candidates, tests.GetRandAddr())
	}

	maps := [][]byte{
		encodeBitmap(candidatesCount, 0, 1),
		encodeBitmap(candidatesCount, 0, 1),
		encodeBitmap(candidatesCount, 0),
		{0x03}, // unknown encoding with no bits
		{0x01}, // truncated roaring bitmap
		nil,    // empty payload
	}

	// 6 maps: a candidate needs 4 votes.
	approved := em.CalculateApprovedCandidates(candidates, maps)
	require.Empty(approved)

	// Without the maps that read as no votes, candidates 0 and 1 have a majority.
	approved = em.CalculateApprovedCandidates(candidates, maps[:3])
	require.Equal([]common.Address{candidates[0], candidates[1]}, approved)
}

// Maps that v1.1.2 reads although they were not written by Bitmap.WriteTo: two maps vote for candidate 0, two odd
// maps for candidate 1. v1.1.2 counts all four maps, so neither candidate has the 3 votes it needs.
func TestEvidenceMap_CalculateApprovedCandidatesAsV112(t *testing.T) {
	const candidatesCount = 15
	var candidates []common.Address
	for i := 0; i < candidatesCount; i++ {
		candidates = append(candidates, tests.GetRandAddr())
	}
	roaringWithExtraByte := new(bytes.Buffer)
	roaringWithExtraByte.WriteByte(0x01)
	rb := roaring.NewBitmap()
	rb.Add(1)
	_, err := rb.WriteTo(roaringWithExtraByte)
	require.NoError(t, err)
	roaringWithExtraByte.WriteByte(0xaa)

	for name, odd := range map[string][]byte{
		"first byte 0x03":             {0x03, 0x02},
		"big-integer bit past size":   {0x02, 0x10, 0x00, 0x02},
		"roaring bitmap + extra byte": roaringWithExtraByte.Bytes(),
	} {
		t.Run(name, func(t *testing.T) {
			em := NewEvidenceMap(eventbus.New())
			maps := [][]byte{encodeBitmap(candidatesCount, 0), encodeBitmap(candidatesCount, 0), odd, odd}
			require.Empty(t, em.CalculateApprovedCandidates(candidates, maps))
		})
	}
}

// A map whose values also cover a wide range past the candidates gives its candidates' votes as any other map: three
// such maps of five approve candidate 2.
func TestEvidenceMap_CalculateApprovedCandidatesWithValuesPastCandidates(t *testing.T) {
	const candidatesCount = 15
	var candidates []common.Address
	for i := 0; i < candidatesCount; i++ {
		candidates = append(candidates, tests.GetRandAddr())
	}
	rb := roaring.NewBitmap()
	rb.Add(2)
	rb.AddRange(1<<16, 1<<16+46*65536)
	rb.RunOptimize()
	wide := new(bytes.Buffer)
	wide.WriteByte(0x01)
	_, err := rb.WriteTo(wide)
	require.NoError(t, err)

	em := NewEvidenceMap(eventbus.New())
	maps := [][]byte{wide.Bytes(), wide.Bytes(), wide.Bytes(), nil, nil}
	require.Equal(t, []common.Address{candidates[2]}, em.CalculateApprovedCandidates(candidates, maps))
}

// A roaring array container listing one candidate many times gives that candidate as many votes, as in v1.1.2: one
// map of five approves candidate 5 alone.
func TestEvidenceMap_CalculateApprovedCandidatesCountsRepeatedValue(t *testing.T) {
	const candidatesCount = 15
	var candidates []common.Address
	for i := 0; i < candidatesCount; i++ {
		candidates = append(candidates, tests.GetRandAddr())
	}
	repeated := new(bytes.Buffer)
	repeated.WriteByte(0x01)
	for _, v := range []uint32{12346, 1} { // no run containers, one container
		repeated.Write([]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)})
	}
	repeated.Write([]byte{0, 0, 2, 0}) // key 0, 3 values
	repeated.Write([]byte{0, 0, 0, 0}) // offset
	repeated.Write([]byte{5, 0, 5, 0, 5, 0})

	em := NewEvidenceMap(eventbus.New())
	maps := [][]byte{repeated.Bytes(), nil, nil, nil, nil}
	require.Equal(t, []common.Address{candidates[5]}, em.CalculateApprovedCandidates(candidates, maps))
}

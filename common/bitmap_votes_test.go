package common

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/RoaringBitmap/roaring"
	"github.com/stretchr/testify/require"
)

// listedVotes counts what Read followed by ToArray lists below size.
func listedVotes(data []byte, size uint32) []int {
	score := make([]int, size)
	bitmap := NewBitmap(size)
	bitmap.Read(data)
	for _, v := range bitmap.ToArray() {
		if v < size {
			score[v]++
		}
	}
	return score
}

func roaringBytes(rb *roaring.Bitmap) []byte {
	buf := new(bytes.Buffer)
	buf.WriteByte(serializeDefault)
	_, _ = rb.WriteTo(buf)
	return buf.Bytes()
}

func addVotes(data []byte, size uint32) []int {
	score := make([]int, size)
	AddVotes(data, score)
	return score
}

// A bitmap holding one container of the given kind and key; runs are (start, length - 1) pairs.
func oneContainer(key uint16, declared int, run bool, body []uint16, words []uint64) []byte {
	buf := new(bytes.Buffer)
	buf.WriteByte(serializeDefault)
	if run {
		_ = binary.Write(buf, binary.LittleEndian, uint32(roaringCookie))
		buf.WriteByte(1)
	} else {
		_ = binary.Write(buf, binary.LittleEndian, uint32(roaringCookieNoRun))
		_ = binary.Write(buf, binary.LittleEndian, uint32(1))
	}
	_ = binary.Write(buf, binary.LittleEndian, key)
	_ = binary.Write(buf, binary.LittleEndian, uint16(declared-1))
	if !run {
		_ = binary.Write(buf, binary.LittleEndian, uint32(0))
	}
	if run {
		_ = binary.Write(buf, binary.LittleEndian, uint16(len(body)/2))
	}
	_ = binary.Write(buf, binary.LittleEndian, body)
	if words != nil {
		_ = binary.Write(buf, binary.LittleEndian, words)
	}
	return buf.Bytes()
}

func TestAddVotes_MatchesListing(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 300; i++ {
		size := uint32(1 + rng.Intn(140000))
		rb := roaring.New()
		for j := 0; j < rng.Intn(3000); j++ {
			rb.Add(uint32(rng.Intn(int(size) + 70000)))
		}
		if rng.Intn(2) == 0 {
			from := uint64(rng.Intn(200000))
			rb.AddRange(from, from+uint64(rng.Intn(100000)))
		}
		if rng.Intn(2) == 0 {
			rb.RunOptimize()
		}
		data := roaringBytes(rb)
		require.Equal(t, listedVotes(data, size), addVotes(data, size), "case %d", i)
	}
	for _, data := range [][]byte{nil, {serializeDefault}, {serializeDefault, 1, 2, 3}, {0x2, 0xff, 0x01}} {
		require.Equal(t, listedVotes(data, 20), addVotes(data, 20))
	}
}

// Values far beyond the candidates do not change the votes a map gives the candidates.
func TestAddVotes_ValuesBeyondTheCandidates(t *testing.T) {
	rb := roaring.New()
	rb.Add(2)
	rb.Add(7)
	rb.AddRange(1<<17, 1<<17+1000*65536)
	rb.RunOptimize()
	score := addVotes(roaringBytes(rb), 15)
	want := make([]int, 15)
	want[2], want[7] = 1, 1
	require.Equal(t, want, score)
}

// A run lists start..start+length even past 0xffff, and each value counts as many times as runs list it.
func TestAddVotes_Runs(t *testing.T) {
	data := oneContainer(0, 1, true, []uint16{65530, 19, 65533, 1}, nil)
	require.Equal(t, listedVotes(data, 70000), addVotes(data, 70000))
	score := addVotes(data, 70000)
	require.Equal(t, 1, score[65530])
	require.Equal(t, 2, score[65534])
	require.Equal(t, 1, score[65549])
	require.Equal(t, 0, score[65550])
}

// A bitmap container that declares more values than it holds lists the rest as 0.
func TestAddVotes_BitmapContainerDeclaringMoreValues(t *testing.T) {
	words := make([]uint64, roaringBitmapWords)
	words[0] = 0b1010
	data := oneContainer(0, 5000, false, nil, words)
	score := addVotes(data, 10)
	require.Equal(t, listedVotes(data, 10), score)
	require.Equal(t, 4998, score[0])
	require.Equal(t, 1, score[1])
	require.Equal(t, 1, score[3])
}

// A bitmap container that holds more values than it declares gives no votes.
func TestAddVotes_BitmapContainerHoldingMoreValues(t *testing.T) {
	words := make([]uint64, roaringBitmapWords)
	for i := range words {
		words[i] = ^uint64(0)
	}
	data := oneContainer(0, 5000, false, nil, words)
	require.Equal(t, make([]int, 10), addVotes(data, 10))
}

// An array container lists its entries as stored, repeated ones included.
func TestAddVotes_RepeatedEntries(t *testing.T) {
	data := oneContainer(0, 4, false, []uint16{5, 5, 5, 1}, nil)
	score := addVotes(data, 10)
	require.Equal(t, listedVotes(data, 10), score)
	require.Equal(t, 3, score[5])
}

package common

import (
	"bytes"
	"math/big"

	"github.com/RoaringBitmap/roaring"
)

const (
	serializeDefault = byte(0x1)
	serializeBigInt  = byte(0x2)
)

type Bitmap struct {
	rmap *roaring.Bitmap
	size uint32
}

func NewBitmap(size uint32) *Bitmap {
	return &Bitmap{size: size, rmap: roaring.NewBitmap()}
}

func (m *Bitmap) Add(value uint32) {
	if value >= m.size {
		panic("value is out of range")
	}
	m.rmap.Add(value)

}

func (m *Bitmap) Contains(value uint32) bool {
	return m.rmap.Contains(value)
}

func (m *Bitmap) WriteTo(buffer *bytes.Buffer) {
	if m.rmap.HasRunCompression() {
		m.rmap.RunOptimize()
	}
	if m.rmap.GetSerializedSizeInBytes() > uint64(m.size/8+1) {
		bits := big.NewInt(0)
		buffer.WriteByte(serializeBigInt)
		for _, v := range m.rmap.ToArray() {
			t := big.NewInt(1)
			bits.Or(bits, t.Lsh(t, uint(v)))
		}
		buffer.Write(bits.Bytes())
	} else {
		buffer.WriteByte(serializeDefault)
		m.rmap.WriteTo(buffer)
	}
}

// Read decodes a bitmap as idena-go v1.1.2 does (common/bitmap.go at 938be81), so that every node reads an evidence
// map to the same votes: a first byte other than serializeDefault selects the big-integer encoding, big-integer bits
// at or above the size are left out, roaring values are kept as decoded and bytes after the roaring bitmap are
// ignored. An empty input reads as an empty bitmap, and so does a roaring bitmap that does not decode to the end.
// bitmap_legacy_vectors_test.go holds inputs decoded by the v1.1.2 code.
func (m *Bitmap) Read(data []byte) {
	m.rmap = roaring.NewBitmap()
	if len(data) == 0 {
		return
	}
	if data[0] == serializeDefault {
		if _, err := m.rmap.ReadFrom(bytes.NewBuffer(data[1:])); err != nil {
			m.rmap = roaring.NewBitmap()
		}
		return
	}
	bits := new(big.Int).SetBytes(data[1:])
	for i := uint32(0); i < m.size; i++ {
		if bits.Bit(int(i)) == 1 {
			m.rmap.Add(i)
		}
	}
}

func (m *Bitmap) ToArray() []uint32 {
	return m.rmap.ToArray()
}

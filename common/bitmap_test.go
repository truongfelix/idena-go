package common

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/RoaringBitmap/roaring"
	"github.com/stretchr/testify/require"
)

func TestBitmap_Size(t *testing.T) {
	size := uint32(8888)

	rmap := roaring.NewBitmap()
	bitmap := NewBitmap(size)

	for i := uint32(0); i < size; i++ {
		if i%3 == 0 {
			rmap.Add(i)
			bitmap.Add(i)
		}
	}

	buf := new(bytes.Buffer)
	rmap.WriteTo(buf)

	buf2 := new(bytes.Buffer)
	bitmap.WriteTo(buf2)

	require.True(t, buf.Len() >= buf2.Len())
	require.Equal(t, buf2.Len(), int(size/8+1))

	rmap = roaring.NewBitmap()
	bitmap = NewBitmap(size)

	rmap.Add(0)
	bitmap.Add(0)
	rmap.Add(size - 1)
	bitmap.Add(size - 1)

	buf = new(bytes.Buffer)
	rmap.WriteTo(buf)

	buf2 = new(bytes.Buffer)
	bitmap.WriteTo(buf2)
	require.True(t, buf.Len()+1 >= buf2.Len())
}

func TestBitmap_Contains(t *testing.T) {
	size := uint32(8888)
	bitmap := NewBitmap(size)

	for i := uint32(0); i < size; i++ {
		if i%3 == 0 {
			bitmap.Add(i)
		}
	}

	buf := new(bytes.Buffer)
	bitmap.WriteTo(buf)

	bitmap.Read(buf.Bytes())

	for i := uint32(0); i < size; i++ {
		if i%3 == 0 {
			require.True(t, bitmap.Contains(i))
		} else {
			require.False(t, bitmap.Contains(i))
		}
	}

	size = 2
	bitmap = NewBitmap(size)
	bitmap.Add(1)

	require.True(t, bitmap.Contains(1))

	buf = new(bytes.Buffer)
	bitmap.WriteTo(buf)

	bitmap.Read(buf.Bytes())

	require.True(t, bitmap.Contains(1))

	require.False(t, bitmap.Contains(0))
}

func TestBitmap_Serialize(t *testing.T) {
	size := uint32(8)
	bitmap := NewBitmap(size)

	for i := uint32(0); i < size; i++ {
		if i%3 == 0 {
			bitmap.Add(i)
		}
	}

	buf := new(bytes.Buffer)
	bitmap.WriteTo(buf)

	bitmap2 := NewBitmap(size)
	bitmap2.Read(buf.Bytes())

	require.True(t, bitmap.rmap.Equals(bitmap2.rmap))
}

func TestBitmap_SerializeSparseRoaringEncoding(t *testing.T) {
	const size = uint32(8888)
	bitmap := NewBitmap(size)
	bitmap.Add(0)
	bitmap.Add(size - 1)

	buf := new(bytes.Buffer)
	bitmap.WriteTo(buf)
	require.Equal(t, serializeDefault, buf.Bytes()[0])

	decoded := NewBitmap(size)
	decoded.Read(buf.Bytes())
	require.True(t, bitmap.rmap.Equals(decoded.rmap))
}

func TestBitmapSerializationMatchesLegacyNode(t *testing.T) {
	const size = uint32(8888)
	sparse := NewBitmap(size)
	sparse.Add(0)
	sparse.Add(size - 1)
	sparseData := new(bytes.Buffer)
	sparse.WriteTo(sparseData)
	require.Equal(t, "013a3000000100000000000100100000000000b722", hex.EncodeToString(sparseData.Bytes()))

	dense := NewBitmap(size)
	for i := uint32(0); i < size; i++ {
		if i%3 == 0 {
			dense.Add(i)
		}
	}
	denseData := new(bytes.Buffer)
	dense.WriteTo(denseData)
	digest := sha256.Sum256(denseData.Bytes())
	require.Equal(t, 1112, denseData.Len())
	require.Equal(t, "9ccd4716743d33c9efe2f3120c41ec1b3ba3f61c3ccfa7eebc5f9674cfa43303", hex.EncodeToString(digest[:]))
}

func TestBitmap_ReadKeepsLegacyLenientDecoding(t *testing.T) {
	const size = uint32(8)

	outOfRange := roaring.NewBitmap()
	outOfRange.Add(1)
	outOfRange.Add(size)
	outOfRangeData := new(bytes.Buffer)
	require.NoError(t, outOfRangeData.WriteByte(serializeDefault))
	_, err := outOfRange.WriteTo(outOfRangeData)
	require.NoError(t, err)

	tests := []struct {
		name     string
		data     []byte
		expected []uint32
	}{
		{name: "empty", data: nil, expected: []uint32{}},
		{name: "unknown encoding is read as big integer", data: []byte{0xff, 0x05}, expected: []uint32{0, 2}},
		{name: "big integer bits beyond size are ignored", data: []byte{serializeBigInt, 0x01, 0x02}, expected: []uint32{1}},
		{name: "truncated roaring bitmap", data: []byte{serializeDefault}, expected: []uint32{}},
		{name: "roaring values beyond size are kept", data: outOfRangeData.Bytes(), expected: []uint32{1, size}},
		{name: "trailing bytes are ignored", data: append(append([]byte{}, outOfRangeData.Bytes()...), 0xaa, 0xbb), expected: []uint32{1, size}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bitmap := NewBitmap(size)
			bitmap.Add(3)

			bitmap.Read(test.data)
			require.Equal(t, test.expected, bitmap.ToArray())
		})
	}
}

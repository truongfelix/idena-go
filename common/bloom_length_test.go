package common

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

// A TxBloom whose length is not a canonical filter size is refused with an error.
func TestNewSerializableBFFromData_RejectsMalformedLength(t *testing.T) {
	// zero, short, and non-multiple-of-8 lengths
	for _, n := range []int{0, 1, 2, 3, 4, 5, 6, 7, 9, 15, 17, 23} {
		bf, err := NewSerializableBFFromData(make([]byte, n))
		require.Error(t, err, "length %d must be rejected", n)
		require.Nil(t, bf, "length %d", n)
	}
	// aligned (multiple of 8) but not a power-of-two word count, or larger than any filter bloomLength builds
	for _, n := range []int{24, 40, 72, 520, 1024} {
		bf, err := NewSerializableBFFromData(make([]byte, n))
		require.Error(t, err, "aligned non-canonical length %d must be rejected", n)
		require.Nil(t, bf, "length %d", n)
	}
}

// Every size a real filter can have (8, 16, 32, 64, 128, 256, 512 bytes) is accepted and answers Has.
func TestNewSerializableBFFromData_AcceptsCanonicalSizes(t *testing.T) {
	for _, bytesLen := range []int{8, 16, 32, 64, 128, 256, 512} {
		bf, err := NewSerializableBFFromData(make([]byte, bytesLen))
		require.NoError(t, err, "canonical length %d", bytesLen)
		require.NotNil(t, bf)
		require.False(t, bf.Has([]byte{0xaa, 0xbb}), "length %d", bytesLen)
	}
}

// A valid filter built by NewSerializableBF still round-trips and tests as before, at every element count.
func TestNewSerializableBFFromData_ValidRoundTrip(t *testing.T) {
	for _, n := range []int{1, 10, 100, 1000} {
		bf := NewSerializableBF(n)
		present := []byte{byte(n), 0x01, 0x02}
		bf.Add(present)
		data, err := bf.Serialize()
		require.NoError(t, err)
		require.Positive(t, len(data))
		require.Zero(t, len(data)%binary.Size(uint64(0)), "n=%d length %d", n, len(data))

		decoded, err := NewSerializableBFFromData(data)
		require.NoError(t, err)
		require.True(t, decoded.Has(present), "n=%d", n)
	}
}

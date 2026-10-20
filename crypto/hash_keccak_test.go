package crypto

import (
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
	xsha3 "golang.org/x/crypto/sha3"
)

// Hash must match the maintained Keccak-256 for inputs of every length, including those that fill the sponge rate
// (136 bytes) partially or exactly.
func TestHash_MatchesMaintainedKeccak(t *testing.T) {
	ref := func(data []byte) [32]byte {
		h := xsha3.NewLegacyKeccak256()
		h.Write(data)
		var out [32]byte
		copy(out[:], h.Sum(nil))
		return out
	}
	for n := 0; n <= 420; n++ {
		data := make([]byte, n)
		_, err := rand.Read(data)
		require.NoError(t, err)
		require.Equal(t, ref(data), Hash(data), "length %d", n)
	}
	for i := 0; i < 5000; i++ {
		data := make([]byte, 1+i%300)
		_, err := rand.Read(data)
		require.NoError(t, err)
		require.Equal(t, ref(data), Hash(data), "random len %d", len(data))
	}
}

package p256

import (
	"crypto/ecdsa"
	"crypto/rand"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

func testKey(t *testing.T) (*ecdsa.PrivateKey, *PublicKey) {
	ek, err := ecdsa.GenerateKey(curve, rand.Reader)
	require.NoError(t, err)
	return ek, &PublicKey{&ek.PublicKey}
}

// A proof produced by Evaluate still verifies to the same index: ProofToHash is unchanged on well-formed proofs.
func TestProofToHash_ValidProofUnchanged(t *testing.T) {
	for i := 0; i < 300; i++ {
		ek, pub := testKey(t)
		m := make([]byte, 40)
		rand.Read(m)
		wantIdx, proof := PrivateKey{ek}.Evaluate(m)

		idx, err := pub.ProofToHash(m, proof)
		require.NoError(t, err)
		require.Equal(t, wantIdx, idx)
	}
}

// Proofs that the VRF equations are not defined on are reported as invalid.
func TestProofToHash_RejectsProofsOutsideDomain(t *testing.T) {
	ek, pub := testKey(t)
	m := []byte("message")

	nBytes := func(v *big.Int) []byte { b := make([]byte, 32); copy(b[32-len(v.Bytes()):], v.Bytes()); return b }
	onCurve := func() []byte {
		a := make([]byte, 32)
		rand.Read(a)
		x, y := curve.ScalarBaseMult(a)
		return curve.Marshal(x, y)
	}
	zero := make([]byte, 32)
	some := nBytes(big.NewInt(42))
	mk := func(s, tt, vrf []byte) []byte { return append(append(append([]byte{}, s...), tt...), vrf...) }

	// s and t with [t]G and [s][k]G sharing an x-coordinate (t = s*k mod N makes them the same point).
	sShare := big.NewInt(3)
	tShare := new(big.Int).Mod(new(big.Int).Mul(sShare, ek.D), params.N)

	cases := map[string][]byte{
		"zero s":                    mk(zero, some, onCurve()),
		"zero t":                    mk(some, zero, onCurve()),
		"s not below order":         mk(nBytes(params.N), some, onCurve()),
		"t not below order":         mk(some, nBytes(params.N), onCurve()),
		"point not on curve":        mk(some, some, curve.Marshal(big.NewInt(1), big.NewInt(1))),
		"point at origin":           mk(some, some, curve.Marshal(big.NewInt(0), big.NewInt(0))),
		"summands share an x-coord": mk(nBytes(sShare), nBytes(tShare), onCurve()),
		"wrong length short":        make([]byte, 128),
		"wrong length long":         make([]byte, 130),
		"empty":                     nil,
	}
	for name, proof := range cases {
		t.Run(name, func(t *testing.T) {
			idx, err := pub.ProofToHash(m, proof)
			require.Equal(t, ErrInvalidVRF, err)
			require.Equal(t, [32]byte{}, idx)
		})
	}
}

func FuzzProofToHash(f *testing.F) {
	ek, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		f.Fatal(err)
	}
	pub := &PublicKey{&ek.PublicKey}
	_, p := PrivateKey{ek}.Evaluate([]byte("seed"))
	f.Add([]byte("seed"), p)
	f.Add([]byte("m"), make([]byte, 129))
	f.Fuzz(func(t *testing.T, m, proof []byte) {
		_, _ = pub.ProofToHash(m, proof)
	})
}

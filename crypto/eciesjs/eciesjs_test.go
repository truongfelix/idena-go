package eciesjs

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/idena-network/idena-go/crypto"
	"github.com/stretchr/testify/require"
)

type vector struct {
	PrivateKey string `json:"privateKey"`
	PublicKey  string `json:"publicKey"`
	Message    string `json:"message"`
	Payload    string `json:"payload"`
}

// testdata/eciesjs-0.5.0.json: payloads made by eciesjs 0.5.0 (encrypt, default config) for fixed keys.
func readVectors(t *testing.T) []vector {
	data, err := os.ReadFile("testdata/eciesjs-0.5.0.json")
	require.NoError(t, err)
	var vectors []vector
	require.NoError(t, json.Unmarshal(data, &vectors))
	require.Len(t, vectors, 2)
	return vectors
}

func TestDecryptEciesjsPayloads(t *testing.T) {
	for _, v := range readVectors(t) {
		priv, err := crypto.HexToECDSA(v.PrivateKey)
		require.NoError(t, err)
		require.Equal(t, v.PublicKey, hex.EncodeToString(crypto.FromECDSAPub(&priv.PublicKey)))
		payload, err := base64.StdEncoding.DecodeString(v.Payload)
		require.NoError(t, err)
		msg, err := Decrypt(priv, payload)
		require.NoError(t, err)
		require.Equal(t, v.Message, string(msg))
	}
}

func TestDecryptRefusesOtherKeysAndChanges(t *testing.T) {
	vectors := readVectors(t)
	payload, err := base64.StdEncoding.DecodeString(vectors[0].Payload)
	require.NoError(t, err)
	other, err := crypto.HexToECDSA(vectors[1].PrivateKey)
	require.NoError(t, err)
	_, err = Decrypt(other, payload)
	require.ErrorIs(t, err, ErrInvalidPayload)

	priv, err := crypto.HexToECDSA(vectors[0].PrivateKey)
	require.NoError(t, err)
	for _, at := range []int{0, 64, 65, 81, 97, len(payload) - 1} {
		changed := append([]byte(nil), payload...)
		changed[at] ^= 1
		_, err = Decrypt(priv, changed)
		require.ErrorIs(t, err, ErrInvalidPayload, "byte %d changed", at)
	}
	_, err = Decrypt(priv, payload[:65+16+15])
	require.ErrorIs(t, err, ErrInvalidPayload)
}

func TestEncryptRoundTrip(t *testing.T) {
	priv, err := crypto.GenerateKey()
	require.NoError(t, err)
	for _, msg := range []string{"", "hello", string(make([]byte, 5000))} {
		payload, err := Encrypt(rand.Reader, &priv.PublicKey, []byte(msg))
		require.NoError(t, err)
		require.Len(t, payload, 65+16+16+len(msg))
		require.Equal(t, byte(4), payload[0], "uncompressed ephemeral key")
		got, err := Decrypt(priv, payload)
		require.NoError(t, err)
		require.Equal(t, msg, string(got))
	}
}

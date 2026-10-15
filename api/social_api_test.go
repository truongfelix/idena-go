package api

import (
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/idena-network/idena-go/common/hexutil"
	"github.com/idena-network/idena-go/crypto"
	"github.com/idena-network/idena-go/crypto/eciesjs"
	"github.com/idena-network/idena-go/secstore"
	"github.com/stretchr/testify/require"
)

func socialApiWithKey(t *testing.T, privateKeyHex string) *SocialApi {
	key, err := hex.DecodeString(privateKeyHex)
	require.NoError(t, err)
	store := secstore.NewSecStore()
	store.AddKey(key)
	t.Cleanup(store.Destroy)
	return NewSocialApi(NewBaseApi(nil, nil, nil, store, nil))
}

func TestSocialEncryptMakesTheMessageParts(t *testing.T) {
	api := socialApiWithKey(t, "0202020202020202020202020202020202020202020202020202020202020202")
	alice, err := crypto.GenerateKey()
	require.NoError(t, err)
	bob, err := crypto.GenerateKey()
	require.NoError(t, err)
	uncompressed := crypto.FromECDSAPub(&alice.PublicKey)
	parts, err := api.Encrypt(SocialEncryptArgs{
		PublicKeys: []hexutil.Bytes{uncompressed, crypto.CompressPubkey(&bob.PublicKey)},
		Message:    "hello",
	})
	require.NoError(t, err)
	require.Len(t, parts, 3, "the node's own copy, then one per recipient")

	for i, key := range []*ecdsa.PrivateKey{alice, bob} {
		payload, err := base64.StdEncoding.DecodeString(parts[i+1])
		require.NoError(t, err)
		msg, err := eciesjs.Decrypt(key, payload)
		require.NoError(t, err)
		require.Equal(t, "hello", string(msg))
	}

	got, err := api.Decrypt(SocialDecryptArgs{Parts: parts})
	require.NoError(t, err)
	require.Equal(t, &SocialDecrypted{Indexes: []int{0}, Message: "hello"}, got)

	// 64 bytes: a key without its 0x04 prefix.
	_, err = api.Encrypt(SocialEncryptArgs{PublicKeys: []hexutil.Bytes{uncompressed[1:]}, Message: "x"})
	require.NoError(t, err)
	_, err = api.Encrypt(SocialEncryptArgs{Message: "x"})
	require.Error(t, err, "no recipient")
	_, err = api.Encrypt(SocialEncryptArgs{PublicKeys: []hexutil.Bytes{uncompressed[:40]}, Message: "x"})
	require.Error(t, err, "a key of 40 bytes")
}

func TestSocialDecryptReadsAnEciesjsMessage(t *testing.T) {
	data, err := os.ReadFile("../crypto/eciesjs/testdata/eciesjs-0.5.0.json")
	require.NoError(t, err)
	var vectors []struct {
		PrivateKey string `json:"privateKey"`
		Message    string `json:"message"`
		Payload    string `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(data, &vectors))
	api := socialApiWithKey(t, vectors[0].PrivateKey)

	// The node's part is the second; the others are for another key, or not base64.
	got, err := api.Decrypt(SocialDecryptArgs{Parts: []string{vectors[1].Payload, vectors[0].Payload, "not base64!"}})
	require.NoError(t, err)
	require.Equal(t, &SocialDecrypted{Indexes: []int{1}, Message: vectors[0].Message}, got)

	none, err := api.Decrypt(SocialDecryptArgs{Parts: []string{vectors[1].Payload}})
	require.NoError(t, err)
	require.Nil(t, none, "no part for the node's key")
}

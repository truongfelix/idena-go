package api

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/idena-network/idena-go/common/hexutil"
	"github.com/idena-network/idena-go/crypto"
	"github.com/idena-network/idena-go/crypto/eciesjs"
)

// The most recipients of one message, parts tried in one decrypt call, and bytes of one message.
const (
	maxSocialRecipients = 64
	maxSocialParts      = 256
	maxSocialMessage    = 1 << 20
)

// SocialApi encrypts and decrypts idena.social private messages with the node's key, which stays in the node.
type SocialApi struct {
	baseApi *BaseApi
}

func NewSocialApi(baseApi *BaseApi) *SocialApi {
	return &SocialApi{baseApi}
}

type SocialEncryptArgs struct {
	// The recipients' public keys: 65 bytes (uncompressed), 33 (compressed) or 64 (without the 0x04 prefix).
	PublicKeys []hexutil.Bytes `json:"publicKeys"`
	Message    string          `json:"message"`
}

// Encrypt returns the parts of an idena.social message (the sendMessage argument's "message"): the message
// encrypted for the node's own key, then for each of the public keys in their order, each in base64.
func (api *SocialApi) Encrypt(args SocialEncryptArgs) ([]string, error) {
	if len(args.PublicKeys) == 0 || len(args.PublicKeys) > maxSocialRecipients {
		return nil, fmt.Errorf("from 1 to %d public keys", maxSocialRecipients)
	}
	if len(args.Message) > maxSocialMessage {
		return nil, fmt.Errorf("message larger than %d bytes", maxSocialMessage)
	}
	own, err := crypto.UnmarshalPubkey(api.baseApi.secStore.GetPubKey())
	if err != nil {
		return nil, err
	}
	keys := []*ecdsa.PublicKey{own}
	for i, raw := range args.PublicKeys {
		key, err := parseSocialPublicKey(raw)
		if err != nil {
			return nil, fmt.Errorf("public key %d: %v", i, err)
		}
		keys = append(keys, key)
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		payload, err := eciesjs.Encrypt(rand.Reader, key, []byte(args.Message))
		if err != nil {
			return nil, err
		}
		parts = append(parts, base64.StdEncoding.EncodeToString(payload))
	}
	return parts, nil
}

type SocialDecryptArgs struct {
	// The parts of a message (the sendMessage argument's "message"), in base64.
	Parts []string `json:"parts"`
}

type SocialDecrypted struct {
	// The parts the node's key decrypted, from 0.
	Indexes []int `json:"indexes"`
	// The first of them decrypted.
	Message string `json:"message"`
}

// Decrypt tries each part with the node's key; null when none is for it. A part that is not base64 or not a
// payload for the key is skipped.
func (api *SocialApi) Decrypt(args SocialDecryptArgs) (*SocialDecrypted, error) {
	if len(args.Parts) > maxSocialParts {
		return nil, fmt.Errorf("at most %d parts", maxSocialParts)
	}
	var result *SocialDecrypted
	for i, part := range args.Parts {
		payload, err := base64.StdEncoding.DecodeString(part)
		if err != nil || len(payload) > maxSocialMessage+128 {
			continue
		}
		msg, err := api.baseApi.secStore.DecryptSocialMessage(payload)
		if err != nil {
			continue
		}
		if result == nil {
			result = &SocialDecrypted{Message: string(msg)}
		}
		result.Indexes = append(result.Indexes, i)
	}
	return result, nil
}

func parseSocialPublicKey(raw []byte) (*ecdsa.PublicKey, error) {
	switch len(raw) {
	case 65:
		return crypto.UnmarshalPubkey(raw)
	case 64:
		return crypto.UnmarshalPubkey(append([]byte{4}, raw...))
	case 33:
		return crypto.DecompressPubkey(raw)
	default:
		return nil, fmt.Errorf("%d bytes, not 33, 64 or 65", len(raw))
	}
}

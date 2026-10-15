// Package eciesjs encrypts and decrypts as the JavaScript library eciesjs 0.5 does with its default
// configuration, which idena.social uses for its messages: secp256k1, an uncompressed ephemeral public key,
// HKDF-SHA256 (no salt, no info) over the ephemeral public key and the uncompressed shared point, then
// AES-256-GCM with a 16-byte nonce. Payload: ephemeral public key (65) || nonce (16) || tag (16) || ciphertext.
package eciesjs

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"io"
	"math/big"

	"github.com/idena-network/idena-go/crypto"
)

const (
	publicKeySize = 65
	nonceSize     = 16
	tagSize       = 16
	keySize       = 32
)

// ErrInvalidPayload is returned for a payload too short, with an invalid ephemeral key, or that does not
// authenticate with the key.
var ErrInvalidPayload = errors.New("invalid ecies payload")

// Encrypt encrypts msg for the holder of pub, with randomness from rnd.
func Encrypt(rnd io.Reader, pub *ecdsa.PublicKey, msg []byte) ([]byte, error) {
	ephemeral, err := ecdsa.GenerateKey(crypto.S256(), rnd)
	if err != nil {
		return nil, err
	}
	ephemeralPub := crypto.FromECDSAPub(&ephemeral.PublicKey)
	key, err := sharedKey(ephemeralPub, ephemeral, pub)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rnd, nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, msg, nil) // ciphertext || tag
	out := make([]byte, 0, publicKeySize+nonceSize+len(sealed))
	out = append(out, ephemeralPub...)
	out = append(out, nonce...)
	out = append(out, sealed[len(msg):]...)
	return append(out, sealed[:len(msg)]...), nil
}

// Decrypt decrypts a payload made by Encrypt (or eciesjs) for priv's public key.
func Decrypt(priv *ecdsa.PrivateKey, payload []byte) ([]byte, error) {
	if len(payload) < publicKeySize+nonceSize+tagSize {
		return nil, ErrInvalidPayload
	}
	ephemeralPub := payload[:publicKeySize]
	pub, err := crypto.UnmarshalPubkey(ephemeralPub)
	if err != nil {
		return nil, ErrInvalidPayload
	}
	key, err := sharedKey(ephemeralPub, priv, pub)
	if err != nil {
		return nil, ErrInvalidPayload
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := payload[publicKeySize : publicKeySize+nonceSize]
	tag := payload[publicKeySize+nonceSize : publicKeySize+nonceSize+tagSize]
	ciphertext := payload[publicKeySize+nonceSize+tagSize:]
	sealed := make([]byte, 0, len(ciphertext)+tagSize)
	sealed = append(append(sealed, ciphertext...), tag...)
	msg, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, ErrInvalidPayload
	}
	return msg, nil
}

// sharedKey derives the AES key: HKDF-SHA256 over ephemeralPub (uncompressed) || priv * pub (uncompressed).
func sharedKey(ephemeralPub []byte, priv *ecdsa.PrivateKey, pub *ecdsa.PublicKey) ([]byte, error) {
	curve := crypto.S256()
	if pub.X == nil || pub.Y == nil || !curve.IsOnCurve(pub.X, pub.Y) {
		return nil, ErrInvalidPayload
	}
	x, y := curve.ScalarMult(pub.X, pub.Y, priv.D.FillBytes(make([]byte, 32)))
	if x == nil || (x.Sign() == 0 && y.Sign() == 0) {
		return nil, ErrInvalidPayload
	}
	shared := crypto.FromECDSAPub(&ecdsa.PublicKey{Curve: curve, X: new(big.Int).Set(x), Y: new(big.Int).Set(y)})
	master := make([]byte, 0, len(ephemeralPub)+len(shared))
	master = append(append(master, ephemeralPub...), shared...)
	return hkdf.Key(sha256.New, master, nil, "", keySize)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithNonceSize(block, nonceSize)
}

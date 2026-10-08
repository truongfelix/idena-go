// Copyright 2014 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package crypto

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/fileutil"
	"github.com/idena-network/idena-go/common/math"
	"github.com/idena-network/idena-go/crypto/sha3"
)

// maxPrivateKeyFileSize bounds a key file: 64 hex characters, then at most some trailing whitespace.
const maxPrivateKeyFileSize = 128

var (
	secp256k1N, _  = new(big.Int).SetString("fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141", 16)
	secp256k1halfN = new(big.Int).Div(secp256k1N, big.NewInt(2))
)

var errInvalidPubkey = errors.New("invalid secp256k1 public key")

// Keccak256 calculates and returns the Keccak256 hash of the input data.
func Keccak256(data ...[]byte) []byte {
	d := sha3.NewKeccak256()
	for _, b := range data {
		d.Write(b)
	}
	return d.Sum(nil)
}

// Keccak256Hash calculates and returns the Keccak256 hash of the input data,
// converting it to an internal Hash data structure.
func Keccak256Hash(data ...[]byte) (h common.Hash) {
	d := sha3.NewKeccak256()
	for _, b := range data {
		d.Write(b)
	}
	d.Sum(h[:0])
	return h
}

// Keccak512 calculates and returns the Keccak512 hash of the input data.
func Keccak512(data ...[]byte) []byte {
	d := sha3.NewKeccak512()
	for _, b := range data {
		d.Write(b)
	}
	return d.Sum(nil)
}

// ToECDSA creates a private key with the given D value.
func ToECDSA(d []byte) (*ecdsa.PrivateKey, error) {
	return toECDSA(d, true)
}

// ToECDSAUnsafe blindly converts a binary blob to a private key. It should almost
// never be used unless you are sure the input is valid and want to avoid hitting
// errors due to bad origin encoding (0 prefixes cut off).
func ToECDSAUnsafe(d []byte) *ecdsa.PrivateKey {
	priv, _ := toECDSA(d, false)
	return priv
}

// toECDSA creates a private key with the given D value. The strict parameter
// controls whether the key's length should be enforced at the curve size or
// it can also accept legacy encodings (0 prefixes).
func toECDSA(d []byte, strict bool) (*ecdsa.PrivateKey, error) {
	priv := new(ecdsa.PrivateKey)
	priv.PublicKey.Curve = S256()
	if strict && 8*len(d) != priv.Params().BitSize {
		return nil, fmt.Errorf("invalid length, need %d bits", priv.Params().BitSize)
	}
	priv.D = new(big.Int).SetBytes(d)

	// The priv.D must < N
	if priv.D.Cmp(secp256k1N) >= 0 {
		return nil, fmt.Errorf("invalid private key, >=N")
	}
	// The priv.D must not be zero or negative.
	if priv.D.Sign() <= 0 {
		return nil, fmt.Errorf("invalid private key, zero or negative")
	}

	priv.PublicKey.X, priv.PublicKey.Y = priv.PublicKey.Curve.ScalarBaseMult(d)
	if priv.PublicKey.X == nil {
		return nil, errors.New("invalid private key")
	}
	return priv, nil
}

// FromECDSA exports a private key into a binary dump.
func FromECDSA(priv *ecdsa.PrivateKey) []byte {
	if priv == nil {
		return nil
	}
	return math.PaddedBigBytes(priv.D, priv.Params().BitSize/8)
}

// UnmarshalPubkey converts bytes to a secp256k1 public key.
func UnmarshalPubkey(pub []byte) (*ecdsa.PublicKey, error) {
	x, y := elliptic.Unmarshal(S256(), pub)
	if x == nil {
		return nil, errInvalidPubkey
	}
	return &ecdsa.PublicKey{Curve: S256(), X: x, Y: y}, nil
}

func PubKeyBytesToAddress(b []byte) (common.Address, error) {
	p, err := UnmarshalPubkey(b)
	if err == nil {
		return PubkeyToAddress(*p), nil
	}
	return common.Address{}, nil
}

func FromECDSAPub(pub *ecdsa.PublicKey) []byte {
	if pub == nil || pub.X == nil || pub.Y == nil {
		return nil
	}
	return elliptic.Marshal(S256(), pub.X, pub.Y)
}

// HexToECDSA parses a secp256k1 private key.
func HexToECDSA(hexkey string) (*ecdsa.PrivateKey, error) {
	b, err := hex.DecodeString(hexkey)
	if err != nil {
		return nil, errors.New("invalid hex string")
	}
	return ToECDSA(b)
}

// LoadECDSA loads a secp256k1 private key from the given file.
func LoadECDSA(file string) (*ecdsa.PrivateKey, error) {
	info, err := os.Lstat(file)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("private key path is not a regular file: %q", file)
	}

	fd, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer fd.Close()
	openedInfo, err := fd.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("private key path changed while opening: %q", file)
	}
	if err := fd.Chmod(0600); err != nil {
		return nil, err
	}

	content, err := io.ReadAll(io.LimitReader(fd, maxPrivateKeyFileSize+1))
	if err != nil {
		return nil, err
	}
	defer zeroBytes(content)
	if len(content) > maxPrivateKeyFileSize {
		return nil, fmt.Errorf("invalid private key file length: more than %d bytes", maxPrivateKeyFileSize)
	}
	// The official node reads the first 64 characters of the file, so a key file written by hand or saved by an
	// editor can end with a line break: trailing whitespace is ignored, any other trailing data is refused.
	encoded := bytes.TrimRight(content, " \t\r\n")
	if len(encoded) != 64 {
		return nil, fmt.Errorf("invalid private key file length: got %d, want 64", len(encoded))
	}

	key := make([]byte, hex.DecodedLen(len(encoded)))
	defer zeroBytes(key)
	_, err = hex.Decode(key, encoded)
	if err != nil {
		return nil, err
	}
	return ToECDSA(key)
}

// SaveECDSA saves a secp256k1 private key to the given file with
// restrictive permissions. The key data is saved hex-encoded.
func SaveECDSA(file string, key *ecdsa.PrivateKey) error {
	if key == nil || key.D == nil || key.Curve != S256() || key.D.Sign() <= 0 || key.D.Cmp(secp256k1N) >= 0 {
		return errors.New("invalid secp256k1 private key")
	}
	rawKey := FromECDSA(key)
	defer zeroBytes(rawKey)
	encodedKey := make([]byte, hex.EncodedLen(len(rawKey)))
	defer zeroBytes(encodedKey)
	hex.Encode(encodedKey, rawKey)
	return fileutil.WriteFileAtomic(file, encodedKey, 0600)
}

func GenerateKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(S256(), rand.Reader)
}

func GenerateKeyFromSeed(reader io.Reader) (*ecdsa.PrivateKey, error) {
	// Preserve the Go 1.17 ecdsa.GenerateKey mapping used by the original
	// Idena releases. Flip-key derivation depends on this exact 40-byte mapping.
	params := S256().Params()
	seed := make([]byte, params.BitSize/8+8)
	if _, err := io.ReadFull(reader, seed); err != nil {
		return nil, err
	}

	k := new(big.Int).SetBytes(seed)
	nMinusOne := new(big.Int).Sub(params.N, big.NewInt(1))
	k.Mod(k, nMinusOne)
	k.Add(k, big.NewInt(1))
	return ToECDSA(math.PaddedBigBytes(k, params.BitSize/8))
}

// ValidateSignatureValues verifies whether the signature values are valid with
// the given chain rules. The v value is assumed to be either 0 or 1.
func ValidateSignatureValues(v byte, r, s *big.Int, homestead bool) bool {
	if r.Cmp(common.Big1) < 0 || s.Cmp(common.Big1) < 0 {
		return false
	}
	// reject upper range of s values (ECDSA malleability)
	// see discussion in secp256k1/libsecp256k1/include/secp256k1.h
	if homestead && s.Cmp(secp256k1halfN) > 0 {
		return false
	}
	// Frontier: allow s to be in full N range
	return r.Cmp(secp256k1N) < 0 && s.Cmp(secp256k1N) < 0 && (v == 0 || v == 1)
}

func PubkeyToAddress(p ecdsa.PublicKey) common.Address {
	pubBytes := FromECDSAPub(&p)
	return common.BytesToAddress(Keccak256(pubBytes[1:])[12:])
}

func zeroBytes(bytes []byte) {
	for i := range bytes {
		bytes[i] = 0
	}
}

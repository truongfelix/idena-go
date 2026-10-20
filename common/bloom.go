package common

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/willf/bloom"
)

const (
	K = 3
)

type SerializableBF struct {
	*bloom.BloomFilter
	data []uint64
}

func NewSerializableBF(n int) *SerializableBF {
	data := make([]uint64, bloomLength(n)/64)
	return &SerializableBF{bloom.From(data, K), data}
}

// maxBloomWords is the most uint64 words a filter NewSerializableBF builds can have: bloomLength caps the bit size
// at 4096, i.e. 64 words.
var maxBloomWords = bloomLength(1<<30) / 64

func NewSerializableBFFromData(data []byte) (*SerializableBF, error) {
	// A serialized filter holds the words of a filter whose bit size comes from bloomLength, so its byte length is
	// a power-of-two count of 8-byte words between 1 and maxBloomWords (8, 16, 32, 64, 128, 256 or 512 bytes). Any
	// other length is malformed and returns an error.
	wordSize := binary.Size(uint64(0))
	words := len(data) / wordSize
	if len(data) == 0 || len(data)%wordSize != 0 || words > maxBloomWords || words&(words-1) != 0 {
		return nil, fmt.Errorf("invalid bloom filter length %d", len(data))
	}
	buf := make([]uint64, words)
	if err := binary.Read(bytes.NewReader(data), binary.BigEndian, buf); err != nil {
		return nil, err
	}
	return &SerializableBF{bloom.From(buf, K), buf}, nil
}

func (s *SerializableBF) Serialize() ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.BigEndian, s.data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *SerializableBF) Add(value []byte) {
	s.BloomFilter.Add(value)
}

func (s *SerializableBF) Has(value []byte) bool {
	return s.BloomFilter.Test(value)
}

func bloomLength(n int) (m int) {
	switch {
	case n < 8:
		return 64
	case n < 16:
		return 128
	case n < 32:
		return 256
	case n < 64:
		return 512
	case n < 128:
		return 1024
	case n < 256:
		return 2048
	default:
		return 4096
	}
}

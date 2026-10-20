package common

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// legacyBitmapVector is an input and the values the Bitmap.Read of idena-go v1.1.2 (938be81, on roaring v0.9.4)
// decodes it to: roaring and big-integer encodings, odd first bytes, values beyond the size, inputs cut short or
// with extra bytes.
type legacyBitmapVector struct {
	Size   uint32 `json:"size"`
	Data   string `json:"data"`
	Count  int    `json:"count"`
	Digest string `json:"digest"`
}

func legacyDigest(values []uint32) string {
	h := sha256.New()
	var b [4]byte
	for _, v := range values {
		binary.LittleEndian.PutUint32(b[:], v)
		h.Write(b[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestBitmap_ReadMatchesV112 checks that Read decodes every input to the values v1.1.2 decodes it to: the votes
// of an evidence map decide the approved candidates of a ceremony, which every node must compute alike.
func TestBitmap_ReadMatchesV112(t *testing.T) {
	f, err := os.Open("testdata/legacy_bitmap_vectors.json.gz")
	require.NoError(t, err)
	defer f.Close()
	zr, err := gzip.NewReader(f)
	require.NoError(t, err)

	scanner := bufio.NewScanner(zr)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	checked := 0
	for scanner.Scan() {
		var v legacyBitmapVector
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &v))
		data, err := hex.DecodeString(v.Data)
		require.NoError(t, err)

		bitmap := NewBitmap(v.Size)
		bitmap.Read(data)
		values := bitmap.ToArray()
		require.Equal(t, v.Count, len(values), "value count differs on %s (size %d)", v.Data, v.Size)
		require.Equal(t, v.Digest, legacyDigest(values), "values differ on %s (size %d)", v.Data, v.Size)
		checked++
	}
	require.NoError(t, scanner.Err())
	require.Greater(t, checked, 5000)
}

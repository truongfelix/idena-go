package common

import (
	"bytes"
	"encoding/binary"
	"math/bits"

	"github.com/RoaringBitmap/roaring"
)

// Layout of a serialized roaring bitmap (github.com/RoaringBitmap/RoaringFormatSpec), as roaring's ReadFrom reads it.
const (
	roaringCookie          = 12347 // low 16 bits of the first word; the high 16 bits hold the container count - 1
	roaringCookieNoRun     = 12346 // the first word; the container count follows as a uint32
	roaringNoOffsetMax     = 4     // with run containers, an offset table follows only from this many containers
	roaringArrayMaxEntries = 4096  // a container declaring more values is a bitmap container
	roaringBitmapWords     = 1024
)

// AddVotes adds to score[v], for every value v below len(score), the number of times Read(data) followed by ToArray
// lists v, with the bitmap sized len(score). It counts each container from its entries, runs or words instead of
// listing it: a run adds its range at once, and a container whose values all lie at or above len(score) is only
// counted. ToArray sizes its result by the cardinality the containers declare: when a bitmap container declares
// more values than it holds, the rest of the result is 0, which counts for value 0; when the containers hold more
// values than declared, ToArray does not return and AddVotes adds nothing.
func AddVotes(data []byte, score []int) {
	size := uint32(len(score))
	if len(data) == 0 {
		return
	}
	if data[0] != serializeDefault {
		bitmap := NewBitmap(size)
		bitmap.Read(data)
		for _, v := range bitmap.ToArray() {
			score[v]++
		}
		return
	}
	// Read keeps a roaring bitmap only when ReadFrom accepts it; anything else reads as no votes.
	if _, err := roaring.NewBitmap().ReadFrom(bytes.NewBuffer(data[1:])); err != nil {
		return
	}
	counts, ok := roaringVotes(data[1:], size)
	if !ok {
		return
	}
	for i, c := range counts {
		score[i] += c
	}
}

type byteReader struct {
	data []byte
	ok   bool
}

func (r *byteReader) next(n uint64) []byte {
	if !r.ok || n > uint64(len(r.data)) {
		r.ok = false
		return nil
	}
	b := r.data[:n]
	r.data = r.data[n:]
	return b
}

func (r *byteReader) uint16() uint16 {
	if b := r.next(2); b != nil {
		return binary.LittleEndian.Uint16(b)
	}
	return 0
}

func (r *byteReader) uint32() uint32 {
	if b := r.next(4); b != nil {
		return binary.LittleEndian.Uint32(b)
	}
	return 0
}

// roaringVotes counts the values below size that ToArray lists for the serialized bitmap data, which ReadFrom
// accepted. ok is false when ToArray would not return.
func roaringVotes(data []byte, size uint32) (counts []int, ok bool) {
	r := &byteReader{data: data, ok: true}
	var containers uint32
	var runFlags []byte
	cookie := r.uint32()
	switch {
	case cookie&0xffff == roaringCookie:
		containers = cookie>>16 + 1
		runFlags = r.next(uint64(containers+7) / 8)
	case cookie == roaringCookieNoRun:
		containers = r.uint32()
	default:
		return nil, false
	}
	if containers > 1<<16 {
		return nil, false
	}
	header := r.next(4 * uint64(containers))
	if runFlags == nil || containers >= roaringNoOffsetMax {
		r.next(4 * uint64(containers))
	}
	if !r.ok {
		return nil, false
	}

	// added[i] - added[i-1] is how many more times value i is listed than value i-1.
	added := make([]int, size+1)
	addRange := func(from, to uint64) {
		if size == 0 {
			return
		}
		if to >= uint64(size) {
			to = uint64(size) - 1
		}
		if from > to {
			return
		}
		added[from]++
		added[to+1]--
	}
	var declared, listed uint64
	for i := uint32(0); i < containers; i++ {
		key := uint64(binary.LittleEndian.Uint16(header[4*i:]))
		declaredValues := uint64(binary.LittleEndian.Uint16(header[4*i+2:])) + 1
		high := key << 16
		inRange := high < uint64(size)
		switch {
		case runFlags != nil && runFlags[i/8]&(1<<(i%8)) != 0:
			runs := r.next(4 * uint64(r.uint16()))
			for j := 0; j+4 <= len(runs); j += 4 {
				start := uint64(binary.LittleEndian.Uint16(runs[j:]))
				last := start + uint64(binary.LittleEndian.Uint16(runs[j+2:]))
				declared += last - start + 1
				listed += last - start + 1
				if !inRange {
					continue
				}
				// A run lists start..last, which can pass 0xffff; each value is OR-ed with the container's key.
				if start <= 0xffff {
					addRange(high+start, high+min(last, 0xffff))
				}
				if last > 0xffff {
					addRange(high|0x10000, (high|0x10000)+(last-0x10000))
				}
			}
		case declaredValues > roaringArrayMaxEntries:
			words := r.next(8 * roaringBitmapWords)
			declared += declaredValues
			for w := 0; w+8 <= len(words); w += 8 {
				word := binary.LittleEndian.Uint64(words[w:])
				listed += uint64(bits.OnesCount64(word))
				base := high + uint64(w/8)*64
				for ; inRange && word != 0 && base < uint64(size); word &= word - 1 {
					v := base + uint64(bits.TrailingZeros64(word))
					addRange(v, v)
				}
			}
		default:
			entries := r.next(2 * declaredValues)
			declared += declaredValues
			listed += declaredValues
			for e := 0; inRange && e+2 <= len(entries); e += 2 {
				v := high | uint64(binary.LittleEndian.Uint16(entries[e:]))
				addRange(v, v)
			}
		}
		if !r.ok {
			return nil, false
		}
	}
	if listed > declared {
		return nil, false
	}
	counts = make([]int, size)
	running := 0
	for i := range counts {
		running += added[i]
		counts[i] = running
	}
	// ToArray leaves the values the bitmap containers declare but do not hold as 0.
	if size > 0 {
		counts[0] += int(declared - listed)
	}
	return counts, true
}

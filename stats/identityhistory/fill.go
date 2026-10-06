package identityhistory

import (
	"sort"
	"time"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/core/state"
	"github.com/pkg/errors"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// Header keys of the chain database (database/schema.go): "h" + block hash -> header; "h" + height + "n" -> the
// canonical hash.
var headerPrefix = []byte("h")

const headerKeyLength = 1 + common.HashLength

// Validation times on mainnet: 13:30 UTC until the v12 upgrade, 15:00 UTC since (config.GetNextValidationTime).
// Epochs last whole days, so every ceremony starts at one of these times of day.
var validationTimesOfDay = []time.Duration{15 * time.Hour, 13*time.Hour + 30*time.Minute}

// fill records the ceremonies the index lacks. The first time it reads every stored header once (exact, about a
// minute on a hard disk for mainnet); afterwards, a gap between two recorded ceremonies (left by a fast sync) is
// searched by block time and accepted only when it holds exactly the missing epochs, else the full pass runs again.
// Only ceremony blocks a reset can no longer revert are written: the newer one is recorded when its block is added.
func (s *Service) fill() error {
	appState, err := s.chain.ReadonlyAppState()
	if err != nil {
		return err
	}
	if appState.State.Epoch() == 0 {
		return nil
	}
	top := anchor{epoch: appState.State.Epoch() - 1, height: appState.State.EpochBlock()}
	prevBlocks := appState.State.PrevEpochBlocks()
	head := s.chain.Head()
	if head == nil {
		return errors.New("no head block")
	}
	final := uint64(0)
	if head.Height() > state.MaxSavedStatesCount {
		final = head.Height() - state.MaxSavedStatesCount
	}

	m, err := s.store.readMeta()
	if err != nil {
		return err
	}
	if !m.Passed {
		return s.pass(top, prevBlocks, final)
	}
	recorded, err := s.store.Ceremonies()
	if err != nil {
		return err
	}
	found := make(map[uint16]*Ceremony)
	if c, ok := recorded[top.epoch]; !ok || c.Height != top.height {
		header := s.chain.HeaderByHeight(top.height)
		if header == nil {
			return nil // below the node's headers: nothing to place
		}
		c := &Ceremony{Height: top.height, Time: header.Time()}
		recorded[top.epoch] = c
		found[top.epoch] = c
	}
	epochs := make([]uint16, 0, len(recorded))
	for epoch, c := range recorded {
		if epoch <= top.epoch && c.Height <= top.height {
			epochs = append(epochs, epoch)
		}
	}
	sort.Slice(epochs, func(i, j int) bool { return epochs[i] < epochs[j] })
	for i := 1; i < len(epochs); i++ {
		lo, hi := epochs[i-1], epochs[i]
		if hi-lo <= 1 {
			continue
		}
		between, ok := s.searchGap(anchor{lo, recorded[lo].Height}, anchor{hi, recorded[hi].Height}, recorded[hi].Time)
		if !ok {
			s.log.Warn("Ceremony search did not place every epoch of a gap, reading all headers", "from", lo, "to", hi)
			if err := s.store.markForPass(); err != nil {
				return err
			}
			return s.pass(top, prevBlocks, final)
		}
		for epoch, c := range between {
			found[epoch] = c
		}
	}
	return s.store.writeFound(finalOnly(found, final), false)
}

type anchor struct {
	epoch  uint16
	height uint64
}

func finalOnly(found map[uint16]*Ceremony, final uint64) map[uint16]*Ceremony {
	result := make(map[uint16]*Ceremony, len(found))
	for epoch, c := range found {
		if c.Height <= final {
			result[epoch] = c
		}
	}
	return result
}

// pass reads every stored header once, in key order (sequential reads), and labels the canonical ceremony blocks
// by counting back from the state's epoch block; the state's previous epoch blocks must match.
func (s *Service) pass(top anchor, prevBlocks []uint64, final uint64) error {
	start := time.Now()
	times := make(map[uint64]int64)
	headers := 0
	err := s.iterateHeaders(func(hash common.Hash, header *types.Header) {
		headers++
		if !header.Flags().HasFlag(types.ValidationFinished) || header.Height() > top.height {
			return
		}
		if s.chain.CanonicalHash(header.Height()) != hash {
			return
		}
		times[header.Height()] = header.Time()
	})
	if err != nil {
		return err
	}
	heights := make([]uint64, 0, len(times))
	for h := range times {
		heights = append(heights, h)
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })

	found := make(map[uint16]*Ceremony)
	if len(heights) > 0 {
		if heights[len(heights)-1] != top.height {
			return errors.Errorf("the last ceremony block of the headers is %d, the state's epoch block %d",
				heights[len(heights)-1], top.height)
		}
		if int(top.epoch)+1 < len(heights) {
			return errors.Errorf("%d ceremony blocks for %d epochs", len(heights), top.epoch+1)
		}
		for i, h := range heights {
			epoch := top.epoch - uint16(len(heights)-1-i)
			found[epoch] = &Ceremony{Height: h, Time: times[h]}
		}
		// PrevEpochBlocks: the blocks that ended the two epochs before the last one, oldest first.
		for i, h := range prevBlocks {
			back := uint16(len(prevBlocks) - i)
			if back > top.epoch || h < heights[0] {
				continue
			}
			if c := found[top.epoch-back]; c == nil || c.Height != h {
				return errors.Errorf("ceremony block of epoch %d is %d in the state", top.epoch-back, h)
			}
		}
	}
	if err := s.store.writeFound(finalOnly(found, final), true); err != nil {
		return err
	}
	s.log.Info("Ceremony blocks found in the stored headers", "headers", headers, "ceremonies", len(found),
		"duration", time.Since(start))
	return nil
}

// iterateHeaders calls f for every stored header (any order). On the node's goleveldb the reads do not fill the
// block cache: the pass reads every header once.
func (s *Service) iterateHeaders(f func(hash common.Hash, header *types.Header)) error {
	visit := func(k, v []byte) {
		if len(k) != headerKeyLength {
			return
		}
		header := new(types.Header)
		if err := header.FromBytes(v); err != nil {
			return
		}
		f(common.BytesToHash(k[1:]), header)
	}
	if g, ok := s.db.(interface{ DB() *leveldb.DB }); ok {
		it := g.DB().NewIterator(util.BytesPrefix(headerPrefix), &opt.ReadOptions{DontFillCache: true})
		defer it.Release()
		for it.Next() {
			visit(it.Key(), it.Value())
		}
		return it.Error()
	}
	it, err := s.db.Iterator(headerPrefix, []byte{headerPrefix[0] + 1})
	if err != nil {
		return err
	}
	defer it.Close()
	for ; it.Valid(); it.Next() {
		visit(it.Key(), it.Value())
	}
	return it.Error()
}

// searchGap places the ceremonies strictly between two recorded ones. Candidates are the validation times of each
// day, newest first, so the latest ceremony below a bound is always the one found; at the first block at or after a
// candidate time, a ShortSessionStarted flag tells that a ceremony started then (isShortSessionStarted). ok is false
// unless exactly the hi-lo-1 missing ceremonies were found: the epochs between two ceremonies are consecutive, so
// that many ceremony blocks in the range are all of them.
func (s *Service) searchGap(lo, hi anchor, hiTime int64) (map[uint16]*Ceremony, bool) {
	expected := int(hi.epoch - lo.epoch - 1)
	r := &headerReader{chain: s.chain, cache: make(map[uint64]*types.Header)}
	loHeader := r.get(lo.height)
	if loHeader == nil {
		return nil, false
	}
	var vfHeights []uint64
	skippedHi := false
	upperH, upperT := hi.height-1, hiTime
	low, lowT := lo.height+1, loHeader.Time()
	var offset uint64
	const day = 24 * time.Hour
	for dayStart := time.Unix(upperT, 0).UTC().Truncate(day); len(vfHeights) < expected; dayStart = dayStart.Add(-day) {
		if dayStart.Add(day).Unix() <= lowT {
			break
		}
		for _, tod := range validationTimesOfDay {
			if len(vfHeights) == expected {
				break
			}
			v := dayStart.Add(tod).Unix()
			if v >= upperT || v <= lowT || upperH < low {
				continue
			}
			h, ok := r.heightAt(v, low, upperH)
			if !ok {
				continue
			}
			ss := uint64(0)
			for k := uint64(0); k < 3 && h+k <= upperH; k++ {
				if header := r.get(h + k); header != nil && header.Flags().HasFlag(types.ShortSessionStarted) {
					ss = h + k
					break
				}
			}
			if ss == 0 {
				continue
			}
			vf := r.findFinished(ss, upperH, offset)
			if vf == 0 {
				// Only hi's own ceremony ends at or after the bound: it is the first one the search can meet.
				if len(vfHeights) > 0 || skippedHi {
					return nil, false
				}
				skippedHi = true
			} else {
				offset = vf - ss
				vfHeights = append(vfHeights, vf)
			}
			upperH, upperT = ss-1, v-int64(time.Minute.Seconds())
		}
	}
	if len(vfHeights) != expected {
		return nil, false
	}
	found := make(map[uint16]*Ceremony, expected)
	for i, h := range vfHeights {
		found[hi.epoch-1-uint16(i)] = &Ceremony{Height: h, Time: r.get(h).Time()}
	}
	return found, true
}

type headerReader struct {
	chain Chain
	cache map[uint64]*types.Header
}

func (r *headerReader) get(height uint64) *types.Header {
	if header, ok := r.cache[height]; ok {
		return header
	}
	header := r.chain.HeaderByHeight(height)
	r.cache[height] = header
	return header
}

// heightAt returns the first height in [lo, hi] whose block time is at or after t (block times increase with the
// height): interpolation by time, every other step a bisection.
func (r *headerReader) heightAt(t int64, lo, hi uint64) (uint64, bool) {
	hiHeader, loHeader := r.get(hi), r.get(lo)
	if hiHeader == nil || loHeader == nil || hiHeader.Time() < t {
		return 0, false
	}
	if loHeader.Time() >= t {
		return lo, true
	}
	for step := 0; hi-lo > 1; step++ {
		tl, th := r.get(lo).Time(), r.get(hi).Time()
		mid := lo + (hi-lo)/2
		if step%2 == 0 && th > tl {
			mid = lo + uint64(float64(hi-lo)*float64(t-tl)/float64(th-tl))
			mid = max(lo+1, min(mid, hi-1))
		}
		header := r.get(mid)
		if header == nil {
			return 0, false
		}
		if header.Time() >= t {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi, true
}

// findFinished returns the ceremony block after the short session block ss (0: none up to limit): near the previous
// ceremony's distance first, then block by block.
func (r *headerReader) findFinished(ss, limit, offset uint64) uint64 {
	finished := func(h uint64) bool {
		header := r.get(h)
		return header != nil && header.Flags().HasFlag(types.ValidationFinished)
	}
	if offset > 0 {
		for k := uint64(0); k <= 8; k++ {
			if h := ss + offset + k; h <= limit && finished(h) {
				return h
			}
			if k < offset {
				if h := ss + offset - k; h <= limit && finished(h) {
					return h
				}
			}
		}
	}
	for h := ss + 1; h <= limit; h++ {
		header := r.get(h)
		if header == nil {
			return 0
		}
		if header.Flags().HasFlag(types.ValidationFinished) {
			return h
		}
	}
	return 0
}

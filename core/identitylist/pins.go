package identitylist

import (
	"bytes"
	"encoding/binary"
	"io"
	"sort"
	"sync"

	"github.com/idena-network/idena-go/ipfs"
	"github.com/ipfs/go-cid"
	"github.com/pkg/errors"
	dbm "github.com/tendermint/tm-db"
)

var pinsKey = []byte("identity-list-pins")

// KeepAll, as the number of lists kept, keeps every list (a history node).
const KeepAll = -1

type pinned struct {
	height   uint64
	ceremony bool
	cids     []cid.Cid // the index first, then the pages
}

// Pins stores each list in IPFS, pinned, and unpins the old ones: it keeps the last ceremony's list and the last
// keep others (KeepAll: every list). A page that did not change between two lists has the same CID in both: it is
// unpinned only when no kept list holds it.
type Pins struct {
	db   dbm.DB
	ipfs ipfs.Proxy
	keep int
	mu   sync.Mutex
}

func NewPins(db dbm.DB, proxy ipfs.Proxy, keep int) *Pins {
	return &Pins{db: db, ipfs: proxy, keep: keep}
}

// Add stores and pins a list; ceremony is set for the list of the block that applied a ceremony's results.
//
// The new set of lists is saved before anything is pinned or unpinned: a crash after the save leaves at worst a
// listed list whose blocks are not pinned yet (read from peers, unpinned harmlessly later) or dropped blocks still
// pinned, never a pinned block no saved entry names, except the dropped ones of that one call.
func (p *Pins) Add(list *List, ceremony bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := pinned{height: list.Height, ceremony: ceremony, cids: []cid.Cid{list.IndexCid}}
	entry.cids = append(entry.cids, list.PageCids...)
	blocks := append([][]byte{list.Index}, list.Pages...)

	entries, err := p.load()
	if err != nil {
		return err
	}
	// A list at a height already listed replaces it and every later one: the chain switched to another branch.
	before := entries[:0:0]
	var dropped []pinned
	for _, e := range entries {
		if e.height >= entry.height {
			dropped = append(dropped, e)
		} else {
			before = append(before, e)
		}
	}
	kept, old := p.choose(append(before, entry))
	dropped = append(dropped, old...)
	if err := p.save(kept); err != nil {
		return err
	}
	for i, data := range blocks {
		c, err := p.ipfs.Add(data, true)
		if err == nil && !c.Equals(entry.cids[i]) {
			err = errors.Errorf("IPFS stored %v, expected %v", c, entry.cids[i])
		}
		if err != nil {
			// Give up this list and keep the lists as they were: unpin what this call pinned, unless one of them
			// holds it.
			p.unpin([]pinned{{cids: entry.cids[:i]}}, entries)
			if saveErr := p.save(entries); saveErr != nil {
				return saveErr
			}
			return errors.Wrapf(err, "identity list of block %v", list.Height)
		}
	}
	p.unpin(dropped, kept)
	return nil
}

// choose splits the entries, sorted by height, into the kept and the dropped ones.
func (p *Pins) choose(entries []pinned) (kept, dropped []pinned) {
	if p.keep < 0 {
		return entries, nil
	}
	lastCeremony := -1
	for i, e := range entries {
		if e.ceremony {
			lastCeremony = i
		}
	}
	others := 0
	keepIdx := make(map[int]bool)
	for i := len(entries) - 1; i >= 0; i-- {
		if i == lastCeremony {
			keepIdx[i] = true
			continue
		}
		if !entries[i].ceremony && others < p.keep {
			keepIdx[i] = true
			others++
		}
	}
	for i, e := range entries {
		if keepIdx[i] {
			kept = append(kept, e)
		} else {
			dropped = append(dropped, e)
		}
	}
	return kept, dropped
}

func (p *Pins) unpin(dropped, kept []pinned) {
	keep := make(map[string]bool)
	for _, e := range kept {
		for _, c := range e.cids {
			keep[c.KeyString()] = true
		}
	}
	for _, e := range dropped {
		for _, c := range e.cids {
			if keep[c.KeyString()] {
				continue
			}
			keep[c.KeyString()] = true // once
			_ = p.ipfs.Unpin(c.Bytes())
		}
	}
}

// Pinned is a list this node keeps pinned.
type Pinned struct {
	Height   uint64
	IndexCid cid.Cid
}

// Lists lists the pinned lists, lowest height first.
func (p *Pins) Lists() ([]Pinned, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries, err := p.load()
	if err != nil {
		return nil, err
	}
	res := make([]Pinned, 0, len(entries))
	for _, e := range entries {
		res = append(res, Pinned{Height: e.height, IndexCid: e.cids[0]})
	}
	return res, nil
}

// Heights lists the heights of the pinned lists, lowest first.
func (p *Pins) Heights() ([]uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries, err := p.load()
	if err != nil {
		return nil, err
	}
	var res []uint64
	for _, e := range entries {
		res = append(res, e.height)
	}
	return res, nil
}

func (p *Pins) load() ([]pinned, error) {
	data, err := p.db.Get(pinsKey)
	if err != nil || len(data) == 0 {
		return nil, err
	}
	var entries []pinned
	r := bytes.NewReader(data)
	for r.Len() > 0 {
		var e pinned
		var head [13]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return nil, errors.Wrap(err, "identity list pins")
		}
		e.height = binary.BigEndian.Uint64(head[:8])
		e.ceremony = head[8] == 1
		n := binary.BigEndian.Uint32(head[9:13])
		for i := uint32(0); i < n; i++ {
			l, err := r.ReadByte()
			if err != nil {
				return nil, errors.Wrap(err, "identity list pins")
			}
			b := make([]byte, l)
			if _, err := io.ReadFull(r, b); err != nil {
				return nil, errors.Wrap(err, "identity list pins")
			}
			c, err := cid.Cast(b)
			if err != nil {
				return nil, errors.Wrap(err, "identity list pins")
			}
			e.cids = append(e.cids, c)
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].height < entries[j].height })
	return entries, nil
}

func (p *Pins) save(entries []pinned) error {
	var b []byte
	for _, e := range entries {
		b = binary.BigEndian.AppendUint64(b, e.height)
		if e.ceremony {
			b = append(b, 1)
		} else {
			b = append(b, 0)
		}
		b = binary.BigEndian.AppendUint32(b, uint32(len(e.cids)))
		for _, c := range e.cids {
			cb := c.Bytes()
			b = append(b, byte(len(cb)))
			b = append(b, cb...)
		}
	}
	return p.db.Set(pinsKey, b)
}

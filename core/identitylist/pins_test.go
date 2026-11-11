package identitylist

import (
	"errors"
	"testing"

	"github.com/idena-network/idena-go/ipfs"
	"github.com/ipfs/go-cid"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

type pinTracker struct {
	ipfs.Proxy
	pinned   map[string]bool
	unpinned []string
}

func newPinTracker() *pinTracker {
	return &pinTracker{Proxy: ipfs.NewMemoryIpfsProxy(), pinned: map[string]bool{}}
}

func (p *pinTracker) Add(data []byte, pin bool) (cid.Cid, error) {
	c, err := p.Proxy.Add(data, pin)
	if pin {
		p.pinned[c.KeyString()] = true
	}
	return c, err
}

func (p *pinTracker) Unpin(key []byte) error {
	c, err := cid.Cast(key)
	if err != nil {
		return err
	}
	if !p.pinned[c.KeyString()] {
		panic("unpinned twice or never pinned: " + c.String())
	}
	delete(p.pinned, c.KeyString())
	p.unpinned = append(p.unpinned, c.KeyString())
	return nil
}

func listAt(t *testing.T, height uint64, rows ...Row) *List {
	if len(rows) == 0 {
		rows = []Row{fullRow(int(height))}
	}
	l, err := Build(height, rows)
	require.NoError(t, err)
	return l
}

func (p *pinTracker) holds(l *List) bool {
	if !p.pinned[l.IndexCid.KeyString()] {
		return false
	}
	for _, c := range l.PageCids {
		if !p.pinned[c.KeyString()] {
			return false
		}
	}
	return true
}

func TestPinsKeepLastCeremonyAndLastOthers(t *testing.T) {
	tracker := newPinTracker()
	pins := NewPins(db.NewMemDB(), tracker, 3)
	lists := map[uint64]*List{}
	for _, h := range []uint64{10, 20, 30, 35, 40, 50} {
		lists[h] = listAt(t, h)
		require.NoError(t, pins.Add(lists[h], h == 35))
	}
	heights, err := pins.Heights()
	require.NoError(t, err)
	require.Equal(t, []uint64{30, 35, 40, 50}, heights)
	for h, l := range lists {
		require.Equal(t, h >= 30, tracker.holds(l), "height %v", h)
	}

	// A new ceremony list replaces the old one; the old one is not among the last 3 others.
	lists[60] = listAt(t, 60)
	require.NoError(t, pins.Add(lists[60], true))
	heights, _ = pins.Heights()
	require.Equal(t, []uint64{30, 40, 50, 60}, heights)
	require.False(t, tracker.holds(lists[35]))
}

func TestPinsKeepAPageSharedWithAKeptList(t *testing.T) {
	tracker := newPinTracker()
	pins := NewPins(db.NewMemDB(), tracker, 1)
	same := fullRow(7)
	a, b, c := listAt(t, 10, same), listAt(t, 20, same), listAt(t, 30, fullRow(8))
	require.Equal(t, a.PageCids, b.PageCids)
	require.NotEqual(t, a.IndexCid, b.IndexCid)

	require.NoError(t, pins.Add(a, false))
	require.NoError(t, pins.Add(b, false))
	require.False(t, tracker.pinned[a.IndexCid.KeyString()])
	require.True(t, tracker.holds(b), "the page both lists hold stays pinned")

	require.NoError(t, pins.Add(c, false))
	require.False(t, tracker.pinned[a.PageCids[0].KeyString()])
	require.True(t, tracker.holds(c))
}

func TestPinsBranchSwitchReplacesLaterLists(t *testing.T) {
	tracker := newPinTracker()
	pins := NewPins(db.NewMemDB(), tracker, 3)
	for _, h := range []uint64{10, 20, 30} {
		require.NoError(t, pins.Add(listAt(t, h), false))
	}
	other := listAt(t, 20, fullRow(99))
	require.NoError(t, pins.Add(other, false))
	heights, _ := pins.Heights()
	require.Equal(t, []uint64{10, 20}, heights)
	require.True(t, tracker.holds(other))
	require.False(t, tracker.holds(listAt(t, 30)))
}

func TestPinsKeepAll(t *testing.T) {
	tracker := newPinTracker()
	pins := NewPins(db.NewMemDB(), tracker, KeepAll)
	for h := uint64(1); h <= 20; h++ {
		require.NoError(t, pins.Add(listAt(t, h), h%5 == 0))
	}
	heights, _ := pins.Heights()
	require.Len(t, heights, 20)
	require.Empty(t, tracker.unpinned)
}

func TestPinsSurviveARestart(t *testing.T) {
	tracker := newPinTracker()
	store := db.NewMemDB()
	pins := NewPins(store, tracker, 2)
	for _, h := range []uint64{10, 20} {
		require.NoError(t, pins.Add(listAt(t, h), false))
	}
	again := NewPins(store, tracker, 2)
	require.NoError(t, again.Add(listAt(t, 30), false))
	heights, _ := again.Heights()
	require.Equal(t, []uint64{20, 30}, heights)
	require.False(t, tracker.holds(listAt(t, 10)))
}

type failingAdds struct {
	*pinTracker
	left int
}

func (f *failingAdds) Add(data []byte, pin bool) (cid.Cid, error) {
	if f.left == 0 {
		return cid.Cid{}, errors.New("ipfs is down")
	}
	f.left--
	return f.pinTracker.Add(data, pin)
}

// A list IPFS stores only in part leaves the lists as they were, and nothing of it pinned.
func TestPinsFailedAddLeavesNothingPinned(t *testing.T) {
	tracker := newPinTracker()
	adds := &failingAdds{pinTracker: tracker, left: -1}
	pins := NewPins(db.NewMemDB(), adds, 1)
	first := listAt(t, 10)
	require.NoError(t, pins.Add(first, false))

	rows := make([]Row, 0, PageSize+5)
	for i := 0; i < PageSize+5; i++ {
		rows = append(rows, fullRow(1000+i))
	}
	big := listAt(t, 20, rows...)
	require.Len(t, big.Pages, 2)
	adds.left = 2 // the index and the first page, then a failure
	require.Error(t, pins.Add(big, false))

	heights, err := pins.Heights()
	require.NoError(t, err)
	require.Equal(t, []uint64{10}, heights)
	require.True(t, tracker.holds(first))
	require.Len(t, tracker.pinned, 1+len(first.PageCids))
}

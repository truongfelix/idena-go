package identitylist

import (
	"crypto/sha256"
	"fmt"
	"math/big"
	"math/rand"
	"testing"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/core/state"
	"github.com/multiformats/go-multihash"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

func addr(i int) common.Address {
	var a common.Address
	a[0], a[1], a[19] = byte(i>>8), byte(i), 0xaa
	return a
}

func fullRow(i int) Row {
	d := addr(i + 50000)
	return Row{
		Address:          addr(i),
		State:            state.Human,
		Birthday:         uint16(100 + i),
		Stake:            new(big.Int).Mul(big.NewInt(int64(i+1)), common.DnaBase),
		LockedStake:      big.NewInt(int64(i * 7)),
		ReplenishedStake: big.NewInt(int64(i * 3)),
		Delegatee:        &d,
		Penalty:          big.NewInt(int64(i)),
		PenaltySeconds:   uint16(i),
		PenaltyTimestamp: 1_700_000_000 + int64(i),
		QualifiedFlips:   uint32(30 + i),
		ShortFlipPoints:  uint32(55 + i),
		Scores:           []byte{0x63, 0x53, byte(i)},
		// i+1 so that every full row has a window, and points below twice the flips
		FlipQualityFlips:  uint16(i + 1),
		FlipQualityPoints: uint16(i),
	}
}

func normalized(r Row) Row {
	for _, v := range []**big.Int{&r.Stake, &r.LockedStake, &r.ReplenishedStake, &r.Penalty} {
		if *v == nil {
			*v = new(big.Int)
		}
	}
	if len(r.Scores) == 0 {
		r.Scores = nil
	}
	return r
}

func decodeAll(t *testing.T, l *List) (*Index, []Row) {
	idx, err := DecodeIndex(l.Index)
	require.NoError(t, err)
	var rows []Row
	for i, page := range l.Pages {
		c, err := Cid(page)
		require.NoError(t, err)
		require.Equal(t, idx.PageCids[i], c)
		r, err := DecodePage(page)
		require.NoError(t, err)
		rows = append(rows, r...)
	}
	return idx, rows
}

func TestRowSize(t *testing.T) {
	require.Equal(t, 205, RowSize)
	require.Len(t, appendRow(nil, fullRow(1)), RowSize)
	require.Len(t, appendRow(nil, Row{}), RowSize)
	require.Less(t, 1+PageSize*RowSize, maxRawBlockSize)
}

func TestRoundTripSortsByAddress(t *testing.T) {
	rows := []Row{fullRow(3), {Address: addr(1), State: state.Candidate}, fullRow(2)}
	l, err := Build(1234, rows)
	require.NoError(t, err)
	idx, got := decodeAll(t, l)
	require.Equal(t, uint64(1234), idx.Height)
	require.Equal(t, uint32(3), idx.Count)
	require.Equal(t, []Row{normalized(rows[1]), normalized(rows[2]), normalized(rows[0])}, got)
}

func TestSameCidWhateverTheOrder(t *testing.T) {
	rows := make([]Row, 0, 2500)
	for i := 0; i < 2500; i++ {
		rows = append(rows, fullRow(i))
	}
	a, err := Build(7, rows)
	require.NoError(t, err)
	rand.New(rand.NewSource(1)).Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
	b, err := Build(7, rows)
	require.NoError(t, err)
	require.Equal(t, a.IndexCid, b.IndexCid)
	require.Equal(t, a.Index, b.Index)
}

func TestPages(t *testing.T) {
	for _, n := range []int{0, 1, PageSize - 1, PageSize, PageSize + 1, 2*PageSize + 500} {
		rows := make([]Row, 0, n)
		for i := 0; i < n; i++ {
			rows = append(rows, fullRow(i))
		}
		l, err := Build(1, rows)
		require.NoError(t, err)
		require.Len(t, l.Pages, (n+PageSize-1)/PageSize, "n=%v", n)
		for i, p := range l.Pages {
			require.LessOrEqual(t, len(p), maxRawBlockSize)
			if i < len(l.Pages)-1 {
				require.Len(t, p, 1+PageSize*RowSize)
			}
		}
		idx, got := decodeAll(t, l)
		require.Equal(t, uint32(n), idx.Count)
		require.Len(t, got, n)
	}
}

func TestDuplicateAddressRefused(t *testing.T) {
	_, err := Build(1, []Row{fullRow(1), fullRow(1)})
	require.Error(t, err)
}

// The CID is the CIDv1 raw SHA2-256 of the bytes: anyone checks a page or an index with a SHA-256.
func TestCidIsSha256OfTheBytes(t *testing.T) {
	l, err := Build(9, []Row{fullRow(1)})
	require.NoError(t, err)
	sum := sha256.Sum256(l.Index)
	decoded, err := multihash.Decode(l.IndexCid.Hash())
	require.NoError(t, err)
	require.Equal(t, uint64(multihash.SHA2_256), decoded.Code)
	require.Equal(t, sum[:], decoded.Digest)
	require.Equal(t, uint64(0x55), l.IndexCid.Prefix().Codec)
	require.Equal(t, uint64(1), l.IndexCid.Version())
}

func TestEveryFieldChangesTheCid(t *testing.T) {
	base, err := Build(5, []Row{fullRow(1)})
	require.NoError(t, err)
	d := addr(999)
	changes := map[string]func(r *Row){
		"address":           func(r *Row) { r.Address = addr(2) },
		"state":             func(r *Row) { r.State = state.Verified },
		"birthday":          func(r *Row) { r.Birthday++ },
		"stake":             func(r *Row) { r.Stake = big.NewInt(1) },
		"locked stake":      func(r *Row) { r.LockedStake = big.NewInt(1) },
		"replenished stake": func(r *Row) { r.ReplenishedStake = big.NewInt(1) },
		"delegatee":         func(r *Row) { r.Delegatee = &d },
		"no delegatee":      func(r *Row) { r.Delegatee = nil },
		"penalty":           func(r *Row) { r.Penalty = big.NewInt(1000) },
		"penalty seconds":   func(r *Row) { r.PenaltySeconds++ },
		"penalty timestamp": func(r *Row) { r.PenaltyTimestamp++ },
		"qualified flips":   func(r *Row) { r.QualifiedFlips++ },
		"short flip points": func(r *Row) { r.ShortFlipPoints++ },
		"scores":            func(r *Row) { r.Scores = []byte{0x63} },
	}
	for name, change := range changes {
		r := fullRow(1)
		change(&r)
		l, err := Build(5, []Row{r})
		require.NoError(t, err)
		require.NotEqual(t, base.IndexCid, l.IndexCid, name)
	}
	other, err := Build(6, []Row{fullRow(1)})
	require.NoError(t, err)
	require.NotEqual(t, base.IndexCid, other.IndexCid, "height")
}

func TestFromState(t *testing.T) {
	s, err := state.NewLazy(db.NewMemDB())
	require.NoError(t, err)
	a, b, pool := addr(2), addr(1), addr(77)
	s.SetState(a, state.Human)
	s.SetBirthday(a, 12)
	s.AddStake(a, big.NewInt(500))
	s.AddLockedStake(a, big.NewInt(40))
	s.AddReplenishedStake(a, big.NewInt(30))
	s.SetPenaltySeconds(a, 600)
	s.SetPenaltyTimestamp(a, 1_750_000_000)
	s.AddNewScore(a, common.EncodeScore(5, 6))
	s.SetDelegatee(a, pool)
	s.SetState(b, state.Candidate)
	s.Commit(false)
	// A change left in the working tree after Precommit, as when a block is checked.
	s.SetBirthday(b, 3)
	s.Precommit(false)

	l, err := FromState(42, s)
	require.NoError(t, err)
	_, rows := decodeAll(t, l)
	require.Len(t, rows, 2)
	require.Equal(t, b, rows[0].Address)
	require.Equal(t, state.Candidate, rows[0].State)
	require.Equal(t, uint16(3), rows[0].Birthday)
	require.Equal(t, a, rows[1].Address)
	require.Equal(t, state.Human, rows[1].State)
	require.Equal(t, uint16(12), rows[1].Birthday)
	require.Equal(t, big.NewInt(500), rows[1].Stake)
	require.Equal(t, big.NewInt(40), rows[1].LockedStake)
	require.Equal(t, big.NewInt(30), rows[1].ReplenishedStake)
	require.Equal(t, uint16(600), rows[1].PenaltySeconds)
	require.Equal(t, int64(1_750_000_000), rows[1].PenaltyTimestamp)
	require.Equal(t, []byte{common.EncodeScore(5, 6)}, rows[1].Scores)
	require.Equal(t, &pool, rows[1].Delegatee)
}

func TestDecodeRefusesDamagedBlocks(t *testing.T) {
	l, err := Build(1, []Row{fullRow(1), fullRow(2)})
	require.NoError(t, err)
	_, err = DecodeIndex(l.Index[:len(l.Index)-1])
	require.Error(t, err)
	_, err = DecodeIndex(append(append([]byte{}, l.Index...), 0))
	require.Error(t, err)
	_, err = DecodePage(l.Pages[0][:len(l.Pages[0])-1])
	require.Error(t, err)
	bad := append([]byte{}, l.Index...)
	for _, v := range []byte{Version - 1, Version + 1} {
		bad[0] = v
		_, err = DecodeIndex(bad)
		require.Error(t, err, "version %v", v)
		page := append([]byte{v}, l.Pages[0][1:]...)
		_, err = DecodePage(page)
		require.Error(t, err, "page version %v", v)
	}
}

// The cost a list block adds to every node: the walk over the state's identities, the encoding and the hashes.
func BenchmarkFromState(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000} {
		s, err := state.NewLazy(db.NewMemDB())
		require.NoError(b, err)
		for i := 0; i < n; i++ {
			a := common.Address{byte(i >> 16), byte(i >> 8), byte(i), 1}
			s.SetState(a, state.Human)
			s.AddStake(a, big.NewInt(int64(i)))
			s.AddNewScore(a, common.EncodeScore(5, 6))
		}
		s.Commit(false)
		s.Clear()
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if _, err := FromState(1, s); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

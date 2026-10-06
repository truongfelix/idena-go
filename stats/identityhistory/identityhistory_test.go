package identityhistory

import (
	"bytes"
	"math/big"
	"testing"
	"time"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/crypto"
	"github.com/idena-network/idena-go/database"
	"github.com/idena-network/idena-go/events"
	"github.com/idena-network/idena-go/stats/validationsummary"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

func dna(n int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(n), common.DnaBase)
}

func addr(b byte) common.Address {
	return common.Address{b}
}

// countingDB counts the passes over the stored headers.
type countingDB struct {
	db.DB
	headerPasses int
}

func (c *countingDB) Iterator(start, end []byte) (db.Iterator, error) {
	if bytes.Equal(start, headerPrefix) {
		c.headerPasses++
	}
	return c.DB.Iterator(start, end)
}

type testChain struct {
	repo     *database.Repo
	head     *types.Header
	appState *appstate.AppState
	reads    int
}

func (c *testChain) Head() *types.Header                           { return c.head }
func (c *testChain) ReadonlyAppState() (*appstate.AppState, error) { return c.appState, nil }
func (c *testChain) CanonicalHash(height uint64) common.Hash       { return c.repo.ReadCanonicalHash(height) }

func (c *testChain) HeaderByHeight(height uint64) *types.Header {
	c.reads++
	hash := c.repo.ReadCanonicalHash(height)
	if hash == (common.Hash{}) {
		return nil
	}
	return c.repo.ReadBlockHeader(hash)
}

func (c *testChain) IdentityStateDiff(height uint64) *state.IdentityStateDiff {
	data := c.repo.ReadIdentityStateDiff(height)
	if data == nil {
		return nil
	}
	diff := new(state.IdentityStateDiff)
	_ = diff.FromBytes(data)
	return diff
}

func (c *testChain) SavedTxs(address common.Address, count int, token []byte) ([]*types.SavedTransaction, []byte) {
	return c.repo.GetSavedTxs(address, count, token)
}

const blockInterval = 10 * time.Minute

// ceremonyAt: a validation started at the time; the ceremony block comes 3 blocks after the short session block.
type ceremonyAt struct {
	validation time.Time
}

type fixture struct {
	t         *testing.T
	memdb     *countingDB
	bus       eventbus.Bus
	appState  *appstate.AppState
	repo      *database.Repo
	chain     *testChain
	store     *Store
	summaries *validationsummary.Store
	service   *Service
	own       map[common.Address]bool
	// vf: the ceremony block heights, oldest first; firstEpoch: the epoch the first one ended.
	vf         []uint64
	firstEpoch uint16
}

// newFixture stores a chain of blocks every 10 minutes from start to the last ceremony + tail, with the given
// ceremonies; the state stands at its head.
func newFixture(t *testing.T, start time.Time, ceremonies []ceremonyAt, firstEpoch uint16, tail time.Duration) *fixture {
	memdb := &countingDB{DB: db.NewMemDB()}
	bus := eventbus.New()
	appState, err := appstate.NewAppState(memdb, bus)
	require.NoError(t, err)
	require.NoError(t, appState.Initialize(0))
	repo := database.NewRepo(memdb)
	f := &fixture{t: t, memdb: memdb, bus: bus, appState: appState, repo: repo, firstEpoch: firstEpoch,
		own: map[common.Address]bool{}}

	end := ceremonies[len(ceremonies)-1].validation.Add(tail)
	next := 0
	var vfAt uint64
	var parent common.Hash
	var head *types.Header
	for height, t0 := uint64(1), start; !t0.After(end); height, t0 = height+1, t0.Add(blockInterval) {
		var flags types.BlockFlag
		if next < len(ceremonies) && !t0.Before(ceremonies[next].validation) {
			flags |= types.ShortSessionStarted
			vfAt = height + 3
			next++
		}
		if height == vfAt {
			flags |= types.ValidationFinished
			f.vf = append(f.vf, height)
		}
		header := &types.Header{ProposedHeader: &types.ProposedHeader{Height: height, Time: t0.Unix(), Flags: flags,
			ParentHash: parent}}
		repo.WriteBlockHeader(header)
		repo.WriteCanonicalHash(height, header.Hash())
		parent, head = header.Hash(), header
	}
	require.Len(t, f.vf, len(ceremonies), "every ceremony block is below the head")

	last := firstEpoch + uint16(len(f.vf)) - 1
	appState.State.SetGlobalEpoch(last + 1)
	for _, h := range f.vf[max(0, len(f.vf)-3) : len(f.vf)-1] {
		appState.State.AddPrevEpochBlock(h)
	}
	appState.State.SetEpochBlock(f.vf[len(f.vf)-1])
	_, _, _, err = appState.State.Commit(true)
	require.NoError(t, err)

	f.chain = &testChain{repo: repo, head: head, appState: appState}
	f.store = NewStore(memdb)
	f.summaries = validationsummary.NewStore(memdb)
	f.service = NewService(f.store, f.chain, memdb, f.summaries, func(a common.Address) bool { return f.own[a] })
	return f
}

func (f *fixture) expected() map[uint16]uint64 {
	result := make(map[uint16]uint64)
	for i, h := range f.vf {
		result[f.firstEpoch+uint16(i)] = h
	}
	return result
}

func (f *fixture) recorded() map[uint16]uint64 {
	ceremonies, err := f.store.Ceremonies()
	require.NoError(f.t, err)
	result := make(map[uint16]uint64)
	for epoch, c := range ceremonies {
		result[epoch] = c.Height
	}
	return result
}

func day(t time.Time, n int, hour, minute int) time.Time {
	d := t.AddDate(0, 0, n)
	return time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, time.UTC)
}

// mainnetLike: 13:30 UTC ceremonies, then the v12 switch to 15:00, epochs of 1 to 7 days.
func mainnetLike(start time.Time) []ceremonyAt {
	return []ceremonyAt{
		{day(start, 2, 13, 30)}, {day(start, 9, 13, 30)}, {day(start, 10, 13, 30)}, {day(start, 15, 15, 0)},
		{day(start, 20, 15, 0)}, {day(start, 21, 15, 0)}, {day(start, 26, 15, 0)}, {day(start, 31, 15, 0)},
	}
}

var start = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

func TestPassFindsEveryCeremony(t *testing.T) {
	f := newFixture(t, start, mainnetLike(start), 40, 48*time.Hour)
	// A block of a fork left in the database, with the flag: not a canonical block.
	fork := &types.Header{ProposedHeader: &types.ProposedHeader{Height: f.vf[3] - 1, Time: 1,
		Flags: types.ValidationFinished}}
	f.repo.WriteBlockHeader(fork)
	require.NoError(t, f.service.fill())
	require.Equal(t, f.expected(), f.recorded())
	require.Equal(t, 1, f.memdb.headerPasses)

	// Nothing missing: no second pass, no search.
	f.chain.reads = 0
	require.NoError(t, f.service.fill())
	require.Equal(t, 1, f.memdb.headerPasses)
	require.Less(t, f.chain.reads, 5)

	h, err := f.service.History(addr(1))
	require.NoError(t, err)
	require.True(t, h.CeremoniesComplete)
	require.Equal(t, uint16(48), h.Epoch)
	require.Equal(t, uint16(48), h.Epochs[0].Epoch)
	require.Nil(t, h.Epochs[0].Ceremony)
	require.Equal(t, f.vf[len(f.vf)-1], h.Epochs[1].Ceremony.Height)
	require.Equal(t, uint16(40), h.Epochs[len(h.Epochs)-1].Epoch)
}

// The pass counts back from the state's epoch block; the state's previous epoch blocks must be the ones it found.
func TestPassChecksStateEpochBlocks(t *testing.T) {
	f := newFixture(t, start, mainnetLike(start), 40, 48*time.Hour)
	f.appState.State.AddPrevEpochBlock(f.vf[2])
	_, _, _, err := f.appState.State.Commit(true)
	require.NoError(t, err)
	require.Error(t, f.service.fill())
	require.Empty(t, f.recorded())
}

// A fast sync over ceremonies leaves a gap between two recorded ones: the search by block time fills it, epoch
// gaps of one day included, without reading every header again.
func TestGapSearch(t *testing.T) {
	f := newFixture(t, start, mainnetLike(start), 40, 48*time.Hour)
	require.NoError(t, f.service.fill())
	for _, epoch := range []uint16{42, 43, 44, 45} {
		require.NoError(t, f.memdb.Delete(ceremonyKey(epoch)))
	}
	f.chain.reads = 0
	require.NoError(t, f.service.fill())
	require.Equal(t, f.expected(), f.recorded())
	require.Equal(t, 1, f.memdb.headerPasses)
	t.Logf("gap of 4 ceremonies: %d header reads", f.chain.reads)

	// The top of the gap is the state's epoch block when the last ceremonies were skipped.
	for _, epoch := range []uint16{45, 46, 47} {
		require.NoError(t, f.memdb.Delete(ceremonyKey(epoch)))
	}
	require.NoError(t, f.service.fill())
	require.Equal(t, f.expected(), f.recorded())
	require.Equal(t, 1, f.memdb.headerPasses)
}

// A ceremony at a time the search does not try (here 14:00 UTC) leaves the gap short of an epoch: the search is
// refused and the full pass runs again.
func TestGapSearchFallsBackToPass(t *testing.T) {
	ceremonies := mainnetLike(start)
	ceremonies[4] = ceremonyAt{day(start, 20, 14, 0)}
	f := newFixture(t, start, ceremonies, 40, 48*time.Hour)
	require.NoError(t, f.service.fill())
	require.Equal(t, f.expected(), f.recorded())
	for _, epoch := range []uint16{43, 44, 45} {
		require.NoError(t, f.memdb.Delete(ceremonyKey(epoch)))
	}
	require.NoError(t, f.service.fill())
	require.Equal(t, f.expected(), f.recorded())
	require.Equal(t, 2, f.memdb.headerPasses)
}

// A ceremony block a reset could still revert is left to the recorder.
func TestFillWritesOnlyFinalBlocks(t *testing.T) {
	f := newFixture(t, start, mainnetLike(start), 40, 2*time.Hour)
	require.NoError(t, f.service.fill())
	expected := f.expected()
	delete(expected, 47)
	require.Equal(t, expected, f.recorded())

	h, err := f.service.History(addr(1))
	require.NoError(t, err)
	require.False(t, h.CeremoniesComplete)
}

func block(height uint64, timestamp int64, flags types.BlockFlag) *types.Block {
	return &types.Block{Header: &types.Header{ProposedHeader: &types.ProposedHeader{Height: height, Time: timestamp,
		Flags: flags}}}
}

func TestRecorderMiningAndReset(t *testing.T) {
	f := newFixture(t, start, mainnetLike(start), 40, 48*time.Hour)
	me, pool, other := addr(1), addr(2), addr(3)
	f.own[me] = true
	r := NewRecorder(nil, f.store, f.appState, func(a common.Address) bool { return f.own[a] }, f.bus)
	require.NoError(t, f.service.fill())
	epoch := f.appState.State.Epoch()
	// New blocks above the stored chain.
	base := f.chain.head.Height()

	// As the chain adds a block: collect while applying it, publish it, then complete (blockchain.go AddBlock).
	apply := func(height uint64, flags types.BlockFlag, rewards func()) {
		r.EnableCollecting()
		rewards()
		f.bus.Publish(&events.NewBlockEvent{Block: block(height, int64(height)*20, flags)})
		r.CompleteCollecting()
	}

	apply(base, 0, func() {
		r.AddProposerReward(me, me, dna(4), dna(1), nil)
		r.AddFinalCommitteeReward(me, me, dna(2), dna(1), nil)
		r.AddFinalCommitteeReward(other, other, dna(2), dna(1), nil)
	})
	apply(base+1, 0, func() {
		// Mining for a pool: the own identity gets the stake part, the pool the balance part.
		r.AddPenaltyBurntCoins(pool, dna(1))
		r.AddFinalCommitteeReward(pool, me, dna(2), dna(1), nil)
	})
	apply(base+2, 0, func() {
		r.AddPenaltyBurntCoins(me, dna(1))
		r.AddFinalCommitteeReward(me, me, dna(3), dna(1), nil)
	})
	m, err := f.store.Mining(me, epoch)
	require.NoError(t, err)
	require.Equal(t, uint32(1), m.ProposedBlocks)
	require.Equal(t, dna(5), m.ProposerReward)
	require.Equal(t, uint32(3), m.CommitteeBlocks)
	require.Equal(t, dna(8), m.CommitteeReward)
	require.Equal(t, dna(1), m.PenaltyBurnt)
	otherMining, err := f.store.Mining(other, epoch)
	require.NoError(t, err)
	require.Nil(t, otherMining)

	// A reset to base undoes the two blocks above it.
	f.bus.Publish(&events.BlockchainResetEvent{Header: block(base, 0, 0).Header})
	m, err = f.store.Mining(me, epoch)
	require.NoError(t, err)
	require.Equal(t, uint32(1), m.CommitteeBlocks)
	require.Equal(t, dna(3), m.CommitteeReward)
	require.Equal(t, 0, m.PenaltyBurnt.Sign())

	// A ceremony block records the epoch it ended; the state is in the next epoch already.
	f.appState.State.SetGlobalEpoch(epoch + 1)
	apply(base+1, types.ValidationFinished, func() {})
	c, err := f.store.Ceremony(epoch)
	require.NoError(t, err)
	require.Equal(t, base+1, c.Height)
	select {
	case <-r.Gaps():
		t.Fatal("no gap: the epoch before is recorded")
	default:
	}

	// Blocks after a range the node did not apply, and a ceremony whose previous epoch is unknown: a gap.
	f.appState.State.SetGlobalEpoch(epoch + 3)
	apply(base+500, types.ValidationFinished, func() {})
	select {
	case <-r.Gaps():
	default:
		t.Fatal("gap not signalled")
	}
	f.bus.Publish(&events.BlockchainResetEvent{Header: block(base, 0, 0).Header})
	c, err = f.store.Ceremony(epoch)
	require.NoError(t, err)
	require.Nil(t, c, "the reset removed the ceremony block above it")
}

func TestValidatedBy(t *testing.T) {
	f := newFixture(t, start, mainnetLike(start), 40, 48*time.Hour)
	validated, _ := (&state.ApprovedIdentity{Validated: true}).ToBytes()
	online, _ := (&state.ApprovedIdentity{Validated: false, Online: true}).ToBytes()
	diff := &state.IdentityStateDiff{Values: []*state.IdentityStateDiffValue{
		{Address: addr(1), Value: validated},
		{Address: addr(2), Deleted: true},
		{Address: addr(3), Value: online},
	}}
	data, err := diff.ToBytes()
	require.NoError(t, err)
	f.repo.WriteIdentityStateDiff(f.vf[3], data)
	require.NoError(t, f.service.fill())

	check := func(a common.Address, expected *bool) {
		h, err := f.service.History(a)
		require.NoError(t, err)
		for _, e := range h.Epochs {
			if e.Ceremony != nil && e.Ceremony.Height == f.vf[3] {
				require.Equal(t, expected, e.Validated)
				return
			}
		}
		t.Fatal("ceremony not listed")
	}
	yes, no := true, false
	check(addr(1), &yes)
	check(addr(2), &no)
	check(addr(3), &no)
	check(addr(4), nil)
}

func TestTransactionsAndScores(t *testing.T) {
	f := newFixture(t, start, mainnetLike(start), 40, 48*time.Hour)
	key, _ := crypto.GenerateKey()
	me := crypto.PubkeyToAddress(key.PublicKey)
	f.own[me] = true
	send := func(epoch uint16, txType types.TxType, nonce uint32) {
		tx, err := types.SignTx(&types.Transaction{Type: txType, Epoch: epoch, AccountNonce: nonce, To: &me}, key)
		require.NoError(t, err)
		f.repo.SaveTx(me, common.Hash{}, int64(nonce), big.NewInt(1), tx)
	}
	// Answers sent in 44, 45 and 46; 47 has a summary (not missed), 46 one (missed despite the answers).
	nonce := uint32(1)
	for _, e := range []uint16{44, 45, 46} {
		send(e, types.SubmitFlipTx, nonce)
		send(e, types.SubmitShortAnswersTx, nonce+1)
		send(e, types.SubmitLongAnswersTx, nonce+2)
		nonce += 3
	}
	send(47, types.SendTx, nonce)
	require.NoError(t, f.summaries.Write(47, false, []*validationsummary.Summary{{Epoch: 47, Address: me,
		Participated: true}}))
	require.NoError(t, f.summaries.Write(46, false, []*validationsummary.Summary{{Epoch: 46, Address: me,
		Participated: true, Missed: true}}))
	for _, score := range []byte{0x13, 0x24, 0x35} {
		f.appState.State.AddNewScore(me, score)
	}
	_, _, _, err := f.appState.State.Commit(true)
	require.NoError(t, err)
	require.NoError(t, f.service.fill())

	h, err := f.service.History(me)
	require.NoError(t, err)
	require.True(t, h.Own)
	byEpoch := make(map[uint16]*Epoch)
	for _, e := range h.Epochs {
		byEpoch[e.Epoch] = e
	}
	require.Equal(t, &Transactions{Sent: 3, Flips: 1, ShortAnswers: true, LongAnswers: true}, byEpoch[45].Transactions)
	require.Equal(t, &Transactions{Sent: 1}, byEpoch[47].Transactions)
	require.True(t, byEpoch[46].Summary.Missed)

	// 47 from its summary; 46 missed; before the summaries, 45 and 44 had both answers: two epochs for the two
	// scores left.
	require.Len(t, h.Scores, 3)
	epochs := []uint16{44, 45, 47}
	for i, s := range h.Scores {
		require.NotNil(t, s.Epoch)
		require.Equal(t, epochs[i], *s.Epoch)
	}

	// With answers sent in one more epoch than there are scores left, those scores stay without epoch.
	send(43, types.SubmitShortAnswersTx, 100)
	send(43, types.SubmitLongAnswersTx, 101)
	h, err = f.service.History(me)
	require.NoError(t, err)
	require.Nil(t, h.Scores[0].Epoch)
	require.Nil(t, h.Scores[1].Epoch)
	require.Equal(t, uint16(47), *h.Scores[2].Epoch)

	// Another address: no transactions, no mining.
	h, err = f.service.History(addr(9))
	require.NoError(t, err)
	require.False(t, h.Own)
	for _, e := range h.Epochs {
		require.Nil(t, e.Transactions)
		require.Nil(t, e.Mining)
	}
}

// At epoch 0 (a new network) no epoch has ended: the history is complete and asks for no fill.
func TestHistoryCompleteAtEpochZero(t *testing.T) {
	memdb := &countingDB{DB: db.NewMemDB()}
	appState, err := appstate.NewAppState(memdb, eventbus.New())
	require.NoError(t, err)
	require.NoError(t, appState.Initialize(0))
	repo := database.NewRepo(memdb)
	head := &types.Header{ProposedHeader: &types.ProposedHeader{Height: 1, Time: start.Unix()}}
	repo.WriteBlockHeader(head)
	repo.WriteCanonicalHash(1, head.Hash())
	service := NewService(NewStore(memdb), &testChain{repo: repo, head: head, appState: appState}, memdb,
		validationsummary.NewStore(memdb), func(common.Address) bool { return false })

	h, err := service.History(addr(1))
	require.NoError(t, err)
	require.Equal(t, uint16(0), h.Epoch)
	require.True(t, h.CeremoniesComplete)
	select {
	case <-service.wake:
		t.Fatal("a fill was requested")
	default:
	}
}

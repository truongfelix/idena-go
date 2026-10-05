package oraclevotings

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/crypto"
	"github.com/idena-network/idena-go/events"
	"github.com/idena-network/idena-go/stats/collector"
	"github.com/idena-network/idena-go/vm/embedded"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

func dna(n int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(n), common.DnaBase)
}

func header(height uint64, time int64) *types.Header {
	return &types.Header{ProposedHeader: &types.ProposedHeader{Height: height, Time: time}}
}

const epoch = 100

type testChain struct {
	head      *types.Header
	appState  *appstate.AppState
	validated map[common.Address]map[uint16]bool
}

func (c *testChain) Head() *types.Header { return c.head }

func (c *testChain) ReadonlyAppState() (*appstate.AppState, error) { return c.appState, nil }

// Every block is 20 s after block 0 at time 0.
func (c *testChain) HeaderTime(height uint64) (int64, bool) {
	if height > c.head.Height() {
		return 0, false
	}
	return int64(height) * 20, true
}

func (c *testChain) ValidatedIn(addr common.Address, e uint16) (bool, bool) {
	v, ok := c.validated[addr][e]
	return v, ok
}

type fixture struct {
	t        *testing.T
	memdb    db.DB
	bus      eventbus.Bus
	appState *appstate.AppState
	store    *Store
	chain    *testChain
	service  *Service
}

func newFixture(t *testing.T, height uint64) *fixture {
	memdb := db.NewMemDB()
	bus := eventbus.New()
	appState, err := appstate.NewAppState(memdb, bus)
	require.NoError(t, err)
	require.NoError(t, appState.Initialize(0))
	appState.State.SetGlobalEpoch(epoch)
	store := NewStore(memdb)
	chain := &testChain{head: header(height, int64(height)*20), appState: appState,
		validated: map[common.Address]map[uint16]bool{}}
	return &fixture{t, memdb, bus, appState, store, chain, NewService(store, chain)}
}

// voting is the storage a deployed (and maybe started) OracleVoting holds, as the contract writes it.
type voting struct {
	addr                 common.Address
	owner                common.Address
	state                byte
	startTime            uint64
	startBlock           uint64
	epoch                uint16
	votingDuration       uint64
	publicVotingDuration uint64
	committeeSize        uint64
	network              uint64
	quorum, threshold    byte
	ownerFee             byte
	minPayment           *big.Int
	balance              *big.Int
	votedCount           uint64
	secretVotes          uint64
	voters               []common.Address
	optionVotes          map[byte]uint64
}

func (f *fixture) deploy(v voting) {
	s := f.appState.State
	s.DeployContract(v.addr, embedded.OracleVotingContract, dna(10))
	if v.balance != nil {
		s.SetBalance(v.addr, v.balance)
	}
	set := func(k string, value []byte) { s.SetContractValue(v.addr, []byte(k), value) }
	set("owner", v.owner.Bytes())
	set("state", []byte{v.state})
	set("startTime", common.ToBytes(v.startTime))
	set("votingDuration", common.ToBytes(v.votingDuration))
	set("publicVotingDuration", common.ToBytes(v.publicVotingDuration))
	set("committeeSize", common.ToBytes(v.committeeSize))
	set("cs", common.ToBytes(v.committeeSize))
	set("ns", common.ToBytes(v.network))
	set("quorum", []byte{v.quorum})
	set("winnerThreshold", []byte{v.threshold})
	set("ownerFee", []byte{v.ownerFee})
	set("fact", []byte(`{"title":"t"}`))
	if v.minPayment != nil {
		set("votingMinPayment", v.minPayment.Bytes())
	}
	if v.state != contractPending {
		set("startBlock", common.ToBytes(v.startBlock))
		set("epoch", common.ToBytes(v.epoch))
		set("network", common.ToBytes(v.network))
		set("vrfSeed", []byte{1, 2, 3})
		set("votedCount", common.ToBytes(v.votedCount))
		set("secretVotesCount", common.ToBytes(v.secretVotes))
	}
	for _, voter := range v.voters {
		set("votes"+string(voter.Bytes()), []byte{1})
	}
	for option, count := range v.optionVotes {
		set("voteOptions"+string([]byte{option}), common.ToBytes(count))
		set("allVotes"+string([]byte{option}), common.ToBytes(count))
	}
}

func (f *fixture) identity(addr common.Address, st state.IdentityState) {
	key, _ := crypto.GenerateKey()
	f.appState.State.SetState(addr, st)
	f.appState.State.SetPubKey(addr, crypto.FromECDSAPub(&key.PublicKey))
}

func (f *fixture) get(addr common.Address, oracle *common.Address) *Voting {
	v, err := f.service.Voting(addr, oracle)
	require.NoError(f.t, err)
	return v
}

func addr(b byte) common.Address {
	return common.Address{b}
}

// The whole committee: the committee size is the network size.
func started(a common.Address, startBlock uint64) voting {
	return voting{addr: a, owner: addr(0xee), state: contractStarted, startBlock: startBlock, epoch: epoch,
		votingDuration: 100, publicVotingDuration: 50, committeeSize: 10, network: 10, quorum: 20, threshold: 51,
		minPayment: dna(1), balance: dna(100)}
}

func TestStates(t *testing.T) {
	f := newFixture(t, 1000)
	oracle := addr(1)
	f.identity(oracle, state.Verified)

	pending := started(addr(0x10), 0)
	pending.state = contractPending
	f.deploy(pending)

	open := started(addr(0x11), 950)
	f.deploy(open)

	voted := started(addr(0x12), 950)
	voted.voters = []common.Address{oracle}
	voted.votedCount = 1
	f.deploy(voted)

	counting := started(addr(0x13), 850) // secret voting over, quorum reached
	counting.secretVotes = 5
	f.deploy(counting)

	noQuorum := started(addr(0x14), 850) // secret voting over without a quorum: can be prolonged
	f.deploy(noQuorum)

	oldEpoch := started(addr(0x15), 950) // drawn in an older epoch: must be prolonged before votes
	oldEpoch.epoch = epoch - 1
	f.deploy(oldEpoch)

	finished := started(addr(0x16), 500)
	finished.state = contractFinished
	f.deploy(finished)

	for a, want := range map[common.Address]string{
		pending.addr: StatePending, open.addr: StateOpen, voted.addr: StateVoted, counting.addr: StateCounting,
		noQuorum.addr: StateCanBeProlonged, oldEpoch.addr: StateCanBeProlonged, finished.addr: StateArchive,
	} {
		require.Equal(t, want, f.get(a, &oracle).State, a.Hex())
	}
	// Voted is relative to the oracle.
	require.Equal(t, StateOpen, f.get(voted.addr, nil).State)

	// The stored epoch is 2 bytes.
	require.Equal(t, uint64(epoch), *f.get(open.addr, nil).CommitteeEpoch)
	// Times: block 1000 at 20 s per block, voting until block 1050, public voting until 1100.
	v := f.get(open.addr, nil)
	require.Equal(t, int64(1050*20), v.EstimatedVotingFinishTime.Unix())
	require.Equal(t, int64(1100*20), v.EstimatedPublicVotingFinishTime.Unix())
	require.Nil(t, v.VotingFinishTime)
	require.Equal(t, int64(950*20), v.CreateTime.Unix(), "found by the scan: the start block's time")
	c := f.get(counting.addr, nil)
	require.Equal(t, int64(950*20), c.VotingFinishTime.Unix())
	require.Nil(t, c.EstimatedVotingFinishTime)
}

func TestIsOracle(t *testing.T) {
	f := newFixture(t, 1000)
	verified, candidate, suspended, voter := addr(1), addr(2), addr(3), addr(4)
	f.identity(verified, state.Verified)
	f.identity(candidate, state.Candidate)
	f.identity(suspended, state.Suspended)
	f.identity(voter, state.Suspended)

	open := started(addr(0x11), 950)
	open.voters = []common.Address{voter}
	f.deploy(open)
	// Taking votes: the identities validated now.
	require.True(t, f.get(open.addr, &verified).IsOracle)
	require.False(t, f.get(open.addr, &candidate).IsOracle)
	require.False(t, f.get(open.addr, &suspended).IsOracle)
	require.True(t, f.get(open.addr, &voter).IsOracle)

	// An earlier draw: the identity's state in the draw's epoch, when the node recorded it; otherwise voters only.
	finished := started(addr(0x12), 500)
	finished.state = contractFinished
	finished.epoch = epoch - 1
	finished.voters = []common.Address{voter}
	f.deploy(finished)
	f.chain.validated[suspended] = map[uint16]bool{epoch - 1: true}
	f.chain.validated[verified] = map[uint16]bool{epoch - 1: false}
	require.True(t, f.get(finished.addr, &suspended).IsOracle, "validated then")
	require.False(t, f.get(finished.addr, &verified).IsOracle, "not validated then")
	require.False(t, f.get(finished.addr, &candidate).IsOracle, "not recorded")
	require.True(t, f.get(finished.addr, &voter).IsOracle)

	// The draw hashes whatever public key the state holds, none included (a genesis identity), as the contract does.
	noKey := addr(5)
	f.appState.State.SetState(noKey, state.Human)
	require.True(t, f.get(open.addr, &noKey).IsOracle)

	// A started voting left in an older epoch has no committee until it is prolonged.
	old := started(addr(0x13), 950)
	old.epoch = epoch - 1
	f.deploy(old)
	require.False(t, f.get(old.addr, &verified).IsOracle)

	// Nobody is drawn when the committee is tiny next to the network (the draw is the contract's).
	small := started(addr(0x14), 950)
	small.committeeSize, small.network = 1, 1_000_000_000
	f.deploy(small)
	require.False(t, f.get(small.addr, &verified).IsOracle)
}

func TestEstimatedRewards(t *testing.T) {
	f := newFixture(t, 1000)
	v := started(addr(0x11), 950)
	f.deploy(v)
	got := f.get(v.addr, nil)
	// Committee 10 paying 1 each: (100 + 10) / 10.
	require.Equal(t, "11", got.EstimatedOracleReward.String())
	// Quorum 20% of 10 = 2 voters, 51% of them = 2 winners: (100 + 2) / 2.
	require.Equal(t, "51", got.EstimatedMaxOracleReward.String())
	require.Equal(t, "100", got.EstimatedTotalReward.String())

	// A 10% owner fee is taken from the fund less the payments (the contract's calculateOwnerReward).
	fee := started(addr(0x12), 950)
	fee.ownerFee = 10
	f.deploy(fee)
	got = f.get(fee.addr, nil)
	require.Equal(t, "10", got.EstimatedOracleReward.String()) // (110 - (110-10)·0.1) / 10
	require.Equal(t, "90", got.EstimatedTotalReward.String())
}

func TestListFiltersAndPages(t *testing.T) {
	f := newFixture(t, 1000)
	oracle := addr(1)
	f.identity(oracle, state.Human)
	for i := byte(0); i < 5; i++ {
		v := started(addr(0x20+i), 910+uint64(i)*10)
		v.balance = dna(int64(100 + i))
		f.deploy(v)
	}
	pending := started(addr(0x30), 0)
	pending.state = contractPending
	pending.owner = oracle
	f.deploy(pending)

	// All open votings by reward (the default for open/pending), two per page, each once.
	var got []common.Address
	token := ""
	for pages := 0; ; pages++ {
		res, err := f.service.Votings(ListArgs{All: true, Oracle: &oracle, States: []string{"open"}, Limit: 2,
			ContinuationToken: token})
		require.NoError(t, err)
		for _, v := range res.Result {
			got = append(got, v.ContractAddress)
		}
		if res.ContinuationToken == nil {
			break
		}
		token = *res.ContinuationToken
		require.Less(t, pages, 5)
	}
	require.Equal(t, []common.Address{addr(0x24), addr(0x23), addr(0x22), addr(0x21), addr(0x20)}, got)

	// The oracle's committees: a pending voting has none.
	res, err := f.service.Votings(ListArgs{Oracle: &oracle, Limit: 100})
	require.NoError(t, err)
	require.Len(t, res.Result, 5)
	// Without an oracle there is no committee to list; nothing matches: no result at all (as the indexer).
	res, err = f.service.Votings(ListArgs{Limit: 100})
	require.NoError(t, err)
	data, _ := json.Marshal(res)
	require.Equal(t, "{}", string(data))
	// The address's own votings: the one it created.
	res, err = f.service.Votings(ListArgs{Address: &oracle, Limit: 100})
	require.NoError(t, err)
	require.Len(t, res.Result, 1)
	require.Equal(t, StatePending, res.Result[0].State)

	_, err = f.service.Votings(ListArgs{All: true, States: []string{"nope"}})
	require.Error(t, err)
	_, err = f.service.Votings(ListArgs{All: true, Limit: MaxLimit + 1})
	require.Error(t, err)
}

// signedTx is a contract call from a fresh key.
func signedTx(t *testing.T, txType types.TxType, to common.Address, amount *big.Int) (*types.Transaction, common.Address) {
	key, _ := crypto.GenerateKey()
	tx, err := types.SignTx(&types.Transaction{Type: txType, To: &to, Amount: amount, MaxFee: dna(1)}, key)
	require.NoError(t, err)
	return tx, crypto.PubkeyToAddress(key.PublicKey)
}

func TestRecorder(t *testing.T) {
	f := newFixture(t, 1000)
	me := addr(0x01)
	rec := NewRecorder(nil, f.store, func(a common.Address) bool { return a == me }, f.bus)
	s := f.appState.State
	votingAddr := addr(0x40)
	balances := map[common.Address]*big.Int{}
	getBalance := func(a common.Address) *big.Int {
		if b, ok := balances[a]; ok {
			return b
		}
		return s.GetBalance(a)
	}
	pay := func(to common.Address, amount *big.Int) {
		next := new(big.Int).Add(getBalance(to), amount)
		rec.AddContractBalanceUpdate(nil, to, getBalance, next, f.appState, nil)
		balances[to] = next
	}
	block := func(height uint64) {
		f.bus.Publish(&events.NewBlockEvent{Block: &types.Block{Header: header(height, int64(height)*20)}})
		rec.CompleteCollecting()
	}

	// Block 1001: the deploy.
	rec.EnableCollecting()
	deployTx, _ := signedTx(t, types.DeployContractTx, votingAddr, nil)
	rec.BeginApplyingTx(deployTx, f.appState)
	rec.AddOracleVotingDeploy(votingAddr, 0, nil, nil, 0, 0, 0, 0, 0, 0, 0, 0, nil, nil, nil, nil)
	f.deploy(started(votingAddr, 0))
	rec.AddTxReceipt(&types.TxReceipt{Success: true, ContractAddress: votingAddr}, f.appState)
	rec.CompleteApplyingTx(f.appState)
	block(1001)
	r, err := f.store.Record(votingAddr)
	require.NoError(t, err)
	require.Equal(t, uint64(1001), r.CreateHeight)
	require.Equal(t, int64(1001*20), r.CreateTime)

	// Block 1002: a failed finish changes nothing, a successful one pays me and someone else.
	rec.EnableCollecting()
	failed, _ := signedTx(t, types.CallContractTx, votingAddr, nil)
	rec.BeginApplyingTx(failed, f.appState)
	pay(me, dna(5))
	rec.AddOracleVotingCallFinish(contractFinished, nil, dna(50), dna(5), dna(0))
	rec.AddTxReceipt(&types.TxReceipt{Success: false, ContractAddress: votingAddr}, f.appState)
	rec.CompleteApplyingTx(f.appState)
	balances = map[common.Address]*big.Int{}
	finish, sender := signedTx(t, types.CallContractTx, votingAddr, nil)
	rec.BeginApplyingTx(finish, f.appState)
	pay(me, dna(7))
	pay(addr(0x02), dna(7))
	rec.AddOracleVotingCallFinish(contractFinished, nil, dna(14), dna(7), dna(0))
	rec.AddTxReceipt(&types.TxReceipt{Success: true, ContractAddress: votingAddr, Method: "finishVoting",
		GasCost: big.NewInt(3)}, f.appState)
	rec.CompleteApplyingTx(f.appState)
	block(1002)
	r, err = f.store.Record(votingAddr)
	require.NoError(t, err)
	require.Equal(t, uint64(1002), r.StateHeight)
	require.Equal(t, uint64(1001), r.CreateHeight)
	require.Equal(t, 0, r.Finish.Fund.Cmp(dna(14)))
	rows, err := f.store.BalanceRows(me, votingAddr)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, finish.Hash(), rows[0].Hash)
	require.Equal(t, sender, rows[0].From)
	require.Equal(t, 0, rows[0].BalanceChange.Cmp(dna(7)))
	require.Equal(t, "finishVoting", rows[0].Method)
	rows, _ = f.store.BalanceRows(addr(0x02), votingAddr)
	require.Empty(t, rows, "not the node's address")

	// Block 1003: the termination keeps the voting as it was, the state drops it.
	rec.EnableCollecting()
	terminate, _ := signedTx(t, types.TerminateContractTx, votingAddr, nil)
	rec.BeginApplyingTx(terminate, f.appState)
	rec.AddOracleVotingTermination(nil, nil, nil)
	s.DropContract(votingAddr)
	rec.AddTxReceipt(&types.TxReceipt{Success: true, ContractAddress: votingAddr}, f.appState)
	rec.CompleteApplyingTx(f.appState)
	block(1003)
	v := f.get(votingAddr, nil)
	require.Equal(t, StateTerminated, v.State)
	require.Equal(t, int64(1003*20), v.TerminationTime.Unix())
	require.Equal(t, `{"title":"t"}`, string(v.Fact))
	require.Equal(t, "14", v.TotalReward.String(), "the fund paid out at the finish")

	// A block that does not follow the last one: blocks were skipped, the next request scans the state.
	needed, _, err := f.store.scanState()
	require.NoError(t, err)
	require.False(t, needed, "scanned by the request above")
	rec.EnableCollecting()
	block(1010)
	needed, _, err = f.store.scanState()
	require.NoError(t, err)
	require.True(t, needed)
	f.bus.Publish(&events.BlockchainResetEvent{Header: header(1005, 0)})
	needed, gaps, err := f.store.scanState()
	require.NoError(t, err)
	require.True(t, needed)
	require.Equal(t, uint64(2), gaps)

	var _ collector.StatsCollector = rec
}

func TestScanMerge(t *testing.T) {
	f := newFixture(t, 1000)
	live, stale, later, terminated := addr(0x50), addr(0x51), addr(0x52), addr(0x53)
	f.deploy(started(live, 900))
	changes := &blockChanges{height: 990, time: 990 * 20, records: map[common.Address]func(*Record){
		stale:      func(r *Record) { r.StateHeight = 990 },
		terminated: func(r *Record) { r.Termination = &Termination{Height: 990} },
	}}
	require.NoError(t, f.store.writeBlock(changes))
	require.NoError(t, f.store.writeBlock(&blockChanges{height: 1001, records: map[common.Address]func(*Record){
		later: func(r *Record) { r.CreateHeight = 1001 },
	}}))
	// A scan of the state at 1000: the live voting is added, the stale one (gone from the state, no termination
	// recorded) removed; the terminated one and the one deployed after the scanned height stay.
	_, gaps, err := f.store.scanState()
	require.NoError(t, err)
	require.NoError(t, f.store.writeScan(1000, map[common.Address]*Record{live: {}}, gaps))
	records, err := f.store.Votings()
	require.NoError(t, err)
	require.Contains(t, records, live)
	require.NotContains(t, records, stale)
	require.Contains(t, records, terminated)
	require.Contains(t, records, later)
	needed, _, err := f.store.scanState()
	require.NoError(t, err)
	require.False(t, needed)

	// A gap while the scan ran leaves it to do again.
	require.NoError(t, f.store.markForScan())
	_, gaps, _ = f.store.scanState()
	require.NoError(t, f.store.markForScan())
	require.NoError(t, f.store.writeScan(1000, map[common.Address]*Record{live: {}}, gaps))
	needed, _, _ = f.store.scanState()
	require.True(t, needed)
}

func TestBalanceUpdates(t *testing.T) {
	f := newFixture(t, 1000)
	me, votingAddr := addr(0x01), addr(0x40)
	h := func(b byte) common.Hash { return common.Hash{b} }
	// Recorded: a reward in someone's finish (1002) and the refund in my own termination (1003).
	require.NoError(t, f.store.writeBlock(&blockChanges{height: 1002, time: 2002, rows: []*BalanceRow{
		{Address: me, Voting: votingAddr, Hash: h(2), Type: types.CallContractTx, From: addr(9), BalanceChange: dna(7), Success: true},
	}}))
	require.NoError(t, f.store.writeBlock(&blockChanges{height: 1003, time: 2003, rows: []*BalanceRow{
		{Address: me, Voting: votingAddr, Hash: h(3), Type: types.TerminateContractTx, From: me, BalanceChange: dna(1), Success: true},
	}}))
	// Sent, from the node's history: my vote (1001) and my termination (1003).
	sent := []*BalanceRow{
		{Address: me, Voting: votingAddr, Hash: h(1), Height: 1001, Time: 2001, Type: types.CallContractTx, From: me, Amount: dna(1), Success: true},
		{Address: me, Voting: votingAddr, Hash: h(3), Height: 1003, Time: 2003, Type: types.TerminateContractTx, From: me, Success: true},
	}
	res, err := f.service.BalanceUpdates(me, votingAddr, sent, 2, "")
	require.NoError(t, err)
	require.Len(t, res.Result, 2)
	require.Equal(t, h(3), res.Result[0].Hash)
	require.Equal(t, "1", res.Result[0].BalanceChange.String(), "merged into my own row")
	require.Equal(t, h(2), res.Result[1].Hash)
	require.Equal(t, "TerminateContract", res.Result[0].Type)
	require.NotNil(t, res.ContinuationToken)
	res, err = f.service.BalanceUpdates(me, votingAddr, sent, 2, *res.ContinuationToken)
	require.NoError(t, err)
	require.Len(t, res.Result, 1)
	require.Equal(t, h(1), res.Result[0].Hash)
	require.Nil(t, res.Result[0].BalanceChange)
	require.True(t, res.Result[0].Amount.Equal(decimal.NewFromInt(1)))
	require.Nil(t, res.ContinuationToken)
}

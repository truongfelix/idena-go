package oraclevotings

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/log"
	"github.com/idena-network/idena-go/vm/embedded"
	"github.com/pkg/errors"
	"github.com/shopspring/decimal"
)

const (
	DefaultLimit = 20
	MaxLimit     = 100
)

// Chain is what the service reads from the node: the head, the state at the head, past block times, and whether an
// identity was validated in a past epoch (known: the node recorded it, dna_validationSummary).
type Chain interface {
	Head() *types.Header
	ReadonlyAppState() (*appstate.AppState, error)
	HeaderTime(height uint64) (int64, bool)
	ValidatedIn(addr common.Address, epoch uint16) (validated, known bool)
}

// Service answers the oracle voting RPCs from the index and the state.
type Service struct {
	store  *Store
	chain  Chain
	scanMu sync.Mutex
	log    log.Logger
}

func NewService(store *Store, chain Chain) *Service {
	return &Service{store: store, chain: chain, log: log.New("component", "oraclevotings")}
}

// ListArgs selects votings like the indexer's OracleVotingContracts: by default the votings whose committee has the
// oracle (all: every voting); Address: only the votings the address created or voted in, newest first.
type ListArgs struct {
	Oracle            *common.Address `json:"oracle"`
	All               bool            `json:"all"`
	States            []string        `json:"states"`
	SortBy            string          `json:"sortBy"`
	Address           *common.Address `json:"address"`
	Limit             int             `json:"limit"`
	ContinuationToken string          `json:"continuationToken"`
}

// ListResult has no result when nothing matches (as the indexer answered).
type ListResult struct {
	Result            []*Voting `json:"result,omitempty"`
	ContinuationToken *string   `json:"continuationToken,omitempty"`
}

const (
	SortByReward    = "reward"
	SortByTimestamp = "timestamp"
)

var stateNames = map[string]string{}

func init() {
	for _, s := range []string{StatePending, StateOpen, StateVoted, StateCounting, StateArchive, StateTerminated,
		StateCanBeProlonged} {
		stateNames[strings.ToLower(s)] = s
	}
}

// snapshot reads the state at the head; the index is completed by a scan first if blocks were skipped.
func (s *Service) snapshot() (*appstate.AppState, *chainView, error) {
	head := s.chain.Head()
	if head == nil {
		return nil, nil, errors.New("no head block")
	}
	appState, err := s.chain.ReadonlyAppState()
	if err != nil {
		return nil, nil, err
	}
	if err := s.ensureScanned(appState, head.Height()); err != nil {
		return nil, nil, errors.Wrap(err, "oracle voting scan")
	}
	c := &chainView{
		height:      head.Height(),
		time:        head.Time(),
		epoch:       appState.State.Epoch(),
		networkSize: appState.ValidatorsCache.NetworkSize(),
		headerTime:  s.chain.HeaderTime,
		identity:    appState.State.GetIdentity,
		validatedIn: s.chain.ValidatedIn,
	}
	return appState, c, nil
}

// ensureScanned adds the live votings the index lacks: the votings deployed in blocks this node did not add.
func (s *Service) ensureScanned(appState *appstate.AppState, height uint64) error {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	needed, gaps, err := s.store.scanState()
	if err != nil || !needed {
		return err
	}
	start := time.Now()
	live := make(map[common.Address]*Record)
	appState.State.IterateOverAccounts(func(addr common.Address, account state.Account) {
		if account.Contract != nil && account.Contract.CodeHash == embedded.OracleVotingContract {
			live[addr] = &Record{}
		}
	})
	if err := s.store.writeScan(height, live, gaps); err != nil {
		return err
	}
	s.log.Info("Oracle votings found in the state", "height", height, "votings", len(live), "duration", time.Since(start))
	return nil
}

func isVoting(appState *appstate.AppState, addr common.Address) bool {
	hash := appState.State.GetCodeHash(addr)
	return hash != nil && *hash == embedded.OracleVotingContract
}

func readIndexed(appState *appstate.AppState, addr common.Address, record *Record) *votingData {
	if record != nil && record.Termination != nil {
		t := record.Termination
		return readVoting(addr, record, keptStorage(t.Storage), t.Balance, t.Stake)
	}
	// Not in the state: a voting of reverted blocks, or one dropped while blocks were skipped (the next scan
	// removes it).
	if !isVoting(appState, addr) {
		return nil
	}
	s := appState.State
	return readVoting(addr, record, liveStorage{s, addr}, s.GetBalance(addr), s.GetContractStake(addr))
}

// Voting returns one voting, relative to the oracle (nil: nobody in particular).
func (s *Service) Voting(addr common.Address, oracle *common.Address) (*Voting, error) {
	appState, c, err := s.snapshot()
	if err != nil {
		return nil, err
	}
	record, err := s.store.Record(addr)
	if err != nil {
		return nil, err
	}
	d := readIndexed(appState, addr, record)
	if d == nil {
		return nil, errors.New("oracle voting not found")
	}
	return d.view(c, oracle), nil
}

type listItem struct {
	voting *Voting
	key    decimal.Decimal
}

// before: the order of the list, by key then address, both descending.
func (a *listItem) before(key decimal.Decimal, addr common.Address) bool {
	if c := a.key.Cmp(key); c != 0 {
		return c > 0
	}
	return bytes.Compare(a.voting.ContractAddress.Bytes(), addr.Bytes()) > 0
}

func (a *listItem) token() string {
	return a.key.String() + ":" + a.voting.ContractAddress.Hex()
}

func parseToken(token string) (decimal.Decimal, common.Address, error) {
	i := strings.LastIndex(token, ":")
	if i < 0 {
		return decimal.Zero, common.Address{}, errors.New("invalid continuation token")
	}
	key, err := decimal.NewFromString(token[:i])
	if err != nil {
		return decimal.Zero, common.Address{}, errors.Wrap(err, "invalid continuation token")
	}
	addr := common.HexToAddress(token[i+1:])
	return key, addr, nil
}

func limitOf(limit int) (int, error) {
	if limit == 0 {
		return DefaultLimit, nil
	}
	if limit < 0 || limit > MaxLimit {
		return 0, errors.Errorf("limit must be between 1 and %v", MaxLimit)
	}
	return limit, nil
}

// rewardKey: what an oracle may expect, the indexer's order for open and pending votings.
func (d *votingData) rewardKey() decimal.Decimal {
	committee := d.deployCommittee
	if committee == 0 {
		committee = 1
	}
	fee := decimal.NewFromInt(int64(d.ownerFee)).Div(decimal.NewFromInt(100))
	key := toDna(d.balance).Mul(decimal.NewFromInt(1).Sub(fee)).Div(decimal.NewFromInt(int64(committee)))
	if d.minPayment != nil {
		key = key.Add(fee.Mul(toDna(d.minPayment)))
	}
	return key
}

// stateKey: the block of the last state change (deploy, start, prolongation, finish, termination); for a voting
// found by a scan, its start block.
func (d *votingData) stateKey() decimal.Decimal {
	if d.record != nil && d.record.StateHeight > 0 {
		return decimal.NewFromInt(int64(d.record.StateHeight))
	}
	return decimal.NewFromInt(int64(d.startBlock))
}

// createKey: the block that deployed the voting; for a voting found by a scan, its start block.
func (d *votingData) createKey() decimal.Decimal {
	if d.record != nil && d.record.CreateHeight > 0 {
		return decimal.NewFromInt(int64(d.record.CreateHeight))
	}
	return decimal.NewFromInt(int64(d.startBlock))
}

// Votings lists the votings matching args, a page at a time.
func (s *Service) Votings(args ListArgs) (*ListResult, error) {
	limit, err := limitOf(args.Limit)
	if err != nil {
		return nil, err
	}
	states := make(map[string]bool)
	onlyOpenOrPending := len(args.States) > 0
	for _, arg := range args.States {
		for _, name := range strings.Split(arg, ",") {
			state, ok := stateNames[strings.ToLower(strings.TrimSpace(name))]
			if !ok {
				return nil, errors.Errorf("unknown state %q", name)
			}
			states[state] = true
			onlyOpenOrPending = onlyOpenOrPending && (state == StateOpen || state == StatePending)
		}
	}
	sortBy := strings.ToLower(args.SortBy)
	switch sortBy {
	case "":
		sortBy = SortByTimestamp
		if onlyOpenOrPending {
			sortBy = SortByReward
		}
	case SortByReward, SortByTimestamp:
	default:
		return nil, errors.Errorf("unknown sortBy %q", args.SortBy)
	}
	if args.Address == nil && !args.All && args.Oracle == nil {
		return &ListResult{}, nil
	}
	relativeTo := args.Oracle
	if args.Address != nil {
		relativeTo = args.Address
	}

	appState, c, err := s.snapshot()
	if err != nil {
		return nil, err
	}
	records, err := s.store.Votings()
	if err != nil {
		return nil, err
	}
	var items []*listItem
	for addr, record := range records {
		d := readIndexed(appState, addr, record)
		if d == nil {
			continue
		}
		switch {
		case args.Address != nil:
			if common.BytesToAddress(d.s.value("owner")) != *args.Address && !d.voted(*args.Address) {
				continue
			}
		case !args.All:
			if !d.isOracle(*args.Oracle, c) {
				continue
			}
		}
		v := d.view(c, relativeTo)
		if len(states) > 0 && !states[v.State] {
			continue
		}
		item := &listItem{voting: v}
		switch {
		case args.Address != nil:
			item.key = d.createKey()
		case sortBy == SortByReward:
			item.key = d.rewardKey()
		default:
			item.key = d.stateKey()
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].before(items[j].key, items[j].voting.ContractAddress)
	})
	if args.ContinuationToken != "" {
		key, addr, err := parseToken(args.ContinuationToken)
		if err != nil {
			return nil, err
		}
		start := sort.Search(len(items), func(i int) bool { return !items[i].before(key, addr) })
		items = items[start:]
	}
	res := &ListResult{}
	if len(items) > limit {
		token := items[limit].token()
		res.ContinuationToken = &token
		items = items[:limit]
	}
	for _, item := range items {
		res.Result = append(res.Result, item.voting)
	}
	return res, nil
}

// BalanceUpdate is a row of the indexer's Address/{address}/Contract/{contract}/BalanceUpdates.
type BalanceUpdate struct {
	Hash            common.Hash      `json:"hash"`
	Type            string           `json:"type"`
	Timestamp       time.Time        `json:"timestamp"`
	From            common.Address   `json:"from"`
	To              *common.Address  `json:"to,omitempty"`
	Amount          decimal.Decimal  `json:"amount"`
	Tips            decimal.Decimal  `json:"tips"`
	MaxFee          decimal.Decimal  `json:"maxFee"`
	Fee             decimal.Decimal  `json:"fee"`
	Address         common.Address   `json:"address"`
	ContractAddress common.Address   `json:"contractAddress"`
	ContractType    string           `json:"contractType"`
	BalanceChange   *decimal.Decimal `json:"balanceChange,omitempty"`
	TxReceipt       *ReceiptView     `json:"txReceipt,omitempty"`
}

type ReceiptView struct {
	Success  bool            `json:"success"`
	GasUsed  uint64          `json:"gasUsed"`
	GasCost  decimal.Decimal `json:"gasCost"`
	Method   string          `json:"method,omitempty"`
	ErrorMsg string          `json:"errorMsg,omitempty"`
}

type BalanceUpdatesResult struct {
	Result            []*BalanceUpdate `json:"result,omitempty"`
	ContinuationToken *string          `json:"continuationToken,omitempty"`
}

var txTypeNames = map[types.TxType]string{
	types.SendTx:              "SendTx",
	types.DeployContractTx:    "DeployContract",
	types.CallContractTx:      "CallContract",
	types.TerminateContractTx: "TerminateContract",
}

func (row *BalanceRow) update() *BalanceUpdate {
	u := &BalanceUpdate{
		Hash:            row.Hash,
		Type:            txTypeNames[row.Type],
		Timestamp:       unixTime(row.Time),
		From:            row.From,
		To:              row.To,
		Amount:          toDna(row.Amount),
		Tips:            toDna(row.Tips),
		MaxFee:          toDna(row.MaxFee),
		Fee:             toDna(row.Fee),
		Address:         row.Address,
		ContractAddress: row.Voting,
		ContractType:    "OracleVoting",
		BalanceChange:   toDnaPtr(row.BalanceChange),
	}
	if u.Type == "" {
		u.Type = fmt.Sprintf("0x%x", row.Type)
	}
	if row.Type != types.SendTx {
		u.TxReceipt = &ReceiptView{Success: row.Success, GasUsed: row.GasUsed, GasCost: toDna(row.GasCost),
			Method: row.Method, ErrorMsg: row.Error}
	}
	return u
}

func rowToken(row *BalanceRow) string {
	return strconv.FormatUint(row.Height, 10) + ":" + row.Hash.Hex()
}

func rowBefore(a *BalanceRow, height uint64, hash common.Hash) bool {
	if a.Height != height {
		return a.Height > height
	}
	return bytes.Compare(a.Hash.Bytes(), hash.Bytes()) > 0
}

// BalanceUpdates merges the address's own transactions with the voting (sent, from the node's history) and the
// recorded balance changes (rewards and refunds the contract paid it, also in other people's transactions), newest
// first.
func (s *Service) BalanceUpdates(address, voting common.Address, sent []*BalanceRow, limit int, token string) (*BalanceUpdatesResult, error) {
	limit, err := limitOf(limit)
	if err != nil {
		return nil, err
	}
	recorded, err := s.store.BalanceRows(address, voting)
	if err != nil {
		return nil, err
	}
	byHash := make(map[common.Hash]*BalanceRow)
	var rows []*BalanceRow
	for _, row := range sent {
		byHash[row.Hash] = row
		rows = append(rows, row)
	}
	for _, row := range recorded {
		if own, ok := byHash[row.Hash]; ok {
			own.BalanceChange = row.BalanceChange
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rowBefore(rows[i], rows[j].Height, rows[j].Hash) })
	if token != "" {
		i := strings.Index(token, ":")
		if i < 0 {
			return nil, errors.New("invalid continuation token")
		}
		height, err := strconv.ParseUint(token[:i], 10, 64)
		if err != nil {
			return nil, errors.Wrap(err, "invalid continuation token")
		}
		hash := common.HexToHash(token[i+1:])
		start := sort.Search(len(rows), func(i int) bool { return !rowBefore(rows[i], height, hash) })
		rows = rows[start:]
	}
	res := &BalanceUpdatesResult{}
	if len(rows) > limit {
		t := rowToken(rows[limit])
		res.ContinuationToken = &t
		rows = rows[:limit]
	}
	for _, row := range rows {
		res.Result = append(res.Result, row.update())
	}
	return res, nil
}

// IsVoting tells whether the address holds an oracle voting in the state, or is a terminated one the index kept.
func (s *Service) IsVoting(appState *appstate.AppState, addr common.Address) bool {
	if isVoting(appState, addr) {
		return true
	}
	record, err := s.store.Record(addr)
	return err == nil && record != nil && record.Termination != nil
}

package oraclevotings

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"sync"

	"github.com/idena-network/idena-go/blockchain/fee"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/pkg/errors"
	dbm "github.com/tendermint/tm-db"
)

// Keys (local data in the chain database, not part of the state):
//
//	prefix + 'm'                                       -> meta
//	prefix + 'v' + voting                              -> Record
//	prefix + 'b' + address + voting + height + tx hash -> BalanceRow (the node's own addresses only)
var keyPrefix = []byte("ovot")

const (
	kindMeta    = 'm'
	kindVoting  = 'v'
	kindBalance = 'b'
)

// meta tells whether the index has every live voting. A node that did not add every block since the last scan (its
// first start with the index, or a fast sync over a range of blocks) scans the state again before answering.
type meta struct {
	LastHeight uint64 `json:"lastHeight"`
	NeedsScan  bool   `json:"needsScan"`
	// Gaps counts the times blocks were skipped: a scan that ran across a new gap leaves NeedsScan set.
	Gaps uint64 `json:"gaps"`
}

// Result is what the voting paid out at its finish or termination.
type Result struct {
	Fund         *big.Int `json:"fund,omitempty"`
	OracleReward *big.Int `json:"oracleReward,omitempty"`
	OwnerReward  *big.Int `json:"ownerReward,omitempty"`
}

// StoredValue is one key of a contract's storage.
type StoredValue struct {
	Key   []byte `json:"k"`
	Value []byte `json:"v"`
}

// Termination keeps what the voting looked like in the block that terminated it: the state drops the contract.
type Termination struct {
	Height  uint64         `json:"height"`
	Time    int64          `json:"time"`
	Result  *Result        `json:"result,omitempty"`
	Balance *big.Int       `json:"balance,omitempty"`
	Stake   *big.Int       `json:"stake,omitempty"`
	Storage []*StoredValue `json:"storage,omitempty"`
}

// Record is what the node knows about a voting besides its contract storage. A voting found by a state scan has no
// creation block: the node did not add the block that deployed it.
type Record struct {
	CreateHeight uint64 `json:"createHeight,omitempty"`
	CreateTime   int64  `json:"createTime,omitempty"`
	// StateHeight is the block of the last deploy, start, prolongation, finish or termination.
	StateHeight uint64       `json:"stateHeight,omitempty"`
	Finish      *Result      `json:"finish,omitempty"`
	Termination *Termination `json:"termination,omitempty"`
}

// BalanceRow is a transaction to a voting that changed the balance of one of the node's addresses, with what the
// API shows of it (the transaction's block may be gone from the node later).
type BalanceRow struct {
	Address common.Address  `json:"address"`
	Voting  common.Address  `json:"voting"`
	Hash    common.Hash     `json:"hash"`
	Height  uint64          `json:"height"`
	Time    int64           `json:"time"`
	Type    types.TxType    `json:"type"`
	From    common.Address  `json:"from"`
	To      *common.Address `json:"to,omitempty"`
	// Amount is what the sender paid to the contract (0 when the call failed); Fee includes the contract's gas.
	Amount  *big.Int `json:"amount,omitempty"`
	Tips    *big.Int `json:"tips,omitempty"`
	MaxFee  *big.Int `json:"maxFee,omitempty"`
	Fee     *big.Int `json:"fee,omitempty"`
	Success bool     `json:"success"`
	GasUsed uint64   `json:"gasUsed,omitempty"`
	GasCost *big.Int `json:"gasCost,omitempty"`
	Method  string   `json:"method,omitempty"`
	Error   string   `json:"error,omitempty"`
	// BalanceChange is what the contract sent to the address (rewards, refunds), not the tx amount or fee.
	BalanceChange *big.Int `json:"balanceChange,omitempty"`
}

// NewBalanceRow describes a contract transaction of the voting as the block applied it (feePerGas and networkSize
// at that block); the balance change is added by the caller.
func NewBalanceRow(address, voting common.Address, tx *types.Transaction, receipt *types.TxReceipt, feePerGas *big.Int,
	networkSize int) *BalanceRow {
	sender, _ := types.Sender(tx)
	row := &BalanceRow{
		Address: address,
		Voting:  voting,
		Hash:    tx.Hash(),
		Type:    tx.Type,
		From:    sender,
		To:      tx.To,
		Amount:  new(big.Int),
		Tips:    copyInt(tx.TipsOrZero()),
		MaxFee:  copyInt(tx.MaxFeeOrZero()),
		Fee:     fee.CalculateFee(networkSize, feePerGas, tx),
	}
	if receipt != nil {
		row.Success = receipt.Success
		row.GasUsed = receipt.GasUsed
		row.GasCost = copyInt(receipt.GasCost)
		row.Method = receipt.Method
		if receipt.Error != nil {
			row.Error = receipt.Error.Error()
		}
		if receipt.GasCost != nil {
			row.Fee.Add(row.Fee, receipt.GasCost)
		}
	}
	if receipt == nil || receipt.Success {
		row.Amount = copyInt(tx.AmountOrZero())
	}
	return row
}

// Store keeps the index in the node's chain database.
type Store struct {
	db dbm.DB
	// mu orders the read-modify-writes of the recorder (one per added block) and of a scan.
	mu sync.Mutex
}

func NewStore(db dbm.DB) *Store {
	return &Store{db: db}
}

func key(kind byte, parts ...[]byte) []byte {
	k := append(append([]byte{}, keyPrefix...), kind)
	for _, p := range parts {
		k = append(k, p...)
	}
	return k
}

func votingKey(voting common.Address) []byte {
	return key(kindVoting, voting[:])
}

func balanceKey(row *BalanceRow) []byte {
	return key(kindBalance, row.Address[:], row.Voting[:], binary.BigEndian.AppendUint64(nil, row.Height), row.Hash[:])
}

// prefixEnd is the first key after every key that starts with prefix.
func prefixEnd(prefix []byte) []byte {
	end := append([]byte{}, prefix...)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}

func (s *Store) readJSON(k []byte, v interface{}) (bool, error) {
	data, err := s.db.Get(k)
	if err != nil {
		return false, err
	}
	if data == nil {
		return false, nil
	}
	return true, json.Unmarshal(data, v)
}

func setJSON(batch dbm.Batch, k []byte, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return batch.Set(k, data)
}

// readMeta: without a meta (the index never ran on this database) a scan is needed.
func (s *Store) readMeta() (*meta, error) {
	m := &meta{}
	found, err := s.readJSON(key(kindMeta), m)
	if err != nil {
		return nil, err
	}
	if !found {
		m.NeedsScan = true
	}
	return m, nil
}

func (s *Store) Record(voting common.Address) (*Record, error) {
	r := &Record{}
	found, err := s.readJSON(votingKey(voting), r)
	if err != nil || !found {
		return nil, err
	}
	return r, nil
}

// Votings returns every indexed voting.
func (s *Store) Votings() (map[common.Address]*Record, error) {
	prefix := key(kindVoting)
	it, err := s.db.Iterator(prefix, prefixEnd(prefix))
	if err != nil {
		return nil, err
	}
	defer it.Close()
	res := make(map[common.Address]*Record)
	for ; it.Valid(); it.Next() {
		var addr common.Address
		addr.SetBytes(it.Key()[len(prefix):])
		r := &Record{}
		if err := json.Unmarshal(it.Value(), r); err != nil {
			return nil, errors.Wrapf(err, "voting %v", addr.Hex())
		}
		res[addr] = r
	}
	return res, it.Error()
}

// BalanceRows returns the rows of the address with the voting, newest first.
func (s *Store) BalanceRows(address, voting common.Address) ([]*BalanceRow, error) {
	prefix := key(kindBalance, address[:], voting[:])
	it, err := s.db.ReverseIterator(prefix, prefixEnd(prefix))
	if err != nil {
		return nil, err
	}
	defer it.Close()
	var res []*BalanceRow
	for ; it.Valid(); it.Next() {
		row := &BalanceRow{}
		if err := json.Unmarshal(it.Value(), row); err != nil {
			return nil, err
		}
		res = append(res, row)
	}
	return res, it.Error()
}

// blockChanges is what one added block changed in the index.
type blockChanges struct {
	height  uint64
	time    int64
	records map[common.Address]func(r *Record)
	rows    []*BalanceRow
}

// writeBlock applies the changes of an added block. A block that does not follow the last one added marks the
// index for a scan: the blocks in between were not added (fast sync).
func (s *Store) writeBlock(changes *blockChanges) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readMeta()
	if err != nil {
		return err
	}
	if m.LastHeight != 0 && changes.height > m.LastHeight+1 {
		m.NeedsScan = true
		m.Gaps++
	}
	m.LastHeight = changes.height

	batch := s.db.NewBatch()
	defer batch.Close()
	for voting, change := range changes.records {
		r, err := s.Record(voting)
		if err != nil {
			return err
		}
		if r == nil {
			r = &Record{}
		}
		change(r)
		if err := setJSON(batch, votingKey(voting), r); err != nil {
			return err
		}
	}
	for _, row := range changes.rows {
		row.Height, row.Time = changes.height, changes.time
		if err := setJSON(batch, balanceKey(row), row); err != nil {
			return err
		}
	}
	if err := setJSON(batch, key(kindMeta), m); err != nil {
		return err
	}
	return batch.Write()
}

// markForScan: the index may hold votings the state no longer has (reverted blocks).
func (s *Store) markForScan() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readMeta()
	if err != nil {
		return err
	}
	m.NeedsScan = true
	m.Gaps++
	batch := s.db.NewBatch()
	defer batch.Close()
	if err := setJSON(batch, key(kindMeta), m); err != nil {
		return err
	}
	return batch.Write()
}

// scanState says what a scan needs: whether one is due, and the gap count it started from.
func (s *Store) scanState() (needed bool, gaps uint64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readMeta()
	if err != nil {
		return false, 0, err
	}
	return m.NeedsScan, m.Gaps, nil
}

// writeScan stores what a scan of the state at height found: live adds the votings the index lacks (keeping what it
// knows of the others), and the votings the index had at that height but the state did not (dropped while blocks
// were skipped, with no termination recorded) are removed; blocks added since the scanned height keep their
// changes. The scan is complete unless blocks were skipped again while it ran.
func (s *Store) writeScan(height uint64, live map[common.Address]*Record, gaps uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readMeta()
	if err != nil {
		return err
	}
	known, err := s.Votings()
	if err != nil {
		return err
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	for addr, r := range live {
		if _, ok := known[addr]; ok {
			continue
		}
		if err := setJSON(batch, votingKey(addr), r); err != nil {
			return err
		}
	}
	for addr, r := range known {
		if _, ok := live[addr]; ok || r.Termination != nil || r.CreateHeight > height || r.StateHeight > height {
			continue
		}
		if err := batch.Delete(votingKey(addr)); err != nil {
			return err
		}
	}
	if m.Gaps == gaps {
		m.NeedsScan = false
	}
	if err := setJSON(batch, key(kindMeta), m); err != nil {
		return err
	}
	return batch.Write()
}

func storageValue(storage []*StoredValue, k []byte) []byte {
	for _, v := range storage {
		if bytes.Equal(v.Key, k) {
			return v.Value
		}
	}
	return nil
}

package identityhistory

import (
	"encoding/binary"
	"encoding/json"
	"math/big"
	"sync"

	"github.com/idena-network/idena-go/common"
	"github.com/pkg/errors"
	dbm "github.com/tendermint/tm-db"
)

// Keys (local data in the chain database, not part of the state):
//
//	prefix + 'k'                      -> meta
//	prefix + 'c' + epoch              -> Ceremony (the block that ended the epoch)
//	prefix + 'm' + address + epoch    -> Mining (the node's own addresses only)
//	prefix + 'j' + height + address   -> journalEntry (the last blocks' mining, to undo a chain reset)
var keyPrefix = []byte("ihst")

const (
	kindMeta     = 'k'
	kindCeremony = 'c'
	kindMining   = 'm'
	kindJournal  = 'j'
)

// meta: Passed tells that the full pass over the stored headers ran: every ceremony block of the stored headers
// up to the epoch block of that time is recorded (gaps left later by a fast sync are filled by a search).
type meta struct {
	Passed bool `json:"passed"`
}

// Ceremony is the block that ended an epoch: the one with the ValidationFinished flag.
type Ceremony struct {
	Height uint64 `json:"height"`
	Time   int64  `json:"time"`
}

// Mining is what the chain paid an address for blocks in one epoch: as the block's proposer (block reward, fees and
// tips) and as a member of the block's final committee. Amounts are the parts the address received (balance and
// stake) before a penalty took its share; PenaltyBurnt is what a penalty burnt of them.
type Mining struct {
	ProposedBlocks  uint32   `json:"proposedBlocks"`
	ProposerReward  *big.Int `json:"proposerReward"`
	CommitteeBlocks uint32   `json:"committeeBlocks"`
	CommitteeReward *big.Int `json:"committeeReward"`
	PenaltyBurnt    *big.Int `json:"penaltyBurnt"`
}

// journalEntry is one block's mining of one address, kept while the block can still be reverted.
type journalEntry struct {
	Epoch  uint16  `json:"epoch"`
	Mining *Mining `json:"mining"`
}

// Store keeps the ceremony index and the mining records in the node's chain database.
type Store struct {
	db dbm.DB
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

func epochBytes(epoch uint16) []byte {
	return binary.BigEndian.AppendUint16(nil, epoch)
}

func heightBytes(height uint64) []byte {
	return binary.BigEndian.AppendUint64(nil, height)
}

func ceremonyKey(epoch uint16) []byte {
	return key(kindCeremony, epochBytes(epoch))
}

func miningKey(address common.Address, epoch uint16) []byte {
	return key(kindMining, address[:], epochBytes(epoch))
}

func journalKey(height uint64, address common.Address) []byte {
	return key(kindJournal, heightBytes(height), address[:])
}

// kindEnd: the first key after every key of the kind.
func kindEnd(kind byte) []byte {
	return key(kind + 1)
}

func (s *Store) readJSON(k []byte, v interface{}) (bool, error) {
	data, err := s.db.Get(k)
	if err != nil || data == nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, errors.Wrapf(err, "read identity history key %x", k)
	}
	return true, nil
}

func setJSON(batch dbm.Batch, k []byte, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return batch.Set(k, data)
}

func (s *Store) readMeta() (meta, error) {
	var m meta
	_, err := s.readJSON(key(kindMeta), &m)
	return m, err
}

// Ceremony returns the block that ended the epoch, nil when the node does not know it (yet).
func (s *Store) Ceremony(epoch uint16) (*Ceremony, error) {
	c := new(Ceremony)
	found, err := s.readJSON(ceremonyKey(epoch), c)
	if !found {
		return nil, err
	}
	return c, nil
}

// Ceremonies returns every recorded ceremony by epoch.
func (s *Store) Ceremonies() (map[uint16]*Ceremony, error) {
	it, err := s.db.Iterator(key(kindCeremony), kindEnd(kindCeremony))
	if err != nil {
		return nil, err
	}
	defer it.Close()
	result := make(map[uint16]*Ceremony)
	for ; it.Valid(); it.Next() {
		k := it.Key()
		if len(k) != len(keyPrefix)+1+2 {
			continue
		}
		c := new(Ceremony)
		if err := json.Unmarshal(it.Value(), c); err != nil {
			return nil, errors.Wrap(err, "read ceremony")
		}
		result[binary.BigEndian.Uint16(k[len(keyPrefix)+1:])] = c
	}
	return result, it.Error()
}

// writeCeremony records the block that ended the epoch, as the node applied it.
func (s *Store) writeCeremony(epoch uint16, c *Ceremony) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch := s.db.NewBatch()
	defer batch.Close()
	if err := setJSON(batch, ceremonyKey(epoch), c); err != nil {
		return err
	}
	return batch.Write()
}

// writeFound records ceremonies the node found in its stored headers; passed ends the full pass.
func (s *Store) writeFound(found map[uint16]*Ceremony, passed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch := s.db.NewBatch()
	defer batch.Close()
	for epoch, c := range found {
		if err := setJSON(batch, ceremonyKey(epoch), c); err != nil {
			return err
		}
	}
	if passed {
		if err := setJSON(batch, key(kindMeta), meta{Passed: true}); err != nil {
			return err
		}
	}
	return batch.Write()
}

// markForPass asks for the full pass again: a search could not place every ceremony of a gap.
func (s *Store) markForPass() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readMeta()
	if err != nil {
		return err
	}
	m.Passed = false
	batch := s.db.NewBatch()
	defer batch.Close()
	if err := setJSON(batch, key(kindMeta), m); err != nil {
		return err
	}
	return batch.Write()
}

// Mining returns the address's mining record of the epoch, nil when nothing was recorded.
func (s *Store) Mining(address common.Address, epoch uint16) (*Mining, error) {
	m := new(Mining)
	found, err := s.readJSON(miningKey(address, epoch), m)
	if !found {
		return nil, err
	}
	return m, nil
}

// MiningEpochs returns the address's mining records by epoch.
func (s *Store) MiningEpochs(address common.Address) (map[uint16]*Mining, error) {
	start := key(kindMining, address[:])
	end := key(kindMining, address[:], []byte{0xff, 0xff, 0xff})
	it, err := s.db.Iterator(start, end)
	if err != nil {
		return nil, err
	}
	defer it.Close()
	result := make(map[uint16]*Mining)
	for ; it.Valid(); it.Next() {
		k := it.Key()
		if len(k) != len(start)+2 {
			continue
		}
		m := new(Mining)
		if err := json.Unmarshal(it.Value(), m); err != nil {
			return nil, errors.Wrap(err, "read mining record")
		}
		result[binary.BigEndian.Uint16(k[len(start):])] = m
	}
	return result, it.Error()
}

// writeBlockMining adds a block's mining to the addresses' records of the epoch, journals it while the block can be
// reverted (keepFrom: the oldest height a reset can go back to) and drops the older journal entries.
func (s *Store) writeBlockMining(height uint64, epoch uint16, mining map[common.Address]*Mining, keepFrom uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch := s.db.NewBatch()
	defer batch.Close()
	for addr, m := range mining {
		record, err := s.Mining(addr, epoch)
		if err != nil {
			return err
		}
		if record == nil {
			record = &Mining{}
		}
		record.add(m, 1)
		if err := setJSON(batch, miningKey(addr, epoch), record); err != nil {
			return err
		}
		if err := setJSON(batch, journalKey(height, addr), &journalEntry{Epoch: epoch, Mining: m}); err != nil {
			return err
		}
	}
	if err := s.deleteRange(batch, key(kindJournal), key(kindJournal, heightBytes(keepFrom))); err != nil {
		return err
	}
	return batch.Write()
}

// revertTo undoes what the blocks above height recorded: their mining and their ceremonies.
func (s *Store) revertTo(height uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch := s.db.NewBatch()
	defer batch.Close()

	type recordKey struct {
		addr  common.Address
		epoch uint16
	}
	records := make(map[recordKey]*Mining)
	it, err := s.db.Iterator(key(kindJournal, heightBytes(height+1)), kindEnd(kindJournal))
	if err != nil {
		return err
	}
	defer it.Close()
	for ; it.Valid(); it.Next() {
		k := append([]byte{}, it.Key()...)
		entry := new(journalEntry)
		if err := json.Unmarshal(it.Value(), entry); err != nil {
			return errors.Wrap(err, "read mining journal")
		}
		var addr common.Address
		copy(addr[:], k[len(k)-common.AddressLength:])
		rk := recordKey{addr, entry.Epoch}
		record, ok := records[rk]
		if !ok {
			if record, err = s.Mining(addr, entry.Epoch); err != nil {
				return err
			}
			if record == nil {
				record = &Mining{}
			}
			records[rk] = record
		}
		record.add(entry.Mining, -1)
		if err := batch.Delete(k); err != nil {
			return err
		}
	}
	if err := it.Error(); err != nil {
		return err
	}
	for rk, record := range records {
		if record.empty() {
			err = batch.Delete(miningKey(rk.addr, rk.epoch))
		} else {
			err = setJSON(batch, miningKey(rk.addr, rk.epoch), record)
		}
		if err != nil {
			return err
		}
	}

	ceremonies, err := s.Ceremonies()
	if err != nil {
		return err
	}
	for epoch, c := range ceremonies {
		if c.Height > height {
			if err := batch.Delete(ceremonyKey(epoch)); err != nil {
				return err
			}
		}
	}
	return batch.Write()
}

func (s *Store) deleteRange(batch dbm.Batch, start, end []byte) error {
	it, err := s.db.Iterator(start, end)
	if err != nil {
		return err
	}
	defer it.Close()
	for ; it.Valid(); it.Next() {
		if err := batch.Delete(append([]byte{}, it.Key()...)); err != nil {
			return err
		}
	}
	return it.Error()
}

// add adds (sign 1) or removes (sign -1) one block's mining.
func (m *Mining) add(other *Mining, sign int) {
	if sign >= 0 {
		m.ProposedBlocks += other.ProposedBlocks
		m.CommitteeBlocks += other.CommitteeBlocks
	} else {
		m.ProposedBlocks -= min(m.ProposedBlocks, other.ProposedBlocks)
		m.CommitteeBlocks -= min(m.CommitteeBlocks, other.CommitteeBlocks)
	}
	m.ProposerReward = addInt(m.ProposerReward, other.ProposerReward, sign)
	m.CommitteeReward = addInt(m.CommitteeReward, other.CommitteeReward, sign)
	m.PenaltyBurnt = addInt(m.PenaltyBurnt, other.PenaltyBurnt, sign)
}

func (m *Mining) empty() bool {
	return m.ProposedBlocks == 0 && m.CommitteeBlocks == 0 && common.ZeroOrNil(m.ProposerReward) &&
		common.ZeroOrNil(m.CommitteeReward) && common.ZeroOrNil(m.PenaltyBurnt)
}

func addInt(sum, value *big.Int, sign int) *big.Int {
	if value == nil {
		return sum
	}
	if sum == nil {
		sum = new(big.Int)
	}
	if sign >= 0 {
		return sum.Add(sum, value)
	}
	return sum.Sub(sum, value)
}

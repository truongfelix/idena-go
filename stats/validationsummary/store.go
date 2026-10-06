package validationsummary

import (
	"encoding/binary"
	"encoding/json"

	"github.com/idena-network/idena-go/common"
	"github.com/pkg/errors"
	dbm "github.com/tendermint/tm-db"
)

// KeptEpochs: summaries of older epochs are deleted when a new one is recorded.
const KeptEpochs = 10

// Keys: prefix + epoch (big endian) -> epoch record; prefix + epoch + address -> summary; ownPrefix + address +
// epoch -> the summary of one of the node's own addresses, kept for good.
var (
	keyPrefix = []byte("vsum")
	ownPrefix = []byte("vsmo")
)

type epochRecord struct {
	ValidationFailed bool `json:"validationFailed"`
}

// Store keeps the summaries in the node's chain database (local data, not part of the state).
type Store struct {
	db dbm.DB
	// own lists the node's addresses (coinbase and keystore accounts): their summaries are kept for good.
	own func() []common.Address
}

// SetOwn names the node's addresses: from now on their summaries are kept for good, and those of the kept epochs
// are copied now.
func (s *Store) SetOwn(own func() []common.Address) error {
	s.own = own
	it, err := s.db.Iterator(epochKey(0), rangeEnd(^uint16(0)))
	if err != nil {
		return err
	}
	var epochs []uint16
	for ; it.Valid(); it.Next() {
		if k := it.Key(); len(k) == len(keyPrefix)+2 {
			epochs = append(epochs, binary.BigEndian.Uint16(k[len(keyPrefix):]))
		}
	}
	if err := it.Error(); err != nil {
		it.Close()
		return err
	}
	it.Close()
	batch := s.db.NewBatch()
	defer batch.Close()
	for _, epoch := range epochs {
		if err := s.copyOwn(batch, epoch, nil); err != nil {
			return err
		}
	}
	return batch.Write()
}

func ownKey(address common.Address, epoch uint16) []byte {
	key := append(append([]byte{}, ownPrefix...), address[:]...)
	return binary.BigEndian.AppendUint16(key, epoch)
}

// copyOwn writes the own addresses' summaries of the epoch (written: the epoch's new summaries by address; nil:
// read them from the store), the identities with no summary included, as Get shows them.
func (s *Store) copyOwn(batch dbm.Batch, epoch uint16, written map[common.Address]*Summary) error {
	if s.own == nil {
		return nil
	}
	for _, addr := range s.own() {
		summary, ok := written[addr]
		if !ok {
			var err error
			if summary, err = s.get(epoch, addr); err != nil {
				return err
			}
		}
		if summary == nil {
			continue
		}
		data, err := json.Marshal(summary)
		if err != nil {
			return err
		}
		if err := batch.Set(ownKey(addr, epoch), data); err != nil {
			return err
		}
	}
	return nil
}

func NewStore(db dbm.DB) *Store {
	return &Store{db: db}
}

func epochKey(epoch uint16) []byte {
	key := make([]byte, 0, len(keyPrefix)+2+common.AddressLength)
	key = append(key, keyPrefix...)
	return binary.BigEndian.AppendUint16(key, epoch)
}

func summaryKey(epoch uint16, address common.Address) []byte {
	return append(epochKey(epoch), address[:]...)
}

// rangeEnd: the first key after every key of the epoch.
func rangeEnd(epoch uint16) []byte {
	if epoch == ^uint16(0) {
		return append(append([]byte{}, keyPrefix[:len(keyPrefix)-1]...), keyPrefix[len(keyPrefix)-1]+1)
	}
	return epochKey(epoch + 1)
}

// Write replaces what was recorded for the epoch (a reset can apply its ceremony block again) and deletes the
// epochs older than KeptEpochs.
func (s *Store) Write(epoch uint16, validationFailed bool, summaries []*Summary) error {
	batch := s.db.NewBatch()
	defer batch.Close()

	if err := s.deleteRange(batch, epochKey(epoch), rangeEnd(epoch)); err != nil {
		return err
	}
	if epoch >= KeptEpochs {
		if err := s.deleteRange(batch, epochKey(0), epochKey(epoch-KeptEpochs+1)); err != nil {
			return err
		}
	}
	data, err := json.Marshal(epochRecord{ValidationFailed: validationFailed})
	if err != nil {
		return err
	}
	if err := batch.Set(epochKey(epoch), data); err != nil {
		return err
	}
	written := make(map[common.Address]*Summary, len(summaries))
	for _, summary := range summaries {
		data, err := json.Marshal(summary)
		if err != nil {
			return err
		}
		if err := batch.Set(summaryKey(epoch, summary.Address), data); err != nil {
			return err
		}
		written[summary.Address] = summary
	}
	if s.own != nil {
		// An own address with no summary (no identity then) is kept as Get shows it.
		for _, addr := range s.own() {
			if _, ok := written[addr]; !ok {
				written[addr] = absentSummary(epoch, addr, validationFailed)
			}
		}
		if err := s.copyOwn(batch, epoch, written); err != nil {
			return err
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
		key := append([]byte{}, it.Key()...)
		if err := batch.Delete(key); err != nil {
			return err
		}
	}
	return it.Error()
}

// Get returns the summary of the address in the epoch's ceremony: nil when this node did not record that epoch, or
// no longer keeps it (an own address's summaries are kept for good); Participated false and states Undefined when
// the address had no identity then.
func (s *Store) Get(epoch uint16, address common.Address) (*Summary, error) {
	summary, err := s.get(epoch, address)
	if summary != nil || err != nil {
		return summary, err
	}
	data, err := s.db.Get(ownKey(address, epoch))
	if err != nil || data == nil {
		return nil, err
	}
	summary = new(Summary)
	if err := json.Unmarshal(data, summary); err != nil {
		return nil, errors.Wrap(err, "read validation summary")
	}
	return summary, nil
}

// get reads the summary from the kept epochs.
func (s *Store) get(epoch uint16, address common.Address) (*Summary, error) {
	data, err := s.db.Get(epochKey(epoch))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var record epochRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, errors.Wrap(err, "read validation summary epoch")
	}
	data, err = s.db.Get(summaryKey(epoch, address))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return absentSummary(epoch, address, record.ValidationFailed), nil
	}
	summary := new(Summary)
	if err := json.Unmarshal(data, summary); err != nil {
		return nil, errors.Wrap(err, "read validation summary")
	}
	return summary, nil
}

// absentSummary: the address had no identity in the ceremony (or the ceremony failed: states unknown).
func absentSummary(epoch uint16, address common.Address, validationFailed bool) *Summary {
	summary := &Summary{Epoch: epoch, Address: address, ValidationFailed: validationFailed}
	if !validationFailed {
		summary.PrevState, summary.State = stateName(0), stateName(0)
	}
	return summary
}

// ValidatedIn tells whether the address was a validated identity (Newbie, Verified, Human) during the epoch: its
// state after the ceremony that ended the epoch before. known is false when this node did not record that
// ceremony, or it failed (the identities kept their states, which the summaries do not hold).
func (s *Store) ValidatedIn(epoch uint16, address common.Address) (validated, known bool, err error) {
	if epoch == 0 {
		return false, false, nil
	}
	summary, err := s.Get(epoch-1, address)
	if err != nil || summary == nil || summary.ValidationFailed {
		return false, false, err
	}
	switch summary.State {
	case "Newbie", "Verified", "Human":
		return true, true, nil
	}
	return false, true, nil
}

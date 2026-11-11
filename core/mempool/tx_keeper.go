package mempool

import (
	"encoding/json"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/hexutil"
	"github.com/idena-network/idena-go/crypto"
	"github.com/idena-network/idena-go/log"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	Folder = "mempool-txs"
)

var txKeeperPersistInterval = time.Second * 20

type addCommand struct {
	tx *types.Transaction
}

type rmCommand struct {
	txs []common.Hash
}

type txKeeper struct {
	ch         chan interface{}
	txs        map[common.Hash]hexutil.Bytes
	hasChanges bool
	datadir    string
	mutex      sync.RWMutex
	persistMu  sync.Mutex
	stop       chan struct{}
	stopOnce   sync.Once
	loops      sync.WaitGroup
}

func NewTxKeeper(datadir string) *txKeeper {
	return &txKeeper{datadir: datadir, txs: make(map[common.Hash]hexutil.Bytes), ch: make(chan interface{}, 20000),
		stop: make(chan struct{})}
}

// Stop ends the keeper's loops and returns once they have: no save of txs.json is under way or will start.
// The transactions not saved yet are not saved.
func (k *txKeeper) Stop() {
	k.stopOnce.Do(func() { close(k.stop) })
	k.loops.Wait()
}

// wait pauses for d, or until the keeper stops; it returns false when it stopped.
func (k *txKeeper) wait(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-k.stop:
		return false
	case <-timer.C:
		return true
	}
}

func (k *txKeeper) persist() error {
	k.persistMu.Lock()
	defer k.persistMu.Unlock()

	file, err := k.openFile()
	if err != nil {
		return err
	}
	defer file.Close()

	list := k.snapshotForPersist()

	data, err := json.Marshal(list)
	if err != nil {
		k.markHasChanges()
		return err
	}
	if err := file.Truncate(0); err != nil {
		k.markHasChanges()
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		k.markHasChanges()
		return err
	}
	if _, err := file.Write(data); err != nil {
		k.markHasChanges()
		return err
	}
	return nil
}

func (k *txKeeper) snapshotForPersist() []hexutil.Bytes {
	k.mutex.Lock()
	defer k.mutex.Unlock()

	list := make([]hexutil.Bytes, 0, len(k.txs))
	for _, d := range k.txs {
		list = append(list, d)
	}
	k.hasChanges = false
	return list
}

func (k *txKeeper) markHasChanges() {
	k.mutex.Lock()
	k.hasChanges = true
	k.mutex.Unlock()
}

func (k *txKeeper) hasPendingChanges() bool {
	k.mutex.RLock()
	defer k.mutex.RUnlock()
	return k.hasChanges
}
func (k *txKeeper) Load() {
	file, err := k.openFile()
	if err != nil {
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return
	}
	list := []hexutil.Bytes{}

	if err := json.Unmarshal(data, &list); err != nil && len(data) > 0 {
		log.Warn("cannot parse txs.json", "err", err)
	}

	k.mutex.Lock()
	for _, hex := range list {
		k.txs[crypto.Hash(hex)] = hex
	}
	k.mutex.Unlock()

	k.loops.Add(2)
	go k.loop()
	go k.persistLoop()
}

func (k *txKeeper) openFile() (file *os.File, err error) {
	newpath := filepath.Join(k.datadir, Folder)
	if err := os.MkdirAll(newpath, 0700); err != nil {
		return nil, err
	}
	filePath := filepath.Join(newpath, "txs.json")
	f, err := os.OpenFile(filePath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (k *txKeeper) AddTx(tx *types.Transaction) {
	select {
	case k.ch <- &addCommand{tx: tx}:
	default:
	}
}

func (k *txKeeper) addTx(tx *types.Transaction) {
	k.mutex.RLock()
	if _, ok := k.txs[tx.Hash()]; ok {
		k.mutex.RUnlock()
		return
	}
	k.mutex.RUnlock()
	data, _ := tx.ToBytes()

	k.mutex.Lock()
	k.txs[tx.Hash()] = data
	k.hasChanges = true
	k.mutex.Unlock()
}

func (k *txKeeper) RemoveTxs(hashes []common.Hash) {
	select {
	case k.ch <- &rmCommand{txs: hashes}:
	default:
	}
}

func (k *txKeeper) removeTxs(hashes []common.Hash) {
	k.mutex.Lock()
	defer k.mutex.Unlock()
	for _, hash := range hashes {
		if _, ok := k.txs[hash]; ok {
			delete(k.txs, hash)
			k.hasChanges = true
		}
	}
}

func (k *txKeeper) List() []*types.Transaction {
	k.mutex.RLock()
	defer k.mutex.RUnlock()
	result := make([]*types.Transaction, 0, len(k.txs))
	for _, hex := range k.txs {
		tx := new(types.Transaction)
		if err := tx.FromBytes(hex); err == nil {
			result = append(result, tx)
		}
	}
	return result
}

func (k *txKeeper) Len() int {
	k.mutex.RLock()
	defer k.mutex.RUnlock()
	return len(k.txs)
}

func (k *txKeeper) Clear() {
	k.mutex.Lock()
	defer k.mutex.Unlock()
	k.txs = map[common.Hash]hexutil.Bytes{}
	k.hasChanges = true
}

func (k *txKeeper) loop() {
	defer k.loops.Done()
	for {
		var cmd interface{}
		select {
		case <-k.stop:
			return
		case cmd = <-k.ch:
		}

		if add, ok := cmd.(*addCommand); ok {
			k.addTx(add.tx)
			continue
		}
		if rm, ok := cmd.(*rmCommand); ok {
			k.removeTxs(rm.txs)
		}
	}
}

func (k *txKeeper) persistLoop() {
	defer k.loops.Done()
	for {
		if !k.wait(time.Millisecond * 100) {
			return
		}
		if k.hasPendingChanges() {
			err := k.persist()
			if err == nil && !k.wait(txKeeperPersistInterval) {
				return
			}
		}
	}
}

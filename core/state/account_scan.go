package state

import (
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/log"
)

const maxAccountScanWorkers = 16

// IterateOverMatchingAccounts calls callback for every account for which match returns true, in
// the order IterateOverAccounts visits accounts: accounts loaded in this state first, then the
// stored ones in key order. match must only depend on the account it is given: when the tree
// has no unsaved changes, the stored accounts are read on several read-only views of the tree in
// parallel and match runs on those goroutines. callback always runs on the calling goroutine,
// after the stored accounts have been read.
func (s *StateDB) IterateOverMatchingAccounts(match func(account Account) bool, callback func(addr common.Address, account Account)) {
	usedAccounts := make(map[common.Address]Account)
	s.lock.Lock()
	for addr, item := range s.stateAccounts {
		usedAccounts[addr] = item.data
	}
	s.lock.Unlock()

	for addr, item := range usedAccounts {
		if match(item) {
			callback(addr, item)
		}
	}

	scanStart := time.Now() // REPLAY TEST HARNESS ONLY
	matches, ok := s.scanStoredAccountsInParallel(usedAccounts, match)
	log.Info("HARNESS: stored account scan", "parallel", ok, "ms", time.Since(scanStart).Milliseconds(), "matches", len(matches))
	if ok {
		for _, m := range matches {
			callback(m.addr, m.account)
		}
		return
	}

	seqStart := time.Now() // REPLAY TEST HARNESS ONLY
	s.IterateAccounts(func(key []byte, value []byte) bool {
		if key == nil {
			return true
		}
		addr := StateDbKeys.AddressKeyToAddress(key)
		if _, ok := usedAccounts[addr]; ok {
			return false
		}
		var data Account
		if err := data.FromBytes(value); err != nil {
			return false
		}
		if match(data) {
			callback(addr, data)
		}
		return false
	})
	log.Info("HARNESS: stored account walk (sequential)", "ms", time.Since(seqStart).Milliseconds())
}

type accountMatch struct {
	addr    common.Address
	account Account
}

// scanStoredAccountsInParallel returns the stored accounts not in skip for which match returns
// true, in key order. Each worker walks one slice of the account key range on its own read-only
// view of the tree (its own node cache and lock), so the reads run in parallel. It gives up
// (ok == false) unless every view has exactly the working tree's root, i.e. the tree has no
// unsaved changes and the view shows the same content.
func (s *StateDB) scanStoredAccountsInParallel(skip map[common.Address]Account, match func(account Account) bool) (matches []accountMatch, ok bool) {
	tree, isMutable := s.tree.(*MutableTree)
	workers := runtime.GOMAXPROCS(0)
	if w, err := strconv.Atoi(os.Getenv("IDENA_HARNESS_SCAN_WORKERS")); err == nil { // REPLAY TEST HARNESS ONLY
		workers = w
	}
	if !isMutable || workers < 2 {
		return nil, false
	}
	if workers > maxAccountScanWorkers {
		workers = maxAccountScanWorkers
	}
	version := tree.Version()
	workingHash := tree.WorkingHash()

	bounds := make([][]byte, workers+1)
	for i := 0; i < workers; i++ {
		bounds[i] = StateDbKeys.AddressKey(common.Address{byte(i * 256 / workers)})
	}
	bounds[0] = StateDbKeys.AddressKey(common.MinAddr)
	bounds[workers] = StateDbKeys.AddressKey(common.MaxAddr)

	parts := make([][]accountMatch, workers)
	var failed int32
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			view := NewMutableTree(s.db)
			if _, err := view.LazyLoad(version); err != nil {
				atomic.StoreInt32(&failed, 1)
				return
			}
			immutable := view.GetImmutable()
			if immutable.Hash() != workingHash {
				atomic.StoreInt32(&failed, 1)
				return
			}
			immutable.IterateRange(bounds[i], bounds[i+1], true, func(key []byte, value []byte) bool {
				if key == nil {
					atomic.StoreInt32(&failed, 1)
					return true
				}
				addr := StateDbKeys.AddressKeyToAddress(key)
				if _, ok := skip[addr]; ok {
					return false
				}
				var data Account
				if err := data.FromBytes(value); err != nil {
					return false
				}
				if match(data) {
					parts[i] = append(parts[i], accountMatch{addr: addr, account: data})
				}
				return false
			})
		}(i)
	}
	wg.Wait()
	if atomic.LoadInt32(&failed) != 0 {
		return nil, false
	}
	for _, part := range parts {
		matches = append(matches, part...)
	}
	return matches, true
}

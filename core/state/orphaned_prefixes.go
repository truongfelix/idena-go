package state

import (
	"bytes"
	"encoding/binary"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/log"
	"github.com/pkg/errors"
	dbm "github.com/tendermint/tm-db"
)

// prefixLen is the length of a state or identity state prefix: its space byte and a height.
const prefixLen = 9

// storedDbPrefix returns the prefix stored under key if it is a prefix of the space (stateDbPrefixBytes or
// identityStateDbPrefixBytes), else nil.
func storedDbPrefix(db dbm.DB, key []byte, space []byte) ([]byte, error) {
	prefix, err := db.Get(key)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get value")
	}
	if len(prefix) != prefixLen || !bytes.HasPrefix(prefix, space) {
		return nil, nil
	}
	return prefix, nil
}

// DropOrphanedPrefixes removes the state and identity state data stored under prefixes that nothing refers to,
// and returns those prefixes. A fast sync abandoned by older versions left its copy of the identity state, and
// a node stopped while it loaded a snapshot or cleared the state that a snapshot replaced left that data. It
// must run before the node syncs: then the only prefixes in use are the current ones of the state and the
// identity state, and the identity state copy of a fast sync whose headers are kept. A space whose current
// prefix is not stored is left as it is.
func DropOrphanedPrefixes(db dbm.DB) ([][]byte, error) {
	var dropped [][]byte
	for _, space := range []struct {
		name                        string
		prefix, currentKey, copyKey []byte
	}{
		{"state", stateDbPrefixBytes, currentStateDbPrefixKey, nil},
		{"identity state", identityStateDbPrefixBytes, currentIdentityStateDbPrefixKey, preliminaryIdentityStateDbPrefixKey},
	} {
		current, err := storedDbPrefix(db, space.currentKey, space.prefix)
		if err != nil {
			return dropped, err
		}
		if current == nil {
			continue
		}
		inUse := [][]byte{current}
		if space.copyKey != nil {
			copyPrefix, err := storedDbPrefix(db, space.copyKey, space.prefix)
			if err != nil {
				return dropped, err
			}
			if copyPrefix != nil {
				inUse = append(inUse, copyPrefix)
			}
		}
		prefixes, err := storedPrefixes(db, space.prefix)
		if err != nil {
			return dropped, err
		}
		for _, prefix := range prefixes {
			if containsPrefix(inUse, prefix) {
				continue
			}
			// Before the clearing: hundreds of MB can take minutes, with nothing else in the log meanwhile.
			log.Info("Dropping orphaned state data", "space", space.name, "height", binary.LittleEndian.Uint64(prefix[1:]))
			if err := common.ClearDb(dbm.NewPrefixDB(db, prefix)); err != nil {
				return dropped, err
			}
			dropped = append(dropped, prefix)
		}
	}
	return dropped, nil
}

// storedPrefixes returns the prefixes that data of the space is stored under. It seeks from one prefix to the
// next, reading one key per prefix whatever the size of its data. Keys of the space shorter than a prefix (the
// keys that store the current prefixes) are no data.
func storedPrefixes(db dbm.DB, space []byte) ([][]byte, error) {
	var prefixes [][]byte
	end := nextKey(space)
	for start := space; start != nil; {
		it, err := db.Iterator(start, end)
		if err != nil {
			return nil, err
		}
		if !it.Valid() {
			it.Close()
			break
		}
		key := append([]byte{}, it.Key()...)
		it.Close()
		if len(key) < prefixLen {
			start = append(key, 0)
			continue
		}
		prefix := key[:prefixLen]
		prefixes = append(prefixes, prefix)
		start = nextKey(prefix)
	}
	return prefixes, nil
}

// nextKey returns the first key after all the keys that start with prefix, or nil if there is none.
func nextKey(prefix []byte) []byte {
	next := append([]byte{}, prefix...)
	for i := len(next) - 1; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			return next[:i+1]
		}
	}
	return nil
}

func containsPrefix(prefixes [][]byte, prefix []byte) bool {
	for _, p := range prefixes {
		if bytes.Equal(p, prefix) {
			return true
		}
	}
	return false
}

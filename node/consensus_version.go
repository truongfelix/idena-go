package node

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/database"
	"github.com/idena-network/idena-go/log"
	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/opt"
	tmdb "github.com/tendermint/tm-db"
)

// ApplyStoredConsensusVersion brings cfg.Consensus up to the consensus version stored in the chain database of
// cfg.DataDir. A node applies consensus upgrades to its configuration when the chain reaches them, so every start
// has to apply them again: a node running the default rules on an upgraded chain computes other state roots for
// the blocks that depend on the upgrades. A new database stores no version and leaves the configuration as it is.
//
// The database is opened read-only: nothing is written, and the journal left by a stop without a clean close stays
// for the node's own open to convert, with the node's write buffer. With two journals or more it is opened as the
// node opens it.
func ApplyStoredConsensusVersion(cfg *config.Config) error {
	var db tmdb.DB
	var err error
	if journalCount(filepath.Join(cfg.DataDir, "idenachain.db")) > 1 {
		// goleveldb's read-only open fails with EOF on two journals or more: it returns the end of one as an error
		// when it goes on to the next (its read-write open ignores it), and leaves the next one open, which a
		// read-write open on Windows then cannot remove. A stop while a full memtable is written to a table leaves
		// two. The node's own open converts them.
		log.Info("Chain database with several journals: opening it read-write")
		db, err = openChainDatabase(cfg, false)
	} else {
		db, err = openDatabaseReadOnly(cfg.DataDir, "idenachain")
		if os.IsNotExist(err) {
			return nil // no chain database yet
		}
	}
	if err != nil {
		log.Error("Cannot transform consensus config", "err", err)
		return fmt.Errorf("open chain database: %w", err)
	}
	defer db.Close()
	consVersion, err := database.NewRepo(db).ReadConsensusVersionWithError()
	if err != nil {
		return fmt.Errorf("read consensus version: %w", err)
	}
	if consVersion <= uint32(cfg.Consensus.Version) {
		return nil
	}
	// The default configuration points to the package's default consensus config: upgrade a copy.
	consensus := *cfg.Consensus
	for v := consensus.Version + 1; v <= config.ConsensusVerson(consVersion); v++ {
		config.ApplyConsensusVersion(v, &consensus)
	}
	cfg.Consensus = &consensus
	log.Info("Consensus config transformed to", "ver", consVersion)
	return nil
}

// journalCount counts the journals of the goleveldb database in dir (NNNNNN.log files).
func journalCount(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
			n++
		}
	}
	return n
}

// openDatabaseReadOnly opens a chain database without writing to it; the journal is replayed in memory only. A
// database that does not exist yet is an os.IsNotExist error (a read-only open does not create it).
func openDatabaseReadOnly(datadir string, name string) (tmdb.DB, error) {
	return tmdb.NewGoLevelDBWithOpts(name, datadir, &opt.Options{
		OpenFilesCacheCapacity: 16,
		BlockCacheCapacity:     8 * opt.MiB,
		Filter:                 filter.NewBloomFilter(10),
		ReadOnly:               true,
	})
}

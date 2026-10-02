package node

import (
	"fmt"
	"os"

	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/database"
	"github.com/idena-network/idena-go/log"
	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/tendermint/tm-db"
)

// ApplyStoredConsensusVersion brings cfg.Consensus up to the consensus version stored in the chain database of
// cfg.DataDir. A node applies consensus upgrades to its configuration when the chain reaches them, so every start
// has to apply them again: a node running the default rules on an upgraded chain computes other state roots for
// the blocks that depend on the upgrades. A new database stores no version and leaves the configuration as it is.
//
// The database is opened read-only: nothing is written, and the journal left by a stop without a clean close stays
// for the node's own open to convert, with the node's write buffer.
func ApplyStoredConsensusVersion(cfg *config.Config) error {
	db, err := openDatabaseReadOnly(cfg.DataDir, "idenachain")
	if os.IsNotExist(err) {
		return nil // no chain database yet
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

// openDatabaseReadOnly opens a chain database without writing to it; the journal is replayed in memory only. A
// database that does not exist yet is an os.IsNotExist error (a read-only open does not create it).
func openDatabaseReadOnly(datadir string, name string) (db.DB, error) {
	return db.NewGoLevelDBWithOpts(name, datadir, &opt.Options{
		OpenFilesCacheCapacity: 16,
		BlockCacheCapacity:     8 * opt.MiB,
		Filter:                 filter.NewBloomFilter(10),
		ReadOnly:               true,
	})
}

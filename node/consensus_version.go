package node

import (
	"fmt"

	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/database"
	"github.com/idena-network/idena-go/log"
)

// ApplyStoredConsensusVersion brings cfg.Consensus up to the consensus version stored in the chain database of
// cfg.DataDir. A node applies consensus upgrades to its configuration when the chain reaches them, so every start
// has to apply them again: a node running the default rules on an upgraded chain computes other state roots for
// the blocks that depend on the upgrades. A new database stores no version and leaves the configuration as it is.
func ApplyStoredConsensusVersion(cfg *config.Config) error {
	db, err := OpenDatabase(cfg.DataDir, "idenachain", 16, 16, false)
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

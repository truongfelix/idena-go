package node

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/database"
)

func writeConsensusVersion(t *testing.T, datadir string, version uint32) {
	t.Helper()
	db, err := OpenDatabase(datadir, "idenachain", 16, 16, false)
	if err != nil {
		t.Fatalf("OpenDatabase() error = %v", err)
	}
	database.NewRepo(db).WriteConsensusVersion(nil, version)
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func defaultConsensusCopy() *config.ConsensusConf {
	consensus := *config.GetDefaultConsensusConfig()
	return &consensus
}

func TestApplyStoredConsensusVersionAppliesTheUpgrades(t *testing.T) {
	datadir := t.TempDir()
	writeConsensusVersion(t, datadir, uint32(config.ConsensusV12))
	cfg := &config.Config{DataDir: datadir, Consensus: defaultConsensusCopy()}

	if err := ApplyStoredConsensusVersion(cfg); err != nil {
		t.Fatalf("ApplyStoredConsensusVersion() error = %v", err)
	}

	if cfg.Consensus.Version != config.ConsensusV12 {
		t.Fatalf("consensus version = %d, want %d", cfg.Consensus.Version, config.ConsensusV12)
	}
	if !cfg.Consensus.EnableUpgrade10 || !cfg.Consensus.EnableUpgrade11 || !cfg.Consensus.EnableUpgrade12 {
		t.Fatalf("upgrades 10-12 enabled = %v %v %v, want all", cfg.Consensus.EnableUpgrade10,
			cfg.Consensus.EnableUpgrade11, cfg.Consensus.EnableUpgrade12)
	}
}

func TestApplyStoredConsensusVersionKeepsTheDefaultConfig(t *testing.T) {
	datadir := t.TempDir()
	writeConsensusVersion(t, datadir, uint32(config.ConsensusV12))
	cfg := &config.Config{DataDir: datadir, Consensus: config.GetDefaultConsensusConfig()}

	if err := ApplyStoredConsensusVersion(cfg); err != nil {
		t.Fatalf("ApplyStoredConsensusVersion() error = %v", err)
	}

	if def := config.GetDefaultConsensusConfig(); def.Version != config.ConsensusV9 || def.EnableUpgrade10 {
		t.Fatalf("default consensus config changed to version %d", def.Version)
	}
}

func TestApplyStoredConsensusVersionOnANewDatabase(t *testing.T) {
	cfg := &config.Config{DataDir: t.TempDir(), Consensus: defaultConsensusCopy()}

	if err := ApplyStoredConsensusVersion(cfg); err != nil {
		t.Fatalf("ApplyStoredConsensusVersion() error = %v", err)
	}

	if cfg.Consensus.Version != config.ConsensusV9 || cfg.Consensus.EnableUpgrade10 {
		t.Fatalf("consensus version = %d, want the default %d", cfg.Consensus.Version, config.ConsensusV9)
	}
}

func TestApplyStoredConsensusVersionDoesNotDowngrade(t *testing.T) {
	datadir := t.TempDir()
	writeConsensusVersion(t, datadir, uint32(config.ConsensusV10))
	consensus := defaultConsensusCopy()
	for v := config.ConsensusV10; v <= config.ConsensusV12; v++ {
		config.ApplyConsensusVersion(v, consensus)
	}
	cfg := &config.Config{DataDir: datadir, Consensus: consensus}

	if err := ApplyStoredConsensusVersion(cfg); err != nil {
		t.Fatalf("ApplyStoredConsensusVersion() error = %v", err)
	}

	if cfg.Consensus.Version != config.ConsensusV12 || !cfg.Consensus.EnableUpgrade12 {
		t.Fatalf("consensus version = %d, want %d kept", cfg.Consensus.Version, config.ConsensusV12)
	}
}

func TestMakeMobileConfigAppliesTheStoredConsensusVersion(t *testing.T) {
	path := t.TempDir()
	writeConsensusVersion(t, filepath.Join(path, config.DefaultDataDir), uint32(config.ConsensusV12))

	cfg, err := makeMobileConfig(path, `{"IpfsConf":{"Profile":""}}`)
	if err != nil {
		t.Fatalf("makeMobileConfig() error = %v", err)
	}

	if cfg.Consensus.Version != config.ConsensusV12 || !cfg.Consensus.EnableUpgrade10 {
		t.Fatalf("mobile node consensus version = %d, want %d", cfg.Consensus.Version, config.ConsensusV12)
	}
}

// databaseFiles lists the files of the chain database with their sizes, without goleveldb's text log.
func databaseFiles(t *testing.T, datadir string) map[string]int64 {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(datadir, "idenachain.db"))
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	files := map[string]int64{}
	for _, e := range entries {
		if e.Name() == "LOG" || e.Name() == "LOG.old" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			t.Fatalf("Info() error = %v", err)
		}
		files[e.Name()] = info.Size()
	}
	return files
}

func TestApplyStoredConsensusVersionWritesNothing(t *testing.T) {
	// A version left in the journal by a stop without a clean close is read, and no file of the database changes:
	// the node's own open converts the journal, with its own write buffer.
	datadir := t.TempDir()
	writeConsensusVersion(t, datadir, uint32(config.ConsensusV12))
	before := databaseFiles(t, datadir)
	cfg := &config.Config{DataDir: datadir, Consensus: defaultConsensusCopy()}

	if err := ApplyStoredConsensusVersion(cfg); err != nil {
		t.Fatalf("ApplyStoredConsensusVersion() error = %v", err)
	}

	if cfg.Consensus.Version != config.ConsensusV12 {
		t.Fatalf("consensus version = %d, want %d", cfg.Consensus.Version, config.ConsensusV12)
	}
	if after := databaseFiles(t, datadir); !reflect.DeepEqual(before, after) {
		t.Fatalf("database files changed:\nbefore %v\nafter  %v", before, after)
	}
}

func TestApplyStoredConsensusVersionCreatesNothingOnANewDataDir(t *testing.T) {
	datadir := t.TempDir()
	cfg := &config.Config{DataDir: datadir, Consensus: defaultConsensusCopy()}

	if err := ApplyStoredConsensusVersion(cfg); err != nil {
		t.Fatalf("ApplyStoredConsensusVersion() error = %v", err)
	}

	if entries, _ := os.ReadDir(datadir); len(entries) != 0 {
		t.Fatalf("new data directory: %d entries created", len(entries))
	}
}

func TestApplyStoredConsensusVersionOnAnEmptyDatabaseDirectory(t *testing.T) {
	// A database directory without a database in it (e.g. a first start interrupted early) has no stored version.
	datadir := t.TempDir()
	if err := os.Mkdir(filepath.Join(datadir, "idenachain.db"), 0755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	cfg := &config.Config{DataDir: datadir, Consensus: defaultConsensusCopy()}

	if err := ApplyStoredConsensusVersion(cfg); err != nil {
		t.Fatalf("ApplyStoredConsensusVersion() error = %v", err)
	}

	if cfg.Consensus.Version != config.ConsensusV9 {
		t.Fatalf("consensus version = %d, want the default %d", cfg.Consensus.Version, config.ConsensusV9)
	}
}

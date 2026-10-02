package node

import (
	"path/filepath"
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

package node

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/database"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"
	dbm "github.com/tendermint/tm-db"
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

// noTableStorage creates no table: a full memtable is never written to one, and its journal stays.
type noTableStorage struct {
	storage.Storage
}

func (s noTableStorage) Create(fd storage.FileDesc) (storage.Writer, error) {
	if fd.Type == storage.TypeTable {
		return nil, errors.New("no table in this test")
	}
	return s.Storage.Create(fd)
}

func journals(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".log") {
			n++
		}
	}
	return n
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer src.Close()
	dst, err := os.Create(to)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
}

// writeConsensusVersionInTwoJournals leaves the chain database of datadir as a node stopped while it wrote a full
// memtable to a table: the memtable's journal, with the version first, and the next journal, with the version
// second.
func writeConsensusVersionInTwoJournals(t *testing.T, datadir string, first, second uint32) {
	t.Helper()
	dir := t.TempDir()
	stor, err := storage.OpenFile(dir, false)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	ldb, err := leveldb.Open(noTableStorage{stor}, &opt.Options{WriteBuffer: 64 * opt.KiB})
	if err != nil {
		t.Fatalf("leveldb.Open() error = %v", err)
	}
	writeVersion := func(v uint32) {
		mem := dbm.NewMemDB()
		database.NewRepo(mem).WriteConsensusVersion(nil, v)
		it, err := mem.Iterator(nil, nil)
		if err != nil {
			t.Fatalf("Iterator() error = %v", err)
		}
		defer it.Close()
		for ; it.Valid(); it.Next() {
			if err := ldb.Put(it.Key(), it.Value(), nil); err != nil {
				t.Fatalf("Put() error = %v", err)
			}
		}
	}
	writeVersion(first)
	for i := 0; journals(t, dir) < 2; i++ {
		if i == 1000 {
			t.Fatalf("the memtable was not rotated")
		}
		if err := ldb.Put([]byte(fmt.Sprintf("filler-%d", i)), make([]byte, 1024), nil); err != nil {
			t.Fatalf("Put() error = %v", err)
		}
	}
	writeVersion(second)

	// The files as the stop leaves them.
	target := filepath.Join(datadir, "idenachain.db")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, e := range entries {
		if e.Name() != "LOCK" {
			copyFile(t, filepath.Join(dir, e.Name()), filepath.Join(target, e.Name()))
		}
	}
	_ = ldb.Close() // it reports the table it could not write
	if err := stor.Close(); err != nil {
		t.Fatalf("storage Close() error = %v", err)
	}
}

func TestApplyStoredConsensusVersionAfterAStopWithTwoJournals(t *testing.T) {
	datadir := t.TempDir()
	writeConsensusVersionInTwoJournals(t, datadir, uint32(config.ConsensusV11), uint32(config.ConsensusV12))
	if n := journals(t, filepath.Join(datadir, "idenachain.db")); n != 2 {
		t.Fatalf("journals = %d, want 2", n)
	}
	// The state is the one that goleveldb's read-only open cannot read. Checked on a copy: the failed open leaves a
	// journal open, which Windows cannot remove.
	control, err := os.MkdirTemp("", "two-journals-control")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(control) })
	if err := os.Mkdir(filepath.Join(control, "idenachain.db"), 0755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(datadir, "idenachain.db"))
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, e := range entries {
		copyFile(t, filepath.Join(datadir, "idenachain.db", e.Name()), filepath.Join(control, "idenachain.db", e.Name()))
	}
	if ro, err := openDatabaseReadOnly(control, "idenachain"); err == nil {
		ro.Close()
		t.Fatalf("read-only open of two journals succeeded: the test does not build the failing state")
	}
	cfg := &config.Config{DataDir: datadir, Consensus: defaultConsensusCopy()}

	if err := ApplyStoredConsensusVersion(cfg); err != nil {
		t.Fatalf("ApplyStoredConsensusVersion() error = %v", err)
	}

	if cfg.Consensus.Version != config.ConsensusV12 {
		t.Fatalf("consensus version = %d, want the one of the last journal %d", cfg.Consensus.Version, config.ConsensusV12)
	}
	// The node's own open follows.
	db, err := openChainDatabase(cfg, false)
	if err != nil {
		t.Fatalf("openChainDatabase() error = %v", err)
	}
	defer db.Close()
	if v, err := database.NewRepo(db).ReadConsensusVersionWithError(); err != nil || v != uint32(config.ConsensusV12) {
		t.Fatalf("stored consensus version = %d, %v, want %d", v, err, config.ConsensusV12)
	}
}

package state

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/state/snapshot"
	"github.com/idena-network/idena-go/database"
	"github.com/idena-network/idena-go/events"
	"github.com/idena-network/idena-go/ipfs"
	"github.com/idena-network/idena-go/log"
	"github.com/ipfs/go-cid"
	dbm "github.com/tendermint/tm-db"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	SnapshotsFolder = "/snapshots"
)

type SnapshotVersion byte

const SnapshotVersionV2 = SnapshotVersion(2)

var (
	InvalidManifestPrefix = []byte("im")
	MaxManifestTimeouts   = byte(5)
	// MaxSnapshotDownloadTime ends a snapshot download that still receives data: the idle timeout alone lets a
	// download that trickles hold the sync for as long as it trickles.
	MaxSnapshotDownloadTime = 20 * time.Minute
	// MaxSnapshotSize ends a snapshot download that goes past it. A mainnet snapshot was 13 MB in October 2026: the
	// bound leaves the chain's state room to grow many times over and keeps a download from filling the disk.
	MaxSnapshotSize int64 = 1 << 30

	ErrSnapshotTooLarge = errors.New("snapshot is larger than the size limit")
)

// downloadSuffix names a snapshot file being downloaded, until PromoteSnapshot gives it the name of a kept one.
const downloadSuffix = ".download"

type SnapshotManager struct {
	db        dbm.DB
	state     *StateDB
	ipfs      ipfs.Proxy
	bus       eventbus.Bus
	isSyncing bool
	cfg       *config.Config
	log       log.Logger
	repo      *database.Repo
}

func NewSnapshotManager(db dbm.DB, state *StateDB, bus eventbus.Bus, ipfs ipfs.Proxy, cfg *config.Config) *SnapshotManager {
	pdb := dbm.NewPrefixDB(db, database.SnapshotDbPrefix)
	m := &SnapshotManager{
		db:    pdb,
		state: state,
		repo:  database.NewRepo(db),
		bus:   bus,
		cfg:   cfg,
		log:   log.New(),
		ipfs:  ipfs,
	}
	_ = bus.Subscribe(events.AddBlockEventID,
		func(e eventbus.Event) {
			newBlockEvent := e.(*events.NewBlockEvent)
			m.createSnapshotIfNeeded(newBlockEvent.Block.Header)
		})
	return m
}

func snapshotFilePath(datadir string, height uint64, version SnapshotVersion) string {
	return filepath.Join(datadir, SnapshotsFolder, strconv.FormatUint(height, 10)+"."+strconv.FormatInt(int64(version), 10)+".tar")
}

func createSnapshotFile(datadir string, height uint64, version SnapshotVersion) (fileName string, file *os.File, err error) {
	return createFileInSnapshotFolder(datadir, snapshotFilePath(datadir, height, version))
}

func createFileInSnapshotFolder(datadir string, filePath string) (fileName string, file *os.File, err error) {
	if err := os.MkdirAll(filepath.Join(datadir, SnapshotsFolder), 0700); err != nil {
		return "", nil, err
	}
	f, err := os.OpenFile(filePath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return "", nil, err
	}
	return filePath, f, nil
}

func (m *SnapshotManager) createSnapshotIfNeeded(block *types.Header) {
	if m.isSyncing {
		return
	}
	if m.state.LastSnapshot() == block.Height() {
		go m.createSnapshot(block.Height())
	}
}

func (m *SnapshotManager) createShapshotForVersion(height uint64, version SnapshotVersion) (cidBytes []byte, root common.Hash, filePath string) {
	filePath, file, err := createSnapshotFile(m.cfg.DataDir, height, version)
	if err != nil {
		m.log.Error("Cannot create file for snapshot", "err", err)
		return nil, common.Hash{}, ""
	}

	if root, err = m.state.WriteSnapshot2(height, file); err != nil {
		file.Close()
		m.log.Error("Cannot write snapshot to file", "err", err)
		return nil, common.Hash{}, ""
	}

	file.Close()
	var f *os.File
	var cid cid.Cid
	if f, err = os.Open(filePath); err != nil {
		m.log.Error("Cannot open snapshot file", "err", err)
		os.Remove(filePath)
		return nil, common.Hash{}, ""
	}
	stat, _ := f.Stat()
	if cid, err = m.ipfs.AddFile(f.Name(), f, stat); err != nil {
		m.log.Error("Cannot add snapshot file to ipfs", "err", err)
		f.Close()
		if err = os.Remove(filePath); err != nil {
			m.log.Error("Cannot remove file", "err", err)
		}
		return nil, common.Hash{}, ""
	}
	return cid.Bytes(), root, filePath
}

func (m *SnapshotManager) createSnapshot(height uint64) (root common.Hash) {

	cidV2, root, filePath := m.createShapshotForVersion(height, SnapshotVersionV2)
	if cidV2 != nil {
		m.clearFs([]string{filePath})
		m.writeLastManifest(cidV2, root, height, filePath)
	}
	return root
}

func (m *SnapshotManager) clearFs(excludedFiles []string) {
	m.clearFsFor(nil, excludedFiles)
}

// clearFsFor unpins the last snapshot unless it is the one with cid newCidV2, and removes every file of the snapshot
// folder but excludedFiles.
func (m *SnapshotManager) clearFsFor(newCidV2 []byte, excludedFiles []string) {
	if prevCidV2, _, _, _ := m.repo.LastSnapshotManifest(); prevCidV2 != nil && !bytes.Equal(prevCidV2, newCidV2) {
		m.ipfs.Unpin(prevCidV2)
	}
	m.clearSnapshotFolder(excludedFiles)
}

func (m *SnapshotManager) clearSnapshotFolder(excludedFiles []string) {
	directory := filepath.Join(m.cfg.DataDir, SnapshotsFolder)

	var files []string

	var contains = func(arr []string, value string) bool {
		for _, s := range arr {
			if s == value {
				return true
			}
		}
		return false
	}

	err := filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && !contains(excludedFiles, path) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		m.log.Error("Cannot walk through snapshot directory", "err", err)
	}
	for _, file := range files {
		if err := os.Remove(file); err != nil {
			if err != nil {
				m.log.Error("Cannot remove file from snapshot directory", "err", err)
			}
		}
	}
}

func (m *SnapshotManager) writeLastManifest(snapshotCidV2 []byte, root common.Hash, height uint64, fileV2 string) {
	m.repo.WriteLastSnapshotManifest(snapshotCidV2, root, height, fileV2)
}

// DownloadSnapshot downloads the snapshot of the manifest into a file of its own, and leaves the node's last snapshot
// as it is: the caller checks the file against the header's root, then keeps it with PromoteSnapshot or drops it with
// DiscardSnapshot. On an error the file is already removed. A download stops when nothing arrives for a minute, after
// MaxSnapshotDownloadTime, or past MaxSnapshotSize.
func (m *SnapshotManager) DownloadSnapshot(snapshot *snapshot.Manifest) (filePath string, version SnapshotVersion, err error) {
	version = SnapshotVersionV2
	filePath, file, err := createFileInSnapshotFolder(m.cfg.DataDir,
		snapshotFilePath(m.cfg.DataDir, snapshot.Height, version)+downloadSuffix)
	if err != nil {
		return "", 0, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	deadline := time.Now().Add(MaxSnapshotDownloadTime)
	lastLoad := time.Now()
	lock := sync.Mutex{}
	logLevels := []float32{0.15, 0.3, 0.5, 0.75}
	var tooLarge atomic.Bool
	onLoading := func(size, read int64) {
		if size > MaxSnapshotSize {
			tooLarge.Store(true)
			cancel()
			return
		}
		lock.Lock()
		lastLoad = time.Now()
		lock.Unlock()
		if size > 0 && len(logLevels) > 0 && float32(read)/float32(size) >= logLevels[0] {
			m.log.Info("Snapshot loading", "progress", fmt.Sprintf("%v%%", logLevels[0]*100))
			logLevels = logLevels[1:]
		}
	}
	var loadToErr error

	wg := sync.WaitGroup{}
	wg.Add(1)
	//done := false
	done := make(chan struct{})

	go func() {
		loadToErr = m.ipfs.LoadTo(snapshot.CidV2, &limitedWriter{w: file, left: MaxSnapshotSize}, ctx, onLoading)
		wg.Done()
		close(done)
	}()

	go func() {
		for {
			select {
			case <-done:
				return
			default:
				time.Sleep(1 * time.Second)
				lock.Lock()
				idleDuration := time.Now().Sub(lastLoad)
				lock.Unlock()
				if idleDuration > time.Minute || time.Now().After(deadline) {
					cancel()
				}

			}
		}
	}()

	wg.Wait()
	cancel()
	if err := file.Close(); err != nil && loadToErr == nil {
		loadToErr = err
	}
	if tooLarge.Load() || errors.Is(loadToErr, ErrSnapshotTooLarge) {
		loadToErr = ErrSnapshotTooLarge
	}
	if loadToErr != nil {
		m.DiscardSnapshot(filePath)
		return "", version, loadToErr
	}
	return filePath, version, nil
}

// PromoteSnapshot keeps a snapshot downloaded by DownloadSnapshot, once checked against its header's root, in place of
// the node's last snapshot: the node announces it from then on.
func (m *SnapshotManager) PromoteSnapshot(snapshot *snapshot.Manifest, downloadedFile string) error {
	filePath := strings.TrimSuffix(downloadedFile, downloadSuffix)
	if filePath == downloadedFile {
		return errors.New("not a downloaded snapshot file")
	}
	if err := os.Rename(downloadedFile, filePath); err != nil {
		return err
	}
	m.clearFsFor(snapshot.CidV2, []string{filePath})
	m.writeLastManifest(snapshot.CidV2, snapshot.Root, snapshot.Height, filePath)
	return nil
}

// DiscardSnapshot removes a downloaded snapshot file that is not kept.
func (m *SnapshotManager) DiscardSnapshot(downloadedFile string) {
	if err := os.Remove(downloadedFile); err != nil && !os.IsNotExist(err) {
		m.log.Error("Cannot remove downloaded snapshot", "err", err)
	}
}

// limitedWriter writes up to left bytes, then fails with ErrSnapshotTooLarge.
type limitedWriter struct {
	w    io.Writer
	left int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.left {
		return 0, ErrSnapshotTooLarge
	}
	n, err := l.w.Write(p)
	l.left -= int64(n)
	return n, err
}

func (m *SnapshotManager) StartSync() {
	m.isSyncing = true
}

func (m *SnapshotManager) StopSync() {
	m.isSyncing = false
}

func (m *SnapshotManager) IsInvalidManifest(cid []byte) bool {
	key := append(InvalidManifestPrefix, cid...)
	if has, err := m.db.Has(key); err != nil || !has {
		return false
	}
	v, err := m.db.Get(key)
	if err != nil {
		return false
	}
	return v[0] >= MaxManifestTimeouts
}

func (m *SnapshotManager) AddInvalidManifest(cid []byte) {
	key := append(InvalidManifestPrefix, cid...)
	m.db.Set(key, []byte{MaxManifestTimeouts})
}
func (m *SnapshotManager) AddTimeoutManifest(cid []byte) {
	key := append(InvalidManifestPrefix, cid...)
	value := []byte{0x1}
	if has, err := m.db.Has(key); err == nil && has {
		if value, err = m.db.Get(key); err == nil {
			value[0]++
		}
	}
	m.db.Set(key, value)
}

// ManifestTimeouts returns how many downloads of the snapshot have failed (MaxManifestTimeouts once it is invalid).
func (m *SnapshotManager) ManifestTimeouts(cid []byte) byte {
	key := append(append([]byte{}, InvalidManifestPrefix...), cid...)
	v, err := m.db.Get(key)
	if err != nil || len(v) == 0 {
		return 0
	}
	return v[0]
}

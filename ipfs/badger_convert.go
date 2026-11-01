//go:build !idena_memory_ipfs

package ipfs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dgraph-io/badger"
	"github.com/dgraph-io/badger/options"
	"github.com/idena-network/idena-go/log"
	ds "github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/mount"
	"github.com/ipfs/go-datastore/query"
	badgerds "github.com/ipfs/go-ds-badger"
	flatfs "github.com/ipfs/go-ds-flatfs"
	levelds "github.com/ipfs/go-ds-leveldb"
	ipfsConf "github.com/ipfs/kubo/config"
	"github.com/pkg/errors"
	ldbopts "github.com/syndtr/goleveldb/leveldb/opt"
)

// The official node's IPFS repository keeps everything in one Badger v1 store (datastore_spec
// {"path":"badgerds","type":"badgerds"}). Badger maps its table files into memory, and the DHT scans the provider
// records in it every hour: on a desktop that came from the official app, about 3 GB of mapped pages stay in the
// app's memory and grow until the system stops the app under memory pressure. convertBadgerRepo copies such a store
// once into the layout this node gives a new repository (the flatfs profile: blocks in flatfs, the rest in LevelDB),
// switches the repository to it, and deletes the Badger store at the next start.
//
// The conversion never stops the node: whatever fails before the switch leaves the repository on Badger (the partial
// copy removed), and after maxConversionAttempts attempts it is no longer tried (deleting the marker allows more).
// The marker file holds the step reached, so that a start interrupted at any point resumes:
//   - copying: a copy was being written. It goes on from its last checkpoint if Badger is unchanged since; it is
//     dropped and made again otherwise.
//   - copied: the copy is complete and on disk; it is checked (key count, Badger unchanged) and switched to.
//   - switched: the repository runs on the copy; the Badger store, kept for one start, is deleted.
//   - failed: the last attempt failed.

const (
	badgerConversionMarker = "badger-conversion.json"
	// badgerBackup: the suffix of the Badger config and spec kept until the old store is deleted.
	badgerBackup      = ".badger.bak"
	validationGuard   = 4 * time.Hour
	badgerDirName     = "badgerds"
	badgerOldDirName  = "badgerds.converted"
	flatfsDirName     = "blocks"
	leveldbDirName    = "datastore"
	datastoreSpecFile = "datastore_spec"
	// The log lines around the copy: the desktop's node watchdog (main/node-watch.js) does not restart a node
	// between them, as during the chain database's compaction. Kept as they are.
	conversionStartLine = "Start converting the IPFS repository"
	conversionEndLine   = "IPFS repository conversion ended"
	// badgerConversionMargin: free space kept on top of the copy's estimated size, and the least the copy leaves.
	badgerConversionMargin = 512 << 20
	badgerConversionBatch  = 1000
	// A flatfs block is a file: on disk it takes whole clusters, plus its directory entry.
	diskCluster           = 4096
	fileOverhead          = 1024
	maxConversionAttempts = 3
	stepCopying           = "copying"
	stepCopied            = "copied"
	stepSwitched          = "switched"
	stepFailed            = "failed"
	blocksPrefix          = "/blocks/"
	flatfsExtension       = ".data"
)

type badgerConversionState struct {
	Step string `json:"step"`
	Keys int64  `json:"keys,omitempty"`
	// Attempts: copies started on this Badger store; none after maxConversionAttempts.
	Attempts int `json:"attempts,omitempty"`
	// Badger: the Badger store's files when the copy was made (badgerFingerprint): a copy is switched to only if
	// Badger is unchanged since.
	Badger string `json:"badger,omitempty"`
	// Copying only, from the first checkpoint: the last key the copy holds on disk, and the space taken so far
	// (keyDiskSize). The copy goes on after LastKey if Badger is unchanged since (Badger: its files at the checkpoint).
	LastKey []byte `json:"lastKey,omitempty"`
	Copied  int64  `json:"copied,omitempty"`
}

// blockFlushBytes: the most block bytes copied between two checkpoints (their files then leave the page cache);
// checkpointEvery: the longest time between two. Variables for the tests, as are copyCheckpoints (the checkpoints
// made) and stopCopyAt (the copy stops after that many checkpoints of one start, as a quit would; 0 never).
var (
	blockFlushBytes = 256 << 20
	checkpointEvery = time.Minute
	copyCheckpoints int
	stopCopyAt      int
)

// errCopyStopped: the copy stopped at stopCopyAt.
var errCopyStopped = errors.New("the IPFS conversion was stopped")

// freeDiskSpace is the free space of the disk holding dir; a variable so that tests can set it.
var freeDiskSpace = diskFreeBytes

// isBadgerRepo tells whether the repository's datastore is the official node's single Badger store.
func isBadgerRepo(repoDir string) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(repoDir, datastoreSpecFile))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		return false, errors.Wrap(err, "cannot read the IPFS datastore spec")
	}
	return spec["type"] == "badgerds", nil
}

// readConversionState: nil when there is no marker; a marker that cannot be read is a state with no step (dropped on
// a repository that is not Badger, rewritten on one that is).
func readConversionState(repoDir string) *badgerConversionState {
	raw, err := os.ReadFile(filepath.Join(repoDir, badgerConversionMarker))
	if os.IsNotExist(err) {
		return nil
	}
	state := new(badgerConversionState)
	if err == nil {
		err = json.Unmarshal(raw, state)
	}
	if err != nil {
		log.Warn("Cannot read the IPFS conversion marker", "err", err)
		return &badgerConversionState{}
	}
	return state
}

func writeConversionState(repoDir string, state badgerConversionState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(repoDir, badgerConversionMarker), raw)
}

func removeConversionState(repoDir string) {
	if err := os.Remove(filepath.Join(repoDir, badgerConversionMarker)); err != nil && !os.IsNotExist(err) {
		log.Warn("Cannot remove the IPFS conversion marker", "err", err)
	}
}

// convertBadgerRepo converts a Badger repository, finishes a conversion an earlier start left, or does nothing. Its
// error means the repository cannot be read at all (the spec file); every conversion failure is logged and leaves
// the repository as it opened before.
func convertBadgerRepo(repoDir string, nextValidation time.Time) error {
	state := readConversionState(repoDir)
	badger, err := isBadgerRepo(repoDir)
	if err != nil {
		return err
	}
	if !badger {
		if state != nil && state.Step != stepSwitched && state.Step != stepCopied {
			log.Warn("Dropping an IPFS conversion marker of a repository that is not Badger", "step", state.Step)
		}
		if state != nil {
			// The repository runs on the copy (switched), or never was Badger: an old store goes, then the marker.
			if !removeOldBadger(repoDir) {
				return nil
			}
			removeBadgerBackups(repoDir)
			removeConversionState(repoDir)
		}
		return nil
	}

	fingerprint, err := badgerFingerprint(filepath.Join(repoDir, badgerDirName))
	if err != nil {
		log.Warn("Cannot read the IPFS Badger store; it is not converted", "err", err)
		return nil
	}
	// Attempts count whatever the store's files: the node runs on Badger between two starts, which rewrites them.
	attempts := 0
	if state != nil {
		attempts = state.Attempts
	}
	if state != nil && state.Step == stepFailed && attempts >= maxConversionAttempts {
		log.Warn("The IPFS repository stays on Badger: its conversion failed", "attempts", attempts)
		return nil
	}

	copied := state != nil && state.Step == stepCopied && state.Badger == fingerprint && copyComplete(repoDir, state.Keys)
	if !copied && conversionPostponed(nextValidation, time.Now()) {
		log.Info("The IPFS repository is converted from Badger at a later start: a validation is near",
			"nextValidation", nextValidation.UTC().Format(time.RFC3339))
		return nil
	}
	if !copied {
		// A copy a quit stopped goes on from its last checkpoint, as the same attempt.
		from := badgerConversionState{Step: stepCopying, Attempts: attempts + 1}
		if state != nil && state.Step == stepCopying && len(state.LastKey) > 0 && state.Badger == fingerprint {
			from = *state
		}
		keys, ok := copyBadgerStore(repoDir, from)
		if !ok {
			return nil
		}
		// The copy closed the Badger store: its files as the next start will find them.
		if fingerprint, err = badgerFingerprint(filepath.Join(repoDir, badgerDirName)); err != nil {
			abandonConversion(repoDir, from.Attempts, err)
			return nil
		}
		state = &badgerConversionState{Step: stepCopied, Keys: keys, Attempts: from.Attempts, Badger: fingerprint}
		if err := writeConversionState(repoDir, *state); err != nil {
			abandonConversion(repoDir, from.Attempts, err)
			return nil
		}
	}

	if err := switchToFlatfsSpec(repoDir); err != nil {
		abandonConversion(repoDir, state.Attempts, err)
		return nil
	}
	if err := writeConversionState(repoDir, badgerConversionState{Step: stepSwitched, Keys: state.Keys}); err != nil {
		log.Warn("Cannot write the IPFS conversion marker", "err", err)
	}
	// The Badger store is kept until the next start, by then long written: it is renamed so that nothing opens it.
	if err := os.Rename(filepath.Join(repoDir, badgerDirName), filepath.Join(repoDir, badgerOldDirName)); err != nil {
		log.Warn("Cannot rename the old IPFS Badger store; it is deleted at a later start", "err", err)
	}
	log.Info("IPFS repository converted from Badger", "keys", state.Keys)
	return nil
}

// keepDroppingFileCache drops the page cache of dirs' files every interval until the returned function is called.
func keepDroppingFileCache(interval time.Duration, dirs ...string) (stop func()) {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				for _, dir := range dirs {
					dropFileCache(dir)
				}
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

// conversionPostponed: the copy takes about an hour on a hard disk with a few gigabytes of Badger, during which the
// node does not run (no sync, no mining, no validation). It waits while the next validation is less than
// validationGuard away or under way (its time stays past until the ceremony ends); a time a day past or more is that
// of a node still behind the chain, unknown (zero) converts.
func conversionPostponed(nextValidation, now time.Time) bool {
	if nextValidation.IsZero() {
		return false
	}
	left := nextValidation.Sub(now)
	return left < validationGuard && left > -24*time.Hour
}

// abandonConversion leaves the repository on Badger: the copy is removed and the attempt recorded against the Badger
// store's files as they are now (the store is closed: opening it may have rewritten some).
func abandonConversion(repoDir string, attempts int, cause error) {
	log.Error("Cannot convert the IPFS repository from Badger; it stays on Badger", "attempt", attempts, "err", cause)
	if err := restoreBadgerSpec(repoDir); err != nil {
		log.Error("Cannot restore the IPFS Badger spec", "err", err)
	}
	removeCopy(repoDir)
	fingerprint, _ := badgerFingerprint(filepath.Join(repoDir, badgerDirName))
	if err := writeConversionState(repoDir, badgerConversionState{Step: stepFailed, Attempts: attempts, Badger: fingerprint}); err != nil {
		log.Warn("Cannot write the IPFS conversion marker", "err", err)
	}
}

func removeCopy(repoDir string) {
	for _, dir := range []string{flatfsDirName, leveldbDirName} {
		if err := os.RemoveAll(filepath.Join(repoDir, dir)); err != nil {
			log.Warn("Cannot remove a partial IPFS copy", "dir", dir, "err", err)
		}
	}
}

// removeOldBadger deletes a Badger store the repository no longer uses; false when one is left (tried again later).
func removeOldBadger(repoDir string) bool {
	done := true
	for _, dir := range []string{badgerOldDirName, badgerDirName} {
		if err := os.RemoveAll(filepath.Join(repoDir, dir)); err != nil {
			log.Warn("Cannot delete the old IPFS Badger store; it is tried again at the next start", "err", err)
			done = false
		}
	}
	return done
}

// badgerFingerprint names the Badger store's state by its files: any write changes a size or a time.
func badgerFingerprint(dir string) (string, error) {
	var b strings.Builder
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s:%d:%d;", entry.Name(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	return b.String(), err
}

// openBadgerToRead opens the store without the memory its node use costs: tables read from disk instead of mapped,
// no value log GC and no compaction (a compaction builds its new tables in memory: gigabytes on a store of a few).
// Read-only first. A store the official node did not close cleanly needs its value log replayed, which only a
// writable open does, and Badger v1 has no read-only mode on Windows: those open writable, with every compaction
// trigger out of reach.
func openBadgerToRead(dir string) (*badgerds.Datastore, error) {
	store, err := badgerds.NewDatastore(dir, badgerReadOptions(true))
	if err == nil {
		return store, nil
	}
	log.Info("The IPFS Badger store cannot be opened read-only; it is opened writable, without compaction", "err", err)
	return badgerds.NewDatastore(dir, badgerReadOptions(false))
}

func badgerReadOptions(readOnly bool) *badgerds.Options {
	opts := badgerds.DefaultOptions
	opts.Options.TableLoadingMode = options.FileIO
	opts.Options.ValueLogLoadingMode = options.FileIO
	opts.GcInterval = 0
	opts.Options.ReadOnly = readOnly
	opts.Options.CompactL0OnClose = false
	// Badger compacts a level when its size or table count reaches these (scores below 1 never run). Zero
	// compactors would be simpler, but Badger v1 then panics on close.
	opts.Options.NumLevelZeroTables = 1 << 20
	opts.Options.NumLevelZeroTablesStall = 1<<20 + 1
	opts.Options.LevelOneSize = 1 << 40
	return &opts
}

func openCopy(repoDir string) (ds.Batching, error) {
	blocks, err := flatfs.CreateOrOpen(filepath.Join(repoDir, flatfsDirName), flatfs.NextToLast(2), false)
	if err != nil {
		return nil, err
	}
	rest, err := levelds.NewDatastore(filepath.Join(repoDir, leveldbDirName), &levelds.Options{Compression: ldbopts.NoCompression})
	if err != nil {
		blocks.Close()
		return nil, err
	}
	return mount.New([]mount.Mount{
		{Prefix: ds.NewKey("/blocks"), Datastore: blocks},
		{Prefix: ds.NewKey("/"), Datastore: rest},
	}), nil
}

// copyComplete: the copy holds keys keys (an earlier start wrote it and its marker).
func copyComplete(repoDir string, keys int64) bool {
	store, err := openCopy(repoDir)
	if err != nil {
		return false
	}
	defer store.Close()
	count, err := countKeys(context.Background(), store)
	return err == nil && count == keys
}

// copyBadgerStore copies every key of the Badger store into new flatfs and LevelDB stores, mounted as the flatfs
// profile mounts them, and puts them on disk. from is the copying marker: a fresh copy (no LastKey) starts from an
// empty folder, a resumed one goes on after from.LastKey with the copy an earlier start put on disk. ok is false when
// it did not finish (logged; the repository stays on Badger).
func copyBadgerStore(repoDir string, from badgerConversionState) (keys int64, ok bool) {
	resuming := len(from.LastKey) > 0
	log.Info(conversionStartLine, "attempt", from.Attempts, "resumedAtKeys", from.Keys)
	defer func() { log.Info(conversionEndLine, "converted", ok, "keys", keys) }()
	if !resuming {
		removeCopy(repoDir)
	}
	badgerDir := filepath.Join(repoDir, badgerDirName)
	leveldbDir := filepath.Join(repoDir, leveldbDirName)
	// Badger reads every table whole when it opens the store, and the estimate and the copy read them again: each
	// time gigabytes of page cache, charged to the node's cgroup. They are dropped as the reading goes.
	stopDropping := keepDroppingFileCache(5*time.Second, badgerDir, leveldbDir)
	defer stopDropping()
	source, err := openBadgerToRead(badgerDir)
	if err != nil {
		abandonConversion(repoDir, from.Attempts, errors.Wrap(err, "cannot open the Badger store"))
		return 0, false
	}
	closeSource := func() {
		if err := source.Close(); err != nil {
			log.Warn("Cannot close the IPFS Badger store", "err", err)
		}
	}

	ctx := context.Background()
	need, err := copySize(ctx, source)
	if err != nil {
		closeSource()
		abandonConversion(repoDir, from.Attempts, err)
		return 0, false
	}
	free, err := freeDiskSpace(repoDir)
	if err != nil || free < need-from.Copied+badgerConversionMargin {
		closeSource()
		log.Warn("Not enough disk space to convert the IPFS repository from Badger; it stays on Badger",
			"needMiB", (need-from.Copied+badgerConversionMargin)>>20, "freeMiB", free>>20, "err", err)
		if !resuming {
			removeConversionState(repoDir)
		}
		return 0, false
	}
	if !resuming {
		if err := writeConversionState(repoDir, from); err != nil {
			closeSource()
			log.Warn("Cannot write the IPFS conversion marker; the repository stays on Badger", "err", err)
			return 0, false
		}
	}

	keys, err = copyKeys(ctx, source, repoDir, need, from)
	closeSource()
	if err == errCopyStopped {
		return keys, false
	}
	if err == nil {
		// The copy must be on disk before the repository switches to it: the blocks are, LevelDB wrote on its close.
		if err = syncTree(leveldbDir); err != nil {
			err = errors.Wrap(err, "cannot write the IPFS copy to disk")
		}
	}
	if err != nil {
		abandonConversion(repoDir, from.Attempts, err)
		return keys, false
	}
	return keys, true
}

// keyDiskSize: the disk space a key takes in the copy, each block a file of whole clusters, the rest in LevelDB.
func keyDiskSize(key string, size int) int64 {
	if strings.HasPrefix(key, blocksPrefix) {
		return (int64(size)+diskCluster-1)/diskCluster*diskCluster + fileOverhead
	}
	return int64(size) + int64(len(key))
}

// copySize estimates the disk space the whole copy takes.
func copySize(ctx context.Context, source ds.Datastore) (int64, error) {
	results, err := source.Query(ctx, query.Query{KeysOnly: true, ReturnsSizes: true})
	if err != nil {
		return 0, err
	}
	defer results.Close()
	var need int64
	for result := range results.Next() {
		if result.Error != nil {
			return 0, errors.Wrap(result.Error, "cannot read the Badger store")
		}
		need += keyDiskSize(result.Key, result.Size)
	}
	return need, nil
}

// copyKeys copies the keys after from.LastKey, in Badger's order (the blocks first). At each checkpoint the copy is
// put on disk and the marker records where it stands, for a start after a quit to go on from there: every
// blockFlushBytes of blocks (their files leave the page cache with it), once past the blocks, at least every
// checkpointEvery, and at the end.
func copyKeys(ctx context.Context, source *badgerds.Datastore, repoDir string, need int64, from badgerConversionState) (keys int64, err error) {
	target, err := openCopy(repoDir)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := target.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	batch, err := target.Batch(ctx)
	if err != nil {
		return 0, err
	}
	badgerDir := filepath.Join(repoDir, badgerDirName)
	flatfsDir := filepath.Join(repoDir, flatfsDirName)
	leveldbDir := filepath.Join(repoDir, leveldbDirName)
	shard := flatfs.NextToLast(2).Func()
	keys, copied, lastKey := from.Keys, from.Copied, from.LastKey
	// written: the block files since the last checkpoint, where they are synced one by one (not Linux).
	var written []string
	pending, blockBytes, checkpoints := 0, 0, 0
	started, startCopied := time.Now(), copied
	lastCheckpoint, lastLog := started, started

	commit := func() error {
		if err := batch.Commit(ctx); err != nil {
			return err
		}
		pending = 0
		batch, err = target.Batch(ctx)
		return err
	}
	putOnDisk := func(since time.Time) error {
		if err := syncCopied(flatfsDir, written, leveldbDir, since); err != nil {
			return errors.Wrap(err, "cannot put the copied keys on disk")
		}
		written = written[:0]
		if blockBytes > 0 {
			dropFileCache(flatfsDir)
			blockBytes = 0
		}
		return nil
	}
	checkpoint := func() error {
		if err := commit(); err != nil {
			return err
		}
		since := lastCheckpoint
		lastCheckpoint = time.Now()
		if err := putOnDisk(since); err != nil {
			return err
		}
		fingerprint, err := badgerFingerprint(badgerDir)
		if err != nil {
			return err
		}
		state := badgerConversionState{Step: stepCopying, Attempts: from.Attempts, Keys: keys, Copied: copied,
			LastKey: lastKey, Badger: fingerprint}
		if err := writeConversionState(repoDir, state); err != nil {
			return err
		}
		copyCheckpoints++
		if checkpoints++; stopCopyAt > 0 && checkpoints >= stopCopyAt {
			return errCopyStopped
		}
		return nil
	}
	progress := func() []any {
		fields := []any{"keys", keys}
		if need > 0 {
			fields = append(fields, "percent", min(copied*100/need, 99))
		}
		if elapsed := time.Since(started); elapsed > 30*time.Second && copied > startCopied {
			rate := float64(copied-startCopied) / elapsed.Minutes()
			fields = append(fields, "minutesLeft", int64(math.Ceil(float64(max(need-copied, 0))/rate)))
		}
		return fields
	}

	txn := source.DB.NewTransaction(false)
	defer txn.Discard()
	it := txn.NewIterator(badger.IteratorOptions{})
	defer it.Close()
	it.Seek(lastKey)
	if len(lastKey) > 0 && it.Valid() && bytes.Equal(it.Item().Key(), lastKey) {
		it.Next()
	}
	blocksDone := len(lastKey) > 0 && !bytes.HasPrefix(lastKey, []byte(blocksPrefix))
	log.Info("Converting the IPFS repository from Badger", append([]any{"estimatedMiB", need >> 20}, progress()...)...)
	for ; it.Valid(); it.Next() {
		item := it.Item()
		key := item.KeyCopy(nil)
		isBlock := bytes.HasPrefix(key, []byte(blocksPrefix))
		if !blocksDone && !isBlock && keys > 0 {
			blocksDone = true
			if err := checkpoint(); err != nil {
				return keys, err
			}
		}
		value, err := item.ValueCopy(nil)
		if err != nil {
			return keys, errors.Wrap(err, "cannot read the Badger store")
		}
		if err := batch.Put(ctx, ds.RawKey(string(key)), value); err != nil {
			return keys, errors.Wrapf(err, "cannot copy %s", key)
		}
		keys++
		copied += keyDiskSize(string(key), len(value))
		lastKey = key
		if isBlock {
			blockBytes += len(value)
			if !syncsFilesystem {
				name := strings.TrimPrefix(string(key), blocksPrefix)
				written = append(written, filepath.Join(flatfsDir, shard(name), name+flatfsExtension))
			}
		}
		pending++
		switch {
		case isBlock && blockBytes >= blockFlushBytes, time.Since(lastCheckpoint) >= checkpointEvery:
			err = checkpoint()
		case pending >= badgerConversionBatch:
			err = commit()
		}
		if err != nil {
			return keys, err
		}
		if pending > 0 {
			continue
		}
		// Stop before the disk fills: the node needs room for its chain.
		if free, err := freeDiskSpace(repoDir); err != nil || free < badgerConversionMargin {
			return keys, errors.Errorf("the disk is nearly full (free %d MiB)", free>>20)
		}
		if time.Since(lastLog) > 10*time.Second {
			log.Info("Converting the IPFS repository from Badger", progress()...)
			lastLog = time.Now()
		}
	}
	if err := commit(); err != nil {
		return keys, err
	}
	if err := putOnDisk(lastCheckpoint); err != nil {
		return keys, err
	}
	// Every key read must be readable back.
	count, err := countKeys(ctx, target)
	dropFileCache(leveldbDir)
	if err != nil {
		return keys, err
	}
	if count != keys {
		return keys, fmt.Errorf("IPFS conversion copied %d keys but finds %d", keys, count)
	}
	return keys, nil
}

func countKeys(ctx context.Context, store ds.Datastore) (int64, error) {
	results, err := store.Query(ctx, query.Query{KeysOnly: true})
	if err != nil {
		return 0, err
	}
	defer results.Close()
	var n int64
	for result := range results.Next() {
		if result.Error != nil {
			return 0, result.Error
		}
		n++
	}
	return n, nil
}

// The spec a new repository gets with the flatfs profile, as kubo writes it in datastore_spec.
func flatfsDiskSpec() ([]byte, error) {
	return json.Marshal(map[string]any{
		"mounts": []map[string]any{
			{"mountpoint": "/blocks", "path": flatfsDirName, "shardFunc": "/repo/flatfs/shard/v1/next-to-last/2", "type": "flatfs"},
			{"mountpoint": "/", "path": leveldbDirName, "type": "levelds"},
		},
		"type": "mount",
	})
}

// switchToFlatfsSpec points the config and the datastore spec at the copy, as a new repository gets them. The Badger
// config is kept next to it (config.badger.bak) for restoreBadgerSpec.
func switchToFlatfsSpec(repoDir string) error {
	configFile := filepath.Join(repoDir, "config")
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	conf, err := decodeConfig(raw)
	if err != nil {
		return err
	}
	if spec, _ := conf["Datastore"].(map[string]any); spec != nil && specHasType(asMap(spec["Spec"]), "badgerds") {
		diskSpec, err := os.ReadFile(filepath.Join(repoDir, datastoreSpecFile))
		if err != nil {
			return err
		}
		if err := writeFileAtomic(filepath.Join(repoDir, datastoreSpecFile)+badgerBackup, diskSpec); err != nil {
			return err
		}
		if err := writeFileAtomic(configFile+badgerBackup, raw); err != nil {
			return err
		}
	}
	profile := &ipfsConf.Config{}
	if err := ipfsConf.Profiles[defaultIpfsDatastoreProfile].Transform(profile); err != nil {
		return err
	}
	datastore, _ := conf["Datastore"].(map[string]any)
	if datastore == nil {
		datastore = map[string]any{}
	}
	datastore["Spec"] = profile.Datastore.Spec
	conf["Datastore"] = datastore
	converted, err := json.MarshalIndent(conf, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(configFile, append(converted, '\n')); err != nil {
		return err
	}
	spec, err := flatfsDiskSpec()
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(repoDir, datastoreSpecFile), spec)
}

// restoreBadgerSpec undoes a switch that did not complete: the Badger config and spec come back.
func restoreBadgerSpec(repoDir string) error {
	for _, file := range []string{"config", datastoreSpecFile} {
		path := filepath.Join(repoDir, file)
		backup, err := os.ReadFile(path + badgerBackup)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err := writeFileAtomic(path, backup); err != nil {
			return err
		}
	}
	return nil
}

func removeBadgerBackups(repoDir string) {
	for _, file := range []string{"config", datastoreSpecFile} {
		if err := os.Remove(filepath.Join(repoDir, file) + badgerBackup); err != nil && !os.IsNotExist(err) {
			log.Warn("Cannot remove an IPFS Badger backup", "file", file, "err", err)
		}
	}
}

// specHasType: the datastore spec, or one of its children or mounts, has the type.
func specHasType(spec map[string]any, datastoreType string) bool {
	if spec["type"] == datastoreType {
		return true
	}
	for _, child := range spec {
		switch value := child.(type) {
		case map[string]any:
			if specHasType(value, datastoreType) {
				return true
			}
		case []any:
			for _, item := range value {
				if m, ok := item.(map[string]any); ok && specHasType(m, datastoreType) {
					return true
				}
			}
		}
	}
	return false
}

func decodeConfig(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var conf map[string]any
	if err := decoder.Decode(&conf); err != nil {
		return nil, errors.Wrap(err, "cannot decode the IPFS config")
	}
	return conf, nil
}

func asMap(value any) map[string]any {
	m, _ := value.(map[string]any)
	return m
}

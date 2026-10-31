//go:build !idena_memory_ipfs

package ipfs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

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
//   - copying: a copy was being written; it is dropped and made again.
//   - copied: the copy is complete and on disk; it is checked (key count, Badger unchanged) and switched to.
//   - switched: the repository runs on the copy; the Badger store, kept for one start, is deleted.
//   - failed: the last attempt failed.

const (
	badgerConversionMarker = "badger-conversion.json"
	// badgerBackup: the suffix of the Badger config and spec kept until the old store is deleted.
	badgerBackup      = ".badger.bak"
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
)

type badgerConversionState struct {
	Step string `json:"step"`
	Keys int64  `json:"keys,omitempty"`
	// Attempts: copies started on this Badger store; none after maxConversionAttempts.
	Attempts int `json:"attempts,omitempty"`
	// Badger: the Badger store's files when the copy was made (badgerFingerprint): a copy is switched to only if
	// Badger is unchanged since.
	Badger string `json:"badger,omitempty"`
}

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
func convertBadgerRepo(repoDir string) error {
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
	if !copied {
		keys, ok := copyBadgerStore(repoDir, attempts+1)
		if !ok {
			return nil
		}
		// The copy closed the Badger store: its files as the next start will find them.
		if fingerprint, err = badgerFingerprint(filepath.Join(repoDir, badgerDirName)); err != nil {
			abandonConversion(repoDir, attempts+1, err)
			return nil
		}
		state = &badgerConversionState{Step: stepCopied, Keys: keys, Attempts: attempts + 1, Badger: fingerprint}
		if err := writeConversionState(repoDir, *state); err != nil {
			abandonConversion(repoDir, attempts+1, err)
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

func openBadgerToRead(dir string) (*badgerds.Datastore, error) {
	opts := badgerds.DefaultOptions
	// Read the tables from disk instead of mapping them, and no value log GC: the store is only read, then deleted.
	opts.Options.TableLoadingMode = options.FileIO
	opts.Options.ValueLogLoadingMode = options.FileIO
	opts.GcInterval = 0
	return badgerds.NewDatastore(dir, &opts)
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
// profile mounts them, and puts them on disk. ok is false when it did not (logged; the repository stays on Badger).
func copyBadgerStore(repoDir string, attempt int) (keys int64, ok bool) {
	log.Info(conversionStartLine, "attempt", attempt)
	defer func() { log.Info(conversionEndLine, "converted", ok, "keys", keys) }()
	removeCopy(repoDir)
	badgerDir := filepath.Join(repoDir, badgerDirName)
	source, err := openBadgerToRead(badgerDir)
	if err != nil {
		abandonConversion(repoDir, attempt, errors.Wrap(err, "cannot open the Badger store"))
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
		abandonConversion(repoDir, attempt, err)
		return 0, false
	}
	free, err := freeDiskSpace(repoDir)
	if err != nil || free < need+badgerConversionMargin {
		closeSource()
		log.Warn("Not enough disk space to convert the IPFS repository from Badger; it stays on Badger",
			"needMiB", (need+badgerConversionMargin)>>20, "freeMiB", free>>20, "err", err)
		removeConversionState(repoDir)
		return 0, false
	}
	if err := writeConversionState(repoDir, badgerConversionState{Step: stepCopying, Attempts: attempt}); err != nil {
		closeSource()
		log.Warn("Cannot write the IPFS conversion marker; the repository stays on Badger", "err", err)
		return 0, false
	}

	keys, err = copyKeys(ctx, source, repoDir, need)
	closeSource()
	if err == nil {
		// The copy must be on disk before the repository switches to it.
		err = syncCopy(repoDir)
	}
	if err != nil {
		abandonConversion(repoDir, attempt, err)
		return keys, false
	}
	return keys, true
}

// copySize estimates the disk space the copy takes: each block a file of whole clusters, the rest in LevelDB.
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
		size := int64(result.Size) + int64(len(result.Key))
		if strings.HasPrefix(result.Key, "/blocks/") {
			size = (int64(result.Size)+diskCluster-1)/diskCluster*diskCluster + fileOverhead
		}
		need += size
	}
	return need, nil
}

func copyKeys(ctx context.Context, source ds.Datastore, repoDir string, need int64) (keys int64, err error) {
	target, err := openCopy(repoDir)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := target.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	results, err := source.Query(ctx, query.Query{})
	if err != nil {
		return 0, err
	}
	defer results.Close()
	batch, err := target.Batch(ctx)
	if err != nil {
		return 0, err
	}
	pending := 0
	lastLog := time.Now()
	log.Info("Converting the IPFS repository from Badger", "estimatedMiB", need>>20)
	for result := range results.Next() {
		if result.Error != nil {
			return keys, errors.Wrap(result.Error, "cannot read the Badger store")
		}
		if err := batch.Put(ctx, ds.RawKey(result.Key), result.Value); err != nil {
			return keys, errors.Wrapf(err, "cannot copy %s", result.Key)
		}
		keys++
		if pending++; pending < badgerConversionBatch {
			continue
		}
		if err := batch.Commit(ctx); err != nil {
			return keys, err
		}
		if batch, err = target.Batch(ctx); err != nil {
			return keys, err
		}
		pending = 0
		// Stop before the disk fills: the node needs room for its chain.
		if free, err := freeDiskSpace(repoDir); err != nil || free < badgerConversionMargin {
			return keys, errors.Errorf("the disk is nearly full (free %d MiB)", free>>20)
		}
		if time.Since(lastLog) > 10*time.Second {
			log.Info("Converting the IPFS repository from Badger", "keys", keys)
			lastLog = time.Now()
		}
	}
	if err := batch.Commit(ctx); err != nil {
		return keys, err
	}
	// Every key read must be readable back.
	count, err := countKeys(ctx, target)
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

// syncCopy puts the copy's files on disk (flatfs writes them without syncing).
func syncCopy(repoDir string) error {
	for _, dir := range []string{flatfsDirName, leveldbDirName} {
		if err := syncTree(filepath.Join(repoDir, dir)); err != nil {
			return errors.Wrap(err, "cannot write the IPFS copy to disk")
		}
	}
	return nil
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

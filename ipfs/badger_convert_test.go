//go:build !idena_memory_ipfs

package ipfs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dgraph-io/badger"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/ipfs/boxo/datastore/dshelp"
	ds "github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/query"
	badgerds "github.com/ipfs/go-ds-badger"
	ipfsConf "github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/repo/fsrepo"
	"github.com/multiformats/go-multihash"
	"github.com/stretchr/testify/require"
)

// badgerRepo makes an official-style repository (one Badger store) holding blocks, provider records and other keys,
// written through kubo's own repository; it returns every key with its value.
func badgerRepo(t *testing.T, blocks, others int) (string, map[string][]byte) {
	dataDir := t.TempDir()
	config, err := ipfsConf.Init(io.Discard, 2048)
	require.NoError(t, err)
	require.NoError(t, ipfsConf.Profiles["badgerds"].Transform(config))
	require.NoError(t, fsrepo.Init(dataDir, config))
	ok, err := isBadgerRepo(dataDir)
	require.NoError(t, err)
	require.True(t, ok)

	want := make(map[string][]byte)
	putAll(t, dataDir, want, func(put func(string, []byte)) {
		for i := 0; i < blocks; i++ {
			data := []byte(fmt.Sprintf("block %d %s", i, bytes.Repeat([]byte{byte(i)}, i%300)))
			hash, err := multihash.Sum(data, multihash.SHA2_256, -1)
			require.NoError(t, err)
			put("/blocks"+dshelp.MultihashToDsKey(hash).String(), data)
		}
		for i := 0; i < others; i++ {
			put(fmt.Sprintf("/providers/CIQA%d/CIQB", i), []byte(fmt.Sprintf("provider record %d", i)))
		}
		put("/local/filesroot", []byte("root"))
		put("/pins/state", []byte{0x1, 0x2})
	})
	return dataDir, want
}

// putAll writes keys through kubo's repository as it is configured now.
func putAll(t *testing.T, dataDir string, want map[string][]byte, fill func(put func(string, []byte))) {
	repo, err := fsrepo.Open(dataDir)
	require.NoError(t, err)
	ctx := context.Background()
	fill(func(key string, value []byte) {
		require.NoError(t, repo.Datastore().Put(ctx, ds.NewKey(key), value))
		want[ds.NewKey(key).String()] = value
	})
	require.NoError(t, repo.Close())
}

// readAll opens the repository with kubo and reads every key back.
func readAll(t *testing.T, dataDir string) map[string][]byte {
	repo, err := fsrepo.Open(dataDir)
	require.NoError(t, err)
	defer repo.Close()
	results, err := repo.Datastore().Query(context.Background(), query.Query{})
	require.NoError(t, err)
	defer results.Close()
	got := make(map[string][]byte)
	for result := range results.Next() {
		require.NoError(t, result.Error)
		got[result.Key] = result.Value
	}
	return got
}

func requireSameKeys(t *testing.T, dataDir string, want map[string][]byte) {
	got := readAll(t, dataDir)
	require.Len(t, got, len(want))
	for key, value := range want {
		require.Equal(t, value, got[key], key)
	}
}

func exists(t *testing.T, path string) bool {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	require.NoError(t, err)
	return true
}

func requireBadger(t *testing.T, dataDir string, want map[string][]byte) {
	ok, err := isBadgerRepo(dataDir)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, exists(t, filepath.Join(dataDir, flatfsDirName)))
	require.False(t, exists(t, filepath.Join(dataDir, leveldbDirName)))
	requireSameKeys(t, dataDir, want)
}

func readState(t *testing.T, dataDir string) *badgerConversionState {
	return readConversionState(dataDir)
}

// requireConverted: the repository runs on the copy; the Badger store is kept until the next start.
func requireConverted(t *testing.T, dataDir string, want map[string][]byte) {
	ok, err := isBadgerRepo(dataDir)
	require.NoError(t, err)
	require.False(t, ok)
	require.False(t, exists(t, filepath.Join(dataDir, badgerDirName)))
	requireSameKeys(t, dataDir, want)
	files, err := filepath.Glob(filepath.Join(dataDir, flatfsDirName, "*", "*.data"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
}

// requireCleaned: a later start deleted the old store, the backups and the marker.
func requireCleaned(t *testing.T, dataDir string) {
	for _, gone := range []string{badgerOldDirName, badgerDirName, badgerConversionMarker, "config" + badgerBackup,
		datastoreSpecFile + badgerBackup} {
		require.False(t, exists(t, filepath.Join(dataDir, gone)), gone)
	}
}

func withFreeSpace(t *testing.T, free func(string) (int64, error)) {
	old := freeDiskSpace
	freeDiskSpace = free
	t.Cleanup(func() { freeDiskSpace = old })
}

func TestConvertBadgerRepoCopiesEveryKey(t *testing.T) {
	dataDir, want := badgerRepo(t, 2500, 1500)
	require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
	requireConverted(t, dataDir, want)
	require.True(t, exists(t, filepath.Join(dataDir, badgerOldDirName)))
	require.Equal(t, stepSwitched, readState(t, dataDir).Step)
	// The spec is the one a new repository gets.
	spec, err := os.ReadFile(filepath.Join(dataDir, datastoreSpecFile))
	require.NoError(t, err)
	require.JSONEq(t, `{"mounts":[{"mountpoint":"/blocks","path":"blocks","shardFunc":"/repo/flatfs/shard/v1/next-to-last/2","type":"flatfs"},{"mountpoint":"/","path":"datastore","type":"levelds"}],"type":"mount"}`, string(spec))

	// The next start deletes the old store; the one after has nothing to do.
	require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
	requireConverted(t, dataDir, want)
	requireCleaned(t, dataDir)
	require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
	requireConverted(t, dataDir, want)
}

func TestConvertBadgerRepoKeepsBadgerWithoutDiskSpace(t *testing.T) {
	dataDir, want := badgerRepo(t, 10, 1)
	withFreeSpace(t, func(string) (int64, error) { return badgerConversionMargin, nil })

	require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
	requireBadger(t, dataDir, want)
	require.Nil(t, readState(t, dataDir))
}

// A start stopped while copying left a partial copy: it is removed before the space is measured, so that it does not
// count as used (the copy then goes on, or the repository stays on Badger without it).
func TestConvertBadgerRepoDropsAnInterruptedCopy(t *testing.T) {
	for _, enoughSpace := range []bool{true, false} {
		t.Run(fmt.Sprintf("enoughSpace=%v", enoughSpace), func(t *testing.T) {
			dataDir, want := badgerRepo(t, 50, 1)
			require.NoError(t, writeConversionState(dataDir, badgerConversionState{Step: stepCopying, Attempts: 1}))
			partial := filepath.Join(dataDir, flatfsDirName, "XX")
			require.NoError(t, os.MkdirAll(partial, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(partial, "LEFTOVER.data"), []byte("x"), 0600))
			if !enoughSpace {
				withFreeSpace(t, func(string) (int64, error) { return badgerConversionMargin, nil })
			}

			require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
			require.False(t, exists(t, partial))
			if enoughSpace {
				requireConverted(t, dataDir, want)
			} else {
				requireBadger(t, dataDir, want)
			}
		})
	}
}

// A copy that fails leaves the repository on Badger, and the node starts; after maxConversionAttempts failures on an
// unchanged store, no copy is tried any more.
func TestConvertBadgerRepoFailureKeepsBadger(t *testing.T) {
	dataDir, want := badgerRepo(t, 2500, 1)
	// Enough space for the estimate, then the disk "fills" during the copy.
	calls := 0
	withFreeSpace(t, func(string) (int64, error) {
		calls++
		if calls == 1 {
			return 1 << 40, nil
		}
		return 0, nil
	})
	for attempt := 1; attempt <= maxConversionAttempts; attempt++ {
		calls = 0
		require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
		requireBadger(t, dataDir, want)
		state := readState(t, dataDir)
		require.Equal(t, stepFailed, state.Step)
		require.Equal(t, attempt, state.Attempts)
	}

	// No more attempts, even with the space back.
	withFreeSpace(t, func(string) (int64, error) { return 1 << 40, nil })
	require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
	requireBadger(t, dataDir, want)
	require.Equal(t, maxConversionAttempts, readState(t, dataDir).Attempts)

	// The node still starts, on Badger.
	configured, err := configureIpfs(testIpfsConfig(dataDir), eventbus.New())
	require.NoError(t, err)
	require.True(t, specHasType(configured.Datastore.Spec, "badgerds"))
}

// A start stopped after the copy, before or after the switch: the next one checks the copy and finishes.
func TestConvertBadgerRepoFinishesAfterTheCopy(t *testing.T) {
	for _, switched := range []bool{false, true} {
		t.Run(fmt.Sprintf("switched=%v", switched), func(t *testing.T) {
			dataDir, want := badgerRepo(t, 50, 1)
			keys, ok := copyBadgerStore(dataDir, badgerConversionState{Step: stepCopying, Attempts: 1})
			require.True(t, ok)
			require.EqualValues(t, len(want), keys)
			fingerprint, err := badgerFingerprint(filepath.Join(dataDir, badgerDirName))
			require.NoError(t, err)
			require.NoError(t, writeConversionState(dataDir, badgerConversionState{Step: stepCopied, Keys: keys, Attempts: 1, Badger: fingerprint}))
			if switched {
				require.NoError(t, switchToFlatfsSpec(dataDir))
			}

			require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
			requireConverted(t, dataDir, want)
			if !switched {
				require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
			}
			requireCleaned(t, dataDir)
		})
	}
}

// A copy made, then Badger written again (an older build run after a crash): the copy is outdated and made again.
func TestConvertBadgerRepoCopiesAgainAfterBadgerChanged(t *testing.T) {
	dataDir, want := badgerRepo(t, 50, 1)
	keys, ok := copyBadgerStore(dataDir, badgerConversionState{Step: stepCopying, Attempts: 1})
	require.True(t, ok)
	fingerprint, err := badgerFingerprint(filepath.Join(dataDir, badgerDirName))
	require.NoError(t, err)
	require.NoError(t, writeConversionState(dataDir, badgerConversionState{Step: stepCopied, Keys: keys, Attempts: 1, Badger: fingerprint}))

	putAll(t, dataDir, want, func(put func(string, []byte)) {
		put("/local/written-later", []byte("after the copy"))
	})
	require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
	requireConverted(t, dataDir, want)
}

// A copy damaged after its marker (a block lost) is not switched to: it is made again.
func TestConvertBadgerRepoCopiesAgainADamagedCopy(t *testing.T) {
	dataDir, want := badgerRepo(t, 50, 1)
	keys, ok := copyBadgerStore(dataDir, badgerConversionState{Step: stepCopying, Attempts: 1})
	require.True(t, ok)
	fingerprint, err := badgerFingerprint(filepath.Join(dataDir, badgerDirName))
	require.NoError(t, err)
	require.NoError(t, writeConversionState(dataDir, badgerConversionState{Step: stepCopied, Keys: keys, Attempts: 1, Badger: fingerprint}))
	files, err := filepath.Glob(filepath.Join(dataDir, flatfsDirName, "*", "*.data"))
	require.NoError(t, err)
	require.NoError(t, os.Remove(files[0]))

	require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
	requireConverted(t, dataDir, want)
}

// The old store cannot be deleted (a file held open, as on Windows): the node starts, and a later start deletes it.
func TestConvertBadgerRepoRetriesTheCleanup(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root deletes read-only directories")
	}
	dataDir, want := badgerRepo(t, 50, 1)
	require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
	old := filepath.Join(dataDir, badgerOldDirName)
	// Unix: a directory without write permission; Windows ignores it, but cannot delete a file held open.
	release := func() { require.NoError(t, os.Chmod(old, 0700)) }
	if runtime.GOOS == "windows" {
		files, err := filepath.Glob(filepath.Join(old, "*.vlog"))
		require.NoError(t, err)
		require.NotEmpty(t, files)
		held, err := os.Open(files[0])
		require.NoError(t, err)
		release = func() { require.NoError(t, held.Close()) }
		t.Cleanup(func() { _ = held.Close() })
	} else {
		require.NoError(t, os.Chmod(old, 0500))
		t.Cleanup(func() { _ = os.Chmod(old, 0700) })
	}

	require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
	requireConverted(t, dataDir, want)
	require.True(t, exists(t, old))
	require.Equal(t, stepSwitched, readState(t, dataDir).Step)

	release()
	require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
	requireCleaned(t, dataDir)
}

func TestConvertBadgerRepoLeavesOtherRepos(t *testing.T) {
	dataDir := t.TempDir()
	config, err := ipfsConf.Init(io.Discard, 2048)
	require.NoError(t, err)
	require.NoError(t, ipfsConf.Profiles[defaultIpfsDatastoreProfile].Transform(config))
	require.NoError(t, fsrepo.Init(dataDir, config))
	before, err := os.ReadFile(filepath.Join(dataDir, "config"))
	require.NoError(t, err)

	require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
	after, err := os.ReadFile(filepath.Join(dataDir, "config"))
	require.NoError(t, err)
	require.Equal(t, before, after)

	// A stray marker, unreadable or at a step that does not apply, is dropped; the node starts.
	for _, marker := range []string{`{"step":"other"}`, `{not json`} {
		require.NoError(t, os.WriteFile(filepath.Join(dataDir, badgerConversionMarker), []byte(marker), 0600))
		require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
		require.False(t, exists(t, filepath.Join(dataDir, badgerConversionMarker)))
	}
}

// The node's start converts the official node's repository and keeps its identity.
func TestConfigureIpfsConvertsBadgerRepo(t *testing.T) {
	dataDir, want := badgerRepo(t, 20, 1)
	raw, err := os.ReadFile(filepath.Join(dataDir, "config"))
	require.NoError(t, err)
	var before map[string]any
	require.NoError(t, json.Unmarshal(raw, &before))

	configured, err := configureIpfs(testIpfsConfig(dataDir), eventbus.New())
	require.NoError(t, err)
	require.True(t, specHasType(configured.Datastore.Spec, "flatfs"))
	require.False(t, specHasType(configured.Datastore.Spec, "badgerds"))
	require.Equal(t, before["Identity"].(map[string]any)["PeerID"], configured.Identity.PeerID)
	requireConverted(t, dataDir, want)
}

// copyTree copies a folder's files as they are on disk now (a store still open: a node stopped without closing it).
func copyTree(t *testing.T, from, to string) {
	require.NoError(t, os.MkdirAll(to, 0700))
	entries, err := os.ReadDir(from)
	require.NoError(t, err)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(from, entry.Name()))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(to, entry.Name()), data, 0600))
	}
}

// The store is read without changing it: read-only, so no compaction rewrites its tables (on the node's PC a
// compaction builds its new tables in memory, gigabytes on a store of a few).
func TestConvertBadgerRepoReadsBadgerWithoutChangingIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Badger v1 has no read-only mode on Windows: the conversion opens it writable there")
	}
	dataDir, want := badgerRepo(t, 2500, 1500)
	before, err := badgerFingerprint(filepath.Join(dataDir, badgerDirName))
	require.NoError(t, err)
	require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
	requireConverted(t, dataDir, want)
	after, err := badgerFingerprint(filepath.Join(dataDir, badgerOldDirName))
	require.NoError(t, err)
	require.Equal(t, before, after)
}

// A store its node did not close (killed, power cut) holds writes Badger must replay, which a read-only open refuses:
// it is opened writable, and every key, the replayed ones too, is copied.
func TestConvertBadgerRepoConvertsAStoreNotClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows locks an open store's files: they cannot be copied (every conversion opens writable there)")
	}
	dataDir, want := badgerRepo(t, 50, 20)
	repo, err := fsrepo.Open(dataDir)
	require.NoError(t, err)
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		key := ds.NewKey(fmt.Sprintf("/providers/CIQC%d/CIQD", i))
		value := []byte(fmt.Sprintf("written before the stop %d", i))
		require.NoError(t, repo.Datastore().Put(ctx, key, value))
		want[key.String()] = value
	}
	unclosed := filepath.Join(t.TempDir(), "badgerds")
	copyTree(t, filepath.Join(dataDir, badgerDirName), unclosed)
	require.NoError(t, repo.Close())
	require.NoError(t, os.RemoveAll(filepath.Join(dataDir, badgerDirName)))
	require.NoError(t, os.Rename(unclosed, filepath.Join(dataDir, badgerDirName)))

	_, err = badgerds.NewDatastore(filepath.Join(dataDir, badgerDirName), badgerReadOptions(true))
	require.ErrorContains(t, err, badger.ErrReplayNeeded.Error())

	require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
	requireConverted(t, dataDir, want)
}

func TestConversionPostponedNearAValidation(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		next      time.Time
		postponed bool
	}{
		{time.Time{}, false},                            // no chain yet
		{now.Add(validationGuard + time.Minute), false}, // far enough
		{now.Add(validationGuard - time.Minute), true},
		{now.Add(time.Minute), true},
		{now.Add(-2 * time.Hour), true},   // the ceremony is under way
		{now.Add(-25 * time.Hour), false}, // a node still behind the chain
	} {
		require.Equal(t, c.postponed, conversionPostponed(c.next, now), c.next)
	}
}

// A validation near: the store stays on Badger for this start, untouched, and converts at a later one.
func TestConvertBadgerRepoWaitsForTheValidation(t *testing.T) {
	dataDir, want := badgerRepo(t, 50, 10)
	require.NoError(t, convertBadgerRepo(dataDir, time.Now().Add(time.Hour)))
	requireBadger(t, dataDir, want)
	require.Nil(t, readState(t, dataDir))

	require.NoError(t, convertBadgerRepo(dataDir, time.Now().Add(7*24*time.Hour)))
	requireConverted(t, dataDir, want)
}

func TestConvertBadgerRepoFlushesTheBlocksAsItCopiesThem(t *testing.T) {
	dataDir, want := badgerRepo(t, 2500, 1500)
	oldBytes, oldCheckpoints := blockFlushBytes, copyCheckpoints
	t.Cleanup(func() { blockFlushBytes, copyCheckpoints = oldBytes, oldCheckpoints })
	// The 2500 blocks hold about 400 KB: a flush every 64 KiB, and one after the blocks.
	blockFlushBytes, copyCheckpoints = 64<<10, 0
	require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
	requireConverted(t, dataDir, want)
	require.GreaterOrEqual(t, copyCheckpoints, 5)
}

// The per-file sync (macOS, Windows) runs on a flatfs tree, whose _README is read-only.
func TestSyncEachFileSkipsReadOnlyFiles(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root opens read-only files for writing")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "_README"), []byte("read me"), 0444))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "AB"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "AB", "block.data"), []byte("block"), 0600))
	require.NoError(t, syncEachFile(dir))
}

// stopCopy makes the copies of this test stop as a quit would, after stopAt checkpoints of one start.
func stopCopy(t *testing.T, flushBytes int, every time.Duration, stopAt int) {
	oldBytes, oldEvery, oldStop := blockFlushBytes, checkpointEvery, stopCopyAt
	t.Cleanup(func() { blockFlushBytes, checkpointEvery, stopCopyAt = oldBytes, oldEvery, oldStop })
	blockFlushBytes, checkpointEvery, stopCopyAt = flushBytes, every, stopAt
}

// markCopiedBlock dates one block file of the copy far back: a copy made again rewrites it, a resumed one does not.
func markCopiedBlock(t *testing.T, dataDir string) string {
	files, err := filepath.Glob(filepath.Join(dataDir, flatfsDirName, "*", "*.data"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(files[0], old, old))
	return files[0]
}

func modifiedIn2000(t *testing.T, path string) bool {
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.ModTime().Year() == 2000
}

// A start stopped during the copy: the next one goes on from the last checkpoint, wherever it was.
func TestConvertBadgerRepoResumesAStoppedCopy(t *testing.T) {
	for _, c := range []struct {
		name       string
		flushBytes int
		every      time.Duration
		stopAt     int
	}{
		{"in the blocks", 8 << 10, time.Hour, 2},
		{"after the blocks", 1 << 30, time.Hour, 1},
		{"in the other keys", 1 << 30, time.Nanosecond, 250},
	} {
		t.Run(c.name, func(t *testing.T) {
			dataDir, want := badgerRepo(t, 200, 300)
			stopCopy(t, c.flushBytes, c.every, c.stopAt)
			require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
			onBadger, err := isBadgerRepo(dataDir)
			require.NoError(t, err)
			require.True(t, onBadger)
			state := readState(t, dataDir)
			require.Equal(t, stepCopying, state.Step)
			require.Equal(t, 1, state.Attempts)
			require.NotEmpty(t, state.LastKey)
			require.Greater(t, state.Keys, int64(0))
			require.Less(t, state.Keys, int64(len(want)))
			marked := markCopiedBlock(t, dataDir)

			stopCopyAt = 0
			require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
			requireConverted(t, dataDir, want)
			require.True(t, modifiedIn2000(t, marked), "a block copied before the stop was copied again")
		})
	}
}

// A copy stopped, then Badger written (the official app run in between): the copy is made again from the start.
func TestConvertBadgerRepoStartsOverAStoppedCopyOfAChangedStore(t *testing.T) {
	dataDir, want := badgerRepo(t, 200, 300)
	stopCopy(t, 8<<10, time.Hour, 2)
	require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
	require.NotEmpty(t, readState(t, dataDir).LastKey)
	marked := markCopiedBlock(t, dataDir)
	putAll(t, dataDir, want, func(put func(string, []byte)) {
		put("/local/written-later", []byte("after the stop"))
	})

	stopCopyAt = 0
	require.NoError(t, convertBadgerRepo(dataDir, time.Time{}))
	requireConverted(t, dataDir, want)
	require.False(t, exists(t, marked) && modifiedIn2000(t, marked), "the stopped copy was kept")
}

// The progress counts keys as well as bytes: 15 M small provider records take most of the time on a hard disk.
func TestCopyProgress(t *testing.T) {
	require.Equal(t, int64(10+keyWorkBytes), keyWork(10))
	// Before 30 s, a percent only; 100 waits for the end.
	require.Equal(t, []any{"percent", int64(25)}, copyProgress(250, 1000, 0, 10*time.Second))
	require.Equal(t, []any{"percent", int64(99)}, copyProgress(1000, 1000, 0, 10*time.Second))
	// 300 done in 3 min at this start: 100 a minute, 600 left.
	require.Equal(t, []any{"percent", int64(40), "minutesLeft", int64(6)}, copyProgress(400, 1000, 100, 3*time.Minute))
	// A resumed start with nothing done yet has no pace.
	require.Equal(t, []any{"percent", int64(40)}, copyProgress(400, 1000, 400, 3*time.Minute))
	require.Empty(t, copyProgress(0, 0, 0, time.Minute))
}
